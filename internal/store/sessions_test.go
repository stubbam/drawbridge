package store

import (
	"context"
	"testing"
)

func TestClientSessionRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	c, err := s.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}

	if sessions, err := s.CurrentClientSessions(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions %v, err %v, want none", sessions, err)
	}

	cs, err := s.OpenClientSession(ctx, c.ID, "203.0.113.5:51820", 1000, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if cs.ID == "" || cs.ClientID != c.ID || cs.BaselineRx != 1000 || cs.BaselineTx != 2000 {
		t.Fatalf("opened %+v", cs)
	}

	sessions, err := s.CurrentClientSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := sessions[c.ID]; !ok || got.ID != cs.ID || got.Endpoint != "203.0.113.5:51820" {
		t.Fatalf("open sessions %v", sessions)
	}

	if err := s.UpdateClientSession(ctx, cs.ID, "203.0.113.6:51820", 500, 700); err != nil {
		t.Fatal(err)
	}
	sessions, err = s.CurrentClientSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessions[c.ID]; got.Endpoint != "203.0.113.6:51820" || got.RxBytes != 500 || got.TxBytes != 700 {
		t.Fatalf("after update: %+v", got)
	}

	if err := s.CloseClientSession(ctx, cs.ID, 900, 1200); err != nil {
		t.Fatal(err)
	}
	if sessions, err := s.CurrentClientSessions(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions %v, err %v, want none after closing", sessions, err)
	}

	// Closing again, or updating a closed session, changes nothing: both target only
	// WHERE ended_at IS NULL.
	if err := s.CloseClientSession(ctx, cs.ID, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateClientSession(ctx, cs.ID, "elsewhere:1", 1, 1); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyOneOpenClientSessionAtATime(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	c, err := s.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenClientSession(ctx, c.ID, "a:1", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenClientSession(ctx, c.ID, "b:1", 0, 0); err == nil {
		t.Fatal("a second open session for the same client was accepted")
	}
}

// A database from before client_sessions exists keeps its data when a newer build opens
// it, and starts with no sessions (mirrors TestMigrationAddsAdminAllowedToAnExistingServer).
func TestMigrationAddsClientSessionsToAnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	s, path := openTest(t)
	if _, err := s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	rollBackTo(t, s, 3)
	_ = s.Close()

	again, err := Open(ctx, path, testSealer(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := again.Client(ctx, ByName("phone")); err != nil {
		t.Fatalf("client lost after the upgrade: %v", err)
	}
	if sessions, err := again.CurrentClientSessions(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions %v, err %v, want none", sessions, err)
	}
}
