package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yanzhi/server/internal/access"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/reading"
)

// ReadingBackend 定义按页对照阅读与流式翻译路由所需的后端契约。
type ReadingBackend interface {
	GetPage(ctx context.Context, owner, docID string, page int) (reading.PageData, error)
	PrepareTranslation(ctx context.Context, req reading.TranslateRequest) (*reading.TranslationPlan, string, error)
	StreamTranslation(ctx context.Context, plan *reading.TranslationPlan, emit func(event string, payload any) error) error
}

// WithReading 为 httpapi 装配按页对照阅读能力。
func WithReading(r ReadingBackend) Option {
	return func(h *handler) {
		h.reading = r
	}
}

func (h *handler) registerReadingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/pages/{page}", h.getDocumentPage)
	mux.HandleFunc("POST /api/v1/documents/{id}/pages/{page}/translation", h.translateDocumentPage)
}

func parsePageParam(raw string) (int, bool) {
	page, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return page, true
}

func (h *handler) getDocumentPage(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpen)
	if !ok {
		return
	}
	page, valid := parsePageParam(r.PathValue("page"))
	if !valid {
		h.writeErr(w, r, apperr.ErrPageOutOfRange)
		return
	}
	data, err := h.reading.GetPage(r.Context(), caller.User.ID, r.PathValue("id"), page)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (h *handler) translateDocumentPage(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActTranslate)
	if !ok {
		return
	}
	page, valid := parsePageParam(r.PathValue("page"))
	if !valid {
		h.writeErr(w, r, apperr.ErrPageOutOfRange)
		return
	}
	plan, emptyMsg, err := h.reading.PrepareTranslation(r.Context(), reading.TranslateRequest{
		Owner:     caller.User.ID,
		DocID:     r.PathValue("id"),
		Page:      page,
		RequestID: requestID(r),
	})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if emptyMsg != "" {
		writeJSON(w, http.StatusOK, map[string]string{"message": emptyMsg})
		return
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	var seq int64
	_ = h.reading.StreamTranslation(r.Context(), plan, func(event string, payload any) error {
		seq++
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			raw = []byte("{}")
		}
		if _, writeErr := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, event, raw); writeErr != nil {
			return writeErr
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
}
