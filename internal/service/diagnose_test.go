package service

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stuffam/drawbridge/internal/diag"
	"github.com/stuffam/drawbridge/internal/lan"
)

// diagHost is a host with nothing wrong that the checks read from a map.
func diagHost() *diag.Host {
	return &diag.Host{
		FS: fstest.MapFS{
			"proc/sys/net/ipv4/ip_forward":          {Data: []byte("1\n")},
			"proc/sys/net/ipv6/conf/all/forwarding": {Data: []byte("1\n")},
			"run/systemd/timesync/synchronized":     {},
		},
		Network: func() (lan.Snapshot, error) {
			return lan.Snapshot{Uplinks4: []string{"eth0"}, Uplinks6: []string{"eth0"}, LAN: []netip.Prefix{netip.MustParsePrefix("192.168.4.0/22")}}, nil
		},
		Nft: func(context.Context) ([]byte, error) { return []byte(`{"nftables":[]}`), nil },
	}
}

func byID(t *testing.T, checks []diag.Check, id string) diag.Check {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no %q check in %v", id, checks)
	return diag.Check{}
}

func TestDiagnoseWithoutAHost(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Diagnose(context.Background()); !errors.Is(err, ErrNoDiagnostics) {
		t.Fatalf("err = %v, want ErrNoDiagnostics", err)
	}
}

func TestDiagnoseReadsTheTunnelAndTheFirewallTable(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	svc.Diag = diagHost()
	if _, _, err := svc.AddClient(web(ctx, "admin"), "a phone"); err != nil {
		t.Fatal(err)
	}
	before := kinds(t, svc)

	checks, err := svc.Diagnose(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c := byID(t, checks, "tunnel"); c.Status != diag.Pass || c.Detail != "wg0 is up with 1 peer." {
		t.Errorf("tunnel = %+v", c)
	}
	if c := byID(t, checks, "firewall-table"); c.Status != diag.Pass {
		t.Errorf("firewall-table = %+v", c)
	}
	// A fresh install has no endpoint yet, and public DNS servers, which aren't tested.
	if c := byID(t, checks, "endpoint"); c.Status != diag.Fail {
		t.Errorf("endpoint = %+v, want a fail until one is set", c)
	}
	if c := byID(t, checks, "dns"); c.Status != diag.Pass {
		t.Errorf("dns = %+v", c)
	}
	if c := byID(t, checks, "subnet-overlap"); c.Status != diag.Pass {
		t.Errorf("subnet-overlap = %+v", c)
	}
	if got := kinds(t, svc); !slices.Equal(got, before) {
		t.Errorf("Diagnose recorded events: %v -> %v", before, got)
	}

	if err := svc.Rec.Down(ctx); err != nil {
		t.Fatal(err)
	}
	checks, err = svc.Diagnose(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c := byID(t, checks, "tunnel"); c.Status != diag.Fail {
		t.Errorf("tunnel after Down = %+v, want a fail", c)
	}
	if c := byID(t, checks, "firewall-table"); c.Status != diag.Skip {
		t.Errorf("firewall-table after Down = %+v, want a skip", c)
	}
}

func TestDiagnoseTestsOnlyTheServersOwnDNSAddresses(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	svc.Diag = diagHost()
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

	// Public resolvers: nothing of the server's is asked.
	if _, err := svc.Diagnose(ctx); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 0 {
		t.Fatalf("asked %v with public DNS configured", asked)
	}

	// The server's own addresses, both families, and a public one alongside them.
	st, err := svc.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := st.ServerAddrs()
	if err != nil {
		t.Fatal(err)
	}
	dns := []netip.Addr{srv.IPv4, srv.IPv6, netip.MustParseAddr("1.1.1.1")}
	if _, _, err := svc.UpdateSettings(web(ctx, "admin"), SettingsPatch{DNS: &dns}); err != nil {
		t.Fatal(err)
	}
	checks, err := svc.Diagnose(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 {
		t.Fatalf("asked %v, want the two VPN addresses and not 1.1.1.1", asked)
	}
	// IPv4 answers and IPv6 doesn't: one address clients are given goes unanswered.
	if c := byID(t, checks, "dns"); c.Status != diag.Warn {
		t.Errorf("dns = %+v, want a warning", c)
	}
}
