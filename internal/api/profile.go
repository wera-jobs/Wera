package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"wera/internal/config"
	"wera/internal/normalize"
	"wera/internal/pipeline"
	"wera/internal/profile"
	"wera/internal/scoring"
	"wera/internal/store"
)

// profileOptions is GET /api/profile/options: everything the signup and
// profile forms let a user pick from.
func (s *Server) profileOptions(w http.ResponseWriter, _ *http.Request) {
	// Role families A to Z by label, so every client lists them the same
	// way; levels keep their seniority order.
	families := slices.Clone(s.Roles.RoleFamilies)
	slices.SortFunc(families, func(a, b config.RoleFamily) int {
		return strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label))
	})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"fields":        s.Roles.Fields,
		"role_families": families,
		"levels":        s.Roles.Levels,
		"industries":    s.industriesAZ(),
	})
}

// profileView is GET /api/profile.
type profileView struct {
	Markdown    string             `json:"markdown"`
	Preferences config.Preferences `json:"preferences"`
	Answers     json.RawMessage    `json:"answers"`
	ResumeChars int                `json:"resume_chars"` // 0 = no resume on file
	ResumeText  string             `json:"resume_text"`
	ResumeFile  *resumeFileView    `json:"resume_file"` // nil when only text was pasted
	Ready       bool               `json:"ready"`       // matching runs for this profile
	UpdatedAt   *time.Time         `json:"updated_at"`
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	p, err := store.GetProfile(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.Log.Error("get profile failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	v := profileView{Answers: json.RawMessage(`{}`), Preferences: config.Preferences{
		RoleFamilies: []string{}, Levels: []string{}}}
	if p != nil {
		v.Markdown, v.Preferences, v.Ready = p.Markdown, p.Preferences, p.Ready()
		v.ResumeChars = len([]rune(p.ResumeText))
		v.ResumeText = p.ResumeText
		if len(p.Answers) > 0 {
			v.Answers = p.Answers
		}
		v.UpdatedAt = &p.UpdatedAt
	}
	name, uploaded, ok, err := store.ResumeFileInfo(r.Context(), s.Pool, currentUser(r).ID)
	if err == nil && ok {
		v.ResumeFile = &resumeFileView{Filename: name, UploadedAt: uploaded}
	}
	s.writeJSON(w, http.StatusOK, v)
}

type resumeFileView struct {
	Filename   string    `json:"filename"`
	UploadedAt time.Time `json:"uploaded_at"`
}

// uploadResume is POST /api/profile/resume: a multipart form with either a
// PDF in "file" or pasted text in "text". Only the extracted text is kept.
func (s *Server) uploadResume(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, profile.MaxResumeBytes+64<<10)
	if err := r.ParseMultipartForm(profile.MaxResumeBytes); err != nil { //nolint:gosec // the body is capped by MaxBytesReader above
		s.writeError(w, http.StatusBadRequest, "upload a PDF of at most 5 MB, or paste the text")
		return
	}
	var text string
	if f, hdr, err := r.FormFile("file"); err == nil {
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "could not read the upload")
			return
		}
		if text, err = profile.ExtractPDFText(data); err != nil {
			s.writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		// Keep the PDF itself so the user can view what they uploaded.
		if err := store.SaveResumeFile(r.Context(), s.Pool, currentUser(r).ID, safeFilename(hdr.Filename), data); err != nil {
			s.Log.Error("save resume file failed", "err", err)
		}
	} else {
		text = profile.CleanText(r.FormValue("text"))
		if len(text) < 50 {
			s.writeError(w, http.StatusBadRequest, "upload a PDF or paste at least a few lines of your resume")
			return
		}
	}
	if err := store.SaveResumeText(r.Context(), s.Pool, currentUser(r).ID, text); err != nil {
		s.Log.Error("save resume failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not save the resume")
		return
	}
	s.importInBackground(currentUser(r).ID, text)
	s.writeJSON(w, http.StatusOK, map[string]any{"resume_chars": len([]rune(text)), "preview": normalize.Clip(text, 600)})
}

