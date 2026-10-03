package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/views"
)

// Read-only API tokens (docs/PLAN.md §6.5), for a dashboard such as Homepage that can't log in.

// tokenReadable is the routes an API token may call: what a dashboard shows, and nothing it
// could use to get further. It's a list of what's allowed, so a route added later is closed to
// tokens until someone puts it here, and the tests say what can never be on it:
//
//   - A client's config holds its private key.
//   - A client's DNS log is what it browsed.
//   - The event log has who did what, and from where.
//   - The settings, the integrations, and the account's own routes (including these tokens)
//     are the admin's.
//
// Every entry is a GET. A token can't change anything.
var tokenReadable = map[string]bool{
	"GET /api/server/status":         true,
	"GET /api/clients":               true,
	"GET /api/clients/{id}":          true,
	"GET /api/clients/{id}/traffic":  true,
	"GET /api/clients/{id}/sessions": true,
	"GET /api/traffic":               true,
	"GET /api/traffic/clients":       true,
}

// requireAuth lets a request in if it has a valid session, or, for a route that tokens may use,
// a valid API token. A request that presents a token is a token's request, whatever else it
// carries: a cookie sent along doesn't widen it.
func (h *handler) requireAuth(route string, next http.Handler) http.Handler {
	session := h.requireSession(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			session.ServeHTTP(w, r)
			return
		}
		invalid := func() {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid API token"})
		}
		scheme, secret, _ := strings.Cut(header, " ")
		if !strings.EqualFold(scheme, "Bearer") {
			invalid()
			return
		}
		tok, err := h.svc.AuthenticateToken(r.Context(), strings.TrimSpace(secret))
		if err != nil {
			if errors.Is(err, store.ErrNoToken) {
				invalid()
				return
			}
			h.fail(w, err)
			return
		}
		if !tokenReadable[route] {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "an API token can't use this endpoint"})
			return
		}
		actor := service.ActorFrom(r.Context())
		actor.Name = "token: " + tok.Name
		next.ServeHTTP(w, r.WithContext(service.WithActor(r.Context(), actor)))
	})
}

func (h *handler) listTokens(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListAPITokens(r.Context(), sessionFrom(r.Context()).user.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, views.APITokens(list))
}

func (h *handler) createToken(w http.ResponseWriter, r *http.Request) {
	var req views.NewAPITokenRequest
	if err := decode(w, r, &req); err != nil {
		h.fail(w, err)
		return
	}
	tok, secret, err := h.svc.CreateAPIToken(r.Context(), sessionFrom(r.Context()).user, req.Password, req.Name)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, views.NewAPITokenResult{Token: views.NewAPIToken(tok), Secret: secret})
}

func (h *handler) revokeToken(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RevokeAPIToken(r.Context(), sessionFrom(r.Context()).user.ID, r.PathValue("id")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
