package api

import (
	"testing"
	"time"

	"wera/internal/store"
)

func TestEmailNotYet(t *testing.T) {
	past := []store.Event{{Kind: "interview", At: time.Now().Add(-time.Hour)}}
	future := []store.Event{{Kind: "interview", At: time.Now().Add(time.Hour)}}
	for _, c := range []struct {
		kind, status string
		events       []store.Event
		ok           bool
	}{
		{"follow_up", "saved", nil, false},
		{"follow_up", "applied", nil, true},
		{"thank_you", "applied", nil, false},
		{"thank_you", "applied", future, false},
		{"thank_you", "applied", past, true},
		{"thank_you", "interviewing", nil, true},
		{"offer_reply", "interviewing", nil, false},
		{"offer_reply", "offer", nil, true},
		{"referral_request", "saved", nil, true},
	} {
		if got := emailNotYet(c.kind, c.status, c.events) == ""; got != c.ok {
			t.Errorf("%s with %s: ok %v, want %v", c.kind, c.status, got, c.ok)
		}
	}
}
