package service

import (
	"sync"

	"github.com/stuffam/drawbridge/internal/store"
)

// sessionTotals holds each open session's latest endpoint and bytes, as of the last poll.
//
// The database gets them once per traffic flush (SaveMonitoring), and at once when a client
// roams, and not on every poll: a write for each connected client every five seconds is
// thousands of transactions an hour, which is what the SD card is spared (CLAUDE.md,
// "Protect the SD card"). A reader of an open session goes through overlay, so it still sees
// this poll's bytes and not the last flush's. A restart starts the totals empty, and the
// database's copy, at most a flush old, stands in until the next poll.
//
// The poll writes it and the API reads it, so it has a lock.
type sessionTotals struct {
	mu   sync.Mutex
	open map[string]*sessionTotal // by session ID
}

type sessionTotal struct {
	latest store.SessionBytes // as of the last poll
	saved  store.SessionBytes // what the database holds
}

// update records a poll's totals for a session. db is the session as the database has it, which
// is what's saved when this is the first the totals have heard of the session.
func (t *sessionTotals) update(db store.ClientSession, latest store.SessionBytes) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.open == nil {
		t.open = map[string]*sessionTotal{}
	}
	e, ok := t.open[latest.ID]
	if !ok {
		e = &sessionTotal{saved: store.SessionBytes{ID: db.ID, Endpoint: db.Endpoint, RxBytes: db.RxBytes, TxBytes: db.TxBytes}}
		t.open[latest.ID] = e
	}
	e.latest = latest
}

// markSaved notes that the database now holds these totals.
func (t *sessionTotals) markSaved(saved ...store.SessionBytes) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, sb := range saved {
		if e, ok := t.open[sb.ID]; ok {
			e.saved = sb
		}
	}
}

// unsaved returns the sessions whose totals differ from what the database holds.
func (t *sessionTotals) unsaved() []store.SessionBytes {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []store.SessionBytes
	for _, e := range t.open {
		if e.latest != e.saved {
			out = append(out, e.latest)
		}
	}
	return out
}

// drop forgets a session, which has ended.
func (t *sessionTotals) drop(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.open, id)
}

// keepOnly forgets every session but those in open, which a client's deletion can end without
// the poll seeing it close.
func (t *sessionTotals) keepOnly(open map[string]store.ClientSession) {
	t.mu.Lock()
	defer t.mu.Unlock()
	live := make(map[string]bool, len(open))
	for _, s := range open {
		live[s.ID] = true
	}
	for id := range t.open {
		if !live[id] {
			delete(t.open, id)
		}
	}
}

// overlay replaces an open session's endpoint and bytes with the latest poll's.
func (t *sessionTotals) overlay(s *store.ClientSession) {
	if s.EndedAt != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.open[s.ID]; ok {
		s.Endpoint, s.RxBytes, s.TxBytes = e.latest.Endpoint, e.latest.RxBytes, e.latest.TxBytes
	}
}
