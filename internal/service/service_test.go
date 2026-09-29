package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/auth"
	"github.com/stuffam/drawbridge/internal/firewall"
	"github.com/stuffam/drawbridge/internal/keys"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/reconcile"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/wg"
)

// memFirewall remembers the applied revision, like the kernel's table does.
type memFirewall struct{ rev string }

func (f *memFirewall) Apply(_ context.Context, rs firewall.Ruleset) error {
	f.rev = rs.Revision
	return nil
}
func (f *memFirewall) Remove(context.Context) error                   { f.rev = ""; return nil }
func (f *memFirewall) Revision(context.Context) (string, bool, error) { return f.rev, f.rev != "", nil }

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestService(t *testing.T) (*Service, *clock) {
	t.Helper()
	ctx := context.Background()
	sealer, err := keys.NewSealer(bytes.Repeat([]byte{7}, keys.SecretSize))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db"), sealer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	backend := wg.NewFake()
	rec := &reconcile.Reconciler{State: st, WG: backend, Firewall: &memFirewall{}}
	if _, err := rec.Up(ctx); err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	limiter := auth.NewLimiter()
	limiter.Now = c.now
	return &Service{
		Store:   st,
		Rec:     rec,
		WG:      backend,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Hasher:  auth.NewHasher(auth.Params{Memory: 64, Time: 1, Threads: 1}),
		Limiter: limiter,
		Now:     c.now,
	}, c
}

// web is a request from the admin's browser on the LAN.
func web(ctx context.Context, name string) context.Context {
	return WithActor(ctx, Actor{Name: name, Via: ViaWeb, SourceIP: "192.168.4.20"})
}

