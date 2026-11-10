package profile

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"wera/internal/scoring"
)

const autofillInstructions = `You fill in one page of a job application form for the candidate. ` + scoring.Untrusted + `

You get today's date, the candidate's profile, resume, saved application details, answers they gave on earlier forms, the job posting, and the form's fields (id, label, kind, options, character limit) as JSON.

Return ONLY a JSON object: {"answers": [{"id": "<field id>", "value": "<answer>", "confidence": "high" or "low", "why": "<a few words: where the answer came from>"}]}

Rules:
- Use only facts from the data. Never invent employers, dates, numbers, degrees, links or credentials. Leave a field out when the data does not answer it.
- An earlier answer to the same or a very similar question is the best source; reuse it.
- For select, radio, buttons, listbox and combobox fields with options, the value must be exactly one of the options, copied as written.
- For checkboxes with options, the value is one option, or several separated by " | ".
- For a single checkbox, answer "yes" to tick it or leave it out.
- Never answer gender, race, ethnicity, sexual orientation, transgender, veteran, disability or other self-identification questions, legal agreements, signatures, consents or attestations: leave them out. The candidate answers those.
- Dates as YYYY-MM-DD unless the label shows another format. Numbers as plain digits.
- A start date or availability date is never before today. Use the candidate's stated start date only when it is still ahead; with only a notice period, count it from today; otherwise leave the field out.
- Text fields get a short, plain answer (a name, a number, a few words). Longer free-text questions get at most 80 words in the first person, specific to this job, and confidence "low".
- Salary questions: use the candidate's stated expectation only; never guess one.
- confidence "high" when the data states the answer directly; "low" when you inferred it.
- A "no" to what the candidate has, can do or would accept can end their application, so it is "high" only when the data says so outright. Something missing from the resume is not a no: a skill, license or certification it does not mention, or a place or schedule it does not address, is confidence "low".
- Never use em dashes or en dashes.`

// FormField is one field of an application form page, as the extension reads it.
type FormField struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Kind      string   `json:"kind"`
	Options   []string `json:"options,omitempty"`
	Required  bool     `json:"required,omitempty"`
	MaxLength int      `json:"max_length,omitempty"`
}

// PriorAnswer is an answer the candidate gave on an earlier form.
type PriorAnswer struct {
	Question string `json:"q"`
	Answer   string `json:"a"`
}

// AutofillInput is what one page of suggestions is made from.
type AutofillInput struct {
	Fields      []FormField
	Profile     string
	Resume      string
	Details     map[string]string // application details, blanks dropped
	Prior       []PriorAnswer
	Company     string
	Title       string
	Description string
	Today       time.Time // zero means now
}

// Suggestion is the AI's answer for one field.
type Suggestion struct {
	ID         string `json:"id"`
	Value      string `json:"value"`
	Confidence string `json:"confidence"`
	Why        string `json:"why,omitempty"`
}

// Bounds on one autofill request.
const (
	MaxAutofillFields  = 60
	maxAutofillOptions = 120
)

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// CleanFields is the form's fields as a person reads them, without what
// their labels and options say to AI tools; a field left with no question
// is dropped. The extension does the same: this covers older versions.
func CleanFields(fields []FormField) []FormField {
	out := make([]FormField, 0, len(fields))
	for _, f := range fields {
		f.Label = scoring.ScrubPosting(f.Label)
		if f.Label == "" {
			continue
		}
		var opts []string
		for _, o := range f.Options {
			if c := scoring.ScrubPosting(o); c == strings.TrimSpace(o) {
				opts = append(opts, o)
			}
		}
		f.Options = opts
		out = append(out, f)
	}
	return out
}

