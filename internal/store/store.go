// Package store keeps Drawbridge's state in SQLite, the source of truth for everything
// the reconciler applies to the kernel (ADR 0003, ADR 0004).
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	_ "modernc.org/sqlite" // The pure-Go SQLite driver (ADR 0003).

	"github.com/stuffam/drawbridge/internal/ipam"
	"github.com/stuffam/drawbridge/internal/keys"
	"github.com/stuffam/drawbridge/internal/model"
)

// DefaultPath is where the packaged daemon keeps its database.
const DefaultPath = "/var/lib/drawbridge/drawbridge.db"

var (
	// ErrNotInitialized means the database has no server settings yet.
	ErrNotInitialized = errors.New("the server isn't initialized yet")
	// ErrNotFound means no client has the given name or ID.
	ErrNotFound = errors.New("no such client")
	// ErrNameTaken means another client already has the name.
	ErrNameTaken = errors.New("a client with that name already exists")
	// ErrHasClients means a change needs the client list to be empty.
	ErrHasClients = errors.New("the VPN subnets can't change while clients exist (re-addressing isn't supported yet)")
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Store is Drawbridge's database. Secrets are sealed before they're written.
type Store struct {
	db     *sql.DB
	sealer *keys.Sealer
	now    func() time.Time
}

// Open opens (creating if needed) the database at path and brings its schema up to date.
func Open(ctx context.Context, path string, sealer *keys.Sealer) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	// Write transactions take the lock up front, so two processes (the tunnel unit and
	// the daemon) wait on busy_timeout instead of failing to upgrade a read lock.
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// One connection serializes access within the process; the load is tiny.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, sealer: sealer, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT    NOT NULL
	)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		version, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s has no version number", base)
		}
		if version <= current {
			continue
		}
		script, err := migrationFiles.ReadFile(name)
		if err != nil {
			return err
		}
		err = s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(script)); err != nil {
				return fmt.Errorf("migration %s: %w", base, err)
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
				version, s.timestamp())
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// timeFormat is how the store writes times: always UTC and always the same width, so
// SQL can compare them as strings. It's a valid RFC 3339 time, so time.RFC3339Nano
// parses it, along with the shorter form M1 wrote.
const timeFormat = "2006-01-02T15:04:05.000000Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func (s *Store) timestamp() string { return formatTime(s.now()) }

const serverKeyPurpose = "server/private-key"

func clientKeyPurpose(id string) string { return "client/" + id + "/private-key" }
func clientPSKPurpose(id string) string { return "client/" + id + "/preshared-key" }

