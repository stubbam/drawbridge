package lan

import (
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"slices"
	"testing"
	"time"
)

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func addrs(ss ...string) []netip.Addr {
	out := make([]netip.Addr, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}

func TestNormalize(t *testing.T) {
	got := Normalize(prefixes(
		"fd3a:5c1e:92b0:1::7/64", // unmasked
		"10.8.0.0/24",
		"10.0.0.0/8", // contains 10.8.0.0/24
		"192.168.4.0/22",
		"192.168.5.0/24", // inside the /22
		"10.8.0.0/24",    // duplicate
		"fe80::/10",
		"::ffff:10.0.0.0/104", // IPv4-mapped
	))
	want := prefixes("10.0.0.0/8", "192.168.4.0/22", "fd3a:5c1e:92b0:1::/64", "fe80::/10")
	if !slices.Equal(got, want) {
		t.Fatalf("Normalize = %v, want %v", got, want)
	}
	if got := Normalize(nil); len(got) != 0 {
		t.Fatalf("Normalize(nil) = %v", got)
	}
}

func TestAllowlist(t *testing.T) {
	list := Allowlist(prefixes("10.8.0.0/24", "fd3a:5c1e:92b0:1::/64"),
		prefixes("192.168.4.0/22", "2001:db8:1234:5600::/64"))
	for _, ok := range []string{
		"127.0.0.1", "::1", "169.254.1.1", "fe80::1", // the host itself and link-local
		"10.8.0.2", "fd3a:5c1e:92b0:1::2", // the VPN
		"192.168.4.20", "192.168.7.254", "2001:db8:1234:5600:aaaa::1", // the LAN
		"::ffff:192.168.4.20", // IPv4 seen on a dual-stack socket
	} {
		if !Contains(list, netip.MustParseAddr(ok)) {
			t.Errorf("%s isn't allowed", ok)
		}
	}
	for _, bad := range []string{
		"203.0.113.9", "192.168.72.1", "10.9.0.1", "172.16.0.1", // the internet, other private ranges
		"2001:db8:1234:5601::1", "fd00::1", "2a00:1450::1",
	} {
		if Contains(list, netip.MustParseAddr(bad)) {
			t.Errorf("%s is allowed", bad)
		}
	}
}

func TestSelectLAN(t *testing.T) {
	const (
		eth0   = 2
		wlan0  = 3
		docker = 4
		wg0    = 5
	)
	v4def := netip.PrefixFrom(netip.IPv4Unspecified(), 0)
	v6def := netip.PrefixFrom(netip.IPv6Unspecified(), 0)
	routes := []route{
		{Dst: v4def, Links: []int{eth0}, Gateway: true},
		{Dst: netip.MustParsePrefix("192.168.4.0/22"), Links: []int{eth0}},
		{Dst: netip.MustParsePrefix("192.168.4.1/32"), Links: []int{eth0}}, // the router, inside the LAN
		{Dst: v6def, Links: []int{eth0}, Gateway: true},
		{Dst: netip.MustParsePrefix("2001:db8:1234:5600::/64"), Links: []int{eth0}}, // from the RA
		{Dst: netip.MustParsePrefix("fd12:3456:789a::/64"), Links: []int{eth0}},     // the router's ULA
		{Dst: netip.MustParsePrefix("fe80::/64"), Links: []int{eth0}},
		// Not the uplink: Wi-Fi on another network, a bridge, and the tunnel.
		{Dst: netip.MustParsePrefix("192.168.1.0/24"), Links: []int{wlan0}},
		{Dst: netip.MustParsePrefix("172.17.0.0/16"), Links: []int{docker}},
		{Dst: netip.MustParsePrefix("10.8.0.0/24"), Links: []int{wg0}},
		// On the uplink, but not a subnet the host has an address in.
		{Dst: netip.MustParsePrefix("198.51.100.0/24"), Links: []int{eth0}},
		// A static route through a router on the LAN.
		{Dst: netip.MustParsePrefix("10.20.0.0/16"), Links: []int{eth0}, Gateway: true},
	}
	ifAddrs := map[int][]netip.Addr{
		eth0:   addrs("192.168.4.10", "2001:db8:1234:5600::10", "fd12:3456:789a::10", "fe80::10"),
		wlan0:  addrs("192.168.1.10"),
		docker: addrs("172.17.0.1"),
		wg0:    addrs("10.8.0.1"),
	}
	got := selectLAN(routes, ifAddrs)
	want := prefixes("192.168.4.0/22", "2001:db8:1234:5600::/64", "fd12:3456:789a::/64")
	if !slices.Equal(got, want) {
		t.Fatalf("selectLAN = %v, want %v", got, want)
	}

	stable := map[int][]netip.Addr{
		eth0:  addrs("192.168.4.10", "2001:db8:1234:5600::10", "fe80::10"),
		wlan0: addrs("192.168.1.10"),
		wg0:   addrs("10.8.0.1"),
	}
	if got, want := selectHostAddrs(got, stable), addrs("192.168.4.10", "2001:db8:1234:5600::10"); !slices.Equal(got, want) {
		t.Fatalf("selectHostAddrs = %v, want %v", got, want)
	}

	// Both Ethernet and Wi-Fi as uplinks (a multipath default route): both LANs.
	routes[0] = route{Dst: v4def, Links: []int{eth0, wlan0}, Gateway: true}
	got = selectLAN(routes, ifAddrs)
	if !slices.Contains(got, netip.MustParsePrefix("192.168.1.0/24")) {
		t.Fatalf("selectLAN with two uplinks = %v, want Wi-Fi's subnet too", got)
	}

	// No default route (offline): no LAN, so only loopback, link-local, and the VPN.
	if got := selectLAN(routes[1:3], ifAddrs); len(got) != 0 {
		t.Fatalf("selectLAN without a default route = %v, want none", got)
	}
}

func TestDetect(t *testing.T) {
	ps, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if runtime.GOOS != "linux" && len(ps) != 0 {
		t.Fatalf("Detect off Linux = %v, want none", ps)
	}
	host, err := HostAddrs()
	if err != nil {
		t.Fatalf("HostAddrs: %v", err)
	}
	for _, a := range host {
		if !Contains(ps, a) {
			t.Errorf("host address %s isn't in the LAN %v", a, ps)
		}
	}
	t.Logf("this machine's LAN: %v; its addresses there: %v", ps, host)
}

func TestCache(t *testing.T) {
	calls := 0
	var errs []error
	fail := false
	c := &Cache{
		TTL: time.Hour,
		Detect: func() ([]netip.Prefix, error) {
			calls++
			if fail {
				return nil, errors.New("netlink is down")
			}
			return prefixes(fmt.Sprintf("192.168.%d.0/24", calls)), nil
		},
		OnError: func(err error) { errs = append(errs, err) },
	}
	first := c.Prefixes()
	if again := c.Prefixes(); !slices.Equal(first, again) || calls != 1 {
		t.Fatalf("second call within the TTL looked up again (%d calls)", calls)
	}
	c.TTL = 0
	fail = true
	if kept := c.Prefixes(); !slices.Equal(kept, first) || len(errs) != 1 {
		t.Fatalf("after a failed lookup: %v (errors %v), want the previous answer", kept, errs)
	}
	fail = false
	if next := c.Prefixes(); slices.Equal(next, first) {
		t.Fatal("an expired cache wasn't refreshed")
	}
}
