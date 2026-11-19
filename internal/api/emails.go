package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"wera/internal/pipeline"
	"wera/internal/profile"
	"wera/internal/scoring"
	"wera/internal/store"
)

// emailDraft is POST /api/jobs/{id}/email-draft {"kind", "request"}: a
// draft of a follow-up, thank-you, referral request or offer reply for a
// tracked job, from the profile, the posting and the tracker. Nothing is
// stored or sent; the user copies it. Facts the AI lacks come back as
// "needs", never in the text.
func (s *Server) emailDraft(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "id", "job")
	if !ok {
		return
	}
	var in struct {
		Kind    string `json:"kind"`
		Request string `json:"request"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if _, ok := profile.EmailKinds[in.Kind]; !ok || len(in.Request) > 1000 {
		s.writeError(w, http.StatusBadRequest, "kind must be follow_up, thank_you, referral_request or offer_reply")
		return
	}
	user := currentUser(r)
	if s.Env == nil || !s.Env.AI.Enabled() {
		s.writeError(w, http.StatusServiceUnavailable, "email drafts are not configured on this server")
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
	if job.AppStatus == nil {
		s.writeError(w, http.StatusConflict, "track this job first: save it or mark it applied")
		return
	}
	events, err := store.JobEvents(r.Context(), s.Pool, user.ID, id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if why := emailNotYet(in.Kind, *job.AppStatus, events); why != "" {
		s.writeError(w, http.StatusConflict, why)
		return
	}
	if !s.emailLimit.Allow(strconv.FormatInt(user.ID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many drafts this hour; try again later")
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
		s.writeError(w, http.StatusPaymentRequired, "drafts are unavailable: "+why)
		return
	}
	desc, err := store.JobDescription(r.Context(), s.Pool, id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	details, err := s.aiDetails(r.Context(), user)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	input := profile.EmailInput{
		Kind: in.Kind, CandidateName: user.Name, Profile: prof.Markdown, Resume: prof.ResumeText,
		Company: job.Company, Title: job.Title, Description: desc, Details: details,
		Status: strings.ReplaceAll(*job.AppStatus, "_", " "), Request: in.Request,
	}
	if job.AppNotes != nil {
		input.Notes = *job.AppNotes
	}
	if c := job.Contact; c != nil {
		parts := []string{c.Name}
		if c.Role != "" {
			parts = append(parts, c.Role)
		}
		input.Contact = strings.Join(parts, ", ")
		if c.Link != "" {
			input.Contact += " (" + c.Link + ")"
		}
	}
	var applied *time.Time
	if err := s.Pool.QueryRow(r.Context(), `SELECT applied_at FROM applications WHERE user_id = $1 AND job_id = $2`, user.ID, id).Scan(&applied); err == nil {
		input.AppliedAt = applied
	}
	{
		for _, e := range events {
			line := fmt.Sprintf("%s on %s", strings.ReplaceAll(e.Kind, "_", " "), e.At.UTC().Format("January 2, 2006"))
			if e.With != "" {
				line += " with " + e.With
			}
			if e.Notes != "" {
				line += ": " + e.Notes
			}
			input.Events = append(input.Events, line)
		}
	}

	msgs := profile.EmailMessages(input)
	var draft profile.Email
	for attempt := range 2 {
		reply, err := s.chatWithModel(r.Context(), user.ID, "email_draft", s.Env.AI.Model, 700, msgs)
		if err != nil {
			s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again")
			return
		}
		var ok bool
		if draft, ok = profile.ParseEmail(reply); !ok {
			s.writeError(w, http.StatusBadGateway, "the AI returned an unusable draft; try again")
			return
		}
		fixes := profile.EmailFixes(in.Kind, draft)
		if len(fixes) == 0 {
			break
		}
		if attempt == 1 {
			s.writeError(w, http.StatusBadGateway, "the AI couldn't write a draft that keeps to the facts; try again")
			return
		}
		msgs = append(msgs,
			scoring.Message{Role: "assistant", Content: reply},
			scoring.Message{Role: "user", Content: profile.ReworkFor(fixes)})
	}
	if draft.Subject == "" {
		draft.Subject = job.Title + " at " + job.Company
	}
	s.writeJSON(w, http.StatusOK, draft)
}

// emailNotYet says why a kind of email doesn't fit the application yet,
// or "" when it does: a follow-up needs an application, a thank-you an
// interview, an offer reply an offer. A draft written on a false premise
// ("following up on my application" for a job only saved) misleads.
func emailNotYet(kind, status string, events []store.Event) string {
	sent := status == "applied" || status == "interviewing" || status == "offer"
	switch kind {
	case "follow_up":
		if !sent {
			return "mark the job applied first: a follow-up is about an application"
		}
	case "thank_you":
		interviewed := status == "interviewing" || status == "offer"
		for _, e := range events {
			if e.Kind != "assessment" && e.At.Before(time.Now()) {
				interviewed = true
			}
		}
		if !interviewed {
			return "add the interview first (or mark the job interviewing): a thank-you follows one"
		}
	case "offer_reply":
		if status != "offer" {
			return "mark the job Offer first: this replies to an offer"
		}
	}
	return ""
}
