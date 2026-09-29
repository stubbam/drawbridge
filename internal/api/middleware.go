package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/stuffam/drawbridge/internal/lan"
	"github.com/stuffam/drawbridge/internal/service"
	"github.com/stuffam/drawbridge/internal/store"
	"github.com/stuffam/drawbridge/internal/webui"
)

// sessionCookie holds the session token. The __Host- prefix makes browsers refuse it
// unless it's Secure, has Path=/, and has no Domain, so no other site or subdomain can
// set it.
const sessionCookie = "__Host-drawbridge"

// csrfHeader must be on every request that changes something. Sending a custom header
// cross-origin needs a CORS preflight, which Drawbridge never approves.
const csrfHeader = "X-Drawbridge"

// securityHeaders sets the headers every response gets (docs/PLAN.md §6.5). There's no
// HSTS: the certificate is self-signed, and HSTS would stop people from clicking
// through its warning.
func securityHeaders(csp string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

// inlineScript matches a script element without a src attribute.
var inlineScript = regexp.MustCompile(`(?s)<script(\s[^>]*)?>(.*?)</script>`)

// contentSecurityPolicy builds the policy for the web app: its own files only, and
// no inline scripts except the app shell's bootstrap script, allowed by its hash. The
// shell's elements have inline style attributes, so styles allow 'unsafe-inline'; styles
// can't run code.
func contentSecurityPolicy(ui fs.FS) string {
	scripts := []string{"'self'"}
	if ui != nil {
		if shell, err := fs.ReadFile(ui, webui.Shell); err == nil {
			for _, m := range inlineScript.FindAllSubmatch(shell, -1) {
				if strings.Contains(string(m[1]), "src=") {
					continue
				}
				sum := sha256.Sum256(m[2])
				scripts = append(scripts, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
			}
		}
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + strings.Join(scripts, " "),
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

// remoteAddr is the request's source address. Only the TCP connection counts:
// X-Forwarded-For and similar headers are ignored, so no proxy can make a request look
// like it came from the LAN (docs/PLAN.md §6.5).
func remoteAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	// Link-local addresses carry a zone (fe80::1%eth0), which prefixes never match.
	return ap.Addr().Unmap().WithZone("")
}

// allowlist refuses sources outside the admin allowlist. The firewall drops them first
// (docs/PLAN.md §5.3); this is the second, independent layer. It also attributes the
// request to its source, for the event log.
func allowlist(allowed func(context.Context) []netip.Prefix, log *slog.Logger, next http.Handler) http.Handler {
	var (
		mu       sync.Mutex
		lastLog  time.Time
		refusals int
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr := remoteAddr(r)
		if allowed != nil && !lan.Contains(allowed(r.Context()), addr) {
			// Log at most once a minute, so a scan can't flood the journal.
			mu.Lock()
			refusals++
			if time.Since(lastLog) > time.Minute {
				log.Warn("refused requests from outside the home network and the VPN",
					"source", addr.String(), "count", refusals)
				lastLog, refusals = time.Now(), 0
			}
			mu.Unlock()
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "the Drawbridge admin UI is only reachable from the home network and the VPN",
			})
			return
		}
		ctx := service.WithActor(r.Context(), service.Actor{Via: service.ViaWeb, SourceIP: addr.String()})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type handler struct {
	svc *service.Service
	log *slog.Logger
}

// checkCSRF refuses changes that a browser could have sent on another site's behalf
// (docs/PLAN.md §6.5). SameSite=Strict already keeps the cookie off cross-site
// requests; these checks don't depend on the browser honoring it.
func (h *handler) checkCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		refuse := func(why string) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": why})
		}
		if r.Header.Get(csrfHeader) == "" {
			refuse("changes need the " + csrfHeader + " header")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "https://"+r.Host {
			refuse("cross-origin requests aren't allowed")
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			refuse("cross-site requests aren't allowed")
			return
		}
		if r.ContentLength != 0 {
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "the request body must be JSON"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type sessionKey struct{}

type sessionInfo struct {
	session store.Session
	user    store.User
}

func sessionFrom(ctx context.Context) sessionInfo {
	s, _ := ctx.Value(sessionKey{}).(sessionInfo)
	return s
}

// requireSession refuses requests without a valid session, and attributes the rest to
// the logged-in account.
func (h *handler) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		token := ""
		if err == nil {
			token = c.Value
		}
		sess, u, err := h.svc.Authenticate(r.Context(), token)
		if errors.Is(err, store.ErrNoSession) {
			if token != "" {
				clearSessionCookie(w)
			}
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not logged in"})
			return
		}
		if err != nil {
			h.fail(w, err)
			return
		}
		actor := service.ActorFrom(r.Context())
		actor.Name = u.Username
		ctx := service.WithActor(r.Context(), actor)
		ctx = context.WithValue(ctx, sessionKey{}, sessionInfo{session: sess, user: u})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}
