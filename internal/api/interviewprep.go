package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// practiceAnswer is POST /api/jobs/{id}/interview-prep/practice: feedback
// on the user's practice answer, with the follow-up an interviewer would
// likely ask next. Answering question starts its thread over; answering
// with follow_up set (the follow-up being answered) adds a turn, and the
// thread's earlier turns go with the request. history, when given, is used
// as the earlier turns instead. The thread is stored and returned.
func (s *Server) practiceAnswer(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	var body struct {
		Question string                 `json:"question"`
		Answer   string                 `json:"answer"`
		FollowUp string                 `json:"follow_up"`
		History  []profile.PracticeTurn `json:"history"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	body.Question, body.Answer, body.FollowUp = strings.TrimSpace(body.Question), strings.TrimSpace(body.Answer), strings.TrimSpace(body.FollowUp)
	switch {
	case body.Question == "" || len(body.Question) > 300:
		s.writeError(w, http.StatusBadRequest, "the question is missing or too long")
		return
	case len(body.FollowUp) > 300:
		s.writeError(w, http.StatusBadRequest, "the follow-up question is too long")
		return
	case len(strings.Fields(body.Answer)) < 10:
		s.writeError(w, http.StatusBadRequest, "write a few sentences first; feedback needs at least 10 words")
		return
	case len(body.Answer) > 4000:
		s.writeError(w, http.StatusBadRequest, "the answer is too long (4,000 characters max)")
		return
	case len(body.History) > profile.MaxPracticeHistory:
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("send at most %d earlier turns", profile.MaxPracticeHistory))
		return
	}
	for _, t := range body.History {
		if len(t.Question) > 300 || len(t.Answer) > 4000 {
			s.writeError(w, http.StatusBadRequest, "an earlier turn is too long")
			return
		}
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
	asked, history := body.Question, body.History
	if body.FollowUp != "" {
		asked = body.FollowUp
		if len(history) == 0 {
			thread, err := store.PracticeThreadFor(r.Context(), s.Pool, user.ID, id, body.Question)
			if err != nil {
				s.writeError(w, http.StatusInternalServerError, "query failed")
				return
			}
			if thread != nil {
				for _, t := range thread.Turns {
					history = append(history, profile.PracticeTurn{Question: t.Question, Answer: t.Answer})
				}
			}
		}
	}
	reply, err := s.chatWithModel(r.Context(), user.ID, "interview_practice", s.Env.AI.Model, 1200, profile.PracticeMessages(*in, asked, body.Answer, history))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again")
		return
	}
	// What the candidate said in this and earlier answers is theirs too.
	said := []string{own, body.Answer}
	for _, t := range history {
		said = append(said, t.Answer)
	}
	f, err := profile.ParsePracticeFeedback(reply, strings.Join(said, "\n"))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI returned unusable feedback; try again")
		return
	}
	fb, err := json.Marshal(f)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not save the practice")
		return
	}
	thread, err := store.AddPracticeTurn(r.Context(), s.Pool, user.ID, id, body.Question,
		store.PracticeTurn{Question: asked, Answer: body.Answer, Feedback: fb, At: time.Now().UTC()}, body.FollowUp == "")
	if err != nil {
		s.Log.Error("save practice failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not save the practice")
		return
	}
	s.writeJSON(w, http.StatusOK, struct {
		*profile.PracticeFeedback
		Thread *store.PracticeThread `json:"thread"`
	}{f, thread})
}

// listPractice is GET /api/jobs/{id}/interview-prep/practice: the user's
// practice threads for the job, most recent first.
func (s *Server) listPractice(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	threads, err := store.PracticeThreads(r.Context(), s.Pool, currentUser(r).ID, id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"threads": threads})
}
