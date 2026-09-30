package diag

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/stuffam/drawbridge/internal/lan"
)

// safeIface matches the interface names a hint may quote in a shell command. Linux allows
// almost anything in a name, so the hint falls back to a placeholder for the rest.
var safeIface = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)

func quoteIface(name string) string {
	if safeIface.MatchString(name) {
		return name
	}
	return "<interface>"
}

func (e *env) tunnel() Check {
	c := Check{ID: "tunnel", Name: "Tunnel"}
	switch {
	case e.in.TunnelErr != nil:
		c.Status = Fail
		c.Detail = fmt.Sprintf("Can't read the WireGuard interface %s: %v.", e.in.Interface, e.in.TunnelErr)
		c.Hint = "Check the daemon's log: journalctl -u drawbridge"
	case !e.in.TunnelUp:
		c.Status = Fail
		c.Detail = fmt.Sprintf("The tunnel (%s) is stopped, so no client can connect.", e.in.Interface)
		c.Hint = "sudo systemctl start drawbridge-tunnel (journalctl -u drawbridge-tunnel says why it stopped)"
	default:
		c.Status = Pass
		c.Detail = fmt.Sprintf("%s is up with %s.", e.in.Interface, plural(e.in.Peers, "peer"))
	}
	return c
}

func (e *env) kernelModule() Check {
	c := Check{ID: "kernel-module", Name: "WireGuard kernel module"}
	switch {
	case e.in.TunnelUp:
		c.Status, c.Detail = Pass, "Loaded: the WireGuard interface exists."
	case e.host.exists("sys/module/wireguard"):
		c.Status, c.Detail = Pass, "The wireguard module is loaded."
	default:
		// A module built into the kernel has no /sys/module entry, so this can't be a fail.
		c.Status = Warn
		c.Detail = "The wireguard module isn't loaded, as far as this can tell. (One built into the kernel doesn't show.)"
		c.Hint = "sudo modprobe wireguard; the package loads it at boot through /usr/lib/modules-load.d/drawbridge.conf"
	}
	return c
}

