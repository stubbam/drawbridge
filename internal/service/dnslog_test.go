package service

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/adguard/adguardtest"
	"github.com/stuffam/drawbridge/internal/store"
)

func (e *syncEnv) dnsLog(c string, limit int) DNSLog {
	e.t.Helper()
	log, err := e.s.ClientDNSLog(e.ctx, store.ByName(c), limit)
	if err != nil {
		e.t.Fatal(err)
	}
	return log
}

func TestDNSLogIsOffUntilTheIntegrationIsOn(t *testing.T) {
	s, _, fake, ctx := adguardEnv(t)
	c, _, err := s.AddClient(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	check := func(what string) {
		t.Helper()
		log, err := s.ClientDNSLog(ctx, store.ByID(c.ID), 0)
		if err != nil || log.State != DNSLogOff || log.Queries == nil || len(log.Queries) != 0 || log.Error != "" {
			t.Errorf("%s: %+v, %v", what, log, err)
		}
		if len(fake.Calls()) != 0 {
			t.Errorf("%s: AdGuard Home was asked %v", what, fake.Calls())
		}
	}
	check("nothing saved")
	saveFake(t, s, ctx, fake)
	check("saved, and not turned on")

	if _, err := s.ClientDNSLog(ctx, store.ByName("nobody"), 0); !errorsIsNotFound(err) {
		t.Errorf("an unknown client: err = %v, want store.ErrNotFound", err)
	}
}

func TestDNSLogShowsOnlyTheClientsOwnQueries(t *testing.T) {
	e := newSyncEnv(t)
	phone := e.addClient("phone")
	v4, v6 := phone.IPv4.String(), phone.IPv6.String()
	// A neighbor whose address starts with the client's: AdGuard Home's search finds it too.
	neighbor := v4 + "0"
	base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }
	e.fake.AddQuery(adguardtest.Entry{Time: at(1), Client: v4, Domain: "one.example.com"})
	e.fake.AddQuery(adguardtest.Entry{Time: at(2), Client: neighbor, Domain: "neighbor.example.com"})
	e.fake.AddQuery(adguardtest.Entry{Time: at(3), Client: v6, Domain: "six.example.com", Type: "AAAA", Answers: []string{"2001:db8::1"}})
	e.fake.AddQuery(adguardtest.Entry{Time: at(4), Client: "192.0.2.9", Domain: v4 + ".example.com"})
	e.fake.AddQuery(adguardtest.Entry{Time: at(5), Client: v4, Domain: "ads.example.net", Blocked: true, Rule: "||ads.example.net^"})

	log := e.dnsLog("phone", 0)
	if log.State != DNSLogOK || log.BaseURL != e.fake.URL() || len(log.Addresses) != 2 || len(log.Warnings) != 0 {
		t.Fatalf("log = %+v", log)
	}
	var got []string
	for _, q := range log.Queries {
		got = append(got, q.Domain)
	}
	if want := []string{"ads.example.net", "six.example.com", "one.example.com"}; !slices.Equal(got, want) {
		t.Fatalf("domains = %v, want %v: this client's, newest first, with neither the neighbor's nor the one that only mentions its address", got, want)
	}
	if q := log.Queries[0]; !q.Blocked || q.Rule != "||ads.example.net^" || q.Client != phone.IPv4 {
		t.Errorf("the blocked query = %+v", q)
	}
	if q := log.Queries[1]; q.Client != phone.IPv6 || q.Type != "AAAA" || !slices.Equal(q.Answers, []string{"2001:db8::1"}) {
		t.Errorf("the IPv6 query = %+v", q)
	}
	// Reading a log is no change, so it's no event.
	if slices.Contains(kinds(t, e.s), "client.dns_log_viewed") {
		t.Error("viewing the DNS log was recorded as an event")
	}

	if log = e.dnsLog("phone", 2); len(log.Queries) != 2 || log.Queries[1].Domain != "six.example.com" {
		t.Errorf("with a limit of 2: %+v", log.Queries)
	}
}

