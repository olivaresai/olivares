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

// createWorkspaceUnder creates a workspace as a department under parent (zero is a root).
func (h *harness) createWorkspaceUnder(tenant model.TenantID, parent model.ID, slug string) model.ID {
	h.t.Helper()
	var id model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(context.Background(), model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive, ParentID: parent})
		id = ws.ID
		return err
	}); err != nil {
		h.t.Fatalf("create workspace %s: %v", slug, err)
	}
	return id
}

func (h *harness) moveWorkspace(tenant model.TenantID, node, parent model.ID) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Workspaces().SetParent(context.Background(), node, parent)
		return err
	}); err != nil {
		h.t.Fatalf("move workspace %s: %v", node, err)
	}
}

// A grant on a department reaches every sub-department below it, for each kind of node the
// scope tree holds, and nothing beside or above it. Proven on the real DELETE /agents/{id}
// path as well as on the engine's scoped decision.
func TestScopedGrantOnADepartmentReachesItsSubDepartments(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "deptco")
	hdr := tenantHdr(tenant)

	eng := h.createWorkspaceUnder(tenant, "", "eng")
	platform := h.createWorkspaceUnder(tenant, eng, "eng-platform")
	sre := h.createWorkspaceUnder(tenant, platform, "eng-sre")
	sales := h.createWorkspaceUnder(tenant, "", "sales")

	inEng := h.createAgentIn(tenant, "eng-bot", eng)
	inPlatform := h.createAgentIn(tenant, "platform-bot", platform)
	inSRE := h.createAgentIn(tenant, "sre-bot", sre)
	inSales := h.createAgentIn(tenant, "sales-bot", sales)
	inDefault := h.createAgentIn(tenant, "default-bot", model.ID(""))
	// An agent in no department by itself, reached through a group that lives in eng-sre.
	viaGroup := h.createAgentIn(tenant, "group-bot", model.ID(""))
	h.addAgentToGroup(tenant, viaGroup.ID, "sre-team", sre)

	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"eng" };`)

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	for _, tc := range []struct {
		name  string
		agent model.ID
		want  auth.Effect
	}{
		{"the department itself", inEng.ID, auth.EffectGrant},
		{"a sub-department", inPlatform.ID, auth.EffectGrant},
		{"a sub-sub-department", inSRE.ID, auth.EffectGrant},
		{"an agent group of a sub-sub-department", viaGroup.ID, auth.EffectGrant},
		{"a sibling department", inSales.ID, auth.EffectAbstain},
		{"the default workspace", inDefault.ID, auth.EffectAbstain},
	} {
		if sd := h.scoped(tenant, viewer, "agent:write", tc.agent); sd.Effect != tc.want {
			t.Errorf("%s: effect %v, want %v (%s)", tc.name, sd.Effect, tc.want, sd.Reason)
		}
	}

	_, viewerToken := h.roleUser(admin, tenant, "viewer@deptco.io", auth.RoleViewer)
	if r := h.do("DELETE", "/v1/agents/"+inSRE.ID.String(), viewerToken, nil, hdr); r.code != http.StatusNoContent {
		t.Errorf("a grant on eng: viewer delete of an eng-sre agent = %d %s, want 204", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/agents/"+inSales.ID.String(), viewerToken, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("a grant on eng must not reach sales: delete = %d %s, want 403", r.code, r.raw)
	}
}

// A grant on a sub-department never reaches the departments above it, and moving a
// department in the tree moves what a grant reaches with it.
func TestScopedGrantFollowsTheDepartmentTree(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "treeco")

	eng := h.createWorkspaceUnder(tenant, "", "eng")
	platform := h.createWorkspaceUnder(tenant, eng, "eng-platform")
	sales := h.createWorkspaceUnder(tenant, "", "sales")
	inEng := h.createAgentIn(tenant, "eng-bot", eng)
	inPlatform := h.createAgentIn(tenant, "platform-bot", platform)
	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	effect := func(agent model.ID) auth.Effect { return h.scoped(tenant, viewer, "agent:write", agent).Effect }

	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"eng-platform" };`)
	if got := effect(inEng.ID); got != auth.EffectAbstain {
		t.Errorf("a grant on eng-platform reached its parent eng: %v", got)
	}
	if got := effect(inPlatform.ID); got != auth.EffectGrant {
		t.Errorf("a grant on eng-platform must reach eng-platform: %v", got)
	}

	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"eng" };`)
	if got := effect(inPlatform.ID); got != auth.EffectGrant {
		t.Fatalf("a grant on eng must reach eng-platform before the move: %v", got)
	}
	h.moveWorkspace(tenant, platform, sales)
	if got := effect(inPlatform.ID); got != auth.EffectAbstain {
		t.Errorf("after eng-platform moved under sales a grant on eng still reaches it: %v", got)
	}
	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"sales" };`)
	if got := effect(inPlatform.ID); got != auth.EffectGrant {
		t.Errorf("after eng-platform moved under sales a grant on sales must reach it: %v", got)
	}
}

// A forbid on a department is as absolute below it as at it: a lineage too short to see the
// parent would let a sub-department escape it, which is the unsafe direction.
func TestScopedForbidOnADepartmentReachesItsSubDepartments(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "forbidco")

	eng := h.createWorkspaceUnder(tenant, "", "eng")
	sre := h.createWorkspaceUnder(tenant, h.createWorkspaceUnder(tenant, eng, "eng-platform"), "eng-sre")
	sales := h.createWorkspaceUnder(tenant, "", "sales")
	inSRE := h.createAgentIn(tenant, "sre-bot", sre)
	inSales := h.createAgentIn(tenant, "sales-bot", sales)

	h.publishGrant(admin, tenant, `forbid(principal, action, resource) when { resource in Workspace::"eng" };`)
	admins := auth.ScopedPrincipal("cred-a", "a", tenant, auth.RoleAdmin)
	if sd := h.scoped(tenant, admins, "agent:write", inSRE.ID); sd.Effect != auth.EffectForbid {
		t.Errorf("a forbid on eng must reach an eng-sre agent, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := h.scoped(tenant, admins, "agent:write", inSales.ID); sd.Effect == auth.EffectForbid {
		t.Errorf("a forbid on eng must not reach sales, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// A collection action declares its workspace and has no stored row to walk: the declared
// department still sits in the tree, so a grant on its parent reaches it.
func TestScopedGrantReachesADeclaredSubDepartment(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "declareco")

	eng := h.createWorkspaceUnder(tenant, "", "eng")
	sre := h.createWorkspaceUnder(tenant, h.createWorkspaceUnder(tenant, eng, "eng-platform"), "eng-sre")
	sales := h.createWorkspaceUnder(tenant, "", "sales")
	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"eng" };`)

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	declared := func(ws model.ID) auth.Effect {
		res := auth.ResourceFor("agent:write")
		res.WorkspaceID = ws
		sd, err := h.gov.ScopedGrants().Scoped(context.Background(), auth.Request{Principal: viewer, Permission: "agent:write", Tenant: tenant, Resource: res})
		if err != nil {
			t.Fatalf("Scoped: %v", err)
		}
		return sd.Effect
	}
	if got := declared(sre); got != auth.EffectGrant {
		t.Errorf("a collection action declaring eng-sre under a grant on eng = %v, want grant", got)
	}
	if got := declared(sales); got != auth.EffectAbstain {
		t.Errorf("a collection action declaring sales under a grant on eng = %v, want abstain", got)
	}
}
