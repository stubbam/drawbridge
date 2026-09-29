package auth

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/model"
)

// cheap keeps the tests fast; the format and the logic are the same.
var cheap = Params{Memory: 64, Time: 1, Threads: 1}

func TestHashAndVerify(t *testing.T) {
	ctx := context.Background()
	h := NewHasher(cheap)
	hash, err := h.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("hash %q isn't a PHC string with the configured parameters", hash)
	}
	if ok, err := h.Verify(ctx, hash, "correct horse battery"); err != nil || !ok {
		t.Fatalf("Verify(right password) = %v, %v", ok, err)
	}
	if ok, err := h.Verify(ctx, hash, "correct horse batterY"); err != nil || ok {
		t.Fatalf("Verify(wrong password) = %v, %v", ok, err)
	}
	again, _ := h.Hash(ctx, "correct horse battery")
	if again == hash {
		t.Fatal("two hashes of one password are equal; the salt isn't random")
	}

	// A hash keeps working after the defaults change: Verify reads its parameters.
	other := NewHasher(Params{Memory: 128, Time: 2, Threads: 2})
	if ok, err := other.Verify(ctx, hash, "correct horse battery"); err != nil || !ok {
		t.Fatalf("Verify with different parameters = %v, %v", ok, err)
	}
	h.VerifyNothing(ctx, "anything")
}

func TestVerifyRejectsBadHashes(t *testing.T) {
	ctx := context.Background()
	h := NewHasher(cheap)
	good, _ := h.Hash(ctx, "a password!")
	parts := strings.Split(good, "$")
	for name, hash := range map[string]string{
		"empty":          "",
		"bcrypt":         "$2b$10$abcdefghijklmnopqrstuu",
		"argon2i":        strings.Replace(good, "argon2id", "argon2i", 1),
		"old version":    strings.Replace(good, "v=19", "v=16", 1),
		"huge memory":    strings.Replace(good, "m=64,", "m=4194304,", 1),
		"no salt":        strings.Join([]string{"", parts[1], parts[2], parts[3], "", parts[5]}, "$"),
		"short key":      strings.Join([]string{"", parts[1], parts[2], parts[3], parts[4], "AAAA"}, "$"),
		"missing fields": "$argon2id$v=19$m=64,t=1,p=1",
	} {
		if ok, err := h.Verify(ctx, hash, "a password!"); ok || err == nil {
			t.Errorf("%s: Verify = %v, %v; want an error", name, ok, err)
		}
	}
}

func TestVerifyHonorsCancellation(t *testing.T) {
	h := NewHasher(cheap)
	hash, _ := h.Hash(context.Background(), "a password!")
	h.sem <- struct{}{} // another hash is running
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Verify(ctx, hash, "a password!"); err == nil {
		t.Fatal("Verify waited past its context")
	}
}

func TestSessionTokens(t *testing.T) {
	a, hashA, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := NewSessionToken()
	if a == b || len(a) != 43 {
		t.Fatalf("tokens %q and %q: want two different 43-character tokens", a, b)
	}
	if string(HashToken(a)) != string(hashA) || len(hashA) != 32 {
		t.Fatal("HashToken doesn't match the hash NewSessionToken returned")
	}
	id, _ := NewID()
	if len(id) != 32 {
		t.Fatalf("NewID() = %q, want 32 hex digits", id)
	}
}

func TestSetupTokens(t *testing.T) {
	tok, err := NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 20 || strings.Trim(tok, alphabet) != "" {
		t.Fatalf("token %q: want 20 characters from %s", tok, alphabet)
	}
	shown := FormatSetupToken(tok)
	if len(shown) != 23 || strings.Count(shown, "-") != 3 {
		t.Fatalf("FormatSetupToken = %q, want four groups of five", shown)
	}
	typed := " " + strings.ToLower(strings.ReplaceAll(shown, "-", " - ")) + "\n"
	if got := NormalizeSetupToken(typed); got != tok {
		t.Fatalf("NormalizeSetupToken(%q) = %q, want %q", typed, got, tok)
	}
}

