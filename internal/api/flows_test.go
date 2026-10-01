package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/stuffam/drawbridge/internal/auth"
	"github.com/stuffam/drawbridge/internal/firewall"
	"github.com/stuffam/drawbridge/internal/keys"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/reconcile"
	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/views"
	"github.com/stuffam/drawbridge/internal/wg"
)

type memFirewall struct{ rev string }

func (f *memFirewall) Apply(_ context.Context, rs firewall.Ruleset) error {
	f.rev = rs.Revision
	return nil
}
func (f *memFirewall) Remove(context.Context) error                   { f.rev = ""; return nil }
func (f *memFirewall) Revision(context.Context) (string, bool, error) { return f.rev, f.rev != "", nil }

func newService(t *testing.T) *service.Service {
	t.Helper()
	ctx := context.Background()
	sealer, err := keys.NewSealer(bytes.Repeat([]byte{9}, keys.SecretSize))
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
	return &service.Service{
		Store:   st,
		Rec:     rec,
		WG:      backend,
		Log:     slog.New(slog.DiscardHandler),
		Hasher:  auth.NewHasher(auth.Params{Memory: 64, Time: 1, Threads: 1}),
		Limiter: auth.NewLimiter(),
	}
}

// browser is a client with a cookie jar, like the web app in a browser.
type browser struct {
	t    *testing.T
	srv  *httptest.Server
	http *http.Client
}

func newBrowser(t *testing.T, srv *httptest.Server) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	// srv.Client() is shared; each browser needs its own cookie jar.
	c := *srv.Client()
	c.Jar = jar
	return &browser{t: t, srv: srv, http: &c}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decoding %s: %v", r.body, err)
	}
}

func (r response) errorText() string {
	var e views.Error
	_ = json.Unmarshal(r.body, &e)
	return e.Error
}

// do sends a request the way the web app does: JSON, with the CSRF header.
func (b *browser) do(method, path string, body any, headers ...string) response {
	b.t.Helper()
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			b.t.Fatal(err)
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, b.srv.URL+path, rd)
	if err != nil {
		b.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(csrfHeader, "1")
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := b.http.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: data}
}

func (b *browser) expect(want int, method, path string, body any, headers ...string) response {
	b.t.Helper()
	r := b.do(method, path, body, headers...)
	if r.status != want {
		b.t.Fatalf("%s %s: status %d, want %d; body %s", method, path, r.status, want, r.body)
	}
	return r
}

func newServer(t *testing.T, svc *service.Service) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(New(Options{UI: builtUI, Service: svc}))
	t.Cleanup(srv.Close)
	return srv
}

