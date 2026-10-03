package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stuffam/drawbridge/internal/auth"
	"github.com/stuffam/drawbridge/internal/control"
	"github.com/stuffam/drawbridge/internal/diag"
	"github.com/stuffam/drawbridge/internal/firewall"
	"github.com/stuffam/drawbridge/internal/keys"
	"github.com/stuffam/drawbridge/internal/lan"
	"github.com/stuffam/drawbridge/internal/reconcile"
	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/tlscert"
	"github.com/stuffam/drawbridge/internal/version"
	"github.com/stuffam/drawbridge/internal/views"
	"github.com/stuffam/drawbridge/internal/wg"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type result struct {
	code           int
	stdout, stderr string
}

func runCLI(stdin string, args ...string) result {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr)
	return result{code, stdout.String(), stderr.String()}
}

func TestRunVersion(t *testing.T) {
	r := runCLI("", "version")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if want := "drawbridge " + version.String() + "\n"; r.stdout != want {
		t.Fatalf("stdout %q, want %q", r.stdout, want)
	}
	if !strings.HasPrefix(version.String(), "v") {
		t.Fatalf("version %q, want a lowercase v prefix", version.String())
	}
}

func TestRunHelpAndErrors(t *testing.T) {
	cases := []struct {
		args       []string
		code       int
		wantStdout string
		wantStderr string
	}{
		{args: nil, code: 2, wantStderr: "Usage: drawbridge"},
		{args: []string{"help"}, code: 0, wantStdout: "client qr NAME"},
		{args: []string{"frobnicate"}, code: 2, wantStderr: `unknown command "frobnicate"`},
		{args: []string{"serve", "extra"}, code: 2, wantStderr: `unexpected argument "extra"`},
		{args: []string{"serve", "-bogus"}, code: 2, wantStderr: "flag provided but not defined"},
		{args: []string{"serve", "-listen", "not-an-address"}, code: 1, wantStderr: "can't listen"},
		{args: []string{"tunnel"}, code: 2, wantStderr: "Usage: drawbridge tunnel"},
		{args: []string{"tunnel", "sideways"}, code: 2, wantStderr: "Usage: drawbridge tunnel"},
		{args: []string{"server"}, code: 2, wantStderr: "Usage: drawbridge server"},
		{args: []string{"client"}, code: 2, wantStderr: "Usage: drawbridge client"},
		{args: []string{"client", "fly"}, code: 2, wantStderr: `unknown command "fly"`},
		{args: []string{"client", "add"}, code: 2, wantStderr: "needs exactly one NAME"},
		{args: []string{"client", "add", "a", "b"}, code: 2, wantStderr: "needs exactly one NAME"},
		{args: []string{"client", "list", "extra"}, code: 2, wantStderr: `unexpected argument "extra"`},
		{args: []string{"client", "rename", "a"}, code: 2, wantStderr: "needs NAME and NEW-NAME"},
		{args: []string{"admin"}, code: 2, wantStderr: "Usage: drawbridge admin"},
		{args: []string{"admin", "create"}, code: 2, wantStderr: "Usage: drawbridge admin"},
		{args: []string{"admin", "reset-password", "a", "b"}, code: 2, wantStderr: "Usage: drawbridge admin"},
		{args: []string{"admin", "promote"}, code: 2, wantStderr: `unknown command "promote"`},
		{args: []string{"events", "extra"}, code: 2, wantStderr: `unexpected argument "extra"`},
		{args: []string{"serve", "--backend", "userspace"}, code: 2, wantStderr: "--backend must be kernel or fake"},
	}
	for _, c := range cases {
		r := runCLI("", c.args...)
		if r.code != c.code {
			t.Errorf("%v: exit %d, want %d (stderr %q)", c.args, r.code, c.code, r.stderr)
		}
		if !strings.Contains(r.stdout, c.wantStdout) {
			t.Errorf("%v: stdout %q, want it to contain %q", c.args, r.stdout, c.wantStdout)
		}
		if !strings.Contains(r.stderr, c.wantStderr) {
			t.Errorf("%v: stderr %q, want it to contain %q", c.args, r.stderr, c.wantStderr)
		}
	}
}

// memFirewall remembers the applied revision, like the kernel's table does.
type memFirewall struct{ rev string }

