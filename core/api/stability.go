// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// API stability marking (EXT-1). The public stability policy
// (docs-site: reference/api-stability) commits to RFC 9745 Deprecation and
// RFC 8594 Sunset signaling with minimum support windows per tier. This file
// is the enforceable half of that policy: routeDeprecations is the single
// in-code deprecation table, consumed by
//   - the deprecationHeaders middleware (response headers on deprecated routes),
//   - buildOpenAPI (deprecated: true + x-deprecated-at / x-sunset-at /
//     x-migration-guide on the published spec), and
//   - the stability tests, which fail the build on a sunset scheduled earlier
//     than the tier's minimum window after its deprecation announcement.

// StabilityTier classifies a published API surface for the stability policy.
type StabilityTier string

const (
	// StabilityStable is the contract tier: covered by the published OpenAPI
	// document, breaking-change-free within /v1, minimum 24 months between a
	// deprecation announcement and its sunset.
	StabilityStable StabilityTier = "stable"
	// StabilityBeta is the opt-in preview tier: shapes may still change with
	// notice, minimum 12 months between deprecation and sunset.
	StabilityBeta StabilityTier = "beta"
)

// minSupportWindowMonths is the policy's minimum span, in calendar months,
// between a surface's deprecation announcement and its sunset.
func (t StabilityTier) minSupportWindowMonths() int {
	if t == StabilityBeta {
		return 12
	}
	return 24
}

// stabilityPolicyURL is the canonical, human-readable stability policy this
// code enforces; published in the OpenAPI document (info.x-stability-policy)
// and linked from deprecation responses when an entry sets no Docs URL.
//
// It points at the docs ROOT on olivares.ai (2026-08-01: «ahora mismo están en
// olivares.ai/docs … [docs.olivares.ai] es irrelevante»). It previously pointed at
// docs.olivares.ai/reference/api-stability/, which then resolved to NOTHING: that
// hostname routed to the web Worker, not to a build of docs-site/, so every
// deprecated-route response shipped a dead link to whoever followed our own
// deprecation signal.
//
// ⛔ THE PREMISE UNDER THAT CHOICE HAS EXPIRED, and this note says so rather than
// letting the paragraph above go on reading as current. Re-measured 2026-08-27:
//
//	https://docs.olivares.ai/reference/api-stability/   -> 200, in all 7 locales
//	https://olivares.ai/docs/reference/api-stability    -> 404 (the apex has no depth)
//
// docs-site IS deployed now, as the `olivares-docs` Worker, and docs.olivares.ai serves
// it. Both halves of the old reasoning are gone: the page no longer "exists only in the
// undeployed docs-site", and the 13-locale cost was the cost of publishing it on the
// MARKETING site — on the docs site it already ships in 7 locales, translated and gated.
//
// ⚠ AND THE CONSTANT IS NOT CHANGED HERE, deliberately. Two reasons, both stated so the
// next session decides instead of rediscovering:
//  1. the line it would overturn carries FRAN'S WORDS. They were a statement of where
//     the docs were, with «docs.olivares.ai es irrelevante» as the reason — and the
//     reason is what expired. That is a re-decision worth making explicitly, not a
//     comment refresh to slip into an unrelated lot.
//  2. this constant is published in the OpenAPI document (info.x-stability-policy), so
//     changing it makes `task openapi:check` demand a regenerated snapshot and typed
//     client. Cheap, but it is a separate change with its own verification.
//
// ⇒ Repointing to https://docs.olivares.ai/reference/api-stability/ is one line plus that
// regen, and it is the better link: a deprecation response should hand the reader the
// policy, not a documentation homepage. Reported to PLAN with the measurement above.
const stabilityPolicyURL = "https://olivares.ai/docs"

// RouteDeprecation declares one deprecated REST route. Method and Path use the
// spec-canonical form: upper-case method plus the OpenAPI path (no trailing
// slash), e.g. "GET" + "/v1/agents/{id}".
type RouteDeprecation struct {
	Method string
	Path   string
	// Tier selects the minimum support window (stable: 24 months, beta: 12).
	Tier StabilityTier
	// DeprecatedAt is the announcement instant, emitted as the RFC 9745
	// Deprecation field (a Structured Field Date: "@" + Unix seconds).
	DeprecatedAt time.Time
	// SunsetAt, when non-zero, is the scheduled removal instant, emitted as the
	// RFC 8594 Sunset field (an HTTP-date). RFC 9745 §3: it MUST NOT be earlier
	// than DeprecatedAt; the policy tests additionally hold it to the tier's
	// minimum window.
	SunsetAt time.Time
	// Docs is the migration guide URL, emitted as Link rel="deprecation" (and
	// rel="sunset" once a sunset is scheduled). Empty falls back to the policy.
	Docs string
}

// routeDeprecations is the deprecation table. It is intentionally EMPTY today:
// nothing on the published surface is deprecated. A deprecation lands as one
// entry here (plus a migration guide) in the same change that announces it —
// the middleware, the OpenAPI document and the policy tests pick it up from
// this single declaration.
var routeDeprecations = []RouteDeprecation{}

