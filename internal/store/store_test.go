package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/netip"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/keys"
	"github.com/stuffam/drawbridge/internal/model"
)

func testSealer(t *testing.T, fill byte) *keys.Sealer {
	t.Helper()
	s, err := keys.NewSealer(bytes.Repeat([]byte{fill}, keys.SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "drawbridge.db")
	s, err := Open(context.Background(), path, testSealer(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func initialized(t *testing.T) *Store {
	t.Helper()
	s, _ := openTest(t)
	if _, err := s.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

// undo is what takes back each migration after the first two: the statements that remove what
// it added. A new migration needs an entry here, and TestEveryMigrationCanBeRolledBack says so.
var undo = map[int][]string{
	3: {`ALTER TABLE server DROP COLUMN admin_allowed`},
	4: {`DROP TABLE client_sessions`},
	5: {`DROP TABLE traffic`},
	6: {`DROP TABLE dns_integration`},
	7: {`DROP TABLE dns_integration_clients`, `ALTER TABLE dns_integration DROP COLUMN sync_names`,
		`ALTER TABLE dns_integration DROP COLUMN enabled`},
	8: {`DROP TABLE api_tokens`},
}

// rollBackTo puts the database back as it was after the given migration, so a test can open it
// with a newer build and see the upgrade. migrate() tracks one high-water mark, not a set of
// applied versions, so every later migration has to go, not just the one under test.
func rollBackTo(t *testing.T, s *Store, version int) {
	t.Helper()
	ctx := context.Background()
	for v := len(undo) + 2; v > version; v-- {
		for _, stmt := range undo[v] {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("rolling back migration %d: %v", v, err)
			}
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version > ?`, version); err != nil {
		t.Fatal(err)
	}
}

func TestEveryMigrationCanBeRolledBack(t *testing.T) {
	files, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The first two build the tables everything else changes, and nothing rolls them back.
	if want := len(files) - 2; len(undo) != want {
		t.Errorf("%d migrations after the first two, but %d entries in undo; add the new migration's", want, len(undo))
	}
	for v := 3; v <= len(files); v++ {
		if len(undo[v]) == 0 {
			t.Errorf("no way to roll back migration %d", v)
		}
	}
}

func TestSettingsBeforeInitialize(t *testing.T) {
	s, _ := openTest(t)
	if _, err := s.Settings(context.Background()); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("err %v, want ErrNotInitialized", err)
	}
}

func TestInitializeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	created, err := s.Initialize(ctx)
	if err != nil || !created {
		t.Fatalf("first Initialize: created %v, err %v", created, err)
	}
	first, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	created, err = s.Initialize(ctx)
	if err != nil || created {
		t.Fatalf("second Initialize: created %v, err %v", created, err)
	}
	second, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.PrivateKey != second.PrivateKey || first.IPv6 != second.IPv6 {
		t.Fatal("a second Initialize changed the settings")
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("initial settings are invalid: %v", err)
	}
	if !first.IPv6.IsValid() {
		t.Fatal("no IPv6 subnet was generated")
	}
}

func TestReopenKeepsDataAndSchema(t *testing.T) {
	ctx := context.Background()
	s, path := openTest(t)
	if _, err := s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	again, err := Open(ctx, path, testSealer(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := again.Client(ctx, ByName("phone")); err != nil {
		t.Fatalf("client lost after reopening: %v", err)
	}
	var migrations int
	if err := again.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	files, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if migrations != len(files) {
		t.Fatalf("%d migrations recorded, want %d (reopening mustn't re-run them)", migrations, len(files))
	}
}

func TestSecretsAreSealedAtRest(t *testing.T) {
	ctx := context.Background()
	s, path := openTest(t)
	if _, err := s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := s.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := s.Settings(ctx)
	_ = s.Close()

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var serverKey, clientKey, psk []byte
	if err := raw.QueryRow(`SELECT private_key_enc FROM server`).Scan(&serverKey); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT private_key_enc, psk_enc FROM clients`).Scan(&clientKey, &psk); err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2][]byte{
		"server key": {serverKey, settings.PrivateKey[:]},
		"client key": {clientKey, c.PrivateKey[:]},
		"psk":        {psk, c.PresharedKey[:]},
	} {
		if bytes.Contains(pair[0], pair[1]) {
			t.Errorf("%s is stored in the clear", name)
		}
	}

	// The wrong secret key can't read the settings.
	wrong, err := Open(ctx, path, testSealer(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if _, err := wrong.Settings(ctx); !errors.Is(err, keys.ErrOpen) {
		t.Fatalf("wrong secret key: err %v, want keys.ErrOpen", err)
	}
}

func TestAddClientAllocatesAndGeneratesKeys(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	settings, _ := s.Settings(ctx)

	a, err := s.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddClient(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if a.IPv4 != netip.MustParseAddr("10.8.0.2") || b.IPv4 != netip.MustParseAddr("10.8.0.3") {
		t.Fatalf("IPv4: %s, %s", a.IPv4, b.IPv4)
	}
	if !settings.IPv6.Contains(a.IPv6) || a.IPv6.As16()[15] != 0x02 {
		t.Fatalf("IPv6 %s doesn't mirror host 2 in %s", a.IPv6, settings.IPv6)
	}
	if a.PrivateKey == nil || a.PrivateKey.PublicKey() != a.PublicKey {
		t.Fatal("the stored key pair doesn't match")
	}
	if a.PresharedKey == b.PresharedKey || a.PublicKey == b.PublicKey {
		t.Fatal("two clients share key material")
	}
	if !a.Enabled {
		t.Fatal("a new client should be enabled")
	}

	got, err := s.Client(ctx, ByName("PHONE"))
	if err != nil {
		t.Fatalf("case-insensitive lookup: %v", err)
	}
	if got.ID != a.ID || got.PresharedKey != a.PresharedKey || *got.PrivateKey != *a.PrivateKey {
		t.Fatal("the client read back differs from the one created")
	}

	all, err := s.Clients(ctx)
	if err != nil || len(all) != 2 || all[0].Name != "phone" {
		t.Fatalf("Clients() = %v, %v", all, err)
	}
}

func TestAddClientRejectsDuplicateAndInvalidNames(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	if _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddClient(ctx, "Phone"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: err %v, want ErrNameTaken", err)
	}
	if _, err := s.AddClient(ctx, "bad\nname"); err == nil {
		t.Fatal("invalid name accepted")
	}
}

func TestAddClientReusesFreedAddress(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	a, _ := s.AddClient(ctx, "a")
	if _, err := s.AddClient(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteClient(ctx, ByName("a")); err != nil {
		t.Fatal(err)
	}
	c, err := s.AddClient(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if c.IPv4 != a.IPv4 || c.IPv6 != a.IPv6 {
		t.Fatalf("got %s/%s, want the freed %s/%s", c.IPv4, c.IPv6, a.IPv4, a.IPv6)
	}
}

func TestSetEnabledAndDelete(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	if _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	c, err := s.SetEnabled(ctx, ByName("phone"), false)
	if err != nil || c.Enabled {
		t.Fatalf("pause: %+v, %v", c, err)
	}
	if got, _ := s.Client(ctx, ByName("phone")); got.Enabled {
		t.Fatal("pause wasn't saved")
	}
	if c, err = s.SetEnabled(ctx, ByName("phone"), true); err != nil || !c.Enabled {
		t.Fatalf("resume: %+v, %v", c, err)
	}
	if _, err := s.DeleteClient(ctx, ByName("phone")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Client(ctx, ByName("phone")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: err %v, want ErrNotFound", err)
	}
	if _, err := s.SetEnabled(ctx, ByName("ghost"), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown client: err %v, want ErrNotFound", err)
	}
	if _, err := s.DeleteClient(ctx, ByName("ghost")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown client: err %v, want ErrNotFound", err)
	}
}

func TestUpdateSettings(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	updated, err := s.UpdateSettings(ctx, func(st *model.Settings) error {
		st.EndpointHost = "vpn.example.com"
		st.MTU = 1412
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Settings(ctx)
	if got.EndpointHost != "vpn.example.com" || got.MTU != 1412 || updated.MTU != 1412 {
		t.Fatalf("got %+v", got)
	}

	// Invalid settings aren't saved.
	if _, err := s.UpdateSettings(ctx, func(st *model.Settings) error { st.MTU = 100; return nil }); err == nil {
		t.Fatal("invalid MTU accepted")
	}
	if got, _ := s.Settings(ctx); got.MTU != 1412 {
		t.Fatalf("a rejected update changed the MTU to %d", got.MTU)
	}

	// An error from fn aborts the update.
	boom := errors.New("boom")
	if _, err := s.UpdateSettings(ctx, func(st *model.Settings) error { st.MTU = 1300; return boom }); !errors.Is(err, boom) {
		t.Fatalf("err %v, want boom", err)
	}
	if got, _ := s.Settings(ctx); got.MTU != 1412 {
		t.Fatal("an aborted update was saved")
	}
}

func TestSubnetsCantChangeWithClients(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	change := func(st *model.Settings) error {
		st.IPv4 = netip.MustParsePrefix("10.9.0.0/24")
		return nil
	}
	if _, err := s.UpdateSettings(ctx, change); err != nil {
		t.Fatalf("without clients: %v", err)
	}
	if _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateSettings(ctx, func(st *model.Settings) error {
		st.IPv4 = netip.MustParsePrefix("10.10.0.0/24")
		return nil
	}); !errors.Is(err, ErrHasClients) {
		t.Fatalf("with a client: err %v, want ErrHasClients", err)
	}
}

func TestClientRefsAndRename(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	phone, err := s.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddClient(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Client(ctx, ByID(phone.ID)); err != nil || got.Name != "phone" {
		t.Fatalf("Client(ByID) = %+v, %v", got, err)
	}
	if _, err := s.Client(ctx, ByID("phone")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a name used as an ID: err %v, want ErrNotFound", err)
	}

	renamed, err := s.RenameClient(ctx, ByID(phone.ID), "Pixel")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Pixel" || renamed.ID != phone.ID || renamed.IPv4 != phone.IPv4 {
		t.Fatalf("renamed %+v", renamed)
	}
	// Changing only the case of its own name is fine.
	if _, err := s.RenameClient(ctx, ByName("pixel"), "PIXEL"); err != nil {
		t.Fatalf("case-only rename: %v", err)
	}
	if _, err := s.RenameClient(ctx, ByName("pixel"), "Laptop"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("rename onto another client: err %v, want ErrNameTaken", err)
	}
	if _, err := s.RenameClient(ctx, ByName("pixel"), "bad\nname"); !model.IsInvalid(err) {
		t.Fatalf("invalid name: err %v, want an InvalidError", err)
	}
	if _, err := s.RenameClient(ctx, ByName("ghost"), "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown client: err %v", err)
	}
}

func TestTimestampsCompareAsText(t *testing.T) {
	a := formatTime(time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC))
	b := formatTime(time.Date(2026, 9, 26, 12, 0, 0, 500_000_000, time.UTC))
	c := formatTime(time.Date(2026, 9, 26, 7, 0, 1, 0, time.FixedZone("EST", -5*3600)))
	if a >= b || b >= c || len(a) != len(b) {
		t.Fatalf("%s, %s, %s: want fixed-width UTC times that sort as text", a, b, c)
	}
	if _, err := time.Parse(time.RFC3339Nano, a); err != nil {
		t.Fatal(err)
	}
}

func TestAdminAllowedRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	if got, _ := s.Settings(ctx); len(got.AdminAllowed) != 0 {
		t.Fatalf("a new server has extra admin sources: %v", got.AdminAllowed)
	}
	want := []netip.Prefix{netip.MustParsePrefix("100.64.10.0/24")}
	if _, err := s.UpdateSettings(ctx, func(st *model.Settings) error { st.AdminAllowed = want; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Settings(ctx); !slices.Equal(got.AdminAllowed, want) {
		t.Fatalf("got %v, want %v", got.AdminAllowed, want)
	}
	// A public range is refused and nothing is saved.
	bad := []netip.Prefix{netip.MustParsePrefix("8.8.8.0/24")}
	if _, err := s.UpdateSettings(ctx, func(st *model.Settings) error { st.AdminAllowed = bad; return nil }); err == nil {
		t.Fatal("a public range was saved")
	}
	if got, _ := s.Settings(ctx); !slices.Equal(got.AdminAllowed, want) {
		t.Fatalf("a rejected update changed the sources to %v", got.AdminAllowed)
	}
	if _, err := s.UpdateSettings(ctx, func(st *model.Settings) error { st.AdminAllowed = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Settings(ctx); len(got.AdminAllowed) != 0 {
		t.Fatalf("clearing left %v", got.AdminAllowed)
	}
}

// A database from before the admin_allowed column exists keeps its server row when it's
// opened by a newer build, and starts with no extra sources.
func TestMigrationAddsAdminAllowedToAnExistingServer(t *testing.T) {
	ctx := context.Background()
	s, path := openTest(t)
	if _, err := s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateSettings(ctx, func(st *model.Settings) error { st.EndpointHost = "vpn.example.com"; return nil }); err != nil {
		t.Fatal(err)
	}
	rollBackTo(t, s, 2)
	_ = s.Close()

	again, err := Open(ctx, path, testSealer(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	got, err := again.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.EndpointHost != "vpn.example.com" || len(got.AdminAllowed) != 0 {
		t.Fatalf("after the upgrade: endpoint %q, sources %v", got.EndpointHost, got.AdminAllowed)
	}
	if sessions, err := again.CurrentClientSessions(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions %v, err %v, want none", sessions, err)
	}
}
