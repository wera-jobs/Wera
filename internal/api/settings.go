package api

import (
	"net/http"

	"wera/internal/auth"
	"wera/internal/store"
)

type settingsView struct {
	store.Settings
	HiddenCompanies []store.HiddenCompany `json:"hidden_companies"`
}

// getSettings is GET /api/settings.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	st, err := store.GetSettings(r.Context(), s.Pool, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	hidden, err := store.HiddenCompanies(r.Context(), s.Pool, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, settingsView{Settings: st, HiddenCompanies: hidden})
}

// putSettings is PUT /api/settings.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var st store.Settings
	if err := decodeJSON(r, &st); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := st.Validate(); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := store.SaveSettings(r.Context(), s.Pool, currentUser(r).ID, st); err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	s.getSettings(w, r)
}

// hideCompany is PUT (hide) / DELETE (unhide) /api/companies/{id}/hidden.
func (s *Server) hideCompany(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "company")
	if !ok {
		return
	}
	hidden := r.Method == http.MethodPut
	if err := store.SetCompanyHidden(r.Context(), s.Pool, currentUser(r).ID, id, hidden); err != nil {
		s.writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteAccount is DELETE /api/auth/account: the current password is
// required, then the account and everything that belongs to it is gone.
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	user := currentUser(r)
	if !s.sessionOnly(w, r) || !s.passwordTry(w, user.ID) {
		return
	}
	hash, err := store.PasswordHash(r.Context(), s.Pool, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if hash == "" || !auth.CheckPassword(hash, body.Password) {
		s.writeError(w, http.StatusUnauthorized, "password is wrong")
		return
	}
	if err := store.DeleteUser(r.Context(), s.Pool, user.ID); err != nil {
		s.Log.Error("delete account failed", "user_id", user.ID, "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not delete the account")
		return
	}
	s.Log.Info("account deleted", "user_id", user.ID)
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
