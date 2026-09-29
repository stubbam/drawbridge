package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrSetupDone means the admin account already exists, so there's nothing to set up.
	ErrSetupDone = errors.New("setup is already complete")
	// ErrBadSetupToken means the setup token doesn't match.
	ErrBadSetupToken = errors.New("the setup token is wrong")
	// ErrUserExists means an admin account already exists; Drawbridge has one (D11).
	ErrUserExists = errors.New("the admin account already exists")
	// ErrNoUser means there's no such account.
	ErrNoUser = errors.New("no such account")
	// ErrNoSession means there's no such session, or it has ended.
	ErrNoSession = errors.New("no such session")
)

const setupTokenPurpose = "setup-token"

// User is an admin account.
type User struct {
	ID                string
	Username          string
	PasswordHash      string
	CreatedAt         time.Time
	PasswordChangedAt time.Time
	// LastLoginAt is zero if the account has never logged in.
	LastLoginAt time.Time
}

// Session is a logged-in browser.
type Session struct {
	// ID is public: the API shows it so a session can be revoked.
	ID string
	// TokenHash is the SHA-256 hash of the cookie's token.
	TokenHash  []byte
	UserID     string
	CreatedAt  time.Time
	LastSeenAt time.Time
	// ExpiresAt is the absolute limit; the idle limit is applied to LastSeenAt.
	ExpiresAt time.Time
	IP        string
	UserAgent string
}

// HasUsers reports whether the admin account exists.
func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	return s.hasUsers(ctx, s.db)
}

func (s *Store) hasUsers(ctx context.Context, q queryer) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n > 0, err
}

// EnsureSetupToken returns the pending setup token. If there's none yet, it stores
// candidate and returns that. Once the admin account exists, it returns ErrSetupDone.
func (s *Store) EnsureSetupToken(ctx context.Context, candidate string) (string, error) {
	var token string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if has, err := s.hasUsers(ctx, tx); err != nil {
			return err
		} else if has {
			return ErrSetupDone
		}
		var sealed []byte
		err := tx.QueryRowContext(ctx, `SELECT token_enc FROM setup_token WHERE id = 1`).Scan(&sealed)
		if err == nil {
			plain, err := s.sealer.Open(sealed, setupTokenPurpose)
			token = string(plain)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		token = candidate
		_, err = tx.ExecContext(ctx, `INSERT INTO setup_token (id, token_enc, created_at) VALUES (1, ?, ?)`,
			s.sealer.Seal([]byte(candidate), setupTokenPurpose), s.timestamp())
		return err
	})
	return token, err
}

// CompleteSetup creates the admin account if token matches the setup token, and then
// deletes the token.
func (s *Store) CompleteSetup(ctx context.Context, token, username, passwordHash string) (User, error) {
	var u User
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if has, err := s.hasUsers(ctx, tx); err != nil {
			return err
		} else if has {
			return ErrSetupDone
		}
		var sealed []byte
		err := tx.QueryRowContext(ctx, `SELECT token_enc FROM setup_token WHERE id = 1`).Scan(&sealed)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrBadSetupToken
		}
		if err != nil {
			return err
		}
		want, err := s.sealer.Open(sealed, setupTokenPurpose)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(want, []byte(token)) != 1 {
			return ErrBadSetupToken
		}
		u, err = s.insertUser(ctx, tx, username, passwordHash)
		return err
	})
	return u, err
}

// CreateUser creates the admin account without a setup token: the CLI's recovery path,
// where access to the control socket is the authority.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string) (User, error) {
	var u User
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if has, err := s.hasUsers(ctx, tx); err != nil {
			return err
		} else if has {
			return ErrUserExists
		}
		var err error
		u, err = s.insertUser(ctx, tx, username, passwordHash)
		return err
	})
	return u, err
}

