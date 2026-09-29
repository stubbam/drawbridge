// Package views has the JSON shapes that the control socket (the CLI) and the web API
// share, and the conversions to them. Keys never appear in a view.
package views

import (
	"net/netip"
	"strings"
	"time"

	"github.com/stuffam/drawbridge/internal/model"
	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/store"
)

// SettingsView is the server's settings, without the private key.
type SettingsView struct {
	Interface        string         `json:"interface"`
	ListenPort       uint16         `json:"listen_port"`
	EndpointHost     string         `json:"endpoint_host"`
	EndpointPort     uint16         `json:"endpoint_port"`
	Endpoint         string         `json:"endpoint"`
	PublicKey        string         `json:"public_key"`
	MTU              int            `json:"mtu"`
	IPv4Subnet       netip.Prefix   `json:"ipv4_subnet"`
	IPv4Address      netip.Addr     `json:"ipv4_address"`
	IPv6Subnet       netip.Prefix   `json:"ipv6_subnet"`
	IPv6Address      netip.Addr     `json:"ipv6_address"`
	DNS              []netip.Addr   `json:"dns"`
	Keepalive        int            `json:"keepalive"`
	ClientIsolation  bool           `json:"client_isolation"`
	ClientAllowedIPs []netip.Prefix `json:"client_allowed_ips"`
	AdminAllowed     []netip.Prefix `json:"admin_allowed"`
}

// Settings converts settings to their view.
func Settings(s model.Settings) SettingsView {
	v := SettingsView{
		Interface:        s.Interface,
		ListenPort:       s.ListenPort,
		EndpointHost:     s.EndpointHost,
		EndpointPort:     s.EndpointPort,
		PublicKey:        s.PublicKey().String(),
		MTU:              s.MTU,
		IPv4Subnet:       s.IPv4,
		IPv6Subnet:       s.IPv6,
		DNS:              s.DNS,
		Keepalive:        s.Keepalive,
		ClientIsolation:  s.ClientIsolation,
		ClientAllowedIPs: s.ClientAllowedIPs,
		AdminAllowed:     nonNil(s.AdminAllowed),
	}
	if ep, err := s.Endpoint(); err == nil {
		v.Endpoint = ep
	}
	if srv, err := s.ServerAddrs(); err == nil {
		v.IPv4Address, v.IPv6Address = srv.IPv4, srv.IPv6
	}
	return v
}

// DNSProbeResult is one address's DNS check.
type DNSProbeResult struct {
	Address  netip.Addr `json:"address"`
	Answered bool       `json:"answered"`
	Detail   string     `json:"detail"`
}

// DNSCheck is what asking the server's VPN addresses for DNS found. Usable lists the
// addresses that answered: the client DNS that "this server" means, and empty when no
// resolver answers on the host.
type DNSCheck struct {
	Results []DNSProbeResult `json:"results"`
	Usable  []netip.Addr     `json:"usable"`
}

// NewDNSCheck converts the service's probe results.
func NewDNSCheck(probes []service.DNSProbe) DNSCheck {
	c := DNSCheck{Results: []DNSProbeResult{}, Usable: []netip.Addr{}}
	for _, p := range probes {
		c.Results = append(c.Results, DNSProbeResult{Address: p.Address, Answered: p.Answered, Detail: p.Detail})
		if p.Answered {
			c.Usable = append(c.Usable, p.Address)
		}
	}
	return c
}

// SettingsPatch changes some settings. Omitted fields stay as they are. DNSDefault resets
// the DNS servers to the server's VPN addresses (D12).
type SettingsPatch struct {
	EndpointHost    *string         `json:"endpoint_host,omitempty"`
	EndpointPort    *uint16         `json:"endpoint_port,omitempty"`
	ListenPort      *uint16         `json:"listen_port,omitempty"`
	MTU             *int            `json:"mtu,omitempty"`
	DNS             *[]netip.Addr   `json:"dns,omitempty"`
	DNSDefault      bool            `json:"dns_default,omitempty"`
	Keepalive       *int            `json:"keepalive,omitempty"`
	ClientIsolation *bool           `json:"client_isolation,omitempty"`
	AdminAllowed    *[]netip.Prefix `json:"admin_allowed,omitempty"`
}

// Service converts the patch for the service layer.
func (p SettingsPatch) Service() service.SettingsPatch {
	return service.SettingsPatch{
		EndpointHost: p.EndpointHost, EndpointPort: p.EndpointPort, ListenPort: p.ListenPort,
		MTU: p.MTU, DNS: p.DNS, DNSDefault: p.DNSDefault, Keepalive: p.Keepalive,
		ClientIsolation: p.ClientIsolation, AdminAllowed: p.AdminAllowed,
	}
}

