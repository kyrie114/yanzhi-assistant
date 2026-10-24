package httpapi

import "net/http"

func (a *App) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	user, err := a.Identity.Register(r.Context(), req.Email, req.Name, req.Password)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	result, err := a.Identity.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.Identity.Logout(r.Context(), p); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := a.Identity.ChangePassword(r.Context(), p.UserID, req.OldPassword, req.NewPassword); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	p, err := a.identityOf(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	user, err := a.Identity.Me(r.Context(), p.UserID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}
