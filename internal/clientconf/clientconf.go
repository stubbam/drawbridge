// Package clientconf renders a client's WireGuard configuration (docs/PLAN.md §6.1).
//
// Only validated values reach the file: keys, addresses, the endpoint, and numbers. The
// client's name never does (CLAUDE.md, "Free text never reaches a rendered file").
package clientconf

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/stuffam/drawbridge/internal/model"
)

// ErrNoPrivateKey means the server doesn't store this client's private key, so it can't
// render a complete config.
var ErrNoPrivateKey = errors.New("the server doesn't store this client's private key")

// Render returns the client's config in wg-quick format.
func Render(s model.Settings, c model.Client) (string, error) {
	if c.PrivateKey == nil {
		return "", ErrNoPrivateKey
	}
	endpoint, err := s.Endpoint()
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", c.PrivateKey)
	addrs := []string{netip.PrefixFrom(c.IPv4, 32).String()}
	if c.IPv6.IsValid() {
		addrs = append(addrs, netip.PrefixFrom(c.IPv6, 128).String())
	}
	fmt.Fprintf(&b, "Address = %s\n", strings.Join(addrs, ", "))
	if len(s.DNS) > 0 {
		dns := make([]string, len(s.DNS))
		for i, a := range s.DNS {
			dns[i] = a.String()
		}
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(dns, ", "))
	}
	fmt.Fprintf(&b, "MTU = %d\n", s.MTU)

	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", s.PublicKey())
	if c.PresharedKey != ([32]byte{}) {
		fmt.Fprintf(&b, "PresharedKey = %s\n", c.PresharedKey)
	}
	fmt.Fprintf(&b, "Endpoint = %s\n", endpoint)
	allowed := make([]string, len(s.ClientAllowedIPs))
	for i, p := range s.ClientAllowedIPs {
		allowed[i] = p.String()
	}
	fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(allowed, ", "))
	if s.Keepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", s.Keepalive)
	}
	return b.String(), nil
}
