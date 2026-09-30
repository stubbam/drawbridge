// Command drawbridge is the Drawbridge daemon and its command-line tool.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/stuffam/drawbridge/internal/control"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/version"
)

// Default paths for the packaged daemon (docs/PLAN.md §4.4).
const (
	defaultDB      = store.DefaultPath
	defaultSecret  = "/etc/drawbridge/secret.key"
	defaultControl = control.DefaultSocket
)

const usageText = `Usage: drawbridge <command> [arguments]

Commands:
  serve                Run the Drawbridge daemon.
  tunnel up|down       Bring the WireGuard tunnel up or down
                       (drawbridge-tunnel.service runs these).
  server show          Show the server's settings.
  server set FLAGS     Change the server's settings.
  client list          List clients and their status.
  client add NAME      Add a client.
  client show NAME     Show one client.
  client pause NAME    Pause a client, so it can't connect until it's resumed.
  client resume NAME   Resume a paused client.
  client rename NAME NEW-NAME
                       Rename a client.
  client delete NAME   Delete a client.
  client config NAME   Print a client's WireGuard config.
  client qr NAME       Show a client's config as a QR code for the WireGuard app.
  events               Show the event log: changes, logins, and corrected drift.
  doctor               Check the host and network for problems that stop the VPN working.
  admin setup-token    Show the token that first-run setup in the web UI asks for.
  admin create NAME    Create the admin account with a random password.
  admin reset-password Give the admin account a new random password.
  version              Print the version.
  help                 Show this help.

The server, client, events, doctor, and admin commands talk to the daemon, so run them as root
(sudo) or as a member of the drawbridge group. Run "drawbridge <command> -h" for a command's flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run executes one command and returns the process exit status.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(ctx, args[1:], stderr)
	case "tunnel":
		return tunnelCmd(ctx, args[1:], stderr)
	case "server":
		return serverCmd(ctx, args[1:], stdout, stderr)
	case "client":
		return clientCmd(ctx, args[1:], stdin, stdout, stderr)
	case "events":
		return eventsCmd(ctx, args[1:], stdout, stderr)
	case "doctor":
		return doctorCmd(ctx, args[1:], stdout, stderr)
	case "admin":
		return adminCmd(ctx, args[1:], stdout, stderr)
	case "version", "-version", "--version":
		fmt.Fprintf(stdout, "drawbridge %s\n", version.String())
		return 0
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		fmt.Fprintf(stderr, "drawbridge: unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
}

// parseArgs parses flags that may appear before, between, or after positional
// arguments, and returns the positional ones.
func parseArgs(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}
		args = flags.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// flagStatus converts a flag parsing error to an exit status: 0 for -h, 2 otherwise.
func flagStatus(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

// lockPath is the reconcile lock shared by the tunnel unit and the daemon. It lives next
// to the database, in the state directory both units share.
func lockPath(db string) string {
	return filepath.Join(filepath.Dir(db), "reconcile.lock")
}

// rulesetPath is where the last applied nftables ruleset is saved for inspection.
func rulesetPath(db string) string {
	return filepath.Join(filepath.Dir(db), "nftables.conf")
}

// newLogger logs text to w. Under systemd, journald adds its own timestamps, so the
// logger leaves them out.
func newLogger(w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{}
	if os.Getenv("JOURNAL_STREAM") != "" {
		opts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
