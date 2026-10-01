package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Traffic resolutions (docs/PLAN.md §6.4, §7). "raw" is the sampler's own bucket width
// (configurable, default a minute); "hourly" rows are always exactly an hour wide,
// produced from "raw" rows by RollupTraffic.
const (
	ResolutionRaw    = "raw"
	ResolutionHourly = "hourly"
)

// TrafficSample is one client's RX/TX delta for one bucket.
type TrafficSample struct {
	ClientID    string
	Resolution  string
	BucketStart time.Time
	RxBytes     int64
	TxBytes     int64
}

// InsertTraffic writes one flush's worth of buckets in a single transaction. A bucket
// written twice (shouldn't normally happen for "raw" samples, but RollupTraffic upserts
// "hourly" ones the same way) has its bytes replaced, not summed.
func (s *Store) InsertTraffic(ctx context.Context, samples []TrafficSample) error {
	if len(samples) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO traffic
			(client_id, resolution, bucket_start, rx_bytes, tx_bytes) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (client_id, resolution, bucket_start) DO UPDATE SET
				rx_bytes = excluded.rx_bytes, tx_bytes = excluded.tx_bytes`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, sm := range samples {
			if _, err := stmt.ExecContext(ctx, sm.ClientID, sm.Resolution,
				formatTime(sm.BucketStart), sm.RxBytes, sm.TxBytes); err != nil {
				return err
			}
		}
		return nil
	})
}

// hourBucketOfT and hourBucketOfTraffic truncate a bucket_start column to its hour, in
// the same fixed-width format the store writes every timestamp in, so it lines up with a
// real "hourly" row's bucket_start. Each names its table explicitly (as "t" or
// "traffic", matching the query it's used in) because SQLite resolves an unqualified
// column in a correlated subquery against the subquery's own tables first, not the outer
// one — a bare "bucket_start" inside a NOT EXISTS correlated on a second "traffic" table
// would silently bind to the wrong table's column. These are plain constants, not built
// from a helper function, only so gosec's G202 can see the SQL fragment is fixed at
// compile time, never built from anything a caller supplies.
const (
	hourBucketOfT       = "substr(t.bucket_start, 1, 13) || ':00:00.000000Z'"
	hourBucketOfTraffic = "substr(traffic.bucket_start, 1, 13) || ':00:00.000000Z'"
)

// ClientTraffic returns one client's samples at resolution, from since onward, oldest
// first. At "hourly", a bucket not yet rolled up by RollupTraffic is filled in from
// "raw" rows for that hour, so a long-range chart doesn't show a gap for the most recent
// stretch of time (up to the raw retention window) that hasn't been rolled up yet. A
// "raw" row is only folded in when the client has no "hourly" row for that hour yet, so
// a bucket already rolled up (but whose "raw" rows aren't pruned yet) isn't double
// counted.
func (s *Store) ClientTraffic(ctx context.Context, clientID, resolution string, since time.Time) ([]TrafficSample, error) {
	if resolution != ResolutionHourly {
		return s.queryTraffic(ctx, `SELECT bucket_start, rx_bytes, tx_bytes FROM traffic
			WHERE client_id = ?1 AND resolution = ?2 AND bucket_start >= ?3
			ORDER BY bucket_start`, clientID, clientID, resolution, formatTime(since))
	}
	return s.queryTraffic(ctx, `
		SELECT bucket_start, SUM(rx_bytes), SUM(tx_bytes) FROM (
			SELECT bucket_start, rx_bytes, tx_bytes FROM traffic
			WHERE client_id = ?1 AND resolution = 'hourly' AND bucket_start >= ?2
			UNION ALL
			SELECT `+hourBucketOfT+` AS bucket_start, rx_bytes, tx_bytes FROM traffic t
			WHERE client_id = ?1 AND resolution = 'raw' AND bucket_start >= ?2
				AND NOT EXISTS (SELECT 1 FROM traffic h WHERE h.client_id = t.client_id
					AND h.resolution = 'hourly' AND h.bucket_start = `+hourBucketOfT+`)
		)
		GROUP BY bucket_start ORDER BY bucket_start`, clientID, clientID, formatTime(since))
}

// TotalTraffic is the same as ClientTraffic, summed across every client — the
// dashboard's all-clients aggregate.
func (s *Store) TotalTraffic(ctx context.Context, resolution string, since time.Time) ([]TrafficSample, error) {
	if resolution != ResolutionHourly {
		return s.queryTraffic(ctx, `SELECT bucket_start, SUM(rx_bytes), SUM(tx_bytes) FROM traffic
			WHERE resolution = ?1 AND bucket_start >= ?2
			GROUP BY bucket_start ORDER BY bucket_start`, "", resolution, formatTime(since))
	}
	return s.queryTraffic(ctx, `
		SELECT bucket_start, SUM(rx_bytes), SUM(tx_bytes) FROM (
			SELECT bucket_start, rx_bytes, tx_bytes FROM traffic
			WHERE resolution = 'hourly' AND bucket_start >= ?1
			UNION ALL
			SELECT `+hourBucketOfT+` AS bucket_start, rx_bytes, tx_bytes FROM traffic t
			WHERE resolution = 'raw' AND bucket_start >= ?1
				AND NOT EXISTS (SELECT 1 FROM traffic h WHERE h.client_id = t.client_id
					AND h.resolution = 'hourly' AND h.bucket_start = `+hourBucketOfT+`)
		)
		GROUP BY bucket_start ORDER BY bucket_start`, "", formatTime(since))
}

// ClientsTraffic returns every client's samples at resolution for the buckets starting in
// [since, until), grouped by client ID, each client's oldest first; a client with none isn't in
// the map. At "hourly" it folds in "raw" rows that haven't been rolled up yet, exactly as
// ClientTraffic does, so both bounds should lie on an hour boundary: a raw row's hour is judged
// by its own start, and an hour only partly inside the range would come back short.
func (s *Store) ClientsTraffic(ctx context.Context, resolution string, since, until time.Time) (map[string][]TrafficSample, error) {
	query := `SELECT client_id, bucket_start, rx_bytes, tx_bytes FROM traffic
		WHERE resolution = ?1 AND bucket_start >= ?2 AND bucket_start < ?3
		ORDER BY client_id, bucket_start`
	args := []any{resolution, formatTime(since), formatTime(until)}
	if resolution == ResolutionHourly {
		query = `
			SELECT client_id, bucket_start, SUM(rx_bytes), SUM(tx_bytes) FROM (
				SELECT client_id, bucket_start, rx_bytes, tx_bytes FROM traffic
				WHERE resolution = 'hourly' AND bucket_start >= ?1 AND bucket_start < ?2
				UNION ALL
				SELECT client_id, ` + hourBucketOfT + ` AS bucket_start, rx_bytes, tx_bytes FROM traffic t
				WHERE resolution = 'raw' AND bucket_start >= ?1 AND bucket_start < ?2
					AND NOT EXISTS (SELECT 1 FROM traffic h WHERE h.client_id = t.client_id
						AND h.resolution = 'hourly' AND h.bucket_start = ` + hourBucketOfT + `)
			)
			GROUP BY client_id, bucket_start ORDER BY client_id, bucket_start`
		args = args[1:]
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]TrafficSample{}
	for rows.Next() {
		var (
			id, ts string
			rx, tx int64
		)
		if err := rows.Scan(&id, &ts, &rx, &tx); err != nil {
			return nil, err
		}
		bucket, err := time.Parse(timeFormat, ts)
		if err != nil {
			return nil, fmt.Errorf("traffic bucket %q: %w", ts, err)
		}
		out[id] = append(out[id], TrafficSample{ClientID: id, BucketStart: bucket, RxBytes: rx, TxBytes: tx})
	}
	return out, rows.Err()
}

// queryTraffic runs a (bucket_start, rx_bytes, tx_bytes) query and fills in clientID on
// every row (the aggregate queries don't select it, since they sum across resolutions or
// clients).
func (s *Store) queryTraffic(ctx context.Context, sqlQuery, clientID string, args ...any) ([]TrafficSample, error) {
	rows, err := s.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrafficSample
	for rows.Next() {
		var (
			ts     string
			rx, tx int64
		)
		if err := rows.Scan(&ts, &rx, &tx); err != nil {
			return nil, err
		}
		bucket, err := time.Parse(timeFormat, ts)
		if err != nil {
			return nil, fmt.Errorf("traffic bucket %q: %w", ts, err)
		}
		out = append(out, TrafficSample{ClientID: clientID, BucketStart: bucket, RxBytes: rx, TxBytes: tx})
	}
	return out, rows.Err()
}

// RollupTraffic sums "raw" rows older than before into "hourly" rows, upserting each
// hour (safe to re-run: it always recomputes the sum from whatever "raw" rows still
// exist for that hour).
func (s *Store) RollupTraffic(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO traffic (client_id, resolution, bucket_start, rx_bytes, tx_bytes)
		SELECT client_id, 'hourly', `+hourBucketOfTraffic+`, SUM(rx_bytes), SUM(tx_bytes)
		FROM traffic
		WHERE resolution = 'raw' AND bucket_start < ?1
		GROUP BY client_id, substr(bucket_start, 1, 13)
		ON CONFLICT (client_id, resolution, bucket_start) DO UPDATE SET
			rx_bytes = excluded.rx_bytes, tx_bytes = excluded.tx_bytes`,
		formatTime(before))
	return err
}

// PruneTraffic deletes rows at resolution older than before, and reports how many.
func (s *Store) PruneTraffic(ctx context.Context, resolution string, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM traffic WHERE resolution = ? AND bucket_start < ?`,
		resolution, formatTime(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