func TestRandomPassword(t *testing.T) {
	p, err := RandomPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 23 || p != strings.ToLower(p) {
		t.Fatalf("RandomPassword() = %q, want four lowercase groups of five", p)
	}
	if err := ValidatePassword(p); err != nil {
		t.Fatalf("a random password fails validation: %v", err)
	}
}

func TestValidateUsername(t *testing.T) {
	for _, ok := range []string{"admin", "stuffam", "a", "José", "ops.admin_2-b", strings.Repeat("x", 32)} {
		if err := ValidateUsername(ok); err != nil {
			t.Errorf("ValidateUsername(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".admin", "ad min", "admin\n", "a'b", strings.Repeat("x", 33), "\xff"} {
		err := ValidateUsername(bad)
		if err == nil || !model.IsInvalid(err) {
			t.Errorf("ValidateUsername(%q) = %v, want an InvalidError", bad, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	for _, ok := range []string{"0123456789", "ten chars!", "ñññññññññññ", strings.Repeat("x", 1024)} {
		if err := ValidatePassword(ok); err != nil {
			t.Errorf("ValidatePassword(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "short", "123456789", strings.Repeat("x", 1025), "\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff"} {
		if err := ValidatePassword(bad); err == nil || !model.IsInvalid(err) {
			t.Errorf("ValidatePassword(%q) = %v, want an InvalidError", bad, err)
		}
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	l.Now = func() time.Time { return now }
	ip, user := "ip:192.0.2.7", "user:1"

	for i := range 5 {
		if w := l.Wait(ip, user); w != 0 {
			t.Fatalf("attempt %d: wait %v, want none of the five free attempts to wait", i+1, w)
		}
		l.Fail(ip, user)
	}
	// The sixth failure starts the backoff: 2s, then 4s, 8s, …
	l.Fail(ip, user)
	if w := l.Wait(ip, user); w != 2*time.Second {
		t.Fatalf("after 6 failures: wait %v, want 2s", w)
	}
	l.Fail(ip, user)
	if w := l.Wait(ip, user); w != 4*time.Second {
		t.Fatalf("after 7 failures: wait %v, want 4s", w)
	}
	if w := l.Wait("ip:192.0.2.8"); w != 0 {
		t.Fatalf("another source waits %v; keys must be independent", w)
	}
	if w := l.Wait("ip:192.0.2.8", user); w != 4*time.Second {
		t.Fatalf("another source for the same account waits %v, want the account's 4s", w)
	}
	for range 40 {
		l.Fail(ip)
	}
	if w := l.Wait(ip); w != 15*time.Minute {
		t.Fatalf("after many failures: wait %v, want the 15-minute cap", w)
	}

	now = now.Add(15 * time.Minute)
	if w := l.Wait(ip); w != 0 {
		t.Fatalf("after the wait: %v", w)
	}
	// Still over the free attempts until an hour passes without a failure.
	l.Fail(ip)
	if w := l.Wait(ip); w == 0 {
		t.Fatal("a failure right after a lockout didn't wait")
	}
	now = now.Add(2 * time.Hour)
	l.Fail(ip)
	if w := l.Wait(ip); w != 0 {
		t.Fatalf("an hour after the last failure, the count wasn't forgotten: wait %v", w)
	}

	l.Succeed(user)
	if w := l.Wait(user); w != 0 {
		t.Fatalf("after a success: wait %v", w)
	}
}

func TestLimiterBoundsMemory(t *testing.T) {
	l := NewLimiter()
	for i := range maxEntries + 10 {
		l.Fail("ip:" + netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}).String())
	}
	l.Fail("user:1")
	if n := len(l.entries); n > maxEntries {
		t.Fatalf("%d entries, want at most %d", n, maxEntries)
	}
}

func TestSourceKey(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.4.20":             "ip:192.168.4.20",
		"::ffff:192.168.4.20":      "ip:192.168.4.20",
		"2001:db8:1:2:aaaa::1":     "ip:2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb:cccc::": "ip:2001:db8:1:2::/64",
	} {
		if got := SourceKey(netip.MustParseAddr(in)); got != want {
			t.Errorf("SourceKey(%s) = %q, want %q", in, got, want)
		}
	}
}
