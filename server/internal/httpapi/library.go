package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/provider"
)

func NewLibraryHandler(accounts *identity.Service, library knowledge.Service, models provider.Store) http.Handler {
	h := &libraryHandler{accounts: accounts, library: library, models: models}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("GET /api/v1/me", h.me)
	mux.HandleFunc("POST /api/v1/kbs", h.createKB)
	mux.HandleFunc("GET /api/v1/kbs", h.listKBs)
	mux.HandleFunc("POST /api/v1/kbs/{kb_id}/documents", h.upload)
	mux.HandleFunc("GET /api/v1/documents/{id}", h.document)
	mux.HandleFunc("GET /api/v1/admin/model-configs", h.listModels)
	mux.HandleFunc("PUT /api/v1/admin/model-configs/{kind}", h.putModel)
	mux.HandleFunc("POST /api/v1/admin/model-configs/{kind}/connection-tests", h.probeModel)
	return withRequestID(mux)
}

type libraryHandler struct {
	accounts *identity.Service
	library  knowledge.Service
	models   provider.Store
}

func (h *libraryHandler) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	user, err := h.accounts.Register(r.Context(), req.Email, req.Name, req.Password)
	if err != nil {
		writeLibraryErr(w, r, err)
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
		writeLibraryErr(w, r, err)
		return
	}
	result, err := h.accounts.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *libraryHandler) me(w http.ResponseWriter, r *http.Request) {
	owner, err := h.owner(r)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	user, err := h.accounts.Me(r.Context(), owner)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (h *libraryHandler) createKB(w http.ResponseWriter, r *http.Request) {
	owner, err := h.owner(r)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	kb, err := h.library.CreateKB(r.Context(), owner, req.Name)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, kb)
}

func (h *libraryHandler) listKBs(w http.ResponseWriter, r *http.Request) {
	owner, err := h.owner(r)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	list, err := h.library.ListKBs(r.Context(), owner)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	if list == nil {
		list = []knowledge.KB{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (h *libraryHandler) upload(w http.ResponseWriter, r *http.Request) {
	owner, err := h.owner(r)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeLibraryErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "请上传 PDF 文件", map[string]any{"field": "file"}))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeLibraryErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "请上传 PDF 文件", map[string]any{"field": "file"}))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 51<<20))
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	var authors []string
	if raw := r.FormValue("authors"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &authors); err != nil {
			writeLibraryErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "作者需要是 JSON 数组", map[string]any{"field": "authors"}))
			return
		}
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = strings.TrimSuffix(header.Filename, ".pdf")
	}
	var year *int
	if raw := strings.TrimSpace(r.FormValue("year")); raw != "" {
		n := 0
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			writeLibraryErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "年份不正确", map[string]any{"field": "year"}))
			return
		}
		year = &n
	}
	result, err := h.library.Upload(r.Context(), owner, r.PathValue("kb_id"), knowledge.UploadMeta{
		FileName:       header.Filename,
		Title:          title,
		Authors:        authors,
		Year:           year,
		Venue:          r.FormValue("venue"),
		RelativePath:   r.FormValue("relative_path"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		RequestID:      requestID(r),
	}, data)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	writeJSON(w, result.Status, result.Doc)
}

func (h *libraryHandler) document(w http.ResponseWriter, r *http.Request) {
	owner, err := h.owner(r)
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	doc, err := h.library.GetDoc(r.Context(), owner, r.PathValue("id"))
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *libraryHandler) owner(r *http.Request) (string, error) {
	principal, err := h.principal(r)
	if err != nil {
		return "", err
	}
	return principal.UserID, nil
}

func (h *libraryHandler) principal(r *http.Request) (identity.Principal, error) {
	raw, err := bearer(r)
	if err != nil {
		return identity.Principal{}, err
	}
	return h.accounts.Authenticate(r.Context(), raw)
}

func (h *libraryHandler) admin(r *http.Request) error {
	principal, err := h.principal(r)
	if err != nil {
		return err
	}
	if principal.Role != "admin" {
		return apperr.ErrForbidden
	}
	return nil
}

func (h *libraryHandler) listModels(w http.ResponseWriter, r *http.Request) {
	if err := h.admin(r); err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	items, err := h.models.List(r.Context())
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *libraryHandler) putModel(w http.ResponseWriter, r *http.Request) {
	if err := h.admin(r); err != nil {
		writeLibraryErr(w, r, err)
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
		writeLibraryErr(w, r, err)
		return
	}
	saved, err := h.models.Put(r.Context(), r.PathValue("kind"), provider.PutInput{
		BaseURL: req.BaseURL, ModelName: req.ModelName, APIKey: req.APIKey,
		Dimension: req.Dimension, ConfirmReindex: req.ConfirmReindex,
	})
	if err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (h *libraryHandler) probeModel(w http.ResponseWriter, r *http.Request) {
	if err := h.admin(r); err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	var req struct {
		BaseURL   string `json:"base_url"`
		ModelName string `json:"model_name"`
		APIKey    string `json:"api_key"`
		Dimension int    `json:"dimension"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeLibraryErr(w, r, err)
		return
	}
	probe := h.models.HTTP.Probe(r.Context(), provider.Remote{
		Kind: r.PathValue("kind"), BaseURL: req.BaseURL, Model: req.ModelName,
		APIKey: req.APIKey, Dimension: req.Dimension,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"result": probe.Result, "message": probe.Message, "elapsed_ms": probe.ElapsedMS,
	})
}

func writeLibraryErr(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "INTERNAL"
	message := "服务暂时不可用，请稍后重试"
	details := map[string]any{}
	if app := apperr.As(err); app != nil {
		code = app.Code
		message = app.Message
		if app.Details != nil {
			details = app.Details
		}
		switch app.Code {
		case "UNAUTHENTICATED", "INVALID_CREDENTIALS":
			status = http.StatusUnauthorized
		case "EMAIL_TAKEN", "DUPLICATE_DOCUMENT", "REINDEX_CONFIRMATION_REQUIRED":
			status = http.StatusConflict
		case "FORBIDDEN":
			status = http.StatusForbidden
		case "VALIDATION_ERROR", "PASSWORD_WEAK":
			status = http.StatusUnprocessableEntity
		case "NOT_FOUND":
			status = http.StatusNotFound
		}
	}
	writeJSON(w, status, errorBody{Code: code, Message: message, RequestID: requestID(r), Details: details})
}
