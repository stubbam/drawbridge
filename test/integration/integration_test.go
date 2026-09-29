//go:build integration

// Package integration runs the real drawbridge binary against kernel WireGuard in network
// namespaces (docs/PLAN.md §12). It needs root (CAP_NET_ADMIN and CAP_SYS_ADMIN), the ip
// and nft tools, IPv6, and the wireguard kernel module. `make test-integration` runs it;
// CI runs it on a GitHub-hosted Ubuntu VM.
//
// The topology is three namespaces:
//
//	cli (198.51.100.1, 2001:db8:2::1) ── net (the "internet") ── srv (192.0.2.1, 2001:db8:1::1)
//	                              web host 203.0.113.1, 2001:db8:3::1 (on net's loopback)
//
// The server runs `drawbridge tunnel up` and `drawbridge serve` in srv. The client
// configures WireGuard in cli from the config `drawbridge client config` prints, and
// fetches from the web host, which echoes the source address it sees.
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/stuffam/drawbridge/internal/wg"
)

const (
	srvAddr4, srvGW4 = "192.0.2.1", "192.0.2.254"
	srvAddr6, srvGW6 = "2001:db8:1::1", "2001:db8:1::fe"
	cliAddr4, cliGW4 = "198.51.100.1", "198.51.100.254"
	cliAddr6, cliGW6 = "2001:db8:2::1", "2001:db8:2::fe"
	web4, web6       = "203.0.113.1", "2001:db8:3::1"
	webPort          = "8080"
)

func TestMain(m *testing.M) {
	if os.Getenv("DRAWBRIDGE_INTEGRATION") != "1" {
		fmt.Println("skipping: set DRAWBRIDGE_INTEGRATION=1 (make test-integration) to run the kernel tests")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// run runs a command and fails the test if it fails.
func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// inNS runs fn with the calling OS thread in network namespace ns. Sockets that fn
// opens stay in ns after it returns.
func inNS(t *testing.T, ns netns.NsHandle, fn func()) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	orig, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer orig.Close()
	if err := netns.Set(ns); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := netns.Set(orig); err != nil {
			panic(fmt.Sprintf("can't return to the original network namespace: %v", err))
		}
	}()
	fn()
}

type topology struct {
	srv, cli, net       string
	srvNS, cliNS, netNS netns.NsHandle
}

