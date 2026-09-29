package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/stuffam/drawbridge/internal/control"
	"github.com/stuffam/drawbridge/internal/views"
)

const serverUsage = `Usage: drawbridge server show|set [flags]

  show   Show the server's settings.
  set    Change settings. Changes apply to the tunnel right away; client configs pick
         up endpoint, port, MTU, DNS, and keepalive changes when they're downloaded
         again.

Flags for set:
`

func serverCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "show" && args[0] != "set") {
		fmt.Fprint(stderr, serverUsage)
		return 2
	}
	sub := args[0]
	flags := newFlagSet("server "+sub, stderr)
	socket := flags.String("control", defaultControl, "daemon control socket `path`")
	endpoint := flags.String("endpoint", "", "public `host[:port]` clients connect to, such as vpn.example.com")
	port := flags.Uint("port", 0, "UDP listen `port`")
	mtu := flags.Int("mtu", 0, "tunnel MTU, 1280–1500")
	dns := flags.String("dns", "", "DNS servers for clients: comma-separated `addresses`, \"default\" (the server's VPN addresses, which works only if a DNS resolver such as AdGuard Home listens on them), or \"none\"")
	keepalive := flags.Int("keepalive", -1, "clients' PersistentKeepalive in `seconds` (0 turns it off)")
	isolation := flags.Bool("client-isolation", true, "block traffic between clients")
	adminAllow := flags.String("admin-allow", "", "extra sources that may reach the web UI, besides the home network and the VPN: comma-separated `prefixes` inside 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10 (Tailscale), or fc00::/7, or \"none\"")
	flags.Usage = func() { fmt.Fprint(stderr, serverUsage); flags.PrintDefaults() }
	pos, err := parseArgs(flags, args[1:])
	if err != nil {
		return flagStatus(err)
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "drawbridge server %s: unexpected argument %q\n", sub, pos[0])
		return 2
	}
	c := control.NewClient(*socket)

	if sub == "show" {
		s, err := c.Settings(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "drawbridge:", err)
			return 1
		}
		printSettings(stdout, s)
		return 0
	}

	var p views.SettingsPatch
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	delete(set, "control")
	if len(set) == 0 {
		fmt.Fprint(stderr, "drawbridge server set: nothing to change\n\n")
		flags.Usage()
		return 2
	}
	if set["endpoint"] {
		host, epPort, err := parseEndpoint(*endpoint)
		if err != nil {
			fmt.Fprintln(stderr, "drawbridge:", err)
			return 2
		}
		p.EndpointHost, p.EndpointPort = &host, &epPort
	}
	if set["port"] {
		if *port == 0 || *port > 65535 {
			fmt.Fprintln(stderr, "drawbridge: --port must be 1–65535")
			return 2
		}
		v := uint16(*port)
		p.ListenPort = &v
	}
	if set["mtu"] {
		p.MTU = mtu
	}
	if set["dns"] {
		switch strings.TrimSpace(*dns) {
		case "default":
			p.DNSDefault = true
		case "none", "":
			p.DNS = &[]netip.Addr{}
		default:
			var addrs []netip.Addr
			for _, s := range strings.Split(*dns, ",") {
				a, err := netip.ParseAddr(strings.TrimSpace(s))
				if err != nil {
					fmt.Fprintf(stderr, "drawbridge: --dns: %q isn't an IP address\n", s)
					return 2
				}
				addrs = append(addrs, a)
			}
			p.DNS = &addrs
		}
	}
	if set["keepalive"] {
		p.Keepalive = keepalive
	}
	if set["client-isolation"] {
		p.ClientIsolation = isolation
	}
	if set["admin-allow"] {
		prefixes := []netip.Prefix{}
		if v := strings.TrimSpace(*adminAllow); v != "" && v != "none" {
			for _, s := range strings.Split(v, ",") {
				pfx, err := netip.ParsePrefix(strings.TrimSpace(s))
				if err != nil {
					fmt.Fprintf(stderr, "drawbridge: --admin-allow: %q isn't a prefix such as 100.64.10.0/24\n", s)
					return 2
				}
				prefixes = append(prefixes, pfx)
			}
		}
		p.AdminAllowed = &prefixes
	}

	res, err := c.UpdateSettings(ctx, p)
	if err != nil {
		fmt.Fprintln(stderr, "drawbridge:", err)
		return 1
	}
	printSettings(stdout, res.Settings)
	return warn(stderr, res.Warning, res.ApplyFailed)
}

// parseEndpoint splits "host", "host:port", "IPv6", or "[IPv6]:port". A port of 0 means
// the listen port.
func parseEndpoint(s string) (string, uint16, error) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return a.String(), 0, nil
	}
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return s, 0, nil
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || port == 0 {
		return "", 0, fmt.Errorf("--endpoint: %q isn't a valid port", portStr)
	}
	return host, uint16(port), nil
}

func printSettings(w io.Writer, s views.SettingsView) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = "not set; run: drawbridge server set --endpoint <FQDN>"
	}
	ipv6 := "off"
	if s.IPv6Subnet.IsValid() {
		ipv6 = fmt.Sprintf("%s in %s", s.IPv6Address, s.IPv6Subnet)
	}
	dns := joinAddrs(s.DNS)
	if dns == "" {
		dns = "none"
	}
	keepalive := "off"
	if s.Keepalive > 0 {
		keepalive = fmt.Sprintf("%d s", s.Keepalive)
	}
	allowed := make([]string, len(s.ClientAllowedIPs))
	for i, p := range s.ClientAllowedIPs {
		allowed[i] = p.String()
	}
	adminAllowed := "none"
	if len(s.AdminAllowed) > 0 {
		parts := make([]string, len(s.AdminAllowed))
		for i, p := range s.AdminAllowed {
			parts[i] = p.String()
		}
		adminAllowed = strings.Join(parts, ", ")
	}
	rows := [][2]string{
		{"Interface", s.Interface},
		{"Listen port", fmt.Sprintf("%d (UDP)", s.ListenPort)},
		{"Endpoint", endpoint},
		{"Public key", s.PublicKey},
		{"IPv4", fmt.Sprintf("%s in %s", s.IPv4Address, s.IPv4Subnet)},
		{"IPv6", ipv6},
		{"MTU", strconv.Itoa(s.MTU)},
		{"DNS", dns},
		{"Keepalive", keepalive},
		{"Client isolation", onOff(s.ClientIsolation)},
		{"Client AllowedIPs", strings.Join(allowed, ", ")},
		{"Admin sources", adminAllowed},
	}
	for _, r := range rows {
		fmt.Fprintf(tw, "%s:\t%s\n", r[0], r[1])
	}
	_ = tw.Flush()
}

func joinAddrs(addrs []netip.Addr) string {
	s := make([]string, len(addrs))
	for i, a := range addrs {
		s[i] = a.String()
	}
	return strings.Join(s, ", ")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// warn prints a warning from the daemon, and returns 1 if applying the change failed.
func warn(stderr io.Writer, warning string, failed bool) int {
	if warning != "" {
		fmt.Fprintln(stderr, "warning:", warning)
	}
	if failed {
		return 1
	}
	return 0
}
