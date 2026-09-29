package service

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/wg"
)

func TestTrackConnectionsOpensAndUpdatesASession(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	fake := s.WG.(*wg.Fake)

	// No handshake yet: nothing opens.
	s.TrackConnections(ctx)
	if cs, _ := s.Client(ctx, store.ByName("phone")); cs.Session != nil {
		t.Fatalf("a session opened before any handshake: %+v", cs.Session)
	}

	ep1 := netip.MustParseAddrPort("203.0.113.5:51820")
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{
		LastHandshake: clk.t, Endpoint: ep1, ReceiveBytes: 1000, SendBytes: 500,
	})
	s.TrackConnections(ctx)
	cs, err := s.Client(ctx, store.ByName("phone"))
	if err != nil {
		t.Fatal(err)
	}
	if cs.Session == nil || cs.Session.RxBytes != 0 || cs.Session.TxBytes != 0 || cs.Session.Endpoint != ep1.String() {
		t.Fatalf("after the first handshake: %+v", cs.Session)
	}
	if got := kinds(t, s); got[len(got)-1] != "client.connected" {
		t.Fatalf("events %v, want the last one to be client.connected", got)
	}

	// More traffic, same endpoint: the session's own bytes grow, no new event.
	clk.advance(30 * time.Second)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{
		LastHandshake: clk.t, Endpoint: ep1, ReceiveBytes: 1500, SendBytes: 900,
	})
	s.TrackConnections(ctx)
	cs, _ = s.Client(ctx, store.ByName("phone"))
	if cs.Session.RxBytes != 500 || cs.Session.TxBytes != 400 {
		t.Fatalf("session bytes %+v, want 500/400", cs.Session)
	}
	if n := countKind(t, s, "client.connected"); n != 1 {
		t.Fatalf("%d client.connected events, want 1", n)
	}

	// A new endpoint: roamed, same session.
	sessionID := cs.Session.ID
	ep2 := netip.MustParseAddrPort("203.0.113.6:51820")
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{
		LastHandshake: clk.t, Endpoint: ep2, ReceiveBytes: 1600, SendBytes: 950,
	})
	s.TrackConnections(ctx)
	cs, _ = s.Client(ctx, store.ByName("phone"))
	if cs.Session.ID != sessionID {
		t.Fatal("roaming opened a new session instead of keeping the old one")
	}
	if cs.Session.Endpoint != ep2.String() || cs.Session.RxBytes != 600 || cs.Session.TxBytes != 450 {
		t.Fatalf("after roaming: %+v", cs.Session)
	}
	if n := countKind(t, s, "client.roamed"); n != 1 {
		t.Fatalf("%d client.roamed events, want 1", n)
	}

	// Pausing removes the peer: the session closes with its final bytes.
	if _, _, err := s.SetEnabled(ctx, store.ByName("phone"), false); err != nil {
		t.Fatal(err)
	}
	s.TrackConnections(ctx)
	cs, _ = s.Client(ctx, store.ByName("phone"))
	if cs.Session != nil {
		t.Fatalf("the session is still open after pausing: %+v", cs.Session)
	}
	events, err := s.Events(ctx, store.EventFilter{Category: CategoryConnection})
	if err != nil {
		t.Fatal(err)
	}
	disconnect := events[0]
	if disconnect.Kind != "client.disconnected" || disconnect.Data["receive_bytes"] != "600" ||
		disconnect.Data["send_bytes"] != "450" {
		t.Fatalf("disconnected event %+v", disconnect)
	}

	// Resuming, then a fresh handshake, starts a new session from a fresh baseline.
	if _, _, err := s.SetEnabled(ctx, store.ByName("phone"), true); err != nil {
		t.Fatal(err)
	}
	s.TrackConnections(ctx)
	if cs, _ := s.Client(ctx, store.ByName("phone")); cs.Session != nil {
		t.Fatalf("a session opened before the resumed client re-handshakes: %+v", cs.Session)
	}
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{
		LastHandshake: clk.t, Endpoint: ep1, ReceiveBytes: 0, SendBytes: 0,
	})
	s.TrackConnections(ctx)
	cs, _ = s.Client(ctx, store.ByName("phone"))
	if cs.Session == nil || cs.Session.ID == sessionID {
		t.Fatalf("resuming didn't start a new session: %+v", cs.Session)
	}
	if n := countKind(t, s, "client.connected"); n != 2 {
		t.Fatalf("%d client.connected events, want 2", n)
	}
}

