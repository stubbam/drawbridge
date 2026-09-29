//go:build !linux

package wg

import (
	"errors"
	"net/netip"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// errUnsupported is what Kernel returns off Linux. Everything else in Drawbridge builds
// and tests on other systems (with the Fake backend), so development works on a Mac.
var errUnsupported = errors.New("Drawbridge configures WireGuard through the Linux kernel; this system isn't Linux")

// Kernel is unavailable off Linux.
type Kernel struct{}

// NewKernel reports that the kernel backend needs Linux.
func NewKernel() (*Kernel, error) { return nil, errUnsupported }

// Close does nothing.
func (k *Kernel) Close() error { return nil }

// Device reports that the kernel backend needs Linux.
func (k *Kernel) Device(string) (Device, error) { return Device{}, errUnsupported }

// Create reports that the kernel backend needs Linux.
func (k *Kernel) Create(string) error { return errUnsupported }

// Delete reports that the kernel backend needs Linux.
func (k *Kernel) Delete(string) error { return errUnsupported }

// Configure reports that the kernel backend needs Linux.
func (k *Kernel) Configure(string, wgtypes.Config) error { return errUnsupported }

// SetMTU reports that the kernel backend needs Linux.
func (k *Kernel) SetMTU(string, int) error { return errUnsupported }

// AddAddr reports that the kernel backend needs Linux.
func (k *Kernel) AddAddr(string, netip.Prefix) error { return errUnsupported }

// DelAddr reports that the kernel backend needs Linux.
func (k *Kernel) DelAddr(string, netip.Prefix) error { return errUnsupported }

// SetUp reports that the kernel backend needs Linux.
func (k *Kernel) SetUp(string) error { return errUnsupported }
