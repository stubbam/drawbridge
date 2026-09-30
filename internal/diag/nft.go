package diag

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/stuffam/drawbridge/internal/firewall"
)

// The host-firewall check reads `nft -j list ruleset` for a base chain on the forward hook
// with a drop policy, outside Drawbridge's table. Drawbridge's own table only restricts and
// NATs; it can't accept, because an accept in one table doesn't override another table's
// drop (docs/PLAN.md §5.3). So a host whose firewall drops forwarded packets by default,
// as ufw, firewalld, and rootful Docker set up, drops the VPN's traffic too, unless a rule
// there accepts it.
//
// The check is a heuristic. It can't see iptables-legacy rules, which nft doesn't list, and
// it doesn't look at input chains (a drop of UDP 51820 there stops clients connecting at
// all, which the tunnel and endpoint checks show as a problem in their own way).

type ruleset struct {
	chains map[chainKey]*chain
	// order keeps the chains as listed, so the result is stable.
	order []chainKey
}

type chainKey struct{ family, table, name string }

type chain struct {
	key    chainKey
	hook   string
	policy string
	rules  []rule
}

type rule struct {
	// ifaces are the interface names its iifname and oifname matches compare with.
	ifaces []string
	// otherMatch is set when the rule matches on anything but an interface (an address, a
	// port, a connection state), which makes an accept narrower than "everything of
	// the VPN's".
	otherMatch bool
	verdict    string
	// target is the chain a jump or goto verdict names.
	target string
}

