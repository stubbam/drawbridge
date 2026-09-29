package service

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/model"
)

// startResolver listens on ip and replies to every query with a response carrying rcode.
// wrongID makes it send an unrelated datagram first.
func startResolver(t *testing.T, ip string, rcode byte, wrongID bool) netip.AddrPort {
	t.Helper()
	pc, err := net.ListenPacket("udp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Skipf("can't listen on %s: %v", ip, err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			reply := append([]byte(nil), buf[:n]...)
			reply[2] |= 0x80 // QR: a response
			reply[3] = reply[3]&0xf0 | rcode
			if wrongID {
				bad := append([]byte(nil), reply...)
				bad[0] ^= 0xff
				_, _ = pc.WriteTo(bad, from)
			}
			_, _ = pc.WriteTo(reply, from)
		}
	}()
	return netip.MustParseAddrPort(pc.LocalAddr().String())
}

// The probe must work over both address families, since the default DNS lists both.
func TestProbeResolver(t *testing.T) {
	cases := []struct {
		name     string
		rcode    byte
		wrongID  bool
		answered bool
	}{
		{"answers", 0, false, true},
		{"no such name still counts", 3, false, true},
		{"ignores a reply with the wrong ID", 0, true, true},
		{"refused", 5, false, false},
		{"server failure", 2, false, false},
	}
	for _, ip := range []string{"127.0.0.1", "::1"} {
		for _, c := range cases {
			t.Run(ip+"/"+c.name, func(t *testing.T) {
				to := startResolver(t, ip, c.rcode, c.wrongID)
				got := ProbeResolver(context.Background(), to)
				if got.Answered != c.answered || got.Detail == "" || got.Address != to.Addr() {
					t.Fatalf("ProbeResolver = %+v, want answered=%v", got, c.answered)
				}
			})
		}
	}
}

func TestProbeResolverNothingListening(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1"} {
		t.Run(ip, func(t *testing.T) {
			pc, err := net.ListenPacket("udp", net.JoinHostPort(ip, "0"))
			if err != nil {
				t.Skipf("can't listen on %s: %v", ip, err)
			}
			to := netip.MustParseAddrPort(pc.LocalAddr().String())
			_ = pc.Close() // the port is now closed, so the kernel answers with "port unreachable"
			start := time.Now()
			got := ProbeResolver(context.Background(), to)
			if got.Answered || got.Detail != "Nothing is listening for DNS on port 53." {
				t.Fatalf("ProbeResolver = %+v", got)
			}
			if time.Since(start) > time.Second {
				t.Errorf("took %s; a closed port should fail at once", time.Since(start))
			}
		})
	}
}

func TestProbeResolverSilence(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0") // reads nothing, answers nothing
	if err != nil {
		t.Skip(err)
	}
	defer pc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	got := ProbeResolver(ctx, netip.MustParseAddrPort(pc.LocalAddr().String()))
	if got.Answered || got.Detail != "Nothing answered within two seconds." {
		t.Fatalf("ProbeResolver = %+v", got)
	}
}

func TestProbeDNSAsksBothVPNAddresses(t *testing.T) {
	svc, _ := newTestService(t)
	var (
		mu    sync.Mutex
		asked []netip.Addr
	)
	svc.DNSProbe = func(_ context.Context, a netip.Addr) DNSProbe {
		mu.Lock()
		asked = append(asked, a)
		mu.Unlock()
		return DNSProbe{Answered: a.Is4(), Detail: "x"}
	}
	got, err := svc.ProbeDNS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Address != netip.MustParseAddr("10.8.0.1") || !got[0].Answered {
		t.Fatalf("probes = %+v, want the IPv4 address first, answered", got)
	}
	if !got[1].Address.Is6() || got[1].Answered {
		t.Fatalf("probes = %+v, want the server's IPv6 address second, not answered", got)
	}
	if len(asked) != 2 {
		t.Fatalf("asked %v, want both addresses", asked)
	}
}

func TestProbeDNSWithoutIPv6AsksOnlyIPv4(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Store.UpdateSettings(context.Background(), func(st *model.Settings) error {
		st.IPv6 = netip.Prefix{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc.DNSProbe = func(context.Context, netip.Addr) DNSProbe { return DNSProbe{Answered: true} }
	got, err := svc.ProbeDNS(context.Background())
	if err != nil || len(got) != 1 || !got[0].Address.Is4() {
		t.Fatalf("probes = %+v, %v; want only the IPv4 address", got, err)
	}
}
