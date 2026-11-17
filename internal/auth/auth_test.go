package auth

import (
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestNormalizeEmail(t *testing.T) {
	good := map[string]string{
		" Jesse@Example.com ":   "jesse@example.com", //nolint:gocritic // surrounding spaces are what is tested
		"a.b+c@sub.example.org": "a.b+c@sub.example.org",
	}
	for in, want := range good {
		got, err := NormalizeEmail(in)
		if err != nil || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "nobody", "a@b", "Name <a@b.com>", "a@@b.com"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Errorf("NormalizeEmail(%q): expected an error", bad)
		}
	}
}

func TestPasswords(t *testing.T) {
	if CheckPasswordPolicy("short") == nil {
		t.Error("short password accepted")
	}
	if err := CheckPasswordPolicy("long enough pw"); err != nil {
		t.Errorf("valid password rejected: %v", err)
	}
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct horse") || CheckPassword(h, "wrong horse") {
		t.Error("CheckPassword mismatch")
	}
}

func TestTokens(t *testing.T) {
	tok, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 40 || HashToken(tok) != hash || hash == tok {
		t.Errorf("bad token/hash pair: %q %q", tok, hash)
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(2, time.Hour)
	first, second, third := l.Allow("ip"), l.Allow("ip"), l.Allow("ip")
	if !first || !second || third {
		t.Error("third event in window should be refused")
	}
	if !l.Allow("other") {
		t.Error("keys are independent")
	}
}

func TestTestBinariesHashCheaply(t *testing.T) {
	if bcryptCost != bcrypt.MinCost || productionCost < 12 {
		t.Errorf("cost in tests %d, in production %d", bcryptCost, productionCost)
	}
	h, err := HashPassword("correct horse battery")
	if err != nil || !CheckPassword(h, "correct horse battery") {
		t.Fatalf("hash round trip failed: %v", err)
	}
}
