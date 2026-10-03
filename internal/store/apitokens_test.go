package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func tokenUser(t *testing.T, s *Store) User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func newToken(u User, id, name string) APIToken {
	return APIToken{
		ID: id, UserID: u.ID, Name: name, Prefix: "dbt_AbCd", TokenHash: []byte("hash-of-" + id),
		Scope: "read", CreatedAt: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC),
	}
}

func TestAPITokens(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	u := tokenUser(t, s)

	if got, err := s.APITokens(ctx, u.ID); err != nil || len(got) != 0 {
		t.Fatalf("APITokens = %v, %v; want none", got, err)
	}
	a, b := newToken(u, "a", "Homepage"), newToken(u, "b", "Grafana")
	b.CreatedAt = a.CreatedAt.Add(time.Hour)
	for _, tok := range []APIToken{a, b} {
		if err := s.CreateAPIToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.CountAPITokens(ctx, u.ID); n != 2 {
		t.Errorf("CountAPITokens = %d", n)
	}

	// Newest first, and a token that hasn't been used has no last use.
	got, err := s.APITokens(ctx, u.ID)
	if err != nil || len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" {
		t.Fatalf("APITokens = %+v, %v", got, err)
	}
	if !got[1].LastUsedAt.IsZero() || got[1].Name != "Homepage" || got[1].Prefix != "dbt_AbCd" || got[1].Scope != "read" ||
		!got[1].CreatedAt.Equal(a.CreatedAt) {
		t.Errorf("token = %+v", got[1])
	}

	// A lookup by the hash finds the token; any other hash doesn't.
	found, err := s.APITokenByHash(ctx, []byte("hash-of-a"))
	if err != nil || found.ID != "a" {
		t.Fatalf("APITokenByHash = %+v, %v", found, err)
	}
	if _, err := s.APITokenByHash(ctx, []byte("hash-of-zzz")); !errors.Is(err, ErrNoToken) {
		t.Errorf("an unknown hash: err = %v, want ErrNoToken", err)
	}

	used := a.CreatedAt.Add(3 * time.Hour)
	if err := s.TouchAPIToken(ctx, "a", used); err != nil {
		t.Fatal(err)
	}
	if found, _ = s.APITokenByHash(ctx, []byte("hash-of-a")); !found.LastUsedAt.Equal(used) {
		t.Errorf("LastUsedAt = %v, want %v", found.LastUsedAt, used)
	}

	// Revoking returns what was revoked, and it's gone. Another account can't revoke it.
	if _, err := s.DeleteAPIToken(ctx, "someone-else", "a"); !errors.Is(err, ErrNoToken) {
		t.Errorf("revoking another account's token: err = %v, want ErrNoToken", err)
	}
	gone, err := s.DeleteAPIToken(ctx, u.ID, "a")
	if err != nil || gone.Name != "Homepage" {
		t.Fatalf("DeleteAPIToken = %+v, %v", gone, err)
	}
	if _, err := s.APITokenByHash(ctx, []byte("hash-of-a")); !errors.Is(err, ErrNoToken) {
		t.Errorf("a revoked token is still found: %v", err)
	}
	if _, err := s.DeleteAPIToken(ctx, u.ID, "a"); !errors.Is(err, ErrNoToken) {
		t.Errorf("revoking twice: err = %v", err)
	}

	if n, err := s.DeleteAPITokens(ctx, u.ID); err != nil || n != 1 {
		t.Errorf("DeleteAPITokens = %d, %v; want 1", n, err)
	}
	if n, _ := s.CountAPITokens(ctx, u.ID); n != 0 {
		t.Errorf("tokens left: %d", n)
	}
}

func TestAPITokenNamesAreUnique(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	u := tokenUser(t, s)
	if err := s.CreateAPIToken(ctx, newToken(u, "a", "Homepage")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAPIToken(ctx, newToken(u, "b", "homepage")); !errors.Is(err, ErrTokenNameTaken) {
		t.Errorf("the same name in another case: err = %v, want ErrTokenNameTaken", err)
	}
	// Revoking frees the name.
	if _, err := s.DeleteAPIToken(ctx, u.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAPIToken(ctx, newToken(u, "d", "Homepage")); err != nil {
		t.Errorf("a revoked token's name stays taken: %v", err)
	}
}

// Only a scope of "read" exists, and the database says so, whatever the code does.
func TestAPITokenScopeIsRead(t *testing.T) {
	s, _ := openTest(t)
	u := tokenUser(t, s)
	tok := newToken(u, "a", "Homepage")
	tok.Scope = "admin"
	if err := s.CreateAPIToken(context.Background(), tok); err == nil {
		t.Error("a token with another scope was stored")
	}
}

// What's in the database file is the hash, and not the token the hash is of.
func TestAPITokenIsNotStoredInTheClear(t *testing.T) {
	ctx := context.Background()
	s, path := openTest(t)
	u := tokenUser(t, s)
	const secret = "dbt_the-token-that-must-not-be-in-the-file-0123456789"
	tok := newToken(u, "a", "Homepage")
	tok.TokenHash = []byte("a hash, not the secret")
	if err := s.CreateAPIToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Error("the token is in the database file")
	}
}

// A database from before API tokens keeps its data when a newer build opens it.
func TestMigrationAddsAPITokensToAnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	s, path := openTest(t)
	if _, err := s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	u := tokenUser(t, s)
	rollBackTo(t, s, 7)
	_ = s.Close()

	again, err := Open(ctx, path, testSealer(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, err := again.UserByName(ctx, "admin"); err != nil || got.ID != u.ID {
		t.Fatalf("the account was lost: %+v, %v", got, err)
	}
	if got, err := again.APITokens(ctx, u.ID); err != nil || len(got) != 0 {
		t.Fatalf("APITokens = %v, %v; want none", got, err)
	}
}
