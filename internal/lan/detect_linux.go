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
	routes, addrs, err := read()
	if err != nil {
		return nil, err
	}
	return selectLAN(routes, plainAddrs(addrs)), nil
}

// HostAddrs returns this machine's stable addresses on the LAN: not IPv6 temporary
// (privacy) addresses, which change every day.
func HostAddrs() ([]netip.Addr, error) {
	routes, addrs, err := read()
	if err != nil {
		return nil, err
	}
	return selectHostAddrs(selectLAN(routes, plainAddrs(addrs)), stableAddrs(addrs)), nil
}

// Read returns the host's network for the diagnostics: the default routes' interfaces by
// name, the LAN, and every address with its flags.
func Read() (Snapshot, error) {
	routes, addrs, err := read()
	if err != nil {
		return Snapshot{}, err
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return Snapshot{}, fmt.Errorf("listing interfaces: %w", err)
	}
	names := make(map[int]string, len(ifaces))
	for _, i := range ifaces {
		names[i.Index] = i.Name
	}
	return snapshot(routes, addrs, names), nil
}

// read returns the main routing table and every interface's addresses, by link index.
func read() ([]route, map[int][]addrInfo, error) {
	nlRoutes, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the routing table: %w", err)
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

	addrs := map[int][]addrInfo{}
	nlAddrs, err := netlink.AddrList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return nil, nil, fmt.Errorf("reading addresses: %w", err)
	}
	for _, a := range nlAddrs {
		if a.IPNet == nil {
			continue
		}
		addr, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			continue
		}
		addrs[a.LinkIndex] = append(addrs[a.LinkIndex], addrInfo{
			Addr:       addr.Unmap(),
			Temporary:  a.Flags&unix.IFA_F_TEMPORARY != 0,
			Deprecated: a.Flags&unix.IFA_F_DEPRECATED != 0,
		})
	}
	return routes, addrs, nil
}

func prefix(n net.IPNet) (netip.Prefix, bool) {
	addr, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	ones, _ := n.Mask.Size()
	return netip.PrefixFrom(addr.Unmap(), ones), true
}
