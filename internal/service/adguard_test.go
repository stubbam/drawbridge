package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/adguard/adguardtest"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
)

func sp(s string) *string { return &s }

// adguardEnv is a service with a fake AdGuard Home that wants drawbridge / secret, and a DNS
// check that finds a resolver on the first VPN address only.
func adguardEnv(t *testing.T) (*Service, *clock, *adguardtest.Server, context.Context) {
	t.Helper()
	s, c := newTestService(t)
	fake := adguardtest.New(t, "drawbridge", "secret")
	s.DNSProbe = func(_ context.Context, a netip.Addr) DNSProbe {
		if a.Is4() {
			return DNSProbe{Answered: true, Detail: "A resolver answered."}
		}
		return DNSProbe{Detail: "Nothing answered within two seconds."}
	}
	return s, c, fake, web(context.Background(), "admin")
}

func saveFake(t *testing.T, s *Service, ctx context.Context, fake *adguardtest.Server) {
	t.Helper()
	if _, err := s.UpdateAdGuard(ctx, AdGuardPatch{BaseURL: sp(fake.URL()), Username: sp("drawbridge"), Password: sp("secret")}); err != nil {
		t.Fatal(err)
	}
}

func TestAdGuardConnectionStartsUnconfigured(t *testing.T) {
	s, _ := newTestService(t)
	got, err := s.AdGuard(context.Background())
	if err != nil || got.Configured || got.BaseURL != "http://127.0.0.1:3000/control" || got.HasPassword {
		t.Fatalf("AdGuard = %+v, %v; want the usual local address to start from, and nothing saved", got, err)
	}
}

func TestUpdateAdGuardSavesAndRecordsWithoutThePassword(t *testing.T) {
	s, _ := newTestService(t)
	var logs bytes.Buffer
	s.Log = slog.New(slog.NewTextHandler(&logs, nil))
	ctx := web(context.Background(), "admin")

	const password = "hunter2-hunter2"
	got, err := s.UpdateAdGuard(ctx, AdGuardPatch{BaseURL: sp(" http://127.0.0.1:3000/ "), Username: sp(" drawbridge "), Password: sp(password)})
	if err != nil {
		t.Fatal(err)
	}
	if want := (AdGuardConnection{Configured: true, BaseURL: "http://127.0.0.1:3000/control", Username: "drawbridge", HasPassword: true, SyncNames: true}); got != want {
		t.Errorf("UpdateAdGuard = %+v, want %+v", got, want)
	}
	in, ok, _ := s.Store.DNSIntegration(ctx)
	if !ok || in.Password != password || in.Kind != store.KindAdGuard {
		t.Errorf("saved %+v", in)
	}

	events, _ := s.Events(ctx, store.EventFilter{})
	if len(events) != 1 || events[0].Kind != "integration.adguard_changed" || events[0].Category != CategoryAdmin ||
		events[0].Actor != "admin" || events[0].Data["password"] != "set" ||
		events[0].Data["address"] != "none → http://127.0.0.1:3000/control" || events[0].Data["username"] != "none → drawbridge" {
		t.Fatalf("events = %+v", events)
	}
	// The event log, and the journal that gets every event, never see the password.
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), password) || strings.Contains(logs.String(), password) {
		t.Error("the password is in an event or in the log")
	}

	// A change to the password alone says that it changed, and nothing else.
	if _, err := s.UpdateAdGuard(ctx, AdGuardPatch{Password: sp("another-one")}); err != nil {
		t.Fatal(err)
	}
	events, _ = s.Events(ctx, store.EventFilter{})
	if len(events) != 2 || len(events[0].Data) != 1 || events[0].Data["password"] != "changed" {
		t.Errorf("after changing the password: %+v", events[0])
	}
	// Saving what's already saved changes nothing, so it isn't an event.
	if _, err := s.UpdateAdGuard(ctx, AdGuardPatch{BaseURL: sp("http://127.0.0.1:3000/control"), Username: sp("drawbridge")}); err != nil {
		t.Fatal(err)
	}
	if events, _ = s.Events(ctx, store.EventFilter{}); len(events) != 2 {
		t.Errorf("saving the same connection recorded an event: %d events", len(events))
	}
	if in, _, _ = s.Store.DNSIntegration(ctx); in.Password != "another-one" {
		t.Error("leaving the password out of a save didn't keep it")
	}
}

