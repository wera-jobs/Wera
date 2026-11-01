package notify

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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

func TestDigestWindow(t *testing.T) {
	zone, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 6, day, hour, minute, 0, 0, zone) }
	ptr := func(v time.Time) *time.Time { return &v }
	cases := []struct {
		name      string
		frequency string
		last      *time.Time
		now       time.Time
		due       bool
		since     time.Time
	}{
		{"first daily digest looks back a day", "daily", nil, at(10, 9, 30), true, at(9, 9, 30)},
		{"first weekly digest looks back a week", "weekly", nil, at(10, 9, 30), true, at(3, 9, 30)},
		{"one a day however many runs", "daily", ptr(at(10, 9, 40)), at(10, 19, 30), false, at(10, 9, 40)},
		{"tomorrow's run, even a little earlier", "daily", ptr(at(10, 9, 40)), at(11, 9, 35), true, at(10, 9, 40)},
		{"a late first digest moves to the morning", "daily", ptr(at(10, 19, 40)), at(11, 9, 35), true, at(10, 19, 40)},
		{"a long gap still covers one day", "daily", ptr(at(1, 9, 40)), at(11, 9, 35), true, at(10, 9, 35)},
		{"weekly waits a week", "weekly", ptr(at(10, 9, 40)), at(16, 19, 30), false, at(10, 9, 40)},
		{"weekly on the seventh day", "weekly", ptr(at(10, 9, 40)), at(17, 9, 35), true, at(10, 9, 40)},
		{"the day is the zone's, not UTC's", "daily", ptr(time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)), at(10, 23, 0), false, time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		since, due := digestWindow(c.frequency, c.last, c.now)
		if due != c.due || !since.Equal(c.since) {
			t.Errorf("%s: due %v since %v, want %v %v", c.name, due, since, c.due, c.since)
		}
	}
}

// scored records an analysis for the user's job, made at a given time.
func scored(t *testing.T, pool *pgxpool.Pool, userID, jobID int64, hash string, fit int, at time.Time) {
	t.Helper()
	ctx := context.Background()
	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO analyses (job_id, model, profile_hash, fit_score, raw, created_at, user_id)
		VALUES ($1, 'test', $2, $3, '{}', $4, $5) RETURNING id`, jobID, hash, fit, at, userID).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO user_jobs (user_id, job_id, content_hash, stage, analysis_id) VALUES ($1, $2, 'hash', 'scored', $3)
		ON CONFLICT (user_id, job_id) DO UPDATE SET analysis_id = EXCLUDED.analysis_id`, userID, jobID, id)
	if err != nil {
		t.Fatal(err)
	}
}

func TestQueueDigests(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	s := &Service{Pool: pool, Mailer: &flaky{}, Log: slog.Default(), Config: mail.Config{Provider: "log", From: "Wera <hello@wera.test>", PublicURL: "https://wera.test"}}
	company, _ := testutil.Company(t, pool, "technology")
	account := func(confirmed bool, settings string) *store.User {
		t.Helper()
		u := testutil.User(t, pool, false)
		t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM email_outbox WHERE user_id = $1`, u.ID) })
		if confirmed {
			if ok, err := store.MarkEmailVerified(ctx, pool, u.ID, u.Email); err != nil || !ok {
				t.Fatalf("confirm: %v %v", ok, err)
			}
		}
		if _, err := pool.Exec(ctx, `UPDATE users SET settings = $2 WHERE id = $1`, u.ID, settings); err != nil {
			t.Fatal(err)
		}
		return u
	}
	digests := func(u *store.User) []string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT text FROM email_outbox WHERE user_id = $1 AND tag = 'digest' ORDER BY id`, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		texts, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return texts
	}
	now := time.Now()
	queue := func(at time.Time) {
		t.Helper()
		if _, err := s.QueueDigests(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	jobs := testutil.Jobs(t, pool, company, testutil.Job{Title: "Go Engineer"}, testutil.Job{Title: "Weak Fit"}, testutil.Job{Title: "Next Day Job"},
		testutil.Job{Title: "Rescored Job"}, testutil.Job{Title: "Unconfirmed Job"}, testutil.Job{Title: "Weekly Job"})

	reader := account(true, `{}`)
	scored(t, pool, reader.ID, jobs[0], testutil.Unique("hash"), 90, now.Add(-time.Hour))
	scored(t, pool, reader.ID, jobs[1], testutil.Unique("hash"), 40, now.Add(-time.Hour))
	unconfirmed := account(false, `{}`)
	scored(t, pool, unconfirmed.ID, jobs[4], testutil.Unique("hash"), 90, now.Add(-time.Hour))
	off := account(true, `{"notifications":{"email_digest":false,"frequency":"daily","min_score":80}}`)
	scored(t, pool, off.ID, jobs[0], testutil.Unique("hash"), 90, now.Add(-time.Hour))
	rescored := account(true, `{}`)
	scored(t, pool, rescored.ID, jobs[3], testutil.Unique("hash"), 70, now.AddDate(0, 0, -30))
	scored(t, pool, rescored.ID, jobs[3], testutil.Unique("hash"), 90, now.Add(-time.Hour))
	weekly := account(true, `{"notifications":{"email_digest":true,"frequency":"weekly","min_score":80}}`)
	scored(t, pool, weekly.ID, jobs[5], testutil.Unique("hash"), 90, now.AddDate(0, 0, -3))
	quiet := account(true, `{}`)

	queue(now)
	first := digests(reader)
	if len(first) != 1 {
		t.Fatalf("reader got %d digests, want 1", len(first))
	}
	if !strings.Contains(first[0], "Go Engineer") || strings.Contains(first[0], "Weak Fit") || !strings.Contains(first[0], "scored 2 new jobs") {
		t.Errorf("digest: %s", first[0])
	}
	for name, u := range map[string]*store.User{"unconfirmed": unconfirmed, "off": off, "rescored": rescored, "quiet": quiet} {
		if got := digests(u); len(got) != 0 {
			t.Errorf("%s got a digest: %s", name, got[0])
		}
	}
	if got := digests(weekly); len(got) != 1 || !strings.Contains(got[0], "this week") {
		t.Errorf("weekly digest looks back a week: %q", got)
	}

	// Later runs the same day add nothing; the next day's digest has only
	// what was scored since, and a weekly reader still waits.
	queue(now)
	if n := len(digests(reader)); n != 1 {
		t.Errorf("same day: %d digests", n)
	}
	scored(t, pool, reader.ID, jobs[2], testutil.Unique("hash"), 85, time.Now().Add(time.Minute))
	queue(now.AddDate(0, 0, 1))
	next := digests(reader)
	if len(next) != 2 || !strings.Contains(next[1], "Next Day Job") || strings.Contains(next[1], "Go Engineer") {
		t.Errorf("next day: %q", next)
	}
	if n := len(digests(weekly)); n != 1 {
		t.Errorf("weekly next day: %d digests", n)
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
