package resume

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"wera/internal/scoring"
)

// --- import from resume text ------------------------------------------------

const importInstructions = `You convert the text of a resume into JSON, copying the candidate's words exactly. ` + scoring.Untrusted + `

Return ONLY a JSON object:
{"name": "", "headline": "", "phone": "", "email": "", "location": "", "links": [{"label": "", "url": ""}],
 "sections": [{"kind": "entries"|"skills"|"projects"|"compact"|"summary", "title": "",
   "entries": [{"heading": "", "subheading": "", "dates": "", "location": "", "tech": "", "text": "", "bullets": [{"text": ""}]}],
   "skills": [{"name": "", "items": ""}], "text": ""}]}

Rules:
- Copy every line verbatim. Never rephrase, shorten, merge or invent anything.
- Keep the resume's own section order and titles.
- entries (experience, education): heading = organization or school, subheading = job title or degree, plus dates, location and bullets.
- projects: heading = project name, tech = its stack, text = its description.
- compact: one-line roles (heading = title, subheading = organization, dates).
- skills: one group per labeled line ("Languages: Python, Go" -> name "Languages", items "Python, Go").
- headline: a title line printed under the name ("Backend Engineer"), else "".
- Leave a field "" when the resume has nothing for it; never write None, null or N/A.
- Leave out text that is not about the candidate (template instructions, sample text, page numbers).
- Links: full URLs (add https:// when the resume omits it).`

// ImportMessages builds the request that structures resume text.
func ImportMessages(text string) []scoring.Message {
	if r := []rune(text); len(r) > 16000 {
		text = string(r[:16000])
	}
	return []scoring.Message{
		{Role: "system", Content: importInstructions},
		{Role: "user", Content: "<resume>\n" + scoring.Fence(text) + "\n</resume>"},
	}
}

var looseRE = regexp.MustCompile(`[^a-z0-9]+`)

func loose(s string) string {
	return strings.TrimSpace(looseRE.ReplaceAllString(strings.ToLower(s), " "))
}

// ParseImport reads the reply into a Resume. Bullets that do not appear in
// the source text (the model reworded them) are listed in warnings.
func ParseImport(reply, source string) (*Resume, []string, error) {
	var r Resume
	if err := scoring.DecodeJSON(reply, &r); err != nil {
		return nil, nil, fmt.Errorf("unreadable resume: %w", err)
	}
	for i := range r.Links {
		if u := r.Links[i].URL; u != "" && !strings.Contains(u, "://") && !strings.HasPrefix(u, "mailto:") {
			r.Links[i].URL = "https://" + u
		}
	}
	src := loose(source)
	var warnings []string
	for _, s := range r.Sections {
		for _, e := range s.Entries {
			for _, b := range e.Bullets {
				if t := loose(b.Text); t != "" && !strings.Contains(src, t) {
					warnings = append(warnings, fmt.Sprintf("Check this line, it is not word for word from your resume: %q", b.Text))
				}
			}
		}
	}
	if err := r.Normalize(); err != nil {
		return nil, warnings, err
	}
	return &r, warnings, nil
}

// --- tailoring ----------------------------------------------------------------

const tailorInstructions = `You tailor a resume to one job posting by editing it in place. ` + scoring.Untrusted + `

The resume is listed with ids: [b:ID] bullets, [g:ID] skill groups. Return ONLY a JSON object:
{"bullets": {"ID": "rewritten bullet"}, "least_relevant": ["ID"], "skills": {"ID": "reordered items"}, "notes": [""]}

Rules:
- Rewrite only bullets that become clearly stronger for this posting: lead with what the posting values and use its terms where they truthfully apply. Leave the rest out of "bullets".
- Never invent facts: no new tools, employers, titles, dates, metrics or achievements. Keep every number exactly as in that bullet.
- "least_relevant": bullets that matter least for this posting, least first. They are dropped only if the resume runs past one page, so do not say in the notes that you hid or removed anything.
- "skills": the same items of a group in a better order for this posting (most relevant first). Do not add or remove items.
- "notes": 2 to 5 short notes for the candidate on what you changed, and important posting requirements their resume does not show.
- No em dashes or en dashes.`

