package inbound

import (
	"regexp"
	"strings"
	"unicode"
)

// Kinds of reply, and the tracker status each suggests.
const (
	KindRejection  = "rejection"
	KindOffer      = "offer"
	KindInterview  = "interview"
	KindAssessment = "assessment"
	KindReceived   = "received"
)

// kindRules are checked in order: a rejection often thanks the candidate
// for applying and mentions interviews, and an offer congratulates like an
// invitation does, so the stronger news goes first.
var kindRules = []struct {
	kind string
	re   *regexp.Regexp
}{
	{KindRejection, regexp.MustCompile(`(?i)\b(` +
		`unfortunately|regret to inform|not (be )?(moving|move) forward|won'?t be moving forward|will not be moving forward|` +
		`(decided|chosen|chose|opted) (not )?to (move|proceed|pursue|go) (forward )?(with|in) (other|another|a different)|` +
		`move forward with (other|another) candidates|pursue other candidates|go in a different direction|` +
		`decided not to (proceed|move forward|pursue)|not (able|be able) to (move forward|proceed|offer you)|unable to offer you|` +
		`position has been filled|role (has been|is no longer) (filled|open)|no longer (open|available|considering)|filled (it|the (role|position)) internally|` +
		`not selected|were not selected|other applicants|better (meet|match|align)|more closely (match|align)` +
		`)`)},
	{KindOffer, regexp.MustCompile(`(?i)\b(` +
		`(pleased|thrilled|excited|happy|delighted) to (extend|offer)|extend (you )?an offer|offer letter|formal offer|verbal offer|` +
		`your offer from|offer of employment|we'?d love to have you join|welcome to the team` +
		`)`)},
	{KindAssessment, regexp.MustCompile(`(?i)\b(` +
		`coding (assessment|challenge|test|exercise)|(online|technical|general coding|skills) assessment|take[- ]home|` +
		`case study|modeling exercise|complete (the|your|a|an) (assessment|test|exercise|challenge)|hackerrank|codesignal|codility|coderpad assessment` +
		`)`)},
	{KindInterview, regexp.MustCompile(`(?i)\b(` +
		`(schedule|set up|book|arrange|invite you to|invitation to|invite you for|move forward with)( a| an| your| the)?( \w+){0,3} (interview|call|chat|screen|conversation|onsite|on-site|review)|` +
		`(phone|recruiter|video|technical|virtual|final|second|first)[- ](round |)(screen|interview|call|onsite)|` +
		`interview (confirmation|schedule|details|invitation|with)|your interview (for|is|on)|onsite (interview|details)|virtual onsite|` +
		`(your|share your|what does your|let us know your) availability|times? that works?( for you)?|select a time|pick a time|choose a time|` +
		`portfolio review|new event:|would like to (meet|speak|chat|talk) (with )?you|love to (chat|set up|schedule|speak)|final round` +
		`)`)},
	{KindReceived, regexp.MustCompile(`(?i)\b(` +
		`(have |we'?ve )?received your application|application (has been |was )?(successfully )?(submitted|received)|thanks? (you )?for (applying|your application)|confirmation that we'?ve received` +
		`)`)},
}

// reminder marks a note about something already arranged: it suggests nothing.
var reminder = regexp.MustCompile(`(?i)\b(reminder|see you (then|tomorrow|soon))\b`)

// Classify says what a reply is: one of the kinds, or "" when it is none.
func Classify(e *Email) string {
	text := e.Subject + "\n" + e.Text
	for _, r := range kindRules {
		if r.re.MatchString(text) {
			if r.kind == KindInterview && reminder.MatchString(text) {
				return ""
			}
			return r.kind
		}
	}
	return ""
}

// SuggestedStatus is the tracker status a kind of reply moves a job to,
// or "" when it does not move it from current: nothing goes backwards
// except to rejected, and nothing changes a closed application.
func SuggestedStatus(kind, current string) string {
	rank := map[string]int{"saved": 0, "applied": 1, "interviewing": 2, "offer": 3}
	to := map[string]string{
		KindRejection: "rejected", KindOffer: "offer", KindInterview: "interviewing",
		KindAssessment: "interviewing", KindReceived: "applied",
	}[kind]
	if to == "" || to == current || current == "rejected" || current == "not_interested" {
		return ""
	}
	if to == "rejected" {
		return to
	}
	if rank[to] <= rank[current] {
		return ""
	}
	return to
}

// App is one tracked application the matching may pick.
type App struct {
	JobID   int64
	Company string
	Token   string // the company's board name at its applicant tracking system
	Title   string
	Status  string
}

