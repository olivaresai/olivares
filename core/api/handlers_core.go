// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
)

// handleHealth is the unauthenticated liveness probe. It exposes nothing beyond
// liveness.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleServerInfo reports non-sensitive server facts, including whether setup is
// still required and the informational license status (which gates nothing).
func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	lic := s.licenseStatus()
	// The license sub-object is the public "licensed-to" badge. status + licensee are
	// always present; the attested plan / support-tier labels are added ONLY when a
	// license verified (omitted for community/none), and gate nothing (LICENSING.md).
	license := map[string]string{"status": lic.Status, "licensee": lic.Licensee}
	if lic.Plan != "" {
		license["plan"] = lic.Plan
	}
	if lic.SupportTier != "" {
		license["support_tier"] = lic.SupportTier
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        s.version,
		"engine":         string(s.st.Engine()),
		"setup_required": !s.setupCompleteNow(r),
		"license":        license,
		"protocol_currency": map[string]any{
			"mcp_revision":                 "2026-07-28",
			"mcp_revision_status":          "current",
			"a2a_version":                  "1.0",
			"a2a_security_scheme_enforced": true,
			"agents_md_enforce_available":  true,
			"aaif_standards":               []string{"MCP", "A2A", "AGENTS.md"},
		},
	})
}

// --- Agents (representative tenant-scoped CRUD) ------------------------------

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "agent:read")
	if !ok {
		return
	}
	confinedWS, _ := p.ConfinedWorkspaceIn(tenant)
	var out listResponse[AgentDTO]
	err := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		agents, page, err := sc.Agents().List(r.Context(), parseFilteredListQuery(r, confinedWS))
		if err != nil {
			return err
		}
		out.Items = make([]AgentDTO, 0, len(agents))
		for _, a := range agents {
			out.Items = append(out.Items, toAgentDTO(a))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	id := model.ID(chi.URLParam(r, "id"))
	_, tenant, ok := s.authzTenantEntity(w, r, "agent:read", id)
	if !ok {
		return
	}
	var dto AgentDTO
	err := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = toAgentDTO(a)
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "agent:write")
	if !ok {
		return
	}
	var in AgentInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	var dto AgentDTO
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		var a model.Agent
		in.apply(&a)
		created, err := sc.Agents().Create(r.Context(), a)
		if err != nil {
			return err
		}
		dto = toAgentDTO(created)
		return appendAudit(r.Context(), sc, p, "agent.create", "core.agent", created.ID)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := model.ID(chi.URLParam(r, "id"))
	p, tenant, ok := s.authzTenantEntity(w, r, "agent:write", id)
	if !ok {
		return
	}
	var in AgentInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	var dto AgentDTO
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Get(r.Context(), id)
		if err != nil {
			return err
		}
		in.apply(&a)
		updated, err := sc.Agents().Update(r.Context(), a)
		if err != nil {
			return err
		}
		dto = toAgentDTO(updated)
		return appendAudit(r.Context(), sc, p, "agent.update", "core.agent", id)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := model.ID(chi.URLParam(r, "id"))
	p, tenant, ok := s.authzTenantEntity(w, r, "agent:write", id)
	if !ok {
		return
	}
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		if err := sc.Agents().Delete(r.Context(), id); err != nil {
			return err
		}
		return appendAudit(r.Context(), sc, p, "agent.delete", "core.agent", id)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Access graph (sensitive reads: self-audited) ----------------------------

