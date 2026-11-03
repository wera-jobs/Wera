package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"testing"
	"time"

	"wera/internal/mail"
	"wera/internal/notify"
)

func mailServer(t *testing.T) (string, func(tag, to string) string, func(tag, to string) int) {
	t.Helper()
	ts, pool := testServerWith(t, nil, func(s *Server) {
		s.Notify = &notify.Service{Pool: s.Pool, Mailer: &mail.Log{From: "Wera <hello@wera.test>"}, Log: slog.Default(),
			Config: mail.Config{Provider: "log", From: "Wera <hello@wera.test>", PublicURL: "https://wera.test"}}
	})
	// latest returns the token in the newest email with tag sent to "to".
	latest := func(tag, to string) string {
		var text string
		err := pool.QueryRow(context.Background(), `SELECT text FROM email_outbox WHERE tag = $1 AND to_addr = $2 ORDER BY id DESC LIMIT 1`, tag, to).Scan(&text)
		if err != nil {
			t.Fatalf("no %s email to %s: %v", tag, to, err)
		}
		m := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("no link in %q", text)
		}
		return m[1]
	}
	count := func(tag, to string) int {
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM email_outbox WHERE tag = $1 AND to_addr = $2`, tag, to).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return ts.URL, latest, count
}

func TestEmailVerificationAndPasswordReset(t *testing.T) {
	base, latest, count := mailServer(t)
	jar, _ := cookiejar.New(nil)
	user := &http.Client{Jar: jar}
	email := fmt.Sprintf("mail-%d@example.com", time.Now().UnixNano())

	if code, body := getBody(t, base+"/api/auth/config"); code != 200 || !strings.Contains(body, `"mail_enabled":true`) {
		t.Fatalf("config: %d %s", code, body)
	}
	if code, _ := do(t, user, http.MethodPost, base+"/api/auth/signup", `{"email":"`+email+`","password":"first password 1","name":"Mae"}`); code != 201 {
		t.Fatalf("signup: %d", code)
	}
	// Signing up sends a confirmation link; it works once.
	token := latest("verify", email)
	if code, body := do(t, user, http.MethodGet, base+"/api/auth/me", ""); !strings.Contains(body, `"email_verified":false`) {
		t.Errorf("before: %d %s", code, body)
	}
	if code, body := do(t, &http.Client{}, http.MethodPost, base+"/api/auth/verify-email", `{"token":"`+token+`"}`); code != 200 {
		t.Fatalf("verify: %d %s", code, body)
	}
	if _, body := do(t, user, http.MethodGet, base+"/api/auth/me", ""); !strings.Contains(body, `"email_verified":true`) {
		t.Errorf("after: %s", body)
	}
	if n := count("welcome", email); n != 1 {
		t.Errorf("welcome emails after confirming: %d, want 1", n)
	}
	if code, _ := do(t, &http.Client{}, http.MethodPost, base+"/api/auth/verify-email", `{"token":"`+token+`"}`); code != 400 {
		t.Errorf("reused link: want 400, got %d", code)
	}

	// Forgot password: the same answer for unknown addresses.
	if code, _ := do(t, &http.Client{}, http.MethodPost, base+"/api/auth/password/forgot", `{"email":"nobody-at-all@example.com"}`); code != 202 {
		t.Errorf("unknown address: %d", code)
	}
	if code, _ := do(t, &http.Client{}, http.MethodPost, base+"/api/auth/password/forgot", `{"email":"`+strings.ToUpper(email)+`"}`); code != 202 {
		t.Fatalf("forgot: %d", code)
	}
	reset := latest("reset", email)
	if code, _ := do(t, &http.Client{}, http.MethodPost, base+"/api/auth/password/reset", `{"token":"`+reset+`","password":"short"}`); code != 400 {
		t.Errorf("weak password: want 400, got %d", code)
	}
	if code, body := do(t, &http.Client{}, http.MethodPost, base+"/api/auth/password/reset", `{"token":"`+reset+`","password":"second password 2"}`); code != 200 {
		t.Fatalf("reset: %d %s", code, body)
	}
	// Every session ends; the new password works, the old one does not.
	if code, _ := do(t, user, http.MethodGet, base+"/api/auth/me", ""); code != 401 {
		t.Errorf("old session after reset: want 401, got %d", code)
	}
	if code, _ := do(t, &http.Client{}, http.MethodPost, base+"/api/auth/login", `{"email":"`+email+`","password":"first password 1"}`); code != 401 {
		t.Errorf("old password: want 401, got %d", code)
	}
	if code, _ := do(t, &http.Client{}, http.MethodPost, base+"/api/auth/login", `{"email":"`+email+`","password":"second password 2"}`); code != 200 {
		t.Errorf("new password: %d", code)
	}
}

func TestEmailOffByDefault(t *testing.T) {
	ts, _ := testServer(t, nil)
	if code, body := getBody(t, ts.URL+"/api/auth/config"); code != 200 || !strings.Contains(body, `"mail_enabled":false`) {
		t.Errorf("config: %d %s", code, body)
	}
	for _, path := range []string{"/api/auth/password/forgot", "/api/auth/verify-email", "/api/auth/verify-email/send"} {
		if code, _ := do(t, client, http.MethodPost, ts.URL+path, `{"email":"a@b.co","token":"x"}`); code != 404 {
			t.Errorf("%s with email off: want 404, got %d", path, code)
		}
	}
	// Admins can still preview every template.
	for _, name := range mail.Templates() {
		code, body := getBody(t, ts.URL+"/api/admin/email/preview/"+name)
		if code != 200 || !strings.Contains(body, "<!doctype html>") {
			t.Errorf("preview %s: %d", name, code)
		}
	}
	if code, body := getBody(t, ts.URL+"/api/admin/email"); code != 200 || !strings.Contains(body, `"enabled":false`) {
		t.Errorf("overview: %d %.200s", code, body)
	}
}
