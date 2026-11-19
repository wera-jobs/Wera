package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"wera/internal/store"
)

// companyPage is GET /api/companies/{id}: the company page in one request,
// with its H-1B history (null when no filings are matched to it).
func (s *Server) companyPage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "company")
	if !ok {
		return
	}
	p, err := store.GetCompanyPage(r.Context(), s.Pool, currentUser(r).ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "company not found")
		return
	}
	if err != nil {
		s.Log.Error("company page failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	var h1b *store.H1BDetail
	if keys, err := store.CompanyH1BKeys(r.Context(), s.Pool, id); err == nil && len(keys) > 0 {
		v, err := s.h1bCache.get("detail|"+strings.Join(keys, "|"), func() (any, error) {
			return store.H1BEmployerDetail(context.WithoutCancel(r.Context()), s.Pool, keys)
		})
		if err == nil {
			if d := v.(*store.H1BDetail); d.Filings > 0 {
				h1b = d
			}
		}
	}
	s.writeJSON(w, http.StatusOK, struct {
		*store.CompanyPage
		H1B *store.H1BDetail `json:"h1b"`
	}{p, h1b})
}

// followCompany is PUT (follow) or DELETE (unfollow) /api/companies/{id}/follow.
func (s *Server) followCompany(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "company")
	if !ok {
		return
	}
	err := store.SetCompanyFollowed(r.Context(), s.Pool, currentUser(r).ID, id, r.Method == http.MethodPut)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "company not found")
		return
	}
	if err != nil {
		s.Log.Error("follow company failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
