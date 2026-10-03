package journal

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// listen is a stand-in for journald: it receives the entries a handler sends.
func listen(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// entry is one journal entry, as journald reads it.
type entry map[string]string

// receive reads one datagram and parses it the way journald does.
func receive(t *testing.T, conn *net.UnixConn) entry {
	t.Helper()
	buf := make([]byte, 1<<20)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("no entry arrived: %v", err)
	}
	e, err := parse(buf[:n])
	if err != nil {
		t.Fatalf("entry %q: %v", buf[:n], err)
	}
	return e
}

// parse reads the native protocol: NAME=value lines, and NAME, a newline, a 64-bit
// little-endian length, and the value for one with a newline in it.
func parse(b []byte) (entry, error) {
	e := entry{}
	for len(b) > 0 {
		nl := bytes.IndexByte(b, '\n')
		if nl < 0 {
			return nil, errors.New("a field without a newline")
		}
		line := b[:nl]
		if name, value, ok := bytes.Cut(line, []byte("=")); ok {
			e[string(name)] = string(value)
			b = b[nl+1:]
			continue
		}
		b = b[nl+1:]
		if len(b) < 8 {
			return nil, errors.New("a binary field without a length")
		}
		size := binary.LittleEndian.Uint64(b)
		b = b[8:]
		if uint64(len(b)) < size+1 || b[size] != '\n' {
			return nil, errors.New("a binary field with the wrong length")
		}
		e[string(line)] = string(b[:size])
		b = b[size+1:]
	}
	return e, nil
}

func newHandler(t *testing.T) (*slog.Logger, *net.UnixConn, *bytes.Buffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.sock")
	conn := listen(t, path)
	var fallback bytes.Buffer
	h, err := NewHandler(path, &fallback)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return slog.New(h), conn, &fallback
}

func TestRecordsBecomeEntriesWithFields(t *testing.T) {
	log, conn, fallback := newHandler(t)

	log.Warn("client paused", "client", "phone", "client_id", "c1", "ipv4", "10.8.0.2")
	e := receive(t, conn)
	want := entry{
		"PRIORITY":             "4",
		"SYSLOG_IDENTIFIER":    "drawbridge",
		"DRAWBRIDGE_CLIENT":    "phone",
		"DRAWBRIDGE_CLIENT_ID": "c1",
		"DRAWBRIDGE_IPV4":      "10.8.0.2",
	}
	for name, value := range want {
		if e[name] != value {
			t.Errorf("%s = %q, want %q", name, e[name], value)
		}
	}
	// The message reads as the text handler's line did: no time, since journald has its own,
	// and every attribute, so the default `journalctl` output still shows them.
	if got, want := e["MESSAGE"], `level=WARN msg="client paused" client=phone client_id=c1 ipv4=10.8.0.2`; got != want {
		t.Errorf("MESSAGE = %q, want %q", got, want)
	}
	if fallback.Len() != 0 {
		t.Errorf("the fallback got %q although journald was listening", fallback.String())
	}
}

func TestLevelsAreJournaldPriorities(t *testing.T) {
	log, conn, _ := newHandler(t)
	ctx := context.Background()
	for level, want := range map[slog.Level]string{
		slog.LevelDebug: "7", slog.LevelInfo: "6", slog.LevelWarn: "4", slog.LevelError: "3", slog.LevelError + 4: "3",
	} {
		log.Log(ctx, level, "x")
		if got := receive(t, conn)["PRIORITY"]; got != want {
			t.Errorf("%v: PRIORITY %s, want %s", level, got, want)
		}
	}
}

func TestGroupsAndWithAttrs(t *testing.T) {
	log, conn, _ := newHandler(t)

	log.With("via", "web").WithGroup("data").With("from", "phone").Info("renamed", "to", "tablet", slog.Group("net", "ipv4", "10.8.0.2"))
	e := receive(t, conn)
	for name, value := range map[string]string{
		"DRAWBRIDGE_VIA":           "web",
		"DRAWBRIDGE_DATA_FROM":     "phone",
		"DRAWBRIDGE_DATA_TO":       "tablet",
		"DRAWBRIDGE_DATA_NET_IPV4": "10.8.0.2",
	} {
		if e[name] != value {
			t.Errorf("%s = %q, want %q", name, e[name], value)
		}
	}
	if want := `level=INFO msg=renamed via=web data.from=phone data.to=tablet data.net.ipv4=10.8.0.2`; e["MESSAGE"] != want {
		t.Errorf("MESSAGE = %q, want %q", e["MESSAGE"], want)
	}
}

