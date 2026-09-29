// Package lan finds the home network's subnets and builds the admin UI's allowlist
// (docs/PLAN.md §6.5): loopback, link-local, the VPN subnets, and the LAN, which is the
// on-link subnets of the interfaces that carry the default routes. The allowlist doesn't
// just trust "private ranges", because LAN devices often reach the host over the LAN's
// global IPv6 prefix.
package lan

import (
	"net/netip"
	"slices"
	"sync"
	"time"
)

// Always-allowed sources: the host itself, and link-local neighbors, which can only be on
// a directly attached network.
var alwaysAllowed = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fe80::/10"),
}

// Allowlist returns the sources that may reach the admin UI: always-allowed ones, the
// VPN subnets, and the LAN's. The result is normalized: masked, sorted, with no prefix
// inside another, which nftables interval sets need.
func Allowlist(vpn, lan []netip.Prefix) []netip.Prefix {
	all := slices.Concat(alwaysAllowed, vpn, lan)
	return Normalize(all)
}

// Normalize masks the prefixes, drops invalid ones and IPv4-mapped IPv6 ones, and
// removes any prefix that another contains. The result is sorted, IPv4 first.
func Normalize(prefixes []netip.Prefix) []netip.Prefix {
	var ps []netip.Prefix
	for _, p := range prefixes {
		if !p.IsValid() || (p.Addr().Is4In6()) {
			continue
		}
		ps = append(ps, p.Masked())
	}
	// Shortest first within an address, so a containing prefix sorts before the ones
	// it contains.
	slices.SortFunc(ps, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	var out []netip.Prefix
	for _, p := range ps {
		if n := len(out); n > 0 && out[n-1].Contains(p.Addr()) && out[n-1].Bits() <= p.Bits() {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Contains reports whether addr is in one of the prefixes.
func Contains(prefixes []netip.Prefix, addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// route is the part of a routing table entry that matters here.
type route struct {
	// Dst is the destination; a zero prefix length means a default route.
	Dst netip.Prefix
	// Links are the interfaces the route leaves by (several for a multipath route).
	Links []int
	// Gateway is set when the next hop is a router rather than the destination itself.
	Gateway bool
}

// selectLAN picks the LAN's subnets: routes without a gateway (on-link) that leave by
// an interface with a default route, and that contain one of that interface's own
// addresses. IPv6 link-local routes are left out; link-local is always allowed.
func selectLAN(routes []route, addrs map[int][]netip.Addr) []netip.Prefix {
	uplinks := map[int]bool{}
	for _, r := range routes {
		if r.Dst.Bits() == 0 {
			for _, l := range r.Links {
				uplinks[l] = true
			}
		}
	}
	var out []netip.Prefix
	for _, r := range routes {
		if r.Gateway || r.Dst.Bits() == 0 || r.Dst.Addr().IsLinkLocalUnicast() || r.Dst.Addr().IsMulticast() {
			continue
		}
		for _, l := range r.Links {
			if !uplinks[l] {
				continue
			}
			if slices.ContainsFunc(addrs[l], r.Dst.Contains) {
				out = append(out, r.Dst)
				break
			}
		}
	}
	return Normalize(out)
}

// selectHostAddrs returns the addresses inside the LAN's subnets, sorted.
func selectHostAddrs(lan []netip.Prefix, addrs map[int][]netip.Addr) []netip.Addr {
	var out []netip.Addr
	for _, as := range addrs {
		for _, a := range as {
			if Contains(lan, a) && !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return out
}

// Cache remembers the LAN's subnets for a while, so every request and every reconcile
// doesn't read the routing table. Detect is the lookup; nil means Detect.
type Cache struct {
	TTL    time.Duration
	Detect func() ([]netip.Prefix, error)
	// OnError is told when a lookup fails; the previous answer is kept.
	OnError func(error)

	mu      sync.Mutex
	at      time.Time
	cached  []netip.Prefix
	fetched bool
}

// Prefixes returns the LAN's subnets.
func (c *Cache) Prefixes() []netip.Prefix {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fetched && time.Since(c.at) < c.TTL {
		return c.cached
	}
	detect := c.Detect
	if detect == nil {
		detect = Detect
	}
	ps, err := detect()
	if err != nil {
		if c.OnError != nil {
			c.OnError(err)
		}
		return c.cached
	}
	c.cached, c.at, c.fetched = ps, time.Now(), true
	return ps
}
