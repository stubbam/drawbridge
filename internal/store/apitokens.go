package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrNoToken means there's no such API token, or it was revoked.
	ErrNoToken = errors.New("no such API token")
	// ErrTokenNameTaken means another of the account's API tokens has the name.
	ErrTokenNameTaken = errors.New("a token with that name already exists")
)

// APIToken is a read-only API token (docs/PLAN.md §6.5). The token itself isn't kept, only its
// hash.
type APIToken struct {
	ID     string
	UserID string
	Name   string
	// Prefix is the start of the token, to tell one from another.
	Prefix    string
	TokenHash []byte
	// Scope is "read".
	Scope     string
	CreatedAt time.Time
	// LastUsedAt is zero until the token is first used.
	LastUsedAt time.Time
}

const apiTokenColumns = `id, user_id, name, prefix, token_hash, scope, created_at, last_used_at` //nolint:gosec // G101: column names, not a credential.

func scanAPIToken(row interface{ Scan(...any) error }) (APIToken, error) {
	var (
		t             APIToken
		created, used sql.NullString
	)
	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.TokenHash, &t.Scope, &created, &used); err != nil {
		return APIToken{}, err
	}
	var err error
	if t.CreatedAt, err = time.Parse(time.RFC3339Nano, created.String); err != nil {
		return APIToken{}, err
	}
	if used.Valid {
		if t.LastUsedAt, err = time.Parse(time.RFC3339Nano, used.String); err != nil {
			return APIToken{}, err
		}
	}
	return t, nil
}

// CreateAPIToken stores a new token. It returns ErrTokenNameTaken when the account already has
// one of that name (in any case).
func (s *Store) CreateAPIToken(ctx context.Context, t APIToken) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE user_id = ? AND name = ? COLLATE NOCASE`,
			t.UserID, t.Name).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return ErrTokenNameTaken
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO api_tokens (`+apiTokenColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
			t.ID, t.UserID, t.Name, t.Prefix, t.TokenHash, t.Scope, formatTime(t.CreatedAt))
		return err
	})
}

// CountAPITokens returns how many tokens an account has.
func (s *Store) CountAPITokens(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// APITokenByHash returns the token whose hash this is, or ErrNoToken.
func (s *Store) APITokenByHash(ctx context.Context, hash []byte) (APIToken, error) {
	t, err := scanAPIToken(s.db.QueryRowContext(ctx, `SELECT `+apiTokenColumns+` FROM api_tokens WHERE token_hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrNoToken
	}
	return t, err
}

// APITokens returns an account's tokens, newest first.
func (s *Store) APITokens(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+apiTokenColumns+` FROM api_tokens WHERE user_id = ?
		ORDER BY created_at DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TouchAPIToken records that a token was used.
func (s *Store) TouchAPIToken(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, formatTime(at), id)
	return err
}

// DeleteAPIToken revokes one of an account's tokens, and returns it. It returns ErrNoToken when
// the account has no such token.
func (s *Store) DeleteAPIToken(ctx context.Context, userID, id string) (APIToken, error) {
	var out APIToken
	err := s.tx(ctx, func(tx *sql.Tx) error {
		t, err := scanAPIToken(tx.QueryRowContext(ctx, `SELECT `+apiTokenColumns+` FROM api_tokens
			WHERE id = ? AND user_id = ?`, id, userID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrNoToken, id)
		}
		if err != nil {
			return err
		}
		out = t
		_, err = tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
		return err
	})
	return out, err
}

// DeleteAPITokens revokes all of an account's tokens, and returns how many there were.
func (s *Store) DeleteAPITokens(ctx context.Context, userID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