// parseRuleset reads the JSON that `nft -j list ruleset` prints.
func parseRuleset(data []byte) (*ruleset, error) {
	var doc struct {
		Nftables []struct {
			Chain *struct {
				Family string `json:"family"`
				Table  string `json:"table"`
				Name   string `json:"name"`
				Hook   string `json:"hook"`
				Policy string `json:"policy"`
			} `json:"chain"`
			Rule *struct {
				Family string            `json:"family"`
				Table  string            `json:"table"`
				Chain  string            `json:"chain"`
				Expr   []json.RawMessage `json:"expr"`
			} `json:"rule"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("reading the nftables ruleset: %w", err)
	}
	rs := &ruleset{chains: map[chainKey]*chain{}}
	for _, item := range doc.Nftables {
		if c := item.Chain; c != nil {
			k := chainKey{c.Family, c.Table, c.Name}
			if _, dup := rs.chains[k]; !dup {
				rs.order = append(rs.order, k)
			}
			rs.chains[k] = &chain{key: k, hook: c.Hook, policy: c.Policy}
		}
	}
	// A rule's chain is listed before its rules, so a second pass finds it.
	for _, item := range doc.Nftables {
		if r := item.Rule; r != nil {
			c, ok := rs.chains[chainKey{r.Family, r.Table, r.Chain}]
			if !ok {
				continue
			}
			c.rules = append(c.rules, parseRule(r.Expr))
		}
	}
	return rs, nil
}

func parseRule(exprs []json.RawMessage) rule {
	var r rule
	for _, raw := range exprs {
		var e map[string]json.RawMessage
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		switch {
		case e["match"] != nil:
			var m struct {
				Op    string          `json:"op"`
				Left  json.RawMessage `json:"left"`
				Right json.RawMessage `json:"right"`
			}
			// A != or a range comparison isn't "this interface", so it can't make an accept
			// count as the VPN's.
			if json.Unmarshal(e["match"], &m) != nil || !isIfaceMatch(m.Left) || (m.Op != "==" && m.Op != "in") {
				r.otherMatch = true
				continue
			}
			r.ifaces = append(r.ifaces, names(m.Right)...)
		case e["accept"] != nil:
			r.verdict = "accept"
		case e["drop"] != nil:
			r.verdict = "drop"
		case e["return"] != nil:
			r.verdict = "return"
		case e["reject"] != nil:
			r.verdict = "reject"
		case e["jump"] != nil || e["goto"] != nil:
			key := "jump"
			if e["goto"] != nil {
				key = "goto"
			}
			var t struct {
				Target string `json:"target"`
			}
			_ = json.Unmarshal(e[key], &t)
			r.verdict, r.target = "jump", t.Target
		default:
			// A counter or a log doesn't change what the rule matches. Anything else
			// unrecognized might, so it narrows the rule.
			if e["counter"] == nil && e["log"] == nil && !isCommentXT(e["xt"]) {
				r.otherMatch = true
			}
		}
	}
	return r
}

// isCommentXT reports whether an iptables-nft compatibility expression is a comment, which
// matches everything.
func isCommentXT(raw json.RawMessage) bool {
	var x struct {
		Name string `json:"name"`
	}
	return raw != nil && json.Unmarshal(raw, &x) == nil && x.Name == "comment"
}

// isIfaceMatch reports whether a match's left side is `meta iifname` or `meta oifname`.
func isIfaceMatch(left json.RawMessage) bool {
	var l struct {
		Meta struct {
			Key string `json:"key"`
		} `json:"meta"`
	}
	if json.Unmarshal(left, &l) != nil {
		return false
	}
	return l.Meta.Key == "iifname" || l.Meta.Key == "oifname"
}

// names reads the right side of an interface match: a string, or a set of strings.
func names(right json.RawMessage) []string {
	var s string
	if json.Unmarshal(right, &s) == nil {
		return []string{s}
	}
	var set struct {
		Set []string `json:"set"`
	}
	if json.Unmarshal(right, &set) == nil {
		return set.Set
	}
	return nil
}

// dropsForward returns the forward-hook chains, outside Drawbridge's table, whose policy
// is drop and that have no rule accepting the interface's traffic.
func (rs *ruleset) dropsForward(iface string) []*chain {
	var out []*chain
	for _, k := range rs.order {
		c := rs.chains[k]
		if c.hook != "forward" || c.policy != "drop" || k.family+" "+k.table == firewall.Table {
			continue
		}
		if !rs.accepts(c, iface, 0) {
			out = append(out, c)
		}
	}
	return out
}

// accepts reports whether chain c has a rule (or one in a chain it jumps to) that accepts
// the interface's traffic: an accept whose only matches are on interface names, with none
// naming a different interface than iface, or no matches at all. depth stops a loop.
func (rs *ruleset) accepts(c *chain, iface string, depth int) bool {
	if depth > 4 {
		return false
	}
	for _, r := range c.rules {
		if r.otherMatch {
			continue
		}
		if len(r.ifaces) > 0 && !slices.Contains(r.ifaces, iface) {
			continue
		}
		switch r.verdict {
		case "accept":
			return true
		case "jump":
			if t, ok := rs.chains[chainKey{c.key.family, c.key.table, r.target}]; ok && rs.accepts(t, iface, depth+1) {
				return true
			}
		}
	}
	return false
}

// kind names the tool that owns a chain, from the names it uses, so the hint can give that
// tool's own commands.
func (rs *ruleset) kind(c *chain) string {
	switch {
	case c.key.table == "firewalld":
		return "firewalld"
	case strings.HasPrefix(c.key.name, "ufw-") || rs.has(c.key.table, func(n string) bool { return strings.HasPrefix(n, "ufw-") }):
		return "ufw"
	case rs.has(c.key.table, func(n string) bool { return n == "DOCKER-USER" }):
		return "docker"
	}
	return "other"
}

// has reports whether the table has a chain whose name satisfies f.
func (rs *ruleset) has(table string, f func(name string) bool) bool {
	for k := range rs.chains {
		if k.table == table && f(k.name) {
			return true
		}
	}
	return false
}

func (e *env) hostFirewall() Check {
	c := Check{ID: "host-firewall", Name: "Host firewall"}
	if e.nftErr != nil {
		c.Status, c.Detail = Skip, fmt.Sprintf("Couldn't list the host's firewall rules: %v.", e.nftErr)
		return c
	}
	rs, err := parseRuleset(e.nft)
	if err != nil {
		c.Status, c.Detail = Skip, err.Error()
		return c
	}
	blocking := rs.dropsForward(e.in.Interface)
	if len(blocking) == 0 {
		c.Status = Pass
		c.Detail = "No other firewall drops forwarded traffic that nothing accepts."
		return c
	}

	iface := quoteIface(e.in.Interface)
	var owners []string
	var hint string
	for _, ch := range blocking {
		kind := rs.kind(ch)
		what := fmt.Sprintf("the %s %s %s chain", ch.key.family, ch.key.table, ch.key.name)
		switch kind {
		case "firewalld":
			what = fmt.Sprintf("firewalld's %s %s chain", ch.key.family, ch.key.name)
		case "ufw":
			what = fmt.Sprintf("ufw's %s %s %s chain", ch.key.family, ch.key.table, ch.key.name)
		case "docker":
			what = fmt.Sprintf("Docker's %s %s %s chain", ch.key.family, ch.key.table, ch.key.name)
		}
		if !slices.Contains(owners, what) {
			owners = append(owners, what)
		}
		if hint != "" {
			continue
		}
		switch kind {
		case "ufw":
			hint = fmt.Sprintf("sudo ufw route allow in on %s && sudo ufw route allow out on %s", iface, iface)
		case "docker":
			hint = fmt.Sprintf("sudo iptables -I DOCKER-USER -i %s -j ACCEPT && sudo iptables -I DOCKER-USER -o %s -j ACCEPT (and ip6tables for IPv6). These rules don't survive a reboot until saved; see docs/REQUIREMENTS.md.", iface, iface)
		case "firewalld":
			hint = fmt.Sprintf("sudo firewall-cmd --permanent --zone=trusted --add-interface=%s && sudo firewall-cmd --reload", iface)
		default:
			hint = fmt.Sprintf("Add a rule to that firewall accepting forwarded traffic in and out of %s. See docs/REQUIREMENTS.md.", iface)
		}
	}
	c.Status = Warn
	c.Detail = fmt.Sprintf("Forwarded packets are dropped by default by %s, and no rule there accepts %s's traffic, so clients may connect but reach nothing. This is a best guess from nftables' rules; it can't see iptables-legacy.",
		join(owners), e.in.Interface)
	c.Hint = hint
	return c
}
