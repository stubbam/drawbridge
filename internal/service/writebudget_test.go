package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the store registers it; the meter opens a connection of its own

	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/wg"
)

// walMeter counts what a database writes, by reading its write-ahead log from outside: the
// numbers come from the file, not from instrumenting the code that writes it, so a new way of
// writing can't slip past. Every committed transaction ends a "commit frame", and every page
// it changed is a frame, which is what an SD card is made to write (a whole page for a few
// bytes), and why the budget below counts them.
//
// It works because a second connection holds a read snapshot from the start. SQLite can't
// restart the log while a reader is using it, so the log only grows, and nothing a checkpoint
// does hides a write from the count.
type walMeter struct {
	path string
	db   *sql.DB
	tx   *sql.Tx
}

// walUsage is what the log holds.
type walUsage struct {
	commits, frames int
	pageSize        int
}

// bytes is how much the frames hold, headers included.
func (u walUsage) bytes() int { return u.frames * (u.pageSize + 24) }

func (u walUsage) sub(o walUsage) walUsage {
	return walUsage{commits: u.commits - o.commits, frames: u.frames - o.frames, pageSize: u.pageSize}
}

func startWALMeter(t *testing.T, dbPath string) *walMeter {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	// Start from an empty log, so every frame in it is one of ours.
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	m := &walMeter{path: dbPath + "-wal", db: db, tx: tx}
	t.Cleanup(func() { _ = tx.Rollback(); _ = db.Close() })
	return m
}

// usage reads the log: how many transactions have committed, and how many pages they wrote.
func (m *walMeter) usage(t *testing.T) walUsage {
	t.Helper()
	b, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return walUsage{pageSize: 4096}
		}
		t.Fatal(err)
	}
	if len(b) < 32 {
		return walUsage{pageSize: 4096}
	}
	// The header is magic, version, page size, checkpoint number, two salts, and a checksum,
	// and each frame has a 24-byte header: page number, the database's size in pages if it
	// commits (else 0), the same two salts, and a checksum. A frame from an older run of the
	// log, left behind after a restart of it, has other salts.
	if magic := binary.BigEndian.Uint32(b[0:4]); magic != 0x377f0682 && magic != 0x377f0683 {
		t.Fatalf("%s doesn't start like a write-ahead log: %x", m.path, b[:4])
	}
	u := walUsage{pageSize: int(binary.BigEndian.Uint32(b[8:12]))}
	salts := b[16:24]
	for off := 32; off+24+u.pageSize <= len(b); off += 24 + u.pageSize {
		if !bytes.Equal(b[off+8:off+16], salts) {
			break
		}
		u.frames++
		if binary.BigEndian.Uint32(b[off+4:off+8]) != 0 {
			u.commits++
		}
	}
	return u
}

