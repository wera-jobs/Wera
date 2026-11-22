// Package resume is Wera's structured resume: one document per user plus
// tailored copies per job, rendered to PDF in a classic one-column
// template (the widely used "Jake's resume" layout), fitted to one page,
// exported as LaTeX, and compared against a job's keywords.
package resume

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Kind is how a section's entries are laid out.
type Kind string

const (
	// KindEntries renders a bold heading and dates, italic subheading and location,
	// then bullets (education, experience).
	KindEntries Kind = "entries"
	// KindProjects renders a bold name with the tech stack on the right, then a
	// short description.
	KindProjects Kind = "projects"
	// KindCompact renders one line: "Title | Organization" and dates.
	KindCompact Kind = "compact"
	// KindSkills renders "Group: items" lines.
	KindSkills Kind = "skills"
	// KindSummary renders a short paragraph.
	KindSummary Kind = "summary"
)

// Bullet is one achievement line.
type Bullet struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Hidden bool   `json:"hidden,omitempty"`
}

// Entry is one row of a section; which fields show depends on the kind.
type Entry struct {
	ID         string   `json:"id"`
	Heading    string   `json:"heading"`              // organization, school, project name, or title (compact)
	Subheading string   `json:"subheading,omitempty"` // job title, degree, or organization (compact)
	Dates      string   `json:"dates,omitempty"`
	Location   string   `json:"location,omitempty"`
	Tech       string   `json:"tech,omitempty"` // projects: the stack
	Text       string   `json:"text,omitempty"` // projects: the description
	URL        string   `json:"url,omitempty"`  // optional link (on the heading, or after a project description)
	LinkLabel  string   `json:"link_label,omitempty"`
	Bullets    []Bullet `json:"bullets,omitempty"`
	Hidden     bool     `json:"hidden,omitempty"`
}

// SkillGroup is one "Languages: Go, Python" line.
type SkillGroup struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Items string `json:"items"`
}

// Section is a titled block of the resume.
type Section struct {
	ID      string       `json:"id"`
	Kind    Kind         `json:"kind"`
	Title   string       `json:"title"`
	Hidden  bool         `json:"hidden,omitempty"`
	Entries []Entry      `json:"entries,omitempty"`
	Skills  []SkillGroup `json:"skills,omitempty"`
	Text    string       `json:"text,omitempty"` // summary
}

// Link is a contact link shown in the header.
type Link struct {
	Label string `json:"label"` // shown text, e.g. linkedin.com/in/ada
	URL   string `json:"url"`
}

// Resume is the whole document.
type Resume struct {
	Name     string    `json:"name"`
	Headline string    `json:"headline,omitempty"` // optional line under the name: "Backend Engineer"
	Phone    string    `json:"phone,omitempty"`
	Email    string    `json:"email,omitempty"`
	Location string    `json:"location,omitempty"`
	Links    []Link    `json:"links,omitempty"`
	Sections []Section `json:"sections"`
}

// Layout is how the document is set. Fit chooses the size, spacing and
// margins; the user picks the template, accent and paper.
type Layout struct {
	FontSize float64 `json:"font_size"`          // points, 10 to 11
	Spacing  float64 `json:"spacing"`            // gaps between blocks, 0.7 to 1.2 (line height stays readable)
	Margin   float64 `json:"margin"`             // inches, 0.35 to 0.75
	Template string  `json:"template,omitempty"` // a Templates id; "" is ivy
	Accent   string  `json:"accent,omitempty"`   // #rrggbb, for templates that use color
	Paper    string  `json:"paper,omitempty"`    // "us-letter" (default) or "a4"
}

// DefaultLayout matches the classic LaTeX template at 11pt.
var DefaultLayout = Layout{FontSize: 11, Spacing: 1, Margin: 0.5, Template: "ivy", Accent: DefaultAccent, Paper: "us-letter"}

// DefaultAccent is a deep blue.
const DefaultAccent = "#1d4ed8"

// Template describes one resume design.
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Tagline     string `json:"tagline"`
	Description string `json:"description"`
	UsesAccent  bool   `json:"uses_accent"`
	ATS         string `json:"ats"` // how plainly applicant tracking systems read it
}