func (s *Store) insertUser(ctx context.Context, tx *sql.Tx, username, passwordHash string) (User, error) {
	id, err := newID()
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC()
	u := User{ID: id, Username: username, PasswordHash: passwordHash, CreatedAt: now, PasswordChangedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, created_at,
		password_changed_at) VALUES (?, ?, ?, ?, ?)`,
		id, username, passwordHash, formatTime(now), formatTime(now)); err != nil {
		return User{}, err
	}
	// The token has done its job.
	_, err = tx.ExecContext(ctx, `DELETE FROM setup_token`)
	return u, err
}

const userColumns = `id, username, password_hash, created_at, password_changed_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var (
		u                  User
		created, changed   string
		lastLogin          sql.NullString
		errCreated, errChg error
	)
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &created, &changed, &lastLogin); err != nil {
		return User{}, err
	}
	u.CreatedAt, errCreated = time.Parse(time.RFC3339Nano, created)
	u.PasswordChangedAt, errChg = time.Parse(time.RFC3339Nano, changed)
	if err := errors.Join(errCreated, errChg); err != nil {
		return User{}, err
	}
	if lastLogin.Valid {
		t, err := time.Parse(time.RFC3339Nano, lastLogin.String)
		if err != nil {
			return User{}, err
		}
		u.LastLoginAt = t
	}
	return u, nil
}

// UserByName returns the account with the given username (case-insensitive for ASCII
// letters).
func (s *Store) UserByName(ctx context.Context, username string) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoUser
	}
	return u, err
}

// OnlyUser returns the admin account.
func (s *Store) OnlyUser(ctx context.Context) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY created_at LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoUser
	}
	return u, err
}

// RecordLogin sets the account's last login time.
func (s *Store) RecordLogin(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, s.timestamp(), userID)
	return err
}

// SetPassword changes an account's password hash and ends all of its sessions except
// keep ("" to end them all).
func (s *Store) SetPassword(ctx context.Context, userID, passwordHash, keep string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?`,
			passwordHash, s.timestamp(), userID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNoUser
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = ? AND id != ?`, userID, keep)
		return err
	})
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions (id, token_hash, user_id, created_at,
		last_seen_at, expires_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.TokenHash, sess.UserID, formatTime(sess.CreatedAt), formatTime(sess.LastSeenAt),
		formatTime(sess.ExpiresAt), sess.IP, sess.UserAgent)
	return err
}

const sessionColumns = `s.id, s.token_hash, s.user_id, s.created_at, s.last_seen_at, s.expires_at,
	s.ip, s.user_agent`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var (
		sess                   Session
		created, seen, expires string
		errC, errS, errE       error
	)
	if err := row.Scan(&sess.ID, &sess.TokenHash, &sess.UserID, &created, &seen, &expires,
		&sess.IP, &sess.UserAgent); err != nil {
		return Session{}, err
	}
	sess.CreatedAt, errC = time.Parse(time.RFC3339Nano, created)
	sess.LastSeenAt, errS = time.Parse(time.RFC3339Nano, seen)
	sess.ExpiresAt, errE = time.Parse(time.RFC3339Nano, expires)
	return sess, errors.Join(errC, errS, errE)
}

// SessionByToken returns the session whose token has the given hash, and its account.
// It doesn't check expiry; the caller applies the idle and absolute limits.
func (s *Store) SessionByToken(ctx context.Context, tokenHash []byte) (Session, User, error) {
	sess, err := scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+`
		FROM auth_sessions s WHERE s.token_hash = ?`, tokenHash))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrNoSession
	}
	if err != nil {
		return Session{}, User{}, err
	}
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, sess.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrNoSession
	}
	return sess, u, err
}

// TouchSession records that a session was used.
func (s *Store) TouchSession(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE auth_sessions SET last_seen_at = ? WHERE id = ?`, formatTime(at), id)
	return err
}

// Sessions returns an account's sessions, newest first.
func (s *Store) Sessions(ctx context.Context, userID string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionColumns+` FROM auth_sessions s
		WHERE s.user_id = ? ORDER BY s.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// DeleteSession ends one of an account's sessions.
func (s *Store) DeleteSession(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%w: %s", ErrNoSession, id)
	}
	return nil
}

// PruneSessions deletes sessions that have passed their absolute limit, or have been
// idle since before idleSince.
func (s *Store) PruneSessions(ctx context.Context, now, idleSince time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at <= ? OR last_seen_at < ?`,
		formatTime(now), formatTime(idleSince))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
