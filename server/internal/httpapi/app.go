// Package httpapi 把领域服务映射成 HTTP 接口。
package httpapi

import (
	"net/http"

	"yanzhi/server/internal/access"
	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/identity"
	"yanzhi/server/internal/knowledge"
	"yanzhi/server/internal/provider"
	"yanzhi/server/internal/qa"
	"yanzhi/server/internal/suggest"
)

// App 是组合根交给 HTTP 层的服务集合。字段都可为零值，缺哪个就不注册对应路由。
type App struct {
	Identity      *identity.Service
	Knowledge     knowledge.Service
	QA            *qa.Service
	Graph         graph.Service
	Models        provider.Store
	Suggest       *suggest.Memory
	RestrictEmail bool
}

// Handler 返回带请求编号的根处理器。
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)

	if a.Identity != nil {
		mux.HandleFunc("POST /api/v1/auth/register", a.register)
		mux.HandleFunc("POST /api/v1/auth/login", a.login)
		mux.HandleFunc("POST /api/v1/auth/logout", a.logout)
		mux.HandleFunc("POST /api/v1/auth/password", a.changePassword)
		mux.HandleFunc("GET /api/v1/me", a.me)
	}

	mux.HandleFunc("GET /api/v1/kbs", a.listKBs)
	mux.HandleFunc("POST /api/v1/kbs", a.createKB)
	mux.HandleFunc("GET /api/v1/kbs/{kb_id}", a.getKB)
	mux.HandleFunc("PATCH /api/v1/kbs/{kb_id}/settings", a.patchKBSettings)
	mux.HandleFunc("POST /api/v1/kbs/{kb_id}/documents", a.uploadDocument)

	mux.HandleFunc("GET /api/v1/documents", a.listDocuments)
	mux.HandleFunc("GET /api/v1/documents/{id}", a.getDocument)
	mux.HandleFunc("PATCH /api/v1/documents/{id}", a.patchDocument)
	mux.HandleFunc("DELETE /api/v1/documents/{id}", a.deleteDocument)
	mux.HandleFunc("GET /api/v1/documents/{id}/file", a.downloadFile)
	mux.HandleFunc("POST /api/v1/documents/{id}/retries", a.retryDocument)
	mux.HandleFunc("GET /api/v1/documents/{id}/ingest", a.documentIngest)
	mux.HandleFunc("GET /api/v1/documents/{id}/chunks", a.listChunks)
	mux.HandleFunc("GET /api/v1/documents/{id}/chunks/{chunk_id}", a.getChunk)

	mux.HandleFunc("GET /api/v1/tags", a.listTags)
	mux.HandleFunc("POST /api/v1/tags", a.createTag)
	mux.HandleFunc("PATCH /api/v1/tags/{id}", a.patchTag)
	mux.HandleFunc("DELETE /api/v1/tags/{id}", a.deleteTag)
	mux.HandleFunc("PUT /api/v1/documents/{id}/tags", a.putDocumentTags)

	mux.HandleFunc("GET /api/v1/graph", a.getGraph)
	mux.HandleFunc("GET /api/v1/graph/nodes/{id}", a.getGraphNode)
	mux.HandleFunc("GET /api/v1/graph/edges/{id}", a.getGraphEdge)
	mux.HandleFunc("GET /api/v1/documents/{id}/graph", a.getDocumentGraph)
	mux.HandleFunc("GET /api/v1/documents/{id}/unresolved-references", a.unresolvedReferences)
	mux.HandleFunc("GET /api/v1/documents/{id}/graph-extraction", a.graphExtraction)
	mux.HandleFunc("POST /api/v1/documents/{id}/graph-extraction/retries", a.retryGraphExtraction)

	mux.HandleFunc("POST /api/v1/qa/sessions", a.createSession)
	mux.HandleFunc("GET /api/v1/qa/sessions", a.listSessions)
	mux.HandleFunc("GET /api/v1/qa/sessions/{id}", a.getSession)
	mux.HandleFunc("PATCH /api/v1/qa/sessions/{id}", a.patchSession)
	mux.HandleFunc("DELETE /api/v1/qa/sessions/{id}", a.deleteSession)
	mux.HandleFunc("POST /api/v1/qa/sessions/{id}/stop", a.stopSession)
	mux.HandleFunc("POST /api/v1/qa/sessions/{id}/turns", a.ask)

	mux.HandleFunc("GET /api/v1/admin/model-configs", a.listModelConfigs)
	mux.HandleFunc("PUT /api/v1/admin/model-configs/{kind}", a.putModelConfig)
	mux.HandleFunc("POST /api/v1/admin/model-configs/{kind}/connection-tests", a.testModelConfig)

	var root http.Handler = mux
	if a.Suggest != nil {
		sug := NewSuggestHandler(a.Suggest, func(r *http.Request) (string, error) {
			p, err := a.identityOf(r)
			return p.UserID, err
		})
		combined := http.NewServeMux()
		combined.Handle("GET /api/v1/documents/{id}/suggested-questions", sug)
		combined.Handle("GET /api/v1/kbs/{kb_id}/suggested-questions", sug)
		combined.Handle("GET /assistant", sug)
		combined.Handle("POST /assistant", sug)
		combined.Handle("/", mux)
		root = combined
	}
	return withRequestID(root)
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	if a.Identity != nil && a.Identity.Pool != nil {
		if err := a.Identity.Pool.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *App) identityOf(r *http.Request) (identity.Principal, error) {
	raw, err := bearer(r)
	if err != nil {
		return identity.Principal{}, apperr.ErrUnauthenticated
	}
	return a.Identity.Authenticate(r.Context(), raw)
}

// guard 把 access 的判定顺序翻译成领域错误，处理器只映射状态码。
func (a *App) guard(p identity.Principal, action string) error {
	switch access.Gate(&access.Principal{Role: p.Role, Verified: p.Verified}, action, a.RestrictEmail) {
	case access.Allow:
		return nil
	case access.Unauthenticated:
		return apperr.ErrUnauthenticated
	case access.Forbidden:
		return apperr.ErrForbidden
	case access.EmailUnverified:
		return apperr.ErrEmailUnverified
	default:
		return apperr.ErrNotFound
	}
}