func (f *memFirewall) Apply(_ context.Context, rs firewall.Ruleset) error {
	f.rev = rs.Revision
	return nil
}
func (f *memFirewall) Remove(context.Context) error                   { f.rev = ""; return nil }
func (f *memFirewall) Revision(context.Context) (string, bool, error) { return f.rev, f.rev != "", nil }

type fakeEnv struct {
	store  *store.Store
	wg     *wg.Fake
	rec    *reconcile.Reconciler
	svc    *service.Service
	socket string
}

// newFakeEnv builds the service over a fake tunnel that's already up.
func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	sealer, err := keys.NewSealer(bytes.Repeat([]byte{5}, keys.SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(dir, "db"), sealer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	backend := wg.NewFake()
	rec := &reconcile.Reconciler{State: st, WG: backend, Firewall: &memFirewall{},
		Lock: reconcile.FileLock{Path: filepath.Join(dir, "reconcile.lock")}}
	if _, err := rec.Up(ctx); err != nil {
		t.Fatal(err)
	}
	svc := &service.Service{Store: st, Rec: rec, WG: backend, Log: discard,
		Hasher: auth.NewHasher(auth.Params{Memory: 64, Time: 1, Threads: 1}), Limiter: auth.NewLimiter()}
	return &fakeEnv{store: st, wg: backend, rec: rec, svc: svc, socket: filepath.Join(dir, "control.sock")}
}

// startDaemon runs the daemon with the fake service and returns once it's ready.
func startDaemon(t *testing.T, env *fakeEnv) string {
	t.Helper()
	web, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := control.Listen(env.socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- daemon{web: web, control: ctl, svc: env.svc, drift: time.Hour, log: discard}.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("daemon: %v", err)
		}
	})
	waitFor(t, func() bool {
		resp, err := http.Get("http://" + web.Addr().String() + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	return web.Addr().String()
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDaemonReportsReadinessAndShutsDown(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "notify.sock")
	notify, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer notify.Close()
	t.Setenv("NOTIFY_SOCKET", sockPath)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon{web: ln, log: discard}.run(ctx) }()

	if got := readNotification(t, notify); got != "READY=1" {
		t.Fatalf("first notification %q, want READY=1", got)
	}
	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz: status %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon didn't stop after cancel")
	}
	if got := readNotification(t, notify); got != "STOPPING=1" {
		t.Fatalf("second notification %q, want STOPPING=1", got)
	}
}

func readNotification(t *testing.T, conn *net.UnixConn) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("reading notification: %v", err)
	}
	return string(buf[:n])
}

func TestDaemonReconcilesAtStartup(t *testing.T) {
	env := newFakeEnv(t)
	// Drift before the daemon starts: a peer the database doesn't know about.
	if _, err := env.store.AddClient(context.Background(), "phone"); err != nil {
		t.Fatal(err)
	}
	startDaemon(t, env)
	// /healthz answers before the startup reconcile finishes, so wait for its effect.
	waitFor(t, func() bool {
		d, err := env.wg.Device("wg0")
		return err == nil && len(d.Peers) == 1
	})
}

