package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/views"
)

// tokenEnv is a logged-in admin, a client, and a browser with no login that can only send a token.
type tokenFixture struct {
	svc      *service.Service
	srv      *httptest.Server
	admin    *browser // logged in
	password string
	clientID string
	token    views.APIToken
	secret   string
}

func newTokenFixture(t *testing.T) *tokenFixture {
	t.Helper()
	svc := newService(t)
	srv := newServer(t, svc)
	admin, password := loggedIn(t, svc, srv)
	host := "vpn.example.com"
	admin.expect(http.StatusOK, "PATCH", "/api/server", views.SettingsPatch{EndpointHost: &host})
	var created views.ClientResult
	admin.expect(http.StatusCreated, "POST", "/api/clients", views.NewClientRequest{Name: "phone"}).decode(t, &created)
	var made views.NewAPITokenResult
	admin.expect(http.StatusCreated, "POST", "/api/auth/tokens",
		views.NewAPITokenRequest{Name: "Homepage", Password: password}).decode(t, &made)
	return &tokenFixture{svc: svc, srv: srv, admin: admin, password: password, clientID: created.Client.ID,
		token: made.Token, secret: made.Secret}
}

// with sends a request from a browser that has nothing but the token.
func (f *tokenFixture) with(t *testing.T, secret, method, path string) response {
	t.Helper()
	return newBrowser(t, f.srv).do(method, path, nil, "Authorization", "Bearer "+secret)
}

func (f *tokenFixture) path(pattern string) string {
	p := strings.ReplaceAll(pattern, "{id}", f.clientID)
	if strings.HasPrefix(pattern, "/api/auth/tokens/") {
		p = "/api/auth/tokens/" + f.token.ID
	}
	return p
}

