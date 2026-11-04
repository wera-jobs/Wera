package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"wera/internal/auth"
	"wera/internal/pipeline"
	"wera/internal/profile"
	"wera/internal/resume"
	"wera/internal/scoring"
	"wera/internal/sources"
	"wera/internal/store"
)

// extTokenPrefix marks extension tokens, so a leaked one is recognizable.
const extTokenPrefix = "wera_ext_" //nolint:gosec // a prefix, not a credential

// bearerHash returns the hash of an "Authorization: Bearer" extension
// token, or "".
func bearerHash(r *http.Request) string {
	v := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(v, "Bearer ")
	if !ok || !strings.HasPrefix(tok, extTokenPrefix) {
		return ""
	}
	return auth.HashToken(tok)
}

// checkCredentials verifies an email and password with the login rate
// limits; on failure it has already written the response.
func (s *Server) checkCredentials(w http.ResponseWriter, r *http.Request, email, password string) *store.User {
	if !s.loginLimit.Allow(s.clientIP(r)) || !s.accountLimit.Allow(strings.ToLower(strings.TrimSpace(email))) {
		s.writeError(w, http.StatusTooManyRequests, "too many login attempts; try again in a few minutes")
		return nil
	}
	u, hash, err := store.UserCredentials(r.Context(), s.Pool, strings.TrimSpace(email))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.Log.Error("login lookup failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "login failed")
		return nil
	}
	if u == nil || hash == "" {
		auth.EqualizeTiming(password)
		s.writeError(w, http.StatusUnauthorized, "wrong email or password")
		return nil
	}
	if !auth.CheckPassword(hash, password) {
		s.writeError(w, http.StatusUnauthorized, "wrong email or password")
		return nil
	}
	return u
}

// createExtToken is POST /api/ext/tokens: the extension signs in with an
// email and password and receives a long-lived token (shown once).
func (s *Server) createExtToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	u := s.checkCredentials(w, r, in.Email, in.Password)
	if u == nil {
		return
	}
	token, _, err := auth.NewToken()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not create a token")
		return
	}
	token = extTokenPrefix + token
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 80 {
		name = "Chrome extension"
	}
	t, err := store.CreateAPIToken(r.Context(), s.Pool, u.ID, auth.HashToken(token), name)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not create a token")
		return
	}
	s.Log.Info("extension connected", "user_id", u.ID, "token_id", t.ID)
	s.writeJSON(w, http.StatusCreated, map[string]any{"token": token, "user": u, "connection": t})
}

func (s *Server) listExtTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := store.ListAPITokens(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"connections": ts})
}

