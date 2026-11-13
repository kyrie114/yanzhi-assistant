package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/account"
	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/qa"
	"yanzhi/server/internal/retrieval"
)

type qaHTTPFixture struct {
	accounts *account.MemoryStore
	acctSvc  *account.Service
	kb       *knowledge.MemoryStore
	qaStore  *qa.MemoryStore
	gw       *provider.MemoryGateway
	graphMem *graph.MemoryStore
	qaSvc    *qa.Service
	handler  http.Handler

	mu  sync.Mutex
	now time.Time
}

func newQAHTTPFixture(t *testing.T, opts ...Option) *qaHTTPFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	acctStore := account.NewMemoryStore()
	acctSvc := account.NewService(acctStore, clock.System{}, []byte("qa-http-test-secret"), bcrypt.MinCost)
	kb := knowledge.NewMemoryStore(acctStore, nil, nil)
	qaStore := qa.NewMemoryStore(qa.WithAliveCheck(qa.MemoryAliveChecker(kb)))
	gw := provider.NewMemoryGateway()
	graphMem := graph.NewMemoryStore()
	ret := retrieval.NewEngine(
		retrieval.NewMemoryCorpus(qa.MemoryChunkSource(kb)),
		gw,
		retrieval.DefaultSearchConfig(),
	)

	f := &qaHTTPFixture{
		accounts: acctStore,
		acctSvc:  acctSvc,
		kb:       kb,
		qaStore:  qaStore,
		gw:       gw,
		graphMem: graphMem,
		now:      time.Date(2026, 11, 13, 14, 0, 0, 0, time.UTC),
	}
	f.qaSvc = qa.NewService(ctx, qaStore, kb, gw, ret, graphMem,
		qa.WithStreamTTL(15*time.Minute),
		qa.WithNowFunc(func() time.Time {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.now
		}),
	)
	baseOpts := []Option{WithKnowledge(kb), WithQA(f.qaSvc)}
	baseOpts = append(baseOpts, opts...)
	f.handler = NewHandler(acctSvc, baseOpts...)
	return f
}

func (f *qaHTTPFixture) advance(d time.Duration) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	return f.now
}

func (f *qaHTTPFixture) registerAndLogin(t *testing.T, email, role string, verified bool) (string, string) {
	t.Helper()
	var userID string
	now := time.Now().UTC()
	if role == "admin" {
		hash, err := bcrypt.GenerateFromPassword([]byte("Password123"), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		userID = uuid.NewString()
		var verifiedAt *time.Time
		if verified {
			verifiedAt = &now
		}
		if err := f.accounts.Create(account.User{
			ID:              userID,
			Email:           email,
			Name:            "管理员",
			PasswordHash:    string(hash),
			Role:            "admin",
			EmailVerifiedAt: verifiedAt,
			CreatedAt:       now,
		}); err != nil {
			t.Fatalf("create admin %s: %v", email, err)
		}
	} else {
		user, err := f.acctSvc.Register(email, "用户", "Password123")
		if err != nil {
			t.Fatalf("register %s: %v", email, err)
		}
		userID = user.ID
		if verified {
			if err := f.accounts.SetEmailVerified(userID, &now); err != nil {
				t.Fatalf("verify %s: %v", email, err)
			}
		}
	}
	res, err := f.acctSvc.Login(email, "Password123")
	if err != nil {
		t.Fatalf("login %s: %v", email, err)
	}
	return userID, res.AccessToken
}

func (f *qaHTTPFixture) seedReadyDoc(t *testing.T, ownerID, kbID, title string, parentID, chunkID, parentText, chunkText string, page int) string {
	t.Helper()
	res, err := f.kb.Upload(context.Background(), ownerID, kbID, knowledge.UploadMeta{
		FileName: title + ".pdf",
		Title:    title,
	}, []byte(fmt.Sprintf("%%PDF-1.4\n%% %s-%s\n/Type /Page\n%%%%EOF", title, uuid.NewString())))
	if err != nil {
		t.Fatalf("upload %s: %v", title, err)
	}
	if err := f.kb.SeedChunks(ownerID, res.Doc.ID, 1,
		[]knowledge.MemoryParentChunk{{ID: parentID, PageStart: page, Text: parentText}},
		[]knowledge.MemoryChunkInput{{ID: chunkID, ParentID: parentID, PageStart: page, Text: chunkText, Embedding: []float32{1, 0}}},
	); err != nil {
		t.Fatalf("seed chunks %s: %v", title, err)
	}
	if err := f.kb.SetDocStatus(ownerID, res.Doc.ID, "ready"); err != nil {
		t.Fatalf("set ready %s: %v", title, err)
	}
	return res.Doc.ID
}

type sseFrame struct {
	ID    int64
	Event string
	Data  map[string]any
}

func readSSEFrames(t *testing.T, r io.Reader) []sseFrame {
	t.Helper()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var frames []sseFrame
	var cur sseFrame
	var dataLines []string
	flush := func() {
		if cur.Event == "" && len(dataLines) == 0 {
			return
		}
		raw := strings.Join(dataLines, "\n")
		var m map[string]any
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				t.Fatalf("unmarshal SSE data %q: %v", raw, err)
			}
		}
		cur.Data = m
		frames = append(frames, cur)
		cur = sseFrame{}
		dataLines = nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "id:") {
			idVal, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "id:")), 10, 64)
			if err != nil {
				t.Fatalf("invalid SSE id line %q: %v", line, err)
			}
			cur.ID = idVal
		} else if strings.HasPrefix(line, "event:") {
			cur.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	return frames
}

