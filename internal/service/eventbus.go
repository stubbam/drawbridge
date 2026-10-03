package service

import (
	"sync"
	"time"

	"github.com/stuffam/drawbridge/internal/store"
)

// subscriberBuffer is how many events a watcher can be behind by. Events happen at the pace of
// people and connections, so a watcher that's fallen this far behind is stuck, and loses the
// ones that don't fit; the web UI reloads what it shows each time its stream reconnects.
const subscriberBuffer = 64

// eventBus hands every recorded event to whoever's watching: the web UI's stream
// (docs/PLAN.md §8). Recording an event never waits for a watcher.
type eventBus struct {
	mu   sync.Mutex
	subs map[chan store.Event]struct{}
}

func (b *eventBus) subscribe() (<-chan store.Event, func()) {
	ch := make(chan store.Event, subscriberBuffer)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[chan store.Event]struct{}{}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *eventBus) publish(e store.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // a stuck watcher misses it, and the others don't wait for it
		}
	}
}

// Subscribe returns a channel of the events recorded from now on, with the IDs they have in
// the event log, and a function that stops the subscription. A subscriber that doesn't keep up
// misses events instead of holding anything up.
func (s *Service) Subscribe() (<-chan store.Event, func()) { return s.bus.subscribe() }

// StreamInterval is how often the stream pushes the status: as often as the daemon polls the
// peers, since nothing it shows changes faster.
func (s *Service) StreamInterval() time.Duration { return s.trackInterval() }
