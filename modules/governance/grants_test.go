// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// --- seeding helpers (scope entities) ---------------------------------------

func (h *harness) createWorkspace(tenant model.TenantID, slug string) model.ID {
	h.t.Helper()
	var id model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive})
		id = ws.ID
		return err
	}); err != nil {
		h.t.Fatalf("create workspace %s: %v", slug, err)
	}
	return id
}

func (h *harness) createAgentIn(tenant model.TenantID, name string, ws model.ID) model.Agent {
	h.t.Helper()
	var out model.Agent
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(context.Background(), model.Agent{
			Name: name, Kind: "claude-code", Status: model.StatusActive, WorkspaceID: ws,
		})
		out = a
		return err
	}); err != nil {
		h.t.Fatalf("create agent %s: %v", name, err)
	}
	return out
}

// addAgentToGroup creates an agent-group (slug, optionally workspace-scoped) and binds
// the agent to it, returning the group.
func (h *harness) addAgentToGroup(tenant model.TenantID, agentID model.ID, groupSlug string, ws model.ID) model.AgentGroup {
	h.t.Helper()
	var g model.AgentGroup
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		grp, err := sc.AgentGroups().Create(context.Background(), model.AgentGroup{
			Name: groupSlug, Slug: groupSlug, Status: model.StatusActive, WorkspaceID: ws,
		})
		if err != nil {
			return err
		}
		g = grp
		_, err = sc.AgentGroupMembers().Create(context.Background(), model.AgentGroupMember{GroupID: grp.ID, AgentID: agentID})
		return err
	}); err != nil {
		h.t.Fatalf("add agent to group %s: %v", groupSlug, err)
	}
	return g
}

// publishGrant activates a tenant's authored Cedar grant policy through the real
// authoring surface (admin must hold governance:policy:admin in the tenant).
func (h *harness) publishGrant(admin string, tenant model.TenantID, src string) {
	h.t.Helper()
	r := h.do("POST", "/v1/m/governance/pdp/publish", admin, map[string]any{"engine": "cedar", "source": src}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("publish grant = %d %s", r.code, r.raw)
	}
}

func (h *harness) scoped(tenant model.TenantID, p auth.Principal, perm auth.Permission, resourceID model.ID) auth.ScopedDecision {
	h.t.Helper()
	res := auth.ResourceFor(perm)
	res.ID = resourceID.String()
	sd, err := h.gov.ScopedGrants().Scoped(context.Background(), auth.Request{
		Principal: p, Permission: perm, Tenant: tenant, Resource: res,
	})
	if err != nil {
		h.t.Fatalf("Scoped(%s): %v", perm, err)
	}
	return sd
}

// --- e2e: positive scoped grant on the real REST path ----------------------------

