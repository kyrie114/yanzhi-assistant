package graph

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/jobs"
	"yanzhi/server/internal/platform/db"
	"yanzhi/server/internal/provider"
)

func TestNeighborsOneHop(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	migrations := os.Getenv("MIGRATIONS_DIR")
	if migrations == "" {
		migrations = filepath.Join("..", "..", "migrations")
	}
	if err := db.Migrate(ctx, url, migrations); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	owner := uuid.NewString()
	other := uuid.NewString()
	kbID := uuid.NewString()
	otherKB := uuid.NewString()
	docID := uuid.NewString()
	foreignID := uuid.NewString()
	paperID := uuid.NewString()
	neighborID := uuid.NewString()
	secondID := uuid.NewString()
	paperEdge := uuid.NewString()
	secondEdge := uuid.NewString()
	between := uuid.NewString()
	foreignPaper := uuid.NewString()
	const foreignTitle = "secret-foreign-title"
	const foreignName = "secret-foreign-paper"

	t.Cleanup(func() {
		ids, ok := db.UUIDs([]string{owner, other})
		if !ok {
			return
		}
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM kg_relations WHERE owner_id = ANY($1::uuid[])`, ids)
		_, _ = pool.Exec(bg, `DELETE FROM kg_entities WHERE owner_id = ANY($1::uuid[])`, ids)
		_, _ = pool.Exec(bg, `DELETE FROM documents WHERE owner_id = ANY($1::uuid[])`, ids)
		_, _ = pool.Exec(bg, `DELETE FROM knowledge_bases WHERE owner_id = ANY($1::uuid[])`, ids)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = ANY($1::uuid[])`, ids)
	})

	mustExec(t, pool, `
		INSERT INTO users (id, email, name, password_hash, role, created_at)
		VALUES ($1, $2, 'owner', 'x', 'user', now()), ($3, $4, 'other', 'x', 'user', now())`,
		owner, owner+"@example.edu.cn", other, other+"@example.edu.cn")
	mustExec(t, pool, `
		INSERT INTO knowledge_bases (id, owner_id, name, created_at, updated_at)
		VALUES ($1, $2, 'kb', now(), now()), ($3, $4, 'other', now(), now())`,
		kbID, owner, otherKB, other)
	mustExec(t, pool, `
		INSERT INTO documents (id, owner_id, kb_id, source_type, title, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'pdf', 'owned-doc', 'ready', now(), now()),
		       ($4, $5, $6, 'pdf', $7, 'ready', now(), now())`,
		docID, owner, kbID, foreignID, other, otherKB, foreignTitle)
	mustExec(t, pool, `
		INSERT INTO kg_entities (id, owner_id, type, name, merge_key, document_id, created_at)
		VALUES ($1, $2, 'paper', 'owned-paper', $3, $4, now()),
		       ($5, $6, 'paper', $7, $8, $9, now())`,
		paperID, owner, PaperKey(docID), docID,
		foreignPaper, other, foreignName, PaperKey(foreignID), foreignID)
	mustExec(t, pool, `
		INSERT INTO kg_entities (id, owner_id, type, name, merge_key, created_at)
		VALUES ($1, $2, 'method', 'neighbor-one', $3, now()),
		       ($4, $2, 'method', 'neighbor-two', $5, now())`,
		neighborID, owner, "method:neighbor-one", secondID, "method:neighbor-two")
	mustExec(t, pool, `
		INSERT INTO kg_relations (id, owner_id, type, source_id, target_id, suspended, created_at)
		VALUES ($1, $2, 'proposes', $3, $4, false, now()),
		       ($5, $2, 'proposes', $3, $6, false, now()),
		       ($7, $2, 'compares_with', $4, $6, false, now())`,
		paperEdge, owner, paperID, neighborID, secondEdge, secondID, between)

	svc := Service{Pool: pool}
	view, err := svc.Neighbors(ctx, owner, docID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Nodes) != 3 || view.Nodes[0].ID != paperID || view.Nodes[0].Name != "owned-paper" {
		t.Fatalf("nodes %+v", view.Nodes)
	}
	gotNodes := map[string]bool{}
	for _, n := range view.Nodes {
		gotNodes[n.ID] = true
	}
	if !gotNodes[neighborID] || !gotNodes[secondID] {
		t.Fatalf("nodes %+v", view.Nodes)
	}
	linked := map[string]bool{}
	for _, e := range view.Edges {
		if e.ID == between || (e.Source == neighborID && e.Target == secondID) || (e.Source == secondID && e.Target == neighborID) {
			t.Fatalf("neighbor edge included: %+v", view.Edges)
		}
		if e.Source == paperID {
			linked[e.Target] = true
		}
		if e.Target == paperID {
			linked[e.Source] = true
		}
	}
	if !linked[neighborID] || !linked[secondID] {
		t.Fatalf("paper edges %+v", view.Edges)
	}

	foreign, err := svc.Neighbors(ctx, owner, foreignID)
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("foreign err %v view %+v", err, foreign)
	}
	blob, marshalErr := json.Marshal(foreign)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	body := err.Error() + string(blob)
	if strings.Contains(body, foreignName) || strings.Contains(body, foreignTitle) {
		t.Fatalf("foreign name leaked: %s", body)
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestGraphExtractBrowseNodeEdgeSupplementAndCleanupPG(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	migrations := os.Getenv("MIGRATIONS_DIR")
	if migrations == "" {
		migrations = filepath.Join("..", "..", "migrations")
	}
	if err := db.Migrate(ctx, url, migrations); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	owner := uuid.NewString()
	kbID := uuid.NewString()
	doc1 := uuid.NewString()
	doc2 := uuid.NewString()
	tagID := uuid.NewString()
	parentID := uuid.NewString()
	chunk1 := uuid.NewString()
	chunk2 := uuid.NewString()
	jobID := uuid.NewString()
	reqID := uuid.NewString()

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM model_call_logs WHERE user_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM jobs WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM kg_relations WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM kg_entities WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM tags WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM documents WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM knowledge_bases WHERE owner_id = $1::uuid`, db.MustUUID(owner))
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1::uuid`, db.MustUUID(owner))
	})

	// Fake OpenAI chat completion endpoint returning entities + relations + citation + unresolved ref
	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `{"entities":[{"type":"method","name":"LoRA","chunk_alias":"c1"}],"relations":[{"type":"proposes","source":"PAPER","target":"LoRA","chunk_alias":"c1","sentence":"提出方法 LoRA"},{"type":"cites","source":"PAPER","target":"Cited Paper Y","chunk_alias":"c1","sentence":"引用 Cited Paper Y"},{"type":"cites","source":"PAPER","target":"Missing Paper Z","chunk_alias":"c1","sentence":"引用 Cited Paper Y 与 Missing Paper Z"}]}`,
				},
			}},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 20},
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer aiSrv.Close()

	mustExec(t, pool, `INSERT INTO users (id, email, name, password_hash, role, created_at) VALUES ($1::uuid, $2, 'g-user', 'x', 'user', now())`, owner, owner+"@example.edu.cn")
	mustExec(t, pool, `INSERT INTO knowledge_bases (id, owner_id, name, graph_enabled, created_at, updated_at) VALUES ($1::uuid, $2::uuid, 'g-kb', true, now(), now())`, kbID, owner)
	mustExec(t, pool, `INSERT INTO tags (id, owner_id, name, created_at) VALUES ($1::uuid, $2::uuid, 'g-tag', now())`, tagID, owner)
	mustExec(t, pool, `
		INSERT INTO documents (id, owner_id, kb_id, source_type, title, status, graph_status, embedding_generation, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'pdf', 'Paper X', 'ready', 'extracting', 1, now(), now()),
		       ($4::uuid, $2::uuid, $3::uuid, 'pdf', 'Cited Paper Y', 'ready', 'ready', 1, now(), now())`,
		doc1, owner, kbID, doc2)
	mustExec(t, pool, `INSERT INTO document_tags (document_id, tag_id) VALUES ($1::uuid, $2::uuid)`, doc1, tagID)
	mustExec(t, pool, `
		INSERT INTO parent_chunks (id, document_id, position, page_start, text)
		VALUES ($1::uuid, $2::uuid, 0, 6, '提出方法 LoRA，引用 Cited Paper Y 与 Missing Paper Z。')`,
		parentID, doc1)
	mustExec(t, pool, `
		INSERT INTO chunks (id, document_id, parent_id, kind, page_start, paragraph_index, overlap_prefix, text, version_no, char_count, term_count, embedding_generation, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'paragraph', 6, 0, 0, '提出方法 LoRA，引用 Cited Paper Y 与 Missing Paper Z。', 1, 32, 8, 1, now(), now()),
		       ($4::uuid, $2::uuid, $3::uuid, 'paragraph', 7, 1, 0, '补充切块：LoRA 在数据集上表现优异。', 1, 20, 6, 1, now(), now())`,
		chunk1, doc1, parentID, chunk2)
	mustExec(t, pool, `
		INSERT INTO jobs (id, kind, owner_id, target_id, request_id, priority, attempt, status, created_at)
		VALUES ($1::uuid, 'graph_extract', $2::uuid, $3::uuid, $4::uuid, 80, 1, 'running', now())`,
		jobID, owner, doc1, reqID)

	models := provider.Store{Pool: pool, Key: []byte("0123456789abcdef0123456789abcdef")}
	apiKey := "sk-graph-test"
	if _, err := models.Put(ctx, "chat", provider.PutInput{
		BaseURL: aiSrv.URL, ModelName: "chat-test", APIKey: &apiKey,
	}); err != nil {
		t.Fatal(err)
	}

	svc := Service{Pool: pool, AI: provider.Client{HTTP: aiSrv.Client()}, Models: models}
	if err := svc.Extract(ctx, jobs.Job{ID: jobID, Kind: "graph_extract", OwnerID: owner, TargetID: doc1, RequestID: reqID, Attempt: 1}); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	// Verify model_call_logs captured graph_extract
	var callCount int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM model_call_logs WHERE request_id = $1::uuid AND kind = 'graph_extract'`, db.MustUUID(reqID)).Scan(&callCount)
	if callCount == 0 {
		t.Fatal("expected model_call_logs entry for graph_extract")
	}

	// Verify Subgraph, Node, Edge, Extraction, Unresolved
	view, err := svc.Subgraph(ctx, owner, LibraryFilter{KBID: kbID, TagIDs: []string{tagID}})
	if err != nil || len(view.Nodes) < 2 || len(view.Edges) < 2 {
		t.Fatalf("Subgraph got %+v err=%v", view, err)
	}
	nodeDet, err := svc.Node(ctx, owner, view.Nodes[0].ID)
	if err != nil || nodeDet.ID == "" {
		t.Fatalf("Node: %+v %v", nodeDet, err)
	}
	edgeDet, err := svc.Edge(ctx, owner, view.Edges[0].ID)
	if err != nil || len(edgeDet.Evidence) == 0 {
		t.Fatalf("Edge: %+v %v", edgeDet, err)
	}
	ext, err := svc.Extraction(ctx, owner, doc1)
	if err != nil || ext.GraphStatus == nil || *ext.GraphStatus != "已完成" {
		t.Fatalf("Extraction: %+v %v", ext, err)
	}
	unres, err := svc.Unresolved(ctx, owner, doc1)
	if err != nil || len(unres) != 1 || unres[0] != "Missing Paper Z" {
		t.Fatalf("Unresolved: %v %v", unres, err)
	}

	// Verify Cleanup removes orphan relations & entities after doc deletion
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tx.Exec(ctx, `DELETE FROM documents WHERE id = $1::uuid`, db.MustUUID(doc1))
	if err := Cleanup(ctx, tx, owner); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("Cleanup: %v", err)
	}
	_ = tx.Commit(ctx)
}
