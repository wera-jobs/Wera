package profile

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"wera/internal/normalize"
	"wera/internal/scoring"
)

const applicantInstructions = `You read a resume and pull out contact and education details for job application forms. ` + scoring.Untrusted + `

Return ONLY a JSON object with these string fields, "" when the resume does not say:
{"phone": "", "city": "", "state": "", "country": "", "linkedin": "", "github": "", "portfolio": "", "current_company": "", "current_title": "", "years_experience": "", "school": "", "degree": "", "major": "", "graduation_year": "", "gpa": ""}
Copy values as written; never guess. School, degree and major are for the most recent degree.`

// ApplicantMessages builds the request.
func ApplicantMessages(resume string) []scoring.Message {
	if r := []rune(resume); len(r) > 12000 {
		resume = string(r[:12000])
	}
	return []scoring.Message{
		{Role: "system", Content: applicantInstructions},
		{Role: "user", Content: "<resume>\n" + scoring.Fence(resume) + "\n</resume>"},
	}
}

var (
	linkedInRE = regexp.MustCompile(`(?i)(?:https?://)?(?:[a-z]{2,3}\.)?linkedin\.com/in/[A-Za-z0-9_-]+/?`)
	gitHubRE   = regexp.MustCompile(`(?i)(?:https?://)?github\.com/[A-Za-z0-9_-]+/?`)
	phoneRE    = regexp.MustCompile(`(?:\+?\d{1,2}[\s.-]?)?\(?\d{3}\)?[\s.-]?\d{3}[\s.-]?\d{4}`)
)

// ParseApplicantSuggestions merges the AI reply with what plain patterns
// find in the resume (links and phone numbers are taken from the resume
// text itself, so they are never made up).
func ParseApplicantSuggestions(reply, resume string) (map[string]string, error) {
	var raw map[string]any
	if err := scoring.DecodeJSON(reply, &raw); err != nil {
		return nil, fmt.Errorf("unreadable details: %w", err)
	}
	// Models sometimes answer numbers (a GPA, a year) as numbers.
	out := map[string]string{}
	for k, v := range raw {
		var s string
		switch t := v.(type) {
		case string:
			s = t
		case float64:
			s = strconv.FormatFloat(t, 'f', -1, 64)
		default:
			continue
		}
		out[k] = normalize.Clip(strings.TrimSpace(s), 200)
	}
	// Contact details must appear in the resume verbatim.
	out["linkedin"], out["github"], out["phone"] = "", "", ""
	if m := linkedInRE.FindString(resume); m != "" {
		out["linkedin"] = withScheme(m)
	}
	if m := gitHubRE.FindString(resume); m != "" {
		out["github"] = withScheme(m)
	}
	if m := phoneRE.FindString(resume); m != "" {
		out["phone"] = strings.TrimSpace(m)
	}
	if p := out["portfolio"]; p != "" && !strings.Contains(resume, strings.TrimPrefix(strings.TrimPrefix(p, "https://"), "http://")) {
		out["portfolio"] = ""
	}
	return out, nil
}

func withScheme(u string) string {
	u = strings.TrimSuffix(u, "/")
	if !strings.HasPrefix(strings.ToLower(u), "http") {
		return "https://" + u
	}
	return u
}