// A viewer cannot agent:write by role, but a workspace-scoped grant authorizes it for
// agents IN that workspace — and only there. Proven on the real DELETE /agents/{id}
// path (the chokepoint that funnels every REST + gRPC request).
func TestScopedGrantWorkspaceEnforcementE2E(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	payments := h.createWorkspace(tenant, "payments")
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	inDefault := h.createAgentIn(tenant, "default-bot", model.ID("")) // zero ⇒ default workspace
	_, viewer := h.roleUser(admin, tenant, "viewer@acme.io", auth.RoleViewer)

	// Baseline: with no grant, a viewer cannot delete (agent:write) any agent.
	if r := h.do("DELETE", "/v1/agents/"+inPayments.ID.String(), viewer, nil, hdr); r.code != http.StatusForbidden {
		t.Fatalf("baseline viewer delete must be 403, got %d %s", r.code, r.raw)
	}

	// Grant agent:write to viewers, scoped to the payments workspace.
	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`)

	// Now the viewer CAN delete an agent in payments...
	if r := h.do("DELETE", "/v1/agents/"+inPayments.ID.String(), viewer, nil, hdr); r.code != http.StatusNoContent {
		t.Errorf("scoped grant: viewer delete of in-payments agent must be 204, got %d %s", r.code, r.raw)
	}
	// ...but NOT an agent in the default workspace (the grant does not reach it).
	if r := h.do("DELETE", "/v1/agents/"+inDefault.ID.String(), viewer, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("scoped grant must not reach the default workspace, got %d %s", r.code, r.raw)
	}
}

// A scoped forbid overrides an RBAC grant on the real path: a tenant admin who holds
// agent:write by role is denied for agents in a quarantined workspace.
func TestScopedForbidOverridesRBACE2E(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "globex")
	hdr := tenantHdr(tenant)

	quarantine := h.createWorkspace(tenant, "quarantine")
	inQuarantine := h.createAgentIn(tenant, "q-bot", quarantine)
	inDefault := h.createAgentIn(tenant, "ok-bot", model.ID(""))
	_, tenantAdmin := h.roleUser(admin, tenant, "admin@globex.io", auth.RoleAdmin)

	h.publishGrant(admin, tenant, `forbid(principal, action, resource) when { resource in Workspace::"quarantine" };`)

	// The admin (RBAC: agent:write) is FORBIDDEN in quarantine...
	if r := h.do("DELETE", "/v1/agents/"+inQuarantine.ID.String(), tenantAdmin, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("scoped forbid must override RBAC, got %d %s", r.code, r.raw)
	}
	// ...but unaffected elsewhere.
	if r := h.do("DELETE", "/v1/agents/"+inDefault.ID.String(), tenantAdmin, nil, hdr); r.code != http.StatusNoContent {
		t.Errorf("forbid must not bleed past its scope, got %d %s", r.code, r.raw)
	}
}

// --- engine-level: the scope tree (agent-group fold, folder subtree, AAL) -----

// The agent-group fold: a grant targeting an AgentGroup expands to its member agents.
func TestScopedGrantAgentGroupFold(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "groupco")

	member := h.createAgentIn(tenant, "member", model.ID(""))
	outsider := h.createAgentIn(tenant, "outsider", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "payments-bots", model.ID(""))

	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in AgentGroup::"payments-bots" };`)

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	if sd := h.scoped(tenant, viewer, "agent:write", member.ID); sd.Effect != auth.EffectGrant {
		t.Errorf("a group member must be GRANTED, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := h.scoped(tenant, viewer, "agent:write", outsider.ID); sd.Effect != auth.EffectAbstain {
		t.Errorf("a non-member must ABSTAIN (fall to RBAC), got %v (%s)", sd.Effect, sd.Reason)
	}
}