func (e *env) forwarding() Check {
	c := Check{ID: "forwarding", Name: "Forwarding sysctls"}
	var problems, fixes []string
	v4, ok := e.host.readInt("proc/sys/net/ipv4/ip_forward")
	switch {
	case !ok:
		problems = append(problems, "can't read net.ipv4.ip_forward")
	case v4 != 1:
		problems = append(problems, "IPv4 forwarding is off")
		fixes = append(fixes, "net.ipv4.ip_forward=1")
	}
	if e.in.IPv6.IsValid() {
		v6, ok := e.host.readInt("proc/sys/net/ipv6/conf/all/forwarding")
		switch {
		case !e.host.exists("proc/sys/net/ipv6"):
			problems = append(problems, "IPv6 is disabled on this host, so the VPN's IPv6 can't be forwarded")
		case !ok:
			problems = append(problems, "can't read net.ipv6.conf.all.forwarding")
		case v6 != 1:
			problems = append(problems, "IPv6 forwarding is off")
			fixes = append(fixes, "net.ipv6.conf.all.forwarding=1")
		}
	}
	if len(problems) == 0 {
		c.Status = Pass
		c.Detail = "IPv4 forwarding is on."
		if e.in.IPv6.IsValid() {
			c.Detail = "IPv4 and IPv6 forwarding are on."
		}
		return c
	}
	c.Status = Fail
	c.Detail = capitalize(join(problems)) + ", so VPN clients' traffic isn't routed."
	if len(fixes) > 0 {
		c.Hint = "sudo sysctl -w " + strings.Join(fixes, " ") +
			". The package sets them at boot in /usr/lib/sysctl.d/90-drawbridge.conf; if they turn off again, another file in /etc/sysctl.d is overriding it."
	} else {
		c.Hint = "sysctl net.ipv4.ip_forward net.ipv6.conf.all.forwarding shows the values; the daemon couldn't read them itself."
	}
	return c
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (e *env) uplink() Check {
	c := Check{ID: "uplink", Name: "Uplink"}
	if e.snapErr != nil {
		c.Status, c.Detail = Skip, fmt.Sprintf("Couldn't read the routing table: %v.", e.snapErr)
		return c
	}
	s := e.snap
	if len(s.Uplinks4) == 0 {
		c.Status = Fail
		c.Detail = "The host has no IPv4 default route, so VPN clients' traffic has nowhere to go."
		c.Hint = "Check the host's network connection: ip route show default"
		return c
	}
	if e.in.IPv6.IsValid() && len(s.Uplinks6) == 0 {
		c.Status = Warn
		c.Detail = fmt.Sprintf("IPv4 leaves by %s, but the host has no IPv6 default route, so VPN clients' IPv6 traffic has nowhere to go.",
			join(s.Uplinks4))
		c.Hint = "If the host's IPv6 stopped when Drawbridge was installed, see the accept_ra check below and docs/REQUIREMENTS.md. Otherwise the network may not offer the host IPv6: ip -6 route show default"
		return c
	}
	c.Status = Pass
	c.Detail = "IPv4 leaves by " + join(s.Uplinks4)
	switch {
	case len(s.Uplinks6) > 0:
		c.Detail += "; IPv6 by " + join(s.Uplinks6)
	case !e.in.IPv6.IsValid():
		c.Detail += " (the VPN doesn't use IPv6)"
	}
	c.Detail += "."
	return c
}

// acceptRA checks the bug docs/PLAN.md §5.5 describes: with IPv6 forwarding on, the kernel
// ignores router advertisements where accept_ra is 1, so a host that gets its IPv6 from
// the kernel's own SLAAC loses its address and default route.
func (e *env) acceptRA() Check {
	c := Check{ID: "accept-ra", Name: "Router advertisements"}
	if e.snapErr != nil {
		c.Status, c.Detail = Skip, "Couldn't find the uplink, so accept_ra wasn't checked."
		return c
	}
	if !e.host.exists("proc/sys/net/ipv6") {
		c.Status, c.Detail = Skip, "IPv6 is disabled on this host."
		return c
	}
	// The IPv4 default route stands in when the IPv6 one is already gone, which is what
	// the bug leaves behind.
	ifaces := e.snap.Uplinks6
	if len(ifaces) == 0 {
		ifaces = e.snap.Uplinks4
	}
	if len(ifaces) == 0 {
		c.Status, c.Detail = Skip, "The host has no default route to check."
		return c
	}

	var found, bad []string
	for _, name := range ifaces {
		if strings.Contains(name, "/") {
			continue
		}
		base := "proc/sys/net/ipv6/conf/" + name + "/"
		ra, ok := e.host.readInt(base + "accept_ra")
		if !ok {
			continue
		}
		fwd, _ := e.host.readInt(base + "forwarding")
		found = append(found, fmt.Sprintf("uplink %s has accept_ra %d", name, ra))
		if ra == 1 && fwd == 1 {
			bad = append(bad, name)
		}
	}
	switch {
	case len(found) == 0:
		c.Status, c.Detail = Skip, "IPv6 is disabled on the uplink."
	case len(bad) > 0:
		c.Status = Fail
		c.Detail = fmt.Sprintf("On %s, accept_ra is 1 and IPv6 forwarding is on, so the kernel ignores router advertisements. The host loses its own IPv6 address and default route.",
			join(bad))
		c.Hint = fmt.Sprintf("echo 'net.ipv6.conf.%s.accept_ra = 2' | sudo tee /etc/sysctl.d/80-accept-ra.conf && sudo sysctl -p /etc/sysctl.d/80-accept-ra.conf",
			quoteIface(bad[0]))
	default:
		c.Status = Pass
		c.Detail = capitalize(join(found)) + ": the host keeps receiving router advertisements with forwarding on."
	}
	return c
}

func (e *env) subnetOverlap() Check {
	c := Check{ID: "subnet-overlap", Name: "VPN subnets and the LAN"}
	if e.snapErr != nil {
		c.Status, c.Detail = Skip, fmt.Sprintf("Couldn't read the LAN's subnets: %v.", e.snapErr)
		return c
	}
	var clashes []string
	for _, vpn := range []netip.Prefix{e.in.IPv4, e.in.IPv6} {
		if !vpn.IsValid() {
			continue
		}
		for _, l := range e.snap.LAN {
			if vpn.Overlaps(l) {
				clashes = append(clashes, fmt.Sprintf("the VPN's %s overlaps the LAN's %s", vpn.Masked(), l))
			}
		}
	}
	if len(clashes) == 0 {
		c.Status = Pass
		c.Detail = "The VPN's subnets don't overlap the LAN's."
		return c
	}
	c.Status = Fail
	c.Detail = capitalize(join(clashes)) + ", so a client can't tell which network an address is on."
	c.Hint = "Drawbridge can't change the VPN's subnet after setup yet. Move the LAN to another subnet on the router, or ask for the feature in the project's issue tracker."
	return c
}

func (e *env) firewallTable() Check {
	c := Check{ID: "firewall-table", Name: "Drawbridge's firewall table"}
	switch {
	case e.in.FirewallErr != nil:
		c.Status, c.Detail = Skip, fmt.Sprintf("Couldn't list the nftables table: %v.", e.in.FirewallErr)
	case !e.in.TunnelUp:
		c.Status, c.Detail = Skip, "The tunnel is stopped, and the table goes with it."
	case !e.in.FirewallLoaded:
		c.Status = Fail
		c.Detail = "Drawbridge's nftables table isn't loaded, so VPN clients aren't NATed and the admin UI isn't restricted to the LAN and the VPN."
		c.Hint = "The daemon reloads it within 30 seconds. If it doesn't, journalctl -u drawbridge says why."
	default:
		c.Status, c.Detail = Pass, "The table is loaded."
	}
	return c
}

func (e *env) dns() Check {
	c := Check{ID: "dns", Name: "DNS for clients"}
	if len(e.in.DNS) == 0 {
		c.Status, c.Detail = Pass, "Clients aren't given DNS servers, so they use their own."
		return c
	}
	if len(e.in.DNSProbes) == 0 {
		var addrs []string
		for _, a := range e.in.DNS {
			addrs = append(addrs, a.String())
		}
		c.Status = Pass
		c.Detail = "Clients use " + join(addrs) + ", outside this host. Drawbridge doesn't test those."
		return c
	}
	var answered, silent []string
	var hints []string
	for _, p := range e.in.DNSProbes {
		if p.Answered {
			answered = append(answered, p.Address.String())
			continue
		}
		silent = append(silent, p.Address.String())
		hints = append(hints, p.Detail)
	}
	switch {
	case len(silent) == 0:
		c.Status = Pass
		c.Detail = "This server answers DNS on " + join(answered) + "."
	default:
		c.Status = Warn
		if len(answered) == 0 {
			c.Status = Fail
		}
		c.Detail = fmt.Sprintf("Clients use this server for DNS, but nothing answers on %s. %s", join(silent), strings.Join(hints, " "))
		c.Hint = "Make the resolver listen on the VPN addresses (for AdGuard Home, bind_hosts) and allow the VPN's subnets, or give clients other servers: sudo drawbridge server set --dns 1.1.1.1,1.0.0.1"
	}
	return c
}

// isPublic reports whether a client on the internet could reach addr.
func isPublic(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsValid() && !addr.IsPrivate() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() &&
		!addr.IsUnspecified() && !addr.IsMulticast() && !cgnat.Contains(addr)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func (e *env) endpoint() Check {
	c := Check{ID: "endpoint", Name: "Endpoint"}
	host := e.in.EndpointHost
	if host == "" {
		c.Status = Fail
		c.Detail = "The server's public address isn't set, so clients can't get a config."
		c.Hint = "sudo drawbridge server set --endpoint vpn.example.com"
		return c
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a.Unmap()}
	} else {
		if e.host.LookupIP == nil {
			c.Status, c.Detail = Skip, fmt.Sprintf("Couldn't look up %s.", host)
			return c
		}
		ips, err := e.host.LookupIP(e.ctx, host)
		if err != nil || len(ips) == 0 {
			c.Status = Fail
			c.Detail = fmt.Sprintf("%s doesn't resolve from this host", host)
			if err != nil {
				c.Detail += fmt.Sprintf(": %v", err)
			}
			c.Detail += ", so clients that use it can't find the server."
			c.Hint = "Create an A record for it (and an AAAA record for IPv6) at your DNS provider, or set the public IP address as the endpoint."
			return c
		}
		addrs = ips
	}

	var private, temporary, shown []string
	for _, a := range addrs {
		shown = append(shown, a.String())
		if !isPublic(a) {
			private = append(private, a.String())
		}
		if i := slices.IndexFunc(e.snap.Addrs, func(h lan.HostAddr) bool { return h.Addr == a && (h.Temporary || h.Deprecated) }); i >= 0 {
			temporary = append(temporary, fmt.Sprintf("%s (on %s)", a, e.snap.Addrs[i].Interface))
		}
	}
	switch {
	case len(private) > 0:
		c.Status = Warn
		c.Detail = fmt.Sprintf("%s resolves to %s here, which a client on the internet can't reach.", host, join(private))
		c.Hint = "Point the A record at your public IPv4 address (and the AAAA record at the host's IPv6 address). If this network's resolver answers privately on purpose (split DNS), check what a device outside your network gets."
	case len(temporary) > 0:
		c.Status = Warn
		c.Detail = fmt.Sprintf("%s resolves to %s, a temporary (privacy) IPv6 address that changes about daily.", host, join(temporary))
		c.Hint = "Point the AAAA record at one of the host's stable addresses: ip -6 addr show scope global -temporary"
	default:
		c.Status = Pass
		c.Detail = fmt.Sprintf("%s resolves to %s.", host, join(shown))
		if host == addrs[0].String() {
			c.Detail = fmt.Sprintf("Clients connect to %s.", host)
		}
	}
	return c
}

func (e *env) timeSync() Check {
	c := Check{ID: "time-sync", Name: "Clock"}
	if e.host.exists("run/systemd/timesync/synchronized") {
		c.Status, c.Detail = Pass, "systemd-timesyncd reports the clock is synchronized."
		return c
	}
	c.Status = Warn
	c.Detail = "systemd-timesyncd hasn't reported a synchronized clock. TLS certificates and WireGuard handshakes depend on the time."
	c.Hint = "sudo timedatectl set-ntp true, then timedatectl status. Only systemd-timesyncd is recognized: a host that keeps time with chrony or ntpd shows this warning even when its clock is right."
	return c
}

const (
	diskWarn = 200 << 20
	diskFail = 50 << 20
)

func (e *env) diskSpace() Check {
	c := Check{ID: "disk-space", Name: "Free disk space"}
	if e.host.Statfs == nil || e.host.StateDir == "" {
		c.Status, c.Detail = Skip, "The state directory isn't known."
		return c
	}
	free, total, err := e.host.Statfs(e.host.StateDir)
	if err != nil {
		c.Status, c.Detail = Skip, fmt.Sprintf("Couldn't read the free space of %s: %v.", e.host.StateDir, err)
		return c
	}
	c.Detail = fmt.Sprintf("%s free of %s, where the database is (%s).", formatBytes(free), formatBytes(total), e.host.StateDir)
	switch {
	case free < diskFail:
		c.Status = Fail
		c.Hint = "Free up space on that filesystem: SQLite can't write when the disk is full, so changes are lost."
	case free < diskWarn:
		c.Status = Warn
		c.Hint = "Free up space on that filesystem before it fills."
	default:
		c.Status = Pass
	}
	return c
}

func (e *env) tlsCertificate() Check {
	c := Check{ID: "tls-certificate", Name: "TLS certificate"}
	if e.host.CertNotAfter.IsZero() {
		c.Status, c.Detail = Skip, "The web UI has no TLS certificate."
		return c
	}
	left := e.host.CertNotAfter.Sub(e.host.now())
	day := e.host.CertNotAfter.Format(time.DateOnly)
	switch {
	case left <= 0:
		c.Status = Fail
		c.Detail = fmt.Sprintf("The web UI's certificate expired on %s.", day)
		c.Hint = "sudo systemctl restart drawbridge creates a new one at startup."
	case left < certWarn:
		c.Status = Warn
		c.Detail = fmt.Sprintf("The web UI's certificate expires on %s (%s).", day, plural(int(left/(24*time.Hour)), "day"))
		c.Hint = "The daemon renews it only when it starts: sudo systemctl restart drawbridge"
	default:
		c.Status = Pass
		c.Detail = fmt.Sprintf("Valid until %s (%s).", day, plural(int(left/(24*time.Hour)), "day"))
	}
	return c
}

// certWarn matches tlscert.RenewBefore: the daemon renews a certificate this close to its
// end whenever it starts.
const certWarn = 30 * 24 * time.Hour
