package profile

import (
	"strings"
	"testing"
	"time"
)

func TestParseEmail(t *testing.T) {
	e, ok := ParseEmail("```\nSubject: Following up — SRE role\n\nHello Sam,\n\nI applied for the Site Reliability Engineer role two weeks ago and remain very interested — keeping robots online is the work I want. I recently automated deploys for twelve services, which maps to your on-call needs. Could you share where things stand?\n\nThanks,\nAda\nNEEDS_INPUT: the name of the team you spoke to\n```")
	if !ok || e.Subject != "Following up, SRE role" || strings.Contains(e.Body, "—") || strings.Contains(e.Body, "NEEDS") || e.Needs != "the name of the team you spoke to" {
		t.Errorf("parsed: %+v %v", e, ok)
	}
	if _, ok := ParseEmail("Subject: Hi\n\nThanks!"); ok {
		t.Error("a two-word body is not a draft")
	}
}

func TestEmailFixes(t *testing.T) {
	body := "Hello, I would love a referral for the SRE role at Acme. I will need visa sponsorship, and I can share my resume and a short summary whenever useful. Thanks for considering it, Ada"
	if f := EmailFixes("referral_request", Email{Body: body}); len(f) == 0 {
		t.Error("a referral request volunteering sponsorship was let through")
	}
	if f := EmailFixes("offer_reply", Email{Body: "Thank you for the offer. Could you share the salary breakdown and the start date in writing? I'm excited to join."}); len(f) != 0 {
		t.Errorf("an offer reply may ask about salary: %v", f)
	}
	if f := EmailFixes("follow_up", Email{Subject: "Following up", Body: "Hello [Name], I applied two weeks ago and remain interested."}); len(f) == 0 {
		t.Error("a placeholder was let through")
	}
}

func TestEmailMessages(t *testing.T) {
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	msgs := EmailMessages(EmailInput{Kind: "thank_you", CandidateName: "Ada", Profile: "SRE", Company: "Acme", Title: "SRE",
		Status: "interviewing", AppliedAt: &at, Contact: "Sam Rivera, Recruiter", Events: []string{"onsite on November 20, 2026 with the team"}})
	u := msgs[1].Content
	for _, want := range []string{"A thank-you after an interview", "Applied: November 2, 2026", "Contact: Sam Rivera, Recruiter", "Event: onsite on November 20, 2026"} {
		if !strings.Contains(u, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}
