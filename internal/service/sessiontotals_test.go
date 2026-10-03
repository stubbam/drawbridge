package service

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/wg"
)

// savedSession is the open session as the database holds it, which isn't what a reader sees.
func savedSession(t *testing.T, s *Service, clientID string) store.ClientSession {
	t.Helper()
	all, err := s.Store.CurrentClientSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := all[clientID]
	if !ok {
		t.Fatal("no open session in the database")
	}
	return sess
}

func TestSessionBytesAreSavedOncePerFlushNotOncePerPoll(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	clk.t = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) // on a bucket boundary
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	fake := s.WG.(*wg.Fake)
	ep := netip.MustParseAddrPort("203.0.113.5:51820")
	poll := func(rx, tx int64, ep netip.AddrPort) {
		t.Helper()
		fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: rx, SendBytes: tx})
		s.TrackConnections(ctx)
	}
	live := func() store.ClientSession {
		t.Helper()
		cs, err := s.Client(ctx, store.ByName("phone"))
		if err != nil || cs.Session == nil {
			t.Fatalf("no open session: %v", err)
		}
		return *cs.Session
	}

	poll(1000, 500, ep) // opens the session
	// Five more polls inside the minute. Each moves bytes, and none is a write.
	for i := int64(1); i <= 5; i++ {
		clk.advance(5 * time.Second)
		poll(1000+i*100, 500+i*40, ep)
	}
	if got := live(); got.RxBytes != 500 || got.TxBytes != 200 {
		t.Fatalf("a reader sees %d/%d, want this poll's 500/200", got.RxBytes, got.TxBytes)
	}
	history, err := s.ClientSessionHistory(ctx, store.ByName("phone"), time.Time{}, 10)
	if err != nil || len(history) != 1 || history[0].RxBytes != 500 || history[0].TxBytes != 200 {
		t.Fatalf("the history shows %+v (err %v), want the open session at 500/200", history, err)
	}
	if got := savedSession(t, s, phone.ID); got.RxBytes != 0 || got.TxBytes != 0 {
		t.Fatalf("the database has %d/%d after five polls, want it untouched until the flush", got.RxBytes, got.TxBytes)
	}

	// The poll that crosses the minute flushes the session as of the poll before it.
	clk.advance(30 * time.Second)
	poll(1900, 800, ep)
	clk.advance(30 * time.Second) // 12:01:00 and a bit: the next bucket
	poll(2000, 900, ep)
	if got := savedSession(t, s, phone.ID); got.RxBytes != 900 || got.TxBytes != 300 {
		t.Fatalf("the database has %d/%d after the flush, want the previous poll's 900/300", got.RxBytes, got.TxBytes)
	}
	if got := live(); got.RxBytes != 1000 || got.TxBytes != 400 {
		t.Fatalf("a reader sees %d/%d, want this poll's 1000/400", got.RxBytes, got.TxBytes)
	}

	// A roam is saved at once, and it isn't announced again by a restart.
	ep2 := netip.MustParseAddrPort("203.0.113.6:51820")
	clk.advance(5 * time.Second)
	poll(2100, 940, ep2)
	if got := savedSession(t, s, phone.ID); got.Endpoint != ep2.String() {
		t.Fatalf("the database has endpoint %s after a roam, want %s", got.Endpoint, ep2)
	}
	restarted := &Service{Store: s.Store, WG: s.WG, Log: s.Log, Now: clk.now}
	cs, err := restarted.Client(ctx, store.ByName("phone"))
	if err != nil || cs.Session == nil || cs.Session.Endpoint != ep2.String() {
		t.Fatalf("after a restart: %+v (err %v), want the session on %s", cs.Session, err, ep2)
	}
	// The restarted daemon's reader sees what was saved until its first poll: here, the roam's
	// own write, which carried the bytes of that poll.
	if cs.Session.RxBytes != 1100 {
		t.Fatalf("after a restart, before a poll: %d bytes received, want the saved 1100", cs.Session.RxBytes)
	}
	clk.advance(5 * time.Second)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep2, ReceiveBytes: 2200, SendBytes: 980})
	restarted.TrackConnections(ctx)
	if cs, _ = restarted.Client(ctx, store.ByName("phone")); cs.Session.RxBytes != 1200 {
		t.Fatalf("after the first poll of a restarted daemon: %d bytes received, want 1200", cs.Session.RxBytes)
	}
	if n := countKind(t, s, "client.roamed"); n != 1 {
		t.Fatalf("%d client.roamed events, want 1: a restart announced the roam again", n)
	}
	if n := countKind(t, s, "client.connected"); n != 1 {
		t.Fatalf("%d client.connected events, want 1", n)
	}

	// Pausing ends the session with the bytes of the last poll, not the last flush's.
	if _, _, err := s.SetEnabled(ctx, store.ByName("phone"), false); err != nil {
		t.Fatal(err)
	}
	restarted.TrackConnections(ctx)
	events, _ := s.Events(ctx, store.EventFilter{Category: CategoryConnection})
	if d := events[0]; d.Kind != "client.disconnected" || d.Data["receive_bytes"] != "1200" || d.Data["send_bytes"] != "480" {
		t.Fatalf("disconnected event %+v, want 1200 received and 480 sent", d)
	}
	history, _ = s.ClientSessionHistory(ctx, store.ByName("phone"), time.Time{}, 10)
	if len(history) != 1 || history[0].EndedAt == nil || history[0].RxBytes != 1200 || history[0].TxBytes != 480 {
		t.Fatalf("the closed session is %+v, want it ended at 1200/480", history)
	}
	if n := len(restarted.sessionLive.open); n != 0 {
		t.Fatalf("%d sessions still tracked in memory after the close", n)
	}
}

func TestDeletingAClientForgetsItsSessionTotals(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	s.WG.(*wg.Fake).SetHandshake("wg0", phone.PublicKey, wg.Peer{
		LastHandshake: clk.t, Endpoint: netip.MustParseAddrPort("203.0.113.5:51820"), ReceiveBytes: 10, SendBytes: 10,
	})
	s.TrackConnections(ctx)
	if len(s.sessionLive.open) != 1 {
		t.Fatalf("%d sessions tracked, want 1", len(s.sessionLive.open))
	}
	// Deleting the client cascades to its sessions in the database.
	if _, _, err := s.DeleteClient(ctx, store.ByName("phone")); err != nil {
		t.Fatal(err)
	}
	s.TrackConnections(ctx)
	if n := len(s.sessionLive.open); n != 0 {
		t.Fatalf("%d sessions still tracked for a deleted client", n)
	}
}
