package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"yanzhi/server/internal/access"
	"yanzhi/server/internal/account"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/knowledge"
)

type KnowledgeBackend interface {
	ListKBs(ctx context.Context, owner string) ([]knowledge.KB, error)
	CreateKB(ctx context.Context, owner, name string) (knowledge.KB, error)
	GetKB(ctx context.Context, owner, id string) (knowledge.KB, error)
	PatchKB(ctx context.Context, owner, id string, raw map[string]json.RawMessage) (knowledge.KB, error)
	Upload(ctx context.Context, owner, kbID string, meta knowledge.UploadMeta, data []byte) (knowledge.UploadResult, error)
	ListDocs(ctx context.Context, owner, kbID, q string, tagIDs []string, page, size int) ([]knowledge.Doc, int, error)
	GetDoc(ctx context.Context, owner, id string) (knowledge.Doc, error)
	PatchDoc(ctx context.Context, owner, id string, raw map[string]json.RawMessage) (knowledge.Doc, error)
	Delete(ctx context.Context, owner, id string) error
	File(ctx context.Context, owner, id string) (string, []byte, error)
	ChunkDetail(ctx context.Context, owner, docID, chunkID string) (knowledge.ChunkDetail, error)
	ListTags(ctx context.Context, owner string) ([]knowledge.Tag, error)
	CreateTag(ctx context.Context, owner, name string) (knowledge.Tag, error)
	PatchTag(ctx context.Context, owner, id, name string) (knowledge.Tag, error)
	DeleteTag(ctx context.Context, owner, id string) error
	PutTags(ctx context.Context, owner, docID string, tagIDs []string) (knowledge.Doc, error)
}

type DocumentRetry interface {
	Retry(ctx context.Context, owner, id, requestID string) error
}

var _ DocumentRetry = knowledge.Service{}

func WithDocumentRetry(r DocumentRetry) Option {
	return func(h *handler) {
		h.docRetry = r
	}
}

func (h *handler) registerKnowledgeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/kbs", h.listKBs)
	mux.HandleFunc("POST /api/v1/kbs", h.createKB)
	mux.HandleFunc("GET /api/v1/kbs/{kb_id}", h.getKB)
	mux.HandleFunc("POST /api/v1/kbs/{kb_id}/documents", h.uploadDocument)
	mux.HandleFunc("GET /api/v1/documents", h.listDocuments)
	mux.HandleFunc("GET /api/v1/documents/{id}", h.getDocument)
	mux.HandleFunc("PATCH /api/v1/documents/{id}", h.patchDocument)
	mux.HandleFunc("DELETE /api/v1/documents/{id}", h.deleteDocument)
	mux.HandleFunc("GET /api/v1/documents/{id}/file", h.downloadDocumentFile)
	mux.HandleFunc("GET /api/v1/tags", h.listTags)
	mux.HandleFunc("POST /api/v1/tags", h.createTag)
	mux.HandleFunc("PATCH /api/v1/tags/{id}", h.patchTag)
	mux.HandleFunc("DELETE /api/v1/tags/{id}", h.deleteTag)
	mux.HandleFunc("PUT /api/v1/documents/{id}/tags", h.putDocumentTags)
}

func (h *handler) checkGate(w http.ResponseWriter, r *http.Request, caller account.Caller, act string) bool {
	verified := caller.User.EmailVerifiedAt != nil
	dec := access.Gate(&access.Principal{Role: caller.User.Role, Verified: verified}, act, h.restrictEmail)
	switch dec {
	case access.Allow:
		return true
	case access.Unauthenticated:
		h.writeErr(w, r, apperr.ErrUnauthenticated)
		return false
	case access.Forbidden:
		h.writeErr(w, r, apperr.ErrForbidden)
		return false
	case access.EmailUnverified:
		h.writeErr(w, r, apperr.ErrEmailUnverified)
		return false
	case access.NotFound:
		h.writeErr(w, r, apperr.ErrNotFound)
		return false
	default:
		h.writeErr(w, r, apperr.ErrForbidden)
		return false
	}
}

func (h *handler) listKBs(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActList) {
		return
	}
	kbs, err := h.kb.ListKBs(r.Context(), caller.User.ID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if kbs == nil {
		kbs = []knowledge.KB{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": kbs})
}

func (h *handler) createKB(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActCreate) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, r, err)
		return
	}
	kb, err := h.kb.CreateKB(r.Context(), caller.User.ID, req.Name)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, kb)
}

func (h *handler) getKB(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActOpen) {
		return
	}
	kbID := r.PathValue("kb_id")
	kb, err := h.kb.GetKB(r.Context(), caller.User.ID, kbID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, kb)
}

