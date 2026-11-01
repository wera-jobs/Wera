package notify

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"wera/internal/mail"
	"wera/internal/store"
	"wera/internal/testutil"
)

type flaky struct {
	fail bool
	sent []mail.Message
}

func (f *flaky) Send(_ context.Context, m mail.Message) (string, error) {
	if f.fail {
		return "", errors.New("relay said no")
	}
	f.sent = append(f.sent, m)
	return "<id@wera.test>", nil
}

func TestDeliverRetriesThenGivesUp(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	u := testutil.User(t, pool, false)
	m := &flaky{fail: true}
	s := &Service{Pool: pool, Mailer: m, Log: slog.Default(), Config: mail.Config{Provider: "log", From: "Wera <hello@wera.test>", PublicURL: "https://wera.test"}}
	if err := s.SendTest(ctx, u.Email); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT max(id) FROM email_outbox WHERE to_addr = $1`, u.Email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM email_outbox WHERE id = $1`, id) })
	due := func() { pool.Exec(ctx, `UPDATE email_outbox SET next_attempt_at = now() WHERE id = $1`, id) }

	for attempt := 1; attempt <= store.MaxEmailAttempts; attempt++ {
		due()
		if _, err := s.Deliver(ctx, 50); err != nil {
			t.Fatal(err)
		}
		var status string
		var attempts int
		_ = pool.QueryRow(ctx, `SELECT status, attempts FROM email_outbox WHERE id = $1`, id).Scan(&status, &attempts)
		want := "queued"
		if attempt == store.MaxEmailAttempts {
			want = "failed"
		}
		if status != want || attempts != attempt {
			t.Fatalf("attempt %d: status %s, attempts %d", attempt, status, attempts)
		}
	}

	// A working provider: queued mail goes out once.
	m.fail = false
	pool.Exec(ctx, `UPDATE email_outbox SET status = 'queued', attempts = 0 WHERE id = $1`, id)
	due()
	if n, err := s.Deliver(ctx, 50); err != nil || n < 1 {
		t.Fatalf("deliver: %d %v", n, err)
	}
	if n, _ := s.Deliver(ctx, 50); n != 0 {
		t.Errorf("sent twice")
	}
	if len(m.sent) == 0 || m.sent[len(m.sent)-1].Subject != "Wera email is working" {
		t.Errorf("sent: %+v", m.sent)
	}
}

func TestFirstSentence(t *testing.T) {
	if got := firstSentence("Strong Go fit. Also onsite in Austin.", 180); got != "Strong Go fit." {
		t.Errorf("got %q", got)
	}
	long := "This mid-level remote role matches the candidate's three years of experience and core stack closely though it is in fintech underwriting rather than aviation"
	if got := firstSentence(long, 60); len(got) > 64 || got[len(got)-3:] != "…" {
		t.Errorf("got %q", got)
	}
}

func TestSendWelcomeOnce(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	s := &Service{Pool: pool, Mailer: &flaky{}, Log: slog.Default(), Config: mail.Config{Provider: "log", From: "Wera <hello@wera.test>", PublicURL: "https://wera.test"}}
	u := testutil.User(t, pool, false)
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM email_outbox WHERE user_id = $1`, u.ID) })
	for range 2 {
		if err := s.SendWelcome(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE user_id = $1 AND tag = 'welcome'`, u.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("welcome emails: %d, %v", n, err)
	}
}