func TestQAGatesKBSettingsChunkDetailAndListFormat(t *testing.T) {
	f := newQAHTTPFixture(t, WithEmailRestriction(true))
	ownerID, token := f.registerAndLogin(t, "qa-user@example.com", "user", true)
	_, unverifiedToken := f.registerAndLogin(t, "qa-unverified@example.com", "user", false)
	_, otherToken := f.registerAndLogin(t, "qa-other@example.com", "user", true)
	_, adminToken := f.registerAndLogin(t, "qa-admin@example.com", "admin", true)

	// 未登录访问 QA -> 401
	code, _, _ := do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions", "", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}

	// 未验证邮箱受限：会话 CRUD/列表/停止/续传均放行，唯独提问 POST /turns 返回 403 EMAIL_UNVERIFIED
	code, _, _ = do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions", unverifiedToken, nil)
	if code != http.StatusOK {
		t.Fatalf("unverified GET /qa/sessions expected 200, got %d", code)
	}
	code, _, body := do(t, f.handler, http.MethodPost, "/api/v1/qa/sessions", unverifiedToken, map[string]string{"title": "未验证会话"})
	if code != http.StatusCreated {
		t.Fatalf("unverified POST /qa/sessions expected 201, got %d: %s", code, body)
	}
	var unvSess qa.Session
	decode(t, body, &unvSess)

	code, _, _ = do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions/"+unvSess.ID, unverifiedToken, nil)
	if code != http.StatusOK {
		t.Fatalf("unverified GET /qa/sessions/{id} expected 200, got %d", code)
	}
	code, _, _ = do(t, f.handler, http.MethodPatch, "/api/v1/qa/sessions/"+unvSess.ID, unverifiedToken, map[string]string{"title": "新名称"})
	if code != http.StatusOK {
		t.Fatalf("unverified PATCH /qa/sessions/{id} expected 200, got %d", code)
	}
	code, _, body = do(t, f.handler, http.MethodPost, "/api/v1/qa/sessions/"+unvSess.ID+"/turns", unverifiedToken, map[string]any{
		"question": "你好",
		"scope":    map[string]any{"type": "all"},
	})
	mustError(t, code, body, http.StatusForbidden, "EMAIL_UNVERIFIED", "请先完成邮箱验证")
	code, _, _ = do(t, f.handler, http.MethodPost, "/api/v1/qa/sessions/"+unvSess.ID+"/stop", unverifiedToken, nil)
	if code != http.StatusOK {
		t.Fatalf("unverified POST /stop expected 200, got %d", code)
	}
	code, _, _ = do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions/"+unvSess.ID+"/turns/"+uuid.NewString()+"/stream", unverifiedToken, nil)
	if code != http.StatusNotFound {
		t.Fatalf("unverified GET /stream for missing turn expected 404 (not EMAIL_UNVERIFIED), got %d", code)
	}
	code, _, _ = do(t, f.handler, http.MethodDelete, "/api/v1/qa/sessions/"+unvSess.ID, unverifiedToken, nil)
	if code != http.StatusOK {
		t.Fatalf("unverified DELETE /qa/sessions/{id} expected 200, got %d", code)
	}

	// 管理员：GET /kbs 与 GET /qa/sessions 返回 200 空列表；POST /qa/sessions 返回 403 FORBIDDEN；打开任意个人会话/提问返回 404
	code, _, body = do(t, f.handler, http.MethodGet, "/api/v1/kbs", adminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("admin GET /kbs expected 200, got %d: %s", code, body)
	}
	var kbEnvelope struct {
		Items []knowledge.KB `json:"items"`
	}
	decode(t, body, &kbEnvelope)
	if len(kbEnvelope.Items) != 0 {
		t.Fatalf("expected empty items, got %+v", kbEnvelope.Items)
	}
	code, _, body = do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions", adminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("admin GET /qa/sessions expected 200, got %d: %s", code, body)
	}
	var adminSessEnv struct {
		Items    []qa.Session `json:"items"`
		Page     int          `json:"page"`
		PageSize int          `json:"page_size"`
		Total    int          `json:"total"`
	}
	decode(t, body, &adminSessEnv)
	if len(adminSessEnv.Items) != 0 || adminSessEnv.Total != 0 || adminSessEnv.PageSize != 20 {
		t.Fatalf("admin GET /qa/sessions expected empty list with page_size=20, got %s", body)
	}
	code, _, body = do(t, f.handler, http.MethodPost, "/api/v1/qa/sessions", adminToken, map[string]string{})
	mustError(t, code, body, http.StatusForbidden, "FORBIDDEN", "没有权限执行此操作")

	ownerSess, err := f.qaSvc.CreateSession(context.Background(), ownerID, "普通用户会话")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/qa/sessions/" + ownerSess.ID},
		{http.MethodPatch, "/api/v1/qa/sessions/" + ownerSess.ID},
		{http.MethodDelete, "/api/v1/qa/sessions/" + ownerSess.ID},
		{http.MethodPost, "/api/v1/qa/sessions/" + ownerSess.ID + "/turns"},
		{http.MethodPost, "/api/v1/qa/sessions/" + ownerSess.ID + "/stop"},
		{http.MethodGet, "/api/v1/qa/sessions/" + ownerSess.ID + "/turns/" + uuid.NewString() + "/stream"},
	} {
		c, _, _ := do(t, f.handler, tc.method, tc.path, adminToken, map[string]any{"title": "x", "question": "q", "scope": map[string]any{"type": "all"}})
		if c != http.StatusNotFound {
			t.Fatalf("admin %s %s expected 404, got %d", tc.method, tc.path, c)
		}
	}

	// 正常用户创建 KB、修补 KB 设置、查询切片详情与统一 GET /kbs 格式
	kb, err := f.kb.CreateKB(context.Background(), ownerID, "系统架构")
	if err != nil {
		t.Fatal(err)
	}
	code, _, body = do(t, f.handler, http.MethodGet, "/api/v1/kbs", token, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /kbs expected 200, got %d", code)
	}
	decode(t, body, &kbEnvelope)
	if len(kbEnvelope.Items) != 1 || kbEnvelope.Items[0].ID != kb.ID {
		t.Fatalf("unexpected GET /kbs envelope: %+v", kbEnvelope)
	}

	// PATCH /kbs/{kb_id}/settings
	code, _, body = do(t, f.handler, http.MethodPatch, "/api/v1/kbs/"+kb.ID+"/settings", token, map[string]any{
		"answer_instruction":  "分条作答",
		"retrieval_threshold": 0.45,
		"graph_enabled":       false,
	})
	if code != http.StatusOK {
		t.Fatalf("PATCH settings expected 200, got %d: %s", code, body)
	}
	var updatedKB knowledge.KB
	decode(t, body, &updatedKB)
	if updatedKB.AnswerInstruction == nil || *updatedKB.AnswerInstruction != "分条作答" || updatedKB.GraphEnabled {
		t.Fatalf("unexpected updated KB settings: %+v", updatedKB)
	}
	code, _, _ = do(t, f.handler, http.MethodPatch, "/api/v1/kbs/"+kb.ID+"/settings", token, map[string]any{
		"retrieval_threshold": 1.5,
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid threshold expected 422, got %d", code)
	}
	code, _, _ = do(t, f.handler, http.MethodPatch, "/api/v1/kbs/"+kb.ID+"/settings", otherToken, map[string]any{
		"graph_enabled": true,
	})
	if code != http.StatusNotFound {
		t.Fatalf("foreign PATCH settings expected 404, got %d", code)
	}

	// GET /documents/{document_id}/chunks/{chunk_id}
	pID := uuid.NewString()
	cID := uuid.NewString()
	docID := f.seedReadyDoc(t, ownerID, kb.ID, "Chunk Doc", pID, cID, "完整父段落内容", "命中子块文本", 7)

	code, _, body = do(t, f.handler, http.MethodGet, "/api/v1/documents/"+docID+"/chunks/"+cID, token, nil)
	if code != http.StatusOK {
		t.Fatalf("GET chunk detail expected 200, got %d: %s", code, body)
	}
	var chunkResp struct {
		Text       string `json:"text"`
		PageStart  int    `json:"page_start"`
		ParentText string `json:"parent_text"`
	}
	decode(t, body, &chunkResp)
	if chunkResp.Text != "命中子块文本" || chunkResp.PageStart != 7 || chunkResp.ParentText != "完整父段落内容" {
		t.Fatalf("unexpected chunk detail: %+v", chunkResp)
	}
	code, _, _ = do(t, f.handler, http.MethodGet, "/api/v1/documents/"+docID+"/chunks/"+cID, otherToken, nil)
	if code != http.StatusNotFound {
		t.Fatalf("foreign chunk detail expected 404, got %d", code)
	}
	code, _, _ = do(t, f.handler, http.MethodGet, "/api/v1/documents/"+docID+"/chunks/"+cID, adminToken, nil)
	if code != http.StatusNotFound {
		t.Fatalf("admin chunk detail expected 404, got %d", code)
	}
}

