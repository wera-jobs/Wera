// Package inbound reads the emails a user forwards to their Wera address
// (recruiter replies) and turns them into tracker suggestions: which
// tracked job a reply is about and what it says (an interview, a
// rejection, an offer). Nothing here stores or logs an email: the caller
// keeps only the match and the suggestion.
package inbound

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// Email is what the matching needs of one message.
type Email struct {
	FromName, FromAddr string
	To                 []string
	Subject            string
	Date               time.Time
	Text               string // the plain text body (HTML turned to text when there is no plain part)
}

// FromDomain is the sender's domain, lower case.
func (e *Email) FromDomain() string {
	if i := strings.LastIndexByte(e.FromAddr, '@'); i >= 0 {
		return strings.ToLower(e.FromAddr[i+1:])
	}
	return ""
}

// maxText is how much body text is read: replies put what matters first.
const maxText = 20000

var decoder = &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
	return input, nil // UTF-8, ASCII and Latin-1 read well enough for matching
}}

// Parse reads a raw RFC 5322 message. When the user forwarded it by hand,
// the original sender, subject and date come from the forwarded header
// block in the body.
func Parse(raw []byte) (*Email, error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("read message: %w", err)
	}
	e := &Email{}
	if subj, err := decoder.DecodeHeader(m.Header.Get("Subject")); err == nil {
		e.Subject = strings.TrimSpace(subj)
	} else {
		e.Subject = strings.TrimSpace(m.Header.Get("Subject"))
	}
	if from, err := (&mail.AddressParser{WordDecoder: decoder}).Parse(m.Header.Get("From")); err == nil {
		e.FromName, e.FromAddr = from.Name, strings.ToLower(from.Address)
	}
	if to, err := (&mail.AddressParser{WordDecoder: decoder}).ParseList(m.Header.Get("To")); err == nil {
		for _, a := range to {
			e.To = append(e.To, strings.ToLower(a.Address))
		}
	}
	if d, err := m.Header.Date(); err == nil {
		e.Date = d
	}
	text, err := bodyText(m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), m.Body, 0)
	if err != nil {
		return nil, err
	}
	e.Text = text
	if len(e.Text) > maxText {
		e.Text = e.Text[:maxText]
	}
	e.unforward()
	return e, nil
}

// bodyText returns the best text of a part: text/plain first, else HTML
// as text. Attachments are skipped.
func bodyText(contentType, encoding string, r io.Reader, depth int) (string, error) {
	if depth > 5 {
		return "", nil
	}
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || contentType == "" {
		mt = "text/plain"
	}
	if strings.HasPrefix(mt, "multipart/") {
		mr := multipart.NewReader(r, params["boundary"])
		var plain, htm string
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				break // keep what was read
			}
			if strings.HasPrefix(strings.ToLower(p.Header.Get("Content-Disposition")), "attachment") {
				continue
			}
			pt := p.Header.Get("Content-Type")
			t, err := bodyText(pt, p.Header.Get("Content-Transfer-Encoding"), p, depth+1)
			if err != nil || strings.TrimSpace(t) == "" {
				continue
			}
			if strings.HasPrefix(strings.ToLower(pt), "text/html") {
				if htm == "" {
					htm = t
				}
			} else if plain == "" {
				plain = t
			}
		}
		if plain != "" {
			return plain, nil
		}
		return htm, nil
	}
	if !strings.HasPrefix(mt, "text/") {
		return "", nil
	}
	var body io.Reader = io.LimitReader(r, 1<<20)
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, &stripSpace{r: body})
	}
	b, err := io.ReadAll(body)
	if err != nil && len(b) == 0 {
		return "", nil
	}
	if mt == "text/html" {
		return htmlText(string(b)), nil
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n"), nil
}

// stripSpace drops the line breaks inside base64 bodies.
type stripSpace struct{ r io.Reader }

func (s *stripSpace) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	j := 0
	for _, c := range p[:n] {
		if c != '\r' && c != '\n' && c != ' ' && c != '\t' {
			p[j] = c
			j++
		}
	}
	return j, err
}

var (
	dropBlocks = regexp.MustCompile(`(?is)<(script|style|head)\b.*?</(script|style|head)>`)
	breaks     = regexp.MustCompile(`(?i)<(br|/p|/div|/tr|/li|/h[1-6])\b[^>]*>`)
	tags       = regexp.MustCompile(`<[^>]*>`)
	blankLines = regexp.MustCompile(`\n[ \t]*(\n[ \t]*)+`)
	spaces     = regexp.MustCompile(`[ \t\x{00a0}]+`)
)

// htmlText is an HTML body as plain text.
func htmlText(s string) string {
	s = dropBlocks.ReplaceAllString(s, " ")
	s = breaks.ReplaceAllString(s, "\n")
	s = tags.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = spaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(blankLines.ReplaceAllString(s, "\n\n"))
}

// forwardedFrom finds the header block a mail client writes when a
// message is forwarded by hand ("---------- Forwarded message ---------",
// "-----Original Message-----", "Begin forwarded message:").
var (
	forwardMark   = regexp.MustCompile(`(?im)^[ >]*(-+ ?forwarded message ?-+|-+ ?original message ?-+|begin forwarded message:)\s*$`)
	forwardHeader = regexp.MustCompile(`(?im)^[ >*]*(from|subject|date|sent)\*?:\s*(.+)$`)
)

// unforward replaces the forwarder's own From, Subject and Date with the
// original's when the body starts with a forwarded message.
func (e *Email) unforward() {
	loc := forwardMark.FindStringIndex(e.Text)
	if loc == nil {
		return
	}
	block := e.Text[loc[1]:]
	if len(block) > 1500 {
		block = block[:1500]
	}
	var from, subject string
	for _, m := range forwardHeader.FindAllStringSubmatch(block, 6) {
		switch strings.ToLower(m[1]) {
		case "from":
			if from == "" {
				from = strings.TrimSpace(m[2])
			}
		case "subject":
			if subject == "" {
				subject = strings.TrimSpace(m[2])
			}
		}
	}
	if from == "" {
		return
	}
	if a, err := mail.ParseAddress(strings.ReplaceAll(from, "mailto:", "")); err == nil {
		e.FromName, e.FromAddr = a.Name, strings.ToLower(a.Address)
	} else if m := bareAddr.FindString(from); m != "" {
		e.FromAddr = strings.ToLower(m)
		e.FromName = strings.TrimSpace(strings.Trim(strings.Replace(from, m, "", 1), " <>[]\"'"))
	}
	if subject != "" {
		e.Subject = subject
	}
	e.Text = e.Text[loc[1]:]
}

var bareAddr = regexp.MustCompile(`[\w.+-]+@[\w-]+(\.[\w-]+)+`)
