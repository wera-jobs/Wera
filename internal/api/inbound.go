package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"wera/internal/inbound"
	"wera/internal/store"
)

// maxInboundBytes is the most of one email the webhook reads; the worker
// sends no more.
const maxInboundBytes = 1 << 20

// inboundEnabled reports whether forwarding addresses work on this server.
func (s *Server) inboundEnabled() bool {
	return s.Env != nil && s.Env.InboundEmailDomain != "" && s.Env.InboundEmailSecret != ""
}

// inboundSignature is what the worker signs: the time, the recipient and
// the message, so none can be swapped.
func inboundSignature(secret, ts, rcpt string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "." + strings.ToLower(rcpt) + "."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

var (
	gmailConfirmCode = regexp.MustCompile(`\(#(\d{6,12})\)`)
	gmailConfirmURL  = regexp.MustCompile(`https://(mail-settings|mail)\.google\.com/mail/[^\s"'<>]+`)
)

// receiveEmail is POST /api/inbound-email: the Cloudflare Email Worker
// hands over one message sent to a user's forwarding address. The body is
// the raw message; X-Wera-Rcpt is the envelope recipient, X-Wera-Timestamp
// the Unix time and X-Wera-Signature the HMAC-SHA256 (hex) of
// "timestamp.recipient.message" with INBOUND_EMAIL_SECRET. An unknown
// address gets 404 (the worker bounces it); everything else 204, matched
// or not. The message itself is read in memory and dropped.
func (s *Server) receiveEmail(w http.ResponseWriter, r *http.Request) {
	if !s.inboundEnabled() {
		s.writeError(w, http.StatusNotFound, "not found")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboundBytes+1))
	if err != nil || len(body) > maxInboundBytes {
		s.writeError(w, http.StatusRequestEntityTooLarge, "message too large")
		return
	}
	ts, rcpt := r.Header.Get("X-Wera-Timestamp"), strings.ToLower(strings.TrimSpace(r.Header.Get("X-Wera-Rcpt")))
	sec, err := strconv.ParseInt(ts, 10, 64)
	want := inboundSignature(s.Env.InboundEmailSecret, ts, rcpt, body)
	if err != nil || !hmac.Equal([]byte(want), []byte(strings.ToLower(r.Header.Get("X-Wera-Signature")))) {
		s.writeError(w, http.StatusUnauthorized, "bad signature")
		return
	}
	if d := time.Since(time.Unix(sec, 0)); d > 5*time.Minute || d < -5*time.Minute {
		s.writeError(w, http.StatusUnauthorized, "stale request")
		return
	}
	local, domain, ok := strings.Cut(rcpt, "@")
	token, isOurs := strings.CutPrefix(local, "u-")
	if !ok || !isOurs || domain != strings.ToLower(s.Env.InboundEmailDomain) || token == "" {
		s.writeError(w, http.StatusNotFound, "unknown address")
		return
	}
	ctx := r.Context()
	userID, err := store.InboundUser(ctx, s.Pool, token)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if userID == 0 {
		s.writeError(w, http.StatusNotFound, "unknown address")
		return
	}
	e, err := inbound.Parse(body)
	if err != nil {
		w.WriteHeader(http.StatusNoContent) // unreadable mail is dropped, not retried
		return
	}
	// Gmail asks the address to confirm before it forwards anything.
	if e.FromAddr == "forwarding-noreply@google.com" {
		code := ""
		if m := gmailConfirmCode.FindStringSubmatch(e.Subject); m != nil {
			code = m[1]
		}
		link := ""
		if m := gmailConfirmURL.FindString(e.Text); m != "" {
			if u, err := url.Parse(m); err == nil && (u.Host == "mail-settings.google.com" || u.Host == "mail.google.com") {
				link = u.String()
			}
		}
		if code != "" || link != "" {
			if err := store.SaveForwardConfirmation(ctx, s.Pool, userID, code, link); err != nil {
				s.Log.Error("saving forwarding confirmation failed", "err", err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	apps, err := store.InboundApps(ctx, s.Pool, userID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	sug := inbound.Suggest(e, apps)
	if sug == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	at := e.Date
	if at.IsZero() || at.After(time.Now().Add(time.Hour)) {
		at = time.Now()
	}
	if err := store.SaveEmailSuggestion(ctx, s.Pool, userID, *sug, e.FromDomain(), e.Subject, at); err != nil {
		s.Log.Error("saving email suggestion failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	s.Log.Info("email suggestion", "user_id", userID, "job_id", sug.App.JobID, "kind", sug.Kind)
	w.WriteHeader(http.StatusNoContent)
}

// inboundAddressView is the user's forwarding setup.
type inboundAddressView struct {
	Available bool                  `json:"available"` // the server takes forwarded mail
	Address   *string               `json:"address"`   // nil while it is off
	Setup     *store.InboundAddress `json:"setup"`
}

func (s *Server) inboundView(a *store.InboundAddress) inboundAddressView {
	v := inboundAddressView{Available: s.inboundEnabled(), Setup: a}
	if a != nil && v.Available {
		addr := "u-" + a.Token + "@" + s.Env.InboundEmailDomain
		v.Address = &addr
	}
	return v
}

// getInboundAddress is GET /api/inbound-address.
func (s *Server) getInboundAddress(w http.ResponseWriter, r *http.Request) {
	a, err := store.GetInboundAddress(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, s.inboundView(a))
}

// newInboundAddress is POST /api/inbound-address: turn forwarding on, or
// replace the address (the old one stops working).
func (s *Server) newInboundAddress(w http.ResponseWriter, r *http.Request) {
	if !s.inboundEnabled() {
		s.writeError(w, http.StatusServiceUnavailable, "email forwarding is not set up on this server")
		return
	}
	a, err := store.NewInboundAddress(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "save failed")
		return
	}
	s.writeJSON(w, http.StatusOK, s.inboundView(a))
}

// deleteInboundAddress is DELETE /api/inbound-address: turn forwarding off.
func (s *Server) deleteInboundAddress(w http.ResponseWriter, r *http.Request) {
	if err := store.DeleteInboundAddress(r.Context(), s.Pool, currentUser(r).ID); err != nil {
		s.writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	s.writeJSON(w, http.StatusOK, s.inboundView(nil))
}

// emailSuggestions is GET /api/email-suggestions: tracker changes waiting
// for the user's approval.
func (s *Server) emailSuggestions(w http.ResponseWriter, r *http.Request) {
	list, err := store.PendingEmailSuggestions(r.Context(), s.Pool, currentUser(r).ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"suggestions": list})
}

// decideEmailSuggestion is POST /api/email-suggestions/{id}/{accept|dismiss}.
// Accepting sets the job's tracker status; nothing changes otherwise.
func (s *Server) decideEmailSuggestion(accept bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.pathID(w, r, "id", "suggestion")
		if !ok {
			return
		}
		uid := currentUser(r).ID
		jobID, status, err := store.DecideEmailSuggestion(r.Context(), s.Pool, uid, id, accept)
		if errors.Is(err, store.ErrSuggestionNotFound) {
			s.writeError(w, http.StatusNotFound, "suggestion not found")
			return
		}
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "save failed")
			return
		}
		if accept {
			if err := store.PutApplication(r.Context(), s.Pool, uid, jobID, store.ApplicationInput{Status: status}); err != nil {
				s.Log.Error("applying email suggestion failed", "err", err)
				s.writeError(w, http.StatusInternalServerError, "save failed")
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
