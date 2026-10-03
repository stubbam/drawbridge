//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/journal"
)

const journalSocket = "/run/systemd/journal/socket"

// journalEntries returns the journal's entries that have field set to value, as journalctl
// prints them with -o json: one object per line.
func journalEntries(t *testing.T, field, value string) []map[string]any {
	t.Helper()
	out, err := exec.Command("journalctl", "--no-pager", "-o", "json", field+"="+value).Output()
	if err != nil {
		t.Fatalf("journalctl: %v", err)
	}
	var entries []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("journalctl printed %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

// TestEventsReachTheJournalWithFields runs the daemon the way systemd runs it (with
// JOURNAL_STREAM set, which makes it log to the journal's native socket) and checks that
// journald, the real one, keeps an event's parts as fields it can filter on. A fake backend is
// enough: this is about logging, not the kernel.
func TestEventsReachTheJournalWithFields(t *testing.T) {
	if _, err := os.Stat(journalSocket); err != nil {
		t.Skipf("no journald here: %v", err)
	}
	if _, err := exec.LookPath("journalctl"); err != nil {
		t.Skip("no journalctl here")
	}
	dir := t.TempDir()
	s := &server{t: t, bin: drawbridgeBinary(t), db: filepath.Join(dir, "drawbridge.db"),
		secret: filepath.Join(dir, "secret.key"), socket: filepath.Join(dir, "control.sock")}
	if err := os.WriteFile(s.secret, bytes.Repeat([]byte{5}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	// Not the default port, in case a real daemon runs on this machine.
	s.daemon = exec.Command(s.bin, "serve", "--backend", "fake", "--db", s.db, "--secret-key", s.secret,
		"--control", s.socket, "--listen", "127.0.0.1:51897", "--tls-dir", filepath.Join(dir, "tls"))
	s.daemon.Env = append(os.Environ(), "JOURNAL_STREAM=1:1")
	s.daemon.Stdout, s.daemon.Stderr = s, s
	if err := s.daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.stopDaemon()
		if t.Failed() {
			s.logMu.Lock()
			t.Logf("daemon output:\n%s", s.daemonLg.String())
			s.logMu.Unlock()
		}
	})
	eventually(t, 10*time.Second, "the daemon to answer on its control socket", func() error {
		_, err := s.cliErr("server", "show")
		return err
	})

	name := fmt.Sprintf("journal-probe-%d", os.Getpid())
	s.cli("client", "add", name)
	s.cli("client", "pause", name)

	var added, paused map[string]any
	eventually(t, 10*time.Second, "the client's events in the journal, filtered by its name", func() error {
		added, paused = nil, nil
		for _, e := range journalEntries(t, "DRAWBRIDGE_CLIENT", name) {
			switch e["DRAWBRIDGE_EVENT"] {
			case "client.added":
				added = e
			case "client.paused":
				paused = e
			}
		}
		if added == nil || paused == nil {
			return fmt.Errorf("added %v, paused %v", added != nil, paused != nil)
		}
		return nil
	})
	for field, want := range map[string]string{
		"PRIORITY":             "6",
		"SYSLOG_IDENTIFIER":    "drawbridge",
		"DRAWBRIDGE_CATEGORY":  "admin",
		"DRAWBRIDGE_VIA":       "cli",
		"DRAWBRIDGE_CLIENT":    name,
		"DRAWBRIDGE_DATA_IPV4": "10.8.0.2",
		"DRAWBRIDGE_EVENT":     "client.added",
	} {
		if got, _ := added[field].(string); got != want {
			t.Errorf("client.added entry: %s = %q, want %q\nentry: %v", field, got, want, added)
		}
	}
	if msg, _ := added["MESSAGE"].(string); !strings.Contains(msg, `msg="client added"`) || !strings.Contains(msg, "client="+name) {
		t.Errorf("MESSAGE = %q, want the line the text log would have had", msg)
	}
	if id, _ := added["DRAWBRIDGE_CLIENT_ID"].(string); id == "" {
		t.Errorf("no DRAWBRIDGE_CLIENT_ID on %v", added)
	}
	// Filtering by two fields is how an admin asks for just one kind of thing.
	var n int
	for _, e := range journalEntries(t, "DRAWBRIDGE_EVENT", "client.paused") {
		if e["DRAWBRIDGE_CLIENT"] == name {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d client.paused entries for %s, want 1", n, name)
	}
}

// TestJournalKeepsAStrangersTextInOneField checks that real journald reads the protocol's
// form for a value with newlines in it the way the handler writes it: a failed login's name is
// whatever a stranger typed, and it must arrive whole and never become fields of its own.
func TestJournalKeepsAStrangersTextInOneField(t *testing.T) {
	if _, err := os.Stat(journalSocket); err != nil {
		t.Skipf("no journald here: %v", err)
	}
	if _, err := exec.LookPath("journalctl"); err != nil {
		t.Skip("no journalctl here")
	}
	h, err := journal.NewHandler(journalSocket, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	probe := fmt.Sprintf("%d", os.Getpid())
	hostile := "x\nPRIORITY=0\nMESSAGE=forged\n_UID=0"
	slog.New(h).Warn("auth login failed", "probe", probe, "actor", hostile)

	var entry map[string]any
	eventually(t, 10*time.Second, "the entry in the journal", func() error {
		entries := journalEntries(t, "DRAWBRIDGE_PROBE", probe)
		if len(entries) != 1 {
			return fmt.Errorf("%d entries", len(entries))
		}
		entry = entries[0]
		return nil
	})
	// journalctl prints a value with unusual characters as an array of bytes.
	var actor string
	switch v := entry["DRAWBRIDGE_ACTOR"].(type) {
	case string:
		actor = v
	case []any:
		for _, b := range v {
			actor += string(rune(b.(float64)))
		}
	}
	if actor != hostile {
		t.Errorf("DRAWBRIDGE_ACTOR = %q, want %q\nentry: %v", actor, hostile, entry)
	}
	if entry["PRIORITY"] != "4" || strings.Contains(fmt.Sprint(entry["MESSAGE"]), "forged\n") || entry["_UID"] != strconv.Itoa(os.Getuid()) {
		t.Errorf("the value reached the entry's own fields: %v", entry)
	}
	if msg, _ := entry["MESSAGE"].(string); !strings.HasPrefix(msg, "level=WARN msg=\"auth login failed\"") {
		t.Errorf("MESSAGE = %q", entry["MESSAGE"])
	}
}
