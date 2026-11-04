package scoring

import (
	"strings"

	"wera/internal/relevance"
)

// genericSkillWords say nothing on their own ("distributed systems" is
// matched on "distributed", not on "systems").
var genericSkillWords = map[string]bool{
	"experience": true, "systems": true, "system": true, "development": true, "engineering": true,
	"design": true, "skills": true, "tools": true, "knowledge": true, "strong": true, "software": true,
	"programming": true, "languages": true, "language": true, "frameworks": true, "framework": true,
	"technologies": true, "concepts": true, "principles": true, "practices": true, "fundamentals": true,
	"working": true, "understanding": true, "proficiency": true, "familiarity": true, "basic": true,
	"advanced": true, "modern": true, "services": true, "platforms": true, "platform": true,
}

// SkillSet is the set of terms in a profile, for MatchSkills.
type SkillSet map[string]bool

// NewSkillSet tokenizes a profile once.
func NewSkillSet(profile string) SkillSet {
	set := SkillSet{}
	for _, t := range relevance.Tokens(profile) {
		set[t] = true
	}
	return set
}

// MatchSkills splits a job's skills into those the profile mentions and
// those it does not (at most 8 each), with no model call. A skill counts
// as matched when the profile contains any of its specific words.
func MatchSkills(profile SkillSet, skills []string) (matched, missing []string) {
	matched, missing = []string{}, []string{}
	for _, skill := range skills {
		skill = strings.TrimSpace(skill)
		toks := relevance.Tokens(skill)
		if len(toks) == 0 {
			continue
		}
		specific := toks[:0:0]
		for _, t := range toks {
			if !genericSkillWords[t] {
				specific = append(specific, t)
			}
		}
		if len(specific) == 0 {
			specific = toks
		}
		hit := false
		for _, t := range specific {
			if profile[t] {
				hit = true
				break
			}
		}
		if hit && len(matched) < 8 {
			matched = append(matched, skill)
		} else if !hit && len(missing) < 8 {
			missing = append(missing, skill)
		}
	}
	return matched, missing
}

// TailoredFit is a job's fit score again with more evidence of the
// candidate's skills (a resume tailored to the job): the model's stored
// ratings (raw is its reply) are kept, and only the measured coverage of
// the job's required skills is worked out again from the profile and that
// text. It returns the score from the profile alone and the score with
// the text; ok is false when the reply has no usable ratings.
func TailoredFit(raw, profile, text string, required []string) (before, after int, ok bool) {
	fit, err := ParseFit(raw)
	if err != nil {
		return 0, 0, false
	}
	score := func(evidence string) int {
		return fit.Ratings.Score(Coverage(MatchSkills(NewSkillSet(evidence), required)))
	}
	return score(profile), score(profile + "\n" + text), true
}
