package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"wera/internal/store"
)

// searchKeys are the Jobs filters a saved search may hold (as /api/jobs
// takes them); anything else (the open job, the page) is dropped.
var searchKeys = map[string]bool{
	"q": true, "industry": true, "category": true, "min_score": true, "min_pay": true,
	"sponsorship": true, "work_mode": true, "status": true, "posted_within": true, "sort": true,
}

const maxSearchName = 60

// cleanSearch keeps only filter keys with values, in a stable order.
func cleanSearch(raw string) (string, bool) {
	if len(raw) > 1000 {
		return "", false
	}
	in, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
	if err != nil {
		return "", false
	}
	out := url.Values{}
	for k, vs := range in {
		if searchKeys[k] && len(vs) > 0 && vs[0] != "" {
			out.Set(k, vs[0])
		}
	}
	return out.Encode(), true // Encode sorts by key
}

func searchName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	return name, name != "" && len([]rune(name)) <= maxSearchName
}

// listSearches is GET /api/searches.
func (s *Server) listSearches(w http.ResponseWriter, r *http.Request) {
	out, err := store.ListSearches(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.Log.Error("list searches failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"searches": out})
}

// addSearch is POST /api/searches {"name", "query"}.
func (s *Server) addSearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Query string `json:"query"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	name, ok := searchName(body.Name)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "give the search a name of up to 60 characters")
		return
	}
	query, ok := cleanSearch(body.Query)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "the filters could not be read")
		return
	}
	saved, err := store.AddSearch(r.Context(), s.Pool, currentUser(r).ID, name, query)
	if errors.Is(err, store.ErrTooManySearches) {
		s.writeError(w, http.StatusConflict, "you can keep up to 20 saved searches: delete one first")
		return
	}
	if err != nil {
		s.Log.Error("add search failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not save the search")
		return
	}
	s.writeJSON(w, http.StatusCreated, saved)
}

// renameSearch is PATCH /api/searches/{id} {"name"}.
func (s *Server) renameSearch(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "search")
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	name, ok := searchName(body.Name)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "give the search a name of up to 60 characters")
		return
	}
	err := store.RenameSearch(r.Context(), s.Pool, currentUser(r).ID, id, name)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "search not found")
		return
	}
	if err != nil {
		s.Log.Error("rename search failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not rename the search")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteSearch is DELETE /api/searches/{id}.
func (s *Server) deleteSearch(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "search")
	if !ok {
		return
	}
	err := store.DeleteSearch(r.Context(), s.Pool, currentUser(r).ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "search not found")
		return
	}
	if err != nil {
		s.Log.Error("delete search failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not delete the search")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
