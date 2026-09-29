// Package auth has the building blocks of the admin login (docs/PLAN.md §6.5):
// Argon2id password hashing, random tokens, validation, and the rate limiter that slows
// down password guessing. The login flows themselves are in package service.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Params are Argon2id's cost parameters.
type Params struct {
	// Memory is in KiB.
	Memory  uint32
	Time    uint32
	Threads uint8
}

// DefaultParams is RFC 9106's second recommended option: 64 MiB, three passes, four
// lanes. It takes about 0.2 seconds on a Raspberry Pi 5.
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 4}

const (
	saltLen = 16
	keyLen  = 32
)

// ErrBadHash means a stored hash isn't an Argon2id PHC string this package can read.
var ErrBadHash = errors.New("unrecognized password hash")

// Hasher hashes and verifies passwords. It runs one hash at a time, so a burst of
// logins can't take more than one hash's worth of memory from a small host that runs
// other services.
type Hasher struct {
	Params Params

	sem       chan struct{}
	dummyOnce sync.Once
	dummy     string
}

// NewHasher returns a Hasher that uses p for new hashes.
func NewHasher(p Params) *Hasher {
	return &Hasher{Params: p, sem: make(chan struct{}, 1)}
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.sem }

// Hash returns password's hash in the PHC string format:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	p := h.Params
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, keyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time,
		p.Threads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify reports whether password matches the hash. It uses the parameters stored in
// the hash, so hashes made with older parameters keep working.
func (h *Hasher) Verify(ctx context.Context, hash, password string) (bool, error) {
	p, salt, want, err := parseHash(hash)
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want))) //nolint:gosec // G115: len(want) is at most 1 KiB (parseHash).
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// VerifyNothing takes as long as a Verify, for a login with an unknown username, so
// the response time doesn't reveal whether the username exists.
func (h *Hasher) VerifyNothing(ctx context.Context, password string) {
	h.dummyOnce.Do(func() {
		h.dummy, _ = h.Hash(context.Background(), "drawbridge: no such account")
	})
	_, _ = h.Verify(ctx, h.dummy, password)
}

func parseHash(hash string) (Params, []byte, []byte, error) {
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, key
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Params{}, nil, nil, ErrBadHash
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return Params{}, nil, nil, ErrBadHash
	}
	// Refuse costs no hash of ours would have, so a corrupted row can't exhaust memory.
	if p.Memory < 8 || p.Memory > 1<<20 || p.Time < 1 || p.Time > 64 || p.Threads < 1 {
		return Params{}, nil, nil, ErrBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return Params{}, nil, nil, ErrBadHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 1024 {
		return Params{}, nil, nil, ErrBadHash
	}
	return p, salt, key, nil
}
