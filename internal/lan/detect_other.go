//go:build !linux

package lan

import "net/netip"

// Detect finds nothing off Linux, where Drawbridge only runs for development: the admin
// UI is then reachable from loopback and link-local addresses only.
func Detect() ([]netip.Prefix, error) { return nil, nil }

// HostAddrs finds nothing off Linux.
func HostAddrs() ([]netip.Addr, error) { return nil, nil }

// Read finds nothing off Linux.
func Read() (Snapshot, error) { return Snapshot{}, nil }
