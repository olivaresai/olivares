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
)

// the AAL3 step-up floor on CRITICAL human checkpoints. The harness
// operators are step-up-verified (harness stepUp); these tests mint sessions
// that deliberately SKIP the step-up and pin the machine-readable denial.

// loginNoStepUp creates a user with role in tenant and logs in WITHOUT the
// harness's automatic AAL3 elevation: a plain password session (AAL1).
func (h *harness) loginNoStepUp(admin string, tenant model.TenantID, email, role string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("grant %s = %d %s", email, r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, r.code, r.raw)
	}
	return r.body["token"].(string)
}

func stepUpDenied(t *testing.T, r resp) {
	t.Helper()
	if r.code != http.StatusForbidden {
		t.Fatalf("status = %d %s, want 403", r.code, r.raw)
	}
	if r.body["error"].(map[string]any)["code"] != "step_up_required" {
		t.Fatalf("error code = %s, want step_up_required", r.raw)
	}
}

// TestStepUpRequiredForCriticalDecision pins the engine floor: a CRITICAL
// decision from a session without a fresh AAL3 step-up is refused with
// step_up_required and leaves NO decision row — after the operator steps up,
// the SAME user's decision is accepted (it was never half-recorded).
func TestStepUpRequiredForCriticalDecision(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")

	// deploy.apply is CRITICAL by default classification.
	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy.apply", "subject_kind": "deployment", "subject_ref": "d-1"})
	if r.code != http.StatusCreated {
		t.Fatalf("create approval = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	low := h.loginNoStepUp(admin, tenant, "aal1@x.io", "admin")
	stepUpDenied(t, h.decide(low, tenant, id, "approve"))

	// The refusal left no decision row: the same user CAN decide after step-up
	// (a recorded decision would have tripped the duplicate-decider guard).
	h.stepUp(low)
	if r := h.decide(low, tenant, id, "approve"); r.code != http.StatusOK {
		t.Fatalf("decide after step-up = %d %s, want 200", r.code, r.raw)
	}

	// A user-bound API token carries NO human assurance (AAL 0): refused on a
	// CRITICAL decision even though it has a stable UserID.
	tok := h.do("POST", "/v1/tokens", admin, map[string]any{"name": "ci", "tenant": tenant.String(), "role": "admin"}, nil)
	if tok.code != http.StatusCreated {
		t.Fatalf("issue token = %d %s", tok.code, tok.raw)
	}
	stepUpDenied(t, h.decide(tok.body["token"].(string), tenant, id, "approve"))
}

// TestStepUpNotRequiredBelowCritical pins the floor's scope: a non-critical
// action accepts an AAL1 decision (the bar rises with the tier, it is not a
// blanket gate).
func TestStepUpNotRequiredBelowCritical(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")

	r := h.createApproval(editor, tenant, map[string]any{"action": "config.update", "subject_kind": "config", "subject_ref": "c-1"})
	if r.code != http.StatusCreated {
		t.Fatalf("create approval = %d %s", r.code, r.raw)
	}
	if r.body["risk_tier"] == "critical" {
		t.Fatalf("test premise broken: config.update classified critical (%s)", r.raw)
	}
	id := r.body["id"].(string)

	low := h.loginNoStepUp(admin, tenant, "aal1@x.io", "admin")
	if rr := h.decide(low, tenant, id, "approve"); rr.code != http.StatusOK {
		t.Fatalf("non-critical decide at AAL1 = %d %s, want 200", rr.code, rr.raw)
	}
}

// TestStepUpRequiredForBreakGlass pins the unconditional bar on the emergency
// path: activation from an AAL1 session is refused (step_up_required); the
// same admin activates after stepping up.
func TestStepUpRequiredForBreakGlass(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	low := h.loginNoStepUp(admin, tenant, "resp@x.io", "admin")
	stepUpDenied(t, h.activateBreakGlass(low, tenant, map[string]any{"reason": "incident at 03:00"}))

	h.stepUp(low)
	if r := h.activateBreakGlass(low, tenant, map[string]any{"reason": "incident at 03:00"}); r.code != http.StatusCreated {
		t.Fatalf("activation after step-up = %d %s, want 201", r.code, r.raw)
	}
}

// TestABACMinAALRule pins the policy predicate: a min_aal deny-rule
// matches only under-assured principals — AAL1 sessions and tokens (AAL 0) are
// denied, an AAL3 session passes, and min_aal alone is a valid selector.
func TestABACMinAALRule(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.authorPolicy(admin, tenant, "deploy-needs-aal3", map[string]any{"rules": []any{
		map[string]any{"deny": true, "permission": "deployment:admin", "min_aal": 3},
	}})
	eval := h.gov.Evaluator()

	mk := func(kind auth.PrincipalKind, aal int) auth.Request {
		return auth.Request{Principal: auth.Principal{Kind: kind, AAL: aal}, Permission: "deployment:admin", Tenant: tenant}
	}
	if d, _ := eval.Evaluate(context.Background(), mk(auth.KindUser, 1)); d.Allow {
		t.Fatal("an AAL1 session must be denied by the min_aal rule")
	}
	if d, _ := eval.Evaluate(context.Background(), mk(auth.KindToken, 0)); d.Allow {
		t.Fatal("a token (no human assurance) must be denied by the min_aal rule")
	}
	if d, err := eval.Evaluate(context.Background(), mk(auth.KindUser, 3)); err != nil || !d.Allow {
		t.Fatalf("an AAL3 session must pass the min_aal rule: allow=%v err=%v", d.Allow, err)
	}
	// Other permissions are untouched by the scoped rule.
	if d, _ := eval.Evaluate(context.Background(), auth.Request{
		Principal: auth.Principal{Kind: auth.KindUser, AAL: 1}, Permission: "agent:read", Tenant: tenant,
	}); !d.Allow {
		t.Fatal("an unscoped permission must not match the deploy min_aal rule")
	}

	// min_aal counts as a selector on its own (a tenant-wide assurance bar)...
	h2 := newHarness(t)
	admin2 := h2.adminLogin()
	tenant2 := h2.createOrg(admin2, "bcorp")
	h2.authorPolicy(admin2, tenant2, "everything-needs-aal2", map[string]any{"rules": []any{
		map[string]any{"deny": true, "min_aal": 2},
	}})
	// ...while a rule with no selector at all is still rejected at write time.
	if r := h2.do("POST", "/v1/m/governance/policies", admin2,
		map[string]any{"name": "bad", "kind": "abac", "enabled": true,
			"spec": map[string]any{"rules": []any{map[string]any{"deny": true}}}}, tenantHdr(tenant2)); r.code != http.StatusBadRequest {
		t.Fatalf("selector-less rule = %d %s, want 400", r.code, r.raw)
	}
	// Out-of-range min_aal is rejected.
	if r := h2.do("POST", "/v1/m/governance/policies", admin2,
		map[string]any{"name": "bad2", "kind": "abac", "enabled": true,
			"spec": map[string]any{"rules": []any{map[string]any{"deny": true, "min_aal": 7}}}}, tenantHdr(tenant2)); r.code != http.StatusBadRequest {
		t.Fatalf("min_aal out of range = %d %s, want 400", r.code, r.raw)
	}
}