func newTopology(t *testing.T) *topology {
	t.Helper()
	id := strconv.Itoa(os.Getpid())
	tp := &topology{srv: "dbsrv" + id, cli: "dbcli" + id, net: "dbnet" + id}
	for _, ns := range []string{tp.srv, tp.cli, tp.net} {
		run(t, "ip", "netns", "add", ns)
		t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", ns).Run() })
		run(t, "ip", "-n", ns, "link", "set", "lo", "up")
	}
	ip := func(ns string, args ...string) { run(t, "ip", append([]string{"-n", ns}, args...)...) }

	run(t, "ip", "link", "add", "srv0", "netns", tp.srv, "type", "veth", "peer", "name", "n-srv", "netns", tp.net)
	run(t, "ip", "link", "add", "cli0", "netns", tp.cli, "type", "veth", "peer", "name", "n-cli", "netns", tp.net)

	ip(tp.srv, "addr", "add", srvAddr4+"/24", "dev", "srv0")
	ip(tp.srv, "-6", "addr", "add", srvAddr6+"/64", "dev", "srv0", "nodad")
	ip(tp.cli, "addr", "add", cliAddr4+"/24", "dev", "cli0")
	ip(tp.cli, "-6", "addr", "add", cliAddr6+"/64", "dev", "cli0", "nodad")
	ip(tp.net, "addr", "add", srvGW4+"/24", "dev", "n-srv")
	ip(tp.net, "-6", "addr", "add", srvGW6+"/64", "dev", "n-srv", "nodad")
	ip(tp.net, "addr", "add", cliGW4+"/24", "dev", "n-cli")
	ip(tp.net, "-6", "addr", "add", cliGW6+"/64", "dev", "n-cli", "nodad")
	// The web host's addresses live on net's loopback: they're local to net and
	// reachable from the other namespaces, with no extra kernel module.
	ip(tp.net, "addr", "add", web4+"/32", "dev", "lo")
	ip(tp.net, "-6", "addr", "add", web6+"/128", "dev", "lo")
	for ns, links := range map[string][]string{tp.srv: {"srv0"}, tp.cli: {"cli0"}, tp.net: {"n-srv", "n-cli"}} {
		for _, l := range links {
			ip(ns, "link", "set", l, "up")
		}
	}
	ip(tp.srv, "route", "add", "default", "via", srvGW4)
	ip(tp.srv, "-6", "route", "add", "default", "via", srvGW6)
	ip(tp.cli, "route", "add", "default", "via", cliGW4)
	ip(tp.cli, "-6", "route", "add", "default", "via", cliGW6)
	// The net namespace routes between the others. On a real host, the drawbridge package
	// turns on forwarding in the server's namespace; here the test does.
	for _, ns := range []string{tp.net, tp.srv} {
		// Written directly rather than with sysctl, which minimal runner images lack.
		run(t, "ip", "netns", "exec", ns, "sh", "-c",
			"echo 1 >/proc/sys/net/ipv4/ip_forward && echo 1 >/proc/sys/net/ipv6/conf/all/forwarding")
	}

	var err error
	for name, h := range map[string]*netns.NsHandle{tp.srv: &tp.srvNS, tp.cli: &tp.cliNS, tp.net: &tp.netNS} {
		if *h, err = netns.GetFromName(name); err != nil {
			t.Fatal(err)
		}
		handle := *h
		t.Cleanup(func() { _ = handle.Close() })
	}
	return tp
}

// startWeb serves, in the net namespace, the source address of each request.
func startWeb(t *testing.T, tp *topology) {
	t.Helper()
	var listeners []net.Listener
	inNS(t, tp.netNS, func() {
		for _, addr := range []string{web4, web6} {
			ln, err := net.Listen("tcp", net.JoinHostPort(addr, webPort))
			if err != nil {
				t.Fatal(err)
			}
			listeners = append(listeners, ln)
		}
	})
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		_, _ = io.WriteString(w, host)
	})}
	for _, ln := range listeners {
		go func() { _ = srv.Serve(ln) }()
	}
	t.Cleanup(func() { _ = srv.Close() })
}

// server runs the drawbridge binary in the server's namespace.
type server struct {
	t        *testing.T
	tp       *topology
	bin      string
	db       string
	secret   string
	socket   string
	daemon   *exec.Cmd
	logMu    sync.Mutex
	daemonLg bytes.Buffer
}

func (s *server) Write(p []byte) (int, error) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return s.daemonLg.Write(p)
}

func drawbridgeBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("DRAWBRIDGE_BIN"); bin != "" {
		return bin
	}
	bin := filepath.Join(t.TempDir(), "drawbridge")
	run(t, "go", "build", "-o", bin, "github.com/stuffam/drawbridge/cmd/drawbridge")
	return bin
}

