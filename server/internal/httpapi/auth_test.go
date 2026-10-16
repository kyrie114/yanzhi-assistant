package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/account"
	"yanzhi/server/internal/platform/clock"
)

func newAPI() http.Handler {
	svc := account.NewService(account.NewMemoryStore(), clock.System{}, []byte("test-secret"), bcrypt.MinCost)
	return NewHandler(svc)
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) (int, http.Header, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Header(), rec.Body.Bytes()
}

func decode(t *testing.T, body []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(body, dst); err != nil {
		t.Fatalf("json: %v body %s", err, body)
	}
}

type apiError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details"`
}

func mustError(t *testing.T, status int, body []byte, wantStatus int, code, message string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status %d, want %d, body %s", status, wantStatus, body)
	}
	var got apiError
	decode(t, body, &got)
	if got.Code != code || got.Message != message {
		t.Fatalf("code %s message %s", got.Code, got.Message)
	}
	if got.RequestID == "" || got.Details == nil {
		t.Fatalf("error body %+v", got)
	}
}

func registerAndLogin(t *testing.T, h http.Handler, email, name, password string) string {
	t.Helper()
	status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": email, "name": name, "password": password,
	})
	if status != http.StatusCreated {
		t.Fatalf("register %d %s", status, body)
	}
	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": email, "password": password,
	})
	if status != http.StatusOK {
		t.Fatalf("login %d %s", status, body)
	}
	var login map[string]any
	decode(t, body, &login)
	token, _ := login["access_token"].(string)
	if token == "" {
		t.Fatalf("missing token %s", body)
	}
	return token
}

