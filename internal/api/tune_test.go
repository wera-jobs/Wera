package api

import (
	"strings"
	"testing"

	"wera/internal/config"
	"wera/internal/store"
)

func TestSuggestTunes(t *testing.T) {
	levels := []config.Level{{ID: "entry", Label: "Entry level / new grad"}, {ID: "mid", Label: "Mid level"}, {ID: "senior", Label: "Senior"}}
	prefs := config.Preferences{RoleFamilies: []string{"swe"}, Levels: []string{"entry", "mid", "senior"}}
	sp, ip := func(s string) *string { return &s }, func(n int) *int { return &n }
	d := func(reason, level string, years int) store.Dismissal {
		out := store.Dismissal{Reason: reason}
		if level != "" {
			out.Seniority = sp(level)
		}
		if years > 0 {
			out.YearsRequired = ip(years)
		}
		return out
	}

	if got := suggestTunes(prefs, levels, []store.Dismissal{d("too_senior", "senior", 6), d("too_senior", "senior", 8)}); len(got) != 0 {
		t.Errorf("two repeats suggest nothing: %+v", got)
	}

	got := suggestTunes(prefs, levels, []store.Dismissal{d("too_senior", "senior", 6), d("too_senior", "senior", 8), d("too_senior", "mid", 5)})
	if len(got) != 1 || got[0].Preferences == nil || got[0].Preferences.MaxYearsRequired != 4 {
		t.Fatalf("too senior with years: %+v", got)
	}
	if prefs.MaxYearsRequired != 0 || len(prefs.Levels) != 3 {
		t.Error("the user's preferences changed")
	}
	if !strings.Contains(got[0].Text, "more than 4 years") {
		t.Errorf("text: %q", got[0].Text)
	}

	// Already capped lower: drop the level most were at instead.
	capped := prefs
	capped.MaxYearsRequired = 3
	got = suggestTunes(capped, levels, []store.Dismissal{d("too_senior", "senior", 6), d("too_senior", "senior", 0), d("too_senior", "mid", 5)})
	if len(got) != 1 || got[0].Preferences == nil || strings.Join(got[0].Preferences.Levels, ",") != "entry,mid" || !strings.Contains(got[0].Text, "senior jobs") {
		t.Fatalf("too senior, capped: %+v", got)
	}

	got = suggestTunes(prefs, levels, []store.Dismissal{d("too_junior", "junior", 0), d("too_junior", "entry", 0), d("too_junior", "", 0)})
	if len(got) != 1 || strings.Join(got[0].Preferences.Levels, ",") != "mid,senior" || got[0].Reason != "too_junior" {
		t.Fatalf("too junior: %+v", got)
	}

	// Too senior at the user's lowest level says nothing about levels.
	if got = suggestTunes(prefs, levels, []store.Dismissal{d("too_senior", "entry", 1), d("too_senior", "entry", 1), d("too_senior", "mid", 2)}); len(got) != 0 {
		t.Errorf("too senior at the bottom level: %+v", got)
	}
	// Too junior at the user's top level neither.
	if got = suggestTunes(prefs, levels, []store.Dismissal{d("too_junior", "senior", 0), d("too_junior", "senior", 0), d("too_junior", "mid", 0)}); len(got) != 0 {
		t.Errorf("too junior at the top level: %+v", got)
	}

	// The only level is never dropped.
	only := config.Preferences{RoleFamilies: []string{"swe"}, Levels: []string{"mid"}}
	if got = suggestTunes(only, levels, []store.Dismissal{d("too_junior", "mid", 0), d("too_junior", "mid", 0), d("too_junior", "mid", 0)}); len(got) != 0 {
		t.Errorf("only level: %+v", got)
	}

	got = suggestTunes(prefs, levels, []store.Dismissal{d("location", "", 0), d("location", "", 0), d("location", "", 0), d("pay", "", 0), d("pay", "", 0), d("pay", "", 0)})
	if len(got) != 1 || got[0].Reason != "location" || got[0].Preferences != nil || got[0].Count != 3 {
		t.Errorf("location: %+v", got)
	}
}
