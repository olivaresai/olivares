// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

const inlineBoot = `console.log('boot')`

// The embedded index.html carries the __CSP_NONCE__ placeholder on the no-FOUC
// bootstrap and on Vite's module entry + csp-nonce meta (Vite's html.cspNonce).
func testWebFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {Data: []byte(
			`<!doctype html><html><head>` +
				`<meta property="csp-nonce" nonce="__CSP_NONCE__">` +
				`<script nonce="__CSP_NONCE__">` + inlineBoot + `</script></head>` +
				`<body><div id="root"></div>` +
				`<script type="module" nonce="__CSP_NONCE__" src="/assets/app-abc123.js"></script></body></html>`)},
		"assets/app-abc123.js": {Data: []byte(`console.log('app')`)},
		"favicon.svg":          {Data: []byte(`<svg/>`)},
	}
}

// sentinelAPI marks any request it handles so tests can prove routing.
func sentinelAPI() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler", "api")
		_, _ = w.Write([]byte("api:" + r.URL.Path))
	})
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, target, nil))
	return rr
}

var nonceRe = regexp.MustCompile(`script-src 'nonce-([^']+)' 'strict-dynamic'`)

func cspNonce(csp string) string {
	m := nonceRe.FindStringSubmatch(csp)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestSPA_ServesIndexAtRoot(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	rr := do(h, http.MethodGet, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	if !strings.Contains(rr.Body.String(), inlineBoot) {
		t.Errorf("body is not index.html: %q", rr.Body.String())
	}
	// The nonce'd document is single-use and must never be cached.
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if rr.Header().Get("X-Handler") == "api" {
		t.Error("root was routed to the API handler")
	}
}

func TestSPA_CSPLevel3NonceInjected(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	rr := do(h, http.MethodGet, "/")
	csp := rr.Header().Get("Content-Security-Policy")
	body := rr.Body.String()

	// L3 hardening directives must all be present.
	for _, must := range []string{
		"default-src 'none'",
		"'strict-dynamic'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"require-trusted-types-for 'script'",
		"trusted-types olivares-html default",
		"style-src 'self' 'unsafe-inline'",
		"connect-src 'self'",
	} {
		if !strings.Contains(csp, must) {
			t.Errorf("CSP missing %q: %q", must, csp)
		}
	}
	// The hash-based allowance is gone; strict-dynamic + nonce replace it.
	if strings.Contains(csp, "sha256") {
		t.Errorf("CSP should not carry a script hash anymore: %q", csp)
	}

	// The nonce in the header must be a non-empty value and must appear in the body
	// (so the served scripts are actually authorized) with NO placeholder left.
	nonce := cspNonce(csp)
	if nonce == "" {
		t.Fatalf("no script-src nonce in CSP: %q", csp)
	}
	if strings.Contains(body, "__CSP_NONCE__") {
		t.Errorf("nonce placeholder not fully substituted in body")
	}
	if !strings.Contains(body, `nonce="`+nonce+`"`) {
		t.Errorf("body does not carry the header nonce %q", nonce)
	}
}

func TestSPA_NoncePerResponse(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	n1 := cspNonce(do(h, http.MethodGet, "/").Header().Get("Content-Security-Policy"))
	n2 := cspNonce(do(h, http.MethodGet, "/").Header().Get("Content-Security-Policy"))
	if n1 == "" || n2 == "" {
		t.Fatalf("missing nonce(s): %q %q", n1, n2)
	}
	if n1 == n2 {
		t.Errorf("nonce was reused across responses: %q", n1)
	}
}

func TestSPA_FallbackForClientRoute(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	rr := do(h, http.MethodGet, "/inventory/agents")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), inlineBoot) {
		t.Errorf("unknown client route did not fall back to index.html: %q", rr.Body.String())
	}
	// Fallback routes also get the nonce'd document (so the SPA boots there too).
	if cspNonce(rr.Header().Get("Content-Security-Policy")) == "" {
		t.Errorf("client-route fallback served no CSP nonce")
	}
}

func TestSPA_ServesHashedAssetImmutable(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	rr := do(h, http.MethodGet, "/assets/app-abc123.js")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "app") {
		t.Fatalf("asset not served: status=%d body=%q", rr.Code, rr.Body.String())
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset Cache-Control = %q, want immutable", cc)
	}
}

func TestSPA_RoutesAPIPaths(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	// /livez, /readyz, /metrics are the operational probe/scrape roots: they MUST
	// reach the engine, not the SPA shell (C1 — the SPA-shadow regression that
	// made the Helm readinessProbe never see a 503). The last two are non-normalized
	// variants that resolve INTO the API namespace from a non-API prefix.
	for _, p := range []string{
		"/healthz", "/openapi.json", "/livez", "/readyz", "/metrics",
		"/v1/server-info", "/v1/auth/login", "/x/../v1/agents", "/./v1/server-info", "/./livez",
	} {
		rr := do(h, http.MethodGet, p)
		if rr.Header().Get("X-Handler") != "api" {
			t.Errorf("%s was not routed to the API handler", p)
		}
		if rr.Body.String() != "api:"+p {
			t.Errorf("%s body = %q", p, rr.Body.String())
		}
	}
}

