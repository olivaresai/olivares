// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// resultIDs extracts the result entity ids from an AuthZEN search response.
func resultIDs(r resp) []string {
	var ids []string
	rs, _ := r.body["results"].([]any)
	for _, it := range rs {
		if m, ok := it.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// THE anti-divergence proof: the reverse queries (AuthZEN subject/resource search)
// reflect the SAME scoped-grant decision the enforced request path makes — workspace
// scope resolution and all. It mirrors TestScopedGrantWorkspaceEnforcementE2E (the
// forward path) but asks the question backwards. The harness wires the production-grade
// composed Authorizer (RBAC + Cedar scoped grants + deny-overlay), so this is the real
// engine, not a reimplementation.
func TestAuthZenReverseQueryReflectsScopedGrant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	payments := h.createWorkspace(tenant, "payments")
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	inDefault := h.createAgentIn(tenant, "default-bot", model.ID("")) // default workspace
	viewerID, _ := h.roleUser(admin, tenant, "viewer@acme.io", auth.RoleViewer)

	// A viewer cannot agent:write by role; a workspace-scoped grant authorizes it for
	// agents IN payments only.
	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`)

	// search/subject — "who can agent:write the in-payments agent?" includes the viewer
	// (via the scoped grant). The engine resolves the agent's TRUE workspace from the row.
	sIn := h.do("POST", "/access/v1/search/subject", admin, map[string]any{
		"subject": map[string]any{"type": "user"}, "action": map[string]any{"name": "agent:write"},
		"resource": map[string]any{"type": "agent", "id": inPayments.ID.String()},
	}, hdr)
	if sIn.code != http.StatusOK {
		t.Fatalf("search subject (payments) = %d %s", sIn.code, sIn.raw)
	}
	if !contains(resultIDs(sIn), viewerID) {
		t.Errorf("viewer must appear for the in-payments agent (scoped grant); results %v", resultIDs(sIn))
	}

	// ...but NOT for the default-workspace agent (the grant does not reach it) — exactly
	// as the forward DELETE path enforces.
	sDef := h.do("POST", "/access/v1/search/subject", admin, map[string]any{
		"subject": map[string]any{"type": "user"}, "action": map[string]any{"name": "agent:write"},
		"resource": map[string]any{"type": "agent", "id": inDefault.ID.String()},
	}, hdr)
	if contains(resultIDs(sDef), viewerID) {
		t.Errorf("viewer must NOT appear for the default-workspace agent; results %v", resultIDs(sDef))
	}

	// search/resource — "what agents can the viewer agent:write?" → only the payments one.
	sr := h.do("POST", "/access/v1/search/resource", admin, map[string]any{
		"subject": map[string]any{"type": "user", "id": viewerID}, "action": map[string]any{"name": "agent:write"},
		"resource": map[string]any{"type": "agent"},
	}, hdr)
	ids := resultIDs(sr)
	if !contains(ids, inPayments.ID.String()) || contains(ids, inDefault.ID.String()) {
		t.Errorf("viewer resource search = %v, want only the in-payments agent (%s)", ids, inPayments.ID.String())
	}

	// evaluation agrees per-resource (the single-decision PDP endpoint).
	evalViewer := func(agentID string) bool {
		r := h.do("POST", "/access/v1/evaluation", admin, map[string]any{
			"subject": map[string]any{"type": "user", "id": viewerID}, "action": map[string]any{"name": "agent:write"},
			"resource": map[string]any{"type": "agent", "id": agentID},
		}, hdr)
		d, _ := r.body["decision"].(bool)
		return d
	}
	if !evalViewer(inPayments.ID.String()) {
		t.Error("evaluation: viewer should be permitted to agent:write the in-payments agent")
	}
	if evalViewer(inDefault.ID.String()) {
		t.Error("evaluation: viewer must be denied agent:write on the default-workspace agent")
	}
}

// The sealed access-review export reflects a scoped grant too: the viewer appears with
// via=scoped-grant for the in-payments agent, and the export is sealed in the ledger.
func TestAccessReviewExportScopedGrant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin() // adminLogin already steps up to AAL3
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	payments := h.createWorkspace(tenant, "payments")
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	viewerID, _ := h.roleUser(admin, tenant, "viewer@acme.io", auth.RoleViewer)
	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`)

	r := h.do("POST", "/access/v1/access-review/export", admin, map[string]any{
		"resource": map[string]any{"type": "agent", "id": inPayments.ID.String()},
	}, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("export = %d %s", r.code, r.raw)
	}
	if integ, _ := r.body["integrity"].(map[string]any); integ["sealed"] != true {
		t.Errorf("export must be sealed; integrity = %v", r.body["integrity"])
	}
	entries, _ := r.body["entries"].([]any)
	foundViaGrant := false
	for _, e := range entries {
		m, _ := e.(map[string]any)
		subj, _ := m["subject"].(map[string]any)
		if subj["id"] == viewerID && m["permission"] == "agent:write" {
			foundViaGrant = true
			if m["via"] != "scoped-grant" {
				t.Errorf("viewer agent:write via = %v, want scoped-grant", m["via"])
			}
		}
	}
	if !foundViaGrant {
		t.Errorf("export must show the viewer's scoped-grant access; entries = %v", entries)
	}
	if !contains(h.auditActions(tenant), "access_review.export") {
		t.Error("export must be sealed with an access_review.export ledger event")
	}
}
