package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/stuffam/drawbridge/internal/control"
	"github.com/stuffam/drawbridge/internal/views"
)

const doctorUsage = `Usage: drawbridge doctor [flags]

Checks the host and its network for the problems that most often stop the VPN from
working: the tunnel, IP forwarding, the uplink, a firewall that drops forwarded traffic,
DNS on the VPN addresses, the endpoint, the clock, disk space, and the web UI's certificate.
Each check that fails or warns says how to fix it. Nothing is changed.

The exit status is 1 if any check failed, and 0 otherwise (warnings don't count).

Flags:
`

const (
	doctorWrap   = 78
	doctorIndent = "      "
)

func doctorCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := newFlagSet("doctor", stderr)
	socket := flags.String("control", defaultControl, "daemon control socket `path`")
	flags.Usage = func() { fmt.Fprint(stderr, doctorUsage); flags.PrintDefaults() }
	pos, err := parseArgs(flags, args)
	if err != nil {
		return flagStatus(err)
	}
	if len(pos) > 0 {
		fmt.Fprintf(stderr, "drawbridge doctor: unexpected argument %q\n", pos[0])
		return 2
	}
	d, err := control.NewClient(*socket).Diagnostics(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "drawbridge:", err)
		return 1
	}
	if printDiagnostics(stdout, d) > 0 {
		return 1
	}
	return 0
}

// printDiagnostics writes the checks and a summary line, and returns how many failed.
func printDiagnostics(w io.Writer, d views.Diagnostics) int {
	var pass, warn, fail, skip int
	for _, c := range d.Checks {
		label := strings.ToUpper(c.Status)
		switch c.Status {
		case "pass":
			pass++
		case "warn":
			warn++
		case "fail":
			fail++
		case "skip":
			skip++
		}
		fmt.Fprintf(w, "%-4s  %s\n", label, c.Name)
		for _, line := range wrapText(c.Detail, doctorWrap-len(doctorIndent)) {
			fmt.Fprintf(w, "%s%s\n", doctorIndent, line)
		}
		if c.Hint != "" && (c.Status == "warn" || c.Status == "fail") {
			fmt.Fprintf(w, "%sFix: %s\n", doctorIndent, c.Hint)
		}
	}
	fmt.Fprintf(w, "\n%d passed, %s, %d failed, %d skipped\n", pass, count(warn, "warning", "warnings"), fail, skip)
	return fail
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// wrapText breaks s at spaces into lines of at most width characters. A word longer than
// the width gets a line to itself.
func wrapText(s string, width int) []string {
	var lines []string
	var line strings.Builder
	for _, word := range strings.Fields(s) {
		if line.Len() > 0 && line.Len()+1+len(word) > width {
			lines = append(lines, line.String())
			line.Reset()
		}
		if line.Len() > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(word)
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
	}
	return lines
}