func TestAC0101RegisterSuccess(t *testing.T) {
	h := newAPI()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(`{"email":"a@example.edu.cn","name":"张三","password":"Abc12345","role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "8f1c0d6e-2a4b-4c0a-9d11-e0aa00000001")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-Id") != "8f1c0d6e-2a4b-4c0a-9d11-e0aa00000001" {
		t.Fatalf("request id %s", rec.Header().Get("X-Request-Id"))
	}
	var user map[string]any
	decode(t, rec.Body.Bytes(), &user)
	if user["email"] != "a@example.edu.cn" || user["name"] != "张三" || user["role"] != "注册用户" {
		t.Fatalf("user %+v", user)
	}
	if user["email_verified"] != false || user["storage_bytes"] != float64(0) || user["id"] == "" {
		t.Fatalf("user %+v", user)
	}
	raw := rec.Body.String()
	if strings.Contains(raw, "Abc12345") || strings.Contains(raw, "password") || strings.Contains(raw, "$2") || strings.Contains(raw, "access_token") {
		t.Fatalf("register response leaked a secret: %s", raw)
	}
}

func TestAC0102DuplicateEmail(t *testing.T) {
	h := newAPI()
	registerAndLogin(t, h, "a@example.edu.cn", "张三", "Abc12345")
	status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": "A@Example.edu.cn", "name": "李四", "password": "Abc12345",
	})
	mustError(t, status, body, http.StatusConflict, "EMAIL_TAKEN", "该邮箱已注册")
	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "a@example.edu.cn", "password": "Abc12345",
	})
	if status != http.StatusOK {
		t.Fatalf("original account changed: %d %s", status, body)
	}
}

func TestAC0103WeakPassword(t *testing.T) {
	h := newAPI()
	for _, password := range []string{"abc123", "abcdefgh"} {
		status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
			"email": "a@example.edu.cn", "name": "张三", "password": password,
		})
		mustError(t, status, body, http.StatusUnprocessableEntity, "PASSWORD_WEAK", "密码至少 8 位，且需同时包含字母和数字")
	}
	status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "a@example.edu.cn", "password": "Abc12345",
	})
	mustError(t, status, body, http.StatusUnauthorized, "INVALID_CREDENTIALS", "邮箱或密码错误")
}

func TestAC0104LoginSuccess(t *testing.T) {
	h := newAPI()
	registerAndLogin(t, h, "a@example.edu.cn", "张三", "Abc12345")
	status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "a@example.edu.cn", "password": "Abc12345",
	})
	if status != http.StatusOK {
		t.Fatalf("status %d %s", status, body)
	}
	var login map[string]any
	decode(t, body, &login)
	if login["token_type"] != "Bearer" || login["expires_in"] != float64(7200) || login["role"] != "注册用户" || login["email_verified"] != false {
		t.Fatalf("login %+v", login)
	}
	token, _ := login["access_token"].(string)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %s", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	decode(t, payload, &claims)
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if exp-iat != 7200 || claims["sub"] == "" || claims["jti"] == "" || claims["role"] != "user" {
		t.Fatalf("claims %+v", claims)
	}
	status, header, body := do(t, h, http.MethodGet, "/api/v1/me", token, nil)
	if status != http.StatusOK {
		t.Fatalf("me %d %s", status, body)
	}
	if header.Get("X-Request-Id") == "" {
		t.Fatal("missing request id")
	}
	var me map[string]any
	decode(t, body, &me)
	if me["name"] != "张三" || strings.Contains(string(body), "password") {
		t.Fatalf("me %s", body)
	}
}

func TestAC0105LoginFailure(t *testing.T) {
	h := newAPI()
	registerAndLogin(t, h, "a@example.edu.cn", "张三", "Abc12345")
	statusMissing, _, missingBody := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "missing@example.edu.cn", "password": "Abc12345",
	})
	statusWrong, _, wrongBody := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "a@example.edu.cn", "password": "wrong123",
	})
	mustError(t, statusMissing, missingBody, http.StatusUnauthorized, "INVALID_CREDENTIALS", "邮箱或密码错误")
	mustError(t, statusWrong, wrongBody, http.StatusUnauthorized, "INVALID_CREDENTIALS", "邮箱或密码错误")
	var missing, wrong apiError
	decode(t, missingBody, &missing)
	decode(t, wrongBody, &wrong)
	if missing.Message != wrong.Message || missing.Code != wrong.Code {
		t.Fatalf("messages differ: %s vs %s", missingBody, wrongBody)
	}
}

func TestAC0106LogoutRevokesOnlyCurrentToken(t *testing.T) {
	h := newAPI()
	status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/register", "", map[string]string{
		"email": "a@example.edu.cn", "name": "张三", "password": "Abc12345",
	})
	if status != http.StatusCreated {
		t.Fatalf("register %d %s", status, body)
	}
	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "a@example.edu.cn", "password": "Abc12345",
	})
	if status != http.StatusOK {
		t.Fatalf("login %d %s", status, body)
	}
	var first map[string]any
	decode(t, body, &first)
	token, _ := first["access_token"].(string)
	if token == "" {
		t.Fatalf("missing token %s", body)
	}
	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "a@example.edu.cn", "password": "Abc12345",
	})
	if status != http.StatusOK {
		t.Fatalf("second login %d %s", status, body)
	}
	var second map[string]any
	decode(t, body, &second)
	token2 := second["access_token"].(string)

	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/logout", token, nil)
	if status != http.StatusNoContent {
		t.Fatalf("logout %d %s", status, body)
	}
	if len(body) != 0 {
		t.Fatalf("logout body %s", body)
	}
	status, _, body = do(t, h, http.MethodGet, "/api/v1/documents", token, nil)
	mustError(t, status, body, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录或登录已过期")
	status, _, body = do(t, h, http.MethodGet, "/api/v1/documents", token2, nil)
	if status != http.StatusOK {
		t.Fatalf("second token %d %s", status, body)
	}
	var list map[string]any
	decode(t, body, &list)
	items, _ := list["items"].([]any)
	if items == nil || list["total"] != float64(0) {
		t.Fatalf("list %+v", list)
	}
}

func TestAC0107ChangePassword(t *testing.T) {
	start := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	clk := clock.NewManual(start)
	h := NewHandler(account.NewService(account.NewMemoryStore(), clk, []byte("test-secret"), bcrypt.MinCost))
	token := registerAndLogin(t, h, "a@example.edu.cn", "张三", "Abc12345")
	clk.Set(start.Add(time.Second))
	status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/password", token, map[string]string{
		"old_password": "Abc12345", "new_password": "Abc123456",
	})
	if status != http.StatusOK {
		t.Fatalf("change %d %s", status, body)
	}
	status, _, body = do(t, h, http.MethodGet, "/api/v1/documents", token, nil)
	mustError(t, status, body, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录或登录已过期")
	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "a@example.edu.cn", "password": "Abc123456",
	})
	if status != http.StatusOK {
		t.Fatalf("new password %d %s", status, body)
	}
	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "a@example.edu.cn", "password": "Abc12345",
	})
	mustError(t, status, body, http.StatusUnauthorized, "INVALID_CREDENTIALS", "邮箱或密码错误")

	other := registerAndLogin(t, h, "b@example.edu.cn", "李四", "Abc12345")
	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/password", other, map[string]string{
		"old_password": "wrong123", "new_password": "Abc123456",
	})
	mustError(t, status, body, http.StatusUnauthorized, "INVALID_CREDENTIALS", "邮箱或密码错误")
	status, _, body = do(t, h, http.MethodGet, "/api/v1/documents", other, nil)
	if status != http.StatusOK {
		t.Fatalf("token changed after rejected password: %d %s", status, body)
	}
	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "b@example.edu.cn", "password": "Abc12345",
	})
	if status != http.StatusOK {
		t.Fatalf("old password lost: %d %s", status, body)
	}

	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/password", other, map[string]string{
		"old_password": "Abc12345", "new_password": "abc",
	})
	mustError(t, status, body, http.StatusUnprocessableEntity, "PASSWORD_WEAK", "密码至少 8 位，且需同时包含字母和数字")
	status, _, body = do(t, h, http.MethodGet, "/api/v1/documents", other, nil)
	if status != http.StatusOK {
		t.Fatalf("token changed after weak password: %d %s", status, body)
	}
	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "b@example.edu.cn", "password": "Abc12345",
	})
	if status != http.StatusOK {
		t.Fatalf("hash changed after weak password: %d %s", status, body)
	}
}
