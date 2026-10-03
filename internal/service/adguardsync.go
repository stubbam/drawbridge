package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stuffam/drawbridge/internal/adguard"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
)

// How the name sync is doing.
const (
	// SyncOff: the connection isn't turned on, or name sync is off.
	SyncOff = "off"
	// SyncPending: it's on, and no pass has finished yet.
	SyncPending = "pending"
	// SyncOK: the last pass finished. Some clients may still have conflicts.
	SyncOK = "ok"
	// SyncError: the last pass failed, and another is coming.
	SyncError = "error"
	// SyncStopped: AdGuard Home refused the account. It stays stopped until the connection
	// changes, or a test or Sync now shows the account works, because each try can extend
	// AdGuard Home's 15-minute block.
	SyncStopped = "stopped"
)

const (
	// adguardSyncInterval is how often the sync looks again when nothing prompted it, so that
	// an AdGuard Home that was down catches up, and so does a name the admin deleted there.
	adguardSyncInterval = 5 * time.Minute
	// adguardSyncSettle is how long it waits after a change, so that a burst of them (a client
	// added and then renamed) becomes one pass.
	adguardSyncSettle = time.Second
	// A failed pass is tried again after adguardBackoffStart, then twice that, up to the cap.
	adguardBackoffStart = 30 * time.Second
	adguardBackoffMax   = 5 * time.Minute
	// A pass that changed something looks again, up to this many times, because one change can
	// make another possible: two clients that swapped names each wait on the other.
	adguardSyncRounds = 3
)

// AdGuardConflict is a client the sync couldn't name in AdGuard Home, and why. The admin settles
// it there, and the sync notices on its next pass.
type AdGuardConflict struct {
	ClientID string
	Client   string
	Reason   string
}

// AdGuardSyncStatus is how the name sync is doing, as of its last pass. It's kept in memory: a
// sync that changes nothing writes nothing, because of the SD card (docs/PLAN.md §6.4).
type AdGuardSyncStatus struct {
	State string
	// LastSync is when a pass last finished without failing. Zero before the first.
	LastSync time.Time
	// Error is why the last pass failed, in words for the admin.
	Error string
	// Synced is how many clients have their name in AdGuard Home, as of the last pass.
	Synced    int
	Conflicts []AdGuardConflict
}

// adguardSyncState is what the sync remembers between passes.
type adguardSyncState struct {
	mu       sync.Mutex
	status   AdGuardSyncStatus
	failures int
	// reported are the conflicts already in the event log, so that a conflict that lasts
	// is one event and not one per pass.
	reported map[string]bool

	run       sync.Mutex // one pass at a time
	nudgeOnce sync.Once
	nudge     chan struct{}
}

func (st *adguardSyncState) reset() {
	st.mu.Lock()
	st.status, st.failures, st.reported = AdGuardSyncStatus{}, 0, nil
	st.mu.Unlock()
}

func (st *adguardSyncState) get() AdGuardSyncStatus {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := st.status
	out.Conflicts = slices.Clone(out.Conflicts)
	return out
}

func (s *Service) nudges() chan struct{} {
	s.adguardSync.nudgeOnce.Do(func() { s.adguardSync.nudge = make(chan struct{}, 1) })
	return s.adguardSync.nudge
}

// nudgeAdGuard asks the sync loop for a pass soon. It never waits, and a nudge when one is already
// waiting changes nothing.
func (s *Service) nudgeAdGuard() {
	select {
	case s.nudges() <- struct{}{}:
	default:
	}
}

// AdGuardSync reports how the name sync is doing.
func (s *Service) AdGuardSync(ctx context.Context) (AdGuardSyncStatus, error) {
	in, ok, err := s.Store.DNSIntegration(ctx)
	if err != nil {
		return AdGuardSyncStatus{}, err
	}
	if !syncsNames(in, ok) {
		return AdGuardSyncStatus{State: SyncOff}, nil
	}
	st := s.adguardSync.get()
	if st.State == "" {
		st.State = SyncPending
	}
	return st, nil
}

