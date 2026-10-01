package service

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/wg"
)

// Traffic-history defaults (docs/PLAN.md §6.4). All three are configurable, because a
// host on an SD card and one on an NVMe SSD can tolerate very different write budgets
// (CLAUDE.md, "Protect the SD card"): the SD-card-safe defaults below are conservative,
// and an admin on NVMe can set a finer TrafficRawInterval and/or longer retention.
const (
	DefaultTrafficRawInterval     = time.Minute
	DefaultTrafficRawRetention    = 48 * time.Hour
	DefaultTrafficHourlyRetention = 90 * 24 * time.Hour
	// DefaultTrackInterval is the daemon's --session-interval default, which is how often
	// TrackConnections runs and so how wide a live sample is.
	DefaultTrackInterval = 5 * time.Second
)

func (s *Service) trackInterval() time.Duration {
	if s.TrackInterval > 0 {
		return s.TrackInterval
	}
	return DefaultTrackInterval
}

func (s *Service) trafficRawInterval() time.Duration {
	if s.TrafficRawInterval > 0 {
		return s.TrafficRawInterval
	}
	return DefaultTrafficRawInterval
}

func (s *Service) trafficRawRetention() time.Duration {
	if s.TrafficRawRetention > 0 {
		return s.TrafficRawRetention
	}
	return DefaultTrafficRawRetention
}

func (s *Service) trafficHourlyRetention() time.Duration {
	if s.TrafficHourlyRetention > 0 {
		return s.TrafficHourlyRetention
	}
	return DefaultTrafficHourlyRetention
}

// ResolutionLive asks ClientTraffic or TotalTraffic for the in-memory polls below instead of
// stored buckets. The stored "raw" buckets are too coarse for the chart's 1 minute range: they
// are a minute wide by default, and the newest isn't flushed until its minute is over.
const ResolutionLive = "live"

// liveRetention is how long a poll stays in memory. It's longer than the 1 minute range reads,
// so a request never finds its window already trimmed.
const liveRetention = 2 * time.Minute

// liveBytes is what one client moved during one poll.
type liveBytes struct{ rx, tx int64 }

// liveTick is one TrackConnections poll: the bytes each connected client moved since the
// previous one. A client that wasn't connected, or whose counters were only just baselined
// or reset, isn't in it, which reads as zero.
type liveTick struct {
	at      time.Time
	clients map[string]liveBytes
}

// liveTraffic keeps the last liveRetention of polls in memory only: nothing here is written to
// the database (CLAUDE.md, "Protect the SD card"), so a restart starts it empty and the 1
// minute chart fills back in within a minute.
type liveTraffic struct {
	mu    sync.Mutex
	ticks []liveTick
}

func (l *liveTraffic) add(t liveTick) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := t.at.Add(-liveRetention)
	i := 0
	for i < len(l.ticks) && l.ticks[i].at.Before(cutoff) {
		i++
	}
	l.ticks = append(l.ticks[i:], t)
}

// samples returns one sample per poll since the given time, oldest first: clientID's bytes,
// or every client's summed when clientID is "". The poll's time stands in for the bucket
// start, and a poll in which the client moved nothing is a zero sample, not a gap.
func (l *liveTraffic) samples(clientID string, since time.Time) []store.TrafficSample {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []store.TrafficSample
	for _, t := range l.ticks {
		if t.at.Before(since) {
			continue
		}
		var sum liveBytes
		if clientID == "" {
			for _, b := range t.clients {
				sum.rx += b.rx
				sum.tx += b.tx
			}
		} else {
			sum = t.clients[clientID]
		}
		out = append(out, store.TrafficSample{ClientID: clientID, BucketStart: t.at, RxBytes: sum.rx, TxBytes: sum.tx})
	}
	return out
}

// byClient returns, for each of ids, one sample per poll since the given time, oldest first. A
// client that moved nothing in a poll gets a zero sample, so every client has a sample at every
// poll and the series line up.
func (l *liveTraffic) byClient(ids []string, since time.Time) map[string][]store.TrafficSample {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string][]store.TrafficSample, len(ids))
	for _, t := range l.ticks {
		if t.at.Before(since) {
			continue
		}
		for _, id := range ids {
			b := t.clients[id]
			out[id] = append(out[id], store.TrafficSample{ClientID: id, BucketStart: t.at, RxBytes: b.rx, TxBytes: b.tx})
		}
	}
	return out
}

// trafficState is one client's running counters, held only in memory (a crash loses at
// most the current bucket's unflushed bytes, which is acceptable for a chart — unlike
// client_sessions, this isn't authoritative data).
type trafficState struct {
	// lastRx/lastTx are the peer's cumulative counters as of the last tick, for computing
	// deltas and detecting a reset (a new value lower than the last one).
	lastRx, lastTx int64
	// pendingRx/pendingTx accumulate this bucket's deltas until the next flush.
	pendingRx, pendingTx int64
}

