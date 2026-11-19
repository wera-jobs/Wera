package profile

import (
	"regexp"
	"strings"
)

// needsInput finds the marker the AI uses to ask the candidate for a fact,
// wherever it lands in a reply ("NEEDS_INPUT:", "Needs input:").
var needsInput = regexp.MustCompile(`(?i)\bneeds[_ ]input\s*:`)

// SplitNeedsInput separates what the AI wrote for the form from what it
// asks the candidate for. The marker never reaches a form.
func SplitNeedsInput(s string) (text, needs string) {
	loc := needsInput.FindStringIndex(s)
	if loc == nil {
		return s, ""
	}
	return strings.TrimSpace(s[:loc[0]]), strings.TrimSpace(s[loc[1]:])
}