// Initialize creates the server's settings on first use: a new key pair, the default
// IPv4 subnet, and a random unique local IPv6 /64. It reports whether it created them.
func (s *Store) Initialize(ctx context.Context) (bool, error) {
	created := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM server`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		key, err := keys.NewPrivateKey()
		if err != nil {
			return err
		}
		ula, err := ipam.NewULA(nil)
		if err != nil {
			return err
		}
		settings, err := model.NewSettings(key, ula)
		if err != nil {
			return err
		}
		created = true
		return s.writeSettings(ctx, tx, settings, true)
	})
	return created, err
}

// Settings returns the server's settings.
func (s *Store) Settings(ctx context.Context) (model.Settings, error) {
	return s.readSettings(ctx, s.db)
}

// UpdateSettings applies fn to the settings and saves the result if it's valid.
func (s *Store) UpdateSettings(ctx context.Context, fn func(*model.Settings) error) (model.Settings, error) {
	var updated model.Settings
	err := s.tx(ctx, func(tx *sql.Tx) error {
		current, err := s.readSettings(ctx, tx)
		if err != nil {
			return err
		}
		next := current
		next.DNS = append([]netip.Addr(nil), current.DNS...)
		next.ClientAllowedIPs = append([]netip.Prefix(nil), current.ClientAllowedIPs...)
		if err := fn(&next); err != nil {
			return err
		}
		if err := next.Validate(); err != nil {
			return err
		}
		if next.IPv4 != current.IPv4 || next.IPv6 != current.IPv6 {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients`).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrHasClients
			}
		}
		updated = next
		return s.writeSettings(ctx, tx, next, false)
	})
	return updated, err
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Store) readSettings(ctx context.Context, q queryer) (model.Settings, error) {
	var (
		st                          model.Settings
		listenPort, endpointPort    int
		keyEnc                      []byte
		ipv4, ipv6, dns, allowedIPs string
		adminAllowed                string
		isolation                   bool
	)
	err := q.QueryRowContext(ctx, `SELECT iface, listen_port, endpoint_host, endpoint_port,
		private_key_enc, mtu, ipv4_cidr, ipv6_cidr, dns, keepalive, client_isolation,
		client_allowed_ips, admin_allowed FROM server WHERE id = 1`).Scan(
		&st.Interface, &listenPort, &st.EndpointHost, &endpointPort, &keyEnc, &st.MTU,
		&ipv4, &ipv6, &dns, &st.Keepalive, &isolation, &allowedIPs, &adminAllowed)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Settings{}, ErrNotInitialized
	}
	if err != nil {
		return model.Settings{}, err
	}
	st.ListenPort, st.EndpointPort = uint16(listenPort), uint16(endpointPort) //nolint:gosec // G115: validated 0–65535 on write.
	st.ClientIsolation = isolation
	if st.PrivateKey, err = s.sealer.OpenKey(keyEnc, serverKeyPurpose); err != nil {
		return model.Settings{}, err
	}
	if st.IPv4, err = netip.ParsePrefix(ipv4); err != nil {
		return model.Settings{}, fmt.Errorf("stored IPv4 subnet: %w", err)
	}
	if ipv6 != "" {
		if st.IPv6, err = netip.ParsePrefix(ipv6); err != nil {
			return model.Settings{}, fmt.Errorf("stored IPv6 subnet: %w", err)
		}
	}
	if err := json.Unmarshal([]byte(dns), &st.DNS); err != nil {
		return model.Settings{}, fmt.Errorf("stored DNS servers: %w", err)
	}
	if err := json.Unmarshal([]byte(allowedIPs), &st.ClientAllowedIPs); err != nil {
		return model.Settings{}, fmt.Errorf("stored AllowedIPs: %w", err)
	}
	if err := json.Unmarshal([]byte(adminAllowed), &st.AdminAllowed); err != nil {
		return model.Settings{}, fmt.Errorf("stored admin sources: %w", err)
	}
	return st, nil
}

func (s *Store) writeSettings(ctx context.Context, tx *sql.Tx, st model.Settings, insert bool) error {
	dns, err := json.Marshal(st.DNS)
	if err != nil {
		return err
	}
	allowedIPs, err := json.Marshal(st.ClientAllowedIPs)
	if err != nil {
		return err
	}
	adminAllowed, err := json.Marshal(cmpNonNil(st.AdminAllowed))
	if err != nil {
		return err
	}
	ipv6 := ""
	if st.IPv6.IsValid() {
		ipv6 = st.IPv6.String()
	}
	args := []any{st.Interface, int(st.ListenPort), st.EndpointHost, int(st.EndpointPort),
		s.sealer.SealKey(st.PrivateKey, serverKeyPurpose), st.MTU, st.IPv4.String(), ipv6,
		string(dns), st.Keepalive, st.ClientIsolation, string(allowedIPs), string(adminAllowed),
		s.timestamp()}
	if insert {
		_, err = tx.ExecContext(ctx, `INSERT INTO server (id, iface, listen_port, endpoint_host,
			endpoint_port, private_key_enc, mtu, ipv4_cidr, ipv6_cidr, dns, keepalive,
			client_isolation, client_allowed_ips, admin_allowed, updated_at)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE server SET iface = ?, listen_port = ?,
			endpoint_host = ?, endpoint_port = ?, private_key_enc = ?, mtu = ?, ipv4_cidr = ?,
			ipv6_cidr = ?, dns = ?, keepalive = ?, client_isolation = ?, client_allowed_ips = ?,
			admin_allowed = ?, updated_at = ? WHERE id = 1`, args...)
	}
	return err
}