type profileInput struct {
	Markdown    string             `json:"markdown"`
	Preferences config.Preferences `json:"preferences"`
	Answers     profile.Answers    `json:"answers"`
}

func (s *Server) validateInput(in *profileInput) error {
	if err := in.Preferences.Validate(s.Roles); err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, i := range s.Industries {
		ids[i.ID] = true
	}
	return in.Answers.Validate(ids)
}

// draftProfile is POST /api/profile/draft: the LLM writes a profile from
// the stored resume text and the submitted answers and preferences. The
// draft is returned for the user to review; nothing is saved except the
// cost, which counts toward the user's budget.
func (s *Server) draftProfile(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if s.Env == nil || !s.Env.AI.Enabled() {
		s.writeError(w, http.StatusServiceUnavailable, "profile drafting is not configured on this server")
		return
	}
	if !s.draftLimit.Allow(strconv.FormatInt(user.ID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many drafts; try again in an hour or edit the profile by hand")
		return
	}
	var in profileInput
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if err := s.validateInput(&in); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	allowance, why, err := pipeline.Allowance(r.Context(), s.Pool, s.Env, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "budget check failed")
		return
	}
	if allowance <= 0 {
		s.writeError(w, http.StatusPaymentRequired, "AI drafting is unavailable: "+why)
		return
	}
	p, err := store.GetProfile(r.Context(), s.Pool, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	resume := ""
	if p != nil {
		resume = p.ResumeText
	}
	if resume == "" && strings.TrimSpace(in.Answers.TargetRoles+in.Answers.CurrentTitle+in.Answers.Notes) == "" {
		s.writeError(w, http.StatusBadRequest, "upload a resume or describe your background first")
		return
	}

	content, err := s.chatForUser(r.Context(), user.ID, "profile", 2500,
		profile.DraftMessages(resume, in.Answers, in.Preferences, s.Roles, s.Industries))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again or write the profile by hand")
		return
	}
	draft := profile.CleanDraft(content)
	if draft == "" {
		s.writeError(w, http.StatusBadGateway, "the AI returned an empty draft; try again")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"markdown": draft})
}

