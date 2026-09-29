package keys

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSealer(t *testing.T) *Sealer {
	t.Helper()
	s, err := NewSealer(bytes.Repeat([]byte{7}, SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSealRoundTrip(t *testing.T) {
	s := testSealer(t)
	sealed := s.Seal([]byte("hello"), "purpose")
	if bytes.Contains(sealed, []byte("hello")) {
		t.Fatal("sealed value contains the plaintext")
	}
	got, err := s.Open(sealed, "purpose")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestSealUsesFreshNonces(t *testing.T) {
	s := testSealer(t)
	if bytes.Equal(s.Seal([]byte("x"), "p"), s.Seal([]byte("x"), "p")) {
		t.Fatal("sealing the same value twice gave the same ciphertext")
	}
}

func TestOpenRejectsWrongPurposeKeyAndTampering(t *testing.T) {
	s := testSealer(t)
	sealed := s.Seal([]byte("secret"), "client:a")

	if _, err := s.Open(sealed, "client:b"); !errors.Is(err, ErrOpen) {
		t.Errorf("wrong purpose: err %v, want ErrOpen", err)
	}

	other, err := NewSealer(bytes.Repeat([]byte{8}, SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(sealed, "client:a"); !errors.Is(err, ErrOpen) {
		t.Errorf("wrong secret: err %v, want ErrOpen", err)
	}

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	if _, err := s.Open(tampered, "client:a"); !errors.Is(err, ErrOpen) {
		t.Errorf("tampered: err %v, want ErrOpen", err)
	}

	if _, err := s.Open([]byte("short"), "client:a"); !errors.Is(err, ErrOpen) {
		t.Errorf("truncated: err %v, want ErrOpen", err)
	}
}

func TestSealKeyRoundTrip(t *testing.T) {
	s := testSealer(t)
	k, err := NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.OpenKey(s.SealKey(k, "server"), "server")
	if err != nil {
		t.Fatal(err)
	}
	if got != k {
		t.Fatal("key changed in a seal round trip")
	}
}

func TestNewSealerRejectsWrongSize(t *testing.T) {
	if _, err := NewSealer(make([]byte, 16)); err == nil {
		t.Fatal("16-byte secret accepted")
	}
}

func TestLoadSealer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, SecretSize), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSealer(path); err != nil {
		t.Fatalf("0640 secret: %v", err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSealer(path); err == nil || !strings.Contains(err.Error(), "other users") {
		t.Fatalf("world-readable secret: err %v, want a permissions error", err)
	}

	short := filepath.Join(dir, "short.key")
	if err := os.WriteFile(short, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSealer(short); err == nil {
		t.Fatal("3-byte secret accepted")
	}

	if _, err := LoadSealer(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing secret accepted")
	}
}

func TestPresharedKeysDiffer(t *testing.T) {
	a, err := NewPresharedKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewPresharedKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two preshared keys are equal")
	}
}
