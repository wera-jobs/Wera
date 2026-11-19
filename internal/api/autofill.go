package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"wera/internal/normalize"
	"wera/internal/profile"
	"wera/internal/store"
)

// selfID are application details the AI never sees: forms ask them for
// equal-opportunity reporting, and only the user's own choice fills them.
var selfID = map[string]bool{
	"gender": true, "pronouns": true, "transgender": true, "sexual_orientation": true,
	"race": true, "hispanic": true, "veteran": true, "disability": true,
}

// suggestFill is POST /api/ext/autofill: the AI answers the fields of one
// form page the extension could not fill from the details alone, from
// the profile, resume, details and earlier answers. Nothing is saved.
func (s *Server) suggestFill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Fields      []profile.FormField `json:"fields"`
		JobID       int64               `json:"job_id"`
		Company     string              `json:"company"`
		Title       string              `json:"title"`
		Description string              `json:"description"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if len(in.Fields) == 0 || len(in.Fields) > profile.MaxAutofillFields {
		s.writeError(w, http.StatusBadRequest, "send 1 to 60 fields")
		return
	}
	for i := range in.Fields {
		f := &in.Fields[i]
		if f.ID == "" || len(f.ID) > 40 || len(f.Label) > 1000 || len(f.Options) > 120 {
			s.writeError(w, http.StatusBadRequest, "a field is malformed or too long")
			return
		}
		for _, o := range f.Options {
			if len(o) > 300 {
				s.writeError(w, http.StatusBadRequest, "an option is too long")
				return
			}
		}
	}
	in.Description = normalize.Clip(in.Description, 60000)
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
	if !s.aiReady(w, r, "suggesting answers") {
		return
	}
	details, err := s.aiDetails(r.Context(), user)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	saved, err := store.ListAnswers(r.Context(), s.Pool, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	prior := make([]profile.PriorAnswer, 0, len(saved))
	for _, sa := range saved {
		if sa.Kind != "long" {
			prior = append(prior, profile.PriorAnswer{Question: sa.Question, Answer: sa.Answer})
		}
	}
	now := time.Now()
	reply, err := s.chatForUser(r.Context(), user.ID, "autofill", 2000, profile.AutofillMessages(profile.AutofillInput{
		Fields: in.Fields, Profile: prof.Markdown, Resume: prof.ResumeText, Details: details, Prior: prior,
		Company: company, Title: title, Description: desc, Today: now,
	}))
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI service did not answer; try again")
		return
	}
	out, err := profile.ParseAutofill(reply, in.Fields, now)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "the AI returned unusable answers; try again")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"answers": out})
}

// aiDetails are the user's application details the AI may see: every
// filled one except self-identification. They win over the profile text
// and resume wherever those disagree (see profile.FactsBlock).
func (s *Server) aiDetails(ctx context.Context, user *store.User) (map[string]string, error) {
	view, err := s.applicantFor(ctx, user)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(view.Applicant)
	var all map[string]string
	_ = json.Unmarshal(raw, &all)
	details := map[string]string{}
	for k, v := range all {
		if v != "" && !selfID[k] {
			details[k] = v
		}
	}
	return details, nil
}