// atsDomains send mail for many employers: their domain says nothing
// about which.
var atsDomains = []string{
	"greenhouse-mail.io", "greenhouse.io", "lever.co", "ashbyhq.com", "myworkday.com", "myworkdayjobs.com",
	"icims.com", "smartrecruiters.com", "workablemail.com", "workable.com", "jobvite.com", "bamboohr.com",
	"hackerrank.com", "codesignal.com", "codility.com", "calendly.com", "goodtime.io", "gem.com", "docusign.net",
	"linkedin.com", "indeed.com", "glassdoor.com", "joinhandshake.com", "ziprecruiter.com",
}

// jobBoards are sites that send job alerts naming many employers.
var jobBoards = []string{"linkedin.com", "indeed.com", "glassdoor.com", "joinhandshake.com", "ziprecruiter.com"}

func hasSuffixDomain(domain string, list []string) bool {
	for _, d := range list {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}

// norm lower-cases s and keeps only letters and digits, with single spaces.
func norm(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// containsWords reports whether hay holds needle as whole words (both normed).
func containsWords(hay, needle string) bool {
	if needle == "" {
		return false
	}
	return strings.Contains(" "+hay+" ", " "+needle+" ")
}

// companyNames are the names a company goes by in mail: its full name and,
// for "Anduril Industries" or "Scale AI", the first word when distinctive.
func companyNames(c string) []string {
	n := norm(c)
	names := []string{n}
	for _, suffix := range []string{" inc", " llc", " ltd", " corp", " corporation", " company", " co", " industries", " technologies", " labs", " ai", " hq", " group"} {
		if s := strings.TrimSuffix(n, suffix); s != n && len(s) >= 4 {
			names = append(names, s)
		}
	}
	return names
}

// domainLabels are the parts of a domain before its last label:
// "mail.datadoghq.com" gives "mail" and "datadoghq".
func domainLabels(domain string) []string {
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return nil
	}
	return parts[:len(parts)-1]
}

// Match picks the tracked application a reply is about, or nil. The
// company must be named by the sender's own domain, the sender's name or
// the subject; the body alone is not enough (job alerts and newsletters
// name many). Between several applications at one company, the title
// decides; without it, the reply is not matched.
func Match(e *Email, apps []App) *App {
	domain := e.FromDomain()
	if hasSuffixDomain(domain, jobBoards) {
		return nil
	}
	ats := hasSuffixDomain(domain, atsDomains)
	head := norm(e.FromName + " " + e.Subject)
	body := norm(e.Text)
	labels := domainLabels(domain)

	type cand struct {
		app   *App
		score int
	}
	var best []cand
	for i := range apps {
		a := &apps[i]
		score := 0
		if !ats {
			for _, l := range labels {
				t := strings.ReplaceAll(norm(a.Company), " ", "")
				if l == t || l == a.Token || (len(t) >= 5 && strings.HasPrefix(l, t)) {
					score += 3
					break
				}
			}
		}
		for _, n := range companyNames(a.Company) {
			if containsWords(head, n) {
				score += 2
				break
			}
		}
		if score == 0 {
			continue
		}
		for _, n := range companyNames(a.Company) {
			if containsWords(body, n) {
				score++
				break
			}
		}
		best = append(best, cand{a, score})
	}
	if len(best) == 0 {
		return nil
	}
	top := 0
	for _, c := range best {
		top = max(top, c.score)
	}
	var tied []*App
	for _, c := range best {
		if c.score == top {
			tied = append(tied, c.app)
		}
	}
	if len(tied) == 1 {
		return tied[0]
	}
	// One company, several roles: the title named decides.
	all := head + " " + body
	var named []*App
	longest := 0
	for _, a := range tied {
		t := norm(a.Title)
		if containsWords(all, t) {
			switch {
			case len(t) > longest:
				named, longest = []*App{a}, len(t)
			case len(t) == longest:
				named = append(named, a)
			}
		}
	}
	if len(named) == 1 {
		return named[0]
	}
	return nil
}

// Suggestion is what one reply suggests for the tracker.
type Suggestion struct {
	App    *App
	Kind   string
	Status string
}

// Suggest matches a reply and says what it suggests, or nil when it
// suggests nothing.
func Suggest(e *Email, apps []App) *Suggestion {
	kind := Classify(e)
	if kind == "" {
		return nil
	}
	a := Match(e, apps)
	if a == nil {
		return nil
	}
	st := SuggestedStatus(kind, a.Status)
	if st == "" {
		return nil
	}
	return &Suggestion{App: a, Kind: kind, Status: st}
}
