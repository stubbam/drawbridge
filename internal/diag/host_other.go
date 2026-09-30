//go:build !linux

package diag

import (
	"time"

	"github.com/stuffam/drawbridge/internal/lan"
)

// NewHost returns a host that can read only what works off Linux, where Drawbridge runs
// for development: the resolver and the (empty) network snapshot.
func NewHost(stateDir string) Host {
	return Host{StateDir: stateDir, Now: time.Now, LookupIP: lookupIP, Network: lan.Read}
}