// The saved password goes only where it was saved to. A change of address or username that doesn't
// bring a password is refused, in a save and in a test, and nothing is sent anywhere.
func TestAdGuardPasswordStaysWithItsAddressAndAccount(t *testing.T) {
	s, _, fake, ctx := adguardEnv(t)
	saveFake(t, s, ctx, fake)
	before, _ := s.Events(ctx, store.EventFilter{})

	var asked atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked.Add(1) }))
	t.Cleanup(elsewhere.Close)

	for name, p := range map[string]AdGuardPatch{
		"another address":  {BaseURL: sp(elsewhere.URL + "/control")},
		"another username": {Username: sp("someone-else")},
		"both":             {BaseURL: sp(elsewhere.URL), Username: sp("someone-else")},
	} {
		if _, err := s.UpdateAdGuard(ctx, p); !model.IsInvalid(err) || !strings.Contains(err.Error(), "enter the password again") {
			t.Errorf("saving %s without a password: err = %v, want an invalid-input error", name, err)
		}
		if _, err := s.TestAdGuard(ctx, p); !model.IsInvalid(err) {
			t.Errorf("testing %s without a password: err = %v, want an invalid-input error", name, err)
		}
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("%d requests reached the other server; the saved password could have gone with them", n)
	}
	if in, _, _ := s.Store.DNSIntegration(ctx); in.BaseURL != fake.URL() || in.Password != "secret" {
		t.Errorf("a refused save changed what's saved: %+v", in)
	}
	if after, _ := s.Events(ctx, store.EventFilter{}); len(after) != len(before) {
		t.Error("a refused save recorded an event")
	}

	// With a password of its own, the change goes through.
	if _, err := s.UpdateAdGuard(ctx, AdGuardPatch{BaseURL: sp(elsewhere.URL), Password: sp("new")}); err != nil {
		t.Fatalf("saving another address with a password: %v", err)
	}
	// And an AdGuard Home with no login has no password to protect.
	got, err := s.UpdateAdGuard(ctx, AdGuardPatch{Username: sp("")})
	if err != nil || got.Username != "" || got.HasPassword {
		t.Errorf("clearing the username: %+v, %v; want no username and no password", got, err)
	}
}

func TestUpdateAdGuardChecksWhatItGets(t *testing.T) {
	s, _ := newTestService(t)
	ctx := web(context.Background(), "admin")
	for name, p := range map[string]AdGuardPatch{
		"a bad address":           {BaseURL: sp("not a url")},
		"an address with a login": {BaseURL: sp("http://u:p@127.0.0.1:3000")},
		"a colon in the username": {Username: sp("a:b")},
		"a long username":         {Username: sp(strings.Repeat("u", 129))},
		"a newline in a password": {Username: sp("u"), Password: sp("a\nb")},
		"a long password":         {Username: sp("u"), Password: sp(strings.Repeat("p", 1025))},
	} {
		if _, err := s.UpdateAdGuard(ctx, p); !model.IsInvalid(err) {
			t.Errorf("%s: err = %v, want an invalid-input error", name, err)
		}
	}
	if _, ok, _ := s.Store.DNSIntegration(ctx); ok {
		t.Error("something was saved from input that was refused")
	}
}

