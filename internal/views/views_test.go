package views

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/stuffam/drawbridge/internal/diag"
	"github.com/stuffam/drawbridge/internal/ipam"
	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/wg"
)

func TestErrorStatus(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{&model.InvalidError{Err: errors.New("bad")}, http.StatusBadRequest},
		{&model.InvalidError{Err: service.ErrWrongPassword}, http.StatusBadRequest},
		{fmt.Errorf("wrapped: %w", store.ErrNotFound), http.StatusNotFound},
		{store.ErrNoUser, http.StatusNotFound},
		{store.ErrNoSession, http.StatusNotFound},
		{store.ErrNameTaken, http.StatusConflict},
		{store.ErrHasClients, http.StatusConflict},
		{ipam.ErrExhausted, http.StatusConflict},
		{model.ErrNoEndpoint, http.StatusConflict},
		{store.ErrSetupDone, http.StatusConflict},
		{store.ErrUserExists, http.StatusConflict},
		{service.ErrNoDiagnostics, http.StatusNotImplemented},
		{service.ErrBadLogin, http.StatusUnauthorized},
		{store.ErrBadSetupToken, http.StatusForbidden},
		{&service.RateLimitedError{Wait: 1}, http.StatusTooManyRequests},
		{fmt.Errorf("x: %w", &service.RateLimitedError{Wait: 1}), http.StatusTooManyRequests},
		{errors.New("disk on fire"), http.StatusInternalServerError},
	} {
		if got := ErrorStatus(c.err); got != c.want {
			t.Errorf("ErrorStatus(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestNewDiagnostics(t *testing.T) {
	empty, err := json.Marshal(NewDiagnostics(nil))
	if err != nil || string(empty) != `{"checks":[]}` {
		t.Fatalf("no checks: %s, %v", empty, err)
	}
	got, err := json.Marshal(NewDiagnostics([]diag.Check{
		{ID: "tunnel", Name: "Tunnel", Status: diag.Pass, Detail: "up"},
		{ID: "dns", Name: "DNS", Status: diag.Fail, Detail: "silent", Hint: "fix it"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"checks":[{"id":"tunnel","name":"Tunnel","status":"pass","detail":"up"},` +
		`{"id":"dns","name":"DNS","status":"fail","detail":"silent","hint":"fix it"}]}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestParseEventFilter(t *testing.T) {
	f, err := ParseEventFilter(url.Values{"before": {"42"}, "limit": {"10"}, "category": {"system"}})
	if err != nil || f.Before != 42 || f.Limit != 10 || f.Category != "system" {
		t.Fatalf("ParseEventFilter = %+v, %v", f, err)
	}
	if f, err := ParseEventFilter(url.Values{}); err != nil || f != (store.EventFilter{}) {
		t.Fatalf("no parameters: %+v, %v", f, err)
	}
	if f, err := ParseEventFilter(url.Values{"category": {"connection"}}); err != nil || f.Category != "connection" {
		t.Fatalf("category=connection: %+v, %v", f, err)
	}
	for _, q := range []string{"before=0", "before=x", "limit=0", "limit=501", "category=bogus"} {
		v, _ := url.ParseQuery(q)
		if _, err := ParseEventFilter(v); !model.IsInvalid(err) {
			t.Errorf("%s: err %v, want an InvalidError", q, err)
		}
	}
}

func TestStatusFillsTheSessionFieldsOnlyWhenOneIsOpen(t *testing.T) {
	c := model.Client{ID: "c1", Name: "phone", IPv4: netip.MustParseAddr("10.8.0.2")}

	v := Status(service.ClientStatus{Client: c})
	if v.Peer != nil {
		t.Fatalf("no peer, but got %+v", v.Peer)
	}

	started := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	v = Status(service.ClientStatus{
		Client: c,
		Peer:   &wg.Peer{ReceiveBytes: 5000, SendBytes: 3000},
		Session: &store.ClientSession{
			StartedAt: started, RxBytes: 500, TxBytes: 300,
		},
	})
	if v.Peer == nil {
		t.Fatal("a peer, but no PeerView")
	}
	if v.Peer.ReceiveBytes != 5000 || v.Peer.SendBytes != 3000 {
		t.Fatalf("all-time totals: %+v", v.Peer)
	}
	if !v.Peer.SessionStartedAt.Equal(started) || v.Peer.SessionReceiveBytes != 500 || v.Peer.SessionSendBytes != 300 {
		t.Fatalf("session fields: %+v", v.Peer)
	}

	// A peer with no open session leaves the session fields zero.
	v = Status(service.ClientStatus{Client: c, Peer: &wg.Peer{ReceiveBytes: 5000, SendBytes: 3000}})
	if !v.Peer.SessionStartedAt.IsZero() || v.Peer.SessionReceiveBytes != 0 || v.Peer.SessionSendBytes != 0 {
		t.Fatalf("no open session, but got %+v", v.Peer)
	}
}

func TestParseTrafficRange(t *testing.T) {
	for _, c := range []struct {
		in         string
		resolution string
		lookback   time.Duration
	}{
		{"", store.ResolutionRaw, 24 * time.Hour},
		{"1m", service.ResolutionLive, time.Minute},
		{"1h", store.ResolutionRaw, time.Hour},
		{"12h", store.ResolutionRaw, 12 * time.Hour},
		{"24h", store.ResolutionRaw, 24 * time.Hour},
		{"7d", store.ResolutionHourly, 7 * 24 * time.Hour},
		{"30d", store.ResolutionHourly, 30 * 24 * time.Hour},
		{"90d", store.ResolutionHourly, 90 * 24 * time.Hour},
	} {
		resolution, lookback, err := ParseTrafficRange(c.in)
		if err != nil || resolution != c.resolution || lookback != c.lookback {
			t.Errorf("ParseTrafficRange(%q) = %q, %v, %v; want %q, %v", c.in, resolution, lookback, err, c.resolution, c.lookback)
		}
	}
	for _, bad := range []string{"30m", "1w", "1M", "24H"} {
		if _, _, err := ParseTrafficRange(bad); !model.IsInvalid(err) {
			t.Errorf("range=%s: err %v, want an InvalidError", bad, err)
		}
	}
	_, _, err := ParseTrafficRange("nope")
	if want := `range must be one of 1m, 1h, 12h, 24h, 7d, 30d, 90d, not "nope"`; err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}

func TestParseSessionHistoryFilter(t *testing.T) {
	before, limit, err := ParseSessionHistoryFilter(url.Values{"before": {"2026-09-27T12:00:00Z"}, "limit": {"10"}})
	want := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	if err != nil || !before.Equal(want) || limit != 10 {
		t.Fatalf("ParseSessionHistoryFilter = %v, %d, %v", before, limit, err)
	}
	if before, limit, err := ParseSessionHistoryFilter(url.Values{}); err != nil || !before.IsZero() || limit != 0 {
		t.Fatalf("no parameters: %v, %d, %v", before, limit, err)
	}
	for _, q := range []string{"before=not-a-time", "limit=0", "limit=201"} {
		v, _ := url.ParseQuery(q)
		if _, _, err := ParseSessionHistoryFilter(v); !model.IsInvalid(err) {
			t.Errorf("%s: err %v, want an InvalidError", q, err)
		}
	}
}

func TestTrafficSamples(t *testing.T) {
	bucket := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	got := TrafficSamples([]store.TrafficSample{{BucketStart: bucket, RxBytes: 100, TxBytes: 50}})
	if len(got) != 1 || !got[0].BucketStart.Equal(bucket) || got[0].ReceiveBytes != 100 || got[0].SendBytes != 50 {
		t.Fatalf("got %+v", got)
	}
	if got := TrafficSamples(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil input: got %+v, want an empty (not nil) slice", got)
	}
}

func TestClientSessions(t *testing.T) {
	started := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	ended := started.Add(5 * time.Minute)
	got := ClientSessions([]store.ClientSession{
		{ID: "s1", StartedAt: started, Endpoint: "a:1", RxBytes: 10, TxBytes: 20},
		{ID: "s2", StartedAt: started, EndedAt: &ended, Endpoint: "b:1", RxBytes: 30, TxBytes: 40},
	})
	if len(got) != 2 || got[0].EndedAt != nil {
		t.Fatalf("open session: %+v", got[0])
	}
	if got[1].EndedAt == nil || !got[1].EndedAt.Equal(ended) {
		t.Fatalf("closed session: %+v", got[1])
	}
}

func TestViewsHaveNoKeys(t *testing.T) {
	settings, err := model.NewSettings(wgtypes.Key{1}, netip.MustParsePrefix("fd00:1:2:3::/64"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(Settings(settings))
	if strings.Contains(strings.ToLower(string(b)), "private") {
		t.Fatalf("the settings view has a private key: %s", b)
	}
}