func TestClientCommands(t *testing.T) {
	env := newFakeEnv(t)
	startDaemon(t, env)
	sock := "--control=" + env.socket

	r := runCLI("", "client", "list", sock)
	if r.code != 0 || !strings.Contains(r.stdout, "No clients yet") {
		t.Fatalf("empty list: %+v", r)
	}

	r = runCLI("", "client", "add", "Alex's iPhone", sock)
	if r.code != 0 {
		t.Fatalf("add: %+v", r)
	}
	for _, want := range []string{`Added client "Alex's iPhone": 10.8.0.2, fd`, `drawbridge client qr 'Alex'\''s iPhone'`, "> Alex-s-iPhone.conf"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("add output lacks %q:\n%s", want, r.stdout)
		}
	}
	if r := runCLI("", "client", "add", "Alex's iPhone", sock); r.code != 1 || !strings.Contains(r.stderr, "already exists") {
		t.Errorf("duplicate add: %+v", r)
	}

	r = runCLI("", "client", "list", sock)
	if r.code != 0 || !strings.Contains(r.stdout, "Alex's iPhone") || !strings.Contains(r.stdout, "active") ||
		!strings.Contains(r.stdout, "never") {
		t.Fatalf("list:\n%s", r.stdout)
	}

	if r := runCLI("", "client", "pause", "Alex's iPhone", sock); r.code != 0 || !strings.Contains(r.stdout, "Paused") {
		t.Fatalf("pause: %+v", r)
	}
	if r := runCLI("", "client", "show", "Alex's iPhone", sock); !strings.Contains(r.stdout, "State:       paused") {
		t.Fatalf("show after pause:\n%s", r.stdout)
	}
	if r := runCLI("", "client", "resume", "Alex's iPhone", sock); r.code != 0 || !strings.Contains(r.stdout, "Resumed") {
		t.Fatalf("resume: %+v", r)
	}

	// No endpoint yet, so there's no config.
	if r := runCLI("", "client", "config", "Alex's iPhone", sock); r.code != 1 || !strings.Contains(r.stderr, "server set --endpoint") {
		t.Fatalf("config without an endpoint: %+v", r)
	}
	if r := runCLI("", "server", "set", "--endpoint", "vpn.example.com:443", sock); r.code != 0 ||
		!strings.Contains(r.stdout, "vpn.example.com:443") {
		t.Fatalf("server set: %+v", r)
	}
	r = runCLI("", "client", "config", "Alex's iPhone", sock)
	if r.code != 0 || !strings.HasPrefix(r.stdout, "[Interface]\n") || !strings.Contains(r.stdout, "Endpoint = vpn.example.com:443") {
		t.Fatalf("config: %+v", r)
	}
	r = runCLI("", "client", "qr", "Alex's iPhone", sock)
	if r.code != 0 || !strings.Contains(r.stdout, "▀") || !strings.Contains(r.stderr, "Scan from QR code") {
		t.Fatalf("qr: code %d, stderr %q", r.code, r.stderr)
	}
	if r := runCLI("", "client", "add", "laptop", "--qr", sock); r.code != 0 || !strings.Contains(r.stdout, "▀") {
		t.Fatalf("add --qr: %+v", r.stderr)
	}

	// delete asks first.
	if r := runCLI("n\n", "client", "delete", "laptop", sock); r.code != 1 || !strings.Contains(r.stdout, "Not deleted") {
		t.Fatalf("declined delete: %+v", r)
	}
	if r := runCLI("y\n", "client", "delete", "laptop", sock); r.code != 0 || !strings.Contains(r.stdout, `Deleted client "laptop"`) {
		t.Fatalf("confirmed delete: %+v", r)
	}
	if r := runCLI("", "client", "delete", "Alex's iPhone", "--yes", sock); r.code != 0 {
		t.Fatalf("delete --yes: %+v", r)
	}
	if r := runCLI("", "client", "show", "Alex's iPhone", sock); r.code != 1 || !strings.Contains(r.stderr, "no such client") {
		t.Fatalf("show after delete: %+v", r)
	}
}

func TestRenameAndEvents(t *testing.T) {
	env := newFakeEnv(t)
	startDaemon(t, env)
	sock := "--control=" + env.socket

	if r := runCLI("", "events", sock); r.code != 0 || !strings.Contains(r.stdout, "No events yet") {
		t.Fatalf("empty events: %+v", r)
	}
	runCLI("", "client", "add", "phone", sock)
	if r := runCLI("", "client", "rename", "phone", "Pixel 9", sock); r.code != 0 ||
		!strings.Contains(r.stdout, `Renamed client "phone" to "Pixel 9"`) {
		t.Fatalf("rename: %+v", r)
	}
	if r := runCLI("", "client", "show", "pixel 9", sock); r.code != 0 {
		t.Fatalf("show the renamed client: %+v", r)
	}
	runCLI("", "client", "add", "laptop", sock)
	if r := runCLI("", "client", "rename", "laptop", "PIXEL 9", sock); r.code != 1 || !strings.Contains(r.stderr, "already exists") {
		t.Fatalf("rename onto another client's name: %+v", r)
	}

	r := runCLI("", "events", "--client", "Pixel 9", sock)
	if r.code != 0 {
		t.Fatalf("events: %+v", r)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "client.renamed") || !strings.Contains(lines[2], "client.added") {
		t.Fatalf("events for one client:\n%s", r.stdout)
	}
	// The CLI's changes are attributed to the account that ran it, through the socket.
	if !strings.Contains(lines[1], "(cli)") || !strings.Contains(lines[1], "from: phone") ||
		!strings.Contains(lines[1], "Pixel 9") {
		t.Fatalf("rename event: %s", lines[1])
	}
	if r := runCLI("", "events", "--limit", "1", sock); strings.Count(r.stdout, "\n") != 2 {
		t.Fatalf("events --limit 1:\n%s", r.stdout)
	}
	if r := runCLI("", "events", "--limit", "0", sock); r.code != 2 || !strings.Contains(r.stderr, "limit must be") {
		t.Fatalf("events --limit 0: %+v", r)
	}
	if r := runCLI("", "events", "--client", "ghost", sock); r.code != 1 || !strings.Contains(r.stderr, "no such client") {
		t.Fatalf("events for an unknown client: %+v", r)
	}
}

