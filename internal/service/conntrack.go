package service

import (
	"context"
	"strconv"
	"time"

	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/wg"
)

// TrackConnections polls every client's live peer and updates client_sessions
// (docs/PLAN.md §6.4): a session opens on a handshake and closes when the peer leaves
// the tunnel (paused, deleted, or the tunnel down) or goes idle for OnlineWithin —
// WireGuard has no "disconnect" signal, so a real device turning its tunnel off looks
// identical to it going quiet: the handshake simply stops advancing. Treating an idle
// peer as disconnected is what makes "this session" reset when a client actually
// reconnects, instead of only when Drawbridge itself removes the peer. Roaming (an
// endpoint change while still within the online window) is logged but doesn't end the
// session. A failure is logged, like Sync.
//
// This is also where traffic history is sampled (SampleTraffic, traffic.go): it reuses
// this call's wgctrl read instead of polling a second time.
func (s *Service) TrackConnections(ctx context.Context) {
	clients, err := s.Store.Clients(ctx)
	if err != nil {
		s.Log.Warn("can't track connections", "err", err)
		return
	}
	peers, sessions := s.peers(ctx), s.sessions(ctx)
	if sessions != nil { // nil is a failed read, which says nothing about what's open
		s.sessionLive.keepOnly(sessions)
	}
	s.SampleTraffic(ctx, clients, peers)
	for _, c := range clients {
		open, hasSession := sessions[c.ID]
		p, connected := peers[c.PublicKey.String()]
		if !connected {
			if hasSession {
				s.closeSession(ctx, c, open, open.RxBytes, open.TxBytes)
			}
			continue
		}
		s.trackPeer(ctx, c, p, open, hasSession)
	}
}

func (s *Service) trackPeer(ctx context.Context, c model.Client, p wg.Peer, open store.ClientSession, hasSession bool) {
	endpoint := ""
	if p.Endpoint.IsValid() {
		endpoint = p.Endpoint.String()
	}
	if hasSession && (p.ReceiveBytes < open.BaselineRx+open.RxBytes || p.SendBytes < open.BaselineTx+open.TxBytes) {
		// The peer's counters went backward: the interface or peer was recreated
		// outside of a pause, so Drawbridge never saw it disappear. End the session
		// here; a fresh handshake opens a new one below.
		s.closeSession(ctx, c, open, open.RxBytes, open.TxBytes)
		hasSession = false
	}
	online := !p.LastHandshake.IsZero() && s.now().Sub(p.LastHandshake) < OnlineWithin
	if hasSession && !online {
		// No handshake in a while: the same heuristic the Online/Idle badge uses,
		// reused here as the session boundary. This can't flap between ticks: as
		// long as no newer handshake arrives, the peer's LastHandshake doesn't
		// change, so it never becomes "online" again on its own.
		s.closeSession(ctx, c, open, open.RxBytes, open.TxBytes)
		return
	}
	if !hasSession {
		if !online {
			return
		}
		ns, err := s.Store.OpenClientSession(ctx, c.ID, endpoint, p.ReceiveBytes, p.SendBytes)
		if err != nil {
			s.Log.Warn("can't open a client session", "client", c.Name, "err", err)
			return
		}
		s.record(ctx, Event{Kind: "client.connected", Category: CategoryConnection, Client: &c})
		open = ns
	}
	rx, tx := p.ReceiveBytes-open.BaselineRx, p.SendBytes-open.BaselineTx
	latest := store.SessionBytes{ID: open.ID, Endpoint: endpoint, RxBytes: rx, TxBytes: tx}
	s.sessionLive.update(open, latest)
	if endpoint != open.Endpoint {
		s.record(ctx, Event{Kind: "client.roamed", Category: CategoryConnection, Client: &c,
			Data: map[string]string{"from": orNone(open.Endpoint), "to": orNone(endpoint)}})
		// A roam is rare, and a restart mustn't announce it twice, so it's saved at once. The
		// bytes of every poll in between wait for the next flush (sessionTotals).
		if err := s.Store.UpdateClientSession(ctx, open.ID, endpoint, rx, tx); err != nil {
			s.Log.Warn("can't update a client session", "client", c.Name, "err", err)
		} else {
			s.sessionLive.markSaved(latest)
		}
	}
}

func (s *Service) closeSession(ctx context.Context, c model.Client, open store.ClientSession, rx, tx int64) {
	if err := s.Store.CloseClientSession(ctx, open.ID, rx, tx); err != nil {
		s.Log.Warn("can't close a client session", "client", c.Name, "err", err)
		return
	}
	s.sessionLive.drop(open.ID)
	s.record(ctx, Event{Kind: "client.disconnected", Category: CategoryConnection, Client: &c, Data: map[string]string{
		"duration":      s.now().Sub(open.StartedAt).Round(time.Second).String(),
		"receive_bytes": strconv.FormatInt(rx, 10),
		"send_bytes":    strconv.FormatInt(tx, 10),
	}})
}

// ClientSessionHistory returns one client's connection history, newest first (open and
// closed sessions alike).
func (s *Service) ClientSessionHistory(ctx context.Context, ref store.Ref, before time.Time, limit int) ([]store.ClientSession, error) {
	c, err := s.Store.Client(ctx, ref)
	if err != nil {
		return nil, err
	}
	out, err := s.Store.ClientSessions(ctx, c.ID, before, limit)
	for i := range out {
		s.sessionLive.overlay(&out[i])
	}
	return out, err
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
