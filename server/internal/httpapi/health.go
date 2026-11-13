package httpapi

import (
	"context"
	"net/http"
)

func WithHealthProbe(probe func(context.Context) bool) Option {
	return func(h *handler) {
		h.healthProbe = probe
	}
}

func (h *handler) registerHealthRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/health", h.health)
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	docreaderStatus := "unavailable"
	if h.healthProbe != nil && h.healthProbe(r.Context()) {
		docreaderStatus = "serving"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "ok",
		"docreader": docreaderStatus,
	})
}