func TestAdminCommands(t *testing.T) {
	env := newFakeEnv(t)
	startDaemon(t, env)
	sock := "--control=" + env.socket

	r := runCLI("", "admin", "setup-token", sock)
	if r.code != 0 || !strings.Contains(r.stdout, "Setup token: ") || !strings.Contains(r.stdout, ":51821") {
		t.Fatalf("setup-token: %+v", r)
	}
	if again := runCLI("", "admin", "setup-token", sock); again.stdout != r.stdout {
		t.Fatalf("the setup token changed:\n%s\n%s", r.stdout, again.stdout)
	}
	if r := runCLI("", "admin", "reset-password", sock); r.code != 1 || !strings.Contains(r.stderr, "no such account") {
		t.Fatalf("reset before the account exists: %+v", r)
	}
	if r := runCLI("", "admin", "create", "bad name", sock); r.code != 1 || !strings.Contains(r.stderr, "can't contain") {
		t.Fatalf("create with an invalid name: %+v", r)
	}

	r = runCLI("", "admin", "create", "admin", sock)
	if r.code != 0 || !strings.Contains(r.stdout, `Created the admin account "admin".`) {
		t.Fatalf("create: %+v", r)
	}
	password := strings.TrimSpace(strings.SplitN(strings.SplitN(r.stdout, "Password: ", 2)[1], "\n", 2)[0])
	if len(password) != 23 {
		t.Fatalf("password %q", password)
	}
	ctx := context.Background()
	if _, err := env.svc.Login(ctx, "admin", password, "test"); err != nil {
		t.Fatalf("logging in with the printed password: %v", err)
	}
	if r := runCLI("", "admin", "setup-token", sock); r.code != 1 || !strings.Contains(r.stderr, "admin reset-password") {
		t.Fatalf("setup-token after setup: %+v", r)
	}
	if r := runCLI("", "admin", "create", "second", sock); r.code != 1 || !strings.Contains(r.stderr, "already exists") {
		t.Fatalf("a second admin: %+v", r)
	}

	r = runCLI("", "admin", "reset-password", sock)
	if r.code != 0 || !strings.Contains(r.stdout, `New password for "admin": `) {
		t.Fatalf("reset-password: %+v", r)
	}
	if _, err := env.svc.Login(ctx, "admin", password, "test"); !errors.Is(err, service.ErrBadLogin) {
		t.Fatalf("the old password after a reset: %v", err)
	}
	if r := runCLI("", "events", sock); !strings.Contains(r.stdout, "auth.password_reset") ||
		!strings.Contains(r.stdout, "auth.admin_created") {
		t.Fatalf("admin events:\n%s", r.stdout)
	}
}

