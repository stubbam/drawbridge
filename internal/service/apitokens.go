package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/stuffam/drawbridge/internal/auth"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
)

// Read-only API tokens (docs/PLAN.md §6.5), for a dashboard such as Homepage that can't log in.
// What a token may read is the API's business (internal/api, tokenReadable). Here: making them,
// listing them, revoking them, and telling who is knocking.

const (
	// MaxAPITokens is the most tokens an account can have. A household has a few dashboards.
	MaxAPITokens = 20
	// MaxTokenNameLength is the longest token name, in characters.
	MaxTokenNameLength = 64

	// apiTokenTouchEvery is how often a token's last use is written. It's only for the admin to
	// see which tokens are still in use, and a dashboard asks every few seconds, so a write per
	// request would wear an SD card (CLAUDE.md, "Protect the SD card").
	apiTokenTouchEvery = time.Hour
)

// ErrTooManyTokens means the account already has MaxAPITokens tokens.
var ErrTooManyTokens = fmt.Errorf("an account can have at most %d API tokens; revoke one first", MaxAPITokens)

// CreateAPIToken makes a read-only token for the account, and returns it with its secret, which
// is shown once and kept nowhere. It takes the password again, even for a logged-in session: a
// token outlives the session and a password change, so a hijacked session mustn't be able to
// mint one. The attempts count against the same limits as a login.
func (s *Service) CreateAPIToken(ctx context.Context, u store.User, password, name string) (store.APIToken, string, error) {
	keys := []string{sourceKey(ctx), auth.AccountKey(u.ID)}
	if wait := s.Limiter.Wait(keys...); wait > 0 {
		return store.APIToken{}, "", &RateLimitedError{Wait: wait}
	}
	name = strings.TrimSpace(name)
	if n := len([]rune(name)); n == 0 || n > MaxTokenNameLength {
		return store.APIToken{}, "", &model.InvalidError{Err: fmt.Errorf("a token name must be 1–%d characters", MaxTokenNameLength)}
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return store.APIToken{}, "", &model.InvalidError{Err: errors.New("a token name can't contain control characters")}
	}
	ok, err := s.Hasher.Verify(ctx, u.PasswordHash, password)
	if err != nil {
		return store.APIToken{}, "", err
	}
	if !ok {
		s.Limiter.Fail(keys...)
		s.record(ctx, Event{Kind: "auth.token_failed", Data: map[string]string{"reason": "wrong password"}})
		return store.APIToken{}, "", &model.InvalidError{Err: ErrWrongPassword}
	}
	s.Limiter.Succeed(keys...)
	if n, err := s.Store.CountAPITokens(ctx, u.ID); err != nil {
		return store.APIToken{}, "", err
	} else if n >= MaxAPITokens {
		return store.APIToken{}, "", ErrTooManyTokens
	}

	secret, prefix, hash, err := auth.NewAPIToken()
	if err != nil {
		return store.APIToken{}, "", err
	}
	id, err := auth.NewID()
	if err != nil {
		return store.APIToken{}, "", err
	}
	tok := store.APIToken{
		ID: id, UserID: u.ID, Name: name, Prefix: prefix, TokenHash: hash, Scope: "read", CreatedAt: s.now().UTC(),
	}
	if err := s.Store.CreateAPIToken(ctx, tok); err != nil {
		return store.APIToken{}, "", err
	}
	// The event says which token, and never what it is.
	s.record(ctx, Event{Kind: "auth.token_created", Data: map[string]string{"name": name, "prefix": prefix, "token": id}})
	return tok, secret, nil
}

// ListAPITokens returns an account's tokens, newest first. A token's secret isn't among them.
func (s *Service) ListAPITokens(ctx context.Context, userID string) ([]store.APIToken, error) {
	return s.Store.APITokens(ctx, userID)
}

// RevokeAPIToken ends one of an account's tokens at once.
func (s *Service) RevokeAPIToken(ctx context.Context, userID, id string) error {
	tok, err := s.Store.DeleteAPIToken(ctx, userID, id)
	if err != nil {
		return err
	}
	s.record(ctx, Event{Kind: "auth.token_revoked", Data: map[string]string{"name": tok.Name, "prefix": tok.Prefix, "token": id}})
	return nil
}

// AuthenticateToken returns the token for a secret. It returns store.ErrNoToken when the secret
// isn't one that exists, whatever the reason: a made-up secret, a wrong one, or a revoked one.
func (s *Service) AuthenticateToken(ctx context.Context, secret string) (store.APIToken, error) {
	if !auth.LooksLikeAPIToken(secret) {
		return store.APIToken{}, store.ErrNoToken
	}
	tok, err := s.Store.APITokenByHash(ctx, auth.HashToken(secret))
	if err != nil {
		return store.APIToken{}, err
	}
	if now := s.now(); tok.LastUsedAt.IsZero() || now.Sub(tok.LastUsedAt) >= apiTokenTouchEvery {
		if err := s.Store.TouchAPIToken(ctx, tok.ID, now); err != nil {
			s.Log.Warn("can't record a token's use", "err", err)
		}
		tok.LastUsedAt = now
	}
	return tok, nil
}