// The folder subtree: a grant on a folder reaches every resource under it (materialized
// path), and nothing outside it.
func TestScopedGrantResourceSubtree(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "treeco")

	var folderID, childID, outsideID model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		folder, err := sc.Resources().Create(context.Background(), model.Resource{Name: "secrets", Kind: "folder"})
		if err != nil {
			return err
		}
		child, err := sc.Resources().CreateUnder(context.Background(), folder.ID, model.Resource{Name: "db", Kind: "postgres.table"})
		if err != nil {
			return err
		}
		outside, err := sc.Resources().Create(context.Background(), model.Resource{Name: "public", Kind: "s3.bucket"})
		folderID, childID, outsideID = folder.ID, child.ID, outside.ID
		return err
	}); err != nil {
		t.Fatalf("seed resource tree: %v", err)
	}

	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"resource:write", resource) when { resource in Resource::"`+folderID.String()+`" };`)

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	if sd := h.scoped(tenant, viewer, "resource:write", childID); sd.Effect != auth.EffectGrant {
		t.Errorf("a resource under the granted folder must be GRANTED, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := h.scoped(tenant, viewer, "resource:write", outsideID); sd.Effect != auth.EffectAbstain {
		t.Errorf("a resource outside the folder must ABSTAIN, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// An AAL-conditioned grant: only a step-up-verified (AAL3) principal is granted.
func TestScopedGrantAALCondition(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "aalco")
	agent := h.createAgentIn(tenant, "bot", model.ID(""))

	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { context.aal >= 3 };`)

	elevated := auth.ScopedPrincipal("cred-e", "e", tenant, auth.RoleViewer)
	elevated.AAL = 3
	if sd := h.scoped(tenant, elevated, "agent:write", agent.ID); sd.Effect != auth.EffectGrant {
		t.Errorf("an AAL3 principal must be GRANTED, got %v (%s)", sd.Effect, sd.Reason)
	}
	weak := auth.ScopedPrincipal("cred-w", "w", tenant, auth.RoleViewer)
	weak.AAL = 1
	if sd := h.scoped(tenant, weak, "agent:write", agent.ID); sd.Effect != auth.EffectAbstain {
		t.Errorf("an under-assured principal must ABSTAIN, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// Regression (review bug 1): a role-conditioned forbid must fire in the hooks-PEP
// RESTRICT-VIEW (Evaluator().Evaluate), not only on the main scoped path. Both paths
// build the SAME principal entity graph (role/user parents), so a
// `forbid(principal in Role::"viewer", …)` applies to a tool-call too.
func TestScopedRestrictViewAppliesRoleForbid(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "restrictco")
	h.publishGrant(admin, tenant, `forbid(principal in Role::"viewer", action == Action::"agent:read", resource);`)

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	reqV := auth.Request{Principal: viewer, Permission: "agent:read", Tenant: tenant, Resource: auth.ResourceFor("agent:read")}
	if dec, _ := h.gov.Evaluator().Evaluate(context.Background(), reqV); dec.Allow {
		t.Error("restrict-view must apply a role-conditioned forbid (principal role parent built)")
	}
	owner := auth.ScopedPrincipal("cred-o", "o", tenant, auth.RoleOwner)
	reqO := auth.Request{Principal: owner, Permission: "agent:read", Tenant: tenant, Resource: auth.ResourceFor("agent:read")}
	if dec, _ := h.gov.Evaluator().Evaluate(context.Background(), reqO); !dec.Allow {
		t.Error("restrict-view must not restrict a non-matching role")
	}
}

func TestScopedForbidRestrictsDelegatedToken(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "delegation-restrictco")
	h.publishGrant(admin, tenant, `forbid(principal, action == Action::"agent:read", resource) when { principal.is_delegated };`)

	direct := auth.ScopedPrincipal("cred-direct", "direct", tenant, auth.RoleViewer)
	delegated := direct.WithActAs(model.ID("on-behalf-of-user"))

	delegatedReq := auth.Request{
		Principal: delegated, Permission: "agent:read", Tenant: tenant, Resource: auth.ResourceFor("agent:read"),
	}
	delegatedDecision, err := h.gov.Evaluator().Evaluate(context.Background(), delegatedReq)
	if err != nil {
		t.Fatalf("evaluate delegated principal: %v", err)
	}
	if delegatedDecision.Allow {
		t.Error("a delegated principal must be denied by an is_delegated forbid")
	}

	directReq := auth.Request{
		Principal: direct, Permission: "agent:read", Tenant: tenant, Resource: auth.ResourceFor("agent:read"),
	}
	directDecision, err := h.gov.Evaluator().Evaluate(context.Background(), directReq)
	if err != nil {
		t.Fatalf("evaluate direct principal: %v", err)
	}
	if !directDecision.Allow {
		t.Error("a direct principal must remain allowed by an is_delegated forbid")
	}
}

