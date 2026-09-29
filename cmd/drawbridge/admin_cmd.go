package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/stuffam/drawbridge/internal/control"
	"github.com/stuffam/drawbridge/internal/views"
)

const adminUsage = `Usage: drawbridge admin <command> [USERNAME] [flags]

  setup-token        Show the one-time token that first-run setup in the web UI asks
                     for, until the admin account exists.
  create USERNAME    Create the admin account with a random password, instead of
                     using setup in the web UI.
  reset-password [USERNAME]
                     Give the admin account a new random password, log out all of its
                     sessions, and lift any lockout from failed logins.

Flags:
`

func adminCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	// The number of arguments each command takes: at least, at most.
	commands := map[string][2]int{"setup-token": {0, 0}, "create": {1, 1}, "reset-password": {0, 1}}
	if len(args) == 0 {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	sub := args[0]
	n, ok := commands[sub]
	if !ok {
		fmt.Fprintf(stderr, "drawbridge admin: unknown command %q\n\n%s", sub, adminUsage)
		return 2
	}
	flags := newFlagSet("admin "+sub, stderr)
	socket := flags.String("control", defaultControl, "daemon control socket `path`")
	flags.Usage = func() { fmt.Fprint(stderr, adminUsage); flags.PrintDefaults() }
	pos, err := parseArgs(flags, args[1:])
	if err != nil {
		return flagStatus(err)
	}
	if len(pos) < n[0] || len(pos) > n[1] {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	c := control.NewClient(*socket)
	fail := func(err error) int {
		fmt.Fprintln(stderr, "drawbridge:", err)
		return 1
	}

	switch sub {
	case "setup-token":
		res, err := c.SetupToken(ctx)
		var cerr *control.Error
		if errors.As(err, &cerr) && cerr.Status == http.StatusConflict {
			fmt.Fprintln(stderr, "Setup is complete: the admin account exists. If you can't log in, run:")
			fmt.Fprintln(stderr, "  sudo drawbridge admin reset-password")
			return 1
		}
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Setup token: %s\n", res.Token)
		fmt.Fprintf(stdout, "Open %s from your home network or the VPN, and enter it to create the admin account.\n", uiURL())
		if res.Fingerprint != "" {
			fmt.Fprintf(stdout, "Your browser will warn that the certificate is self-signed. Check that its SHA-256\nfingerprint is %s\n", res.Fingerprint)
		}
		return 0

	case "create":
		res, err := c.CreateAdmin(ctx, pos[0])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Created the admin account %q.\nPassword: %s\n", res.Username, res.Password)
		fmt.Fprintf(stdout, "Log in at %s, then change the password.\n", uiURL())
		return 0

	case "reset-password":
		username := ""
		if len(pos) == 1 {
			username = pos[0]
		}
		res, err := c.ResetPassword(ctx, username)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "New password for %q: %s\n", res.Username, res.Password)
		fmt.Fprintln(stdout, "Its sessions are logged out. Log in with the new password, then change it.")
		return 0
	}
	return 2
}

// uiURL is the web UI's address on this machine, as people on the LAN would type it.
func uiURL() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "https://<this machine's address>:51821"
	}
	return "https://" + host + ":51821"
}

const eventsUsage = `Usage: drawbridge events [flags]

Shows the event log, newest first: changes made in the web UI and with this command
line, logins, and drift the daemon corrected.

Flags:
`

func eventsCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("events", stderr)
	socket := flags.String("control", defaultControl, "daemon control socket `path`")
	client := flags.String("client", "", "show only events about the client `NAME`")
	limit := flags.Int("limit", 50, fmt.Sprintf("show at most `N` events (1–%d)", views.MaxEvents))
	flags.Usage = func() { fmt.Fprint(stderr, eventsUsage); flags.PrintDefaults() }
	pos, err := parseArgs(flags, args)
	if err != nil {
		return flagStatus(err)
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "drawbridge events: unexpected argument %q\n", pos[0])
		return 2
	}
	if *limit < 1 || *limit > views.MaxEvents {
		fmt.Fprintf(stderr, "drawbridge events: --limit must be 1–%d\n", views.MaxEvents)
		return 2
	}
	events, err := control.NewClient(*socket).Events(ctx, *client, *limit)
	if err != nil {
		fmt.Fprintln(stderr, "drawbridge:", err)
		return 1
	}
	printEvents(stdout, events, time.Local)
	return 0
}

func printEvents(w io.Writer, events []views.EventView, loc *time.Location) {
	if len(events) == 0 {
		fmt.Fprintln(w, "No events yet.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tEVENT\tWHO\tCLIENT\tDETAILS")
	for _, e := range events {
		who := e.Actor + " (" + e.Via
		if e.SourceIP != "" {
			who += " " + e.SourceIP
		}
		who += ")"
		client := e.ClientName
		if client == "" {
			client = "-"
		}
		var details []string
		for _, k := range slices.Sorted(maps.Keys(e.Data)) {
			details = append(details, k+": "+e.Data[k])
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Time.In(loc).Format("2006-01-02 15:04:05"), e.Kind,
			who, client, strings.Join(details, "; "))
	}
	_ = tw.Flush()
}
