package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stuffam/drawbridge/internal/version"
)

var builtUI = fstest.MapFS{
	"200.html":                      {Data: []byte("<!doctype html><title>shell</title>")},
	"favicon.svg":                   {Data: []byte("<svg/>")},
	"_app/immutable/entry/app.js":   {Data: []byte("console.log(1)")},
	"_app/immutable/assets/app.css": {Data: []byte("body{}")},
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	rec := get(t, New(Options{UI: builtUI}), "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body %v, want status ok", body)
	}
}

func TestVersion(t *testing.T) {
	rec := get(t, New(Options{UI: builtUI}), "/api/version")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != version.Version || body["commit"] != version.Commit {
		t.Fatalf("body %v, want version %q and commit %q", body, version.Version, version.Commit)
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	rec := get(t, New(Options{UI: builtUI}), "/api/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q, want application/json (not the web app's fallback page)", ct)
	}
}

func TestAppServesFiles(t *testing.T) {
	h := New(Options{UI: builtUI})

	rec := get(t, h, "/favicon.svg")
	if rec.Code != http.StatusOK || rec.Body.String() != "<svg/>" {
		t.Fatalf("favicon: status %d body %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("favicon Cache-Control %q, want no-cache", cc)
	}

	rec = get(t, h, "/_app/immutable/entry/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("immutable asset: status %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("immutable asset Cache-Control %q, want immutable", cc)
	}
}

func TestAppFallsBackToShell(t *testing.T) {
	h := New(Options{UI: builtUI})
	for _, target := range []string{"/", "/clients", "/clients/42", "/_app/immutable", "/etc/passwd"} {
		rec := get(t, h, target)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", target, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "<title>shell</title>") {
			t.Errorf("%s: body %q, want the fallback page", target, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", target, ct)
		}
	}
}

func TestPathTraversalIsRedirectedToCleanPath(t *testing.T) {
	rec := get(t, New(Options{UI: builtUI}), "/../etc/passwd")
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status %d, want 307", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/etc/passwd" {
		t.Fatalf("Location %q, want /etc/passwd (which serves the fallback page)", loc)
	}
}

func TestAppNotBuilt(t *testing.T) {
	rec := get(t, New(Options{UI: fstest.MapFS{".keep": {}}}), "/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "make web") {
		t.Fatalf("body %q, want a hint to run make web", rec.Body.String())
	}
}

func TestWrongMethodIsRejected(t *testing.T) {
	rec := httptest.NewRecorder()
	New(Options{UI: builtUI}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz: status %d, want 405", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	if hsts := get(t, New(Options{UI: builtUI}), "/").Header().Get("Strict-Transport-Security"); hsts != "" {
		t.Errorf("HSTS %q: a self-signed certificate mustn't be pinned", hsts)
	}
	for _, target := range []string{"/healthz", "/", "/api/nope"} {
		rec := get(t, New(Options{UI: builtUI}), target)
		for header, want := range map[string]string{
			"X-Content-Type-Options":       "nosniff",
			"X-Frame-Options":              "DENY",
			"Referrer-Policy":              "no-referrer",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Resource-Policy": "same-origin",
		} {
			if got := rec.Header().Get(header); got != want {
				t.Errorf("%s: %s = %q, want %q", target, header, got, want)
			}
		}
	}
}

// shell is the shape of SvelteKit's app shell: a bootstrap script inline, the rest in
// files.
const shell = `<!doctype html>
<html><head><link href="/_app/immutable/entry/start.js" rel="modulepreload">
<script src="/_app/immutable/other.js"></script></head>
<body><div style="display: contents">
			<script>
				{ __sveltekit_x = { base: "" }; }
			</script>
		</div></body></html>`

func TestContentSecurityPolicy(t *testing.T) {
	ui := fstest.MapFS{"200.html": {Data: []byte(shell)}}
	sum := sha256.Sum256([]byte("\n\t\t\t\t{ __sveltekit_x = { base: \"\" }; }\n\t\t\t"))
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"

	for _, target := range []string{"/", "/clients", "/api/version", "/healthz"} {
		csp := get(t, New(Options{UI: ui}), target).Header().Get("Content-Security-Policy")
		directives := map[string]string{}
		for _, d := range strings.Split(csp, "; ") {
			name, value, _ := strings.Cut(d, " ")
			directives[name] = value
		}
		if got := directives["script-src"]; got != "'self' "+hash {
			t.Errorf("%s: script-src %q, want 'self' and exactly the bootstrap script's hash %s", target, got, hash)
		}
		for name, want := range map[string]string{
			"default-src":     "'self'",
			"object-src":      "'none'",
			"base-uri":        "'none'",
			"frame-ancestors": "'none'",
			"connect-src":     "'self'",
		} {
			if directives[name] != want {
				t.Errorf("%s: %s %q, want %q", target, name, directives[name], want)
			}
		}
	}
	// Without a built app, no inline script is allowed at all.
	csp := get(t, New(Options{UI: fstest.MapFS{}}), "/healthz").Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self';") {
		t.Errorf("CSP without an app: %q", csp)
	}
}

