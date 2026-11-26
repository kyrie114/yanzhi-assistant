package httpapi

import (
	"context"
	"net/http"
	"time"

	"yanzhi/server/internal/account"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/provider"
)

func NewLibraryHandler(accounts *identity.Service, library knowledge.Service, models provider.Store, opts ...Option) http.Handler {
	domain := &handler{
		kb:         library,
		graphRetry: library,
		authFn: func(r *http.Request) (account.Caller, error) {
			raw, err := bearer(r)
			if err != nil {
				return account.Caller{}, err
			}
			p, err := accounts.Authenticate(r.Context(), raw)
			if err != nil {
				return account.Caller{}, err
			}
			var verifiedAt *time.Time
			if p.Verified {
				now := time.Now().UTC()
				verifiedAt = &now
			}
			return account.Caller{
				User: account.User{
					ID:              p.UserID,
					Role:            p.Role,
					EmailVerifiedAt: verifiedAt,
				},
			}, nil
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(domain)
		}
	}

	h := &libraryHandler{
		accounts: accounts,
		models:   models,
		domain:   domain,
	}
	mux := http.NewServeMux()
	domain.registerHealthRoute(mux)
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.HandleFunc("POST /api/v1/auth/password", h.changePassword)
	mux.HandleFunc("GET /api/v1/me", h.me)
	domain.registerDomainRoutes(mux)
	mux.HandleFunc("GET /api/v1/admin/model-configs", h.listModels)
	mux.HandleFunc("PUT /api/v1/admin/model-configs/{kind}", h.putModel)
	mux.HandleFunc("POST /api/v1/admin/model-configs/{kind}/connection-tests", h.probeModel)
	return withRequestID(mux)
}

type modelStore interface {
	List(ctx context.Context) ([]provider.Saved, error)
	Put(ctx context.Context, kind string, in provider.PutInput) (provider.Saved, error)
	ResolveKey(ctx context.Context, kind, base, model string, key *string, dim *int) (provider.Remote, error)
	Probe(ctx context.Context, remote provider.Remote) provider.Probe
}

type libraryHandler struct {
	accounts *identity.Service
	models   modelStore
	domain   *handler
	authFn   func(*http.Request) (identity.Principal, error)
}

func (h *libraryHandler) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	user, err := h.accounts.Register(r.Context(), req.Email, req.Name, req.Password)
	if err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (h *libraryHandler) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	result, err := h.accounts.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *libraryHandler) me(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	user, err := h.accounts.Me(r.Context(), p.UserID)
	if err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (h *libraryHandler) logout(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	if err := h.accounts.Logout(r.Context(), p); err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *libraryHandler) changePassword(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	if err := h.accounts.ChangePassword(r.Context(), p.UserID, req.OldPassword, req.NewPassword); err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *libraryHandler) principal(r *http.Request) (identity.Principal, error) {
	if h.authFn != nil {
		return h.authFn(r)
	}
	raw, err := bearer(r)
	if err != nil {
		return identity.Principal{}, err
	}
	return h.accounts.Authenticate(r.Context(), raw)
}

func (h *libraryHandler) admin(r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	if p.Role != "admin" {
		return apperr.ErrForbidden
	}
	return nil
}

func (h *libraryHandler) listModels(w http.ResponseWriter, r *http.Request) {
	if err := h.admin(r); err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	items, err := h.models.List(r.Context())
	if err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *libraryHandler) putModel(w http.ResponseWriter, r *http.Request) {
	if err := h.admin(r); err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	var req struct {
		BaseURL        string  `json:"base_url"`
		ModelName      string  `json:"model_name"`
		APIKey         *string `json:"api_key"`
		Dimension      *int    `json:"dimension"`
		ConfirmReindex bool    `json:"confirm_reindex"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	saved, err := h.models.Put(r.Context(), r.PathValue("kind"), provider.PutInput{
		BaseURL: req.BaseURL, ModelName: req.ModelName, APIKey: req.APIKey,
		Dimension: req.Dimension, ConfirmReindex: req.ConfirmReindex,
	})
	if err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (h *libraryHandler) probeModel(w http.ResponseWriter, r *http.Request) {
	if err := h.admin(r); err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	var req struct {
		BaseURL   string `json:"base_url"`
		ModelName string `json:"model_name"`
		APIKey    string `json:"api_key"`
		Dimension int    `json:"dimension"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	remote, err := h.models.ResolveKey(r.Context(), r.PathValue("kind"), req.BaseURL, req.ModelName, &req.APIKey, &req.Dimension)
	if err != nil {
		h.domain.writeErr(w, r, err)
		return
	}
	probe := h.models.Probe(r.Context(), remote)
	writeJSON(w, http.StatusOK, map[string]any{
		"result": probe.Result, "message": probe.Message, "elapsed_ms": probe.ElapsedMS,
	})
}
