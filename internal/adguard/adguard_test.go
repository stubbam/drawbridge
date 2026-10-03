package adguard_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/adguard"
	"github.com/stuffam/drawbridge/internal/adguard/adguardtest"
)

func TestNormalizeBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:3000/control":    "http://127.0.0.1:3000/control",
		"http://127.0.0.1:3000":            "http://127.0.0.1:3000/control",
		"  http://127.0.0.1:3000/  ":       "http://127.0.0.1:3000/control",
		"https://dns.example.com/control/": "https://dns.example.com/control",
		"http://[2001:db8::53]:3000":       "http://[2001:db8::53]:3000/control",
		"https://example.com/adguard/api/": "https://example.com/adguard/api",
	} {
		got, err := adguard.NormalizeBaseURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "127.0.0.1:3000", "ftp://127.0.0.1/control", "http://", "http://user:pw@127.0.0.1:3000",
		"http://127.0.0.1:3000/control?x=1", "http://127.0.0.1:3000/control#top", "http://127.0.0.1:99999",
		"http://127.0.0.1:0", "file:///etc/passwd", "javascript:alert(1)",
	} {
		if got, err := adguard.NormalizeBaseURL(in); err == nil {
			t.Errorf("NormalizeBaseURL(%q) = %q, want an error", in, got)
		}
	}
}

// A client added with only a name and addresses has filtering off in AdGuard Home. This is the
// trap NewPersistent avoids, shown on the fake that reproduces it.
func TestAddedClientsAreFilteredLikeAnyOther(t *testing.T) {
	fake := adguardtest.New(t, "u", "p")
	c := mustClient(t, fake.URL(), "u", "p")

	if err := fake.AddRaw(`{"name":"bare","ids":["192.0.2.1"]}`); err != nil {
		t.Fatal(err)
	}
	if bare, _ := fake.Client("bare"); bare.UsesGlobalSettings() {
		t.Fatal("the fake should reproduce AdGuard Home turning the global settings off for a bare client")
	}

	if err := c.AddClient(t.Context(), adguard.NewPersistent("named", []string{"192.0.2.2"})); err != nil {
		t.Fatal(err)
	}
	named, _ := fake.Client("named")
	if !named.UsesGlobalSettings() {
		t.Error("a client added through the client doesn't use the global settings")
	}
	if v, _ := named.Field("filtering_enabled"); string(v) != "false" {
		t.Errorf("filtering_enabled = %s; the global settings decide, so this stays at AdGuard Home's default", v)
	}
}

// An update replaces the whole client, so what the client doesn't know, it must send back.
func TestUpdateKeepsWhatItDoesNotKnow(t *testing.T) {
	fake := adguardtest.New(t, "u", "p")
	c := mustClient(t, fake.URL(), "u", "p")
	err := fake.AddRaw(`{"name":"phone","ids":["192.0.2.5","aa:bb:cc:dd:ee:ff"],"tags":["device_phone"],
		"parental_enabled":true,"future_setting":{"a":[1,2,3]},"use_global_settings":true}`)
	if err != nil {
		t.Fatal(err)
	}

	all, err := c.Clients(t.Context())
	if err != nil || len(all) != 1 {
		t.Fatalf("Clients = %v, %v", all, err)
	}
	p := all[0]
	p.Name = "phone-renamed"
	if err := c.UpdateClient(t.Context(), "phone", p); err != nil {
		t.Fatal(err)
	}
	got, ok := fake.Client("phone-renamed")
	if !ok {
		t.Fatal("the rename didn't happen")
	}
	for key, want := range map[string]string{
		"tags": `["device_phone"]`, "parental_enabled": "true", "future_setting": `{"a":[1,2,3]}`,
		"use_global_settings": "true",
	} {
		if v, _ := got.Field(key); string(v) != want {
			t.Errorf("%s = %s after the rename, want %s", key, v, want)
		}
	}
	if !adguard.SameIDs(got.IDs, []string{"192.0.2.5", "AA:BB:CC:DD:EE:FF"}) {
		t.Errorf("IDs = %v; the MAC address the admin added should stay", got.IDs)
	}

	// An update that sends only a name and addresses is the mistake this prevents.
	bare := adguard.Persistent{Name: "phone-renamed", IDs: []string{"192.0.2.5"}}
	if err := c.UpdateClient(t.Context(), "phone-renamed", bare); err != nil {
		t.Fatal(err)
	}
	wiped, _ := fake.Client("phone-renamed")
	if v, _ := wiped.Field("tags"); string(v) != "null" {
		t.Errorf("tags = %s; the fake should replace the whole client, as AdGuard Home does", v)
	}
}

