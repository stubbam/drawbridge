package adguard

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// Persistent is a client AdGuard Home knows by name (its "persistent client"): a name and the
// identifiers its queries come from, plus settings of its own that the admin may have changed
// in AdGuard Home.
//
// Only the name and the identifiers are fields here. The rest is kept as AdGuard Home sent it
// and sent back unchanged, because an update replaces the whole client: a struct with the
// fields this code knows would drop tags, upstreams, and settings, and any field a newer
// AdGuard Home adds.
type Persistent struct {
	Name string
	// IDs are the addresses (and, for a client the admin made, MAC addresses, CIDRs, or client
	// IDs) its queries are known by.
	IDs  []string
	rest map[string]json.RawMessage
}

// NewPersistent returns a client to add, with its name and identifiers, that uses AdGuard
// Home's global settings. Without that, AdGuard Home adds a client with its own filtering off,
// so naming a device would stop AdGuard Home blocking anything for it.
func NewPersistent(name string, ids []string) Persistent {
	return Persistent{Name: name, IDs: slices.Clone(ids), rest: map[string]json.RawMessage{
		"use_global_settings":         json.RawMessage("true"),
		"use_global_blocked_services": json.RawMessage("true"),
	}}
}

// UsesGlobalSettings reports whether the client is filtered by AdGuard Home's global settings
// and blocked services, as opposed to settings of its own.
func (p Persistent) UsesGlobalSettings() bool {
	return p.flag("use_global_settings") && p.flag("use_global_blocked_services")
}

func (p Persistent) flag(key string) bool {
	var b bool
	_ = json.Unmarshal(p.rest[key], &b)
	return b
}

// Field returns one of the client's other settings, as JSON.
func (p Persistent) Field(key string) (json.RawMessage, bool) {
	v, ok := p.rest[key]
	return v, ok
}

// WithField returns the client with another setting changed. The original is left alone.
func (p Persistent) WithField(key string, v any) (Persistent, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return Persistent{}, err
	}
	p.rest = cloneFields(p.rest)
	p.rest[key] = raw
	return p, nil
}

// cloneFields copies a client's other settings into a map that can be written to, even when
// there were none.
func cloneFields(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m)+2)
	maps.Copy(out, m)
	return out
}

// MarshalJSON writes the client as AdGuard Home's API takes it.
func (p Persistent) MarshalJSON() ([]byte, error) {
	m := cloneFields(p.rest)
	name, err := json.Marshal(p.Name)
	if err != nil {
		return nil, err
	}
	ids := p.IDs
	if ids == nil {
		ids = []string{}
	}
	idsJSON, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	m["name"], m["ids"] = name, idsJSON
	return json.Marshal(m)
}

// UnmarshalJSON reads a client as AdGuard Home's API gives it.
func (p *Persistent) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*p = Persistent{}
	if v, ok := m["name"]; ok {
		if err := json.Unmarshal(v, &p.Name); err != nil {
			return err
		}
		delete(m, "name")
	}
	if v, ok := m["ids"]; ok {
		if err := json.Unmarshal(v, &p.IDs); err != nil {
			return err
		}
		delete(m, "ids")
	}
	p.rest = m
	return nil
}

// NormalizeID puts an identifier in the form AdGuard Home keeps it in, so two spellings of the
// same one compare equal: addresses are written in their short lowercase form, and everything
// else (MAC addresses, CIDRs, client IDs) is lowercased.
func NormalizeID(id string) string {
	id = strings.TrimSpace(id)
	if a, err := netip.ParseAddr(id); err == nil {
		return a.Unmap().String()
	}
	return strings.ToLower(id)
}

// HasID reports whether ids has id, in any spelling.
func HasID(ids []string, id string) bool {
	id = NormalizeID(id)
	return slices.ContainsFunc(ids, func(x string) bool { return NormalizeID(x) == id })
}

// SameIDs reports whether two lists have the same identifiers, in any order and spelling.
func SameIDs(a, b []string) bool {
	norm := func(ids []string) []string {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = NormalizeID(id)
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.Equal(norm(a), norm(b))
}

// Clients lists AdGuard Home's persistent clients. The clients it found on its own (from
// /etc/hosts, its DHCP leases, and reverse lookups) aren't in the list.
func (c *Client) Clients(ctx context.Context) ([]Persistent, error) {
	var out struct {
		Clients []Persistent `json:"clients"`
	}
	if err := c.do(ctx, http.MethodGet, "/clients", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Clients, nil
}

// AddClient adds a persistent client. AdGuard Home refuses a name or an address another client
// already has (as an Error with status 400).
func (c *Client) AddClient(ctx context.Context, p Persistent) error {
	return c.do(ctx, http.MethodPost, "/clients/add", nil, p, nil)
}

// UpdateClient replaces the client called name with p, which may have another name. Start from
// the client Clients returned, and change what needs changing: everything in p is written.
func (c *Client) UpdateClient(ctx context.Context, name string, p Persistent) error {
	body := struct {
		Name string     `json:"name"`
		Data Persistent `json:"data"`
	}{name, p}
	return c.do(ctx, http.MethodPost, "/clients/update", nil, body, nil)
}

// DeleteClient deletes the client called name.
func (c *Client) DeleteClient(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/clients/delete", nil, map[string]string{"name": name}, nil)
}
