// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestApplyDeniedByDefaultWithoutGate proves the core security property: with NO
// approval gate wired, a governed mutation is DENIED. Phase 1 reports no_gate;
// phase 2 is blocked; the executor is never invoked.
func TestApplyDeniedByDefaultWithoutGate(t *testing.T) {
	ex := newMockExecutor()
	h := newHarnessWith(t, WithExecutor(ex), WithIdentityBinder(&fakeBinder{firm: true})) // no gate => deny-closed
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")

	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	p1 := h.applyPhase1(tok, tid, defID)
	if p1.code != http.StatusAccepted {
		t.Fatalf("apply phase1 = %d %s", p1.code, p1.raw)
	}
	if got := p1.body["gate_status"]; got != string(StatusNoGate) {
		t.Fatalf("gate_status = %v, want no_gate", got)
	}
	ref := p1.body["approval_ref"].(string)

	p2 := h.applyPhase2(tok, tid, defID, ref)
	if p2.code != http.StatusForbidden {
		t.Fatalf("apply phase2 = %d %s, want 403", p2.code, p2.raw)
	}
	if ex.applyCalls != 0 {
		t.Fatalf("executor.Apply was called %d times; a denied mutation must NEVER reach the runtime", ex.applyCalls)
	}
}

// TestApplyBlocksUntilApproved proves a governed mutation blocks while the HITL
// approval is pending and proceeds once approved — and that the approved apply
// publishes the declared PERMITTED wiring as a SignalPolicy edge.
func TestApplyBlocksUntilApproved(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	p1 := h.applyPhase1(tok, tid, defID)
	if p1.code != http.StatusAccepted || p1.body["gate_status"] != string(StatusPending) {
		t.Fatalf("apply phase1 = %d %s (gate=%v)", p1.code, p1.raw, p1.body["gate_status"])
	}
	ref := p1.body["approval_ref"].(string)

	// Pending => denied.
	if p2 := h.applyPhase2(tok, tid, defID, ref); p2.code != http.StatusForbidden {
		t.Fatalf("apply while pending = %d %s, want 403", p2.code, p2.raw)
	}
	if h.exec.applyCalls != 0 {
		t.Fatalf("executor.Apply called %d times while pending", h.exec.applyCalls)
	}

	// Human approves => proceeds.
	h.gate.set(ref, StatusApproved)
	p2 := h.applyPhase2(tok, tid, defID, ref)
	if p2.code != http.StatusOK || p2.body["status"] != opStatusApplied {
		t.Fatalf("apply after approval = %d %s (status=%v)", p2.code, p2.raw, p2.body["status"])
	}
	if h.exec.applyCalls != 1 {
		t.Fatalf("executor.Apply called %d times, want 1", h.exec.applyCalls)
	}

	// The declared PERMITTED wiring is published as a SignalPolicy edge.
	edges := h.capturedEdges(1)
	if len(edges) != 1 {
		t.Fatalf("captured %d permitted edges, want 1", len(edges))
	}
	e := edges[0]
	if e.Source != sdkmodel.SignalPolicy {
		t.Fatalf("edge source = %q, want policy (the permitted-grant feed reconciles)", e.Source)
	}
	if e.OriginKind != "identity" || e.OriginRef != "agent:billing" {
		t.Fatalf("edge origin = %s/%s, want identity/agent:billing (firm per-agent attribution)", e.OriginKind, e.OriginRef)
	}
	if e.ResourceRef != "public.customers" || e.Mode != sdkmodel.ModeReadWrite {
		t.Fatalf("edge resource = %s mode %s, want public.customers/readwrite", e.ResourceRef, e.Mode)
	}
	if e.Confidence != sdkmodel.ConfidenceAttributed {
		t.Fatalf("edge confidence = %q, want attributed (firm binding)", e.Confidence)
	}
}

// TestApplyDeniedWhenApprovalExpired proves deny-by-default at expiry: an
// approval that lapses before phase 2 blocks the mutation.
func TestApplyDeniedWhenApprovalExpired(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	ref := h.applyPhase1(tok, tid, defID).body["approval_ref"].(string)
	h.gate.set(ref, StatusExpired)
	if p2 := h.applyPhase2(tok, tid, defID, ref); p2.code != http.StatusForbidden {
		t.Fatalf("apply with expired approval = %d %s, want 403", p2.code, p2.raw)
	}
	if h.exec.applyCalls != 0 {
		t.Fatalf("executor.Apply called %d times with an expired approval", h.exec.applyCalls)
	}
}

// TestApplyRejectsStalePlanAfterRespec proves the anti-TOCTOU binding: an
// approval bound to one plan cannot authorize a different plan. After the spec is
// updated, the old approval's plan hash no longer matches, so apply is denied.
func TestApplyRejectsStalePlanAfterRespec(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	ref := h.applyPhase1(tok, tid, defID).body["approval_ref"].(string)
	h.gate.set(ref, StatusApproved)

	// Re-spec the deployment (new revision) BEFORE applying — the approved plan is
	// now stale.
	if r := h.do("PUT", "/v1/m/deploy/definitions/"+defID, tok, map[string]any{"spec": agentSpec("img:2", "agent:billing")}, tenantHdr(tid)); r.code != http.StatusOK {
		t.Fatalf("update = %d %s", r.code, r.raw)
	}
	if p2 := h.applyPhase2(tok, tid, defID, ref); p2.code != http.StatusForbidden {
		t.Fatalf("apply with stale approval = %d %s, want 403 (plan hash mismatch)", p2.code, p2.raw)
	}
	if h.exec.applyCalls != 0 {
		t.Fatalf("executor.Apply ran against a stale (unapproved) plan")
	}
}

