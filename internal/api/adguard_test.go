package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/adguard/adguardtest"
	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/views"
)

func sp(s string) *string { return &s }

func TestAdGuardConnectionFlow(t *testing.T) {
	svc := newService(t)
	svc.DNSProbe = func(_ context.Context, a netip.Addr) service.DNSProbe {
		return service.DNSProbe{Answered: a.Is4(), Detail: "probed " + a.String()}
	}
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)
	fake := adguardtest.New(t, "drawbridge", "s3cret-pass")

	// Nothing is saved at first, and the usual local address is there to start from.
	var conn views.AdGuardConnection
	b.expect(http.StatusOK, "GET", "/api/integrations/adguard", nil).decode(t, &conn)
	if conn.Configured || conn.BaseURL != "http://127.0.0.1:3000/control" || conn.HasPassword {
		t.Fatalf("a new server's connection = %+v", conn)
	}

	// Saving needs the CSRF header, like every change.
	b.expect(http.StatusForbidden, "PUT", "/api/integrations/adguard",
		views.AdGuardRequest{BaseURL: sp(fake.URL())}, csrfHeader, "")

	// Save it, and read it back. The password goes in, and never comes out.
	save := b.expect(http.StatusOK, "PUT", "/api/integrations/adguard", views.AdGuardRequest{
		BaseURL: sp(fake.URL()), Username: sp("drawbridge"), Password: sp("s3cret-pass"),
	})
	save.decode(t, &conn)
	if !conn.Configured || conn.BaseURL != fake.URL() || conn.Username != "drawbridge" || !conn.HasPassword {
		t.Fatalf("saved connection = %+v", conn)
	}
	got := b.expect(http.StatusOK, "GET", "/api/integrations/adguard", nil)
	for name, body := range map[string][]byte{"the save": save.body, "the read": got.body} {
		if strings.Contains(string(body), "s3cret-pass") {
			t.Errorf("the password is in %s: %s", name, body)
		}
	}
	// And never in the event log, which the admin reads.
	events := b.expect(http.StatusOK, "GET", "/api/events?kind=integration.adguard_changed", nil)
	if strings.Contains(string(events.body), "s3cret-pass") || !strings.Contains(string(events.body), `"password":"set"`) {
		t.Errorf("the event log: %s", events.body)
	}

	// A change of address that doesn't bring the password is refused, and nothing changes.
	r := b.expect(http.StatusBadRequest, "PUT", "/api/integrations/adguard", views.AdGuardRequest{BaseURL: sp("http://192.0.2.1:3000")})
	if !strings.Contains(r.errorText(), "enter the password again") {
		t.Errorf("error = %q", r.errorText())
	}
	b.expect(http.StatusBadRequest, "PUT", "/api/integrations/adguard", views.AdGuardRequest{BaseURL: sp("not a url")})
	b.expect(http.StatusBadRequest, "PUT", "/api/integrations/adguard", map[string]any{"base_url": fake.URL(), "extra": 1})

	// The test without a body checks the saved connection, password included.
	var res views.AdGuardTest
	b.expect(http.StatusOK, "POST", "/api/integrations/adguard/test", nil).decode(t, &res)
	if !res.OK || res.Version == "" || !res.Running || res.QueryLog == nil || !res.QueryLog.Enabled ||
		len(res.DNS) != 2 || !res.DNS[0].Answered || res.DNS[1].Answered || len(res.Warnings) != 0 {
		t.Fatalf("test result = %+v", res)
	}
	// A test of a different address, with no password, is refused before anything is sent.
	var asked atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked.Add(1) }))
	t.Cleanup(elsewhere.Close)
	b.expect(http.StatusBadRequest, "POST", "/api/integrations/adguard/test", views.AdGuardRequest{BaseURL: sp(elsewhere.URL)})
	if asked.Load() != 0 {
		t.Error("the saved password could have gone to another server")
	}
	// A wrong password is an answer, not an error.
	b.expect(http.StatusOK, "POST", "/api/integrations/adguard/test", views.AdGuardRequest{Password: sp("wrong")}).decode(t, &res)
	if res.OK || !res.Refused || !strings.Contains(res.Error, "refused the account") {
		t.Errorf("a wrong password: %+v", res)
	}
	// So is a server that isn't there.
	fake.SetDown(true)
	b.expect(http.StatusOK, "POST", "/api/integrations/adguard/test", views.AdGuardRequest{}).decode(t, &res)
	if res.OK || res.Refused || !strings.Contains(res.Error, "can't reach AdGuard Home") || len(res.DNS) != 2 {
		t.Errorf("an unreachable AdGuard Home: %+v", res)
	}

	// Removing it forgets the password too.
	b.expect(http.StatusOK, "DELETE", "/api/integrations/adguard", nil).decode(t, &conn)
	if conn.Configured || conn.HasPassword || conn.Username != "" {
		t.Errorf("after the delete: %+v", conn)
	}
	b.expect(http.StatusOK, "DELETE", "/api/integrations/adguard", nil)
	var list []views.EventView
	b.expect(http.StatusOK, "GET", "/api/events?category=admin&limit=2", nil).decode(t, &list)
	if len(list) != 2 || list[0].Kind != "integration.adguard_removed" {
		t.Errorf("events = %+v, want the removal recorded once", list)
	}
}

