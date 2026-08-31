// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
)

// The single-binary control plane serves BOTH the JSON API and the embedded web
// UI from the SAME origin (ARCHITECTURE.md,§8). newSPAHandler is the composition-root
// wrapper that routes API paths to the engine's hardened handler and everything
// else to the embedded SPA, with client-side-routing fallback. The SPA is served
// OUTSIDE the API's authenticate/setup-gate middleware on purpose: the bundle is
// static, public, same-origin assets (the API calls inside it are what's
// authenticated), and the engine's API CSP (`default-src 'none'`) would otherwise
// break the app — so SPA responses get their own strict, self-derived CSP.
//
// CSP Level 3 + Trusted Types (ADM-CORE-05): the SPA document is served with a
// strict L3 policy — `script-src 'nonce-<random>' 'strict-dynamic'`, `object-src
// 'none'`, `base-uri 'none'`, `frame-ancestors 'none'`, and `require-trusted-types-
// for 'script'` + a named `trusted-types` allow-list. A fresh 128-bit nonce is
// generated PER RESPONSE and substituted into the `__CSP_NONCE__` placeholders that
// Vite stamped onto the module entry, the modulepreload links and the csp-nonce
// meta (and that index.html carries on the no-FOUC bootstrap). Because the nonce is
// injected into BOTH the header and the document body in the same response, the
// policy can never drift from the script it must allow — the same anti-desync
// guarantee the previous hash-derivation gave, now stronger (strict-dynamic +
// nonce). The document is therefore per-response and `no-store`; the content-hashed
// /assets/* chunks stay immutable-cacheable. `style-src` keeps `'unsafe-inline'`:
// React/Radix/Recharts/Sigma set inline style ATTRIBUTES (positioning, transforms),
// which a nonce cannot cover and which are benign on a script-locked document.

// noncePlaceholder is the literal Vite stamps via `html.cspNonce` (vite.config.ts)
// onto every script/style/modulepreload it emits, plus the csp-nonce meta; the
// no-FOUC bootstrap in index.html carries it too. Replaced per response.
var noncePlaceholder = []byte("__CSP_NONCE__")

// isAPIPath reports whether a request must reach the engine API rather than the SPA.
// It matches the whole /v1 tree plus the engine's root-level, non-/v1 endpoints,
// sourced from the SAME api.RootEnginePaths the setup-gate exemption uses — so the
// SPA router can never again shadow /livez, /readyz or /metrics (which would make
// them return index.html 200 and silently defeat the Helm readinessProbe C1).
// The path is cleaned first so a non-normalized request (e.g. "//v1/agents" or
// "/./livez" from a proxy or tool) still routes to the API, not the SPA shell.
func isAPIPath(p string) bool {
	p = path.Clean(p)
	if p == "/v1" || strings.HasPrefix(p, "/v1/") {
		return true
	}
	// the top-level AuthZEN tree (/access/v1/...) is engine API, not the SPA;
	// its discovery doc rides RootEnginePaths below like the other root endpoints.
	if strings.HasPrefix(p, api.AuthZenPathPrefix) {
		return true
	}
	return slices.Contains(api.RootEnginePaths, p)
}

// newSPAHandler wraps the engine API handler with the embedded web UI.
func newSPAHandler(engineAPI http.Handler, assets fs.FS) http.Handler {
	s := &spaServer{fsys: assets}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) {
			engineAPI.ServeHTTP(w, r)
			return
		}
		s.serve(w, r)
	})
}

type spaServer struct {
	fsys      fs.FS
	once      sync.Once
	indexHTML []byte
	indexErr  error
}

func (s *spaServer) serve(w http.ResponseWriter, r *http.Request) {
	s.once.Do(s.load)

	// The SPA only answers GET/HEAD; a write to a non-API path is a client error.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		s.serveIndex(w, r)
		return
	}

	data, err := fs.ReadFile(s.fsys, name)
	if err != nil {
		// Unknown path → a client-side route (e.g. /inventory, /login) → the SPA
		// shell (200). Returning index.html, not 404, is what lets the browser
		// router take over (ARCHITECTURE.md: "fallback to index.html").
		s.serveIndex(w, r)
		return
	}

	setSecurityHeaders(w)
	// Vite emits content-hashed asset filenames under assets/, safe to cache hard;
	// everything else (favicon, etc.) revalidates so a redeploy is picked up.
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func (s *spaServer) serveIndex(w http.ResponseWriter, r *http.Request) {
	if s.indexErr != nil {
		http.Error(w, "web UI bundle missing", http.StatusInternalServerError)
		return
	}
	nonce, err := randomNonce()
	if err != nil {
		http.Error(w, "nonce generation failed", http.StatusInternalServerError)
		return
	}
	setSecurityHeaders(w)
	// The document carries the SPA's own L3 CSP with this response's nonce; it must
	// never be cached (the nonce is single-use) so a new bundle is also picked up
	// immediately after deploy.
	w.Header().Set("Content-Security-Policy", buildCSP(nonce))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Inject the nonce into every placeholder Vite/index.html left, so the header
	// and the body share it atomically (no drift possible).
	html := bytes.ReplaceAll(s.indexHTML, noncePlaceholder, []byte(nonce))
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(html))
}