func newServer(t *testing.T, tp *topology) *server {
	t.Helper()
	dir := t.TempDir()
	s := &server{t: t, tp: tp, bin: drawbridgeBinary(t), db: filepath.Join(dir, "drawbridge.db"),
		secret: filepath.Join(dir, "secret.key"), socket: filepath.Join(dir, "control.sock")}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.secret, key, 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

// inSrv runs a drawbridge command in the server's namespace.
func (s *server) inSrv(args ...string) (string, error) {
	cmd := exec.Command("ip", append([]string{"netns", "exec", s.tp.srv, s.bin}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *server) tunnel(sub string) {
	s.t.Helper()
	if out, err := s.inSrv("tunnel", sub, "--db", s.db, "--secret-key", s.secret); err != nil {
		s.t.Fatalf("tunnel %s: %v\n%s", sub, err, out)
	}
}

func (s *server) startDaemon() {
	s.t.Helper()
	// The default listen address: every address in the namespace, port 51821.
	s.daemon = exec.Command("ip", "netns", "exec", s.tp.srv, s.bin, "serve",
		"--db", s.db, "--secret-key", s.secret, "--control", s.socket, "--drift-interval", "1s")
	s.daemon.Stdout, s.daemon.Stderr = s, s
	if err := s.daemon.Start(); err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() {
		s.stopDaemon()
		if s.t.Failed() {
			s.logMu.Lock()
			s.t.Logf("daemon log:\n%s", s.daemonLg.String())
			s.logMu.Unlock()
		}
	})
	eventually(s.t, 10*time.Second, "the daemon to answer on its control socket", func() error {
		_, err := s.cliErr("server", "show")
		return err
	})
}

func (s *server) stopDaemon() {
	if s.daemon == nil || s.daemon.Process == nil {
		return
	}
	_ = s.daemon.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = s.daemon.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = s.daemon.Process.Kill()
		<-done
	}
	s.daemon = nil
}

// cli runs a CLI command against the daemon. The control socket is a file, so the CLI
// doesn't need to be in the server's namespace.
func (s *server) cli(args ...string) string {
	s.t.Helper()
	out, err := s.cliErr(args...)
	if err != nil {
		s.t.Fatalf("drawbridge %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (s *server) cliErr(args ...string) (string, error) {
	out, err := exec.Command(s.bin, append(args, "--control", s.socket)...).CombinedOutput()
	return string(out), err
}

// device reads the server's wg0.
func (s *server) device() (wg.Device, error) {
	var k *wg.Kernel
	var err error
	inNS(s.t, s.tp.srvNS, func() { k, err = wg.NewKernel() })
	if err != nil {
		return wg.Device{}, err
	}
	defer k.Close()
	return k.Device("wg0")
}

// adminClient returns an HTTPS client that connects from inside ns and trusts the
// daemon's self-signed certificate, as a browser does once the admin accepts it.
func (s *server) adminClient(ns netns.NsHandle) *http.Client {
	s.t.Helper()
	certPEM, err := os.ReadFile(filepath.Join(filepath.Dir(s.db), "tls", "cert.pem"))
	if err != nil {
		s.t.Fatalf("the daemon's certificate: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		s.t.Fatal("can't parse the daemon's certificate")
	}
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var conn net.Conn
			var err error
			inNS(s.t, ns, func() { conn, err = (&net.Dialer{}).DialContext(ctx, network, addr) })
			return conn, err
		},
	}}
}

// healthz fetches the admin UI's health check at host.
func healthz(c *http.Client, host netip.Addr) error {
	resp, err := c.Get("https://" + net.JoinHostPort(host.String(), "51821") + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// clientConfig is the part of a rendered config the test uses.
type clientConfig struct {
	privateKey, peerKey, psk wgtypes.Key
	addrs, allowedIPs        []netip.Prefix
	endpoint                 netip.AddrPort
	mtu, keepalive           int
}

func parseConfig(t *testing.T, text string) clientConfig {
	t.Helper()
	var c clientConfig
	key := func(v string) wgtypes.Key {
		k, err := wgtypes.ParseKey(v)
		if err != nil {
			t.Fatalf("bad key %q: %v", v, err)
		}
		return k
	}
	prefixes := func(v string) []netip.Prefix {
		var out []netip.Prefix
		for _, s := range strings.Split(v, ",") {
			out = append(out, netip.MustParsePrefix(strings.TrimSpace(s)))
		}
		return out
	}
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, " = ")
		if !ok {
			continue
		}
		switch k {
		case "PrivateKey":
			c.privateKey = key(v)
		case "Address":
			c.addrs = prefixes(v)
		case "MTU":
			c.mtu, _ = strconv.Atoi(v)
		case "PublicKey":
			c.peerKey = key(v)
		case "PresharedKey":
			c.psk = key(v)
		case "Endpoint":
			c.endpoint = netip.MustParseAddrPort(v)
		case "AllowedIPs":
			c.allowedIPs = prefixes(v)
		case "PersistentKeepalive":
			c.keepalive, _ = strconv.Atoi(v)
		}
	}
	if c.privateKey == (wgtypes.Key{}) || c.peerKey == (wgtypes.Key{}) || !c.endpoint.IsValid() || len(c.addrs) != 2 {
		t.Fatalf("incomplete config:\n%s", text)
	}
	return c
}

// client is a WireGuard client in the cli namespace, configured from a rendered config.
type client struct {
	t    *testing.T
	tp   *topology
	wg   *wg.Kernel
	http *http.Client
}

func newClient(t *testing.T, tp *topology) *client {
	t.Helper()
	c := &client{t: t, tp: tp}
	var err error
	inNS(t, tp.cliNS, func() { c.wg, err = wg.NewKernel() })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.wg.Close() })
	c.http = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var conn net.Conn
			var err error
			inNS(t, tp.cliNS, func() { conn, err = (&net.Dialer{}).DialContext(ctx, network, addr) })
			return conn, err
		},
	}}
	return c
}

