package wg

import (
	"fmt"
	"net/netip"
	"slices"
	"sync"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Fake is an in-memory Backend for tests. It follows the kernel's semantics for peer
// changes, and records every call so tests can check what the reconciler did.
type Fake struct {
	mu      sync.Mutex
	devices map[string]*Device
	// Calls lists every changing call, such as "create wg0" or "configure wg0".
	Calls []string
	// Err, when set, is returned by the next changing call.
	Err error
}

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{devices: map[string]*Device{}}
}

func (f *Fake) record(call string) error {
	f.Calls = append(f.Calls, call)
	if err := f.Err; err != nil {
		f.Err = nil
		return err
	}
	return nil
}

// Device returns a copy of the interface's state.
func (f *Fake) Device(name string) (Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[name]
	if !ok {
		return Device{}, ErrNoDevice
	}
	out := *d
	out.Addrs = slices.Clone(d.Addrs)
	out.Peers = make([]Peer, len(d.Peers))
	for i, p := range d.Peers {
		p.AllowedIPs = slices.Clone(p.AllowedIPs)
		out.Peers[i] = p
	}
	return out, nil
}

// Create creates an interface.
func (f *Fake) Create(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("create " + name); err != nil {
		return err
	}
	if _, ok := f.devices[name]; ok {
		return fmt.Errorf("%s already exists", name)
	}
	f.devices[name] = &Device{Name: name, MTU: 1420}
	return nil
}

// Delete deletes an interface.
func (f *Fake) Delete(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("delete " + name); err != nil {
		return err
	}
	delete(f.devices, name)
	return nil
}

func (f *Fake) device(name string) (*Device, error) {
	d, ok := f.devices[name]
	if !ok {
		return nil, ErrNoDevice
	}
	return d, nil
}

// Configure applies a configuration like the kernel does.
func (f *Fake) Configure(name string, cfg wgtypes.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("configure " + name); err != nil {
		return err
	}
	d, err := f.device(name)
	if err != nil {
		return err
	}
	if cfg.ReplacePeers {
		return fmt.Errorf("ReplacePeers would drop every session")
	}
	if cfg.PrivateKey != nil {
		d.PrivateKey = *cfg.PrivateKey
	}
	if cfg.ListenPort != nil {
		d.ListenPort = *cfg.ListenPort
	}
	for _, pc := range cfg.Peers {
		i := slices.IndexFunc(d.Peers, func(p Peer) bool { return p.PublicKey == pc.PublicKey })
		if pc.Remove {
			if i >= 0 {
				d.Peers = slices.Delete(d.Peers, i, i+1)
			}
			continue
		}
		if i < 0 {
			if pc.UpdateOnly {
				continue
			}
			d.Peers = append(d.Peers, Peer{PublicKey: pc.PublicKey})
			i = len(d.Peers) - 1
		}
		p := &d.Peers[i]
		if pc.PresharedKey != nil {
			p.PresharedKey = *pc.PresharedKey
		}
		if pc.ReplaceAllowedIPs {
			p.AllowedIPs = nil
		}
		for _, n := range pc.AllowedIPs {
			if pr, ok := Prefix(n); ok {
				p.AllowedIPs = append(p.AllowedIPs, pr)
			}
		}
	}
	return nil
}

// SetMTU sets the MTU.
func (f *Fake) SetMTU(name string, mtu int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(fmt.Sprintf("mtu %s %d", name, mtu)); err != nil {
		return err
	}
	d, err := f.device(name)
	if err != nil {
		return err
	}
	d.MTU = mtu
	return nil
}

// AddAddr adds an address.
func (f *Fake) AddAddr(name string, addr netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(fmt.Sprintf("addr add %s %s", name, addr)); err != nil {
		return err
	}
	d, err := f.device(name)
	if err != nil {
		return err
	}
	d.Addrs = append(d.Addrs, addr)
	return nil
}

// DelAddr removes an address.
func (f *Fake) DelAddr(name string, addr netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record(fmt.Sprintf("addr del %s %s", name, addr)); err != nil {
		return err
	}
	d, err := f.device(name)
	if err != nil {
		return err
	}
	d.Addrs = slices.DeleteFunc(d.Addrs, func(a netip.Prefix) bool { return a == addr })
	return nil
}

// SetUp brings the interface up.
func (f *Fake) SetUp(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("up " + name); err != nil {
		return err
	}
	d, err := f.device(name)
	if err != nil {
		return err
	}
	d.Up = true
	return nil
}

// SetHandshake sets a peer's status fields, as if traffic had flowed.
func (f *Fake) SetHandshake(name string, key wgtypes.Key, p Peer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[name]
	if !ok {
		return
	}
	for i := range d.Peers {
		if d.Peers[i].PublicKey == key {
			d.Peers[i].Endpoint = p.Endpoint
			d.Peers[i].LastHandshake = p.LastHandshake
			d.Peers[i].ReceiveBytes = p.ReceiveBytes
			d.Peers[i].SendBytes = p.SendBytes
		}
	}
}

var _ Backend = (*Fake)(nil)
var _ Backend = (*Kernel)(nil)