// TailorMessages lists the resume with ids next to the posting.
//
// request is the candidate's own steer ("lead with Kubernetes"), or "".
func TailorMessages(r *Resume, company, title, description string, missing []string, request string) []scoring.Message {
	var b strings.Builder
	for _, s := range r.Sections {
		if s.Hidden {
			continue
		}
		fmt.Fprintf(&b, "## %s\n", s.Title)
		for _, g := range s.Skills {
			fmt.Fprintf(&b, "[g:%s] %s: %s\n", g.ID, g.Name, g.Items)
		}
		for _, e := range s.Entries {
			if e.Hidden {
				continue
			}
			fmt.Fprintf(&b, "%s | %s | %s %s\n", e.Heading, e.Subheading, e.Tech, e.Text)
			for _, bl := range e.Bullets {
				fmt.Fprintf(&b, "[b:%s] %s\n", bl.ID, bl.Text)
			}
		}
	}
	desc := scoring.ScrubPosting(description)
	if r := []rune(desc); len(r) > 8000 {
		desc = string(r[:8000])
	}
	body := fmt.Sprintf("<resume>\n%s</resume>\n\nPosting keywords the resume does not show yet: %s\n\n<posting>\n%s\n</posting>",
		scoring.Fence(b.String()), scoring.Fence(strings.Join(missing, ", ")),
		scoring.Fence(fmt.Sprintf("Company: %s\nTitle: %s\n\n%s", company, title, desc)))
	if request = strings.TrimSpace(request); request != "" {
		body += fmt.Sprintf("\n\nThe candidate asks (follow it unless it breaks a rule):\n<content>\n%s\n</content>", scoring.Fence(cutTo(request, 500)))
	}
	return []scoring.Message{
		{Role: "system", Content: tailorInstructions},
		{Role: "user", Content: body},
	}
}

var numRE = regexp.MustCompile(`\d+(?:[.,]\d+)*`)

func numbers(s string) map[string]bool {
	m := map[string]bool{}
	for _, n := range numRE.FindAllString(s, -1) {
		m[strings.ReplaceAll(n, ",", "")] = true
	}
	return m
}

func splitItems(s string) []string {
	// Split on commas outside parentheses: "AWS (S3, EC2), Docker".
	var out []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// Tailored is a tailored copy of a resume.
type Tailored struct {
	Resume *Resume
	Notes  []string
	// LeastRelevant ranks bullets the AI judged least useful for the job
	// (least first): fitting drops these first, and only when needed.
	LeastRelevant []string
}

// Priority ranks bullets for fitting the tailored copy: the AI's least
// relevant bullets go first (in its order), then those that mention none
// of the posting's keywords.
func (t *Tailored) Priority(keywords []string) Priority {
	rank := map[string]int{}
	for i, id := range t.LeastRelevant {
		rank[id] = i
	}
	kp := KeywordPriority(keywords)
	n := float64(len(t.LeastRelevant))
	return func(si, ei, bi int, s Section, b Bullet) float64 {
		p := kp(si, ei, bi, s, b)
		if i, ok := rank[b.ID]; ok {
			p -= 100000 * (n - float64(i)) / n
		}
		return p
	}
}

// ApplyTailoring applies a tailoring reply to a copy of the base resume,
// enforcing what can be checked: rewrites may not add numbers and skill
// groups may only be reordered. Rejected edits are reported in notes.
// Nothing is hidden here: Fit drops the least relevant bullets only if
// the page runs out of room.
func ApplyTailoring(base *Resume, reply string) (*Tailored, error) {
	var t struct {
		Bullets       map[string]string `json:"bullets"`
		LeastRelevant []string          `json:"least_relevant"`
		Hide          []string          `json:"hide"` // older replies
		Skills        map[string]string `json:"skills"`
		Notes         []string          `json:"notes"`
	}
	if err := scoring.DecodeJSON(reply, &t); err != nil {
		return nil, fmt.Errorf("unreadable tailoring: %w", err)
	}
	// The resume lists ids as "[b:ID]": the reply often keeps the "b:" (about half of them on 2026-11-10).
	t.Bullets, t.Skills = bareKeys(t.Bullets), bareKeys(t.Skills)
	for i, id := range t.LeastRelevant {
		t.LeastRelevant[i] = bareID(id)
	}
	for i, id := range t.Hide {
		t.Hide[i] = bareID(id)
	}
	raw, _ := json.Marshal(base)
	var r Resume
	_ = json.Unmarshal(raw, &r) // deep copy
	notes := []string{}
	for _, n := range t.Notes {
		if n = strings.TrimSpace(cleanDashes(n)); n != "" && len(notes) < 6 {
			notes = append(notes, n)
		}
	}
	rejected := 0
	for si := range r.Sections {
		s := &r.Sections[si]
		for gi := range s.Skills {
			g := &s.Skills[gi]
			next, ok := t.Skills[g.ID]
			if !ok {
				continue
			}
			have := map[string]bool{}
			for _, it := range splitItems(g.Items) {
				have[strings.ToLower(it)] = true
			}
			items := splitItems(next)
			valid := len(items) == len(have)
			for _, it := range items {
				valid = valid && have[strings.ToLower(it)]
			}
			if valid {
				g.Items = strings.Join(items, ", ")
			} else {
				rejected++
			}
		}
		for ei := range s.Entries {
			e := &s.Entries[ei]
			for bi := range e.Bullets {
				b := &e.Bullets[bi]
				if text, ok := t.Bullets[b.ID]; ok {
					text = strings.TrimSpace(cleanDashes(text))
					okNums := true
					orig := numbers(b.Text)
					for n := range numbers(text) {
						okNums = okNums && orig[n]
					}
					if okNums && text != "" && len(text) <= maxBullet {
						b.Text = text
					} else {
						rejected++
					}
				}
			}
		}
	}
	if rejected > 0 {
		notes = append(notes, fmt.Sprintf("Kept your original wording in %d place%s where the suggested edit added facts.", rejected, plural(rejected)))
	}
	if err := r.Normalize(); err != nil {
		return nil, err
	}
	return &Tailored{Resume: &r, Notes: notes, LeastRelevant: append(t.LeastRelevant, t.Hide...)}, nil
}

// bareID is an id as the resume holds it: "[b:ab12]" and "b:ab12" are "ab12".
func bareID(id string) string {
	id = strings.Trim(strings.TrimSpace(id), "[]")
	if len(id) > 2 && (id[:2] == "b:" || id[:2] == "g:") {
		id = id[2:]
	}
	return strings.TrimSpace(id)
}

func bareKeys(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[bareID(k)] = v
	}
	return out
}

