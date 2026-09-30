package diag

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// These build the JSON that `nft -j list ruleset` prints, in the shapes nftables 1.0.9 uses.

func nftDoc(items ...string) string {
	all := append([]string{`{"metainfo":{"version":"1.0.9","json_schema_version":1}}`}, items...)
	return `{"nftables":[` + strings.Join(all, ",") + `]}`
}

func baseChain(family, table, name, hook, policy string) string {
	return fmt.Sprintf(`{"chain":{"family":%q,"table":%q,"name":%q,"type":"filter","hook":%q,"prio":0,"policy":%q}}`,
		family, table, name, hook, policy)
}

func plainChain(family, table, name string) string {
	return fmt.Sprintf(`{"chain":{"family":%q,"table":%q,"name":%q}}`, family, table, name)
}

func rl(family, table, chain string, exprs ...string) string {
	return fmt.Sprintf(`{"rule":{"family":%q,"table":%q,"chain":%q,"expr":[%s]}}`,
		family, table, chain, strings.Join(exprs, ","))
}

func ifMatch(key, op, name string) string {
	return fmt.Sprintf(`{"match":{"op":%q,"left":{"meta":{"key":%q}},"right":%q}}`, op, key, name)
}

func iif(name string) string { return ifMatch("iifname", "==", name) }
func oif(name string) string { return ifMatch("oifname", "==", name) }

func ifSet(key string, names ...string) string {
	return fmt.Sprintf(`{"match":{"op":"==","left":{"meta":{"key":%q}},"right":{"set":["%s"]}}}`,
		key, strings.Join(names, `","`))
}

func jump(target string) string { return fmt.Sprintf(`{"jump":{"target":%q}}`, target) }
func goTo(target string) string { return fmt.Sprintf(`{"goto":{"target":%q}}`, target) }

const (
	accept  = `{"accept":null}`
	drop    = `{"drop":null}`
	ret     = `{"return":null}`
	counter = `{"counter":{"packets":0,"bytes":0}}`
	ctState = `{"match":{"op":"in","left":{"ct":{"key":"state"}},"right":["established","related"]}}`
	srcNet  = `{"match":{"op":"==","left":{"payload":{"protocol":"ip","field":"saddr"}},"right":{"prefix":{"addr":"10.0.0.0","len":8}}}}`
	comment = `{"xt":{"type":"match","name":"comment"}}`
	conntrk = `{"xt":{"type":"match","name":"conntrack"}}`
)

// dockerRules is rootful Docker's filter table: a drop policy on FORWARD, and a
// DOCKER-USER chain, where an admin's accepts belong, that returns by default.
func dockerRules(userRules ...string) []string {
	items := []string{
		baseChain("ip", "filter", "FORWARD", "forward", "drop"),
		plainChain("ip", "filter", "DOCKER-USER"),
		plainChain("ip", "filter", "DOCKER-FORWARD"),
		rl("ip", "filter", "FORWARD", counter, jump("DOCKER-USER")),
		rl("ip", "filter", "FORWARD", counter, jump("DOCKER-FORWARD")),
		rl("ip", "filter", "DOCKER-FORWARD", ctState, counter, accept),
		rl("ip", "filter", "DOCKER-FORWARD", iif("docker0"), counter, accept),
		rl("ip", "filter", "DOCKER-FORWARD", oif("docker0"), counter, drop),
	}
	items = append(items, userRules...)
	return append(items, rl("ip", "filter", "DOCKER-USER", counter, ret))
}

// ufwRules is ufw's filter table with its default deny for routed traffic.
func ufwRules(userRules ...string) []string {
	items := []string{
		baseChain("ip", "filter", "FORWARD", "forward", "drop"),
		plainChain("ip", "filter", "ufw-before-forward"),
		plainChain("ip", "filter", "ufw-user-forward"),
		rl("ip", "filter", "FORWARD", counter, jump("ufw-before-forward")),
		rl("ip", "filter", "FORWARD", counter, jump("ufw-user-forward")),
		rl("ip", "filter", "ufw-before-forward", ctState, counter, accept),
	}
	return append(items, userRules...)
}

