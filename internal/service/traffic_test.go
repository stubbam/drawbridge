package service

import (
	"context"
	"errors"
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

// livePeers is a two-client fixture for the live samples: each set call is one poll's worth of
// counters, and tick runs TrackConnections on them.
type livePeers struct {
	s    *Service
	clk  *clock
	ctx  context.Context
	fake *wg.Fake
}

func (p livePeers) set(c ClientStatus, rx, tx int64) {
	p.fake.SetHandshake("wg0", c.PublicKey, wg.Peer{
		LastHandshake: p.clk.t, Endpoint: netip.MustParseAddrPort("203.0.113.5:51820"),
		ReceiveBytes: rx, SendBytes: tx,
	})
}

func (p livePeers) tick(after time.Duration) {
	p.clk.advance(after)
	p.s.TrackConnections(p.ctx)
}

func TestLiveTrafficHasASamplePerPollForAClientAndForTheTotal(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	for _, name := range []string{"phone", "laptop"} {
		if _, _, err := s.AddClient(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	laptop, _ := s.Client(ctx, store.ByName("laptop"))
	p := livePeers{s: s, clk: clk, ctx: ctx, fake: s.WG.(*wg.Fake)}
	t0 := clk.t

	p.set(phone, 1000, 500)
	p.set(laptop, 0, 0)
	p.tick(0) // the first observation baselines: nothing moved yet
	p.set(phone, 1300, 650)
	p.set(laptop, 40, 10)
	p.tick(5 * time.Second)
	p.set(phone, 1300, 650) // the phone is idle this poll
	p.set(laptop, 100, 30)
	p.tick(5 * time.Second)
	p.set(phone, 10, 5) // the phone's counters went backward: re-added
	p.set(laptop, 100, 30)
	p.tick(5 * time.Second)
	p.set(phone, 60, 25)
	p.set(laptop, 100, 30)
	p.tick(5 * time.Second)

	type point struct {
		at     time.Time
		rx, tx int64
	}
	points := func(ts TrafficSamples, err error) []point {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var out []point
		for _, sm := range ts.Samples {
			out = append(out, point{sm.BucketStart, sm.RxBytes, sm.TxBytes})
		}
		return out
	}
	at := func(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }
	check := func(what string, got, want []point) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
		for i := range want {
			if !got[i].at.Equal(want[i].at) || got[i].rx != want[i].rx || got[i].tx != want[i].tx {
				t.Fatalf("%s: got %v, want %v", what, got, want)
			}
		}
	}

	// One sample per poll, oldest first, and a poll in which the client moved nothing (or was
	// only rebaselined) is a zero rather than a gap, so the chart doesn't draw a line across it.
	check("phone", points(s.ClientTraffic(ctx, store.ByName("phone"), ResolutionLive, time.Minute)),
		[]point{{at(0), 0, 0}, {at(5), 300, 150}, {at(10), 0, 0}, {at(15), 0, 0}, {at(20), 50, 20}})
	check("laptop", points(s.ClientTraffic(ctx, store.ByName("laptop"), ResolutionLive, time.Minute)),
		[]point{{at(0), 0, 0}, {at(5), 40, 10}, {at(10), 60, 20}, {at(15), 0, 0}, {at(20), 0, 0}})
	check("total", points(s.TotalTraffic(ctx, ResolutionLive, time.Minute)),
		[]point{{at(0), 0, 0}, {at(5), 340, 160}, {at(10), 60, 20}, {at(15), 0, 0}, {at(20), 50, 20}})

	// The stored buckets are untouched by any of this: nothing was flushed yet.
	if got, err := s.Store.TotalTraffic(ctx, store.ResolutionRaw, t0.Add(-time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("stored raw buckets %v, err %v, want none: live samples never reach the database", got, err)
	}

	if _, err := s.ClientTraffic(ctx, store.ByName("no-such-client"), ResolutionLive, time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown client: err %v, want not found", err)
	}
}

func TestLiveTrafficKeepsOnlyTheRecentPolls(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	p := livePeers{s: s, clk: clk, ctx: ctx, fake: s.WG.(*wg.Fake)}

	p.set(phone, 0, 0)
	p.tick(0)
	p.set(phone, 100, 50)
	p.tick(5 * time.Second)

	// The range is a window ending now: polls older than a minute are outside the 1 minute
	// range, though they're still in memory.
	p.set(phone, 200, 100)
	p.tick(70 * time.Second)
	got, err := s.ClientTraffic(ctx, store.ByName("phone"), ResolutionLive, time.Minute)
	if err != nil || len(got.Samples) != 1 || got.Samples[0].RxBytes != 100 || got.Samples[0].TxBytes != 50 {
		t.Fatalf("1 minute range: %+v, err %v, want only the newest poll (100/50)", got, err)
	}
	if n := len(s.live.ticks); n != 3 {
		t.Fatalf("%d polls in memory, want 3 (all within %v)", n, liveRetention)
	}

	// Past the retention, old polls are dropped, so memory stays bounded however long it runs.
	p.set(phone, 300, 150)
	p.tick(liveRetention + time.Second)
	if n := len(s.live.ticks); n != 1 {
		t.Fatalf("%d polls in memory after %v, want only the newest", n, liveRetention)
	}
}

func TestLiveTrafficIsSafeToReadWhileThePollerWrites(t *testing.T) {
	var l liveTraffic
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 2000 {
			l.add(liveTick{at: base.Add(time.Duration(i) * time.Second), clients: map[string]liveBytes{"c": {rx: 1, tx: 1}}})
		}
	}()
	// The race detector (make test-go) is what fails this if the lock goes missing.
	for {
		select {
		case <-done:
			return
		default:
			_ = l.samples("", base)
			_ = l.samples("c", base)
		}
	}
}

func TestClientsTrafficLiveHasEveryClientAtEveryPoll(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	for _, name := range []string{"zed", "Alpha"} {
		if _, _, err := s.AddClient(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	zed, _ := s.Client(ctx, store.ByName("zed"))
	alpha, _ := s.Client(ctx, store.ByName("Alpha"))
	p := livePeers{s: s, clk: clk, ctx: ctx, fake: s.WG.(*wg.Fake)}

	p.set(zed, 0, 0)
	p.set(alpha, 0, 0)
	p.tick(0)
	p.set(zed, 100, 40)
	p.tick(5 * time.Second)
	p.set(alpha, 10, 5)
	p.tick(5 * time.Second)

	h, err := s.ClientsTraffic(ctx, ResolutionLive, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if h.Step != DefaultTrackInterval || !h.Until.Equal(clk.t) {
		t.Fatalf("step %v until %v, want %v and %v", h.Step, h.Until, DefaultTrackInterval, clk.t)
	}
	if len(h.Series) != 2 || h.Series[0].Client.Name != "Alpha" || h.Series[1].Client.Name != "zed" {
		t.Fatalf("series %+v, want Alpha then zed (by name, ignoring case)", h.Series)
	}
	rxtx := func(sr TrafficSeries) [][2]int64 {
		var out [][2]int64
		for _, sm := range sr.Samples {
			out = append(out, [2]int64{sm.RxBytes, sm.TxBytes})
		}
		return out
	}
	// Both clients have a sample at all three polls, zero where nothing moved.
	if got := rxtx(h.Series[0]); len(got) != 3 || got[0] != [2]int64{} || got[1] != [2]int64{} || got[2] != [2]int64{10, 5} {
		t.Fatalf("Alpha %v", got)
	}
	if got := rxtx(h.Series[1]); len(got) != 3 || got[0] != [2]int64{} || got[1] != [2]int64{100, 40} || got[2] != [2]int64{} {
		t.Fatalf("zed %v", got)
	}
	for i := range h.Series[0].Samples {
		if !h.Series[0].Samples[i].BucketStart.Equal(h.Series[1].Samples[i].BucketStart) {
			t.Fatalf("poll %d is at different times for the two clients", i)
		}
	}

	// The poll interval is a setting, and it sets the step.
	s.TrackInterval = 2 * time.Second
	if h, err = s.ClientsTraffic(ctx, ResolutionLive, time.Minute); err != nil || h.Step != 2*time.Second {
		t.Fatalf("step %v, err %v, want 2s", h.Step, err)
	}
}

func TestClientsTrafficStoredEndsAtTheLastSettledBucket(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	clk.t = time.Date(2026, 9, 26, 12, 34, 7, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2026, 9, 26, h, m, 0, 0, time.UTC) }
	err := s.Store.InsertTraffic(ctx, []store.TrafficSample{
		{ClientID: phone.ID, Resolution: store.ResolutionRaw, BucketStart: at(11, 20), RxBytes: 1, TxBytes: 1}, // before the hour's range
		{ClientID: phone.ID, Resolution: store.ResolutionRaw, BucketStart: at(12, 31), RxBytes: 2, TxBytes: 2},
		{ClientID: phone.ID, Resolution: store.ResolutionRaw, BucketStart: at(12, 32), RxBytes: 3, TxBytes: 3},
		{ClientID: phone.ID, Resolution: store.ResolutionRaw, BucketStart: at(12, 33), RxBytes: 4, TxBytes: 4}, // still settling
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now is 12:34:07. The 12:33 bucket ended seven seconds ago, so the poll that saves it may
	// not have run: the history stops at 12:33, leaving the bucket out rather than showing zero.
	h, err := s.ClientsTraffic(ctx, store.ResolutionRaw, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if h.Step != time.Minute || !h.Until.Equal(at(12, 33)) {
		t.Fatalf("step %v until %v, want 1m and 12:33", h.Step, h.Until)
	}
	if got := h.Series[0].Samples; len(got) != 2 || !got[0].BucketStart.Equal(at(12, 31)) || !got[1].BucketStart.Equal(at(12, 32)) {
		t.Fatalf("samples %+v, want 12:31 and 12:32 only", got)
	}

	// Past the margin, the 12:33 bucket is in.
	clk.advance(10 * time.Second)
	if h, err = s.ClientsTraffic(ctx, store.ResolutionRaw, time.Hour); err != nil || len(h.Series[0].Samples) != 3 {
		t.Fatalf("after the margin: %+v, err %v, want 12:33 too", h.Series[0].Samples, err)
	}

	// The hourly rollup ends at the last whole hour, and its step is an hour, whatever the raw
	// interval is.
	s.TrafficRawInterval = 15 * time.Second
	if err = s.Store.InsertTraffic(ctx, []store.TrafficSample{
		{ClientID: phone.ID, Resolution: store.ResolutionRaw, BucketStart: at(10, 5), RxBytes: 10, TxBytes: 5},
	}); err != nil {
		t.Fatal(err)
	}
	if h, err = s.ClientsTraffic(ctx, store.ResolutionHourly, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if h.Step != time.Hour || !h.Until.Equal(at(12, 0)) {
		t.Fatalf("hourly step %v until %v, want 1h and 12:00", h.Step, h.Until)
	}
	// 10:00 (the raw row at 10:05) and 11:00 (11:20); 12:00's rows are in an hour that isn't over.
	if got := h.Series[0].Samples; len(got) != 2 || !got[0].BucketStart.Equal(at(10, 0)) || got[0].RxBytes != 10 ||
		!got[1].BucketStart.Equal(at(11, 0)) {
		t.Fatalf("hourly samples %+v", got)
	}
}

func TestClientAndTotalTrafficEndAtTheLastSettledBucket(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	for _, name := range []string{"phone", "laptop"} {
		if _, _, err := s.AddClient(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	laptop, _ := s.Client(ctx, store.ByName("laptop"))
	clk.t = time.Date(2026, 9, 26, 12, 34, 7, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2026, 9, 26, h, m, 0, 0, time.UTC) }
	raw := func(c ClientStatus, h, m int, rx int64) store.TrafficSample {
		return store.TrafficSample{ClientID: c.ID, Resolution: store.ResolutionRaw, BucketStart: at(h, m), RxBytes: rx, TxBytes: rx}
	}
	if err := s.Store.InsertTraffic(ctx, []store.TrafficSample{
		raw(phone, 12, 31, 1), raw(laptop, 12, 31, 10),
		raw(phone, 12, 32, 2), raw(laptop, 12, 32, 20),
		raw(phone, 12, 33, 4), raw(laptop, 12, 33, 40), // the minute that ended seven seconds ago
		raw(phone, 11, 10, 100), // an earlier hour
	}); err != nil {
		t.Fatal(err)
	}
	rx := func(ss []store.TrafficSample) []int64 {
		var out []int64
		for _, sm := range ss {
			out = append(out, sm.RxBytes)
		}
		return out
	}
	same := func(got, want []int64) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	// The 12:33 bucket may not be saved yet, so a day's history ends at 12:33 and leaves it out.
	one, err := s.ClientTraffic(ctx, store.ByName("phone"), store.ResolutionRaw, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if one.Step != time.Minute || !one.Until.Equal(at(12, 33)) || !same(rx(one.Samples), []int64{1, 2}) {
		t.Fatalf("client: step %v until %v samples %v, want 1m, 12:33, [1 2]", one.Step, one.Until, rx(one.Samples))
	}
	all, err := s.TotalTraffic(ctx, store.ResolutionRaw, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if all.Step != time.Minute || !all.Until.Equal(at(12, 33)) || !same(rx(all.Samples), []int64{11, 22}) {
		t.Fatalf("total: step %v until %v samples %v, want 1m, 12:33, [11 22]", all.Step, all.Until, rx(all.Samples))
	}

	// Ten seconds on, the bucket is in.
	clk.advance(10 * time.Second)
	if all, err = s.TotalTraffic(ctx, store.ResolutionRaw, time.Hour); err != nil || !same(rx(all.Samples), []int64{11, 22, 44}) {
		t.Fatalf("after the margin: %v, err %v, want [11 22 44]", rx(all.Samples), err)
	}

	// The hourly rollup ends at the last whole hour: 11:00's rows are in, 12:00's partial hour
	// isn't, and a sample is an hour wide.
	one, err = s.ClientTraffic(ctx, store.ByName("phone"), store.ResolutionHourly, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if one.Step != time.Hour || !one.Until.Equal(at(12, 0)) || !same(rx(one.Samples), []int64{100}) {
		t.Fatalf("hourly client: step %v until %v samples %v, want 1h, 12:00, [100]", one.Step, one.Until, rx(one.Samples))
	}
	all, err = s.TotalTraffic(ctx, store.ResolutionHourly, 7*24*time.Hour)
	if err != nil || !same(rx(all.Samples), []int64{100}) {
		t.Fatalf("hourly total: %v, err %v, want [100]", rx(all.Samples), err)
	}
}
