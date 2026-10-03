// Package adguardtest is an in-memory AdGuard Home for tests: the part of its API that
// Drawbridge calls, answering the way AdGuard Home v0.107.79 did when it was checked on
// 2026-10-03 (see package adguard), including the ways that surprised us:
//
//   - A client added without "use global settings" is stored with it off, and filtering with it.
//   - An update replaces the whole client.
//   - Every refusal is a 400 with a plain-text message.
//   - Names are case-sensitive, and addresses are stored in their short lowercase form.
//   - The query log's search matches part of an address.
//   - Five failed logins block the caller, and then even the right password gets a bare 401.
//
// The contract test in package adguard runs the same checks against this server and, when
// DRAWBRIDGE_ADGUARD_URL is set, a real AdGuard Home, which keeps the two from drifting apart.
package adguardtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stuffam/drawbridge/internal/adguard"
)

// failedLoginLimit is how many refused logins in a row block the caller, as in AdGuard Home.
const failedLoginLimit = 5

// Entry is a query to put in the fake's log.
type Entry struct {
	Time   time.Time
	Client string
	Domain string
	// Type is the record type; A when empty.
	Type string
	// Status is the DNS answer's code; NOERROR when empty.
	Status string
	// Blocked answers 0.0.0.0 for the rule, as a filter would.
	Blocked bool
	Rule    string
	Answers []string
}

// Server is a fake AdGuard Home on a local port.
type Server struct {
	srv        *httptest.Server
	user, pass string

	mu      sync.Mutex
	clients []map[string]json.RawMessage
	log     []Entry
	logCfg  adguard.LogConfig
	failed  int
	blocked bool
	down    bool
	calls   []string
}

// New starts a fake that wants the given account. An empty username means no login.
func New(t testing.TB, username, password string) *Server {
	t.Helper()
	s := &Server{user: username, pass: password, logCfg: adguard.LogConfig{Enabled: true}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /control/status", s.status)
	mux.HandleFunc("GET /control/clients", s.list)
	mux.HandleFunc("POST /control/clients/add", s.add)
	mux.HandleFunc("POST /control/clients/update", s.update)
	mux.HandleFunc("POST /control/clients/delete", s.remove)
	mux.HandleFunc("GET /control/querylog", s.queryLog)
	mux.HandleFunc("GET /control/querylog/config", s.queryLogConfig)
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		down := s.down
		s.mu.Unlock()
		if down {
			// An unreachable server: the connection ends with no answer.
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		if !s.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		s.calls = append(s.calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/control"))
		s.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the address of the API, to give to adguard.New.
func (s *Server) URL() string { return s.srv.URL + "/control" }

// SetDown makes the server unreachable (true) or reachable again.
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	s.down = down
	s.mu.Unlock()
}

// Unblock clears the block that failed logins cause, as AdGuard Home does after 15 minutes.
func (s *Server) Unblock() {
	s.mu.Lock()
	s.blocked, s.failed = false, 0
	s.mu.Unlock()
}

// FailedLogins is how many logins have been refused since the last Unblock.
func (s *Server) FailedLogins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// Blocked reports whether failed logins have blocked the caller.
func (s *Server) Blocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blocked
}

// SetPassword changes the account's password, as the admin might in AdGuard Home.
func (s *Server) SetPassword(password string) {
	s.mu.Lock()
	s.pass = password
	s.mu.Unlock()
}

// Calls returns the requests that were allowed in, like "POST /clients/add", oldest first.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// ResetCalls forgets the requests so far.
func (s *Server) ResetCalls() {
	s.mu.Lock()
	s.calls = nil
	s.mu.Unlock()
}

// SetLogConfig sets whether the query log is on and whether it hides client addresses.
func (s *Server) SetLogConfig(enabled, anonymize bool) {
	s.mu.Lock()
	s.logCfg = adguard.LogConfig{Enabled: enabled, AnonymizeClientIP: anonymize}
	s.mu.Unlock()
}

// Clients returns the persistent clients, sorted by name as AdGuard Home lists them.
func (s *Server) Clients() []adguard.Persistent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]adguard.Persistent, 0, len(s.clients))
	for _, c := range s.sorted() {
		b, _ := json.Marshal(c)
		var p adguard.Persistent
		_ = json.Unmarshal(b, &p)
		out = append(out, p)
	}
	return out
}

