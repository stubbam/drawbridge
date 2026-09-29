package wg

import (
	"net"
	"net/netip"
	"testing"
)

func TestPrefixRoundTrip(t *testing.T) {
	for _, s := range []string{"10.8.0.1/24", "10.8.0.2/32", "fd3a:5c1e:92b0:1::1/64", "::/0", "0.0.0.0/0"} {
		p := netip.MustParsePrefix(s)
		got, ok := Prefix(IPNet(p))
		if !ok || got != p {
			t.Errorf("%s: round trip gave %s (%v)", s, got, ok)
		}
	}
}

func TestPrefixUnmapsIPv4(t *testing.T) {
	// net.ParseCIDR returns 16-byte IPs for IPv4 in some paths; they must come back as
	// plain IPv4 prefixes.
	n := net.IPNet{IP: net.ParseIP("10.8.0.2"), Mask: net.CIDRMask(32, 32)}
	got, ok := Prefix(n)
	if !ok || got != netip.MustParsePrefix("10.8.0.2/32") {
		t.Fatalf("got %s, %v", got, ok)
	}
	if _, ok := Prefix(net.IPNet{}); ok {
		t.Fatal("an empty IPNet converted")
	}
}
