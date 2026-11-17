// Package auth holds password hashing, session tokens, and the small
// in-memory rate limiter used by the login and signup endpoints.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// MinPasswordLen is the shortest accepted password, in characters.
const MinPasswordLen = 10

// maxPasswordBytes is bcrypt's input limit; longer passwords are rejected
// rather than silently truncated.
const maxPasswordBytes = 72

// productionCost balances login latency (~250ms) against brute force.
const productionCost = 12

// bcryptCost is productionCost, except in test binaries: there the cost
// protects nothing, and under the race detector it made the API tests
// spend about two minutes hashing.
var bcryptCost = func() int {
	if testing.Testing() {
		return bcrypt.MinCost
	}
	return productionCost
}()

// Validation errors returned to the client verbatim.
var (
	ErrBadEmail      = errors.New("enter a valid email address")
	ErrShortPassword = errors.New("password must be at least 10 characters")
	ErrLongPassword  = errors.New("password must be at most 72 bytes")
)

// NormalizeEmail trims and lower-cases an email and checks its shape.
func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@"):], ".") {
		return "", ErrBadEmail
	}
	return email, nil
}

// CheckPasswordPolicy enforces the length rules.
func CheckPasswordPolicy(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return ErrShortPassword
	}
	if len(pw) > maxPasswordBytes {
		return ErrLongPassword
	}
	return nil
}

// HashPassword returns a bcrypt hash of pw.
func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword reports whether pw matches the bcrypt hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// dummyHash is compared against when an email is unknown, so login takes
// the same time whether or not the account exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("wera-timing-equalizer"), bcryptCost)

// EqualizeTiming burns one bcrypt comparison.
func EqualizeTiming(pw string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
}

// NewToken returns a random 256-bit token (base64url) and its storage hash.
func NewToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is the sha256 hex digest stored for a session token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RandomPassword returns a random password for CLI-created accounts.
func RandomPassword() (string, error) {
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
