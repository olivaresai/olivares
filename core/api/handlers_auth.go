// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Login-attempt outcomes for olivares_auth_login_attempts_total (the
// docs/17 §5 "failed/throttled-login counter", previously audit-only). The
// classification deliberately counts only CREDENTIAL outcomes: a store outage
// during login is a 5xx in olivares_http_requests_total, not an abuse signal.
const (
	loginOutcomeSuccess   = "success"
	loginOutcomeFailed    = "failed"
	loginOutcomeLockedOut = "locked_out"
)

// firstOrgDefaultName and firstOrgDefaultSlug name the organization first-boot
// setup creates when the operator supplies none. Deliberately neutral and
// self-evident: what the estate is called is the operator's decision, and putting
// an invented brand in someone else's install is worse than an obvious label.
const (
	firstOrgDefaultName = "Default Organization"
	firstOrgDefaultSlug = "default"
)

// setupRollbackTimeout bounds the compensating tenant drop below. It runs on a
// context detached from the request, so it needs a deadline of its own.
const setupRollbackTimeout = 15 * time.Second

// handleSetup runs first-boot setup: it creates the FIRST ORGANIZATION and the
// first superadmin that owns it. It requires the one-time setup token (verified
// in constant time), is atomic against concurrent setup, and consumes the token
// on success. It is exempt from the setup gate so it is reachable while no user
// exists.
//
// Both halves are required for a usable install. Every tenant-scoped route
// resolves its tenant from the caller's grants or an explicit X-Olivares-Tenant
// header, so a superadmin created with no organization and no membership has
// nothing to select: the console cannot send the header and every tenant-scoped
// endpoint answers 400 "tenant required" forever. That check is correct and stays
// — what was missing is the tenant it asks for.
//
// Ordering and atomicity. The tenant is provisioned FIRST and the superadmin
// second, because the two live in different units of work (the cross-tenant
// System path and the auth partition) and only this order is recoverable: the
// account+membership pair commits atomically in ONE transaction, and if it fails
// the tenant is rolled back by rollbackFirstOrg. The reverse order would close
// setup (a user exists ⇒ ErrSetupComplete) before its tenant existed, leaving an
// install that can neither be used nor re-set-up.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in setupInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if !s.setupTok.Verify(in.Token) {
		s.writeError(w, r, errForbidden)
		return
	}
	// Reject what is already known to be unusable BEFORE provisioning anything: a
	// request that cannot produce a superadmin must not leave a tenant behind.
	if len(in.Password) < auth.MinPasswordLen {
		s.writeError(w, r, auth.ErrWeakPassword)
		return
	}
	name, slug := firstOrgNaming(in.Organization)
	org, err := s.firstOrg(r.Context(), name, slug)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	u, _, err := s.authr.BootstrapSuperadminOwning(r.Context(), in.Email, in.Password, org.TenantID)
	if err != nil {
		s.rollbackFirstOrg(r.Context(), org.TenantID)
		s.writeError(w, r, err)
		return
	}
	// Single-use: the token is invalidated and setup is now closed.
	if cerr := s.setupTok.Consume(); cerr != nil {
		s.log.Error("api: failed to consume setup token", "err", cerr)
	}
	s.setupComplete.Store(true)
	writeJSON(w, http.StatusCreated, setupResponse{UserDTO: toUserDTO(u), Organization: toOrgDTO(org)})
}

// firstOrg returns the organization the first superadmin will own, provisioning
// it through provisionOrg — the same path POST /v1/system/orgs uses, which also
// seeds the tenant's "Default" workspace and its audit chain.
//
// It ADOPTS an existing organization carrying the same slug instead of failing on
// the unique slug index. While setup is still open no user exists, so such a row
// can only be the residue of an earlier attempt whose rollback did not run (see
// rollbackFirstOrg): an install must never become permanently un-set-uppable
// because of it. Adoption is safe — BootstrapSuperadminOwning re-checks "no user
// exists" transactionally, and rollbackFirstOrg refuses to drop a tenant that
// already has a member.
//
// The first tenant is provisioned pinned to the home region when this instance
// enforces residency (a region-scoped instance must hold only data it serves) and
// unpinned otherwise, which residency.Registry.Serves accepts everywhere.
func (s *Server) firstOrg(ctx context.Context, name, slug string) (model.Org, error) {
	existing, found, err := s.orgBySlug(ctx, slug)
	if err != nil {
		return model.Org{}, err
	}
	if found {
		return existing, nil
	}
	region := ""
	if s.residency.Enforces() {
		region = s.residency.Home().String()
	}
	return s.provisionOrg(ctx, name, slug, region)
}

