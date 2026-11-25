package profile

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxSkillLen bounds a skill added with AddSkill, in characters.
const MaxSkillLen = 60

// addedSkillsLabel starts the Skills line that one-click additions go on.
const addedSkillsLabel = "- Also: "

// CleanSkill trims a skill and checks it is one short name: no line
// breaks or markdown, and at least one letter or digit.
func CleanSkill(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	switch {
	case s == "":
		return "", errors.New("the skill is empty")
	case utf8.RuneCountInString(s) > MaxSkillLen:
		return "", errors.New("the skill is too long")
	case strings.ContainsAny(s, "#<>`*[]|\\"):
		return "", errors.New("the skill has characters a skill name doesn't need")
	case !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }):
		return "", errors.New("the skill needs a letter or a digit")
	}
	return s, nil
}

// isSkillsHeading: "## Skills", "## Technical skills" and the like.
func isSkillsHeading(line string) bool {
	return strings.HasPrefix(line, "## ") && strings.Contains(strings.ToLower(line), "skill")
}

// AddSkill adds skill to the profile's Skills section and reports whether
// the text changed. A skill the section already names (any case, whole
// words) is left alone. Additions go on one "- Also:" line at the end of
// the section; a profile without the section gets one before Experience
// (or at the end). The rest of the text is kept byte for byte.
func AddSkill(markdown, skill string) (string, bool) {
	lines := strings.Split(markdown, "\n")
	start := -1
	for i, l := range lines {
		if isSkillsHeading(strings.TrimSpace(l)) {
			start = i
			break
		}
	}
	if start < 0 {
		section := []string{"## Skills", addedSkillsLabel + skill, ""}
		at := len(lines)
		for i, l := range lines {
			if t := strings.ToLower(strings.TrimSpace(l)); strings.HasPrefix(t, "## experience") || strings.HasPrefix(t, "## scoring guidance") {
				at = i
				break
			}
		}
		if at == len(lines) {
			section = append([]string{""}, section[:2]...)
		}
		out := append(append(append([]string{}, lines[:at]...), section...), lines[at:]...)
		return strings.Join(out, "\n"), true
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			end = i
			break
		}
	}
	word := regexp.MustCompile(`(?i)(^|[^\pL\pN+#.])` + regexp.QuoteMeta(skill) + `($|[^\pL\pN+#])`)
	for _, l := range lines[start+1 : end] {
		if word.MatchString(l) {
			return markdown, false
		}
	}
	for i := start + 1; i < end; i++ {
		if strings.HasPrefix(lines[i], addedSkillsLabel) {
			lines[i] = strings.TrimRight(lines[i], " ") + ", " + skill
			return strings.Join(lines, "\n"), true
		}
	}
	// After the section's last non-blank line.
	last := start
	for i := start + 1; i < end; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			last = i
		}
	}
	out := append(append(append([]string{}, lines[:last+1]...), addedSkillsLabel+skill), lines[last+1:]...)
	return strings.Join(out, "\n"), true
}
