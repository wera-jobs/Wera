package api

import (
	"fmt"
	"net/http"
	"time"

	"wera/internal/store"
)

// Suggestion in a weekly review: a change to try, with the user's own
// numbers behind it, and where to act on it.
type reviewSuggestion struct {
	Text string `json:"text"`
	Link string `json:"link"`
}

// weeklySuggestions turns a review into at most three suggestions, each
// citing the user's own data. Nothing generic: no data, no suggestion.
func weeklySuggestions(r *store.WeeklyReview) []reviewSuggestion {
	out := []reviewSuggestion{}
	if b := r.StrongBest; b != nil && r.StrongNew > r.StrongActed {
		out = append(out, reviewSuggestion{
			Text: fmt.Sprintf("%d of this week's %d strong matches are still waiting for you; the best is %s at %s.", r.StrongNew-r.StrongActed, r.StrongNew, b.Title, b.Company),
			Link: fmt.Sprintf("/jobs?job=%d", b.JobID),
		})
	}
	if len(r.Gaps) > 0 && r.GapsOf >= 3 && r.Gaps[0].Count*3 >= r.GapsOf {
		g := r.Gaps[0]
		out = append(out, reviewSuggestion{
			Text: fmt.Sprintf("%s was missing in %d of the %d jobs that went nowhere in the last month. If you have it, put it on your resume; if not, it's worth learning or filtering out.", g.Skill, g.Count, r.GapsOf),
			Link: "/profile/resume",
		})
	}
	if r.Waiting >= 3 {
		out = append(out, reviewSuggestion{
			Text: fmt.Sprintf("%d applications have waited over two weeks without a reply. A short follow-up is one click from each job's Your application.", r.Waiting),
			Link: "/tracker",
		})
	}
	if n := len(r.Applied); n >= 4 && r.OldApplied*2 >= n {
		out = append(out, reviewSuggestion{
			Text: fmt.Sprintf("%d of this week's %d applications went to postings up for over two months, which are often filled. The Posted filter on Today finds fresher ones.", r.OldApplied, n),
			Link: "/",
		})
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// weeklyReview is GET /api/review/weekly?week=YYYY-MM-DD (any day of the
// week; this week when left out): the user's week, from their tracker
// and matches, with suggestions grounded in it.
func (s *Server) weeklyReview(w http.ResponseWriter, r *http.Request) {
	day := time.Now().UTC()
	if v := r.URL.Query().Get("week"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "week must be a date like 2026-11-16")
			return
		}
		day = t
	}
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	monday := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	rev, err := store.GetWeeklyReview(r.Context(), s.Pool, currentUser(r).ID, monday)
	if err != nil {
		s.Log.Error("weekly review failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, struct {
		*store.WeeklyReview
		Suggestions []reviewSuggestion `json:"suggestions"`
	}{rev, weeklySuggestions(rev)})
}
