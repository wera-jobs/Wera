package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"wera/internal/auth"
	"wera/internal/mail"
	"wera/internal/notify"
	"wera/internal/store"
)

// authConfig is GET /api/auth/config: what the sign-in pages can offer.
func (s *Server) authConfig(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"signup_enabled": s.SignupEnabled, "mail_enabled": s.Notify.Enabled()})
}

func (s *Server) mailOff(w http.ResponseWriter) bool {
	if s.Notify.Enabled() {
		return false
	}
	s.writeError(w, http.StatusNotFound, "email is not set up on this server")
	return true
}

// sendVerification is POST /api/auth/verify-email/send.
func (s *Server) sendVerification(w http.ResponseWriter, r *http.Request) {
	if s.mailOff(w) {
		return
	}
	u := currentUser(r)
	if ok, err := store.EmailVerified(r.Context(), s.Pool, u.ID); err == nil && ok {
		s.writeJSON(w, http.StatusOK, map[string]any{"verified": true})
		return
	}
	if n, err := store.RecentEmailCount(r.Context(), s.Pool, u.ID, "verify", time.Hour); err != nil || n >= 3 {
		s.writeError(w, http.StatusTooManyRequests, "we sent a few links already; check your inbox and spam, or try again in an hour")
		return
	}
	if err := s.Notify.SendVerification(r.Context(), u); err != nil {
		s.Log.Error("verification email failed", "user_id", u.ID, "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not send the email")
		return
	}
	s.writeJSON(w, http.StatusAccepted, map[string]any{"sent_to": u.Email})
}

// verifyEmail is POST /api/auth/verify-email {token}: the link's landing.
func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	if s.mailOff(w) {
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Token == "" || len(in.Token) > 100 {
		s.writeError(w, http.StatusBadRequest, "the link is incomplete")
		return
	}
	userID, email, err := store.UseEmailToken(r.Context(), s.Pool, "verify", in.Token)
	if errors.Is(err, store.ErrTokenInvalid) {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err == nil {
		var ok bool
		if ok, err = store.MarkEmailVerified(r.Context(), s.Pool, userID, email); err == nil && !ok {
			s.writeError(w, http.StatusBadRequest, "this link was for an address the account no longer uses")
			return
		}
	}
	if err != nil {
		s.Log.Error("verify email failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not confirm the address")
		return
	}
	s.welcome(r.Context(), email)
	s.writeJSON(w, http.StatusOK, map[string]any{"verified": true, "email": email})
}

// welcome greets the account behind a newly confirmed address. The address
// is confirmed either way, so a failure here is only logged.
func (s *Server) welcome(ctx context.Context, email string) {
	u, err := store.UserByEmail(ctx, s.Pool, email)
	if err == nil {
		err = s.Notify.SendWelcome(ctx, u)
	}
	if err != nil {
		s.Log.Error("welcome email failed", "err", err)
	}
}

// forgotPassword is POST /api/auth/password/forgot {email}. The answer is
// the same whether or not an account exists.
func (s *Server) forgotPassword(w http.ResponseWriter, r *http.Request) {
	if s.mailOff(w) {
		return
	}
	if !s.loginLimit.Allow(s.clientIP(r)) {
		s.writeError(w, http.StatusTooManyRequests, "too many requests; try again in a few minutes")
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	email, err := auth.NormalizeEmail(in.Email)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Notify.SendReset(r.Context(), email); err != nil {
		s.Log.Error("reset email failed", "err", err)
	}
	s.writeJSON(w, http.StatusAccepted, map[string]any{"message": "If an account uses that address, a reset link is on its way."})
}

// resetPassword is POST /api/auth/password/reset {token, password}: sets
// the password, signs out every session and extension, and confirms the
// address (the link proved access to the inbox).
func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	if s.mailOff(w) {
		return
	}
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Token == "" || len(in.Token) > 100 {
		s.writeError(w, http.StatusBadRequest, "the link is incomplete")
		return
	}
	if err := auth.CheckPasswordPolicy(in.Password); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	userID, email, err := store.UseEmailToken(r.Context(), s.Pool, "reset", in.Token)
	if errors.Is(err, store.ErrTokenInvalid) {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err == nil {
		err = store.SetPassword(r.Context(), s.Pool, userID, hash, "")
	}
	if err != nil {
		s.Log.Error("password reset failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not reset the password")
		return
	}
	_, _ = store.MarkEmailVerified(r.Context(), s.Pool, userID, email)
	s.Log.Info("password reset", "user_id", userID)
	s.writeJSON(w, http.StatusOK, map[string]any{"reset": true})
}

// --- admin -------------------------------------------------------------------

// emailOverview is GET /api/admin/email: setup status, templates and the
// latest outbox entries.
func (s *Server) emailOverview(w http.ResponseWriter, r *http.Request) {
	recent, err := store.RecentEmails(r.Context(), s.Pool, 50)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	cfg := map[string]any{"enabled": s.Notify.Enabled()}
	if s.Notify.Enabled() {
		cfg["provider"], cfg["from"], cfg["public_url"] = s.Notify.Config.Provider, s.Notify.Config.From, s.Notify.Config.PublicURL
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"config": cfg, "templates": mail.Templates(), "outbox": recent})
}

// emailPreview is GET /api/admin/email/preview/{name}: the template with
// sample data (the digest uses the admin's own matches when there are
// any), as HTML or, with ?format=text, plain text.
func (s *Server) emailPreview(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	u := currentUser(r)
	publicURL := "https://wera.example.com"
	if s.Notify.Enabled() {
		publicURL = s.Notify.Config.PublicURL
	}
	data := mail.Sample(name, publicURL, u.Email, u.Name)
	if data == nil {
		s.writeError(w, http.StatusNotFound, "no such template")
		return
	}
	if name == "digest" {
		if d, err := notify.Digest(r.Context(), s.Pool, publicURL, u, time.Now().AddDate(0, 0, -7), "daily"); err == nil && d != nil {
			data = *d
		}
	}
	out, err := mail.Render(name, data)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Shown in a sandboxed frame: no scripts, no requests.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; frame-ancestors 'self'")
	w.Header().Set("X-Email-Subject", out.Subject)
	if r.URL.Query().Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Subject: " + out.Subject + "\n\n" + out.Text))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(out.HTML))
}

// emailTest is POST /api/admin/email/test {to}: queue the setup test.
func (s *Server) emailTest(w http.ResponseWriter, r *http.Request) {
	if s.mailOff(w) {
		return
	}
	var in struct {
		To string `json:"to"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	to := strings.TrimSpace(in.To)
	if to == "" {
		to = currentUser(r).Email
	}
	if _, err := auth.NormalizeEmail(to); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Notify.SendTest(r.Context(), to); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "email.test", "", map[string]string{"to": to})
	s.writeJSON(w, http.StatusAccepted, map[string]any{"queued_to": to})
}
