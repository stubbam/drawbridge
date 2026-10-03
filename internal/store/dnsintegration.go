package store

import (
	"context"
	"database/sql"
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
}

// DNSIntegration returns the saved connection. ok is false when there isn't one.
func (s *Store) DNSIntegration(ctx context.Context) (in DNSIntegration, ok bool, err error) {
	var sealed []byte
	err = s.db.QueryRowContext(ctx,
		`SELECT kind, base_url, username, password_enc FROM dns_integration WHERE id = 1`).
		Scan(&in.Kind, &in.BaseURL, &in.Username, &sealed)
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

// SaveDNSIntegration saves the connection, replacing any other.
func (s *Store) SaveDNSIntegration(ctx context.Context, in DNSIntegration) error {
	var sealed any // NULL when there's no password
	if in.Password != "" {
		sealed = s.sealer.Seal([]byte(in.Password), dnsIntegrationPurpose)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO dns_integration (id, kind, base_url, username, password_enc, updated_at)
		VALUES (1, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, base_url = excluded.base_url,
			username = excluded.username, password_enc = excluded.password_enc, updated_at = excluded.updated_at`,
		in.Kind, in.BaseURL, in.Username, sealed, s.timestamp())
	return err
}

// DeleteDNSIntegration forgets the connection, and the password with it. It's not an error when
// there's none.
func (s *Store) DeleteDNSIntegration(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dns_integration WHERE id = 1`)
	return err
}