func TestIDsCompareInAnySpelling(t *testing.T) {
	for _, tc := range []struct {
		a, b []string
		want bool
	}{
		{[]string{"192.0.2.1", "2001:db8::1"}, []string{"2001:DB8:0:0:0:0:0:1", "192.0.2.1"}, true},
		{[]string{"MyClient"}, []string{"myclient"}, true},
		{[]string{"::ffff:192.0.2.1"}, []string{"192.0.2.1"}, true},
		{[]string{"192.0.2.1"}, []string{"192.0.2.1", "192.0.2.2"}, false},
		{[]string{"192.0.2.1", "192.0.2.1"}, []string{"192.0.2.1"}, true},
		{nil, nil, true},
	} {
		if got := adguard.SameIDs(tc.a, tc.b); got != tc.want {
			t.Errorf("SameIDs(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	if !adguard.HasID([]string{"2001:db8::1", "192.0.2.7"}, "2001:DB8:0::1") {
		t.Error("HasID missed an address in another spelling")
	}
}

// After failed logins AdGuard Home blocks the caller, and even the right password gets a 401.
func TestRefusedAccountAndTheBlock(t *testing.T) {
	fake := adguardtest.New(t, "u", "right")
	wrong := mustClient(t, fake.URL(), "u", "wrong")

	for range 5 {
		if _, err := wrong.Status(t.Context()); !errors.Is(err, adguard.ErrUnauthorized) {
			t.Fatalf("Status with a wrong password: err = %v, want ErrUnauthorized", err)
		}
	}
	right := mustClient(t, fake.URL(), "u", "right")
	_, err := right.Status(t.Context())
	if !errors.Is(err, adguard.ErrUnauthorized) {
		t.Fatalf("Status while blocked: err = %v, want ErrUnauthorized even with the right password", err)
	}
	if !strings.Contains(err.Error(), "15 minutes") {
		t.Errorf("the message %q should say why a right password can be refused", err)
	}
	fake.Unblock()
	if _, err := right.Status(t.Context()); err != nil {
		t.Errorf("Status after the block ended: %v", err)
	}
}

func TestNoLoginMeansNothingIsSent(t *testing.T) {
	var sawAuth atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
		_, _ = w.Write([]byte(`{"version":"v0.107.79","running":true}`))
	}))
	t.Cleanup(srv.Close)
	st, err := mustClient(t, srv.URL, "", "").Status(t.Context())
	if err != nil || st.Version == "" {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	if sawAuth.Load() {
		t.Error("a client with no username sent an Authorization header")
	}
}

func TestUnreachable(t *testing.T) {
	fake := adguardtest.New(t, "u", "p")
	c := mustClient(t, fake.URL(), "u", "p")
	fake.SetDown(true)
	_, err := c.Status(t.Context())
	var ae *adguard.Error
	if err == nil || errors.As(err, &ae) || !strings.Contains(err.Error(), "can't reach AdGuard Home") {
		t.Errorf("Status on a dead server: err = %v, want \"can't reach\", not an Error from AdGuard Home", err)
	}

	// Nothing listening at all.
	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()
	_, err = mustClient(t, url, "", "").Status(t.Context())
	if err == nil || !strings.Contains(err.Error(), "can't reach AdGuard Home") {
		t.Errorf("Status with nothing listening: err = %v", err)
	}
}

func TestRepliesThatAreNotAdGuardHome(t *testing.T) {
	tooBig := strings.Repeat("x", 9<<20)
	for name, h := range map[string]http.HandlerFunc{
		"a web page": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>hello</html>")) },
		"too large":  func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tooBig)) },
		"a 500 with a long, multi-line message": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, strings.Repeat("y", 500)+"\nsecond line", http.StatusInternalServerError)
		},
	} {
		srv := httptest.NewServer(h)
		_, err := mustClient(t, srv.URL, "u", "p").Status(t.Context())
		srv.Close()
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if len(err.Error()) > 400 || strings.Contains(err.Error(), "second line") {
			t.Errorf("%s: the error should be short, and one line: %q", name, err)
		}
	}
}

// A redirect isn't followed: it wouldn't be AdGuard Home's API, and the password goes with it.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var hit atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Store(true) }))
	t.Cleanup(elsewhere.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	_, err := mustClient(t, srv.URL, "u", "p").Status(t.Context())
	var ae *adguard.Error
	if !errors.As(err, &ae) || ae.Status != http.StatusFound {
		t.Errorf("Status: err = %v, want an Error with status 302", err)
	}
	if hit.Load() {
		t.Error("the client followed a redirect to another host")
	}
}

func at(m int) time.Time {
	return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC).Add(time.Duration(m) * time.Minute)
}

