package service

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
)

// Where a change came from.
const (
	ViaWeb    = "web"
	ViaCLI    = "cli"
	ViaSystem = "system"
)

// Event categories (docs/PLAN.md §7).
const (
	CategoryAdmin      = "admin"
	CategorySystem     = "system"
	CategoryConnection = "connection"
)

// Actor is who asked for a change, for the event log.
type Actor struct {
	// Name is the admin's username, the CLI user's account name, or "drawbridge".
	Name string
	Via  string
	// SourceIP is the web client's address; empty for the CLI and the daemon itself.
	SourceIP string
}

type actorKey struct{}

// WithActor returns a context that attributes changes to a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the context's actor: by default, the daemon itself.
func ActorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return Actor{Name: "drawbridge", Via: ViaSystem}
}

// Event is an event to record. The actor comes from the context.
type Event struct {
	Kind string
	// Category defaults to admin.
	Category string
	Client   *model.Client
	Data     map[string]string
	// Actor overrides the context's actor's name, for failed logins, where there's no
	// logged-in user yet.
	Actor string
}

// record appends an event to the log. The change it describes has already happened, so
// a failure to record it is logged rather than returned.
func (s *Service) record(ctx context.Context, e Event) {
	a := ActorFrom(ctx)
	se := store.Event{
		Time:     s.now(),
		Kind:     e.Kind,
		Category: e.Category,
		Actor:    a.Name,
		Via:      a.Via,
		SourceIP: a.SourceIP,
		Data:     e.Data,
	}
	if se.Category == "" {
		se.Category = CategoryAdmin
	}
	if e.Actor != "" {
		se.Actor = e.Actor
	}
	if e.Client != nil {
		se.ClientID, se.ClientName = e.Client.ID, e.Client.Name
	}
	// The journal gets the event first, so it has it even when the database can't take it.
	s.logEvent(ctx, se)
	// The event outlives the request that caused it.
	if err := s.Store.AddEvent(context.WithoutCancel(ctx), se); err != nil {
		s.Log.Warn("can't record an event", "kind", e.Kind, "err", err)
	}
}

// logEvent writes an event to the log, for the journal (`journalctl -u drawbridge`). Its
// parts are attributes, which journald keeps as fields of their own (DRAWBRIDGE_EVENT,
// DRAWBRIDGE_CLIENT, and so on), so the journal can be filtered the way the event log is.
// Failures are warnings, and so is drift, because someone should read them.
func (s *Service) logEvent(ctx context.Context, e store.Event) {
	attrs := []slog.Attr{
		slog.String("event", e.Kind),
		slog.String("category", e.Category),
		slog.String("actor", e.Actor),
		slog.String("via", e.Via),
	}
	if e.SourceIP != "" {
		attrs = append(attrs, slog.String("source_ip", e.SourceIP))
	}
	if e.ClientName != "" {
		attrs = append(attrs, slog.String("client", e.ClientName))
	}
	if e.ClientID != "" {
		attrs = append(attrs, slog.String("client_id", e.ClientID))
	}
	if len(e.Data) > 0 {
		data := make([]slog.Attr, 0, len(e.Data))
		for _, k := range slices.Sorted(maps.Keys(e.Data)) {
			data = append(data, slog.String(k, e.Data[k]))
		}
		attrs = append(attrs, slog.Attr{Key: "data", Value: slog.GroupValue(data...)})
	}
	level := slog.LevelInfo
	if strings.HasSuffix(e.Kind, "_failed") || e.Kind == "tunnel.drift_corrected" {
		level = slog.LevelWarn
	}
	// "client.config_viewed" reads as "client config viewed".
	s.Log.LogAttrs(ctx, level, strings.NewReplacer(".", " ", "_", " ").Replace(e.Kind), attrs...)
}

// Events returns recorded events, newest first.
func (s *Service) Events(ctx context.Context, f store.EventFilter) ([]store.Event, error) {
	return s.Store.Events(ctx, f)
}

// eventPage is how many events EachEvent reads at a time.
const eventPage = 1000

// EachEvent calls fn with every event that matches f, newest first, one page at a time, so
// an export doesn't hold the whole log in memory. f's Limit doesn't apply.
func (s *Service) EachEvent(ctx context.Context, f store.EventFilter, fn func([]store.Event) error) error {
	f.Limit = eventPage
	for {
		page, err := s.Store.Events(ctx, f)
		if err != nil {
			return err
		}
		if len(page) > 0 {
			if err := fn(page); err != nil {
				return err
			}
		}
		if len(page) < eventPage {
			return nil
		}
		f.Before = page[len(page)-1].ID
	}
}
