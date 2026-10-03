package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/stuffam/drawbridge/internal/adguard"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
)

// AdGuardConnection is the saved connection to AdGuard Home, without its password
// (docs/PLAN.md §6.3).
type AdGuardConnection struct {
	// Configured is whether a connection is saved. When it isn't, BaseURL is the usual address of
	// a local AdGuard Home, to start from.
	Configured  bool
	BaseURL     string
	Username    string
	HasPassword bool
}

// AdGuard returns the saved connection to AdGuard Home.
func (s *Service) AdGuard(ctx context.Context) (AdGuardConnection, error) {
	in, ok, err := s.Store.DNSIntegration(ctx)
	if err != nil {
		return AdGuardConnection{}, err
	}
	return adguardConnection(in, ok), nil
}

func adguardConnection(in store.DNSIntegration, ok bool) AdGuardConnection {
	if !ok || in.Kind != store.KindAdGuard {
		return AdGuardConnection{BaseURL: adguard.DefaultBaseURL}
	}
	return AdGuardConnection{Configured: true, BaseURL: in.BaseURL, Username: in.Username, HasPassword: in.Password != ""}
}

// AdGuardPatch changes the connection, or says what to test. Nil fields stay as they are.
type AdGuardPatch struct {
	BaseURL  *string
	Username *string
	// Password replaces the saved one; "" removes it.
	Password *string
}

// adguardAccount is what the client calls AdGuard Home with.
type adguardAccount struct {
	BaseURL, Username, Password string
}

// accountFor applies a patch to the saved connection, and checks the result.
//
// The saved password goes only to the address and the account it was saved with. A patch that
// changes either has to bring a password of its own: otherwise whoever could save an address
// (a hijacked session, say) could point it at a server of their own, and have the daemon send
// them the password.
func (s *Service) accountFor(ctx context.Context, p AdGuardPatch) (acct adguardAccount, saved store.DNSIntegration, had bool, err error) {
	saved, had, err = s.Store.DNSIntegration(ctx)
	if err != nil {
		return adguardAccount{}, saved, false, err
	}
	if had && saved.Kind != store.KindAdGuard {
		had, saved = false, store.DNSIntegration{}
	}
	acct = adguardAccount{BaseURL: adguard.DefaultBaseURL, Username: saved.Username, Password: saved.Password}
	if had {
		acct.BaseURL = saved.BaseURL
	}
	moved := false
	if p.BaseURL != nil {
		base, err := adguard.NormalizeBaseURL(*p.BaseURL)
		if err != nil {
			return adguardAccount{}, saved, had, &model.InvalidError{Err: fmt.Errorf("address: %w", err)}
		}
		moved = moved || base != acct.BaseURL
		acct.BaseURL = base
	}
	if p.Username != nil {
		user := strings.TrimSpace(*p.Username)
		if err := checkCredential("username", user, 128); err != nil {
			return adguardAccount{}, saved, had, err
		}
		if strings.Contains(user, ":") {
			return adguardAccount{}, saved, had, &model.InvalidError{Err: errors.New("username can't contain a colon")}
		}
		moved = moved || user != acct.Username
		acct.Username = user
	}
	switch {
	case p.Password != nil:
		if err := checkCredential("password", *p.Password, 1024); err != nil {
			return adguardAccount{}, saved, had, err
		}
		acct.Password = *p.Password
	case moved && acct.Username != "" && acct.Password != "":
		return adguardAccount{}, saved, had, &model.InvalidError{Err: errors.New(
			"enter the password again to use a different address or username: the saved one is only sent to the address it was saved with")}
	}
	if acct.Username == "" {
		// An AdGuard Home with no login has nothing to send a password to.
		acct.Password = ""
	}
	return acct, saved, had, nil
}

// checkCredential rejects a username or password that can't be what someone typed.
func checkCredential(what, v string, max int) error {
	if len(v) > max {
		return &model.InvalidError{Err: fmt.Errorf("%s can be at most %d characters", what, max)}
	}
	if strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return &model.InvalidError{Err: fmt.Errorf("%s can't contain control characters", what)}
	}
	return nil
}

// UpdateAdGuard saves the connection to AdGuard Home. It doesn't ask AdGuard Home anything: the
// test does that, and it works on values that aren't saved yet.
func (s *Service) UpdateAdGuard(ctx context.Context, p AdGuardPatch) (AdGuardConnection, error) {
	acct, saved, had, err := s.accountFor(ctx, p)
	if err != nil {
		return AdGuardConnection{}, err
	}
	next := store.DNSIntegration{Kind: store.KindAdGuard, BaseURL: acct.BaseURL, Username: acct.Username, Password: acct.Password}
	changes := map[string]string{}
	if !had {
		changes["address"] = "none → " + next.BaseURL
		if next.Username != "" {
			changes["username"] = "none → " + next.Username
		}
	} else {
		if saved.BaseURL != next.BaseURL {
			changes["address"] = saved.BaseURL + " → " + next.BaseURL
		}
		if saved.Username != next.Username {
			changes["username"] = orNone(saved.Username) + " → " + orNone(next.Username)
		}
	}
	// The event says the password changed, never what it is.
	switch {
	case saved.Password == next.Password:
	case saved.Password == "":
		changes["password"] = "set"
	case next.Password == "":
		changes["password"] = "removed"
	default:
		changes["password"] = "changed"
	}
	if len(changes) == 0 {
		return adguardConnection(next, true), nil
	}
	if err := s.Store.SaveDNSIntegration(ctx, next); err != nil {
		return AdGuardConnection{}, err
	}
	s.record(ctx, Event{Kind: "integration.adguard_changed", Data: changes})
	return adguardConnection(next, true), nil
}

