// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// THE ASYMMETRY BETWEEN WHAT THE ENGINE ALLOWS AND WHAT THE CONSOLE IS TOLD.
//
// /v1/auth/whoami hands the console one permission set per grant, computed from the
// ROLE alone (core/api/whoami_perms.go caches by rolePermKey{role, confined}); since
// #578 the console's can() is pure membership of that set. A scoped grant is not an
// input to it, so any authority a principal holds by grant is invisible — and because
// can() HIDES rather than over-offers, there is no 403 anywhere to notice it by.
//
// These tests do not assert an opinion about that. They pin the two halves of the fact,
// so nobody has to re-derive them from the code, and so the day either half changes a
// test says which one.

// whoamiPerms returns the permission strings whoami reports for tok in tenant, and
// whether a grant for that tenant was present at all.
func whoamiPerms(t *testing.T, h *harness, tok string, tenant model.TenantID) ([]string, bool) {
	t.Helper()
	r := h.do("GET", "/v1/auth/whoami", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("whoami = %d %s", r.code, r.raw)
	}
	grants, _ := r.body["grants"].([]any)
	for _, g := range grants {
		gm, _ := g.(map[string]any)
		if gm == nil || gm["tenant"] != tenant.String() {
			continue
		}
		raw, _ := gm["permissions"].([]any)
		out := make([]string, 0, len(raw))
		for _, p := range raw {
			if s, ok := p.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	}
	return nil, false
}

func hasPerm(perms []string, want string) bool {
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}

// TestWhoamiHidesWhatAWorkspaceScopedGrantAllows is the defect, end to end, on the real
// REST path: the SAME principal, in the SAME request window, is allowed the action by the
// engine and told by whoami that it does not hold the permission.
//
// The 204 is not incidental — it is the control. Without it the whoami assertion would
// pass just as happily against a grant that authorizes nothing at all, which is the shape
// of a test that measures nothing.
func TestWhoamiHidesWhatAWorkspaceScopedGrantAllows(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	payments := h.createWorkspace(tenant, "payments")
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	_, viewer := h.roleUser(admin, tenant, "viewer@acme.io", auth.RoleViewer)

	// Baseline, both sides: the role denies it and whoami says so.
	if r := h.do("DELETE", "/v1/agents/"+inPayments.ID.String(), viewer, nil, hdr); r.code != http.StatusForbidden {
		t.Fatalf("baseline viewer delete must be 403, got %d %s", r.code, r.raw)
	}
	before, ok := whoamiPerms(t, h, viewer, tenant)
	if !ok {
		t.Fatal("whoami reported no grant for the tenant; the assertions below would be vacuous")
	}
	if len(before) == 0 {
		t.Fatal("whoami reported an EMPTY set for a viewer; the absence assertions below would be vacuous")
	}
	if hasPerm(before, "agent:write") {
		t.Fatal("precondition: a viewer must not hold agent:write by role")
	}
	// The set is not empty of everything — it really is reporting a viewer.
	if !hasPerm(before, "agent:read") {
		t.Fatalf("precondition: a viewer's set must contain agent:read, got %v", before)
	}

	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`)

	// THE ENGINE ALLOWS IT — the control that makes the next assertion mean something.
	if r := h.do("DELETE", "/v1/agents/"+inPayments.ID.String(), viewer, nil, hdr); r.code != http.StatusNoContent {
		t.Fatalf("scoped grant must authorize the delete (204), got %d %s", r.code, r.raw)
	}

	// ...AND WHOAMI STILL SAYS THE PRINCIPAL MAY NOT. The console hides the button it
	// just proved the operator may press, and no request fails to reveal it.
	after, _ := whoamiPerms(t, h, viewer, tenant)
	if hasPerm(after, "agent:write") {
		t.Fatal("whoami now reports agent:write — this test pins the OPPOSITE; if the set " +
			"grew a grant-aware path, update this test WITH its reason, do not delete it")
	}
}

// TestWhoamiHidesWhatAFreeFormCedarGrantAllows pins the LIMIT of the fix, and it is
// a limit by nature rather than by effort.
//
// whoami now reports the permissions a tenant-scoped STRUCTURED grant confers
// (auth.UnconditionalGrantReporter). It cannot report the operator's free-form `cedar`
// surface, which this test publishes: that surface can express an unconditional permit,
// but it can equally condition on `context.aal`, on time, or on resource attributes, and
// no permission set computed before the request can decide those. So the free-form
// surface keeps the pre-existing under-report — it hides, which is the safe direction.
//
// If this ever starts failing, someone taught whoami to evaluate authored Cedar
// statically. That is a much larger claim than it looks and it needs its own design, not
// a test update.
func TestWhoamiHidesWhatAFreeFormCedarGrantAllows(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "globex")
	hdr := tenantHdr(tenant)

	_, viewer := h.roleUser(admin, tenant, "viewer@globex.io", auth.RoleViewer)

	// A COLLECTION route (no entity, no workspace): the shape a scope-tree grant cannot
	// reach and a tenant-scoped grant can.
	create := map[string]any{"name": "t-bot", "kind": "claude-code"}
	if r := h.do("POST", "/v1/agents", viewer, create, hdr); r.code != http.StatusForbidden {
		t.Fatalf("baseline viewer create must be 403, got %d %s", r.code, r.raw)
	}

	// Tenant-wide: no resource condition at all — this is what scope tree "tenant" projects.
	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource);`)

	if r := h.do("POST", "/v1/agents", viewer, create, hdr); r.code != http.StatusCreated {
		t.Fatalf("tenant-scoped grant must authorize the collection create (201), got %d %s", r.code, r.raw)
	}
	perms, ok := whoamiPerms(t, h, viewer, tenant)
	if !ok || len(perms) == 0 {
		t.Fatal("whoami reported no set; the assertion below would be vacuous")
	}
	if hasPerm(perms, "agent:write") {
		t.Fatal("whoami reports agent:write from a FREE-FORM cedar policy. Deciding an " +
			"authored Cedar policy statically is not something a permission set can do in " +
			"general; if this became possible it needs its own design, not a test update")
	}
}

