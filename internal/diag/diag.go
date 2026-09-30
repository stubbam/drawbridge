// Package diag runs the host diagnostics behind `drawbridge doctor` (docs/PLAN.md §6.6).
// Each check reads the host and reports pass, warn, fail, or skip, with a fix hint when
// something's wrong. Nothing here changes the host.
//
// The daemon runs the checks, not the CLI: it holds CAP_NET_ADMIN (to list the firewall)
// and sees what the tunnel and the VPN see, under its own sandbox. Every read of the host
// goes through Host, so the tests can stand in a fake one.
package diag

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/stuffam/drawbridge/internal/lan"
)

// Status is a check's outcome.
type Status string

const (
	Pass Status = "pass"
	// Warn means something may be wrong, or is about to be.
	Warn Status = "warn"
	// Fail means VPN clients or the admin are affected now.
	Fail Status = "fail"
	// Skip means the check couldn't run, or doesn't apply.
	Skip Status = "skip"
)

// Check is one diagnostic's result.
type Check struct {
	// ID is a stable slug, for scripts and the web page.
	ID     string
	Name   string
	Status Status
	// Detail says what was found, in words for the admin.
	Detail string
	// Hint says how to fix it; it's empty for a pass or a skip.
	Hint string
}

// Host is everything the checks read from the machine. NewHost's is the real one; a nil
// field makes the checks that need it skip.
type Host struct {
	// FS is rooted at the host's "/", so a path has no leading slash.
	FS fs.FS
	// StateDir is the daemon's state directory, whose free space is checked.
	StateDir string
	// CertNotAfter is when the web UI's TLS certificate expires; zero when there's none.
	CertNotAfter time.Time
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Statfs returns the free and total bytes of the filesystem holding path.
	Statfs func(path string) (free, total uint64, err error)
	// LookupIP resolves a name to its IPv4 and IPv6 addresses.
	LookupIP func(ctx context.Context, name string) ([]netip.Addr, error)
	// Network reads the routing table and addresses.
	Network func() (lan.Snapshot, error)
	// Nft returns `nft -j list ruleset`'s output.
	Nft func(ctx context.Context) ([]byte, error)
}

func (h Host) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Input is what the service knows, which the checks compare the host with.
type Input struct {
	// Interface is the tunnel's name.
	Interface string
	// TunnelUp is whether the interface exists; Peers is how many it has. TunnelErr is set
	// when reading it failed for another reason.
	TunnelUp  bool
	Peers     int
	TunnelErr error
	// IPv4 and IPv6 are the VPN's subnets; IPv6 is the zero prefix when the VPN has none.
	IPv4, IPv6 netip.Prefix
	// EndpointHost is the public name or address clients connect to; empty until set.
	EndpointHost string
	// DNS are the resolvers clients are given, and DNSProbes what asking the ones that are
	// this server's own VPN addresses found.
	DNS       []netip.Addr
	DNSProbes []DNSResult
	// FirewallLoaded is whether Drawbridge's nftables table is in the kernel;
	// FirewallErr is set when that couldn't be read.
	FirewallLoaded bool
	FirewallErr    error
}

// DNSResult is one test query to one of the server's own resolver addresses.
type DNSResult struct {
	Address  netip.Addr
	Answered bool
	Detail   string
}

// errUnavailable is what a nil Host hook reports.
var errUnavailable = errors.New("not available on this host")

// env is one run: the input, the host, and the readings shared by several checks.
type env struct {
	ctx  context.Context
	host Host
	in   Input

	snap    lan.Snapshot
	snapErr error
	nft     []byte
	nftErr  error
}

// Run runs every check, in the order the admin should read them: the tunnel, then the
// host under it, then what's around the host.
func Run(ctx context.Context, h Host, in Input) []Check {
	e := &env{ctx: ctx, host: h, in: in, snapErr: errUnavailable, nftErr: errUnavailable}
	if h.Network != nil {
		e.snap, e.snapErr = h.Network()
	}
	if h.Nft != nil {
		nctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		e.nft, e.nftErr = h.Nft(nctx)
		cancel()
	}
	return []Check{
		e.tunnel(),
		e.kernelModule(),
		e.forwarding(),
		e.uplink(),
		e.acceptRA(),
		e.subnetOverlap(),
		e.firewallTable(),
		e.hostFirewall(),
		e.dns(),
		e.endpoint(),
		e.timeSync(),
		e.diskSpace(),
		e.tlsCertificate(),
	}
}

// Summary counts the checks by status.
func Summary(checks []Check) (pass, warn, fail, skip int) {
	for _, c := range checks {
		switch c.Status {
		case Pass:
			pass++
		case Warn:
			warn++
		case Fail:
			fail++
		case Skip:
			skip++
		}
	}
	return
}

// readInt reads a sysctl-style file holding one integer. ok is false when the file is
// missing or isn't one.
func (h Host) readInt(path string) (n int, ok bool) {
	if h.FS == nil {
		return 0, false
	}
	b, err := fs.ReadFile(h.FS, path)
	if err != nil {
		return 0, false
	}
	n, err = strconv.Atoi(strings.TrimSpace(string(b)))
	return n, err == nil
}

func (h Host) exists(path string) bool {
	if h.FS == nil {
		return false
	}
	_, err := fs.Stat(h.FS, path)
	return err == nil
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// join lists items as "a, b, and c".
func join(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}

func formatBytes(n uint64) string {
	const mib, gib = 1 << 20, 1 << 30
	if n >= gib {
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	}
	return fmt.Sprintf("%d MiB", n/mib)
}

// lookupIP is the real Host's LookupIP: the system resolver, with a short deadline, so a
// dead resolver doesn't hold up the whole report.
func lookupIP(ctx context.Context, name string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", name)
	for i, a := range addrs {
		addrs[i] = a.Unmap()
	}
	return addrs, err
}
