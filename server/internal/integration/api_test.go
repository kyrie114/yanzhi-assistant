package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/httpapi"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/qa"
	"yanzhi/server/internal/suggest"
)

func newTestAPI(t *testing.T) *httptest.Server {
	t.Helper()
	pool := requireDB(t)
	ids := newIdentity(t)
	kb := newKnowledge(t)
	models := provider.Store{Pool: pool, Key: []byte("0123456789abcdef0123456789abcdef")}
	chat := fakeChat(t)
	t.Cleanup(chat.Close)
	configureChat(t, models, chat.URL)
	graphSvc := graph.Service{Pool: pool, Models: models, Timeout: 5 * time.Second}
	qaSvc := &qa.Service{
		Pool: pool, AI: provider.Client{}, Models: models, Graph: graphSvc,
		TopK: 5, RRFK: 60, K1: 1.2, B: 0.75, Threshold: 0.3,
		Stops: qa.NewStops(), Timeout: 5 * time.Second,
	}
	app := &httpapi.App{
		Identity: ids, Knowledge: kb, QA: qaSvc, Graph: graphSvc,
		Models: models, Suggest: suggest.NewMemory(),
	}
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func doJSON(t *testing.T, method, url, token string, payload any) (int, map[string]any) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func registerLogin(t *testing.T, srv *httptest.Server, email string) string {
	t.Helper()
	status, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/register", "", map[string]string{
		"email": email, "name": "张三", "password": "Abc12345",
	})
	if status != http.StatusCreated {
		t.Fatalf("register: %d %v", status, body)
	}
	status, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", map[string]string{
		"email": email, "password": "Abc12345",
	})
	if status != http.StatusOK {
		t.Fatalf("login: %d %v", status, body)
	}
	token, _ := body["access_token"].(string)
	if token == "" {
		t.Fatalf("login returned no token: %v", body)
	}
	return token
}

func TestAC0101RegisterSuccess(t *testing.T) {
	srv := newTestAPI(t)
	status, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/register", "", map[string]string{
		"email": uniqueEmail("ac0101"), "name": "张三", "password": "Abc12345",
	})
	if status != http.StatusCreated {
		t.Fatalf("expected 201, got %d %v", status, body)
	}
	if body["id"] == "" || body["email"] == "" {
		t.Fatalf("response should carry public user: %v", body)
	}
	if _, ok := body["access_token"]; ok {
		t.Fatalf("register must not return a token: %v", body)
	}
	if _, ok := body["password"]; ok {
		t.Fatalf("register must not return a password: %v", body)
	}
}

func TestAC0102DuplicateEmail(t *testing.T) {
	srv := newTestAPI(t)
	email := uniqueEmail("ac0102")
	registerLogin(t, srv, email)
	status, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/register", "", map[string]string{
		"email": email, "name": "李四", "password": "Abc12345",
	})
	if status != http.StatusConflict || body["code"] != "EMAIL_TAKEN" {
		t.Fatalf("expected 409 EMAIL_TAKEN, got %d %v", status, body)
	}
}

func TestAC0103WeakPassword(t *testing.T) {
	srv := newTestAPI(t)
	for _, password := range []string{"abc123", "abcdefgh"} {
		status, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/register", "", map[string]string{
			"email": uniqueEmail("ac0103"), "name": "张三", "password": password,
		})
		if status != http.StatusUnprocessableEntity || body["code"] != "PASSWORD_WEAK" {
			t.Fatalf("password %q: expected 422 PASSWORD_WEAK, got %d %v", password, status, body)
		}
	}
}

func TestAC0104LoginSuccess(t *testing.T) {
	srv := newTestAPI(t)
	email := uniqueEmail("ac0104")
	registerLogin(t, srv, email)
	status, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", map[string]string{
		"email": email, "password": "Abc12345",
	})
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d %v", status, body)
	}
	if body["token_type"] != "Bearer" || body["expires_in"].(float64) != 7200 {
		t.Fatalf("unexpected token body: %v", body)
	}
}

func TestAC0105LoginFailure(t *testing.T) {
	newTestAPIWith := func() *httptest.Server { return newTestAPI(t) }
	srv := newTestAPIWith()
	email := uniqueEmail("ac0105")
	registerLogin(t, srv, email)

	statusUnknown, bodyUnknown := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", map[string]string{
		"email": uniqueEmail("missing"), "password": "Abc12345",
	})
	statusWrong, bodyWrong := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", map[string]string{
		"email": email, "password": "Wrong12345",
	})
	if statusUnknown != http.StatusUnauthorized || statusWrong != http.StatusUnauthorized {
		t.Fatalf("expected 401 for both, got %d and %d", statusUnknown, statusWrong)
	}
	if bodyUnknown["message"] != bodyWrong["message"] {
		t.Fatalf("messages must match: %q vs %q", bodyUnknown["message"], bodyWrong["message"])
	}
	if bodyWrong["message"] != "邮箱或密码错误" {
		t.Fatalf("unexpected message: %v", bodyWrong["message"])
	}
}

