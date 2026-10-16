// Package httpapi 把身份模块的结果映射成 HTTP 状态码。
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"yanzhi/server/internal/account"
)

type requestIDKey struct{}

type handler struct {
	svc *account.Service
}

// NewHandler 注册本切片的账户路由，以及用于检查令牌的资料列表。
func NewHandler(svc *account.Service) http.Handler {
	h := &handler{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.HandleFunc("POST /api/v1/auth/password", h.changePassword)
	mux.HandleFunc("GET /api/v1/me", h.me)
	mux.HandleFunc("GET /api/v1/documents", h.documents)
	return withRequestID(mux)
}

func (h *handler) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, r, err)
		return
	}
	user, err := h.svc.Register(req.Email, req.Name, req.Password)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, user.Public())
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, r, err)
		return
	}
	result, err := h.svc.Login(req.Email, req.Password)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":   result.AccessToken,
		"token_type":     "Bearer",
		"expires_in":     result.ExpiresIn,
		"role":           result.Role,
		"email_verified": result.EmailVerified,
	})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	raw, err := bearer(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if err := h.svc.Logout(raw); err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) changePassword(w http.ResponseWriter, r *http.Request) {
	raw, err := bearer(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, r, err)
		return
	}
	if err := h.svc.ChangePassword(raw, req.OldPassword, req.NewPassword); err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, caller.User.Public())
}

// documents 供退出登录验收调用。资料表属于后续切片，已登录用户目前得到空列表。
func (h *handler) documents(w http.ResponseWriter, r *http.Request) {
	if _, err := h.caller(r); err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     []any{},
		"page":      1,
		"page_size": 20,
		"total":     0,
	})
}

func (h *handler) caller(r *http.Request) (account.Caller, error) {
	raw, err := bearer(r)
	if err != nil {
		return account.Caller{}, err
	}
	return h.svc.Authenticate(raw)
}

func (h *handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var validation *account.ValidationError
	switch {
	case errors.As(err, &validation):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{
			Code:      "VALIDATION_ERROR",
			Message:   "请求字段不符合要求",
			RequestID: requestID(r),
			Details:   stringMap(validation.Details),
		})
	case errors.Is(err, account.ErrEmailTaken):
		writeJSON(w, http.StatusConflict, errorBody{
			Code: "EMAIL_TAKEN", Message: "该邮箱已注册", RequestID: requestID(r), Details: map[string]any{},
		})
	case errors.Is(err, account.ErrWeakPassword):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{
			Code: "PASSWORD_WEAK", Message: "密码至少 8 位，且需同时包含字母和数字", RequestID: requestID(r), Details: map[string]any{},
		})
	case errors.Is(err, account.ErrInvalidCredentials):
		writeJSON(w, http.StatusUnauthorized, errorBody{
			Code: "INVALID_CREDENTIALS", Message: "邮箱或密码错误", RequestID: requestID(r), Details: map[string]any{},
		})
	case errors.Is(err, account.ErrUnauthenticated):
		writeJSON(w, http.StatusUnauthorized, errorBody{
			Code: "UNAUTHENTICATED", Message: "未登录或登录已过期", RequestID: requestID(r), Details: map[string]any{},
		})
	default:
		writeJSON(w, http.StatusInternalServerError, errorBody{
			Code: "INTERNAL", Message: "服务暂时不可用，请稍后重试", RequestID: requestID(r), Details: map[string]any{},
		})
	}
}

type errorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details"`
}

func stringMap(in map[string]string) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func decodeJSON(r *http.Request, dst any) error {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return &account.ValidationError{Details: map[string]string{"body": "请求体不是合法的 JSON"}}
	}
	return nil
}

func bearer(r *http.Request) (string, error) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return "", account.ErrUnauthenticated
	}
	raw := strings.TrimSpace(header[len(prefix):])
	if raw == "" {
		return "", account.ErrUnauthenticated
	}
	return raw, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !validUUID(id) {
			id = newRequestUUID()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newRequestUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}
