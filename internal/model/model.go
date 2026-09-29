// Package model holds Drawbridge's domain types and their validation (docs/PLAN.md §6, §7).
//
// Everything that reaches a rendered file (a WireGuard config or the nftables ruleset) is
// a parsed, validated value here. Free text, such as client names, never does
// (CLAUDE.md, "Free text never reaches a rendered file").
package model

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/stuffam/drawbridge/internal/ipam"
)

// Defaults for a new installation (docs/PLAN.md §5).
const (
	DefaultInterface  = "wg0"
	DefaultListenPort = 51820
	DefaultMTU        = 1420
	DefaultKeepalive  = 25
)

// AdminPort is the web UI's TCP port. The firewall protects it (docs/PLAN.md §5.3), and
// it's the one TCP port Drawbridge uses, chosen to stay clear of ports that other services
// on the same host commonly take (53, 80, 443, 3000).
const AdminPort = 51821

// DefaultIPv4 is the default IPv4 VPN subnet.
var DefaultIPv4 = netip.MustParsePrefix("10.8.0.0/24")

// AdminAllowedRanges bound what the admin may add to the admin UI's allowlist: private
// IPv4 (RFC 1918), the carrier-grade NAT range that Tailscale assigns from, and IPv6 unique
// local addresses. A globally routable range is refused, so no setting can put the UI on
// the internet (D11).
var AdminAllowedRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fc00::/7"),
}

// MaxAdminAllowed is how many extra prefixes the admin UI's allowlist accepts.
const MaxAdminAllowed = 16

// FullTunnel is the default AllowedIPs for clients: all IPv4 and IPv6 traffic. ::/0 stays
// even when the VPN has no IPv6 subnet, so a client's IPv6 traffic is dropped inside the
// tunnel instead of leaking outside it (docs/PLAN.md §5.2).
var FullTunnel = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}

// Settings are the server's settings (the `server` table).
type Settings struct {
	Interface  string
	ListenPort uint16
	// EndpointHost is the public FQDN or IP address clients connect to. It's empty until
	// the admin sets it, and no client config can be rendered before then.
	EndpointHost string
	// EndpointPort is the port clients connect to, when the router translates ports.
	// Zero means ListenPort.
	EndpointPort uint16
	PrivateKey   wgtypes.Key
	MTU          int
	IPv4         netip.Prefix
	// IPv6 is the zero Prefix when the VPN has no IPv6 subnet.
	IPv6 netip.Prefix
	// DNS is the resolver list pushed to clients. By default it's the server's VPN
	// addresses, which work only if a resolver (such as AdGuard Home) listens on them (D12).
	DNS              []netip.Addr
	Keepalive        int
	ClientIsolation  bool
	ClientAllowedIPs []netip.Prefix
	// AdminAllowed lists extra sources, beyond the LAN, loopback, link-local, and the VPN,
	// that may reach the admin UI. Each is inside AdminAllowedRanges.
	AdminAllowed []netip.Prefix
}

// NewSettings returns the settings for a new installation.
func NewSettings(privateKey wgtypes.Key, ipv6 netip.Prefix) (Settings, error) {
	s := Settings{
		Interface:        DefaultInterface,
		ListenPort:       DefaultListenPort,
		PrivateKey:       privateKey,
		MTU:              DefaultMTU,
		IPv4:             DefaultIPv4,
		IPv6:             ipv6,
		Keepalive:        DefaultKeepalive,
		ClientIsolation:  true,
		ClientAllowedIPs: FullTunnel,
	}
	srv, err := s.ServerAddrs()
	if err != nil {
		return Settings{}, err
	}
	s.DNS = []netip.Addr{srv.IPv4}
	if srv.IPv6.IsValid() {
		s.DNS = append(s.DNS, srv.IPv6)
	}
	return s, s.Validate()
}

// VPNSubnets returns the VPN's subnets: IPv4, and IPv6 if it's on.
func (s Settings) VPNSubnets() []netip.Prefix {
	out := []netip.Prefix{s.IPv4}
	if s.IPv6.IsValid() {
		out = append(out, s.IPv6)
	}
	return out
}