// nonNil returns ps, or an empty list for nil, so JSON shows [] rather than null.
func nonNil(ps []netip.Prefix) []netip.Prefix {
	if ps == nil {
		return []netip.Prefix{}
	}
	return ps
}

// PeerView is a client's live status in the tunnel.
type PeerView struct {
	Endpoint      string    `json:"endpoint,omitempty"`
	LastHandshake time.Time `json:"last_handshake,omitzero"`
	// ReceiveBytes and SendBytes are the peer's all-time totals: they reset only when
	// its peer is recreated (a pause and resume, or the tunnel restarting).
	ReceiveBytes int64 `json:"receive_bytes"`
	SendBytes    int64 `json:"send_bytes"`
	// SessionStartedAt and the bytes below describe the client's current connection
	// (docs/PLAN.md §6.4); they're zero when it has none.
	SessionStartedAt    time.Time `json:"session_started_at,omitzero"`
	SessionReceiveBytes int64     `json:"session_receive_bytes,omitempty"`
	SessionSendBytes    int64     `json:"session_send_bytes,omitempty"`
}

// ClientView is a client, without its keys.
type ClientView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Enabled   bool       `json:"enabled"`
	IPv4      netip.Addr `json:"ipv4"`
	IPv6      netip.Addr `json:"ipv6"`
	PublicKey string     `json:"public_key"`
	CreatedAt time.Time  `json:"created_at"`
	// Peer is nil when the client isn't in the tunnel (paused, or the tunnel is down).
	Peer *PeerView `json:"peer,omitempty"`
}

// Client converts a client to its view, without live status.
func Client(c model.Client) ClientView {
	return ClientView{
		ID:        c.ID,
		Name:      c.Name,
		Enabled:   c.Enabled,
		IPv4:      c.IPv4,
		IPv6:      c.IPv6,
		PublicKey: c.PublicKey.String(),
		CreatedAt: c.CreatedAt,
	}
}

// Status converts a client and its live status to a view.
func Status(cs service.ClientStatus) ClientView {
	v := Client(cs.Client)
	if p := cs.Peer; p != nil {
		v.Peer = &PeerView{
			LastHandshake: p.LastHandshake,
			ReceiveBytes:  p.ReceiveBytes,
			SendBytes:     p.SendBytes,
		}
		if p.Endpoint.IsValid() {
			v.Peer.Endpoint = p.Endpoint.String()
		}
		if cs.Session != nil {
			v.Peer.SessionStartedAt = cs.Session.StartedAt
			v.Peer.SessionReceiveBytes = cs.Session.RxBytes
			v.Peer.SessionSendBytes = cs.Session.TxBytes
		}
	}
	return v
}

// Statuses converts a list of clients.
func Statuses(cs []service.ClientStatus) []ClientView {
	out := make([]ClientView, len(cs))
	for i, c := range cs {
		out[i] = Status(c)
	}
	return out
}

// ClientResult is the response to a change to a client.
type ClientResult struct {
	Client  ClientView `json:"client"`
	Warning string     `json:"warning,omitempty"`
	// ApplyFailed means the change is saved but applying it to the tunnel failed.
	ApplyFailed bool `json:"apply_failed,omitempty"`
}

// NewClientResult is the response to a change that was applied.
func NewClientResult(c model.Client, a service.Applied) ClientResult {
	return ClientResult{Client: Client(c), Warning: a.Warning(), ApplyFailed: a.Err != nil}
}

// SettingsResult is the response to a settings change.
type SettingsResult struct {
	Settings SettingsView `json:"settings"`
	Warning  string       `json:"warning,omitempty"`
	// ApplyFailed means the change is saved but applying it to the tunnel failed.
	ApplyFailed bool `json:"apply_failed,omitempty"`
}

// NewSettingsResult is the response to a settings change that was applied.
func NewSettingsResult(s model.Settings, a service.Applied) SettingsResult {
	return SettingsResult{Settings: Settings(s), Warning: a.Warning(), ApplyFailed: a.Err != nil}
}

// NewClientRequest creates a client.
type NewClientRequest struct {
	Name string `json:"name"`
}

// ClientPatch changes a client. Omitted fields stay as they are.
type ClientPatch struct {
	Name *string `json:"name,omitempty"`
}

// Error is the body of every error response.
type Error struct {
	Error string `json:"error"`
}

