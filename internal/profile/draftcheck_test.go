package profile

import (
	"strings"
	"testing"
)

func TestDraftFixes(t *testing.T) {
	cases := []struct {
		q, a string
		want []string
	}{
		// From the 19 Nov live test and the drafts that followed.
		{"Please describe your experience operating full cycle product partnerships.", "I have not held a partnerships title, but my work at Example Airlines ran a full cycle.", []string{"Start with"}},
		{"Tell us about a time you led a team through a hard deadline.", "While I haven't formally managed a team, I led the dashboard launch.", []string{"Start with"}},
		{"Why do you want to join Figma?", "Observability is my day job. I am based in San Francisco and work hybrid, so I can be hands-on.", []string{"where the candidate lives"}},
		{"Why do you want to join Figma?", "I love the product. I will need sponsorship later.", []string{"sponsorship"}},
		{"Why Ramp?", "Ramp's mission fits, and my $150k expectation is flexible.", []string{"salary"}},
		// Asked, so fine; and good answers pass.
		{"Are you able to work from our New York office three days a week, and where are you based?", "Yes. I am based in San Francisco and would relocate.", nil},
		{"What is your experience with Kubernetes in production?", "My production work has centered on data pipelines rather than running Kubernetes directly.", nil},
		{"Why do you want to work at Discord?", "I use Discord every day and want to make the secure way the easy way.", nil},
	}
	for _, c := range cases {
		got := DraftFixes(c.q, c.a)
		if len(got) != len(c.want) {
			t.Errorf("%q: fixes %v, want %d", c.a, got, len(c.want))
			continue
		}
		for i, w := range c.want {
			if !strings.Contains(got[i], w) {
				t.Errorf("%q: fix %q lacks %q", c.a, got[i], w)
			}
		}
	}
}