// orgBySlug looks up a tenant org by its unique slug, skipping the reserved
// system tenant (which is not a business tenant anybody is granted a role in).
func (s *Server) orgBySlug(ctx context.Context, slug string) (model.Org, bool, error) {
	var (
		out   model.Org
		found bool
	)
	err := s.st.System(ctx, func(sys store.SystemScope) error {
		orgs, lerr := sys.ListOrgs(ctx)
		if lerr != nil {
			return lerr
		}
		for _, o := range orgs {
			if o.TenantID.IsZero() || o.TenantID.IsSystem() || o.Slug != slug {
				continue
			}
			out, found = o, true
			return nil
		}
		return nil
	})
	if err != nil {
		return model.Org{}, false, err
	}
	return out, found, nil
}

// rollbackFirstOrg compensates a setup that provisioned a tenant and then failed
// to create the superadmin that owns it: the tenant has no owner and setup is
// still OPEN, so it must not survive as an orphan.
//
// It refuses to drop a tenant that ALREADY HAS A MEMBER. Under concurrent setup
// the loser must never delete the winner's tenant, and the check is exact rather
// than racy: the winner's user and membership commit in the SAME transaction, so
// by the time the loser is told ErrSetupComplete that membership is committed and
// visible. It runs on a context detached from the request, because a client that
// hung up must not leave the orphan behind.
//
// LIMIT, stated rather than hidden: this is a compensating action, not one
// transaction — the auth partition and the cross-tenant provisioning path are
// separate units of work by design, so no single commit spans them. If the
// compensation itself fails (a store outage), an ownerless tenant survives; setup
// stays open, and the next attempt ADOPTS that tenant (see firstOrg) instead of
// colliding with its slug, so the install is still completable. What can never
// happen in any ordering is the state this whole handler exists to prevent: a
// closed setup whose superadmin owns nothing.
func (s *Server) rollbackFirstOrg(ctx context.Context, tenant model.TenantID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), setupRollbackTimeout)
	defer cancel()
	owned, err := s.authr.TenantHasMembership(ctx, tenant)
	if err != nil {
		// Deny-closed on the DESTRUCTIVE side: not knowing whether the tenant has an
		// owner is not permission to delete it.
		s.log.Error("api: setup rollback could not establish tenant ownership; leaving the tenant in place",
			"tenant", tenant.String(), "err", err)
		return
	}
	if owned {
		return // a concurrent setup won the bootstrap and owns this tenant
	}
	if derr := s.st.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, tenant)
	}); derr != nil {
		s.log.Error("api: setup failed and its tenant could not be rolled back; the next setup will adopt it",
			"tenant", tenant.String(), "err", derr)
	}
}

// firstOrgNaming derives the (name, slug) of the first organization from the
// OPTIONAL name in the setup body, falling back to the neutral default.
func firstOrgNaming(requested string) (name, slug string) {
	name = strings.TrimSpace(requested)
	if name == "" {
		return firstOrgDefaultName, firstOrgDefaultSlug
	}
	slug = orgSlugFrom(name)
	if slug == "" {
		// A name with nothing sluggable in it (e.g. non-Latin script) keeps its name
		// and takes the default handle — never an empty slug.
		slug = firstOrgDefaultSlug
	}
	return name, slug
}