// RemoveAdGuard forgets the connection to AdGuard Home, and its password with it. It's not an
// error when there's none.
func (s *Service) RemoveAdGuard(ctx context.Context) error {
	saved, had, err := s.Store.DNSIntegration(ctx)
	if err != nil || !had {
		return err
	}
	if err := s.Store.DeleteDNSIntegration(ctx); err != nil {
		return err
	}
	s.record(ctx, Event{Kind: "integration.adguard_removed", Data: map[string]string{"address": saved.BaseURL}})
	return nil
}

// AdGuardTest is what a test of the connection found. A connection that doesn't work is a result,
// not an error: the admin ran the test to find out.
type AdGuardTest struct {
	// OK means AdGuard Home answered, and accepted the account.
	OK bool
	// Error says why not, in words for the admin.
	Error string
	// Refused means the account was refused. Trying again won't help, and each try can extend
	// AdGuard Home's block.
	Refused bool

	Version           string
	Running           bool
	ProtectionEnabled bool
	// QueryLog is nil when AdGuard Home's query-log settings couldn't be read.
	QueryLog *adguard.LogConfig
	// DNS is what asking the server's VPN addresses for DNS found, whatever AdGuard Home said.
	DNS []DNSProbe
	// Warnings are things that work against what the integration is for.
	Warnings []string
}

// refusedWait is how long the same refused account isn't tried again.
const refusedWait = 30 * time.Second

// TestAdGuard checks a connection: that AdGuard Home answers, that it accepts the account, how
// its query log is set, and whether a DNS resolver answers on the VPN addresses. It works on the
// saved connection with the patch applied, and saves nothing.
func (s *Service) TestAdGuard(ctx context.Context, p AdGuardPatch) (AdGuardTest, error) {
	acct, _, _, err := s.accountFor(ctx, p)
	if err != nil {
		return AdGuardTest{}, err
	}
	key := acct.key()
	if wait := s.adguardRefused.wait(key, s.now()); wait > 0 {
		return AdGuardTest{Refused: true, Error: fmt.Sprintf(
			"AdGuard Home refused this username and password a moment ago. It blocks an address for 15 minutes after "+
				"several refusals, so Drawbridge waits before trying the same ones again. Check them, and try again in %d seconds.",
			int(math.Ceil(wait.Seconds())))}, nil
	}
	c, err := adguard.New(acct.BaseURL, acct.Username, acct.Password)
	if err != nil {
		return AdGuardTest{}, &model.InvalidError{Err: err}
	}

	// The VPN addresses are asked at the same time. A resolver that answers there is what makes
	// AdGuard Home the clients' DNS, whatever its API says, so it's reported even when the API
	// can't be reached.
	var probes []DNSProbe
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if p, err := s.ProbeDNS(ctx); err == nil {
			probes = p
		}
	}()
	res := checkAdGuard(ctx, c)
	if res.Refused {
		s.adguardRefused.remember(key, s.now())
	}
	wg.Wait()
	res.DNS = probes
	return res, nil
}

// checkAdGuard asks AdGuard Home how it is and how its query log is set.
func checkAdGuard(ctx context.Context, c *adguard.Client) AdGuardTest {
	var res AdGuardTest
	st, err := c.Status(ctx)
	if err != nil {
		res.Error = err.Error()
		res.Refused = errors.Is(err, adguard.ErrUnauthorized)
		return res
	}
	res.OK = true
	res.Version, res.Running, res.ProtectionEnabled = st.Version, st.Running, st.ProtectionEnabled
	if cfg, err := c.QueryLogConfig(ctx); err == nil {
		res.QueryLog = &cfg
	}
	if !st.Running {
		res.Warnings = append(res.Warnings, "AdGuard Home's DNS server isn't running.")
	}
	if !st.ProtectionEnabled {
		res.Warnings = append(res.Warnings, "Protection is off in AdGuard Home, so it isn't blocking anything.")
	}
	if res.QueryLog != nil && !res.QueryLog.Enabled {
		res.Warnings = append(res.Warnings, "AdGuard Home's query log is off, so a client's page can't show what it looked up.")
	}
	if res.QueryLog != nil && res.QueryLog.Enabled && res.QueryLog.AnonymizeClientIP {
		res.Warnings = append(res.Warnings, "AdGuard Home hides the end of each client's address in its query log, "+
			"so one client's queries can't be picked out.")
	}
	return res
}

// key identifies an account without keeping its password: the same address, username, and
// password make the same key.
func (a adguardAccount) key() string {
	sum := sha256.Sum256([]byte(a.BaseURL + "\x00" + a.Username + "\x00" + a.Password))
	return hex.EncodeToString(sum[:])
}

// refusedLogin remembers the last account AdGuard Home refused. After five refusals it blocks
// the daemon's address for 15 minutes, and then answers even the right password with a bare 401
// (CLAUDE.md, "Verified facts"), so a double click on Test, or anything that asks in a loop,
// mustn't go on asking.
type refusedLogin struct {
	mu  sync.Mutex
	key string
	at  time.Time
}

func (r *refusedLogin) remember(key string, now time.Time) {
	r.mu.Lock()
	r.key, r.at = key, now
	r.mu.Unlock()
}

// wait is how much longer the account with this key shouldn't be tried; zero when it can be.
func (r *refusedLogin) wait(key string, now time.Time) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.key == "" || r.key != key {
		return 0
	}
	if left := refusedWait - now.Sub(r.at); left > 0 {
		return left
	}
	return 0
}