func TestQASessionCRUDPreflightAndSmalltalkSSE(t *testing.T) {
	f := newQAHTTPFixture(t)
	_, token := f.registerAndLogin(t, "qa-s1@example.com", "user", true)
	_, otherToken := f.registerAndLogin(t, "qa-s2@example.com", "user", true)

	// 创建、列表（page_size 统一分页契约与 +08:00 时间偏移）、重命名、跨用户 404
	code, _, body := do(t, f.handler, http.MethodPost, "/api/v1/qa/sessions", token, nil)
	if code != http.StatusCreated {
		t.Fatalf("create session expected 201, got %d: %s", code, body)
	}
	var sess qa.Session
	decode(t, body, &sess)
	if sess.Title != "新会话" {
		t.Fatalf("expected default title 新会话, got %q", sess.Title)
	}
	if !strings.HasSuffix(sess.CreatedAt, "+08:00") || !strings.HasSuffix(sess.UpdatedAt, "+08:00") {
		t.Fatalf("expected +08:00 Asia/Shanghai timestamps, got %+v", sess)
	}

	// 再建第二个会话以验证 page_size=1 真实分页且响应只含 page_size
	code, _, body2 := do(t, f.handler, http.MethodPost, "/api/v1/qa/sessions", token, map[string]string{"title": "第二会话"})
	if code != http.StatusCreated {
		t.Fatalf("create second session expected 201, got %d: %s", code, body2)
	}
	var sess2 qa.Session
	decode(t, body2, &sess2)

	code, _, body = do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions?page=1&page_size=1", token, nil)
	if code != http.StatusOK {
		t.Fatalf("list sessions expected 200, got %d: %s", code, body)
	}
	var rawList map[string]any
	decode(t, body, &rawList)
	if _, hasSize := rawList["size"]; hasSize {
		t.Fatalf("unexpected legacy key 'size' in list response: %s", body)
	}
	if ps, ok := rawList["page_size"].(float64); !ok || int(ps) != 1 {
		t.Fatalf("expected page_size=1 in response, got %s", body)
	}
	var listResp struct {
		Items    []qa.Session `json:"items"`
		PageSize int          `json:"page_size"`
		Total    int          `json:"total"`
	}
	decode(t, body, &listResp)
	if listResp.Total != 2 || listResp.PageSize != 1 || len(listResp.Items) != 1 {
		t.Fatalf("unexpected session list pagination: %+v", listResp)
	}
	code, _, _ = do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions?page=1&page_size=101", token, nil)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for out-of-range page_size, got %d", code)
	}
	_, _, _ = do(t, f.handler, http.MethodDelete, "/api/v1/qa/sessions/"+sess2.ID, token, nil)

	code, _, _ = do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions/"+sess.ID, otherToken, nil)
	if code != http.StatusNotFound {
		t.Fatalf("foreign get session expected 404, got %d", code)
	}

	// 无就绪文档的非寒暄提问 -> 流开始前返回 JSON 409 NO_READY_DOCUMENTS
	code, _, body = do(t, f.handler, http.MethodPost, "/api/v1/qa/sessions/"+sess.ID+"/turns", token, map[string]any{
		"question": "请解释 Attention 机制",
		"scope":    map[string]any{"type": "all"},
	})
	if code != http.StatusConflict {
		t.Fatalf("expected 409 NO_READY_DOCUMENTS, got %d: %s", code, body)
	}
	var errResp errorBody
	decode(t, body, &errResp)
	if errResp.Code != "NO_READY_DOCUMENTS" {
		t.Fatalf("expected NO_READY_DOCUMENTS code, got %+v", errResp)
	}

	// 同状态下发送寒暄 -> 绕过 NO_READY_DOCUMENTS 并正常返回 SSE + X-Turn-Id
	f.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		return onDelta("你好，请问有什么可以帮你？")
	}
	srv := httptest.NewServer(f.handler)
	defer srv.Close()

	reqBody, _ := json.Marshal(map[string]any{
		"question": "你好！",
		"scope":    map[string]any{"type": "all"},
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/qa/sessions/"+sess.ID+"/turns", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 SSE, got %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", resp.Header.Get("Content-Type"))
	}
	turnID := resp.Header.Get("X-Turn-Id")
	if turnID == "" {
		t.Fatal("expected non-empty X-Turn-Id response header")
	}
	frames := readSSEFrames(t, resp.Body)
	if len(frames) < 2 || frames[len(frames)-1].Event != "done" {
		t.Fatalf("expected token + done frames in smalltalk SSE, got %+v", frames)
	}
	for _, fr := range frames {
		if fr.Event != "token" && fr.Event != "done" {
			t.Fatalf("smalltalk SSE must only emit token and done, got %q (%+v)", fr.Event, fr)
		}
		if fr.Event == "done" && fr.Data["session_id"] != sess.ID {
			t.Fatalf("smalltalk done frame missing session_id=%q: %+v", sess.ID, fr.Data)
		}
	}

	// PATCH + DELETE
	code, _, body = do(t, f.handler, http.MethodPatch, "/api/v1/qa/sessions/"+sess.ID, token, map[string]string{"title": "已重命名"})
	if code != http.StatusOK {
		t.Fatalf("patch session expected 200, got %d: %s", code, body)
	}

	// 就绪文档存在但未配置 chat 模型 -> 流开启前返回 JSON 409 MODEL_NOT_CONFIGURED
	f.gw.RemoveModel("chat")
	meCode, _, meBody := do(t, f.handler, http.MethodGet, "/api/v1/me", token, nil)
	if meCode != http.StatusOK {
		t.Fatalf("GET /me expected 200, got %d", meCode)
	}
	var meUser struct {
		ID string `json:"id"`
	}
	decode(t, meBody, &meUser)
	kbReady, err := f.kb.CreateKB(context.Background(), meUser.ID, "NoModelKB")
	if err != nil {
		t.Fatal(err)
	}
	f.seedReadyDoc(t, meUser.ID, kbReady.ID, "Ready Doc", uuid.NewString(), uuid.NewString(), "p", "c", 1)
	code, _, body = do(t, f.handler, http.MethodPost, "/api/v1/qa/sessions/"+sess.ID+"/turns", token, map[string]any{
		"question": "What is this?",
		"scope":    map[string]any{"type": "kb", "kb_id": kbReady.ID},
	})
	if code != http.StatusConflict {
		t.Fatalf("expected 409 MODEL_NOT_CONFIGURED, got %d: %s", code, body)
	}
	decode(t, body, &errResp)
	if errResp.Code != "MODEL_NOT_CONFIGURED" {
		t.Fatalf("expected MODEL_NOT_CONFIGURED code, got %+v", errResp)
	}

	code, _, _ = do(t, f.handler, http.MethodDelete, "/api/v1/qa/sessions/"+sess.ID, token, nil)
	if code != http.StatusOK {
		t.Fatalf("delete session expected 200, got %d", code)
	}
}

