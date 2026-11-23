package resume

import (
	"embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

//go:embed latex/*.tex
var latexFS embed.FS

// --- export ---------------------------------------------------------------

var texEscaper = strings.NewReplacer(
	`\`, `\textbackslash{}`, `{`, `\{`, `}`, `\}`, `$`, `\$`, `&`, `\&`, `#`, `\#`,
	`^`, `\textasciicircum{}`, `_`, `\_`, `%`, `\%`, `~`, `\textasciitilde{}`,
)

// esc makes user text safe inside LaTeX: every special character is
// escaped, so a resume can never run LaTeX commands.
func esc(s string) string { return texEscaper.Replace(s) }

// escRich is esc that also turns **bold** into \textbf{bold}.
func escRich(s string) string {
	parts := strings.Split(s, "**")
	if len(parts) < 3 {
		return esc(s)
	}
	var b strings.Builder
	for i, part := range parts {
		switch {
		case i%2 == 1 && i < len(parts)-1:
			b.WriteString(`\textbf{` + esc(part) + `}`)
		case i%2 == 1:
			b.WriteString(esc("**" + part))
		default:
			b.WriteString(esc(part))
		}
	}
	return b.String()
}

// texURL makes a URL safe inside \href's first argument: no character can
// close the argument or start a command.
func texURL(url string) string {
	return strings.NewReplacer(`\`, "", `{`, "%7B", `}`, "%7D", `%`, `\%`, `#`, `\#`).Replace(url)
}

// untexURL reads a URL from \href back: texURL's escapes, and the ones a
// user editing the LaTeX may add, are not part of the address.
func untexURL(url string) string {
	return strings.NewReplacer(`\%`, "%", `\#`, "#", `\&`, "&", `\_`, "_").Replace(url)
}

func href(url, text string) string {
	return `\href{` + texURL(url) + `}{` + esc(text) + `}`
}

// texPreamble is the template's preamble for a layout: the shared head
// (paper, margins, encodings, accent) and the template's own style.
func texPreamble(l Layout) string {
	l = l.Clamp()
	head, _ := latexFS.ReadFile("latex/head.tex")
	style, _ := latexFS.ReadFile("latex/" + l.Template + ".tex")
	t := template.Must(template.New("head").Delims("<<", ">>").Parse(string(head)))
	paper := "letterpaper"
	if l.Paper == "a4" {
		paper = "a4paper"
	}
	size := "11pt"
	if l.FontSize < 10.75 {
		size = "10pt"
	}
	var b strings.Builder
	_ = t.Execute(&b, map[string]string{
		"Paper": paper, "Size": size, "Name": TemplateByID(l.Template).Name,
		"Margin": fmt.Sprintf("%.2f", l.Margin), "Top": fmt.Sprintf("%.2f", l.Margin*0.8),
		"Accent": strings.ToUpper(strings.TrimPrefix(l.Accent, "#")),
		"Gap":    fmt.Sprintf("%.1f", 4.6*l.Spacing),
	})
	b.WriteString("\n")
	b.Write(style)
	return b.String()
}

// ExportTeX writes the resume as a LaTeX file in the layout's template,
// for editing in Overleaf or a local TeX setup. Every template uses the
// same content macros, so ImportTeX reads any of them back.
func (r *Resume) ExportTeX(l Layout) string {
	var b strings.Builder
	b.WriteString(texPreamble(l))
	b.WriteString("\n\\begin{document}\n\n")
	b.WriteString(r.TeXBody())
	b.WriteString("\n\\end{document}\n")
	return b.String()
}

// TeXBody is the content between \begin{document} and \end{document}.
func (r *Resume) TeXBody() string {
	var b strings.Builder
	var parts []string
	if r.Phone != "" {
		parts = append(parts, esc(r.Phone))
	}
	if r.Email != "" {
		parts = append(parts, href("mailto:"+r.Email, r.Email))
	}
	if r.Location != "" {
		parts = append(parts, esc(r.Location))
	}
	for _, l := range r.Links {
		switch {
		case l.URL != "":
			parts = append(parts, href(l.URL, l.Label))
		case l.Label != "":
			parts = append(parts, esc(l.Label))
		}
	}
	fmt.Fprintf(&b, "\\resumeHeader{%s}{%s}\n  {%s}\n", esc(r.Name), esc(r.Headline), strings.Join(parts, " \\contactSep\n   "))

	for _, s := range r.Sections {
		if s.Hidden {
			continue
		}
		var entries []Entry
		for _, e := range s.Entries {
			if !e.Hidden {
				entries = append(entries, e)
			}
		}
		empty := (s.Kind == KindSkills && len(s.Skills) == 0) || (s.Kind == KindSummary && s.Text == "") ||
			(s.Kind != KindSkills && s.Kind != KindSummary && len(entries) == 0)
		if empty {
			continue // an empty list would not compile
		}
		fmt.Fprintf(&b, "\n\\section{%s}\n", esc(s.Title))
		switch s.Kind {
		case KindSkills:
			b.WriteString("\\resumeSkillsStart\n")
			for _, g := range s.Skills {
				fmt.Fprintf(&b, "  \\resumeSkill{%s}{%s}\n", esc(g.Name), esc(g.Items))
			}
			b.WriteString("\\resumeSkillsEnd\n")
		case KindSummary:
			b.WriteString("\\resumeSummary{" + escRich(s.Text) + "}\n")
		default:
			b.WriteString("\\resumeSubHeadingListStart\n")
			for _, e := range entries {
				switch s.Kind {
				case KindProjects:
					desc := escRich(e.Text)
					if e.URL != "" {
						label := e.LinkLabel
						if label == "" {
							label = "link"
						}
						desc += ` \href{` + texURL(e.URL) + `}{\underline{` + esc(label) + `}}`
					}
					fmt.Fprintf(&b, "  \\resumeProjectEntry{%s}{%s}{%s}\n    {%s}\n", esc(e.Heading), esc(e.Tech), esc(e.Dates), strings.TrimSpace(desc))
				case KindCompact:
					fmt.Fprintf(&b, "  \\resumeCompact{%s}{%s}{%s}\n", esc(e.Heading), esc(e.Subheading), esc(e.Dates))
				default:
					heading := esc(e.Heading)
					if e.URL != "" {
						heading = `\href{` + texURL(e.URL) + `}{` + heading + `}`
					}
					fmt.Fprintf(&b, "  \\resumeSubheading{%s}{%s}\n    {%s}{%s}\n", heading, esc(e.Dates), esc(e.Subheading), esc(e.Location))
				}
				var items []string
				for _, bl := range e.Bullets {
					if !bl.Hidden {
						items = append(items, "      \\resumeItem{"+escRich(bl.Text)+"}")
					}
				}
				if len(items) > 0 {
					b.WriteString("    \\resumeItemListStart\n" + strings.Join(items, "\n") + "\n    \\resumeItemListEnd\n")
				}
			}
			b.WriteString("\\resumeSubHeadingListEnd\n")
		}
	}
	return b.String()
}

// --- import ---------------------------------------------------------------

// readArg reads one {...} argument starting at s[i] (after optional
// whitespace), honoring nested and escaped braces. It returns the inside
// and the index after the closing brace, or ok=false.
func readArg(s string, i int) (string, int, bool) {
	for i < len(s) && (s[i] == ' ' || s[i] == '\n' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	if i >= len(s) || s[i] != '{' {
		return "", i, false
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++ // skip the escaped character
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[i+1 : j], j + 1, true
			}
		}
	}
	return "", i, false
}

// call is one macro use: its arguments and where it starts in the body.
type call struct {
	args []string
	at   int
}

// macroCalls finds \name{..}{..}... calls with n arguments, in order.
func macroCalls(body, name string, n int) []call {
	var out []call
	needle := `\` + name
	for i := 0; ; {
		k := strings.Index(body[i:], needle)
		if k < 0 {
			break
		}
		at := i + k + len(needle)
		// \resumeItem must not match \resumeItemListStart.
		if at < len(body) && (body[at] >= 'a' && body[at] <= 'z' || body[at] >= 'A' && body[at] <= 'Z') {
			i = at
			continue
		}
		args := make([]string, 0, n)
		pos := at
		ok := true
		for range n {
			var v string
			v, pos, ok = readArg(body, pos)
			if !ok {
				break
			}
			args = append(args, v)
		}
		if ok {
			out = append(out, call{args: args, at: i + k})
		}
		i = max(pos, at)
	}
	return out
}

var (
	hrefRE     = regexp.MustCompile(`\\href\{([^}]*)\}\{`)
	cmdArgRE   = regexp.MustCompile(`\\(?:textbf|textit|emph|underline|small|large|Large|Huge|huge|scshape|textsc|texttt|mbox)\b\s*`)
	cmdRE      = regexp.MustCompile(`\\[a-zA-Z]+\*?(\[[^\]]*\])?`)
	vspaceRE   = regexp.MustCompile(`\\vspace\{[^}]*\}|\\hspace\{[^}]*\}`)
	spacesRE   = regexp.MustCompile(`[ \t]+`)
	commentRE  = regexp.MustCompile(`(?m)(^|[^\\])%.*$`)
	sectionRE  = regexp.MustCompile(`\\section\*?\{`)
	skillLine  = regexp.MustCompile(`\\textbf\{([^}]*)\}\s*([^\\]*(?:\\[&%$#_][^\\]*)*)`)
	unescapeRE = strings.NewReplacer(`\textendash{}`, "–", `\textemdash{}`, "—", `\textendash`, "–", `\textemdash`, "—",
		`\textasciitilde{}`, "~", `\textasciicircum{}`, "^", `\&`, "&", `\%`, "%", `\$`, "$", `\_`, "_", `\#`, "#", "---", "—", "--", "–", "~", " ", `$|$`, "|", `\textbar`, "|", `\\`, " ", `\ `, " ")
)

// plain turns a LaTeX fragment into text: links become their text (the
// first URL is returned too), formatting commands vanish, escapes resolve.
func plain(s string) (text, firstURL string) {
	// Escaped backslashes and braces are the user's text, not structure:
	// hold them aside so command and brace stripping cannot touch them.
	s = protectRE.Replace(s)
	defer func() { text = restoreRE.Replace(text) }()
	s = vspaceRE.ReplaceAllString(s, "")
	if m := hrefRE.FindStringSubmatch(s); m != nil {
		firstURL = untexURL(m[1])
	}
	s = hrefRE.ReplaceAllString(s, "{")
	s = cmdArgRE.ReplaceAllString(s, "")
	s = unescapeRE.Replace(s)
	s = cmdRE.ReplaceAllString(s, "")
	s = strings.NewReplacer("{", "", "}", "").Replace(s)
	s = spacesRE.ReplaceAllString(strings.ReplaceAll(s, "\n", " "), " ")
	return strings.TrimSpace(s), firstURL
}

var (
	protectRE = strings.NewReplacer(`\textbackslash{}`, "\uE000", `\{`, "\uE001", `\}`, "\uE002")
	restoreRE = strings.NewReplacer("\uE000", `\`, "\uE001", "{", "\uE002", "}")
)

func p(s string) string {
	t, _ := plain(s)
	return t
}

var boldRE = regexp.MustCompile(`\\textbf\{([^{}]*)\}`)

// pr is p that keeps bold as **bold** (bullets, descriptions, summaries).
func pr(s string) string {
	return p(boldRE.ReplaceAllString(s, "**$1**"))
}

var digitsRE = regexp.MustCompile(`\d`)

// ImportTeX reads a resume written in the "Jake's resume" family of LaTeX
// templates (\resumeSubheading, \resumeItem, \resumeProject,
// \resumeCompact and a \textbf{Group:} skills block). Anything it cannot
// place is reported in warnings rather than guessed.
func ImportTeX(src string) (*Resume, []string, error) {
	var warnings []string
	src = commentRE.ReplaceAllString(src, "$1")
	if i := strings.Index(src, `\begin{document}`); i >= 0 {
		src = src[i+len(`\begin{document}`):]
	}
	if i := strings.Index(src, `\end{document}`); i >= 0 {
		src = src[:i]
	}
	r := &Resume{}

	// Header: everything before the first \section.
	head := src
	if loc := sectionRE.FindStringIndex(src); loc != nil {
		head = src[:loc[0]]
	}
	var contactBlock string
	if hc := macroCalls(head, "resumeHeader", 3); len(hc) > 0 {
		// Wera's own templates: \resumeHeader{Name}{Headline}{contacts}.
		r.Name, r.Headline = p(hc[0].args[0]), p(hc[0].args[1])
		contactBlock = strings.ReplaceAll(hc[0].args[2], `\contactSep`, `$|$`)
	} else {
		if m := regexp.MustCompile(`\\scshape\s+([^}]*)\}`).FindStringSubmatch(head); m != nil {
			r.Name = p(m[1])
		}
		contactBlock = head
		if i := strings.Index(head, `\small`); i >= 0 {
			contactBlock = head[i:]
		}
		if i := strings.Index(contactBlock, `\end{center}`); i >= 0 {
			contactBlock = contactBlock[:i]
		}
	}
	for _, part := range strings.Split(contactBlock, `$|$`) {
		text, url := plain(part)
		switch {
		case url != "" && strings.HasPrefix(url, "mailto:"):
			r.Email = strings.TrimPrefix(url, "mailto:")
		case url != "":
			r.Links = append(r.Links, Link{Label: text, URL: url})
		case strings.Contains(text, "@"):
			r.Email = text
		case len(digitsRE.FindAllString(text, -1)) >= 7:
			r.Phone = text
		case text != "":
			r.Location = text
		}
	}
	if r.Name == "" {
		return nil, nil, errors.New("could not find the name (expected \\resumeHeader{Your Name}{...}{...} or {\\Huge \\scshape Your Name})")
	}

	// Sections.
	locs := sectionRE.FindAllStringIndex(src, -1)
	for k, loc := range locs {
		title, after, ok := readArg(src, loc[1]-1)
		if !ok {
			continue
		}
		end := len(src)
		if k+1 < len(locs) {
			end = locs[k+1][0]
		}
		body := src[after:end]
		sec := Section{ID: newID(), Title: p(title)}
		subs := macroCalls(body, "resumeSubheading", 4)
		projs := macroCalls(body, "resumeProject", 3)
		projs4 := macroCalls(body, "resumeProjectEntry", 4)
		comps := macroCalls(body, "resumeCompact", 3)
		skills := macroCalls(body, "resumeSkill", 2)
		summary := macroCalls(body, "resumeSummary", 1)
		switch {
		case len(subs) > 0:
			sec.Kind = KindEntries
			for i, c := range subs {
				a := c.args
				heading, url := plain(a[0])
				e := Entry{ID: newID(), Heading: heading, URL: url, Dates: p(a[1]), Subheading: p(a[2]), Location: p(a[3])}
				e.Bullets = bulletsBetween(body, c.at, nextStart(subs, i, len(body)))
				sec.Entries = append(sec.Entries, e)
			}
		case len(projs4) > 0:
			sec.Kind = KindProjects
			for i, c := range projs4 {
				a := c.args
				desc, label, url := splitLink(a[3])
				e := Entry{ID: newID(), Heading: p(a[0]), Tech: p(a[1]), Dates: p(a[2]), Text: desc, URL: url, LinkLabel: label}
				e.Bullets = bulletsBetween(body, c.at, nextStart(projs4, i, len(body)))
				sec.Entries = append(sec.Entries, e)
			}
		case len(skills) > 0:
			sec.Kind = KindSkills
			for _, c := range skills {
				if name, items := p(c.args[0]), p(c.args[1]); name != "" || items != "" {
					sec.Skills = append(sec.Skills, SkillGroup{ID: newID(), Name: name, Items: items})
				}
			}
		case len(summary) > 0:
			sec.Kind = KindSummary
			sec.Text = pr(summary[0].args[0])
		case len(projs) > 0:
			sec.Kind = KindProjects
			for i, c := range projs {
				a := c.args
				desc, label, url := splitLink(a[2])
				e := Entry{ID: newID(), Heading: p(a[0]), Tech: p(a[1]), Text: desc, URL: url, LinkLabel: label}
				e.Bullets = bulletsBetween(body, c.at, nextStart(projs, i, len(body)))
				sec.Entries = append(sec.Entries, e)
			}
		case len(comps) > 0:
			sec.Kind = KindCompact
			for _, c := range comps {
				a := c.args
				sec.Entries = append(sec.Entries, Entry{ID: newID(), Heading: p(a[0]), Subheading: p(a[1]), Dates: p(a[2])})
			}
		case strings.Contains(body, `\textbf{`):
			sec.Kind = KindSkills
			for _, m := range skillLine.FindAllStringSubmatch(body, -1) {
				name := strings.TrimSuffix(strings.TrimSpace(p(m[1])), ":")
				if items := p(m[2]); name != "" && items != "" {
					sec.Skills = append(sec.Skills, SkillGroup{ID: newID(), Name: name, Items: items})
				}
			}
		default:
			sec.Kind = KindSummary
			sec.Text = p(body)
			if sec.Text == "" {
				warnings = append(warnings, fmt.Sprintf("section %q was empty or not understood", sec.Title))
				continue
			}
		}
		r.Sections = append(r.Sections, sec)
	}
	if len(r.Sections) == 0 {
		warnings = append(warnings, "no \\section found")
	}
	if err := r.Normalize(); err != nil {
		return nil, warnings, err
	}
	return r, warnings, nil
}

// splitLink separates a trailing \href from a description: the text
// before it, the link's label and its URL.
func splitLink(s string) (desc, label, url string) {
	loc := hrefRE.FindStringSubmatchIndex(s)
	if loc == nil {
		return pr(s), "", ""
	}
	url = untexURL(s[loc[2]:loc[3]])
	inner, _, _ := readArg(s, loc[1]-1)
	return pr(s[:loc[0]]), p(inner), url
}

func nextStart(calls []call, i, end int) int {
	if i+1 < len(calls) {
		return calls[i+1].at
	}
	return end
}

// bulletsBetween collects \resumeItem{...} texts between the macro call
// starting at offset from and the next entry.
func bulletsBetween(body string, from, end int) []Bullet {
	chunk := body[from:min(end, len(body))]
	var out []Bullet
	for _, c := range macroCalls(chunk, "resumeItem", 1) {
		if t := pr(c.args[0]); t != "" {
			out = append(out, Bullet{ID: newID(), Text: t})
		}
	}
	return out
}