func TestDNSLogLimits(t *testing.T) {
	e := newSyncEnv(t)
	phone := e.addClient("phone")
	base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for i := range 250 {
		e.fake.AddQuery(adguardtest.Entry{Time: base.Add(time.Duration(i) * time.Second), Client: phone.IPv4.String(), Domain: fmt.Sprintf("d%d.example.com", i)})
	}
	if got := len(e.dnsLog("phone", 0).Queries); got != DefaultDNSLogLimit {
		t.Errorf("by default %d queries, want %d", got, DefaultDNSLogLimit)
	}
	if got := len(e.dnsLog("phone", 100000).Queries); got != MaxDNSLogLimit {
		t.Errorf("with a huge limit %d queries, want %d", got, MaxDNSLogLimit)
	}
	if q := e.dnsLog("phone", 1).Queries; len(q) != 1 || q[0].Domain != "d249.example.com" {
		t.Errorf("the newest: %+v", q)
	}
}

// An empty list says why when AdGuard Home's own settings are the reason, and says nothing when
// the client has just not looked anything up.
func TestDNSLogSaysWhyItIsEmpty(t *testing.T) {
	e := newSyncEnv(t)
	e.addClient("phone")

	log := e.dnsLog("phone", 0)
	if log.State != DNSLogOK || len(log.Queries) != 0 || len(log.Warnings) != 0 {
		t.Errorf("a client with no queries: %+v", log)
	}
	e.fake.SetLogConfig(false, false)
	if log = e.dnsLog("phone", 0); len(log.Warnings) != 1 || !strings.Contains(log.Warnings[0], "query log is off") {
		t.Errorf("with the log off: %+v", log.Warnings)
	}
	e.fake.SetLogConfig(true, true)
	if log = e.dnsLog("phone", 0); len(log.Warnings) != 1 || !strings.Contains(log.Warnings[0], "hides the end of each client's address") {
		t.Errorf("with addresses hidden: %+v", log.Warnings)
	}
}

func TestDNSLogWhenAdGuardHomeIsDown(t *testing.T) {
	e := newSyncEnv(t)
	e.addClient("phone")
	e.fake.SetDown(true)
	log := e.dnsLog("phone", 0)
	if log.State != DNSLogError || !strings.Contains(log.Error, "can't reach AdGuard Home") || log.Queries == nil || log.BaseURL == "" {
		t.Errorf("log = %+v", log)
	}
	e.fake.SetDown(false)
	if log = e.dnsLog("phone", 0); log.State != DNSLogOK {
		t.Errorf("after it came back: %+v", log)
	}
}

// A refused account is asked about once. A page that's reloaded, or a sync that has already been
// refused, can't walk the daemon into AdGuard Home's 15-minute block.
func TestDNSLogDoesNotKeepAskingARefusedAccount(t *testing.T) {
	e := newSyncEnv(t)
	e.addClient("phone")
	e.fake.SetPassword("changed-in-adguard-home")

	log := e.dnsLog("phone", 0)
	if log.State != DNSLogError || !strings.Contains(log.Error, "refused the account") {
		t.Fatalf("log = %+v", log)
	}
	for range 10 {
		if log = e.dnsLog("phone", 0); log.State != DNSLogError || !strings.Contains(log.Error, "refused Drawbridge's account") {
			t.Fatalf("a repeat: %+v", log)
		}
	}
	if n := e.fake.FailedLogins(); n != 1 {
		t.Errorf("AdGuard Home saw %d refused logins, want 1", n)
	}
	if e.fake.Blocked() {
		t.Error("the log got the caller blocked")
	}

	// And it's the same memory as the sync's: a refusal there stops the log, too.
	e.clk.advance(time.Hour)
	e.fake.SetPassword("secret")
	if res, _ := e.s.TestAdGuard(e.ctx, AdGuardPatch{}); !res.OK {
		t.Fatalf("test = %+v", res)
	}
	if log = e.dnsLog("phone", 0); log.State != DNSLogOK {
		t.Errorf("after a test showed the account works: %+v", log)
	}
	e.fake.SetPassword("changed-again")
	e.pass() // the sync is refused
	before := e.fake.FailedLogins()
	if log = e.dnsLog("phone", 0); log.State != DNSLogError || e.fake.FailedLogins() != before {
		t.Errorf("the log asked after the sync was refused: %+v (%d refused logins, was %d)", log, e.fake.FailedLogins(), before)
	}
}

func errorsIsNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
