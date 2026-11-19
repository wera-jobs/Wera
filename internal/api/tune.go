package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"wera/internal/config"
	"wera/internal/store"
)

// Repeated Not interested reasons suggest a preference change, which the
// user accepts or ignores: nothing here changes preferences by itself.
const (
	tuneRepeats = 3                   // the same reason this many times...
	tuneWindow  = 14 * 24 * time.Hour // ...within this long
)

// Suggestion is one proposed preference change. Preferences is the full
// set with the change made, ready to save; nil means there is no single
// change to make and the user should look at their preferences.
type Suggestion struct {
	Reason      string              `json:"reason"`
	Count       int                 `json:"count"`
	Text        string              `json:"text"`
	Preferences *config.Preferences `json:"preferences"`
}

// suggestTunes turns recent dismissals into suggestions, in a fixed order.
func suggestTunes(prefs config.Preferences, levels []config.Level, ds []store.Dismissal) []Suggestion {
	by := map[string][]store.Dismissal{}
	for _, d := range ds {
		by[d.Reason] = append(by[d.Reason], d)
	}
	label := func(id string) string {
		for _, l := range levels {
			if l.ID == id {
				return strings.ToLower(l.Label)
			}
		}
		return id
	}
	out := []Suggestion{}
	if got := by["too_senior"]; len(got) >= tuneRepeats {
		if s, ok := tooSenior(prefs, levels, got, label); ok {
			out = append(out, s)
		}
	}
	if got := by["too_junior"]; len(got) >= tuneRepeats {
		if s, ok := dropLevel(prefs, levels, got, label, "too junior"); ok {
			out = append(out, s)
		}
	}
	for _, r := range []struct{ reason, text string }{
		{"location", "You passed on %d jobs for where they are. Review where you want to work?"},
		{"field", "You passed on %d jobs as not your field. Review the roles you're looking for?"},
	} {
		if n := len(by[r.reason]); n >= tuneRepeats {
			out = append(out, Suggestion{Reason: r.reason, Count: n, Text: fmt.Sprintf(r.text, n)})
		}
	}
	return out
}

// tooSenior caps the years a job may ask for just under the fewest any of
// the dismissed jobs asked; failing that, it drops their level.
func tooSenior(prefs config.Preferences, levels []config.Level, ds []store.Dismissal, label func(string) string) (Suggestion, bool) {
	fewest := 0
	for _, d := range ds {
		if d.YearsRequired != nil && *d.YearsRequired > 0 && (fewest == 0 || *d.YearsRequired < fewest) {
			fewest = *d.YearsRequired
		}
	}
	if limit := fewest - 1; limit >= 1 && (prefs.MaxYearsRequired == 0 || limit < prefs.MaxYearsRequired) {
		next := prefs
		next.Levels = slices.Clone(prefs.Levels)
		next.MaxYearsRequired = limit
		return Suggestion{
			Reason: "too_senior", Count: len(ds), Preferences: &next,
			Text: fmt.Sprintf("You passed on %d jobs as too senior. Hide jobs asking for more than %d years?", len(ds), limit),
		}, true
	}
	return dropLevel(prefs, levels, ds, label, "too senior")
}

// dropLevel offers to drop the user's highest level (too senior) or lowest
// (too junior), and only when most of the dismissed jobs with a known level
// were at it: a job called too senior at the user's lowest level says the
// years were too many, not that the level is wrong. Never their only level.
func dropLevel(prefs config.Preferences, levels []config.Level, ds []store.Dismissal, label func(string) string, why string) (Suggestion, bool) {
	var mine []string // the user's levels, lowest first
	for _, l := range levels {
		if prefs.AcceptsLevel(l.ID) {
			mine = append(mine, l.ID)
		}
	}
	if len(mine) < 2 {
		return Suggestion{}, false
	}
	edge := mine[len(mine)-1]
	if why == "too junior" {
		edge = mine[0]
	}
	at, known := 0, 0
	for _, d := range ds {
		if d.Seniority == nil || *d.Seniority == "unknown" {
			continue
		}
		l := *d.Seniority
		if l == "junior" {
			l = "entry"
		}
		known++
		if l == edge {
			at++
		}
	}
	if known == 0 || at*2 <= known {
		return Suggestion{}, false
	}
	top := edge
	next := prefs
	next.Levels = slices.DeleteFunc(slices.Clone(prefs.Levels), func(l string) bool { return l == top })
	return Suggestion{
		Reason: strings.ReplaceAll(why, " ", "_"), Count: len(ds), Preferences: &next,
		Text: fmt.Sprintf("You passed on %d jobs as %s. Stop showing %s jobs?", len(ds), why, label(top)),
	}, true
}

// getTuneSuggestions is GET /api/profile/suggestions: preference changes
// suggested by the reasons the user gave for passing on jobs lately.
func (s *Server) getTuneSuggestions(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	prof, err := store.GetProfile(r.Context(), s.Pool, user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if prof == nil || !prof.Ready() {
		s.writeJSON(w, http.StatusOK, map[string]any{"suggestions": []Suggestion{}})
		return
	}
	ds, err := store.RecentDismissals(r.Context(), s.Pool, user.ID, time.Now().Add(-tuneWindow))
	if err != nil {
		s.Log.Error("recent dismissals failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	var levels []config.Level
	if s.Roles != nil {
		levels = s.Roles.Levels
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"suggestions": suggestTunes(prof.Preferences, levels, ds)})
}