// deleteExtToken is DELETE /api/ext/tokens/{id}, or /current for the
// token making the request (the extension's sign out).
func (s *Server) deleteExtToken(w http.ResponseWriter, r *http.Request) {
	if chi.URLParam(r, "id") == "current" {
		h := bearerHash(r)
		if h == "" {
			s.writeError(w, http.StatusBadRequest, "not an extension request")
			return
		}
		if err := store.DeleteAPITokenByHash(r.Context(), s.Pool, h); err != nil {
			s.writeError(w, http.StatusInternalServerError, "revoke failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	id, ok := s.pathID(w, r, "id", "")
	if !ok {
		return
	}
	if err := store.DeleteAPIToken(r.Context(), s.Pool, currentUser(r).ID, id); errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "no such connection")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "revoke failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getApplicant(w http.ResponseWriter, r *http.Request) {
	v, err := s.applicantFor(r.Context(), currentUser(r))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, v)
}

func (s *Server) putApplicant(w http.ResponseWriter, r *http.Request) {
	var a store.Applicant
	if err := decodeJSON(r, &a); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := a.Validate(); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := store.SaveApplicant(r.Context(), s.Pool, currentUser(r).ID, &a); err != nil {
		s.writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	s.getApplicant(w, r)
}

// aiReady checks the server and the user's budget before an AI call; on
// failure it has written the response.
func (s *Server) aiReady(w http.ResponseWriter, r *http.Request, what string) bool {
	if s.Env == nil || !s.Env.AI.Enabled() {
		s.writeError(w, http.StatusServiceUnavailable, what+" is not configured on this server")
		return false
	}
	if !s.extAILimit.Allow(strconv.FormatInt(currentUser(r).ID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many AI requests this hour; try again later")
		return false
	}
	if code, msg := s.aiAvailable(r.Context(), currentUser(r).ID, what); code != 0 {
		s.writeError(w, code, msg)
		return false
	}
	return true
}

// aiAvailable checks the server and the user's budget, returning an HTTP
// status and message when an AI call cannot be made (0 when it can).
func (s *Server) aiAvailable(ctx context.Context, userID int64, what string) (int, string) {
	if s.Env == nil || !s.Env.AI.Enabled() {
		return http.StatusServiceUnavailable, what + " is not configured on this server"
	}
	allowance, why, err := pipeline.Allowance(ctx, s.Pool, s.Env, userID)
	if err != nil {
		return http.StatusInternalServerError, "budget check failed"
	}
	if allowance <= 0 {
		return http.StatusPaymentRequired, what + " is unavailable: " + why
	}
	return 0, ""
}

// suggestApplicant is POST /api/applicant/suggest: details read off the
// stored resume (links and phone verbatim), for the user to review.
func (s *Server) suggestApplicant(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	prof, err := store.GetProfile(r.Context(), s.Pool, user.ID)
	if err != nil || prof == nil || strings.TrimSpace(prof.ResumeText) == "" {
		s.writeError(w, http.StatusBadRequest, "upload a resume first")
		return
	}
	if !s.aiReady(w, r, "reading your resume") {
		return
	}
	reply, err := s.chatForUser(r.Context(), user.ID, "applicant", 600, profile.ApplicantMessages(prof.ResumeText))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again")
		return
	}
	sug, err := profile.ParseApplicantSuggestions(reply, prof.ResumeText)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI returned unusable details; try again")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"suggestions": sug})
}

func (s *Server) industryIDs() ([]string, map[string]bool) {
	ids := []string{}
	set := map[string]bool{}
	for _, in := range s.Industries {
		ids = append(ids, in.ID)
		set[in.ID] = true
	}
	if !set["other"] {
		ids, set["other"] = append(ids, "other"), true
	}
	sort.Strings(ids)
	return ids, set
}

// extractJob is POST /api/ext/extract: the AI reads a page's text into job
// fields when the page has no structured job data.
func (s *Server) extractJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL   string `json:"url"`
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if len(strings.TrimSpace(in.Text)) < 200 {
		s.writeError(w, http.StatusUnprocessableEntity, "this page has too little text to read a job from")
		return
	}
	if !s.aiReady(w, r, "reading job pages") {
		return
	}
	ids, set := s.industryIDs()
	reply, err := s.chatForUser(r.Context(), currentUser(r).ID, "extract", 500, profile.ExtractMessages(in.URL, in.Title, in.Text, ids))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again")
		return
	}
	d, err := profile.ParseJobDraft(reply, in.Text, set)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI could not read this page; fill the form yourself")
		return
	}
	s.writeJSON(w, http.StatusOK, d)
}

// maxPostingChars bounds the posting text a keyword match reads.
const maxPostingChars = 60000

