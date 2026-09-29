package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/stuffam/drawbridge/internal/clientconf"
	"github.com/stuffam/drawbridge/internal/control"
	"github.com/stuffam/drawbridge/internal/views"
)

const clientUsage = `Usage: drawbridge client <command> [NAME] [flags]

  list          List clients and their status.
  add NAME      Add a client. Names are 1–64 letters, digits, spaces, and . _ ' -.
  show NAME     Show one client.
  pause NAME    Pause a client: it's removed from the tunnel and can't connect.
  resume NAME   Resume a paused client.
  rename NAME NEW-NAME
                Rename a client. Its config and keys don't change.
  delete NAME   Delete a client.
  config NAME   Print the client's WireGuard config (save it as a .conf file).
  qr NAME       Show the config as a QR code to scan with the WireGuard app.

Quote names that contain spaces: drawbridge client add "Alex's iPhone"

Flags:
`

func clientCmd(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// The number of names each command takes.
	commands := map[string]int{"list": 0, "add": 1, "show": 1, "pause": 1, "resume": 1,
		"rename": 2, "delete": 1, "config": 1, "qr": 1}
	if len(args) == 0 {
		fmt.Fprint(stderr, clientUsage)
		return 2
	}
	sub := args[0]
	names, ok := commands[sub]
	if !ok {
		fmt.Fprintf(stderr, "drawbridge client: unknown command %q\n\n%s", sub, clientUsage)
		return 2
	}
	flags := newFlagSet("client "+sub, stderr)
	socket := flags.String("control", defaultControl, "daemon control socket `path`")
	showQR := flags.Bool("qr", false, "with add: show the new client's QR code")
	yes := flags.Bool("yes", false, "with delete: don't ask for confirmation")
	flags.Usage = func() { fmt.Fprint(stderr, clientUsage); flags.PrintDefaults() }
	pos, err := parseArgs(flags, args[1:])
	if err != nil {
		return flagStatus(err)
	}
	switch {
	case names == 1 && len(pos) != 1:
		fmt.Fprintf(stderr, "drawbridge client %s needs exactly one NAME (quote names with spaces)\n", sub)
		return 2
	case names == 2 && len(pos) != 2:
		fmt.Fprintf(stderr, "drawbridge client %s needs NAME and NEW-NAME (quote names with spaces)\n", sub)
		return 2
	case names == 0 && len(pos) > 0:
		fmt.Fprintf(stderr, "drawbridge client %s: unexpected argument %q\n", sub, pos[0])
		return 2
	}
	c := control.NewClient(*socket)
	fail := func(err error) int {
		fmt.Fprintln(stderr, "drawbridge:", err)
		return 1
	}

	switch sub {
	case "list":
		clients, err := c.Clients(ctx)
		if err != nil {
			return fail(err)
		}
		printClients(stdout, clients, time.Now())
		return 0

	case "show":
		v, err := c.Client(ctx, pos[0])
		if err != nil {
			return fail(err)
		}
		printClient(stdout, v, time.Now())
		return 0

	case "add":
		res, err := c.AddClient(ctx, pos[0])
		if err != nil {
			return fail(err)
		}
		v := res.Client
		fmt.Fprintf(stdout, "Added client %q: %s\n", v.Name, addrList(v))
		if *showQR {
			if code := printQR(ctx, c, v.Name, stdout, stderr); code != 0 {
				return code
			}
		} else {
			q := shellQuote(v.Name)
			fmt.Fprintf(stdout, "Show its QR code:   drawbridge client qr %s\n", q)
			fmt.Fprintf(stdout, "Save its config:    drawbridge client config %s > %s\n", q, views.ConfigFileName(v.Name))
		}
		return warn(stderr, res.Warning, res.ApplyFailed)

	case "pause", "resume":
		res, err := c.SetEnabled(ctx, pos[0], sub == "resume")
		if err != nil {
			return fail(err)
		}
		if sub == "pause" {
			fmt.Fprintf(stdout, "Paused client %q. It can't connect until it's resumed.\n", res.Client.Name)
		} else {
			fmt.Fprintf(stdout, "Resumed client %q.\n", res.Client.Name)
		}
		return warn(stderr, res.Warning, res.ApplyFailed)

	case "rename":
		res, err := c.RenameClient(ctx, pos[0], pos[1])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Renamed client %q to %q.\n", pos[0], res.Client.Name)
		return 0

	case "delete":
		if !*yes {
			ok, err := confirm(stdin, stdout, fmt.Sprintf("Delete client %q? Its config stops working. [y/N] ", pos[0]))
			if err != nil {
				return fail(err)
			}
			if !ok {
				fmt.Fprintln(stdout, "Not deleted.")
				return 1
			}
		}
		res, err := c.DeleteClient(ctx, pos[0])
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Deleted client %q.\n", res.Client.Name)
		return warn(stderr, res.Warning, res.ApplyFailed)

	case "config":
		conf, err := c.Config(ctx, pos[0])
		if err != nil {
			return fail(err)
		}
		fmt.Fprint(stdout, conf)
		return 0

	case "qr":
		return printQR(ctx, c, pos[0], stdout, stderr)
	}
	return 2
}