func TestFieldNamesAreSafe(t *testing.T) {
	log, conn, _ := newHandler(t)

	// A key can't claim a name that's the journal's own, or one it would drop.
	log.Info("x", "MESSAGE", "forged", "_PID", "1", "client-name", "a", "ünï", "b", "9lives", "c", "", "skipped")
	e := receive(t, conn)
	for name := range e {
		if name != "MESSAGE" && name != "PRIORITY" && name != "SYSLOG_IDENTIFIER" && !strings.HasPrefix(name, "DRAWBRIDGE_") {
			t.Errorf("field %q isn't one of ours", name)
		}
		for _, r := range name {
			if r != '_' && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
				t.Errorf("field name %q has %q in it", name, r)
			}
		}
	}
	if e["DRAWBRIDGE_MESSAGE"] != "forged" || e["DRAWBRIDGE__PID"] != "1" || e["DRAWBRIDGE_CLIENT_NAME"] != "a" {
		t.Errorf("entry %v: want the keys under DRAWBRIDGE_", e)
	}
	if strings.Contains(e["MESSAGE"], "forged") && !strings.HasPrefix(e["MESSAGE"], "level=INFO msg=x") {
		t.Errorf("MESSAGE = %q", e["MESSAGE"])
	}
	if n := len(fieldName([]string{strings.Repeat("a", 100)})); n != maxName {
		t.Errorf("a long key made a %d-character name, want %d", n, maxName)
	}
}

func TestValuesWithNewlinesSurvive(t *testing.T) {
	log, conn, _ := newHandler(t)

	// A failed login's name is whatever a stranger typed, newlines included. It can't become a
	// second field, and it arrives whole.
	hostile := "x\nMESSAGE=forged\nPRIORITY=0"
	log.Warn("failed login", "actor", hostile)
	e := receive(t, conn)
	if e["DRAWBRIDGE_ACTOR"] != hostile {
		t.Errorf("DRAWBRIDGE_ACTOR = %q, want %q", e["DRAWBRIDGE_ACTOR"], hostile)
	}
	if e["PRIORITY"] != "4" || strings.Contains(e["MESSAGE"], "\n") {
		t.Errorf("entry %v: the value reached the entry's own fields", e)
	}
}

func TestLongValuesAreCut(t *testing.T) {
	log, conn, _ := newHandler(t)

	log.Info(strings.Repeat("m", 3*maxMessage), "big", strings.Repeat("é", maxValue))
	e := receive(t, conn)
	if len(e["MESSAGE"]) > maxMessage || len(e["DRAWBRIDGE_BIG"]) > maxValue {
		t.Fatalf("lengths %d and %d, want at most %d and %d", len(e["MESSAGE"]), len(e["DRAWBRIDGE_BIG"]), maxMessage, maxValue)
	}
	if v := e["DRAWBRIDGE_BIG"]; v != strings.Repeat("é", len(v)/2) {
		t.Errorf("the cut split a character")
	}
}

func TestFallsBackToTextWhenJournaldIsGone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.sock")
	conn := listen(t, path)
	var fallback bytes.Buffer
	h, err := NewHandler(path, &fallback)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	log := slog.New(h)

	log.Info("one")
	receive(t, conn)

	// Journald stops: the record isn't lost, it goes to the fallback.
	_ = conn.Close()
	log.Info("two", "client", "phone")
	if got, want := fallback.String(), "level=INFO msg=two client=phone\n"; got != want {
		t.Fatalf("fallback = %q, want %q", got, want)
	}

	// Journald comes back as a new socket at the same path (a restart), and the handler
	// connects to it again.
	_ = os.Remove(path)
	conn = listen(t, path)
	fallback.Reset()
	log.Info("three")
	if e := receive(t, conn); !strings.Contains(e["MESSAGE"], "msg=three") {
		t.Errorf("MESSAGE = %q after the restart", e["MESSAGE"])
	}
	if fallback.Len() != 0 {
		t.Errorf("the fallback got %q after journald returned", fallback.String())
	}
}

func TestNewHandlerFailsWithoutJournald(t *testing.T) {
	if _, err := NewHandler(filepath.Join(t.TempDir(), "missing.sock"), &bytes.Buffer{}); err == nil {
		t.Fatal("NewHandler with nothing listening succeeded")
	}
}

func TestEncode(t *testing.T) {
	got := encode([]field{{"A", "1"}, {"B", "x\ny"}, {"C", ""}})
	want := "A=1\nB\n\x03\x00\x00\x00\x00\x00\x00\x00x\ny\nC=\n"
	if string(got) != want {
		t.Fatalf("encode = %q, want %q", got, want)
	}
}
