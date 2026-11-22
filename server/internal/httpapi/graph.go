package httpapi

import (
	"context"
	"net/http"

	"yanzhi/server/internal/access"
	"yanzhi/server/internal/graph"
	"yanzhi/server/internal/knowledge"
)

type GraphBrowse interface {
	Subgraph(ctx context.Context, owner string, f graph.LibraryFilter) (graph.View, error)
	Neighbors(ctx context.Context, owner, documentID string) (graph.View, error)
	Node(ctx context.Context, owner, id string) (graph.NodeDetail, error)
	Edge(ctx context.Context, owner, id string) (graph.EdgeDetail, error)
	Extraction(ctx context.Context, owner, documentID string) (graph.Extraction, error)
	Unresolved(ctx context.Context, owner, documentID string) ([]string, error)
}

type GraphRetry interface {
	RetryGraph(ctx context.Context, owner, id, requestID string) error
}

func WithGraph(g GraphBrowse) Option {
	return func(h *handler) { h.graphs = g }
}

func WithGraphRetry(g GraphRetry) Option {
	return func(h *handler) { h.graphRetry = g }
}

func (h *handler) registerGraphRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/graph", h.getGraph)
	mux.HandleFunc("GET /api/v1/graph/nodes/{id}", h.getGraphNode)
	mux.HandleFunc("GET /api/v1/graph/edges/{id}", h.getGraphEdge)
	mux.HandleFunc("GET /api/v1/documents/{id}/graph", h.getDocumentGraph)
	mux.HandleFunc("GET /api/v1/documents/{id}/graph-extraction", h.getGraphExtraction)
	mux.HandleFunc("POST /api/v1/documents/{id}/graph-extraction/retries", h.retryGraphExtraction)
	mux.HandleFunc("GET /api/v1/documents/{id}/unresolved-references", h.getUnresolved)
}

func (h *handler) getGraph(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpenGraph)
	if !ok {
		return
	}
	q := r.URL.Query()
	view, err := h.graphs.Subgraph(r.Context(), caller.User.ID, graph.LibraryFilter{
		KBID:        q.Get("kb_id"),
		DocumentIDs: q["document_id"],
		TagIDs:      q["tag_id"],
	})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeView(w, view)
}

func (h *handler) getDocumentGraph(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpenGraph)
	if !ok {
		return
	}
	view, err := h.graphs.Neighbors(r.Context(), caller.User.ID, r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeView(w, view)
}

func (h *handler) getGraphNode(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpenGraph)
	if !ok {
		return
	}
	detail, err := h.graphs.Node(r.Context(), caller.User.ID, r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if detail.Sources == nil {
		detail.Sources = []graph.SourcePage{}
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *handler) getGraphEdge(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpenGraph)
	if !ok {
		return
	}
	detail, err := h.graphs.Edge(r.Context(), caller.User.ID, r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if detail.Evidence == nil {
		detail.Evidence = []graph.Evidence{}
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *handler) getGraphExtraction(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpenGraph)
	if !ok {
		return
	}
	extraction, err := h.graphs.Extraction(r.Context(), caller.User.ID, r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if extraction.Stages == nil {
		extraction.Stages = []graph.Stage{}
	}
	writeJSON(w, http.StatusOK, extraction)
}

func (h *handler) getUnresolved(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpenGraph)
	if !ok {
		return
	}
	titles, err := h.graphs.Unresolved(r.Context(), caller.User.ID, r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if titles == nil {
		titles = []string{}
	}
	writeJSON(w, http.StatusOK, titles)
}

func (h *handler) retryGraphExtraction(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authorize(w, r, access.ActOpenGraph)
	if !ok {
		return
	}
	if err := h.graphRetry.RetryGraph(r.Context(), caller.User.ID, r.PathValue("id"), requestID(r)); err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func writeView(w http.ResponseWriter, view graph.View) {
	if view.Nodes == nil {
		view.Nodes = []graph.Node{}
	}
	if view.Edges == nil {
		view.Edges = []graph.Edge{}
	}
	writeJSON(w, http.StatusOK, view)
}

var (
	_ GraphBrowse = graph.Service{}
	_ GraphRetry  = knowledge.Service{}
)
