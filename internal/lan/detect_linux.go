package lan

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Detect reads the LAN's subnets from the main routing table (see selectLAN).
func Detect() ([]netip.Prefix, error) {
	routes, addrs, _, err := read()
	if err != nil {
		return nil, err
	}
	return selectLAN(routes, addrs), nil
}

// HostAddrs returns this machine's stable addresses on the LAN: not IPv6 temporary
// (privacy) addresses, which change every day.
func HostAddrs() ([]netip.Addr, error) {
	routes, addrs, stable, err := read()
	if err != nil {
		return nil, err
	}
	return selectHostAddrs(selectLAN(routes, addrs), stable), nil
}

// read returns the main routing table, every interface's addresses, and its stable
// addresses.
func read() ([]route, map[int][]netip.Addr, map[int][]netip.Addr, error) {
	nlRoutes, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading the routing table: %w", err)
	}
	routes := make([]route, 0, len(nlRoutes))
	for _, nr := range nlRoutes {
		r := route{Gateway: nr.Gw != nil}
		if nr.Dst != nil {
			p, ok := prefix(*nr.Dst)
			if !ok {
				continue
			}
			r.Dst = p
		} else if nr.Family == netlink.FAMILY_V6 {
			r.Dst = netip.PrefixFrom(netip.IPv6Unspecified(), 0)
		} else {
			r.Dst = netip.PrefixFrom(netip.IPv4Unspecified(), 0)
		}
		if len(nr.MultiPath) > 0 {
			for _, hop := range nr.MultiPath {
				r.Links = append(r.Links, hop.LinkIndex)
				r.Gateway = r.Gateway || hop.Gw != nil
			}
		} else {
			r.Links = []int{nr.LinkIndex}
		}
		routes = append(routes, r)
	}

	addrs, stable := map[int][]netip.Addr{}, map[int][]netip.Addr{}
	nlAddrs, err := netlink.AddrList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading addresses: %w", err)
	}
	for _, a := range nlAddrs {
		if a.IPNet == nil {
			continue
		}
		addr, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		addrs[a.LinkIndex] = append(addrs[a.LinkIndex], addr)
		if a.Flags&(unix.IFA_F_TEMPORARY|unix.IFA_F_DEPRECATED) == 0 {
			stable[a.LinkIndex] = append(stable[a.LinkIndex], addr)
		}
	}
	return routes, addrs, stable, nil
}

func prefix(n net.IPNet) (netip.Prefix, bool) {
	addr, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	ones, _ := n.Mask.Size()
	return netip.PrefixFrom(addr.Unmap(), ones), true
}
