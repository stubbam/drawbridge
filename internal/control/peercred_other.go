//go:build !linux

package control

import "net"

// peerUID isn't implemented off Linux, where Drawbridge only runs for development.
func peerUID(net.Conn) (uint32, bool) { return 0, false }
