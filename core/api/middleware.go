// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// statusRecorder captures the response status for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the wrapped ResponseWriter so http.ResponseController can reach
// the base writer's optional capabilities (Flush, SetWriteDeadline, Hijack). A
// streaming module route — server-sent events for live operation (module II),
// later voice/realtime (module XVI) — needs to flush each event and clear the
// hardened WriteTimeout for the duration of the stream; without this Unwrap the
// access-log wrapper would hide those capabilities. It does not change the
// status capture for any non-streaming response.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// recoverer turns a handler panic into a 500 (and logs it) so one bad handler
// cannot crash the server. It composes with — does not replace — the eventbus /
// runtime panic isolation (S02), which guards background work.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("api: handler panic", "panic", rec, "path", r.URL.Path, "request_id", requestID(r.Context()))
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"internal error"}}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestIDMW assigns or propagates a request id, exposed as X-Request-ID.
func (s *Server) requestIDMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(withRequestID(r.Context(), id)))
	})
}

// secureHeaders sets conservative security headers on every response.
func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		// HSTS is safe because the server is TLS-by-default.
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// accessLog logs one line per request AFTER it completes. It never logs the
// request body or the Authorization header (so a token or setup token cannot leak
// into logs). It installs an actor holder that the authenticate middleware (which
// runs inside) fills, so the line is attributed to the real principal.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.clock.Now().Time()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		ctx, holder := withActorHolder(r.Context())
		s.mInflight.Inc()
		next.ServeHTTP(rec, r.WithContext(ctx))
		dur := time.Since(start)
		s.mInflight.Dec()
		s.recordRequest(r.Method, rec.status, dur)
		s.log.Info("api request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"dur_ms", dur.Milliseconds(), "actor", holder.actor,
			"request_id", requestID(r.Context()))
	})
}

// authenticate resolves a bearer credential into the request context. A present
// but invalid credential is rejected immediately (401); an absent credential
// leaves the request anonymous for routes that allow it (login, setup, health).
//
// when a metricsGate is configured, /metrics handles its OWN bearer auth
// (a static scrape token, not an API credential) — the middleware passes through
// so the endpoint's allowMetrics gate resolves it. Without a gate the original
// behavior is unchanged (the scraper presents no bearer and passes through as
// anonymous).
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if h == "" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/metrics" && s.metricsGate != nil {
			next.ServeHTTP(w, r)
			return
		}
		token, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			s.writeError(w, r, auth.ErrUnauthenticated)
			return
		}
		p, err := s.authr.Authenticate(r.Context(), strings.TrimSpace(token))
		if err != nil {
			s.writeError(w, r, auth.ErrUnauthenticated)
			return
		}
		if h := actorHolderFrom(r.Context()); h != nil {
			h.actor = p.Actor() // attribute the access-log line to the real principal
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// RootEnginePaths are the engine's root-level, non-/v1, unauthenticated endpoints:
// the health check, the OpenAPI document, the Prometheus scrape target and the
// liveness/readiness probes. They are the SINGLE SOURCE shared by the setup-gate
// exemption (below) and the SPA-vs-API router in the single-binary composition root
// (cmd/olivares's isAPIPath), so the two lists can never drift apart. That drift
// once shadowed /livez, /readyz and /metrics behind the SPA shell — they returned
// index.html 200 instead of the engine handlers, so the Helm readinessProbe never
// saw the store-down 503 and the LB kept routing to wedged pods (C1).
var RootEnginePaths = []string{"/healthz", "/openapi.json", "/openapi.beta.json", "/metrics", "/livez", "/readyz", "/pod-readyz", "/status", AuthZenConfigPath, OAuthAuthorizationServerMetadataPath}

// leaderGate is the HA leader-routing backstop (stage-2, design §B.1). In the
// leader-routing layout every healthy replica is Pod-Ready, so a standby IS
// reachable: the leader-selecting Service resolves traffic from a label the engine
// publishes, and a label is not transactional with the Postgres election lock. A
// brief stale label — or a direct dial to a pod IP — could otherwise land an
// application request on a standby, which would serve READS from a node that is
// not the active writer (the store's write fence stops writes, not reads).
//
// So every application route re-checks leadership at the edge and answers the same
// retryable 503 not_leader the write fence produces. Labels affect discovery;
// leadership authority stays with the Postgres lock. The operational endpoints
// (probes, metrics, status, discovery documents) are exempt — the kubelet and the
// scraper MUST reach a standby, and /readyz's own leader verdict is precisely the
// signal a failover test reads.
//
// It is opt-in (Options.LeaderRouteGate): only pods deployed in the leader-routing
// layout enable it, so a legacy HA deployment — where standbys are drained from
// the Service by /readyz and reads to them are deliberate — is unchanged.
//
// The predicate is IsLeader(), NOT Active(). The store-private bootstrap gate can
// remain open during PROMOTION, but public Active and IsLeader stay false until
// the callback establishes leadership. IsLeader() is the signal the label
// publisher advertises on, so the gate and the routing label can never disagree.
func (s *Server) leaderGate(next http.Handler) http.Handler {
	exempt := make(map[string]bool, len(RootEnginePaths))
	for _, p := range RootEnginePaths {
		exempt[p] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt[r.URL.Path] || s.st.Leader().IsLeader() {
			next.ServeHTTP(w, r)
			return
		}
		s.writeError(w, r, store.ErrNotLeader)
	})
}