// postingKeywords is POST /api/ext/keywords: the skills and tools a job
// page names, and which of them the user's main resume shows. For a job
// not in Wera yet, so the panel can say how the resume meets it before
// anything is saved. Word matching only: no AI call, nothing stored.
func (s *Server) postingKeywords(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Description string `json:"description"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	text := in.Description
	if len(text) > maxPostingChars {
		text = text[:maxPostingChars]
	}
	kws := resume.KnownTerms(scoring.ScrubPosting(text))
	if len(kws) > 30 {
		kws = kws[:30]
	}
	out := map[string]any{"keywords": kws, "coverage": nil, "resume_id": nil}
	doc, err := store.BaseResume(r.Context(), s.Pool, currentUser(r).ID)
	if err == nil {
		out["coverage"] = resume.KeywordCoverage(doc.Data.PlainText(), kws)
		out["resume_id"] = doc.ID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// lookupJob is GET /api/ext/jobs/lookup?url=: the user's job for a page.
func (s *Server) lookupJob(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id, err := store.LookupJobByURL(r.Context(), s.Pool, user.ID, r.URL.Query().Get("url"))
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeJSON(w, http.StatusOK, map[string]any{"job": nil})
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	job, err := store.GetJob(r.Context(), s.Pool, user.ID, id)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"job": nil})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

// postedAt reads the posting date a page gives, keeping only a sane one:
// not in the future and not before job boards existed.
func postedAt(v string) *time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
	if err != nil {
		if t, err = time.Parse("2006-01-02", strings.TrimSpace(v)); err != nil {
			return nil
		}
	}
	if t.After(time.Now().Add(24*time.Hour)) || t.Year() < 2000 {
		return nil
	}
	return &t
}

// addJob is POST /api/jobs (and /api/ext/jobs): save a job from any page
// to the user's lists and tracker, then share it with the other users it
// suits. A URL the user already has returns their job (200).
func (s *Server) addJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL         string `json:"url"`
		Company     string `json:"company"`
		Title       string `json:"title"`
		Location    string `json:"location"`
		WorkMode    string `json:"work_mode"`
		Department  string `json:"department"`
		Description string `json:"description"`
		Industry    string `json:"industry"`
		Posted      string `json:"posted"` // ISO date from the page's job data, optional
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	in.Company, in.Title, in.URL = strings.TrimSpace(in.Company), strings.TrimSpace(in.Title), strings.TrimSpace(in.URL)
	switch {
	case in.Company == "" || in.Title == "":
		s.writeError(w, http.StatusBadRequest, "company and title are required")
		return
	case !strings.HasPrefix(in.URL, "https://") && !strings.HasPrefix(in.URL, "http://"):
		s.writeError(w, http.StatusBadRequest, "the job needs its web address (http or https)")
		return
	case len(in.Company) > 140 || len(in.Title) > 200 || len(in.Location) > 200 || len(in.URL) > 2000 || len(in.Description) > 60000:
		s.writeError(w, http.StatusBadRequest, "a field is too long")
		return
	}
	if _, set := s.industryIDs(); !set[in.Industry] {
		in.Industry = "other"
	}
	var remote *bool
	if in.WorkMode == "remote" {
		t := true
		remote = &t
	}
	user := currentUser(r)
	id, created, err := store.AddManualJob(r.Context(), s.Pool, user.ID, store.ManualJob{
		URL: in.URL, Company: in.Company, Title: in.Title, Location: in.Location, Remote: remote,
		Department: in.Department, Description: in.Description, Industry: in.Industry, PostedAt: postedAt(in.Posted),
	})
	if err != nil {
		s.Log.Error("add job failed", "user_id", user.ID, "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not add the job")
		return
	}
	if created {
		// Scoring outlives the request on purpose.
		s.background("added job", func() { s.afterAdd(user.ID, id, in.URL) })
	}
	job, err := store.GetJob(r.Context(), s.Pool, user.ID, id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.writeJSON(w, status, job)
}

// afterAdd scores a just-added job for the user who added it, offers it
// to everyone else, and starts watching the job board it came from.
func (s *Server) afterAdd(userID, jobID int64, jobURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if s.ScoreNow != nil {
		if err := s.ScoreNow(ctx, userID, jobID); err != nil {
			s.Log.Warn("scoring an added job failed", "user_id", userID, "job_id", jobID, "err", err)
		}
	}
	if s.ShareJob != nil {
		s.ShareJob(ctx, jobID, userID)
	}
	s.watchBoard(ctx, userID, jobID, jobURL)
}

// watchBoard adds the job board an added job came from to the companies
// every run fetches, when the address is on a board Wera reads (Greenhouse,
// Lever, Ashby, SmartRecruiters, Workday), no company has that board yet,
// and the board answers with open jobs.
func (s *Server) watchBoard(ctx context.Context, userID, jobID int64, jobURL string) {
	ats, token, ok := sources.BoardFromURL(jobURL)
	if !ok || s.CheckBoard == nil {
		return
	}
	if can, err := store.CanWatchBoard(ctx, s.Pool, jobID, ats, token); err != nil || !can {
		if err != nil {
			s.Log.Warn("checking an added job's board failed", "job_id", jobID, "err", err)
		}
		return
	}
	n, err := s.CheckBoard(ctx, ats, token)
	if err != nil || n == 0 {
		s.Log.Info("not watching an added job's board", "ats", ats, "token", token, "jobs", n, "err", err)
		return
	}
	name, added, err := store.WatchAddedBoard(ctx, s.Pool, userID, jobID, ats, token)
	if err != nil {
		s.Log.Error("watching an added job's board failed", "job_id", jobID, "err", err)
		return
	}
	if added {
		s.Log.Info("watching a board found through an added job", "company", name, "ats", ats, "token", token, "jobs", n)
	}
}

// jobContext returns a job's company, title and description for prompts:
// the user's job when jobID is set, else the fields sent by the extension.
func (s *Server) jobContext(r *http.Request, jobID int64, company, title, desc string) (string, string, string, error) {
	if jobID <= 0 {
		return company, title, desc, nil
	}
	job, err := store.GetJob(r.Context(), s.Pool, currentUser(r).ID, jobID)
	if err != nil {
		return "", "", "", err
	}
	d, err := store.JobDescription(r.Context(), s.Pool, jobID)
	if err != nil {
		return "", "", "", err
	}
	return job.Company, job.Title, d, nil
}

// answerQuestion is POST /api/ext/answer: a draft answer to one
// application question, for the user to edit.
func (s *Server) answerQuestion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Question     string `json:"question"`
		MaxWords     int    `json:"max_words"`
		Instructions string `json:"instructions"`
		Draft        string `json:"draft"`
		JobID        int64  `json:"job_id"`
		Company      string `json:"company"`
		Title        string `json:"title"`
		Description  string `json:"description"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if q := strings.TrimSpace(in.Question); len(q) < 5 || len(q) > 1000 {
		s.writeError(w, http.StatusBadRequest, "the question needs 5 to 1000 characters")
		return
	}
	// A box whose label is nothing but an order to AI tools asks nothing.
	if scoring.ScrubPosting(in.Question) == "" {
		s.writeError(w, http.StatusBadRequest, "this box asks no question Wera should answer")
		return
	}
	if len(in.Instructions) > 600 || len(in.Draft) > 8000 {
		s.writeError(w, http.StatusBadRequest, "the request or the draft is too long")
		return
	}
	user := currentUser(r)
	prof, err := store.GetProfile(r.Context(), s.Pool, user.ID)
	if err != nil || prof == nil || prof.Markdown == "" {
		s.writeError(w, http.StatusBadRequest, "finish your profile first")
		return
	}
	company, title, desc, err := s.jobContext(r, in.JobID, in.Company, in.Title, in.Description)
	if errors.Is(err, pgx.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "job not found")
		return
	} else if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if !s.aiReady(w, r, "drafting answers") {
		return
	}
	// Answers the user wrote on earlier forms: their own stories and voice, for questions like "Tell me about a time...".
	saved, err := store.ListAnswers(r.Context(), s.Pool, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	var prior []profile.PriorAnswer
	for _, sa := range saved {
		if sa.Kind == "long" {
			prior = append(prior, profile.PriorAnswer{Question: sa.Question, Answer: sa.Answer})
		}
	}
	reply, err := s.chatForUser(r.Context(), user.ID, "answer", 900, profile.AnswerMessages(profile.AnswerInput{
		Question: in.Question, MaxWords: in.MaxWords, Profile: prof.Markdown, Resume: prof.ResumeText,
		Company: company, Title: title, Description: desc, Prior: prior,
		Instructions: strings.TrimSpace(in.Instructions), Draft: strings.TrimSpace(in.Draft), Today: time.Now(),
	}))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again")
		return
	}
	s.writeJSON(w, http.StatusOK, profile.ParseAnswer(reply, in.MaxWords))
}
