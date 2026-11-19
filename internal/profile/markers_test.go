package profile

import (
	"strings"
	"testing"
	"time"
)

func TestAMarkerNeverReachesAForm(t *testing.T) {
	// As the Hugging Face form got it: a full answer, then the marker.
	a := ParseAnswer("I have built Go services used in production every day. NEEDS_INPUT: any public GitHub profile or merged PRs, with links.", 150)
	if strings.Contains(strings.ToLower(a.Answer), "needs") || !strings.HasPrefix(a.Answer, "I have built") {
		t.Errorf("answer kept the marker: %q", a.Answer)
	}
	if a.NeedsInput != "any public GitHub profile or merged PRs, with links." {
		t.Errorf("needs = %q", a.NeedsInput)
	}
	if a := ParseAnswer("Needs input: your notice period", 100); a.Answer != "" || a.NeedsInput != "your notice period" {
		t.Errorf("marker alone: %+v", a)
	}
	if l := CleanLetter("Dear Hiring Team,\n\nI build things.\n\nNEEDS_INPUT: the hiring manager's name"); strings.Contains(l, "NEEDS") {
		t.Errorf("letter kept the marker: %q", l)
	}
	got, err := ParseAutofill(`{"answers":[{"id":"a","value":"Mostly internal work. NEEDS_INPUT: links","confidence":"high"},{"id":"b","value":"NEEDS_INPUT: salary","confidence":"high"}]}`,
		[]FormField{{ID: "a", Label: "Open source work", Kind: "textarea"}, {ID: "b", Label: "Salary", Kind: "text"}}, time.Now())
	if err != nil || len(got) != 1 || got[0].Value != "Mostly internal work." || got[0].Confidence != "low" {
		t.Errorf("autofill = %+v, %v", got, err)
	}
}