// TestPlanIsDryRun proves /plan mutates nothing: it returns the diff but never
// invokes the executor's Apply.
func TestPlanIsDryRun(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	r := h.do("POST", "/v1/m/deploy/definitions/"+defID+"/plan", tok, nil, tenantHdr(tid))
	if r.code != http.StatusOK {
		t.Fatalf("plan = %d %s", r.code, r.raw)
	}
	if r.body["up_to_date"] != false {
		t.Fatalf("plan up_to_date = %v, want false (never applied)", r.body["up_to_date"])
	}
	if h.exec.applyCalls != 0 {
		t.Fatalf("plan invoked executor.Apply %d times; plan must be a dry-run", h.exec.applyCalls)
	}
}

// TestApplyIsIdempotent proves re-applying an unchanged spec is a no-op that needs
// no approval and changes nothing on the runtime.
func TestApplyIsIdempotent(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	ref := h.applyPhase1(tok, tid, defID).body["approval_ref"].(string)
	h.gate.set(ref, StatusApproved)
	if p2 := h.applyPhase2(tok, tid, defID, ref); p2.code != http.StatusOK {
		t.Fatalf("first apply = %d %s", p2.code, p2.raw)
	}
	plan := h.do("POST", "/v1/m/deploy/definitions/"+defID+"/plan", tok, nil, tenantHdr(tid))
	changes, changesAreArray := plan.body["changes"].([]any)
	if plan.code != http.StatusOK || plan.body["up_to_date"] != true ||
		!changesAreArray || len(changes) != 0 {
		t.Fatalf("in-sync plan collections = %d %s", plan.code, plan.raw)
	}
	verify := h.do("POST", "/v1/m/deploy/definitions/"+defID+"/verify", tok, nil, tenantHdr(tid))
	drift, driftIsArray := verify.body["drift"].([]any)
	if verify.code != http.StatusOK || verify.body["in_sync"] != true ||
		!driftIsArray || len(drift) != 0 {
		t.Fatalf("in-sync verify collections = %d %s", verify.code, verify.raw)
	}

	// Re-apply with no approval reference: the spec is already in effect, so it is
	// an idempotent no-op (no new approval, no executor.Apply).
	again := h.applyPhase1(tok, tid, defID)
	if again.code != http.StatusOK || again.body["status"] != opStatusNoop {
		t.Fatalf("re-apply = %d %s (status=%v), want 200 noop", again.code, again.raw, again.body["status"])
	}
	if h.exec.applyCalls != 1 {
		t.Fatalf("executor.Apply called %d times across two applies of the same spec, want 1 (idempotent)", h.exec.applyCalls)
	}
}

// TestRollbackRestoresPriorVersion proves rollback + apply restores a prior
// known-good spec on the runtime.
func TestRollbackRestoresPriorVersion(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	apply := func() {
		ref := h.applyPhase1(tok, tid, defID).body["approval_ref"].(string)
		h.gate.set(ref, StatusApproved)
		if p2 := h.applyPhase2(tok, tid, defID, ref); p2.code != http.StatusOK {
			t.Fatalf("apply = %d %s", p2.code, p2.raw)
		}
	}
	apply() // v1 (img:1) applied

	req := h.execReqFor(defID, tid)
	v1Hash := h.exec.appliedHash(req)

	// Update to v2 (img:2) and apply.
	if r := h.do("PUT", "/v1/m/deploy/definitions/"+defID, tok, map[string]any{"spec": agentSpec("img:2", "agent:billing")}, tenantHdr(tid)); r.code != http.StatusOK {
		t.Fatalf("update = %d %s", r.code, r.raw)
	}
	apply() // v2 applied
	if h.exec.appliedHash(req) == v1Hash {
		t.Fatalf("v2 apply did not change the runtime state")
	}

	// Roll back to v1 (creates v3 = spec(v1)) and apply.
	if r := h.do("POST", "/v1/m/deploy/definitions/"+defID+"/rollback", tok, map[string]any{"to_version": 1}, tenantHdr(tid)); r.code != http.StatusOK {
		t.Fatalf("rollback = %d %s", r.code, r.raw)
	}
	apply() // v3 (== v1 spec) applied
	if got := h.exec.appliedHash(req); got != v1Hash {
		t.Fatalf("after rollback+apply runtime hash = %q, want the v1 hash %q", got, v1Hash)
	}
}

// execReqFor reads a definition's target/subject from the store so a test can
// inspect the mock executor's applied state for it.
func (h *harness) execReqFor(defID string, tenant model.TenantID) ExecRequest {
	h.t.Helper()
	var req ExecRequest
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(definitionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(defID))
		if err != nil {
			return err
		}
		req = ExecRequest{Tenant: tenant, Target: rec.String(colTarget), Runtime: rec.String(colRuntime), SubjectKind: rec.String(colSubjectKind), SubjectRef: rec.String(colSubjectRef)}
		return nil
	}); err != nil {
		h.t.Fatal(err)
	}
	return req
}
