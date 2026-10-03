package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stuffam/drawbridge/internal/store"
)

// logCapture is a log handler that keeps what it's given.
type logCapture struct {
	mu      *sync.Mutex
	records *[]logged
}

type logged struct {
	level slog.Level
	msg   string
	attrs map[string]string // groups flattened with dots, as the journal handler does
}

func newLogCapture() (*logCapture, func() []logged) {
	c := &logCapture{mu: &sync.Mutex{}, records: new([]logged)}
	return c, func() []logged {
		c.mu.Lock()
		defer c.mu.Unlock()
		return append([]logged(nil), *c.records...)
	}
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *logCapture) WithGroup(string) slog.Handler            { return c }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	l := logged{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	var add func(prefix string, a slog.Attr)
	add = func(prefix string, a slog.Attr) {
		if a.Value.Kind() == slog.KindGroup {
			for _, g := range a.Value.Group() {
				add(prefix+a.Key+".", g)
			}
			return
		}
		l.attrs[prefix+a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool { add("", a); return true })
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.records = append(*c.records, l)
	return nil
}

func TestEveryEventIsLoggedOnceWithItsFields(t *testing.T) {
	s, _ := newTestService(t)
	capture, records := newLogCapture()
	s.Log = slog.New(capture)
	ctx := web(context.Background(), "admin")

	c, _, err := s.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameClient(ctx, store.ByID(c.ID), "tablet"); err != nil {
		t.Fatal(err)
	}
	hostile := "=cmd|' /C calc'!A0\nPRIORITY=0"
	if _, err := s.Login(ctx, hostile, "guess", "test"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("Login err %v, want ErrBadLogin", err)
	}
	st, _ := s.Settings(context.Background())
	if err := s.WG.SetMTU(st.Interface, 1500); err != nil {
		t.Fatal(err)
	}
	s.Sync(context.Background())

	// One log record for each event in the log, and no other record about the same thing.
	events, err := s.Events(context.Background(), store.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var got []logged
	for _, r := range records() {
		if r.attrs["event"] != "" {
			got = append(got, r)
		} else if strings.HasPrefix(r.msg, "client ") || strings.HasPrefix(r.msg, "corrected") {
			t.Errorf("a log line besides the event's own: %q", r.msg)
		}
	}
	if len(got) != len(events) {
		t.Fatalf("%d event records for %d events: %+v", len(got), len(events), got)
	}
	byKind := map[string]logged{}
	for _, r := range got {
		byKind[r.attrs["event"]] = r
	}

	added := byKind["client.added"]
	for attr, want := range map[string]string{
		"category": "admin", "actor": "admin", "via": "web", "source_ip": "192.168.4.20",
		"client": "phone", "client_id": c.ID, "data.ipv4": c.IPv4.String(),
	} {
		if added.attrs[attr] != want {
			t.Errorf("client.added: %s = %q, want %q", attr, added.attrs[attr], want)
		}
	}
	if added.msg != "client added" || added.level != slog.LevelInfo {
		t.Errorf("client.added logged as %v %q, want INFO \"client added\"", added.level, added.msg)
	}

	// A rename is about the client under its new name, and says what it was.
	renamed := byKind["client.renamed"]
	if renamed.attrs["client"] != "tablet" || renamed.attrs["data.from"] != "phone" || renamed.msg != "client renamed" {
		t.Errorf("client.renamed logged as %+v", renamed)
	}

	// What goes wrong is a warning, and a stranger's text is a value, never part of the entry.
	failed := byKind["auth.login_failed"]
	if failed.level != slog.LevelWarn || failed.msg != "auth login failed" || failed.attrs["actor"] == "" {
		t.Errorf("auth.login_failed logged as %+v", failed)
	}
	if _, ok := failed.attrs["client"]; ok {
		t.Errorf("a login has no client, but the record has one: %+v", failed.attrs)
	}
	drift := byKind["tunnel.drift_corrected"]
	if drift.level != slog.LevelWarn || drift.attrs["category"] != "system" || drift.attrs["via"] != "system" ||
		!strings.Contains(drift.attrs["data.changes"], "MTU") {
		t.Errorf("tunnel.drift_corrected logged as %+v", drift)
	}
}

func TestAnEventIsLoggedEvenWhenTheDatabaseCantTakeIt(t *testing.T) {
	s, _ := newTestService(t)
	capture, records := newLogCapture()
	s.Log = slog.New(capture)
	_ = s.Store.Close()

	s.record(context.Background(), Event{Kind: "auth.logout"})
	var kinds []string
	for _, r := range records() {
		kinds = append(kinds, r.attrs["event"]+r.msg)
	}
	if got := strings.Join(kinds, "|"); !strings.Contains(got, "auth.logoutauth logout") || !strings.Contains(got, "can't record an event") {
		t.Fatalf("records %q: want the event, then the failure to store it", got)
	}
}
