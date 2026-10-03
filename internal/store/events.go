package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Event is one entry in the event log (docs/PLAN.md §6.4).
type Event struct {
	ID       int64
	Time     time.Time
	Kind     string
	Category string
	Actor    string
	Via      string
	SourceIP string
	// ClientID and ClientName are set when the event is about a client. The name is
	// the one the client had then.
	ClientID   string
	ClientName string
	Data       map[string]string
}

// AddEvent appends an event. A zero Time means now.
func (s *Store) AddEvent(ctx context.Context, e Event) error {
	if e.Time.IsZero() {
		e.Time = s.now()
	}
	data := []byte("{}")
	if len(e.Data) > 0 {
		var err error
		if data, err = json.Marshal(e.Data); err != nil {
			return err
		}
	}
	var clientID any
	if e.ClientID != "" {
		clientID = e.ClientID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO events (ts, kind, category, actor, via, source_ip,
		client_id, client_name, data) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		formatTime(e.Time), e.Kind, e.Category, e.Actor, e.Via, e.SourceIP, clientID, e.ClientName,
		string(data))
	return err
}

// EventFilter selects events. Zero fields don't filter.
type EventFilter struct {
	// Before returns only events older than the one with this ID, for paging.
	Before   int64
	ClientID string
	Category string
	// Kind selects one kind of event, such as "client.connected".
	Kind string
	// From and To bound the events' times: From inclusive, To exclusive.
	From, To time.Time
	// Limit caps the number of events; 0 means 100.
	Limit int
}

// Events returns events, newest first.
func (s *Store) Events(ctx context.Context, f EventFilter) ([]Event, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	// Each condition applies only when its filter is set. Scanning newest first by ID
	// and stopping at the limit is fast at the log's size. The stored times are all UTC and
	// the same width (timeFormat), so comparing them as text compares them as times.
	const query = `SELECT id, ts, kind, category, actor, via, source_ip, client_id, client_name, data
		FROM events
		WHERE (?1 = 0 OR id < ?1) AND (?2 = '' OR client_id = ?2) AND (?3 = '' OR category = ?3)
			AND (?4 = '' OR kind = ?4) AND (?5 = '' OR ts >= ?5) AND (?6 = '' OR ts < ?6)
		ORDER BY id DESC LIMIT ?7`
	var from, to string
	if !f.From.IsZero() {
		from = formatTime(f.From)
	}
	if !f.To.IsZero() {
		to = formatTime(f.To)
	}
	args := []any{f.Before, f.ClientID, f.Category, f.Kind, from, to, f.Limit}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			e        Event
			ts, data string
			clientID sql.NullString
		)
		if err := rows.Scan(&e.ID, &ts, &e.Kind, &e.Category, &e.Actor, &e.Via, &e.SourceIP,
			&clientID, &e.ClientName, &data); err != nil {
			return nil, err
		}
		if e.Time, err = time.Parse(time.RFC3339Nano, ts); err != nil {
			return nil, fmt.Errorf("event %d: %w", e.ID, err)
		}
		e.ClientID = clientID.String
		if err := json.Unmarshal([]byte(data), &e.Data); err != nil {
			return nil, fmt.Errorf("event %d: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