func TestRemoveAdGuard(t *testing.T) {
	s, _, fake, ctx := adguardEnv(t)
	if err := s.RemoveAdGuard(ctx); err != nil {
		t.Fatalf("removing nothing: %v", err)
	}
	if events, _ := s.Events(ctx, store.EventFilter{}); len(events) != 0 {
		t.Error("removing nothing recorded an event")
	}
	saveFake(t, s, ctx, fake)
	if err := s.RemoveAdGuard(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AdGuard(ctx); got.Configured || got.HasPassword {
		t.Errorf("after Remove: %+v", got)
	}
	if _, ok, _ := s.Store.DNSIntegration(ctx); ok {
		t.Error("the row is still there, with its password")
	}
	if got := kinds(t, s); got[len(got)-1] != "integration.adguard_removed" {
		t.Errorf("events = %v", got)
	}
}

func TestTestAdGuard(t *testing.T) {
	s, _, fake, ctx := adguardEnv(t)

	// With nothing saved, the values in the request are what's tested, and nothing is saved.
	res, err := s.TestAdGuard(ctx, AdGuardPatch{BaseURL: sp(fake.URL()), Username: sp("drawbridge"), Password: sp("secret")})
	if err != nil || !res.OK || res.Error != "" || res.Refused {
		t.Fatalf("TestAdGuard = %+v, %v", res, err)
	}
	if res.Version == "" || !res.Running || !res.ProtectionEnabled || res.QueryLog == nil || !res.QueryLog.Enabled {
		t.Errorf("what AdGuard Home said is missing: %+v", res)
	}
	if len(res.DNS) != 2 || !res.DNS[0].Answered || res.DNS[1].Answered || len(res.Warnings) != 0 {
		t.Errorf("DNS = %+v, warnings = %v", res.DNS, res.Warnings)
	}
	if _, ok, _ := s.Store.DNSIntegration(ctx); ok {
		t.Error("a test saved the connection")
	}
	if kinds(t, s) != nil {
		t.Errorf("a test is recorded as an event: %v", kinds(t, s))
	}

	// With a connection saved, a test of nothing in particular uses it, password and all.
	saveFake(t, s, ctx, fake)
	if res, err := s.TestAdGuard(ctx, AdGuardPatch{}); err != nil || !res.OK {
		t.Fatalf("TestAdGuard of the saved connection = %+v, %v", res, err)
	}

	// What AdGuard Home has set that works against the integration is said.
	fake.SetLogConfig(false, false)
	res, _ = s.TestAdGuard(ctx, AdGuardPatch{})
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "query log is off") {
		t.Errorf("with the log off: %v", res.Warnings)
	}
	fake.SetLogConfig(true, true)
	res, _ = s.TestAdGuard(ctx, AdGuardPatch{})
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "hides the end of each client's address") {
		t.Errorf("with addresses hidden: %v", res.Warnings)
	}
}

func TestTestAdGuardReportsWhatWentWrong(t *testing.T) {
	s, _, fake, ctx := adguardEnv(t)

	fake.SetDown(true)
	res, err := s.TestAdGuard(ctx, AdGuardPatch{BaseURL: sp(fake.URL()), Username: sp("drawbridge"), Password: sp("secret")})
	if err != nil || res.OK || res.Refused || !strings.Contains(res.Error, "can't reach AdGuard Home") {
		t.Errorf("an unreachable AdGuard Home: %+v, %v", res, err)
	}
	// The VPN addresses are still asked, because that answer doesn't depend on AdGuard Home's API.
	if len(res.DNS) != 2 {
		t.Errorf("DNS = %+v", res.DNS)
	}
	// An unreachable server isn't a refused account, so there's no wait before the next try.
	fake.SetDown(false)
	if res, _ = s.TestAdGuard(ctx, AdGuardPatch{BaseURL: sp(fake.URL()), Username: sp("drawbridge"), Password: sp("secret")}); !res.OK {
		t.Errorf("after it came back: %+v", res)
	}
}

// A refused account is remembered, so that a double click, or a script, can't go on asking until
// AdGuard Home has blocked the daemon for 15 minutes.
func TestTestAdGuardDoesNotAskAgainAfterARefusal(t *testing.T) {
	s, clk, fake, ctx := adguardEnv(t)
	wrong := AdGuardPatch{BaseURL: sp(fake.URL()), Username: sp("drawbridge"), Password: sp("wrong")}

	res, err := s.TestAdGuard(ctx, wrong)
	if err != nil || res.OK || !res.Refused || !strings.Contains(res.Error, "refused the account") {
		t.Fatalf("a wrong password: %+v, %v", res, err)
	}
	for range 10 {
		res, _ = s.TestAdGuard(ctx, wrong)
		if !res.Refused || !strings.Contains(res.Error, "a moment ago") || !strings.Contains(res.Error, "30 seconds") {
			t.Fatalf("a repeat: %+v", res)
		}
	}
	if n := fake.FailedLogins(); n != 1 {
		t.Errorf("AdGuard Home saw %d refused logins, want 1: the rest shouldn't have been sent", n)
	}
	if fake.Blocked() {
		t.Error("the repeats got the caller blocked")
	}

	// A different password is a different question, and goes through.
	if res, _ = s.TestAdGuard(ctx, AdGuardPatch{BaseURL: sp(fake.URL()), Username: sp("drawbridge"), Password: sp("secret")}); !res.OK {
		t.Errorf("the right password: %+v", res)
	}
	// And after the wait, the refused one may be tried again.
	clk.advance(refusedWait + time.Second)
	if res, _ = s.TestAdGuard(ctx, wrong); !res.Refused || strings.Contains(res.Error, "a moment ago") {
		t.Errorf("after the wait: %+v", res)
	}
	if n := fake.FailedLogins(); n != 2 {
		t.Errorf("AdGuard Home saw %d refused logins, want 2", n)
	}
}
