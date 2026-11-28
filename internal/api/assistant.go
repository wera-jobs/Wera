package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"wera/internal/profile"
	"wera/internal/scoring"
	"wera/internal/store"
)

// assistantHistory is how many earlier turns of a thread go with a question.
const assistantHistory = 10

type askInput struct {
	ThreadID int64  `json:"thread_id"`
	JobID    int64  `json:"job_id"`
	Message  string `json:"message"`
}

// askAssistant is POST /api/assistant {"message", "thread_id"?, "job_id"?}:
// an answer grounded on the user's data, streamed as server-sent events.
// "thread" comes first ({"thread_id"}), then "delta" pieces ({"text"}),
// then "done" ({"message_id", "content", "sources"}), or "error"
// ({"error"}) when the AI fails partway. Failures before the stream starts
// are ordinary JSON errors.
func (s *Server) askAssistant(w http.ResponseWriter, r *http.Request) {
	var in askInput
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	in.Message = strings.TrimSpace(in.Message)
	if in.Message == "" || utf8.RuneCountInString(in.Message) > 2000 {
		s.writeError(w, http.StatusBadRequest, "ask a question of at most 2000 characters")
		return
	}
	user := currentUser(r)
	ctx := r.Context()

	var thread *store.AssistantThread
	var history []scoring.Message
	if in.ThreadID > 0 {
		t, err := store.GetAssistantThread(ctx, s.Pool, user.ID, in.ThreadID)
		if errors.Is(err, store.ErrThreadNotFound) {
			s.writeError(w, http.StatusNotFound, "conversation not found")
			return
		}
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		thread = t
		msgs, err := store.AssistantMessages(ctx, s.Pool, user.ID, t.ID)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		for _, m := range msgs[max(0, len(msgs)-assistantHistory):] {
			history = append(history, scoring.Message{Role: m.Role, Content: m.Content})
		}
		if t.JobID != nil {
			in.JobID = *t.JobID
		}
	}

	prof, err := store.GetProfile(ctx, s.Pool, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if prof == nil || strings.TrimSpace(prof.Markdown) == "" {
		s.writeError(w, http.StatusBadRequest, "finish your profile first")
		return
	}
	var job *profile.AssistantJob
	var jobTitle string
	if in.JobID > 0 {
		v, err := store.GetJob(ctx, s.Pool, user.ID, in.JobID)
		if errors.Is(err, pgx.ErrNoRows) {
			s.writeError(w, http.StatusNotFound, "job not found")
			return
		}
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		if job, err = s.assistantJob(ctx, user.ID, v); err != nil {
			s.Log.Error("assistant job context failed", "err", err)
			s.writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		jobTitle = v.Title + " at " + v.Company
	}

	if s.Env == nil || !s.Env.AI.Enabled() {
		s.writeError(w, http.StatusServiceUnavailable, "Ask Wera is not configured on this server")
		return
	}
	if limit := s.Env.AssistantPerDay; limit > 0 {
		n, err := store.AssistantQuestionsSince(ctx, s.Pool, user.ID, time.Now().Add(-24*time.Hour))
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		if n >= limit {
			s.writeError(w, http.StatusTooManyRequests, fmt.Sprintf("you have asked %d questions in the last 24 hours, the daily limit; try again later", limit))
			return
		}
	}
	if !s.aiReady(w, r, "Ask Wera") {
		return
	}
	details, err := s.aiDetails(ctx, user)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	tracked, err := store.TrackedApplications(ctx, s.Pool, user.ID, 60)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	tracker := make([]profile.AssistantTracked, len(tracked))
	for i, a := range tracked {
		tracker[i] = profile.AssistantTracked{Company: a.Company, Title: a.Title, Status: a.Status, AppliedAt: a.AppliedAt}
	}

	if thread == nil {
		title := in.Message
		if jobTitle != "" {
			title = jobTitle
		}
		var jobID *int64
		if in.JobID > 0 {
			jobID = &in.JobID
		}
		if thread, err = store.CreateAssistantThread(ctx, s.Pool, user.ID, jobID, cutTitle(title)); err != nil {
			s.writeError(w, http.StatusInternalServerError, "save failed")
			return
		}
	}
	if _, err := store.AddAssistantMessage(ctx, s.Pool, user.ID, thread.ID, "user", in.Message, nil); err != nil {
		s.writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	msgs := profile.AssistantMessages(profile.AssistantInput{
		Details: details, Profile: prof.Markdown, Resume: prof.ResumeText, Job: job,
		Tracker: tracker, History: history, Question: in.Message,
	})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // nginx in front must not hold the stream
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		_ = rc.Flush()
	}
	send("thread", map[string]any{"thread_id": thread.ID, "title": thread.Title, "job_id": thread.JobID})

	comp, err := s.streamForUser(ctx, user.ID, "assistant", 900, msgs, func(d string) {
		send("delta", map[string]string{"text": d})
	})
	answer := ""
	if comp != nil {
		answer = profile.CleanAssistant(comp.Content)
	}
	if err != nil && answer == "" {
		send("error", map[string]string{"error": "the AI service did not answer; try again"})
		return
	}
	sources := profile.AnswerSources(answer)
	saved, serr := store.AddAssistantMessage(context.WithoutCancel(ctx), s.Pool, user.ID, thread.ID, "assistant", answer, sources)
	if serr != nil {
		s.Log.Error("saving assistant answer failed", "err", serr)
		send("error", map[string]string{"error": "the answer could not be saved"})
		return
	}
	send("done", map[string]any{"message_id": saved.ID, "content": answer, "sources": sources, "cut_short": err != nil})
}

// streamForUser is chatWithModel for a streamed reply on the scoring model:
// the usage is recorded against the user's budget like any other call.
func (s *Server) streamForUser(ctx context.Context, userID int64, kind string, maxTokens int, msgs []scoring.Message, onDelta func(string)) (*scoring.Completion, error) {
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	model := s.Env.AI.Model
	opts := s.Env.AI.Options(model)
	opts.Timeout, opts.MaxTokens, opts.Log = 95*time.Second, maxTokens, s.Log
	client := scoring.NewClient(opts)
	start := time.Now()
	comp, err := client.ChatStream(ctx, msgs, onDelta)
	if err != nil {
		s.Log.Error("llm stream failed", "kind", kind, "user_id", userID, "err", err)
	}
	if comp == nil {
		return nil, err
	}
	cost := 0.0
	if comp.CostUSD != nil {
		cost = *comp.CostUSD
	} else if prices, ok := scoring.PriceFor(model); ok {
		cost = prices.CostUSD(comp.Usage.PromptTokens, comp.Usage.CachedTokens, comp.Usage.CompletionTokens)
	}
	if rerr := store.RecordLLMUsage(context.WithoutCancel(ctx), s.Pool, userID, kind, model,
		comp.Usage.PromptTokens, comp.Usage.CachedTokens, comp.Usage.CompletionTokens, cost); rerr != nil {
		s.Log.Error("recording llm usage failed", "kind", kind, "user_id", userID, "err", rerr)
	}
	s.Log.Info("llm call", "kind", kind, "user_id", userID, "cost_usd", cost, "ms", time.Since(start).Milliseconds())
	return comp, err
}

// assistantJob is what the assistant knows of one job: the posting, its
// facts digest, Wera's fit review, the H-1B summary and the tracker state.
func (s *Server) assistantJob(ctx context.Context, userID int64, v *store.JobView) (*profile.AssistantJob, error) {
	desc, err := store.JobDescription(ctx, s.Pool, v.ID)
	if err != nil {
		return nil, err
	}
	j := &profile.AssistantJob{
		Company: v.Company, Title: v.Title, Location: v.LocationRaw, Description: desc,
		FitScore: v.FitScore, SkillsMatched: v.SkillsMatched, SkillsMissing: v.SkillsMissing,
		AppliedAt: v.AppliedAt, Pay: payText(v),
	}
	if v.LocationSummary != nil && *v.LocationSummary != "" {
		j.Location = *v.LocationSummary
	}
	if v.Verdict != nil {
		j.Verdict = *v.Verdict
	}
	if v.Reason != nil {
		j.Reason = *v.Reason
	}
	if v.AppStatus != nil {
		j.Status = *v.AppStatus
	}
	if v.NextStep != nil {
		j.NextStep = *v.NextStep
	}
	facts, err := store.FactsFor(ctx, s.Pool, []int64{v.ID})
	if err != nil {
		return nil, err
	}
	if f := facts[v.ID]; f != nil {
		j.Digest = f.Digest
	}
	if h, err := store.JobH1BFor(ctx, s.Pool, v.CompanyID, v.Title, ""); err == nil && h != nil {
		j.H1B = h1bText(v.Company, h)
	}
	return j, nil
}

// payText is a posting's pay as one phrase, "" when it gives none.
func payText(v *store.JobView) string {
	if v.PayMin == nil && v.PayMax == nil {
		return ""
	}
	n := func(f *float64) string { return strconv.FormatFloat(*f, 'f', -1, 64) }
	var s string
	switch {
	case v.PayMin != nil && v.PayMax != nil && *v.PayMin != *v.PayMax:
		s = n(v.PayMin) + " to " + n(v.PayMax)
	case v.PayMin != nil:
		s = n(v.PayMin)
	default:
		s = n(v.PayMax)
	}
	if v.PayCurrency != nil {
		s += " " + *v.PayCurrency
	}
	if v.PayPeriod != nil {
		s += " per " + *v.PayPeriod
	}
	return s
}

// h1bText summarizes a job's H-1B data in a sentence or two.
func h1bText(company string, h *store.JobH1B) string {
	if !h.Matched {
		return fmt.Sprintf("No H-1B filings were found for %s in the data.", company)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s filed %d H-1B Labor Condition Applications (%d for new hires)", company, h.Filings, h.NewHires)
	if h.Period.From != nil && h.Period.To != nil {
		fmt.Fprintf(&b, " between %s and %s", h.Period.From.Format("Jan 2006"), h.Period.To.Format("Jan 2006"))
	}
	b.WriteString(".")
	if h.WageMedian != nil {
		fmt.Fprintf(&b, " Median filed wage: %.0f USD a year.", *h.WageMedian)
	}
	if h.SimilarCount > 0 && h.SimilarMid != nil {
		fmt.Fprintf(&b, " For titles like this one, %d filings with a median of %.0f USD", h.SimilarCount, *h.SimilarMid)
		if h.SimilarLow != nil && h.SimilarHi != nil {
			fmt.Fprintf(&b, " (middle half %.0f to %.0f)", *h.SimilarLow, *h.SimilarHi)
		}
		b.WriteString(".")
	}
	return b.String()
}

// cutTitle shortens a thread's title to a line.
func cutTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		return string(r[:77]) + "..."
	}
	return s
}

// assistantThreads is GET /api/assistant/threads?job_id=: the user's
// conversations, latest first.
func (s *Server) assistantThreads(w http.ResponseWriter, r *http.Request) {
	var jobID *int64
	if v := r.URL.Query().Get("job_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			s.writeError(w, http.StatusBadRequest, "job_id must be a number")
			return
		}
		jobID = &id
	}
	ts, err := store.AssistantThreads(r.Context(), s.Pool, currentUser(r).ID, jobID, 50)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	out := map[string]any{"threads": ts}
	if s.Env != nil {
		left := s.Env.AssistantPerDay
		if left > 0 {
			n, err := store.AssistantQuestionsSince(r.Context(), s.Pool, currentUser(r).ID, time.Now().Add(-24*time.Hour))
			if err == nil {
				left = max(0, left-n)
			}
		}
		out["daily_limit"], out["questions_left"] = s.Env.AssistantPerDay, left
		out["enabled"] = s.Env.AI.Enabled()
	}
	s.writeJSON(w, http.StatusOK, out)
}

// assistantThread is GET /api/assistant/threads/{id}: one conversation.
func (s *Server) assistantThread(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "conversation")
	if !ok {
		return
	}
	uid := currentUser(r).ID
	t, err := store.GetAssistantThread(r.Context(), s.Pool, uid, id)
	if errors.Is(err, store.ErrThreadNotFound) {
		s.writeError(w, http.StatusNotFound, "conversation not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	msgs, err := store.AssistantMessages(r.Context(), s.Pool, uid, id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"thread": t, "messages": msgs})
}

// deleteAssistantThread is DELETE /api/assistant/threads/{id}.
func (s *Server) deleteAssistantThread(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "conversation")
	if !ok {
		return
	}
	err := store.DeleteAssistantThread(r.Context(), s.Pool, currentUser(r).ID, id)
	if errors.Is(err, store.ErrThreadNotFound) {
		s.writeError(w, http.StatusNotFound, "conversation not found")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteAssistantThreads is DELETE /api/assistant/threads: every conversation.
func (s *Server) deleteAssistantThreads(w http.ResponseWriter, r *http.Request) {
	if err := store.DeleteAssistantThreads(r.Context(), s.Pool, currentUser(r).ID); err != nil {
		s.writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