func printQR(ctx context.Context, c *control.Client, name string, stdout, stderr io.Writer) int {
	conf, err := c.Config(ctx, name)
	if err != nil {
		fmt.Fprintln(stderr, "drawbridge:", err)
		return 1
	}
	if err := clientconf.WriteQR(stdout, conf); err != nil {
		fmt.Fprintln(stderr, "drawbridge:", err)
		return 1
	}
	fmt.Fprintln(stderr, "In the WireGuard app, tap + and choose \"Scan from QR code\".")
	return 0
}

// confirm asks a yes-or-no question on a terminal. Without a terminal, the answer is
// no: scripts pass --yes.
func confirm(stdin io.Reader, stdout io.Writer, question string) (bool, error) {
	if f, ok := stdin.(*os.File); ok {
		if info, err := f.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false, fmt.Errorf("not a terminal; add --yes to delete without asking")
		}
	}
	fmt.Fprint(stdout, question)
	answer, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && answer == "" {
		return false, nil
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func addrList(v views.ClientView) string {
	s := v.IPv4.String()
	if v.IPv6.IsValid() {
		s += ", " + v.IPv6.String()
	}
	return s
}

func clientState(v views.ClientView) string {
	switch {
	case !v.Enabled:
		return "paused"
	case v.Peer == nil:
		return "not in tunnel"
	}
	return "active"
}

func printClients(w io.Writer, clients []views.ClientView, now time.Time) {
	if len(clients) == 0 {
		fmt.Fprintln(w, `No clients yet. Add one with: drawbridge client add NAME`)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tIPV4\tIPV6\tHANDSHAKE\tENDPOINT\tRECEIVED\tSENT")
	for _, v := range clients {
		ipv6 := "-"
		if v.IPv6.IsValid() {
			ipv6 = v.IPv6.String()
		}
		handshake, endpoint, rx, tx := "-", "-", "-", "-"
		if p := v.Peer; p != nil {
			handshake = ago(p.LastHandshake, now)
			if p.Endpoint != "" {
				endpoint = p.Endpoint
			}
			rx, tx = bytesText(p.ReceiveBytes), bytesText(p.SendBytes)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.Name, clientState(v), v.IPv4, ipv6,
			handshake, endpoint, rx, tx)
	}
	_ = tw.Flush()
}

func printClient(w io.Writer, v views.ClientView, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	ipv6 := "none"
	if v.IPv6.IsValid() {
		ipv6 = v.IPv6.String()
	}
	rows := [][2]string{
		{"Name", v.Name},
		{"State", clientState(v)},
		{"IPv4", v.IPv4.String()},
		{"IPv6", ipv6},
		{"Public key", v.PublicKey},
		{"Created", v.CreatedAt.UTC().Format("2006-01-02 15:04 MST")},
	}
	if p := v.Peer; p != nil {
		endpoint := p.Endpoint
		if endpoint == "" {
			endpoint = "-"
		}
		rows = append(rows,
			[2]string{"Handshake", ago(p.LastHandshake, now)},
			[2]string{"Endpoint", endpoint},
			[2]string{"Received (total)", bytesText(p.ReceiveBytes)},
			[2]string{"Sent (total)", bytesText(p.SendBytes)})
		if !p.SessionStartedAt.IsZero() {
			rows = append(rows,
				[2]string{"Connected", ago(p.SessionStartedAt, now)},
				[2]string{"Received (session)", bytesText(p.SessionReceiveBytes)},
				[2]string{"Sent (session)", bytesText(p.SessionSendBytes)})
		}
	}
	for _, r := range rows {
		fmt.Fprintf(tw, "%s:\t%s\n", r[0], r[1])
	}
	_ = tw.Flush()
}

// ago formats how long ago t was: "never", "12s ago", "5m ago", "3h ago", or "2d ago".
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// bytesText formats a byte count with binary units.
func bytesText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// shellQuote quotes a name for a POSIX shell when it needs it.
func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\"$`\\!*?&;|<>(){}[]#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
