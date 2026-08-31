// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
)

// govPath is the module route prefix.
const govPath = "/v1/m/governance"

func (h *harness) createApproval(token string, tenant model.TenantID, body map[string]any) resp {
	h.t.Helper()
	return h.do("POST", govPath+"/approvals", token, body, tenantHdr(tenant))
}

func (h *harness) decide(token string, tenant model.TenantID, id, decision string) resp {
	h.t.Helper()
	return h.do("POST", govPath+"/approvals/"+id+"/decisions", token, map[string]any{"decision": decision}, tenantHdr(tenant))
}

func resolvedPayloads(t *testing.T, h *harness) []event.ApprovalResolution {
	t.Helper()
	evs := h.host.ofType(event.TypeApprovalResolved)
	out := make([]event.ApprovalResolution, 0, len(evs))
	for _, e := range evs {
		res, ok := event.ApprovalResolutionOf(e)
		if !ok {
			t.Fatalf("approval.resolved payload has type %T", e.Payload)
		}
		out = append(out, res)
	}
	return out
}

func TestApprovalSingleApprovalLifecycle(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "editor@x.io", "editor")

	r := h.createApproval(editor, tenant, map[string]any{"subject_kind": "deployment", "subject_ref": "deploy-1", "action": "deploy"})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r.body["status"] != "pending" {
		t.Fatalf("new request should be pending, got %v", r.body["status"])
	}
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "approved" {
		t.Fatalf("approve = %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
	resolved := resolvedPayloads(t, h)
	if len(resolved) != 1 {
		t.Fatalf("approval.resolved events = %d, want 1", len(resolved))
	}
	if got := resolved[0]; got.ApprovalID != id || got.Outcome != "approved" || got.ApproveCount != 1 || got.RejectCount != 0 || got.RequiredApprovals != 1 {
		t.Fatalf("approval.resolved payload = %+v", got)
	}
	if resolved[0].DecidedAt.IsZero() {
		t.Fatal("approval.resolved DecidedAt should be set")
	}
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusConflict {
		t.Fatalf("duplicate decision after terminal status must be 409, got %d %s", rr.code, rr.raw)
	}
	if got := len(h.host.ofType(event.TypeApprovalResolved)); got != 1 {
		t.Fatalf("terminal duplicate decision must not emit, got %d approval.resolved events", got)
	}
}

func TestApprovalSeparationOfDuty(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// A second admin who both requests and tries to decide their own request.
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")
	r := h.createApproval(a2, tenant, map[string]any{"action": "deploy"})
	id := r.body["id"].(string)
	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusForbidden {
		t.Fatalf("requester deciding own request must be 403 (SoD), got %d %s", rr.code, rr.raw)
	}
	// A different admin can decide it.
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusOK {
		t.Fatalf("a different admin should decide: %d %s", rr.code, rr.raw)
	}
}

func TestApprovalMultiApprovalAndDuplicateDecider(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")
	_, a3 := h.roleUser(admin, tenant, "a3@x.io", "admin")

	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "required_approvals": 2})
	id := r.body["id"].(string)
	if got := r.body["required_approvals"].(float64); got != 2 {
		t.Fatalf("required_approvals = %v, want 2", got)
	}
	// First approval keeps it pending.
	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "pending" {
		t.Fatalf("first of two approvals should stay pending: %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
	// Same admin deciding again is rejected (duplicate decider keyed on user id).
	if rr := h.decide(a2, tenant, id, "approve"); rr.code != http.StatusConflict {
		t.Fatalf("duplicate decider must be 409, got %d %s", rr.code, rr.raw)
	}
	// A different admin crosses the threshold.
	if rr := h.decide(a3, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "approved" {
		t.Fatalf("second distinct approval should approve: %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
}

func TestApprovalRejection(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "required_approvals": 2})
	id := r.body["id"].(string)
	if rr := h.decide(admin, tenant, id, "reject"); rr.code != http.StatusOK || rr.body["status"] != "rejected" {
		t.Fatalf("a single rejection should reject: %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
	resolved := resolvedPayloads(t, h)
	if len(resolved) != 1 {
		t.Fatalf("approval.resolved events = %d, want 1", len(resolved))
	}
	if got := resolved[0]; got.ApprovalID != id || got.Outcome != "rejected" || got.ApproveCount != 0 || got.RejectCount != 1 {
		t.Fatalf("approval.resolved rejection payload = %+v", got)
	}
}

func TestApprovalLazyExpiryBlocksDecision(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "expires_in_seconds": 60})
	id := r.body["id"].(string)

	h.clk.advance(61 * time.Second)
	// Read reports the EFFECTIVE status (expired) even before a sweep persists it.
	if rr := h.do("GET", govPath+"/approvals/"+id, admin, nil, tenantHdr(tenant)); rr.body["status"] != "expired" {
		t.Fatalf("lazy expiry should report expired, got %v", rr.body["status"])
	}
	// A decision on a logically-expired request is refused.
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusConflict {
		t.Fatalf("deciding an expired request must be 409, got %d %s", rr.code, rr.raw)
	}
}

func TestApprovalSweepEscalatesThenExpiresOnce(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "escalate_in_seconds": 30, "expires_in_seconds": 60})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	sweep := func() resp { return h.do("POST", govPath+"/approvals/sweep", admin, nil, tenantHdr(tenant)) }

	h.clk.advance(31 * time.Second) // past escalate, before expiry
	if rr := sweep(); rr.body["escalated"].(float64) != 1 || rr.body["expired"].(float64) != 0 {
		t.Fatalf("first sweep should escalate 1, expire 0: %s", rr.raw)
	}
	// A repeated sweep at the same time must NOT re-escalate (gated on escalated_at).
	if rr := sweep(); rr.body["escalated"].(float64) != 0 {
		t.Fatalf("repeated sweep must not double-escalate: %s", rr.raw)
	}
	h.clk.advance(30 * time.Second) // now past expiry
	if rr := sweep(); rr.body["expired"].(float64) != 1 {
		t.Fatalf("sweep past expiry should expire 1: %s", rr.raw)
	}
	// The now-expired request leaves the pending set; a further sweep is a no-op.
	if rr := sweep(); rr.body["scanned"].(float64) != 0 {
		t.Fatalf("expired request must not be re-scanned: %s", rr.raw)
	}

	fs := h.host.findings()
	esc, exp := 0, 0
	for _, f := range fs {
		switch f.Kind {
		case "governance_approval_escalated":
			esc++
		case "governance_approval_expired":
			exp++
		}
	}
	if esc != 1 || exp != 1 {
		t.Fatalf("expected exactly one escalation and one expiry finding, got esc=%d exp=%d", esc, exp)
	}
	resolved := resolvedPayloads(t, h)
	if len(resolved) != 1 || resolved[0].Outcome != "expired" || resolved[0].ApprovalID != r.body["id"].(string) {
		t.Fatalf("sweep must emit exactly one expired approval.resolved, got %+v", resolved)
	}
}

