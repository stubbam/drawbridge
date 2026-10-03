package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/views"
)

// message is one Server-Sent Event, as a browser's EventSource sees it.
type message struct{ event, id, data string }

// stream is an open /api/stream, read the way EventSource does.
type stream struct {
	t      *testing.T
	body   interface{ Close() error }
	header http.Header
	msgs   chan message
	cancel context.CancelFunc
}

// openStream opens the stream as the browser b, and returns it with its response status.
func openStream(t *testing.T, b *browser) (*stream, int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", b.srv.URL+"/api/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := b.http.Do(req) //nolint:bodyclose // (*stream).close closes it, and a cleanup runs that
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &stream{t: t, body: resp.Body, header: resp.Header, msgs: make(chan message, 64), cancel: cancel}
	t.Cleanup(s.close)
	if resp.StatusCode != http.StatusOK {
		s.close()
		close(s.msgs)
		return s, resp.StatusCode
	}
	go func() {
		defer close(s.msgs)
		sc := bufio.NewScanner(resp.Body)
		var m message
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if m.event != "" { // a message that is only a retry has no event
					s.msgs <- m
				}
				m = message{}
			case strings.HasPrefix(line, "event: "):
				m.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				m.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: "):
				m.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return s, resp.StatusCode
}

func (s *stream) close() {
	s.cancel()
	_ = s.body.Close()
}

// next returns the next message, or fails if none comes.
func (s *stream) next(within time.Duration) message {
	s.t.Helper()
	select {
	case m, ok := <-s.msgs:
		if !ok {
			s.t.Fatal("the stream closed")
		}
		return m
	case <-time.After(within):
		s.t.Fatalf("no message in %s", within)
		return message{}
	}
}

// until reads messages until one is of the named event.
func (s *stream) until(event string, within time.Duration) message {
	s.t.Helper()
	deadline := time.Now().Add(within)
	for {
		m := s.next(time.Until(deadline))
		if m.event == event {
			return m
		}
	}
}

// closed reports whether the server ends the stream within the time.
func (s *stream) closed(within time.Duration) bool {
	timeout := time.After(within)
	for {
		select {
		case _, ok := <-s.msgs:
			if !ok {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

func TestStreamPushesStatusAndEvents(t *testing.T) {
	svc := newService(t)
	svc.TrackInterval = 50 * time.Millisecond
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)

	s, status := openStream(t, b)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if got := s.header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := s.header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}

	// A status as it opens: the tunnel, and nobody yet.
	first := s.next(2 * time.Second)
	var st views.StreamStatus
	if first.event != "status" || first.id != "" {
		t.Fatalf("first message %+v, want a status", first)
	}
	mustJSON(t, first.data, &st)
	if !st.Server.TunnelUp || st.Server.Clients != 0 || len(st.Clients) != 0 {
		t.Fatalf("first status %+v", st)
	}

	// A change made over the API is an event on the stream, with the ID it has in the log,
	// and then a status that has it.
	var created views.ClientResult
	b.expect(http.StatusCreated, "POST", "/api/clients", views.NewClientRequest{Name: "phone"}).decode(t, &created)
	ev := s.until("event", 2*time.Second)
	var got views.EventView
	mustJSON(t, ev.data, &got)
	if got.Kind != "client.added" || got.ClientName != "phone" || got.ClientID != created.Client.ID ||
		got.Actor != "admin" || got.Via != "web" || ev.id != strconv.FormatInt(got.ID, 10) || got.ID == 0 {
		t.Fatalf("event %+v (message id %q)", got, ev.id)
	}
	var logged []views.EventView
	b.expect(http.StatusOK, "GET", "/api/events?client="+created.Client.ID, nil).decode(t, &logged)
	if len(logged) != 1 || logged[0].ID != got.ID {
		t.Fatalf("the log has %+v, the stream said %+v", logged, got)
	}
	var after views.StreamStatus
	deadline := time.Now().Add(2 * time.Second)
	for len(after.Clients) == 0 {
		m := s.until("status", time.Until(deadline))
		mustJSON(t, m.data, &after)
	}
	if after.Server.Clients != 1 || after.Clients[0].Name != "phone" {
		t.Fatalf("status after the change %+v", after)
	}

	// And it keeps coming without one.
	s.until("status", time.Second)
	s.until("status", time.Second)
}

func TestStreamNeedsASession(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	if _, status := openStream(t, newBrowser(t, srv)); status != http.StatusUnauthorized {
		t.Fatalf("status %d without a session, want 401", status)
	}
}

func TestStreamEndsWithItsSession(t *testing.T) {
	svc := newService(t)
	svc.TrackInterval = 50 * time.Millisecond
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)

	s, _ := openStream(t, b)
	s.next(2 * time.Second)
	b.expect(http.StatusNoContent, "POST", "/api/auth/logout", nil)
	if !s.closed(2 * time.Second) {
		t.Fatal("the stream stayed open after its session ended")
	}
	if _, status := openStream(t, b); status != http.StatusUnauthorized {
		t.Fatalf("a new stream got %d, want 401", status)
	}
}

func TestStreamsAreLimitedAndLetGo(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)

	var open []*stream
	for range maxStreams {
		s, status := openStream(t, b)
		if status != http.StatusOK {
			t.Fatalf("stream %d: status %d", len(open)+1, status)
		}
		s.next(2 * time.Second)
		open = append(open, s)
	}
	if _, status := openStream(t, b); status != http.StatusServiceUnavailable {
		t.Fatalf("stream %d got %d, want 503", maxStreams+1, status)
	}
	// One that goes away makes room.
	open[0].close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s, status := openStream(t, b)
		if status == http.StatusOK {
			s.next(2 * time.Second)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %d after a stream closed", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStreamsEndWhenTheDaemonStops(t *testing.T) {
	svc := newService(t)
	stopping := make(chan struct{})
	srv := httptest.NewTLSServer(New(Options{UI: builtUI, Service: svc, Shutdown: stopping}))
	t.Cleanup(srv.Close)
	b, _ := loggedIn(t, svc, srv)

	s, _ := openStream(t, b)
	s.next(2 * time.Second)
	close(stopping)
	if !s.closed(2 * time.Second) {
		t.Fatal("the stream stayed open after the daemon began to stop")
	}
}

func mustJSON(t *testing.T, data string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(data), v); err != nil {
		t.Fatalf("%q: %v", data, err)
	}
}