// setupGate blocks every non-exempt route with 409 setup_required until the first
// superadmin exists. The check is transactional (HasAnyUser) until setup is first
// observed complete, then cached — so a crash mid-setup safely re-enters setup
// mode, while steady state costs no extra query.
func (s *Server) setupGate(next http.Handler) http.Handler {
	// The exempt set is the shared root endpoints PLUS the two unauthenticated /v1
	// leaves needed to bootstrap (server-info and first-run setup).
	exempt := map[string]bool{"/v1/server-info": true, "/v1/setup": true}
	for _, p := range RootEnginePaths {
		exempt[p] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt[r.URL.Path] || s.setupCompleteNow(r) {
			next.ServeHTTP(w, r)
			return
		}
		s.writeError(w, r, errSetupRequired)
	})
}

// setupCompleteNow reports whether setup is complete, caching the first true.
func (s *Server) setupCompleteNow(r *http.Request) bool {
	return s.isSetupComplete(r.Context())
}

// isSetupComplete reports whether the first superadmin exists, caching the first
// true so steady state costs no extra query while a crash mid-setup safely
// re-enters setup mode. It is shared by the REST setup gate and the gRPC path.
func (s *Server) isSetupComplete(ctx context.Context) bool {
	if s.setupComplete.Load() {
		return true
	}
	has, err := s.authr.HasAnyUser(ctx)
	if err != nil {
		s.log.Error("api: setup-gate check failed", "err", err)
		return false // fail closed: stay in setup mode on error
	}
	if has {
		s.setupComplete.Store(true)
	}
	return has
}

// authzTenant authenticates, resolves the single canonical tenant, and authorizes
// perm at COLLECTION level (no specific entity). On any failure it writes the response
// and returns ok=false. The returned tenant is the ONLY tenant a handler may use (never
// re-derive from the request).
func (s *Server) authzTenant(w http.ResponseWriter, r *http.Request, perm auth.Permission) (auth.Principal, model.TenantID, bool) {
	return s.authzTenantResource(w, r, perm, auth.ResourceFor(perm))
}

// authzTenantEntity authorizes perm for a SPECIFIC entity: it seeds Resource.ID
// so the scoped-grant engine resolves the entity's TRUE scope (its workspace,
// folder ancestors and agent-group membership) from the stored row — which the caller
// cannot forge — and may GRANT a workspace-scoped principal access the flat RBAC layer
// would deny, or a forbid may narrow it. When no scoped grants are active the engine
// abstains and this is exactly authzTenant. An empty id degrades safely to collection
// level. Same failure/return contract as authzTenant.
func (s *Server) authzTenantEntity(w http.ResponseWriter, r *http.Request, perm auth.Permission, id model.ID) (auth.Principal, model.TenantID, bool) {
	res := auth.ResourceFor(perm)
	res.ID = id.String()
	return s.authzTenantResource(w, r, perm, res)
}

// authzTenantEntityKind authorizes an entity action whose entity KIND differs from the
// permission's resource segment — e.g. an agent-group read gated by "agent:read" but whose
// entity is an "agent_group". The kind drives the scoped engine's workspace resolution
// (readScope, F3), so it MUST name the STORED entity, not the permission's resource;
// otherwise the engine cannot derive the group's workspace and a workspace-confined operator
// reads a cross-workspace group.
func (s *Server) authzTenantEntityKind(w http.ResponseWriter, r *http.Request, perm auth.Permission, kind string, id model.ID) (auth.Principal, model.TenantID, bool) {
	return s.authzTenantResource(w, r, perm, auth.ResourceAttrs{Kind: kind, ID: id.String()})
}

