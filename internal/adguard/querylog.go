package adguard

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Query is one DNS query from AdGuard Home's log.
type Query struct {
	Time time.Time
	// Client is the address the query came from. It's the zero Addr when AdGuard Home logged
	// something that isn't an address.
	Client netip.Addr
	// Domain is the name asked for, and Type the kind of record (A, AAAA, HTTPS, and so on).
	Domain, Type string
	// Status is the DNS answer's code (NOERROR, NXDOMAIN, SERVFAIL, …).
	Status string
	// Reason is why AdGuard Home answered as it did, in its own words: NotFilteredNotFound for
	// an ordinary lookup, FilteredBlackList for a blocked one.
	Reason string
	// Blocked means AdGuard Home's filters answered instead of the upstream resolver.
	Blocked bool
	// Rule is the filter rule that blocked it, when one did.
	Rule     string
	Cached   bool
	Upstream string
	// Elapsed is how long AdGuard Home took to answer.
	Elapsed time.Duration
	// Answers are the values it answered with, such as addresses.
	Answers []string
}

// LogConfig is the part of AdGuard Home's query-log settings that decides whether a client's
// queries can be found.
type LogConfig struct {
	// Enabled is whether AdGuard Home keeps a query log at all.
	Enabled bool `json:"enabled"`
	// AnonymizeClientIP means it hides the end of each client's address, so no one client's
	// queries can be picked out.
	AnonymizeClientIP bool `json:"anonymize_client_ip"`
}

// QueryLogConfig reads AdGuard Home's query-log settings.
func (c *Client) QueryLogConfig(ctx context.Context) (LogConfig, error) {
	var cfg LogConfig
	err := c.do(ctx, http.MethodGet, "/querylog/config", nil, nil, &cfg)
	return cfg, err
}

// logPage is how many entries one request to AdGuard Home asks for, and logPages how many
// requests a search makes at most, so a busy log can't keep a page of the web UI waiting.
const (
	logPage  = 200
	logPages = 5
)

// QueriesFrom returns up to limit of the most recent queries from the given addresses, newest
// first. AdGuard Home's search matches part of an address, so a search for 10.8.0.2 also finds
// 10.8.0.20, and this keeps only the entries from exactly these addresses. It looks through at
// most logPages pages for each, so a client that's been quiet for a while behind busy
// neighbors can come back with fewer than limit.
func (c *Client) QueriesFrom(ctx context.Context, addrs []netip.Addr, limit int) ([]Query, error) {
	var out []Query
	for _, addr := range addrs {
		qs, err := c.queriesFrom(ctx, addr, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, qs...)
	}
	slices.SortStableFunc(out, func(a, b Query) int { return b.Time.Compare(a.Time) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *Client) queriesFrom(ctx context.Context, addr netip.Addr, limit int) ([]Query, error) {
	var out []Query
	var olderThan string
	for range logPages {
		q := url.Values{"search": {addr.String()}, "limit": {strconv.Itoa(logPage)}}
		if olderThan != "" {
			q.Set("older_than", olderThan)
		}
		var page struct {
			Data   []queryJSON `json:"data"`
			Oldest string      `json:"oldest"`
		}
		if err := c.do(ctx, http.MethodGet, "/querylog", q, nil, &page); err != nil {
			return nil, err
		}
		for _, e := range page.Data {
			if q := e.query(); q.Client == addr {
				out = append(out, q)
			}
		}
		if len(out) >= limit || len(page.Data) == 0 || page.Oldest == "" || page.Oldest == olderThan {
			break
		}
		olderThan = page.Oldest
	}
	return out, nil
}

// queryJSON is a log entry as AdGuard Home writes it.
type queryJSON struct {
	Time     string `json:"time"`
	Client   string `json:"client"`
	Question struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"question"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	Cached    bool      `json:"cached"`
	Upstream  string    `json:"upstream"`
	ElapsedMs flexFloat `json:"elapsedMs"`
	Answer    []struct {
		Value string `json:"value"`
	} `json:"answer"`
	Rules []struct {
		Text string `json:"text"`
	} `json:"rules"`
}

func (e queryJSON) query() Query {
	q := Query{
		Domain:   strings.TrimSuffix(e.Question.Name, "."),
		Type:     e.Question.Type,
		Status:   e.Status,
		Reason:   e.Reason,
		Blocked:  strings.HasPrefix(e.Reason, "Filtered"),
		Cached:   e.Cached,
		Upstream: e.Upstream,
		Elapsed:  time.Duration(float64(e.ElapsedMs) * float64(time.Millisecond)),
	}
	q.Time, _ = time.Parse(time.RFC3339Nano, e.Time)
	if a, err := netip.ParseAddr(e.Client); err == nil {
		q.Client = a.Unmap()
	}
	if len(e.Rules) > 0 {
		q.Rule = e.Rules[0].Text
	}
	for _, a := range e.Answer {
		q.Answers = append(q.Answers, a.Value)
	}
	return q
}

// flexFloat is a number that AdGuard Home writes as a string ("25.68") and other versions
// may write as a number.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// A reply that has a bad timing in it still has its queries.
		*f = 0
		return nil //nolint:nilerr // See above.
	}
	*f = flexFloat(v)
	return nil
}
