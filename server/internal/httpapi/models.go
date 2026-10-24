package httpapi

import (
	"net/http"
	"strings"

	"yanzhi/server/internal/access"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/provider"
)

func (a *App) requireAdmin(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return p, false
	}
	if err := a.guard(p, access.ActAdmin); err != nil {
		writeErr(w, r, err)
		return p, false
	}
	return p, true
}

func (a *App) listModelConfigs(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	items, err := a.Models.List(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type modelConfigBody struct {
	BaseURL        string  `json:"base_url"`
	ModelName      string  `json:"model_name"`
	APIKey         *string `json:"api_key"`
	Dimension      *int    `json:"dimension"`
	ConfirmReindex bool    `json:"confirm_reindex"`
}

func (a *App) putModelConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var req modelConfigBody
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	saved, err := a.Models.Put(r.Context(), r.PathValue("kind"), provider.PutInput{
		BaseURL: req.BaseURL, ModelName: req.ModelName, APIKey: req.APIKey,
		Dimension: req.Dimension, ConfirmReindex: req.ConfirmReindex,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (a *App) testModelConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var req modelConfigBody
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	remote, err := a.Models.ResolveKey(r.Context(), r.PathValue("kind"), req.BaseURL, req.ModelName, req.APIKey, req.Dimension)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if strings.TrimSpace(remote.BaseURL) == "" || strings.TrimSpace(remote.Model) == "" {
		writeErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "地址和模型名不能为空", map[string]any{"field": "base_url"}))
		return
	}
	probe := provider.Client{}.Probe(r.Context(), remote)
	writeJSON(w, http.StatusOK, map[string]any{
		"result": probe.Result, "message": probe.Message, "elapsed_ms": probe.ElapsedMS,
	})
}