func bp(b bool) *bool { return &b }

func TestAdGuardSyncFlow(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)
	fake := adguardtest.New(t, "drawbridge", "s3cret-pass")
	// An AdGuard Home client of the admin's, with a name a Drawbridge client is about to want.
	if err := fake.AddRaw(`{"name":"tablet","ids":["192.0.2.60"]}`); err != nil {
		t.Fatal(err)
	}

	// Saved, and not on: nothing is synced, and the usual defaults show.
	var conn views.AdGuardConnection
	b.expect(http.StatusOK, "PUT", "/api/integrations/adguard", views.AdGuardRequest{
		BaseURL: sp(fake.URL()), Username: sp("drawbridge"), Password: sp("s3cret-pass"),
	}).decode(t, &conn)
	if conn.Enabled || !conn.SyncNames || conn.Sync.State != "off" || conn.Sync.Conflicts == nil {
		t.Fatalf("saved connection = %+v", conn)
	}
	var sync views.AdGuardSync
	b.expect(http.StatusOK, "POST", "/api/integrations/adguard/sync", nil).decode(t, &sync)
	if sync.State != "off" || len(fake.Calls()) != 0 {
		t.Fatalf("a sync while off = %+v, calls %v", sync, fake.Calls())
	}

	// Turning it on takes no password, and the clients are named by a pass.
	b.expect(http.StatusCreated, "POST", "/api/clients", views.NewClientRequest{Name: "phone"})
	b.expect(http.StatusCreated, "POST", "/api/clients", views.NewClientRequest{Name: "tablet"})
	b.expect(http.StatusOK, "PUT", "/api/integrations/adguard", views.AdGuardRequest{Enabled: bp(true)}).decode(t, &conn)
	if !conn.Enabled || conn.Sync.State != "pending" || conn.Sync.LastSync != nil {
		t.Fatalf("turned on = %+v", conn)
	}
	b.expect(http.StatusOK, "POST", "/api/integrations/adguard/sync", nil).decode(t, &sync)
	if sync.State != "ok" || sync.Synced != 1 || sync.LastSync == nil || len(sync.Conflicts) != 1 ||
		sync.Conflicts[0].Client != "tablet" || !strings.Contains(sync.Conflicts[0].Reason, "didn't make") {
		t.Fatalf("sync = %+v", sync)
	}
	if _, ok := fake.Client("phone"); !ok {
		t.Error("the client wasn't named in AdGuard Home")
	}
	if p, _ := fake.Client("tablet"); len(p.IDs) != 1 {
		t.Errorf("the admin's own client was changed: %+v", p)
	}
	b.expect(http.StatusOK, "GET", "/api/integrations/adguard", nil).decode(t, &conn)
	if conn.Sync.State != "ok" || conn.Sync.Synced != 1 {
		t.Errorf("GET shows %+v", conn.Sync)
	}

	// What the sync changed is in the log, as the daemon's own work, started by the admin.
	var events []views.EventView
	b.expect(http.StatusOK, "GET", "/api/events?kind=integration.adguard_name_added", nil).decode(t, &events)
	if len(events) != 1 || events[0].ClientName != "phone" || events[0].Category != "system" || events[0].Actor != "admin" {
		t.Errorf("events = %+v", events)
	}
	// And the switches are in the connection's own change events.
	b.expect(http.StatusOK, "GET", "/api/events?kind=integration.adguard_changed&limit=1", nil).decode(t, &events)
	if len(events) != 1 || events[0].Data["enabled"] != "off → on" {
		t.Errorf("events = %+v", events)
	}
}

