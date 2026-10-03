// Package journal logs to systemd-journald with its native protocol, so that a log record's
// attributes become journald fields: `journalctl -u drawbridge DRAWBRIDGE_CLIENT=phone` finds
// one client's records, and `-p warning` filters by priority. Lines written to standard error
// can't carry either; journald keeps only their text.
package journal

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Socket is where journald listens for native messages.
const Socket = "/run/systemd/journal/socket"

// fieldPrefix starts the name of every field an attribute becomes. Journald field names are
// capitals, digits, and underscores, and one that starts with an underscore is the journal's
// own (it ignores it from a service). The prefix keeps a key from claiming a name like
// MESSAGE or _PID, and it namespaces what a record carries.
const fieldPrefix = "DRAWBRIDGE_"

const (
	// maxMessage and maxValue keep a datagram well inside what a Unix socket carries.
	maxMessage = 16 << 10
	maxValue   = 4 << 10
	maxName    = 64
	// writeTimeout is how long a record waits for journald to take it, so a stalled journal
	// can't stall the daemon.
	writeTimeout = 2 * time.Second
)

// field is one NAME=value pair of a journal entry.
type field struct{ name, value string }

// Handler is a slog.Handler that sends each record to journald. The entry's MESSAGE is the
// line slog's text handler would write, without a time (journald adds its own), so the
// journal reads as it did before. PRIORITY follows the record's level, and each attribute
// is also a field named DRAWBRIDGE_ and the attribute's key in capitals, with a group's name
// before it: client=phone is DRAWBRIDGE_CLIENT=phone, and data.ipv4 is DRAWBRIDGE_DATA_IPV4.
//
// When journald can't be reached, the line goes to the fallback writer instead, so a record
// is never lost to the journal being restarted.
type Handler struct {
	s      *shared
	text   slog.Handler
	fields []field // attributes from WithAttrs, flattened
	groups []string
}

// shared is what a Handler and the handlers derived from it have in common: the connection
// and the buffer that the text handlers write a record's line into.
type shared struct {
	socket   string
	fallback io.Writer

	mu   sync.Mutex // guards conn and line
	conn *net.UnixConn
	line bytes.Buffer
}

// Write collects the text handler's output. It runs inside Handle, with mu held.
func (s *shared) Write(p []byte) (int, error) { return s.line.Write(p) }

// NewHandler connects to the journal socket and returns a handler that sends to it. It fails
// if nothing is listening, so a caller can log some other way.
func NewHandler(socket string, fallback io.Writer) (*Handler, error) {
	s := &shared{socket: socket, fallback: fallback}
	conn, err := dial(socket)
	if err != nil {
		return nil, err
	}
	s.conn = conn
	opts := &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}
	return &Handler{s: s, text: slog.NewTextHandler(s, opts)}, nil
}

func dial(socket string) (*net.UnixConn, error) {
	return net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
}

// Close closes the connection to journald.
func (h *Handler) Close() error {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if h.s.conn == nil {
		return nil
	}
	err := h.s.conn.Close()
	h.s.conn = nil
	return err
}

// Enabled reports that every level is logged.
func (h *Handler) Enabled(context.Context, slog.Level) bool { return true }

// WithAttrs returns a handler whose records carry attrs.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	d := *h
	d.text = h.text.WithAttrs(attrs)
	d.fields = slices.Clone(h.fields)
	for _, a := range attrs {
		flatten(h.groups, a, &d.fields)
	}
	return &d
}

// WithGroup returns a handler that puts the attributes after it in a group.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	d := *h
	d.text = h.text.WithGroup(name)
	d.groups = append(slices.Clone(h.groups), name)
	return &d
}

// Handle sends the record to journald.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	h.s.line.Reset()
	if err := h.text.Handle(ctx, r); err != nil {
		return err
	}
	line := strings.TrimRight(h.s.line.String(), "\n")

	fields := []field{
		{"MESSAGE", truncate(line, maxMessage)},
		{"PRIORITY", priority(r.Level)},
		{"SYSLOG_IDENTIFIER", "drawbridge"},
	}
	fields = append(fields, h.fields...)
	r.Attrs(func(a slog.Attr) bool {
		flatten(h.groups, a, &fields)
		return true
	})
	if err := h.send(encode(fields)); err != nil {
		_, _ = io.WriteString(h.s.fallback, line+"\n")
	}
	return nil
}

// send writes one entry. If journald was restarted, its socket is a new file and the old
// connection is dead, so a failed write connects again once.
func (h *Handler) send(entry []byte) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if h.s.conn == nil {
			if h.s.conn, err = dial(h.s.socket); err != nil {
				return err
			}
		}
		_ = h.s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err = h.s.conn.Write(entry); err == nil {
			return nil
		}
		_ = h.s.conn.Close()
		h.s.conn = nil
	}
	return err
}

func priority(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "3" // err
	case l >= slog.LevelWarn:
		return "4" // warning
	case l >= slog.LevelInfo:
		return "6" // info
	default:
		return "7" // debug
	}
}

// flatten adds an attribute, and the attributes inside a group, as fields. groups are the
// names of the groups around it.
func flatten(groups []string, a slog.Attr, out *[]field) {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		inner := groups
		if a.Key != "" {
			inner = append(slices.Clone(groups), a.Key)
		}
		for _, ga := range v.Group() {
			flatten(inner, ga, out)
		}
		return
	}
	if a.Key == "" {
		return
	}
	*out = append(*out, field{fieldName(append(slices.Clone(groups), a.Key)), truncate(v.String(), maxValue)})
}

// fieldName is a journald field name for an attribute: DRAWBRIDGE_, then the group names and
// key joined with underscores, in capitals, with anything that isn't a capital or a digit
// turned into an underscore.
func fieldName(parts []string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, strings.Join(parts, "_"))
	return truncate(fieldPrefix+name, maxName)
}

// truncate cuts s to at most n bytes, without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// encode writes fields in the native protocol: NAME=value and a newline. A value that has a
// newline in it can't be written that way, so it's NAME, a newline, the value's length as 64
// bits in little-endian, the value, and a newline.
func encode(fields []field) []byte {
	var b bytes.Buffer
	for _, f := range fields {
		if strings.Contains(f.value, "\n") {
			b.WriteString(f.name)
			b.WriteByte('\n')
			_ = binary.Write(&b, binary.LittleEndian, uint64(len(f.value)))
			b.WriteString(f.value)
		} else {
			b.WriteString(f.name)
			b.WriteByte('=')
			b.WriteString(f.value)
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}
