// Command dispatch for `wera mail`: preview templates, send a test, and
// render or deliver email by hand.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"wera/internal/config"
	"wera/internal/mail"
	"wera/internal/notify"
	"wera/internal/store"
)

const mailUsage = `usage:
  wera mail preview [--out DIR]            write every template (HTML and text) with sample data
  wera mail test --to ADDRESS              send the setup test (needs MAIL_PROVIDER)
  wera mail digest --email E [--days 1] [--out DIR]
                                           render a user's real digest to a file (sends nothing)
  wera mail digests                        queue every digest that is due (the worker does this after a run)
  wera mail send                           deliver queued email now

Email is off until MAIL_PROVIDER is set: "log" (development: messages go
to the log and MAIL_LOG_DIR) or "smtp" (SMTP_HOST, SMTP_PORT,
SMTP_USERNAME, SMTP_PASSWORD, SMTP_SECURITY), with MAIL_FROM and
PUBLIC_URL`

func runMail(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New(mailUsage)
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("mail "+sub, flag.ContinueOnError)
	out := fs.String("out", "email-preview", "folder to write into")
	to := fs.String("to", "", "test: recipient")
	email := fs.String("email", "", "digest: the user's email")
	days := fs.Int("days", 1, "digest: jobs first seen in the last N days")
	if err := fs.Parse(args); err != nil {
		return err
	}
	env, err := config.LoadEnv()
	if err != nil {
		return err
	}
	publicURL := env.Mail.PublicURL
	if publicURL == "" {
		publicURL = "https://wera.example.com"
	}

	write := func(name string, r mail.Rendered) error {
		// The operator chooses the folder on their own machine.
		if err := os.MkdirAll(*out, 0o750); err != nil { //nolint:gosec // operator-chosen output folder
			return err
		}
		base := filepath.Join(*out, name)
		if err := os.WriteFile(base+".html", []byte(r.HTML), 0o600); err != nil { //nolint:gosec // operator-chosen output folder
			return err
		}
		if err := os.WriteFile(base+".txt", []byte("Subject: "+r.Subject+"\n\n"+r.Text), 0o600); err != nil { //nolint:gosec // operator-chosen output folder
			return err
		}
		fmt.Printf("%-8s %s\n", name, r.Subject)
		return nil
	}

	if sub == "preview" {
		for _, name := range mail.Templates() {
			r, err := mail.Render(name, mail.Sample(name, publicURL, "ada@example.com", "Ada Lovelace"))
			if err != nil {
				return err
			}
			if err := write(name, r); err != nil {
				return err
			}
		}
		fmt.Printf("open %s/*.html in a browser\n", *out)
		return nil
	}

	pool, err := store.Open(ctx, env.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	switch sub {
	case "digest":
		u, err := store.UserByEmail(ctx, pool, *email)
		if err != nil {
			return fmt.Errorf("no user %q: %w", *email, err)
		}
		d, err := notify.Digest(ctx, pool, publicURL, u, time.Now().AddDate(0, 0, -*days), "daily")
		if err != nil {
			return err
		}
		if d == nil {
			fmt.Println("nothing new to send")
			return nil
		}
		r, err := mail.Render("digest", *d)
		if err != nil {
			return err
		}
		return write("digest-"+fmt.Sprint(u.ID), r)
	case "test", "send", "digests":
		svc, err := notify.New(pool, env.Mail, slog.Default())
		if err != nil {
			return err
		}
		if !svc.Enabled() {
			return mail.ErrDisabled
		}
		if sub == "digests" {
			n, err := svc.QueueDigests(ctx, time.Now().In(env.RunZone))
			fmt.Printf("queued %d\n", n)
			return err
		}
		if sub == "test" {
			if *to == "" {
				return errors.New("--to is required")
			}
			if err := svc.SendTest(ctx, *to); err != nil {
				return err
			}
		}
		n, err := svc.Deliver(ctx, 50)
		fmt.Printf("sent %d\n", n)
		return err
	}
	return errors.New(mailUsage)
}
