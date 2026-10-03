package service

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/stuffam/drawbridge/internal/adguard"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/store"
)

// States of a client's DNS log.
const (
	// DNSLogOff: the AdGuard Home integration isn't turned on, so there's no log to read.
	DNSLogOff = "off"
	// DNSLogOK: AdGuard Home answered. The log may still be empty, and Warnings may say why.
	DNSLogOK = "ok"
	// DNSLogError: it couldn't be read: AdGuard Home is unreachable, or refused the account.
	DNSLogError = "error"
)

const (
	// DefaultDNSLogLimit is how many queries a client's DNS log shows unless asked for another number.
	DefaultDNSLogLimit = 50
	// MaxDNSLogLimit is the most one request returns.
	MaxDNSLogLimit = 200

	// dnsLogTimeout bounds the whole read. A search behind busy neighbors takes several requests
	// (adguard.QueriesFrom), and a page of the web UI is waiting on it.
	dnsLogTimeout = 20 * time.Second
)

// DNSLog is a client's recent DNS queries, from AdGuard Home's query log (docs/PLAN.md §6.3).
type DNSLog struct {
	State string
	// Error says why the log couldn't be read, in words for the admin.
	Error string
	// BaseURL is the address of AdGuard Home's API, for a link to its own query log.
	BaseURL string
	// Addresses are the client's, which the queries are from.
	Addresses []netip.Addr
	// Queries are the newest first.
	Queries []adguard.Query
	// Warnings say why an empty log is empty, when AdGuard Home's settings are the reason.
	Warnings []string
}

// ClientDNSLog returns what a client has looked up lately, according to AdGuard Home's query log.
// Viewing it changes nothing, so it isn't an event. It follows the same rule as the sync for a
// refused account: it asks no more once AdGuard Home has refused it (CLAUDE.md), and a page that
// the admin reloads would otherwise keep asking.
func (s *Service) ClientDNSLog(ctx context.Context, ref store.Ref, limit int) (DNSLog, error) {
	if limit <= 0 {
		limit = DefaultDNSLogLimit
	}
	limit = min(limit, MaxDNSLogLimit)
	c, err := s.Store.Client(ctx, ref)
	if err != nil {
		return DNSLog{}, err
	}
	in, ok, err := s.Store.DNSIntegration(ctx)
	if err != nil {
		return DNSLog{}, err
	}
	if !ok || in.Kind != store.KindAdGuard || !in.Enabled {
		return DNSLog{State: DNSLogOff, Queries: []adguard.Query{}}, nil
	}
	return s.readDNSLog(ctx, in, c, limit), nil
}

func (s *Service) readDNSLog(ctx context.Context, in store.DNSIntegration, c model.Client, limit int) DNSLog {
	res := DNSLog{State: DNSLogOK, BaseURL: in.BaseURL, Queries: []adguard.Query{}}
	addrs := []netip.Addr{c.IPv4}
	if c.IPv6.IsValid() {
		addrs = append(addrs, c.IPv6)
	}
	res.Addresses = addrs
	fail := func(msg string) DNSLog {
		res.State, res.Error = DNSLogError, msg
		return res
	}

	key := adguardAccount{BaseURL: in.BaseURL, Username: in.Username, Password: in.Password}.key()
	if s.adguardRefused.has(key) {
		return fail("AdGuard Home refused Drawbridge's account. Fix the connection in Settings, then press Test connection.")
	}
	client, err := adguard.New(in.BaseURL, in.Username, in.Password)
	if err != nil {
		return fail(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, dnsLogTimeout)
	defer cancel()

	qs, err := client.QueriesFrom(ctx, addrs, limit)
	if err != nil {
		if errors.Is(err, adguard.ErrUnauthorized) {
			s.adguardRefused.remember(key, s.now())
		}
		return fail(err.Error())
	}
	if len(qs) > 0 {
		res.Queries = qs
		return res
	}
	// Nothing found. If AdGuard Home's settings are why, say so, so that an empty list isn't
	// taken for a client that looks nothing up.
	if cfg, err := client.QueryLogConfig(ctx); err == nil {
		res.Warnings = queryLogWarnings(cfg)
	}
	return res
}