// loggedIn sets up the admin account and returns a browser logged in as it.
func loggedIn(t *testing.T, svc *service.Service, srv *httptest.Server) (*browser, string) {
	t.Helper()
	password, err := svc.CreateAdmin(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowser(t, srv)
	b.expect(http.StatusOK, "POST", "/api/auth/login", views.LoginRequest{Username: "admin", Password: password})
	return b, password
}

func TestSetupFlow(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	b := newBrowser(t, srv)

	var status views.SetupStatus
	b.expect(http.StatusOK, "GET", "/api/setup", nil).decode(t, &status)
	if !status.Needed {
		t.Fatal("setup isn't needed on a new server")
	}
	token, err := svc.SetupToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req := views.SetupRequest{Token: token, Username: "admin", Password: "a long password"}

	b.expect(http.StatusForbidden, "POST", "/api/setup", req, csrfHeader, "")
	bad := req
	bad.Token = "WRONG"
	if r := b.expect(http.StatusForbidden, "POST", "/api/setup", bad); !strings.Contains(r.errorText(), "setup token") {
		t.Fatalf("wrong token: %s", r.body)
	}
	short := req
	short.Password = "short"
	b.expect(http.StatusBadRequest, "POST", "/api/setup", short)

	r := b.expect(http.StatusCreated, "POST", "/api/setup", req)
	var me views.Me
	r.decode(t, &me)
	if me.User.Username != "admin" || !me.Session.Current {
		t.Fatalf("setup response %+v", me)
	}
	cookie := r.header.Get("Set-Cookie")
	for _, attr := range []string{sessionCookie + "=", "Path=/", "HttpOnly", "Secure", "SameSite=Strict", "Expires="} {
		if !strings.Contains(cookie, attr) {
			t.Errorf("Set-Cookie %q lacks %s", cookie, attr)
		}
	}
	if strings.Contains(cookie, "Domain=") {
		t.Errorf("Set-Cookie %q has a Domain; __Host- cookies mustn't", cookie)
	}

	// Setup logged the browser in.
	b.expect(http.StatusOK, "GET", "/api/auth/me", nil)
	b.expect(http.StatusOK, "GET", "/api/setup", nil).decode(t, &status)
	if status.Needed {
		t.Fatal("setup is still needed")
	}
	b.expect(http.StatusConflict, "POST", "/api/setup", req)
}

func TestEverythingButLoginNeedsASession(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	b := newBrowser(t, srv)
	for _, rt := range routes {
		path := strings.ReplaceAll(rt.pattern, "{id}", "x")
		r := b.do(rt.method, path, nil)
		switch {
		case rt.public && r.status == http.StatusUnauthorized:
			t.Errorf("%s %s: 401, but it's public", rt.method, rt.pattern)
		case !rt.public && r.status != http.StatusUnauthorized:
			t.Errorf("%s %s without a session: status %d, want 401", rt.method, rt.pattern, r.status)
		}
	}
	// A made-up cookie is no better, and the browser is told to drop it.
	b.http.Jar.SetCookies(mustURL(t, srv.URL), []*http.Cookie{{Name: sessionCookie, Value: "forged"}})
	r := b.expect(http.StatusUnauthorized, "GET", "/api/clients", nil)
	if !strings.Contains(r.header.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("a bad cookie wasn't cleared: %q", r.header.Get("Set-Cookie"))
	}
}

func TestLoginFailuresAndRateLimit(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	_, password := loggedIn(t, svc, srv)
	b := newBrowser(t, srv)

	for i := range 6 {
		r := b.do("POST", "/api/auth/login", views.LoginRequest{Username: "admin", Password: "guess"})
		if r.status != http.StatusUnauthorized || r.errorText() != "wrong username or password" {
			t.Fatalf("guess %d: status %d, body %s", i+1, r.status, r.body)
		}
	}
	r := b.expect(http.StatusTooManyRequests, "POST", "/api/auth/login", views.LoginRequest{Username: "admin", Password: password})
	if r.header.Get("Retry-After") != "2" {
		t.Fatalf("Retry-After %q, want 2", r.header.Get("Retry-After"))
	}
	// An unknown username gets the same answer as a wrong password.
	other := newBrowser(t, srv)
	svc.Limiter.Succeed(auth.SourceKey(netip.MustParseAddr("127.0.0.1")))
	if r := other.do("POST", "/api/auth/login", views.LoginRequest{Username: "nobody", Password: "guess"}); r.status != http.StatusUnauthorized ||
		r.errorText() != "wrong username or password" {
		t.Fatalf("unknown user: %d %s", r.status, r.body)
	}
}

func TestCSRFChecks(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)
	add := views.NewClientRequest{Name: "phone"}

	b.expect(http.StatusForbidden, "POST", "/api/clients", add, csrfHeader, "")
	b.expect(http.StatusForbidden, "POST", "/api/clients", add, "Origin", "https://evil.example")
	b.expect(http.StatusForbidden, "POST", "/api/clients", add, "Origin", "null")
	b.expect(http.StatusForbidden, "POST", "/api/clients", add, "Sec-Fetch-Site", "cross-site")
	b.expect(http.StatusForbidden, "POST", "/api/clients", add, "Sec-Fetch-Site", "same-site")
	b.expect(http.StatusUnsupportedMediaType, "POST", "/api/clients", add, "Content-Type", "text/plain")
	b.expect(http.StatusUnsupportedMediaType, "POST", "/api/clients", add, "Content-Type", "application/x-www-form-urlencoded")

	// What the web app sends passes.
	b.expect(http.StatusCreated, "POST", "/api/clients", add, "Origin", srv.URL, "Sec-Fetch-Site", "same-origin")
	// Reads need no header.
	b.expect(http.StatusOK, "GET", "/api/clients", nil, csrfHeader, "")
}