// AdminSources returns the VPN's subnets and the extra admin sources: the prefixes,
// besides the LAN's, that the admin UI's allowlist is built from (lan.Allowlist).
func (s Settings) AdminSources() []netip.Prefix {
	return append(s.VPNSubnets(), s.AdminAllowed...)
}

// ServerAddrs returns the server's VPN addresses.
func (s Settings) ServerAddrs() (ipam.Allocation, error) {
	return ipam.Server(s.IPv4, s.IPv6)
}

// PublicKey returns the server's public key.
func (s Settings) PublicKey() wgtypes.Key {
	return s.PrivateKey.PublicKey()
}

// AdvertisedPort returns the port clients connect to.
func (s Settings) AdvertisedPort() uint16 {
	if s.EndpointPort != 0 {
		return s.EndpointPort
	}
	return s.ListenPort
}

// ErrNoEndpoint means the server's public endpoint hasn't been set yet.
var ErrNoEndpoint = errors.New("the server's endpoint isn't set; run `drawbridge server set --endpoint <FQDN>` first")

// Endpoint returns the endpoint clients connect to, such as "vpn.example.com:51820" or
// "[2001:db8::1]:51820".
func (s Settings) Endpoint() (string, error) {
	if s.EndpointHost == "" {
		return "", ErrNoEndpoint
	}
	return net.JoinHostPort(s.EndpointHost, strconv.Itoa(int(s.AdvertisedPort()))), nil
}

var ifaceName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// InvalidError marks a validation failure: the input was wrong, not the system.
type InvalidError struct {
	Err error
}

func (e *InvalidError) Error() string { return e.Err.Error() }
func (e *InvalidError) Unwrap() error { return e.Err }

// IsInvalid reports whether err is a validation failure.
func IsInvalid(err error) bool {
	var ie *InvalidError
	return errors.As(err, &ie)
}

func invalid(err error) error {
	if err == nil {
		return nil
	}
	return &InvalidError{Err: err}
}

// Validate checks every setting.
func (s Settings) Validate() error {
	var errs []error
	if !ifaceName.MatchString(s.Interface) {
		errs = append(errs, fmt.Errorf("interface name %q must be 1–15 letters, digits, '.', '_', or '-'", s.Interface))
	}
	if s.ListenPort == 0 {
		errs = append(errs, errors.New("listen port must be 1–65535"))
	}
	if s.EndpointHost != "" {
		if err := ValidateHost(s.EndpointHost); err != nil {
			errs = append(errs, err)
		}
	}
	if s.PrivateKey == (wgtypes.Key{}) {
		errs = append(errs, errors.New("the server has no private key"))
	}
	if s.MTU < 1280 || s.MTU > 1500 {
		errs = append(errs, fmt.Errorf("MTU %d must be 1280–1500", s.MTU))
	}
	if err := ipam.ValidateV4(s.IPv4); err != nil {
		errs = append(errs, err)
	}
	if s.IPv6.IsValid() {
		if err := ipam.ValidateV6(s.IPv6); err != nil {
			errs = append(errs, err)
		}
	}
	if len(s.DNS) > 8 {
		errs = append(errs, errors.New("at most 8 DNS servers"))
	}
	for _, a := range s.DNS {
		if !a.IsValid() || a.Zone() != "" || a.IsUnspecified() {
			errs = append(errs, fmt.Errorf("DNS server %s isn't a usable address", a))
		}
	}
	if s.Keepalive < 0 || s.Keepalive > 3600 {
		errs = append(errs, fmt.Errorf("keepalive %d must be 0–3600 seconds", s.Keepalive))
	}
	if len(s.ClientAllowedIPs) == 0 {
		errs = append(errs, errors.New("clients need at least one AllowedIPs prefix"))
	}
	for _, p := range s.ClientAllowedIPs {
		if !p.IsValid() || p != p.Masked() {
			errs = append(errs, fmt.Errorf("AllowedIPs %s isn't a canonical prefix", p))
		}
	}
	if len(s.AdminAllowed) > MaxAdminAllowed {
		errs = append(errs, fmt.Errorf("at most %d extra admin sources", MaxAdminAllowed))
	}
	for _, p := range s.AdminAllowed {
		if !p.IsValid() || p != p.Masked() || p.Addr().Zone() != "" {
			errs = append(errs, fmt.Errorf("admin source %s isn't a canonical prefix", p))
			continue
		}
		if !inAnyRange(p, AdminAllowedRanges) {
			errs = append(errs, fmt.Errorf("admin source %s must be inside a private range (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16), Tailscale's 100.64.0.0/10, or fc00::/7", p))
		}
	}
	return invalid(errors.Join(errs...))
}