// TestWhoamiReportsWhatATenantScopedStructuredGrantAllows is the fix, on the wire.
//
// The grant is authored through the REAL delegation API (POST /rbac/grants), which is what
// the console's own RBAC screen calls — so this is the path an operator actually takes,
// not a hand-written policy. Scope tree `tenant` projects a permit with no `when` clause,
// so the authority is flat and a flat permission set can carry it honestly.
//
// Both directions are asserted, because either alone is satisfiable by a bug: the engine
// must allow the action AND whoami must say so. A set that reported the permission while
// the engine refused would be the over-offer #578 removed.
func TestWhoamiReportsWhatATenantScopedStructuredGrantAllows(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "umbrella")
	hdr := tenantHdr(tenant)

	uid, viewer := h.roleUser(admin, tenant, "viewer@umbrella.io", auth.RoleViewer)

	before, ok := whoamiPerms(t, h, viewer, tenant)
	if !ok || len(before) == 0 {
		t.Fatal("whoami reported no set for the viewer; every assertion below would be vacuous")
	}
	if hasPerm(before, "agent:write") {
		t.Fatal("precondition: a viewer must not hold agent:write by role")
	}
	if r := h.do("POST", "/v1/agents", viewer, map[string]any{"name": "b0", "kind": "claude-code"}, hdr); r.code != http.StatusForbidden {
		t.Fatalf("baseline viewer create must be 403, got %d %s", r.code, r.raw)
	}

	// The real authoring surface: "this user is an editor, tenant-wide".
	grant := map[string]any{
		"subject_kind": "user", "subject_ref": uid,
		"role": auth.RoleEditor, "scope_tree": "tenant",
	}
	if r := h.do("POST", "/v1/m/governance/rbac/grants", admin, grant, hdr); r.code != http.StatusCreated {
		t.Fatalf("create scoped grant = %d %s", r.code, r.raw)
	}

	// The engine allows it...
	if r := h.do("POST", "/v1/agents", viewer, map[string]any{"name": "b1", "kind": "claude-code"}, hdr); r.code != http.StatusCreated {
		t.Fatalf("the tenant-scoped grant must authorize the create (201), got %d %s", r.code, r.raw)
	}
	// ...and whoami now SAYS so, which is the whole point: the console can offer the button.
	after, _ := whoamiPerms(t, h, viewer, tenant)
	if !hasPerm(after, "agent:write") {
		t.Errorf("whoami still hides agent:write after a tenant-scoped grant conferred it; got %v", after)
	}
	// The role is unchanged — the extra authority arrives as permissions, not as a
	// promotion. A console that re-derived from the role would still hide the action.
	r := h.do("GET", "/v1/auth/whoami", viewer, nil, hdr)
	if gs, _ := r.body["grants"].([]any); len(gs) > 0 {
		if gm, _ := gs[0].(map[string]any); gm != nil && gm["role"] != auth.RoleViewer {
			t.Errorf("role reported as %v, want viewer: the grant must not rewrite the membership", gm["role"])
		}
	}
	// It must never carry the system permission, whatever a grant says.
	if hasPerm(after, "system:admin") {
		t.Error("whoami reported system:admin in a tenant set")
	}
}