func syncsNames(in store.DNSIntegration, ok bool) bool {
	return ok && in.Kind == store.KindAdGuard && in.Enabled && in.SyncNames
}

// RunAdGuardSync runs the name sync until ctx ends: a pass at the start, one soon after each
// change to a client or to the connection, and one every few minutes, backing off when AdGuard
// Home can't be reached. The daemon runs it in a goroutine.
func (s *Service) RunAdGuardSync(ctx context.Context) {
	for {
		next := s.syncAdGuard(ctx)
		var timer *time.Timer
		var tick <-chan time.Time
		if next > 0 {
			timer = time.NewTimer(next)
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.nudges():
			select {
			case <-ctx.Done():
			case <-time.After(s.adguardSettle()):
			}
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (s *Service) adguardSettle() time.Duration {
	if s.AdGuardSyncSettle > 0 {
		return s.AdGuardSyncSettle
	}
	return adguardSyncSettle
}

func (s *Service) adguardInterval() time.Duration {
	if s.AdGuardSyncInterval > 0 {
		return s.AdGuardSyncInterval
	}
	return adguardSyncInterval
}

// SyncAdGuardNow runs a pass now, for the admin who has just settled a conflict, or fixed what
// stopped the sync. It's one attempt: a refused account isn't tried again for 30 seconds, as with
// a test.
func (s *Service) SyncAdGuardNow(ctx context.Context) (AdGuardSyncStatus, error) {
	in, ok, err := s.Store.DNSIntegration(ctx)
	if err != nil {
		return AdGuardSyncStatus{}, err
	}
	if !syncsNames(in, ok) {
		return AdGuardSyncStatus{State: SyncOff}, nil
	}
	key := adguardAccount{BaseURL: in.BaseURL, Username: in.Username, Password: in.Password}.key()
	if s.adguardRefused.wait(key, s.now()) > 0 {
		return s.AdGuardSync(ctx)
	}
	s.adguardRefused.forget(key)
	s.syncAdGuard(ctx)
	return s.AdGuardSync(ctx)
}

// backoff is how long to wait after the nth failure in a row.
func backoff(failures int) time.Duration {
	d := adguardBackoffStart
	for i := 1; i < failures && d < adguardBackoffMax; i++ {
		d *= 2
	}
	return min(d, adguardBackoffMax)
}

// syncAdGuard makes one pass, and returns how long to wait before the next. Zero means wait for
// a change: the account was refused, and trying again can't help.
func (s *Service) syncAdGuard(ctx context.Context) time.Duration {
	st := &s.adguardSync
	st.run.Lock()
	defer st.run.Unlock()

	in, ok, err := s.Store.DNSIntegration(ctx)
	if err != nil {
		s.Log.Warn("can't read the AdGuard Home connection", "err", err)
		return backoff(1)
	}
	if !syncsNames(in, ok) {
		st.reset()
		return s.adguardInterval()
	}
	acct := adguardAccount{BaseURL: in.BaseURL, Username: in.Username, Password: in.Password}
	key := acct.key()
	if s.adguardRefused.has(key) {
		// Still the account AdGuard Home refused. Nothing is asked.
		return 0
	}
	c, err := adguard.New(acct.BaseURL, acct.Username, acct.Password)
	if err != nil {
		s.syncFailed(ctx, err)
		return s.adguardInterval()
	}

	var res passResult
	for range adguardSyncRounds {
		res, err = s.syncRound(ctx, c)
		if err != nil || !res.changed {
			break
		}
	}
	switch {
	case err != nil && ctx.Err() != nil:
		// The daemon is stopping, or the admin closed the page. That's no failure of AdGuard Home's.
		return 0
	case errors.Is(err, adguard.ErrUnauthorized):
		s.adguardRefused.remember(key, s.now())
		s.syncFailed(ctx, err)
		st.mu.Lock()
		st.status.State = SyncStopped
		st.mu.Unlock()
		return 0
	case err != nil:
		s.syncFailed(ctx, err)
		return backoff(st.failureCount())
	}
	s.syncSucceeded(ctx, res)
	return s.adguardInterval()
}

func (st *adguardSyncState) failureCount() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.failures
}

// syncFailed records a failed pass. The event is for the first failure in a run of them, not one
// for every retry.
func (s *Service) syncFailed(ctx context.Context, err error) {
	st := &s.adguardSync
	st.mu.Lock()
	first := st.status.State != SyncError && st.status.State != SyncStopped
	st.failures++
	st.status.State, st.status.Error = SyncError, err.Error()
	st.mu.Unlock()
	if first {
		s.record(ctx, Event{Kind: "integration.adguard_sync_failed", Category: CategorySystem,
			Data: map[string]string{"error": err.Error()}})
	}
}

func (s *Service) syncSucceeded(ctx context.Context, res passResult) {
	st := &s.adguardSync
	sort.Slice(res.conflicts, func(i, j int) bool { return res.conflicts[i].Client < res.conflicts[j].Client })
	st.mu.Lock()
	recovered := st.status.State == SyncError || st.status.State == SyncStopped
	st.failures = 0
	st.status = AdGuardSyncStatus{State: SyncOK, LastSync: s.now(), Synced: res.synced, Conflicts: res.conflicts}
	seen := map[string]bool{}
	var fresh []AdGuardConflict
	for _, c := range res.conflicts {
		k := c.ClientID + "\x00" + c.Reason
		seen[k] = true
		if !st.reported[k] {
			fresh = append(fresh, c)
		}
	}
	st.reported = seen
	st.mu.Unlock()

	if recovered {
		s.record(ctx, Event{Kind: "integration.adguard_sync_recovered", Category: CategorySystem})
	}
	for _, c := range fresh {
		s.record(ctx, Event{Kind: "integration.adguard_name_failed", Category: CategorySystem,
			Client: &model.Client{ID: c.ClientID, Name: c.Client}, Data: map[string]string{"reason": c.Reason}})
	}
}

// passResult is what one round of the sync found and did.
type passResult struct {
	synced    int
	conflicts []AdGuardConflict
	// changed means it wrote to AdGuard Home, so another round may find more to do.
	changed bool
}

// wantedID is the address ids a client has in AdGuard Home: its VPN addresses.
func wantedIDs(c model.Client) []string {
	ids := []string{c.IPv4.String()}
	if c.IPv6.IsValid() {
		ids = append(ids, c.IPv6.String())
	}
	return ids
}

// fatal means the pass can't go on: AdGuard Home didn't answer, refused the account, or broke.
// Anything else is AdGuard Home refusing one change, which is that client's problem.
func fatal(err error) bool {
	var ae *adguard.Error
	if !errors.As(err, &ae) {
		return true
	}
	return errors.Is(err, adguard.ErrUnauthorized) || ae.Status >= 500
}

func refusalText(err error) string {
	var ae *adguard.Error
	if errors.As(err, &ae) && ae.Message != "" {
		return "AdGuard Home refused it: " + ae.Message
	}
	return err.Error()
}

// syncRound makes the persistent clients in AdGuard Home match Drawbridge's clients, changing only
// the ones Drawbridge made. Those are the ones it has a record of, and the ones whose addresses are
// exactly a client's. Nothing else is edited or deleted.
func (s *Service) syncRound(ctx context.Context, c *adguard.Client) (passResult, error) {
	var res passResult
	clients, err := s.Store.Clients(ctx)
	if err != nil {
		return res, err
	}
	records, err := s.Store.SyncedClients(ctx)
	if err != nil {
		return res, err
	}
	actual, err := c.Clients(ctx)
	if err != nil {
		return res, err
	}
	byName := func(name string) *adguard.Persistent {
		for i := range actual {
			if actual[i].Name == name {
				return &actual[i]
			}
		}
		return nil
	}
	// holder is the client that has any of ids, other than except.
	holder := func(ids []string, except *adguard.Persistent) (*adguard.Persistent, string) {
		for i := range actual {
			if &actual[i] == except {
				continue
			}
			for _, id := range ids {
				if adguard.HasID(actual[i].IDs, id) {
					return &actual[i], id
				}
			}
		}
		return nil, ""
	}
	// ours are the names Drawbridge gave, and the client each is for.
	ours := make(map[string]string, len(records))
	for id, rec := range records {
		ours[rec.Name] = id
	}
	nameTaken := func(cl model.Client, name string) string {
		if owner, ok := ours[name]; ok && owner != cl.ID {
			return fmt.Sprintf("the name %q is still on another client's entry in AdGuard Home, which is waiting to be renamed", name)
		}
		return fmt.Sprintf("AdGuard Home already has a client named %q that Drawbridge didn't make", name)
	}
	conflict := func(cl model.Client, reason string) {
		res.conflicts = append(res.conflicts, AdGuardConflict{ClientID: cl.ID, Client: cl.Name, Reason: reason})
	}
	save := func(cl model.Client, name string, ids []string) error {
		return s.Store.SaveSyncedClient(ctx, store.SyncedClient{ClientID: cl.ID, Name: name, IDs: ids})
	}

	wanted := make(map[string]bool, len(clients))
	for _, cl := range clients {
		wanted[cl.ID] = true
		ids := wantedIDs(cl)
		rec, hasRec := records[cl.ID]

		// The client Drawbridge made for this one, if there is one: by the name it gave, or, when
		// the admin renamed or replaced it in AdGuard Home, by the addresses.
		var cur *adguard.Persistent
		if hasRec {
			if cur = byName(rec.Name); cur == nil || !sharesID(cur.IDs, slices.Concat(rec.IDs, ids)) {
				// Only a client with exactly the addresses Drawbridge gave it counts as the one the
				// admin renamed. One with more, or other, addresses may be theirs.
				if h, _ := holder(ids, nil); h != nil && adguard.SameIDs(h.IDs, rec.IDs) {
					cur = h
				} else {
					cur = nil
				}
			}
		} else if n := byName(cl.Name); n != nil {
			if !adguard.SameIDs(n.IDs, ids) {
				conflict(cl, nameTaken(cl, cl.Name))
				continue
			}
			// Same name, same addresses: this is it, from before the record, or a restored database.
			if err := save(cl, n.Name, ids); err != nil {
				return res, err
			}
			res.synced++
			continue
		}

		if cur == nil {
			if other, id := holder(ids, nil); other != nil {
				conflict(cl, fmt.Sprintf("AdGuard Home's client %q already has the address %s", other.Name, id))
				continue
			}
			if n := byName(cl.Name); n != nil {
				conflict(cl, nameTaken(cl, cl.Name))
				continue
			}
			p := adguard.NewPersistent(cl.Name, ids)
			if err := c.AddClient(ctx, p); err != nil {
				if fatal(err) {
					return res, err
				}
				conflict(cl, refusalText(err))
				continue
			}
			actual = append(actual, p)
			res.changed = true
			if err := save(cl, cl.Name, ids); err != nil {
				return res, err
			}
			s.record(ctx, Event{Kind: "integration.adguard_name_added", Category: CategorySystem, Client: &cl,
				Data: map[string]string{"name": cl.Name, "ids": strings.Join(ids, ", ")}})
			res.synced++
			continue
		}

		// There's one. It needs the client's name and addresses, and nothing else of it changes:
		// the admin's tags, upstreams, and settings stay, and so do identifiers they added.
		ids2 := mergeIDs(cur.IDs, rec.IDs, ids)
		if cur.Name != cl.Name || !adguard.SameIDs(ids2, cur.IDs) {
			if cur.Name != cl.Name {
				if n := byName(cl.Name); n != nil && n != cur {
					conflict(cl, nameTaken(cl, cl.Name))
					continue
				}
			}
			if other, id := holder(ids, cur); other != nil {
				conflict(cl, fmt.Sprintf("AdGuard Home's client %q already has the address %s", other.Name, id))
				continue
			}
			from := cur.Name
			p := *cur
			p.Name, p.IDs = cl.Name, ids2
			if err := c.UpdateClient(ctx, from, p); err != nil {
				if fatal(err) {
					return res, err
				}
				conflict(cl, refusalText(err))
				continue
			}
			*cur = p
			res.changed = true
			if from != cl.Name {
				s.record(ctx, Event{Kind: "integration.adguard_name_renamed", Category: CategorySystem, Client: &cl,
					Data: map[string]string{"from": from, "to": cl.Name}})
			}
			if err := save(cl, cl.Name, ids); err != nil {
				return res, err
			}
		} else if rec.Name != cl.Name || !adguard.SameIDs(rec.IDs, ids) {
			// Right already, but the record is of something else: the admin renamed it back, say.
			if err := save(cl, cl.Name, ids); err != nil {
				return res, err
			}
		}
		res.synced++
	}

	// A client Drawbridge deleted: its name goes too, if it's still the client Drawbridge made.
	// An admin who has since made that name into something else keeps it.
	for id, rec := range records {
		if wanted[id] {
			continue
		}
		if cur := byName(rec.Name); cur != nil && sharesID(cur.IDs, rec.IDs) {
			if err := c.DeleteClient(ctx, cur.Name); err != nil {
				if fatal(err) {
					return res, err
				}
				s.Log.Warn("can't delete a client's name in AdGuard Home", "name", rec.Name, "err", refusalText(err))
				continue // kept, so that the next pass tries again
			}
			res.changed = true
			s.record(ctx, Event{Kind: "integration.adguard_name_removed", Category: CategorySystem,
				Client: &model.Client{ID: id, Name: rec.Name}, Data: map[string]string{"name": rec.Name}})
		}
		if err := s.Store.DeleteSyncedClient(ctx, id); err != nil {
			return res, err
		}
	}
	return res, nil
}

// sharesID reports whether the client has any of ids.
func sharesID(have, ids []string) bool {
	return slices.ContainsFunc(ids, func(id string) bool { return adguard.HasID(have, id) })
}

// mergeIDs is a client's identifiers with the addresses Drawbridge wrote before (old) swapped for
// the ones it wants now. Any other identifier, a MAC address the admin added, stays.
func mergeIDs(have, old, want []string) []string {
	var out []string
	for _, id := range have {
		if adguard.HasID(old, id) && !adguard.HasID(want, id) {
			continue
		}
		out = append(out, id)
	}
	for _, id := range want {
		if !adguard.HasID(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// adguardWarning is a line for the dashboard when the name sync needs the admin: it can't reach
// AdGuard Home, AdGuard Home refused the account, or some clients couldn't be named. It's "" when
// all is well or the sync is off. It reads memory only, because the dashboard asks every few
// seconds.
func (st *adguardSyncState) warning() string {
	s := st.get()
	switch {
	case s.State == SyncStopped:
		return "AdGuard Home refused Drawbridge's account, so client names aren't being synced."
	case s.State == SyncError:
		return "Drawbridge can't sync client names to AdGuard Home: " + s.Error + "."
	case s.State == SyncOK && len(s.Conflicts) == 1:
		return "One client couldn't be named in AdGuard Home."
	case s.State == SyncOK && len(s.Conflicts) > 1:
		return fmt.Sprintf("%d clients couldn't be named in AdGuard Home.", len(s.Conflicts))
	}
	return ""
}
