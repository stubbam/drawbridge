package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestSetupToken(t *testing.T) {
	ctx := context.Background()
	s, path := openTest(t)

	token, err := s.EnsureSetupToken(ctx, "FIRSTCANDIDATE")
	if err != nil || token != "FIRSTCANDIDATE" {
		t.Fatalf("EnsureSetupToken = %q, %v", token, err)
	}
	// The token stays the same until it's used.
	if token, _ = s.EnsureSetupToken(ctx, "SECOND"); token != "FIRSTCANDIDATE" {
		t.Fatalf("second EnsureSetupToken = %q, want the first token", token)
	}
	// It's sealed at rest.
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT token_enc FROM setup_token`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("FIRSTCANDIDATE")) {
		t.Fatal("the setup token is stored in the clear")
	}

	if _, err := s.CompleteSetup(ctx, "WRONG", "admin", "hash"); !errors.Is(err, ErrBadSetupToken) {
		t.Fatalf("wrong token: err %v, want ErrBadSetupToken", err)
	}
	if has, _ := s.HasUsers(ctx); has {
		t.Fatal("a wrong token created an account")
	}
	u, err := s.CompleteSetup(ctx, "FIRSTCANDIDATE", "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == "" || u.Username != "admin" || u.PasswordHash != "hash" || u.CreatedAt.IsZero() {
		t.Fatalf("user %+v", u)
	}
	if _, err := s.CompleteSetup(ctx, "FIRSTCANDIDATE", "other", "hash"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("setup again: err %v, want ErrSetupDone", err)
	}
	if _, err := s.EnsureSetupToken(ctx, "X"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("token after setup: err %v, want ErrSetupDone", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM setup_token`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d setup tokens left after setup, want 0 (err %v)", n, err)
	}

	// A different secret key can't read the token.
	_ = s.Close()
	other, err := Open(ctx, path, testSealer(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.UserByName(ctx, "ADMIN"); err != nil {
		t.Fatalf("usernames should match case-insensitively: %v", err)
	}
}

func TestCreateUserAndPasswords(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	if _, err := s.OnlyUser(ctx); !errors.Is(err, ErrNoUser) {
		t.Fatalf("OnlyUser with no account: err %v, want ErrNoUser", err)
	}
	if _, err := s.EnsureSetupToken(ctx, "TOKEN"); err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, "admin", "hash1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureSetupToken(ctx, "X"); !errors.Is(err, ErrSetupDone) {
		t.Fatal("creating the account from the CLI didn't end setup")
	}
	if _, err := s.CreateUser(ctx, "second", "hash"); !errors.Is(err, ErrUserExists) {
		t.Fatalf("a second account: err %v, want ErrUserExists", err)
	}
	if only, err := s.OnlyUser(ctx); err != nil || only.ID != u.ID {
		t.Fatalf("OnlyUser = %+v, %v", only, err)
	}
	if _, err := s.UserByName(ctx, "nobody"); !errors.Is(err, ErrNoUser) {
		t.Fatalf("unknown user: err %v, want ErrNoUser", err)
	}

	if err := s.RecordLogin(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, id := range []string{"keep", "drop"} {
		if err := s.CreateSession(ctx, Session{ID: id, TokenHash: []byte(id), UserID: u.ID,
			CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetPassword(ctx, u.ID, "hash2", "keep"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.UserByName(ctx, "admin")
	if got.PasswordHash != "hash2" || got.LastLoginAt.IsZero() {
		t.Fatalf("after SetPassword: %+v", got)
	}
	sessions, _ := s.Sessions(ctx, u.ID)
	if len(sessions) != 1 || sessions[0].ID != "keep" {
		t.Fatalf("sessions after a password change: %+v, want only the one kept", sessions)
	}
	if err := s.SetPassword(ctx, "ghost", "h", ""); !errors.Is(err, ErrNoUser) {
		t.Fatalf("SetPassword for an unknown user: err %v", err)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	u, err := s.CreateUser(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	sess := Session{ID: "s1", TokenHash: []byte("token-hash-1"), UserID: u.ID, CreatedAt: t0,
		LastSeenAt: t0, ExpiresAt: t0.Add(12 * time.Hour), IP: "192.168.4.20", UserAgent: "Firefox"}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, gotUser, err := s.SessionByToken(ctx, []byte("token-hash-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "s1" || got.IP != "192.168.4.20" || !got.ExpiresAt.Equal(sess.ExpiresAt) || gotUser.ID != u.ID {
		t.Fatalf("SessionByToken = %+v, %+v", got, gotUser)
	}
	if _, _, err := s.SessionByToken(ctx, []byte("other")); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown token: err %v, want ErrNoSession", err)
	}

	if err := s.TouchSession(ctx, "s1", t0.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.SessionByToken(ctx, []byte("token-hash-1"))
	if !got.LastSeenAt.Equal(t0.Add(30 * time.Minute)) {
		t.Fatalf("LastSeenAt = %v after a touch", got.LastSeenAt)
	}

	// Pruning: s1 was seen at 12:30; s2 is past its absolute limit.
	if err := s.CreateSession(ctx, Session{ID: "s2", TokenHash: []byte("h2"), UserID: u.ID,
		CreatedAt: t0, LastSeenAt: t0.Add(2 * time.Hour), ExpiresAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneSessions(ctx, t0.Add(90*time.Minute), t0.Add(29*time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("PruneSessions deleted %d (err %v), want 1", n, err)
	}
	n, _ = s.PruneSessions(ctx, t0.Add(90*time.Minute), t0.Add(31*time.Minute))
	if n != 1 {
		t.Fatalf("PruneSessions deleted %d idle sessions, want 1", n)
	}

	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "someone-else", "s1"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("deleting another account's session: err %v, want ErrNoSession", err)
	}
	if err := s.DeleteSession(ctx, u.ID, "s1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Sessions(ctx, u.ID); len(list) != 0 {
		t.Fatalf("sessions after delete: %+v", list)
	}
}

func TestEvents(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	t0 := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for i, e := range []Event{
		{Time: t0, Kind: "client.added", Category: "admin", Actor: "admin", Via: "web",
			SourceIP: "192.168.4.20", ClientID: "c1", ClientName: "phone", Data: map[string]string{"ipv4": "10.8.0.2"}},
		{Kind: "tunnel.drift_corrected", Category: "system", Actor: "drawbridge", Via: "system"},
		{Kind: "client.paused", Category: "admin", Actor: "root", Via: "cli", ClientID: "c1", ClientName: "phone"},
	} {
		if err := s.AddEvent(ctx, e); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	all, err := s.Events(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Kind != "client.paused" || all[2].Kind != "client.added" {
		t.Fatalf("events %+v, want three, newest first", all)
	}
	first := all[2]
	if !first.Time.Equal(t0) || first.Data["ipv4"] != "10.8.0.2" || first.SourceIP != "192.168.4.20" ||
		first.ClientName != "phone" {
		t.Fatalf("first event %+v", first)
	}
	if all[1].Time.IsZero() || all[1].ClientID != "" {
		t.Fatalf("system event %+v: want a time and no client", all[1])
	}

	for name, tc := range map[string]struct {
		f    EventFilter
		want int
	}{
		"client":   {EventFilter{ClientID: "c1"}, 2},
		"category": {EventFilter{Category: "system"}, 1},
		"limit":    {EventFilter{Limit: 2}, 2},
		"before":   {EventFilter{Before: all[1].ID}, 1},
	} {
		got, err := s.Events(ctx, tc.f)
		if err != nil || len(got) != tc.want {
			t.Errorf("%s: %d events (err %v), want %d", name, len(got), err, tc.want)
		}
	}
}
