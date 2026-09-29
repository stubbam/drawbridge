package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/stuffam/drawbridge/internal/model"
)

// NewSessionToken returns a random session token for a cookie, and its hash for the
// database.
func NewSessionToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken returns the hash stored for a session token. Tokens are random, so a plain
// SHA-256 is enough; there's nothing to guess.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// NewID returns a random identifier for a session.
func NewID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// alphabet has no 0, 1, I, or O, so codes read aloud or copied by hand survive.
const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// randomCode returns n characters from alphabet: 5 bits each.
func randomCode(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[b[i]%byte(len(alphabet))] // 256 is a multiple of 32: no bias.
	}
	return string(b), nil
}

// group splits s into groups of five joined by sep.
func group(s, sep string) string {
	var parts []string
	for len(s) > 5 {
		parts = append(parts, s[:5])
		s = s[5:]
	}
	return strings.Join(append(parts, s), sep)
}

// NewSetupToken returns a new first-run setup token (100 bits), normalized.
func NewSetupToken() (string, error) {
	return randomCode(20)
}

// FormatSetupToken shows a token in groups: ABCDE-FGHJK-….
func FormatSetupToken(token string) string {
	return group(token, "-")
}

// NormalizeSetupToken undoes what people do to a token when they copy it: case, dashes,
// and spaces.
func NormalizeSetupToken(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToUpper(r)
	}, s)
}

// RandomPassword returns a password for `drawbridge admin create` and
// `admin reset-password` (100 bits), in groups so it's easy to type.
func RandomPassword() (string, error) {
	code, err := randomCode(20)
	if err != nil {
		return "", err
	}
	return group(strings.ToLower(code), "-"), nil
}

// Limits on usernames and passwords.
const (
	MaxUsernameLength = 32
	MinPasswordLength = 10
	// MaxPasswordLength is in bytes. It only stops absurd inputs.
	MaxPasswordLength = 1024
)

// ValidateUsername checks a new username: letters, digits, and . _ -, starting with a
// letter or digit.
func ValidateUsername(u string) error {
	n := utf8.RuneCountInString(u)
	if n == 0 || n > MaxUsernameLength {
		return invalid(fmt.Errorf("a username must be 1–%d characters", MaxUsernameLength))
	}
	if !utf8.ValidString(u) {
		return invalid(errors.New("a username must be valid UTF-8"))
	}
	first, _ := utf8.DecodeRuneInString(u)
	if !unicode.IsLetter(first) && !unicode.IsDigit(first) {
		return invalid(errors.New("a username must start with a letter or digit"))
	}
	for _, r := range u {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-", r) {
			return invalid(fmt.Errorf("a username can't contain %q; use letters, digits, and . _ -", r))
		}
	}
	return nil
}

// ValidatePassword checks a new password. Length is the only rule (NIST SP 800-63B).
func ValidatePassword(p string) error {
	switch {
	case utf8.RuneCountInString(p) < MinPasswordLength:
		return invalid(fmt.Errorf("a password must be at least %d characters", MinPasswordLength))
	case len(p) > MaxPasswordLength:
		return invalid(fmt.Errorf("a password can be at most %d bytes", MaxPasswordLength))
	case !utf8.ValidString(p):
		return invalid(errors.New("a password must be valid UTF-8"))
	}
	return nil
}

func invalid(err error) error { return &model.InvalidError{Err: err} }
