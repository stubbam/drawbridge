package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"github.com/stuffam/drawbridge/internal/views"
)

// Client talks to the daemon's control socket.
type Client struct {
	socket string
	http   *http.Client
}

// NewClient returns a Client for the socket at path.
func NewClient(path string) *Client {
	return &Client{
		socket: path,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", path)
				},
			},
		},
	}
}

// Error is an error the daemon returned.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://drawbridge"+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.dialError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var e views.Error
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = fmt.Sprintf("the daemon returned %s", resp.Status)
		}
		return &Error{Status: resp.StatusCode, Message: e.Error}
	}
	if s, ok := out.(*string); ok {
		*s = string(data)
		return nil
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) dialError(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("can't reach the Drawbridge daemon at %s; is drawbridge.service running?", c.socket)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("no permission to use %s; run as root (sudo) or as a member of the drawbridge group", c.socket)
	}
	return err
}

func clientPath(name string, suffix string) string {
	return "/v1/clients/" + url.PathEscape(name) + suffix
}

// Settings returns the server's settings.
func (c *Client) Settings(ctx context.Context) (views.SettingsView, error) {
	var v views.SettingsView
	return v, c.do(ctx, http.MethodGet, "/v1/settings", nil, &v)
}

// UpdateSettings applies a patch.
func (c *Client) UpdateSettings(ctx context.Context, p views.SettingsPatch) (views.SettingsResult, error) {
	var v views.SettingsResult
	return v, c.do(ctx, http.MethodPatch, "/v1/settings", p, &v)
}

// Clients lists every client with its live status.
func (c *Client) Clients(ctx context.Context) ([]views.ClientView, error) {
	var v []views.ClientView
	return v, c.do(ctx, http.MethodGet, "/v1/clients", nil, &v)
}

// Client returns one client with its live status.
func (c *Client) Client(ctx context.Context, name string) (views.ClientView, error) {
	var v views.ClientView
	return v, c.do(ctx, http.MethodGet, clientPath(name, ""), nil, &v)
}

// AddClient creates a client.
func (c *Client) AddClient(ctx context.Context, name string) (views.ClientResult, error) {
	var v views.ClientResult
	return v, c.do(ctx, http.MethodPost, "/v1/clients", views.NewClientRequest{Name: name}, &v)
}

// SetEnabled pauses (false) or resumes (true) a client.
func (c *Client) SetEnabled(ctx context.Context, name string, enabled bool) (views.ClientResult, error) {
	action := "/pause"
	if enabled {
		action = "/resume"
	}
	var v views.ClientResult
	return v, c.do(ctx, http.MethodPost, clientPath(name, action), nil, &v)
}

// DeleteClient deletes a client.
func (c *Client) DeleteClient(ctx context.Context, name string) (views.ClientResult, error) {
	var v views.ClientResult
	return v, c.do(ctx, http.MethodDelete, clientPath(name, ""), nil, &v)
}

// Config returns a client's WireGuard config.
func (c *Client) Config(ctx context.Context, name string) (string, error) {
	var s string
	return s, c.do(ctx, http.MethodGet, clientPath(name, "/config"), nil, &s)
}

// RenameClient renames a client.
func (c *Client) RenameClient(ctx context.Context, name, newName string) (views.ClientResult, error) {
	var v views.ClientResult
	return v, c.do(ctx, http.MethodPatch, clientPath(name, ""), views.ClientPatch{Name: &newName}, &v)
}

// Events returns recorded events, newest first. client ("" for all) is a client's name.
func (c *Client) Events(ctx context.Context, client string, limit int) ([]views.EventView, error) {
	q := url.Values{}
	if client != "" {
		q.Set("client", client)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var v []views.EventView
	return v, c.do(ctx, http.MethodGet, "/v1/events?"+q.Encode(), nil, &v)
}

// SetupToken returns the first-run setup token and the web UI certificate's
// fingerprint.
func (c *Client) SetupToken(ctx context.Context) (SetupTokenResult, error) {
	var v SetupTokenResult
	return v, c.do(ctx, http.MethodGet, "/v1/admin/setup-token", nil, &v)
}

// CreateAdmin creates the admin account and returns its random password.
func (c *Client) CreateAdmin(ctx context.Context, username string) (AdminResult, error) {
	var v AdminResult
	return v, c.do(ctx, http.MethodPost, "/v1/admin/create", AdminRequest{Username: username}, &v)
}

// ResetPassword gives the admin account a new random password ("" for the only account).
func (c *Client) ResetPassword(ctx context.Context, username string) (AdminResult, error) {
	var v AdminResult
	return v, c.do(ctx, http.MethodPost, "/v1/admin/reset-password", AdminRequest{Username: username}, &v)
}