func TestApprovalSweepEscalationOnlyDoesNotResolve(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "escalate_in_seconds": 30, "expires_in_seconds": 60})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}

	h.clk.advance(31 * time.Second)
	rr := h.do("POST", govPath+"/approvals/sweep", admin, nil, tenantHdr(tenant))
	if rr.code != http.StatusOK || rr.body["escalated"].(float64) != 1 || rr.body["expired"].(float64) != 0 {
		t.Fatalf("sweep should escalate only: %d %s", rr.code, rr.raw)
	}
	if got := len(h.host.ofType(event.TypeApprovalResolved)); got != 0 {
		t.Fatalf("escalation-only sweep must not emit approval.resolved, got %d", got)
	}
}

func TestApprovalPolicyIsAuthoritativeOverRequest(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// An approval policy requiring two approvals for "deploy" actions.
	if r := h.do("POST", govPath+"/policies", admin, map[string]any{
		"name": "deploys-need-two", "kind": "approval", "enabled": true,
		"spec": map[string]any{"required_approvals": 2, "match": map[string]any{"action": "deploy"}},
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("author approval policy = %d %s", r.code, r.raw)
	}
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	// The request tries to lower the bar to 1; the policy wins.
	r := h.createApproval(editor, tenant, map[string]any{"action": "deploy", "required_approvals": 1})
	if got := r.body["required_approvals"].(float64); got != 2 {
		t.Fatalf("policy should force required_approvals=2, got %v", got)
	}
	if r.body["policy_ref"] == "" || r.body["policy_ref"] == nil {
		t.Fatal("the matched policy should be recorded on the request")
	}
}

func TestApprovalDecisionTrail(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	id := h.createApproval(editor, tenant, map[string]any{"action": "deploy"}).body["id"].(string)
	if rr := h.do("POST", govPath+"/approvals/"+id+"/decisions", admin, map[string]any{"decision": "approve", "note": "looks fine"}, tenantHdr(tenant)); rr.code != http.StatusOK {
		t.Fatalf("decide = %d %s", rr.code, rr.raw)
	}
	r := h.do("GET", govPath+"/approvals/"+id+"/decisions", admin, nil, tenantHdr(tenant))
	trail := items(r)
	if len(trail) != 1 {
		t.Fatalf("decision trail should have one entry, got %d (%s)", len(trail), r.raw)
	}
	entry := trail[0].(map[string]any)
	if entry["decision"] != "approve" || entry["note"] != "looks fine" {
		t.Fatalf("trail entry wrong: %v", entry)
	}
	if d, _ := entry["decider"].(string); len(d) < 5 || d[:5] != "user:" {
		t.Fatalf("decider should be a user actor id, got %q", entry["decider"])
	}
}

func TestApprovalRejectsCredentialInAction(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	// action/subject_kind ride the immutable audit ledger Meta; a credential-shaped
	// value must be refused (docs/SECURITY-HARDENING.md), never sealed into the chain.
	if r := h.createApproval(editor, tenant, map[string]any{"action": "api_key=sk-leak", "subject_ref": "x"}); r.code != http.StatusBadRequest {
		t.Fatalf("a credential in action must be rejected, got %d %s", r.code, r.raw)
	}
}

func TestApprovalCancel(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	uid, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_ = uid
	id := h.createApproval(editor, tenant, map[string]any{"action": "deploy"}).body["id"].(string)
	// The requester can cancel their own pending request.
	if rr := h.do("POST", govPath+"/approvals/"+id+"/cancel", editor, nil, tenantHdr(tenant)); rr.code != http.StatusOK || rr.body["status"] != "canceled" {
		t.Fatalf("requester cancel = %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
	resolved := resolvedPayloads(t, h)
	if len(resolved) != 1 {
		t.Fatalf("approval.resolved events = %d, want 1", len(resolved))
	}
	if got := resolved[0]; got.ApprovalID != id || got.Outcome != "canceled" {
		t.Fatalf("approval.resolved cancel payload = %+v", got)
	}
	// A decision on a canceled request is refused.
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusConflict {
		t.Fatalf("deciding a canceled request must be 409, got %d %s", rr.code, rr.raw)
	}
}

func TestApprovalResolvedPayloadIsMinimalData(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	r := h.createApproval(editor, tenant, map[string]any{
		"subject_kind": "deployment", "subject_ref": "deploy-7", "action": "deploy",
		"reason": "ship the hotfix",
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create approval = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	rr := h.do("POST", govPath+"/approvals/"+id+"/decisions", admin, map[string]any{
		"decision": "approve", "note": "looks good",
	}, tenantHdr(tenant))
	if rr.code != http.StatusOK {
		t.Fatalf("decide = %d %s", rr.code, rr.raw)
	}

	resolved := resolvedPayloads(t, h)
	if len(resolved) != 1 {
		t.Fatalf("approval.resolved events = %d, want 1", len(resolved))
	}
	wire, err := json.Marshal(resolved[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"ship the hotfix", "looks good", "deploy-7", "SubjectRef", "Reason", "Note"} {
		if strings.Contains(string(wire), leak) {
			t.Fatalf("approval.resolved wire payload leaks %q: %s", leak, wire)
		}
	}
}
