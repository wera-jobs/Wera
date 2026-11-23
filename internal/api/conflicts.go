package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"wera/internal/config"
	"wera/internal/profile"
	"wera/internal/store"
)

// The same facts live in the application details (what the extension
// fills and the AI takes as the record), the profile's own answers (what
// the profile text and matching come from) and the matching settings.
// When the person has saved details that disagree with the others, the
// dashboard lists each disagreement and the person says which is right.

// conflict is one fact two places disagree on.
type conflict struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Details   string `json:"details"`   // what the application details say, in words
	Elsewhere string `json:"elsewhere"` // what the profile says, in words
	// Redraft: keeping the details leaves the profile text saying the old thing until it is redrafted.
	Redraft bool `json:"redraft"`
}

// workAuthOf reads the details' two yes/no answers as the profile's choice ("" when they don't say).
func workAuthOf(authorized, sponsorship string) string {
	switch authorized + "/" + sponsorship {
	case "yes/no":
		return "citizen_or_resident"
	case "yes/yes":
		return "sponsorship_future"
	case "no/yes":
		return "sponsorship_now"
	}
	return ""
}

// detailsOfWorkAuth is the inverse: the details' answers for a profile choice.
func detailsOfWorkAuth(choice string) (authorized, sponsorship string, ok bool) {
	switch choice {
	case "citizen_or_resident":
		return "yes", "no", true
	case "sponsorship_future":
		return "yes", "yes", true
	case "sponsorship_now":
		return "no", "yes", true
	}
	return "", "", false
}

var workAuthWords = map[string]string{
	"citizen_or_resident": "Authorized to work, no sponsorship needed",
	"sponsorship_future":  "Authorized now, will need sponsorship later",
	"sponsorship_now":     "Needs sponsorship to work now",
}

// findConflicts compares saved details with the profile's answers and matching settings.
func findConflicts(a *store.Applicant, ans profile.Answers, prefs config.Preferences) []conflict {
	out := []conflict{}
	if mine := workAuthOf(a.AuthorizedToWork, a.NeedsSponsorship); mine != "" {
		theirs := ans.WorkAuthorization
		switch {
		case theirs != "" && theirs != "outside_us" && theirs != mine:
			out = append(out, conflict{Key: "work_authorization", Label: "Work authorization and sponsorship",
				Details: workAuthWords[mine], Elsewhere: workAuthWords[theirs], Redraft: true})
		case prefs.NeedsSponsorship != (a.NeedsSponsorship == "yes"):
			say := map[bool]string{true: "Show only jobs open to sponsorship", false: "No sponsorship needed"}
			out = append(out, conflict{Key: "work_authorization", Label: "Work authorization and sponsorship",
				Details: workAuthWords[mine], Elsewhere: say[prefs.NeedsSponsorship]})
		}
	}
	if d, p := strings.TrimSpace(a.CurrentTitle), strings.TrimSpace(ans.CurrentTitle); d != "" && p != "" && !strings.EqualFold(d, p) {
		out = append(out, conflict{Key: "current_title", Label: "Current title", Details: d, Elsewhere: p, Redraft: true})
	}
	if d, err := strconv.ParseFloat(strings.TrimSpace(a.YearsExperience), 64); err == nil && ans.YearsExperience != nil && math.Abs(d-*ans.YearsExperience) >= 1 {
		out = append(out, conflict{Key: "years_experience", Label: "Years of experience",
			Details: strconv.FormatFloat(d, 'f', -1, 64), Elsewhere: strconv.FormatFloat(*ans.YearsExperience, 'f', -1, 64), Redraft: true})
	}
	return out
}

