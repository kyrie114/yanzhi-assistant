package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/qa"
	"yanzhi/server/internal/textutil"
)

type seeded struct {
	DocID   string
	ChunkID string
}

// seedReadyDocument 直接写库造一份已完成资料，带一个可被关键词命中的切块。
func seedReadyDocument(t *testing.T, pool *pgxpool.Pool, owner, kbID, title string) seeded {
	t.Helper()
	ctx := context.Background()
	docID := db.NewID()
	if _, err := pool.Exec(ctx, `
		INSERT INTO documents (id, owner_id, kb_id, source_type, title, authors, status, embedding_generation, created_at, updated_at)
		VALUES ($1,$2,$3,'pdf',$4,'[]','ready',0,now(),now())`,
		db.MustUUID(docID), db.MustUUID(owner), db.MustUUID(kbID), title); err != nil {
		t.Fatalf("insert document: %v", err)
	}
	text := "Zephyr-Bench 是一个评测集。"
	parentID := db.NewID()
	if _, err := pool.Exec(ctx, `
		INSERT INTO parent_chunks (id, document_id, position, page_start, text)
		VALUES ($1,$2,0,2,$3)`, db.MustUUID(parentID), db.MustUUID(docID), text); err != nil {
		t.Fatalf("insert parent chunk: %v", err)
	}
	chunkID := db.NewID()
	terms := textutil.Terms(text)
	if _, err := pool.Exec(ctx, `
		INSERT INTO chunks (id, document_id, parent_id, kind, page_start, paragraph_index, overlap_prefix,
			text, version_no, char_count, term_count, embedding_generation, created_at, updated_at)
		VALUES ($1,$2,$3,'paragraph',2,0,0,$4,1,$5,$6,0,now(),now())`,
		db.MustUUID(chunkID), db.MustUUID(docID), db.MustUUID(parentID), text,
		textutil.RuneCount(text), len(terms)); err != nil {
		t.Fatalf("insert chunk: %v", err)
	}
	for _, term := range terms {
		if _, err := pool.Exec(ctx, `
			INSERT INTO chunk_postings (chunk_id, term, field, tf) VALUES ($1,$2,'text',1)
			ON CONFLICT DO NOTHING`, db.MustUUID(chunkID), term); err != nil {
			t.Fatalf("insert posting: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO term_stats (owner_id, term, df) VALUES ($1,$2,1)
			ON CONFLICT (owner_id, term) DO NOTHING`, db.MustUUID(owner), term); err != nil {
			t.Fatalf("insert term stat: %v", err)
		}
	}
	return seeded{DocID: docID, ChunkID: chunkID}
}

// fakeChat 是 OpenAI 兼容的最小 SSE 端点，返回带别名标记的回答。
func fakeChat(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, delta := range []string{"Zephyr-Bench 是本库的评测集。", "[[c1]]"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", delta)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func configureChat(t *testing.T, models provider.Store, baseURL string) {
	t.Helper()
	key := "sk-test-1234"
	if _, err := models.Put(context.Background(), "chat", provider.PutInput{
		BaseURL: baseURL, ModelName: "fake-chat", APIKey: &key,
	}); err != nil {
		t.Fatalf("configure chat model: %v", err)
	}
}

func newQA(t *testing.T) *qa.Service {
	t.Helper()
	pool := requireDB(t)
	chat := fakeChat(t)
	t.Cleanup(chat.Close)
	models := provider.Store{Pool: pool, Key: []byte("0123456789abcdef0123456789abcdef")}
	configureChat(t, models, chat.URL)
	return &qa.Service{
		Pool: pool, AI: provider.Client{}, Models: models,
		Graph: graph.Service{Pool: pool, Models: models, Timeout: 5 * time.Second},
		TopK: 5, RRFK: 60, K1: 1.2, B: 0.75, Threshold: 0.3,
		Stops: qa.NewStops(), Timeout: 5 * time.Second,
	}
}

func TestIntegrationQAAskPersistsCheckedCitations(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	user := createUser(t, "qa")
	kbSvc := newKnowledge(t)
	kb := createKB(t, kbSvc, user.ID)
	doc := seedReadyDocument(t, pool, user.ID, kb.ID, "论文一")

	svc := newQA(t)
	session, err := svc.CreateSession(ctx, user.ID, "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	type citation struct {
		ChunkID string
		Label   string
	}
	var (
		names     []string
		citations []citation
	)
	emit := func(event string, data any) error {
		names = append(names, event)
		if event == "citations" {
			payload, _ := data.(map[string]any)
			items, _ := payload["items"].([]map[string]any)
			for _, item := range items {
				chunkID, _ := item["chunk_id"].(string)
				label, _ := item["label"].(string)
				citations = append(citations, citation{ChunkID: chunkID, Label: label})
			}
		}
		return nil
	}
	err = svc.Ask(ctx, qa.AskInput{
		Owner: user.ID, SessionID: session.ID, RequestID: db.NewID(),
		Question: "zephyr-bench 是什么", Scope: qa.Scope{Type: "all"},
	}, func() error { return nil }, emit)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if !contains(names, "citations") || !contains(names, "token") || !contains(names, "done") {
		t.Fatalf("missing events, got %v", names)
	}
	if len(citations) != 1 || citations[0].ChunkID != doc.ChunkID {
		t.Fatalf("expected one citation for seeded chunk, got %+v", citations)
	}
	if citations[0].Label != "论文一, 第 2 页" {
		t.Fatalf("unexpected citation label: %q", citations[0].Label)
	}

	saved, err := svc.GetSession(ctx, user.ID, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(saved.Turns) != 1 {
		t.Fatalf("expected one turn, got %d", len(saved.Turns))
	}
	if strings.Contains(saved.Turns[0].Answer, "[[") {
		t.Fatalf("citation markers must be stripped, got %q", saved.Turns[0].Answer)
	}
	if len(saved.Turns[0].Citations) != 1 || saved.Turns[0].Citations[0].ChunkID == nil ||
		*saved.Turns[0].Citations[0].ChunkID != doc.ChunkID {
		t.Fatalf("persisted citations mismatch: %+v", saved.Turns[0].Citations)
	}
	if saved.Turns[0].AnswerKind != "kb" {
		t.Fatalf("answer kind should be kb, got %q", saved.Turns[0].AnswerKind)
	}
}

func TestIntegrationQARefusesWithoutEvidence(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()
	user := createUser(t, "qa-empty")
	kbSvc := newKnowledge(t)
	kb := createKB(t, kbSvc, user.ID)
	seedReadyDocument(t, pool, user.ID, kb.ID, "论文二")

	svc := newQA(t)
	session, err := svc.CreateSession(ctx, user.ID, "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var replaced string
	emit := func(event string, data any) error {
		if event == "replace" {
			replaced, _ = data.(map[string]string)["text"]
		}
		return nil
	}
	if err := svc.Ask(ctx, qa.AskInput{
		Owner: user.ID, SessionID: session.ID, RequestID: db.NewID(),
		Question: "没有出现过的主题词", Scope: qa.Scope{Type: "all"},
	}, func() error { return nil }, emit); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if replaced != qa.Refusal {
		t.Fatalf("expected refusal %q, got %q", qa.Refusal, replaced)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