func (h *handler) uploadDocument(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActUpload) {
		return
	}
	kbID := r.PathValue("kb_id")

	// Verify KB ownership before streaming multipart
	if _, err := h.kb.GetKB(r.Context(), caller.User.ID, kbID); err != nil {
		h.writeErr(w, r, apperr.ErrNotFound)
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(2 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(2 * time.Minute))

	mr, err := r.MultipartReader()
	if err != nil {
		h.writeErr(w, r, &account.ValidationError{Details: map[string]string{"file": "请求需要是 multipart/form-data"}})
		return
	}

	var fileData []byte
	var fileName string
	var title, venue, relPath string
	var authors []string
	var year *int

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			h.writeErr(w, r, &account.ValidationError{Details: map[string]string{"body": "无法读取上传内容"}})
			return
		}
		formName := part.FormName()
		if formName == "file" {
			fileName = part.FileName()
			var buf bytes.Buffer
			lr := io.LimitReader(part, apperr.MaxUploadBytes+1)
			n, err := buf.ReadFrom(lr)
			if err != nil && !errors.Is(err, io.EOF) {
				h.writeErr(w, r, &account.ValidationError{Details: map[string]string{"file": "文件读取失败"}})
				return
			}
			if n > apperr.MaxUploadBytes {
				h.writeErr(w, r, apperr.ErrFileTooLarge)
				return
			}
			fileData = buf.Bytes()
		} else {
			partBytes, _ := io.ReadAll(io.LimitReader(part, 1<<20))
			val := string(partBytes)
			switch formName {
			case "title":
				title = val
			case "venue":
				venue = val
			case "relative_path":
				relPath = val
			case "authors":
				val = strings.TrimSpace(val)
				if val != "" {
					var arr []string
					if err := json.Unmarshal([]byte(val), &arr); err != nil {
						h.writeErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "作者需要是 JSON 数组字符串", map[string]any{"field": "authors"}))
						return
					}
					authors = arr
				}
			case "year":
				val = strings.TrimSpace(val)
				if val != "" {
					y, err := strconv.Atoi(val)
					if err != nil || y < 0 || y > 3000 {
						h.writeErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "年份不正确", map[string]any{"field": "year"}))
						return
					}
					year = &y
				}
			}
		}
	}

	if len(fileData) == 0 && fileName == "" {
		h.writeErr(w, r, &account.ValidationError{Details: map[string]string{"file": "文件不能为空"}})
		return
	}

	meta := knowledge.UploadMeta{
		FileName:       fileName,
		Title:          title,
		Authors:        authors,
		Year:           year,
		Venue:          venue,
		RelativePath:   relPath,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		RequestID:      requestID(r),
	}

	res, err := h.kb.Upload(r.Context(), caller.User.ID, kbID, meta, fileData)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, res.Status, res.Doc)
}

func (h *handler) listDocuments(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActList) {
		return
	}

	q := r.URL.Query().Get("q")
	kbID := r.URL.Query().Get("kb_id")
	tagIDs := r.URL.Query()["tag_id"]
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	pageSize := 20
	if s, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && s > 0 && s <= 100 {
		pageSize = s
	}

	docs, total, err := h.kb.ListDocs(r.Context(), caller.User.ID, kbID, q, tagIDs, page, pageSize)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if docs == nil {
		docs = []knowledge.Doc{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     docs,
		"page":      page,
		"page_size": pageSize,
		"total":     total,
	})
}

func (h *handler) getDocument(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActOpen) {
		return
	}
	id := r.PathValue("id")
	doc, err := h.kb.GetDoc(r.Context(), caller.User.ID, id)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *handler) patchDocument(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActOpen) {
		return
	}
	id := r.PathValue("id")
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		h.writeErr(w, r, err)
		return
	}
	doc, err := h.kb.PatchDoc(r.Context(), caller.User.ID, id, raw)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *handler) deleteDocument(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActOpen) {
		return
	}
	id := r.PathValue("id")
	if err := h.kb.Delete(r.Context(), caller.User.ID, id); err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handler) downloadDocumentFile(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActOpen) {
		return
	}
	id := r.PathValue("id")
	title, data, err := h.kb.File(r.Context(), caller.User.ID, id)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Minute))
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", contentDisposition(title))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *handler) listTags(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActList) {
		return
	}
	tags, err := h.kb.ListTags(r.Context(), caller.User.ID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if tags == nil {
		tags = []knowledge.Tag{}
	}
	writeJSON(w, http.StatusOK, tags)
}

func (h *handler) createTag(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActCreate) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, r, err)
		return
	}
	tag, err := h.kb.CreateTag(r.Context(), caller.User.ID, req.Name)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, tag)
}

func (h *handler) patchTag(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActOpen) {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, r, err)
		return
	}
	tag, err := h.kb.PatchTag(r.Context(), caller.User.ID, id, req.Name)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tag)
}

func (h *handler) deleteTag(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActOpen) {
		return
	}
	id := r.PathValue("id")
	if err := h.kb.DeleteTag(r.Context(), caller.User.ID, id); err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handler) putDocumentTags(w http.ResponseWriter, r *http.Request) {
	caller, err := h.caller(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if !h.checkGate(w, r, caller, access.ActOpen) {
		return
	}
	id := r.PathValue("id")
	var req struct {
		TagIDs []string `json:"tag_ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		h.writeErr(w, r, err)
		return
	}
	if req.TagIDs == nil {
		req.TagIDs = []string{}
	}
	doc, err := h.kb.PutTags(r.Context(), caller.User.ID, id, req.TagIDs)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *handler) retryDocument(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpen)
	if !ok {
		return
	}
	if err := h.docRetry.Retry(r.Context(), caller.User.ID, r.PathValue("id"), requestID(r)); err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func contentDisposition(title string) string {
	clean := strings.Map(func(r rune) rune {
		if r == '"' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, title)
	clean = strings.TrimSpace(clean)
	if clean == "" {
		clean = "document"
	}
	if !strings.HasSuffix(strings.ToLower(clean), ".pdf") {
		clean += ".pdf"
	}
	encoded := url.PathEscape(clean)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, clean, encoded)
}
