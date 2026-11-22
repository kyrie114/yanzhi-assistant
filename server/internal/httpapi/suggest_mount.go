package httpapi

import (
	"context"
	"errors"
	"net/http"

	"yanzhi/server/internal/access"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/suggest"
)

type suggestionAPI interface {
	List(ctx context.Context, caller, scopeType, scopeID, asked string) (suggest.Result, error)
}

func WithSuggestions(api suggestionAPI) Option {
	return func(h *handler) { h.suggest = api }
}

type libraryCatalog struct {
	knowledge.Service
}

func (c libraryCatalog) Document(ctx context.Context, owner, id string) (suggest.SourceDoc, error) {
	return c.Service.SuggestionDocument(ctx, owner, id)
}

func (c libraryCatalog) Library(ctx context.Context, owner, id string) ([]suggest.SourceDoc, error) {
	return c.Service.SuggestionLibrary(ctx, owner, id)
}

func NewLiveSuggestions(mem *suggest.Memory, library knowledge.Service) suggest.Live {
	return suggest.Live{Memory: mem, Catalog: libraryCatalog{Service: library}}
}

func (h *handler) registerSuggestionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/suggested-questions", h.documentSuggestions)
	mux.HandleFunc("GET /api/v1/kbs/{kb_id}/suggested-questions", h.kbSuggestions)
}

func (h *handler) documentSuggestions(w http.ResponseWriter, r *http.Request) {
	h.suggestions(w, r, "document", r.PathValue("id"))
}

func (h *handler) kbSuggestions(w http.ResponseWriter, r *http.Request) {
	h.suggestions(w, r, "kb", r.PathValue("kb_id"))
}

func (h *handler) suggestions(w http.ResponseWriter, r *http.Request, scope, id string) {
	caller, ok := h.authorize(w, r, access.ActOpen)
	if !ok {
		return
	}
	result, err := h.suggest.List(r.Context(), caller.User.ID, scope, id, r.URL.Query().Get("asked"))
	if err != nil {
		if errors.Is(err, suggest.ErrNotFound) {
			h.writeErr(w, r, apperr.ErrNotFound)
			return
		}
		h.writeErr(w, r, err)
		return
	}
	if result.Questions == nil {
		result.Questions = []string{}
	}
	writeJSON(w, http.StatusOK, result)
}
