package scoring

import (
	"fmt"
	"strings"
)

// deepInstructionsText is the fixed deep-review instruction from PLAN.md
// section 8. As with the scoring prompt, it is byte-identical on every
// call so the instructions+profile prefix stays cached.
const deepInstructionsText = `You are preparing one candidate to apply to one job. Return ONLY a JSON object, no prose, no markdown fences. Be concrete and honest: every claim must trace to the posting or the profile; do not invent experience. "why_fit" is exactly 3 bullets (one sentence each) on why this candidate fits this specific job. "resume_bullets" is the 3 bullets of the candidate's resume most worth emphasizing or adapting for this application. "gaps" lists each real gap (missing skill, experience, or requirement) with a short, honest way to address it in the application or interview; use an empty array only if there are no meaningful gaps. "interview_topics" is exactly 3 topics likely to come up in a first-round interview for this job.`

// deepSchemaJSON is the exact reply shape embedded in the instructions
// (the M3 lesson: stating the schema verbatim keeps replies valid).
const deepSchemaJSON = `{
  "why_fit": ["...", "...", "..."],
  "resume_bullets": ["...", "...", "..."],
  "gaps": [{"gap": "...", "address": "..."}],
  "interview_topics": ["...", "...", "..."]
}`

// DeepInstructions is the full fixed instruction: prose + exact schema.
const DeepInstructions = deepInstructionsText + `

Your reply must be exactly this JSON shape (types matter: why_fit, resume_bullets and interview_topics are arrays of non-empty strings; gaps is an array of objects with non-empty "gap" and "address" strings, possibly empty):

` + deepSchemaJSON

// DeepGap is one real gap and how to address it honestly.
type DeepGap struct {
	Gap     string `json:"gap"`
	Address string `json:"address"`
}

// DeepAnalysis is the structured deep-review reply (PLAN.md section 8).
type DeepAnalysis struct {
	WhyFit          []string  `json:"why_fit"`
	ResumeBullets   []string  `json:"resume_bullets"`
	Gaps            []DeepGap `json:"gaps"`
	InterviewTopics []string  `json:"interview_topics"`
}

// BuildDeepInput assembles the Responses-API input items: the profile
// first, then the job. The profile item must stay byte-identical across
// calls so it is served from the prompt cache; only the job item varies.
func BuildDeepInput(profile []byte, j Job) []Message {
	return []Message{
		{Role: "user", Content: "CANDIDATE PROFILE:\n" + string(profile)},
		{Role: "user", Content: buildJobMessage(j)},
	}
}

// ParseDeepAnalysis strips fences, unmarshals, and validates a
// deep-review reply with the same strictness as ParseAnalysis. It
// accepts 1-5 why_fit/resume_bullets/interview_topics entries and 0-5
// gaps; every entry must be non-empty.
func ParseDeepAnalysis(text string) (*DeepAnalysis, error) {
	d := &DeepAnalysis{}
	if err := DecodeJSON(text, d); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSchema, err)
	}
	if err := validateDeepStrings("why_fit", d.WhyFit, 1, 5); err != nil {
		return nil, err
	}
	if err := validateDeepStrings("resume_bullets", d.ResumeBullets, 1, 5); err != nil {
		return nil, err
	}
	if len(d.Gaps) > 5 {
		return nil, fmt.Errorf("%w: gaps: want at most 5 entries, got %d", ErrInvalidSchema, len(d.Gaps))
	}
	for i, g := range d.Gaps {
		if strings.TrimSpace(g.Gap) == "" || strings.TrimSpace(g.Address) == "" {
			return nil, fmt.Errorf("%w: gaps[%d]: gap and address must be non-empty", ErrInvalidSchema, i)
		}
	}
	if err := validateDeepStrings("interview_topics", d.InterviewTopics, 1, 5); err != nil {
		return nil, err
	}
	return d, nil
}

// validateDeepStrings checks that a string-array field is present with a
// non-empty entry count between lo and hi and no empty entries.
func validateDeepStrings(field string, vals []string, lo, hi int) error {
	if len(vals) < lo || len(vals) > hi {
		return fmt.Errorf("%w: %s: want %d-%d entries, got %d", ErrInvalidSchema, field, lo, hi, len(vals))
	}
	for i, v := range vals {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%w: %s[%d] is empty", ErrInvalidSchema, field, i)
		}
	}
	return nil
}