// Client returns the persistent client with the exact name.
func (s *Server) Client(name string) (adguard.Persistent, bool) {
	for _, p := range s.Clients() {
		if p.Name == name {
			return p, true
		}
	}
	return adguard.Persistent{}, false
}

// AddRaw adds a client from JSON the way the API takes it, for a client the admin made in
// AdGuard Home's own UI: with settings, tags, and extra identifiers.
func (s *Server) AddRaw(body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insert("", []byte(body))
}

// AddQuery puts a query in the log.
func (s *Server) AddQuery(e Entry) {
	s.mu.Lock()
	s.log = append(s.log, e)
	s.mu.Unlock()
}

func (s *Server) authorized(r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.user == "" {
		return true
	}
	if s.blocked {
		return false
	}
	if u, p, ok := r.BasicAuth(); ok && u == s.user && p == s.pass {
		return true
	}
	s.failed++
	if s.failed >= failedLoginLimit {
		s.blocked = true
	}
	return false
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	reply(w, map[string]any{
		"version": "v0.107.79", "running": true, "protection_enabled": true,
		"dns_addresses": []string{"127.0.0.1", "::1"}, "dns_port": 53, "http_port": 3000,
	})
}

func (s *Server) list(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A list with nothing in it is null, as AdGuard Home writes it.
	var clients any
	if len(s.clients) > 0 {
		clients = s.sorted()
	}
	reply(w, map[string]any{
		"clients": clients, "auto_clients": []any{}, "supported_tags": []string{"device_phone", "os_ios"},
	})
}

// sorted returns the clients ordered by name, byte by byte. The caller holds the lock.
func (s *Server) sorted() []map[string]json.RawMessage {
	out := slices.Clone(s.clients)
	sort.Slice(out, func(i, j int) bool { return nameOf(out[i]) < nameOf(out[j]) })
	return out
}

func nameOf(c map[string]json.RawMessage) string {
	var n string
	_ = json.Unmarshal(c["name"], &n)
	return n
}

func idsOf(c map[string]json.RawMessage) []string {
	var ids []string
	_ = json.Unmarshal(c["ids"], &ids)
	return ids
}

func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, "bad request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.insert("", body); err != nil {
		refuse(w, err.Error())
		return
	}
}

func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, "bad request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.insert(body.Name, body.Data); err != nil {
		refuse(w, err.Error())
	}
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		refuse(w, "bad request")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.clients {
		if nameOf(c) == body.Name {
			s.clients = slices.Delete(s.clients, i, i+1)
			return
		}
	}
	refuse(w, "Client not found")
}

// defaults is what a client has when its JSON leaves a setting out: everything off, and
// "use global settings" with it.
func defaults() map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for k, v := range map[string]string{
		"safe_search":                 `{"enabled":false,"bing":false,"duckduckgo":false,"ecosia":false,"google":false,"pixabay":false,"yandex":false,"youtube":false}`,
		"blocked_services_schedule":   `{"time_zone":"UTC"}`,
		"blocked_services":            `null`,
		"tags":                        `null`,
		"upstreams":                   `null`,
		"filtering_enabled":           `false`,
		"parental_enabled":            `false`,
		"safebrowsing_enabled":        `false`,
		"safesearch_enabled":          `false`,
		"use_global_blocked_services": `false`,
		"use_global_settings":         `false`,
		"ignore_querylog":             `false`,
		"ignore_statistics":           `false`,
		"upstreams_cache_size":        `0`,
		"upstreams_cache_enabled":     `false`,
	} {
		m[k] = json.RawMessage(v)
	}
	return m
}

var clientID = regexp.MustCompile(`^[A-Za-z0-9._:/-]+$`)

