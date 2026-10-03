package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/views"
)

const (
	// maxStreams bounds the open streams: each holds a connection and a goroutine, and a page
	// that opened them in a loop could otherwise hold the daemon's connections. An admin has a
	// stream or two (one per visible tab).
	maxStreams = 16
	// streamWriteTimeout is how long one message may take to leave. The server has no write
	// timeout, which would cut every stream at once (docs/adr/0009-server-sent-events.md), so
	// each write has its own, and a reader that has stopped is dropped.
	streamWriteTimeout = 10 * time.Second
	// streamRefresh is how long after an event the status is pushed again. A change is
	// recorded before it's applied to the tunnel, so the status waits a moment to show it, and
	// a burst of events costs one status.
	streamRefresh = 300 * time.Millisecond
	// streamRetry is the delay, in milliseconds, that a browser waits before it reconnects.
	streamRetry = 3000
)

// stream is the live feed of the web UI (docs/PLAN.md §8), as Server-Sent Events:
//
//   - status, as the stream opens and then every poll: the server's state and every client's
//     status (views.StreamStatus), which the pages would otherwise ask for every few seconds.
//   - event, as each event is recorded: the event as GET /api/events returns it, with its ID as
//     the message's id. A status follows it shortly.
//
// The session is checked on every status, which also counts as use, so a page that stays open
// and visible stays logged in, as it did when it polled. When the session ends the stream
// closes, and the browser's reconnect finds out why.
func (h *handler) stream(w http.ResponseWriter, r *http.Request) {
	if h.streams.Add(1) > maxStreams {
		h.streams.Add(-1)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "too many open streams"})
		return
	}
	defer h.streams.Add(-1)

	token := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		token = c.Value
	}
	rc := http.NewResponseController(w)
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	send := func(msg []byte) error {
		_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
		if _, err := w.Write(msg); err != nil {
			return err
		}
		return rc.Flush()
	}
	message := func(name, id string, v any) []byte {
		data, err := json.Marshal(v)
		if err != nil {
			return nil // views marshal; nothing here can fail to
		}
		var b bytes.Buffer
		fmt.Fprintf(&b, "event: %s\n", name)
		if id != "" {
			fmt.Fprintf(&b, "id: %s\n", id)
		}
		fmt.Fprintf(&b, "data: %s\n\n", data)
		return b.Bytes()
	}
	sendStatus := func() error {
		st, clients, err := h.svc.Snapshot(r.Context())
		if err != nil {
			// A read that fails now may not next time, and the stream is worth keeping.
			h.log.Warn("can't read the status for a stream", "err", err)
			return nil
		}
		return send(message("status", "", views.NewStreamStatus(st, clients)))
	}

	// Subscribe before the first status, so that no event falls between them.
	events, unsubscribe := h.svc.Subscribe()
	defer unsubscribe()
	if err := send([]byte(fmt.Sprintf("retry: %d\n\n", streamRetry))); err != nil {
		return
	}
	if err := sendStatus(); err != nil {
		return
	}

	tick := time.NewTicker(h.svc.StreamInterval())
	defer tick.Stop()
	var refresh <-chan time.Time
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.shutdown:
			return
		case <-tick.C:
			if !h.stillLoggedIn(r.Context(), token) {
				return
			}
			if err := sendStatus(); err != nil {
				return
			}
		case e := <-events:
			if err := send(message("event", fmt.Sprint(e.ID), views.Events([]store.Event{e})[0])); err != nil {
				return
			}
			if refresh == nil {
				refresh = time.After(streamRefresh)
			}
		case <-refresh:
			refresh = nil
			if err := sendStatus(); err != nil {
				return
			}
		}
	}
}

// stillLoggedIn is whether the stream's session is still good. Asking counts as use.
func (h *handler) stillLoggedIn(ctx context.Context, token string) bool {
	_, _, err := h.svc.Authenticate(ctx, token)
	if err != nil && !errors.Is(err, store.ErrNoSession) {
		h.log.Warn("can't check a stream's session", "err", err)
		return true // the check failed, which says nothing about the session
	}
	return err == nil
}
