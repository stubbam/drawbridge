//go:build linux

package wg

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Kernel drives the in-kernel WireGuard implementation with wgctrl (keys, port, peers)
// and rtnetlink (the link, its addresses, and its MTU). It works in the network
// namespace that was current when NewKernel ran; it needs CAP_NET_ADMIN there.
type Kernel struct {
	wg *wgctrl.Client
	nl *netlink.Handle
}

// NewKernel opens the netlink sockets.
func NewKernel() (*Kernel, error) {
	nl, err := netlink.NewHandle(unix.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("opening rtnetlink: %w", err)
	}
	c, err := wgctrl.New()
	if err != nil {
		nl.Close()
		return nil, fmt.Errorf("opening WireGuard's netlink interface: %w", err)
	}
	return &Kernel{wg: c, nl: nl}, nil
}

// Close closes the netlink sockets.
func (k *Kernel) Close() error {
	k.nl.Close()
	return k.wg.Close()
}

func (k *Kernel) link(name string) (netlink.Link, error) {
	l, err := k.nl.LinkByName(name)
	var notFound netlink.LinkNotFoundError
	if errors.As(err, &notFound) {
		return nil, ErrNoDevice
	}
	if err != nil {
		return nil, err
	}
	if l.Type() != "wireguard" {
		return nil, fmt.Errorf("interface %s exists but is a %s interface, not WireGuard", name, l.Type())
	}
	return l, nil
}

// Device returns the interface's state.
func (k *Kernel) Device(name string) (Device, error) {
	l, err := k.link(name)
	if err != nil {
		return Device{}, err
	}
	d, err := k.wg.Device(name)
	if errors.Is(err, os.ErrNotExist) {
		return Device{}, ErrNoDevice
	}
	if err != nil {
		return Device{}, fmt.Errorf("reading WireGuard device %s: %w", name, err)
	}
	addrs, err := k.nl.AddrList(l, netlink.FAMILY_ALL)
	if err != nil {
		return Device{}, fmt.Errorf("reading addresses of %s: %w", name, err)
	}
	dev := Device{
		Name:       name,
		PrivateKey: d.PrivateKey,
		ListenPort: d.ListenPort,
		MTU:        l.Attrs().MTU,
		Up:         l.Attrs().Flags&net.FlagUp != 0,
	}
	for _, a := range addrs {
		if a.IPNet == nil {
			continue
		}
		p, ok := Prefix(*a.IPNet)
		if ok && !p.Addr().IsLinkLocalUnicast() {
			dev.Addrs = append(dev.Addrs, p)
		}
	}
	for _, p := range d.Peers {
		peer := Peer{
			PublicKey:     p.PublicKey,
			PresharedKey:  p.PresharedKey,
			LastHandshake: p.LastHandshakeTime,
			ReceiveBytes:  p.ReceiveBytes,
			SendBytes:     p.TransmitBytes,
		}
		if p.Endpoint != nil {
			peer.Endpoint = p.Endpoint.AddrPort()
			peer.Endpoint = netip.AddrPortFrom(peer.Endpoint.Addr().Unmap(), peer.Endpoint.Port())
		}
		for _, n := range p.AllowedIPs {
			if pr, ok := Prefix(n); ok {
				peer.AllowedIPs = append(peer.AllowedIPs, pr)
			}
		}
		dev.Peers = append(dev.Peers, peer)
	}
	return dev, nil
}

// Create creates a WireGuard interface.
func (k *Kernel) Create(name string) error {
	err := k.nl.LinkAdd(&netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: name}})
	if errors.Is(err, unix.EOPNOTSUPP) {
		return fmt.Errorf("creating %s: the kernel doesn't support WireGuard interfaces; "+
			"is the wireguard module loaded (modprobe wireguard)? %w", name, err)
	}
	if err != nil {
		return fmt.Errorf("creating %s: %w", name, err)
	}
	return nil
}

// Delete deletes the interface.
func (k *Kernel) Delete(name string) error {
	l, err := k.link(name)
	if errors.Is(err, ErrNoDevice) {
		return nil
	}
	if err != nil {
		return err
	}
	return k.nl.LinkDel(l)
}

// Configure applies a wgctrl configuration.
func (k *Kernel) Configure(name string, cfg wgtypes.Config) error {
	if cfg.ReplacePeers {
		return errors.New("refusing to replace every peer; that would drop all sessions")
	}
	return k.wg.ConfigureDevice(name, cfg)
}

// SetMTU sets the interface's MTU.
func (k *Kernel) SetMTU(name string, mtu int) error {
	l, err := k.link(name)
	if err != nil {
		return err
	}
	return k.nl.LinkSetMTU(l, mtu)
}

// AddAddr adds an address. IPv6 addresses skip duplicate address detection, which a
// point-to-point tunnel doesn't need and which would otherwise delay them.
func (k *Kernel) AddAddr(name string, addr netip.Prefix) error {
	l, err := k.link(name)
	if err != nil {
		return err
	}
	n := IPNet(addr)
	a := &netlink.Addr{IPNet: &n}
	if addr.Addr().Is6() {
		a.Flags = unix.IFA_F_NODAD
	}
	return k.nl.AddrAdd(l, a)
}

// DelAddr removes an address.
func (k *Kernel) DelAddr(name string, addr netip.Prefix) error {
	l, err := k.link(name)
	if err != nil {
		return err
	}
	n := IPNet(addr)
	return k.nl.AddrDel(l, &netlink.Addr{IPNet: &n})
}

// SetUp brings the interface up.
func (k *Kernel) SetUp(name string) error {
	l, err := k.link(name)
	if err != nil {
		return err
	}
	return k.nl.LinkSetUp(l)
}
