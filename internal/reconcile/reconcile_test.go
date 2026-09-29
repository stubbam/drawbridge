package reconcile

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/stuffam/drawbridge/internal/firewall"
	"github.com/stuffam/drawbridge/internal/keys"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/wg"
)

type fakeFirewall struct {
	revision string
	text     string
	exists   bool
	applied  int
	removed  int
	err      error
}

func (f *fakeFirewall) Apply(_ context.Context, rs firewall.Ruleset) error {
	if f.err != nil {
		return f.err
	}
	f.revision, f.text, f.exists = rs.Revision, rs.Text, true
	f.applied++
	return nil
}

func (f *fakeFirewall) Remove(context.Context) error {
	f.exists = false
	f.removed++
	return nil
}

func (f *fakeFirewall) Revision(context.Context) (string, bool, error) {
	return f.revision, f.exists, nil
}

type env struct {
	store *store.Store
	wg    *wg.Fake
	fw    *fakeFirewall
	rec   *Reconciler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	sealer, err := keys.NewSealer(bytes.Repeat([]byte{1}, keys.SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db"), sealer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	e := &env{store: st, wg: wg.NewFake(), fw: &fakeFirewall{}}
	e.rec = &Reconciler{State: st, WG: e.wg, Firewall: e.fw}
	return e
}

func (e *env) device(t *testing.T) wg.Device {
	t.Helper()
	d, err := e.wg.Device("wg0")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *env) up(t *testing.T) Result {
	t.Helper()
	res, err := e.rec.Up(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *env) sync(t *testing.T) Result {
	t.Helper()
	res, err := e.rec.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func peerKeys(d wg.Device) []wgtypes.Key {
	var ks []wgtypes.Key
	for _, p := range d.Peers {
		ks = append(ks, p.PublicKey)
	}
	return ks
}

func TestUpCreatesAndConfiguresTheTunnel(t *testing.T) {
	e := newEnv(t)
	s, _ := e.store.Settings(context.Background())

	res := e.up(t)
	if len(res.Changes) == 0 || res.Changes[0] != "created wg0" {
		t.Fatalf("changes %v", res.Changes)
	}
	d := e.device(t)
	if d.PrivateKey != s.PrivateKey || d.ListenPort != 51820 || d.MTU != 1420 || !d.Up {
		t.Fatalf("device %+v", d)
	}
	srv, _ := s.ServerAddrs()
	want := []netip.Prefix{netip.PrefixFrom(srv.IPv4, 24), netip.PrefixFrom(srv.IPv6, 64)}
	if !samePrefixes(d.Addrs, want) {
		t.Fatalf("addresses %v, want %v", d.Addrs, want)
	}
	if !e.fw.exists || e.fw.applied != 1 {
		t.Fatalf("firewall not applied: %+v", e.fw)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.up(t)
	if _, err := e.store.AddClient(context.Background(), "phone"); err != nil {
		t.Fatal(err)
	}
	e.sync(t)
	calls := len(e.wg.Calls)
	applied := e.fw.applied

	for _, res := range []Result{e.sync(t), e.up(t)} {
		if len(res.Changes) != 0 {
			t.Fatalf("a repeat run changed %v", res.Changes)
		}
	}
	if len(e.wg.Calls) != calls || e.fw.applied != applied {
		t.Fatalf("a repeat run made calls: %v", e.wg.Calls[calls:])
	}
}

func TestSyncLeavesAStoppedTunnelDown(t *testing.T) {
	e := newEnv(t)
	res := e.sync(t)
	if !res.TunnelDown {
		t.Fatal("Sync didn't report the tunnel as down")
	}
	if len(e.wg.Calls) != 0 || e.fw.applied != 0 {
		t.Fatalf("Sync touched a stopped tunnel: %v, firewall applied %d", e.wg.Calls, e.fw.applied)
	}
}

func TestClientLifecycle(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.up(t)

	phone, _ := e.store.AddClient(ctx, "phone")
	e.sync(t)
	d := e.device(t)
	if len(d.Peers) != 1 || d.Peers[0].PublicKey != phone.PublicKey || d.Peers[0].PresharedKey != phone.PresharedKey {
		t.Fatalf("peers %+v", d.Peers)
	}
	if !samePrefixes(d.Peers[0].AllowedIPs, phone.AllowedIPs()) {
		t.Fatalf("AllowedIPs %v, want %v", d.Peers[0].AllowedIPs, phone.AllowedIPs())
	}

	// Adding a second client touches only the new peer.
	laptop, _ := e.store.AddClient(ctx, "laptop")
	before := len(e.wg.Calls)
	res := e.sync(t)
	if len(res.Changes) != 1 || !strings.HasPrefix(res.Changes[0], "added peer ") {
		t.Fatalf("changes %v, want only the new peer", res.Changes)
	}
	if got := e.wg.Calls[before:]; len(got) != 1 || got[0] != "configure wg0" {
		t.Fatalf("calls %v", got)
	}

	// Pausing removes the peer entirely; resuming restores it.
	if _, err := e.store.SetEnabled(ctx, store.ByName("phone"), false); err != nil {
		t.Fatal(err)
	}
	e.sync(t)
	if ks := peerKeys(e.device(t)); len(ks) != 1 || ks[0] != laptop.PublicKey {
		t.Fatalf("after pause: peers %v", ks)
	}
	if _, err := e.store.SetEnabled(ctx, store.ByName("phone"), true); err != nil {
		t.Fatal(err)
	}
	e.sync(t)
	if ks := peerKeys(e.device(t)); len(ks) != 2 || !slices.Contains(ks, phone.PublicKey) {
		t.Fatalf("after resume: peers %v", ks)
	}

	// Deleting removes the peer.
	if _, err := e.store.DeleteClient(ctx, store.ByName("laptop")); err != nil {
		t.Fatal(err)
	}
	e.sync(t)
	if ks := peerKeys(e.device(t)); len(ks) != 1 || ks[0] != phone.PublicKey {
		t.Fatalf("after delete: peers %v", ks)
	}
}

func TestSyncCorrectsDrift(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.up(t)
	phone, _ := e.store.AddClient(ctx, "phone")
	e.sync(t)
	s, _ := e.store.Settings(ctx)
	srv, _ := s.ServerAddrs()
	v6 := netip.PrefixFrom(srv.IPv6, 64)

	// Someone runs `wg set` and `ip` by hand, and nftables.service flushes the ruleset.
	stray, _ := wgtypes.GeneratePrivateKey()
	_ = e.wg.Configure("wg0", wgtypes.Config{Peers: []wgtypes.PeerConfig{
		{PublicKey: phone.PublicKey, Remove: true},
		{PublicKey: stray.PublicKey()},
	}})
	_ = e.wg.SetMTU("wg0", 1500)
	_ = e.wg.DelAddr("wg0", v6)
	_ = e.wg.AddAddr("wg0", netip.MustParsePrefix("192.168.99.1/24"))
	e.fw.exists = false

	res := e.sync(t)
	if len(res.Changes) == 0 {
		t.Fatal("no drift was corrected")
	}
	d := e.device(t)
	if ks := peerKeys(d); len(ks) != 1 || ks[0] != phone.PublicKey {
		t.Fatalf("peers %v, want only phone", ks)
	}
	if d.MTU != 1420 {
		t.Fatalf("MTU %d", d.MTU)
	}
	if !slices.Contains(d.Addrs, v6) || slices.Contains(d.Addrs, netip.MustParsePrefix("192.168.99.1/24")) {
		t.Fatalf("addresses %v", d.Addrs)
	}
	if !e.fw.exists {
		t.Fatal("the firewall table wasn't restored")
	}
}

func TestFirewallRevisionMismatchIsReapplied(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.up(t)
	e.fw.revision = "stale"
	e.sync(t)
	if e.fw.applied != 2 {
		t.Fatalf("applied %d times, want a reapply for a stale revision", e.fw.applied)
	}
	// A settings change that alters the ruleset is applied too.
	if _, err := e.store.UpdateSettings(ctx, func(s *model.Settings) error {
		s.ClientIsolation = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.sync(t)
	if e.fw.applied != 3 {
		t.Fatalf("applied %d times after turning isolation off", e.fw.applied)
	}
}

func TestAdminAllowlistFollowsTheLAN(t *testing.T) {
	e := newEnv(t)
	lanPrefixes := []netip.Prefix{netip.MustParsePrefix("192.168.4.0/22")}
	e.rec.AdminPort = model.AdminPort
	e.rec.LAN = func() []netip.Prefix { return lanPrefixes }
	e.up(t)
	for _, want := range []string{"tcp dport 51821 ip saddr != @admin_allowed4 drop", "192.168.4.0/22",
		"10.8.0.0/24", "fe80::/10"} {
		if !strings.Contains(e.fw.text, want) {
			t.Errorf("the ruleset lacks %q:\n%s", want, e.fw.text)
		}
	}
	if res := e.sync(t); len(res.Changes) != 0 {
		t.Fatalf("an unchanged LAN changed something: %v", res.Changes)
	}

	// The router hands out a new IPv6 prefix: the next sync admits it.
	lanPrefixes = append(lanPrefixes, netip.MustParsePrefix("2001:db8:1234:5600::/64"))
	e.sync(t)
	if e.fw.applied != 2 || !strings.Contains(e.fw.text, "2001:db8:1234:5600::/64") {
		t.Fatalf("applied %d times; ruleset:\n%s", e.fw.applied, e.fw.text)
	}
}

func TestSettingsChangesApplyLive(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.up(t)
	if _, err := e.store.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	e.sync(t)
	if _, err := e.store.UpdateSettings(ctx, func(s *model.Settings) error {
		s.ListenPort = 51900
		s.MTU = 1412
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := len(e.wg.Calls)
	e.sync(t)
	d := e.device(t)
	if d.ListenPort != 51900 || d.MTU != 1412 {
		t.Fatalf("device %+v", d)
	}
	for _, c := range e.wg.Calls[before:] {
		if strings.HasPrefix(c, "create") || strings.HasPrefix(c, "delete") {
			t.Fatalf("a settings change recreated the interface: %v", e.wg.Calls[before:])
		}
	}
	if len(d.Peers) != 1 {
		t.Fatal("a settings change dropped the peer")
	}
}

func TestFirewallFailureStillConfiguresTheTunnel(t *testing.T) {
	e := newEnv(t)
	e.fw.err = errors.New("nft isn't installed")
	_, err := e.rec.Up(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nft isn't installed") {
		t.Fatalf("err %v", err)
	}
	if d := e.device(t); !d.Up || d.PrivateKey == (wgtypes.Key{}) {
		t.Fatal("the tunnel wasn't configured after a firewall failure")
	}
}

func TestDown(t *testing.T) {
	e := newEnv(t)
	e.up(t)
	if err := e.rec.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.wg.Device("wg0"); !errors.Is(err, wg.ErrNoDevice) {
		t.Fatal("the interface still exists")
	}
	if e.fw.exists || e.fw.removed != 1 {
		t.Fatalf("firewall %+v", e.fw)
	}
	// Down on a stopped tunnel is fine.
	if err := e.rec.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFileLockSerializes(t *testing.T) {
	l := FileLock{Path: filepath.Join(t.TempDir(), "lock")}
	unlock, err := l.Lock()
	if err != nil {
		t.Fatal(err)
	}
	var acquired atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		u, err := l.Lock()
		if err != nil {
			t.Error(err)
			return
		}
		acquired.Store(true)
		u()
	}()
	time.Sleep(100 * time.Millisecond)
	if acquired.Load() {
		t.Fatal("a second holder took the lock while the first held it")
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the second holder never got the lock")
	}
	if !acquired.Load() {
		t.Fatal("the second holder didn't get the lock")
	}
}

// The admin's extra sources reach the nftables ruleset, and leave it again.
func TestExtraAdminSourcesReachTheRuleset(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.rec.AdminPort = model.AdminPort
	e.rec.LAN = func() []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("192.168.4.0/22")} }
	e.up(t)
	if strings.Contains(e.fw.text, "100.64.10.0/24") {
		t.Fatalf("the ruleset admits Tailscale before it's set:\n%s", e.fw.text)
	}

	set := func(ps ...netip.Prefix) {
		t.Helper()
		if _, err := e.store.UpdateSettings(ctx, func(s *model.Settings) error { s.AdminAllowed = ps; return nil }); err != nil {
			t.Fatal(err)
		}
		e.sync(t)
	}
	set(netip.MustParsePrefix("100.64.10.0/24"), netip.MustParsePrefix("fd7a:115c:a1e0::/48"))
	for _, want := range []string{"100.64.10.0/24", "fd7a:115c:a1e0::/48", "192.168.4.0/22", "10.8.0.0/24"} {
		if !strings.Contains(e.fw.text, want) {
			t.Errorf("the ruleset lacks %q:\n%s", want, e.fw.text)
		}
	}
	applied := e.fw.applied
	if res := e.sync(t); len(res.Changes) != 0 || e.fw.applied != applied {
		t.Fatalf("an unchanged setting changed something: %v", res.Changes)
	}

	set()
	if strings.Contains(e.fw.text, "100.64.10.0/24") || strings.Contains(e.fw.text, "fd7a:115c:a1e0::/48") {
		t.Fatalf("the ruleset still admits the removed sources:\n%s", e.fw.text)
	}
}