// orgSlugFrom folds a human organization name into the short, URL-safe handle
// shape the rest of the product uses for slugs (slugPattern: a leading
// alphanumeric, then alphanumerics and hyphens, 63 chars max). It returns "" when
// nothing usable survives, which the caller replaces with the default.
func orgSlugFrom(name string) string {
	var b strings.Builder
	pendingHyphen := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			pendingHyphen = false
			continue
		}
		pendingHyphen = true
	}
	slug := b.String()
	if len(slug) > 63 {
		slug = strings.TrimRight(slug[:63], "-")
	}
	return slug
}

// handleLogin validates email/password and returns a session token.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in loginInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	token, sess, err := s.authr.Login(r.Context(), in.Email, in.Password, clientIP(r))
	switch {
	case err == nil:
		s.mLogin.Inc(loginOutcomeSuccess)
	case errors.Is(err, auth.ErrLockedOut):
		s.mLogin.Inc(loginOutcomeLockedOut)
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.mLogin.Inc(loginOutcomeFailed)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"session_id": sess.ID.String(),
		"expires_at": sess.ExpiresAt.String(),
	})
}

// handleRefresh rotates the calling session's credential and extends its expiry,
// returning a fresh token (the old one stops working). Like logout it applies only to
// session principals; an API token is reissued via /v1/tokens, not refreshed
// (deny-closed for non-renewable principals). Same response shape as login.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	if p.Kind != auth.KindUser {
		s.badRequest(w, r, "refresh applies to session principals; tokens are reissued via /v1/tokens")
		return
	}
	token, sess, err := s.authr.RefreshSession(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"session_id": sess.ID.String(),
		"expires_at": sess.ExpiresAt.String(),
	})
}

// handleLogout revokes the calling session. It does not apply to token principals.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	if p.Kind != auth.KindUser {
		s.badRequest(w, r, "logout applies to session principals; revoke tokens via /v1/tokens")
		return
	}
	if err := s.authr.RevokeSession(r.Context(), p, p.CredID); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleWhoami returns the calling principal's identity and grants.
//
// each grant carries the principal's EFFECTIVE PERMISSION SET in that tenant, so
// the console answers "may I?" by set membership instead of re-deriving the RBAC rule
// client-side. The set is per GRANT, not per principal, for two reasons that are both
// load-bearing: the role — hence the set — differs per tenant, and the console's
// can(permission, {tenant}) accepts a tenant other than the active one, a capability a
// single principal-wide set would drop in silence.
//
// A grant whose membership is workspace-CONFINED also reports the workspace, and
// its set already has the tenant-wide access-matrix recon reads removed — the part of
// confinement that holds regardless of what the action targets. The rest of confinement
// is resolved per request from the store and is NOT expressible here; see
// auth.EffectivePermissions for the full statement of what this set does and does not
// carry.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	grants := make([]map[string]any, 0)
	for _, t := range p.Tenants() {
		role, _ := p.RoleIn(t)
		ws, confined := p.ConfinedWorkspaceIn(t)
		g := map[string]any{
			"tenant":      t.String(),
			"role":        role,
			"permissions": s.grantedPermsFor(r.Context(), p, t, role, confined),
		}
		if confined {
			g["confined_workspace"] = ws.String()
		}
		grants = append(grants, g)
	}
	out := map[string]any{
		"kind":         string(p.Kind),
		"user_id":      idOrEmpty(p.UserID),
		"actor":        p.Actor(),
		"display_name": p.DisplayName,
		"superadmin":   p.Superadmin,
		"grants":       grants,
	}
	// the session's verified assurance (contract: aal is a NUMBER,
	// amr a string list). Emitted only for session principals — a token carries
	// no human assurance and the panel fails closed to AAL1 on absence.
	if p.Kind == auth.KindUser && p.AAL > 0 {
		out["aal"] = p.AAL
		if len(p.AMR) > 0 {
			out["amr"] = p.AMR
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// clientIP returns the peer IP for throttling. It uses the real transport peer
// (RemoteAddr), not a spoofable X-Forwarded-For header; honoring XFF behind a
// trusted reverse proxy is a deployment concern.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