// indexDeprecations keys a deprecation list by "METHOD path" for the request
// hot path.
func indexDeprecations(list []RouteDeprecation) map[string]RouteDeprecation {
	if len(list) == 0 {
		return nil
	}
	m := make(map[string]RouteDeprecation, len(list))
	for _, d := range list {
		m[strings.ToUpper(d.Method)+" "+d.Path] = d
	}
	return m
}

// canonicalRoutePattern maps a matched chi route pattern to the spec-canonical
// path used as the deprecation key: chi reports collection routes registered as
// Route("/agents", r.Get("/")) with a trailing slash that the OpenAPI document
// (and so the table) does not carry.
func canonicalRoutePattern(p string) string {
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// docsLink returns the Link target for an entry: its migration guide, or the
// policy document while a guide has not shipped yet.
func (d RouteDeprecation) docsLink() string {
	if d.Docs != "" {
		return d.Docs
	}
	return stabilityPolicyURL
}

// deprecationHeaders emits the policy's deprecation signal on every response of
// a deprecated route. The matched route pattern is only known after routing, so
// the headers are injected by a writer wrapper immediately before the first
// byte (or after the handler returns untouched, for header-only responses) —
// never after a response has started. With an empty table the middleware
// removes itself from the chain.
//
// Known limit: a handler that streams through the unwrap chain (ResponseController
// Flush against the base writer) bypasses the wrapper; no deprecated route may
// stream today, and the stability tests pin every table entry to a real route.
func (s *Server) deprecationHeaders(next http.Handler) http.Handler {
	if len(s.deprecations) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dw := &deprecationWriter{ResponseWriter: w, req: r, srv: s}
		next.ServeHTTP(dw, r)
		// Nothing written: net/http flushes the header after the chain returns,
		// so injecting here still lands on the wire.
		dw.inject()
	})
}

// deprecationWriter injects the deprecation headers exactly once, before the
// response commits.
type deprecationWriter struct {
	http.ResponseWriter
	req      *http.Request
	srv      *Server
	injected bool
}

func (w *deprecationWriter) inject() {
	if w.injected {
		return
	}
	w.injected = true
	rctx := chi.RouteContext(w.req.Context())
	if rctx == nil {
		return
	}
	pattern := rctx.RoutePattern()
	if pattern == "" && w.srv.mux != nil {
		// Pre-routing write: authenticate (401), the rate limiter (429) and the
		// setup gate (409) all respond from the middleware chain BEFORE chi has
		// routed, so the live route context carries no pattern yet. The policy
		// promises the signal on every response of a deprecated route, so match
		// the request against the router explicitly (a fresh context: the live
		// one must not be mutated).
		tctx := chi.NewRouteContext()
		if w.srv.mux.Match(tctx, w.req.Method, w.req.URL.Path) {
			pattern = tctx.RoutePattern()
		}
	}
	d, ok := w.srv.deprecations[w.req.Method+" "+canonicalRoutePattern(pattern)]
	if !ok {
		return
	}
	h := w.Header()
	// RFC 9745: a Structured Field Date ("@" + Unix seconds).
	h.Set("Deprecation", "@"+strconv.FormatInt(d.DeprecatedAt.Unix(), 10))
	h.Add("Link", "<"+d.docsLink()+`>; rel="deprecation"`)
	if !d.SunsetAt.IsZero() {
		// RFC 8594: an HTTP-date, always GMT.
		h.Set("Sunset", d.SunsetAt.UTC().Format(http.TimeFormat))
		h.Add("Link", "<"+d.docsLink()+`>; rel="sunset"`)
	}
}

func (w *deprecationWriter) WriteHeader(code int) {
	w.inject()
	w.ResponseWriter.WriteHeader(code)
}

func (w *deprecationWriter) Write(b []byte) (int, error) {
	w.inject()
	return w.ResponseWriter.Write(b)
}

// Unwrap exposes the base writer for http.ResponseController, mirroring
// statusRecorder.
func (w *deprecationWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// applyStability decorates the STABLE OpenAPI paths (the core contract) with the
// stability contract: every published operation carries x-stability=stable, and a
// table entry adds the standard deprecated flag plus the policy's x- extensions.
func applyStability(paths map[string]any) { applyStabilityTier(paths, StabilityStable) }

// applyStabilityTier is the shared decorator: it stamps each operation with the
// document's default tier (stable for the core contract, beta for the module-route
// document —), then lets a deprecation-table entry override the tier and add
// the deprecated flag + policy x- extensions. The minimum-window enforcement
// (stability_test.go) keys off the table entry's own tier, so a deprecated module
// route is held to the beta window and a core route to the stable window.
func applyStabilityTier(paths map[string]any, def StabilityTier) {
	deps := indexDeprecations(routeDeprecations)
	for path, item := range paths {
		ops, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for method, raw := range ops {
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			op["x-stability"] = string(def)
			d, ok := deps[strings.ToUpper(method)+" "+path]
			if !ok {
				continue
			}
			op["x-stability"] = string(d.Tier)
			op["deprecated"] = true
			op["x-deprecated-at"] = d.DeprecatedAt.UTC().Format(time.RFC3339)
			op["x-migration-guide"] = d.docsLink()
			if !d.SunsetAt.IsZero() {
				op["x-sunset-at"] = d.SunsetAt.UTC().Format(time.RFC3339)
			}
		}
	}
}
