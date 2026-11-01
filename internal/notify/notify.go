// Package notify sends Wera's account and job emails: it renders a
// template, queues it in the outbox, and a delivery loop hands queued
// mail to the configured provider with retries.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"wera/internal/mail"
	"wera/internal/normalize"
	"wera/internal/store"
)

// Token lifetimes.
const (
	VerifyTTL = 48 * time.Hour
	ResetTTL  = time.Hour
)

// Service sends email for the API and the worker. A nil *Service means
// email is not set up; callers check Enabled.
type Service struct {
	Pool   *pgxpool.Pool
	Mailer mail.Mailer
	Config mail.Config
	Log    *slog.Logger
}

// New returns the service, or nil when no provider is configured.
func New(pool *pgxpool.Pool, cfg mail.Config, log *slog.Logger) (*Service, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	m, err := mail.New(cfg, func(msg string, args ...any) { log.Info(msg, args...) })
	if err != nil {
		return nil, err
	}
	return &Service{Pool: pool, Mailer: m, Config: cfg, Log: log}, nil
}

// Enabled reports whether email can be sent.
func (s *Service) Enabled() bool { return s != nil }

func (s *Service) base(email, name string) mail.Base {
	return mail.NewBase(s.Config.PublicURL, email, name)
}

func (s *Service) link(path, token string) string {
	return strings.TrimRight(s.Config.PublicURL, "/") + path + "?token=" + token
}

// queue renders a template and adds it to the outbox.
func (s *Service) queue(ctx context.Context, userID *int64, to, name string, data any, headers map[string]string) error {
	r, err := mail.Render(name, data)
	if err != nil {
		return err
	}
	_, err = store.QueueEmail(ctx, s.Pool, userID, to, name, r.Subject, r.HTML, r.Text, headers)
	return err
}

// SendVerification emails a link that confirms the user's address.
func (s *Service) SendVerification(ctx context.Context, u *store.User) error {
	token, err := store.CreateEmailToken(ctx, s.Pool, u.ID, "verify", u.Email, VerifyTTL)
	if err != nil {
		return err
	}
	return s.queue(ctx, &u.ID, u.Email, "verify", mail.Verify{Base: s.base(u.Email, u.Name), Link: s.link("/verify-email", token), ExpiresIn: "48 hours"}, nil)
}

