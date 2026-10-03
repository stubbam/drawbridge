package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// KindAdGuard is the DNS integration for AdGuard Home, the first one (docs/PLAN.md §6.3).
const KindAdGuard = "adguard"

const dnsIntegrationPurpose = "dns-integration/password"

// DNSIntegration is the saved connection to a DNS resolver on the host.
type DNSIntegration struct {
	// Kind is which resolver: KindAdGuard.
	Kind string
	// BaseURL is the address of its API, in the form adguard.NormalizeBaseURL gives.
	BaseURL string
	// Username is the account's, empty when the resolver has no login.
	Username string
	// Password is the account's, in the clear: the store seals it on the way in and opens it on
	// the way out. Empty when there's none.
	Password string
	// Enabled is the admin's switch for using the connection at all.
	Enabled bool
	// SyncNames is whether Drawbridge writes its clients' names into the resolver.
	SyncNames bool
}

// SyncedClient is what sync last wrote to the resolver for one of Drawbridge's clients.
type SyncedClient struct {
	ClientID string
	// Name is the name it was given there, which isn't the client's current one after a rename
	// that hasn't been synced yet.
	Name string
	// IDs are the addresses it was given.
	IDs []string
}

// DNSIntegration returns the saved connection. ok is false when there isn't one.
func (s *Store) DNSIntegration(ctx context.Context) (in DNSIntegration, ok bool, err error) {
	var sealed []byte
	err = s.db.QueryRowContext(ctx,
		`SELECT kind, base_url, username, password_enc, enabled, sync_names FROM dns_integration WHERE id = 1`).
		Scan(&in.Kind, &in.BaseURL, &in.Username, &sealed, &in.Enabled, &in.SyncNames)
	if errors.Is(err, sql.ErrNoRows) {
		return DNSIntegration{}, false, nil
	}
	if err != nil {
		return DNSIntegration{}, false, err
	}
	if len(sealed) > 0 {
		plain, err := s.sealer.Open(sealed, dnsIntegrationPurpose)
		if err != nil {
			return DNSIntegration{}, false, err
		}
		in.Password = string(plain)
	}
	return in, true, nil
}

// SaveDNSIntegration saves the connection, replacing any other. The record of what sync wrote
// belongs to one resolver, so it goes when the address does.
func (s *Store) SaveDNSIntegration(ctx context.Context, in DNSIntegration) error {
	var sealed any // NULL when there's no password
	if in.Password != "" {
		sealed = s.sealer.Seal([]byte(in.Password), dnsIntegrationPurpose)
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var before string
		err := tx.QueryRowContext(ctx, `SELECT base_url FROM dns_integration WHERE id = 1`).Scan(&before)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if before != in.BaseURL {
			if _, err := tx.ExecContext(ctx, `DELETE FROM dns_integration_clients`); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO dns_integration
				(id, kind, base_url, username, password_enc, enabled, sync_names, updated_at)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, base_url = excluded.base_url,
				username = excluded.username, password_enc = excluded.password_enc,
				enabled = excluded.enabled, sync_names = excluded.sync_names, updated_at = excluded.updated_at`,
			in.Kind, in.BaseURL, in.Username, sealed, in.Enabled, in.SyncNames, s.timestamp())
		return err
	})
}

// DeleteDNSIntegration forgets the connection, the password with it, and the record of what sync
// wrote. It's not an error when there's none.
func (s *Store) DeleteDNSIntegration(ctx context.Context) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM dns_integration_clients`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM dns_integration WHERE id = 1`)
		return err
	})
}

// SyncedClients returns what sync last wrote for each client, by client ID.
func (s *Store) SyncedClients(ctx context.Context) (map[string]SyncedClient, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT client_id, name, ids FROM dns_integration_clients`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SyncedClient{}
	for rows.Next() {
		var c SyncedClient
		var ids string
		if err := rows.Scan(&c.ClientID, &c.Name, &ids); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(ids), &c.IDs); err != nil {
			return nil, err
		}
		out[c.ClientID] = c
	}
	return out, rows.Err()
}

// SaveSyncedClient records what sync wrote for a client.
func (s *Store) SaveSyncedClient(ctx context.Context, c SyncedClient) error {
	ids, err := json.Marshal(append([]string{}, c.IDs...))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO dns_integration_clients (client_id, name, ids) VALUES (?, ?, ?)
		ON CONFLICT (client_id) DO UPDATE SET name = excluded.name, ids = excluded.ids`,
		c.ClientID, c.Name, string(ids))
	return err
}

// DeleteSyncedClient forgets the record for a client. It's not an error when there's none.
func (s *Store) DeleteSyncedClient(ctx context.Context, clientID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dns_integration_clients WHERE client_id = ?`, clientID)
	return err
}
