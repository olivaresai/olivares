// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// The route-metadata battery (V269 / docs/contracts/COCKPIT-02-authz.md §5).
//
// The property under test is a DIRECTION, not a value: route metadata may only remove
// the RBAC term, never add one. A battery that only checked "editor is denied on a
// RequireScopedGrant route" would pass for an implementation that denies everybody.

func editorPrincipal(tenant string) Principal {
	return newPrincipal(KindUser, "u1", "c1", false, "editor",
		map[model.TenantID]string{model.TenantID(tenant): RoleEditor}, nil)
}

func adminPrincipal(tenant string) Principal {
	return newPrincipal(KindUser, "u2", "c2", false, "admin",
		map[model.TenantID]string{model.TenantID(tenant): RoleAdmin}, nil)
}

func TestRouteMetadataZeroIsInert(t *testing.T) {
	var m RouteMetadata
	if !m.IsZero() {
		t.Fatal("the zero RouteMetadata must report itself inert")
	}
	req := Request{Principal: editorPrincipal("t"), Permission: "ns:thing:write", Tenant: "t"}
	// CONTROL POSITIVO: without metadata the RBAC answer passes through untouched, in
	// BOTH directions.
	if !m.rbacPermitted(req, true) {
		t.Error("inert metadata must let an RBAC allow through")
	}
	if m.rbacPermitted(req, false) {
		t.Error("inert metadata must not manufacture an allow from an RBAC deny")
	}
}

func TestRequireScopedGrantRemovesTheRBACTerm(t *testing.T) {
	m := RouteMetadata{RequireScopedGrant: true}
	for _, p := range []Principal{editorPrincipal("t"), adminPrincipal("t")} {
		req := Request{Principal: p, Permission: "session-cockpit:input:write", Tenant: "t"}
		if m.rbacPermitted(req, true) {
			t.Errorf("%s: RequireScopedGrant must remove the RBAC term, breadth of role included", p.DisplayName)
		}
	}
}

func TestRBACMinimumRoleNarrowsOnlyTheRBACPath(t *testing.T) {
	m := RouteMetadata{RBACMinimumRole: RoleAdmin}
	editor := Request{Principal: editorPrincipal("t"), Permission: "ns:thing:read", Tenant: "t"}
	admin := Request{Principal: adminPrincipal("t"), Permission: "ns:thing:read", Tenant: "t"}
	if m.rbacPermitted(editor, true) {
		t.Error("an editor must not satisfy RBACMinimumRole=admin")
	}
	// CONTROL POSITIVO: the floor is a floor, not a wall.
	if !m.rbacPermitted(admin, true) {
		t.Error("an admin must satisfy RBACMinimumRole=admin")
	}
	// A principal with no membership in the tenant cannot satisfy a role floor.
	none := Request{Principal: editorPrincipal("other"), Permission: "ns:thing:read", Tenant: "t"}
	if m.rbacPermitted(none, true) {
		t.Error("a principal with no role in the tenant must not satisfy a role floor")
	}
}

// TestRouteMetadataNeverGrants is the invariant stated as a test rather than as prose:
// no combination of metadata turns an RBAC deny into an allow.
func TestRouteMetadataNeverGrants(t *testing.T) {
	req := Request{Principal: editorPrincipal("t"), Permission: "ns:thing:write", Tenant: "t"}
	for _, m := range []RouteMetadata{
		{},
		{RequireScopedGrant: true},
		{RBACMinimumRole: RoleViewer},
		{RBACMinimumRole: RoleOwner},
		{CedarAction: "shell:open"},
		{RequireScopedGrant: true, RBACMinimumRole: RoleViewer, CedarAction: "shell:open", MinimumAAL: 3},
	} {
		if m.rbacPermitted(req, false) {
			t.Errorf("metadata %+v turned an RBAC deny into an allow — metadata may only remove a term", m)
		}
	}
}

// TestMinimumAALIsNotAnAlgebraTerm pins that the Authorizer does not read MinimumAAL.
// It is a precondition of AUTHENTICATION, answered with a step-up before the decision;
// folding it into the algebra would make "prove who you are again" indistinguishable
// from "you may not do this", and those have different remedies.
func TestMinimumAALIsNotAnAlgebraTerm(t *testing.T) {
	req := Request{Principal: editorPrincipal("t"), Permission: "ns:thing:write", Tenant: "t"}
	high := RouteMetadata{MinimumAAL: 3}
	var inert RouteMetadata
	if high.rbacPermitted(req, true) != inert.rbacPermitted(req, true) {
		t.Error("MinimumAAL must not change the authorization term; the route wrapper enforces it")
	}
}

// TestTranscriptReadIsPrivileged pins the claim COCKPIT-02 §4 makes and that an
// adversarial contrast found FALSE against the tree: without the entry, the module verb
// tier hands a `:read` permission to every viewer.
//
// Both directions, because the map's guarantee is precisely a role-tier floor and not an
// absolute ceiling: a viewer must not HOLD it, and an editor must.
func TestTranscriptReadIsPrivileged(t *testing.T) {
	const perm Permission = "session-cockpit:transcript:read"
	if RoleGrants(RoleViewer, perm) {
		t.Error("a viewer must not hold the transcript read by role: it is the literal keystrokes " +
			"and output of a governed terminal, recorded with no masking toggle")
	}
	for _, role := range []string{RoleEditor, RoleAdmin, RoleOwner} {
		if !RoleGrants(role, perm) {
			t.Errorf("%s must hold the transcript read by role", role)
		}
	}
	// CONTROL: an ordinary module read of the same namespace still reaches a viewer, so
	// the entry narrows THIS permission and not the namespace.
	if !RoleGrants(RoleViewer, "session-cockpit:session:read") {
		t.Error("the privileged entry must not sweep in the namespace's ordinary reads")
	}
}