// NormalizePrefixes masks each valid prefix's host bits, removes duplicates, and sorts
// the result. Invalid prefixes stay, so Validate can refuse them.
func NormalizePrefixes(in []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(in))
	for _, p := range in {
		if p.IsValid() {
			p = p.Masked()
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	return out
}

// inAnyRange reports whether p lies wholly inside one of the ranges.
func inAnyRange(p netip.Prefix, ranges []netip.Prefix) bool {
	for _, r := range ranges {
		if r.Addr().BitLen() == p.Addr().BitLen() && r.Bits() <= p.Bits() && r.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// ValidateHost checks an endpoint host: an IP address or a DNS name.
func ValidateHost(host string) error {
	return invalid(validateHost(host))
}

func validateHost(host string) error {
	if a, err := netip.ParseAddr(host); err == nil {
		if a.Zone() != "" || a.IsUnspecified() || a.IsLoopback() {
			return fmt.Errorf("endpoint %s isn't an address clients can reach", host)
		}
		return nil
	}
	name := strings.TrimSuffix(host, ".")
	if name == "" || len(name) > 253 {
		return fmt.Errorf("endpoint %q isn't a valid DNS name", host)
	}
	for _, label := range strings.Split(name, ".") {
		if !validLabel(label) {
			return fmt.Errorf("endpoint %q isn't a valid DNS name (bad label %q)", host, label)
		}
	}
	return nil
}

func validLabel(l string) bool {
	if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for _, r := range l {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		digit := r >= '0' && r <= '9'
		if !letter && !digit && r != '-' {
			return false
		}
	}
	return true
}

// NormalizeHost lowercases a DNS name and drops a trailing dot. IP addresses are returned
// in canonical form.
func NormalizeHost(host string) string {
	if a, err := netip.ParseAddr(host); err == nil {
		return a.String()
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// Client is one VPN client (the `clients` table).
type Client struct {
	ID      string
	Name    string
	Enabled bool
	IPv4    netip.Addr
	// IPv6 is the zero Addr when the VPN has no IPv6 subnet.
	IPv6      netip.Addr
	PublicKey wgtypes.Key
	// PrivateKey is nil when the server doesn't store the client's private key.
	PrivateKey   *wgtypes.Key
	PresharedKey wgtypes.Key
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AllowedIPs returns the server-side AllowedIPs for the client: its own addresses.
func (c Client) AllowedIPs() []netip.Prefix {
	ips := []netip.Prefix{netip.PrefixFrom(c.IPv4, 32)}
	if c.IPv6.IsValid() {
		ips = append(ips, netip.PrefixFrom(c.IPv6, 128))
	}
	return ips
}

// MaxNameLength is the longest client name, in characters.
const MaxNameLength = 64

// ValidateName checks a client name: 1–64 characters of letters, digits, spaces, and
// . _ ' -, starting with a letter or digit, with no leading or trailing space.
func ValidateName(name string) error {
	return invalid(validateName(name))
}

func validateName(name string) error {
	n := utf8.RuneCountInString(name)
	if n == 0 || n > MaxNameLength {
		return fmt.Errorf("a client name must be 1–%d characters", MaxNameLength)
	}
	if !utf8.ValidString(name) {
		return errors.New("a client name must be valid UTF-8")
	}
	first, _ := utf8.DecodeRuneInString(name)
	if !unicode.IsLetter(first) && !unicode.IsDigit(first) {
		return fmt.Errorf("client name %q must start with a letter or digit", name)
	}
	if strings.HasSuffix(name, " ") {
		return fmt.Errorf("client name %q ends with a space", name)
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" ._'-", r) {
			return fmt.Errorf("client name %q contains %q; use letters, digits, spaces, and . _ ' -", name, r)
		}
	}
	return nil
}