// pingFailStore wraps a real store but reports the backend as unreachable, so the
// readiness probe takes its store-down branch (handleReadyz → 503).
type pingFailStore struct {
	store.Store
	err error
}

func (p pingFailStore) Ping(context.Context) error { return p.err }

// newProbeHandler builds a REAL engine API server (no modules needed for the probe
// surface) over the given store, wrapped by the SPA handler exactly as the binary
// composes them — so the test exercises the same path external probe traffic takes.
func newProbeHandler(t *testing.T, st store.Store) http.Handler {
	t.Helper()
	priv, _, err := secure.LoadOrCreateSigningKey(filepath.Join(t.TempDir(), "audit.key"))
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")),
		Version: "test",
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	return newSPAHandler(srv.Handler(), testWebFS())
}

// TestSPA_OperationalProbesReachEngine is the C1 behavioral regression: the
// liveness/readiness/metrics endpoints must reach the engine handlers THROUGH the
// SPA wrapper, so /readyz can answer 503 when the store is down (the LB drains the
// pod) — not the SPA shell's index.html 200 that silently defeated the Helm probe.
func TestSPA_OperationalProbesReachEngine(t *testing.T) {
	ctx := context.Background()
	base, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })

	// --- store DOWN: /readyz must be 503 JSON, NOT text/html 200 (the shadow bug) ---
	down := newProbeHandler(t, pingFailStore{Store: base, err: errors.New("store wedged")})
	rr := do(down, http.MethodGet, "/readyz")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with store down = %d, want 503", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("/readyz content-type = %q, want application/json (engine, not SPA)", ct)
	}
	if strings.Contains(rr.Body.String(), inlineBoot) {
		t.Errorf("/readyz returned the SPA shell instead of the engine 503: %q", rr.Body.String())
	}
	var ready struct {
		Status string `json:"status"`
		Store  string `json:"store"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &ready); err != nil || ready.Status != "unavailable" || ready.Store != "down" {
		t.Errorf("/readyz body = %q (err %v), want status=unavailable store=down", rr.Body.String(), err)
	}

	// --- store UP: /readyz 200, /livez 200, /metrics Prometheus, SPA fallback intact ---
	up := newProbeHandler(t, base)

	rr = do(up, http.MethodGet, "/readyz")
	if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json") {
		t.Errorf("/readyz with store up = %d ct=%q, want 200 JSON", rr.Code, rr.Header().Get("Content-Type"))
	}

	rr = do(up, http.MethodGet, "/livez")
	if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json") {
		t.Errorf("/livez = %d ct=%q, want 200 JSON", rr.Code, rr.Header().Get("Content-Type"))
	}
	if strings.Contains(rr.Body.String(), inlineBoot) {
		t.Errorf("/livez returned the SPA shell: %q", rr.Body.String())
	}

	rr = do(up, http.MethodGet, "/metrics")
	if rr.Code != http.StatusOK {
		t.Errorf("/metrics = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != metrics.ContentType {
		t.Errorf("/metrics content-type = %q, want %q (Prometheus, not text/html SPA)", ct, metrics.ContentType)
	}
	if strings.Contains(rr.Body.String(), inlineBoot) {
		t.Errorf("/metrics returned the SPA shell instead of the Prometheus exposition")
	}

	// The SPA fallback must remain intact for genuine client routes.
	for _, p := range []string{"/inventory", "/login"} {
		rr = do(up, http.MethodGet, p)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), inlineBoot) {
			t.Errorf("%s should still serve the SPA shell; got %d %q", p, rr.Code, rr.Body.String())
		}
		if !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s content-type = %q, want text/html", p, rr.Header().Get("Content-Type"))
		}
	}
}

func TestSPA_NonGetOnSPAPathRejected(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	rr := do(h, http.MethodPost, "/login")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /login status = %d, want 405", rr.Code)
	}
	// But a POST to an API path must still reach the API (it owns its methods).
	rr = do(h, http.MethodPost, "/v1/auth/login")
	if rr.Header().Get("X-Handler") != "api" {
		t.Error("POST /v1/auth/login was not routed to the API handler")
	}
}

func TestSPA_SecurityHeaders(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	rr := do(h, http.MethodGet, "/")
	for k, v := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestSPA_NoTraversalOutOfBundle(t *testing.T) {
	h := newSPAHandler(sentinelAPI(), testWebFS())
	// A traversal attempt resolves within the bundle root and, finding nothing,
	// falls back to the SPA shell — never escaping to the host filesystem.
	rr := do(h, http.MethodGet, "/../../etc/passwd")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), inlineBoot) {
		t.Errorf("traversal not contained: status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestBuildCSP_L3Shape(t *testing.T) {
	csp := buildCSP("TESTNONCE")
	if !strings.Contains(csp, "script-src 'nonce-TESTNONCE' 'strict-dynamic'") {
		t.Errorf("script-src not L3 nonce/strict-dynamic: %q", csp)
	}
	if strings.Contains(csp, "'self'") && !strings.Contains(csp, "style-src 'self'") {
		t.Errorf("unexpected 'self' in a script context: %q", csp)
	}
}
