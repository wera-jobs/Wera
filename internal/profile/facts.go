package profile

import (
	"encoding/json"
	"fmt"
	"strings"

	"wera/internal/scoring"
)

// The application details are what the candidate confirmed on Wera's own
// form; the profile text and the resume are older and freer. When they
// disagree on a fact (where they live, work rights, employer, education),
// the details are right, so every AI request that writes for the
// candidate gets them as one authoritative block.

// FactsRule is the instruction that goes with FactsBlock.
const FactsRule = `- The <facts> block is the candidate's own confirmed application details. Where the profile or resume says something different (where they live, work authorization, visa or sponsorship needs, current employer, education), the facts are right: use them and never repeat the conflicting version.`

// factKeys are the details that settle a disagreement, in reading order.
// Contact details and self-identification never go in.
var factKeys = []string{
	"preferred_name", "city", "state", "country", "citizenship", "visa_status",
	"authorized_to_work", "needs_sponsorship", "willing_to_relocate", "work_preference",
	"current_company", "current_title", "years_experience",
	"school", "degree", "major", "graduation_year",
}

// FactsBlock renders the details as a <facts> block, or "" when there are none.
func FactsBlock(details map[string]string) string {
	type kv struct {
		K string `json:"k"`
		V string `json:"v"`
	}
	var facts []kv
	for _, k := range factKeys {
		if v := strings.TrimSpace(details[k]); v != "" {
			facts = append(facts, kv{k, v})
		}
	}
	if len(facts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range facts {
		line, _ := json.Marshal(map[string]string{f.K: f.V})
		b.Write(line)
		b.WriteByte('\n')
	}
	return fmt.Sprintf("<facts>\n%s</facts>\n\n", scoring.Fence(b.String()))
}