// insert adds a client, or replaces the one called replacing when that isn't empty. The caller
// holds the lock.
func (s *Server) insert(replacing string, body []byte) error {
	verb := "adding client"
	if replacing != "" {
		verb = "updating client"
	}
	given := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &given); err != nil {
		return errors.New("bad request")
	}
	var name string
	_ = json.Unmarshal(given["name"], &name)
	var ids []string
	_ = json.Unmarshal(given["ids"], &ids)

	at := -1
	if replacing != "" {
		for i, c := range s.clients {
			if nameOf(c) == replacing {
				at = i
			}
		}
		if at < 0 {
			return fmt.Errorf("%s: client %q is not found", verb, replacing)
		}
	}
	switch {
	case name == "":
		return fmt.Errorf("%s: empty name", verb)
	case len(ids) == 0:
		return fmt.Errorf("%s: id required", verb)
	}
	ids = canonicalIDs(ids)
	for _, id := range ids {
		if clientID.MatchString(id) {
			continue
		}
		bad := id[strings.IndexFunc(id, func(r rune) bool { return !clientID.MatchString(string(r)) }):]
		return fmt.Errorf("invalid clientid %q: bad hostname label rune %q", id, []rune(bad)[0])
	}
	for i, c := range s.clients {
		if i == at {
			continue
		}
		if nameOf(c) == name {
			return fmt.Errorf("%s: another client uses the same name %q", verb, name)
		}
		for _, id := range ids {
			if _, err := netip.ParseAddr(id); err == nil && slices.Contains(idsOf(c), id) {
				return fmt.Errorf("%s: another client %q uses the same IP %q", verb, nameOf(c), id)
			}
		}
	}
	c := defaults()
	for k, v := range given {
		c[k] = v
	}
	c["name"], _ = json.Marshal(name)
	c["ids"], _ = json.Marshal(ids)
	if at >= 0 {
		s.clients[at] = c
	} else {
		s.clients = append(s.clients, c)
	}
	return nil
}

// canonicalIDs writes addresses in their short lowercase form, lowercases the rest, and puts
// the addresses first, as AdGuard Home does.
func canonicalIDs(ids []string) []string {
	var addrs []netip.Addr
	var rest []string
	for _, id := range ids {
		if a, err := netip.ParseAddr(id); err == nil {
			addrs = append(addrs, a)
		} else {
			rest = append(rest, strings.ToLower(id))
		}
	}
	slices.SortFunc(addrs, netip.Addr.Compare)
	out := make([]string, 0, len(ids))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return append(out, rest...)
}

func (s *Server) queryLogConfig(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply(w, s.logCfg)
}

func (s *Server) queryLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 500
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = n
	}
	var olderThan time.Time
	if v := q.Get("older_than"); v != "" {
		olderThan, _ = time.Parse(time.RFC3339Nano, v)
	}
	search := q.Get("search")

	s.mu.Lock()
	entries := slices.Clone(s.log)
	s.mu.Unlock()
	slices.SortStableFunc(entries, func(a, b Entry) int { return b.Time.Compare(a.Time) })

	var data []map[string]any
	oldest := ""
	for _, e := range entries {
		if !olderThan.IsZero() && !e.Time.Before(olderThan) {
			continue
		}
		// A search matches part of the client's address or of the name asked for.
		if search != "" && !strings.Contains(e.Client, search) && !strings.Contains(e.Domain, search) {
			continue
		}
		if len(data) == limit {
			break
		}
		data = append(data, e.json())
		oldest = e.Time.UTC().Format(time.RFC3339Nano)
	}
	reply(w, map[string]any{"oldest": oldest, "data": data})
}

func (e Entry) json() map[string]any {
	typ, status := e.Type, e.Status
	if typ == "" {
		typ = "A"
	}
	if status == "" {
		status = "NOERROR"
	}
	reason, rules := "NotFilteredNotFound", []map[string]any{}
	answers := e.Answers
	if e.Blocked {
		reason = "FilteredBlackList"
		rules = []map[string]any{{"filter_list_id": 0, "text": e.Rule}}
		answers = []string{"0.0.0.0"}
	}
	var answer []map[string]any
	for _, a := range answers {
		answer = append(answer, map[string]any{"type": typ, "value": a, "ttl": 60})
	}
	return map[string]any{
		"time": e.Time.UTC().Format(time.RFC3339Nano), "client": e.Client,
		"question":  map[string]string{"class": "IN", "name": e.Domain, "type": typ},
		"status":    status,
		"reason":    reason,
		"rules":     rules,
		"answer":    answer,
		"cached":    false,
		"upstream":  "9.9.9.9:53",
		"elapsedMs": "12.5",
	}
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// refuse answers the way AdGuard Home does when it won't do something: a 400 and a line of text.
func refuse(w http.ResponseWriter, msg string) {
	http.Error(w, msg, http.StatusBadRequest)
}