// trafficBuffer accumulates every client's traffic since the last flush. It's touched
// only from the connTrackLoop goroutine (the same one that calls SampleTraffic), so it
// needs no mutex.
type trafficBuffer struct {
	bucket  time.Time
	clients map[string]*trafficState
}

// SampleTraffic accumulates each connected client's RX/TX bytes into the current bucket
// and flushes the previous one, in a single transaction, when the wall-clock bucket
// changes (docs/PLAN.md §6.4: "flushed once a minute in one transaction"). It's called
// from TrackConnections (conntrack.go), reusing that call's wgctrl read rather than
// polling a second time.
func (s *Service) SampleTraffic(ctx context.Context, clients []model.Client, peers map[string]wg.Peer) {
	buf := s.trafficBuf
	if buf == nil {
		buf = &trafficBuffer{bucket: s.now().UTC().Truncate(s.trafficRawInterval()), clients: map[string]*trafficState{}}
		s.trafficBuf = buf
	}

	// Drop any client no longer in the client list (deleted), so the buffer doesn't grow
	// without bound.
	known := make(map[string]bool, len(clients))
	for _, c := range clients {
		known[c.ID] = true
	}
	for id := range buf.clients {
		if !known[id] {
			delete(buf.clients, id)
		}
	}

	bucket := s.now().UTC().Truncate(s.trafficRawInterval())
	if bucket.After(buf.bucket) {
		s.flushTraffic(ctx, buf)
		buf.bucket = bucket
	}

	tick := liveTick{at: s.now().UTC().Truncate(time.Second), clients: map[string]liveBytes{}}
	for _, c := range clients {
		p, connected := peers[c.PublicKey.String()]
		if !connected {
			continue
		}
		st, ok := buf.clients[c.ID]
		if !ok {
			buf.clients[c.ID] = &trafficState{lastRx: p.ReceiveBytes, lastTx: p.SendBytes}
			continue
		}
		if p.ReceiveBytes < st.lastRx || p.SendBytes < st.lastTx {
			// The peer's counters went backward (re-added, or the interface was
			// recreated): drop the delta rather than let it go negative or spike, and
			// re-baseline from here.
			st.lastRx, st.lastTx = p.ReceiveBytes, p.SendBytes
			continue
		}
		dRx, dTx := p.ReceiveBytes-st.lastRx, p.SendBytes-st.lastTx
		st.pendingRx += dRx
		st.pendingTx += dTx
		st.lastRx, st.lastTx = p.ReceiveBytes, p.SendBytes
		tick.clients[c.ID] = liveBytes{rx: dRx, tx: dTx}
	}
	s.live.add(tick)
}

// flushTraffic writes every client's accumulated bytes for the buffer's current bucket,
// then clears them (lastRx/lastTx carry forward; only the per-bucket accumulators
// reset). A client with no traffic this bucket isn't written at all, which is both fewer
// writes and doesn't lose information (a missing bucket means zero).
func (s *Service) flushTraffic(ctx context.Context, buf *trafficBuffer) {
	var samples []store.TrafficSample
	for id, st := range buf.clients {
		if st.pendingRx == 0 && st.pendingTx == 0 {
			continue
		}
		samples = append(samples, store.TrafficSample{
			ClientID: id, Resolution: store.ResolutionRaw, BucketStart: buf.bucket,
			RxBytes: st.pendingRx, TxBytes: st.pendingTx,
		})
		st.pendingRx, st.pendingTx = 0, 0
	}
	if err := s.Store.InsertTraffic(ctx, samples); err != nil {
		s.Log.Warn("can't flush traffic samples", "err", err)
	}
}

// TrafficRetention rolls old "raw" samples up into "hourly" ones, then prunes past-
// retention rows of both resolutions (docs/PLAN.md §6.4). Both windows are configurable.
// Rollup-then-prune is idempotent (a second run recomputes the same "hourly" sums and
// deletes nothing new), so no "last run" timestamp is persisted — it's safe to call this
// more than once, including right after a restart mid-job.
func (s *Service) TrafficRetention(ctx context.Context) {
	rawCutoff := s.now().Add(-s.trafficRawRetention())
	if err := s.Store.RollupTraffic(ctx, rawCutoff); err != nil {
		s.Log.Warn("can't roll up traffic samples", "err", err)
		return
	}
	if _, err := s.Store.PruneTraffic(ctx, store.ResolutionRaw, rawCutoff); err != nil {
		s.Log.Warn("can't prune raw traffic samples", "err", err)
	}
	hourlyCutoff := s.now().Add(-s.trafficHourlyRetention())
	if _, err := s.Store.PruneTraffic(ctx, store.ResolutionHourly, hourlyCutoff); err != nil {
		s.Log.Warn("can't prune hourly traffic samples", "err", err)
	}
}

// trafficWindow is the span a chart range covers. Every history (one client's, the total, and
// every client's) is cut to one so the charts agree on where the data ends.
type trafficWindow struct {
	// step is how long one sample covers: the poll interval for live samples, the raw interval
	// for stored ones, and an hour for the hourly rollup. Dividing a sample's bytes by it gives a
	// rate.
	step time.Duration
	// since and until bound the samples: [since, until) for stored ones, and every poll from
	// since on for live ones.
	since, until time.Time
	live         bool
}

