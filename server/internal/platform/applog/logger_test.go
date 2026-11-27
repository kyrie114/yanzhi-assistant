package applog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type captureSink struct {
	entries []RequestLogEntry
}

func (s *captureSink) RecordRequest(_ context.Context, entry RequestLogEntry) {
	s.entries = append(s.entries, entry)
}

func TestLoggerRedactsSentinelsAndFormatsShanghaiTime(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)

	sentinels := []string{
		"SENTINEL_PASS_999",
		"eyJhbGciOiJIUzI1NiJ9.SENTINEL_JWT_888",
		"sk-SENTINEL-APIKEY-777",
		"yzk_SENTINEL_KEY_666",
		"SENTINEL_PROMPT_555",
		"SENTINEL_ANSWER_444",
		"SENTINEL_DOC_CONTENT_333",
	}

	logger.Info("audit_test",
		"request_id", "req-001",
		"password", sentinels[0],
		"authorization", "Bearer "+sentinels[1],
		"api_key", sentinels[2],
		"search_token", sentinels[3],
		"prompt", sentinels[4],
		"answer", sentinels[5],
		"chunk_text", sentinels[6],
		"err_detail", "upstream rejected sk-SENTINEL-APIKEY-777",
		"jwt_raw", sentinels[1],
	)

	out := buf.String()
	for _, s := range sentinels {
		if strings.Contains(out, s) {
			t.Fatalf("sentinel %q leaked in log: %s", s, out)
		}
	}

	var parsed map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &parsed); err != nil {
		t.Fatalf("invalid json log: %v (%s)", err, out)
	}
	ts, _ := parsed["time"].(string)
	if !strings.HasSuffix(ts, "+08:00") {
		t.Fatalf("expected Asia/Shanghai +08:00 timestamp, got %q", ts)
	}
	if parsed["password"] != "[REDACTED]" || parsed["err_detail"] != "[REDACTED]" || parsed["jwt_raw"] != "[REDACTED]" {
		t.Fatalf("expected redacted fields, got %+v", parsed)
	}
}

func TestAccessMiddlewareCapturesStatusPreservesFlusherAndHidesSecrets(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)
	sink := &captureSink{}

	mw := AccessMiddleware(logger, sink,
		func(r *http.Request) string { return r.Header.Get("X-Request-Id") },
		func(*http.Request) string { return "user-123" },
	)

	var flusherOK bool
	var unwrapOK bool
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, flusherOK = w.(http.Flusher)
		type unwrapper interface{ Unwrap() http.ResponseWriter }
		_, unwrapOK = w.(unwrapper)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login?token=eyJSECRET", strings.NewReader(`{"password":"SECRET_PASSWORD_123"}`))
	req.Header.Set("X-Request-Id", "11111111-1111-4111-8111-111111111111")
	req.Header.Set("Authorization", "Bearer sk-HEADER-SECRET")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !flusherOK || !unwrapOK {
		t.Fatalf("expected Flusher (%v) and Unwrap (%v) preserved", flusherOK, unwrapOK)
	}
	out := buf.String()
	for _, secret := range []string{"eyJSECRET", "SECRET_PASSWORD_123", "sk-HEADER-SECRET"} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked in access log: %s", secret, out)
		}
	}
	if len(sink.entries) != 1 || sink.entries[0].Status != http.StatusCreated || sink.entries[0].Path != "/api/v1/auth/login" || sink.entries[0].UserID != "user-123" {
		t.Fatalf("unexpected sink entries: %+v", sink.entries)
	}
}