func TestClientAndServerLifecycle(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)

	var list []views.ClientView
	b.expect(http.StatusOK, "GET", "/api/clients", nil).decode(t, &list)
	if len(list) != 0 {
		t.Fatalf("clients on a new server: %+v", list)
	}

	r := b.expect(http.StatusCreated, "POST", "/api/clients", views.NewClientRequest{Name: "Alex's iPhone"})
	var created views.ClientResult
	r.decode(t, &created)
	c := created.Client
	if c.ID == "" || c.IPv4.String() != "10.8.0.2" || !c.IPv6.IsValid() || created.Warning != "" {
		t.Fatalf("created %+v", created)
	}
	if loc := r.header.Get("Location"); loc != "/api/clients/"+c.ID {
		t.Fatalf("Location %q", loc)
	}
	b.expect(http.StatusConflict, "POST", "/api/clients", views.NewClientRequest{Name: "alex's iphone"})
	b.expect(http.StatusBadRequest, "POST", "/api/clients", views.NewClientRequest{Name: "bad\nname"})
	b.expect(http.StatusBadRequest, "POST", "/api/clients", map[string]string{"name": "x", "admin": "true"})

	var status views.ServerStatus
	b.expect(http.StatusOK, "GET", "/api/server/status", nil).decode(t, &status)
	if !status.TunnelUp || status.Clients != 1 || status.Paused != 0 {
		t.Fatalf("status %+v", status)
	}

	path := "/api/clients/" + c.ID
	var got views.ClientView
	b.expect(http.StatusOK, "GET", path, nil).decode(t, &got)
	if got.Name != "Alex's iPhone" || got.Peer == nil {
		t.Fatalf("GET client %+v", got)
	}
	// Clients are addressed by ID, not by name.
	b.expect(http.StatusNotFound, "GET", "/api/clients/Alex's%20iPhone", nil)

	name := "Pixel"
	b.expect(http.StatusOK, "PATCH", path, views.ClientPatch{Name: &name}).decode(t, &created)
	if created.Client.Name != "Pixel" || created.Client.ID != c.ID {
		t.Fatalf("renamed %+v", created.Client)
	}

	b.expect(http.StatusOK, "POST", path+"/pause", nil).decode(t, &created)
	if created.Client.Enabled {
		t.Fatal("paused client is enabled")
	}
	var paused views.ClientView
	b.expect(http.StatusOK, "GET", path, nil).decode(t, &paused)
	if paused.Peer != nil {
		t.Fatal("a paused client is still in the tunnel")
	}
	b.expect(http.StatusOK, "POST", path+"/resume", nil)

	// The config needs the endpoint.
	b.expect(http.StatusConflict, "GET", path+"/config", nil)
	host := "vpn.example.com"
	var settings views.SettingsResult
	b.expect(http.StatusOK, "PATCH", "/api/server", views.SettingsPatch{EndpointHost: &host}).decode(t, &settings)
	if settings.Settings.Endpoint != "vpn.example.com:51820" {
		t.Fatalf("settings %+v", settings)
	}
	mtu := 900
	b.expect(http.StatusBadRequest, "PATCH", "/api/server", views.SettingsPatch{MTU: &mtu})

	r = b.expect(http.StatusOK, "GET", path+"/config", nil)
	if !strings.HasPrefix(string(r.body), "[Interface]\n") || !strings.Contains(string(r.body), "Endpoint = vpn.example.com:51820") {
		t.Fatalf("config %s", r.body)
	}
	for header, want := range map[string]string{
		"Content-Type":        "text/plain; charset=utf-8",
		"Content-Disposition": "attachment; filename=Pixel.conf",
		"Cache-Control":       "no-store",
	} {
		if got := r.header.Get(header); got != want {
			t.Errorf("config %s = %q, want %q", header, got, want)
		}
	}

	var server views.SettingsView
	b.expect(http.StatusOK, "GET", "/api/server", nil).decode(t, &server)
	if server.EndpointHost != "vpn.example.com" || server.PublicKey == "" {
		t.Fatalf("server %+v", server)
	}
	if strings.Contains(strings.ToLower(string(b.do("GET", "/api/server", nil).body)), "private") {
		t.Fatal("GET /api/server shows a private key")
	}

	b.expect(http.StatusOK, "DELETE", path, nil)
	b.expect(http.StatusNotFound, "GET", path, nil)
	b.expect(http.StatusNotFound, "POST", path+"/pause", nil)

	// Every change is in the event log, attributed to the admin at this address.
	var events []views.EventView
	b.expect(http.StatusOK, "GET", "/api/events?client="+c.ID, nil).decode(t, &events)
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
		if e.Actor != "admin" || e.Via != service.ViaWeb || e.SourceIP != "127.0.0.1" {
			t.Errorf("event %+v: want admin, via web, from 127.0.0.1", e)
		}
	}
	want := "client.deleted client.config_viewed client.resumed client.paused client.renamed client.added"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("events %v, want %s", kinds, want)
	}
	b.expect(http.StatusOK, "GET", "/api/events?category=admin&limit=2", nil).decode(t, &events)
	if len(events) != 2 {
		t.Fatalf("limit=2 returned %d events", len(events))
	}
	b.expect(http.StatusBadRequest, "GET", "/api/events?limit=9999", nil)
}

