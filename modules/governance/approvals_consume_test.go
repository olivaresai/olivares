// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// approvals_consume_test.go pins the (F-02) single-use CONSUME of an approved
// request: the first caller spends it, the SAME caller re-obtains it idempotently
// (result-idempotency for a transport retry), and any OTHER caller is a would-replay
// DENY with a signed-ledger event + finding. Every non-approved state is deny-closed.

func (h *harness) consume(token string, tenant model.TenantID, id, consumer string) resp {
	h.t.Helper()
	return h.do("POST", govPath+"/approvals/"+id+"/consume", token,
		map[string]any{"consumer_id": consumer}, tenantHdr(tenant))
}

// approveHighAction opens a HIGH (single-approval) approval and approves it, returning
// its id. "deploy" is not in the CRITICAL default set, so one approver releases it.
func (h *harness) approveHighAction(t *testing.T, admin, editor string, tenant model.TenantID) string {
	t.Helper()
	r := h.createApproval(editor, tenant, map[string]any{
		"subject_kind": "claude.tool", "subject_ref": "Bash#plan=abc", "action": "deploy",
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "approved" {
		t.Fatalf("approve = %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}
	return id
}

func TestApprovalConsumeIsSingleUse(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "editor@x.io", "editor")
	id := h.approveHighAction(t, admin, editor, tenant)

	// FIRST consume by tool_use_id A → granted, recorded as consumed_by A.
	r := h.consume(editor, tenant, id, "toolu_A")
	if r.code != http.StatusOK || r.body["granted"] != true || r.body["replay"] == true {
		t.Fatalf("first consume must grant: %d %s", r.code, r.raw)
	}
	if r.body["consumed_by"] != "toolu_A" {
		t.Fatalf("first consume must record the consumer, got %v", r.body["consumed_by"])
	}

	// SAME caller re-consumes → idempotent grant (a transport retry re-obtains the grant;
	// it does NOT re-authorize). No would-replay.
	r = h.consume(editor, tenant, id, "toolu_A")
	if r.code != http.StatusOK || r.body["granted"] != true || r.body["replay"] == true {
		t.Fatalf("idempotent re-consume by the same caller must grant: %d %s", r.code, r.raw)
	}

	// DIFFERENT caller → would-replay DENY (not granted, replay=true).
	r = h.consume(editor, tenant, id, "toolu_B")
	if r.code != http.StatusOK || r.body["granted"] != false || r.body["replay"] != true {
		t.Fatalf("a DIFFERENT caller must be denied would-replay: %d %s", r.code, r.raw)
	}

	// The would-replay left a signed-ledger finding (governance_approval_replay_denied).
	var sawReplayFinding bool
	for _, f := range h.host.findings() {
		if f.Kind == "governance_approval_replay_denied" && f.SubjectRef == id {
			sawReplayFinding = true
		}
	}
	if !sawReplayFinding {
		t.Fatal("a would-replay must emit a governance_approval_replay_denied finding")
	}
}

// TestApprovalConsumeIdempotencyWindowBounded pins F-02's bounded result-idempotency:
// a re-consume by the SAME caller is idempotent ONLY within the short transport-retry
// window; past it the single-use approval is SPENT and even the same caller is a
// would-replay DENY — so one approval cannot re-authorize a DEFERRED re-execution hours
// later. RED before the fix (the idempotency branch granted regardless of elapsed time),
// GREEN after.
func TestApprovalConsumeIdempotencyWindowBounded(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "editor@x.io", "editor")
	id := h.approveHighAction(t, admin, editor, tenant)

	// First consume binds consumed_at = now.
	if r := h.consume(editor, tenant, id, "toolu_A"); r.code != http.StatusOK || r.body["granted"] != true {
		t.Fatalf("first consume must grant: %d %s", r.code, r.raw)
	}
	// A re-consume by the SAME caller WITHIN the window is an idempotent transport retry.
	h.clk.advance(30 * time.Second)
	if r := h.consume(editor, tenant, id, "toolu_A"); r.code != http.StatusOK || r.body["granted"] != true || r.body["replay"] == true {
		t.Fatalf("same-caller re-consume within the retry window must grant idempotently: %d %s", r.code, r.raw)
	}
	// PAST the window, the approval is spent: even the SAME caller is a would-replay DENY.
	h.clk.advance(5 * time.Minute)
	r := h.consume(editor, tenant, id, "toolu_A")
	if r.code != http.StatusOK || r.body["granted"] != false || r.body["replay"] != true {
		t.Fatalf("a re-consume PAST the retry window must be denied would-replay (a deferred "+
			"re-execution under one approval is exactly what F-02 prevents): %d %s", r.code, r.raw)
	}
}

// TestApprovalConsumeRefusesStaleApprovedGrant pins F5: effectiveStatus stops applying
// the time-box once a request is stored-approved, so without a consume-time re-anchor a
// stale-but-approved row could still be spent long after its window. A direct consume must
// honor the outer time-box. RED before the fix (a stale approved grant was spendable),
// GREEN after.
func TestApprovalConsumeRefusesStaleApprovedGrant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "editor@x.io", "editor")

	// A time-boxed approval (1h expiry), approved immediately.
	r := h.createApproval(editor, tenant, map[string]any{
		"subject_kind": "claude.tool", "subject_ref": "Bash#plan=stale", "action": "deploy",
		"expires_in_seconds": 3600,
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if rr := h.decide(admin, tenant, id, "approve"); rr.code != http.StatusOK || rr.body["status"] != "approved" {
		t.Fatalf("approve = %d %s status=%v", rr.code, rr.raw, rr.body["status"])
	}

	// Advance PAST the time-box. The stored status stays "approved" (effectiveStatus only
	// expires PENDING), so the F5 consume-time re-anchor is what refuses the stale grant.
	h.clk.advance(2 * time.Hour)
	rr := h.consume(editor, tenant, id, "toolu_A")
	if rr.code != http.StatusOK || rr.body["granted"] != false {
		t.Fatalf("a stale-but-approved grant past its time-box must not be spendable: %d %s", rr.code, rr.raw)
	}
	if rr.body["status"] != "expired" {
		t.Fatalf("the refusal must reflect the expired time-box, got %v", rr.body["status"])
	}
}

func TestApprovalConsumeDeniesNonApproved(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "editor@x.io", "editor")

	// A PENDING (undecided) approval cannot be consumed — deny-closed, never a replay.
	r := h.createApproval(editor, tenant, map[string]any{"subject_kind": "claude.tool", "subject_ref": "Bash", "action": "deploy"})
	id := r.body["id"].(string)
	rr := h.consume(editor, tenant, id, "toolu_A")
	if rr.code != http.StatusOK || rr.body["granted"] != false || rr.body["replay"] == true {
		t.Fatalf("consuming a pending approval must deny-closed (not a replay): %d %s", rr.code, rr.raw)
	}
	if rr.body["status"] != "pending" {
		t.Fatalf("status must reflect the un-spendable state, got %v", rr.body["status"])
	}

	// An empty consumer_id is rejected (single-use cannot be keyed without it).
	if bad := h.consume(editor, tenant, id, ""); bad.code != http.StatusBadRequest {
		t.Fatalf("empty consumer_id must be 400, got %d %s", bad.code, bad.raw)
	}
}
