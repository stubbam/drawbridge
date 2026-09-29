package control

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/stuffam/drawbridge/internal/firewall"
	"github.com/stuffam/drawbridge/internal/keys"
	"github.com/stuffam/drawbridge/internal/reconcile"
	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/views"
	"github.com/stuffam/drawbridge/internal/wg"
)

type nopFirewall struct{}

func (nopFirewall) Apply(context.Context, firewall.Ruleset) error  { return nil }
func (nopFirewall) Remove(context.Context) error                   { return nil }
func (nopFirewall) Revision(context.Context) (string, bool, error) { return "", true, nil }

type testEnv struct {
	client *Client
	wg     *wg.Fake
	rec    *reconcile.Reconciler
	socket string
}

func newTestEnv(t *testing.T, tunnelUp bool) *testEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	sealer, err := keys.NewSealer(bytes.Repeat([]byte{3}, keys.SecretSize))
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
	rec := &reconcile.Reconciler{State: st, WG: backend, Firewall: nopFirewall{}}
	if tunnelUp {
		if _, err := rec.Up(ctx); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &service.Service{Store: st, Rec: rec, WG: backend, Log: log}

	sock := filepath.Join(dir, "control.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(svc, log, "AB:CD")
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &testEnv{client: NewClient(sock), wg: backend, rec: rec, socket: sock}
}

func status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func TestSocketPermissions(t *testing.T) {
	env := newTestEnv(t, true)
	info, err := os.Stat(env.socket)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o660 {
		t.Fatalf("socket mode %o, want 0660", perm)
	}
	// A stale socket from an earlier run is replaced.
	if _, err := Listen(env.socket); err != nil {
		t.Fatalf("relistening over a stale socket: %v", err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, true)

	s, err := env.client.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Interface != "wg0" || s.Endpoint != "" || s.IPv4Address != netip.MustParseAddr("10.8.0.1") || s.PublicKey == "" {
		t.Fatalf("settings %+v", s)
	}

	host, port := "VPN.Example.com", uint16(443)
	res, err := env.client.UpdateSettings(ctx, views.SettingsPatch{EndpointHost: &host, EndpointPort: &port})
	if err != nil {
		t.Fatal(err)
	}
	if res.Settings.Endpoint != "vpn.example.com:443" || res.Warning != "" {
		t.Fatalf("result %+v", res)
	}

	bad := 9000
	if _, err := env.client.UpdateSettings(ctx, views.SettingsPatch{MTU: &bad}); status(err) != http.StatusBadRequest {
		t.Fatalf("invalid MTU: err %v (status %d), want 400", err, status(err))
	}

	dns := []netip.Addr{netip.MustParseAddr("9.9.9.9")}
	res, err = env.client.UpdateSettings(ctx, views.SettingsPatch{DNS: &dns})
	if err != nil || len(res.Settings.DNS) != 1 {
		t.Fatalf("custom DNS: %+v, %v", res.Settings.DNS, err)
	}
	res, err = env.client.UpdateSettings(ctx, views.SettingsPatch{DNSDefault: true})
	if err != nil || len(res.Settings.DNS) != 2 || res.Settings.DNS[0] != netip.MustParseAddr("10.8.0.1") {
		t.Fatalf("default DNS: %+v, %v", res.Settings.DNS, err)
	}
}

func TestClientLifecycle(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, true)

	added, err := env.client.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if added.Client.IPv4 != netip.MustParseAddr("10.8.0.2") || !added.Client.Enabled || added.Warning != "" {
		t.Fatalf("added %+v", added)
	}
	if _, err := env.client.AddClient(ctx, "phone"); status(err) != http.StatusConflict {
		t.Fatalf("duplicate: status %d, want 409", status(err))
	}
	if _, err := env.client.AddClient(ctx, "bad/name"); status(err) != http.StatusBadRequest {
		t.Fatalf("invalid name: status %d, want 400", status(err))
	}

	// Live status comes from the tunnel.
	key := mustParseKey(t, added.Client.PublicKey)
	env.wg.SetHandshake("wg0", key, wg.Peer{
		Endpoint:      netip.MustParseAddrPort("203.0.113.9:40000"),
		LastHandshake: time.Now(),
		ReceiveBytes:  1234,
	})
	list, err := env.client.Clients(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %+v, %v", list, err)
	}
	if list[0].Peer == nil || list[0].Peer.Endpoint != "203.0.113.9:40000" || list[0].Peer.ReceiveBytes != 1234 {
		t.Fatalf("peer status %+v", list[0].Peer)
	}

	paused, err := env.client.SetEnabled(ctx, "phone", false)
	if err != nil || paused.Client.Enabled {
		t.Fatalf("pause: %+v, %v", paused, err)
	}
	if c, _ := env.client.Client(ctx, "phone"); c.Peer != nil {
		t.Fatal("a paused client still has a peer in the tunnel")
	}
	if _, err := env.client.SetEnabled(ctx, "phone", true); err != nil {
		t.Fatal(err)
	}
	if c, _ := env.client.Client(ctx, "phone"); c.Peer == nil {
		t.Fatal("a resumed client has no peer")
	}

	if _, err := env.client.DeleteClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.client.Client(ctx, "phone"); status(err) != http.StatusNotFound {
		t.Fatalf("after delete: status %d, want 404", status(err))
	}
}

func TestNamesWithSpacesAndApostrophes(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, true)
	name := "Alex's iPhone"
	if _, err := env.client.AddClient(ctx, name); err != nil {
		t.Fatal(err)
	}
	c, err := env.client.Client(ctx, name)
	if err != nil || c.Name != name {
		t.Fatalf("got %+v, %v", c, err)
	}
	if _, err := env.client.SetEnabled(ctx, name, false); err != nil {
		t.Fatal(err)
	}
}

