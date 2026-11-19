package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"wera/internal/store"
)

// Limits on one report: a few paragraphs from the person, and the
// extension's diagnostics (its own log is cut to the last 150 lines).
const (
	reportNoteMax    = 4000
	reportDetailsMax = 256 << 10
	reportPageMax    = 500
)

type problemReportIn struct {
	Note    string `json:"note"`
	Page    string `json:"page"`
	Version string `json:"version"`
	Details string `json:"details"`
}

// addProblemReport is POST /api/ext/reports: "Report a problem" in the
// extension. The admin app lists them.
func (s *Server) addProblemReport(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if !s.reportLimit.Allow(strconv.FormatInt(user.ID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many reports for now; try again in an hour")
		return
	}
	var in problemReportIn
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	in.Page, in.Version = strings.TrimSpace(in.Page), strings.TrimSpace(in.Version)
	switch {
	case in.Note == "" && in.Details == "":
		s.writeError(w, http.StatusBadRequest, "say what went wrong")
		return
	case utf8.RuneCountInString(in.Note) > reportNoteMax:
		s.writeError(w, http.StatusBadRequest, "the note is too long")
		return
	case len(in.Details) > reportDetailsMax || len(in.Page) > reportPageMax || len(in.Version) > 40:
		s.writeError(w, http.StatusBadRequest, "the report is too large")
		return
	case !utf8.ValidString(in.Details) || !utf8.ValidString(in.Page) || !utf8.ValidString(in.Version):
		s.writeError(w, http.StatusBadRequest, "the report is not text")
		return
	}
	id, err := store.AddProblemReport(r.Context(), s.Pool, user.ID, "extension", in.Note, in.Page, in.Version, in.Details)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	s.Log.Info("problem reported", "user_id", user.ID, "report_id", id)
	s.writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// adminReports is GET /api/admin/reports?all=1&before=&limit=: open reports
// (all with all=1), newest first, without their details.
func (s *Server) adminReports(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	reports, err := store.ProblemReports(r.Context(), s.Pool, q.Get("all") == "1", max(before, 0), limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"reports": reports})
}

// adminReport is GET /api/admin/reports/{id}: one report with its details.
func (s *Server) adminReport(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "report")
	if !ok {
		return
	}
	rep, err := store.GetProblemReport(r.Context(), s.Pool, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "no such report")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, rep)
}

// resolveReport is PATCH /api/admin/reports/{id} {resolved}: dealt with, or open again.
func (s *Server) resolveReport(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "report")
	if !ok {
		return
	}
	var in struct {
		Resolved bool `json:"resolved"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	found, err := store.ResolveProblemReport(r.Context(), s.Pool, id, in.Resolved)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "save failed")
		return
	} else if !found {
		s.writeError(w, http.StatusNotFound, "no such report")
		return
	}
	s.audit(r, "report.resolve", "report:"+strconv.FormatInt(id, 10), map[string]any{"resolved": in.Resolved})
	w.WriteHeader(http.StatusNoContent)
}