func TestClientDNSLogEndpoint(t *testing.T) {
	svc := newService(t)
	srv := newServer(t, svc)
	b, _ := loggedIn(t, svc, srv)
	fake := adguardtest.New(t, "drawbridge", "s3cret-pass")
	var created views.ClientResult
	b.expect(http.StatusCreated, "POST", "/api/clients", views.NewClientRequest{Name: "phone"}).decode(t, &created)
	path := "/api/clients/" + created.Client.ID + "/dns-log"

	// Off until the integration is turned on: an answer, with nothing in it, and nothing asked.
	var log views.DNSLog
	b.expect(http.StatusOK, "GET", path, nil).decode(t, &log)
	if log.State != "off" || log.Queries == nil || log.Addresses == nil || log.Warnings == nil || len(fake.Calls()) != 0 {
		t.Fatalf("log = %+v", log)
	}
	b.expect(http.StatusNotFound, "GET", "/api/clients/nobody/dns-log", nil)
	for _, q := range []string{"0", "201", "-1", "many"} {
		b.expect(http.StatusBadRequest, "GET", path+"?limit="+q, nil)
	}

	b.expect(http.StatusOK, "PUT", "/api/integrations/adguard", views.AdGuardRequest{
		BaseURL: sp(fake.URL()), Username: sp("drawbridge"), Password: sp("s3cret-pass"), Enabled: bp(true), SyncNames: bp(false),
	})
	v4, v6 := created.Client.IPv4.String(), created.Client.IPv6.String()
	now := time.Now().UTC()
	fake.AddQuery(adguardtest.Entry{Time: now.Add(-3 * time.Minute), Client: v4, Domain: "one.example.com", Answers: []string{"192.0.2.1"}})
	fake.AddQuery(adguardtest.Entry{Time: now.Add(-2 * time.Minute), Client: "192.0.2.99", Domain: "someone-else.example.com"})
	fake.AddQuery(adguardtest.Entry{Time: now.Add(-1 * time.Minute), Client: v6, Domain: "ads.example.net", Type: "AAAA", Blocked: true, Rule: "||ads.example.net^"})

	res := b.expect(http.StatusOK, "GET", path, nil)
	res.decode(t, &log)
	if log.State != "ok" || log.AdGuardURL != fake.URL() || len(log.Addresses) != 2 || len(log.Queries) != 2 {
		t.Fatalf("log = %+v", log)
	}
	if q := log.Queries[0]; q.Domain != "ads.example.net" || !q.Blocked || q.Rule != "||ads.example.net^" || q.Type != "AAAA" ||
		q.Address.String() != v6 || q.ElapsedMs != 12.5 || q.Time.IsZero() {
		t.Errorf("the newest = %+v", q)
	}
	if q := log.Queries[1]; q.Domain != "one.example.com" || q.Blocked || q.Rule != "" || len(q.Answers) != 1 || q.Address.String() != v4 {
		t.Errorf("the other = %+v", q)
	}
	if strings.Contains(string(res.body), "s3cret-pass") {
		t.Error("the password is in the response")
	}
	b.expect(http.StatusOK, "GET", path+"?limit=1", nil).decode(t, &log)
	if len(log.Queries) != 1 || log.Queries[0].Domain != "ads.example.net" {
		t.Errorf("with a limit of 1: %+v", log.Queries)
	}

	// An AdGuard Home that can't be read is an answer, too, and it says why.
	fake.SetDown(true)
	b.expect(http.StatusOK, "GET", path, nil).decode(t, &log)
	if log.State != "error" || !strings.Contains(log.Error, "can't reach AdGuard Home") {
		t.Errorf("an unreachable AdGuard Home: %+v", log)
	}
}