func TestConfigNeedsAnEndpoint(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, true)
	if _, err := env.client.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.client.Config(ctx, "phone"); status(err) != http.StatusConflict ||
		!strings.Contains(err.Error(), "server set --endpoint") {
		t.Fatalf("no endpoint: err %v (status %d)", err, status(err))
	}
	host := "vpn.example.com"
	if _, err := env.client.UpdateSettings(ctx, views.SettingsPatch{EndpointHost: &host}); err != nil {
		t.Fatal(err)
	}
	conf, err := env.client.Config(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "Endpoint = vpn.example.com:51820") || !strings.HasPrefix(conf, "[Interface]\n") {
		t.Fatalf("config:\n%s", conf)
	}
}

func TestChangesWithTheTunnelDownWarn(t *testing.T) {
	env := newTestEnv(t, false)
	res, err := env.client.AddClient(context.Background(), "phone")
	if err != nil {
		t.Fatal(err)
	}
	if res.ApplyFailed || !strings.Contains(res.Warning, "tunnel is stopped") {
		t.Fatalf("warning %q", res.Warning)
	}
}

func TestApplyFailureIsReportedButSaved(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, true)
	env.wg.Err = errors.New("netlink: operation not permitted")
	res, err := env.client.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if !res.ApplyFailed || !strings.Contains(res.Warning, "saved, but applying it failed") {
		t.Fatalf("warning %q", res.Warning)
	}
	if _, err := env.client.Client(ctx, "phone"); err != nil {
		t.Fatalf("the client wasn't saved: %v", err)
	}
}

func TestUnknownFieldsAreRejected(t *testing.T) {
	env := newTestEnv(t, true)
	req, _ := http.NewRequest(http.MethodPost, "http://drawbridge/v1/clients", strings.NewReader(`{"name":"x","admin":true}`))
	resp, err := env.client.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

func TestMissingDaemon(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	_, err := c.Settings(context.Background())
	if err == nil || !strings.Contains(err.Error(), "is drawbridge.service running?") {
		t.Fatalf("err %v", err)
	}
}

func mustParseKey(t *testing.T, s string) wgtypes.Key {
	t.Helper()
	k, err := wgtypes.ParseKey(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