func TestServerCommands(t *testing.T) {
	env := newFakeEnv(t)
	// Which VPN addresses a resolver answers on: 0 none, 1 IPv4 only, 2 both.
	var answering atomic.Int32
	env.svc.DNSProbe = func(_ context.Context, a netip.Addr) service.DNSProbe {
		n := answering.Load()
		return service.DNSProbe{Answered: n == 2 || (n == 1 && a.Is4()), Detail: "probed " + a.String()}
	}
	startDaemon(t, env)
	sock := "--control=" + env.socket

	r := runCLI("", "server", "show", sock)
	for _, want := range []string{"Interface:", "wg0", "not set; run: drawbridge server set --endpoint", "10.8.0.1 in 10.8.0.0/24", "Client isolation:   on"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("show lacks %q:\n%s", want, r.stdout)
		}
	}

	if r := runCLI("", "server", "set", sock); r.code != 2 || !strings.Contains(r.stderr, "nothing to change") {
		t.Errorf("set without flags: %+v", r)
	}
	if r := runCLI("", "server", "set", "--port", "70000", sock); r.code != 2 {
		t.Errorf("bad port: %+v", r)
	}
	if r := runCLI("", "server", "set", "--dns", "not-an-ip", sock); r.code != 2 {
		t.Errorf("bad DNS: %+v", r)
	}
	if r := runCLI("", "server", "set", "--mtu", "9000", sock); r.code != 1 || !strings.Contains(r.stderr, "MTU") {
		t.Errorf("bad MTU: %+v", r)
	}

	r = runCLI("", "server", "set", "--dns", "9.9.9.9, 2620:fe::fe", "--client-isolation=false", "--keepalive", "0", "--mtu", "1412", sock)
	if r.code != 0 {
		t.Fatalf("set: %+v", r)
	}
	for _, want := range []string{"9.9.9.9, 2620:fe::fe", "Client isolation:   off", "Keepalive:          off", "MTU:                1412"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("set output lacks %q:\n%s", want, r.stdout)
		}
	}
	if d, _ := env.wg.Device("wg0"); d.MTU != 1412 {
		t.Errorf("the MTU change wasn't applied: %d", d.MTU)
	}

	// "--dns server" saves only the addresses that answer a test query.
	dnsLine := regexp.MustCompile(`(?m)^DNS:\s+(.*)$`)
	dnsOf := func(out string) string {
		if m := dnsLine.FindStringSubmatch(out); m != nil {
			return m[1]
		}
		return "no DNS line:\n" + out
	}
	r = runCLI("", "server", "set", "--dns", "server", sock)
	if r.code != 1 || !strings.Contains(r.stderr, "no DNS resolver answers") || !strings.Contains(r.stderr, "--force") {
		t.Errorf("no resolver answers: %+v", r)
	}
	if got := dnsOf(runCLI("", "server", "show", sock).stdout); got != "9.9.9.9, 2620:fe::fe" {
		t.Errorf("a refused --dns server changed the DNS to %q", got)
	}
	if r := runCLI("", "server", "set", "--force", sock); r.code != 2 || !strings.Contains(r.stderr, "nothing to change") {
		t.Errorf("--force alone: %+v", r)
	}
	r = runCLI("", "server", "set", "--dns", "server", "--force", sock)
	if got := dnsOf(r.stdout); r.code != 0 || !strings.HasPrefix(got, "10.8.0.1, fd") {
		t.Errorf("--force with nothing answering: %q, %+v", got, r)
	}
	answering.Store(1)
	r = runCLI("", "server", "set", "--dns", "9.9.9.9", sock)
	r = runCLI("", "server", "set", "--dns", "server", sock)
	if got := dnsOf(r.stdout); r.code != 0 || got != "10.8.0.1" {
		t.Errorf("IPv4 answers only: %q, %+v", got, r)
	}
	if !strings.Contains(r.stderr, "DNS check: fd") {
		t.Errorf("the skipped IPv6 address isn't reported:\n%s", r.stderr)
	}
	answering.Store(2)
	r = runCLI("", "server", "set", "--dns", "server", sock)
	if got := dnsOf(r.stdout); r.code != 0 || !strings.HasPrefix(got, "10.8.0.1, fd") {
		t.Errorf("both answer: %q, %+v", got, r)
	}
}

func TestCommandsWithoutDaemon(t *testing.T) {
	sock := "--control=" + filepath.Join(t.TempDir(), "missing.sock")
	r := runCLI("", "client", "list", sock)
	if r.code != 1 || !strings.Contains(r.stderr, "is drawbridge.service running?") {
		t.Fatalf("%+v", r)
	}
}

func TestRunTunnel(t *testing.T) {
	ctx := context.Background()
	env := newFakeEnv(t)
	if err := runTunnel(ctx, "down", env.store, env.rec, discard); err != nil {
		t.Fatal(err)
	}
	if _, err := env.wg.Device("wg0"); !errors.Is(err, wg.ErrNoDevice) {
		t.Fatal("tunnel down left the interface")
	}
	if err := runTunnel(ctx, "up", env.store, env.rec, discard); err != nil {
		t.Fatal(err)
	}
	if d, err := env.wg.Device("wg0"); err != nil || !d.Up {
		t.Fatalf("tunnel up: %+v, %v", d, err)
	}
}

