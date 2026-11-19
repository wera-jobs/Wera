package api

import (
	"strings"
	"testing"

	"wera/internal/store"
)

func TestWeeklySuggestions(t *testing.T) {
	if got := weeklySuggestions(&store.WeeklyReview{}); len(got) != 0 {
		t.Errorf("no data, but suggestions: %+v", got)
	}
	r := &store.WeeklyReview{
		StrongNew: 5, StrongActed: 2, StrongBest: &store.ReviewItem{JobID: 7, Title: "SRE", Company: "Acme"},
		Gaps: []store.ReviewGap{{Skill: "Kubernetes", Count: 4}}, GapsOf: 6,
		Waiting: 3,
		Applied: make([]store.ReviewItem, 4), OldApplied: 3,
	}
	got := weeklySuggestions(r)
	if len(got) != 3 {
		t.Fatalf("want three: %+v", got)
	}
	if !strings.Contains(got[0].Text, "3 of this week's 5 strong matches") || got[0].Link != "/jobs?job=7" {
		t.Errorf("strong: %+v", got[0])
	}
	if !strings.Contains(got[1].Text, "Kubernetes was missing in 4 of the 6 jobs") {
		t.Errorf("gap: %+v", got[1])
	}
	// A gap in too few of the jobs, or too few jobs, says nothing.
	r2 := &store.WeeklyReview{Gaps: []store.ReviewGap{{Skill: "Go", Count: 2}}, GapsOf: 9}
	if got := weeklySuggestions(r2); len(got) != 0 {
		t.Errorf("weak gap: %+v", got)
	}
}