func (s *Server) handleListAccessEdges(w http.ResponseWriter, r *http.Request) {
	// F2: the tenant-wide access graph is denied to a workspace-confined operator by the
	// scoped-authz engine (accessgraph:read is an access-graph recon perm with no workspace to
	// filter on), so the check below never admits a confined principal — the same guard covers
	// the access-map module's /graph|/neighbors|/attack-paths routes and the authz reverse query.
	p, tenant, ok := s.authzTenant(w, r, "accessgraph:read")
	if !ok {
		return
	}
	var out listResponse[AccessEdgeDTO]
	// A sensitive read runs in a committed Mutate so its self-audit persists
	// (an Append inside a View would be rolled back). The self-audit is coarse:
	// one event per call, not per row.
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		edges, page, err := sc.AccessEdges().List(r.Context(), parseListQuery(r))
		if err != nil {
			return err
		}
		out.Items = make([]AccessEdgeDTO, 0, len(edges))
		for _, e := range edges {
			out.Items = append(out.Items, toAccessEdgeDTO(e))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return appendAudit(r.Context(), sc, p, "accessgraph.read", "core.access_edge", "")
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// NOTE: the raw HTTP route GET /v1/access-edges/drift and its handler were removed in
// (C2 /). It exposed UNRECONCILED store drift, which double-counts
// cross-origin access (an agent's observed access against the identity it assumes
// shows up as both a false unexpected access and a false unused grant) — shipping
// false positives to the Terraform provider and the compliance evidence engine. The
// only correct, reconciled drift is module III's GET /v1/m/accessmap/drift (it owns
// the reconciliation logic decision A). The store-level AccessEdges().Drift stays
// (module III consumes it internally via ReconciledDrift; security uses it as
// explicitly approximate, non-headline context) — only the raw HTTP surface is gone.

// --- IAM: users (superadmin), tokens, memberships ----------------------------

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "user:read"); !ok {
		return
	}
	out := listResponse[UserDTO]{Items: []UserDTO{}}
	err := s.st.AuthView(r.Context(), func(as store.AuthScope) error {
		users, page, err := as.Users().List(r.Context(), parseListQuery(r))
		if err != nil {
			return err
		}
		for _, u := range users {
			out.Items = append(out.Items, toUserDTO(u))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "user:write")
	if !ok {
		return
	}
	var in createUserInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	u, err := s.authr.CreateUser(r.Context(), p, auth.NewUser{
		Email: in.Email, DisplayName: in.DisplayName, Password: in.Password, Superadmin: in.Superadmin,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUserDTO(u))
}

// handleListSuperadmins lists every superadmin account with its active/inactive
// status — the read side of the superadmin lifecycle surface. Superadmin-
// scoped, read-only (no AAL3: it returns no secret, only the same non-secret shape
// as GET /v1/users).
func (s *Server) handleListSuperadmins(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "user:read"); !ok {
		return
	}
	admins, err := s.authr.ListSuperadmins(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := listResponse[UserDTO]{Items: []UserDTO{}}
	for _, u := range admins {
		out.Items = append(out.Items, toUserDTO(u))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSetSuperadminActive is the write side of the superadmin lifecycle:
// enable or disable an INTERNAL superadmin account (active selects which). It is
// superadmin-scoped AND AAL3-gated (a privileged account-lifecycle action, like
// onboarding/SSO config), and deny-closed against total lockout in the service
// layer (auth.ErrLastSuperadmin → 409). Disabling is non-destructive and reversible.
func (s *Server) handleSetSuperadminActive(w http.ResponseWriter, r *http.Request, active bool) {
	p, ok := s.authzSystem(w, r, "user:write")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		s.badRequest(w, r, "invalid user id")
		return
	}
	u, err := s.authr.SetSuperadminActive(r.Context(), p, id, active)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(u))
}

func (s *Server) handleDisableSuperadmin(w http.ResponseWriter, r *http.Request) {
	s.handleSetSuperadminActive(w, r, false)
}

func (s *Server) handleEnableSuperadmin(w http.ResponseWriter, r *http.Request) {
	s.handleSetSuperadminActive(w, r, true)
}

func (s *Server) handleIssueToken(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	var in issueTokenInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	spec := auth.TokenSpec{Name: in.Name, Role: in.Role, Superadmin: in.Superadmin}
	if in.Superadmin {
		// Only a superadmin may mint a cross-tenant token.
		if !p.Superadmin {
			s.writeError(w, r, errForbidden)
			return
		}
	} else {
		bt, err := model.ParseTenantID(in.Tenant)
		if err != nil || bt.IsZero() || bt.IsSystem() {
			s.badRequest(w, r, "valid tenant required for a bound token")
			return
		}
		// The caller must hold token:write in the bound tenant.
		if !s.allow(r.Context(), p, "token:write", bt) {
			s.writeError(w, r, errForbidden)
			return
		}
		spec.BoundTenant = bt
	}
	token, stored, err := s.authr.IssueToken(r.Context(), p, spec)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": token, "id": stored.ID.String(), "name": stored.Name,
	})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	tok, err := s.authr.GetToken(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// A superadmin may revoke any token; otherwise the caller must be an admin of
	// the token's bound tenant. A token outside the caller's authority is reported
	// as not-found (404), NOT forbidden (403), so this endpoint cannot be used as a
	// cross-tenant existence oracle (mirrors not-found==other-tenant rule).
	if !p.Superadmin {
		if tok.BoundTenantID.IsZero() || !s.allow(r.Context(), p, "token:write", tok.BoundTenantID) {
			s.writeError(w, r, store.ErrNotFound)
			return
		}
	}
	if err := s.authr.RevokeToken(r.Context(), p, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListTokens returns tokens visible to the caller: a superadmin sees all
// active tokens; a tenant admin sees tokens bound to their tenant. Revoked
// tokens are excluded by default (?include_revoked=true to include them).
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	q := parseListQuery(r)
	if r.URL.Query().Get("include_revoked") != "true" {
		q.Filters = append(q.Filters, model.Filter{Column: "revoked", Op: model.OpEq, Value: false})
	}
	if !p.Superadmin {
		tenant, err := s.resolveTenant(r, p)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if !s.allow(r.Context(), p, "token:read", tenant) {
			s.writeError(w, r, errForbidden)
			return
		}
		q.Filters = append(q.Filters, model.Filter{Column: "bound_tenant_id", Op: model.OpEq, Value: string(tenant)})
	}
	out := listResponse[TokenDTO]{Items: []TokenDTO{}}
	err := s.st.AuthView(r.Context(), func(as store.AuthScope) error {
		tokens, page, err := as.Tokens().List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, t := range tokens {
			out.Items = append(out.Items, toTokenDTO(t))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRotateToken atomically creates a new token with the same spec and
// revokes the old one. The new token value is returned (shown once).
func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	old, err := s.authr.GetToken(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !p.Superadmin {
		if old.BoundTenantID.IsZero() || !s.allow(r.Context(), p, "token:write", old.BoundTenantID) {
			s.writeError(w, r, store.ErrNotFound)
			return
		}
	}
	if old.Revoked {
		s.badRequest(w, r, "cannot rotate a revoked token")
		return
	}
	spec := auth.TokenSpec{
		Name: old.Name, BoundTenant: old.BoundTenantID,
		Role: old.Role, Superadmin: old.IsSuperadmin, ExpiresAt: old.ExpiresAt,
	}
	newToken, newStored, err := s.authr.IssueToken(r.Context(), p, spec)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authr.RevokeToken(r.Context(), p, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": newToken, "id": newStored.ID.String(), "name": newStored.Name,
		"revoked_id": id.String(),
	})
}

func (s *Server) handleGrantMembership(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	var in grantMembershipInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	tenant, err := model.ParseTenantID(in.Tenant)
	if err != nil || tenant.IsZero() || tenant.IsSystem() {
		s.badRequest(w, r, "valid tenant required")
		return
	}
	if !s.allow(r.Context(), p, "membership:write", tenant) {
		s.writeError(w, r, errForbidden)
		return
	}
	// an OPTIONAL workspace confinement. Validate it names a real workspace in the
	// GRANTED tenant (the membership lives in the system tenant with no cross-tenant FK, so
	// the check is here, deny-closed against a typo that would silently confine to nothing).
	workspaceID := strings.TrimSpace(in.WorkspaceID)
	if workspaceID != "" {
		bad := false
		if verr := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
			if _, gerr := sc.Workspaces().Get(r.Context(), model.ID(workspaceID)); gerr != nil {
				if errors.Is(gerr, store.ErrNotFound) {
					bad = true
					return nil
				}
				return gerr
			}
			return nil
		}); verr != nil {
			s.writeError(w, r, verr)
			return
		}
		if bad {
			s.badRequest(w, r, "workspace_id names no workspace in the granted tenant")
			return
		}
	}
	m, err := s.authr.GrantMembership(r.Context(), p, model.ID(in.UserID), tenant, in.Role, model.ID(workspaceID))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	body := map[string]any{
		"id": m.ID.String(), "user_id": m.UserID.String(), "tenant": m.TargetTenantID.String(), "role": m.Role,
	}
	if !m.WorkspaceID.IsZero() {
		body["workspace_id"] = m.WorkspaceID.String()
	}
	writeJSON(w, http.StatusCreated, body)
}

// --- System: tenant provisioning (superadmin) --------------------------------

// handleResidencyRegistry returns this instance's configured residency registry.
// The registry is already sorted by residency.Registry.Known. A nil registry is
// the honest single-region/default posture, represented by an empty (not null)
// region list.
func (s *Server) handleResidencyRegistry(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	out := residencyRegistryDTO{Regions: []string{}}
	if s.residency != nil {
		out.HomeRegion = s.residency.Home().String()
		out.Enforces = s.residency.Enforces()
		for _, region := range s.residency.Known() {
			out.Regions = append(out.Regions, region.String())
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	var in createOrgInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	// normalize and validate the residency pin against the registry BEFORE
	// provisioning. Empty = unpinned (allowed). Deny-closed: an unknown region, a
	// pin on a non-region-scoped instance, or a pin to a region this instance does
	// not serve is a 400 — the tenant is never created half-pinned.
	region := string(residency.Normalize(in.DataRegion))
	if err := s.residency.ValidatePin(region); err != nil {
		s.badRequest(w, r, err.Error())
		return
	}
	org, err := s.provisionOrg(r.Context(), in.Name, in.Slug, region)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toOrgDTO(org))
}

// provisionOrg is the ONE tenant-provisioning path: POST /v1/system/orgs and
// first-boot setup both land here, so the first organization of an install is
// built exactly like every later one. SystemScope.CreateOrg allocates the tenant
// id, inserts the org row, seeds the tenant's "Default" workspace and starts its
// audit chain with org.create — all in a single transaction. The caller has
// already normalized and validated region against the residency registry (the pin
// is request input; an empty pin is unpinned and every instance serves it).
func (s *Server) provisionOrg(ctx context.Context, name, slug, region string) (model.Org, error) {
	var out model.Org
	err := s.st.System(ctx, func(sys store.SystemScope) error {
		o, err := sys.CreateOrg(ctx, model.Org{Name: name, Slug: slug, Status: model.StatusActive, DataRegion: region})
		if err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		return model.Org{}, err
	}
	return out, nil
}

// handleSetOrgRegion sets or clears a tenant's data-residency pin. Superadmin
// only. The requested region is normalized and validated against the residency
// registry (deny-closed: known region, and the home region on a region-scoped
// instance), then persisted via the System path with a version-checked update and an
// audit event. An empty region clears the pin (the tenant becomes unpinned).
func (s *Server) handleSetOrgRegion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	tenant, err := model.ParseTenantID(chi.URLParam(r, "tenant"))
	if err != nil || tenant.IsZero() || tenant.IsSystem() {
		s.badRequest(w, r, "valid tenant required")
		return
	}
	var in setOrgRegionInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	region := string(residency.Normalize(in.DataRegion))
	if err := s.residency.ValidatePin(region); err != nil {
		s.badRequest(w, r, err.Error())
		return
	}
	var dto OrgDTO
	err = s.st.System(r.Context(), func(sys store.SystemScope) error {
		o, oerr := sys.SetOrgRegion(r.Context(), tenant, region)
		if oerr != nil {
			return oerr
		}
		dto = toOrgDTO(o)
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleDropOrg performs the hard, unrecoverable tenant delete used after the
// cloud control plane's 30-day grace period safety net has expired. It verifies
// the tenant org exists before invoking DropTenant because DropTenant itself is a
// destructive purge primitive that treats a missing tenant as a zero-row delete.
func (s *Server) handleDropOrg(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	tenant, err := model.ParseTenantID(chi.URLParam(r, "tenant"))
	if err != nil || tenant.IsZero() || tenant.IsSystem() {
		s.badRequest(w, r, "valid tenant required")
		return
	}
	err = s.st.System(r.Context(), func(sys store.SystemScope) error {
		if _, err := sys.GetOrg(r.Context(), tenant); err != nil {
			return err
		}
		return sys.DropTenant(r.Context(), tenant)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetOrgStatus withdraws or restores a tenant's SERVICE without
// touching its data — the intermediate door between "served" and "deleted" that
// the cloud grace period needs. Superadmin (system:admin), and deliberately NOT
// AAL3.
//
// AAL3 was the first instinct, by analogy with handleSetOrgRegion. It was wrong
// twice over. It would have made the NON-destructive operation harder to reach
// than the DESTRUCTIVE one — handleDropOrg, the unrecoverable purge, requires
// system:admin alone — which is exactly backwards: the safe door must never be
// the locked one, or operators route around it to the dangerous one. And an
// AAL3-gated route is human-session-only by construction (a token principal is
// AAL 0 and can never elevate), so the caller this exists FOR — the cloud control
// plane, which authenticates with an API key — could not have called it at all.
//
// It changes ONE column. It revokes no credential, drops no session and deletes
// nothing, so restoring service is lossless — which is the point: the normal case
// is that the customer pays and comes back. Enforcement lives in the store guard
// (core/suspension), so this handler only records the decision; it does not have
// to destroy anything to make the decision bite.
func (s *Server) handleSetOrgStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	tenant, err := model.ParseTenantID(chi.URLParam(r, "tenant"))
	if err != nil || tenant.IsZero() || tenant.IsSystem() {
		s.badRequest(w, r, "valid tenant required")
		return
	}
	var in setOrgStatusInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	status := model.LifecycleStatus(strings.TrimSpace(in.Status))
	if status != model.StatusActive && status != model.StatusSuspended {
		s.badRequest(w, r, `status must be "active" or "suspended"`)
		return
	}
	var dto OrgDTO
	err = s.st.System(r.Context(), func(sys store.SystemScope) error {
		o, oerr := sys.SetOrgStatus(r.Context(), tenant, status)
		if oerr != nil {
			return oerr
		}
		dto = toOrgDTO(o)
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	out := listResponse[OrgDTO]{Items: []OrgDTO{}}
	err := s.st.System(r.Context(), func(sys store.SystemScope) error {
		orgs, err := sys.ListOrgs(r.Context())
		if err != nil {
			return err
		}
		for _, o := range orgs {
			if o.TenantID.IsSystem() {
				continue // the reserved system tenant is not a customer org
			}
			out.Items = append(out.Items, toOrgDTO(o))
		}
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- helpers -----------------------------------------------------------------

// allow is the boolean authorization check for a specific tenant.
func (s *Server) allow(ctx context.Context, p auth.Principal, perm auth.Permission, tenant model.TenantID) bool {
	return s.authz.Allowed(ctx, p, perm, tenant)
}

// appendAudit records a semantic audit event attributed to the principal, in the
// caller's transaction.
func appendAudit(ctx context.Context, sc store.Scope, p auth.Principal, action string, targetKind model.Kind, target model.ID) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor: p.Actor(), ActorKind: p.ActorKind(),
		Action: action, TargetKind: targetKind, TargetID: target,
	})
	return err
}

// parseListQuery builds a List query from ?limit and ?cursor.
func parseListQuery(r *http.Request) model.Query {
	q := model.Query{}
	if c := r.URL.Query().Get("cursor"); c != "" {
		q.Cursor = c
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			q.Limit = n
		}
	}
	return q
}

// parseFilteredListQuery extends parseListQuery with workspace_id filtering for
// entities that carry the scoping column (agents, sessions, resources, agent-groups).
func parseFilteredListQuery(r *http.Request, confinedWS model.ID) model.Query {
	q := parseListQuery(r)
	// a workspace-confined caller sees ONLY rows in its own workspace — the forced
	// filter OVERRIDES any caller-supplied ?workspace_id, so a confined operator can never
	// enumerate other workspaces' entities (the reconnaissance leak the roster filter also
	// closes). A tenant-wide caller (zero) keeps the optional caller-supplied filter.
	if !confinedWS.IsZero() {
		q.Filters = append(q.Filters, model.Filter{Column: "workspace_id", Op: model.OpEq, Value: confinedWS.String()})
		return q
	}
	if ws := r.URL.Query().Get("workspace_id"); ws != "" {
		q.Filters = append(q.Filters, model.Filter{
			Column: "workspace_id", Op: model.OpEq, Value: ws,
		})
	}
	return q
}
