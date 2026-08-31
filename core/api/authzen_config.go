// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// exposure & extensibility controls for the AuthZEN/access-review surface.
// The surface is professional-grade configurable: an operator can disable it whole,
// disable just the (recon-sensitive) reverse-query searches or the bulk export, and
// confine it to an intra-cluster network — all on TOP of the per-call bearer +
// authz:read / authz:admin + AAL3 gates, never instead of them. The defaults (nil
// config) leave everything enabled with no network restriction, so existing
// embedders and tests are unchanged.

// AuthZenConfig configures the surface exposure. The zero value (or a nil
// *AuthZenConfig in Options) enables everything with no network restriction.
type AuthZenConfig struct {
	// Disabled turns the WHOLE /access/v1 surface + discovery off (routes answer 404,
	// as if the PDP were not built). Use it where no AuthZEN PDP is wanted at all.
	Disabled bool
	// SearchDisabled disables ONLY the reverse-query Search endpoints
	// (subject/resource/action) — the most recon-sensitive — while leaving the
	// evaluation + batch PDP-enforcement endpoints on. Discovery stops advertising them.
	SearchDisabled bool
	// ExportDisabled disables ONLY the sealed access-review export.
	ExportDisabled bool
	// AllowedCIDRs, when non-empty, confines the whole surface (including discovery) to
	// clients whose DIRECT PEER address (net/http RemoteAddr) is inside one of these
	// CIDRs — the in-app "intra-cluster only" control (e.g. the pod/service network). A
	// request from outside gets 403. Empty ⇒ no network restriction (gate on auth +
	// permission only).
	//
	// It checks the DIRECT peer ON PURPOSE and deliberately does NOT trust
	// X-Forwarded-For: an allow-list that honored a client-settable header would be
	// trivially spoofable (set XFF to a permitted IP). So behind a reverse proxy the
	// peer is the proxy — this control then only confirms "the request came through our
	// proxy"; to restrict the ORIGINAL client by IP behind a proxy, use the proxy's own
	// ACL (this CIDR list cannot do it safely).
	AllowedCIDRs []string
}

// authzenGate is the parsed, request-hot-path form of AuthZenConfig (CIDRs compiled
// once at boot). A nil gate means "fully enabled, no restriction".
type authzenGate struct {
	disabled       bool
	searchDisabled bool
	exportDisabled bool
	cidrs          []*net.IPNet
}

// buildAuthzenGate compiles an AuthZenConfig into a gate, failing at build time on a
// malformed CIDR (an embedder deserves the error up front, not silent open exposure).
func buildAuthzenGate(cfg *AuthZenConfig) (*authzenGate, error) {
	if cfg == nil {
		return nil, nil
	}
	g := &authzenGate{disabled: cfg.Disabled, searchDisabled: cfg.SearchDisabled, exportDisabled: cfg.ExportDisabled}
	for _, c := range cfg.AllowedCIDRs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("api: AuthZen.AllowedCIDRs: invalid CIDR %q: %w", c, err)
		}
		g.cidrs = append(g.cidrs, n)
	}
	return g, nil
}

// authzenSurfaceKind classifies a request for the exposure gate.
type authzenSurfaceKind int

const (
	azKindEval   authzenSurfaceKind = iota // evaluation + batch (PDP enforcement)
	azKindSearch                           // the reverse-query search endpoints
	azKindExport                           // the sealed access-review export
	azKindConfig                           // the discovery document
)

// allowSurface enforces the exposure config for a request, writing the response and
// returning false when blocked. It runs BEFORE the auth/permission gate so a disabled
// or out-of-network surface reveals nothing (404 disabled, 403 out-of-network). A nil
// gate (default) always allows.
func (s *Server) allowSurface(w http.ResponseWriter, r *http.Request, kind authzenSurfaceKind) bool {
	g := s.authzen
	if g == nil {
		return true
	}
	if g.disabled ||
		(kind == azKindSearch && g.searchDisabled) ||
		(kind == azKindExport && g.exportDisabled) {
		s.writeError(w, r, store.ErrNotFound) // 404: behave as if not mounted
		return false
	}
	if len(g.cidrs) > 0 && !g.peerAllowed(r.RemoteAddr) {
		s.writeError(w, r, errForbidden) // 403: outside the permitted network
		return false
	}
	return true
}

// peerAllowed reports whether the request's direct peer IP is inside an allowed CIDR.
func (g *authzenGate) peerAllowed(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr // already host-only
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // unparseable peer ⇒ deny-closed
	}
	for _, n := range g.cidrs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (g *authzenGate) searchEnabled() bool { return g == nil || !g.searchDisabled }
func (g *authzenGate) exportEnabled() bool { return g == nil || !g.exportDisabled }

// PrincipalEnumerator sources the candidate principal population for a tenant's
// reverse queries / access-review export. The default implementation is the
// Authenticator (TenantPrincipals), which enumerates LIVE from the store on every
// call — the only source GUARANTEED not to diverge from enforcement (honesty
// rule #1). It is a seam, not an invitation to cache decisions: an enterprise
// deployment MAY supply a materialized/cached population source for latency on very
// large tenants, but it MUST stay correct (invalidate on membership/token/group
// change) — a stale population makes an access-review lie, the exact failure this
// feature exists to prevent. Decisions ALWAYS go live through Authorizer.Authorize;
// only the candidate set is sourced here.
type PrincipalEnumerator interface {
	TenantPrincipals(ctx context.Context, tenant model.TenantID, assurance int) ([]auth.Principal, error)
}
