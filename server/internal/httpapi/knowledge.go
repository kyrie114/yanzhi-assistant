package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"yanzhi/server/internal/access"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/knowledge"
)

func (a *App) listKBs(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActList); err != nil {
		writeErr(w, r, err)
		return
	}
	items, err := a.Knowledge.ListKBs(r.Context(), p.UserID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) createKB(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActList); err != nil {
		writeErr(w, r, err)
		return
	}
	if p.Role == "admin" {
		writeErr(w, r, apperr.ErrForbidden)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	kb, err := a.Knowledge.CreateKB(r.Context(), p.UserID, req.Name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, kb)
}

func (a *App) getKB(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	kb, err := a.Knowledge.GetKB(r.Context(), p.UserID, r.PathValue("kb_id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, kb)
}

func (a *App) patchKBSettings(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		writeErr(w, r, err)
		return
	}
	kb, err := a.Knowledge.PatchKB(r.Context(), p.UserID, r.PathValue("kb_id"), raw)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, kb)
}

func (a *App) uploadDocument(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActUpload); err != nil {
		writeErr(w, r, err)
		return
	}
	// 多留 1 MB 给 multipart 头和表单字段；50 MB 的判定在服务层按文件字节数做。
	r.Body = http.MaxBytesReader(w, r.Body, apperr.MaxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErr(w, r, apperr.ErrFileTooLarge)
			return
		}
		writeErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "表单格式不正确", map[string]any{"body": "表单格式不正确"}))
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "缺少 file 字段", map[string]any{"field": "file"}))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if int64(len(data)) > apperr.MaxUploadBytes {
		writeErr(w, r, apperr.ErrFileTooLarge)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		base := filepath.Base(header.Filename)
		title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	var authors []string
	if raw := strings.TrimSpace(r.FormValue("authors")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &authors); err != nil {
			writeErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "authors 需要是 JSON 数组", map[string]any{"field": "authors"}))
			return
		}
	}
	var year *int
	if raw := strings.TrimSpace(r.FormValue("year")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeErr(w, r, apperr.WithDetails("VALIDATION_ERROR", "year 需要是整数", map[string]any{"field": "year"}))
			return
		}
		year = &n
	}
	meta := knowledge.UploadMeta{
		FileName:       header.Filename,
		Title:          title,
		Authors:        authors,
		Year:           year,
		Venue:          strings.TrimSpace(r.FormValue("venue")),
		RelativePath:   strings.TrimSpace(r.FormValue("relative_path")),
		IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
		RequestID:      requestID(r),
	}
	result, err := a.Knowledge.Upload(r.Context(), p.UserID, r.PathValue("kb_id"), meta, data)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, result.Status, result.Doc)
}

func (a *App) listDocuments(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActList); err != nil {
		writeErr(w, r, err)
		return
	}
	page := queryInt(r, "page", 1)
	size := queryInt(r, "page_size", 20)
	items, total, err := a.Knowledge.ListDocs(r.Context(), p.UserID, r.URL.Query().Get("kb_id"),
		r.URL.Query().Get("q"), r.URL.Query()["tag_id"], page, size)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "page": page, "page_size": size, "total": total})
}

func (a *App) getDocument(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	doc, err := a.Knowledge.GetDoc(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (a *App) patchDocument(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		writeErr(w, r, err)
		return
	}
	doc, err := a.Knowledge.PatchDoc(r.Context(), p.UserID, r.PathValue("id"), raw)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (a *App) deleteDocument(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.Knowledge.Delete(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (a *App) downloadFile(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	title, data, err := a.Knowledge.File(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": title + ".pdf"}))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *App) retryDocument(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.Knowledge.Retry(r.Context(), p.UserID, r.PathValue("id"), requestID(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	doc, err := a.Knowledge.GetDoc(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (a *App) documentIngest(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	progress, err := a.Knowledge.Ingest(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, progress)
}

func (a *App) listChunks(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	page := queryInt(r, "page", 1)
	size := queryInt(r, "page_size", 20)
	items, total, err := a.Knowledge.Chunks(r.Context(), p.UserID, r.PathValue("id"), page, size)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "page": page, "page_size": size, "total": total})
}

func (a *App) getChunk(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	chunk, err := a.Knowledge.Chunk(r.Context(), p.UserID, r.PathValue("id"), r.PathValue("chunk_id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, chunk)
}

func (a *App) listTags(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActList); err != nil {
		writeErr(w, r, err)
		return
	}
	items, err := a.Knowledge.ListTags(r.Context(), p.UserID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) createTag(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if p.Role == "admin" {
		writeErr(w, r, apperr.ErrForbidden)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	tag, err := a.Knowledge.CreateTag(r.Context(), p.UserID, req.Name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, tag)
}

func (a *App) patchTag(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	tag, err := a.Knowledge.PatchTag(r.Context(), p.UserID, r.PathValue("id"), req.Name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tag)
}

func (a *App) deleteTag(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.Knowledge.DeleteTag(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (a *App) putDocumentTags(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		TagIDs []string `json:"tag_ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	doc, err := a.Knowledge.PutTags(r.Context(), p.UserID, r.PathValue("id"), req.TagIDs)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func queryInt(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}
