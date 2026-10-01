// Package api serves Drawbridge's HTTP interface: the JSON API (docs/PLAN.md §8), which
// openapi.json documents, and the embedded web app.
//
// Every request must come from the admin allowlist: loopback, link-local, the VPN, or
// the LAN (docs/PLAN.md §6.5). API calls other than logging in need a session cookie,
// and every change needs the X-Drawbridge header, which a cross-site form or a simple
// cross-origin request can't send.
package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"path"
	"strings"

	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/version"
	"github.com/stuffam/drawbridge/internal/webui"
)

// Options configure the HTTP interface.
type Options struct {
	// UI is the built web app; see package webui.
	UI fs.FS
	// Service runs the API. Without it, only the web app and the health checks are
	// served.
	Service *service.Service
	Log     *slog.Logger
	// Allowed returns the sources that may connect (lan.Allowlist). Nil allows every
	// source; only tests leave it nil.
	Allowed func(context.Context) []netip.Prefix
}

// route is one API endpoint. The table is the single list of routes, which the
// OpenAPI test checks against openapi.json.
type route struct {
	method, pattern string
	// public routes work without logging in.
	public  bool
	handler func(*handler, http.ResponseWriter, *http.Request)
}

var routes = []route{
	{"GET", "/api/version", true, (*handler).version},
	{"GET", "/api/openapi.json", true, (*handler).openAPI},
	{"GET", "/api/setup", true, (*handler).setupStatus},
	{"POST", "/api/setup", true, (*handler).setup},
	{"POST", "/api/auth/login", true, (*handler).login},
	{"POST", "/api/auth/logout", false, (*handler).logout},
	{"GET", "/api/auth/me", false, (*handler).me},
	{"POST", "/api/auth/password", false, (*handler).changePassword},
	{"GET", "/api/auth/sessions", false, (*handler).sessions},
	{"DELETE", "/api/auth/sessions/{id}", false, (*handler).revokeSession},
	{"GET", "/api/server", false, (*handler).getServer},
	{"PATCH", "/api/server", false, (*handler).patchServer},
	{"GET", "/api/server/status", false, (*handler).serverStatus},
	{"GET", "/api/server/dns-check", false, (*handler).dnsCheck},
	{"GET", "/api/clients", false, (*handler).listClients},
	{"POST", "/api/clients", false, (*handler).addClient},
	{"GET", "/api/clients/{id}", false, (*handler).getClient},
	{"PATCH", "/api/clients/{id}", false, (*handler).patchClient},
	{"DELETE", "/api/clients/{id}", false, (*handler).deleteClient},
	{"POST", "/api/clients/{id}/pause", false, (*handler).pauseClient},
	{"POST", "/api/clients/{id}/resume", false, (*handler).resumeClient},
	{"GET", "/api/clients/{id}/config", false, (*handler).clientConfig},
	{"GET", "/api/clients/{id}/traffic", false, (*handler).clientTraffic},
	{"GET", "/api/clients/{id}/sessions", false, (*handler).clientSessions},
	{"GET", "/api/traffic", false, (*handler).totalTraffic},
	{"GET", "/api/traffic/clients", false, (*handler).clientsTraffic},
	{"GET", "/api/events", false, (*handler).events},
}

// New returns the handler for the whole HTTP interface.
func New(opts Options) http.Handler {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	h := &handler{svc: opts.Service, log: opts.Log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	if opts.Service != nil {
		for _, rt := range routes {
			fn := rt.handler
			var next http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fn(h, w, r) })
			if !rt.public {
				next = h.requireSession(next)
			}
			mux.Handle(rt.method+" "+rt.pattern, h.checkCSRF(next))
		}
	} else {
		mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) { h.version(w, r) })
	}
	// Unknown API paths get a JSON 404, never the web app's fallback page.
	mux.HandleFunc("/api/", notFound)
	mux.Handle("/", app(opts.UI))
	return securityHeaders(contentSecurityPolicy(opts.UI), allowlist(opts.Allowed, opts.Log, mux))
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// The status line is already sent, so an encoding error (a client that went
	// away) can't be reported.
	_ = json.NewEncoder(w).Encode(body)
}

// app serves files from the built web app, and the fallback page for every other path.
func app(ui fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != webui.Shell {
			if info, err := fs.Stat(ui, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "_app/immutable/") {
					// File names under _app/immutable/ contain a content hash.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				// io/fs rejects ".." in names (fs.ValidPath), and ui is the embedded web
				// app, so name can't reach outside it.
				http.ServeFileFS(w, r, ui, name) //nolint:gosec // G703, see above.
				return
			}
		}

		shell, err := fs.ReadFile(ui, webui.Shell)
		if err != nil {
			http.Error(w, "The web UI isn't built into this binary. Run `make web`, then rebuild.",
				http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(shell)
	})
}

func (h *handler) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
	})
}