func TestQASSEDisconnectResumeStopAndDeletedDocCitation(t *testing.T) {
	f := newQAHTTPFixture(t)
	ownerID, token := f.registerAndLogin(t, "qa-stream@example.com", "user", true)

	kb, err := f.kb.CreateKB(context.Background(), ownerID, "SSE KB")
	if err != nil {
		t.Fatal(err)
	}
	docID := f.seedReadyDoc(t, ownerID, kb.ID, "Stream Paper", uuid.NewString(), uuid.NewString(), "Parent", "streaming SSE resume keyword", 4)

	sess, err := f.qaSvc.CreateSession(context.Background(), ownerID, "SSE会话")
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(f.handler)
	defer srv.Close()

	// 1. 断线续传与 X-Turn-Id 验证
	firstSent := make(chan struct{})
	releaseRest := make(chan struct{})
	f.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		if err := onDelta("前半段"); err != nil {
			return err
		}
		close(firstSent)
		<-releaseRest
		return onDelta("后半段 [[c1]]。")
	}

	turnCtx, cancelTurnReq := context.WithCancel(context.Background())
	reqBody, _ := json.Marshal(map[string]any{
		"question": "streaming SSE resume",
		"scope":    map[string]any{"type": "kb", "kb_id": kb.ID},
	})
	req, _ := http.NewRequestWithContext(turnCtx, http.MethodPost, srv.URL+"/api/v1/qa/sessions/"+sess.ID+"/turns", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	turnID := resp.Header.Get("X-Turn-Id")
	if turnID == "" {
		t.Fatal("missing X-Turn-Id")
	}

	// 读取到第一个 token 后主动中断初始 HTTP SSE 连接
	scanner := bufio.NewScanner(resp.Body)
	var cutSeq int64
	var curID int64
	var curEvt string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id:") {
			curID, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "id:")), 10, 64)
		} else if strings.HasPrefix(line, "event:") {
			curEvt = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if line == "" && curEvt == "token" {
			cutSeq = curID
			break
		}
	}
	cancelTurnReq()
	resp.Body.Close()
	if cutSeq == 0 {
		t.Fatal("did not observe initial token frame before disconnect")
	}

	// 非法 after -> 422
	code, _, _ := do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions/"+sess.ID+"/turns/"+turnID+"/stream?after=-1", token, nil)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for negative after, got %d", code)
	}

	// 放行生成并使用 ?after=cutSeq 续传
	close(releaseRest)
	resumeReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/qa/sessions/%s/turns/%s/stream?after=%d", srv.URL, sess.ID, turnID, cutSeq), nil)
	resumeReq.Header.Set("Authorization", "Bearer "+token)
	resumeResp, err := http.DefaultClient.Do(resumeReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resumeResp.Body.Close()
	if resumeResp.StatusCode != http.StatusOK {
		t.Fatalf("resume expected 200, got %d", resumeResp.StatusCode)
	}
	tailFrames := readSSEFrames(t, resumeResp.Body)
	if len(tailFrames) == 0 || tailFrames[0].ID != cutSeq+1 || tailFrames[len(tailFrames)-1].Event != "done" {
		t.Fatalf("unexpected resumed SSE frames: cutSeq=%d frames=%+v", cutSeq, tailFrames)
	}

	// 通过 Last-Event-ID 头同样支持续传
	headerResumeReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/qa/sessions/%s/turns/%s/stream", srv.URL, sess.ID, turnID), nil)
	headerResumeReq.Header.Set("Authorization", "Bearer "+token)
	headerResumeReq.Header.Set("Last-Event-ID", strconv.FormatInt(cutSeq, 10))
	headerResumeResp, err := http.DefaultClient.Do(headerResumeReq)
	if err != nil {
		t.Fatal(err)
	}
	headerTail := readSSEFrames(t, headerResumeResp.Body)
	headerResumeResp.Body.Close()
	if len(headerTail) == 0 || headerTail[0].ID != cutSeq+1 {
		t.Fatalf("Last-Event-ID resume mismatch: cutSeq=%d tail=%+v", cutSeq, headerTail)
	}

	// 验证 done 后查会话立即可见已持久化的问答与引用，且删除原文档后引用规范化为 "来源已删除"
	code, _, body := do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions/"+sess.ID, token, nil)
	if code != http.StatusOK {
		t.Fatalf("GET session expected 200, got %d", code)
	}
	var detail qa.Session
	decode(t, body, &detail)
	if len(detail.Turns) != 1 || len(detail.Turns[0].Citations) != 1 || detail.Turns[0].Citations[0].Label != "Stream Paper, 第 4 页" {
		t.Fatalf("unexpected persisted turn detail: %+v", detail)
	}

	// 推进超过 15 分钟重放窗口并清理 -> 再续传同一 turn 返回 409 STREAM_CLOSED
	f.qaSvc.SweepExpiredStreams(f.advance(16 * time.Minute))
	code, _, body = do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions/"+sess.ID+"/turns/"+turnID+"/stream?after=0", token, nil)
	mustError(t, code, body, http.StatusConflict, "STREAM_CLOSED", "生成已结束，请从会话记录查看")

	// 2. StopSession：流中停止返回 error CANCELLED，且不落库半成品
	blocked := make(chan struct{})
	f.gw.StreamFn = func(ctx context.Context, system, user string, onDelta func(string) error) error {
		if err := onDelta("将被停止的半成品"); err != nil {
			return err
		}
		close(blocked)
		<-ctx.Done()
		return ctx.Err()
	}
	stopReqBody, _ := json.Marshal(map[string]any{
		"question": "streaming SSE resume",
		"scope":    map[string]any{"type": "kb", "kb_id": kb.ID},
	})
	stopReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/qa/sessions/"+sess.ID+"/turns", bytes.NewReader(stopReqBody))
	stopReq.Header.Set("Content-Type", "application/json")
	stopReq.Header.Set("Authorization", "Bearer "+token)
	stopResp, err := http.DefaultClient.Do(stopReq)
	if err != nil {
		t.Fatal(err)
	}
	defer stopResp.Body.Close()

	<-blocked
	code, _, _ = do(t, f.handler, http.MethodPost, "/api/v1/qa/sessions/"+sess.ID+"/stop", token, nil)
	if code != http.StatusOK {
		t.Fatalf("POST /stop expected 200, got %d", code)
	}
	stopFrames := readSSEFrames(t, stopResp.Body)
	var sawCancelled bool
	for _, fr := range stopFrames {
		if fr.Event == "error" && fr.Data["code"] == "CANCELLED" {
			sawCancelled = true
		}
		if fr.Event == "done" {
			t.Fatal("stopped turn must not emit done")
		}
	}
	if !sawCancelled {
		t.Fatalf("expected CANCELLED error frame, got %+v", stopFrames)
	}

	// 删除文档后检查会话，仍仅保留第 1 轮，且 citation 已规范化
	code, _, _ = do(t, f.handler, http.MethodDelete, "/api/v1/documents/"+docID, token, nil)
	if code != http.StatusOK {
		t.Fatalf("delete doc expected 200, got %d", code)
	}
	code, _, body = do(t, f.handler, http.MethodGet, "/api/v1/qa/sessions/"+sess.ID, token, nil)
	decode(t, body, &detail)
	if len(detail.Turns) != 1 || detail.Turns[0].Citations[0].Label != "来源已删除" {
		t.Fatalf("expected only 1 turn with deleted label, got %+v", detail.Turns)
	}
}
