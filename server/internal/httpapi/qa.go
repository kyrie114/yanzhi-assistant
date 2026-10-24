package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"yanzhi/server/internal/apperr"
	"yanzhi/server/internal/qa"
)

type sseStream struct {
	w http.ResponseWriter
	f http.Flusher
	n int
}

func (s *sseStream) begin() error {
	s.w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	s.w.Header().Set("Cache-Control", "no-cache")
	s.w.Header().Set("Connection", "keep-alive")
	s.w.Header().Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	if s.f != nil {
		s.f.Flush()
	}
	return nil
}

func (s *sseStream) send(event string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	s.n++
	if _, err := fmt.Fprintf(s.w, "id: %d\nevent: %s\ndata: %s\n\n", s.n, event, body); err != nil {
		return err
	}
	if s.f != nil {
		s.f.Flush()
	}
	return nil
}

func (a *App) createSession(w http.ResponseWriter, r *http.Request) {
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
		Title string `json:"title"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	session, err := a.QA.CreateSession(r.Context(), p.UserID, req.Title)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, session)
}

func (a *App) listSessions(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	page := queryInt(r, "page", 1)
	size := queryInt(r, "page_size", 20)
	items, total, err := a.QA.ListSessions(r.Context(), p.UserID, page, size)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "page": page, "page_size": size, "total": total})
}

func (a *App) getSession(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	session, err := a.QA.GetSession(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (a *App) patchSession(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	session, err := a.QA.PatchSession(r.Context(), p.UserID, r.PathValue("id"), req.Title)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (a *App) deleteSession(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.QA.DeleteSession(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (a *App) stopSession(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.QA.Stop(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (a *App) ask(w http.ResponseWriter, r *http.Request) {
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
		Question  string   `json:"question"`
		Scope     qa.Scope `json:"scope"`
		WebSearch bool     `json:"web_search"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	stream := &sseStream{w: w}
	if f, ok := w.(http.Flusher); ok {
		stream.f = f
	}
	started := false
	start := func() error {
		started = true
		return stream.begin()
	}
	emit := func(event string, data any) error { return stream.send(event, data) }

	in := qa.AskInput{
		Owner: p.UserID, SessionID: r.PathValue("id"), RequestID: requestID(r),
		Question: req.Question, Scope: req.Scope, WebSearch: req.WebSearch,
	}
	if err := a.QA.Ask(r.Context(), in, start, emit); err != nil && !started {
		writeErr(w, r, err)
	}
}
