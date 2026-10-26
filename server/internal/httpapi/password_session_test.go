package httpapi

import (
	"net/http"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/account"
	"yanzhi/server/internal/platform/clock"
)

func TestAC0107SameSecondPasswordChangeRevokesEveryPriorSession(t *testing.T) {
	clk := clock.NewManual(time.Date(2026, 10, 20, 9, 0, 0, 750000000, time.UTC))
	h := NewHandler(account.NewService(account.NewMemoryStore(), clk, []byte("test-secret"), bcrypt.MinCost))
	first := registerAndLogin(t, h, "a@example.edu.cn", "张三", "Abc12345")
	second := loginSessionToken(t, h, "a@example.edu.cn", "Abc12345")
	other := registerAndLogin(t, h, "b@example.edu.cn", "李四", "Abc12345")
	status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/password", first, map[string]string{
		"old_password": "Abc12345", "new_password": "Abc123456",
	})
	if status != http.StatusOK {
		t.Fatalf("password change: %d %s", status, body)
	}
	for i, token := range []string{first, second} {
		for _, path := range []string{"/api/v1/me", "/api/v1/documents"} {
			status, _, body := do(t, h, http.MethodGet, path, token, nil)
			if status != http.StatusUnauthorized {
				t.Errorf("pre-change session %d on %s remains usable: %d %s", i+1, path, status, body)
			} else {
				mustError(t, status, body, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录或登录已过期")
			}
		}
	}
	status, _, body = do(t, h, http.MethodGet, "/api/v1/me", other, nil)
	if status != http.StatusOK {
		t.Errorf("another user's session was revoked: %d %s", status, body)
	}
	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": "a@example.edu.cn", "password": "Abc12345",
	})
	mustError(t, status, body, http.StatusUnauthorized, "INVALID_CREDENTIALS", "邮箱或密码错误")
	current := loginSessionToken(t, h, "a@example.edu.cn", "Abc123456")
	status, _, body = do(t, h, http.MethodGet, "/api/v1/me", current, nil)
	if status != http.StatusOK {
		t.Fatalf("immediate new-password login is unusable: %d %s", status, body)
	}

	// An old session cannot change the password a second time after revocation.
	status, _, body = do(t, h, http.MethodPost, "/api/v1/auth/password", second, map[string]string{
		"old_password": "Abc123456", "new_password": "Next12345",
	})
	if status != http.StatusUnauthorized {
		t.Errorf("revoked session changed the password: %d %s", status, body)
	} else {
		mustError(t, status, body, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录或登录已过期")
	}
	status, _, body = do(t, h, http.MethodGet, "/api/v1/me", current, nil)
	if status != http.StatusOK {
		t.Errorf("revoked session invalidated the current session: %d %s", status, body)
	}
}

func TestAC0107RejectedChangeKeepsSessionsAfterPasswordRotation(t *testing.T) {
	clk := clock.NewManual(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC))
	h := NewHandler(account.NewService(account.NewMemoryStore(), clk, []byte("test-secret"), bcrypt.MinCost))
	initial := registerAndLogin(t, h, "a@example.edu.cn", "张三", "Abc12345")
	status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/password", initial, map[string]string{
		"old_password": "Abc12345", "new_password": "Abc123456",
	})
	if status != http.StatusOK {
		t.Fatalf("initial change: %d %s", status, body)
	}
	first := loginSessionToken(t, h, "a@example.edu.cn", "Abc123456")
	second := loginSessionToken(t, h, "a@example.edu.cn", "Abc123456")
	for _, tc := range []struct {
		name    string
		old     string
		next    string
		status  int
		code    string
		message string
	}{
		{"incorrect old password", "Wrong1234", "Next12345", http.StatusUnauthorized, "INVALID_CREDENTIALS", "邮箱或密码错误"},
		{"weak new password", "Abc123456", "abc", http.StatusUnprocessableEntity, "PASSWORD_WEAK", "密码至少 8 位，且需同时包含字母和数字"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/password", first, map[string]string{
				"old_password": tc.old, "new_password": tc.next,
			})
			mustError(t, status, body, tc.status, tc.code, tc.message)
			for i, token := range []string{first, second} {
				status, _, body := do(t, h, http.MethodGet, "/api/v1/me", token, nil)
				if status != http.StatusOK {
					t.Errorf("rejected change revoked current session %d: %d %s", i+1, status, body)
				}
			}
			loginSessionToken(t, h, "a@example.edu.cn", "Abc123456")
		})
	}
}

func loginSessionToken(t *testing.T, h http.Handler, email, password string) string {
	t.Helper()
	status, _, body := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"email": email, "password": password,
	})
	if status != http.StatusOK {
		t.Fatalf("login %s: %d %s", email, status, body)
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	decode(t, body, &result)
	if result.AccessToken == "" {
		t.Fatal("login did not return an access token")
	}
	return result.AccessToken
}
