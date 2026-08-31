// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"
)

// TestListAccessEdgesDeniedForConfinedOperator is the F2 regression: the tenant-wide
// access graph is a sensitive cross-workspace read; a workspace-confined operator (who holds
// accessgraph:read by role) must be DENIED the collection, not leak every agent→resource edge.
func TestListAccessEdgesDeniedForConfinedOperator(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	ws := h.createWorkspace(tenant, "payments")
	_, confined := h.confinedUser(admin, tenant, "conf@acme.io", "admin", ws)

	// An UNCONFINED admin reads the access graph (proves the role holds accessgraph:read).
	if r := h.do("GET", "/v1/access-edges", admin, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("unconfined admin access-edges = %d %s, want 200", r.code, r.raw)
	}
	// The workspace-confined operator is DENIED (F2).
	if r := h.do("GET", "/v1/access-edges", confined, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("confined operator access-edges = %d %s, want 403 (F2 cross-workspace disclosure)", r.code, r.raw)
	}
}

// TestListAgentGroupMembersConfinedByGroupWorkspace is the F3 regression: a group READ is
// authorized against the GROUP entity, so the scoped engine derives the group's workspace and a
// workspace-confined operator can read its OWN workspace's group but NOT a cross-workspace one.
// Pre-fix the group id resolved with the "agent" kind, so no workspace bound and the cross-
// workspace read was allowed.
func TestListAgentGroupMembersConfinedByGroupWorkspace(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	payments := h.createWorkspace(tenant, "payments")
	other := h.createWorkspace(tenant, "other")

	inPay := h.createAgentIn(tenant, "pay-bot", payments)
	payGroup := h.addAgentToGroup(tenant, inPay.ID, "pay-group", payments)
	inOther := h.createAgentIn(tenant, "other-bot", other)
	otherGroup := h.addAgentToGroup(tenant, inOther.ID, "other-group", other)

	_, confined := h.confinedUser(admin, tenant, "conf@acme.io", "admin", payments)

	// Own-workspace group: allowed.
	if r := h.do("GET", "/v1/agent-groups/"+payGroup.ID.String()+"/members", confined, nil, hdr); r.code != http.StatusOK {
		t.Errorf("confined op read of own-workspace group = %d %s, want 200", r.code, r.raw)
	}
	// Cross-workspace group: DENIED (F3).
	if r := h.do("GET", "/v1/agent-groups/"+otherGroup.ID.String()+"/members", confined, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("confined op read of cross-workspace group = %d %s, want 403 (F3)", r.code, r.raw)
	}
	// The unconfined superadmin reads both (never confined).
	if r := h.do("GET", "/v1/agent-groups/"+otherGroup.ID.String()+"/members", admin, nil, hdr); r.code != http.StatusOK {
		t.Errorf("superadmin read of any group = %d %s, want 200", r.code, r.raw)
	}
}