// TestWhoamiDoesNotReportAWorkspaceScopedStructuredGrant is the boundary of the fix,
// asserted rather than trusted: the SAME authoring API, the SAME role, differing only in
// scope, must NOT reach whoami. Reporting it would offer the action on every agent in the
// tenant while the engine authorizes it only inside the workspace — the over-offer this
// design exists to avoid.
func TestWhoamiDoesNotReportAWorkspaceScopedStructuredGrant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "wayne")
	hdr := tenantHdr(tenant)

	payments := h.createWorkspace(tenant, "payments")
	uid, viewer := h.roleUser(admin, tenant, "viewer@wayne.io", auth.RoleViewer)

	grant := map[string]any{
		"subject_kind": "user", "subject_ref": uid,
		"role": auth.RoleEditor, "scope_tree": "workspace", "scope_ref": "payments",
	}
	if r := h.do("POST", "/v1/m/governance/rbac/grants", admin, grant, hdr); r.code != http.StatusCreated {
		t.Fatalf("create workspace-scoped grant = %d %s", r.code, r.raw)
	}

	// Control: the grant is LIVE — it authorizes inside its workspace. Without this the
	// assertion below would pass against a grant that was simply never created.
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	if r := h.do("DELETE", "/v1/agents/"+inPayments.ID.String(), viewer, nil, hdr); r.code != http.StatusNoContent {
		t.Fatalf("control: the workspace grant must authorize inside its scope (204), got %d %s", r.code, r.raw)
	}
	perms, ok := whoamiPerms(t, h, viewer, tenant)
	if !ok || len(perms) == 0 {
		t.Fatal("whoami reported no set; the assertion below would be vacuous")
	}
	if hasPerm(perms, "agent:write") {
		t.Error("whoami reported agent:write from a WORKSPACE-scoped grant: the console would " +
			"offer it on every agent in the tenant and 403 on all but one workspace")
	}
}

// TestWhoamiReportsTheDelegationPermitOfAScopedGrant covers the case the author missed and
// an adversarial contrast found: ONE grant projects TWO permits with different conditions.
//
// projectManagedCedar emits the grant's ACCESS permit with its scope condition, and — for
// an admin-capable USER or GROUP subject — a SECOND, tenant-wide permit for
// governance:rbac:{read,admin} with an EMPTY when clause (writePermit(subj, acts, "")).
// So a grant scoped to one workspace still confers the delegation surface TENANT-WIDE, and
// reporting only tenant-scoped grants hid the Roles screen from the delegated workspace
// admin — the exact operator scoped delegation exists for.
//
// The test asserts both halves of the SAME grant in opposite directions, which is what
// makes it hard to satisfy by accident: the unconditional half must be reported, the
// scope-conditioned half must not.
func TestWhoamiReportsTheDelegationPermitOfAScopedGrant(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "stark")
	hdr := tenantHdr(tenant)

	h.createWorkspace(tenant, "payments")
	uid, deleg := h.roleUser(root, tenant, "wsadmin@stark.io", auth.RoleViewer)

	before, ok := whoamiPerms(t, h, deleg, tenant)
	if !ok || len(before) == 0 {
		t.Fatal("whoami reported no set; every assertion below would be vacuous")
	}
	if hasPerm(before, "governance:rbac:admin") {
		t.Fatal("precondition: a viewer must not hold governance:rbac:admin by role")
	}

	// An ADMIN inside one workspace: admin-capable (RoleRank >= admin), so the projection
	// also emits the tenant-wide delegation permit.
	grant := map[string]any{
		"subject_kind": "user", "subject_ref": uid,
		"role": auth.RoleAdmin, "scope_tree": "workspace", "scope_ref": "payments",
	}
	if r := h.do("POST", "/v1/m/governance/rbac/grants", root, grant, hdr); r.code != http.StatusCreated {
		t.Fatalf("create workspace-scoped admin grant = %d %s", r.code, r.raw)
	}

	// The control: the delegation authority is REAL — the principal reaches the RBAC API,
	// which is a tenant-level collection route no scope-conditioned permit could authorize.
	if r := h.do("GET", "/v1/m/governance/rbac/grants", deleg, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("control: the delegation permit must authorize the RBAC API (200), got %d %s", r.code, r.raw)
	}

	after, _ := whoamiPerms(t, h, deleg, tenant)
	if !hasPerm(after, "governance:rbac:admin") {
		t.Errorf("whoami hides governance:rbac:admin, so the console hides the Roles screen "+
			"from a delegated workspace admin who may in fact use it; got %v", after)
	}
	// ...and the scope-conditioned half of the SAME grant is still correctly absent.
	if hasPerm(after, "agent:write") {
		t.Error("whoami reported agent:write from the workspace-scoped ACCESS permit: only the " +
			"delegation permit is unconditional")
	}
}

