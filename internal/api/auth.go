package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"wera/internal/auth"
	"wera/internal/normalize"
	"wera/internal/store"
)

// sessionCookie is the name of the login cookie.
const sessionCookie = "wera_session"

// sessionTTL is how long a login lasts.
const sessionTTL = 30 * 24 * time.Hour

type ctxKey int

const userKey ctxKey = iota

// currentUser returns the logged-in user set by requireUser.
func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

// sessionHash returns the hash of the request's session cookie, or "".
func sessionHash(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	return auth.HashToken(c.Value)
}

// requireUser rejects requests without a live session (401).
func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The extension authenticates with a bearer token; when one is
		// present the cookie is ignored.
		if bh := bearerHash(r); bh != "" {
			u, err := store.APITokenUser(r.Context(), s.Pool, bh)
			if errors.Is(err, pgx.ErrNoRows) {
				s.writeError(w, http.StatusUnauthorized, "this extension was disconnected; sign in again")
				return
			}
			if err != nil {
				s.Log.Error("token lookup failed", "err", err)
				s.writeError(w, http.StatusInternalServerError, "token lookup failed")
				return
			}
			s.touch(r.Context(), u.ID)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
			return
		}
		h := sessionHash(r)
		if h == "" {
			s.writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		u, err := store.SessionUser(r.Context(), s.Pool, h)
		if errors.Is(err, pgx.ErrNoRows) {
			s.clearSessionCookie(w)
			s.writeError(w, http.StatusUnauthorized, "session expired; log in again")
			return
		}
		if err != nil {
			s.Log.Error("session lookup failed", "err", err)
			s.writeError(w, http.StatusInternalServerError, "session lookup failed")
			return
		}
		s.touch(r.Context(), u.ID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

// touch records that the user is active (the worker scores fewer jobs
// for users who stay away). A failure only costs accuracy, so it is logged.
func (s *Server) touch(ctx context.Context, userID int64) {
	if err := store.TouchUser(ctx, s.Pool, userID); err != nil {
		s.Log.Warn("recording activity failed", "user_id", userID, "err", err)
	}
}

// requireAdmin allows only admins signed in with a browser session (403):
// an extension token never reaches admin routes. It must run after
// requireUser.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := currentUser(r); u == nil || !u.IsAdmin || bearerHash(r) != "" {
			s.writeError(w, http.StatusForbidden, "admins only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects state-changing requests whose Origin is neither this
// host nor a configured public origin. SameSite=Lax cookies already stop
// most cross-site requests; this closes the rest.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		// Bearer tokens are never attached by a browser on its own, so these
		// requests cannot be forged cross-site; the extension's origin is
		// chrome-extension://. Signing in for a token needs the password,
		// which a forged request does not have.
		if bearerHash(r) != "" || (r.Method == http.MethodPost && r.URL.Path == "/api/ext/tokens") {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if (origin != "" && !s.originAllowed(r, origin)) || (origin == "" && crossSite(r)) {
			s.writeError(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(r *http.Request, origin string) bool {
	if allowedOrigins[origin] {
		return true
	}
	for _, o := range s.PublicOrigins {
		if strings.EqualFold(o, origin) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" && s.TrustProxy {
		host = fh
	}
	return strings.EqualFold(u.Host, host)
}

// sessionOnly refuses an extension token: changing the password, deleting
// the account and similar need the signed-in dashboard, so a leaked
// extension key can't take the account over. It writes the 403 itself.
func (s *Server) sessionOnly(w http.ResponseWriter, r *http.Request) bool {
	if bearerHash(r) != "" {
		s.writeError(w, http.StatusForbidden, "do this from the dashboard, signed in")
		return false
	}
	return true
}

// passwordTry rate-limits checks of a signed-in user's current password
// (changing it, deleting the account), so it can't be guessed online.
func (s *Server) passwordTry(w http.ResponseWriter, userID int64) bool {
	if !s.accountLimit.Allow("password:" + strconv.FormatInt(userID, 10)) {
		s.writeError(w, http.StatusTooManyRequests, "too many password attempts; try again in 15 minutes")
		return false
	}
	return true
}

// clientIP is the rate-limit key: the peer address, or the first proxy
// header when TrustProxy is set (behind cloudflared / Vercel / nginx).
func (s *Server) clientIP(r *http.Request) string {
	if s.TrustProxy {
		// X-Real-IP is set by our own proxy (nginx overwrites any value a
		// client sends), so it comes first; CF-Connecting-IP only counts
		// when that proxy forwards it. For X-Forwarded-For the last hop is
		// the one the proxy appended: earlier entries are client-supplied
		// and would let an attacker pick a fresh rate-limit bucket per try.
		for _, h := range []string{"X-Real-IP", "CF-Connecting-IP"} {
			if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
				return v
			}
		}
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			parts := strings.Split(v, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	// Secure is configurable: the LAN deployment is plain HTTP; set
	// COOKIE_SECURE=true behind TLS.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // see above
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows COOKIE_SECURE, as above
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

// startSession creates a session for u and sets the cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) bool {
	token, hash, err := auth.NewToken()
	if err == nil {
		err = store.CreateSession(r.Context(), s.Pool, u.ID, hash, sessionTTL)
	}
	if err != nil {
		s.Log.Error("create session failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not start a session")
		return false
	}
	s.setSessionCookie(w, token)
	return true
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	return dec.Decode(v)
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	if !s.SignupEnabled {
		s.writeError(w, http.StatusForbidden, "signups are closed")
		return
	}
	if !s.signupLimit.Allow(s.clientIP(r)) {
		s.writeError(w, http.StatusTooManyRequests, "too many signups from your network; try again later")
		return
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	email, err := auth.NormalizeEmail(c.Email)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := auth.CheckPasswordPolicy(c.Password); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := normalize.Clip(strings.TrimSpace(c.Name), 100)
	hash, err := auth.HashPassword(c.Password)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	u, err := store.CreateUser(r.Context(), s.Pool, email, name, hash, false)
	if errors.Is(err, store.ErrEmailTaken) {
		s.writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.Log.Error("signup failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not create the account")
		return
	}
	if err := store.SetBudget(r.Context(), s.Pool, u.ID, s.UserBudgetUSD); err != nil {
		s.Log.Error("setting budget failed", "user_id", u.ID, "err", err)
	}
	s.Log.Info("user signed up", "user_id", u.ID)
	if s.Notify.Enabled() {
		if err := s.Notify.SendVerification(r.Context(), u); err != nil {
			s.Log.Error("verification email failed", "user_id", u.ID, "err", err)
		}
	}
	if s.startSession(w, r, u) {
		s.writeJSON(w, http.StatusCreated, map[string]any{"user": u})
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimit.Allow(s.clientIP(r)) {
		s.writeError(w, http.StatusTooManyRequests, "too many login attempts; try again in a few minutes")
		return
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	// Per account too, so guesses spread over many IPs still hit a wall.
	if !s.accountLimit.Allow(strings.ToLower(strings.TrimSpace(c.Email))) {
		s.writeError(w, http.StatusTooManyRequests, "too many login attempts; try again in a few minutes")
		return
	}
	u, hash, err := store.UserCredentials(r.Context(), s.Pool, strings.TrimSpace(c.Email))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.Log.Error("login lookup failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	if u == nil || hash == "" {
		auth.EqualizeTiming(c.Password)
		s.writeError(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	if !auth.CheckPassword(hash, c.Password) {
		s.writeError(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	if s.startSession(w, r, u) {
		s.writeJSON(w, http.StatusOK, map[string]any{"user": u})
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if h := sessionHash(r); h != "" {
		if err := store.DeleteSession(r.Context(), s.Pool, h); err != nil {
			s.Log.Error("logout failed", "err", err)
		}
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	v, err := store.AvatarVersion(r.Context(), s.Pool, u.ID)
	if err != nil {
		s.Log.Warn("avatar lookup failed", "err", err)
	}
	verified, err := store.EmailVerified(r.Context(), s.Pool, u.ID)
	if err != nil {
		s.Log.Warn("verified lookup failed", "err", err)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"user": u, "avatar_version": v, "email_verified": verified})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	u := currentUser(r)
	if !s.sessionOnly(w, r) || !s.passwordTry(w, u.ID) {
		return
	}
	hash, err := store.PasswordHash(r.Context(), s.Pool, u.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if hash == "" || !auth.CheckPassword(hash, body.Current) {
		s.writeError(w, http.StatusUnauthorized, "current password is wrong")
		return
	}
	if err := auth.CheckPasswordPolicy(body.New); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	newHash, err := auth.HashPassword(body.New)
	if err == nil {
		err = store.SetPassword(r.Context(), s.Pool, u.ID, newHash, sessionHash(r))
	}
	if err != nil {
		s.Log.Error("change password failed", "err", err)
		s.writeError(w, http.StatusInternalServerError, "could not change the password")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
