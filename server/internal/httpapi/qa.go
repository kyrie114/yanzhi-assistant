package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yanzhi/server/internal/access"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/qa"
)

// QABackend 抽象问答服务对 HTTP 层暴露的会话与流式执行接口，由 *qa.Service 满足。
type QABackend interface {
	CreateSession(ctx context.Context, owner, title string) (qa.Session, error)
	ListSessions(ctx context.Context, owner string, page, size int) ([]qa.Session, int, error)
	GetSession(ctx context.Context, owner, id string) (qa.Session, error)
	PatchSession(ctx context.Context, owner, id, title string) (qa.Session, error)
	DeleteSession(ctx context.Context, owner, id string) error
	StartTurn(ctx context.Context, owner, sessionID, reqID string, in qa.AskInput) (qa.TurnHandle, error)
	StopSession(ctx context.Context, owner, sessionID string) error
	ResumeTurn(ctx context.Context, owner, sessionID, turnID string, afterSeq int64) (*qa.StreamSubscription, error)
}

var _ QABackend = (*qa.Service)(nil)

func WithQA(q QABackend) Option {
	return func(h *handler) {
		h.qa = q
	}
}

func (h *handler) registerQARoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/qa/sessions", h.createQASession)
	mux.HandleFunc("GET /api/v1/qa/sessions", h.listQASessions)
	mux.HandleFunc("GET /api/v1/qa/sessions/{id}", h.getQASession)
	mux.HandleFunc("PATCH /api/v1/qa/sessions/{id}", h.patchQASession)
	mux.HandleFunc("DELETE /api/v1/qa/sessions/{id}", h.deleteQASession)
	mux.HandleFunc("POST /api/v1/qa/sessions/{id}/turns", h.startQATurn)
	mux.HandleFunc("POST /api/v1/qa/sessions/{id}/stop", h.stopQASession)
	mux.HandleFunc("GET /api/v1/qa/sessions/{id}/turns/{turn_id}/stream", h.resumeQATurnStream)
}

func (h *handler) createQASession(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActCreate)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			h.writeErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "请求体不是合法的 JSON", map[string]any{"field": "body"}))
			return
		}
	}
	sess, err := h.qa.CreateSession(r.Context(), caller.User.ID, req.Title)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (h *handler) listQASessions(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActList)
	if !ok {
		return
	}
	page, pageSize, err := parseQAPageParams(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items, total, err := h.qa.ListSessions(r.Context(), caller.User.ID, page, pageSize)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if items == nil {
		items = []qa.Session{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"page":      page,
		"page_size": pageSize,
		"total":     total,
	})
}

func (h *handler) getQASession(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpen)
	if !ok {
		return
	}
	sess, err := h.qa.GetSession(r.Context(), caller.User.ID, r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (h *handler) patchQASession(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpen)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, r, err)
		return
	}
	sess, err := h.qa.PatchSession(r.Context(), caller.User.ID, r.PathValue("id"), req.Title)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (h *handler) deleteQASession(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpen)
	if !ok {
		return
	}
	if err := h.qa.DeleteSession(r.Context(), caller.User.ID, r.PathValue("id")); err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handler) startQATurn(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActAsk)
	if !ok {
		return
	}
	var in qa.AskInput
	if err := decodeJSON(r, &in); err != nil {
		h.writeErr(w, r, err)
		return
	}
	handle, err := h.qa.StartTurn(r.Context(), caller.User.ID, r.PathValue("id"), requestID(r), in)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeSSEStream(w, r, handle.TurnID, handle.Stream)
}

func (h *handler) stopQASession(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpen)
	if !ok {
		return
	}
	if err := h.qa.StopSession(r.Context(), caller.User.ID, r.PathValue("id")); err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": true})
}

func (h *handler) resumeQATurnStream(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpen)
	if !ok {
		return
	}
	afterStr := strings.TrimSpace(r.URL.Query().Get("after"))
	if afterStr == "" {
		afterStr = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	var afterSeq int64
	if afterStr != "" {
		n, err := strconv.ParseInt(afterStr, 10, 64)
		if err != nil || n < 0 {
			h.writeErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "after 参数必须是非负整数", map[string]any{"field": "after"}))
			return
		}
		afterSeq = n
	}
	sub, err := h.qa.ResumeTurn(r.Context(), caller.User.ID, r.PathValue("id"), r.PathValue("turn_id"), afterSeq)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeSSEStream(w, r, r.PathValue("turn_id"), sub)
}

func writeSSEStream(w http.ResponseWriter, r *http.Request, turnID string, sub *qa.StreamSubscription) {
	// 解除 net/http.Server 默认 WriteTimeout 对长时 SSE 流式响应的截断。
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if turnID != "" {
		w.Header().Set("X-Turn-Id", turnID)
	}
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	defer sub.Close()
	for {
		evt, ok := sub.Next(r.Context())
		if !ok {
			return
		}
		_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", evt.Seq, evt.Name, evt.Data)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func parseQAPageParams(r *http.Request) (int, int, error) {
	page, pageSize := 1, 20
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return 0, 0, apperr.WithDetails("VALIDATION_ERROR", "page 参数必须是正整数", map[string]any{"field": "page"})
		}
		page = n
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("page_size")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return 0, 0, apperr.WithDetails("VALIDATION_ERROR", "page_size 参数需要在 1 到 100 之间", map[string]any{"field": "page_size"})
		}
		pageSize = n
	}
	return page, pageSize, nil
}