func (s *spaServer) load() {
	data, err := fs.ReadFile(s.fsys, "index.html")
	if err != nil {
		s.indexErr = err
		return
	}
	s.indexHTML = data
}

// randomNonce returns a fresh, base64-encoded 128-bit value for a single response's
// CSP nonce.
func randomNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// buildCSP returns the strict CSP Level 3 policy for the SPA document, authorizing
// scripts ONLY via the per-response nonce + 'strict-dynamic' (the host allow-list is
// intentionally discarded by strict-dynamic), locking object/base-uri/frame
// ancestors, and enforcing Trusted Types for every script sink with a named policy
// allow-list. Inline style ATTRIBUTES stay permitted (see file header) — they carry
// no script and are essential to React/Radix/Recharts/Sigma rendering.
func buildCSP(nonce string) string {
	return strings.Join([]string{
		"default-src 'none'",
		"base-uri 'none'",
		"script-src 'nonce-" + nonce + "' 'strict-dynamic'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		// Vite inlines small font subsets (< 4 KB) as data: URIs in the bundled CSS;
		// allow them (fonts are inert, this never weakens script-src). Mirrors img-src.
		"font-src 'self' data:",
		// The SPA document links /site.webmanifest (web/index.html) and THIS handler
		// serves it — but a manifest has no directive of its own, so without this line
		// it falls back to default-src 'none' and Chromium refuses it. Measured in a
		// real browser against this handler on 2026-08-24: /login and /work each logged
		// "Loading a manifest from '/site.webmanifest' violates ... default-src 'none'.
		// Note that 'manifest-src' was not explicitly set". The product shipped the
		// manifest, served it, linked it, and forbade it.
		//
		// ⚠ THE EMISSION IS CONDITIONAL, and saying otherwise would mislead whoever
		// tries to reproduce it. Another lane walked 50 routes against a binary WITH
		// this defect present and saw ZERO console errors, /login included, waiting on
		// networkidle: Chromium does not fetch the manifest on every load, only when it
		// needs it. So a clean console run is NOT evidence that the policy is right --
		// which is the whole reason the guard below is a test over the shipped document
		// rather than a browser check.
		//
		// 'self' only, and it is inert like font-src/img-src above: a manifest carries
		// no script and cannot weaken script-src.
		"manifest-src 'self'",
		"connect-src 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"object-src 'none'",
		"require-trusted-types-for 'script'",
		// `dompurify` is NOT decoration and NOT a relaxation: without it the safety
		// net silently DESTROYS content instead of sanitizing it. Measured in
		// Chromium 151 against this very handler:
		//
		//   1. DOMPurify creates its own pass-through policy, named `dompurify`, for
		//      the ONE sink it must use internally to parse a payload into an inert
		//      document (DOMParser.parseFromString — a Trusted Types sink here).
		//   2. Omitted from this list, that createPolicy is refused, DOMPurify logs
		//      "TrustedTypes policy dompurify could not be created" and carries on
		//      with a RAW STRING (purify.es.mjs:1340).
		//   3. That raw string re-enters the `default` policy — whose createHTML IS
		//      DOMPurify.sanitize — so the parse recurses. Both of DOMPurify's parse
		//      paths sit inside bare `catch (_) {}` blocks (:1348, :1355), so the
		//      overflow is SWALLOWED, `body` comes back empty and sanitize returns
		//      "" (:2257). `div.innerHTML = "<b>bold</b>"` yields "".
		//
		// So the documented contract of web/src/security/trusted-types.ts ("it
		// sanitizes via DOMPurify rather than letting the page hard-fail") was the
		// opposite of the behavior: safe markup was wiped alongside dangerous
		// markup, with no error anywhere. Allowing the name restores sanitisation.
		// It is not a script-execution grant: the policy only ever mints TrustedHTML
		// for DOMPurify's internal parse, `require-trusted-types-for 'script'` still
		// governs every sink, and duplicate policy names are rejected by default
		// (no 'allow-duplicates'), so the first claimant owns it — which is why
		// installTrustedTypes() primes DOMPurify eagerly, before any lazy chunk runs.
		"trusted-types olivares-html default dompurify",
	}, "; ")
}

// setSecurityHeaders mirrors the engine API's conservative headers for the static
// surface (the API sets these on its own responses via its middleware).
func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	// Safe because the server is TLS-by-default; ignored by browsers over plain
	// HTTP (--insecure dev), so it never hurts.
	h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
}
