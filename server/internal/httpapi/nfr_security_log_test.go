package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/account"
	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/applog"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/qa"
	"yanzhi/server/internal/retrieval"
)

func TestNFR07_SentinelZeroLeakageAcrossFlows(t *testing.T) {
	const (
		sentinelPass    = "SentinelPass999"
		sentinelNewPass = "SentinelNewPass888"
		sentinelAPIKey  = "sk-SENTINEL-APIKEY-777"
		sentinelDocBody = "SENTINEL_DOC_FULLTEXT_BODY_666"
		sentinelAsk     = "SENTINEL_USER_QUESTION_555"
		sentinelAnswer  = "SENTINEL_MODEL_ANSWER_444"
	)

	var logBuf bytes.Buffer
	logger := applog.New(&logBuf)

	accStore := account.NewMemoryStore()
	accSvc := account.NewService(accStore, clock.System{}, []byte("test-secret-with-at-least-32-bytes"), bcrypt.MinCost)
	kbStore := knowledge.NewMemoryStore(accStore, knowledge.NewMemoryBlobStore(), nil)
	gw := provider.NewMemoryGateway()
	gw.CompleteFn = func(context.Context, string, string) (string, error) {
		return sentinelAsk, nil
	}
	gw.StreamFn = func(_ context.Context, _, _ string, onDelta func(string) error) error {
		return onDelta(sentinelAnswer + " [c1]")
	}
	ret := retrieval.NewEngine(retrieval.NewMemoryCorpus(qa.MemoryChunkSource(kbStore)), gw, retrieval.DefaultSearchConfig())
	qaStore := qa.NewMemoryStore(qa.WithAliveCheck(qa.MemoryAliveChecker(kbStore)))
	qaSvc := qa.NewService(context.Background(), qaStore, kbStore, gw, ret, graph.NewMemoryStore())

	h := NewHandler(accSvc,
		WithKnowledge(kbStore),
		WithQA(qaSvc),
		WithLogger(logger),
	)

	// 1. Register & Login with sentinel password
	st, _, body := do(t, h, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": "nfr07@example.edu.cn", "name": "日志审计", "password": sentinelPass,
	})
	if st != http.StatusCreated {
		t.Fatalf("register: %d %s", st, body)
	}
	st, _, body = do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "nfr07@example.edu.cn", "password": sentinelPass,
	})
	if st != http.StatusOK {
		t.Fatalf("login: %d %s", st, body)
	}
	var loginRes map[string]any
	decode(t, body, &loginRes)
	token, _ := loginRes["access_token"].(string)

	// 2. Create KB & upload PDF containing sentinel doc text
	st, _, body = do(t, h, http.MethodPost, "/api/v1/kbs", token, map[string]string{"name": "LogAuditKB"})
	if st != http.StatusCreated {
		t.Fatalf("create kb: %d %s", st, body)
	}
	var kb struct {
		ID string `json:"id"`
	}
	decode(t, body, &kb)

	var mpBuf bytes.Buffer
	mw := multipart.NewWriter(&mpBuf)
	_ = mw.WriteField("title", "LogAuditDoc")
	part, _ := mw.CreateFormFile("file", "audit.pdf")
	_, _ = part.Write([]byte("%PDF-1.4\n% " + sentinelDocBody + "\n1 0 obj\n<< /Type /Page >>\nendobj\n"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kbs/"+kb.ID+"/documents", &mpBuf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	var doc struct {
		ID string `json:"id"`
	}
	decode(t, rec.Body.Bytes(), &doc)

	meSt, _, meBody := do(t, h, http.MethodGet, "/api/v1/me", token, nil)
	if meSt != http.StatusOK {
		t.Fatalf("me: %d %s", meSt, meBody)
	}
	var me struct {
		ID string `json:"id"`
	}
	decode(t, meBody, &me)
	_ = kbStore.SeedChunks(me.ID, doc.ID, 1,
		[]knowledge.MemoryParentChunk{{ID: "p1", PageStart: 1, Text: sentinelDocBody}},
		[]knowledge.MemoryChunkInput{{ID: "c1", ParentID: "p1", PageStart: 1, ParagraphIndex: 0, Text: sentinelDocBody, Embedding: []float32{1, 0, 0, 0}}},
	)
	_ = kbStore.SetDocStatusForTest(me.ID, doc.ID, "ready", nil)

	// 3. Run QA turn with sentinel question & answer
	st, _, body = do(t, h, http.MethodPost, "/api/v1/qa/sessions", token, map[string]string{"title": "LogSession"})
	if st != http.StatusCreated {
		t.Fatalf("create session: %d %s", st, body)
	}
	var sess struct {
		ID string `json:"id"`
	}
	decode(t, body, &sess)
	st, _, body = do(t, h, http.MethodPost, "/api/v1/qa/sessions/"+sess.ID+"/turns", token, map[string]any{
		"question": sentinelAsk,
		"scope":    map[string]any{"type": "all"},
	})
	if st != http.StatusOK {
		t.Fatalf("qa turn: %d %s", st, body)
	}

	// 4. Change password with sentinel passwords
	st, _, body = do(t, h, http.MethodPost, "/api/v1/auth/password", token, map[string]string{
		"old_password": sentinelPass, "new_password": sentinelNewPass,
	})
	if st != http.StatusOK {
		t.Fatalf("change password: %d %s", st, body)
	}

	// 5. Simulate direct structured log calls with accidental sensitive attributes
	logger.Info("model_probe_audit", "request_id", "11111111-1111-4111-8111-111111111111", "api_key", sentinelAPIKey, "prompt", sentinelDocBody, "answer", sentinelAnswer)

	rawLogs := logBuf.String()
	lines := strings.Split(strings.TrimSpace(rawLogs), "\n")
	if len(lines) < 5 {
		t.Fatalf("expected structured JSON access logs, got %d lines: %s", len(lines), rawLogs)
	}
	for i, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d invalid JSON: %v (%s)", i, err, line)
		}
		if rid, _ := m["request_id"].(string); rid == "" {
			t.Fatalf("line %d missing request_id: %s", i, line)
		}
		if ts, _ := m["time"].(string); !strings.HasSuffix(ts, "+08:00") {
			t.Fatalf("line %d time not Asia/Shanghai: %s", i, line)
		}
	}

	sentinels := []string{
		sentinelPass,
		sentinelNewPass,
		sentinelAPIKey,
		sentinelDocBody,
		sentinelAsk,
		sentinelAnswer,
		token,
	}
	for _, s := range sentinels {
		if strings.Contains(rawLogs, s) {
			t.Fatalf("NFR-07 LEAK DETECTED: sentinel %q found in logs:\n%s", s, rawLogs)
		}
	}
}