// A scope-conditioned forbid cannot be resolved by the hooks-PEP restrict-view:
// its basic resource has no live workspace parents. The restriction must therefore
// fail closed for a request targeted by the policy head, without bleeding into a
// request that a constrained head excludes.
func TestScopedRestrictViewFailsClosedOnUnresolvableScopeForbid(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "scope-restrictco")
	principal := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	principal.AAL = 3

	h.publishGrant(admin, tenant, `forbid(principal, action, resource) when { resource in Workspace::"quarantine" };`)
	targeted := auth.Request{
		Principal: principal, Permission: "agent:read", Tenant: tenant, Resource: auth.ResourceFor("agent:read"),
	}
	if dec, _ := h.gov.Evaluator().Evaluate(context.Background(), targeted); dec.Allow {
		t.Error("restrict-view must fail closed when a targeted scope-conditioned forbid cannot be resolved")
	}

	// Re-publish the same scope restriction with an action-constrained head so the
	// negative assertion proves head matching, rather than treating a global forbid
	// as though it excluded any request.
	h.publishGrant(admin, tenant, `forbid(principal, action == Action::"agent:read", resource) when { resource in Workspace::"quarantine" && context.aal >= 3 };`)
	if dec, _ := h.gov.Evaluator().Evaluate(context.Background(), targeted); dec.Allow {
		t.Error("restrict-view must fail closed when the constrained head and non-scope condition target the request")
	}
	unrelated := auth.Request{
		Principal: principal, Permission: "session:read", Tenant: tenant, Resource: auth.ResourceFor("session:read"),
	}
	if dec, _ := h.gov.Evaluator().Evaluate(context.Background(), unrelated); !dec.Allow {
		t.Error("restrict-view must not fail closed when the scope-conditioned forbid head excludes the request")
	}
	weak := targeted
	weak.Principal.AAL = 1
	if dec, _ := h.gov.Evaluator().Evaluate(context.Background(), weak); !dec.Allow {
		t.Error("restrict-view must not fail closed when a non-scope forbid condition excludes the request")
	}
}

// The same fail-closed rule applies when the unresolvable hierarchy membership is
// authored directly in the policy head rather than in a when clause. The other head
// constraints still gate whether the forbid targets the request.
func TestScopedRestrictViewFailsClosedOnHeadFormScopeForbid(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "head-scope-restrictco")
	principal := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)

	h.publishGrant(admin, tenant, `forbid(principal, action == Action::"agent:read", resource in Workspace::"quarantine");`)
	targeted := auth.Request{
		Principal: principal, Permission: "agent:read", Tenant: tenant, Resource: auth.ResourceFor("agent:read"),
	}
	if dec, _ := h.gov.Evaluator().Evaluate(context.Background(), targeted); dec.Allow {
		t.Error("restrict-view must fail closed on an unresolvable head-form scope forbid")
	}

	unrelated := auth.Request{
		Principal: principal, Permission: "session:read", Tenant: tenant, Resource: auth.ResourceFor("session:read"),
	}
	if dec, _ := h.gov.Evaluator().Evaluate(context.Background(), unrelated); !dec.Allow {
		t.Error("restrict-view must preserve a non-matching action guard on a head-form scope forbid")
	}

	// Cover Cedar's other head-membership form and both scope-bearing variables.
	for _, policy := range []string{
		`forbid(principal, action == Action::"agent:read", resource is Resource in Workspace::"quarantine");`,
		`forbid(principal in Workspace::"quarantine", action == Action::"agent:read", resource);`,
	} {
		h.publishGrant(admin, tenant, policy)
		if dec, _ := h.gov.Evaluator().Evaluate(context.Background(), targeted); dec.Allow {
			t.Error("restrict-view must fail closed for an unresolvable head membership")
		}
	}

	// The type half of `is in` is resolvable and remains a strict head guard.
	h.publishGrant(admin, tenant, `forbid(principal, action == Action::"agent:read", resource is Session in Workspace::"quarantine");`)
	if dec, _ := h.gov.Evaluator().Evaluate(context.Background(), targeted); !dec.Allow {
		t.Error("restrict-view must preserve a non-matching resource type guard on a head-form scope forbid")
	}
}

// A tenant with no authored grant policy abstains for EVERY request — the back-compat
// invariant and the hot-path guarantee (no scope resolution happens without a policy).
func TestScopedAbstainsWithoutPolicy(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "nopolicy")
	agent := h.createAgentIn(tenant, "bot", model.ID(""))

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	if sd := h.scoped(tenant, viewer, "agent:write", agent.ID); sd.Effect != auth.EffectAbstain {
		t.Errorf("no policy ⇒ ABSTAIN, got %v (%s)", sd.Effect, sd.Reason)
	}
}
