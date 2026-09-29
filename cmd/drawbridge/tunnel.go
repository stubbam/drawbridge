package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"

	"github.com/stuffam/drawbridge/internal/firewall"
	"github.com/stuffam/drawbridge/internal/keys"
	"github.com/stuffam/drawbridge/internal/lan"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/reconcile"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/wg"
)

const tunnelUsage = `Usage: drawbridge tunnel up|down [flags]

  up     Create the WireGuard interface if it's missing, and apply the settings, the
         clients, and the firewall rules from the database.
  down   Delete the interface and Drawbridge's nftables table.

drawbridge-tunnel.service runs these; use systemctl to start and stop the tunnel.
`

func tunnelCmd(ctx context.Context, args []string, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "up" && args[0] != "down") {
		fmt.Fprint(stderr, tunnelUsage)
		return 2
	}
	sub := args[0]
	flags := newFlagSet("tunnel "+sub, stderr)
	flags.Usage = func() { fmt.Fprint(stderr, tunnelUsage); flags.PrintDefaults() }
	dbPath := flags.String("db", defaultDB, "database `file`")
	secret := flags.String("secret-key", defaultSecret, "at-rest encryption key `file`")
	pos, err := parseArgs(flags, args[1:])
	if err != nil {
		return flagStatus(err)
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "drawbridge tunnel %s: unexpected argument %q\n", sub, pos[0])
		return 2
	}

	log := newLogger(stderr)
	sealer, err := keys.LoadSealer(*secret)
	if err != nil {
		log.Error("can't start", "err", err)
		return 1
	}
	st, err := store.Open(ctx, *dbPath, sealer)
	if err != nil {
		log.Error("can't open the database", "err", err)
		return 1
	}
	defer st.Close()
	backend, err := wg.NewKernel()
	if err != nil {
		log.Error("can't start", "err", err)
		return 1
	}
	defer backend.Close()
	rec := &reconcile.Reconciler{
		State:     st,
		WG:        backend,
		Firewall:  firewall.NewApplier(rulesetPath(*dbPath)),
		Lock:      reconcile.FileLock{Path: lockPath(*dbPath)},
		Log:       log,
		AdminPort: model.AdminPort,
		LAN:       detectLAN(log),
	}
	if err := runTunnel(ctx, sub, st, rec, log); err != nil {
		log.Error("tunnel "+sub+" failed", "err", err)
		return 1
	}
	return 0
}

func runTunnel(ctx context.Context, sub string, st *store.Store, rec *reconcile.Reconciler, log *slog.Logger) error {
	if sub == "down" {
		if _, err := st.Settings(ctx); errors.Is(err, store.ErrNotInitialized) {
			log.Info("tunnel down: nothing to do; the server was never initialized")
			return nil
		}
		if err := rec.Down(ctx); err != nil {
			return err
		}
		log.Info("tunnel down")
		return nil
	}

	if created, err := st.Initialize(ctx); err != nil {
		return err
	} else if created {
		log.Info("initialized a new server: new key pair and IPv6 prefix")
	}
	res, err := rec.Up(ctx)
	if err != nil {
		return err
	}
	s, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	log.Info("tunnel up", "interface", s.Interface, "listen_port", s.ListenPort, "changes", res.Changes)
	return nil
}

// detectLAN returns the reconciler's LAN lookup. If the routing table can't be read,
// the admin UI is reachable from the VPN, loopback, and link-local addresses only until
// the next try.
func detectLAN(log *slog.Logger) func() []netip.Prefix {
	return func() []netip.Prefix {
		ps, err := lan.Detect()
		if err != nil {
			log.Warn("can't detect the LAN's subnets", "err", err)
		}
		return ps
	}
}