func kinds(t *testing.T, s *Service) []string {
	t.Helper()
	events, err := s.Events(context.Background(), store.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(events) - 1; i >= 0; i-- {
		out = append(out, events[i].Kind)
	}
	return out
}

func TestSetup(t *testing.T) {
	s, _ := newTestService(t)
	ctx := web(context.Background(), "")

	if needed, err := s.SetupNeeded(ctx); err != nil || !needed {
		t.Fatalf("SetupNeeded = %v, %v", needed, err)
	}
	token, err := s.SetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.SetupToken(ctx); again != token || len(token) != 23 {
		t.Fatalf("SetupToken = %q then %q: want the same formatted token", token, again)
	}

	if _, err := s.CompleteSetup(ctx, "WRONG-TOKEN", "admin", "a long password", "test"); !errors.Is(err, store.ErrBadSetupToken) {
		t.Fatalf("wrong token: err %v", err)
	}
	if _, err := s.CompleteSetup(ctx, token, "admin", "short", "test"); !model.IsInvalid(err) {
		t.Fatalf("short password: err %v, want an InvalidError", err)
	}
	// People copy tokens in whatever case, with or without the dashes.
	login, err := s.CompleteSetup(ctx, strings.ToLower(strings.ReplaceAll(token, "-", "")), "admin",
		"a long password", "Firefox")
	if err != nil {
		t.Fatal(err)
	}
	if login.Token == "" || login.User.Username != "admin" || login.Session.UserAgent != "Firefox" ||
		login.Session.IP != "192.168.4.20" {
		t.Fatalf("login %+v", login)
	}
	if _, u, err := s.Authenticate(ctx, login.Token); err != nil || u.Username != "admin" {
		t.Fatalf("Authenticate after setup = %+v, %v", u, err)
	}
	if needed, _ := s.SetupNeeded(ctx); needed {
		t.Fatal("setup still needed after it completed")
	}
	if _, err := s.SetupToken(ctx); !errors.Is(err, store.ErrSetupDone) {
		t.Fatalf("SetupToken after setup: err %v, want ErrSetupDone", err)
	}
	if got := kinds(t, s); strings.Join(got, " ") != "auth.setup_failed auth.setup_completed" {
		t.Fatalf("events %v", got)
	}
}

func TestLoginAndSessionLimits(t *testing.T) {
	s, clk := newTestService(t)
	ctx := web(context.Background(), "")
	password, err := s.CreateAdmin(WithActor(context.Background(), Actor{Name: "root", Via: ViaCLI}), "admin")
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range [][2]string{{"admin", "wrong password"}, {"nobody", password}} {
		if _, err := s.Login(ctx, bad[0], bad[1], "test"); !errors.Is(err, ErrBadLogin) {
			t.Fatalf("Login(%q) err %v, want ErrBadLogin", bad[0], err)
		}
	}
	login, err := s.Login(ctx, "ADMIN", password, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, ""); !errors.Is(err, store.ErrNoSession) {
		t.Fatal("an empty token authenticated")
	}
	if _, _, err := s.Authenticate(ctx, login.Token+"x"); !errors.Is(err, store.ErrNoSession) {
		t.Fatal("a wrong token authenticated")
	}

	// Used every 50 minutes, the session outlives the hour's idle limit…
	for range 13 {
		clk.advance(50 * time.Minute)
		if _, _, err := s.Authenticate(ctx, login.Token); err != nil {
			if clk.t.Sub(login.Session.CreatedAt) < 12*time.Hour {
				t.Fatalf("session ended after %v: %v", clk.t.Sub(login.Session.CreatedAt), err)
			}
			break
		}
		// …but not the twelve-hour absolute limit.
		if clk.t.Sub(login.Session.CreatedAt) >= 12*time.Hour {
			t.Fatal("session outlived the absolute limit")
		}
	}

	// An idle session ends after an hour.
	login, _ = s.Login(ctx, "admin", password, "test")
	clk.advance(59 * time.Minute)
	if _, _, err := s.Authenticate(ctx, login.Token); err != nil {
		t.Fatalf("after 59 idle minutes: %v", err)
	}
	clk.advance(61 * time.Minute)
	if _, _, err := s.Authenticate(ctx, login.Token); !errors.Is(err, store.ErrNoSession) {
		t.Fatalf("after 61 idle minutes: err %v, want ErrNoSession", err)
	}
	if list, _ := s.ListSessions(ctx, login.User.ID); len(list) != 0 {
		t.Fatalf("expired sessions are listed: %+v", list)
	}
}

func TestSessionTouchesAreThrottled(t *testing.T) {
	s, clk := newTestService(t)
	ctx := web(context.Background(), "")
	password, _ := s.CreateAdmin(ctx, "admin")
	login, _ := s.Login(ctx, "admin", password, "test")

	clk.advance(time.Minute)
	sess, _, _ := s.Authenticate(ctx, login.Token)
	stored, _, _ := s.Store.SessionByToken(ctx, auth.HashToken(login.Token))
	if !stored.LastSeenAt.Equal(login.Session.LastSeenAt) || !sess.LastSeenAt.Equal(login.Session.LastSeenAt) {
		t.Fatal("a use a minute after login was written to the database")
	}
	clk.advance(5 * time.Minute)
	_, _, _ = s.Authenticate(ctx, login.Token)
	stored, _, _ = s.Store.SessionByToken(ctx, auth.HashToken(login.Token))
	if !stored.LastSeenAt.Equal(clk.t) {
		t.Fatalf("LastSeenAt %v, want %v after five minutes", stored.LastSeenAt, clk.t)
	}
}

func TestLoginRateLimit(t *testing.T) {
	s, clk := newTestService(t)
	ctx := web(context.Background(), "")
	password, _ := s.CreateAdmin(ctx, "admin")

	for i := range 6 {
		if _, err := s.Login(ctx, "admin", "guess", "test"); !errors.Is(err, ErrBadLogin) {
			t.Fatalf("guess %d: err %v", i+1, err)
		}
	}
	// Locked: even the right password is refused, without being checked.
	var limited *RateLimitedError
	if _, err := s.Login(ctx, "admin", password, "test"); !errors.As(err, &limited) || limited.Wait != 2*time.Second {
		t.Fatalf("after 6 failures: err %v, want a 2s RateLimitedError", err)
	}
	// Another source is limited too, because the account is.
	other := WithActor(context.Background(), Actor{Via: ViaWeb, SourceIP: "10.8.0.2"})
	if _, err := s.Login(other, "admin", password, "test"); !errors.As(err, &limited) {
		t.Fatalf("another source: err %v, want a RateLimitedError", err)
	}
	clk.advance(3 * time.Second)
	if _, err := s.Login(ctx, "admin", password, "test"); err != nil {
		t.Fatalf("after the wait: %v", err)
	}

	// The CLI's reset lifts an account's lockout.
	for range 8 {
		_, _ = s.Login(other, "admin", "guess", "test")
	}
	u, newPassword, err := s.ResetPassword(WithActor(context.Background(), Actor{Name: "root", Via: ViaCLI}), "")
	if err != nil || u.Username != "admin" {
		t.Fatalf("ResetPassword = %+v, %v", u, err)
	}
	if _, err := s.Login(ctx, "admin", newPassword, "test"); err != nil {
		t.Fatalf("login after reset: %v", err)
	}
	if _, err := s.Login(ctx, "admin", password, "test"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("the old password after a reset: err %v", err)
	}
}

func TestChangePasswordAndRevoke(t *testing.T) {
	s, _ := newTestService(t)
	ctx := web(context.Background(), "admin")
	password, _ := s.CreateAdmin(ctx, "admin")
	here, _ := s.Login(ctx, "admin", password, "laptop")
	there, _ := s.Login(ctx, "admin", password, "phone")

	if err := s.ChangePassword(ctx, here.User, here.Session, "wrong", "a new long password"); !errors.Is(err, ErrWrongPassword) || !model.IsInvalid(err) {
		t.Fatalf("wrong current password: err %v", err)
	}
	if err := s.ChangePassword(ctx, here.User, here.Session, password, "short"); !model.IsInvalid(err) {
		t.Fatalf("short new password: err %v", err)
	}
	if err := s.ChangePassword(ctx, here.User, here.Session, password, "a new long password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, here.Token); err != nil {
		t.Fatalf("the session that changed the password ended: %v", err)
	}
	if _, _, err := s.Authenticate(ctx, there.Token); !errors.Is(err, store.ErrNoSession) {
		t.Fatal("other sessions survived a password change")
	}

	other, _ := s.Login(ctx, "admin", "a new long password", "tablet")
	if list, _ := s.ListSessions(ctx, here.User.ID); len(list) != 2 {
		t.Fatalf("%d sessions, want 2", len(list))
	}
	if err := s.RevokeSession(ctx, here.User.ID, other.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, other.Token); !errors.Is(err, store.ErrNoSession) {
		t.Fatal("a revoked session still works")
	}
	if err := s.Logout(ctx, here.Session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, here.Token); !errors.Is(err, store.ErrNoSession) {
		t.Fatal("a logged-out session still works")
	}
}

func TestCreateAdminOnce(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	if _, err := s.CreateAdmin(ctx, "bad name"); !model.IsInvalid(err) {
		t.Fatalf("invalid username: err %v", err)
	}
	if _, err := s.CreateAdmin(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAdmin(ctx, "second"); !errors.Is(err, store.ErrUserExists) {
		t.Fatalf("second admin: err %v, want ErrUserExists", err)
	}
	if _, _, err := s.ResetPassword(ctx, "nobody"); !errors.Is(err, store.ErrNoUser) {
		t.Fatalf("reset for an unknown user: err %v", err)
	}
}

func TestChangesAreRecordedWithTheirActor(t *testing.T) {
	s, _ := newTestService(t)
	ctx := web(context.Background(), "admin")
	if _, err := s.Store.UpdateSettings(ctx, func(st *model.Settings) error {
		st.EndpointHost = "vpn.example.com"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	c, _, err := s.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetEnabled(ctx, store.ByID(c.ID), false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetEnabled(ctx, store.ByID(c.ID), true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameClient(ctx, store.ByID(c.ID), "pixel"); err != nil {
		t.Fatal(err)
	}
	// Renaming to the same name changes nothing, so it isn't recorded.
	if _, err := s.RenameClient(ctx, store.ByID(c.ID), "pixel"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Config(ctx, store.ByName("pixel")); err != nil {
		t.Fatal(err)
	}
	mtu := 1380
	if _, _, err := s.UpdateSettings(ctx, SettingsPatch{MTU: &mtu}); err != nil {
		t.Fatal(err)
	}
	// Setting the same value again isn't a change.
	if _, _, err := s.UpdateSettings(ctx, SettingsPatch{MTU: &mtu}); err != nil {
		t.Fatal(err)
	}
	cli := WithActor(context.Background(), Actor{Name: "root", Via: ViaCLI})
	if _, _, err := s.DeleteClient(cli, store.ByName("pixel")); err != nil {
		t.Fatal(err)
	}

	want := "client.added client.paused client.resumed client.renamed client.config_viewed " +
		"server.settings_changed client.deleted"
	if got := strings.Join(kinds(t, s), " "); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	events, _ := s.Events(ctx, store.EventFilter{})
	deleted, settings, renamed := events[0], events[1], events[3]
	if deleted.Actor != "root" || deleted.Via != ViaCLI || deleted.ClientName != "pixel" || deleted.ClientID != c.ID {
		t.Errorf("delete event %+v", deleted)
	}
	if settings.Actor != "admin" || settings.Via != ViaWeb || settings.SourceIP != "192.168.4.20" ||
		settings.Data["mtu"] != "1420 → 1380" || len(settings.Data) != 1 {
		t.Errorf("settings event %+v", settings)
	}
	if got := settingsChanges(model.Settings{}, model.Settings{EndpointHost: "vpn.example.com", MTU: 1420}); got["endpoint_host"] != "none → vpn.example.com" || got["dns"] != "" {
		t.Errorf("settingsChanges from empty values = %v", got)
	}
	if renamed.Data["from"] != "phone" || renamed.ClientName != "pixel" {
		t.Errorf("rename event %+v", renamed)
	}
	if byClient, _ := s.Events(ctx, store.EventFilter{ClientID: c.ID}); len(byClient) != 6 {
		t.Errorf("%d events for the client, want 6", len(byClient))
	}
}

func TestDriftIsRecordedAsASystemEvent(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	st, _ := s.Settings(ctx)
	if err := s.WG.SetMTU(st.Interface, 1500); err != nil {
		t.Fatal(err)
	}
	s.Sync(ctx)
	events, _ := s.Events(ctx, store.EventFilter{Category: CategorySystem})
	if len(events) != 1 || events[0].Kind != "tunnel.drift_corrected" || events[0].Actor != "drawbridge" ||
		!strings.Contains(events[0].Data["changes"], "MTU") {
		t.Fatalf("system events %+v", events)
	}
	s.Sync(ctx)
	if events, _ = s.Events(ctx, store.EventFilter{}); len(events) != 1 {
		t.Fatalf("a Sync with nothing to correct was recorded: %+v", events)
	}
}

func TestStatus(t *testing.T) {
	s, clk := newTestService(t)
	ctx := context.Background()
	for _, name := range []string{"phone", "laptop", "tablet"} {
		if _, _, err := s.AddClient(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.SetEnabled(ctx, store.ByName("tablet"), false); err != nil {
		t.Fatal(err)
	}
	phone, _ := s.Client(ctx, store.ByName("phone"))
	laptop, _ := s.Client(ctx, store.ByName("laptop"))
	fake := s.WG.(*wg.Fake)
	fake.SetHandshake("wg0", phone.PublicKey, wg.Peer{LastHandshake: clk.t.Add(-time.Minute)})
	fake.SetHandshake("wg0", laptop.PublicKey, wg.Peer{LastHandshake: clk.t.Add(-10 * time.Minute)})

	st, err := s.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Status{TunnelUp: true, Clients: 3, Paused: 1, Online: 1}); st != want {
		t.Fatalf("Status = %+v, want %+v", st, want)
	}
	if err := s.WG.Delete("wg0"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Status(ctx); st.TunnelUp || st.Online != 0 || st.Clients != 3 {
		t.Fatalf("Status with the tunnel down = %+v", st)
	}
}

func TestAdminAllowedIsValidatedNormalizedAndRecorded(t *testing.T) {
	ctx := web(context.Background(), "admin")
	s, _ := newTestService(t)

	// Host bits are masked, and the change is an event.
	tailscale := []netip.Prefix{netip.MustParsePrefix("100.64.10.75/24")}
	updated, _, err := s.UpdateSettings(ctx, SettingsPatch{AdminAllowed: &tailscale})
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParsePrefix("100.64.10.0/24"); len(updated.AdminAllowed) != 1 || updated.AdminAllowed[0] != want {
		t.Fatalf("got %v, want [%v]", updated.AdminAllowed, want)
	}
	events, _ := s.Events(ctx, store.EventFilter{})
	if events[0].Kind != "server.settings_changed" || events[0].Data["admin_allowed"] != "none → [100.64.10.0/24]" {
		t.Fatalf("event %+v", events[0])
	}

	// A public range is refused: nothing is saved and nothing is recorded.
	public := []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	if _, _, err := s.UpdateSettings(ctx, SettingsPatch{AdminAllowed: &public}); !model.IsInvalid(err) {
		t.Fatalf("err %v, want a validation error", err)
	}
	got, _ := s.Settings(ctx)
	if len(got.AdminAllowed) != 1 {
		t.Fatalf("a rejected patch left %v", got.AdminAllowed)
	}
	if again, _ := s.Events(ctx, store.EventFilter{}); len(again) != len(events) {
		t.Fatal("a rejected patch was recorded")
	}

	// An empty list removes every extra source.
	none := []netip.Prefix{}
	if _, _, err := s.UpdateSettings(ctx, SettingsPatch{AdminAllowed: &none}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Settings(ctx); len(got.AdminAllowed) != 0 {
		t.Fatalf("got %v", got.AdminAllowed)
	}
}