// configure brings up wgc from cfg. The peer gets the config's AllowedIPs (full tunnel),
// but only the test prefixes are routed into the tunnel, so the encrypted packets to the
// server still leave through cli0.
func (c *client) configure(cfg clientConfig, vpn4, vpn6 netip.Prefix) {
	c.t.Helper()
	const iface = "wgc"
	if err := c.wg.Create(iface); err != nil {
		c.t.Fatal(err)
	}
	c.setPeer(cfg)
	for _, a := range cfg.addrs {
		if err := c.wg.AddAddr(iface, a); err != nil {
			c.t.Fatal(err)
		}
	}
	if err := c.wg.SetMTU(iface, cfg.mtu); err != nil {
		c.t.Fatal(err)
	}
	if err := c.wg.SetUp(iface); err != nil {
		c.t.Fatal(err)
	}
	nl, err := netlink.NewHandleAt(c.tp.cliNS)
	if err != nil {
		c.t.Fatal(err)
	}
	defer nl.Close()
	link, err := nl.LinkByName(iface)
	if err != nil {
		c.t.Fatal(err)
	}
	for _, p := range []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8:3::/64"), vpn4, vpn6} {
		dst := wg.IPNet(p)
		if err := nl.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: &dst}); err != nil {
			c.t.Fatalf("route %s: %v", p, err)
		}
	}
}

// setPeer replaces the client's peer, as a phone does when its tunnel is switched on.
func (c *client) setPeer(cfg clientConfig) {
	c.t.Helper()
	keepalive := time.Duration(cfg.keepalive) * time.Second
	endpoint := net.UDPAddrFromAddrPort(cfg.endpoint)
	peer := wgtypes.PeerConfig{
		PublicKey:                   cfg.peerKey,
		PresharedKey:                &cfg.psk,
		Endpoint:                    endpoint,
		PersistentKeepaliveInterval: &keepalive,
		ReplaceAllowedIPs:           true,
	}
	for _, p := range cfg.allowedIPs {
		peer.AllowedIPs = append(peer.AllowedIPs, wg.IPNet(p))
	}
	err := c.wg.Configure("wgc", wgtypes.Config{
		PrivateKey: &cfg.privateKey,
		Peers:      []wgtypes.PeerConfig{{PublicKey: cfg.peerKey, Remove: true}, peer},
	})
	if err != nil {
		c.t.Fatal(err)
	}
}

