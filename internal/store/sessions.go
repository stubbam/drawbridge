package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ClientSession is one of a client's connections (docs/PLAN.md §6.4): from its first
// handshake to when its peer leaves the tunnel. RxBytes and TxBytes are this session's
// own bytes (the peer's cumulative counters minus BaselineRx/BaselineTx), not the raw
// counters, so a restart doesn't need any in-memory state to keep reporting them
// correctly.
type ClientSession struct {
	ID         string
	ClientID   string
	StartedAt  time.Time
	EndedAt    *time.Time
	Endpoint   string
	BaselineRx int64
	BaselineTx int64
	RxBytes    int64
	TxBytes    int64
}

// OpenClientSession starts a session for a client, with the peer's current cumulative
// counters as its baseline. It fails if one is already open for the client (the
// client_sessions_one_open index).
func (s *Store) OpenClientSession(ctx context.Context, clientID, endpoint string, baselineRx, baselineTx int64) (ClientSession, error) {
	id, err := newID()
	if err != nil {
		return ClientSession{}, err
	}
	cs := ClientSession{ID: id, ClientID: clientID, StartedAt: s.now(), Endpoint: endpoint,
		BaselineRx: baselineRx, BaselineTx: baselineTx}
	_, err = s.db.ExecContext(ctx, `INSERT INTO client_sessions
		(id, client_id, started_at, endpoint, baseline_rx, baseline_tx, rx_bytes, tx_bytes)
		VALUES (?, ?, ?, ?, ?, ?, 0, 0)`,
		cs.ID, cs.ClientID, s.timestamp(), cs.Endpoint, cs.BaselineRx, cs.BaselineTx)
	if err != nil {
		return ClientSession{}, err
	}
	return cs, nil
}

// SessionBytes is an open session's endpoint and its bytes so far.
type SessionBytes struct {
	ID       string
	Endpoint string
	RxBytes  int64
	TxBytes  int64
}

const updateSessionSQL = `UPDATE client_sessions SET endpoint = ?, rx_bytes = ?, tx_bytes = ?
	WHERE id = ? AND ended_at IS NULL`

// UpdateClientSession updates an open session's endpoint (a roam) and its bytes so far.
func (s *Store) UpdateClientSession(ctx context.Context, id, endpoint string, rxBytes, txBytes int64) error {
	_, err := s.db.ExecContext(ctx, updateSessionSQL, endpoint, rxBytes, txBytes, id)
	return err
}

// CloseClientSession ends a session with its final bytes.
func (s *Store) CloseClientSession(ctx context.Context, id string, rxBytes, txBytes int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE client_sessions SET ended_at = ?, rx_bytes = ?,
		tx_bytes = ? WHERE id = ? AND ended_at IS NULL`, s.timestamp(), rxBytes, txBytes, id)
	return err
}

// ClientSessions returns one client's connection history, newest first: open and closed
// sessions alike, unlike CurrentClientSessions. A zero before returns the most recent
// page; otherwise it pages by started_at, the same cursor style as Events' before.
// A limit of 0 or less means 100.
func (s *Store) ClientSessions(ctx context.Context, clientID string, before time.Time, limit int) ([]ClientSession, error) {
	if limit <= 0 {
		limit = 100
	}
	beforeStr := ""
	if !before.IsZero() {
		beforeStr = formatTime(before)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, client_id, started_at, ended_at, endpoint,
		baseline_rx, baseline_tx, rx_bytes, tx_bytes FROM client_sessions
		WHERE client_id = ?1 AND (?2 = '' OR started_at < ?2)
		ORDER BY started_at DESC LIMIT ?3`, clientID, beforeStr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClientSession
	for rows.Next() {
		var (
			cs         ClientSession
			startedTs  string
			endedTsRaw sql.NullString
		)
		if err := rows.Scan(&cs.ID, &cs.ClientID, &startedTs, &endedTsRaw, &cs.Endpoint,
			&cs.BaselineRx, &cs.BaselineTx, &cs.RxBytes, &cs.TxBytes); err != nil {
			return nil, err
		}
		if cs.StartedAt, err = time.Parse(timeFormat, startedTs); err != nil {
			return nil, fmt.Errorf("client session %s: %w", cs.ID, err)
		}
		if endedTsRaw.Valid {
			t, err := time.Parse(timeFormat, endedTsRaw.String)
			if err != nil {
				return nil, fmt.Errorf("client session %s: %w", cs.ID, err)
			}
			cs.EndedAt = &t
		}
		out = append(out, cs)
	}
	return out, rows.Err()
}

// CurrentClientSessions returns every open session, keyed by client ID.
func (s *Store) CurrentClientSessions(ctx context.Context) (map[string]ClientSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, client_id, started_at, endpoint,
		baseline_rx, baseline_tx, rx_bytes, tx_bytes FROM client_sessions WHERE ended_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ClientSession{}
	for rows.Next() {
		var (
			cs ClientSession
			ts string
		)
		if err := rows.Scan(&cs.ID, &cs.ClientID, &ts, &cs.Endpoint, &cs.BaselineRx,
			&cs.BaselineTx, &cs.RxBytes, &cs.TxBytes); err != nil {
			return nil, err
		}
		if cs.StartedAt, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			return nil, fmt.Errorf("client session %s: %w", cs.ID, err)
		}
		out[cs.ClientID] = cs
	}
	return out, rows.Err()
}
