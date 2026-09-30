package diag

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stuffam/drawbridge/internal/lan"
)

var now = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

func addrs(ss ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

func prefixes(ss ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// fixture is a healthy host, dual-stack, that a test then breaks one way.
type fixture struct {
	files    fstest.MapFS
	snap     lan.Snapshot
	snapErr  error
	nft      string
	nftErr   error
	free     uint64
	statErr  error
	stateDir string
	lookup   func(name string) ([]netip.Addr, error)
	cert     time.Time
	in       Input
}

func healthy() *fixture {
	return &fixture{
		files: fstest.MapFS{
			"proc/sys/net/ipv4/ip_forward":           file("1\n"),
			"proc/sys/net/ipv6/conf/all/forwarding":  file("1\n"),
			"proc/sys/net/ipv6/conf/eth0/forwarding": file("1\n"),
			"proc/sys/net/ipv6/conf/eth0/accept_ra":  file("2\n"),
			"sys/module/wireguard/version":           file("1.0.0\n"),
			"run/systemd/timesync/synchronized":      file(""),
		},
		snap: lan.Snapshot{
			Uplinks4: []string{"eth0"},
			Uplinks6: []string{"eth0"},
			LAN:      prefixes("192.168.4.0/22", "2001:db8:aaaa:1::/64"),
			Addrs: []lan.HostAddr{
				{Interface: "eth0", Addr: netip.MustParseAddr("192.168.4.10")},
				{Interface: "eth0", Addr: netip.MustParseAddr("2001:db8:aaaa:1::10")},
				{Interface: "eth0", Addr: netip.MustParseAddr("2001:db8:aaaa:1:5c1f::7"), Temporary: true},
			},
		},
		nft:      nftDoc(),
		free:     10 << 30,
		stateDir: "/var/lib/drawbridge",
		lookup: func(string) ([]netip.Addr, error) {
			return addrs("203.0.113.9", "2001:db8:aaaa:1::10"), nil
		},
		cert: now.Add(90 * 24 * time.Hour),
		in: Input{
			Interface:      "wg0",
			TunnelUp:       true,
			Peers:          3,
			IPv4:           netip.MustParsePrefix("10.8.0.0/24"),
			IPv6:           netip.MustParsePrefix("fd42:4242:4242::/64"),
			EndpointHost:   "vpn.example.com",
			DNS:            addrs("10.8.0.1", "fd42:4242:4242::1"),
			DNSProbes:      []DNSResult{{Address: netip.MustParseAddr("10.8.0.1"), Answered: true}, {Address: netip.MustParseAddr("fd42:4242:4242::1"), Answered: true}},
			FirewallLoaded: true,
		},
	}
}

func (f *fixture) run() []Check {
	h := Host{
		FS:           f.files,
		StateDir:     f.stateDir,
		CertNotAfter: f.cert,
		Now:          func() time.Time { return now },
		Statfs: func(string) (uint64, uint64, error) {
			return f.free, 64 << 30, f.statErr
		},
		Network: func() (lan.Snapshot, error) { return f.snap, f.snapErr },
		Nft:     func(context.Context) ([]byte, error) { return []byte(f.nft), f.nftErr },
	}
	if f.lookup != nil {
		h.LookupIP = func(_ context.Context, name string) ([]netip.Addr, error) { return f.lookup(name) }
	}
	return Run(context.Background(), h, f.in)
}

func (f *fixture) check(t *testing.T, id string) Check {
	t.Helper()
	for _, c := range f.run() {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check with ID %q", id)
	return Check{}
}

func TestHealthyHostPassesEverything(t *testing.T) {
	checks := healthy().run()
	if len(checks) != 13 {
		t.Fatalf("got %d checks, want 13", len(checks))
	}
	seen := map[string]bool{}
	for _, c := range checks {
		if seen[c.ID] {
			t.Errorf("duplicate check ID %q", c.ID)
		}
		seen[c.ID] = true
		if c.Name == "" || c.Detail == "" {
			t.Errorf("%s: name %q or detail %q is empty", c.ID, c.Name, c.Detail)
		}
		if c.Status != Pass {
			t.Errorf("%s: %s, want pass: %s", c.ID, c.Status, c.Detail)
		}
		if c.Hint != "" {
			t.Errorf("%s: a pass has the hint %q", c.ID, c.Hint)
		}
	}
	if p, w, f, s := Summary(checks); p != 13 || w+f+s != 0 {
		t.Errorf("Summary = %d, %d, %d, %d", p, w, f, s)
	}
}

func TestEveryFailureCarriesAFix(t *testing.T) {
	f := healthy()
	f.in.TunnelUp = false
	f.in.EndpointHost = ""
	f.in.DNSProbes = []DNSResult{{Address: netip.MustParseAddr("10.8.0.1"), Detail: "No answer."}}
	f.files["proc/sys/net/ipv4/ip_forward"] = file("0")
	f.files["proc/sys/net/ipv6/conf/eth0/accept_ra"] = file("1")
	f.snap.LAN = prefixes("10.8.0.0/16")
	f.free = 1 << 20
	f.cert = now.Add(-time.Hour)
	delete(f.files, "run/systemd/timesync/synchronized")
	delete(f.files, "sys/module/wireguard/version")
	f.nft = nftDoc(baseChain("ip", "filter", "FORWARD", "forward", "drop"))
	fails := 0
	for _, c := range f.run() {
		if c.Status != Warn && c.Status != Fail {
			continue
		}
		fails++
		if c.Hint == "" {
			t.Errorf("%s (%s) has no hint: %s", c.ID, c.Status, c.Detail)
		}
	}
	if fails < 8 {
		t.Errorf("only %d checks complained; the fixture should break most of them", fails)
	}
}

type tc struct {
	name   string
	mutate func(*fixture)
	id     string
	want   Status
	detail string // a substring of Detail
	hint   string // a substring of Hint
}

func runCases(t *testing.T, cases []tc) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.id+"/"+c.name, func(t *testing.T) {
			f := healthy()
			c.mutate(f)
			got := f.check(t, c.id)
			if got.Status != c.want {
				t.Fatalf("status = %s, want %s (%s)", got.Status, c.want, got.Detail)
			}
			if !strings.Contains(got.Detail, c.detail) {
				t.Errorf("detail %q lacks %q", got.Detail, c.detail)
			}
			if !strings.Contains(got.Hint, c.hint) {
				t.Errorf("hint %q lacks %q", got.Hint, c.hint)
			}
		})
	}
}

