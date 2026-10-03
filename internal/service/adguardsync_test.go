package service

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/adguard"
	"github.com/stuffam/drawbridge/internal/adguard/adguardtest"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
)

func bp(b bool) *bool { return &b }

// syncEnv is a service whose connection to a fake AdGuard Home is saved and turned on.
type syncEnv struct {
	t     *testing.T
	s     *Service
	clk   *clock
	fake  *adguardtest.Server
	ctx   context.Context
	admin *adguard.Client // the admin, changing things in AdGuard Home behind Drawbridge's back
}

func newSyncEnv(t *testing.T) *syncEnv {
	t.Helper()
	s, clk, fake, ctx := adguardEnv(t)
	saveFake(t, s, ctx, fake)
	admin, err := adguard.New(fake.URL(), "drawbridge", "secret")
	if err != nil {
		t.Fatal(err)
	}
	e := &syncEnv{t: t, s: s, clk: clk, fake: fake, ctx: ctx, admin: admin}
	if _, err := s.UpdateAdGuard(ctx, AdGuardPatch{Enabled: bp(true)}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *syncEnv) addClient(name string) model.Client {
	e.t.Helper()
	c, _, err := e.s.AddClient(e.ctx, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *syncEnv) rename(from, to string) {
	e.t.Helper()
	if _, err := e.s.RenameClient(e.ctx, store.ByName(from), to); err != nil {
		e.t.Fatal(err)
	}
}

// pass makes one pass of the sync and returns how long it would wait.
func (e *syncEnv) pass() time.Duration { return e.s.syncAdGuard(e.ctx) }

func (e *syncEnv) status() AdGuardSyncStatus {
	e.t.Helper()
	st, err := e.s.AdGuardSync(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *syncEnv) events(kind string) []store.Event {
	e.t.Helper()
	evs, err := e.s.Events(e.ctx, store.EventFilter{Kind: kind})
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

// names are the persistent clients in AdGuard Home, in its order.
func (e *syncEnv) names() []string {
	var out []string
	for _, p := range e.fake.Clients() {
		out = append(out, p.Name)
	}
	return out
}

func idsOf(c model.Client) []string { return wantedIDs(c) }

func TestSyncNamesNewClients(t *testing.T) {
	e := newSyncEnv(t)
	phone, laptop := e.addClient("phone"), e.addClient("laptop")

	if e.status().State != SyncPending {
		t.Errorf("before the first pass: %+v", e.status())
	}
	if next := e.pass(); next != adguardSyncInterval {
		t.Errorf("the next pass is in %v, want %v", next, adguardSyncInterval)
	}
	if got := e.names(); !slices.Equal(got, []string{"laptop", "phone"}) {
		t.Fatalf("AdGuard Home has %v", got)
	}
	for _, c := range []model.Client{phone, laptop} {
		p, _ := e.fake.Client(c.Name)
		if !adguard.SameIDs(p.IDs, idsOf(c)) || len(p.IDs) != 2 {
			t.Errorf("%s: ids %v, want %v", c.Name, p.IDs, idsOf(c))
		}
		if !p.UsesGlobalSettings() {
			t.Errorf("%s doesn't use AdGuard Home's global settings, so it wouldn't be ad-blocked", c.Name)
		}
	}
	recs, _ := e.s.Store.SyncedClients(e.ctx)
	if len(recs) != 2 || recs[phone.ID].Name != "phone" || !adguard.SameIDs(recs[phone.ID].IDs, idsOf(phone)) {
		t.Errorf("records = %+v", recs)
	}
	st := e.status()
	if st.State != SyncOK || st.Synced != 2 || len(st.Conflicts) != 0 || !st.LastSync.Equal(e.clk.t) {
		t.Errorf("status = %+v", st)
	}
	added := e.events("integration.adguard_name_added")
	if len(added) != 2 || added[0].Category != CategorySystem || added[0].ClientName == "" || added[0].Data["ids"] == "" {
		t.Errorf("events = %+v", added)
	}
}

// A pass that finds nothing to do asks AdGuard Home one question and writes nothing.
func TestSyncIsQuietWhenNothingChanged(t *testing.T) {
	e := newSyncEnv(t)
	e.addClient("phone")
	e.pass()

	events, _ := e.s.Events(e.ctx, store.EventFilter{})
	e.fake.ResetCalls()
	for range 3 {
		e.pass()
	}
	if got := e.fake.Calls(); !slices.Equal(got, []string{"GET /clients", "GET /clients", "GET /clients"}) {
		t.Errorf("AdGuard Home was asked %v; want only the listing, once a pass", got)
	}
	if after, _ := e.s.Events(e.ctx, store.EventFilter{}); len(after) != len(events) {
		t.Error("a pass that changed nothing recorded events")
	}
}

func TestSyncDoesNothingUntilTurnedOn(t *testing.T) {
	s, _, fake, ctx := adguardEnv(t)
	saveFake(t, s, ctx, fake) // saved, and not turned on
	if _, _, err := s.AddClient(ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	if next := s.syncAdGuard(ctx); next != adguardSyncInterval {
		t.Errorf("next = %v", next)
	}
	if st, _ := s.AdGuardSync(ctx); st.State != SyncOff {
		t.Errorf("status = %+v, want off", st)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("AdGuard Home was asked %v while the connection was off", fake.Calls())
	}

	// On, but with name sync off, it still writes nothing.
	if _, err := s.UpdateAdGuard(ctx, AdGuardPatch{Enabled: bp(true), SyncNames: bp(false)}); err != nil {
		t.Fatal(err)
	}
	s.syncAdGuard(ctx)
	if st, _ := s.AdGuardSync(ctx); st.State != SyncOff || len(fake.Calls()) != 0 {
		t.Errorf("status = %+v, calls = %v", st, fake.Calls())
	}
}

func TestSyncRenameKeepsWhatTheAdminSet(t *testing.T) {
	e := newSyncEnv(t)
	e.addClient("phone")
	e.pass()

	// The admin sets tags, a setting, and a MAC address on the client in AdGuard Home.
	all, _ := e.admin.Clients(e.ctx)
	p, _ := all[0].WithField("tags", []string{"device_phone"})
	p, _ = p.WithField("ignore_statistics", true)
	p.IDs = append(p.IDs, "aa:bb:cc:dd:ee:ff")
	if err := e.admin.UpdateClient(e.ctx, "phone", p); err != nil {
		t.Fatal(err)
	}

	e.rename("phone", "Alice's phone")
	e.pass()
	if got := e.names(); !slices.Equal(got, []string{"Alice's phone"}) {
		t.Fatalf("AdGuard Home has %v", got)
	}
	got, _ := e.fake.Client("Alice's phone")
	if v, _ := got.Field("tags"); string(v) != `["device_phone"]` {
		t.Errorf("tags = %s after the rename", v)
	}
	if v, _ := got.Field("ignore_statistics"); string(v) != "true" {
		t.Errorf("ignore_statistics = %s after the rename", v)
	}
	if !got.UsesGlobalSettings() || !adguard.HasID(got.IDs, "aa:bb:cc:dd:ee:ff") || len(got.IDs) != 3 {
		t.Errorf("the client lost something: %+v", got)
	}
	ev := e.events("integration.adguard_name_renamed")
	if len(ev) != 1 || ev[0].Data["from"] != "phone" || ev[0].Data["to"] != "Alice's phone" || ev[0].ClientName != "Alice's phone" {
		t.Errorf("events = %+v", ev)
	}
	recs, _ := e.s.Store.SyncedClients(e.ctx)
	for _, r := range recs {
		if r.Name != "Alice's phone" {
			t.Errorf("the record says %q", r.Name)
		}
	}
}

func TestSyncDeletesOnlyWhatItMade(t *testing.T) {
	e := newSyncEnv(t)
	if err := e.fake.AddRaw(`{"name":"printer","ids":["192.0.2.50"],"tags":["device_printer"]}`); err != nil {
		t.Fatal(err)
	}
	phone, tablet := e.addClient("phone"), e.addClient("tablet")
	e.pass()
	if got := e.names(); len(got) != 3 {
		t.Fatalf("AdGuard Home has %v", got)
	}

	if _, _, err := e.s.DeleteClient(e.ctx, store.ByID(phone.ID)); err != nil {
		t.Fatal(err)
	}
	e.pass()
	if got := e.names(); !slices.Equal(got, []string{"printer", "tablet"}) {
		t.Errorf("after deleting phone, AdGuard Home has %v", got)
	}
	if ev := e.events("integration.adguard_name_removed"); len(ev) != 1 || ev[0].ClientName != "phone" || ev[0].ClientID != phone.ID {
		t.Errorf("events = %+v", ev)
	}
	if recs, _ := e.s.Store.SyncedClients(e.ctx); len(recs) != 1 || recs[tablet.ID].Name != "tablet" {
		t.Errorf("records = %+v", recs)
	}

	// The admin has since made "tablet" into something else: their client, with other addresses.
	all, _ := e.admin.Clients(e.ctx)
	for _, p := range all {
		if p.Name == "tablet" {
			p.IDs = []string{"192.0.2.99"}
			if err := e.admin.UpdateClient(e.ctx, "tablet", p); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, _, err := e.s.DeleteClient(e.ctx, store.ByID(tablet.ID)); err != nil {
		t.Fatal(err)
	}
	e.pass()
	if got := e.names(); !slices.Equal(got, []string{"printer", "tablet"}) {
		t.Errorf("a client that was no longer Drawbridge's was deleted: %v", got)
	}
	if recs, _ := e.s.Store.SyncedClients(e.ctx); len(recs) != 0 {
		t.Errorf("records = %+v; the one for a client that isn't Drawbridge's any more should go", recs)
	}
}

// A client of the admin's that has the name, or the address, is theirs. It isn't edited, and
// the conflict is said once, and goes when they settle it.
func TestSyncLeavesAClientItDidNotMakeAlone(t *testing.T) {
	e := newSyncEnv(t)
	if err := e.fake.AddRaw(`{"name":"phone","ids":["192.0.2.7"],"tags":["device_phone"],"parental_enabled":true}`); err != nil {
		t.Fatal(err)
	}
	phone := e.addClient("phone")
	before, _ := e.fake.Client("phone")
	e.pass()

	if got := e.names(); !slices.Equal(got, []string{"phone"}) {
		t.Fatalf("AdGuard Home has %v", got)
	}
	after, _ := e.fake.Client("phone")
	if !slices.Equal(after.IDs, before.IDs) || after.UsesGlobalSettings() != before.UsesGlobalSettings() {
		t.Errorf("their client was edited: %+v", after)
	}
	if v, _ := after.Field("tags"); string(v) != `["device_phone"]` {
		t.Errorf("their tags = %s", v)
	}
	st := e.status()
	if st.State != SyncOK || st.Synced != 0 || len(st.Conflicts) != 1 || st.Conflicts[0].Client != "phone" ||
		st.Conflicts[0].ClientID != phone.ID || !strings.Contains(st.Conflicts[0].Reason, "didn't make") {
		t.Fatalf("status = %+v", st)
	}
	if len(e.events("integration.adguard_name_failed")) != 1 {
		t.Error("the conflict isn't in the event log once")
	}
	e.pass()
	e.pass()
	if len(e.events("integration.adguard_name_failed")) != 1 {
		t.Error("a conflict that lasts is an event on every pass")
	}
	if recs, _ := e.s.Store.SyncedClients(e.ctx); len(recs) != 0 {
		t.Errorf("a record was made for a client it didn't make: %+v", recs)
	}

	// The admin deletes theirs, and the next pass names the client.
	if err := e.admin.DeleteClient(e.ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	e.pass()
	if st = e.status(); st.Synced != 1 || len(st.Conflicts) != 0 {
		t.Errorf("status = %+v", st)
	}
	if p, ok := e.fake.Client("phone"); !ok || !adguard.SameIDs(p.IDs, idsOf(phone)) {
		t.Errorf("AdGuard Home has %+v", p)
	}
}

func TestSyncReportsAnAddressAnotherClientHas(t *testing.T) {
	e := newSyncEnv(t)
	phone := e.addClient("phone")
	if err := e.fake.AddRaw(`{"name":"nas","ids":["` + phone.IPv4.String() + `"]}`); err != nil {
		t.Fatal(err)
	}
	e.pass()
	st := e.status()
	if len(st.Conflicts) != 1 || !strings.Contains(st.Conflicts[0].Reason, `"nas"`) || !strings.Contains(st.Conflicts[0].Reason, phone.IPv4.String()) {
		t.Fatalf("status = %+v", st)
	}
	if got := e.names(); !slices.Equal(got, []string{"nas"}) {
		t.Errorf("AdGuard Home has %v", got)
	}
}

// A client with the right name and exactly the right addresses is the one: it's taken over
// without a write, as after a restore from a backup.
func TestSyncAdoptsAnIdenticalClient(t *testing.T) {
	e := newSyncEnv(t)
	phone := e.addClient("phone")
	if err := e.fake.AddRaw(`{"name":"phone","ids":["` + strings.Join(idsOf(phone), `","`) + `"],"tags":["device_phone"]}`); err != nil {
		t.Fatal(err)
	}
	e.fake.ResetCalls()
	e.pass()
	if got := e.fake.Calls(); !slices.Equal(got, []string{"GET /clients"}) {
		t.Errorf("AdGuard Home was asked %v; want only the listing", got)
	}
	if st := e.status(); st.Synced != 1 || len(st.Conflicts) != 0 {
		t.Errorf("status = %+v", st)
	}
	if recs, _ := e.s.Store.SyncedClients(e.ctx); recs[phone.ID].Name != "phone" {
		t.Errorf("records = %+v", recs)
	}
	got, _ := e.fake.Client("phone")
	if v, _ := got.Field("tags"); string(v) != `["device_phone"]` || got.UsesGlobalSettings() {
		t.Errorf("an adopted client was changed: %+v", got)
	}
}

// Drawbridge's clients are the truth: a name the admin deleted comes back, and one they
// renamed goes back, as long as it still has exactly the addresses Drawbridge gave it.
func TestSyncRepairsWhatTheAdminChanged(t *testing.T) {
	e := newSyncEnv(t)
	phone := e.addClient("phone")
	e.pass()

	if err := e.admin.DeleteClient(e.ctx, "phone"); err != nil {
		t.Fatal(err)
	}
	e.pass()
	if p, ok := e.fake.Client("phone"); !ok || !p.UsesGlobalSettings() {
		t.Fatalf("a deleted name didn't come back: %v", e.names())
	}

	all, _ := e.admin.Clients(e.ctx)
	all[0].Name = "something else"
	if err := e.admin.UpdateClient(e.ctx, "phone", all[0]); err != nil {
		t.Fatal(err)
	}
	e.pass()
	if got := e.names(); !slices.Equal(got, []string{"phone"}) {
		t.Errorf("a renamed client wasn't renamed back: %v", got)
	}
	if st := e.status(); st.Synced != 1 || len(st.Conflicts) != 0 {
		t.Errorf("status = %+v", st)
	}

	// With another identifier added as well, it's no longer clearly Drawbridge's, and is left alone.
	all, _ = e.admin.Clients(e.ctx)
	all[0].Name, all[0].IDs = "mine now", append(all[0].IDs, "aa:bb:cc:dd:ee:ff")
	if err := e.admin.UpdateClient(e.ctx, "phone", all[0]); err != nil {
		t.Fatal(err)
	}
	e.pass()
	if got := e.names(); !slices.Equal(got, []string{"mine now"}) {
		t.Errorf("a client that may be the admin's was changed: %v", got)
	}
	st := e.status()
	if len(st.Conflicts) != 1 || st.Conflicts[0].ClientID != phone.ID || !strings.Contains(st.Conflicts[0].Reason, `"mine now"`) {
		t.Errorf("status = %+v", st)
	}
}

// Two renames that cross, made together, settle within a pass.
func TestSyncFollowsAChainOfRenames(t *testing.T) {
	e := newSyncEnv(t)
	e.addClient("a")
	e.addClient("b")
	e.pass()

	e.rename("a", "a2")
	e.rename("b", "a") // takes the name that "a" had
	e.pass()
	if got := e.names(); !slices.Equal(got, []string{"a", "a2"}) {
		t.Fatalf("AdGuard Home has %v", got)
	}
	if st := e.status(); st.Synced != 2 || len(st.Conflicts) != 0 {
		t.Errorf("status = %+v", st)
	}
	recs, _ := e.s.Store.SyncedClients(e.ctx)
	got := []string{}
	for _, r := range recs {
		got = append(got, r.Name)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"a", "a2"}) {
		t.Errorf("records = %v", got)
	}
}

func TestSyncStopsWhenTheAccountIsRefused(t *testing.T) {
	e := newSyncEnv(t)
	e.addClient("phone")
	e.fake.SetPassword("changed-in-adguard-home")

	if next := e.pass(); next != 0 {
		t.Errorf("next = %v; a refused account waits for a change, not for a timer", next)
	}
	if st := e.status(); st.State != SyncStopped || !strings.Contains(st.Error, "refused the account") {
		t.Fatalf("status = %+v", st)
	}
	for range 10 {
		e.pass()
	}
	if n := e.fake.FailedLogins(); n != 1 {
		t.Errorf("AdGuard Home saw %d refused logins, want 1: it blocks a caller after five", n)
	}
	if e.fake.Blocked() {
		t.Error("the sync got the caller blocked")
	}
	if len(e.events("integration.adguard_sync_failed")) != 1 {
		t.Errorf("failed events: %+v", e.events("integration.adguard_sync_failed"))
	}

	// Sync now, right away, doesn't ask again either.
	if st, err := e.s.SyncAdGuardNow(e.ctx); err != nil || st.State != SyncStopped {
		t.Errorf("SyncAdGuardNow = %+v, %v", st, err)
	}
	if n := e.fake.FailedLogins(); n != 1 {
		t.Errorf("Sync now asked again: %d refused logins", n)
	}

	// The admin sets the password right in AdGuard Home, and a test shows it: the sync goes on.
	e.fake.SetPassword("secret")
	e.clk.advance(refusedWait + time.Second)
	if res, _ := e.s.TestAdGuard(e.ctx, AdGuardPatch{}); !res.OK {
		t.Fatalf("test = %+v", res)
	}
	if next := e.pass(); next != adguardSyncInterval {
		t.Errorf("next = %v after the account worked again", next)
	}
	if st := e.status(); st.State != SyncOK || st.Synced != 1 {
		t.Errorf("status = %+v", st)
	}
	if len(e.events("integration.adguard_sync_recovered")) != 1 {
		t.Error("no recovery event")
	}
}

// Saving a different password lets the sync try again at once.
func TestSyncTriesAgainWhenTheConnectionChanges(t *testing.T) {
	e := newSyncEnv(t)
	e.addClient("phone")
	e.fake.SetPassword("new-password")
	e.pass()
	if e.status().State != SyncStopped {
		t.Fatalf("status = %+v", e.status())
	}
	if _, err := e.s.UpdateAdGuard(e.ctx, AdGuardPatch{Password: sp("new-password")}); err != nil {
		t.Fatal(err)
	}
	if e.status().State != SyncPending {
		t.Errorf("status = %+v; the old result says nothing about the new password", e.status())
	}
	e.pass()
	if st := e.status(); st.State != SyncOK || st.Synced != 1 {
		t.Errorf("status = %+v", st)
	}
}

func TestSyncBacksOffWhenAdGuardHomeIsDown(t *testing.T) {
	e := newSyncEnv(t)
	e.addClient("phone")
	e.fake.SetDown(true)

	var waits []time.Duration
	for range 7 {
		waits = append(waits, e.pass())
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	if !slices.Equal(waits, want) {
		t.Errorf("waits = %v, want %v", waits, want)
	}
	st := e.status()
	if st.State != SyncError || !strings.Contains(st.Error, "can't reach AdGuard Home") {
		t.Fatalf("status = %+v", st)
	}
	if got := e.events("integration.adguard_sync_failed"); len(got) != 1 {
		t.Errorf("%d failed events for one outage, want 1", len(got))
	}

	e.fake.SetDown(false)
	if next := e.pass(); next != adguardSyncInterval {
		t.Errorf("next = %v after it came back", next)
	}
	if st = e.status(); st.State != SyncOK || st.Error != "" || st.Synced != 1 {
		t.Errorf("status = %+v", st)
	}
	if len(e.events("integration.adguard_sync_recovered")) != 1 {
		t.Error("no recovery event")
	}
	// And the next outage starts the backoff over.
	e.fake.SetDown(true)
	if next := e.pass(); next != 30*time.Second {
		t.Errorf("next = %v after a new outage", next)
	}
}

// AdGuard Home refusing one client's change is that client's conflict, not a failed pass.
func TestSyncReportsARefusedChangePerClient(t *testing.T) {
	e := newSyncEnv(t)
	e.fake.Refuse("tablet", "this name is not allowed")
	tablet := e.addClient("tablet")
	e.addClient("phone")
	e.pass()

	st := e.status()
	if st.State != SyncOK || st.Synced != 1 || len(st.Conflicts) != 1 || st.Conflicts[0].ClientID != tablet.ID ||
		!strings.Contains(st.Conflicts[0].Reason, "refused it: adding client: this name is not allowed") {
		t.Fatalf("status = %+v", st)
	}
	if got := e.names(); !slices.Equal(got, []string{"phone"}) {
		t.Errorf("AdGuard Home has %v; the other client should still be named", got)
	}
	// And when AdGuard Home takes it, the next pass names it.
	e.fake.Refuse("tablet", "")
	e.pass()
	if st = e.status(); st.Synced != 2 || len(st.Conflicts) != 0 {
		t.Errorf("status = %+v", st)
	}
}

func TestRemovingTheConnectionForgetsTheSync(t *testing.T) {
	e := newSyncEnv(t)
	e.addClient("phone")
	e.pass()
	if recs, _ := e.s.Store.SyncedClients(e.ctx); len(recs) != 1 {
		t.Fatalf("records = %+v", recs)
	}
	if err := e.s.RemoveAdGuard(e.ctx); err != nil {
		t.Fatal(err)
	}
	if recs, _ := e.s.Store.SyncedClients(e.ctx); len(recs) != 0 {
		t.Errorf("records = %+v after removing the connection", recs)
	}
	if st := e.status(); st.State != SyncOff {
		t.Errorf("status = %+v", st)
	}
	e.fake.ResetCalls()
	e.pass()
	if len(e.fake.Calls()) != 0 {
		t.Errorf("a removed connection was used: %v", e.fake.Calls())
	}
	// What was written stays in AdGuard Home: the sync no longer has the account to remove it.
	if got := e.names(); !slices.Equal(got, []string{"phone"}) {
		t.Errorf("AdGuard Home has %v", got)
	}
}

func TestWantedIDsFollowTheClientsAddresses(t *testing.T) {
	c := model.Client{IPv4: netip.MustParseAddr("10.8.0.5"), IPv6: netip.MustParseAddr("fd00::5")}
	if got := wantedIDs(c); !slices.Equal(got, []string{"10.8.0.5", "fd00::5"}) {
		t.Errorf("wantedIDs = %v", got)
	}
	c.IPv6 = netip.Addr{}
	if got := wantedIDs(c); !slices.Equal(got, []string{"10.8.0.5"}) {
		t.Errorf("without IPv6: %v", got)
	}
}

// The loop answers a change soon, not on the timer, and stops with its context.
func TestRunAdGuardSync(t *testing.T) {
	e := newSyncEnv(t)
	e.s.AdGuardSyncInterval = time.Hour
	e.s.AdGuardSyncSettle = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(web(context.Background(), "admin"))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); e.s.RunAdGuardSync(ctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })

	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if ok() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s; AdGuard Home has %v", what, e.names())
	}
	// A client added, renamed, and deleted shows up there soon after, though the timer is an hour.
	e.addClient("phone")
	waitFor("the first name", func() bool { return slices.Equal(e.names(), []string{"phone"}) })
	e.rename("phone", "mobile")
	waitFor("the rename", func() bool { return slices.Equal(e.names(), []string{"mobile"}) })
	if _, _, err := e.s.DeleteClient(e.ctx, store.ByName("mobile")); err != nil {
		t.Fatal(err)
	}
	waitFor("the removal", func() bool { return len(e.names()) == 0 })

	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop didn't stop with its context")
	}
}

// The dashboard hears when the sync needs the admin, and only then.
func TestStatusWarnsWhenTheSyncNeedsTheAdmin(t *testing.T) {
	e := newSyncEnv(t)
	warning := func() string {
		t.Helper()
		st, err := e.s.Status(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st.AdGuardWarning
	}
	if got := warning(); got != "" {
		t.Errorf("before the first pass: %q", got)
	}

	// Everything named: no warning.
	e.addClient("phone")
	e.pass()
	if got := warning(); got != "" {
		t.Errorf("when all is well: %q", got)
	}

	// Clients the admin's own clients are in the way of.
	if err := e.fake.AddRaw(`{"name":"a","ids":["192.0.2.1"]}`); err != nil {
		t.Fatal(err)
	}
	if err := e.fake.AddRaw(`{"name":"b","ids":["192.0.2.2"]}`); err != nil {
		t.Fatal(err)
	}
	e.addClient("a")
	e.pass()
	if got := warning(); got != "One client couldn't be named in AdGuard Home." {
		t.Errorf("one conflict: %q", got)
	}
	e.addClient("b")
	e.pass()
	if got := warning(); got != "2 clients couldn't be named in AdGuard Home." {
		t.Errorf("two conflicts: %q", got)
	}
	_ = e.admin.DeleteClient(e.ctx, "a")
	_ = e.admin.DeleteClient(e.ctx, "b")
	e.pass()
	if got := warning(); got != "" {
		t.Errorf("after they were settled: %q", got)
	}

	// Unreachable, and refused.
	e.fake.SetDown(true)
	e.pass()
	if got := warning(); !strings.HasPrefix(got, "Drawbridge can't sync client names to AdGuard Home: can't reach AdGuard Home") {
		t.Errorf("unreachable: %q", got)
	}
	e.fake.SetDown(false)
	e.pass()
	if got := warning(); got != "" {
		t.Errorf("after it came back: %q", got)
	}
	e.fake.SetPassword("changed")
	e.rename("phone", "mobile")
	e.pass()
	if got := warning(); got != "AdGuard Home refused Drawbridge's account, so client names aren't being synced." {
		t.Errorf("refused: %q", got)
	}

	// Turned off, there is nothing to warn about.
	if _, err := e.s.UpdateAdGuard(e.ctx, AdGuardPatch{Enabled: bp(false)}); err != nil {
		t.Fatal(err)
	}
	if got := warning(); got != "" {
		t.Errorf("when turned off: %q", got)
	}
}
