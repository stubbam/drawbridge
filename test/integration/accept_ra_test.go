//go:build integration

package integration

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netns"
)

const raPrefix = "2001:db8:5::/64"

// raHost is a host with one uplink to a router that advertises raPrefix, with IPv6
// forwarding on, as the drawbridge package leaves it.
type raHost struct {
	host, rtr string
	rtrNS     netns.NsHandle
	rtrIf     *net.Interface
}

func newRAHost(t *testing.T, acceptRA string) *raHost {
	t.Helper()
	id := strconv.Itoa(os.Getpid())
	h := &raHost{host: "dbra-h" + id, rtr: "dbra-r" + id}
	for _, ns := range []string{h.host, h.rtr} {
		run(t, "ip", "netns", "add", ns)
		t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", ns).Run() })
		run(t, "ip", "-n", ns, "link", "set", "lo", "up")
	}
	run(t, "ip", "link", "add", "up0", "netns", h.host, "type", "veth", "peer", "name", "rtr0", "netns", h.rtr)
	// No duplicate address detection, so the link-local addresses are usable at once.
	h.sysctl(t, h.host, "net/ipv6/conf/up0/accept_dad", "0")
	h.sysctl(t, h.rtr, "net/ipv6/conf/rtr0/accept_dad", "0")
	h.sysctl(t, h.host, "net/ipv6/conf/up0/accept_ra", acceptRA)
	h.sysctl(t, h.host, "net/ipv6/conf/all/forwarding", "1")
	for ns, l := range map[string]string{h.host: "up0", h.rtr: "rtr0"} {
		run(t, "ip", "-n", ns, "link", "set", l, "up")
	}
	eventually(t, 5*time.Second, "the router's link-local address", func() error {
		out := run(t, "ip", "-n", h.rtr, "-6", "addr", "show", "dev", "rtr0", "scope", "link")
		if !strings.Contains(out, "fe80::") || strings.Contains(out, "tentative") {
			return fmt.Errorf("not ready:\n%s", out)
		}
		return nil
	})
	var err error
	if h.rtrNS, err = netns.GetFromName(h.rtr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.rtrNS.Close() })
	inNS(t, h.rtrNS, func() {
		if h.rtrIf, err = net.InterfaceByName("rtr0"); err != nil {
			t.Fatal(err)
		}
	})
	return h
}

// sysctl writes a kernel setting in ns directly, since minimal runner images lack sysctl.
func (h *raHost) sysctl(t *testing.T, ns, key, value string) {
	t.Helper()
	run(t, "ip", "netns", "exec", ns, "sh", "-c", fmt.Sprintf("echo %s >/proc/sys/%s", value, key))
}

// advertise sends one Router Advertisement from the router: a default route with a
// lifetime, and raPrefix for SLAAC.
func (h *raHost) advertise(t *testing.T) {
	t.Helper()
	ra := make([]byte, 16, 16+32)
	ra[0] = 134 // Router Advertisement
	ra[4] = 64  // current hop limit
	binary.BigEndian.PutUint16(ra[6:], 1800)
	pfx := make([]byte, 32)
	pfx[0], pfx[1], pfx[2] = 3, 4, 64 // Prefix Information, 32 bytes, a /64
	pfx[3] = 0xc0                     // on-link and autonomous
	binary.BigEndian.PutUint32(pfx[4:], 3600)
	binary.BigEndian.PutUint32(pfx[8:], 1800)
	copy(pfx[16:], netip.MustParsePrefix(raPrefix).Addr().AsSlice())
	ra = append(ra, pfx...)

	inNS(t, h.rtrNS, func() {
		fd, err := syscall.Socket(syscall.AF_INET6, syscall.SOCK_RAW, syscall.IPPROTO_ICMPV6)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		// Neighbor Discovery ignores anything whose hop limit isn't 255.
		for _, opt := range []int{syscall.IPV6_UNICAST_HOPS, syscall.IPV6_MULTICAST_HOPS} {
			if err := syscall.SetsockoptInt(fd, syscall.IPPROTO_IPV6, opt, 255); err != nil {
				t.Fatal(err)
			}
		}
		dst := &syscall.SockaddrInet6{ZoneId: uint32(h.rtrIf.Index)}
		copy(dst.Addr[:], net.ParseIP("ff02::1"))
		if err := syscall.Sendto(fd, ra, 0, dst); err != nil {
			t.Fatal(err)
		}
	})
}

// learned reports whether the host has an address from raPrefix and a default route.
func (h *raHost) learned(t *testing.T) (addr, route bool) {
	t.Helper()
	pfx := netip.MustParsePrefix(raPrefix)
	for _, f := range strings.Fields(run(t, "ip", "-n", h.host, "-6", "-o", "addr", "show", "dev", "up0")) {
		if p, err := netip.ParsePrefix(f); err == nil && pfx.Contains(p.Addr()) {
			addr = true
		}
	}
	route = strings.Contains(run(t, "ip", "-n", h.host, "-6", "route", "show", "default"), "proto ra")
	return addr, route
}