func TestTunnel(t *testing.T) {
	runCases(t, []tc{
		{"up", func(f *fixture) {}, "tunnel", Pass, "wg0 is up with 3 peers", ""},
		{"one peer", func(f *fixture) { f.in.Peers = 1 }, "tunnel", Pass, "1 peer.", ""},
		{"stopped", func(f *fixture) { f.in.TunnelUp = false }, "tunnel", Fail, "stopped", "systemctl start drawbridge-tunnel"},
		{"unreadable", func(f *fixture) { f.in.TunnelUp = false; f.in.TunnelErr = errors.New("permission denied") }, "tunnel", Fail, "permission denied", "journalctl -u drawbridge"},
	})
}

func TestKernelModule(t *testing.T) {
	runCases(t, []tc{
		{"tunnel up implies loaded", func(f *fixture) { delete(f.files, "sys/module/wireguard/version") }, "kernel-module", Pass, "interface exists", ""},
		{"module loaded", func(f *fixture) { f.in.TunnelUp = false }, "kernel-module", Pass, "loaded", ""},
		{"not loaded", func(f *fixture) {
			f.in.TunnelUp = false
			delete(f.files, "sys/module/wireguard/version")
		}, "kernel-module", Warn, "built into the kernel", "modprobe wireguard"},
	})
}

