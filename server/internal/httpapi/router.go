package httpapi

import (
	"encoding/json"
	"net/http"

	"yanzhi/server/internal/access"
	"yanzhi/server/internal/account"
	"yanzhi/server/internal/apperr"
)

func (h *handler) registerDomainRoutes(mux *http.ServeMux) {
	if h.kb == nil {
		mux.HandleFunc("GET /api/v1/documents", h.documents)
	} else {
		h.registerKnowledgeRoutes(mux)
		mux.HandleFunc("PATCH /api/v1/kbs/{kb_id}/settings", h.patchKBSettings)
		mux.HandleFunc("GET /api/v1/documents/{document_id}/chunks/{chunk_id}", h.getChunkDetail)
	}
	if h.qa != nil {
		h.registerQARoutes(mux)
	}
}

func (h *handler) authorize(w http.ResponseWriter, r *http.Request, act string) (account.Caller, bool) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return account.Caller{}, false
	}
	if !h.checkGate(w, r, caller, act) {
		return account.Caller{}, false
	}
	return caller, true
}

func (h *handler) patchKBSettings(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpen)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil || raw == nil {
		h.writeErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "请求体不是合法的 JSON 对象", map[string]any{"field": "body"}))
		return
	}
	filtered := make(map[string]json.RawMessage, 3)
	for _, key := range []string{"answer_instruction", "retrieval_threshold", "graph_enabled"} {
		if v, exists := raw[key]; exists {
			filtered[key] = v
		}
	}
	kb, err := h.kb.PatchKB(r.Context(), caller.User.ID, r.PathValue("kb_id"), filtered)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, kb)
}

func (h *handler) getChunkDetail(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpen)
	if !ok {
		return
	}
	detail, err := h.kb.ChunkDetail(r.Context(), caller.User.ID, r.PathValue("document_id"), r.PathValue("chunk_id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"text":        detail.Text,
		"page_start":  detail.PageStart,
		"parent_text": detail.ParentText,
	})
}
