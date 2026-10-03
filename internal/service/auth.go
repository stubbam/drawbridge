package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/stuffam/drawbridge/internal/auth"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
)

// SessionPolicy limits how long a login lasts.
type SessionPolicy struct {
	// Idle ends a session that hasn't been used for this long.
	Idle time.Duration
	// Absolute ends every session this long after it started.
	Absolute time.Duration
}

// DefaultSessionPolicy is an hour idle, and twelve hours at most.
var DefaultSessionPolicy = SessionPolicy{Idle: time.Hour, Absolute: 12 * time.Hour}

// touchEvery is how often a session's last use is written: often enough for the idle
// limit, rarely enough to spare the SD card.
const touchEvery = 5 * time.Minute

var (
	// ErrBadLogin means the username or the password is wrong. It doesn't say which.
	ErrBadLogin = errors.New("wrong username or password")
	// ErrWrongPassword means the current password given for a change is wrong.
	ErrWrongPassword = errors.New("the current password is wrong")
)

// RateLimitedError means there were too many failed attempts.
type RateLimitedError struct {
	Wait time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("too many failed attempts; try again in %s", e.Wait.Round(time.Second))
}

// Login is a new session. Token goes in the browser's cookie; the database has only its
// hash.
type Login struct {
	Token   string
	Session store.Session
	User    store.User
}

func (s *Service) policy() SessionPolicy {
	p := s.Sessions
	if p.Idle <= 0 {
		p.Idle = DefaultSessionPolicy.Idle
	}
	if p.Absolute <= 0 {
		p.Absolute = DefaultSessionPolicy.Absolute
	}
	return p
}

// sourceKey is the rate limiter's key for the request's source address.
func sourceKey(ctx context.Context) string {
	addr, err := netip.ParseAddr(ActorFrom(ctx).SourceIP)
	if err != nil {
		return "ip:unknown"
	}
	return auth.SourceKey(addr)
}

// truncate shortens text from a request (a username that failed to log in, a user
// agent) before it's stored.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// SetupNeeded reports whether the admin account still has to be created.
func (s *Service) SetupNeeded(ctx context.Context) (bool, error) {
	has, err := s.Store.HasUsers(ctx)
	return !has, err
}

// SetupToken returns the first-run setup token, creating it if needed, formatted for
// people. It returns store.ErrSetupDone once the admin account exists.
func (s *Service) SetupToken(ctx context.Context) (string, error) {
	candidate, err := auth.NewSetupToken()
	if err != nil {
		return "", err
	}
	token, err := s.Store.EnsureSetupToken(ctx, candidate)
	if err != nil {
		return "", err
	}
	return auth.FormatSetupToken(token), nil
}

// CompleteSetup creates the admin account with the setup token, and logs it in.
func (s *Service) CompleteSetup(ctx context.Context, token, username, password, userAgent string) (Login, error) {
	src := sourceKey(ctx)
	if wait := s.Limiter.Wait(src); wait > 0 {
		return Login{}, &RateLimitedError{Wait: wait}
	}
	if err := errors.Join(auth.ValidateUsername(username), auth.ValidatePassword(password)); err != nil {
		return Login{}, &model.InvalidError{Err: err}
	}
	hash, err := s.Hasher.Hash(ctx, password)
	if err != nil {
		return Login{}, err
	}
	u, err := s.Store.CompleteSetup(ctx, auth.NormalizeSetupToken(token), username, hash)
	if errors.Is(err, store.ErrBadSetupToken) {
		s.Limiter.Fail(src)
		s.record(ctx, Event{Kind: "auth.setup_failed", Actor: truncate(username, 64)})
	}
	if err != nil {
		return Login{}, err
	}
	s.Limiter.Succeed(src)
	s.record(ctx, Event{Kind: "auth.setup_completed", Actor: u.Username})
	return s.startSession(ctx, u, userAgent)
}

// Login checks a username and password and starts a session.
func (s *Service) Login(ctx context.Context, username, password, userAgent string) (Login, error) {
	keys := []string{sourceKey(ctx)}
	u, err := s.Store.UserByName(ctx, username)
	found := err == nil
	if err != nil && !errors.Is(err, store.ErrNoUser) {
		return Login{}, err
	}
	if found {
		keys = append(keys, auth.AccountKey(u.ID))
	}
	if wait := s.Limiter.Wait(keys...); wait > 0 {
		return Login{}, &RateLimitedError{Wait: wait}
	}

	ok := false
	if found {
		if ok, err = s.Hasher.Verify(ctx, u.PasswordHash, password); err != nil {
			return Login{}, err
		}
	} else {
		s.Hasher.VerifyNothing(ctx, password)
	}
	if !ok {
		s.Limiter.Fail(keys...)
		s.record(ctx, Event{Kind: "auth.login_failed", Actor: truncate(username, 64)})
		return Login{}, ErrBadLogin
	}
	s.Limiter.Succeed(keys...)
	if err := s.Store.RecordLogin(ctx, u.ID); err != nil {
		return Login{}, err
	}
	s.record(ctx, Event{Kind: "auth.login", Actor: u.Username})
	return s.startSession(ctx, u, userAgent)
}

func (s *Service) startSession(ctx context.Context, u store.User, userAgent string) (Login, error) {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return Login{}, err
	}
	id, err := auth.NewID()
	if err != nil {
		return Login{}, err
	}
	now := s.now().UTC()
	sess := store.Session{
		ID:         id,
		TokenHash:  hash,
		UserID:     u.ID,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(s.policy().Absolute),
		IP:         ActorFrom(ctx).SourceIP,
		UserAgent:  truncate(userAgent, 256),
	}
	if err := s.Store.CreateSession(ctx, sess); err != nil {
		return Login{}, err
	}
	return Login{Token: token, Session: sess, User: u}, nil
}

