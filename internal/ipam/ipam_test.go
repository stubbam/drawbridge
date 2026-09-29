package ipam

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"
)

var (
	v4 = netip.MustParsePrefix("10.8.0.0/24")
	v6 = netip.MustParsePrefix("fd3a:5c1e:92b0:1::/64")
)

func TestNewULA(t *testing.T) {
	p, err := NewULA(bytes.NewReader([]byte{0x3a, 0x5c, 0x1e, 0x92, 0xb0}))
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParsePrefix("fd3a:5c1e:92b0:1::/64"); p != want {
		t.Fatalf("got %s, want %s", p, want)
	}
	if err := ValidateV6(p); err != nil {
		t.Fatalf("generated prefix fails validation: %v", err)
	}

	random, err := NewULA(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateV6(random); err != nil {
		t.Fatalf("random prefix %s fails validation: %v", random, err)
	}
}

func TestValidateV4(t *testing.T) {
	for _, s := range []string{"10.8.0.0/24", "192.168.100.0/29", "172.16.0.0/16"} {
		if err := ValidateV4(netip.MustParsePrefix(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"10.8.0.1/24", "8.8.8.0/24", "10.0.0.0/8", "10.8.0.0/30", "fd00::/64"} {
		if err := ValidateV4(netip.MustParsePrefix(s)); err == nil {
			t.Errorf("%s accepted", s)
		}
	}
}

func TestValidateV6(t *testing.T) {
	if err := ValidateV6(v6); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"fd3a:5c1e:92b0:1::1/64", "2001:db8::/64", "fd00::/48", "10.8.0.0/24"} {
		if err := ValidateV6(netip.MustParsePrefix(s)); err == nil {
			t.Errorf("%s accepted", s)
		}
	}
}

func TestMirrorV6(t *testing.T) {
	cases := map[uint32]string{
		1:     "fd3a:5c1e:92b0:1::1",
		23:    "fd3a:5c1e:92b0:1::23",
		254:   "fd3a:5c1e:92b0:1::254",
		1000:  "fd3a:5c1e:92b0:1::1000",
		65534: "fd3a:5c1e:92b0:1::6:5534",
	}
	for n, want := range cases {
		got, err := MirrorV6(v6, n)
		if err != nil {
			t.Fatal(err)
		}
		if got != netip.MustParseAddr(want) {
			t.Errorf("host %d: got %s, want %s", n, got, want)
		}
	}
}

func TestHostV4AndHostNumber(t *testing.T) {
	addr, err := HostV4(v4, 23)
	if err != nil {
		t.Fatal(err)
	}
	if addr != netip.MustParseAddr("10.8.0.23") {
		t.Fatalf("got %s", addr)
	}
	if n, ok := HostNumberV4(v4, addr); !ok || n != 23 {
		t.Fatalf("HostNumberV4 = %d, %v", n, ok)
	}
	for _, bad := range []string{"10.8.0.0", "10.8.0.255", "10.8.1.1", "fd00::1"} {
		if _, ok := HostNumberV4(v4, netip.MustParseAddr(bad)); ok {
			t.Errorf("%s counted as a host in %s", bad, v4)
		}
	}
	if _, err := HostV4(v4, 255); err == nil {
		t.Error("the broadcast address was returned as a host")
	}
	if _, err := HostV4(v4, 0); err == nil {
		t.Error("the network address was returned as a host")
	}

	wide := netip.MustParsePrefix("10.8.0.0/22")
	addr, err = HostV4(wide, 300)
	if err != nil || addr != netip.MustParseAddr("10.8.1.44") {
		t.Fatalf("HostV4(/22, 300) = %s, %v", addr, err)
	}
}

func TestServerAndNext(t *testing.T) {
	srv, err := Server(v4, v6)
	if err != nil {
		t.Fatal(err)
	}
	if srv.IPv4 != netip.MustParseAddr("10.8.0.1") || srv.IPv6 != netip.MustParseAddr("fd3a:5c1e:92b0:1::1") {
		t.Fatalf("server = %+v", srv)
	}

	used := map[netip.Addr]bool{}
	first, err := Next(v4, v6, used)
	if err != nil {
		t.Fatal(err)
	}
	if first.IPv4 != netip.MustParseAddr("10.8.0.2") || first.IPv6 != netip.MustParseAddr("fd3a:5c1e:92b0:1::2") {
		t.Fatalf("first client = %+v", first)
	}

	used[first.IPv4] = true
	used[netip.MustParseAddr("10.8.0.3")] = true
	next, err := Next(v4, v6, used)
	if err != nil {
		t.Fatal(err)
	}
	if next.IPv4 != netip.MustParseAddr("10.8.0.4") {
		t.Fatalf("next client = %+v, want 10.8.0.4", next)
	}

	// A freed address is reused.
	delete(used, first.IPv4)
	again, err := Next(v4, v6, used)
	if err != nil || again.IPv4 != first.IPv4 {
		t.Fatalf("after freeing .2: %+v, %v", again, err)
	}
}

func TestNextWithoutIPv6(t *testing.T) {
	a, err := Next(v4, netip.Prefix{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.IPv6.IsValid() {
		t.Fatalf("got IPv6 %s with IPv6 off", a.IPv6)
	}
}

func TestNextExhausted(t *testing.T) {
	small := netip.MustParsePrefix("10.8.0.0/29") // hosts 1–6: the server and five clients
	used := map[netip.Addr]bool{}
	for i := 0; i < 5; i++ {
		a, err := Next(small, v6, used)
		if err != nil {
			t.Fatalf("client %d: %v", i+1, err)
		}
		used[a.IPv4] = true
	}
	if _, err := Next(small, v6, used); !errors.Is(err, ErrExhausted) {
		t.Fatalf("sixth client: err %v, want ErrExhausted", err)
	}
}
