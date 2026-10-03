package views

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stuffam/drawbridge/internal/ipam"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/store"
)

// ErrorStatus maps an error to an HTTP status: the caller's mistakes are 4xx, and
// everything else is 500.
func ErrorStatus(err error) int {
	var limited *service.RateLimitedError
	switch {
	case model.IsInvalid(err):
		return http.StatusBadRequest
	case errors.Is(err, service.ErrBadLogin):
		return http.StatusUnauthorized
	case errors.Is(err, store.ErrBadSetupToken):
		return http.StatusForbidden
	case errors.As(err, &limited):
		return http.StatusTooManyRequests
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrNoUser), errors.Is(err, store.ErrNoSession),
		errors.Is(err, store.ErrNoToken):
		return http.StatusNotFound
	case errors.Is(err, store.ErrNameTaken), errors.Is(err, store.ErrHasClients),
		errors.Is(err, store.ErrTokenNameTaken), errors.Is(err, service.ErrTooManyTokens),
		errors.Is(err, ipam.ErrExhausted), errors.Is(err, model.ErrNoEndpoint),
		errors.Is(err, store.ErrSetupDone), errors.Is(err, store.ErrUserExists):
		return http.StatusConflict
	case errors.Is(err, service.ErrNoDiagnostics):
		return http.StatusNotImplemented
	}
	return http.StatusInternalServerError
}

// MaxEvents is the most events one request returns.
const MaxEvents = 500

// ParseEventFilter reads the event log's query parameters: before (an event ID, for
// paging), category, kind (one event kind, like client.connected), from and to (RFC 3339 times,
// from inclusive and to exclusive), and limit. Callers resolve the client parameter themselves,
// by ID or by name.
func ParseEventFilter(q url.Values) (store.EventFilter, error) {
	var f store.EventFilter
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return f, &model.InvalidError{Err: fmt.Errorf("before must be an event ID, not %q", v)}
		}
		f.Before = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxEvents {
			return f, &model.InvalidError{Err: fmt.Errorf("limit must be 1–%d, not %q", MaxEvents, v)}
		}
		f.Limit = n
	}
	switch c := q.Get("category"); c {
	case "", service.CategoryAdmin, service.CategorySystem, service.CategoryConnection:
		f.Category = c
	default:
		return f, &model.InvalidError{Err: fmt.Errorf("category must be %s, %s, or %s, not %q",
			service.CategoryAdmin, service.CategorySystem, service.CategoryConnection, c)}
	}
	if v := q.Get("kind"); v != "" {
		if !eventKind.MatchString(v) {
			return f, &model.InvalidError{Err: fmt.Errorf("kind must be an event kind like client.connected, not %q", v)}
		}
		f.Kind = v
	}
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(p.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return f, &model.InvalidError{Err: fmt.Errorf("%s must be a time like 2026-09-30T00:00:00Z, not %q", p.name, v)}
			}
			*p.dst = t
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && !f.From.Before(f.To) {
		return f, &model.InvalidError{Err: errors.New("from must be before to")}
	}
	return f, nil
}

// eventKind matches what the service records as an event's kind: lowercase words joined by
// dots and underscores.
var eventKind = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,63}$`)

// trafficRanges are the chart ranges the API accepts, shortest first. Each reads the store at
// the finest resolution that still gives the chart a useful number of points: the last minute
// from the in-memory polls (the stored buckets are a minute wide), up to a day from the "raw"
// buckets, and anything longer from the hourly rollup.
var trafficRanges = []struct {
	name       string
	resolution string
	lookback   time.Duration
}{
	{"1m", service.ResolutionLive, time.Minute},
	{"1h", store.ResolutionRaw, time.Hour},
	{"12h", store.ResolutionRaw, 12 * time.Hour},
	{"24h", store.ResolutionRaw, 24 * time.Hour},
	{"7d", store.ResolutionHourly, 7 * 24 * time.Hour},
	{"30d", store.ResolutionHourly, 30 * 24 * time.Hour},
	{"90d", store.ResolutionHourly, 90 * 24 * time.Hour},
}

// ParseTrafficRange reads "range" (1m, 1h, 12h, 24h, 7d, 30d, or 90d; "" means 24h) and returns
// the resolution to read the store at and how far back to look. The caller computes "since"
// with its own clock, so this has no time dependency of its own.
func ParseTrafficRange(v string) (resolution string, lookback time.Duration, err error) {
	if v == "" {
		v = "24h"
	}
	names := make([]string, 0, len(trafficRanges))
	for _, r := range trafficRanges {
		if r.name == v {
			return r.resolution, r.lookback, nil
		}
		names = append(names, r.name)
	}
	return "", 0, &model.InvalidError{Err: fmt.Errorf("range must be one of %s, not %q", strings.Join(names, ", "), v)}
}

// MaxSessionHistory is the most session-history rows one request returns.
const MaxSessionHistory = 200

// ParseSessionHistoryFilter reads "before" (an RFC 3339 timestamp, for paging) and
// "limit".
func ParseSessionHistoryFilter(q url.Values) (before time.Time, limit int, err error) {
	if v := q.Get("before"); v != "" {
		before, err = time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, 0, &model.InvalidError{Err: fmt.Errorf("before must be an RFC 3339 timestamp, not %q", v)}
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxSessionHistory {
			return time.Time{}, 0, &model.InvalidError{Err: fmt.Errorf("limit must be 1–%d, not %q", MaxSessionHistory, v)}
		}
		limit = n
	}
	return before, limit, nil
}

// ParseDNSLogLimit reads "limit" for a client's DNS log: 1 to service.MaxDNSLogLimit, and
// service.DefaultDNSLogLimit when it's left out.
func ParseDNSLogLimit(q url.Values) (int, error) {
	v := q.Get("limit")
	if v == "" {
		return service.DefaultDNSLogLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > service.MaxDNSLogLimit {
		return 0, &model.InvalidError{Err: fmt.Errorf("limit must be 1–%d, not %q", service.MaxDNSLogLimit, v)}
	}
	return n, nil
}