// conflictsFor loads what findConflicts compares; nil when the person never saved details (nothing can disagree).
func (s *Server) conflictsFor(ctx context.Context, user *store.User) (*store.Applicant, *store.Profile, profile.Answers, []conflict, error) {
	var ans profile.Answers
	a, saved, err := store.GetApplicant(ctx, s.Pool, user)
	if err != nil || !saved {
		return a, nil, ans, []conflict{}, err
	}
	prof, err := store.GetProfile(ctx, s.Pool, user.ID)
	if errors.Is(err, pgx.ErrNoRows) || prof == nil {
		return a, nil, ans, []conflict{}, nil
	} else if err != nil {
		return nil, nil, ans, nil, err
	}
	if len(prof.Answers) > 0 {
		_ = json.Unmarshal(prof.Answers, &ans)
	}
	return a, prof, ans, findConflicts(a, ans, prof.Preferences), nil
}

// applicantConflicts is GET /api/applicant/conflicts.
func (s *Server) applicantConflicts(w http.ResponseWriter, r *http.Request) {
	_, _, _, list, err := s.conflictsFor(r.Context(), currentUser(r))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"conflicts": list})
}

// resolveConflict is POST /api/applicant/conflicts/resolve {key, keep}: keep "details"
// updates the profile's answers (and matching) to match; keep "profile" updates the details.
func (s *Server) resolveConflict(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key  string `json:"key"`
		Keep string `json:"keep"`
	}
	if err := decodeJSON(r, &in); err != nil || (in.Keep != "details" && in.Keep != "profile") {
		s.writeError(w, http.StatusBadRequest, `keep must be "details" or "profile"`)
		return
	}
	user := currentUser(r)
	a, prof, ans, list, err := s.conflictsFor(r.Context(), user)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	found := false
	for _, c := range list {
		found = found || c.Key == in.Key
	}
	if !found {
		s.writeError(w, http.StatusNotFound, "no such disagreement")
		return
	}
	prefs := prof.Preferences
	if in.Keep == "profile" {
		switch in.Key {
		case "work_authorization":
			if au, sp, ok := detailsOfWorkAuth(ans.WorkAuthorization); ok {
				a.AuthorizedToWork, a.NeedsSponsorship = au, sp
			} else {
				a.NeedsSponsorship = map[bool]string{true: "yes", false: "no"}[prefs.NeedsSponsorship]
			}
		case "current_title":
			a.CurrentTitle = ans.CurrentTitle
		case "years_experience":
			a.YearsExperience = strconv.FormatFloat(*ans.YearsExperience, 'f', -1, 64)
		}
		if err := store.SaveApplicant(r.Context(), s.Pool, user.ID, a); err != nil {
			s.writeError(w, http.StatusInternalServerError, "save failed")
			return
		}
	} else {
		switch in.Key {
		case "work_authorization":
			ans.WorkAuthorization = workAuthOf(a.AuthorizedToWork, a.NeedsSponsorship)
			prefs.NeedsSponsorship = a.NeedsSponsorship == "yes"
		case "current_title":
			ans.CurrentTitle = a.CurrentTitle
		case "years_experience":
			y, _ := strconv.ParseFloat(strings.TrimSpace(a.YearsExperience), 64)
			ans.YearsExperience = &y
		}
		raw, _ := json.Marshal(ans)
		if err := store.SavePreferences(r.Context(), s.Pool, user.ID, prefs, raw); err != nil {
			s.writeError(w, http.StatusInternalServerError, "save failed")
			return
		}
		// Sponsorship changes which jobs are filtered out. A profile with
		// text (every profile that can have a conflict) is filtered by
		// PrepareUser, which also checks scored jobs and known facts
		// again; Prefilter only covers setup, before the text exists.
		refilter := s.PrepareUser
		if refilter == nil {
			refilter = s.Prefilter
		}
		if in.Key == "work_authorization" && refilter != nil {
			s.background("refilter", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				if err := refilter(ctx, user.ID); err != nil {
					s.Log.Warn("refilter after a resolved conflict failed", "user_id", user.ID, "err", err)
				}
			})
		}
	}
	s.applicantConflicts(w, r)
}
