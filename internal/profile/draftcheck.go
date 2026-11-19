package profile

import (
	"regexp"
	"strings"
)

// The answer rules the model most often breaks, checked on what it wrote
// so a draft that breaks one gets a single rework with exactly what to fix.

// opensWithLack: "I have not held…", "While I haven't…", "Although I don't have…".
var opensWithLack = regexp.MustCompile(`(?i)^\W*((while|although|though|even though|admittedly),?\s+)?i\s+(have\s+(not|never)|haven'?t|do\s+not\s+have|don'?t\s+have|lack|cannot\s+claim|can'?t\s+claim|am\s+not)\b`)

// Volunteered topics: what the answer says unasked, and what in a question asks for it.
var volunteered = []struct {
	says, asked *regexp.Regexp
	fix         string
}{
	{regexp.MustCompile(`(?i)\b(sponsor(ship)?|visa|h-?1b|work authori[sz]ation|authori[sz]ed to work|green card|opt\b)`),
		regexp.MustCompile(`(?i)sponsor|visa|authori[sz]|immigration|work permit|right to work|eligib`),
		"Leave out work authorization, visa and sponsorship: the question doesn't ask about them."},
	{regexp.MustCompile(`(?i)\b(i am|i'm|i live|i'm currently|currently)\s+(based|located|living|live)\s+in\b|\bi live in\b|\bwork (hybrid|remotely|on-?site)\b`),
		regexp.MustCompile(`(?i)locat|based|reside|live|relocat|office|commut|remote|hybrid|on-?site|where`),
		"Leave out where the candidate lives and how they work (remote, hybrid, on-site): the question doesn't ask."},
	{regexp.MustCompile(`(?i)\b(salary|compensation)\b|\$\d`),
		regexp.MustCompile(`(?i)salary|compensation|pay|rate|expect`),
		"Leave out salary and pay: the question doesn't ask about them."},
}

// DraftFixes lists what a drafted answer must change, or nothing when it keeps the rules.
func DraftFixes(question, answer string) []string {
	var fixes []string
	if opensWithLack.MatchString(answer) {
		fixes = append(fixes, "Start with the closest real experience itself, not with what the candidate has not done; keep every fact true.")
	}
	for _, v := range volunteered {
		if v.says.MatchString(answer) && !v.asked.MatchString(question) {
			fixes = append(fixes, v.fix)
		}
	}
	return fixes
}

// ReworkFor is the steer for one rework of a draft that broke the rules.
func ReworkFor(fixes []string) string {
	return "Rework this draft. " + strings.Join(fixes, " ")
}