func TestClientJSONCarriesTheOpenSession(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)

	var created views.ClientResult
	b.expect(http.StatusCreated, "POST", "/api/clients", views.NewClientRequest{Name: "phone"}).decode(t, &created)
	path := "/api/clients/" + created.Client.ID

	var got views.ClientView
	b.expect(http.StatusOK, "GET", path, nil).decode(t, &got)
	if got.Peer == nil || !got.Peer.SessionStartedAt.IsZero() || got.Peer.SessionReceiveBytes != 0 {
		t.Fatalf("a session before any handshake: %+v", got.Peer)
	}

	pub, err := wgtypes.ParseKey(got.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	svc.WG.(*wg.Fake).SetHandshake("wg0", pub, wg.Peer{
		LastHandshake: time.Now(), ReceiveBytes: 1000, SendBytes: 500,
	})
	svc.TrackConnections(context.Background())

	b.expect(http.StatusOK, "GET", path, nil).decode(t, &got)
	if got.Peer.ReceiveBytes != 1000 || got.Peer.SendBytes != 500 {
		t.Fatalf("all-time totals: %+v", got.Peer)
	}
	if got.Peer.SessionStartedAt.IsZero() || got.Peer.SessionReceiveBytes != 0 || got.Peer.SessionSendBytes != 0 {
		t.Fatalf("session fields right after the first handshake: %+v", got.Peer)
	}
}