func TestForwarding(t *testing.T) {
	v4only := func(f *fixture) {
		f.in.IPv6 = netip.Prefix{}
		delete(f.files, "proc/sys/net/ipv6/conf/all/forwarding")
	}
	runCases(t, []tc{
		{"both on", func(f *fixture) {}, "forwarding", Pass, "IPv4 and IPv6 forwarding are on", ""},
		{"IPv4 off", func(f *fixture) { f.files["proc/sys/net/ipv4/ip_forward"] = file("0\n") }, "forwarding", Fail, "IPv4 forwarding is off", "net.ipv4.ip_forward=1"},
		{"IPv6 off", func(f *fixture) { f.files["proc/sys/net/ipv6/conf/all/forwarding"] = file("0\n") }, "forwarding", Fail, "IPv6 forwarding is off", "net.ipv6.conf.all.forwarding=1"},
		{"both off names both fixes", func(f *fixture) {
			f.files["proc/sys/net/ipv4/ip_forward"] = file("0")
			f.files["proc/sys/net/ipv6/conf/all/forwarding"] = file("0")
		}, "forwarding", Fail, "IPv4 forwarding is off and IPv6 forwarding is off", "net.ipv4.ip_forward=1 net.ipv6.conf.all.forwarding=1"},
		{"IPv6 off doesn't matter without IPv6", func(f *fixture) {
			v4only(f)
		}, "forwarding", Pass, "IPv4 forwarding is on.", ""},
		{"IPv6 disabled on a dual-stack VPN", func(f *fixture) {
			for k := range f.files {
				if strings.HasPrefix(k, "proc/sys/net/ipv6/") {
					delete(f.files, k)
				}
			}
		}, "forwarding", Fail, "IPv6 is disabled on this host", "sysctl"},
		{"unreadable", func(f *fixture) { delete(f.files, "proc/sys/net/ipv4/ip_forward") }, "forwarding", Fail, "Can't read net.ipv4.ip_forward", "sysctl net.ipv4.ip_forward"},
	})
}

func TestUplink(t *testing.T) {
	runCases(t, []tc{
		{"both", func(f *fixture) {}, "uplink", Pass, "IPv4 leaves by eth0; IPv6 by eth0.", ""},
		{"several", func(f *fixture) { f.snap.Uplinks4 = []string{"eth0", "wlan0"} }, "uplink", Pass, "eth0 and wlan0", ""},
		{"no IPv4 default route", func(f *fixture) { f.snap.Uplinks4 = nil }, "uplink", Fail, "no IPv4 default route", "ip route show default"},
		{"no IPv6 default route", func(f *fixture) { f.snap.Uplinks6 = nil }, "uplink", Warn, "no IPv6 default route", "accept_ra"},
		{"no IPv6 default route, no IPv6 VPN", func(f *fixture) {
			f.snap.Uplinks6 = nil
			f.in.IPv6 = netip.Prefix{}
		}, "uplink", Pass, "doesn't use IPv6", ""},
		{"unreadable", func(f *fixture) { f.snapErr = errors.New("netlink: operation not permitted") }, "uplink", Skip, "operation not permitted", ""},
	})
}