// trafficWindow returns the window for resolution (a store resolution, or ResolutionLive) and
// lookback. Until is the last complete sample: a stored bucket is left out until it has ended and
// the poll after it has saved it, so the chart never mistakes a bucket that's still filling, or
// isn't saved yet, for one in which nothing moved. A live poll is complete as soon as it's taken.
func (s *Service) trafficWindow(resolution string, lookback time.Duration) trafficWindow {
	now := s.now().UTC()
	if resolution == ResolutionLive {
		until := now.Truncate(time.Second)
		return trafficWindow{step: s.trackInterval(), since: until.Add(-lookback), until: until, live: true}
	}
	step := s.trafficRawInterval()
	if resolution == store.ResolutionHourly {
		step = time.Hour
	}
	// A bucket is saved by the first poll after it ends, so give that poll two intervals.
	until := now.Add(-2 * s.trackInterval()).Truncate(step)
	return trafficWindow{step: step, since: until.Add(-lookback), until: until}
}

// TrafficSamples is one series of traffic history, with the window it covers: one client's, or
// every client's summed.
type TrafficSamples struct {
	// Step is how long one sample covers, so a sample's bytes over it is a rate.
	Step time.Duration
	// Until is where the history ends: every sample is complete and starts before it.
	Until time.Time
	// Samples are oldest first. A stored bucket in which nothing moved isn't saved, and means
	// zero; a live series has a sample at every poll.
	Samples []store.TrafficSample
}

// settle drops the samples that aren't complete: the stored buckets at or after until.
func (w trafficWindow) settle(ss []store.TrafficSample) TrafficSamples {
	out := TrafficSamples{Step: w.step, Until: w.until, Samples: ss}
	if w.live {
		return out
	}
	n := len(ss)
	for n > 0 && !ss[n-1].BucketStart.Before(w.until) {
		n--
	}
	out.Samples = ss[:n]
	return out
}

// ClientTraffic returns one client's traffic history at resolution (a store resolution, or
// ResolutionLive), looking back lookback from the last complete sample.
func (s *Service) ClientTraffic(ctx context.Context, ref store.Ref, resolution string, lookback time.Duration) (TrafficSamples, error) {
	c, err := s.Store.Client(ctx, ref)
	if err != nil {
		return TrafficSamples{}, err
	}
	w := s.trafficWindow(resolution, lookback)
	if w.live {
		return w.settle(s.live.samples(c.ID, w.since)), nil
	}
	ss, err := s.Store.ClientTraffic(ctx, c.ID, resolution, w.since)
	if err != nil {
		return TrafficSamples{}, err
	}
	return w.settle(ss), nil
}

// TotalTraffic is the same as ClientTraffic, summed across every client: the dashboard's
// all-clients aggregate.
func (s *Service) TotalTraffic(ctx context.Context, resolution string, lookback time.Duration) (TrafficSamples, error) {
	w := s.trafficWindow(resolution, lookback)
	if w.live {
		return w.settle(s.live.samples("", w.since)), nil
	}
	ss, err := s.Store.TotalTraffic(ctx, resolution, w.since)
	if err != nil {
		return TrafficSamples{}, err
	}
	return w.settle(ss), nil
}

// TrafficSeries is one client's samples in a TrafficHistory.
type TrafficSeries struct {
	Client  model.Client
	Samples []store.TrafficSample
}

// TrafficHistory is every client's traffic over one chart range, for the charts page.
type TrafficHistory struct {
	// Step and Until are as in TrafficSamples.
	Step  time.Duration
	Until time.Time
	// Series has every client, by name. A stored client's samples are sparse; a live client has
	// one at every poll.
	Series []TrafficSeries
}

// ClientsTraffic returns every client's traffic over the range that resolution and lookback
// describe (views.ParseTrafficRange), ending at the last complete sample.
func (s *Service) ClientsTraffic(ctx context.Context, resolution string, lookback time.Duration) (TrafficHistory, error) {
	clients, err := s.Store.Clients(ctx)
	if err != nil {
		return TrafficHistory{}, err
	}
	sort.Slice(clients, func(i, j int) bool {
		a, b := strings.ToLower(clients[i].Name), strings.ToLower(clients[j].Name)
		if a != b {
			return a < b
		}
		return clients[i].ID < clients[j].ID
	})
	w := s.trafficWindow(resolution, lookback)
	var by map[string][]store.TrafficSample
	if w.live {
		ids := make([]string, len(clients))
		for i, c := range clients {
			ids[i] = c.ID
		}
		by = s.live.byClient(ids, w.since)
	} else if by, err = s.Store.ClientsTraffic(ctx, resolution, w.since, w.until); err != nil {
		return TrafficHistory{}, err
	}
	h := TrafficHistory{Step: w.step, Until: w.until}
	for _, c := range clients {
		h.Series = append(h.Series, TrafficSeries{Client: c, Samples: by[c.ID]})
	}
	return h, nil
}