// putProfile is PUT /api/profile: save the reviewed profile text,
// preferences and answers, then match the user's jobs in the background.
func (s *Server) putProfile(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var in profileInput
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	in.Markdown = strings.TrimSpace(in.Markdown)
	switch {
	case in.Markdown == "":
		s.writeError(w, http.StatusBadRequest, "the profile text is empty")
		return
	case len(in.Markdown) > profile.MaxProfileChars:
		s.writeError(w, http.StatusBadRequest, "the profile text is too long (12,000 characters max)")
		return
	}
	if err := s.validateInput(&in); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	prev, err := store.GetProfile(r.Context(), s.Pool, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	answers, _ := json.Marshal(in.Answers)
	hash := scoring.ProfileHash([]byte(in.Markdown))
	// Text that differs only in spacing reads the same to the AI: it keeps
	// its hash, so saving it does not score every job again. (Profiles
	// stored before saves were trimmed end in a newline.)
	if prev != nil && prev.ProfileHash != "" && sameText(prev.Markdown, in.Markdown) {
		hash = prev.ProfileHash
	}
	if err := store.SaveProfile(r.Context(), s.Pool, user.ID, in.Markdown, hash, in.Preferences, answers); err != nil {
		s.Log.Error("save profile failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not save the profile")
		return
	}
	// A first profile waits for its matches (seconds, no LLM), so Today is
	// populated when this request returns. Later saves keep the matches and
	// scores the user has, so the rematch runs in the background.
	if s.PrepareUser != nil && (prev == nil || !prev.Ready()) {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		if err := s.PrepareUser(ctx, user.ID); err != nil {
			s.Log.Warn("preparing matches failed; the background match will retry", "user_id", user.ID, "err", err)
		}
		cancel()
	}
	if s.MatchUser != nil {
		// Matching outlives the request on purpose.
		s.background("match user", func() { s.MatchUser(context.Background(), user.ID) })
	}
	s.getProfile(w, r)
}

// sameText reports whether two profile texts differ only in spacing at
// the ends of lines and of the text.
func sameText(a, b string) bool {
	norm := func(s string) string {
		lines := strings.Split(strings.TrimSpace(s), "\n")
		for i, l := range lines {
			lines[i] = strings.TrimRightFunc(l, unicode.IsSpace)
		}
		return strings.Join(lines, "\n")
	}
	return norm(a) == norm(b)
}

// chatForUser makes one AI chat call on behalf of a user and records
// its cost (kind names the purpose in llm_usage). It returns the reply.
func (s *Server) chatForUser(ctx context.Context, userID int64, kind string, maxTokens int, msgs []scoring.Message) (string, error) {
	return s.chatWithModel(ctx, userID, kind, s.Env.AI.Model, maxTokens, msgs)
}

// chatWithModel is chatForUser with an explicit model.
func (s *Server) chatWithModel(ctx context.Context, userID int64, kind, model string, maxTokens int, msgs []scoring.Message) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 110*time.Second)
	defer cancel()
	opts := s.Env.AI.Options(model)
	opts.Timeout, opts.MaxTokens, opts.Log = 105*time.Second, maxTokens, s.Log
	client := scoring.NewClient(opts)
	start := time.Now()
	comp, err := client.Chat(ctx, "", msgs)
	if err != nil {
		s.Log.Error("llm call failed", "kind", kind, "user_id", userID, "err", err)
		return "", err
	}
	cost := 0.0
	if comp.CostUSD != nil {
		cost = *comp.CostUSD
	} else if prices, ok := scoring.PriceFor(model); ok {
		cost = prices.CostUSD(comp.Usage.PromptTokens, comp.Usage.CachedTokens, comp.Usage.CompletionTokens)
	}
	if err := store.RecordLLMUsage(context.Background(), s.Pool, userID, kind, model,
		comp.Usage.PromptTokens, comp.Usage.CachedTokens, comp.Usage.CompletionTokens, cost); err != nil {
		s.Log.Error("recording llm usage failed", "kind", kind, "user_id", userID, "err", err)
	}
	s.Log.Info("llm call", "kind", kind, "user_id", userID, "cost_usd", cost,
		"ms", time.Since(start).Milliseconds())
	return comp.Content, nil
}

// suggestPreferences is POST /api/profile/suggest: infer role families,
// levels, experience and locations from the stored resume so the signup
// form starts pre-filled. Nothing is saved; the cost counts toward the
// user's budget.
func (s *Server) suggestPreferences(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if s.Env == nil || !s.Env.AI.Enabled() {
		s.writeError(w, http.StatusServiceUnavailable, "suggestions are not configured on this server")
		return
	}
	if !s.draftLimit.Allow(strconv.FormatInt(user.ID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many AI requests; try again in an hour")
		return
	}
	p, err := store.GetProfile(r.Context(), s.Pool, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if p == nil || p.ResumeText == "" {
		s.writeError(w, http.StatusBadRequest, "upload a resume first")
		return
	}
	allowance, why, err := pipeline.Allowance(r.Context(), s.Pool, s.Env, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "budget check failed")
		return
	}
	if allowance <= 0 {
		s.writeError(w, http.StatusPaymentRequired, "AI suggestions are unavailable: "+why)
		return
	}
	content, err := s.chatForUser(r.Context(), user.ID, "suggest", 600, profile.SuggestMessages(p.ResumeText, s.Roles))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI service did not answer; fill the form in by hand")
		return
	}
	sug, err := profile.ParseSuggestions(content, s.Roles)
	if err != nil {
		s.Log.Warn("unreadable suggestions", "user_id", user.ID, "err", err)
		s.writeError(w, http.StatusBadGateway, "the AI's suggestions were unreadable; fill the form in by hand")
		return
	}
	s.writeJSON(w, http.StatusOK, sug)
}