func TestParseEndpoint(t *testing.T) {
	cases := map[string]struct {
		host string
		port uint16
	}{
		"vpn.example.com":      {"vpn.example.com", 0},
		"vpn.example.com:443":  {"vpn.example.com", 443},
		"203.0.113.5":          {"203.0.113.5", 0},
		"203.0.113.5:51820":    {"203.0.113.5", 51820},
		"2001:db8::1":          {"2001:db8::1", 0},
		"[2001:db8::1]:51820":  {"2001:db8::1", 51820},
		" vpn.example.com ":    {"vpn.example.com", 0},
		"2001:DB8:0:0:0:0:0:1": {"2001:db8::1", 0},
	}
	for in, want := range cases {
		host, port, err := parseEndpoint(in)
		if err != nil || host != want.host || port != want.port {
			t.Errorf("%q: got %q, %d, %v", in, host, port, err)
		}
	}
	for _, bad := range []string{"vpn.example.com:0", "vpn.example.com:99999", "vpn.example.com:http"} {
		if _, _, err := parseEndpoint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFormatting(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		12 * time.Second: "12s ago", 5 * time.Minute: "5m ago", 3 * time.Hour: "3h ago", 72 * time.Hour: "3d ago",
	} {
		if got := ago(now.Add(-d), now); got != want {
			t.Errorf("ago(%s) = %q, want %q", d, got, want)
		}
	}
	if ago(time.Time{}, now) != "never" {
		t.Error("zero time isn't never")
	}
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := bytesText(n); got != want {
			t.Errorf("bytesText(%d) = %q, want %q", n, got, want)
		}
	}
	for in, want := range map[string]string{"phone": "phone", "Alex's iPhone": `'Alex'\''s iPhone'`, "a b": "'a b'"} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoggerDropsTimeUnderJournald(t *testing.T) {
	var buf bytes.Buffer
	missing := filepath.Join(t.TempDir(), "no-journal.sock")
	t.Setenv("JOURNAL_STREAM", "8:12345")
	newLoggerAt(&buf, missing).Info("hello")
	if strings.Contains(buf.String(), "time=") {
		t.Fatalf("log line %q has a timestamp; journald adds its own", buf.String())
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("log line %q: with no journal to reach, the text goes to the writer", buf.String())
	}

	buf.Reset()
	t.Setenv("JOURNAL_STREAM", "")
	newLoggerAt(&buf, missing).Info("hello")
	if !strings.Contains(buf.String(), "time=") {
		t.Fatalf("log line %q has no timestamp outside journald", buf.String())
	}
}

func TestLoggerSendsFieldsToJournald(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	read := func() string {
		buf := make([]byte, 64<<10)
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, _ := conn.Read(buf)
		return string(buf[:n])
	}

	// Under systemd, a record is a journal entry with its attributes as fields, and the
	// writer (standard error) gets nothing, so the journal doesn't have it twice.
	var buf bytes.Buffer
	t.Setenv("JOURNAL_STREAM", "8:12345")
	newLoggerAt(&buf, path).Info("client added", "client", "phone")
	entry := read()
	for _, want := range []string{"PRIORITY=6\n", "DRAWBRIDGE_CLIENT=phone\n", `MESSAGE=level=INFO msg="client added" client=phone` + "\n"} {
		if !strings.Contains(entry, want) {
			t.Errorf("entry %q lacks %q", entry, want)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("standard error got %q as well", buf.String())
	}

	// Run by hand, the daemon logs to the terminal, and never to the journal of the host.
	t.Setenv("JOURNAL_STREAM", "")
	newLoggerAt(&buf, path).Info("by hand")
	if entry := read(); entry != "" {
		t.Errorf("a daemon run by hand wrote %q to the journal", entry)
	}
	if !strings.Contains(buf.String(), "by hand") {
		t.Errorf("a daemon run by hand logged %q to the terminal", buf.String())
	}
}

func TestDaemonServesTheAPIOverTLS(t *testing.T) {
	env := newFakeEnv(t)
	cert, _, err := tlscert.Ensure(filepath.Join(t.TempDir(), "tls"), tlscert.DefaultNames("server"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	web, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := control.Listen(env.socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- daemon{web: web, control: ctl, svc: env.svc, drift: time.Hour, log: discard,
			tls: tlscert.Config(cert), fingerprint: tlscert.Fingerprint(cert),
			allowed: allowlistFor(env.svc, func() []netip.Prefix { return nil })}.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("daemon: %v", err)
		}
	})

	pool := x509.NewCertPool()
	pool.AddCert(cert.Leaf)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	base := "https://localhost:" + strconv.Itoa(web.Addr().(*net.TCPAddr).Port)
	waitFor(t, func() bool {
		resp, err := client.Get(base + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	// Plain HTTP gets nowhere.
	if resp, err := http.Get("http://" + web.Addr().String() + "/healthz"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("the daemon answered plain HTTP")
		}
	}

	// The token comes from the CLI, with the certificate's fingerprint to check.
	r := runCLI("", "admin", "setup-token", "--control="+env.socket)
	if r.code != 0 || !strings.Contains(r.stdout, tlscert.Fingerprint(cert)) {
		t.Fatalf("setup-token: %+v", r)
	}
	token := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(r.stdout, "Setup token: "), "\n", 2)[0])

	post := func(path string, body any) int {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", base+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Drawbridge", "1")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if status := post("/api/setup", views.SetupRequest{Token: token, Username: "admin", Password: "a long password"}); status != http.StatusCreated {
		t.Fatalf("setup: status %d", status)
	}
	if status := post("/api/clients", views.NewClientRequest{Name: "phone"}); status != http.StatusCreated {
		t.Fatalf("add a client: status %d", status)
	}
	// The CLI sees what the web did, and the log says who did it.
	if r := runCLI("", "client", "list", "--control="+env.socket); !strings.Contains(r.stdout, "phone") {
		t.Fatalf("client list: %+v", r)
	}
	r = runCLI("", "events", "--control="+env.socket)
	if !strings.Contains(r.stdout, "admin (web 127.0.0.1)") || !strings.Contains(r.stdout, "auth.setup_completed") {
		t.Fatalf("events:\n%s", r.stdout)
	}
}

func TestServeWithTheFakeBackend(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(secret, bytes.Repeat([]byte{4}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "control.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() {
		done <- run(ctx, []string{"serve", "--backend", "fake", "--listen", "127.0.0.1:0",
			"--db", filepath.Join(dir, "db"), "--secret-key", secret, "--control", socket}, nil, io.Discard, &stderr)
	}()
	waitFor(t, func() bool {
		return runCLI("", "server", "show", "--control="+socket).code == 0
	})
	// The fake tunnel is up, so changes apply to it like the real one.
	runCLI("", "client", "add", "phone", "--control="+socket)
	if r := runCLI("", "client", "list", "--control="+socket); !strings.Contains(r.stdout, "active") {
		t.Fatalf("client list with the fake backend:\n%s", r.stdout)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("serve exited %d:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "fake WireGuard backend") {
		t.Fatalf("serve didn't warn about the fake backend:\n%s", stderr.String())
	}
}

func TestAdminAllowSetsTheUIAllowlist(t *testing.T) {
	env := newFakeEnv(t)
	startDaemon(t, env)
	sock := "--control=" + env.socket
	allowed := allowlistFor(env.svc, func() []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("192.168.4.0/22")} })
	reaches := func(addr string) bool { return lan.Contains(allowed(context.Background()), netip.MustParseAddr(addr)) }

	if reaches("100.64.10.9") {
		t.Fatal("Tailscale reaches the UI before it's allowed")
	}
	r := runCLI("", "server", "set", "--admin-allow", "100.64.10.75/24", sock)
	if r.code != 0 || !strings.Contains(r.stdout, "Admin sources:") || !strings.Contains(r.stdout, "100.64.10.0/24") {
		t.Fatalf("set: %+v", r)
	}
	if !reaches("100.64.10.9") || !reaches("192.168.4.20") || reaches("100.64.11.9") || reaches("203.0.113.5") {
		t.Fatalf("allowlist %v", allowed(context.Background()))
	}
	if r := runCLI("", "server", "show", sock); !strings.Contains(r.stdout, "100.64.10.0/24") {
		t.Errorf("show:\n%s", r.stdout)
	}
	if r := runCLI("", "events", sock); !strings.Contains(r.stdout, "server.settings_changed") || !strings.Contains(r.stdout, "admin_allowed") {
		t.Errorf("events:\n%s", r.stdout)
	}

	if r := runCLI("", "server", "set", "--admin-allow", "not-a-prefix", sock); r.code != 2 {
		t.Errorf("bad prefix: %+v", r)
	}
	if r := runCLI("", "server", "set", "--admin-allow", "203.0.113.0/24", sock); r.code != 1 || !strings.Contains(r.stderr, "private range") {
		t.Errorf("public range: %+v", r)
	}
	if r := runCLI("", "server", "set", "--admin-allow", "0.0.0.0/0", sock); r.code != 1 {
		t.Errorf("everything: %+v", r)
	}
	if !reaches("100.64.10.9") {
		t.Fatal("a rejected change removed the allowed source")
	}

	if r := runCLI("", "server", "set", "--admin-allow", "none", sock); r.code != 0 || strings.Contains(r.stdout, "100.64.10.0/24") {
		t.Fatalf("none: %+v", r)
	}
	if reaches("100.64.10.9") {
		t.Fatal("Tailscale still reaches the UI after removing it")
	}
}

func TestDoctorCommand(t *testing.T) {
	env := newFakeEnv(t)
	env.svc.Diag = &diag.Host{
		FS: fstest.MapFS{
			"proc/sys/net/ipv4/ip_forward":          {Data: []byte("0\n")},
			"proc/sys/net/ipv6/conf/all/forwarding": {Data: []byte("1\n")},
		},
		Network: func() (lan.Snapshot, error) { return lan.Snapshot{}, errors.New("no netlink here") },
		Nft:     func(context.Context) ([]byte, error) { return nil, errors.New("no nft here") },
	}
	startDaemon(t, env)
	sock := "--control=" + env.socket

	r := runCLI("", "doctor", sock)
	if r.code != 1 {
		t.Fatalf("doctor with a failing check exited %d:\n%s%s", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{
		"PASS  Tunnel",
		"FAIL  Forwarding sysctls",
		"      IPv4 forwarding is off, so VPN clients' traffic isn't routed.",
		"      Fix: sudo sysctl -w net.ipv4.ip_forward=1. ",
		"SKIP  Uplink",
		" failed, ",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("doctor's output lacks %q:\n%s", want, r.stdout)
		}
	}
	for _, line := range strings.Split(r.stdout, "\n") {
		if strings.HasPrefix(line, "      ") && !strings.Contains(line, "Fix: ") && len(line) > doctorWrap {
			t.Errorf("a detail line is %d columns wide: %q", len(line), line)
		}
	}
	if r.stderr != "" {
		t.Errorf("doctor wrote to stderr: %q", r.stderr)
	}

	if r := runCLI("", "doctor", "extra", sock); r.code != 2 || !strings.Contains(r.stderr, `unexpected argument "extra"`) {
		t.Errorf("doctor with an argument: %+v", r)
	}

	// The diagnostics change nothing, so they leave no events.
	if r := runCLI("", "events", sock); strings.Contains(r.stdout, "doctor") || strings.Contains(r.stdout, "diagnos") {
		t.Errorf("doctor recorded an event:\n%s", r.stdout)
	}
}

func TestDoctorWithoutDiagnostics(t *testing.T) {
	env := newFakeEnv(t)
	startDaemon(t, env)
	if r := runCLI("", "doctor", "--control="+env.socket); r.code != 1 || !strings.Contains(r.stderr, "drawbridge:") || r.stdout != "" {
		t.Errorf("doctor without diagnostics: %+v", r)
	}
	if r := runCLI("", "doctor", "--control="+filepath.Join(t.TempDir(), "none.sock")); r.code != 1 || !strings.Contains(r.stderr, "is drawbridge.service running?") {
		t.Errorf("doctor without a daemon: %+v", r)
	}
}

func TestPrintDiagnostics(t *testing.T) {
	var out bytes.Buffer
	failed := printDiagnostics(&out, views.Diagnostics{Checks: []views.DiagnosticCheck{
		{Name: "One", Status: "pass", Detail: "Fine."},
		{Name: "Two", Status: "warn", Detail: strings.Repeat("word ", 30), Hint: "do this && that"},
		{Name: "Three", Status: "skip", Detail: "Couldn't read it.", Hint: "not shown"},
	}})
	if failed != 0 {
		t.Errorf("%d failed, want 0: warnings don't count", failed)
	}
	want := `PASS  One
      Fine.
WARN  Two
      word word word word word word word word word word word word word word
      word word word word word word word word word word word word word word
      word word
      Fix: do this && that
SKIP  Three
      Couldn't read it.

1 passed, 1 warning, 0 failed, 1 skipped
`
	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}