// installer runs the package's accept-ra helper in the host namespace, as postinst does,
// with its drop-in in a temporary directory. It returns that directory.
func (h *raHost) installer(t *testing.T, dir string) string {
	t.Helper()
	script, err := filepath.Abs("../../packaging/libexec/accept-ra")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ip", "netns", "exec", h.host, "sh", script)
	cmd.Env = append(os.Environ(), "DRAWBRIDGE_SYSCTL_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("accept-ra: %v\n%s", err, out)
	}
	return filepath.Join(dir, "91-drawbridge-accept-ra.conf")
}

func (h *raHost) acceptRA(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(run(t, "ip", "netns", "exec", h.host, "cat", "/proc/sys/net/ipv6/conf/up0/accept_ra"))
}

// The quiet failure: forwarding makes a kernel-SLAAC host ignore Router Advertisements,
// so it never gets, or renews, its own IPv6 address and default route.
func TestAcceptRAKeepsHostIPv6WithForwarding(t *testing.T) {
	h := newRAHost(t, "1")
	// The uplink's default route as it was before the install (the installer finds the
	// uplink through it); "learned" below looks for the route the kernel adds from an RA.
	run(t, "ip", "-n", h.host, "-6", "route", "add", "default", "via", "fe80::1", "dev", "up0")

	// Without the fix, the kernel ignores the advertisement.
	for range 3 {
		h.advertise(t)
		time.Sleep(300 * time.Millisecond)
	}
	if addr, route := h.learned(t); addr || route {
		t.Fatalf("with forwarding on and accept_ra=1, the host learned an address (%v) or a route (%v); "+
			"the kernel should have ignored the advertisement, so this test proves nothing", addr, route)
	}

	dir := t.TempDir()
	dropin := h.installer(t, dir)
	if got := h.acceptRA(t); got != "2" {
		t.Fatalf("accept_ra on the uplink = %s after the installer step, want 2", got)
	}
	// At boot, systemd-sysctl applies the drop-in; its line must be one it accepts.
	want := "net/ipv6/conf/up0/accept_ra = 2\n"
	if b, err := os.ReadFile(dropin); err != nil || !strings.Contains(string(b), want) {
		t.Fatalf("drop-in %s = %q, %v; want a line %q", dropin, b, err, want)
	}

	eventually(t, 10*time.Second, "the host to learn its address and default route from the advertisement", func() error {
		h.advertise(t)
		if addr, route := h.learned(t); !addr || !route {
			return fmt.Errorf("address from %s: %v, default route from the RA: %v", raPrefix, addr, route)
		}
		return nil
	})
}

// The helper touches only what needs it: a stack that handles Router Advertisements
// itself (accept_ra=0, as NetworkManager and systemd-networkd set it) is left alone, and
// a second run changes nothing.
func TestAcceptRALeavesOtherStacksAlone(t *testing.T) {
	h := newRAHost(t, "0")
	run(t, "ip", "-n", h.host, "-6", "route", "add", "default", "via", "fe80::1", "dev", "up0")
	dir := t.TempDir()
	dropin := h.installer(t, dir)
	if got := h.acceptRA(t); got != "0" {
		t.Errorf("accept_ra = %s, want 0 left as it was", got)
	}
	if _, err := os.Stat(dropin); !os.IsNotExist(err) {
		t.Errorf("a drop-in was written for an uplink that didn't need one: %v", err)
	}
}

func TestAcceptRAIsIdempotentAndKeepsItsDropIn(t *testing.T) {
	h := newRAHost(t, "1")
	run(t, "ip", "-n", h.host, "-6", "route", "add", "default", "via", "fe80::1", "dev", "up0")
	dir := t.TempDir()
	dropin := h.installer(t, dir)
	first, err := os.ReadFile(dropin)
	if err != nil {
		t.Fatal(err)
	}
	// An upgrade runs it again: the interface reads 2 now (from the first run), and the
	// drop-in must survive so the setting isn't lost at the next boot.
	h.installer(t, dir)
	second, err := os.ReadFile(dropin)
	if err != nil || string(second) != string(first) {
		t.Fatalf("drop-in after a second run = %q, %v; want it unchanged (%q)", second, err, first)
	}
	// If the admin moves the interface to a stack that handles RAs itself, the entry goes.
	h.sysctl(t, h.host, "net/ipv6/conf/up0/accept_ra", "0")
	h.installer(t, dir)
	if _, err := os.Stat(dropin); !os.IsNotExist(err) {
		t.Errorf("the drop-in stayed after accept_ra was set to 0: %v", err)
	}
}
