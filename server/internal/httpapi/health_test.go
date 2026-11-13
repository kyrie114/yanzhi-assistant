package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"yanzhi/server/internal/account"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/platform/clock"
	"yanzhi/server/internal/provider"
)

func TestHealthRouteInMemoryHandler(t *testing.T) {
	svc := account.NewService(account.NewMemoryStore(), clock.System{}, []byte("test-secret"), bcrypt.MinCost)

	t.Run("nil probe reports unavailable without auth", func(t *testing.T) {
		h := NewHandler(svc)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
		}
		if !validUUID(rec.Header().Get("X-Request-Id")) {
			t.Fatalf("expected valid X-Request-Id, got %q", rec.Header().Get("X-Request-Id"))
		}
		var body map[string]string
		decode(t, rec.Body.Bytes(), &body)
		if body["status"] != "ok" || body["docreader"] != "unavailable" {
			t.Fatalf("unexpected body: %+v", body)
		}
	})

	t.Run("probe false reports unavailable", func(t *testing.T) {
		h := NewHandler(svc, WithHealthProbe(func(context.Context) bool { return false }))
		status, hdr, raw := do(t, h, http.MethodGet, "/api/v1/health", "", nil)
		if status != http.StatusOK {
			t.Fatalf("status %d, body %s", status, raw)
		}
		if !validUUID(hdr.Get("X-Request-Id")) {
			t.Fatalf("expected valid X-Request-Id, got %q", hdr.Get("X-Request-Id"))
		}
		var body map[string]string
		decode(t, raw, &body)
		if body["status"] != "ok" || body["docreader"] != "unavailable" {
			t.Fatalf("unexpected body: %+v", body)
		}
	})

	t.Run("probe true reports serving and preserves request id", func(t *testing.T) {
		h := NewHandler(svc, WithHealthProbe(func(context.Context) bool { return true }))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		const reqID = "8f1c0d6e-2a4b-4c0a-9d11-e0aa00000001"
		req.Header.Set("X-Request-Id", reqID)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Request-Id") != reqID {
			t.Fatalf("X-Request-Id = %q, want %q", rec.Header().Get("X-Request-Id"), reqID)
		}
		var body map[string]string
		decode(t, rec.Body.Bytes(), &body)
		if body["status"] != "ok" || body["docreader"] != "serving" {
			t.Fatalf("unexpected body: %+v", body)
		}
	})
}

func TestLibraryHandlerHealthAndAuthRoutes(t *testing.T) {
	accounts, err := identity.NewService(nil, []byte("test-secret-with-at-least-32-bytes"), bcrypt.MinCost, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("health endpoint reports probe result", func(t *testing.T) {
		h := NewLibraryHandler(accounts, knowledge.Service{}, provider.Store{},
			WithHealthProbe(func(context.Context) bool { return true }),
		)
		status, hdr, raw := do(t, h, http.MethodGet, "/api/v1/health", "", nil)
		if status != http.StatusOK {
			t.Fatalf("status %d, body %s", status, raw)
		}
		if !validUUID(hdr.Get("X-Request-Id")) {
			t.Fatalf("expected valid X-Request-Id, got %q", hdr.Get("X-Request-Id"))
		}
		var body map[string]string
		decode(t, raw, &body)
		if body["status"] != "ok" || body["docreader"] != "serving" {
			t.Fatalf("unexpected body: %+v", body)
		}
	})

	t.Run("health endpoint defaults to unavailable", func(t *testing.T) {
		h := NewLibraryHandler(accounts, knowledge.Service{}, provider.Store{})
		status, hdr, raw := do(t, h, http.MethodGet, "/api/v1/health", "", nil)
		if status != http.StatusOK {
			t.Fatalf("status %d, body %s", status, raw)
		}
		if !validUUID(hdr.Get("X-Request-Id")) {
			t.Fatalf("expected valid X-Request-Id, got %q", hdr.Get("X-Request-Id"))
		}
		var body map[string]string
		decode(t, raw, &body)
		if body["status"] != "ok" || body["docreader"] != "unavailable" {
			t.Fatalf("unexpected body: %+v", body)
		}
	})

	h := NewLibraryHandler(accounts, knowledge.Service{}, provider.Store{})
	for _, tc := range []struct {
		name  string
		path  string
		token string
		body  any
	}{
		{name: "logout missing token", path: "/api/v1/auth/logout"},
		{name: "logout invalid token", path: "/api/v1/auth/logout", token: "invalid.jwt.token"},
		{name: "password missing token", path: "/api/v1/auth/password", body: map[string]string{"old_password": "a", "new_password": "b"}},
		{name: "password invalid token", path: "/api/v1/auth/password", token: "invalid.jwt.token", body: map[string]string{"old_password": "a", "new_password": "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, raw := do(t, h, http.MethodPost, tc.path, tc.token, tc.body)
			mustError(t, status, raw, http.StatusUnauthorized, "UNAUTHENTICATED", "未登录或登录已过期")
		})
	}
}
