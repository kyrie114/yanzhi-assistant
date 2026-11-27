package httpapi

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/account"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/qa"
	"yanzhi/server/internal/retrieval"
	"yanzhi/server/internal/suggest"
)

type nfrFakeSuggest struct{ owner string }

func (f nfrFakeSuggest) List(_ context.Context, caller, _, _, _ string) (suggest.Result, error) {
	if caller != f.owner {
		return suggest.Result{}, suggest.ErrNotFound
	}
	return suggest.Result{Questions: []string{"SECRET-SUGGEST-Q1"}, Action: suggest.ActionFill}, nil
}

type nfrFakeGraph struct{}

func (nfrFakeGraph) Subgraph(context.Context, string, graph.LibraryFilter) (graph.View, error) {
	return graph.View{Nodes: []graph.Node{{ID: "node-a", Name: "SECRET-NODE-NAME", Type: "method"}}}, nil
}
func (nfrFakeGraph) Neighbors(context.Context, string, string) (graph.View, error) {
	return graph.View{Nodes: []graph.Node{{ID: "node-a", Name: "SECRET-NODE-NAME", Type: "method"}}}, nil
}
func (nfrFakeGraph) Node(context.Context, string, string) (graph.NodeDetail, error) {
	return graph.NodeDetail{ID: "node-a", Name: "SECRET-NODE-NAME", Type: "method"}, nil
}
func (nfrFakeGraph) Edge(context.Context, string, string) (graph.EdgeDetail, error) {
	return graph.EdgeDetail{ID: "edge-a", Type: "proposes"}, nil
}
func (nfrFakeGraph) Extraction(context.Context, string, string) (graph.Extraction, error) {
	return graph.Extraction{}, nil
}
func (nfrFakeGraph) Unresolved(context.Context, string, string) ([]string, error) {
	return []string{"SECRET-UNRESOLVED-REF"}, nil
}

type nfrFakeDocRetry struct {
	owner  string
	called int
}

func (f *nfrFakeDocRetry) Retry(_ context.Context, owner, _, _ string) error {
	if owner != f.owner {
		return apperr.ErrNotFound
	}
	f.called++
	return nil
}

