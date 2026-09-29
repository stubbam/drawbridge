package service

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/wg"
)

func TestSampleTrafficAccumulatesAndFlushesAtBucketBoundary(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	fake := s.WG.(*wg.Fake)
	ep := netip.MustParseAddrPort("203.0.113.5:51820")
	t0 := clk.t.Truncate(time.Minute)

	// Two ticks inside the same minute: no flush yet, only accumulation.
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 1000, SendBytes: 500})
	s.TrackConnections(ctx) // first observation: baselines only, no delta yet
	clk.advance(20 * time.Second)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 1300, SendBytes: 650})
	s.TrackConnections(ctx) // +300/+150
	clk.advance(20 * time.Second)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 1600, SendBytes: 800})
	s.TrackConnections(ctx) // +300/+150 -> pending 600/300, still bucket t0

	if got, err := s.Store.ClientTraffic(ctx, phone.ID, store.ResolutionRaw, t0); err != nil || len(got) != 0 {
		t.Fatalf("before crossing a bucket boundary: %v, err %v, want no rows yet", got, err)
	}

	// A tick in the next minute flushes t0's accumulated 600/300, then starts a new bucket.
	clk.advance(25 * time.Second) // now t0+65s, in the next minute
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 1700, SendBytes: 850})
	s.TrackConnections(ctx) // +100/+50, into the new bucket

	got, err := s.Store.ClientTraffic(ctx, phone.ID, store.ResolutionRaw, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].BucketStart.Equal(t0) || got[0].RxBytes != 600 || got[0].TxBytes != 300 {
		t.Fatalf("got %+v, want one flushed bucket at %v with 600/300", got, t0)
	}

	// Crossing a second boundary flushes the second bucket (100/50) too.
	clk.advance(65 * time.Second)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 1750, SendBytes: 875})
	s.TrackConnections(ctx)

	got, err = s.Store.ClientTraffic(ctx, phone.ID, store.ResolutionRaw, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].RxBytes != 100 || got[1].TxBytes != 50 {
		t.Fatalf("got %+v, want a second bucket of 100/50", got)
	}
}

func TestSampleTrafficDetectsAResetWithoutLosingAlreadyAccumulatedBytes(t *testing.T) {
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
	clk.advance(10 * time.Second)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 1400, SendBytes: 700})
	s.TrackConnections(ctx) // pending: +400/+200

	st := s.trafficBuf.clients[phone.ID]
	if st.pendingRx != 400 || st.pendingTx != 200 {
		t.Fatalf("pending before the reset: %+v", st)
	}

	// The peer is re-added (a pause/resume, or the interface recreated): WireGuard's own
	// counters go back to 0, lower than what was last observed. The already-accumulated
	// pending bytes must not be discarded or zeroed; only the baseline moves.
	clk.advance(5 * time.Second)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 0, SendBytes: 0})
	s.TrackConnections(ctx)

	st = s.trafficBuf.clients[phone.ID]
	if st.pendingRx != 400 || st.pendingTx != 200 {
		t.Fatalf("pending after the reset: %+v, want unchanged (400/200)", st)
	}
	if st.lastRx != 0 || st.lastTx != 0 {
		t.Fatalf("baseline after the reset: %+v, want rebased to 0/0", st)
	}

	// A further tick adds a delta relative to the new baseline, not a huge/negative one.
	clk.advance(5 * time.Second)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 50, SendBytes: 25})
	s.TrackConnections(ctx)
	st = s.trafficBuf.clients[phone.ID]
	if st.pendingRx != 450 || st.pendingTx != 225 {
		t.Fatalf("pending after a post-reset tick: %+v, want 450/225", st)
	}
}

