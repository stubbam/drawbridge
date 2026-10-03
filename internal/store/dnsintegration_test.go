package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stuffam/drawbridge/internal/keys"
)

func TestDNSIntegrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)

	if _, ok, err := s.DNSIntegration(ctx); err != nil || ok {
		t.Fatalf("a new database has a connection: ok %v, err %v", ok, err)
	}

	want := DNSIntegration{Kind: KindAdGuard, BaseURL: "http://127.0.0.1:3000/control", Username: "drawbridge", Password: "p@ss w0rd: ✓"}
	if err := s.SaveDNSIntegration(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.DNSIntegration(ctx)
	if err != nil || !ok || got != want {
		t.Fatalf("DNSIntegration = %+v, %v, %v; want %+v", got, ok, err, want)
	}

	// Saving again replaces it, and a connection with no login has no password.
	next := DNSIntegration{Kind: KindAdGuard, BaseURL: "https://dns.example.com/control"}
	if err := s.SaveDNSIntegration(ctx, next); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = s.DNSIntegration(ctx); got != next {
		t.Errorf("after replacing: %+v, want %+v", got, next)
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT password_enc FROM dns_integration`).Scan(&sealed); err != nil || sealed != nil {
		t.Errorf("password_enc = %v, %v; want NULL when there's no password", sealed, err)
	}

	if err := s.DeleteDNSIntegration(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.DNSIntegration(ctx); ok {
		t.Error("the connection is still there after Delete")
	}
	if err := s.DeleteDNSIntegration(ctx); err != nil {
		t.Errorf("deleting nothing: %v", err)
	}
}

// The password is sealed in the database file, and only the secret key it was sealed with
// opens it.
func TestDNSIntegrationPasswordIsSealed(t *testing.T) {
	ctx := context.Background()
	s, path := openTest(t)
	const password = "correct horse battery staple"
	if err := s.SaveDNSIntegration(ctx, DNSIntegration{
		Kind: KindAdGuard, BaseURL: "http://127.0.0.1:3000/control", Username: "u", Password: password,
	}); err != nil {
		t.Fatal(err)
	}
	// A checkpoint puts what's in the write-ahead log into the file itself.
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT password_enc FROM dns_integration`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(password)) {
		t.Error("the password is in the column in the clear")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(password)) {
		t.Error("the password is in the database file in the clear")
	}
	_ = s.Close()

	other, err := Open(ctx, path, testSealer(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if _, _, err := other.DNSIntegration(ctx); !errors.Is(err, keys.ErrOpen) {
		t.Errorf("reading with another secret key: err = %v, want keys.ErrOpen", err)
	}
}

// A database from before the connection table exists keeps its data when a newer build opens
// it, and starts with no connection.
func TestMigrationAddsDNSIntegrationToAnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	s, path := openTest(t)
	if _, err := s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	rollBackTo(t, s, 5)
	_ = s.Close()

	again, err := Open(ctx, path, testSealer(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := again.Client(ctx, ByName("phone")); err != nil {
		t.Fatalf("client lost after the upgrade: %v", err)
	}
	if _, ok, err := again.DNSIntegration(ctx); err != nil || ok {
		t.Fatalf("DNSIntegration: ok %v, err %v, want none", ok, err)
	}
}
