// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// rateLimit is the inbound rate-limit middleware (OPS-5): a per-tenant,
// per-endpoint-class token bucket with per-tier quotas that stops one tenant's
// burst from degrading another's p99 in a shared multi-tenant deployment. It runs
// AFTER authenticate (it needs the principal) and BEFORE setupGate (so it also
// shields the setup-gate store query), and INSIDE accessLog (so a 429 is logged
// and counted in olivares_http_requests_total{code="429"} like any other response).
//
// Scope is the AUTHENTICATED surface — the OPS-5 noisy-neighbor / per-tenant-quota
// threat. Anonymous requests (login, setup, server-info, SSO callbacks) are NOT
// metered here: the unauthenticated edge is guarded by the per-account/per-IP login
// lockout (core/auth/throttle.go) and the deployment's ingress/WAF. An IP-keyed
// anon bucket would collapse to a SINGLE bucket behind a reverse proxy (RemoteAddr
// is the proxy's), turning the fairness control into a self-inflicted global DoS of
// login/setup — strictly worse than delegating that edge to the controls built for
// it. The operational probes (RootEnginePaths) are exempt: a k8s probe or a
// Prometheus scrape must never be throttled.
//
// It never wraps the ResponseWriter (so the streaming SSE Unwrap/Flush chain is
// untouched): it sets the advisory RateLimit-* headers on w before next runs and,
// on a denial, the Retry-After header before writing the standard error envelope
// through writeError (so the 429 body can never drift from the one error envelope).
func (s *Server) rateLimit(next http.Handler) http.Handler {
	// Exempt the same root operational endpoints the setup gate exempts, from the one
	// shared source so the two lists cannot drift.
	exempt := make(map[string]bool, len(RootEnginePaths))
	for _, p := range RootEnginePaths {
		exempt[p] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.rl == nil || exempt[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		p, ok := principalFrom(r.Context())
		if !ok {
			next.ServeHTTP(w, r) // anonymous: out of scope (see doc above)
			return
		}
		key, tier := s.rlIdentity(p, r)
		dec := s.rl.Allow(r.Context(), key, tier, rlClass(r.Method))
		setRateLimitHeaders(w, dec)
		if !dec.OK {
			w.Header().Set("Retry-After", strconv.Itoa(dec.RetryAfter))
			s.writeError(w, r, errRateLimited)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rlIdentity derives the metering identity and tier for a REST request. This is
// METERING only, never authorization: it must never error and must always yield a
// bounded bucket. It selects a CANDIDATE tenant (the sole membership, or the one
// named in X-Olivares-Tenant); rlIdentityFor then gates that candidate by membership,
// so a tenant the principal does not belong to never lends its bucket.
func (s *Server) rlIdentity(p auth.Principal, r *http.Request) (key, tier string) {
	if p.Superadmin {
		return s.rlIdentityFor(p, "")
	}
	// A bound token (or single-membership user) has exactly one tenant — authoritative.
	if ts := p.Tenants(); len(ts) == 1 {
		return s.rlIdentityFor(p, ts[0])
	}
	// A multi-membership user meters against the tenant it names (membership enforced
	// in rlIdentityFor); a malformed/system header falls through to the cred bucket.
	if raw := strings.TrimSpace(r.Header.Get("X-Olivares-Tenant")); raw != "" {
		if t, err := model.ParseTenantID(raw); err == nil && !t.IsSystem() {
			return s.rlIdentityFor(p, t)
		}
	}
	return s.rlIdentityFor(p, "")
}

// rlIdentityFor maps a principal + a CANDIDATE tenant to a metering key and tier.
// Shared by the REST middleware and the gRPC interceptor so both surfaces meter a
// tenant identically — and, crucially, BOTH enforce membership here: the gRPC tenant
// comes from resolveTenantValue, which does NOT itself check membership (authz does,
// later), so without this guard a multi-membership caller could drain a victim
// tenant's bucket by naming it on a request that ends in 403. A superadmin is keyed
// per-credential at the system tier (trusted operator, high but bounded; one leaked
// token cannot starve another — revocation on the same credential id is the primary
// control). A tenant the principal is a MEMBER of keys on the tenant at its tier;
// anything else (zero, or a non-member tenant) falls to a per-credential bucket at
// the default tier (bounded; the handler's authz then rejects the request).
func (s *Server) rlIdentityFor(p auth.Principal, tenant model.TenantID) (key, tier string) {
	if p.Superadmin {
		return "su:" + p.CredID.String(), ratelimit.TierSystem
	}
	if !tenant.IsZero() && p.IsMember(tenant) {
		return "tn:" + tenant.String(), s.rl.TierFor(tenant)
	}
	return "cr:" + p.CredID.String(), s.rl.DefaultTier()
}

// rlClass classifies a request by HTTP method: safe methods are reads, everything
// else is a (costlier, audit-chain-touching) write.
func rlClass(method string) ratelimit.EndpointClass {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return ratelimit.ClassRead
	default:
		return ratelimit.ClassWrite
	}
}

// rlClassForPerm classifies a gRPC RPC by its permission verb (":write" => write).
func rlClassForPerm(perm auth.Permission) ratelimit.EndpointClass {
	if strings.HasSuffix(string(perm), ":write") {
		return ratelimit.ClassWrite
	}
	return ratelimit.ClassRead
}

// setRateLimitHeaders advertises the binding bucket's state. The names follow the
// IETF httpapi RateLimit-headers draft (RateLimit-Limit/Remaining/Reset; Reset is
// delta-seconds) — an IETF draft, NOT yet an RFC, so they are advisory. Retry-After
// (RFC 9110 §10.2.3, used with 429 per RFC 6585) is the normative, authoritative
// machine-readable signal and is set separately on a denial. Neither header carries
// a tenant id, tier name, or bucket key (minimal data, docs/SECURITY-HARDENING.md).
func setRateLimitHeaders(w http.ResponseWriter, dec ratelimit.Decision) {
	h := w.Header()
	h.Set("RateLimit-Limit", strconv.Itoa(dec.Limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(dec.Remaining))
	h.Set("RateLimit-Reset", strconv.Itoa(dec.Reset))
}