// TestWorkspaceScopedGrantCannotReachACollectionRoute is the boundary the measurement
// rests on, and it is what makes the bound narrow rather than assumed: a
// scope-conditioned permit needs a resource it can place in the tree, and a collection
// route hands the engine none.
//
// Without this, the claim "a workspace grant reaches only the entity routes" would be an
// inference from reading grants.go. With it, the engine says so.
func TestWorkspaceScopedGrantCannotReachACollectionRoute(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "initech")
	hdr := tenantHdr(tenant)

	payments := h.createWorkspace(tenant, "payments")
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	_, viewer := h.roleUser(admin, tenant, "viewer@initech.io", auth.RoleViewer)

	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`)

	// The ENTITY route is authorized (the grant is live and does grant) ...
	if r := h.do("DELETE", "/v1/agents/"+inPayments.ID.String(), viewer, nil, hdr); r.code != http.StatusNoContent {
		t.Fatalf("control: the workspace grant must authorize the ENTITY route, got %d %s", r.code, r.raw)
	}
	// ... and the COLLECTION route is NOT: no entity and no declared workspace, so no
	// `resource in Workspace::…` can match. This is why hiding those actions is CORRECT
	// for a workspace grant — the engine would refuse them too.
	if r := h.do("POST", "/v1/agents", viewer, map[string]any{"name": "new-bot", "kind": "claude-code"}, hdr); r.code != http.StatusForbidden {
		t.Errorf("a workspace-scoped grant must NOT reach a collection route, got %d %s", r.code, r.raw)
	}
}

// TestWhoamiStopsReportingAGrantThatHasExpiredOffline is the other case the contrast
// found: the reported set must follow the same CLOCK the engine decides by.
//
// ADR-0024 Q1: past policy_max_staleness a positive grant expires deny-closed — the engine
// turns its Cedar Allow into an ABSTAIN and the request falls back to RBAC (grants.go
// grantExpired). The stored ROWS do not change when that happens, so a reporter reading
// rows alone keeps offering authority the engine has already stopped honoring. An
// over-offer produced by a clock is the hardest kind to diagnose from a 403, because
// nothing an operator can see has changed.
//
// Both directions are asserted at both times, so neither half can pass by standing still.
func TestWhoamiStopsReportingAGrantThatHasExpiredOffline(t *testing.T) {
	const bound = 72 * time.Hour
	h := newHarnessWith(t, harnessOpts{offlineStaleness: bound})
	root := h.adminLogin()
	tenant := h.createOrg(root, "edgecorp")
	hdr := tenantHdr(tenant)

	uid, viewer := h.roleUser(root, tenant, "viewer@edgecorp.io", auth.RoleViewer)
	grant := map[string]any{
		"subject_kind": "user", "subject_ref": uid,
		"role": auth.RoleEditor, "scope_tree": "tenant",
	}
	if r := h.do("POST", "/v1/m/governance/rbac/grants", root, grant, hdr); r.code != http.StatusCreated {
		t.Fatalf("create tenant-scoped grant = %d %s", r.code, r.raw)
	}
	// C3 anchors local policy freshness to the store's transaction clock. Align the
	// deterministic evaluator clock before advancing the offline window; otherwise
	// this test compares its June fixture time with an unrelated live DB timestamp.
	alignHarnessClockToDurableFreshness(t, h, tenant)

	// FRESH: the engine authorizes and whoami says so.
	if r := h.do("POST", "/v1/agents", viewer, map[string]any{"name": "fresh", "kind": "claude-code"}, hdr); r.code != http.StatusCreated {
		t.Fatalf("fresh grant must authorize the create (201), got %d %s", r.code, r.raw)
	}
	if fresh, _ := whoamiPerms(t, h, viewer, tenant); !hasPerm(fresh, "agent:write") {
		t.Fatalf("fresh grant must be reported, or the expiry assertion below proves nothing: %v", fresh)
	}

	// One tick past the bound the positive grant expires deny-closed.
	h.clk.advance(bound + time.Second)

	// The engine now refuses — the control for the assertion that follows.
	if r := h.do("POST", "/v1/agents", viewer, map[string]any{"name": "stale", "kind": "claude-code"}, hdr); r.code != http.StatusForbidden {
		t.Fatalf("past the staleness bound the grant must no longer authorize (403), got %d %s", r.code, r.raw)
	}
	if stale, _ := whoamiPerms(t, h, viewer, tenant); hasPerm(stale, "agent:write") {
		t.Error("whoami still reports agent:write after the grant expired offline: the console " +
			"offers an action the engine now refuses, and nothing visible to the operator changed")
	}
}
