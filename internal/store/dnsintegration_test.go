package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
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

func TestSyncedClients(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	phone, err := s.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	conn := DNSIntegration{Kind: KindAdGuard, BaseURL: "http://127.0.0.1:3000/control", Enabled: true, SyncNames: true}
	if err := s.SaveDNSIntegration(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.DNSIntegration(ctx); !ok || !got.Enabled || !got.SyncNames {
		t.Errorf("the switches weren't kept: %+v", got)
	}

	if got, err := s.SyncedClients(ctx); err != nil || len(got) != 0 {
		t.Fatalf("SyncedClients = %v, %v; want none", got, err)
	}
	rec := SyncedClient{ClientID: phone.ID, Name: "phone", IDs: []string{"10.8.0.2", "fd00::2"}}
	if err := s.SaveSyncedClient(ctx, rec); err != nil {
		t.Fatal(err)
	}
	rec.Name = "phone-renamed"
	if err := s.SaveSyncedClient(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.SyncedClients(ctx)
	if err != nil || len(got) != 1 || got[phone.ID].Name != "phone-renamed" || !slices.Equal(got[phone.ID].IDs, rec.IDs) {
		t.Fatalf("SyncedClients = %+v, %v", got, err)
	}

	// The record outlives its client: it's how sync knows which name to delete in AdGuard Home.
	if _, err := s.DeleteClient(ctx, ByID(phone.ID)); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.SyncedClients(ctx); len(got) != 1 {
		t.Fatalf("deleting a client deleted its record: %v", got)
	}
	if err := s.DeleteSyncedClient(ctx, phone.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSyncedClient(ctx, phone.ID); err != nil {
		t.Errorf("forgetting nothing: %v", err)
	}
	if got, _ = s.SyncedClients(ctx); len(got) != 0 {
		t.Errorf("the record is still there: %v", got)
	}
}

// What sync wrote belongs to one AdGuard Home. Another address is another one, and removing the
// connection forgets it all, but a save that leaves the address alone keeps the record.
func TestSyncedClientsBelongToOneAddress(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	conn := DNSIntegration{Kind: KindAdGuard, BaseURL: "http://127.0.0.1:3000/control", Username: "u", Password: "p", Enabled: true}
	rec := SyncedClient{ClientID: "c1", Name: "phone", IDs: []string{"10.8.0.2"}}
	save := func(c DNSIntegration) {
		t.Helper()
		if err := s.SaveDNSIntegration(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int { got, _ := s.SyncedClients(ctx); return len(got) }

	save(conn)
	if err := s.SaveSyncedClient(ctx, rec); err != nil {
		t.Fatal(err)
	}
	conn.Password, conn.Enabled = "another", false
	save(conn)
	if count() != 1 {
		t.Error("changing the password or the switch forgot what sync wrote")
	}
	conn.BaseURL = "http://192.0.2.7:3000/control"
	save(conn)
	if count() != 0 {
		t.Error("another address kept the record of the first one's clients")
	}

	if err := s.SaveSyncedClient(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDNSIntegration(ctx); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Error("removing the connection kept the record")
	}
}

// A connection saved before the sync existed is kept, and is off until the admin turns it on.
func TestMigrationAddsTheSyncSwitchesToASavedConnection(t *testing.T) {
	ctx := context.Background()
	s, path := openTest(t)
	if _, err := s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	rollBackTo(t, s, 6)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO dns_integration (id, kind, base_url, username, password_enc, updated_at)
		VALUES (1, 'adguard', 'http://127.0.0.1:3000/control', 'u', NULL, '2026-10-03T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	again, err := Open(ctx, path, testSealer(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	got, ok, err := again.DNSIntegration(ctx)
	if err != nil || !ok || got.Username != "u" || got.Enabled || !got.SyncNames {
		t.Fatalf("DNSIntegration = %+v, %v, %v; want it kept, off, with name sync on once it's turned on", got, ok, err)
	}
	if recs, err := again.SyncedClients(ctx); err != nil || len(recs) != 0 {
		t.Errorf("SyncedClients = %v, %v", recs, err)
	}
}
