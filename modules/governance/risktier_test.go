// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// Tier tests. They exercise the 4-tier CLASSIFICATION and the CRITICAL
// dual-authorization floor — the quorum/SoD/duplicate-decider primitives are
// own (approvals_test.go) and are consumed, not re-proven.

// createApprovalPolicy authors an enabled approval policy and returns its id.
func (h *harness) createApprovalPolicy(token string, tenant model.TenantID, name string, spec map[string]any) string {
	h.t.Helper()
	r := h.do("POST", govPath+"/policies", token, map[string]any{
		"name": name, "kind": "approval", "enabled": true, "spec": spec,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create approval policy %s = %d %s", name, r.code, r.raw)
	}
	return r.body["id"].(string)
}

// A CRITICAL action with NO matching policy starts at the AC-3(2) floor: two
// distinct human approvers — one approval leaves it pending, the second (from a
// different human) crosses it.
func TestCriticalActionRequiresTwoApprovers(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")

	r := h.createApproval(editor, tenant, map[string]any{"subject_kind": "deployment", "subject_ref": "prod-1", "action": "deploy.apply"})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	if got := r.body["risk_tier"]; got != "critical" {
		t.Fatalf("deploy.apply must classify critical, got %v", got)
	}
	if got := r.body["required_approvals"]; got != float64(2) {
		t.Fatalf("critical floor: required_approvals = %v, want 2", got)
	}
	id := r.body["id"].(string)

	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "pending" {
		t.Fatalf("one approver must NOT cross a critical threshold: %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "approved" {
		t.Fatalf("two distinct approvers must approve: %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
}

// The requester of a CRITICAL action can never be one of its two approvers
// (SoD, composed with the floor).
func TestCriticalSelfApprovalDenied(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")

	r := h.createApproval(a2, tenant, map[string]any{"action": "deploy.retire", "subject_kind": "deployment", "subject_ref": "prod-1"})
	if r.code != http.StatusCreated || r.body["required_approvals"] != float64(2) {
		t.Fatalf("create = %d %s required=%v", r.code, r.raw, r.body["required_approvals"])
	}
	id := r.body["id"].(string)
	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusForbidden {
		t.Fatalf("self-approval of a critical action must be 403 (SoD), got %d %s", rr.code, rr.raw)
	}
}

// A matching approval policy that sets only a threshold (no explicit tier)
// cannot lower a CRITICAL action below the floor — absence of classification
// never weakens dual-control (deny-closed).
func TestPolicyThresholdCannotLowerCriticalFloor(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createApprovalPolicy(admin, tenant, "lower", map[string]any{"required_approvals": 1, "match": map[string]any{"action": "deploy.apply"}})
	r := h.createApproval(admin, tenant, map[string]any{"action": "deploy.apply", "subject_kind": "deployment", "subject_ref": "prod-1"})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	if got := r.body["required_approvals"]; got != float64(2) {
		t.Fatalf("a tier-silent policy must not lower the critical floor: required=%v, want 2", got)
	}
	if got := r.body["risk_tier"]; got != "critical" {
		t.Fatalf("risk_tier = %v, want critical", got)
	}
}

// An EXPLICIT policy tier is the operator's audited word and reclassifies in
// both directions (the CRITICAL set is configurable by policy).
func TestPolicyExplicitTierReclassifies(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Downgrade: deploy.apply explicitly high → single approval suffices.
	h.createApprovalPolicy(admin, tenant, "downgrade", map[string]any{"risk_tier": "high", "required_approvals": 1, "match": map[string]any{"action": "deploy.apply"}})
	r := h.createApproval(admin, tenant, map[string]any{"action": "deploy.apply", "subject_kind": "deployment", "subject_ref": "prod-1"})
	if r.code != http.StatusCreated || r.body["required_approvals"] != float64(1) || r.body["risk_tier"] != "high" {
		t.Fatalf("explicit downgrade: %d %s required=%v tier=%v", r.code, r.raw, r.body["required_approvals"], r.body["risk_tier"])
	}

	// Upgrade: claude.tool.use explicitly critical → floored at 2.
	h.createApprovalPolicy(admin, tenant, "upgrade", map[string]any{"risk_tier": "critical", "match": map[string]any{"action": "claude.tool.use"}})
	r = h.createApproval(admin, tenant, map[string]any{"action": "claude.tool.use", "subject_kind": "claude.tool", "subject_ref": "Bash"})
	if r.code != http.StatusCreated || r.body["required_approvals"] != float64(2) || r.body["risk_tier"] != "critical" {
		t.Fatalf("explicit upgrade: %d %s required=%v tier=%v", r.code, r.raw, r.body["required_approvals"], r.body["risk_tier"])
	}
}

// Authoring guard: a policy cannot declare an action critical AND set a
// sub-floor threshold — the contradiction is rejected at authoring time.
func TestCriticalPolicyAuthoringGuard(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	r := h.do("POST", govPath+"/policies", admin, map[string]any{
		"name": "bad", "kind": "approval", "enabled": true,
		"spec": map[string]any{"risk_tier": "critical", "required_approvals": 1},
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("critical+required_approvals=1 must be rejected at authoring, got %d %s", r.code, r.raw)
	}
	r = h.do("POST", govPath+"/policies", admin, map[string]any{
		"name": "bad2", "kind": "approval", "enabled": true,
		"spec": map[string]any{"risk_tier": "extreme"},
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("an unknown risk_tier must be rejected, got %d %s", r.code, r.raw)
	}
}

// The tier is re-derived from the LIVE policy at decide time: an approval opened
// before its action was classified critical still demands two approvers once
// the classification lands (a stale snapshot can never hold the bar down).
func TestDecideTimeFloorTracksLivePolicy(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")

	// Opened as a default-high action: threshold 1.
	r := h.createApproval(editor, tenant, map[string]any{"action": "claude.tool.use", "subject_kind": "claude.tool", "subject_ref": "Bash"})
	if r.code != http.StatusCreated || r.body["required_approvals"] != float64(1) {
		t.Fatalf("create = %d %s required=%v", r.code, r.raw, r.body["required_approvals"])
	}
	id := r.body["id"].(string)

	// The operator then classifies the action critical.
	h.createApprovalPolicy(admin, tenant, "crit", map[string]any{"risk_tier": "critical", "match": map[string]any{"action": "claude.tool.use"}})

	// One approval no longer crosses: the decide-time floor re-derived 2.
	rr := h.decide(admin, tenant, id, "approve")
	if rr.code != http.StatusOK || rr.body["status"] != "pending" {
		t.Fatalf("decide under live critical tier must stay pending: %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
	if rr.body["required_approvals"] != float64(2) || rr.body["risk_tier"] != "critical" {
		t.Fatalf("decide must materialize the live floor: required=%v tier=%v", rr.body["required_approvals"], rr.body["risk_tier"])
	}
	if rr = h.decide(a2, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "approved" {
		t.Fatalf("second distinct approver crosses: %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
}
