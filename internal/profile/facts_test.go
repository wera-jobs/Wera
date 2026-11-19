package profile

import (
	"strings"
	"testing"
	"time"
)

// A test account with the contradiction the 19 Nov live test found: the
// profile text says Dallas and future sponsorship, the details say San
// Francisco and none.
var mixedDetails = map[string]string{
	"city": "San Francisco", "state": "CA", "country": "United States",
	"authorized_to_work": "yes", "needs_sponsorship": "no", "visa_status": "US Citizen",
	"current_company": "Nexus AI", "email": "alex@example.com", "phone": "+1 555 010 0100",
}

const mixedProfile = "- Work authorization: authorized now, but will need visa sponsorship in the future (e.g. H-1B).\n- Location: Dallas or remote."

func TestFactsBlockHoldsOnlyFactsThatSettleADisagreement(t *testing.T) {
	b := FactsBlock(mixedDetails)
	for _, want := range []string{`"city":"San Francisco"`, `"needs_sponsorship":"no"`, `"current_company":"Nexus AI"`, "<facts>"} {
		if !strings.Contains(b, want) {
			t.Errorf("facts miss %s:\n%s", want, b)
		}
	}
	// Contact details have no place in an essay.
	for _, never := range []string{"alex@example.com", "555"} {
		if strings.Contains(b, never) {
			t.Errorf("facts leak %s", never)
		}
	}
	if FactsBlock(map[string]string{"email": "a@b.c"}) != "" {
		t.Error("no facts should give no block")
	}
}

func TestEveryWritingPromptPutsTheFactsAboveTheProfile(t *testing.T) {
	answer := AnswerMessages(AnswerInput{Question: "Why do you want to join Figma?", Profile: mixedProfile, Details: mixedDetails, Today: time.Now()})
	letter := CoverLetterMessages(LetterInput{Profile: mixedProfile, Company: "Figma", Title: "Engineer", Details: mixedDetails})
	fill := AutofillMessages(AutofillInput{Fields: []FormField{{ID: "a", Label: "Do you need sponsorship?", Kind: "text"}}, Profile: mixedProfile, Details: mixedDetails})
	for name, msgs := range map[string][]struct{ Role, Content string }{
		"answer": {{answer[0].Role, answer[0].Content}, {answer[1].Role, answer[1].Content}},
		"letter": {{letter[0].Role, letter[0].Content}, {letter[1].Role, letter[1].Content}},
		"fill":   {{fill[0].Role, fill[0].Content}, {fill[1].Role, fill[1].Content}},
	} {
		sys, user := msgs[0].Content, msgs[1].Content
		if !strings.Contains(sys, "the facts are right") {
			t.Errorf("%s: no rule that the facts win", name)
		}
		f, p := strings.Index(user, "<facts>"), strings.Index(user, "<profile>")
		if f < 0 || p < 0 || f > p {
			t.Errorf("%s: facts missing or after the profile", name)
		}
	}
	if !strings.Contains(answer[0].Content, "never open by saying they lack it") {
		t.Error("answers lack the rule against opening with a disqualifier")
	}
}