// get fetches from the web host through the tunnel and returns the source address the
// web host saw.
func (c *client) get(host string) (string, error) {
	resp, err := c.http.Get("http://" + net.JoinHostPort(host, webPort) + "/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func eventually(t *testing.T, timeout time.Duration, what string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := check()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s: %v", timeout, what, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// wantSource checks that a fetch through the tunnel works and was masqueraded to want.
func wantSource(t *testing.T, c *client, host, want string) {
	t.Helper()
	eventually(t, 30*time.Second, "a fetch from "+host+" through the tunnel", func() error {
		got, err := c.get(host)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("the web host saw source %s, want %s (NAT)", got, want)
		}
		return nil
	})
}

func TestEndToEnd(t *testing.T) {
	tp := newTopology(t)
	startWeb(t, tp)
	srv := newServer(t, tp)

	// drawbridge-tunnel.service's job.
	srv.tunnel("up")
	if link := run(t, "ip", "-n", tp.srv, "-d", "link", "show", "wg0"); !strings.Contains(link, "wireguard") {
		t.Fatalf("wg0 isn't a WireGuard interface:\n%s", link)
	}
	table := run(t, "ip", "netns", "exec", tp.srv, "nft", "list", "table", "inet", "drawbridge")
	// The admin chain allows the LAN, which tunnel up detected from the server's
	// uplink (srv0, which has the default routes).
	for _, want := range []string{"masquerade", `comment "drawbridge rev `, "tcp dport 51821", "192.0.2.0/24", "2001:db8:1::/64"} {
		if !strings.Contains(table, want) {
			t.Fatalf("the nftables table lacks %q:\n%s", want, table)
		}
	}

	if saved, err := os.ReadFile(filepath.Join(filepath.Dir(srv.db), "nftables.conf")); err != nil ||
		!strings.Contains(string(saved), "masquerade") {
		t.Fatalf("the applied ruleset wasn't saved next to the database: %v", err)
	}

	srv.startDaemon()
	settings := srv.cli("server", "set", "--endpoint", srvAddr4)
	if !strings.Contains(settings, "Endpoint:           "+srvAddr4+":51820") {
		t.Fatalf("server set:\n%s", settings)
	}
	srv.cli("client", "add", "phone")
	cfg := parseConfig(t, srv.cli("client", "config", "phone"))
	dev, err := srv.device()
	if err != nil {
		t.Fatal(err)
	}
	var vpn4, vpn6 netip.Prefix
	for _, a := range dev.Addrs {
		if a.Addr().Is4() {
			vpn4 = a.Masked()
		} else {
			vpn6 = a.Masked()
		}
	}
	if !vpn4.IsValid() || !vpn6.IsValid() {
		t.Fatalf("server addresses %v, want one of each family", dev.Addrs)
	}

	cl := newClient(t, tp)
	cl.configure(cfg, vpn4, vpn6)

	t.Run("IPv4 endpoint carries IPv4 and IPv6", func(t *testing.T) {
		wantSource(t, cl, web4, srvAddr4) // NAT44
		wantSource(t, cl, web6, srvAddr6) // NAT66
		list := srv.cli("client", "list")
		if !strings.Contains(list, "active") || !strings.Contains(list, cliAddr4+":") || !strings.Contains(list, "s ago") {
			t.Fatalf("client list doesn't show the live peer:\n%s", list)
		}
	})

	t.Run("the admin UI answers the VPN and the LAN only", func(t *testing.T) {
		fromClient := srv.adminClient(tp.cliNS)
		for _, a := range dev.Addrs {
			eventually(t, 30*time.Second, "the admin UI at "+a.Addr().String()+" through the tunnel", func() error {
				return healthz(fromClient, a.Addr())
			})
		}
		// The same client, straight to the server's public addresses rather than through
		// the tunnel, is on the internet as far as the server knows: the firewall drops
		// the connection, so it times out rather than being refused.
		for _, a := range []string{srvAddr4, srvAddr6} {
			err := healthz(fromClient, netip.MustParseAddr(a))
			var nerr net.Error
			if !errors.As(err, &nerr) || !nerr.Timeout() {
				t.Errorf("the admin UI at %s from the internet: err %v, want a timeout (dropped)", a, err)
			}
		}
		// The net namespace shares the server's LAN (192.0.2.0/24, 2001:db8:1::/64).
		fromLAN := srv.adminClient(tp.netNS)
		for _, a := range []string{srvAddr4, srvAddr6} {
			eventually(t, 15*time.Second, "the admin UI at "+a+" from the LAN", func() error {
				return healthz(fromLAN, netip.MustParseAddr(a))
			})
		}
	})

	t.Run("IPv6 endpoint carries IPv4 and IPv6", func(t *testing.T) {
		srv.cli("server", "set", "--endpoint", srvAddr6)
		cfg6 := parseConfig(t, srv.cli("client", "config", "phone"))
		if cfg6.endpoint.Addr() != netip.MustParseAddr(srvAddr6) {
			t.Fatalf("endpoint %s, want %s", cfg6.endpoint, srvAddr6)
		}
		cl.setPeer(cfg6)
		wantSource(t, cl, web4, srvAddr4)
		wantSource(t, cl, web6, srvAddr6)
		eventually(t, 10*time.Second, "the server to see the client's IPv6 endpoint", func() error {
			if show := srv.cli("client", "show", "phone"); !strings.Contains(show, "["+cliAddr6+"]:") {
				return fmt.Errorf("client show:\n%s", show)
			}
			return nil
		})
	})

	t.Run("pause blocks the client and resume restores it", func(t *testing.T) {
		srv.cli("client", "pause", "phone")
		dev, err := srv.device()
		if err != nil || len(dev.Peers) != 0 {
			t.Fatalf("a paused client still has a peer: %+v, %v", dev.Peers, err)
		}
		if got, err := cl.get(web4); err == nil {
			t.Fatalf("a paused client fetched through the tunnel (source %s)", got)
		}
		srv.cli("client", "resume", "phone")
		// Without the client doing anything, WireGuard's timers start a new handshake
		// within about 15 seconds of the first unanswered packet.
		wantSource(t, cl, web4, srvAddr4)
	})

	t.Run("the daemon corrects drift", func(t *testing.T) {
		run(t, "ip", "netns", "exec", tp.srv, "nft", "delete", "table", "inet", "drawbridge")
		run(t, "ip", "-n", tp.srv, "link", "set", "wg0", "mtu", "1500")
		eventually(t, 10*time.Second, "the daemon to restore the table and MTU", func() error {
			if err := exec.Command("ip", "netns", "exec", tp.srv, "nft", "list", "table", "inet", "drawbridge").Run(); err != nil {
				return fmt.Errorf("the table is still missing")
			}
			dev, err := srv.device()
			if err != nil {
				return err
			}
			if dev.MTU != cfg.mtu {
				return fmt.Errorf("MTU is %d", dev.MTU)
			}
			return nil
		})
		wantSource(t, cl, web4, srvAddr4)
	})

	t.Run("tunnel down stays down", func(t *testing.T) {
		srv.tunnel("down")
		// Several drift checks later, the daemon still hasn't brought it back.
		time.Sleep(3 * time.Second)
		if err := exec.Command("ip", "-n", tp.srv, "link", "show", "wg0").Run(); err == nil {
			t.Fatal("wg0 came back after tunnel down")
		}
		if err := exec.Command("ip", "netns", "exec", tp.srv, "nft", "list", "table", "inet", "drawbridge").Run(); err == nil {
			t.Fatal("the nftables table came back after tunnel down")
		}
		if list := srv.cli("client", "list"); !strings.Contains(list, "not in tunnel") {
			t.Fatalf("client list with the tunnel down:\n%s", list)
		}
		srv.tunnel("up")
		wantSource(t, cl, web4, srvAddr4)
	})
}
