package diag

import (
	"context"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stuffam/drawbridge/internal/firewall"
	"github.com/stuffam/drawbridge/internal/lan"
)

// NewHost returns the real host: its files, its resolver, its routing table, and its
// nftables ruleset. stateDir is where the daemon keeps its database.
func NewHost(stateDir string) Host {
	return Host{
		FS:       os.DirFS("/"),
		StateDir: stateDir,
		Now:      time.Now,
		Statfs:   statfs,
		LookupIP: lookupIP,
		Network:  lan.Read,
		Nft: func(ctx context.Context) ([]byte, error) {
			return firewall.Nft(ctx, "", "-j", "list", "ruleset")
		},
	}
}

func statfs(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize) //nolint:gosec // G115: a block size is positive.
	return st.Bavail * bsize, st.Blocks * bsize, nil
}