func TestAcceptRA(t *testing.T) {
	bug := func(f *fixture) { f.files["proc/sys/net/ipv6/conf/eth0/accept_ra"] = file("1\n") }
	runCases(t, []tc{
		{"2 is fine", func(f *fixture) {}, "accept-ra", Pass, "Uplink eth0 has accept_ra 2", ""},
		{"0 is fine (NetworkManager, systemd-networkd)", func(f *fixture) {
			f.files["proc/sys/net/ipv6/conf/eth0/accept_ra"] = file("0\n")
		}, "accept-ra", Pass, "accept_ra 0", ""},
		{"1 without forwarding is fine", func(f *fixture) {
			bug(f)
			f.files["proc/sys/net/ipv6/conf/eth0/forwarding"] = file("0\n")
		}, "accept-ra", Pass, "accept_ra 1", ""},
		{"1 with forwarding is the bug", bug, "accept-ra", Fail, "On eth0, accept_ra is 1",
			"net.ipv6.conf.eth0.accept_ra = 2' | sudo tee /etc/sysctl.d/80-accept-ra.conf"},
		{"the bug, found by the IPv4 default route once the IPv6 one is gone", func(f *fixture) {
			bug(f)
			f.snap.Uplinks6 = nil
		}, "accept-ra", Fail, "On eth0", "accept_ra = 2"},
		{"an interface name that isn't safe in a shell", func(f *fixture) {
			f.snap.Uplinks6 = []string{"a b;c"}
			f.files["proc/sys/net/ipv6/conf/a b;c/accept_ra"] = file("1")
			f.files["proc/sys/net/ipv6/conf/a b;c/forwarding"] = file("1")
		}, "accept-ra", Fail, "a b;c", "net.ipv6.conf.<interface>.accept_ra = 2"},
		{"IPv6 disabled on the host", func(f *fixture) {
			for k := range f.files {
				if strings.HasPrefix(k, "proc/sys/net/ipv6/") {
					delete(f.files, k)
				}
			}
		}, "accept-ra", Skip, "IPv6 is disabled on this host", ""},
		{"IPv6 disabled on the uplink", func(f *fixture) {
			delete(f.files, "proc/sys/net/ipv6/conf/eth0/accept_ra")
		}, "accept-ra", Skip, "disabled on the uplink", ""},
		{"no default route", func(f *fixture) { f.snap.Uplinks4, f.snap.Uplinks6 = nil, nil }, "accept-ra", Skip, "no default route", ""},
		{"routing table unreadable", func(f *fixture) { f.snapErr = errors.New("boom") }, "accept-ra", Skip, "Couldn't find the uplink", ""},
	})
}

func TestSubnetOverlap(t *testing.T) {
	runCases(t, []tc{
		{"apart", func(f *fixture) {}, "subnet-overlap", Pass, "don't overlap", ""},
		{"same IPv4 subnet", func(f *fixture) { f.snap.LAN = prefixes("10.8.0.0/24") }, "subnet-overlap", Fail, "The VPN's 10.8.0.0/24 overlaps the LAN's 10.8.0.0/24", "can't change the VPN's subnet"},
		{"LAN contains the VPN's IPv4 subnet", func(f *fixture) { f.snap.LAN = prefixes("10.0.0.0/8") }, "subnet-overlap", Fail, "overlaps the LAN's 10.0.0.0/8", ""},
		{"VPN contains a LAN's IPv4 subnet", func(f *fixture) { f.snap.LAN = prefixes("10.8.0.128/25") }, "subnet-overlap", Fail, "10.8.0.128/25", ""},
		{"same IPv6 subnet", func(f *fixture) {
			f.snap.LAN = prefixes("192.168.4.0/22", "fd42:4242:4242::/64")
		}, "subnet-overlap", Fail, "The VPN's fd42:4242:4242::/64 overlaps the LAN's fd42:4242:4242::/64", ""},
		{"an unmasked VPN prefix", func(f *fixture) {
			f.in.IPv4 = netip.MustParsePrefix("10.8.0.5/24")
			f.snap.LAN = prefixes("10.8.0.0/24")
		}, "subnet-overlap", Fail, "The VPN's 10.8.0.0/24 overlaps", ""},
		{"both families overlap", func(f *fixture) {
			f.snap.LAN = prefixes("10.8.0.0/24", "fd42:4242:4242::/64")
		}, "subnet-overlap", Fail, "and the VPN's fd42", ""},
		{"unreadable", func(f *fixture) { f.snapErr = errors.New("boom") }, "subnet-overlap", Skip, "boom", ""},
	})
}

