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

	"github.com/olivaresai/olivares/core/model"
)

// s467_test.go — repro + regression tests for the two approval↔effect
// integrity defects:
//
//   D-05  the DIRECT schedule-fire path re-dispatches under one approval (the
//         gate's Status is a pure read that never burns the decision), so a
//         client retry after a lost/failed audit duplicates the actuation and a
//         re-POST of the same approval_ref fires the agent again.
//
//   D-06  a workflow run freezes the STEP GRAPH but not the effect-bearing
//         TARGET: a run approved and paused at a wait/HITL step actuates the
//         CURRENT schedule subject / notify route at execution time, so an editor
//         (schedule:write — one tier below the admin a fire needs) can re-point
//         the target during the pause and the run acts on the new target under
//         the old, still-valid "yes".

// countingDispatcher counts every Fire so a test can prove single-use: an
// approval authorizes exactly ONE dispatch, a re-POST replays without re-firing.
type countingDispatcher struct {
	mu    sync.Mutex
	count int
	ref   string
}

func (d *countingDispatcher) Fire(_ context.Context, _ FireRequest) (DispatchResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.count++
	return DispatchResult{Ref: d.ref}, nil
}

func (d *countingDispatcher) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count
}

// capturingDispatcher records the subject it was fired against so a test can
// prove a re-pointed target was NOT actuated under an old approval.
type capturingDispatcher struct {
	mu             sync.Mutex
	ref            string
	fired          bool
	lastSubjectRef string
}

func (d *capturingDispatcher) Fire(_ context.Context, req FireRequest) (DispatchResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fired = true
	d.lastSubjectRef = req.SubjectRef
	return DispatchResult{Ref: d.ref}, nil
}

func (d *capturingDispatcher) firedSubject() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastSubjectRef
}

// recordingNotifyTester is a NotifyTester whose route fingerprint a test can
// flip mid-run (simulating a re-pointed alert route) and that records every
// Test. It carries RouteFingerprint (the effect-bearing digest seam D-06 adds);
// the method is harmless extra surface before the interface declares it.
type recordingNotifyTester struct {
	mu          sync.Mutex
	name        string
	fingerprint string // what RouteFingerprint reports (the "check" read)
	// sendFingerprint, if set, is what TestBound sees at delivery time — modeling
	// a route re-pointed BETWEEN the check and the atomic send (hole c1). Empty ⇒
	// TestBound uses fingerprint.
	sendFingerprint string
	routeExists     bool
	testCalls       int
}

func (n *recordingNotifyTester) Test(context.Context, model.TenantID, string) (string, string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.testCalls++
	return "sent", "ok", nil
}

func (n *recordingNotifyTester) LookupRoute(context.Context, model.TenantID, string) (string, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.name, n.routeExists, nil
}

// RouteFingerprint is the effect-bearing target digest D-06 verifies at
// execution. Extra method before the seam declares it (harmless in RED).
func (n *recordingNotifyTester) RouteFingerprint(context.Context, model.TenantID, string) (string, bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.fingerprint, n.routeExists, nil
}

// TestBound is the atomic verify-and-deliver seam: it compares its CURRENT
// fingerprint (sendFingerprint if set, else fingerprint) to the frozen expected
// value and refuses on a mismatch — modeling a route re-pointed since approval.
func (n *recordingNotifyTester) TestBound(_ context.Context, _ model.TenantID, _, expectedFingerprint, _ string) (string, string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	cur := n.fingerprint
	if n.sendFingerprint != "" {
		cur = n.sendFingerprint
	}
	if cur != expectedFingerprint {
		return "", "", ErrRouteBindingChanged
	}
	n.testCalls++
	return "sent", "ok", nil
}

func (n *recordingNotifyTester) setFingerprint(f string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.fingerprint = f
}

func (n *recordingNotifyTester) calls() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.testCalls
}

// --------------------------------------------------------------------------
// D-05 — direct schedule fire: replay + duplicate-without-evidence.
// --------------------------------------------------------------------------

// A re-POST of the SAME approval_ref must NOT re-dispatch. The gate's Status is
// a pure read (no Consume), so without a single-use claim reserved BEFORE the
// dispatch, a client retry (its previous audit/response was lost) fires the
// agent a second time — one human "yes" becoming repeated actuation.
func TestS467D05DirectFireApprovalIsSingleUse(t *testing.T) {
	disp := &countingDispatcher{ref: "run-42"}
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved}), WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	fire := func() resp {
		return h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok,
			map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	}

	r1 := fire()
	if r1.code != http.StatusOK || r1.body["op_status"] != opStatusDispatched || r1.body["dispatch_ref"] != "run-42" {
		t.Fatalf("first fire = %d %s", r1.code, r1.raw)
	}

	// A client retry with the same approval_ref (the first response/audit was
	// lost). This is the ambiguous-result retry the defect duplicates.
	r2 := fire()
	if disp.calls() != 1 {
		t.Fatalf("re-POST of the same approval_ref re-dispatched: dispatcher called %d times, want 1 (single-use)", disp.calls())
	}
	// The replay is idempotent: it returns the ORIGINAL dispatch result, never a
	// second actuation.
	if r2.body["dispatch_ref"] != "run-42" {
		t.Fatalf("replay must echo the original dispatch_ref, got %s", r2.raw)
	}
}

