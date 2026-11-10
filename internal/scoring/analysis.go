package scoring

import (
	"errors"
	"fmt"
	"strings"
)

// Analysis is the structured scoring result required by PLAN.md section
// 7.4. YearsRequired and SponsorshipQuote are nullable; every string
// field must match its enum after validation.
type Analysis struct {
	// Ratings are what the model returns; FitScore and Verdict are computed from them.
	Ratings          *Ratings `json:"ratings,omitempty"`
	FitScore         int      `json:"fit_score"`
	Verdict          string   `json:"verdict"`
	Seniority        string   `json:"seniority"`
	YearsRequired    *int     `json:"years_required"`
	Sponsorship      string   `json:"sponsorship"`
	SponsorshipQuote *string  `json:"sponsorship_quote"`
	USEligible       *bool    `json:"us_eligible"`
	WorkMode         string   `json:"work_mode"`
	LocationSummary  string   `json:"location_summary"`
	SkillsMatched    []string `json:"skills_matched"`
	SkillsMissing    []string `json:"skills_missing"`
	Reason           string   `json:"reason"`
}

// schemaJSON is the model-facing schema documentation embedded in tests and
// available for prompts that need to restate the exact shape.
const schemaJSON = `{
  "ratings": {"role": 0, "seniority": 0, "skills": 0, "domain": 0, "location": 0, "blocker": false},
  "seniority": "entry|junior|mid|senior|unknown",
  "years_required": null,
  "sponsorship": "yes|no|unknown",
  "sponsorship_quote": null,
  "us_eligible": true,
  "work_mode": "remote|hybrid|onsite|unknown",
  "location_summary": "",
  "skills_matched": [],
  "skills_missing": [],
  "reason": ""
}`

// ErrInvalidSchema signals a reply that is not valid JSON for the schema
// (bad JSON, missing fences handled, wrong enums, out-of-range score).
var ErrInvalidSchema = errors.New("reply is not valid JSON for the schema")

var (
	seniority = map[string]bool{"entry": true, "junior": true, "mid": true, "senior": true, "unknown": true}
	sponsor   = map[string]bool{"yes": true, "no": true, "unknown": true}
	workModes = map[string]bool{"remote": true, "hybrid": true, "onsite": true, "unknown": true}
)

// StripFences removes accidental markdown code fences around a reply:
// a leading ```json / ``` and a trailing ```, plus any surrounding prose
// lines are tolerated only at the fence level (not arbitrary prose).
func StripFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line ("```json" or "```").
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[idx+1:]
	} else {
		return strings.TrimSpace(s)
	}
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSpace(s[:len(s)-3])
	}
	return s
}

// ParseAnalysis strips fences, unmarshals the reply, and validates every
// enum and range. It returns ErrInvalidSchema (wrapped with detail) when
// the reply cannot be trusted.
func ParseAnalysis(text string) (*Analysis, error) {
	a := &Analysis{}
	if err := DecodeJSON(text, a); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSchema, err)
	}
	if a.Ratings == nil || !a.Ratings.Valid() {
		return nil, fmt.Errorf("%w: ratings missing or out of range 0-10", ErrInvalidSchema)
	}
	a.FitScore = a.Ratings.Score(Coverage(a.SkillsMatched, a.SkillsMissing))
	a.Verdict = VerdictFor(a.FitScore)
	if !seniority[a.Seniority] {
		return nil, fmt.Errorf("%w: seniority %q", ErrInvalidSchema, a.Seniority)
	}
	if !sponsor[a.Sponsorship] {
		return nil, fmt.Errorf("%w: sponsorship %q", ErrInvalidSchema, a.Sponsorship)
	}
	if !workModes[a.WorkMode] {
		return nil, fmt.Errorf("%w: work_mode %q", ErrInvalidSchema, a.WorkMode)
	}
	if a.YearsRequired != nil && *a.YearsRequired < 0 {
		return nil, fmt.Errorf("%w: negative years_required", ErrInvalidSchema)
	}
	return a, nil
}