func TestAC0106LogoutRevokesOnlyCurrentToken(t *testing.T) {
	srv := newTestAPI(t)
	email := uniqueEmail("ac0106")
	first := registerLogin(t, srv, email)
	_, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", map[string]string{
		"email": email, "password": "Abc12345",
	})
	second, _ := body["access_token"].(string)

	status, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/logout", first, nil)
	if status != http.StatusNoContent {
		t.Fatalf("logout: expected 204, got %d", status)
	}
	if status, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/documents", first, nil); status != http.StatusUnauthorized {
		t.Fatalf("revoked token should be 401, got %d", status)
	}
	if status, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/documents", second, nil); status != http.StatusOK {
		t.Fatalf("other token should stay valid, got %d", status)
	}
}

func TestAC0107ChangePassword(t *testing.T) {
	srv := newTestAPI(t)
	email := uniqueEmail("ac0107")
	token := registerLogin(t, srv, email)

	status, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/password", token, map[string]string{
		"old_password": "Abc12345", "new_password": "Abc123456",
	})
	if status != http.StatusOK {
		t.Fatalf("change password: %d %v", status, body)
	}
	if status, _ := doJSON(t, http.MethodGet, srv.URL+"/api/v1/me", token, nil); status != http.StatusUnauthorized {
		t.Fatalf("token issued before change must be invalid, got %d", status)
	}
	if status, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", map[string]string{
		"email": email, "password": "Abc12345",
	}); status != http.StatusUnauthorized {
		t.Fatalf("old password must fail, got %d", status)
	}
	if status, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login", "", map[string]string{
		"email": email, "password": "Abc123456",
	}); status != http.StatusOK {
		t.Fatalf("new password must work, got %d", status)
	}

	fresh := registerLogin(t, srv, uniqueEmail("ac0107b"))
	if status, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/password", fresh, map[string]string{
		"old_password": "Nope12345", "new_password": "Abc123456",
	}); status != http.StatusUnauthorized {
		t.Fatalf("wrong old password must be 401, got %d", status)
	}
	if status, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/password", fresh, map[string]string{
		"old_password": "Abc12345", "new_password": "abc",
	}); status != http.StatusUnprocessableEntity || body["code"] != "PASSWORD_WEAK" {
		t.Fatalf("weak new password: got %d %v", status, body)
	}
}

func TestHTTPKnowledgeUploadAndList(t *testing.T) {
	srv := newTestAPI(t)
	token := registerLogin(t, srv, uniqueEmail("http-kb"))

	status, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/kbs", token, map[string]string{"name": "论文阅读"})
	if status != http.StatusCreated {
		t.Fatalf("create kb: %d %v", status, body)
	}
	kbID, _ := body["id"].(string)
	if kbID == "" {
		t.Fatalf("no kb id: %v", body)
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", "paper.pdf")
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	_, _ = part.Write(tinyPDF(1))
	_ = writer.WriteField("title", "图神经网络综述")
	_ = writer.WriteField("authors", `["张三"]`)
	_ = writer.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/kbs/"+kbID+"/documents", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d %s", resp.StatusCode, raw)
	}
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	if doc["status"] != "待处理" || doc["title"] != "图神经网络综述" {
		t.Fatalf("unexpected doc: %v", doc)
	}

	status, list := doJSON(t, http.MethodGet, srv.URL+"/api/v1/documents", token, nil)
	if status != http.StatusOK {
		t.Fatalf("list documents: %d %v", status, list)
	}
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one document, got %v", list)
	}
}

func TestHTTPAskStreamsCheckedCitations(t *testing.T) {
	pool := requireDB(t)
	srv := newTestAPI(t)
	token := registerLogin(t, srv, uniqueEmail("http-qa"))

	status, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/kbs", token, map[string]string{"name": "问答库"})
	if status != http.StatusCreated {
		t.Fatalf("create kb: %d %v", status, body)
	}
	kbID, _ := body["id"].(string)
	_, me := doJSON(t, http.MethodGet, srv.URL+"/api/v1/me", token, nil)
	owner, _ := me["id"].(string)
	doc := seedReadyDocument(t, pool, owner, kbID, "论文三")

	status, session := doJSON(t, http.MethodPost, srv.URL+"/api/v1/qa/sessions", token, map[string]string{"title": "第一次"})
	if status != http.StatusCreated {
		t.Fatalf("create session: %d %v", status, session)
	}
	sessionID, _ := session["id"].(string)

	payload := strings.NewReader(`{"question":"zephyr-bench 是什么","scope":{"type":"all"}}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/qa/sessions/"+sessionID+"/turns", payload)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ask: %d %s", resp.StatusCode, raw)
	}
	stream := string(raw)
	for _, want := range []string{"event: citations", "event: done", doc.ChunkID} {
		if !strings.Contains(stream, want) {
			t.Fatalf("stream missing %q:\n%s", want, stream)
		}
	}

	status, detail := doJSON(t, http.MethodGet, srv.URL+"/api/v1/qa/sessions/"+sessionID, token, nil)
	if status != http.StatusOK {
		t.Fatalf("session detail: %d %v", status, detail)
	}
	turns, _ := detail["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("expected one persisted turn, got %v", detail)
	}
}
