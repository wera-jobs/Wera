package api

import (
	"net/http"

	"wera/internal/store"
)

// applicationAnalytics is GET /api/analytics/applications: the funnel,
// weekly response rates, results by applicant system and time between
// stages, from the user's tracker.
func (s *Server) applicationAnalytics(w http.ResponseWriter, r *http.Request) {
	a, err := store.ApplicationAnalytics(r.Context(), s.Pool, currentUser(r).ID, 12)
	if err != nil {
		s.Log.Error("application analytics failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, a)
}