func TestSessionsAndLogout(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	laptop, password := loggedIn(t, svc, srv)
	phone := newBrowser(t, srv)
	phone.expect(http.StatusOK, "POST", "/api/auth/login", views.LoginRequest{Username: "admin", Password: password},
		"User-Agent", "Phone")

	var sessions []views.SessionView
	laptop.expect(http.StatusOK, "GET", "/api/auth/sessions", nil).decode(t, &sessions)
	if len(sessions) != 2 {
		t.Fatalf("%d sessions, want 2", len(sessions))
	}
	var phoneID string
	for _, s := range sessions {
		if s.UserAgent == "Phone" {
			phoneID = s.ID
			if s.Current {
				t.Error("the phone's session is marked current in the laptop's list")
			}
		}
	}
	laptop.expect(http.StatusNoContent, "DELETE", "/api/auth/sessions/"+phoneID, nil)
	phone.expect(http.StatusUnauthorized, "GET", "/api/auth/me", nil)
	laptop.expect(http.StatusNotFound, "DELETE", "/api/auth/sessions/"+phoneID, nil)

	// Changing the password ends the other sessions, but not this one.
	phone.expect(http.StatusOK, "POST", "/api/auth/login", views.LoginRequest{Username: "admin", Password: password})
	laptop.expect(http.StatusBadRequest, "POST", "/api/auth/password",
		views.PasswordChange{CurrentPassword: "wrong", NewPassword: "a new long password"})
	laptop.expect(http.StatusNoContent, "POST", "/api/auth/password",
		views.PasswordChange{CurrentPassword: password, NewPassword: "a new long password"})
	phone.expect(http.StatusUnauthorized, "GET", "/api/auth/me", nil)
	laptop.expect(http.StatusOK, "GET", "/api/auth/me", nil)

	r := laptop.expect(http.StatusNoContent, "POST", "/api/auth/logout", nil)
	if !strings.Contains(r.header.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout didn't clear the cookie: %q", r.header.Get("Set-Cookie"))
	}
	laptop.expect(http.StatusUnauthorized, "GET", "/api/auth/me", nil)
}