// Authenticate returns the session for a cookie's token, and its account. It returns
// store.ErrNoSession if there's no such session or it has expired.
func (s *Service) Authenticate(ctx context.Context, token string) (store.Session, store.User, error) {
	if token == "" {
		return store.Session{}, store.User{}, store.ErrNoSession
	}
	sess, u, err := s.Store.SessionByToken(ctx, auth.HashToken(token))
	if err != nil {
		return store.Session{}, store.User{}, err
	}
	now := s.now()
	if !now.Before(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) >= s.policy().Idle {
		if err := s.Store.DeleteSession(ctx, u.ID, sess.ID); err != nil && !errors.Is(err, store.ErrNoSession) {
			s.Log.Warn("can't delete an expired session", "err", err)
		}
		return store.Session{}, store.User{}, store.ErrNoSession
	}
	if now.Sub(sess.LastSeenAt) >= touchEvery {
		if err := s.Store.TouchSession(ctx, sess.ID, now); err != nil {
			s.Log.Warn("can't record a session's use", "err", err)
		}
		sess.LastSeenAt = now
	}
	return sess, u, nil
}

// Logout ends a session.
func (s *Service) Logout(ctx context.Context, sess store.Session) error {
	if err := s.Store.DeleteSession(ctx, sess.UserID, sess.ID); err != nil && !errors.Is(err, store.ErrNoSession) {
		return err
	}
	s.record(ctx, Event{Kind: "auth.logout"})
	return nil
}

// ChangePassword changes the logged-in account's password and ends its other sessions.
func (s *Service) ChangePassword(ctx context.Context, u store.User, current store.Session, oldPassword, newPassword string) error {
	keys := []string{sourceKey(ctx), auth.AccountKey(u.ID)}
	if wait := s.Limiter.Wait(keys...); wait > 0 {
		return &RateLimitedError{Wait: wait}
	}
	if err := auth.ValidatePassword(newPassword); err != nil {
		return err
	}
	ok, err := s.Hasher.Verify(ctx, u.PasswordHash, oldPassword)
	if err != nil {
		return err
	}
	if !ok {
		s.Limiter.Fail(keys...)
		return &model.InvalidError{Err: ErrWrongPassword}
	}
	hash, err := s.Hasher.Hash(ctx, newPassword)
	if err != nil {
		return err
	}
	if err := s.Store.SetPassword(ctx, u.ID, hash, current.ID); err != nil {
		return err
	}
	s.record(ctx, Event{Kind: "auth.password_changed"})
	return nil
}

// ListSessions returns an account's active sessions, newest first.
func (s *Service) ListSessions(ctx context.Context, userID string) ([]store.Session, error) {
	all, err := s.Store.Sessions(ctx, userID)
	if err != nil {
		return nil, err
	}
	now, idle := s.now(), s.policy().Idle
	out := all[:0]
	for _, sess := range all {
		if now.Before(sess.ExpiresAt) && now.Sub(sess.LastSeenAt) < idle {
			out = append(out, sess)
		}
	}
	return out, nil
}

// RevokeSession ends one of an account's sessions.
func (s *Service) RevokeSession(ctx context.Context, userID, id string) error {
	if err := s.Store.DeleteSession(ctx, userID, id); err != nil {
		return err
	}
	s.record(ctx, Event{Kind: "auth.session_revoked", Data: map[string]string{"session": id}})
	return nil
}

// CreateAdmin creates the admin account with a random password, which it returns. It's
// the CLI's alternative to first-run setup in the browser.
func (s *Service) CreateAdmin(ctx context.Context, username string) (string, error) {
	if err := auth.ValidateUsername(username); err != nil {
		return "", err
	}
	password, err := auth.RandomPassword()
	if err != nil {
		return "", err
	}
	hash, err := s.Hasher.Hash(ctx, password)
	if err != nil {
		return "", err
	}
	if _, err := s.Store.CreateUser(ctx, username, hash); err != nil {
		return "", err
	}
	s.record(ctx, Event{Kind: "auth.admin_created", Data: map[string]string{"username": username}})
	return password, nil
}

// ResetPassword gives the admin account (the named one, or the only one if username is
// empty) a new random password, ends its sessions, and lifts any lockout. It returns
// the account and the password.
func (s *Service) ResetPassword(ctx context.Context, username string) (store.User, string, error) {
	var (
		u   store.User
		err error
	)
	if username == "" {
		u, err = s.Store.OnlyUser(ctx)
	} else {
		u, err = s.Store.UserByName(ctx, username)
	}
	if err != nil {
		return store.User{}, "", err
	}
	password, err := auth.RandomPassword()
	if err != nil {
		return store.User{}, "", err
	}
	hash, err := s.Hasher.Hash(ctx, password)
	if err != nil {
		return store.User{}, "", err
	}
	if err := s.Store.SetPassword(ctx, u.ID, hash, ""); err != nil {
		return store.User{}, "", err
	}
	s.Limiter.Succeed(auth.AccountKey(u.ID))
	s.record(ctx, Event{Kind: "auth.password_reset", Data: map[string]string{"username": u.Username}})
	return u, password, nil
}

// PruneSessions deletes expired sessions. The daemon runs it with the drift check.
func (s *Service) PruneSessions(ctx context.Context) {
	now := s.now()
	if _, err := s.Store.PruneSessions(ctx, now, now.Add(-s.policy().Idle)); err != nil {
		s.Log.Warn("can't prune expired sessions", "err", err)
	}
}
