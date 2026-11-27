// Package applog 提供带敏感字段脱敏的 JSON 结构化日志与访问日志中间件（NFR-07）。
package applog

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"yanzhi/server/internal/platform/db"
)

var shanghai = time.FixedZone("Asia/Shanghai", 8*3600)

var sensitiveKeyFragments = []string{
	"password",
	"secret",
	"token",
	"api_key",
	"apikey",
	"authorization",
	"prompt",
	"answer",
	"content",
	"chunk_text",
	"body",
	"cookie",
	"credential",
}

var sensitiveValuePrefixes = []string{
	"Bearer ",
	"sk-",
	"yzk_",
	"eyJ",
}

// New 创建输出到 w 的 JSON slog.Logger，自动按 Asia/Shanghai 输出时间并对敏感属性脱敏。
func New(w io.Writer) *slog.Logger {
	if w == nil {
		w = io.Discard
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: replaceSensitive,
	}))
}

func replaceSensitive(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		if t, ok := a.Value.Any().(time.Time); ok {
			return slog.String(slog.TimeKey, t.In(shanghai).Format(time.RFC3339))
		}
	}
	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, "[REDACTED]")
	}
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, scrubValue(a.Value.String()))
	}
	return a
}

func isSensitiveKey(key string) bool {
	lk := strings.ToLower(key)
	for _, frag := range sensitiveKeyFragments {
		if strings.Contains(lk, frag) {
			return true
		}
	}
	return false
}

func scrubValue(s string) string {
	for _, prefix := range sensitiveValuePrefixes {
		if strings.Contains(s, prefix) {
			return "[REDACTED]"
		}
	}
	return s
}

// RequestLogEntry 表示单次 HTTP 请求的结构化元信息（绝不含请求体、查询参数或鉴权头）。
type RequestLogEntry struct {
	RequestID string
	UserID    string
	Method    string
	Path      string
	Status    int
	ElapsedMS int
	CreatedAt time.Time
}

// RequestLogSink 负责将访问日志元数据持久化（如数据库模式下的 request_logs 表）。
type RequestLogSink interface {
	RecordRequest(ctx context.Context, entry RequestLogEntry)
}

// PGRequestLogSink 将脱敏后的请求元数据写入 PostgreSQL request_logs 表。
type PGRequestLogSink struct {
	Pool *pgxpool.Pool
}

// RecordRequest 以无取消短超时方式写入 request_logs，避免影响主请求返回与 SSE 流。
func (s PGRequestLogSink) RecordRequest(ctx context.Context, entry RequestLogEntry) {
	if s.Pool == nil {
		return
	}
	reqUUID, ok := db.UUID(entry.RequestID)
	if !ok {
		return
	}
	var userVal any
	if u, ok := db.UUID(entry.UserID); ok {
		userVal = u
	}
	ts := entry.CreatedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = s.Pool.Exec(logCtx, `
		INSERT INTO request_logs (id, request_id, user_id, method, path, status, elapsed_ms, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		db.MustUUID(db.NewID()), reqUUID, userVal, entry.Method, entry.Path, entry.Status, entry.ElapsedMS, ts)
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// AccessMiddleware 记录每次 HTTP 请求的脱敏 JSON 访问日志并可选写入 sink。
func AccessMiddleware(logger *slog.Logger, sink RequestLogSink, reqIDFn func(*http.Request) string, userIDFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if logger == nil && sink == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			elapsed := int(time.Since(start).Milliseconds())
			reqID := ""
			if reqIDFn != nil {
				reqID = reqIDFn(r)
			}
			userID := ""
			if userIDFn != nil {
				userID = userIDFn(r)
			}
			if logger != nil {
				attrs := []any{
					"request_id", reqID,
					"method", r.Method,
					"path", r.URL.Path,
					"status", sw.status,
					"latency_ms", elapsed,
				}
				if userID != "" {
					attrs = append(attrs, "user_id", userID)
				}
				logger.Info("http_request", attrs...)
			}
			if sink != nil {
				sink.RecordRequest(r.Context(), RequestLogEntry{
					RequestID: reqID,
					UserID:    userID,
					Method:    r.Method,
					Path:      r.URL.Path,
					Status:    sw.status,
					ElapsedMS: elapsed,
					CreatedAt: time.Now().UTC(),
				})
			}
		})
	}
}