func TestAPITokenFlow(t *testing.T) {
	f := newTokenFixture(t)
	if !strings.HasPrefix(f.secret, "dbt_") || f.token.Name != "Homepage" || f.token.Scope != "read" ||
		f.token.Prefix != f.secret[:8] || f.token.LastUsedAt != nil {
		t.Fatalf("token = %+v, secret %q", f.token, f.secret)
	}

	// The token reads what the web UI would show the admin, and the same things.
	var status, viaCookie views.ServerStatus
	f.with(t, f.secret, "GET", "/api/server/status").decode(t, &status)
	f.admin.expect(http.StatusOK, "GET", "/api/server/status", nil).decode(t, &viaCookie)
	if status != viaCookie || status.Clients != 1 {
		t.Errorf("status by token = %+v, by cookie = %+v", status, viaCookie)
	}
	var clients []views.ClientView
	f.with(t, f.secret, "GET", "/api/clients").decode(t, &clients)
	if len(clients) != 1 || clients[0].Name != "phone" {
		t.Errorf("clients = %+v", clients)
	}

	// The list has the token, and has used it, but never the secret.
	res := f.admin.expect(http.StatusOK, "GET", "/api/auth/tokens", nil)
	var list []views.APIToken
	res.decode(t, &list)
	if len(list) != 1 || list[0].ID != f.token.ID || list[0].LastUsedAt == nil {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(string(res.body), f.secret) {
		t.Error("the secret is in the list")
	}
	// Nor is it in the log the admin reads.
	events := f.admin.expect(http.StatusOK, "GET", "/api/events?limit=100", nil)
	if strings.Contains(string(events.body), f.secret) || !strings.Contains(string(events.body), "auth.token_created") {
		t.Errorf("events: %s", events.body)
	}

	// Revoking ends it at once.
	f.admin.expect(http.StatusNoContent, "DELETE", "/api/auth/tokens/"+f.token.ID, nil)
	if r := f.with(t, f.secret, "GET", "/api/server/status"); r.status != http.StatusUnauthorized {
		t.Errorf("a revoked token: status %d, want 401", r.status)
	}
	f.admin.expect(http.StatusNotFound, "DELETE", "/api/auth/tokens/"+f.token.ID, nil)
}

// With a valid token, every route either answers (the few a token may read) or is refused with a
// 403 and changes nothing.
func TestAPITokensReachOnlyWhatTheyShould(t *testing.T) {
	f := newTokenFixture(t)
	var before views.SettingsView
	f.admin.expect(http.StatusOK, "GET", "/api/server", nil).decode(t, &before)

	seen := 0
	for _, rt := range routes {
		if rt.public {
			continue
		}
		seen++
		key := rt.method + " " + rt.pattern
		r := f.with(t, f.secret, rt.method, f.path(rt.pattern))
		switch {
		case tokenReadable[key] && r.status != http.StatusOK:
			t.Errorf("%s with a token: status %d, want 200; %s", key, r.status, r.body)
		case !tokenReadable[key] && r.status != http.StatusForbidden:
			t.Errorf("%s with a token: status %d, want 403; %s", key, r.status, r.body)
		}
	}
	if seen < 20 {
		t.Fatalf("only %d routes were checked", seen)
	}

	// Nothing the refused requests asked for happened.
	var after views.SettingsView
	f.admin.expect(http.StatusOK, "GET", "/api/server", nil).decode(t, &after)
	if !slices.Equal(before.DNS, after.DNS) || before.MTU != after.MTU {
		t.Error("a token changed the server settings")
	}
	var clients []views.ClientView
	f.admin.expect(http.StatusOK, "GET", "/api/clients", nil).decode(t, &clients)
	if len(clients) != 1 {
		t.Errorf("%d clients, want the 1 there was", len(clients))
	}
	var list []views.APIToken
	f.admin.expect(http.StatusOK, "GET", "/api/auth/tokens", nil).decode(t, &list)
	if len(list) != 1 {
		t.Errorf("%d tokens, want the 1 there was: a token made or revoked tokens", len(list))
	}
	f.admin.expect(http.StatusOK, "GET", "/api/auth/me", nil) // the admin's own session is intact
}

// What a token can read is on a short list, and this is what can never be on it.
func TestTokenReadableIsShortAndHasNoSecrets(t *testing.T) {
	served := map[string]bool{}
	for _, rt := range routes {
		served[rt.method+" "+rt.pattern] = true
	}
	for key := range tokenReadable {
		method, _, _ := strings.Cut(key, " ")
		if method != http.MethodGet {
			t.Errorf("%s: a token may only read", key)
		}
		if !served[key] {
			t.Errorf("%s is in tokenReadable but isn't a route", key)
		}
	}
	for _, key := range []string{
		"GET /api/clients/{id}/config",  // the client's private key
		"GET /api/clients/{id}/dns-log", // what the client browsed
		"GET /api/events",               // who did what, from where
		"GET /api/stream",               // the events, live
		"GET /api/server",               // the admin's settings
		"GET /api/server/dns-check",     // sends DNS queries
		"GET /api/integrations/adguard", // the account used for AdGuard Home
		"GET /api/auth/me",              // the account
		"GET /api/auth/sessions",        // the admin's logins
		"GET /api/auth/tokens",          // the tokens
		"POST /api/auth/tokens",         // a token can't make a token
		"DELETE /api/auth/tokens/{id}",  // or revoke one
		"POST /api/integrations/adguard/test",
	} {
		if !served[key] {
			t.Errorf("%s isn't a route; update this list", key)
		}
		if tokenReadable[key] {
			t.Errorf("%s is readable by a token", key)
		}
	}
	if len(tokenReadable) > 10 {
		t.Errorf("%d routes are readable by a token; a new one is a security decision, so say so here", len(tokenReadable))
	}
}

func TestInvalidAPITokens(t *testing.T) {
	f := newTokenFixture(t)
	// The same secret with its last character changed to another one, whatever that was.
	offByOne := f.secret[:len(f.secret)-1] + "A"
	if strings.HasSuffix(f.secret, "A") {
		offByOne = f.secret[:len(f.secret)-1] + "B"
	}
	for name, header := range map[string]string{
		"a made-up token":   "Bearer dbt_" + strings.Repeat("A", 43),
		"one character off": "Bearer " + offByOne,
		"no token":          "Bearer",
		"an empty token":    "Bearer ",
		"a session token":   "Bearer " + strings.Repeat("a", 43),
		"another scheme":    "Basic " + f.secret,
		"the bare secret":   f.secret,
	} {
		// A route a token may read, and one it may not: the answer is the same, 401, because
		// the token itself is no good.
		for _, path := range []string{"/api/server/status", "/api/clients/" + f.clientID + "/config"} {
			r := newBrowser(t, f.srv).do("GET", path, nil, "Authorization", header)
			if r.status != http.StatusUnauthorized || !strings.Contains(r.header.Get("WWW-Authenticate"), "Bearer") {
				t.Errorf("%s on %s: status %d, WWW-Authenticate %q; want 401 with Bearer", name, path, r.status, r.header.Get("WWW-Authenticate"))
			}
		}
	}
	// The scheme isn't case-sensitive.
	r := newBrowser(t, f.srv).do("GET", "/api/server/status", nil, "Authorization", "bearer "+f.secret)
	if r.status != http.StatusOK {
		t.Errorf("a lowercase scheme: status %d", r.status)
	}
	// And with no token and no login, it's the login that's missing.
	if r := newBrowser(t, f.srv).do("GET", "/api/server/status", nil); r.status != http.StatusUnauthorized {
		t.Errorf("no credentials: status %d", r.status)
	}
}

// A token isn't made wider by a cookie sent with it, and a bad token isn't saved by one.
func TestATokenIsATokenWhateverElseComesWithIt(t *testing.T) {
	f := newTokenFixture(t)
	config := "/api/clients/" + f.clientID + "/config"
	f.admin.expect(http.StatusOK, "GET", config, nil)
	if r := f.admin.do("GET", config, nil, "Authorization", "Bearer "+f.secret); r.status != http.StatusForbidden {
		t.Errorf("a token sent with a logged-in cookie reached the config: status %d", r.status)
	}
	if r := f.admin.do("GET", "/api/server/status", nil, "Authorization", "Bearer dbt_"+strings.Repeat("A", 43)); r.status != http.StatusUnauthorized {
		t.Errorf("a bad token with a good cookie: status %d, want 401", r.status)
	}
}

func TestMakingAnAPIToken(t *testing.T) {
	f := newTokenFixture(t)
	post := func(body any, headers ...string) response {
		return f.admin.do("POST", "/api/auth/tokens", body, headers...)
	}
	// It's a change, so it needs the header every change needs.
	if r := post(views.NewAPITokenRequest{Name: "Grafana", Password: f.password}, csrfHeader, ""); r.status != http.StatusForbidden {
		t.Errorf("without the CSRF header: status %d", r.status)
	}
	// The password again, and a name.
	for name, req := range map[string]views.NewAPITokenRequest{
		"a wrong password": {Name: "Grafana", Password: "wrong"},
		"no password":      {Name: "Grafana"},
		"no name":          {Password: f.password},
		"a long name":      {Name: strings.Repeat("n", 65), Password: f.password},
	} {
		if r := post(req); r.status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400; %s", name, r.status, r.body)
		}
	}
	if r := post(views.NewAPITokenRequest{Name: "homepage", Password: f.password}); r.status != http.StatusConflict {
		t.Errorf("the name of another token: status %d, want 409", r.status)
	}
	if r := post(map[string]any{"name": "x", "password": f.password, "scope": "admin"}); r.status != http.StatusBadRequest {
		t.Errorf("a scope in the request: status %d, want 400 (there's one scope, and it isn't chosen)", r.status)
	}
	if r := newBrowser(t, f.srv).do("POST", "/api/auth/tokens", views.NewAPITokenRequest{Name: "x", Password: f.password}); r.status != http.StatusUnauthorized {
		t.Errorf("without a login: status %d", r.status)
	}
	var list []views.APIToken
	f.admin.expect(http.StatusOK, "GET", "/api/auth/tokens", nil).decode(t, &list)
	if len(list) != 1 {
		t.Errorf("%d tokens after the refused ones, want 1", len(list))
	}

	// Wrong passwords are limited like a login's.
	r := post(views.NewAPITokenRequest{Name: "Grafana", Password: "wrong"})
	for range 6 {
		r = post(views.NewAPITokenRequest{Name: "Grafana", Password: "wrong"})
	}
	if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Errorf("after many wrong passwords: status %d, Retry-After %q", r.status, r.header.Get("Retry-After"))
	}
}