// cmpNonNil returns ps, or an empty list for nil, so JSON encodes "[]" rather than "null".
func cmpNonNil(ps []netip.Prefix) []netip.Prefix {
	if ps == nil {
		return []netip.Prefix{}
	}
	return ps
}

const clientColumns = `id, name, enabled, ipv4, ipv6, public_key, private_key_enc, psk_enc,
	created_at, updated_at`

// Clients returns every client, ordered by IPv4 address.
func (s *Store) Clients(ctx context.Context) ([]model.Client, error) {
	return s.clients(ctx, s.db)
}

func (s *Store) clients(ctx context.Context, q queryer) ([]model.Client, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+clientColumns+` FROM clients`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Client
	for rows.Next() {
		c, err := s.scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IPv4.Less(out[j].IPv4) })
	return out, nil
}

// Ref identifies a client: by ID in the web API, or by name in the CLI.
type Ref struct {
	ID   string
	Name string
}

// ByID refers to the client with the given ID.
func ByID(id string) Ref { return Ref{ID: id} }

// ByName refers to the client with the given name (case-insensitive for ASCII letters).
func ByName(name string) Ref { return Ref{Name: name} }

func (r Ref) String() string {
	if r.ID != "" {
		return "ID " + r.ID
	}
	return strconv.Quote(r.Name)
}

// Client returns one client.
func (s *Store) Client(ctx context.Context, ref Ref) (model.Client, error) {
	return s.client(ctx, s.db, ref)
}

func (s *Store) client(ctx context.Context, q queryer, ref Ref) (model.Client, error) {
	column, value := "name", ref.Name
	if ref.ID != "" {
		column, value = "id", ref.ID
	}
	rows, err := q.QueryContext(ctx, `SELECT `+clientColumns+` FROM clients WHERE `+column+` = ?`, value)
	if err != nil {
		return model.Client{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return model.Client{}, err
		}
		return model.Client{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	return s.scanClient(rows)
}

func (s *Store) scanClient(rows *sql.Rows) (model.Client, error) {
	var (
		c                    model.Client
		ipv4, pub            string
		ipv6                 sql.NullString
		keyEnc, pskEnc       []byte
		createdAt, updatedAt string
	)
	if err := rows.Scan(&c.ID, &c.Name, &c.Enabled, &ipv4, &ipv6, &pub, &keyEnc, &pskEnc,
		&createdAt, &updatedAt); err != nil {
		return model.Client{}, err
	}
	var err error
	if c.IPv4, err = netip.ParseAddr(ipv4); err != nil {
		return model.Client{}, fmt.Errorf("client %s: stored IPv4 address: %w", c.ID, err)
	}
	if ipv6.Valid {
		if c.IPv6, err = netip.ParseAddr(ipv6.String); err != nil {
			return model.Client{}, fmt.Errorf("client %s: stored IPv6 address: %w", c.ID, err)
		}
	}
	if c.PublicKey, err = wgtypes.ParseKey(pub); err != nil {
		return model.Client{}, fmt.Errorf("client %s: stored public key: %w", c.ID, err)
	}
	if keyEnc != nil {
		k, err := s.sealer.OpenKey(keyEnc, clientKeyPurpose(c.ID))
		if err != nil {
			return model.Client{}, fmt.Errorf("client %s: %w", c.ID, err)
		}
		c.PrivateKey = &k
	}
	if pskEnc != nil {
		if c.PresharedKey, err = s.sealer.OpenKey(pskEnc, clientPSKPurpose(c.ID)); err != nil {
			return model.Client{}, fmt.Errorf("client %s: %w", c.ID, err)
		}
	}
	if c.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return model.Client{}, err
	}
	if c.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return model.Client{}, err
	}
	return c, nil
}

