package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"yanzhi/server/internal/apperr"
)

type requestIDKey struct{}

type errorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details"`
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeErr 按错误码给出状态码。没有错误码的异常不把内部信息写给调用者。
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	e := apperr.As(err)
	if e == nil {
		e = apperr.New("INTERNAL", "服务暂时不可用，请稍后重试")
	}
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(w, statusFor(e.Code), errorBody{
		Code: e.Code, Message: e.Message, RequestID: requestID(r), Details: details,
	})
}

func statusFor(code string) int {
	switch code {
	case "UNAUTHENTICATED", "INVALID_CREDENTIALS":
		return http.StatusUnauthorized
	case "FORBIDDEN", "EMAIL_UNVERIFIED":
		return http.StatusForbidden
	case "NOT_FOUND":
		return http.StatusNotFound
	case "EMAIL_TAKEN", "DUPLICATE_DOCUMENT", "DOCUMENT_NOT_READY", "NO_READY_DOCUMENTS",
		"RETRY_EXHAUSTED", "TAG_EXISTS", "MODEL_NOT_CONFIGURED", "REINDEX_CONFIRMATION_REQUIRED",
		"IDEMPOTENCY_CONFLICT", "STREAM_CLOSED":
		return http.StatusConflict
	case "PASSWORD_WEAK", "FILE_ENCRYPTED", "TOO_MANY_PAGES", "STORAGE_QUOTA_EXCEEDED",
		"PAGE_OUT_OF_RANGE", "VALIDATION_ERROR":
		return http.StatusUnprocessableEntity
	case "FILE_TOO_LARGE":
		return http.StatusRequestEntityTooLarge
	case "UNSUPPORTED_MEDIA":
		return http.StatusUnsupportedMediaType
	case "MODEL_UNAVAILABLE", "DOCREADER_UNAVAILABLE", "MAIL_UNAVAILABLE":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func decodeJSON(r *http.Request, dst any) error {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return apperr.WithDetails("VALIDATION_ERROR", "请求体不是合法的 JSON", map[string]any{"body": "请求体不是合法的 JSON"})
	}
	return nil
}

func bearer(r *http.Request) (string, error) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return "", apperr.ErrUnauthenticated
	}
	raw := strings.TrimSpace(header[len(prefix):])
	if raw == "" {
		return "", apperr.ErrUnauthenticated
	}
	return raw, nil
}
