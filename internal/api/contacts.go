package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"wera/internal/auth"
	"wera/internal/store"
)

// Contact field limits, in characters.
const (
	maxContactName  = 200
	maxContactRole  = 200
	maxContactEmail = 254
	maxContactURL   = 500
	maxContactNotes = 5000
)

// optTime is a date field that tells "left out" from null: Set is true
// when the key was in the body, and Value is nil when it was null.
type optTime struct {
	Set   bool
	Value *time.Time
}

func (o *optTime) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(b, []byte("null")) {
		o.Value = nil
		return nil
	}
	var t time.Time
	if err := json.Unmarshal(b, &t); err != nil {
		return errors.New("dates must look like 2026-11-24T15:04:05Z")
	}
	o.Value = &t
	return nil
}

// contactBody is the JSON of a contact to add or change.
type contactBody struct {
	Name            *string `json:"name"`
	Role            *string `json:"role"`
	Email           *string `json:"email"`
	LinkedInURL     *string `json:"linkedin_url"`
	Notes           *string `json:"notes"`
	LastContactedAt optTime `json:"last_contacted_at"`
	FollowUpAt      optTime `json:"follow_up_at"`
}

// input checks the body and turns it into a store.ContactInput. A new
// contact (create) needs a name; a patch may leave it out but not empty it.
func (b *contactBody) input(create bool) (store.ContactInput, error) {
	var in store.ContactInput
	trim := func(p *string, limit int, what string) (*string, error) {
		if p == nil {
			return nil, nil
		}
		v := strings.TrimSpace(*p)
		if utf8.RuneCountInString(v) > limit {
			return nil, errors.New(what + " is too long")
		}
		return &v, nil
	}
	var err error
	if in.Name, err = trim(b.Name, maxContactName, "name"); err != nil {
		return in, err
	}
	if (create && (in.Name == nil || *in.Name == "")) || (!create && in.Name != nil && *in.Name == "") {
		return in, errors.New("name is required")
	}
	if in.Role, err = trim(b.Role, maxContactRole, "role"); err != nil {
		return in, err
	}
	if in.Notes, err = trim(b.Notes, maxContactNotes, "notes"); err != nil {
		return in, err
	}
	if in.Email, err = trim(b.Email, maxContactEmail, "email"); err != nil {
		return in, err
	}
	if in.Email != nil && *in.Email != "" {
		e, err := auth.NormalizeEmail(*in.Email)
		if err != nil {
			return in, errors.New("email doesn't look like an address")
		}
		in.Email = &e
	}
	if in.LinkedInURL, err = trim(b.LinkedInURL, maxContactURL, "linkedin_url"); err != nil {
		return in, err
	}
	if in.LinkedInURL != nil && *in.LinkedInURL != "" {
		u, err := url.Parse(*in.LinkedInURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return in, errors.New("linkedin_url must be an http(s) link")
		}
	}
	in.LastContactedAt, in.ClearLastContacted = b.LastContactedAt.Value, b.LastContactedAt.Set && b.LastContactedAt.Value == nil
	in.FollowUpAt, in.ClearFollowUp = b.FollowUpAt.Value, b.FollowUpAt.Set && b.FollowUpAt.Value == nil
	return in, nil
}

// listJobContacts is GET /api/jobs/{id}/contacts.
func (s *Server) listJobContacts(w http.ResponseWriter, r *http.Request) {
	jobID, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	list, err := store.JobContacts(r.Context(), s.Pool, currentUser(r).ID, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		s.Log.Error("list contacts failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"contacts": list})
}

// allContacts is GET /api/contacts: every contact, with its job.
func (s *Server) allContacts(w http.ResponseWriter, r *http.Request) {
	list, err := store.AllContacts(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.Log.Error("all contacts failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"contacts": list})
}

// addJobContact is POST /api/jobs/{id}/contacts.
func (s *Server) addJobContact(w http.ResponseWriter, r *http.Request) {
	jobID, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	var body contactBody
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	in, err := body.input(true)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := store.AddContact(r.Context(), s.Pool, currentUser(r).ID, jobID, in)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		s.writeError(w, http.StatusNotFound, "job not found")
	case errors.Is(err, store.ErrTooManyContacts):
		s.writeError(w, http.StatusConflict, "this job already has the most contacts it can keep")
	case err != nil:
		s.Log.Error("add contact failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not save the contact")
	default:
		s.writeJSON(w, http.StatusCreated, c)
	}
}

// patchJobContact is PATCH /api/jobs/{id}/contacts/{cid}: fields left out
// stay; null clears a date.
func (s *Server) patchJobContact(w http.ResponseWriter, r *http.Request) {
	jobID, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "cid", "contact")
	if !ok {
		return
	}
	var body contactBody
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	in, err := body.input(false)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := store.UpdateContact(r.Context(), s.Pool, currentUser(r).ID, jobID, id, in)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "contact not found")
		return
	}
	if err != nil {
		s.Log.Error("update contact failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not save the contact")
		return
	}
	s.writeJSON(w, http.StatusOK, c)
}

// deleteJobContact is DELETE /api/jobs/{id}/contacts/{cid}.
func (s *Server) deleteJobContact(w http.ResponseWriter, r *http.Request) {
	jobID, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "cid", "contact")
	if !ok {
		return
	}
	found, err := store.DeleteContact(r.Context(), s.Pool, currentUser(r).ID, jobID, id)
	if err != nil {
		s.Log.Error("delete contact failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not delete the contact")
		return
	}
	if !found {
		s.writeError(w, http.StatusNotFound, "contact not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