func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	var doc struct {
		Info       struct{ Version string }
		Paths      map[string]map[string]json.RawMessage
		Components map[string]map[string]json.RawMessage
	}
	if err := json.Unmarshal(openAPIDoc, &doc); err != nil {
		t.Fatalf("openapi.json isn't valid JSON: %v", err)
	}
	type operation struct {
		Security   *[]map[string]any
		Parameters []struct {
			Ref string `json:"$ref"`
		}
	}
	documented := map[string]operation{}
	for path, ops := range doc.Paths {
		for method, raw := range ops {
			var op operation
			if err := json.Unmarshal(raw, &op); err != nil {
				t.Fatal(err)
			}
			documented[strings.ToUpper(method)+" "+path] = op
		}
	}
	served := map[string]route{"GET /healthz": {public: true}}
	for _, rt := range routes {
		served[rt.method+" "+rt.pattern] = rt
	}
	for key, rt := range served {
		op, ok := documented[key]
		if !ok {
			t.Errorf("%s is served but not in openapi.json", key)
			continue
		}
		if public := op.Security != nil && len(*op.Security) == 0; public != rt.public {
			t.Errorf("%s: openapi.json says public %v, the route table says %v", key, public, rt.public)
		}
		// The operations that say an API token works are the ones the middleware lets a token in
		// to, so the document can't promise a client what the server refuses.
		takesToken := false
		if op.Security != nil {
			for _, scheme := range *op.Security {
				_, ok := scheme["token"]
				takesToken = takesToken || ok
			}
		}
		if takesToken != tokenReadable[key] {
			t.Errorf("%s: openapi.json says a token works %v, tokenReadable says %v", key, takesToken, tokenReadable[key])
		}
		method, _, _ := strings.Cut(key, " ")
		csrf := false
		for _, p := range op.Parameters {
			csrf = csrf || p.Ref == "#/components/parameters/CSRF"
		}
		if unsafe := method != "GET"; csrf != unsafe {
			t.Errorf("%s: documents the %s header: %v, want %v", key, csrfHeader, csrf, unsafe)
		}
	}
	for key := range documented {
		if _, ok := served[key]; !ok {
			t.Errorf("%s is in openapi.json but not served", key)
		}
	}

	// Every reference resolves.
	for _, ref := range regexp.MustCompile(`"\$ref": "#/components/(\w+)/(\w+)"`).FindAllStringSubmatch(string(openAPIDoc), -1) {
		if _, ok := doc.Components[ref[1]][ref[2]]; !ok {
			t.Errorf("unresolved reference %s", ref[0])
		}
	}
}

func TestOpenAPIIsServedWithTheVersion(t *testing.T) {
	h := New(Options{UI: builtUI, Service: newService(t)})
	rec := get(t, h, "/api/openapi.json")
	var doc struct{ Info struct{ Version string } }
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Info.Version != strings.TrimPrefix(version.Version, "v") || strings.HasPrefix(doc.Info.Version, "v") {
		t.Fatalf("info.version %q, want %q without the v", doc.Info.Version, version.Version)
	}
}
