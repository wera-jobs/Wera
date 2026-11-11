package interview

import (
	"strings"
	"testing"
)

func TestScenariosLoad(t *testing.T) {
	if len(Scenarios) < 8 {
		t.Fatalf("%d scenarios", len(Scenarios))
	}
	for _, tr := range Tracks {
		n := 0
		for _, s := range Scenarios {
			for _, id := range s.Tracks {
				if id == tr.ID {
					n++
				}
			}
		}
		if n == 0 {
			t.Errorf("track %s has no scenario", tr.ID)
		}
	}
}

// The best reply must not sit in one place: a player would learn the letter, not the judgment.
func TestBestRepliesMoveAround(t *testing.T) {
	at := map[int]int{}
	total := 0
	for _, s := range Scenarios {
		for _, st := range s.Steps {
			for j, c := range st.Choices {
				if c.Rating == Best {
					at[j]++
				}
			}
			total++
		}
	}
	for j := range 4 {
		if at[j] == 0 || at[j] > total/2 {
			t.Errorf("best reply at position %d in %d of %d steps", j+1, at[j], total)
		}
	}
}

// Users read these: no em dashes, which read as generated.
func TestNoEmDashes(t *testing.T) {
	if strings.ContainsRune(string(scenariosJSON), '—') {
		t.Error("scenarios.json has an em dash")
	}
}

func TestLoadRejectsBadShapes(t *testing.T) {
	bad := []string{
		`[{"id":"x"}]`,
		`[{"id":"x","nope":1}]`,
	}
	for _, b := range bad {
		if _, err := load([]byte(b)); err == nil {
			t.Errorf("accepted %s", b)
		}
	}
	s := Scenarios[0]
	s.Steps = append([]Step(nil), s.Steps...)
	st := s.Steps[0]
	st.Choices = append([]Choice(nil), st.Choices...)
	for i := range st.Choices {
		st.Choices[i].Rating = Best
	}
	s.Steps[0] = st
	if err := s.check(); err == nil {
		t.Error("accepted four best replies")
	}
}

func TestPublicHidesRatings(t *testing.T) {
	for _, st := range Scenarios[0].Public() {
		if len(st.Choices) != 4 {
			t.Fatal("choices lost")
		}
	}
}

func TestSuggest(t *testing.T) {
	cases := map[string][]string{
		"backend":  {"software_engineering"},
		"infra":    {"sre", "data_science"},
		"data":     {"data_analytics"},
		"product":  {"product_management"},
		"general":  {"registered_nursing"},
		"general2": nil,
	}
	for want, fam := range cases {
		if got := Suggest(fam); got != strings.TrimSuffix(want, "2") {
			t.Errorf("Suggest(%v) = %s, want %s", fam, got, want)
		}
	}
}

func TestPoints(t *testing.T) {
	if Points(Best) != 2 || Points(Okay) != 1 || Points(Weak) != 0 || Scenarios[0].MaxPoints() != 2*len(Scenarios[0].Steps) {
		t.Error("points")
	}
}
