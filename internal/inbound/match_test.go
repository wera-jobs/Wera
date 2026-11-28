package inbound

import (
	"fmt"
	"mime/quotedprintable"
	"strings"
	"testing"
)

// raw builds a message as a mail server hands it over; HTML bodies go as
// quoted-printable HTML in a multipart/alternative with an empty plain part.
func raw(from, subject, body string) []byte {
	if strings.HasPrefix(body, "<") {
		var qp strings.Builder
		w := quotedprintable.NewWriter(&qp)
		w.Write([]byte(body))
		w.Close()
		return []byte(fmt.Sprintf("From: %s\r\nTo: u-abc@in.example.com\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=\"b1\"\r\n\r\n--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n\r\n--b1\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n--b1--\r\n", from, subject, qp.String()))
	}
	return []byte(fmt.Sprintf("From: %s\r\nTo: u-abc@in.example.com\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s", from, subject, strings.ReplaceAll(body, "\n", "\r\n")))
}

// TestCorpus is the 50-email test: at least 90% of the replies about a
// tracked job are matched to the right job and read as the right kind,
// and no other mail is matched with a kind (it would suggest a change).
// Whether a kind moves the job is SuggestedStatus's call, checked below.
func TestCorpus(t *testing.T) {
	right, about, falseHits := 0, 0, 0
	for i, c := range corpus {
		e, err := Parse(raw(c.from, c.subject, c.body))
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		kind, gotJob := Classify(e), int64(0)
		if a := Match(e, corpusApps); a != nil {
			gotJob = a.JobID
		}
		switch {
		case c.job != 0 && c.kind != "":
			about++
			if gotJob == c.job && kind == c.kind {
				right++
				continue
			}
		case gotJob == 0 || kind == "":
			continue
		default:
			falseHits++
		}
		t.Logf("miss %d %q: want job %d %q, got job %d %q", i, c.subject, c.job, c.kind, gotJob, kind)
	}
	t.Logf("%d emails: %d of %d replies right (%.0f%%), %d other emails matched with a kind", len(corpus), right, about, 100*float64(right)/float64(about), falseHits)
	if len(corpus) != 50 {
		t.Errorf("the test is 50 emails, not %d", len(corpus))
	}
	if float64(right) < 0.9*float64(about) || falseHits > 0 {
		t.Errorf("below the bar: %d of %d right, %d false", right, about, falseHits)
	}
	// Suggest leaves a job alone when a reply does not move it on: an
	// invitation for a job already interviewing, a receipt for one applied.
	for _, i := range []int{17, 31} {
		c := corpus[i]
		e, _ := Parse(raw(c.from, c.subject, c.body))
		if s := Suggest(e, corpusApps); s != nil {
			t.Errorf("%q suggested %+v", c.subject, s)
		}
	}
	c := corpus[0]
	e, _ := Parse(raw(c.from, c.subject, c.body))
	if s := Suggest(e, corpusApps); s == nil || s.App.JobID != 1 || s.Status != "rejected" {
		t.Errorf("rejection suggested %+v", s)
	}
}

func TestParse(t *testing.T) {
	m := "From: =?UTF-8?Q?Jos=C3=A9_at_Acme?= <Jobs@Acme.com>\r\nTo: u-abc@in.example.com\r\nSubject: =?UTF-8?B?SW50ZXJ2aWV3IOKAkyBBY21l?=\r\nDate: Mon, 30 Nov 2026 09:00:00 -0600\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\nSGkgQWxleCwgbGV0J3MgdGFsay4=\r\n--x\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\nJVBERi0=\r\n--x--\r\n"
	e, err := Parse([]byte(m))
	if err != nil {
		t.Fatal(err)
	}
	if e.FromName != "José at Acme" || e.FromAddr != "jobs@acme.com" || e.FromDomain() != "acme.com" || e.Subject != "Interview – Acme" ||
		e.Text != "Hi Alex, let's talk." || e.Date.IsZero() || len(e.To) != 1 {
		t.Errorf("%+v", e)
	}
}

func TestSuggestedStatus(t *testing.T) {
	for _, c := range [][3]string{
		{KindRejection, "interviewing", "rejected"}, {KindRejection, "rejected", ""}, {KindInterview, "applied", "interviewing"},
		{KindInterview, "interviewing", ""}, {KindReceived, "interviewing", ""}, {KindReceived, "saved", "applied"},
		{KindOffer, "interviewing", "offer"}, {KindOffer, "not_interested", ""}, {"", "applied", ""},
	} {
		if got := SuggestedStatus(c[0], c[1]); got != c[2] {
			t.Errorf("%s from %s: got %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
