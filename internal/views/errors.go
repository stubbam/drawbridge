package views

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrNoUser), errors.Is(err, store.ErrNoSession):
		return http.StatusNotFound
	case errors.Is(err, store.ErrNameTaken), errors.Is(err, store.ErrHasClients),
		errors.Is(err, ipam.ErrExhausted), errors.Is(err, model.ErrNoEndpoint),
		errors.Is(err, store.ErrSetupDone), errors.Is(err, store.ErrUserExists):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// MaxEvents is the most events one request returns.
const MaxEvents = 500

// ParseEventFilter reads the event log's query parameters: before (an event ID, for
// paging), category, and limit. Callers resolve the client parameter themselves, by ID
// or by name.
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
	return f, nil
}

// ParseTrafficRange reads "range" (24h, 7d, or 90d; "" means 24h) and returns the
// resolution to read the store at and how far back to look. The caller computes "since"
// with its own clock, so this has no time dependency of its own.
func ParseTrafficRange(v string) (resolution string, lookback time.Duration, err error) {
	switch v {
	case "", "24h":
		return store.ResolutionRaw, 24 * time.Hour, nil
	case "7d":
		return store.ResolutionHourly, 7 * 24 * time.Hour, nil
	case "90d":
		return store.ResolutionHourly, 90 * 24 * time.Hour, nil
	default:
		return "", 0, &model.InvalidError{Err: fmt.Errorf("range must be 24h, 7d, or 90d, not %q", v)}
	}
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
