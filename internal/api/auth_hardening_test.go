package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"wera/internal/auth"
	"wera/internal/testutil"
)

// A made-up reset token is refused before the password is hashed, and the
// emailed-link endpoints are rate-limited per IP.
func TestResetLinkChecksTokenFirstAndIsRateLimited(t *testing.T) {
	base, _, _ := mailServer(t)
	anon := &http.Client{}

	hashed := 0
	hashPassword = func(pw string) (string, error) { hashed++; return auth.HashPassword(pw) }
	t.Cleanup(func() { hashPassword = auth.HashPassword })

	code, body := do(t, anon, http.MethodPost, base+"/api/auth/password/reset", `{"token":"made-up-token","password":"a long enough password 1"}`)
	if code != 400 || !strings.Contains(body, "link") {
		t.Fatalf("bad token: want 400, got %d %s", code, body)
	}
	if hashed != 0 {
		t.Errorf("a bad token ran bcrypt %d time(s); it should be refused before hashing", hashed)
	}

	// 20 tries per IP in 15 minutes (one used above), shared by both links.
	for i := 2; i <= 20; i++ {
		path, body := "/api/auth/password/reset", `{"token":"made-up-token","password":"a long enough password 1"}`
		if i%2 == 0 {
			path, body = "/api/auth/verify-email", `{"token":"made-up-token"}`
		}
		if code, _ := do(t, anon, http.MethodPost, base+path, body); code != 400 {
			t.Fatalf("try %d on %s: want 400, got %d", i, path, code)
		}
	}
	for _, path := range []string{"/api/auth/password/reset", "/api/auth/verify-email"} {
		if code, _ := do(t, anon, http.MethodPost, base+path, `{"token":"made-up-token","password":"a long enough password 1"}`); code != 429 {
			t.Errorf("%s after 20 tries: want 429, got %d", path, code)
		}
	}
}

// An extension token can't change the password, delete the account or ask
// for a confirmation email: those need the signed-in dashboard.
func TestAccountActionsRefuseExtensionTokens(t *testing.T) {
	base, _, _ := mailServer(t)
	ext, email := connectExtension(t, base)
	pool := testPool(t)
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email) })

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/auth/password", `{"current_password":"extension pass 1","new_password":"taken over 12345"}`},
		{http.MethodDelete, "/api/auth/account", `{"password":"extension pass 1"}`},
		{http.MethodPost, "/api/auth/verify-email/send", ``},
	} {
		if code, body := do(t, ext, c.method, base+c.path, c.body); code != 403 {
			t.Errorf("%s %s with an extension token: want 403, got %d %s", c.method, c.path, code, body)
		}
	}
	// The account and its password are untouched.
	if code, _ := do(t, ext, http.MethodGet, base+"/api/auth/me", ""); code != 200 {
		t.Errorf("the extension token should still work: %d", code)
	}
}

// The current password can be tried 10 times in 15 minutes per user,
// across changing the password and deleting the account.
func TestCurrentPasswordTriesAreLimited(t *testing.T) {
	ts, pool := testServer(t, nil)
	jar, _ := cookiejar.New(nil)
	user := &http.Client{Jar: jar}
	email := testutil.Unique("pwtries") + "@example.com"
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email) })
	if code, body := do(t, user, http.MethodPost, ts.URL+"/api/auth/signup", `{"email":"`+email+`","password":"the real password 1"}`); code != 201 {
		t.Fatalf("signup: %d %s", code, body)
	}
	for i := 1; i <= 10; i++ {
		path, method, body := "/api/auth/password", http.MethodPut, `{"current_password":"guess `+fmt.Sprint(i)+`","new_password":"new password 123"}`
		if i%2 == 0 {
			path, method, body = "/api/auth/account", http.MethodDelete, `{"password":"guess `+fmt.Sprint(i)+`"}`
		}
		if code, _ := do(t, user, method, ts.URL+path, body); code != 401 {
			t.Fatalf("guess %d: want 401, got %d", i, code)
		}
	}
	// Even the right password is refused until the window passes.
	if code, _ := do(t, user, http.MethodDelete, ts.URL+"/api/auth/account", `{"password":"the real password 1"}`); code != 429 {
		t.Errorf("11th try: want 429, got %d", code)
	}
	if code, _ := do(t, user, http.MethodGet, ts.URL+"/api/auth/me", ""); code != 200 {
		t.Errorf("the account should still exist: %d", code)
	}
}