// authzTenantResource is the shared core of authzTenant/authzTenantEntity: it
// authenticates, resolves the single canonical tenant, and authorizes perm against the
// given resource attributes.
func (s *Server) authzTenantResource(w http.ResponseWriter, r *http.Request, perm auth.Permission, res auth.ResourceAttrs) (auth.Principal, model.TenantID, bool) {
	return s.authzTenantResourceWithDenial(w, r, perm, res, errForbidden)
}

// authzTenantResourceWithDenial keeps authentication, tenant resolution and the
// authorization decision in the one shared flow while allowing an exact point
// route to conceal only its final denial. Callers must pass a curated API error;
// authentication and tenant failures are always written before it is considered.
func (s *Server) authzTenantResourceWithDenial(
	w http.ResponseWriter,
	r *http.Request,
	perm auth.Permission,
	res auth.ResourceAttrs,
	denial error,
) (auth.Principal, model.TenantID, bool) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return auth.Principal{}, "", false
	}
	tenant, err := s.resolveTenant(r, p)
	if err != nil {
		s.writeError(w, r, err)
		return auth.Principal{}, "", false
	}
	if dec := s.authz.Authorize(r.Context(), auth.Request{Principal: p, Permission: perm, Tenant: tenant, Resource: res}); !dec.Allow {
		s.writeError(w, r, denial)
		return auth.Principal{}, "", false
	}
	return p, tenant, true
}

// requireAAL3 is the assurance gate for privileged CONFIGURE actions: SSO
// config, scoped-admin delegation, custom-role edits, user onboarding, workspace
// create/archive. It is ORTHOGONAL to RBAC — assurance never lives in the
// authorizer (algebra) — so a handler calls it as a SECOND, explicit gate
// AFTER the RBAC check (authn → RBAC → AAL3), mirroring the credential-lifecycle
// pattern in core/auth (webauthn.go). A principal below AAL3 gets 403
// step_up_required and the console routes it to the WebAuthn/PIV step-up. A token
// principal (AAL=0, never elevatable) can never pass it, so an AAL3-gated route is
// human-session-only by construction. The principal's AAL is the EFFECTIVE value
// the authenticate middleware already collapsed through the 15-min TTL, so this
// reads the live assurance with no extra store round-trip. For writes that change
// AUTHORITY (grants/roles) the service method re-checks inside its transaction
// (TOCTOU); this edge gate is sufficient for config CRUD.
func (s *Server) requireAAL3(w http.ResponseWriter, r *http.Request, p auth.Principal) bool {
	if p.AAL < auth.AAL3 {
		s.writeError(w, r, auth.ErrStepUpRequired)
		return false
	}
	return true
}

// authzSystem requires a superadmin (the system/cross-tenant role) for a route.
func (s *Server) authzSystem(w http.ResponseWriter, r *http.Request, perm auth.Permission) (auth.Principal, bool) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return auth.Principal{}, false
	}
	if dec := s.authz.Authorize(r.Context(), auth.Request{Principal: p, Permission: perm, Tenant: model.SystemTenantID, Resource: auth.ResourceFor(perm)}); !dec.Allow {
		s.writeError(w, r, errForbidden)
		return auth.Principal{}, false
	}
	return p, true
}

// resolveTenant determines the single canonical tenant for an HTTP request from
// the X-Olivares-Tenant header (delegating to the shared resolver).
func (s *Server) resolveTenant(r *http.Request, p auth.Principal) (model.TenantID, error) {
	return s.resolveTenantValue(p, strings.TrimSpace(r.Header.Get("X-Olivares-Tenant")))
}

// resolveTenantValue is the single tenant-resolution rule shared by REST (header)
// and gRPC (request field). It rejects any disagreement between a bound token and
// the supplied tenant, and never resolves the reserved system tenant.
func (s *Server) resolveTenantValue(p auth.Principal, raw string) (model.TenantID, error) {
	var hdr model.TenantID
	if raw != "" {
		t, err := model.ParseTenantID(raw)
		if err != nil || t.IsSystem() {
			return "", errBadRequest
		}
		hdr = t
	}
	// A bound (non-superadmin) token's tenant is authoritative; a header must match.
	if p.Kind == auth.KindToken && !p.Superadmin {
		if ts := p.Tenants(); len(ts) == 1 {
			if hdr != "" && hdr != ts[0] {
				return "", errForbidden
			}
			return ts[0], nil
		}
	}
	if hdr != "" {
		return hdr, nil
	}
	// No tenant given: a single membership defaults; otherwise it must be named.
	if !p.Superadmin {
		if ts := p.Tenants(); len(ts) == 1 {
			return ts[0], nil
		}
	}
	return "", errTenantRequired
}
