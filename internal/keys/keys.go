// Package keys generates WireGuard keys and encrypts secrets for storage (docs/PLAN.md §7).
package keys

import (
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// SecretSize is the size of the at-rest encryption key in /etc/drawbridge/secret.key.
const SecretSize = chacha20poly1305.KeySize

// NewPrivateKey returns a new WireGuard private key.
func NewPrivateKey() (wgtypes.Key, error) {
	return wgtypes.GeneratePrivateKey()
}

// NewPresharedKey returns a new random preshared key.
func NewPresharedKey() (wgtypes.Key, error) {
	return wgtypes.GenerateKey()
}

// Sealer encrypts secrets with XChaCha20-Poly1305 before they reach the database. It
// protects copies of the database and backups; it can't protect against someone who
// can also read the secret key.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer returns a Sealer for a SecretSize-byte secret.
func NewSealer(secret []byte) (*Sealer, error) {
	if len(secret) != SecretSize {
		return nil, fmt.Errorf("secret key is %d bytes, want %d", len(secret), SecretSize)
	}
	aead, err := chacha20poly1305.NewX(secret)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// LoadSealer reads the secret key at path. The file must not be readable by other users.
func LoadSealer(path string) (*Sealer, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading the secret key: %w", err)
	}
	if info.Mode().Perm()&0o007 != 0 {
		return nil, fmt.Errorf("secret key %s is accessible to other users (mode %s); "+
			"run chmod 0640 on it", path, info.Mode().Perm())
	}
	secret, err := os.ReadFile(path) //nolint:gosec // G304: the path is Drawbridge's own configuration.
	if err != nil {
		return nil, fmt.Errorf("reading the secret key: %w", err)
	}
	s, err := NewSealer(secret)
	if err != nil {
		return nil, fmt.Errorf("secret key %s: %w", path, err)
	}
	return s, nil
}

// Seal encrypts plaintext. purpose is authenticated but not stored, so a sealed value
// can only be opened for the purpose it was sealed for (for example, one client's
// private key can't be swapped in for another's).
func (s *Sealer) Seal(plaintext []byte, purpose string) []byte {
	nonce := make([]byte, s.aead.NonceSize(), s.aead.NonceSize()+len(plaintext)+s.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		// crypto/rand.Read doesn't fail on Linux; if it ever did, sealing with a
		// predictable nonce would be worse than stopping.
		panic(err)
	}
	return s.aead.Seal(nonce, nonce, plaintext, []byte(purpose))
}

// ErrOpen means a sealed value couldn't be decrypted: it's corrupt, it was sealed for a
// different purpose, or it was sealed with a different secret key.
var ErrOpen = errors.New("can't decrypt a stored secret; is /etc/drawbridge/secret.key the one this database was created with?")

// Open decrypts a value from Seal. purpose must match the one it was sealed with.
func (s *Sealer) Open(sealed []byte, purpose string) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n+s.aead.Overhead() {
		return nil, ErrOpen
	}
	plaintext, err := s.aead.Open(nil, sealed[:n], sealed[n:], []byte(purpose))
	if err != nil {
		return nil, ErrOpen
	}
	return plaintext, nil
}

// SealKey encrypts a WireGuard key.
func (s *Sealer) SealKey(k wgtypes.Key, purpose string) []byte {
	return s.Seal(k[:], purpose)
}

// OpenKey decrypts a WireGuard key sealed with SealKey.
func (s *Sealer) OpenKey(sealed []byte, purpose string) (wgtypes.Key, error) {
	b, err := s.Open(sealed, purpose)
	if err != nil {
		return wgtypes.Key{}, err
	}
	return wgtypes.NewKey(b)
}