// AutofillMessages builds the request for one form page.
func AutofillMessages(in AutofillInput) []scoring.Message {
	fields, _ := json.Marshal(CleanFields(in.Fields))
	details, _ := json.Marshal(in.Details)
	var prior strings.Builder
	for _, p := range in.Prior {
		line, _ := json.Marshal(p)
		if prior.Len()+len(line) > 6000 {
			break
		}
		prior.Write(line)
		prior.WriteByte('\n')
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Today: %s\n\n", day(in.Today))
	fmt.Fprintf(&b, "<profile>\n%s\n\nApplication details: %s\n</profile>\n\n", scoring.Fence(cutRunes(in.Profile, 6000)), scoring.Fence(string(details)))
	if in.Resume != "" {
		fmt.Fprintf(&b, "<resume>\n%s\n</resume>\n\n", scoring.Fence(cutRunes(in.Resume, 7000)))
	}
	if prior.Len() > 0 {
		fmt.Fprintf(&b, "<answers>\n%s</answers>\n\n", scoring.Fence(prior.String()))
	}
	fmt.Fprintf(&b, "<posting>\n%s\n</posting>\n\n<content>\n%s\n</content>",
		scoring.Fence(cutRunes(fmt.Sprintf("Company: %s\nTitle: %s\n\n%s", in.Company, in.Title, scoring.ScrubPosting(in.Description)), 5000)),
		scoring.Fence(string(fields)))
	return []scoring.Message{
		{Role: "system", Content: autofillInstructions},
		{Role: "user", Content: b.String()},
	}
}

var choiceKinds = map[string]bool{"select": true, "radio": true, "buttons": true, "listbox": true, "combobox": true, "checkboxes": true}

// matchChoice returns the option a value names (case and spacing aside), or "".
func matchChoice(options []string, v string) string {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	for _, o := range options {
		if norm(o) == norm(v) {
			return o
		}
	}
	return ""
}

// day is the date the AI is told it is today.
func day(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.Format("2006-01-02")
}

var (
	startLabel = regexp.MustCompile(`(?i)start|availab|join|begin|commence`)
	isoMonth   = regexp.MustCompile(`^\d{4}-\d{1,2}$`)
	usDate     = regexp.MustCompile(`^\d{1,2}/\d{1,2}/\d{4}$`)
	yearOnly   = regexp.MustCompile(`\b(19|20)\d{2}\b`)
)

// pastStart reports whether v is a start or availability date that has
// already gone by: a form must never be told the candidate starts in the past.
func pastStart(label, v string, today time.Time) bool {
	if !startLabel.MatchString(label) {
		return false
	}
	if today.IsZero() {
		today = time.Now()
	}
	y, m, d := today.Date()
	cut := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	v = strings.TrimSpace(v)
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.Before(cut)
	}
	if usDate.MatchString(v) {
		t, err := time.Parse("1/2/2006", v)
		return err == nil && t.Before(cut)
	}
	if isoMonth.MatchString(v) {
		t, err := time.Parse("2006-1", v)
		return err == nil && !t.AddDate(0, 1, 0).After(cut) // the whole month has gone by
	}
	// "January 2025", "2025": only a year to go by.
	if ys := yearOnly.FindAllString(v, -1); len(ys) > 0 {
		for _, s := range ys {
			if n, _ := strconv.Atoi(s); n >= y {
				return false
			}
		}
		return true
	}
	return false
}

// ParseAutofill keeps the suggestions that fit their field: a known id, an
// option that exists, a length within the field's limit, no start date
// that has passed. Dashes are replaced as in letters.
func ParseAutofill(reply string, fields []FormField, today time.Time) ([]Suggestion, error) {
	var raw struct {
		Answers []struct {
			ID         string `json:"id"`
			Value      any    `json:"value"`
			Confidence string `json:"confidence"`
			Why        string `json:"why"`
		} `json:"answers"`
	}
	if err := scoring.DecodeJSON(reply, &raw); err != nil {
		return nil, fmt.Errorf("unreadable suggestions: %w", err)
	}
	byID := map[string]FormField{}
	for _, f := range fields {
		byID[f.ID] = f
	}
	out := []Suggestion{}
	seen := map[string]bool{}
	for _, a := range raw.Answers {
		f, ok := byID[a.ID]
		if !ok || seen[a.ID] {
			continue
		}
		var v string
		switch t := a.Value.(type) {
		case string:
			v = t
		case float64:
			v = strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
		case bool:
			if t {
				v = "yes"
			}
		}
		v = strings.TrimSpace(CleanLetter(v))
		if v == "" || pastStart(f.Label, v, today) {
			continue
		}
		if choiceKinds[f.Kind] && len(f.Options) > 0 {
			var picked []string
			for _, part := range strings.Split(v, "|") {
				if o := matchChoice(f.Options, strings.TrimSpace(part)); o != "" {
					picked = append(picked, o)
				}
			}
			if len(picked) == 0 || (f.Kind != "checkboxes" && len(picked) > 1) {
				continue
			}
			v = strings.Join(picked, " | ")
		}
		if f.Kind == "checkbox" && strings.ToLower(v) != "yes" {
			continue
		}
		limit := 600
		if f.Kind == "textarea" {
			limit = 2000
		}
		if f.MaxLength > 0 && f.MaxLength < limit {
			limit = f.MaxLength
		}
		if utf8.RuneCountInString(v) > limit {
			v = cutRunes(v, limit)
		}
		conf := "low"
		if a.Confidence == "high" {
			conf = "high"
		}
		seen[a.ID] = true
		out = append(out, Suggestion{ID: a.ID, Value: v, Confidence: conf, Why: cutRunes(strings.TrimSpace(a.Why), 120)})
	}
	return out, nil
}