// Concurrent phase-2 fires with one approval_ref must dispatch exactly ONCE:
// the two callers race find→dispatch, and without an atomic single-use claim
// both read "approved" and both actuate.
func TestS467D05DirectFireConcurrentNoDoubleDispatch(t *testing.T) {
	disp := &countingDispatcher{ref: "run-1"}
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved}), WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	const n = 6
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok,
				map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
		}()
	}
	wg.Wait()
	if disp.calls() != 1 {
		t.Fatalf("concurrent fires double-dispatched: %d, want 1", disp.calls())
	}
}

// --------------------------------------------------------------------------
// D-06 — a paused run must not actuate a re-pointed target under the old approval.
// --------------------------------------------------------------------------

// A run approves a schedule-fire of subject X. While it is paused at a wait, an
// editor re-points the schedule to Y. On resume the run must BLOCK the fire
// (target differs from what was approved), never dispatch Y.
func TestS467D06ScheduleFireBlocksRetargetDuringPause(t *testing.T) {
	clock := newManualClock()
	g := newRoutedGate()
	disp := &capturingDispatcher{ref: "d"}
	h, mod := newHarness(t, WithClock(clock), WithApprovalGate(g), WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@x.io", "editor")
	schedID := h.createAgentSchedule(admin, tenant, "nightly", "agent-prod-safe")
	wf := h.createWorkflow(admin, tenant, "paused-fire", []map[string]any{
		step("pause", "wait", map[string]any{"seconds": 60}),
		step("fire", "schedule-fire", map[string]any{"schedule_id": schedID}, "pause"),
	})
	id := wf["id"].(string)

	r2 := h.runToPhase2(g, admin, tenant, id)
	run := r2.body["run"].(map[string]any)
	runID := run["id"].(string)
	if st := runStepStatuses(run); st["pause"] != stepStatusWaiting || st["fire"] != stepStatusPending {
		t.Fatalf("initial paused state = %v", st)
	}

	// Editor (schedule:write) re-points the schedule AFTER the run is approved.
	if r := h.do("PATCH", "/v1/m/orchestration/schedules/"+schedID, editor,
		map[string]any{"subject_ref": "agent-somewhere-else"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("retarget = %d %s", r.code, r.raw)
	}

	clock.advance(61 * time.Second)
	mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))

	if disp.firedSubject() == "agent-somewhere-else" {
		t.Fatal("the re-pointed schedule was dispatched under the old approval")
	}
	st := runStepStatuses(h.getRun(admin, tenant, id, runID))
	if st["fire"] != stepStatusBlocked {
		t.Fatalf("fire step must BLOCK on a target change since approval, got %v", st["fire"])
	}
}

// The same for a notify-test step: re-pointing the alert route's destination
// during the pause must block the step, never send to the new destination.
func TestS467D06NotifyTestBlocksRepointDuringPause(t *testing.T) {
	clock := newManualClock()
	g := newRoutedGate()
	nt := &recordingNotifyTester{name: "ops", fingerprint: "slack:webhook-A", routeExists: true}
	h, mod := newHarness(t, WithClock(clock), WithApprovalGate(g), WithNotifyTester(nt))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	routeID := "019f0000-0000-7000-8000-00000000abcd"
	wf := h.createWorkflow(admin, tenant, "paused-notify", []map[string]any{
		step("pause", "wait", map[string]any{"seconds": 60}),
		step("notify", "notify-test", map[string]any{"route_id": routeID}, "pause"),
	})
	id := wf["id"].(string)

	r2 := h.runToPhase2(g, admin, tenant, id)
	run := r2.body["run"].(map[string]any)
	runID := run["id"].(string)
	if st := runStepStatuses(run); st["notify"] != stepStatusPending {
		t.Fatalf("notify must be pending behind the wait, got %v", st)
	}

	// The alert route is re-pointed to a different destination during the pause.
	nt.setFingerprint("slack:webhook-EVIL")

	clock.advance(61 * time.Second)
	mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))

	if nt.calls() != 0 {
		t.Fatalf("the re-pointed route was actuated under the old approval (Test called %d)", nt.calls())
	}
	st := runStepStatuses(h.getRun(admin, tenant, id, runID))
	if st["notify"] != stepStatusBlocked {
		t.Fatalf("notify step must BLOCK on a route change since approval, got %v", st["notify"])
	}
}
