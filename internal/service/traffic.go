package service

import (
	"context"
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
)

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
		st.pendingRx += p.ReceiveBytes - st.lastRx
		st.pendingTx += p.SendBytes - st.lastTx
		st.lastRx, st.lastTx = p.ReceiveBytes, p.SendBytes
	}
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

// ClientTraffic returns one client's traffic history at resolution, looking back
// lookback from now.
func (s *Service) ClientTraffic(ctx context.Context, ref store.Ref, resolution string, lookback time.Duration) ([]store.TrafficSample, error) {
	c, err := s.Store.Client(ctx, ref)
	if err != nil {
		return nil, err
	}
	return s.Store.ClientTraffic(ctx, c.ID, resolution, s.now().Add(-lookback))
}

// TotalTraffic is the same as ClientTraffic, summed across every client — the
// dashboard's all-clients aggregate.
func (s *Service) TotalTraffic(ctx context.Context, resolution string, lookback time.Duration) ([]store.TrafficSample, error) {
	return s.Store.TotalTraffic(ctx, resolution, s.now().Add(-lookback))
}
