// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// s467_review_test.go — exploit/regression tests for the adversarial-review
// findings (the model max): receipt-not-enforced (c3), run-creation
// replay (deferral #1), notify approval-binding + atomic delivery (c1/#6),
// settle integrity (c4), and unknown-vs-failed (c5).

// ambiguousDispatcher returns the post-transmit ambiguous sentinel (item 8).
type ambiguousDispatcher struct{}

func (ambiguousDispatcher) Fire(context.Context, FireRequest) (DispatchResult, error) {
	return DispatchResult{}, ErrDispatchAmbiguous
}

// mutableGen is a DispatcherGeneration a test can flip mid-run, modeling an
// operator config reload that re-points a subject to a new image/command/URL.
type mutableGen struct {
	mu  sync.Mutex
	gen string
}

func (g *mutableGen) Generation(string, string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gen
}

func (g *mutableGen) set(v string) {
	g.mu.Lock()
	g.gen = v
	g.mu.Unlock()
}

// listOps reads the operation ledger for a tenant (white-box).
func (h *harness) listOps(tenant model.TenantID) []model.Record {
	h.t.Helper()
	var recs []model.Record
	if err := api.NewScopedData(h.st, tenant).View(context.Background(), func(sc store.Scope) error {
		repo, err := sc.Ext(operationKind)
		if err != nil {
			return err
		}
		rs, _, lerr := repo.List(context.Background(), model.Query{Limit: 200})
		recs = rs
		return lerr
	}); err != nil {
		h.t.Fatalf("list operations: %v", err)
	}
	return recs
}

// opBySurface returns the first operation row of a given surface.
func opBySurface(ops []model.Record, surface string) (model.Record, bool) {
	for _, o := range ops {
		if o.String(colOpSurface) == surface {
			return o, true
		}
	}
	return nil, false
}

// --------------------------------------------------------------------------
// c5 / item 8: an ambiguous dispatch is recorded UNKNOWN, never "failed".
// --------------------------------------------------------------------------

func TestS467ReviewAmbiguousDispatchRecordsUnknown(t *testing.T) {
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved}), WithDispatcher(ambiguousDispatcher{}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusBadGateway {
		t.Fatalf("ambiguous dispatch = %d %s, want 502", r.code, r.raw)
	}
	op, ok := opBySurface(h.listOps(tenant), surfaceScheduleFire)
	if !ok {
		t.Fatal("no fire operation recorded")
	}
	if got := op.String(colOpState); got != opStateUnknown {
		t.Fatalf("operation state = %q, want %q (an ambiguous post-transmit error must not be a definitive failure)", got, opStateUnknown)
	}
}

// --------------------------------------------------------------------------
// item 1: a same-OperationID / different-effect reuse records sdk.FailureReplay.
// The effect digest binds tenant + policy version, so it is a real rebind guard.
// --------------------------------------------------------------------------

func TestS467ReviewEffectDigestBindsTenant(t *testing.T) {
	h, m := newHarness(t)
	admin := h.adminLogin()
	tA := h.createOrg(admin, "acme")
	tB := h.createOrg(admin, "beta")
	base := operationSpec{
		approvalRef: "appr-1", surface: surfaceScheduleFire, action: surfaceScheduleFire,
		planHash: "plan", policyVersion: "approved", bindProfile: bindingProfileV1, targetFp: "fp",
	}
	specA := base
	specA.tenant = tA.String()
	specB := base
	specB.tenant = tB.String()
	if m.effectDigest(specA) == m.effectDigest(specB) {
		t.Fatal("effect digest must differ by tenant (full-binding contract)")
	}
	// And a changed policy version changes the digest.
	specA2 := specA
	specA2.policyVersion = "break_glass"
	if m.effectDigest(specA) == m.effectDigest(specA2) {
		t.Fatal("effect digest must differ by policy version")
	}
}

// settleOperation must treat a missing claim row as an INTEGRITY error (c4).
func TestS467ReviewSettleMissingOperationIsError(t *testing.T) {
	h, m := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	claim := &operationClaim{binding: sdk.EvidenceBinding{OperationID: sdk.OperationID("op-does-not-exist"), EffectDigest: "d"}}
	err := api.NewScopedData(h.st, tenant).Mutate(context.Background(), func(sc store.Scope) error {
		return m.settleOperation(context.Background(), sc, claim, opStateDispatched, obStateDispatched, "", "x")
	})
	if err == nil {
		t.Fatal("settling a missing operation row must be an integrity error, not a silent success")
	}
}

// --------------------------------------------------------------------------
// deferral #1 / item 3: run creation claims a durable run-level operation with
// UNIQUE(approval_ref) in the SAME tx as the run row (closes the Postgres race);
// a second phase-2 with the same approval refuses without a second run.
// --------------------------------------------------------------------------

func TestS467ReviewRunCreationClaimsOperation(t *testing.T) {
	g := newRoutedGate()
	h, _ := newHarness(t, WithApprovalGate(g))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wf := h.createWorkflow(admin, tenant, "once", []map[string]any{emitStep("a")})
	id := wf["id"].(string)

	r1 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, nil, tenantHdr(tenant))
	ref := r1.body["approval_ref"].(string)
	g.set(ref, StatusApproved)
	r2 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, map[string]any{"approval_ref": ref}, tenantHdr(tenant))
	if r2.code != http.StatusOK {
		t.Fatalf("phase 2 = %d %s", r2.code, r2.raw)
	}
	// A durable run-level operation now enforces single-use (UNIQUE approval_ref).
	if _, ok := opBySurface(h.listOps(tenant), surfaceWorkflowRun); !ok {
		t.Fatal("run creation must claim a durable run-level operation (the atomic single-use guard)")
	}
	// Re-POST the same approval → refused, and still exactly one run.
	r3 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, map[string]any{"approval_ref": ref}, tenantHdr(tenant))
	if r3.code != http.StatusConflict {
		t.Fatalf("re-run with a spent approval = %d %s, want 409", r3.code, r3.raw)
	}
	runs := h.workflowRuns(tenant, id)
	if runs != 1 {
		t.Fatalf("run count = %d, want exactly 1", runs)
	}
}