func TestQueriesFromKeepsOnlyTheExactAddress(t *testing.T) {
	fake := adguardtest.New(t, "u", "p")
	c := mustClient(t, fake.URL(), "u", "p")
	// AdGuard Home's search matches part of an address, and part of a domain: 10.8.0.2 also finds
	// 10.8.0.20, 10.8.0.200, and a lookup of a name that contains it.
	fake.AddQuery(adguardtest.Entry{Time: at(1), Client: "10.8.0.2", Domain: "one.example.com"})
	fake.AddQuery(adguardtest.Entry{Time: at(2), Client: "10.8.0.20", Domain: "neighbor.example.com"})
	fake.AddQuery(adguardtest.Entry{Time: at(3), Client: "10.8.0.200", Domain: "neighbor2.example.com"})
	fake.AddQuery(adguardtest.Entry{Time: at(4), Client: "10.8.0.9", Domain: "10.8.0.2.example.com"})
	fake.AddQuery(adguardtest.Entry{Time: at(5), Client: "fd00::2", Domain: "six.example.com", Type: "AAAA"})
	fake.AddQuery(adguardtest.Entry{Time: at(6), Client: "fd00::20", Domain: "neighbor6.example.com"})
	fake.AddQuery(adguardtest.Entry{Time: at(7), Client: "10.8.0.2", Domain: "ads.example.net", Blocked: true, Rule: "||ads.example.net^"})

	got, err := c.QueriesFrom(t.Context(), []netip.Addr{netip.MustParseAddr("10.8.0.2"), netip.MustParseAddr("fd00::2")}, 50)
	if err != nil {
		t.Fatal(err)
	}
	var domains []string
	for _, q := range got {
		domains = append(domains, q.Domain)
	}
	if want := "ads.example.net six.example.com one.example.com"; strings.Join(domains, " ") != want {
		t.Fatalf("domains = %v, want %q (only these two clients, newest first)", domains, want)
	}
	if q := got[0]; !q.Blocked || q.Rule != "||ads.example.net^" || q.Reason != "FilteredBlackList" || q.Answers[0] != "0.0.0.0" {
		t.Errorf("the blocked query = %+v", q)
	}
	if q := got[1]; q.Type != "AAAA" || q.Blocked || q.Client != netip.MustParseAddr("fd00::2") || !q.Time.Equal(at(5)) ||
		q.Elapsed != 12500*time.Microsecond || q.Status != "NOERROR" {
		t.Errorf("the IPv6 query = %+v", q)
	}

	// A limit trims the oldest.
	got, _ = c.QueriesFrom(t.Context(), []netip.Addr{netip.MustParseAddr("10.8.0.2"), netip.MustParseAddr("fd00::2")}, 2)
	if len(got) != 2 || got[1].Domain != "six.example.com" {
		t.Errorf("with a limit of 2: %v", got)
	}
}

// A quiet client behind busy neighbors is found by paging past them, up to a point.
func TestQueriesFromPagesPastNeighbors(t *testing.T) {
	fake := adguardtest.New(t, "u", "p")
	c := mustClient(t, fake.URL(), "u", "p")
	mine := netip.MustParseAddr("10.8.0.2")
	for i := range 3 {
		fake.AddQuery(adguardtest.Entry{Time: at(i), Client: "10.8.0.2", Domain: fmt.Sprintf("mine%d.example.com", i)})
	}
	for i := range 450 { // newer than all of mine
		fake.AddQuery(adguardtest.Entry{Time: at(100 + i), Client: "10.8.0.20", Domain: "busy.example.com"})
	}
	got, err := c.QueriesFrom(t.Context(), []netip.Addr{mine}, 10)
	if err != nil || len(got) != 3 {
		t.Fatalf("QueriesFrom = %d queries, %v; want my 3 from behind 450 of a neighbor's", len(got), err)
	}

	// Past the cap on pages, it gives up with what it has, and doesn't read the log forever.
	for i := range 1500 {
		fake.AddQuery(adguardtest.Entry{Time: at(1000 + i), Client: "10.8.0.20", Domain: "busier.example.com"})
	}
	fake.ResetCalls()
	got, err = c.QueriesFrom(t.Context(), []netip.Addr{mine}, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("QueriesFrom = %d queries, %v; want none, with no error", len(got), err)
	}
	if n := len(fake.Calls()); n > 5 {
		t.Errorf("made %d requests; want at most 5", n)
	}
}

func TestQueryLogConfig(t *testing.T) {
	fake := adguardtest.New(t, "u", "p")
	c := mustClient(t, fake.URL(), "u", "p")
	fake.SetLogConfig(false, true)
	cfg, err := c.QueryLogConfig(t.Context())
	if err != nil || cfg.Enabled || !cfg.AnonymizeClientIP {
		t.Errorf("QueryLogConfig = %+v, %v", cfg, err)
	}
}
