// Package service is Drawbridge's management logic, shared by the control socket (the
// CLI) and, from M2, the web API. Every change follows the same path: validate, save to
// the database, then reconcile (docs/PLAN.md §4.3).
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/stuffam/drawbridge/internal/auth"
	"github.com/stuffam/drawbridge/internal/clientconf"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/reconcile"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/wg"
)

// Service manages the server and its clients.
type Service struct {
	Store *store.Store
	Rec   *reconcile.Reconciler
	// WG is read for live peer status. It's the reconciler's backend.
	WG  wg.Backend
	Log *slog.Logger

	// Hasher, Limiter, and Sessions are for the admin login (auth.go). Hasher and
	// Limiter must be set before the login methods are used.
	Hasher   *auth.Hasher
	Limiter  *auth.Limiter
	Sessions SessionPolicy
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	// TrafficRawInterval, TrafficRawRetention, and TrafficHourlyRetention configure the
	// traffic-history sampler (traffic.go); zero means the Default* constant there. They
	// exist so a host on an SD card can keep the conservative defaults while one on an
	// NVMe SSD can afford a finer interval or longer retention.
	TrafficRawInterval, TrafficRawRetention, TrafficHourlyRetention time.Duration
	// trafficBuf is the in-memory buffer SampleTraffic accumulates into between flushes.
	// It's runtime state, not a dependency, and is touched only from the connTrackLoop
	// goroutine, so it needs no lock.
	trafficBuf *trafficBuffer
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Applied reports what happened when a saved change was applied.
type Applied struct {
	// TunnelDown means the tunnel isn't running, so the change takes effect when
	// drawbridge-tunnel.service starts.
	TunnelDown bool
	// Err is set when applying failed. The change is saved, and the daemon retries
	// every 30 seconds.
	Err error
}

// Warning returns a message for people, or "" when the change applied cleanly.
func (a Applied) Warning() string {
	switch {
	case a.Err != nil:
		return fmt.Sprintf("the change is saved, but applying it failed (the daemon retries every 30 seconds): %v", a.Err)
	case a.TunnelDown:
		return "the change is saved; the tunnel is stopped, so it applies when drawbridge-tunnel.service starts"
	}
	return ""
}

func (s *Service) apply(ctx context.Context, what string) Applied {
	res, err := s.Rec.Sync(ctx)
	if len(res.Changes) > 0 {
		s.Log.Info("applied "+what, "changes", res.Changes)
	}
	if err != nil {
		s.Log.Error("applying "+what+" failed", "err", err)
	}
	return Applied{TunnelDown: res.TunnelDown, Err: err}
}

// Settings returns the server's settings.
func (s *Service) Settings(ctx context.Context) (model.Settings, error) {
	return s.Store.Settings(ctx)
}

// SettingsPatch changes some settings. Nil fields stay as they are.
type SettingsPatch struct {
	EndpointHost    *string
	EndpointPort    *uint16
	ListenPort      *uint16
	MTU             *int
	DNS             *[]netip.Addr
	DNSDefault      bool
	Keepalive       *int
	ClientIsolation *bool
	AdminAllowed    *[]netip.Prefix
}

// UpdateSettings applies a patch.
func (s *Service) UpdateSettings(ctx context.Context, p SettingsPatch) (model.Settings, Applied, error) {
	var before model.Settings
	updated, err := s.Store.UpdateSettings(ctx, func(st *model.Settings) error {
		before = *st
		if p.EndpointHost != nil {
			st.EndpointHost = model.NormalizeHost(*p.EndpointHost)
		}
		if p.EndpointPort != nil {
			st.EndpointPort = *p.EndpointPort
		}
		if p.ListenPort != nil {
			st.ListenPort = *p.ListenPort
		}
		if p.MTU != nil {
			st.MTU = *p.MTU
		}
		if p.DNSDefault {
			srv, err := st.ServerAddrs()
			if err != nil {
				return err
			}
			st.DNS = []netip.Addr{srv.IPv4}
			if srv.IPv6.IsValid() {
				st.DNS = append(st.DNS, srv.IPv6)
			}
		} else if p.DNS != nil {
			st.DNS = *p.DNS
		}
		if p.Keepalive != nil {
			st.Keepalive = *p.Keepalive
		}
		if p.ClientIsolation != nil {
			st.ClientIsolation = *p.ClientIsolation
		}
		if p.AdminAllowed != nil {
			st.AdminAllowed = model.NormalizePrefixes(*p.AdminAllowed)
		}
		return nil
	})
	if err != nil {
		return model.Settings{}, Applied{}, err
	}
	changes := settingsChanges(before, updated)
	if len(changes) == 0 {
		return updated, Applied{}, nil
	}
	s.Log.Info("server settings changed")
	s.record(ctx, Event{Kind: "server.settings_changed", Data: changes})
	return updated, s.apply(ctx, "server settings"), nil
}

// settingsChanges describes what changed, for the event log: "old → new" for each
// setting a patch can change.
func settingsChanges(a, b model.Settings) map[string]string {
	out := map[string]string{}
	show := func(v any) string {
		if s := fmt.Sprint(v); s != "" && s != "[]" {
			return s
		}
		return "none"
	}
	diff := func(name string, x, y any) {
		if xs, ys := show(x), show(y); xs != ys {
			out[name] = xs + " → " + ys
		}
	}
	diff("endpoint_host", a.EndpointHost, b.EndpointHost)
	diff("endpoint_port", a.EndpointPort, b.EndpointPort)
	diff("listen_port", a.ListenPort, b.ListenPort)
	diff("mtu", a.MTU, b.MTU)
	diff("dns", a.DNS, b.DNS)
	diff("keepalive", a.Keepalive, b.Keepalive)
	diff("client_isolation", a.ClientIsolation, b.ClientIsolation)
	diff("admin_allowed", a.AdminAllowed, b.AdminAllowed)
	return out
}

// OnlineWithin is how recent a client's last handshake must be for it to count as
// online. WireGuard rekeys every two minutes while traffic flows.
const OnlineWithin = 3 * time.Minute

// Status is the tunnel's state at a glance.
type Status struct {
	// TunnelUp means the interface exists.
	TunnelUp bool
	Clients  int
	Paused   int
	// Online counts clients with a handshake within OnlineWithin.
	Online int
}

// Status returns the tunnel's state and client counts.
func (s *Service) Status(ctx context.Context) (Status, error) {
	st, err := s.Store.Settings(ctx)
	if err != nil {
		return Status{}, err
	}
	var out Status
	if _, err := s.WG.Device(st.Interface); err == nil {
		out.TunnelUp = true
	} else if !errors.Is(err, wg.ErrNoDevice) {
		return Status{}, err
	}
	clients, err := s.Clients(ctx)
	if err != nil {
		return Status{}, err
	}
	now := s.now()
	for _, c := range clients {
		out.Clients++
		if !c.Enabled {
			out.Paused++
		}
		if c.Peer != nil && !c.Peer.LastHandshake.IsZero() && now.Sub(c.Peer.LastHandshake) < OnlineWithin {
			out.Online++
		}
	}
	return out, nil
}

// ClientStatus is a client and its live peer status.
type ClientStatus struct {
	model.Client
	// Peer is nil when the client has no peer in the tunnel: it's paused, or the
	// tunnel is down.
	Peer *wg.Peer
	// Session is the client's open connection, if it has one (internal/service/conntrack.go).
	Session *store.ClientSession
}

// Clients returns every client with its live status.
func (s *Service) Clients(ctx context.Context) ([]ClientStatus, error) {
	clients, err := s.Store.Clients(ctx)
	if err != nil {
		return nil, err
	}
	peers := s.peers(ctx)
	sessions := s.sessions(ctx)
	out := make([]ClientStatus, len(clients))
	for i, c := range clients {
		out[i] = ClientStatus{Client: c}
		if p, ok := peers[c.PublicKey.String()]; ok {
			out[i].Peer = &p
		}
		if cs, ok := sessions[c.ID]; ok {
			out[i].Session = &cs
		}
	}
	return out, nil
}

// Client returns one client with its live status.
func (s *Service) Client(ctx context.Context, ref store.Ref) (ClientStatus, error) {
	c, err := s.Store.Client(ctx, ref)
	if err != nil {
		return ClientStatus{}, err
	}
	cs := ClientStatus{Client: c}
	if p, ok := s.peers(ctx)[c.PublicKey.String()]; ok {
		cs.Peer = &p
	}
	if sess, ok := s.sessions(ctx)[c.ID]; ok {
		cs.Session = &sess
	}
	return cs, nil
}

// sessions returns every client's open session, keyed by client ID. A read failure is
// logged and treated as no sessions open, the same as peers().
func (s *Service) sessions(ctx context.Context) map[string]store.ClientSession {
	out, err := s.Store.CurrentClientSessions(ctx)
	if err != nil {
		s.Log.Warn("can't read open client sessions", "err", err)
		return nil
	}
	return out
}

func (s *Service) peers(ctx context.Context) map[string]wg.Peer {
	st, err := s.Store.Settings(ctx)
	if err != nil {
		return nil
	}
	dev, err := s.WG.Device(st.Interface)
	if err != nil {
		if !errors.Is(err, wg.ErrNoDevice) {
			s.Log.Warn("can't read peer status", "err", err)
		}
		return nil
	}
	out := make(map[string]wg.Peer, len(dev.Peers))
	for _, p := range dev.Peers {
		out[p.PublicKey.String()] = p
	}
	return out
}

// AddClient creates a client and adds it to the tunnel.
func (s *Service) AddClient(ctx context.Context, name string) (model.Client, Applied, error) {
	c, err := s.Store.AddClient(ctx, name)
	if err != nil {
		return model.Client{}, Applied{}, err
	}
	s.Log.Info("client added", "client", c.Name, "ipv4", c.IPv4)
	s.record(ctx, Event{Kind: "client.added", Client: &c, Data: map[string]string{
		"ipv4": c.IPv4.String(), "ipv6": addrString(c.IPv6),
	}})
	return c, s.apply(ctx, "the new client"), nil
}

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// SetEnabled pauses (false) or resumes (true) a client.
func (s *Service) SetEnabled(ctx context.Context, ref store.Ref, enabled bool) (model.Client, Applied, error) {
	c, err := s.Store.SetEnabled(ctx, ref, enabled)
	if err != nil {
		return model.Client{}, Applied{}, err
	}
	what := "paused"
	if enabled {
		what = "resumed"
	}
	s.Log.Info("client "+what, "client", c.Name)
	s.record(ctx, Event{Kind: "client." + what, Client: &c})
	return c, s.apply(ctx, "the "+what+" client"), nil
}

// RenameClient renames a client. Names never reach the kernel, so nothing is applied.
func (s *Service) RenameClient(ctx context.Context, ref store.Ref, name string) (model.Client, error) {
	before, err := s.Store.Client(ctx, ref)
	if err != nil {
		return model.Client{}, err
	}
	c, err := s.Store.RenameClient(ctx, store.ByID(before.ID), name)
	if err != nil {
		return model.Client{}, err
	}
	if c.Name != before.Name {
		s.Log.Info("client renamed", "from", before.Name, "to", c.Name)
		s.record(ctx, Event{Kind: "client.renamed", Client: &c, Data: map[string]string{"from": before.Name}})
	}
	return c, nil
}

// DeleteClient deletes a client and removes it from the tunnel.
func (s *Service) DeleteClient(ctx context.Context, ref store.Ref) (model.Client, Applied, error) {
	c, err := s.Store.DeleteClient(ctx, ref)
	if err != nil {
		return model.Client{}, Applied{}, err
	}
	s.Log.Info("client deleted", "client", c.Name)
	s.record(ctx, Event{Kind: "client.deleted", Client: &c})
	return c, s.apply(ctx, "the deleted client"), nil
}

// Config renders a client's WireGuard config. It holds the client's private key, so
// each view is recorded (docs/PLAN.md §10).
func (s *Service) Config(ctx context.Context, ref store.Ref) (model.Client, string, error) {
	st, err := s.Store.Settings(ctx)
	if err != nil {
		return model.Client{}, "", err
	}
	c, err := s.Store.Client(ctx, ref)
	if err != nil {
		return model.Client{}, "", err
	}
	conf, err := clientconf.Render(st, c)
	if err != nil {
		return model.Client{}, "", err
	}
	s.record(ctx, Event{Kind: "client.config_viewed", Client: &c})
	return c, conf, nil
}

// Sync runs one reconcile, logging and recording any drift it corrected. The daemon
// runs it at startup and every 30 seconds.
func (s *Service) Sync(ctx context.Context) {
	res, err := s.Rec.Sync(ctx)
	switch {
	case err != nil:
		s.Log.Error("reconcile failed", "err", err)
	case len(res.Changes) > 0:
		s.Log.Warn("corrected drift", "changes", res.Changes)
		s.record(ctx, Event{Kind: "tunnel.drift_corrected", Category: CategorySystem,
			Data: map[string]string{"changes": strings.Join(res.Changes, "; ")}})
	}
}
