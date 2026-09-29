// Package wg reads and configures WireGuard interfaces over netlink (ADR 0005). Only
// the reconciler changes interfaces (CLAUDE.md, "The database is the source of truth").
package wg

import (
	"errors"
	"net"
	"net/netip"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// ErrNoDevice means the interface doesn't exist.
var ErrNoDevice = errors.New("the WireGuard interface doesn't exist")

// Device is an interface's current state.
type Device struct {
	Name       string
	PrivateKey wgtypes.Key
	ListenPort int
	MTU        int
	Up         bool
	// Addrs are the interface's addresses, excluding link-local ones.
	Addrs []netip.Prefix
	Peers []Peer
}

// Peer is one peer's configuration and status.
type Peer struct {
	PublicKey     wgtypes.Key
	PresharedKey  wgtypes.Key
	AllowedIPs    []netip.Prefix
	Endpoint      netip.AddrPort
	LastHandshake time.Time
	ReceiveBytes  int64
	SendBytes     int64
}

// Backend reads and changes WireGuard interfaces. Kernel is the real one; Fake is for
// tests.
type Backend interface {
	// Device returns the interface's state, or ErrNoDevice.
	Device(name string) (Device, error)
	// Create creates a WireGuard interface.
	Create(name string) error
	// Delete deletes the interface. A missing interface isn't an error.
	Delete(name string) error
	// Configure changes the private key, listen port, and peers. It never replaces the
	// whole peer list, so unchanged peers keep their sessions.
	Configure(name string, cfg wgtypes.Config) error
	SetMTU(name string, mtu int) error
	AddAddr(name string, addr netip.Prefix) error
	DelAddr(name string, addr netip.Prefix) error
	SetUp(name string) error
}

// IPNet converts a prefix to the net.IPNet that wgctrl and netlink use.
func IPNet(p netip.Prefix) net.IPNet {
	return net.IPNet{
		IP:   net.IP(p.Addr().AsSlice()),
		Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen()),
	}
}

// Prefix converts a net.IPNet to a prefix. It returns false for an invalid one.
func Prefix(n net.IPNet) (netip.Prefix, bool) {
	addr, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	ones, bits := n.Mask.Size()
	if bits == 0 {
		return netip.Prefix{}, false
	}
	if bits == 32 {
		addr = addr.Unmap()
	}
	return netip.PrefixFrom(addr, ones), true
}