// Templates are the designs a resume can use, in display order.
var Templates = []Template{
	{ID: "ivy", Name: "Ivy", Tagline: "The classic tech resume",
		Description: "The one-column LaTeX resume recruiters at big tech know on sight: Computer Modern, small-caps headings over rules, dense and calm.", ATS: "excellent"},
	{ID: "parse", Name: "Parse", Tagline: "ATS-maxxing",
		Description: "Built for applicant tracking systems first: a plain sans, standard headings, \"Title, Company\" lines in reading order, no small caps or ligatures.", ATS: "best"},
	{ID: "signal", Name: "Signal", Tagline: "Modern, with a color accent",
		Description: "Inter, a left-aligned header with your headline in color, hairline section rules and a skills grid. Startups, product and design-minded teams.", UsesAccent: true, ATS: "excellent"},
	{ID: "ledger", Name: "Ledger", Tagline: "Refined serif",
		Description: "Libertinus Serif with a centered, letterspaced name and titles between hairlines. Finance, consulting, research and law.", ATS: "excellent"},
}

// TemplateByID returns the template, or Ivy when the id is unknown.
func TemplateByID(id string) Template {
	for _, t := range Templates {
		if t.ID == id {
			return t
		}
	}
	return Templates[0]
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Limits keep documents (and render times) bounded.
const (
	maxSections = 12
	maxEntries  = 30
	maxBullets  = 12
	maxField    = 300
	maxBullet   = 600
	maxText     = 1500
)

func newID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func clampLen(field, s string, n int) error {
	if utf8.RuneCountInString(s) > n {
		return fmt.Errorf("%s is too long (%d characters at most)", field, n)
	}
	return nil
}

// clean trims a field and drops placeholder values ("None", "null")
// that converters sometimes leave in empty fields.
func clean(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "none", "null", "nil", "undefined", "n/a", "-":
		return ""
	}
	return s
}

// SafeURL accepts http(s) and mailto links only.
func SafeURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "mailto") && (u.Host != "" || u.Scheme == "mailto")
}

// Normalize trims fields, assigns missing ids and validates limits, kinds
// and links. It changes r in place.
func (r *Resume) Normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return errors.New("the resume needs your name")
	}
	r.Headline = clean(r.Headline)
	r.Phone, r.Email, r.Location = clean(r.Phone), clean(r.Email), clean(r.Location)
	for _, f := range []struct{ name, v string }{{"name", r.Name}, {"headline", r.Headline}, {"phone", r.Phone}, {"email", r.Email}, {"location", r.Location}} {
		if err := clampLen(f.name, f.v, maxField); err != nil {
			return err
		}
	}
	if len(r.Links) > 6 {
		return errors.New("at most 6 links")
	}
	for i := range r.Links {
		l := &r.Links[i]
		l.Label, l.URL = strings.TrimSpace(l.Label), strings.TrimSpace(l.URL)
		if !SafeURL(l.URL) {
			return fmt.Errorf("link %q must be an http(s) or mailto address", l.Label)
		}
		if l.Label == "" {
			l.Label = strings.TrimPrefix(strings.TrimPrefix(l.URL, "https://"), "http://")
		}
	}
	if len(r.Sections) > maxSections {
		return fmt.Errorf("at most %d sections", maxSections)
	}
	for si := range r.Sections {
		s := &r.Sections[si]
		if s.ID == "" {
			s.ID = newID()
		}
		s.Title = strings.TrimSpace(s.Title)
		switch s.Kind {
		case KindEntries, KindProjects, KindCompact, KindSkills, KindSummary:
		default:
			return fmt.Errorf("section %q has an unknown kind %q", s.Title, s.Kind)
		}
		if err := clampLen("section title", s.Title, 80); err != nil {
			return err
		}
		if err := clampLen("summary", s.Text, maxText); err != nil {
			return err
		}
		if len(s.Entries) > maxEntries || len(s.Skills) > maxEntries {
			return fmt.Errorf("section %q has too many entries", s.Title)
		}
		for gi := range s.Skills {
			g := &s.Skills[gi]
			if g.ID == "" {
				g.ID = newID()
			}
			g.Name, g.Items = strings.TrimSpace(g.Name), strings.TrimSpace(g.Items)
			if err := clampLen("skill group", g.Name+g.Items, maxText); err != nil {
				return err
			}
		}
		for ei := range s.Entries {
			e := &s.Entries[ei]
			if e.ID == "" {
				e.ID = newID()
			}
			for _, f := range []*string{&e.Heading, &e.Subheading, &e.Dates, &e.Location, &e.Tech, &e.Text, &e.URL, &e.LinkLabel} {
				*f = clean(*f)
			}
			for _, f := range []string{e.Heading, e.Subheading, e.Dates, e.Location, e.Tech} {
				if err := clampLen("entry field", f, maxField); err != nil {
					return err
				}
			}
			if err := clampLen("description", e.Text, maxText); err != nil {
				return err
			}
			if !SafeURL(e.URL) {
				return fmt.Errorf("the link on %q must be an http(s) address", e.Heading)
			}
			if len(e.Bullets) > maxBullets {
				return fmt.Errorf("%q has more than %d bullets", e.Heading, maxBullets)
			}
			kept := e.Bullets[:0]
			for bi := range e.Bullets {
				b := e.Bullets[bi]
				b.Text = strings.TrimSpace(b.Text)
				if b.Text == "" {
					continue
				}
				if b.ID == "" {
					b.ID = newID()
				}
				if err := clampLen("bullet", b.Text, maxBullet); err != nil {
					return err
				}
				kept = append(kept, b)
			}
			e.Bullets = kept
		}
	}
	return nil
}