func TestSampleTrafficDropsBufferEntriesForDeletedClients(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	fake := s.WG.(*wg.Fake)
	ep := netip.MustParseAddrPort("203.0.113.5:51820")

	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 100, SendBytes: 50})
	s.TrackConnections(ctx)
	if _, ok := s.trafficBuf.clients[phone.ID]; !ok {
		t.Fatal("no buffer entry after the first tick")
	}

	if _, _, err := s.DeleteClient(ctx, store.ByName("phone")); err != nil {
		t.Fatal(err)
	}
	s.TrackConnections(ctx)
	if _, ok := s.trafficBuf.clients[phone.ID]; ok {
		t.Fatal("the deleted client's buffer entry is still there")
	}
}

func TestSampleTrafficLosesOnlyTheCurrentBucketOnARestart(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	fake := s.WG.(*wg.Fake)
	ep := netip.MustParseAddrPort("203.0.113.5:51820")
	t0 := clk.t.Truncate(time.Minute)

	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 1000, SendBytes: 500})
	s.TrackConnections(ctx)
	clk.advance(10 * time.Second)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 1300, SendBytes: 650})
	s.TrackConnections(ctx) // pending 300/150, never flushed

	// A restart is a fresh Service (a fresh in-memory buffer) against the same store, the
	// way the daemon would come back up. The unflushed 300/150 is gone, as expected —
	// unlike client_sessions, this isn't authoritative data.
	restarted := &Service{Store: s.Store, WG: s.WG, Log: s.Log, Now: clk.now}
	if got, err := restarted.Store.ClientTraffic(ctx, phone.ID, store.ResolutionRaw, t0); err != nil || len(got) != 0 {
		t.Fatalf("got %v, err %v, want nothing (the mid-bucket accumulation was never written)", got, err)
	}

	// But a bucket that already crossed its boundary and flushed survives the restart,
	// because it was already committed to the store.
	clk.advance(55 * time.Second) // into the next minute
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t, Endpoint: ep, ReceiveBytes: 1400, SendBytes: 700})
	s.TrackConnections(ctx) // flushes t0's 300/150

	restarted = &Service{Store: s.Store, WG: s.WG, Log: s.Log, Now: clk.now}
	got, err := restarted.Store.ClientTraffic(ctx, phone.ID, store.ResolutionRaw, t0)
	if err != nil || len(got) != 1 || got[0].RxBytes != 300 || got[0].TxBytes != 150 {
		t.Fatalf("got %v, err %v, want the flushed 300/150 bucket to survive", got, err)
	}
}

func TestTrafficRetentionRollsUpThenPrunes(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))

	old := clk.t.Add(-72 * time.Hour).Truncate(time.Hour)
	recent := clk.t.Add(-time.Hour).Truncate(time.Hour)
	err := s.Store.InsertTraffic(ctx, []store.TrafficSample{
		{ClientID: phone.ID, Resolution: store.ResolutionRaw, BucketStart: old, RxBytes: 100, TxBytes: 10},
		{ClientID: phone.ID, Resolution: store.ResolutionRaw, BucketStart: old.Add(30 * time.Minute), RxBytes: 200, TxBytes: 20},
		{ClientID: phone.ID, Resolution: store.ResolutionRaw, BucketStart: recent, RxBytes: 999, TxBytes: 999},
	})
	if err != nil {
		t.Fatal(err)
	}

	s.TrafficRetention(ctx) // default retention: raw kept 48h, hourly kept 90d

	rawLeft, err := s.Store.ClientTraffic(ctx, phone.ID, store.ResolutionRaw, old.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rawLeft) != 1 || rawLeft[0].RxBytes != 999 {
		t.Fatalf("raw rows left: %+v, want only the recent (unrolled) one", rawLeft)
	}

	hourly, err := s.Store.ClientTraffic(ctx, phone.ID, store.ResolutionHourly, old.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(hourly) != 2 {
		t.Fatalf("hourly view: %+v, want the rolled-up hour plus the recent unrolled one", hourly)
	}
	if !hourly[0].BucketStart.Equal(old) || hourly[0].RxBytes != 300 || hourly[0].TxBytes != 30 {
		t.Fatalf("rolled-up hour: %+v, want 300/30 at %v", hourly[0], old)
	}
}
