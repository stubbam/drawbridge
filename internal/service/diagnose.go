package service

import (
	"context"
	"errors"
	"net/netip"
	"slices"

	"github.com/stuffam/drawbridge/internal/diag"
	"github.com/stuffam/drawbridge/internal/wg"
)

// ErrNoDiagnostics means the daemon has no host to run the diagnostics against.
var ErrNoDiagnostics = errors.New("this daemon can't run the diagnostics")

// Diagnose runs the host checks behind `drawbridge doctor` (docs/PLAN.md §6.6). It reads
// the host and the network and changes nothing, so it records no event.
func (s *Service) Diagnose(ctx context.Context) ([]diag.Check, error) {
	if s.Diag == nil {
		return nil, ErrNoDiagnostics
	}
	st, err := s.Store.Settings(ctx)
	if err != nil {
		return nil, err
	}
	in := diag.Input{
		Interface:    st.Interface,
		IPv4:         st.IPv4,
		IPv6:         st.IPv6,
		EndpointHost: st.EndpointHost,
		DNS:          st.DNS,
	}
	switch dev, err := s.WG.Device(st.Interface); {
	case err == nil:
		in.TunnelUp, in.Peers = true, len(dev.Peers)
	case !errors.Is(err, wg.ErrNoDevice):
		in.TunnelErr = err
	}
	if s.Rec != nil && s.Rec.Firewall != nil {
		if _, exists, err := s.Rec.Firewall.Revision(ctx); err != nil {
			in.FirewallErr = err
		} else {
			in.FirewallLoaded = exists
		}
	}

	// Only the server's own VPN addresses can be tested, and only when clients are told
	// to use them; a public resolver is the admin's or the internet's to keep up.
	srv, err := st.ServerAddrs()
	if err != nil {
		return nil, err
	}
	var own []netip.Addr
	for _, a := range []netip.Addr{srv.IPv4, srv.IPv6} {
		if a.IsValid() && slices.Contains(st.DNS, a) {
			own = append(own, a)
		}
	}
	for _, p := range s.probeAddrs(ctx, own) {
		in.DNSProbes = append(in.DNSProbes, diag.DNSResult{Address: p.Address, Answered: p.Answered, Detail: p.Detail})
	}
	return diag.Run(ctx, *s.Diag, in), nil
}
