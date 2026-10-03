package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewAPIToken(t *testing.T) {
	token, prefix, hash, err := NewAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "dbt_") || len(token) != 47 || !LooksLikeAPIToken(token) {
		t.Errorf("token %q isn't dbt_ and 43 characters", token)
	}
	if prefix != token[:8] || !strings.HasPrefix(prefix, "dbt_") {
		t.Errorf("prefix = %q, want the first 8 characters of %q", prefix, token)
	}
	if !bytes.Equal(hash, HashToken(token)) || len(hash) != 32 {
		t.Error("the hash isn't the SHA-256 of the token")
	}
	other, _, otherHash, _ := NewAPIToken()
	if other == token || bytes.Equal(otherHash, hash) {
		t.Error("two tokens are the same")
	}
}

func TestLooksLikeAPIToken(t *testing.T) {
	good, _, _, _ := NewAPIToken()
	if !LooksLikeAPIToken(good) {
		t.Fatal("a real token doesn't look like one")
	}
	for name, s := range map[string]string{
		"empty":         "",
		"a session":     strings.Repeat("a", 43),
		"no prefix":     strings.Repeat("a", 47),
		"short":         good[:46],
		"long":          good + "a",
		"a space":       "dbt_" + strings.Repeat("a", 42) + " ",
		"a newline":     "dbt_" + strings.Repeat("a", 42) + "\n",
		"a slash":       "dbt_" + strings.Repeat("a", 42) + "/",
		"padding":       "dbt_" + strings.Repeat("a", 42) + "=",
		"another case":  "DBT_" + strings.Repeat("a", 43),
		"non-ASCII":     "dbt_" + strings.Repeat("a", 41) + "é",
		"bearer prefix": "Bearer " + good,
	} {
		if LooksLikeAPIToken(s) {
			t.Errorf("%s: %q looks like a token", name, s)
		}
	}
}
