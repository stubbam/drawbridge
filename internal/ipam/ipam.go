// Package ipam allocates VPN addresses (docs/PLAN.md §5.1).
//
// The server is host 1 in each subnet. Clients get the lowest free IPv4 host from 2 up,
// and their IPv6 interface ID mirrors the IPv4 host number written with the same digits,
// so 10.8.0.23 pairs with fd…::23 and the two are easy to match up.
package ipam

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
)

// ServerHost is the host number of the server's address in each subnet.
const ServerHost = 1

// ErrExhausted means every client address in the subnet is taken.
var ErrExhausted = errors.New("no free addresses left in the VPN subnet")

// NewULA returns a random RFC 4193 unique local /64: fd00::/8, a random 40-bit global
// ID, and subnet ID 1.
func NewULA(random io.Reader) (netip.Prefix, error) {
	if random == nil {
		random = rand.Reader
	}
	var a [16]byte
	a[0] = 0xfd
	if _, err := io.ReadFull(random, a[1:6]); err != nil {
		return netip.Prefix{}, fmt.Errorf("generating an IPv6 prefix: %w", err)
	}
	a[7] = 1
	return netip.PrefixFrom(netip.AddrFrom16(a), 64), nil
}

// ValidateV4 checks an IPv4 VPN subnet: private (RFC 1918), in canonical form, and
// between /16 and /29.
func ValidateV4(p netip.Prefix) error {
	switch {
	case !p.IsValid() || !p.Addr().Is4():
		return fmt.Errorf("%s isn't an IPv4 subnet", p)
	case p != p.Masked():
		return fmt.Errorf("%s has host bits set; did you mean %s?", p, p.Masked())
	case !p.Addr().IsPrivate():
		return fmt.Errorf("%s isn't a private (RFC 1918) range", p)
	case p.Bits() < 16 || p.Bits() > 29:
		return fmt.Errorf("%s: the IPv4 subnet must be between /16 and /29", p)
	}
	return nil
}

// ValidateV6 checks an IPv6 VPN subnet: a unique local (fc00::/7) /64 in canonical form.
// Routed global prefixes arrive with routed IPv6 (M6).
func ValidateV6(p netip.Prefix) error {
	switch {
	case !p.IsValid() || !p.Addr().Is6() || p.Addr().Is4In6():
		return fmt.Errorf("%s isn't an IPv6 subnet", p)
	case p != p.Masked():
		return fmt.Errorf("%s has host bits set; did you mean %s?", p, p.Masked())
	case !p.Addr().IsPrivate():
		return fmt.Errorf("%s isn't a unique local (fc00::/7) range", p)
	case p.Bits() != 64:
		return fmt.Errorf("%s: the IPv6 subnet must be a /64", p)
	}
	return nil
}

// MaxHostV4 returns the highest usable host number in an IPv4 subnet (the one below
// the broadcast address).
func MaxHostV4(p netip.Prefix) uint32 {
	return uint32(1)<<(32-p.Bits()) - 2
}

// HostV4 returns host number n in an IPv4 subnet.
func HostV4(p netip.Prefix, n uint32) (netip.Addr, error) {
	if n < 1 || n > MaxHostV4(p) {
		return netip.Addr{}, fmt.Errorf("host %d is outside %s", n, p)
	}
	b := p.Masked().Addr().As4()
	binary.BigEndian.PutUint32(b[:], binary.BigEndian.Uint32(b[:])+n)
	return netip.AddrFrom4(b), nil
}

// HostNumberV4 returns the host number of addr in p, and false if addr isn't a host
// address in p.
func HostNumberV4(p netip.Prefix, addr netip.Addr) (uint32, bool) {
	if !addr.Is4() || !p.Contains(addr) {
		return 0, false
	}
	a, base := addr.As4(), p.Masked().Addr().As4()
	n := binary.BigEndian.Uint32(a[:]) - binary.BigEndian.Uint32(base[:])
	if n < 1 || n > MaxHostV4(p) {
		return 0, false
	}
	return n, true
}

// MirrorV6 returns the IPv6 address in the /64 p whose interface ID is host number n
// written with the same digits: n = 23 gives …::23.
func MirrorV6(p netip.Prefix, n uint32) (netip.Addr, error) {
	if p.Bits() != 64 {
		return netip.Addr{}, fmt.Errorf("%s isn't a /64", p)
	}
	// The decimal digits of n read as hexadecimal. n is at most 65534 (a /16), so this
	// is at most 0x65534 and fits the 64-bit interface ID.
	id, err := strconv.ParseUint(strconv.FormatUint(uint64(n), 10), 16, 64)
	if err != nil {
		return netip.Addr{}, err
	}
	b := p.Masked().Addr().As16()
	binary.BigEndian.PutUint64(b[8:], id)
	return netip.AddrFrom16(b), nil
}

// Allocation is the pair of addresses for one host.
type Allocation struct {
	Host uint32
	IPv4 netip.Addr
	// IPv6 is the zero Addr when the VPN has no IPv6 subnet.
	IPv6 netip.Addr
}

// Server returns the server's addresses.
func Server(v4, v6 netip.Prefix) (Allocation, error) {
	return at(v4, v6, ServerHost)
}

// Next returns the lowest free client allocation. used holds the IPv4 addresses already
// assigned; the server's address is always treated as used.
func Next(v4, v6 netip.Prefix, used map[netip.Addr]bool) (Allocation, error) {
	for n := uint32(ServerHost + 1); n <= MaxHostV4(v4); n++ {
		addr, err := HostV4(v4, n)
		if err != nil {
			return Allocation{}, err
		}
		if !used[addr] {
			return at(v4, v6, n)
		}
	}
	return Allocation{}, ErrExhausted
}

func at(v4, v6 netip.Prefix, n uint32) (Allocation, error) {
	a := Allocation{Host: n}
	var err error
	if a.IPv4, err = HostV4(v4, n); err != nil {
		return Allocation{}, err
	}
	if v6.IsValid() {
		if a.IPv6, err = MirrorV6(v6, n); err != nil {
			return Allocation{}, err
		}
	}
	return a, nil
}
