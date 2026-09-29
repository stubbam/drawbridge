package auth

import (
	"net/netip"
	"sync"
	"time"
)

// Limiter slows down password guessing (docs/PLAN.md §6.5). Each key (a source address
// or an account) gets Free failed attempts; after that, each attempt must wait twice as
// long as the one before, from Base up to Max. A success, or Forget without a failure,
// clears the key.
type Limiter struct {
	Free   int
	Base   time.Duration
	Max    time.Duration
	Forget time.Duration
	Now    func() time.Time

	mu      sync.Mutex
	entries map[string]*limitEntry
}

type limitEntry struct {
	failures int
	last     time.Time
	until    time.Time
}

// NewLimiter returns a Limiter with the defaults: five free attempts, then waits of 2
// seconds doubling up to 15 minutes, forgotten after an hour without a failure.
func NewLimiter() *Limiter {
	return &Limiter{Free: 5, Base: 2 * time.Second, Max: 15 * time.Minute, Forget: time.Hour, Now: time.Now}
}

// maxEntries bounds memory. Keys come from the LAN and the VPN only (the allowlist),
// and IPv6 sources are grouped by /64, so the real number is small.
const maxEntries = 10000

// Wait returns how long the caller must wait before trying again: the longest wait of
// any of the keys, or 0.
func (l *Limiter) Wait(keys ...string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	var wait time.Duration
	for _, k := range keys {
		if e, ok := l.entries[k]; ok && e.until.After(now) {
			wait = max(wait, e.until.Sub(now))
		}
	}
	return wait
}

// Fail records a failed attempt for each key.
func (l *Limiter) Fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	if l.entries == nil {
		l.entries = map[string]*limitEntry{}
	}
	if len(l.entries) >= maxEntries {
		l.prune(now)
	}
	for _, k := range keys {
		e, ok := l.entries[k]
		if !ok || now.Sub(e.last) > l.Forget {
			e = &limitEntry{}
			l.entries[k] = e
		}
		e.failures++
		e.last = now
		if over := e.failures - l.Free; over > 0 {
			d := l.Max
			if over <= 30 && l.Base<<(over-1) < l.Max {
				d = l.Base << (over - 1)
			}
			e.until = now.Add(d)
		}
	}
}

// Succeed clears the keys.
func (l *Limiter) Succeed(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.entries, k)
	}
}

func (l *Limiter) prune(now time.Time) {
	for k, e := range l.entries {
		if now.Sub(e.last) > l.Forget && !e.until.After(now) {
			delete(l.entries, k)
		}
	}
	if len(l.entries) >= maxEntries {
		// Every entry is recent: someone is cycling through many sources. Keep limiting
		// the accounts, and start the sources afresh.
		for k := range l.entries {
			if len(k) > 3 && k[:3] == "ip:" {
				delete(l.entries, k)
			}
		}
	}
}

// SourceKey is the limiter key for a source address. IPv6 sources are grouped by /64,
// since anyone on a network can use any address in its /64.
func SourceKey(addr netip.Addr) string {
	addr = addr.Unmap()
	if addr.Is6() {
		p, _ := addr.Prefix(64)
		return "ip:" + p.String()
	}
	return "ip:" + addr.String()
}

// AccountKey is the limiter key for an account.
func AccountKey(userID string) string { return "user:" + userID }