// EventView is one entry in the event log.
type EventView struct {
	ID         int64             `json:"id"`
	Time       time.Time         `json:"time"`
	Kind       string            `json:"kind"`
	Category   string            `json:"category"`
	Actor      string            `json:"actor"`
	Via        string            `json:"via"`
	SourceIP   string            `json:"source_ip,omitempty"`
	ClientID   string            `json:"client_id,omitempty"`
	ClientName string            `json:"client_name,omitempty"`
	Data       map[string]string `json:"data,omitempty"`
}

// Events converts events to their views.
func Events(es []store.Event) []EventView {
	out := make([]EventView, len(es))
	for i, e := range es {
		out[i] = EventView{
			ID: e.ID, Time: e.Time, Kind: e.Kind, Category: e.Category, Actor: e.Actor,
			Via: e.Via, SourceIP: e.SourceIP, ClientID: e.ClientID, ClientName: e.ClientName,
			Data: e.Data,
		}
	}
	return out
}

// SetupStatus says whether first-run setup is needed.
type SetupStatus struct {
	Needed bool `json:"needed"`
}

// SetupRequest creates the admin account with the setup token.
type SetupRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginRequest logs in.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// PasswordChange changes the logged-in account's password.
type PasswordChange struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// UserView is the admin account.
type UserView struct {
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	// LastLoginAt is when the account last logged in. In the response to logging in,
	// it's the login before this one, so people can spot one they didn't make; zero if
	// there was none.
	LastLoginAt time.Time `json:"last_login_at,omitzero"`
}

// User converts an account to its view.
func User(u store.User) UserView {
	return UserView{Username: u.Username, CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt}
}

// SessionView is a logged-in browser. Its token is never shown.
type SessionView struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	// Current is the session making the request.
	Current bool `json:"current"`
}

// Session converts a session to its view.
func Session(s store.Session, current string) SessionView {
	return SessionView{ID: s.ID, CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt,
		ExpiresAt: s.ExpiresAt, IP: s.IP, UserAgent: s.UserAgent, Current: s.ID == current}
}

// Me is the logged-in account and its session.
type Me struct {
	User    UserView    `json:"user"`
	Session SessionView `json:"session"`
}

// ConfigFileName turns a client's name into a file name for its config. The WireGuard
// apps name the tunnel after the file, and Linux limits interface names to 15
// characters, so it keeps letters, digits, and - _ = + . and at most 15 of them.
func ConfigFileName(name string) string {
	var b []rune
	for _, r := range name {
		switch {
		case len(b) == 15:
		case r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_=+.", r)):
			b = append(b, r)
		case r == ' ' || r == '\'':
			b = append(b, '-')
		}
	}
	s := strings.Trim(string(b), "-.")
	if s == "" {
		s = "wireguard"
	}
	return s + ".conf"
}

// ServerStatus is the tunnel's state at a glance.
type ServerStatus struct {
	TunnelUp bool `json:"tunnel_up"`
	Clients  int  `json:"clients"`
	Paused   int  `json:"paused"`
	// Online counts clients with a handshake in the last three minutes.
	Online int `json:"online"`
}

// NewServerStatus converts the service's status.
func NewServerStatus(s service.Status) ServerStatus {
	return ServerStatus{TunnelUp: s.TunnelUp, Clients: s.Clients, Paused: s.Paused, Online: s.Online}
}

// TrafficSampleView is one bucket of a client's, or every client's, traffic history
// (docs/PLAN.md §6.4).
type TrafficSampleView struct {
	BucketStart  time.Time `json:"bucket_start"`
	ReceiveBytes int64     `json:"receive_bytes"`
	SendBytes    int64     `json:"send_bytes"`
}

// TrafficSamples converts traffic samples to their views. The store already returns
// them oldest first.
func TrafficSamples(ss []store.TrafficSample) []TrafficSampleView {
	out := make([]TrafficSampleView, len(ss))
	for i, s := range ss {
		out[i] = TrafficSampleView{BucketStart: s.BucketStart, ReceiveBytes: s.RxBytes, SendBytes: s.TxBytes}
	}
	return out
}

// ClientSessionView is one of a client's past or current connections.
type ClientSessionView struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	// EndedAt is absent while the session is still open.
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	Endpoint     string     `json:"endpoint"`
	ReceiveBytes int64      `json:"receive_bytes"`
	SendBytes    int64      `json:"send_bytes"`
}

// ClientSessions converts sessions to their views. The store already returns them
// newest first.
func ClientSessions(cs []store.ClientSession) []ClientSessionView {
	out := make([]ClientSessionView, len(cs))
	for i, c := range cs {
		out[i] = ClientSessionView{ID: c.ID, StartedAt: c.StartedAt, EndedAt: c.EndedAt,
			Endpoint: c.Endpoint, ReceiveBytes: c.RxBytes, SendBytes: c.TxBytes}
	}
	return out
}
