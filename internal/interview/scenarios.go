package interview

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// A scenario is a work simulation, like the situational rounds of big-tech
// loops: a few days at Wera told as emails, and for each one, four ways
// to reply. Replies are rated rather than right or wrong, because real
// judgment is graded that way: one best reply, and others that are fine
// but miss something, or that would go badly.

// Ratings of a reply, and the points each is worth.
const (
	Best = "best"
	Okay = "okay"
	Weak = "weak"
)

// Points is what a rating scores; a step is out of 2.
func Points(rating string) int {
	switch rating {
	case Best:
		return 2
	case Okay:
		return 1
	}
	return 0
}

// Person is someone in a scenario's cast.
type Person struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// Email is the message a step opens with.
type Email struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Cc      []string `json:"cc,omitempty"`
	Subject string   `json:"subject"`
	When    string   `json:"when"`
	Body    string   `json:"body"`
}

// Choice is one reply, with how it rates and why.
type Choice struct {
	Text     string `json:"text"`
	Rating   string `json:"rating"`
	Feedback string `json:"feedback"`
}

// Step is one email and the replies to choose from.
type Step struct {
	// Skill is what the step tests, as a debrief names it (Ownership, Technical judgment, ...).
	Skill    string   `json:"skill"`
	Email    Email    `json:"email"`
	Prompt   string   `json:"prompt"`
	Choices  []Choice `json:"choices"`
	Takeaway string   `json:"takeaway"`
}

// Scenario is one simulation.
type Scenario struct {
	ID         string   `json:"id"`
	Tracks     []string `json:"tracks"`
	Title      string   `json:"title"`
	Role       string   `json:"role"`
	Blurb      string   `json:"blurb"`
	Difficulty string   `json:"difficulty"`
	Cast       []Person `json:"cast"`
	Steps      []Step   `json:"steps"`
}

//go:embed scenarios.json
var scenariosJSON []byte

// Scenarios are every simulation, in the file's order.
var Scenarios = mustLoad(scenariosJSON)

func mustLoad(b []byte) []Scenario {
	s, err := load(b)
	if err != nil {
		panic(err)
	}
	return s
}

func load(b []byte) ([]Scenario, error) {
	var out []Scenario
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("scenarios.json: %w", err)
	}
	ids := map[string]bool{}
	for _, s := range out {
		if err := s.check(); err != nil {
			return nil, fmt.Errorf("scenario %q: %w", s.ID, err)
		}
		if ids[s.ID] {
			return nil, fmt.Errorf("scenario %q twice", s.ID)
		}
		ids[s.ID] = true
	}
	return out, nil
}

// check holds a scenario to the shape the player needs.
func (s Scenario) check() error {
	if s.ID == "" || s.Title == "" || s.Role == "" || s.Blurb == "" || len(s.Cast) == 0 {
		return fmt.Errorf("needs an id, title, role, blurb and cast")
	}
	if !slices.Contains([]string{"easy", "medium", "hard"}, s.Difficulty) {
		return fmt.Errorf("difficulty %q", s.Difficulty)
	}
	if len(s.Tracks) == 0 {
		return fmt.Errorf("no tracks")
	}
	for _, t := range s.Tracks {
		if _, ok := TrackByID(t); !ok {
			return fmt.Errorf("unknown track %q", t)
		}
	}
	if len(s.Steps) < 3 || len(s.Steps) > 6 {
		return fmt.Errorf("%d steps, want 3 to 6", len(s.Steps))
	}
	for i, st := range s.Steps {
		if st.Skill == "" || st.Prompt == "" || st.Takeaway == "" || st.Email.From == "" || st.Email.Subject == "" || st.Email.Body == "" {
			return fmt.Errorf("step %d: missing a skill, prompt, takeaway or email field", i+1)
		}
		if len(st.Choices) != 4 {
			return fmt.Errorf("step %d: %d choices, want 4", i+1, len(st.Choices))
		}
		best := 0
		for j, c := range st.Choices {
			if c.Text == "" || c.Feedback == "" {
				return fmt.Errorf("step %d choice %d: empty", i+1, j+1)
			}
			switch c.Rating {
			case Best:
				best++
			case Okay, Weak:
			default:
				return fmt.Errorf("step %d choice %d: rating %q", i+1, j+1, c.Rating)
			}
		}
		if best != 1 {
			return fmt.Errorf("step %d: %d best replies, want 1", i+1, best)
		}
	}
	return nil
}

// ScenarioByID finds a simulation.
func ScenarioByID(id string) (Scenario, bool) {
	i := slices.IndexFunc(Scenarios, func(s Scenario) bool { return s.ID == id })
	if i < 0 {
		return Scenario{}, false
	}
	return Scenarios[i], true
}

// MaxPoints is the best score a scenario allows.
func (s Scenario) MaxPoints() int { return 2 * len(s.Steps) }

// PublicStep is a step as served before it is answered: no ratings.
type PublicStep struct {
	Skill   string   `json:"skill"`
	Email   Email    `json:"email"`
	Prompt  string   `json:"prompt"`
	Choices []string `json:"choices"`
}

// Public is the scenario with its ratings, feedback and takeaways left out.
func (s Scenario) Public() []PublicStep {
	out := make([]PublicStep, len(s.Steps))
	for i, st := range s.Steps {
		ch := make([]string, len(st.Choices))
		for j, c := range st.Choices {
			ch[j] = c.Text
		}
		out[i] = PublicStep{Skill: st.Skill, Email: st.Email, Prompt: st.Prompt, Choices: ch}
	}
	return out
}