// An API token is held to the same network limits as everything else.
func TestAPITokensAreHeldToTheAllowlist(t *testing.T) {
	f := newTokenFixture(t)
	h := New(Options{UI: builtUI, Service: f.svc, Allowed: func(context.Context) []netip.Prefix {
		return []netip.Prefix{netip.MustParsePrefix("192.168.4.0/22")}
	}})
	for remote, want := range map[string]int{
		"192.168.4.20:50000": http.StatusOK,
		"203.0.113.5:50000":  http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/server/status", nil)
		req.RemoteAddr = remote
		req.Header.Set("Authorization", "Bearer "+f.secret)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("from %s: status %d, want %d", remote, rec.Code, want)
		}
	}
}

func TestOnlyTheCreateResponseHasTheSecret(t *testing.T) {
	f := newTokenFixture(t)
	for _, path := range []string{"/api/auth/tokens", "/api/auth/me", "/api/auth/sessions", "/api/events", "/api/server", "/api/clients"} {
		r := f.admin.expect(http.StatusOK, "GET", path, nil)
		if strings.Contains(string(r.body), f.secret) {
			t.Errorf("%s has the secret", path)
		}
	}
	// And the one that does says not to keep it.
	var raw map[string]json.RawMessage
	r := f.admin.expect(http.StatusCreated, "POST", "/api/auth/tokens", views.NewAPITokenRequest{Name: "Another", Password: f.password})
	r.decode(t, &raw)
	if _, ok := raw["secret"]; !ok || r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("create response: %s, Cache-Control %q", r.body, r.header.Get("Cache-Control"))
	}
}
