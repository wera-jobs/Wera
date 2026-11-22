package profile

import (
	"fmt"
	"regexp"
	"strings"

	"wera/internal/normalize"
	"wera/internal/scoring"
)

const prepInstructions = `You prepare a candidate for an interview for one specific job. ` + scoring.Untrusted + `

Return ONLY a JSON object:
{"role": "", "they_value": [], "intro": "", "questions": [{"question": "", "kind": "", "why": "", "points": [], "prepare": ""}], "ask": []}

- role: two or three sentences on what this job is and what doing it well looks like, from the posting only.
- they_value: up to 5 short phrases the posting stresses most (skills, qualities, experience).
- intro: a 60-second answer to "Tell me about yourself" for this role, in the candidate's first person, built only from the candidate's own facts.
- questions: 8 to 10 questions this interviewer is likely to ask, most likely first. kind is one of "role" (skills and work the posting names), "behavioral" (a time when...), "motivation" (why this job, this company, this field) or "company". why: a few words on what in the posting prompts the question.
- points: 1 to 3 talking points for that question, each naming a specific experience, project, course or skill from the candidate's profile, resume or facts. Never invent or embellish experience, employers, schools, numbers, tools or results; a number may appear only if the candidate's own text has it. Keep the candidate's own verbs: do not upgrade what they did (a report someone "uses" was not "presented"; a model they "trained" was not "shipped"; work they "helped with" was not "led").
- prepare: when the candidate's facts do not cover the question, one sentence on what to prepare honestly (and leave points empty); otherwise "".
- ask: 3 to 5 thoughtful questions the candidate can ask the interviewer, specific to this posting.
` + FactsRule + `
- Do not raise visa sponsorship, work authorization or salary unless the posting does.
- Plain sentences: no markdown, and never em dashes or en dashes.`

// PrepInput is what interview prep is written from.
type PrepInput struct {
	CandidateName string
	Profile       string
	Resume        string
	Company       string
	Title         string
	Location      string
	Description   string
	// Details are the candidate's application details; they win over the profile and resume.
	Details map[string]string
}

// PrepQuestion is one likely question with the candidate's talking points.
type PrepQuestion struct {
	Question string   `json:"question"`
	Kind     string   `json:"kind"`
	Why      string   `json:"why"`
	Points   []string `json:"points"`
	Prepare  string   `json:"prepare"`
}

// InterviewPrep is the prep for one job.
type InterviewPrep struct {
	Role      string         `json:"role"`
	TheyValue []string       `json:"they_value"`
	Intro     string         `json:"intro"`
	Questions []PrepQuestion `json:"questions"`
	Ask       []string       `json:"ask"`
}

func trimRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// candidateBlock is the candidate's own material, fenced, shared by prep and practice.
func candidateBlock(name, prof, resume string, details map[string]string) string {
	var b strings.Builder
	if name = strings.TrimSpace(name); name != "" {
		fmt.Fprintf(&b, "Candidate name: %s\n\n", scoring.Fence(name))
	}
	fmt.Fprintf(&b, "%s<profile>\n%s\n</profile>\n\n", FactsBlock(details), scoring.Fence(trimRunes(prof, 8000)))
	if resume = trimRunes(resume, 8000); resume != "" {
		fmt.Fprintf(&b, "<resume>\n%s\n</resume>\n\n", scoring.Fence(resume))
	}
	return b.String()
}

func postingBlock(company, title, location, description string) string {
	desc := trimRunes(scoring.ScrubPosting(description), 8000)
	return fmt.Sprintf("<posting>\n%s\n</posting>", scoring.Fence(fmt.Sprintf("Company: %s\nTitle: %s\nLocation: %s\n\n%s", company, title, location, desc)))
}

// InterviewPrepMessages builds the request for one job's prep.
func InterviewPrepMessages(in PrepInput) []scoring.Message {
	return []scoring.Message{
		{Role: "system", Content: prepInstructions},
		{Role: "user", Content: candidateBlock(in.CandidateName, in.Profile, in.Resume, in.Details) + postingBlock(in.Company, in.Title, in.Location, in.Description)},
	}
}

var (
	prepKinds = map[string]bool{"role": true, "behavioral": true, "motivation": true, "company": true}
	numberRE  = regexp.MustCompile(`\d[\d,.]*\d|\d`)
)

// numbers lists the numbers in s, with thousands commas taken out.
func numbers(s string) []string {
	found := numberRE.FindAllString(s, -1)
	out := make([]string, 0, len(found))
	for _, m := range found {
		out = append(out, strings.ReplaceAll(m, ",", ""))
	}
	return out
}

// grounded reports whether every number in a talking point appears in the
// candidate's own text: a "40%" or "3 years" the candidate never wrote is
// an invented claim.
func grounded(point string, own map[string]bool) bool {
	for _, n := range numbers(point) {
		if !own[n] {
			return false
		}
	}
	return true
}