// Clamp keeps a layout within readable bounds.
func (l Layout) Clamp() Layout {
	c := func(v, lo, hi, def float64) float64 {
		if v == 0 {
			return def
		}
		return max(lo, min(hi, v))
	}
	out := Layout{FontSize: c(l.FontSize, 10, 11, 11), Spacing: c(l.Spacing, 0.7, 1.2, 1), Margin: c(l.Margin, 0.35, 0.75, 0.5),
		Template: TemplateByID(l.Template).ID, Accent: strings.ToLower(l.Accent), Paper: l.Paper}
	if !hexColor.MatchString(out.Accent) {
		out.Accent = DefaultAccent
	}
	if out.Paper != "a4" {
		out.Paper = "us-letter"
	}
	return out
}

// PlainText is the visible resume as text (for keyword checks, AI prompts
// and copying), hidden parts left out.
func (r *Resume) PlainText() string {
	var b strings.Builder
	line := func(parts ...string) {
		var nonEmpty []string
		for _, p := range parts {
			if p != "" {
				nonEmpty = append(nonEmpty, p)
			}
		}
		if len(nonEmpty) > 0 {
			b.WriteString(strings.Join(nonEmpty, " | "))
			b.WriteByte('\n')
		}
	}
	line(r.Name)
	line(r.Headline)
	contacts := make([]string, 0, 3+len(r.Links))
	contacts = append(contacts, r.Phone, r.Email, r.Location)
	for _, l := range r.Links {
		contacts = append(contacts, l.Label)
	}
	line(contacts...)
	for _, s := range r.Sections {
		if s.Hidden {
			continue
		}
		b.WriteString("\n" + strings.ToUpper(s.Title) + "\n")
		if s.Text != "" {
			line(s.Text)
		}
		for _, g := range s.Skills {
			if g.Name == "" {
				line(g.Items)
			} else {
				line(g.Name + ": " + g.Items)
			}
		}
		for _, e := range s.Entries {
			if e.Hidden {
				continue
			}
			line(e.Heading, e.Subheading, e.Tech, e.Dates, e.Location)
			if e.Text != "" {
				line(e.Text)
			}
			for _, bl := range e.Bullets {
				if !bl.Hidden {
					b.WriteString("- " + bl.Text + "\n")
				}
			}
		}
	}
	return strings.TrimSpace(b.String())
}
