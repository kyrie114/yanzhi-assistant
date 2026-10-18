package httpapi

import (
	"errors"
	"net/http"

	"yanzhi/server/internal/suggest"
)

type questionLister interface {
	List(caller, scopeType, scopeID, asked string) (suggest.Result, error)
}

type suggestHandler struct {
	list   questionLister
	caller func(*http.Request) (string, error)
}

// NewSuggestHandler 提供资料和知识库的推荐问题，以及助手页面。
// 打开页面时不带 asked，作为快速开始。回答结束后带上刚问的句子，列表里不再包含它。
// 页面上点击一条问题只填进提问框；提交后才按刚问的句子刷新推荐。
func NewSuggestHandler(list questionLister, caller func(*http.Request) (string, error)) http.Handler {
	h := &suggestHandler{list: list, caller: caller}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/documents/{id}/suggested-questions", h.document)
	mux.HandleFunc("GET /api/v1/kbs/{kb_id}/suggested-questions", h.kb)
	mux.HandleFunc("GET /assistant", h.page)
	mux.HandleFunc("POST /assistant", h.submit)
	return withRequestID(mux)
}

func (h *suggestHandler) document(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, "document", r.PathValue("id"))
}

func (h *suggestHandler) kb(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, "kb", r.PathValue("kb_id"))
}

func (h *suggestHandler) serve(w http.ResponseWriter, r *http.Request, scopeType, scopeID string) {
	caller, err := h.caller(r)
	if err != nil {
		writeSuggestErr(w, r, err)
		return
	}
	result, err := h.list.List(caller, scopeType, scopeID, r.URL.Query().Get("asked"))
	if err != nil {
		writeSuggestErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeSuggestErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, suggest.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody{
			Code: "NOT_FOUND", Message: "资源不存在", RequestID: requestID(r), Details: map[string]any{},
		})
		return
	}
	writeJSON(w, http.StatusUnauthorized, errorBody{
		Code: "UNAUTHENTICATED", Message: "未登录或登录已过期", RequestID: requestID(r), Details: map[string]any{},
	})
}