// cleanLine tidies one generated line: no markdown emphasis, no em or en
// dashes, single spaces, and a length cap.
func cleanLine(s string, n int) string {
	s = mdEmphasis.ReplaceAllString(s, "$1$2")
	s = spacedDash.ReplaceAllString(s, ", ")
	s = digitComma.ReplaceAllString(s, "$1-$2")
	s = strings.ReplaceAll(s, " ,", ",")
	return normalize.Clip(strings.Join(strings.Fields(s), " "), n)
}

func cleanList(in []string, limit, n int) []string {
	out := []string{}
	for _, s := range in {
		if s = cleanLine(s, n); s != "" && len(out) < limit {
			out = append(out, s)
		}
	}
	return out
}

// ParseInterviewPrep reads the model's reply. Talking points with a number
// the candidate's own text (own: profile, resume and details) does not
// have are dropped; a question left without points gets a prepare line.
func ParseInterviewPrep(reply, own string) (*InterviewPrep, error) {
	var p InterviewPrep
	if err := scoring.DecodeJSON(reply, &p); err != nil {
		return nil, fmt.Errorf("unreadable prep: %w", err)
	}
	have := map[string]bool{}
	for _, n := range numbers(own) {
		have[n] = true
	}
	out := InterviewPrep{
		Role:      cleanLine(p.Role, 600),
		TheyValue: cleanList(p.TheyValue, 5, 80),
		Intro:     cleanLine(p.Intro, 1200),
		Ask:       cleanList(p.Ask, 5, 240),
		Questions: []PrepQuestion{},
	}
	if !grounded(out.Intro, have) {
		out.Intro = ""
	}
	for _, q := range p.Questions {
		q.Question = cleanLine(q.Question, 240)
		if q.Question == "" || len(out.Questions) == 10 {
			continue
		}
		q.Kind = strings.ToLower(strings.TrimSpace(q.Kind))
		if !prepKinds[q.Kind] {
			q.Kind = "role"
		}
		q.Why = cleanLine(q.Why, 160)
		points := []string{}
		for _, pt := range cleanList(q.Points, 3, 300) {
			if grounded(pt, have) {
				points = append(points, pt)
			}
		}
		q.Points = points
		q.Prepare = cleanLine(q.Prepare, 300)
		if len(q.Points) == 0 && q.Prepare == "" {
			q.Prepare = "Think of a real example from your own experience that shows this."
		}
		out.Questions = append(out.Questions, q)
	}
	if len(out.Questions) < 5 {
		return nil, fmt.Errorf("only %d usable questions", len(out.Questions))
	}
	return &out, nil
}

const practiceInstructions = `You coach a candidate's practice answer to one interview question for one specific job. ` + scoring.Untrusted + `

Return ONLY a JSON object: {"strengths": [], "improve": [], "outline": []}
- strengths: up to 3 specific things the answer does well (quote or name them).
- improve: up to 3 specific, actionable changes, most important first (structure such as situation, action and result; a concrete example; the result or what was learned; tying it to what the posting needs; length).
- outline: 3 to 5 short steps for a stronger version of this answer, built only from what the candidate said and their own profile, resume and facts. Never invent experience, employers, numbers or results; where the answer needs something the candidate has not given, write the step as what to add, for example "Add the result: what changed after your fix".
` + FactsRule + `
- Be direct and kind. Plain sentences, no markdown, and never em dashes or en dashes.`

// PracticeFeedback is feedback on one practice answer.
type PracticeFeedback struct {
	Strengths []string `json:"strengths"`
	Improve   []string `json:"improve"`
	Outline   []string `json:"outline"`
}

// PracticeMessages builds the request for feedback on one practice answer.
func PracticeMessages(in PrepInput, question, answer string) []scoring.Message {
	user := candidateBlock(in.CandidateName, in.Profile, in.Resume, in.Details) + postingBlock(in.Company, in.Title, in.Location, in.Description) +
		fmt.Sprintf("\n\nThe interview question:\n<content>\n%s\n</content>\n\nThe candidate's practice answer:\n<answers>\n%s\n</answers>",
			scoring.Fence(trimRunes(question, 300)), scoring.Fence(trimRunes(answer, 4000)))
	return []scoring.Message{
		{Role: "system", Content: practiceInstructions},
		{Role: "user", Content: user},
	}
}

// ParsePracticeFeedback reads the model's reply. Outline steps with a
// number that neither the candidate's own text nor their answer has are
// dropped.
func ParsePracticeFeedback(reply, own string) (*PracticeFeedback, error) {
	var f PracticeFeedback
	if err := scoring.DecodeJSON(reply, &f); err != nil {
		return nil, fmt.Errorf("unreadable feedback: %w", err)
	}
	have := map[string]bool{}
	for _, n := range numbers(own) {
		have[n] = true
	}
	out := PracticeFeedback{Strengths: cleanList(f.Strengths, 3, 300), Improve: cleanList(f.Improve, 3, 300), Outline: []string{}}
	for _, s := range cleanList(f.Outline, 5, 300) {
		if grounded(s, have) {
			out.Outline = append(out.Outline, s)
		}
	}
	if len(out.Improve)+len(out.Strengths) == 0 {
		return nil, fmt.Errorf("empty feedback")
	}
	return &out, nil
}