func TestNFR05_IDORMatrixAllSlice1Endpoints(t *testing.T) {
	accStore := account.NewMemoryStore()
	accSvc := account.NewService(accStore, clock.System{}, []byte("test-secret-with-at-least-32-bytes"), bcrypt.MinCost)
	kbStore := knowledge.NewMemoryStore(accStore, knowledge.NewMemoryBlobStore(), nil)
	gw := provider.NewMemoryGateway()
	ret := retrieval.NewEngine(retrieval.NewMemoryCorpus(qa.MemoryChunkSource(kbStore)), gw, retrieval.DefaultSearchConfig())
	graphMem := graph.NewMemoryStore()
	qaStore := qa.NewMemoryStore(qa.WithAliveCheck(qa.MemoryAliveChecker(kbStore)))
	qaSvc := qa.NewService(context.Background(), qaStore, kbStore, gw, ret, graphMem)
	docRetry := &nfrFakeDocRetry{}
	sugFake := &nfrFakeSuggest{}

	h := NewHandler(accSvc,
		WithKnowledge(kbStore),
		WithQA(qaSvc),
		WithGraph(nfrFakeGraph{}),
		WithGraphRetry(fakeRetry{}),
		WithDocumentRetry(docRetry),
		WithSuggestions(sugFake),
	)

	userA, err := accSvc.Register("idor-a@example.edu.cn", "UserA", "Abc12345")
	if err != nil {
		t.Fatal(err)
	}
	tokenA := registerAndLogin(t, h, "idor-a2@example.edu.cn", "UserA2", "Abc12345")
	_ = userA
	// Use userA2 as owner A cleanly:
	meSt, _, meBody := do(t, h, http.MethodGet, "/api/v1/me", tokenA, nil)
	if meSt != http.StatusOK {
		t.Fatalf("me: %d %s", meSt, meBody)
	}
	var meA struct {
		ID string `json:"id"`
	}
	decode(t, meBody, &meA)
	docRetry.owner = meA.ID
	sugFake.owner = meA.ID

	tokenB := registerAndLogin(t, h, "idor-b@example.edu.cn", "UserB", "Abc12345")
	if _, err := accSvc.CreateAdmin("idor-admin@example.edu.cn", "Admin", "Abc12345"); err != nil {
		t.Fatal(err)
	}
	st, _, admLogin := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "idor-admin@example.edu.cn", "password": "Abc12345",
	})
	if st != http.StatusOK {
		t.Fatalf("admin login: %d %s", st, admLogin)
	}
	var admObj map[string]any
	decode(t, admLogin, &admObj)
	adminToken, _ := admObj["access_token"].(string)

	// Seed User A's KB, Doc, Tag, Chunk, Session
	const secretTitle = "SECRET-OWNER-A-TITLE"
	const secretChunk = "SECRET-OWNER-A-CHUNK-TEXT"
	const secretTag = "SECRET-OWNER-A-TAG"
	const secretSess = "SECRET-OWNER-A-SESSION"

	kbA, err := kbStore.CreateKB(context.Background(), meA.ID, "OwnerAKB")
	if err != nil {
		t.Fatal(err)
	}
	upA, err := kbStore.Upload(context.Background(), meA.ID, kbA.ID, knowledge.UploadMeta{
		FileName: "a.pdf", Title: secretTitle,
	}, []byte("%PDF-1.4\n1 0 obj\n<< /Type /Page >>\nendobj\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := kbStore.SeedChunks(meA.ID, upA.Doc.ID, 1,
		[]knowledge.MemoryParentChunk{{ID: "p1", PageStart: 1, Text: secretChunk}},
		[]knowledge.MemoryChunkInput{{ID: "c1", ParentID: "p1", PageStart: 1, ParagraphIndex: 0, Text: secretChunk, Embedding: []float32{1, 0, 0, 0}}},
	); err != nil {
		t.Fatal(err)
	}
	if err := kbStore.SetDocStatusForTest(meA.ID, upA.Doc.ID, "ready", nil); err != nil {
		t.Fatal(err)
	}
	tagA, err := kbStore.CreateTag(context.Background(), meA.ID, secretTag)
	if err != nil {
		t.Fatal(err)
	}
	snaps := kbStore.ScopedChunkSnapshots(meA.ID, []string{upA.Doc.ID})
	if len(snaps) == 0 {
		t.Fatal("expected seeded chunk")
	}
	chunkA := snaps[0].ChunkID
	sessA, err := qaSvc.CreateSession(context.Background(), meA.ID, secretSess)
	if err != nil {
		t.Fatal(err)
	}

	secrets := []string{secretTitle, secretChunk, secretTag, secretSess, "SECRET-NODE-NAME", "SECRET-SUGGEST-Q1", "SECRET-UNRESOLVED-REF"}

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"get_kb", http.MethodGet, "/api/v1/kbs/" + kbA.ID, nil},
		{"patch_kb_settings", http.MethodPatch, "/api/v1/kbs/" + kbA.ID + "/settings", map[string]any{"graph_enabled": false}},
		{"get_kb_suggest", http.MethodGet, "/api/v1/kbs/" + kbA.ID + "/suggested-questions", nil},
		{"get_doc", http.MethodGet, "/api/v1/documents/" + upA.Doc.ID, nil},
		{"patch_doc", http.MethodPatch, "/api/v1/documents/" + upA.Doc.ID, map[string]any{"title": "hack"}},
		{"delete_doc", http.MethodDelete, "/api/v1/documents/" + upA.Doc.ID, nil},
		{"download_doc", http.MethodGet, "/api/v1/documents/" + upA.Doc.ID + "/file", nil},
		{"retry_doc", http.MethodPost, "/api/v1/documents/" + upA.Doc.ID + "/retries", nil},
		{"put_doc_tags", http.MethodPut, "/api/v1/documents/" + upA.Doc.ID + "/tags", map[string]any{"tag_ids": []string{tagA.ID}}},
		{"get_doc_suggest", http.MethodGet, "/api/v1/documents/" + upA.Doc.ID + "/suggested-questions", nil},
		{"get_chunk", http.MethodGet, "/api/v1/documents/" + upA.Doc.ID + "/chunks/" + chunkA, nil},
		{"patch_tag", http.MethodPatch, "/api/v1/tags/" + tagA.ID, map[string]any{"name": "hack"}},
		{"delete_tag", http.MethodDelete, "/api/v1/tags/" + tagA.ID, nil},
		{"get_session", http.MethodGet, "/api/v1/qa/sessions/" + sessA.ID, nil},
		{"patch_session", http.MethodPatch, "/api/v1/qa/sessions/" + sessA.ID, map[string]any{"title": "hack"}},
		{"delete_session", http.MethodDelete, "/api/v1/qa/sessions/" + sessA.ID, nil},
		{"start_turn", http.MethodPost, "/api/v1/qa/sessions/" + sessA.ID + "/turns", map[string]any{"question": "hi", "scope": map[string]any{"type": "all"}}},
		{"stop_session", http.MethodPost, "/api/v1/qa/sessions/" + sessA.ID + "/stop", nil},
		{"resume_stream", http.MethodGet, "/api/v1/qa/sessions/" + sessA.ID + "/turns/turn-1/stream", nil},
	}

	for _, actor := range []struct {
		role  string
		token string
	}{
		{"userB", tokenB},
		{"admin", adminToken},
	} {
		for _, tc := range cases {
			t.Run(actor.role+"/"+tc.name, func(t *testing.T) {
				status, _, body := do(t, h, tc.method, tc.path, actor.token, tc.body)
				mustError(t, status, body, http.StatusNotFound, "NOT_FOUND", "资源不存在")
				for _, s := range secrets {
					if strings.Contains(string(body), s) {
						t.Fatalf("%s %s leaked secret %q in body: %s", actor.role, tc.name, s, body)
					}
				}
			})
		}
	}

	// Admin also gets 404 on graph endpoints & 403 on create endpoints
	for _, gpath := range []string{
		"/api/v1/graph",
		"/api/v1/graph/nodes/node-a",
		"/api/v1/graph/edges/edge-a",
		"/api/v1/documents/" + upA.Doc.ID + "/graph",
		"/api/v1/documents/" + upA.Doc.ID + "/graph-extraction",
		"/api/v1/documents/" + upA.Doc.ID + "/unresolved-references",
	} {
		status, _, body := do(t, h, http.MethodGet, gpath, adminToken, nil)
		mustError(t, status, body, http.StatusNotFound, "NOT_FOUND", "资源不存在")
	}
	status, _, body := do(t, h, http.MethodPost, "/api/v1/documents/"+upA.Doc.ID+"/graph-extraction/retries", adminToken, nil)
	mustError(t, status, body, http.StatusNotFound, "NOT_FOUND", "资源不存在")

	for _, c := range []struct {
		path string
		body any
	}{
		{"/api/v1/kbs", map[string]string{"name": "admin-kb"}},
		{"/api/v1/tags", map[string]string{"name": "admin-tag"}},
		{"/api/v1/qa/sessions", map[string]string{"title": "admin-sess"}},
	} {
		st, _, b := do(t, h, http.MethodPost, c.path, adminToken, c.body)
		mustError(t, st, b, http.StatusForbidden, "FORBIDDEN", "没有权限执行此操作")
	}

	// Owner A can invoke document retry endpoint cleanly
	st, _, body = do(t, h, http.MethodPost, "/api/v1/documents/"+upA.Doc.ID+"/retries", tokenA, nil)
	if st != http.StatusOK || docRetry.called != 1 {
		t.Fatalf("owner retry failed: status=%d called=%d body=%s", st, docRetry.called, body)
	}
}