// TestWALMeterCountsTransactionsAndPages checks the meter against a count we know: each of
// these is one transaction.
func TestWALMeterCountsTransactionsAndPages(t *testing.T) {
	s, _, dbPath := newTestServiceDB(t)
	ctx := context.Background()
	m := startWALMeter(t, dbPath)
	if u := m.usage(t); u.commits != 0 || u.frames != 0 {
		t.Fatalf("a fresh log holds %+v", u)
	}

	const n = 25
	for i := range n {
		_, err := s.Store.AddEvent(ctx, store.Event{Kind: "auth.logout", Category: "admin", Actor: "admin", Via: "web",
			Data: map[string]string{"n": fmt.Sprint(i)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	u := m.usage(t)
	if u.commits != n {
		t.Fatalf("%d commits for %d transactions", u.commits, n)
	}
	if u.frames < n || u.pageSize != 4096 {
		t.Fatalf("%+v: want at least a page per transaction, in 4096-byte pages", u)
	}

	// A transaction that changes nothing writes nothing.
	before := m.usage(t)
	if err := s.Store.RollupTraffic(ctx, s.now()); err != nil {
		t.Fatal(err)
	}
	if after := m.usage(t); after != before {
		t.Fatalf("a rollup with nothing to roll up wrote %+v", after.sub(before))
	}
}

// workload is a day of use, for the write budget.
type workload struct {
	// alwaysOn clients are connected, and moving traffic, all day. commuters are connected for
	// 45 minutes at 08:00 and again at 18:00, and are gone the rest of the day. idle clients
	// exist, and never connect.
	alwaysOn, commuters, idle int
	// adminTab is a browser tab left open on the dashboard, which asks the daemon for the
	// clients every five seconds, all day.
	adminTab bool
	// apiToken is a dashboard such as Homepage that asks for the status with a read-only API
	// token every ten seconds, all day.
	apiToken bool
}

// connectedAt says whether client i of the workload is connected at a time of day.
func (w workload) connectedAt(i int, at time.Time) bool {
	switch {
	case i < w.alwaysOn:
		return true
	case i < w.alwaysOn+w.commuters:
		h, m := at.Hour(), at.Minute()
		return (h == 8 && m < 45) || (h == 18 && m < 45)
	}
	return false
}

func (w workload) clients() int { return w.alwaysOn + w.commuters + w.idle }

const (
	pollEvery  = 5 * time.Second  // the daemon's --session-interval default
	driftEvery = 30 * time.Second // and its --drift-interval
)

// simulate runs the daemon's loops against a fake clock, the way serve does: TrackConnections
// every five seconds, Sync and PruneSessions every 30, and the traffic rollup at the end of
// each day. Clients move traffic on every poll while they're connected, and shake hands every
// two minutes, as WireGuard does. The span starts at midnight, and a span of a day or more
// ends with the rollup, which has a day of raw samples to roll up and prune, as it does every
// day in steady state. It returns what the database wrote in the span.
func simulate(t *testing.T, s *Service, clk *clock, dbPath string, w workload, span time.Duration) walUsage {
	t.Helper()
	ctx := context.Background()
	fake := s.WG.(*wg.Fake)
	if s.TrackInterval == 0 {
		s.TrackInterval = pollEvery
	}
	clk.t = time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)

	all := make([]model.Client, w.clients())
	for i := range all {
		c, _, err := s.AddClient(ctx, fmt.Sprintf("device-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		all[i] = c
	}

	// Yesterday's raw samples, which a day's rollup meets in steady state: those from the day
	// that has just passed the 48-hour retention. One a minute for each client that moved
	// traffic then.
	yesterday := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	var seed []store.TrafficSample
	for i, c := range all {
		for at := yesterday; at.Before(yesterday.Add(24 * time.Hour)); at = at.Add(time.Minute) {
			if w.connectedAt(i, at) {
				seed = append(seed, store.TrafficSample{ClientID: c.ID, Resolution: store.ResolutionRaw,
					BucketStart: at, RxBytes: 1 << 20, TxBytes: 256 << 10})
			}
		}
	}
	if err := s.Store.InsertTraffic(ctx, seed); err != nil {
		t.Fatal(err)
	}

	var password string
	if w.adminTab || w.apiToken {
		var err error
		if password, err = s.CreateAdmin(ctx, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	var secret string
	if w.apiToken {
		admin, err := s.Store.OnlyUser(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, secret, err = s.CreateAPIToken(ctx, admin, password, "dashboard"); err != nil {
			t.Fatal(err)
		}
	}
	var token string
	askForClients := func() {
		// The dashboard's poll: every request carries the cookie, and the daemon checks it.
		if _, _, err := s.Authenticate(ctx, token); err != nil {
			login, err := s.Login(ctx, "admin", password, "test")
			if err != nil {
				t.Fatal(err)
			}
			token = login.Token
		}
	}

	rx, tx := make([]int64, len(all)), make([]int64, len(all))
	shake := make([]time.Time, len(all))
	meter := startWALMeter(t, dbPath)
	start := meter.usage(t)
	{
		for n := 1; n <= int(span/pollEvery); n++ {
			clk.advance(pollEvery)
			now := clk.now()
			for i, c := range all {
				if !w.connectedAt(i, now) {
					continue
				}
				rx[i] += 20<<10 + int64(n*(i+7)%50)<<10
				tx[i] += 5<<10 + int64(n*(i+3)%20)<<10
				if now.Sub(shake[i]) >= 2*time.Minute {
					shake[i] = now
				}
				fake.SetHandshake("wg0", c.PublicKey, wg.Peer{
					LastHandshake: shake[i], ReceiveBytes: rx[i], SendBytes: tx[i],
					Endpoint: netip.AddrPortFrom(netip.AddrFrom4([4]byte{203, 0, 113, byte(10 + i)}), 51820),
				})
			}
			if w.adminTab {
				askForClients()
			}
			if w.apiToken && n%2 == 0 {
				// The dashboard's ask: the daemon checks the token, and notes its use once an hour.
				if _, err := s.AuthenticateToken(ctx, secret); err != nil {
					t.Fatal(err)
				}
			}
			s.TrackConnections(ctx)
			if n%int(driftEvery/pollEvery) == 0 {
				s.Sync(ctx)
				s.PruneSessions(ctx)
			}
		}
		if span >= 24*time.Hour {
			s.TrafficRetention(ctx)
		}
	}
	return meter.usage(t).sub(start)
}

// The budget, for one day of each workload below. It's what the plan promises (docs/PLAN.md
// §6.4): one write a minute however many clients there are, and none when nothing is moving.
// The numbers come from the log the database wrote (walMeter), with room to spare for a
// change that's worth its cost and not for one that writes on every poll, which would be
// thousands of transactions more.
//
// A day is 1,440 minutes, so the flushes alone are 1,440 commits. The rest is what an admin
// does, and what a connecting client records: an admin tab that stays open touches its session
// every five minutes (288 a day), and a client that connects or leaves is an event.
const (
	budgetCommitsPerDay = 2200
	// A page is 4 KiB. The pages a flush writes grow with the clients it covers, a few for each
	// (their rows in the traffic table and its indexes, and their open sessions).
	budgetPagesPerDayTypical = 24_000 // about 94 MiB of log
	budgetPagesPerDayBusy    = 74_000 // about 290 MiB, for twice the clients, all connected
	// A server with nobody connected has nothing to write but the clean-up that finds nothing.
	budgetCommitsPerIdleDay = 30
	budgetPagesPerIdleDay   = 120
)

var (
	// A household's VPN: a few devices that stay connected, a few that come and go, a browser
	// tab left on the dashboard, and a dashboard on the home page that polls with an API token.
	typicalHousehold = workload{alwaysOn: 5, commuters: 5, adminTab: true, apiToken: true}
	// Twice the clients, all connected all day.
	busyHousehold = workload{alwaysOn: 20, adminTab: true}
	// Clients that never connect.
	idleServer = workload{idle: 10}
)

func TestWriteBudget(t *testing.T) {
	if raceEnabled {
		t.Skip("a day of polling takes a minute under the race detector; `make test-go` runs this test without it")
	}
	days := map[string]walUsage{}
	for _, tc := range []struct {
		name                 string
		w                    workload
		maxCommits, maxPages int
	}{
		{"typical", typicalHousehold, budgetCommitsPerDay, budgetPagesPerDayTypical},
		{"busy", busyHousehold, budgetCommitsPerDay, budgetPagesPerDayBusy},
		{"idle", idleServer, budgetCommitsPerIdleDay, budgetPagesPerIdleDay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, clk, dbPath := newTestServiceDB(t)
			day := simulate(t, s, clk, dbPath, tc.w, 24*time.Hour)
			days[tc.name] = day
			t.Logf("a day: %d commits, %d pages (%d KiB of log)", day.commits, day.frames, day.bytes()>>10)
			if day.commits > tc.maxCommits {
				t.Errorf("%d transactions in a day, budget %d: something writes more often than once a minute", day.commits, tc.maxCommits)
			}
			if day.frames > tc.maxPages {
				t.Errorf("%d pages written in a day (%d KiB), budget %d", day.frames, day.bytes()>>10, tc.maxPages)
			}
		})
	}

	// The claim behind the budget: a flush is one transaction for every client, so more
	// clients make it bigger and never more frequent.
	typical, busy := days["typical"], days["busy"]
	if typical.commits > 0 && busy.commits > typical.commits+100 {
		t.Errorf("%d clients took %d transactions in a day and %d took %d: the writes follow the clients",
			typicalHousehold.clients(), typical.commits, busyHousehold.clients(), busy.commits)
	}
}

// TestTheRawIntervalSetsTheWriteRate checks that the knob the plan names for a host on an SSD
// works as documented: a shorter --traffic-raw-interval is proportionally more flushes.
func TestTheRawIntervalSetsTheWriteRate(t *testing.T) {
	hour := func(interval time.Duration) int {
		s, clk, dbPath := newTestServiceDB(t)
		s.TrafficRawInterval = interval
		return simulate(t, s, clk, dbPath, workload{alwaysOn: 5}, time.Hour).commits
	}
	slow, fast := hour(time.Minute), hour(10*time.Second)
	if ratio := float64(fast) / float64(slow); ratio < 5 || ratio > 7 {
		t.Errorf("a 10 s interval wrote %d transactions in an hour and a 1 minute interval %d: ratio %.1f, want about 6", fast, slow, ratio)
	}
}