func TestHostFirewall(t *testing.T) {
	// The IPv6 twin of each ruleset: ip6tables' table is `ip6 filter`, or one `inet`
	// table holds both. Both families' chains get the same verdict.
	inet := func(items []string) []string {
		out := make([]string, len(items))
		for i, s := range items {
			out[i] = strings.ReplaceAll(s, `"family":"ip"`, `"family":"ip6"`)
		}
		return out
	}

	cases := []tc{
		{"nothing else loaded", func(f *fixture) { f.nft = nftDoc() }, "host-firewall", Pass, "No other firewall", ""},
		{"a forward chain that accepts", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "accept"))
		}, "host-firewall", Pass, "", ""},
		{"a drop policy with a blanket accept", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), rl("inet", "fw", "forward", counter, accept))
		}, "host-firewall", Pass, "", ""},
		{"a drop policy that other software configured", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"))
		}, "host-firewall", Warn, "Forwarded packets are dropped by default by the inet fw forward chain",
			"accepting forwarded traffic in and out of wg0. See docs/REQUIREMENTS.md"},
		{"input chains aren't judged", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "input", "input", "drop"))
		}, "host-firewall", Pass, "", ""},
		{"an accept naming wg0's oifname alone", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), rl("inet", "fw", "forward", oif("wg0"), counter, accept))
		}, "host-firewall", Pass, "", ""},
		{"an accept for another interface", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), rl("inet", "fw", "forward", iif("eth1"), accept))
		}, "host-firewall", Warn, "", ""},
		{"an accept for a set with wg0 in it", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), rl("inet", "fw", "forward", ifSet("iifname", "eth1", "wg0"), accept))
		}, "host-firewall", Pass, "", ""},
		{"an accept for a set without wg0", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), rl("inet", "fw", "forward", ifSet("iifname", "eth1", "eth2"), accept))
		}, "host-firewall", Warn, "", ""},
		{"an accept for everything but wg0", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), rl("inet", "fw", "forward", ifMatch("iifname", "!=", "wg0"), accept))
		}, "host-firewall", Warn, "", ""},
		{"an accept only for established connections", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), rl("inet", "fw", "forward", ctState, accept))
		}, "host-firewall", Warn, "", ""},
		{"an accept only for one source network", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), rl("inet", "fw", "forward", iif("wg0"), srcNet, accept))
		}, "host-firewall", Warn, "", ""},
		{"an iptables-nft conntrack match narrows a rule", func(f *fixture) {
			f.nft = nftDoc(baseChain("ip", "filter", "FORWARD", "forward", "drop"), rl("ip", "filter", "FORWARD", iif("wg0"), conntrk, accept))
		}, "host-firewall", Warn, "", ""},
		{"an iptables-nft comment doesn't", func(f *fixture) {
			f.nft = nftDoc(baseChain("ip", "filter", "FORWARD", "forward", "drop"), rl("ip", "filter", "FORWARD", iif("wg0"), comment, counter, accept))
		}, "host-firewall", Pass, "", ""},
		{"a jump to a chain that accepts", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), plainChain("inet", "fw", "vpn"),
				rl("inet", "fw", "forward", jump("vpn")), rl("inet", "fw", "vpn", iif("wg0"), accept))
		}, "host-firewall", Pass, "", ""},
		{"a goto to a chain that accepts", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), plainChain("inet", "fw", "vpn"),
				rl("inet", "fw", "forward", goTo("vpn")), rl("inet", "fw", "vpn", accept))
		}, "host-firewall", Pass, "", ""},
		{"a jump to a chain that doesn't", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), plainChain("inet", "fw", "vpn"),
				rl("inet", "fw", "forward", jump("vpn")), rl("inet", "fw", "vpn", counter, ret))
		}, "host-firewall", Warn, "", ""},
		{"a jump to a missing chain", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), rl("inet", "fw", "forward", jump("gone")))
		}, "host-firewall", Warn, "", ""},
		{"jump loop", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), plainChain("inet", "fw", "a"), plainChain("inet", "fw", "b"),
				rl("inet", "fw", "forward", jump("a")), rl("inet", "fw", "a", jump("b")), rl("inet", "fw", "b", jump("a")))
		}, "host-firewall", Warn, "", ""},
		{"a rule before it drops", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "fw", "forward", "forward", "drop"), rl("inet", "fw", "forward", iif("wg0"), drop), rl("inet", "fw", "forward", iif("wg0"), accept))
		}, "host-firewall", Pass, "", ""}, // a heuristic: it doesn't model order, and says so in the docs
		{"Drawbridge's own table is ignored", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "drawbridge", "forward", "forward", "drop"))
		}, "host-firewall", Pass, "", ""},
		{"a table with the same name in another family isn't ours", func(f *fixture) {
			f.nft = nftDoc(baseChain("ip", "drawbridge", "forward", "forward", "drop"))
		}, "host-firewall", Warn, "", ""},

		{"Docker", func(f *fixture) { f.nft = nftDoc(dockerRules()...) },
			"host-firewall", Warn, "by Docker's ip filter FORWARD chain, and no rule there",
			"sudo iptables -I DOCKER-USER -i wg0 -j ACCEPT && sudo iptables -I DOCKER-USER -o wg0 -j ACCEPT"},
		{"Docker with the admin's DOCKER-USER accepts", func(f *fixture) {
			f.nft = nftDoc(dockerRules(rl("ip", "filter", "DOCKER-USER", iif("wg0"), counter, accept), rl("ip", "filter", "DOCKER-USER", oif("wg0"), counter, accept))...)
		}, "host-firewall", Pass, "", ""},
		{"Docker on IPv6 too", func(f *fixture) {
			f.nft = nftDoc(append(dockerRules(), inet(dockerRules())...)...)
		}, "host-firewall", Warn, "Docker's ip filter FORWARD chain and Docker's ip6 filter FORWARD chain", "DOCKER-USER"},
		{"Docker allowed for IPv4 but not IPv6", func(f *fixture) {
			allow := []string{rl("ip", "filter", "DOCKER-USER", iif("wg0"), accept)}
			f.nft = nftDoc(append(dockerRules(allow...), inet(dockerRules())...)...)
		}, "host-firewall", Warn, "by Docker's ip6 filter FORWARD chain, and", "ip6tables"},

		{"ufw", func(f *fixture) { f.nft = nftDoc(ufwRules()...) },
			"host-firewall", Warn, "",
			"sudo ufw route allow in on wg0 && sudo ufw route allow out on wg0"},
		{"ufw with a route rule", func(f *fixture) {
			f.nft = nftDoc(ufwRules(rl("ip", "filter", "ufw-user-forward", iif("wg0"), counter, accept))...)
		}, "host-firewall", Pass, "", ""},

		{"firewalld", func(f *fixture) {
			f.nft = nftDoc(baseChain("inet", "firewalld", "filter_FORWARD", "forward", "drop"))
		}, "host-firewall", Warn, "", "firewall-cmd --permanent --zone=trusted --add-interface=wg0"},

		{"an interface name that isn't safe in a shell", func(f *fixture) {
			f.in.Interface = "wg 0;x"
			f.nft = nftDoc(ufwRules()...)
		}, "host-firewall", Warn, "wg 0;x", "route allow in on <interface> "},

		{"nft failed", func(f *fixture) { f.nftErr = errors.New("operation not permitted") }, "host-firewall", Skip, "operation not permitted", ""},
		{"nft printed something else", func(f *fixture) { f.nft = "Error: netlink: nope" }, "host-firewall", Skip, "reading the nftables ruleset", ""},
		{"an empty document", func(f *fixture) { f.nft = `{}` }, "host-firewall", Pass, "", ""},
	}
	runCases(t, cases)
}
