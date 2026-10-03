// Package adguard is a client for AdGuard Home's REST API, the optional integration in
// docs/PLAN.md §6.3: it names Drawbridge's clients in AdGuard Home's query log and statistics,
// and reads a client's recent queries back.
//
// How AdGuard Home actually behaves was checked against v0.107.79 on 2026-10-03, and the
// documentation doesn't say any of it (CLAUDE.md, "Verified facts"):
//
//   - Every failure is a 400 with a plain-text message, never a 404 or a 409. So callers decide
//     what to do from a fresh listing, and don't read the messages.
//   - A client added with only a name and addresses has "use global settings" off, and
//     filtering off with it, so it isn't ad-blocked at all. NewPersistent turns the global
//     settings on.
//   - Updating a client replaces all of it. A rename that sends only the name and addresses
//     wipes the tags, upstreams, and per-client settings the admin set. Persistent keeps every
//     field AdGuard Home sent, so an update changes only what it's told to.
//   - After a few failed logins, AdGuard Home blocks the caller's address for 15 minutes, and
//     during the block even the right password gets a bare 401. A caller mustn't retry a 401 on a
//     timer.
//   - The query log's search is a substring match on the client's address (and the domain), so
//     10.8.0.2 also finds 10.8.0.20. QueriesFrom filters for the exact address.
package adguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is where a local AdGuard Home's API is, unless it's been moved.
const DefaultBaseURL = "http://127.0.0.1:3000/control"

// requestTimeout bounds one request. AdGuard Home answers a local call in milliseconds.
const requestTimeout = 10 * time.Second

// maxResponse caps what the client reads from one reply, so a wrong address that answers with
// something enormous can't use the daemon's memory.
const maxResponse = 8 << 20

// NormalizeBaseURL checks the address of AdGuard Home's API and returns it in one form: http
// or https, a host, no account in the address, no query, and a path that ends where the API
// starts. An address with no path gets "/control", so "http://127.0.0.1:3000" is enough.
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("the address must look like http://127.0.0.1:3000/control")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("the address must start with http:// or https://")
	}
	if u.User != nil {
		return "", errors.New("put the account's username and password in their own fields, not in the address")
	}
	if u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(raw, "?") || strings.HasSuffix(raw, "#") {
		return "", errors.New("the address can't have a query or a fragment")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "", errors.New("the port must be a number from 1 to 65535")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/control"
	}
	u.RawPath = ""
	return u.String(), nil
}

// Client talks to one AdGuard Home with one account.
type Client struct {
	base     string
	user     string
	password string
	http     *http.Client
}

// New returns a client for the API at baseURL (see NormalizeBaseURL). An empty username means
// AdGuard Home has no login, and nothing is sent.
func New(baseURL, username, password string) (*Client, error) {
	base, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// AdGuard Home is on this host or this network. A proxy from the environment has no
	// business between them, and would be sent the account's password.
	tr.Proxy = nil
	return &Client{
		base: base, user: username, password: password,
		http: &http.Client{
			Transport: tr,
			Timeout:   requestTimeout,
			// An answer that redirects somewhere else isn't AdGuard Home's API. Never follow it
			// with the account's password.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// BaseURL returns the normalized address the client calls.
func (c *Client) BaseURL() string { return c.base }

// Error is AdGuard Home refusing a request: any answer but 200.
type Error struct {
	Status int
	// Message is AdGuard Home's own words, when it said any. It's plain text, and short.
	Message string
}

func (e *Error) Error() string {
	switch {
	case e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden:
		// The reply has no body. After several failed logins AdGuard Home also blocks the
		// address for 15 minutes and answers this way even to the right password.
		return fmt.Sprintf("AdGuard Home refused the account (%d). If the username and password are right, "+
			"it may have blocked this host for 15 minutes after failed logins", e.Status)
	case e.Message != "":
		return fmt.Sprintf("AdGuard Home answered %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("AdGuard Home answered %d", e.Status)
}

// ErrUnauthorized matches (errors.Is) an Error that means the account was refused. Retrying
// can't fix it, and each try can extend AdGuard Home's block.
var ErrUnauthorized = errors.New("AdGuard Home refused the account")

// Is lets errors.Is(err, ErrUnauthorized) recognize a 401 or 403.
func (e *Error) Is(target error) bool {
	return target == ErrUnauthorized && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden)
}

// do sends one request and decodes a JSON reply into out, when out isn't nil.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	resp, err := c.http.Do(req) //nolint:gosec // The address is the admin's setting, checked by NormalizeBaseURL.
	if err != nil {
		return unreachable(c.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return unreachable(c.base, err)
	}
	if len(data) > maxResponse {
		return fmt.Errorf("the reply from %s is too large to be AdGuard Home's", c.base)
	}
	if resp.StatusCode != http.StatusOK {
		return &Error{Status: resp.StatusCode, Message: shorten(strings.TrimSpace(string(data)))}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("the reply from %s isn't AdGuard Home's API (is the address its /control path?)", c.base)
	}
	return nil
}

// unreachable says the request never got an answer. It keeps the cause, so errors.Is still
// sees a timeout or a refused connection.
func unreachable(base string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("can't reach AdGuard Home at %s: %w", base, err)
}

// shorten keeps an error message from AdGuard Home to one short line.
func shorten(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// Status is what AdGuard Home says about itself.
type Status struct {
	Version string `json:"version"`
	// Running is whether its DNS server is up.
	Running bool `json:"running"`
	// DNSAddresses are the addresses its DNS server listens on, and DNSPort the port.
	DNSAddresses      []string `json:"dns_addresses"`
	DNSPort           int      `json:"dns_port"`
	HTTPPort          int      `json:"http_port"`
	ProtectionEnabled bool     `json:"protection_enabled"`
}

// Status asks AdGuard Home how it is. It's the call that checks an address and an account.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	err := c.do(ctx, http.MethodGet, "/status", nil, nil, &st)
	return st, err
}
