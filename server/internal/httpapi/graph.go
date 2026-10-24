package httpapi

import (
	"net/http"

	"yanzhi/server/internal/access"
	"yanzhi/server/internal/graph"
)

func (a *App) getGraph(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpenGraph); err != nil {
		writeErr(w, r, err)
		return
	}
	q := r.URL.Query()
	view, err := a.Graph.Query(r.Context(), p.UserID, graph.Filter{
		KBID:        q.Get("kb_id"),
		DocumentIDs: q["document_id"],
		TagIDs:      q["tag_id"],
		Limit:       queryInt(r, "limit", 300),
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) getGraphNode(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpenGraph); err != nil {
		writeErr(w, r, err)
		return
	}
	node, err := a.Graph.Node(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (a *App) getGraphEdge(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpenGraph); err != nil {
		writeErr(w, r, err)
		return
	}
	edge, err := a.Graph.Edge(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, edge)
}

func (a *App) getDocumentGraph(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	view, err := a.Graph.DocumentHop(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) unresolvedReferences(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	items, err := a.Graph.Unresolved(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) graphExtraction(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	state, err := a.Graph.State(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (a *App) retryGraphExtraction(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.guard(p, access.ActOpen); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.Knowledge.RetryGraph(r.Context(), p.UserID, r.PathValue("id"), requestID(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
