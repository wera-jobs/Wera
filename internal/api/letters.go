package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"wera/internal/pipeline"
	"wera/internal/profile"
	"wera/internal/store"
)

// pathID parses a positive id from the URL parameter name; when it is not
// one, it writes "bad <what> id" (400) and returns false.
func (s *Server) pathID(w http.ResponseWriter, r *http.Request, name, what string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id <= 0 {
		msg := "bad id"
		if what != "" {
			msg = "bad " + what + " id"
		}
		s.writeError(w, http.StatusBadRequest, msg)
		return 0, false
	}
	return id, true
}

// getCoverLetter is GET /api/jobs/{id}/cover-letter.
func (s *Server) getCoverLetter(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	c, err := store.GetCoverLetter(r.Context(), s.Pool, currentUser(r).ID, id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if c == nil {
		s.writeError(w, http.StatusNotFound, "no cover letter yet")
		return
	}
	s.writeJSON(w, http.StatusOK, c)
}

// generateCoverLetter is POST /api/jobs/{id}/cover-letter: write (or
// rewrite) the letter for a job from the user's profile, resume and the
// posting. It is stored so reopening the job costs nothing.
func (s *Server) generateCoverLetter(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	// An optional steer (tone, length, what to stress); an empty body is fine.
	var in struct {
		Tone    string `json:"tone"`
		Length  string `json:"length"`
		Request string `json:"request"`
	}
	if err := decodeJSON(r, &in); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if _, ok := profile.LetterTones[in.Tone]; !ok || (in.Length != "" && in.Length != "short" && in.Length != "standard" && in.Length != "long") || len(in.Request) > 1000 {
		s.writeError(w, http.StatusBadRequest, "tone, length or request is not valid")
		return
	}
	user := currentUser(r)
	if s.Env == nil || !s.Env.AI.Enabled() {
		s.writeError(w, http.StatusServiceUnavailable, "cover letters are not configured on this server")
		return
	}
	if !s.letterLimit.Allow(strconv.FormatInt(user.ID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many cover letters this hour; try again later")
		return
	}
	job, err := store.GetJob(r.Context(), s.Pool, user.ID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	prof, err := store.GetProfile(r.Context(), s.Pool, user.ID)
	if err != nil || prof == nil || prof.Markdown == "" {
		s.writeError(w, http.StatusBadRequest, "finish your profile first")
		return
	}
	allowance, why, err := pipeline.Allowance(r.Context(), s.Pool, s.Env, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "budget check failed")
		return
	}
	if allowance <= 0 {
		s.writeError(w, http.StatusPaymentRequired, "cover letters are unavailable: "+why)
		return
	}
	desc, err := store.JobDescription(r.Context(), s.Pool, id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	loc := job.LocationRaw
	if job.LocationSummary != nil {
		loc = *job.LocationSummary
	}
	// The fast model writes letters well; the deep (reasoning) model spent
	// its whole token budget thinking and cost ~40x more per letter.
	model := s.Env.AI.Model
	details, err := s.aiDetails(r.Context(), user)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	reply, err := s.chatWithModel(r.Context(), user.ID, "cover_letter", model, 1500, profile.CoverLetterMessages(profile.LetterInput{
		CandidateName: user.Name, Profile: prof.Markdown, Resume: prof.ResumeText,
		Company: job.Company, Title: job.Title, Location: loc, Description: desc,
		Tone: in.Tone, Length: in.Length, Request: in.Request, Details: details,
	}))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again")
		return
	}
	body := profile.CleanLetter(reply)
	if len(strings.Fields(body)) < 80 {
		s.writeError(w, http.StatusBadGateway, "the AI returned an unusable letter; try again")
		return
	}
	c, err := store.SaveCoverLetter(r.Context(), s.Pool, user.ID, id, body, model, false)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not save the letter")
		return
	}
	s.writeJSON(w, http.StatusOK, c)
}

// putCoverLetter is PUT /api/jobs/{id}/cover-letter: save the user's edits.
func (s *Server) putCoverLetter(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	body := profile.CleanLetter(in.Body)
	if body == "" {
		s.writeError(w, http.StatusBadRequest, "the letter is empty")
		return
	}
	user := currentUser(r)
	if _, err := store.GetJob(r.Context(), s.Pool, user.ID, id); err != nil {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	}
	c, err := store.SaveCoverLetter(r.Context(), s.Pool, user.ID, id, body, "", true)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not save the letter")
		return
	}
	s.writeJSON(w, http.StatusOK, c)
}