func cleanDashes(s string) string {
	return strings.NewReplacer(" — ", ", ", "—", ", ", " – ", ", ").Replace(s)
}

// KeywordPriority keeps bullets that mention the posting's keywords: each
// hit counts more than the default ordering.
func KeywordPriority(keywords []string) Priority {
	return func(si, ei, bi int, s Section, b Bullet) float64 {
		hits := len(KeywordCoverage(b.Text, keywords).Matched)
		return DefaultPriority(si, ei, bi, s, b) + float64(hits)*400
	}
}

// --- bullet suggestions -------------------------------------------------------

const bulletInstructions = `You rewrite one resume bullet into three stronger versions. ` + scoring.Untrusted + `

Return ONLY a JSON object: {"suggestions": ["", "", ""]}

Rules:
- Start with a strong past-tense verb (present tense if the role is current). No "I", "my" or "we".
- Lead with the action and its result; keep the candidate's facts, tools and numbers exactly.
- Never invent facts. Where a number would make it stronger but none is given, write a placeholder in square brackets, like "by [X]%" or "for [N] users", for the candidate to fill in.
- Each under 200 characters, one line, no trailing period, no em or en dashes.
- Make the three meaningfully different (impact-first, scope-first, skill-first).`

// BulletMessages asks for rewrites of one bullet, given its role and,
// optionally, the job it is being tailored to.
func BulletMessages(text, role, job string) []scoring.Message {
	user := "Role: " + role + "\nBullet: " + text
	if job != "" {
		user += "\nTarget job (use its terms only where they truthfully apply): " + job
	}
	return []scoring.Message{{Role: "system", Content: bulletInstructions}, {Role: "user", Content: user}}
}

// ParseBulletSuggestions keeps the suggestions that add no number outside
// a [placeholder], cleaned of dashes and trailing periods.
func ParseBulletSuggestions(original, reply string) ([]string, error) {
	var out struct {
		Suggestions []string `json:"suggestions"`
	}
	if err := scoring.DecodeJSON(reply, &out); err != nil {
		return nil, fmt.Errorf("unreadable suggestions: %w", err)
	}
	orig := numbers(original)
	kept := []string{}
	for _, s := range out.Suggestions {
		s = strings.TrimSuffix(strings.TrimSpace(cleanDashes(s)), ".")
		if s == "" || len(s) > maxBullet {
			continue
		}
		ok := true
		for n := range numbers(placeholderRE.ReplaceAllString(s, "")) {
			ok = ok && orig[n]
		}
		if ok && len(kept) < 3 {
			kept = append(kept, s)
		}
	}
	return kept, nil
}

var placeholderRE = regexp.MustCompile(`\[[^\]]{1,20}\]`)

// cutTo shortens s to n runes.
func cutTo(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