func TestNFR06_DirectFilePathAndAuthGuards(t *testing.T) {
	rig := newTestRig(false)
	for _, p := range []string{"/data/a/b.pdf", "/files/a.pdf", "/uploads/a.pdf"} {
		resp, _ := rig.doReq(t, http.MethodGet, p, "", nil, "")
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("direct path %s returned %d, want 404", p, resp.StatusCode)
		}
	}
	for _, p := range []string{"/api/v1/kbs", "/api/v1/documents", "/api/v1/me"} {
		resp, body := rig.doReq(t, http.MethodGet, p, "", nil, "")
		mustError(t, resp.StatusCode, body, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录或登录已过期")
	}
}

func TestNFR04_ExactUploadSizeBoundary(t *testing.T) {
	rig := newTestRig(false)
	_, token := rig.registerUser(t, "nfr04@example.edu.cn", "边界", "Abc12345", true)
	resp, kbBody := rig.doJSON(t, http.MethodPost, "/api/v1/kbs", token, map[string]string{"name": "BoundaryKB"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create kb: %d %s", resp.StatusCode, kbBody)
	}
	var kb struct {
		ID string `json:"id"`
	}
	decode(t, kbBody, &kb)

	uploadExact := func(size int) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("title", "Boundary")
		part, err := w.CreateFormFile("file", "boundary.pdf")
		if err != nil {
			t.Fatal(err)
		}
		header := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Page >>\nendobj\n")
		_, _ = part.Write(header)
		pad := make([]byte, size-len(header))
		for i := range pad {
			pad[i] = ' '
		}
		_, _ = part.Write(pad)
		_ = w.Close()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/kbs/"+kb.ID+"/documents", &buf)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		rig.handler.ServeHTTP(rec, req)
		return rec
	}

	// Exact 50 MiB (52,428,800 bytes) -> 201 Created
	recOK := uploadExact(int(apperr.MaxUploadBytes))
	if recOK.Code != http.StatusCreated {
		t.Fatalf("exact 50MB status = %d, want 201: %s", recOK.Code, recOK.Body.String())
	}
	// 50 MiB + 1 byte -> 413 FILE_TOO_LARGE
	recOver := uploadExact(int(apperr.MaxUploadBytes) + 1)
	mustError(t, recOver.Code, recOver.Body.Bytes(), http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE", "文件大小不能超过 50 MB")
}