func TestTrackConnectionsHandlesACounterReset(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	fake := s.WG.(*wg.Fake)
	ep := netip.MustParseAddrPort("203.0.113.5:51820")

	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 5000, SendBytes: 3000})
	s.TrackConnections(ctx)
	before, _ := s.Client(ctx, store.ByName("phone"))
	firstSession := before.Session.ID

	// The interface (or peer) was recreated without a pause: counters go backward.
	// The old session closes and a new one opens in the same tick, since the
	// handshake is still fresh.
	clk.advance(time.Second)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 100, SendBytes: 50})
	s.TrackConnections(ctx)

	after, err := s.Client(ctx, store.ByName("phone"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Session == nil || after.Session.ID == firstSession {
		t.Fatalf("a counter reset didn't start a new session: %+v", after.Session)
	}
	if after.Session.BaselineRx != 100 || after.Session.BaselineTx != 50 || after.Session.RxBytes != 0 {
		t.Fatalf("new session after reset: %+v", after.Session)
	}
	got := kinds(t, s)
	if n := len(got); n < 3 || got[n-3] != "client.connected" || got[n-2] != "client.disconnected" ||
		got[n-1] != "client.connected" {
		t.Fatalf("events %v, want ...connected, disconnected, connected", got)
	}
}

// A real device turning its tunnel off looks the same as it going quiet: the peer
// stays configured, and the handshake just stops advancing. The session tracker must
// treat that idle gap as a disconnect too, or "this session" never resets when someone
// actually reconnects (reported after real-world testing, 2026-09-28).
func TestTrackConnectionsClosesAnIdleSessionWithoutFlapping(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	fake := s.WG.(*wg.Fake)
	ep := netip.MustParseAddrPort("203.0.113.5:51820")

	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 1000, SendBytes: 500})
	s.TrackConnections(ctx)
	if cs, _ := s.Client(ctx, store.ByName("phone")); cs.Session == nil {
		t.Fatal("no session after the first handshake")
	}

	// The client's app is switched off: no new handshake, ever. Once the existing
	// one is older than OnlineWithin, the session closes on its own.
	clk.advance(OnlineWithin + time.Second)
	s.TrackConnections(ctx)
	cs, err := s.Client(ctx, store.ByName("phone"))
	if err != nil {
		t.Fatal(err)
	}
	if cs.Session != nil {
		t.Fatalf("the session is still open after going idle: %+v", cs.Session)
	}
	if n := countKind(t, s, "client.disconnected"); n != 1 {
		t.Fatalf("%d client.disconnected events, want 1", n)
	}

	// Ticking again with nothing new must not flap: the stale handshake alone can't
	// reopen a session.
	for range 3 {
		clk.advance(5 * time.Second)
		s.TrackConnections(ctx)
	}
	if cs, _ := s.Client(ctx, store.ByName("phone")); cs.Session != nil {
		t.Fatalf("a stale handshake reopened a session: %+v", cs.Session)
	}
	if n := countKind(t, s, "client.connected"); n != 1 {
		t.Fatalf("%d client.connected events after flapping ticks, want 1", n)
	}
	if n := countKind(t, s, "client.disconnected"); n != 1 {
		t.Fatalf("%d client.disconnected events after flapping ticks, want 1", n)
	}

	// The client's app is switched back on: a genuinely new handshake starts a fresh
	// session, with "this session" reset to zero.
	clk.advance(time.Minute)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 9000, SendBytes: 7000})
	s.TrackConnections(ctx)
	cs, _ = s.Client(ctx, store.ByName("phone"))
	if cs.Session == nil || cs.Session.RxBytes != 0 || cs.Session.TxBytes != 0 {
		t.Fatalf("reconnecting didn't start a fresh session: %+v", cs.Session)
	}
	if n := countKind(t, s, "client.connected"); n != 2 {
		t.Fatalf("%d client.connected events, want 2", n)
	}
}

func countKind(t *testing.T, s *Service, kind string) int {
	t.Helper()
	n := 0
	for _, k := range kinds(t, s) {
		if k == kind {
			n++
		}
	}
	return n
}