// AddClient creates a client with the next free addresses, a new key pair, and a new
// preshared key.
func (s *Store) AddClient(ctx context.Context, name string) (model.Client, error) {
	if err := model.ValidateName(name); err != nil {
		return model.Client{}, err
	}
	var created model.Client
	err := s.tx(ctx, func(tx *sql.Tx) error {
		settings, err := s.readSettings(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := s.client(ctx, tx, ByName(name)); err == nil {
			return fmt.Errorf("%w: %q", ErrNameTaken, name)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		existing, err := s.clients(ctx, tx)
		if err != nil {
			return err
		}
		used := make(map[netip.Addr]bool, len(existing))
		for _, c := range existing {
			used[c.IPv4] = true
		}
		alloc, err := ipam.Next(settings.IPv4, settings.IPv6, used)
		if err != nil {
			return err
		}
		priv, err := keys.NewPrivateKey()
		if err != nil {
			return err
		}
		psk, err := keys.NewPresharedKey()
		if err != nil {
			return err
		}
		id, err := newID()
		if err != nil {
			return err
		}
		now := s.now().UTC()
		created = model.Client{
			ID: id, Name: name, Enabled: true, IPv4: alloc.IPv4, IPv6: alloc.IPv6,
			PublicKey: priv.PublicKey(), PrivateKey: &priv, PresharedKey: psk,
			CreatedAt: now, UpdatedAt: now,
		}
		var ipv6 any
		if alloc.IPv6.IsValid() {
			ipv6 = alloc.IPv6.String()
		}
		ts := formatTime(now)
		_, err = tx.ExecContext(ctx, `INSERT INTO clients (id, name, enabled, ipv4, ipv6,
			public_key, private_key_enc, psk_enc, created_at, updated_at)
			VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?)`,
			id, name, alloc.IPv4.String(), ipv6, created.PublicKey.String(),
			s.sealer.SealKey(priv, clientKeyPurpose(id)), s.sealer.SealKey(psk, clientPSKPurpose(id)),
			ts, ts)
		return err
	})
	return created, err
}

// SetEnabled pauses (false) or resumes (true) a client.
func (s *Store) SetEnabled(ctx context.Context, ref Ref, enabled bool) (model.Client, error) {
	var updated model.Client
	err := s.tx(ctx, func(tx *sql.Tx) error {
		c, err := s.client(ctx, tx, ref)
		if err != nil {
			return err
		}
		c.Enabled = enabled
		c.UpdatedAt = s.now().UTC()
		updated = c
		_, err = tx.ExecContext(ctx, `UPDATE clients SET enabled = ?, updated_at = ? WHERE id = ?`,
			enabled, formatTime(c.UpdatedAt), c.ID)
		return err
	})
	return updated, err
}

// RenameClient renames a client. Only the database has names: they never reach the
// tunnel or the firewall, so a rename needs no reconcile.
func (s *Store) RenameClient(ctx context.Context, ref Ref, name string) (model.Client, error) {
	if err := model.ValidateName(name); err != nil {
		return model.Client{}, err
	}
	var updated model.Client
	err := s.tx(ctx, func(tx *sql.Tx) error {
		c, err := s.client(ctx, tx, ref)
		if err != nil {
			return err
		}
		// A different client with the same name, ignoring case, is a conflict; the
		// same client changing only the case of its name isn't.
		if other, err := s.client(ctx, tx, ByName(name)); err == nil && other.ID != c.ID {
			return fmt.Errorf("%w: %q", ErrNameTaken, name)
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		c.Name = name
		c.UpdatedAt = s.now().UTC()
		updated = c
		_, err = tx.ExecContext(ctx, `UPDATE clients SET name = ?, updated_at = ? WHERE id = ?`,
			name, formatTime(c.UpdatedAt), c.ID)
		return err
	})
	return updated, err
}

// DeleteClient deletes a client.
func (s *Store) DeleteClient(ctx context.Context, ref Ref) (model.Client, error) {
	var deleted model.Client
	err := s.tx(ctx, func(tx *sql.Tx) error {
		c, err := s.client(ctx, tx, ref)
		if err != nil {
			return err
		}
		deleted = c
		_, err = tx.ExecContext(ctx, `DELETE FROM clients WHERE id = ?`, c.ID)
		return err
	})
	return deleted, err
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