func TestFirewallTable(t *testing.T) {
	runCases(t, []tc{
		{"loaded", func(f *fixture) {}, "firewall-table", Pass, "loaded", ""},
		{"missing", func(f *fixture) { f.in.FirewallLoaded = false }, "firewall-table", Fail, "isn't loaded", "within 30 seconds"},
		{"tunnel stopped", func(f *fixture) { f.in.TunnelUp, f.in.FirewallLoaded = false, false }, "firewall-table", Skip, "tunnel is stopped", ""},
		{"unreadable", func(f *fixture) { f.in.FirewallErr = errors.New("nft: not permitted") }, "firewall-table", Skip, "not permitted", ""},
	})
}

func TestDNS(t *testing.T) {
	a4, a6 := netip.MustParseAddr("10.8.0.1"), netip.MustParseAddr("fd42:4242:4242::1")
	runCases(t, []tc{
		{"answers on both", func(f *fixture) {}, "dns", Pass, "answers DNS on 10.8.0.1 and fd42:4242:4242::1", ""},
		{"no servers given", func(f *fixture) { f.in.DNS, f.in.DNSProbes = nil, nil }, "dns", Pass, "use their own", ""},
		{"public resolvers aren't tested", func(f *fixture) {
			f.in.DNS, f.in.DNSProbes = addrs("1.1.1.1", "2606:4700:4700::1111"), nil
		}, "dns", Pass, "1.1.1.1 and 2606:4700:4700::1111, outside this host", ""},
		{"IPv6 doesn't answer", func(f *fixture) {
			f.in.DNSProbes = []DNSResult{{Address: a4, Answered: true}, {Address: a6, Detail: "Nothing answered."}}
		}, "dns", Warn, "nothing answers on fd42:4242:4242::1. Nothing answered.", "server set --dns"},
		{"IPv4 doesn't answer", func(f *fixture) {
			f.in.DNSProbes = []DNSResult{{Address: a4, Detail: "Refused."}, {Address: a6, Answered: true}}
		}, "dns", Warn, "nothing answers on 10.8.0.1", ""},
		{"nothing answers", func(f *fixture) {
			f.in.DNSProbes = []DNSResult{{Address: a4}, {Address: a6}}
		}, "dns", Fail, "nothing answers on 10.8.0.1 and fd42:4242:4242::1", "bind_hosts"},
	})
}

func TestEndpoint(t *testing.T) {
	resolves := func(ss ...string) func(*fixture) {
		return func(f *fixture) {
			f.lookup = func(string) ([]netip.Addr, error) { return addrs(ss...), nil }
		}
	}
	literal := func(s string) func(*fixture) {
		return func(f *fixture) {
			f.in.EndpointHost = s
			f.lookup = func(string) ([]netip.Addr, error) { t.Errorf("looked up the literal %s", s); return nil, nil }
		}
	}
	runCases(t, []tc{
		{"public name", func(f *fixture) {}, "endpoint", Pass, "vpn.example.com resolves to 203.0.113.9 and 2001:db8:aaaa:1::10", ""},
		{"not set", func(f *fixture) { f.in.EndpointHost = "" }, "endpoint", Fail, "isn't set", "server set --endpoint"},
		{"public IPv4 literal", literal("203.0.113.9"), "endpoint", Pass, "Clients connect to 203.0.113.9", ""},
		{"public IPv6 literal", literal("2001:db8::9"), "endpoint", Pass, "Clients connect to 2001:db8::9", ""},
		{"private IPv4 literal", literal("192.168.4.10"), "endpoint", Warn, "192.168.4.10", "public IPv4"},
		{"CGNAT IPv4 literal", literal("100.64.10.5"), "endpoint", Warn, "100.64.10.5", ""},
		{"loopback literal", literal("127.0.0.1"), "endpoint", Warn, "127.0.0.1", ""},
		{"unique-local IPv6 literal", literal("fd00::1"), "endpoint", Warn, "fd00::1", "AAAA"},
		{"link-local IPv6 literal", literal("fe80::1"), "endpoint", Warn, "fe80::1", ""},
		{"doesn't resolve", func(f *fixture) {
			f.lookup = func(string) ([]netip.Addr, error) { return nil, errors.New("no such host") }
		}, "endpoint", Fail, "doesn't resolve from this host: no such host", "A record"},
		{"resolves to nothing", resolves(), "endpoint", Fail, "doesn't resolve", ""},
		{"resolves to a private address", resolves("192.168.4.10"), "endpoint", Warn, "resolves to 192.168.4.10 here", "split DNS"},
		{"one of two is private", resolves("203.0.113.9", "10.0.0.5"), "endpoint", Warn, "10.0.0.5", ""},
		{"IPv6 that is private", resolves("203.0.113.9", "fd00::5"), "endpoint", Warn, "fd00::5", ""},
		{"AAAA is a temporary address", resolves("203.0.113.9", "2001:db8:aaaa:1:5c1f::7"), "endpoint", Warn,
			"2001:db8:aaaa:1:5c1f::7 (on eth0), a temporary", "-temporary"},
		{"AAAA is a stable address", resolves("2001:db8:aaaa:1::10"), "endpoint", Pass, "2001:db8:aaaa:1::10", ""},
		{"no way to look up", func(f *fixture) { f.lookup = nil }, "endpoint", Skip, "Couldn't look up", ""},
		{"an IPv4-mapped answer is unmapped", resolves("::ffff:203.0.113.9"), "endpoint", Pass, "203.0.113.9", ""},
	})
}

