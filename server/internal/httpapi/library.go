package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/knowledge"
)

func NewLibraryHandler(accounts *identity.Service, library knowledge.Service) http.Handler {
	h := &libraryHandler{accounts: accounts, library: library}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("GET /api/v1/me", h.me)
	mux.HandleFunc("POST /api/v1/kbs", h.createKB)
	mux.HandleFunc("GET /api/v1/kbs", h.listKBs)
	mux.HandleFunc("POST /api/v1/kbs/{kb_id}/documents", h.upload)
	mux.HandleFunc("GET /api/v1/documents/{id}", h.document)
	return withRequestID(mux)
}

type libraryHandler struct {
	accounts *identity.Service
	library  knowledge.Service
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
	raw, err := bearer(r)
	if err != nil {
		return "", err
	}
	principal, err := h.accounts.Authenticate(r.Context(), raw)
	if err != nil {
		return "", err
	}
	return principal.UserID, nil
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
		case "EMAIL_TAKEN", "DUPLICATE_DOCUMENT":
			status = http.StatusConflict
		case "VALIDATION_ERROR", "PASSWORD_WEAK":
			status = http.StatusUnprocessableEntity
		case "NOT_FOUND":
			status = http.StatusNotFound
		}
	}
	writeJSON(w, status, errorBody{Code: code, Message: message, RequestID: requestID(r), Details: details})
}
