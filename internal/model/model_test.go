package model

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var testV6 = netip.MustParsePrefix("fd3a:5c1e:92b0:1::/64")

func newSettings(t *testing.T) Settings {
	t.Helper()
	k, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSettings(k, testV6)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewSettingsDefaults(t *testing.T) {
	s := newSettings(t)
	if s.Interface != "wg0" || s.ListenPort != 51820 || s.MTU != 1420 || s.Keepalive != 25 {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	if !s.ClientIsolation {
		t.Error("client isolation should be on by default")
	}
	want := []netip.Addr{netip.MustParseAddr("10.8.0.1"), netip.MustParseAddr("fd3a:5c1e:92b0:1::1")}
	if len(s.DNS) != 2 || s.DNS[0] != want[0] || s.DNS[1] != want[1] {
		t.Fatalf("DNS = %v, want the server's VPN addresses %v (D12)", s.DNS, want)
	}
	if len(s.ClientAllowedIPs) != 2 || s.ClientAllowedIPs[1] != netip.MustParsePrefix("::/0") {
		t.Fatalf("AllowedIPs = %v, want full tunnel for both families", s.ClientAllowedIPs)
	}
}

func TestNewSettingsWithoutIPv6(t *testing.T) {
	k, _ := wgtypes.GeneratePrivateKey()
	s, err := NewSettings(k, netip.Prefix{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.DNS) != 1 {
		t.Fatalf("DNS = %v, want only the IPv4 server address", s.DNS)
	}
	// ::/0 stays, so IPv6 doesn't leak outside the tunnel.
	if len(s.ClientAllowedIPs) != 2 {
		t.Fatalf("AllowedIPs = %v, want ::/0 kept", s.ClientAllowedIPs)
	}
}

func TestEndpoint(t *testing.T) {
	s := newSettings(t)
	if _, err := s.Endpoint(); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("unset endpoint: err %v", err)
	}
	s.EndpointHost = "vpn.example.com"
	if ep, _ := s.Endpoint(); ep != "vpn.example.com:51820" {
		t.Errorf("got %q", ep)
	}
	s.EndpointPort = 443
	if ep, _ := s.Endpoint(); ep != "vpn.example.com:443" {
		t.Errorf("with a translated port: got %q", ep)
	}
	s.EndpointHost, s.EndpointPort = "2001:db8::1", 0
	if ep, _ := s.Endpoint(); ep != "[2001:db8::1]:51820" {
		t.Errorf("IPv6 literal: got %q", ep)
	}
}

func TestValidateRejectsBadSettings(t *testing.T) {
	cases := map[string]func(*Settings){
		"interface":   func(s *Settings) { s.Interface = "wg0; rm -rf /" },
		"long iface":  func(s *Settings) { s.Interface = "a-very-long-interface" },
		"port":        func(s *Settings) { s.ListenPort = 0 },
		"endpoint":    func(s *Settings) { s.EndpointHost = "vpn example.com" },
		"newline":     func(s *Settings) { s.EndpointHost = "vpn.example.com\nPostUp = x" },
		"loopback":    func(s *Settings) { s.EndpointHost = "127.0.0.1" },
		"no key":      func(s *Settings) { s.PrivateKey = wgtypes.Key{} },
		"mtu low":     func(s *Settings) { s.MTU = 1200 },
		"mtu high":    func(s *Settings) { s.MTU = 9000 },
		"ipv4 public": func(s *Settings) { s.IPv4 = netip.MustParsePrefix("8.8.8.0/24") },
		"ipv6 global": func(s *Settings) { s.IPv6 = netip.MustParsePrefix("2001:db8::/64") },
		"dns zero":    func(s *Settings) { s.DNS = []netip.Addr{{}} },
		"keepalive":   func(s *Settings) { s.Keepalive = -1 },
		"allowedips":  func(s *Settings) { s.ClientAllowedIPs = nil },
		"not masked":  func(s *Settings) { s.ClientAllowedIPs = []netip.Prefix{netip.MustParsePrefix("10.0.0.1/8")} },
	}
	for name, mutate := range cases {
		s := newSettings(t)
		mutate(&s)
		if err := s.Validate(); err == nil || !IsInvalid(err) {
			t.Errorf("%s: err %v, want a validation error", name, err)
		}
	}
}

func TestValidateHost(t *testing.T) {
	for _, ok := range []string{"vpn.example.com", "vpn.example.com.", "server", "203.0.113.5", "2001:db8::1", "xn--bcher-kva.example"} {
		if err := ValidateHost(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "-vpn.example.com", "vpn..example.com", "vpn_example.com", "fe80::1%eth0", "::", strings.Repeat("a", 64) + ".com"} {
		if err := ValidateHost(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	if got := NormalizeHost("VPN.Example.COM."); got != "vpn.example.com" {
		t.Errorf("got %q", got)
	}
	if got := NormalizeHost("2001:DB8:0::1"); got != "2001:db8::1" {
		t.Errorf("got %q", got)
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"phone", "Alex's iPhone", "laptop-2", "Büro.PC", "x"} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " phone", "phone ", "-phone", "phone\nPostUp = x", "a;b", "tab\there", strings.Repeat("a", 65), "\xff"} {
		if err := ValidateName(bad); err == nil || !IsInvalid(err) {
			t.Errorf("%q: err %v, want a validation error", bad, err)
		}
	}
	if ValidateName("phone") != nil || IsInvalid(nil) {
		t.Error("a valid name or nil error counts as invalid")
	}
}

func TestClientAllowedIPs(t *testing.T) {
	c := Client{IPv4: netip.MustParseAddr("10.8.0.2"), IPv6: netip.MustParseAddr("fd3a:5c1e:92b0:1::2")}
	got := c.AllowedIPs()
	if len(got) != 2 || got[0].String() != "10.8.0.2/32" || got[1].String() != "fd3a:5c1e:92b0:1::2/128" {
		t.Fatalf("got %v", got)
	}
	c.IPv6 = netip.Addr{}
	if got := c.AllowedIPs(); len(got) != 1 {
		t.Fatalf("without IPv6: got %v", got)
	}
}

func TestAdminAllowedAcceptsOnlyPrivateAndTailscaleRanges(t *testing.T) {
	for _, ok := range []string{"100.64.10.0/24", "100.64.0.0/10", "10.1.0.0/16", "172.16.5.0/24",
		"192.168.1.0/24", "192.168.1.7/32", "fd12:3456::/48", "fc00::/7"} {
		s := newSettings(t)
		s.AdminAllowed = []netip.Prefix{netip.MustParsePrefix(ok)}
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	// Nothing that could put the UI on the internet: public ranges, ranges that only
	// partly overlap a private one, and everything.
	for _, bad := range []string{"0.0.0.0/0", "::/0", "8.8.8.0/24", "100.0.0.0/8", "100.64.0.0/9",
		"10.0.0.0/7", "192.168.0.0/15", "2001:db8:1234:5600::/64", "fe80::/10", "127.0.0.0/8", "fc00::/6"} {
		s := newSettings(t)
		s.AdminAllowed = []netip.Prefix{netip.MustParsePrefix(bad)}
		if err := s.Validate(); err == nil || !IsInvalid(err) {
			t.Errorf("%s: err %v, want a validation error", bad, err)
		}
	}
	s := newSettings(t)
	s.AdminAllowed = []netip.Prefix{netip.MustParsePrefix("100.64.10.5/24")}
	if err := s.Validate(); err == nil {
		t.Error("a prefix with host bits set was accepted")
	}
	s.AdminAllowed = nil
	for i := 0; i <= MaxAdminAllowed; i++ {
		s.AdminAllowed = append(s.AdminAllowed, netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(i), 0, 0}), 16))
	}
	if err := s.Validate(); err == nil {
		t.Error("more than the maximum was accepted")
	}
}

func TestNormalizePrefixesAndAdminSources(t *testing.T) {
	got := NormalizePrefixes([]netip.Prefix{
		netip.MustParsePrefix("100.64.10.75/24"), netip.MustParsePrefix("10.1.0.0/16"),
		netip.MustParsePrefix("100.64.10.0/24"), {},
	})
	want := []netip.Prefix{{}, netip.MustParsePrefix("10.1.0.0/16"), netip.MustParsePrefix("100.64.10.0/24")}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	s := newSettings(t)
	s.AdminAllowed = []netip.Prefix{netip.MustParsePrefix("100.64.10.0/24")}
	sources := s.AdminSources()
	if len(sources) != len(s.VPNSubnets())+1 || sources[len(sources)-1] != netip.MustParsePrefix("100.64.10.0/24") {
		t.Fatalf("sources %v", sources)
	}
	if len(s.VPNSubnets()) != 2 {
		t.Fatal("AdminSources changed the VPN subnets")
	}
}
