package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"wera/internal/pipeline"
	"wera/internal/profile"
	"wera/internal/store"
)

// prepContext loads what interview prep and practice are written from: the
// job, the user's profile, resume and application details. It writes the
// error and returns nil when something is missing or the AI is not
// available; what says what the feature is called in messages.
func (s *Server) prepContext(w http.ResponseWriter, r *http.Request, jobID int64, what string) (*profile.PrepInput, string) {
	user := currentUser(r)
	if s.Env == nil || !s.Env.AI.Enabled() {
		s.writeError(w, http.StatusServiceUnavailable, what+" is not configured on this server")
		return nil, ""
	}
	job, err := store.GetJob(r.Context(), s.Pool, user.ID, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return nil, ""
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return nil, ""
	}
	prof, err := store.GetProfile(r.Context(), s.Pool, user.ID)
	if err != nil || prof == nil || prof.Markdown == "" {
		s.writeError(w, http.StatusBadRequest, "finish your profile first")
		return nil, ""
	}
	allowance, why, err := pipeline.Allowance(r.Context(), s.Pool, s.Env, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "budget check failed")
		return nil, ""
	}
	if allowance <= 0 {
		s.writeError(w, http.StatusPaymentRequired, what+" is unavailable: "+why)
		return nil, ""
	}
	desc, err := store.JobDescription(r.Context(), s.Pool, jobID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return nil, ""
	}
	details, err := s.aiDetails(r.Context(), user)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return nil, ""
	}
	loc := job.LocationRaw
	if job.LocationSummary != nil {
		loc = *job.LocationSummary
	}
	in := &profile.PrepInput{
		CandidateName: user.Name, Profile: prof.Markdown, Resume: prof.ResumeText,
		Company: job.Company, Title: job.Title, Location: loc, Description: desc, Details: details,
	}
	// The candidate's own text: a number in a talking point must come from here.
	own := []string{prof.Markdown, prof.ResumeText}
	for _, v := range details {
		own = append(own, v)
	}
	return in, strings.Join(own, "\n")
}

// getInterviewPrep is GET /api/jobs/{id}/interview-prep.
func (s *Server) getInterviewPrep(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	p, err := store.GetInterviewPrep(r.Context(), s.Pool, currentUser(r).ID, id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if p == nil {
		s.writeError(w, http.StatusNotFound, "no interview prep yet")
		return
	}
	s.writeJSON(w, http.StatusOK, p)
}

// generateInterviewPrep is POST /api/jobs/{id}/interview-prep: write (or
// rewrite) the prep for a job: likely questions with the user's own talking
// points, a brief and questions to ask. It is stored so reopening costs nothing.
func (s *Server) generateInterviewPrep(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	user := currentUser(r)
	if !s.prepLimit.Allow(strconv.FormatInt(user.ID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many interview preps this hour; try again later")
		return
	}
	in, own := s.prepContext(w, r, id, "interview prep")
	if in == nil {
		return
	}
	model := s.Env.AI.Model
	reply, err := s.chatWithModel(r.Context(), user.ID, "interview_prep", model, 3000, profile.InterviewPrepMessages(*in))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again")
		return
	}
	prep, err := profile.ParseInterviewPrep(reply, own)
	if err != nil {
		s.Log.Warn("interview prep unusable", "job", id, "err", err)
		s.writeError(w, http.StatusBadGateway, "the AI returned an unusable prep; try again")
		return
	}
	saved, err := store.SaveInterviewPrep(r.Context(), s.Pool, user.ID, id, prep, model)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not save the prep")
		return
	}
	s.writeJSON(w, http.StatusOK, saved)
}

// practiceAnswer is POST /api/jobs/{id}/interview-prep/practice: feedback on
// the user's practice answer to one question. Nothing is stored.
func (s *Server) practiceAnswer(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	var body struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	body.Question, body.Answer = strings.TrimSpace(body.Question), strings.TrimSpace(body.Answer)
	switch {
	case body.Question == "" || len(body.Question) > 300:
		s.writeError(w, http.StatusBadRequest, "the question is missing or too long")
		return
	case len(strings.Fields(body.Answer)) < 10:
		s.writeError(w, http.StatusBadRequest, "write a few sentences first; feedback needs at least 10 words")
		return
	case len(body.Answer) > 4000:
		s.writeError(w, http.StatusBadRequest, "the answer is too long (4,000 characters max)")
		return
	}
	user := currentUser(r)
	if !s.practiceLimit.Allow(strconv.FormatInt(user.ID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many practice answers this hour; try again later")
		return
	}
	in, own := s.prepContext(w, r, id, "practice feedback")
	if in == nil {
		return
	}
	reply, err := s.chatWithModel(r.Context(), user.ID, "interview_practice", s.Env.AI.Model, 1200, profile.PracticeMessages(*in, body.Question, body.Answer))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again")
		return
	}
	// What the candidate said in the answer is theirs too.
	f, err := profile.ParsePracticeFeedback(reply, own+"\n"+body.Answer)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI returned unusable feedback; try again")
		return
	}
	s.writeJSON(w, http.StatusOK, f)
}