func TestTimeSync(t *testing.T) {
	runCases(t, []tc{
		{"synchronized", func(f *fixture) {}, "time-sync", Pass, "synchronized", ""},
		{"not", func(f *fixture) { delete(f.files, "run/systemd/timesync/synchronized") }, "time-sync", Warn, "hasn't reported", "timedatectl set-ntp true"},
	})
}

func TestDiskSpace(t *testing.T) {
	runCases(t, []tc{
		{"plenty", func(f *fixture) {}, "disk-space", Pass, "10.0 GiB free of 64.0 GiB", ""},
		{"getting low", func(f *fixture) { f.free = 150 << 20 }, "disk-space", Warn, "150 MiB free", "Free up space"},
		{"just enough", func(f *fixture) { f.free = 200 << 20 }, "disk-space", Pass, "200 MiB free", ""},
		{"nearly full", func(f *fixture) { f.free = 20 << 20 }, "disk-space", Fail, "20 MiB free", "SQLite can't write"},
		{"unreadable", func(f *fixture) { f.statErr = errors.New("no such file") }, "disk-space", Skip, "no such file", ""},
		{"no state directory", func(f *fixture) { f.stateDir = "" }, "disk-space", Skip, "isn't known", ""},
	})
}

func TestTLSCertificate(t *testing.T) {
	runCases(t, []tc{
		{"valid", func(f *fixture) {}, "tls-certificate", Pass, "Valid until 2026-12-28 (90 days)", ""},
		{"a month left is still fine", func(f *fixture) { f.cert = now.Add(31 * 24 * time.Hour) }, "tls-certificate", Pass, "31 days", ""},
		{"expires soon", func(f *fixture) { f.cert = now.Add(10*24*time.Hour + time.Hour) }, "tls-certificate", Warn, "expires on 2026-10-09 (10 days)", "systemctl restart drawbridge"},
		{"expired", func(f *fixture) { f.cert = now.Add(-time.Minute) }, "tls-certificate", Fail, "expired on 2026-09-29", "systemctl restart drawbridge"},
		{"none", func(f *fixture) { f.cert = time.Time{} }, "tls-certificate", Skip, "no TLS certificate", ""},
	})
}

func TestNothingReadableSkipsInsteadOfFailing(t *testing.T) {
	checks := Run(context.Background(), Host{}, Input{Interface: "wg0", TunnelUp: true, IPv4: netip.MustParsePrefix("10.8.0.0/24")})
	for _, c := range checks {
		switch c.ID {
		case "uplink", "accept-ra", "subnet-overlap", "host-firewall", "disk-space", "tls-certificate":
			if c.Status != Skip {
				t.Errorf("%s = %s (%s), want skip", c.ID, c.Status, c.Detail)
			}
		}
	}
}
