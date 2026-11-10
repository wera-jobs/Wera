package profile

import (
	"fmt"
	"strings"

	"wera/internal/config"
	"wera/internal/scoring"
)

// Suggestions are preferences inferred from a resume, used to pre-fill
// the signup form. The user reviews and changes them.
type Suggestions struct {
	RoleFamilies    []string `json:"role_families"`
	Levels          []string `json:"levels"`
	YearsExperience *float64 `json:"years_experience"`
	CurrentTitle    string   `json:"current_title"`
	Locations       []string `json:"locations"`
	TargetRoles     string   `json:"target_roles"`
}

const suggestInstructions = `You read a resume and suggest job-search preferences for the candidate. Return ONLY a JSON object, no prose, no markdown fences:
{"role_families": [ids], "levels": [ids], "years_experience": number or null, "current_title": string, "locations": [strings], "target_roles": string}

Rules:
- role_families: 2 to 6 ids from the ROLE FAMILIES list that fit the candidate's experience and skills, most relevant first. Use only listed ids.
- levels: 1 to 3 ids from the LEVELS list the candidate would realistically apply to. Students and recent graduates: internship and/or entry. Use only listed ids.
- years_experience: total years of professional (non-internship) experience, rounded to 0.5; 0 for students; null if unclear.
- current_title: their current or most recent job title, or "".
- locations: cities they live or work in, as "City, ST" for the U.S. (e.g. "Dallas, TX"); at most 3; [] if none.
- target_roles: a short phrase naming the 2-4 job titles that fit them best.`

// SuggestMessages builds the chat messages that infer preferences.
func SuggestMessages(resume string, roles *config.Roles) []scoring.Message {
	var b strings.Builder
	b.WriteString("ROLE FAMILIES (id: label):\n")
	for _, f := range roles.RoleFamilies {
		fmt.Fprintf(&b, "- %s: %s\n", f.ID, f.Label)
	}
	b.WriteString("\nLEVELS (id: label):\n")
	for _, l := range roles.Levels {
		fmt.Fprintf(&b, "- %s: %s\n", l.ID, l.Label)
	}
	b.WriteString("\n<resume>\n")
	b.WriteString(scoring.Fence(resume))
	b.WriteString("\n</resume>")
	return []scoring.Message{
		{Role: "system", Content: suggestInstructions + "\n\n" + scoring.Untrusted},
		{Role: "user", Content: b.String()},
	}
}

// ParseSuggestions decodes the model reply and drops anything that is not
// in the catalog or out of range.
func ParseSuggestions(reply string, roles *config.Roles) (*Suggestions, error) {
	var s Suggestions
	if err := scoring.DecodeJSON(reply, &s); err != nil {
		return nil, fmt.Errorf("unreadable suggestions: %w", err)
	}
	s.RoleFamilies = keepKnown(s.RoleFamilies, roles.FamilyIDs(), 6)
	s.Levels = keepKnown(s.Levels, roles.LevelIDs(), 3)
	if y := s.YearsExperience; y != nil && (*y < 0 || *y > 60) {
		s.YearsExperience = nil
	}
	s.CurrentTitle = clip(s.CurrentTitle, 120)
	s.TargetRoles = clip(s.TargetRoles, 200)
	var locs []string
	for _, l := range s.Locations {
		if l = clip(l, 80); l != "" && len(locs) < 3 {
			locs = append(locs, l)
		}
	}
	s.Locations = locs
	if s.Locations == nil {
		s.Locations = []string{}
	}
	return &s, nil
}

func keepKnown(ids []string, known map[string]bool, limit int) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if known[id] && !seen[id] && len(out) < limit {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > n {
		s = string([]rune(s)[:n])
	}
	return s
}
