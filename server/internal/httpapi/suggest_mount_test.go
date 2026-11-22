package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/account"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/suggest"
)

const suggestSecret = "secret-suggested-question"

type fakeSuggest struct{}

func (fakeSuggest) List(context.Context, string, string, string, string) (suggest.Result, error) {
	return suggest.Result{Questions: []string{suggestSecret}, Action: suggest.ActionFill}, nil
}

func TestSuggestedQuestionsHideFromAdmin(t *testing.T) {
	svc := account.NewService(account.NewMemoryStore(), clock.System{}, []byte("test-secret"), bcrypt.MinCost)
	h := NewHandler(svc, WithSuggestions(fakeSuggest{}))
	userToken := registerAndLogin(t, h, "suggest-user@example.edu.cn", "用户", "Abc12345")
	if _, err := svc.CreateAdmin("suggest-admin@example.edu.cn", "管理员", "Abc12345"); err != nil {
		t.Fatal(err)
	}
	status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "suggest-admin@example.edu.cn", "password": "Abc12345",
	})
	if status != http.StatusOK {
		t.Fatalf("admin login %d %s", status, body)
	}
	var login map[string]any
	decode(t, body, &login)
	adminToken, _ := login["access_token"].(string)

	status, _, body = do(t, h, http.MethodGet, "/api/v1/documents/doc-1/suggested-questions", adminToken, nil)
	mustError(t, status, body, http.StatusNotFound, "NOT_FOUND", "资源不存在")
	if strings.Contains(string(body), suggestSecret) {
		t.Fatalf("admin body contains the question: %s", body)
	}

	status, _, body = do(t, h, http.MethodGet, "/api/v1/documents/doc-1/suggested-questions", userToken, nil)
	if status != http.StatusOK || !strings.Contains(string(body), suggestSecret) || !strings.Contains(string(body), `"action":"fill"`) {
		t.Fatalf("user suggestions %d %s", status, body)
	}
}