// SendReset emails a password reset link when an account has this
// address; otherwise it does nothing (callers answer the same either
// way, so the form reveals no accounts).
func (s *Service) SendReset(ctx context.Context, email string) error {
	u, err := store.UserByEmail(ctx, s.Pool, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if n, err := store.RecentEmailCount(ctx, s.Pool, u.ID, "reset", time.Hour); err != nil || n >= 3 {
		return err // quietly limited: three resets an hour
	}
	token, err := store.CreateEmailToken(ctx, s.Pool, u.ID, "reset", u.Email, ResetTTL)
	if err != nil {
		return err
	}
	return s.queue(ctx, &u.ID, u.Email, "reset", mail.Reset{Base: s.base(u.Email, u.Name), Link: s.link("/reset-password", token), ExpiresIn: "1 hour"}, nil)
}

// SendWelcome greets an account the first time it confirms an address.
func (s *Service) SendWelcome(ctx context.Context, u *store.User) error {
	if sent, err := store.EmailSent(ctx, s.Pool, u.ID, "welcome"); err != nil || sent {
		return err
	}
	companies, industries, err := store.CatalogSize(ctx, s.Pool)
	if err != nil {
		return err
	}
	d := mail.Sample("welcome", s.Config.PublicURL, u.Email, u.Name).(mail.Welcome)
	d.Companies, d.Industries = companies, industries
	return s.queue(ctx, &u.ID, u.Email, "welcome", d, nil)
}

// SendTest sends the setup test to an address.
func (s *Service) SendTest(ctx context.Context, to string) error {
	return s.queue(ctx, nil, to, "test", mail.Test{Base: s.base(to, ""), Provider: s.Config.Provider, From: s.Config.From}, nil)
}

// Digest builds a user's digest: their best scored, unapplied jobs first
// seen since `since` (at most 8), or nil when there is nothing new.
func Digest(ctx context.Context, pool *pgxpool.Pool, publicURL string, u *store.User, since time.Time, frequency string) (*mail.Digest, error) {
	jobs, _, err := store.TodayJobs(ctx, pool, u.ID, 200)
	if err != nil {
		return nil, err
	}
	d := &mail.Digest{Base: mail.NewBase(publicURL, u.Email, u.Name), Frequency: frequency, Period: "today"}
	if frequency == "weekly" {
		d.Period = "this week"
	}
	d.TodayURL = d.AppURL
	for _, j := range jobs {
		if j.FirstSeenAt.Before(since) || j.FitScore == nil {
			continue
		}
		d.NewCount++
		if len(d.Jobs) >= 8 || *j.FitScore < 60 {
			continue
		}
		dj := mail.DigestJob{Title: j.Title, Company: j.Company, Score: *j.FitScore, URL: fmt.Sprintf("%sjobs?job=%d", d.AppURL, j.ID)}
		if j.LocationSummary != nil {
			dj.Location = *j.LocationSummary
		} else {
			dj.Location = j.LocationRaw
		}
		if j.WorkMode != nil && *j.WorkMode != "" {
			dj.WorkMode = strings.ToUpper((*j.WorkMode)[:1]) + (*j.WorkMode)[1:]
		}
		if j.Sponsorship != nil && *j.Sponsorship == "yes" {
			dj.Sponsorship = "Sponsors visas"
		}
		if j.Reason != nil {
			dj.Reason = firstSentence(*j.Reason, 180)
		}
		d.Jobs = append(d.Jobs, dj)
	}
	if len(d.Jobs) == 0 {
		return nil, nil
	}
	d.Top = &d.Jobs[0]
	return d, nil
}

// QueueDigest queues a user's digest if there is anything new.
func (s *Service) QueueDigest(ctx context.Context, u *store.User, since time.Time, frequency string) (bool, error) {
	d, err := Digest(ctx, s.Pool, s.Config.PublicURL, u, since, frequency)
	if err != nil || d == nil {
		return false, err
	}
	unsub := map[string]string{"List-Unsubscribe": "<" + d.ManageURL + ">"}
	return true, s.queue(ctx, &u.ID, u.Email, "digest", *d, unsub)
}

// Deliver sends up to batch queued emails and returns how many went out.
func (s *Service) Deliver(ctx context.Context, batch int) (int, error) {
	emails, err := store.ClaimEmails(ctx, s.Pool, batch)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, e := range emails {
		id, err := s.Mailer.Send(ctx, mail.Message{To: e.To, Subject: e.Subject, HTML: e.HTML, Text: e.Text, Headers: e.Headers, Tag: e.Tag})
		if err != nil {
			s.Log.Warn("email failed", "id", e.ID, "tag", e.Tag, "attempt", e.Attempts, "err", err)
			if err := store.MarkEmailFailed(ctx, s.Pool, e.ID, e.Attempts, err.Error()); err != nil {
				return sent, err
			}
			continue
		}
		if err := store.MarkEmailSent(ctx, s.Pool, e.ID, id); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

// Run delivers queued email every interval until ctx ends.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if n, err := s.Deliver(ctx, 20); err != nil {
			s.Log.Error("email delivery failed", "err", err)
		} else if n > 0 {
			s.Log.Info("emails sent", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// firstSentence keeps an email short: the first sentence, at most n
// characters (cut at a word).
func firstSentence(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	if len(s) <= n {
		return s
	}
	s = normalize.Clip(s, n)
	if cut := strings.LastIndex(s, " "); cut >= n/2 {
		s = s[:cut]
	}
	return strings.TrimRight(s, " ,;:") + "…"
}