func TestRequestBodies(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)

	big := map[string]string{"name": strings.Repeat("x", 1<<17)}
	b.expect(http.StatusBadRequest, "POST", "/api/clients", big)
	req, _ := http.NewRequest("POST", srv.URL+"/api/clients", strings.NewReader(`{"name":"a"}{"name":"b"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, "1")
	resp, err := b.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("two JSON values: status %d, want 400", resp.StatusCode)
	}
}

func TestInternalErrorsAreNotShown(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)
	_ = svc.Store.Close() // every query now fails
	r := b.expect(http.StatusInternalServerError, "GET", "/api/auth/me", nil)
	if strings.Contains(r.errorText(), "sql") || !strings.Contains(r.errorText(), "journal") {
		t.Fatalf("500 body %s", r.body)
	}
}

func TestAllowlist(t *testing.T) {
	allowed := []netip.Prefix{netip.MustParsePrefix("192.168.4.0/22"), netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("10.8.0.0/24")}
	h := New(Options{UI: builtUI, Allowed: func(context.Context) []netip.Prefix { return allowed }})
	for remote, want := range map[string]int{
		"192.168.4.20:50000":          http.StatusOK,
		"10.8.0.2:50000":              http.StatusOK,
		"[fe80::1%eth0]:50000":        http.StatusOK,
		"[::ffff:192.168.4.20]:50000": http.StatusOK,
		"203.0.113.5:50000":           http.StatusForbidden,
		"[2a00:1450::1]:50000":        http.StatusForbidden,
		"127.0.0.1:50000":             http.StatusForbidden, // not in this list
		"garbage":                     http.StatusForbidden,
	} {
		for _, target := range []string{"/healthz", "/", "/api/version"} {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.RemoteAddr = remote
			// Headers a proxy would add don't count.
			req.Header.Set("X-Forwarded-For", "192.168.4.20")
			req.Header.Set("X-Real-IP", "192.168.4.20")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != want {
				t.Errorf("%s from %s: status %d, want %d", target, remote, rec.Code, want)
			}
		}
	}
}

func TestTrafficAndSessionHistoryEndpoints(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)

	r := b.expect(http.StatusCreated, "POST", "/api/clients", views.NewClientRequest{Name: "phone"})
	var created views.ClientResult
	r.decode(t, &created)
	id := created.Client.ID
	path := "/api/clients/" + id

	// Seed traffic and session history directly, rather than waiting on the real
	// sampler: this test is about the API surface, which conntrack_test.go and
	// traffic_test.go already cover.
	ctx := context.Background()
	// The API answers with a window ending now, so seed inside it.
	t0 := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute)
	if err := svc.Store.InsertTraffic(ctx, []store.TrafficSample{
		{ClientID: id, Resolution: store.ResolutionRaw, BucketStart: t0, RxBytes: 100, TxBytes: 50},
		{ClientID: id, Resolution: store.ResolutionRaw, BucketStart: t0.Add(time.Minute), RxBytes: 200, TxBytes: 75},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store.OpenClientSession(ctx, id, "203.0.113.5:51820", 0, 0); err != nil {
		t.Fatal(err)
	}

	var samples []views.TrafficSampleView
	b.expect(http.StatusOK, "GET", path+"/traffic", nil).decode(t, &samples)
	if len(samples) != 2 || samples[0].ReceiveBytes != 100 || samples[1].SendBytes != 75 {
		t.Fatalf("client traffic %+v", samples)
	}
	b.expect(http.StatusOK, "GET", path+"/traffic?range=24h", nil).decode(t, &samples)
	if len(samples) != 2 {
		t.Fatalf("client traffic range=24h %+v", samples)
	}
	// The seeded samples are two hours old: inside 12h, outside 1h. Nothing has polled yet, so
	// the 1 minute range, which is served from memory, is empty rather than an error.
	b.expect(http.StatusOK, "GET", path+"/traffic?range=12h", nil).decode(t, &samples)
	if len(samples) != 2 {
		t.Fatalf("client traffic range=12h %+v", samples)
	}
	b.expect(http.StatusOK, "GET", path+"/traffic?range=1h", nil).decode(t, &samples)
	if len(samples) != 0 {
		t.Fatalf("client traffic range=1h %+v, want none", samples)
	}
	b.expect(http.StatusOK, "GET", path+"/traffic?range=1m", nil).decode(t, &samples)
	if len(samples) != 0 {
		t.Fatalf("client traffic range=1m %+v, want none", samples)
	}
	b.expect(http.StatusOK, "GET", path+"/traffic?range=30d", nil).decode(t, &samples)
	if len(samples) == 0 {
		t.Fatal("client traffic range=30d is empty, want the seeded hour")
	}
	b.expect(http.StatusBadRequest, "GET", path+"/traffic?range=30m", nil)
	b.expect(http.StatusNotFound, "GET", "/api/clients/no-such-id/traffic", nil)
	b.expect(http.StatusNotFound, "GET", "/api/clients/no-such-id/traffic?range=1m", nil)

	var total []views.TrafficSampleView
	b.expect(http.StatusOK, "GET", "/api/traffic", nil).decode(t, &total)
	if len(total) != 2 || total[0].ReceiveBytes != 100 {
		t.Fatalf("total traffic %+v", total)
	}
	b.expect(http.StatusOK, "GET", "/api/traffic?range=1m", nil).decode(t, &total)
	if len(total) != 0 {
		t.Fatalf("total traffic range=1m %+v, want none", total)
	}
	b.expect(http.StatusBadRequest, "GET", "/api/traffic?range=30m", nil)

	// One series per client, for the charts page.
	var history views.TrafficHistoryView
	b.expect(http.StatusOK, "GET", "/api/traffic/clients?range=12h", nil).decode(t, &history)
	if history.StepSeconds != 60 || len(history.Clients) != 1 || history.Clients[0].ID != id ||
		len(history.Clients[0].Samples) != 2 || history.Clients[0].Samples[1].ReceiveBytes != 200 {
		t.Fatalf("traffic by client %+v", history)
	}
	if history.Until.After(time.Now()) || time.Since(history.Until) > time.Minute+time.Minute {
		t.Fatalf("until %v is not just behind now", history.Until)
	}
	// 1m is the live range: nothing has polled yet, so the client is there with no samples.
	b.expect(http.StatusOK, "GET", "/api/traffic/clients?range=1m", nil).decode(t, &history)
	if history.StepSeconds != 5 || len(history.Clients) != 1 || history.Clients[0].ID != id || len(history.Clients[0].Samples) != 0 {
		t.Fatalf("live traffic by client %+v", history)
	}
	b.expect(http.StatusBadRequest, "GET", "/api/traffic/clients?range=30m", nil)

	var sessions []views.ClientSessionView
	b.expect(http.StatusOK, "GET", path+"/sessions", nil).decode(t, &sessions)
	if len(sessions) != 1 || sessions[0].EndedAt != nil || sessions[0].Endpoint != "203.0.113.5:51820" {
		t.Fatalf("client sessions %+v", sessions)
	}
	b.expect(http.StatusBadRequest, "GET", path+"/sessions?limit=0", nil)
	b.expect(http.StatusNotFound, "GET", "/api/clients/no-such-id/sessions", nil)
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// A fresh server points clients at a public resolver, since nothing may answer on the VPN
// addresses. The check finds out, and the admin's choice lands in the settings and the
// event log.
func TestDNSCheckAndChoice(t *testing.T) {
	svc := newService(t)
	// A resolver that answers on IPv4 only, as a service bound to 0.0.0.0 would.
	svc.DNSProbe = func(_ context.Context, a netip.Addr) service.DNSProbe {
		return service.DNSProbe{Answered: a.Is4(), Detail: "probed " + a.String()}
	}
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)

	var settings views.SettingsView
	b.expect(http.StatusOK, "GET", "/api/server", nil).decode(t, &settings)
	if want := model.PublicDNS(true); !slices.Equal(settings.DNS, want) {
		t.Fatalf("a new server's DNS = %v, want the public resolvers %v", settings.DNS, want)
	}

	var check views.DNSCheck
	b.expect(http.StatusOK, "GET", "/api/server/dns-check", nil).decode(t, &check)
	if len(check.Results) != 2 || !check.Results[0].Answered || check.Results[1].Answered ||
		!check.Results[0].Address.Is4() || !check.Results[1].Address.Is6() {
		t.Fatalf("results %+v, want IPv4 answered and IPv6 not", check.Results)
	}
	if !slices.Equal(check.Usable, []netip.Addr{settings.IPv4Address}) {
		t.Fatalf("usable %v, want only %v", check.Usable, settings.IPv4Address)
	}

	// The wizard saves the usable addresses as the clients' DNS.
	res := b.expect(http.StatusOK, "PATCH", "/api/server", views.SettingsPatch{DNS: &check.Usable})
	var patched views.SettingsResult
	res.decode(t, &patched)
	if !slices.Equal(patched.Settings.DNS, check.Usable) {
		t.Fatalf("saved DNS %v, want %v", patched.Settings.DNS, check.Usable)
	}
	var events []views.EventView
	b.expect(http.StatusOK, "GET", "/api/events?category=admin&limit=1", nil).decode(t, &events)
	if len(events) != 1 || events[0].Kind != "server.settings_changed" || !strings.Contains(events[0].Data["dns"], "→ [10.8.0.1]") {
		t.Fatalf("events %+v, want a settings change that records the DNS change", events)
	}

	// A client's config carries what the admin chose.
	host := "vpn.example.com"
	b.expect(http.StatusOK, "PATCH", "/api/server", views.SettingsPatch{EndpointHost: &host})
	var created views.ClientResult
	b.expect(http.StatusCreated, "POST", "/api/clients", views.NewClientRequest{Name: "phone"}).decode(t, &created)
	cfg := b.expect(http.StatusOK, "GET", "/api/clients/"+created.Client.ID+"/config", nil)
	if !strings.Contains(string(cfg.body), "\nDNS = 10.8.0.1\n") {
		t.Fatalf("client config:\n%s", cfg.body)
	}
}