func (h *harness) workflowRuns(tenant model.TenantID, wfID string) int {
	h.t.Helper()
	n := 0
	if err := api.NewScopedData(h.st, tenant).View(context.Background(), func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rs, _, lerr := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq(colWrWorkflow, wfID)}, Limit: 200})
		n = len(rs)
		return lerr
	}); err != nil {
		h.t.Fatalf("list runs: %v", err)
	}
	return n
}

// --------------------------------------------------------------------------
// hole c1 / item 5a: the notify DESTINATION is in the plan hash, so a route
// re-pointed between phase 1 and phase 2 voids the approval (no race needed).
// --------------------------------------------------------------------------

func TestS467ReviewNotifyRetargetPhase1To2VoidsApproval(t *testing.T) {
	g := newRoutedGate()
	nt := &recordingNotifyTester{name: "ops", fingerprint: "slack:webhook-A", routeExists: true}
	h, _ := newHarness(t, WithApprovalGate(g), WithNotifyTester(nt))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	routeID := "019f0000-0000-7000-8000-00000000abcd"
	wf := h.createWorkflow(admin, tenant, "notify-wf", []map[string]any{
		step("notify", "notify-test", map[string]any{"route_id": routeID}),
	})
	id := wf["id"].(string)

	r1 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, nil, tenantHdr(tenant))
	ref := r1.body["approval_ref"].(string)
	g.set(ref, StatusApproved)
	// Re-point the route BETWEEN phase 1 and phase 2 (a write-tier change).
	nt.setFingerprint("slack:webhook-B")
	r2 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, map[string]any{"approval_ref": ref}, tenantHdr(tenant))
	if r2.code != http.StatusForbidden {
		t.Fatalf("phase 2 after route re-point = %d %s, want 403 (the approved plan no longer describes the destination)", r2.code, r2.raw)
	}
	if nt.calls() != 0 {
		t.Fatal("the re-pointed route was actuated under the old approval")
	}
}

// --------------------------------------------------------------------------
// hole c1 / item 5b: even if the route matches at the pre-check, delivery is
// ATOMIC — a route re-pointed between the check and the send blocks, never
// delivering to the new destination.
// --------------------------------------------------------------------------

func TestS467ReviewNotifyDeliveryIsAtomic(t *testing.T) {
	clock := newManualClock()
	g := newRoutedGate()
	// RouteFingerprint reports "A" (so both the plan hash and the exec pre-check
	// see the approved value), but TestBound sees "B" — the route changed in the
	// window between the check and the send.
	nt := &recordingNotifyTester{name: "ops", fingerprint: "slack:webhook-A", sendFingerprint: "slack:webhook-B", routeExists: true}
	h, mod := newHarness(t, WithClock(clock), WithApprovalGate(g), WithNotifyTester(nt))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	routeID := "019f0000-0000-7000-8000-00000000abcd"
	wf := h.createWorkflow(admin, tenant, "atomic-notify", []map[string]any{
		step("pause", "wait", map[string]any{"seconds": 60}),
		step("notify", "notify-test", map[string]any{"route_id": routeID}, "pause"),
	})
	id := wf["id"].(string)
	r2 := h.runToPhase2(g, admin, tenant, id)
	runID := r2.body["run"].(map[string]any)["id"].(string)

	clock.advance(61 * time.Second)
	mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))

	if nt.calls() != 0 {
		t.Fatalf("a route changed between the check and the atomic send was still delivered (Test called %d)", nt.calls())
	}
	st := runStepStatuses(h.getRun(admin, tenant, id, runID))
	if st["notify"] != stepStatusBlocked {
		t.Fatalf("notify must BLOCK when the route changed under the atomic seam, got %v", st["notify"])
	}
}

// --------------------------------------------------------------------------
// deferral #4 / item 6: the operator-owned dispatcher config is folded into the
// fingerprint, so re-pointing a subject to an attacker image/command/URL/skill
// (a config reload) after approval BLOCKS at execution.
// --------------------------------------------------------------------------

func TestS467ReviewOperatorConfigRepointVoids(t *testing.T) {
	clock := newManualClock()
	g := newRoutedGate()
	gen := &mutableGen{gen: "gen-A"}
	h, mod := newHarness(t, WithClock(clock), WithApprovalGate(g),
		WithDispatcher(fakeDispatcher{ref: "d"}), WithDispatcherGeneration(gen))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	schedID := h.createAgentSchedule(admin, tenant, "nightly", "agent-1")
	wf := h.createWorkflow(admin, tenant, "cfg", []map[string]any{
		step("pause", "wait", map[string]any{"seconds": 60}),
		step("fire", "schedule-fire", map[string]any{"schedule_id": schedID}, "pause"),
	})
	id := wf["id"].(string)
	r2 := h.runToPhase2(g, admin, tenant, id)
	runID := r2.body["run"].(map[string]any)["id"].(string)

	// Operator reloads: the SAME schedule subject now maps to an attacker image
	// (its effective-dispatcher-config generation flips) while the schedule row
	// is unchanged.
	gen.set("gen-ATTACKER")
	clock.advance(61 * time.Second)
	mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))

	st := runStepStatuses(h.getRun(admin, tenant, id, runID))
	if st["fire"] != stepStatusBlocked {
		t.Fatalf("schedule-fire must BLOCK when the operator dispatcher config changed since approval, got %v", st["fire"])
	}
}
