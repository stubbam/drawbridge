package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
)

func tokenEnv(t *testing.T) (*Service, *clock, context.Context, Login, string) {
	t.Helper()
	s, clk := newTestService(t)
	ctx := web(context.Background(), "admin")
	password, err := s.CreateAdmin(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	login, err := s.Login(ctx, "admin", password, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	return s, clk, ctx, login, password
}

func TestCreateAPIToken(t *testing.T) {
	s, _, ctx, login, password := tokenEnv(t)
	var logs bytes.Buffer
	s.Log = slog.New(slog.NewTextHandler(&logs, nil))

	tok, secret, err := s.CreateAPIToken(ctx, login.User, password, "  Homepage ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "dbt_") || len(secret) != 47 || tok.Name != "Homepage" || tok.Scope != "read" ||
		tok.Prefix != secret[:8] || tok.UserID != login.User.ID || tok.ID == "" {
		t.Fatalf("token = %+v, secret %q", tok, secret)
	}

	// The database has the hash, not the secret, and what lists the token can't show it.
	list, err := s.ListAPITokens(ctx, login.User.ID)
	if err != nil || len(list) != 1 || list[0].ID != tok.ID || !list[0].LastUsedAt.IsZero() {
		t.Fatalf("ListAPITokens = %+v, %v", list, err)
	}
	if raw, _ := json.Marshal(list); strings.Contains(string(raw), secret) {
		t.Error("the secret is in the listing")
	}

	// The event says which token, and never what it is. Nor does the journal.
	events, _ := s.Events(ctx, store.EventFilter{Kind: "auth.token_created"})
	if len(events) != 1 || events[0].Data["name"] != "Homepage" || events[0].Data["prefix"] != tok.Prefix ||
		events[0].Data["token"] != tok.ID || events[0].Actor != "admin" {
		t.Fatalf("events = %+v", events)
	}
	all, _ := s.Events(ctx, store.EventFilter{})
	if raw, _ := json.Marshal(all); strings.Contains(string(raw), secret) || strings.Contains(logs.String(), secret) {
		t.Error("the secret is in an event or in the log")
	}
}

// A token outlives a session, so making one takes the password: a logged-in session isn't enough.
func TestCreateAPITokenNeedsThePassword(t *testing.T) {
	s, _, ctx, login, password := tokenEnv(t)
	if _, secret, err := s.CreateAPIToken(ctx, login.User, "not the password", "Homepage"); !errors.Is(err, ErrWrongPassword) ||
		!model.IsInvalid(err) || secret != "" {
		t.Fatalf("a wrong password: err = %v, secret %q", err, secret)
	}
	if _, _, err := s.CreateAPIToken(ctx, login.User, "", "Homepage"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("no password: err = %v", err)
	}
	if list, _ := s.ListAPITokens(ctx, login.User.ID); len(list) != 0 {
		t.Errorf("a token was made without the password: %+v", list)
	}
	if got := kinds(t, s); got[len(got)-1] != "auth.token_failed" {
		t.Errorf("the failed attempt isn't in the log: %v", got)
	}

	// The failures count against the same limit as a login: five are free (two are spent), and
	// after the sixth the right password has to wait, too.
	for range 4 {
		_, _, _ = s.CreateAPIToken(ctx, login.User, "wrong", "Homepage")
	}
	_, _, err := s.CreateAPIToken(ctx, login.User, password, "Homepage")
	var limited *RateLimitedError
	if !errors.As(err, &limited) || limited.Wait <= 0 {
		t.Fatalf("after 6 failures: err = %v, want a RateLimitedError, even for the right password", err)
	}
	if list, _ := s.ListAPITokens(ctx, login.User.ID); len(list) != 0 {
		t.Errorf("a token was made while rate limited: %+v", list)
	}
}

func TestCreateAPITokenChecksTheName(t *testing.T) {
	s, _, ctx, login, password := tokenEnv(t)
	for name, n := range map[string]string{
		"empty": "", "blank": "   ", "too long": strings.Repeat("n", MaxTokenNameLength+1), "a newline": "Home\npage",
	} {
		if _, _, err := s.CreateAPIToken(ctx, login.User, password, n); !model.IsInvalid(err) {
			t.Errorf("%s: err = %v, want an invalid-input error", name, err)
		}
	}
	if _, _, err := s.CreateAPIToken(ctx, login.User, password, strings.Repeat("é", MaxTokenNameLength)); err != nil {
		t.Errorf("a name of the longest length (in characters): %v", err)
	}
	if _, _, err := s.CreateAPIToken(ctx, login.User, password, "Homepage"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateAPIToken(ctx, login.User, password, "HOMEPAGE"); !errors.Is(err, store.ErrTokenNameTaken) {
		t.Errorf("the same name: err = %v, want ErrTokenNameTaken", err)
	}
}

func TestAPITokensAreLimited(t *testing.T) {
	s, _, ctx, login, password := tokenEnv(t)
	for i := range MaxAPITokens {
		if _, _, err := s.CreateAPIToken(ctx, login.User, password, fmt.Sprintf("dash-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.CreateAPIToken(ctx, login.User, password, "one too many"); !errors.Is(err, ErrTooManyTokens) {
		t.Errorf("err = %v, want ErrTooManyTokens", err)
	}
}

func TestAuthenticateToken(t *testing.T) {
	s, _, ctx, login, password := tokenEnv(t)
	tok, secret, err := s.CreateAPIToken(ctx, login.User, password, "Homepage")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AuthenticateToken(ctx, secret)
	if err != nil || got.ID != tok.ID || got.Name != "Homepage" {
		t.Fatalf("AuthenticateToken = %+v, %v", got, err)
	}

	// Anything else is not a token, whatever it looks like.
	flipped := secret[:len(secret)-1] + string(rune(secret[len(secret)-1]^1))
	for name, bad := range map[string]string{
		"empty": "", "a session token": login.Token, "the prefix alone": "dbt_", "one character off": flipped,
		"the right secret with a space": secret + " ", "another token": "dbt_" + strings.Repeat("A", 43),
	} {
		if _, err := s.AuthenticateToken(ctx, bad); !errors.Is(err, store.ErrNoToken) {
			t.Errorf("%s: err = %v, want ErrNoToken", name, err)
		}
	}

	// A token isn't a session, and a session token isn't an API token.
	if _, _, err := s.Authenticate(ctx, secret); !errors.Is(err, store.ErrNoSession) {
		t.Errorf("an API token worked as a session: %v", err)
	}

	if err := s.RevokeAPIToken(ctx, login.User.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateToken(ctx, secret); !errors.Is(err, store.ErrNoToken) {
		t.Errorf("a revoked token still works: %v", err)
	}
	if err := s.RevokeAPIToken(ctx, login.User.ID, tok.ID); !errors.Is(err, store.ErrNoToken) {
		t.Errorf("revoking twice: err = %v, want ErrNoToken", err)
	}
	events, _ := s.Events(ctx, store.EventFilter{Kind: "auth.token_revoked"})
	if len(events) != 1 || events[0].Data["name"] != "Homepage" || events[0].Data["token"] != tok.ID {
		t.Errorf("events = %+v", events)
	}
}

// A dashboard asks every few seconds. The token's last use is written once an hour, not on each ask.
func TestAPITokenUseIsWrittenOncePerHour(t *testing.T) {
	s, clk, ctx, login, password := tokenEnv(t)
	tok, secret, _ := s.CreateAPIToken(ctx, login.User, password, "Homepage")
	lastUsed := func() time.Time {
		list, _ := s.ListAPITokens(ctx, login.User.ID)
		for _, l := range list {
			if l.ID == tok.ID {
				return l.LastUsedAt
			}
		}
		t.Fatal("token gone")
		return time.Time{}
	}

	first := clk.now()
	if _, err := s.AuthenticateToken(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); !got.Equal(first) {
		t.Fatalf("first use: last used %v, want %v", got, first)
	}
	// Every ten seconds for 59 minutes: nothing is written.
	for range 354 {
		clk.advance(10 * time.Second)
		if _, err := s.AuthenticateToken(ctx, secret); err != nil {
			t.Fatal(err)
		}
	}
	if got := lastUsed(); !got.Equal(first) {
		t.Errorf("after 59 minutes of use: last used %v, want it still %v", got, first)
	}
	clk.advance(2 * time.Minute)
	if _, err := s.AuthenticateToken(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); !got.Equal(clk.now()) {
		t.Errorf("after an hour: last used %v, want %v", got, clk.now())
	}
}

// Taking the account back with a reset takes back its tokens; a routine change of password keeps them.
func TestResetPasswordRevokesTokensButChangeDoesNot(t *testing.T) {
	s, _, ctx, login, password := tokenEnv(t)
	_, secret, _ := s.CreateAPIToken(ctx, login.User, password, "Homepage")

	if err := s.ChangePassword(ctx, login.User, login.Session, password, "a new long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateToken(ctx, secret); err != nil {
		t.Fatalf("a password change revoked the token: %v", err)
	}

	if _, _, err := s.ResetPassword(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateToken(ctx, secret); !errors.Is(err, store.ErrNoToken) {
		t.Errorf("a password reset left the token working: %v", err)
	}
	events, _ := s.Events(ctx, store.EventFilter{Kind: "auth.tokens_revoked"})
	if len(events) != 1 || events[0].Data["count"] != "1" {
		t.Errorf("events = %+v", events)
	}
	// With none to revoke, there's no event to read.
	if _, _, err := s.ResetPassword(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if events, _ = s.Events(ctx, store.EventFilter{Kind: "auth.tokens_revoked"}); len(events) != 1 {
		t.Errorf("a reset with no tokens recorded an event: %+v", events)
	}
}
