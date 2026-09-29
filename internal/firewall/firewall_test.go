package firewall

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stuffam/drawbridge/internal/lan"
)

var rules = Rules{
	Interface:       "wg0",
	IPv4:            netip.MustParsePrefix("10.8.0.0/24"),
	IPv6:            netip.MustParsePrefix("fd3a:5c1e:92b0:1::/64"),
	ClientIsolation: true,
	AdminPort:       51821,
	AdminAllowed: lan.Allowlist(
		[]netip.Prefix{netip.MustParsePrefix("10.8.0.0/24"), netip.MustParsePrefix("fd3a:5c1e:92b0:1::/64")},
		[]netip.Prefix{netip.MustParsePrefix("192.168.4.0/22"), netip.MustParsePrefix("2001:db8:1234:5600::/64")},
	),
}

func TestRenderGolden(t *testing.T) {
	rs := Render(rules)
	golden := filepath.Join("testdata", "ruleset.nft")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(rs.Text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Text != string(want) {
		t.Fatalf("ruleset differs from %s (UPDATE_GOLDEN=1 rewrites it):\n%s", golden, rs.Text)
	}
	if !strings.Contains(rs.Text, `comment "drawbridge rev `+rs.Revision+`"`) {
		t.Fatal("the table comment doesn't carry the revision")
	}
}

func TestRenderVariants(t *testing.T) {
	open := rules
	open.ClientIsolation = false
	if strings.Contains(Render(open).Text, `oifname "wg0" drop`) {
		t.Error("isolation off still renders the isolation rule")
	}

	v4only := rules
	v4only.IPv6 = netip.Prefix{}
	if strings.Contains(Render(v4only).Text, "ip6 saddr fd") {
		t.Error("IPv6 off still renders NAT66")
	}

	noAdmin := rules
	noAdmin.AdminPort = 0
	if text := Render(noAdmin).Text; strings.Contains(text, "admin_allowed") || strings.Contains(text, "chain input") {
		t.Error("no admin port still renders the admin chain")
	}

	// A LAN change changes the revision, so the drift check applies it.
	moved := rules
	moved.AdminAllowed = lan.Allowlist(nil, []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")})
	if Render(moved).Revision == Render(rules).Revision {
		t.Error("a different admin allowlist has the same revision")
	}

	empty := rules
	empty.AdminAllowed = nil
	if text := Render(empty).Text; strings.Contains(text, "elements") || !strings.Contains(text, "set admin_allowed6 {") {
		t.Errorf("an empty allowlist should render empty sets:\n%s", text)
	}

	if Render(rules).Revision == Render(open).Revision {
		t.Error("different rulesets have the same revision")
	}
	first, second := Render(rules), Render(rules)
	if first.Revision != second.Revision {
		t.Error("rendering isn't deterministic")
	}
}

type call struct {
	stdin string
	args  []string
}

func recorder(out []byte, err error) (*Applier, *[]call) {
	var calls []call
	return &Applier{Run: func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		calls = append(calls, call{stdin, args})
		return out, err
	}}, &calls
}

func TestApplyReplacesTableAtomically(t *testing.T) {
	a, calls := recorder(nil, nil)
	rs := Render(rules)
	if err := a.Apply(context.Background(), rs); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if strings.Join(c.args, " ") != "-f -" {
		t.Fatalf("args %v", c.args)
	}
	if !strings.HasPrefix(c.stdin, "table inet drawbridge\ndelete table inet drawbridge\n") {
		t.Fatalf("the transaction doesn't replace the table:\n%s", c.stdin)
	}
	if !strings.HasSuffix(c.stdin, rs.Text) {
		t.Fatal("the transaction doesn't end with the ruleset")
	}
}

func TestApplySavesACopy(t *testing.T) {
	a, _ := recorder(nil, nil)
	a.SavePath = filepath.Join(t.TempDir(), "nftables.conf")
	rs := Render(rules)
	if err := a.Apply(context.Background(), rs); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(a.SavePath)
	if err != nil || string(saved) != rs.Text {
		t.Fatalf("saved copy %q, %v", saved, err)
	}
	if info, _ := os.Stat(a.SavePath); info.Mode().Perm() != 0o600 {
		t.Fatalf("saved copy mode %o, want 0600", info.Mode().Perm())
	}
}

func TestApplyReportsNftOutput(t *testing.T) {
	a, _ := recorder([]byte("Error: syntax error"), errors.New("exit status 1"))
	err := a.Apply(context.Background(), Render(rules))
	if err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("err %v, want nft's message", err)
	}
}

func TestRevision(t *testing.T) {
	listing := `{"nftables": [{"metainfo": {"version": "1.0.9"}}, {"table": {"family": "inet", "name": "drawbridge", "handle": 3, "comment": "drawbridge rev 0123456789ab"}}, {"chain": {"name": "forward"}}]}`
	a, calls := recorder([]byte(listing), nil)
	rev, ok, err := a.Revision(context.Background())
	if err != nil || !ok || rev != "0123456789ab" {
		t.Fatalf("got %q, %v, %v", rev, ok, err)
	}
	if strings.Join((*calls)[0].args, " ") != "-j list table inet drawbridge" {
		t.Fatalf("args %v", (*calls)[0].args)
	}

	missing, _ := recorder([]byte("Error: No such file or directory\nlist table inet drawbridge"), errors.New("exit status 1"))
	if _, ok, err := missing.Revision(context.Background()); ok || err != nil {
		t.Fatalf("missing table: ok %v, err %v", ok, err)
	}

	broken, _ := recorder([]byte("Error: Operation not permitted"), errors.New("exit status 1"))
	if _, _, err := broken.Revision(context.Background()); err == nil {
		t.Fatal("a permission error was reported as a missing table")
	}
}

// TestRulesetLoads checks the rendered ruleset with nft itself when it can. nft -c is a
// dry run, but it still asks the kernel, so it needs CAP_NET_ADMIN; the integration tests
// load the ruleset for real.
func TestRulesetLoads(t *testing.T) {
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft isn't installed")
	}
	if os.Geteuid() != 0 {
		t.Skip("nft -c needs root (CAP_NET_ADMIN)")
	}
	out, err := exec.Command("nft", "-c", "-f", "testdata/ruleset.nft").CombinedOutput()
	if err != nil {
		t.Fatalf("nft -c rejects the ruleset: %v\n%s", err, out)
	}
}

func TestMemory(t *testing.T) {
	ctx := context.Background()
	var m Memory
	if _, exists, _ := m.Revision(ctx); exists {
		t.Fatal("a new Memory has a table")
	}
	rs := Render(rules)
	_ = m.Apply(ctx, rs)
	if rev, exists, _ := m.Revision(ctx); !exists || rev != rs.Revision {
		t.Fatalf("Revision = %q, %v", rev, exists)
	}
	_ = m.Remove(ctx)
	if _, exists, _ := m.Revision(ctx); exists {
		t.Fatal("Remove left the table")
	}
}
