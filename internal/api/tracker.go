package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"wera/internal/store"
)

// Lengths a tracker field may run to, in characters.
const (
	maxNextStep   = 200
	maxContactLen = 200
	maxEventWith  = 200
	maxEventNotes = 2000
)

func tooLong(s string, n int) bool { return utf8.RuneCountInString(s) > n }

// patchApplication is PATCH /api/jobs/{id}/application: the next step and
// its date, the contact, and archiving. Fields left out stay as they are;
// "" (or null for the contact and date) clears one. It returns the job.
func (s *Server) patchApplication(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	var body struct {
		NextStep     *string        `json:"next_step"`
		NextStepAt   *string        `json:"next_step_at"` // RFC 3339; "" clears it
		Contact      *store.Contact `json:"contact"`
		ClearContact bool           `json:"clear_contact"`
		Archived     *bool          `json:"archived"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	d := store.ApplicationDetails{Archived: body.Archived, Contact: body.Contact}
	if body.ClearContact {
		d.Contact = &store.Contact{}
	}
	if body.NextStep != nil {
		v := strings.TrimSpace(*body.NextStep)
		if tooLong(v, maxNextStep) {
			s.writeError(w, http.StatusBadRequest, "the next step is too long")
			return
		}
		d.NextStep = &v
	}
	if body.NextStepAt != nil {
		var at time.Time
		if *body.NextStepAt != "" {
			var err error
			if at, err = time.Parse(time.RFC3339, *body.NextStepAt); err != nil {
				s.writeError(w, http.StatusBadRequest, "next_step_at must be an RFC 3339 time")
				return
			}
		}
		d.NextStepAt = &at
	}
	if c := d.Contact; c != nil {
		c.Name, c.Role, c.Link = strings.TrimSpace(c.Name), strings.TrimSpace(c.Role), strings.TrimSpace(c.Link)
		if tooLong(c.Name, maxContactLen) || tooLong(c.Role, maxContactLen) || tooLong(c.Link, maxContactLen) {
			s.writeError(w, http.StatusBadRequest, "a contact field is too long")
			return
		}
	}
	user := currentUser(r)
	err := store.SetApplicationDetails(r.Context(), s.Pool, user.ID, id, d)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "no application for this job: set a status first")
		return
	}
	if err != nil {
		s.Log.Error("patch application failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	job, err := store.GetJob(r.Context(), s.Pool, user.ID, id)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
		return
	}
	s.writeJSON(w, http.StatusOK, job)
}

// listJobEvents is GET /api/jobs/{id}/events: the application's dated
// events, soonest first.
func (s *Server) listJobEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	events, err := store.JobEvents(r.Context(), s.Pool, currentUser(r).ID, id)
	if err != nil {
		s.Log.Error("list events failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// addEvent is POST /api/jobs/{id}/events: an interview or other dated step.
func (s *Server) addEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	var body struct {
		At    string `json:"at"`
		Kind  string `json:"kind"`
		With  string `json:"with"`
		Notes string `json:"notes"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	at, err := time.Parse(time.RFC3339, body.At)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "at must be an RFC 3339 time")
		return
	}
	if !store.EventKinds[body.Kind] {
		s.writeError(w, http.StatusBadRequest, "kind must be phone_screen, interview, onsite, assessment, offer_call or other")
		return
	}
	with, notes := strings.TrimSpace(body.With), strings.TrimSpace(body.Notes)
	if tooLong(with, maxEventWith) || tooLong(notes, maxEventNotes) {
		s.writeError(w, http.StatusBadRequest, "with or notes is too long")
		return
	}
	e, err := store.AddEvent(r.Context(), s.Pool, currentUser(r).ID, store.Event{JobID: id, At: at, Kind: body.Kind, With: with, Notes: notes})
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "no application for this job: set a status first")
		return
	}
	if err != nil {
		s.Log.Error("add event failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not add the event")
		return
	}
	s.writeJSON(w, http.StatusCreated, e)
}

// deleteEvent is DELETE /api/jobs/{id}/events/{eid}.
func (s *Server) deleteEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	eid, ok := s.pathID(w, r, "eid", "event")
	if !ok {
		return
	}
	err := store.DeleteEvent(r.Context(), s.Pool, currentUser(r).ID, id, eid)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		s.Log.Error("delete event failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not delete the event")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// upcomingEvents is GET /api/events?days=N: the user's events from now (or
// the start of today in their view: from=RFC 3339) over the next N days
// (default 14, at most 90), across applications that aren't archived.
func (s *Server) upcomingEvents(w http.ResponseWriter, r *http.Request) {
	from := time.Now()
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "from must be an RFC 3339 time")
			return
		}
		from = t
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 14
	}
	days = min(days, 90)
	events, err := store.UpcomingEvents(r.Context(), s.Pool, currentUser(r).ID, from, from.AddDate(0, 0, days))
	if err != nil {
		s.Log.Error("upcoming events failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
