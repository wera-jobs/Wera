package profile

import (
	"fmt"
	"strings"

	"wera/internal/scoring"
)

// MaxPageChars bounds the page text sent for extraction.
const MaxPageChars = 24000

const extractInstructions = `You read the text of one web page and extract the job posting on it, if there is one. ` + scoring.Untrusted + `

Return ONLY a JSON object:
{"is_job_posting": true|false, "company": "", "title": "", "location": "", "work_mode": "remote"|"hybrid"|"onsite"|"", "employment_type": "full_time"|"part_time"|"contract"|"internship"|"", "department": "", "salary": "", "industry": "<one id from INDUSTRIES or other>", "description_start": "", "description_end": ""}

Rules:
- Copy company, title and location as written on the page; "" when absent. Never guess.
- salary: the pay range exactly as written, or "".
- description_start: the first 6 to 10 words of the job description, copied exactly from the page.
- description_end: the last 6 to 10 words of the job description (before footer, legal or apply-button text), copied exactly.
- If the page is not a single job posting (a list, a login page, an article), set is_job_posting to false.`

// JobDraft is a job posting read off a page; the user reviews it.
type JobDraft struct {
	IsJobPosting   bool   `json:"is_job_posting"`
	Company        string `json:"company"`
	Title          string `json:"title"`
	Location       string `json:"location"`
	WorkMode       string `json:"work_mode"`
	EmploymentType string `json:"employment_type"`
	Department     string `json:"department"`
	Salary         string `json:"salary"`
	Industry       string `json:"industry"`
	Description    string `json:"description"`
	Source         string `json:"source"` // "ai"
}

// ExtractMessages builds the extraction request for a page.
func ExtractMessages(pageURL, pageTitle, text string, industries []string) []scoring.Message {
	if r := []rune(text); len(r) > MaxPageChars {
		text = string(r[:MaxPageChars])
	}
	body := fmt.Sprintf("INDUSTRIES: %s\n\n<content>\nURL: %s\nPage title: %s\n\n%s\n</content>",
		strings.Join(industries, ", "), scoring.Fence(pageURL), scoring.Fence(pageTitle), scoring.Fence(text))
	return []scoring.Message{
		{Role: "system", Content: extractInstructions},
		{Role: "user", Content: body},
	}
}

var validModes = map[string]bool{"remote": true, "hybrid": true, "onsite": true, "": true}
var validTypes = map[string]bool{"full_time": true, "part_time": true, "contract": true, "internship": true, "": true}

// ParseJobDraft reads the reply and cuts the description out of the page
// text between the quoted start and end, so it is the page's own words
// (the model never retypes it). Without usable anchors the whole page
// text is kept for the user to trim.
func ParseJobDraft(reply, text string, industries map[string]bool) (*JobDraft, error) {
	var raw struct {
		JobDraft
		Start string `json:"description_start"`
		End   string `json:"description_end"`
	}
	if err := scoring.DecodeJSON(reply, &raw); err != nil {
		return nil, fmt.Errorf("unreadable extraction: %w", err)
	}
	d := raw.JobDraft
	d.Source = "ai"
	clip := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if r := []rune(s); len(r) > n {
			return string(r[:n])
		}
		return s
	}
	d.Company, d.Title, d.Location = clip(d.Company, 140), clip(d.Title, 200), clip(d.Location, 200)
	d.Department, d.Salary = clip(d.Department, 140), clip(d.Salary, 140)
	if !validModes[d.WorkMode] {
		d.WorkMode = ""
	}
	if !validTypes[d.EmploymentType] {
		d.EmploymentType = ""
	}
	if !industries[d.Industry] {
		d.Industry = "other"
	}
	d.Description = sliceBetween(text, raw.Start, raw.End)
	return &d, nil
}

// sliceBetween returns text from the first occurrence of start through
// the last occurrence of end (whitespace-insensitive), or the whole text.
func sliceBetween(text, start, end string) string {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	t := norm(text)
	full := strings.TrimSpace(text)
	if r := []rune(full); len(r) > MaxPageChars {
		full = string(r[:MaxPageChars])
	}
	s, e := norm(start), norm(end)
	if len(s) < 12 || len(e) < 12 {
		return full
	}
	i := strings.Index(t, s)
	j := strings.LastIndex(t, e)
	if i < 0 || j < 0 || j+len(e) <= i {
		return full
	}
	return t[i : j+len(e)]
}
