package store

import (
	"context"
	"testing"
	"time"
)

func mustClient(t *testing.T, s *Store, name string) string {
	t.Helper()
	c, err := s.AddClient(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func TestTrafficRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	id := mustClient(t, s, "phone")

	t0 := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	err := s.InsertTraffic(ctx, []TrafficSample{
		{ClientID: id, Resolution: ResolutionRaw, BucketStart: t0, RxBytes: 100, TxBytes: 50},
		{ClientID: id, Resolution: ResolutionRaw, BucketStart: t1, RxBytes: 200, TxBytes: 75},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.ClientTraffic(ctx, id, ResolutionRaw, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].BucketStart.Equal(t0) || got[0].RxBytes != 100 ||
		!got[1].BucketStart.Equal(t1) || got[1].TxBytes != 75 {
		t.Fatalf("got %+v", got)
	}

	// A row older than "since" is excluded.
	got, err = s.ClientTraffic(ctx, id, ResolutionRaw, t1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].BucketStart.Equal(t1) {
		t.Fatalf("since t1: %+v", got)
	}
}

func TestInsertTrafficUpsertsReplacesNotSums(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	id := mustClient(t, s, "phone")
	bucket := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

	for _, rx := range []int64{100, 250} {
		err := s.InsertTraffic(ctx, []TrafficSample{
			{ClientID: id, Resolution: ResolutionRaw, BucketStart: bucket, RxBytes: rx, TxBytes: 1},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ClientTraffic(ctx, id, ResolutionRaw, bucket)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RxBytes != 250 {
		t.Fatalf("got %+v, want one row replaced to 250, not summed", got)
	}
}

func TestTotalTrafficSumsAcrossClients(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	a, b := mustClient(t, s, "phone"), mustClient(t, s, "laptop")
	bucket := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

	err := s.InsertTraffic(ctx, []TrafficSample{
		{ClientID: a, Resolution: ResolutionRaw, BucketStart: bucket, RxBytes: 100, TxBytes: 10},
		{ClientID: b, Resolution: ResolutionRaw, BucketStart: bucket, RxBytes: 300, TxBytes: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.TotalTraffic(ctx, ResolutionRaw, bucket)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RxBytes != 400 || got[0].TxBytes != 30 {
		t.Fatalf("got %+v, want one bucket summing to 400/30", got)
	}
}

func TestRollupTrafficSumsIntoHourlyAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	id := mustClient(t, s, "phone")
	hour := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

	err := s.InsertTraffic(ctx, []TrafficSample{
		{ClientID: id, Resolution: ResolutionRaw, BucketStart: hour, RxBytes: 100, TxBytes: 10},
		{ClientID: id, Resolution: ResolutionRaw, BucketStart: hour.Add(30 * time.Minute), RxBytes: 200, TxBytes: 20},
		// Outside the rollup cutoff below: should stay a "raw" row, not rolled up.
		{ClientID: id, Resolution: ResolutionRaw, BucketStart: hour.Add(2 * time.Hour), RxBytes: 999, TxBytes: 999},
	})
	if err != nil {
		t.Fatal(err)
	}

	cutoff := hour.Add(time.Hour)
	if err := s.RollupTraffic(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	assertRolledUp := func() {
		t.Helper()
		got, err := s.ClientTraffic(ctx, id, ResolutionHourly, hour)
		if err != nil {
			t.Fatal(err)
		}
		// The hourly view also folds in the not-yet-pruned raw row past the cutoff, so
		// there are two hours: the rolled-up one and the still-raw one 2h later.
		if len(got) != 2 {
			t.Fatalf("got %+v, want 2 hours", got)
		}
		if !got[0].BucketStart.Equal(hour) || got[0].RxBytes != 300 || got[0].TxBytes != 30 {
			t.Fatalf("rolled-up hour: %+v, want 300/30 at %v", got[0], hour)
		}
	}
	assertRolledUp()

	// Running it again recomputes the same sums rather than doubling them, as long as
	// the underlying "raw" rows haven't been pruned yet.
	if err := s.RollupTraffic(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	assertRolledUp()
}

func TestClientTrafficHourlyDoesNotDoubleCountAfterRollupBeforePrune(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	id := mustClient(t, s, "phone")
	hour := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

	if err := s.InsertTraffic(ctx, []TrafficSample{
		{ClientID: id, Resolution: ResolutionRaw, BucketStart: hour, RxBytes: 100, TxBytes: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RollupTraffic(ctx, hour.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// The "raw" row is still there (not pruned yet); the hourly view must use only the
	// rolled-up row for that hour, not both.
	got, err := s.ClientTraffic(ctx, id, ResolutionHourly, hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RxBytes != 100 {
		t.Fatalf("got %+v, want exactly one hour of 100, not double-counted", got)
	}
}

func TestPruneTrafficDeletesOnlyStrictlyOlderRows(t *testing.T) {
	ctx := context.Background()
	s := initialized(t)
	id := mustClient(t, s, "phone")
	cutoff := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

	err := s.InsertTraffic(ctx, []TrafficSample{
		{ClientID: id, Resolution: ResolutionRaw, BucketStart: cutoff.Add(-time.Minute), RxBytes: 1, TxBytes: 1},
		{ClientID: id, Resolution: ResolutionRaw, BucketStart: cutoff, RxBytes: 2, TxBytes: 2},
		{ClientID: id, Resolution: ResolutionRaw, BucketStart: cutoff.Add(time.Minute), RxBytes: 3, TxBytes: 3},
	})
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneTraffic(ctx, ResolutionRaw, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1 (only strictly before the cutoff)", n)
	}
	got, err := s.ClientTraffic(ctx, id, ResolutionRaw, cutoff.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RxBytes != 2 || got[1].RxBytes != 3 {
		t.Fatalf("remaining rows %+v, want the cutoff row and the one after it", got)
	}
}

// A database from before the traffic table exists keeps its data when a newer build
// opens it (mirrors TestMigrationAddsClientSessionsToAnExistingDatabase).
func TestMigrationAddsTrafficToAnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	s, path := openTest(t)
	if _, err := s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TABLE traffic`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 5`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	again, err := Open(ctx, path, testSealer(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	c, err := again.Client(ctx, ByName("phone"))
	if err != nil {
		t.Fatalf("client lost after the upgrade: %v", err)
	}
	if got, err := again.ClientTraffic(ctx, c.ID, ResolutionRaw, time.Unix(0, 0)); err != nil || len(got) != 0 {
		t.Fatalf("traffic %v, err %v, want none", got, err)
	}
}
