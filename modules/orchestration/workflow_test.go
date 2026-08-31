// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// routedGate is a programmable ApprovalGate whose decisions are keyed by
// approval ref, so a test can approve the RUN approval while a mid-graph
// approval-gate STEP stays pending (two independent HITL decisions in flight).
// Like the real bridge, it BINDS each approval to the plan hash it was
// requested with and echoes that STORED hash back — so a stale approval
// honestly mismatches a changed plan.
type routedGate struct {
	mu     sync.Mutex
	next   int
	status map[string]GateStatus
	bound  map[string]string
}

func newRoutedGate() *routedGate {
	return &routedGate{status: map[string]GateStatus{}, bound: map[string]string{}}
}

func (g *routedGate) Request(_ context.Context, req ApprovalRequest) (GateDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	ref := fmt.Sprintf("appr-%d", g.next)
	g.status[ref] = StatusPending
	g.bound[ref] = req.PlanHash
	return GateDecision{ApprovalRef: ref, Status: StatusPending, PlanHash: req.PlanHash}, nil
}

func (g *routedGate) Status(_ context.Context, chk ApprovalCheck) (GateDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.status[chk.ApprovalRef]
	if !ok {
		st = StatusExpired
	}
	return GateDecision{ApprovalRef: chk.ApprovalRef, Status: st, PlanHash: g.bound[chk.ApprovalRef]}, nil
}

func (g *routedGate) set(ref string, st GateStatus) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.status[ref] = st
}

// mutableStopGate is a StopGate a test can flip mid-run (freeze/resume).
type mutableStopGate struct {
	mu       sync.Mutex
	decision StopDecision
	err      error
}

func (g *mutableStopGate) Check(context.Context, model.TenantID, StopDims) (StopDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return StopDecision{}, g.err
	}
	return g.decision, nil
}

func (g *mutableStopGate) set(d StopDecision, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.decision, g.err = d, err
}

// wfSteps builds a steps payload.
func step(ref, kind string, config map[string]any, deps ...string) map[string]any {
	if deps == nil {
		deps = []string{}
	}
	return map[string]any{"ref": ref, "kind": kind, "config": config, "depends_on": deps}
}

func emitStep(ref string, deps ...string) map[string]any {
	return step(ref, "eventing-emit", map[string]any{"label": "lbl-" + ref}, deps...)
}

// createWorkflow POSTs a workflow and fails the test on non-201.
func (h *harness) createWorkflow(token string, tenant model.TenantID, name string, steps []map[string]any) map[string]any {
	h.t.Helper()
	r := h.do("POST", "/v1/m/orchestration/workflows", token,
		map[string]any{"name": name, "steps": steps}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create workflow = %d %s", r.code, r.raw)
	}
	return r.body
}

// createAgentSchedule declares a manual-trigger agent schedule and returns its id.
func (h *harness) createAgentSchedule(token string, tenant model.TenantID, name, agent string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/m/orchestration/schedules", token, map[string]any{
		"name": name, "subject_kind": "agent", "subject_ref": agent, "trigger_kind": "manual",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create schedule = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

// runToPhase2 drives the two-phase run to completion of phase 2 for a gate
// whose run approval the test flips to approved, returning the phase-2 body.
func (h *harness) runToPhase2(g *routedGate, token string, tenant model.TenantID, wfID string) resp {
	h.t.Helper()
	r1 := h.do("POST", "/v1/m/orchestration/workflows/"+wfID+"/run", token, nil, tenantHdr(tenant))
	if r1.code != http.StatusAccepted {
		h.t.Fatalf("run phase 1 = %d %s", r1.code, r1.raw)
	}
	ref := r1.body["approval_ref"].(string)
	g.set(ref, StatusApproved)
	r2 := h.do("POST", "/v1/m/orchestration/workflows/"+wfID+"/run", token,
		map[string]any{"approval_ref": ref}, tenantHdr(tenant))
	if r2.code != http.StatusOK {
		h.t.Fatalf("run phase 2 = %d %s", r2.code, r2.raw)
	}
	return r2
}

// runSteps extracts {ref: status} from a run JSON object.
func runStepStatuses(run map[string]any) map[string]string {
	out := map[string]string{}
	for _, it := range run["steps"].([]any) {
		m := it.(map[string]any)
		out[m["ref"].(string)] = m["status"].(string)
	}
	return out
}

func (h *harness) getRun(token string, tenant model.TenantID, wfID, runID string) map[string]any {
	h.t.Helper()
	r := h.do("GET", "/v1/m/orchestration/workflows/"+wfID+"/runs/"+runID, token, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("get run = %d %s", r.code, r.raw)
	}
	return r.body
}

func (h *harness) moduleCtx(tenant model.TenantID) api.ModuleContext {
	return api.ModuleContext{Tenant: tenant, Data: api.NewScopedData(h.st, tenant)}
}

// --------------------------------------------------------------------------
// F1: graph validation, CRUD, revisions, limits, isolation, RBAC.
// --------------------------------------------------------------------------

// The server rejects every malformed graph shape with a structured, node-
// addressable error: cycles, unknown/duplicate refs, self-deps, bad kinds,
// bad configs, fan-in over the cap and unknown schedule references.
func TestWorkflowGraphValidationRejects(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	post := func(steps []map[string]any) resp {
		return h.do("POST", "/v1/m/orchestration/workflows", admin,
			map[string]any{"name": "wf-" + fmt.Sprint(len(steps)) + fmt.Sprint(time.Now().UnixNano()), "steps": steps}, tenantHdr(tenant))
	}

	cases := []struct {
		name  string
		steps []map[string]any
	}{
		{"cycle", []map[string]any{emitStep("a", "b"), emitStep("b", "a")}},
		{"self-dep", []map[string]any{emitStep("a", "a")}},
		{"unknown dep", []map[string]any{emitStep("a", "ghost")}},
		{"duplicate ref", []map[string]any{emitStep("a"), emitStep("a")}},
		{"bad ref", []map[string]any{step("UPPER", "eventing-emit", map[string]any{"label": "x"})}},
		{"bad kind", []map[string]any{step("a", "http-call", map[string]any{})}},
		{"bad config key", []map[string]any{step("a", "eventing-emit", map[string]any{"label": "x", "extra": 1})}},
		{"wait out of bounds", []map[string]any{step("a", "wait", map[string]any{"seconds": 0})}},
		{"missing schedule", []map[string]any{step("a", "schedule-fire", map[string]any{"schedule_id": "019f0000-0000-7000-8000-000000000000"})}},
	}
	for _, tc := range cases {
		if r := post(tc.steps); r.code != http.StatusBadRequest {
			t.Errorf("%s: code = %d %s, want 400", tc.name, r.code, r.raw)
		}
	}

	// Fan-in over the cap.
	many := []map[string]any{}
	deps := []string{}
	for i := 0; i < maxFanIn+1; i++ {
		ref := fmt.Sprintf("s%d", i)
		many = append(many, emitStep(ref))
		deps = append(deps, ref)
	}
	many = append(many, emitStep("sink", deps...))
	if r := post(many); r.code != http.StatusBadRequest {
		t.Errorf("fan-in: code = %d %s, want 400", r.code, r.raw)
	}
}

// A valid graph creates, gets, patches, replaces and restores through the
// revision ledger; the plan hash moves with the graph and returns on restore.
func TestWorkflowCRUDRevisionsRestore(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	created := h.createWorkflow(admin, tenant, "deploy-train", []map[string]any{
		emitStep("a"), emitStep("b", "a"),
	})
	id := created["id"].(string)
	hashA := created["plan_hash"].(string)
	if created["version"].(float64) != 1 {
		t.Fatalf("version = %v, want 1", created["version"])
	}

	// Replace the graph: the hash must change and a revision must append.
	r := h.do("PUT", "/v1/m/orchestration/workflows/"+id+"/steps", admin,
		map[string]any{"steps": []map[string]any{emitStep("c")}}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("put steps = %d %s", r.code, r.raw)
	}
	hashB := r.body["plan_hash"].(string)
	if hashA == hashB {
		t.Fatal("plan_hash unchanged after a step-graph replacement")
	}

	// Patch metadata only.
	if r := h.do("PATCH", "/v1/m/orchestration/workflows/"+id, admin,
		map[string]any{"description": "the nightly deploy train"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("patch = %d %s", r.code, r.raw)
	}

	revs := h.do("GET", "/v1/m/orchestration/workflows/"+id+"/revisions", admin, nil, tenantHdr(tenant))
	items := revs.body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("revisions = %d, want 3 (create + steps + patch)", len(items))
	}
	first := items[0].(map[string]any)
	if first["op"].(string) != "create" {
		t.Fatalf("first revision op = %q, want create", first["op"])
	}

	// Restore the original graph: hash returns to hashA.
	r = h.do("POST", "/v1/m/orchestration/workflows/"+id+"/restore", admin,
		map[string]any{"revision_id": first["id"].(string)}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("restore = %d %s", r.code, r.raw)
	}
	if got := r.body["plan_hash"].(string); got != hashA {
		t.Fatalf("restored plan_hash = %s, want %s", got, hashA)
	}

	// A revision of ANOTHER workflow restores nothing and leaks nothing.
	other := h.createWorkflow(admin, tenant, "other", []map[string]any{emitStep("z")})
	orevs := h.do("GET", "/v1/m/orchestration/workflows/"+other["id"].(string)+"/revisions", admin, nil, tenantHdr(tenant))
	foreign := orevs.body["items"].([]any)[0].(map[string]any)["id"].(string)
	if r := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/restore", admin,
		map[string]any{"revision_id": foreign}, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("foreign-revision restore = %d, want 404", r.code)
	}
}

// The per-tenant workflow cap and the per-workflow step cap both enforce.
func TestWorkflowLimits(t *testing.T) {
	h, _ := newHarness(t, WithWorkflowLimits(1, 2))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	h.createWorkflow(admin, tenant, "first", []map[string]any{emitStep("a")})
	if r := h.do("POST", "/v1/m/orchestration/workflows", admin,
		map[string]any{"name": "second"}, tenantHdr(tenant)); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("over-cap create = %d %s, want 422", r.code, r.raw)
	}
	if r := h.do("PUT", "/v1/m/orchestration/workflows/"+"x", admin, nil, tenantHdr(tenant)); r.code == http.StatusOK {
		t.Fatal("bogus PUT succeeded")
	}
	over := []map[string]any{emitStep("a"), emitStep("b"), emitStep("c")}
	if r := h.do("POST", "/v1/m/orchestration/workflows", admin,
		map[string]any{"name": "big", "steps": over}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("over-steps create = %d %s, want 400", r.code, r.raw)
	}
}

// Workflows are tenant-isolated: another org sees neither the list entry nor
// the object, and the not-found never confirms existence.
func TestWorkflowTenantIsolation(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "org-a")
	tenantB := h.createOrg(admin, "org-b")

	wf := h.createWorkflow(admin, tenantA, "private", []map[string]any{emitStep("a")})
	id := wf["id"].(string)

	if r := h.do("GET", "/v1/m/orchestration/workflows", admin, nil, tenantHdr(tenantB)); r.code != http.StatusOK {
		t.Fatalf("list B = %d", r.code)
	} else if n := len(r.body["items"].([]any)); n != 0 {
		t.Fatalf("tenant B sees %d workflows, want 0", n)
	}
	if r := h.do("GET", "/v1/m/orchestration/workflows/"+id, admin, nil, tenantHdr(tenantB)); r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant get = %d, want 404", r.code)
	}
}

// Verb tiers: viewer reads, editor writes, ONLY admin runs; dry-run is a read.
func TestWorkflowPermissionTiers(t *testing.T) {
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	wf := h.createWorkflow(admin, tenant, "tiers", []map[string]any{emitStep("a")})
	id := wf["id"].(string)

	if r := h.do("GET", "/v1/m/orchestration/workflows", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("viewer list = %d, want 200", r.code)
	}
	if r := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/dry-run", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("viewer dry-run = %d, want 200", r.code)
	}
	if r := h.do("POST", "/v1/m/orchestration/workflows", viewer,
		map[string]any{"name": "nope"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("viewer create = %d, want 403", r.code)
	}
	if r := h.do("POST", "/v1/m/orchestration/workflows", editor,
		map[string]any{"name": "ed", "steps": []map[string]any{emitStep("a")}}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Errorf("editor create = %d %s, want 201", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", editor, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("editor run = %d, want 403", r.code)
	}
}

// --------------------------------------------------------------------------
// F2: dry-run, two-phase run, gates, runner semantics.
// --------------------------------------------------------------------------

// The dry-run returns the topological plan with per-step actions and gate
// requirements, flags a stale schedule reference, and has ZERO effects.
func TestWorkflowDryRunPlanAndNoEffects(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	schedID := h.createAgentSchedule(admin, tenant, "nightly", "agent-1")

	wf := h.createWorkflow(admin, tenant, "plan", []map[string]any{
		step("fire", "schedule-fire", map[string]any{"schedule_id": schedID}, "gate"),
		step("gate", "approval-gate", map[string]any{"reason": "release window"}),
	})
	id := wf["id"].(string)

	r := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/dry-run", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("dry-run = %d %s", r.code, r.raw)
	}
	if r.body["plan_hash"].(string) != wf["plan_hash"].(string) {
		t.Fatal("dry-run plan_hash differs from the workflow's")
	}
	steps := r.body["steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("plan steps = %d, want 2", len(steps))
	}
	first := steps[0].(map[string]any)
	second := steps[1].(map[string]any)
	if first["ref"].(string) != "gate" || second["ref"].(string) != "fire" {
		t.Fatalf("topo order = %s,%s want gate,fire", first["ref"], second["ref"])
	}
	if second["order"].(float64) != 2 {
		t.Fatalf("order = %v, want 2", second["order"])
	}

	// Retire the schedule → the plan must WARN, and still cause no effects.
	if r := h.do("PATCH", "/v1/m/orchestration/schedules/"+schedID, admin,
		map[string]any{"desired_status": "retired"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("retire schedule = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/m/orchestration/workflows/"+id+"/dry-run", admin, nil, tenantHdr(tenant))
	warned := false
	for _, it := range r.body["steps"].([]any) {
		if w, ok := it.(map[string]any)["warning"].(string); ok && w != "" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("dry-run did not warn about the retired schedule")
	}

	// No effects: no runs and no new decision-ledger rows beyond the schedule ops.
	runs := h.do("GET", "/v1/m/orchestration/workflows/"+id+"/runs", admin, nil, tenantHdr(tenant))
	if n := len(runs.body["items"].([]any)); n != 0 {
		t.Fatalf("dry-run created %d runs", n)
	}
}

// Deny-by-default: with no approval gate wired, phase 2 blocks the run and the
// denial is recorded; phase 1 still records the request honestly.
func TestWorkflowRunDenyClosedNoGate(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wf := h.createWorkflow(admin, tenant, "ungoverned", []map[string]any{emitStep("a")})
	id := wf["id"].(string)

	r1 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, nil, tenantHdr(tenant))
	if r1.code != http.StatusAccepted {
		t.Fatalf("phase 1 = %d %s", r1.code, r1.raw)
	}
	ref := r1.body["approval_ref"].(string)
	r2 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin,
		map[string]any{"approval_ref": ref}, tenantHdr(tenant))
	if r2.code != http.StatusForbidden {
		t.Fatalf("phase 2 = %d %s, want 403", r2.code, r2.raw)
	}
	runs := h.do("GET", "/v1/m/orchestration/workflows/"+id+"/runs", admin, nil, tenantHdr(tenant))
	if n := len(runs.body["items"].([]any)); n != 0 {
		t.Fatalf("denied run created %d runs", n)
	}
	if !h.waitForFinding(busUngovernedFire) {
		t.Fatal("no ungoverned-run finding emitted")
	}
}

// An approval bound to a STALE plan (the graph changed after phase 1) is void:
// strict plan binding blocks phase 2.
func TestWorkflowRunPlanHashMismatchBlocks(t *testing.T) {
	g := newRoutedGate()
	h, _ := newHarness(t, WithApprovalGate(g))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wf := h.createWorkflow(admin, tenant, "toctou", []map[string]any{emitStep("a")})
	id := wf["id"].(string)

	r1 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, nil, tenantHdr(tenant))
	ref := r1.body["approval_ref"].(string)
	g.set(ref, StatusApproved)

	// The graph changes between request and consume — the approval must void.
	if r := h.do("PUT", "/v1/m/orchestration/workflows/"+id+"/steps", admin,
		map[string]any{"steps": []map[string]any{emitStep("changed")}}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put steps = %d", r.code)
	}
	r2 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin,
		map[string]any{"approval_ref": ref}, tenantHdr(tenant))
	if r2.code != http.StatusForbidden {
		t.Fatalf("stale-plan phase 2 = %d %s, want 403", r2.code, r2.raw)
	}
}

// The happy path: emit → schedule-fire → notify-test drains in-request; every
// actuating step leaves an append-only evidence row; the schedule's
// last_fired_at advances; the workflow.signal reaches the bus with its payload.
func TestWorkflowRunCompletesWithEvidence(t *testing.T) {
	g := newRoutedGate()
	h, _ := newHarness(t,
		WithApprovalGate(g),
		WithDispatcher(fakeDispatcher{ref: "disp-9"}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	schedID := h.createAgentSchedule(admin, tenant, "nightly", "agent-1")

	var sigMu sync.Mutex
	var signals []WorkflowSignal
	if _, err := h.bus.Subscribe([]event.Type{TypeWorkflowSignal}, func(_ context.Context, e event.Event) error {
		raw, _ := json.Marshal(e.Payload)
		var s WorkflowSignal
		_ = json.Unmarshal(raw, &s)
		sigMu.Lock()
		signals = append(signals, s)
		sigMu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	wf := h.createWorkflow(admin, tenant, "train", []map[string]any{
		emitStep("announce"),
		step("fire", "schedule-fire", map[string]any{"schedule_id": schedID}, "announce"),
		step("notify", "notify-test", map[string]any{"route_id": "019f0000-0000-7000-8000-00000000abcd"}, "fire"),
	})
	id := wf["id"].(string)

	r2 := h.runToPhase2(g, admin, tenant, id)
	run := r2.body["run"].(map[string]any)
	if run["status"].(string) != runStatusCompleted {
		t.Fatalf("run status = %s %s, want completed", run["status"], r2.raw)
	}
	st := runStepStatuses(run)
	if st["announce"] != stepStatusEmitted || st["fire"] != stepStatusDispatched || st["notify"] != stepStatusDeclared {
		t.Fatalf("step statuses = %v", st)
	}

	// The bus saw exactly one signal with the run's refs and the bounded label.
	deadline := time.Now().Add(2 * time.Second)
	for {
		sigMu.Lock()
		n := len(signals)
		sigMu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	sigMu.Lock()
	if len(signals) != 1 || signals[0].StepRef != "announce" || signals[0].Label != "lbl-announce" || signals[0].RunRef != run["id"].(string) {
		t.Fatalf("signals = %+v", signals)
	}
	sigMu.Unlock()

	// The schedule's last_fired_at advanced (a workflow fire is activity).
	sched := h.do("GET", "/v1/m/orchestration/schedules/"+schedID, admin, nil, tenantHdr(tenant))
	if sched.body["last_fired_at"] == nil || sched.body["last_fired_at"].(string) == "" {
		t.Fatal("schedule last_fired_at did not advance")
	}

	// Evidence: request + start + 3 step rows (2 dispatched + 1 declared) + end.
	dec := h.do("GET", "/v1/m/orchestration/decisions", admin, nil, tenantHdr(tenant))
	byOp := map[string]int{}
	for _, it := range dec.body["items"].([]any) {
		m := it.(map[string]any)
		if m["subject_kind"].(string) == "workflow" {
			byOp[m["op"].(string)]++
		}
	}
	if byOp[opRunRequest] != 1 || byOp[opRun] != 1 || byOp[opRunStep] != 3 || byOp[opRunEnd] != 1 {
		t.Fatalf("ledger ops = %v", byOp)
	}
}

// A mid-graph approval-gate step pauses the run; approving it lets the pump
// advance to completion; rejecting it blocks the step, skips dependents and
// fails the run.
func TestWorkflowApprovalGateStepPausesAndResumes(t *testing.T) {
	for _, verdictApproved := range []bool{true, false} {
		g := newRoutedGate()
		h, mod := newHarness(t, WithApprovalGate(g))
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "acme")
		wf := h.createWorkflow(admin, tenant, "gated", []map[string]any{
			step("hold", "approval-gate", map[string]any{"reason": "manual check"}),
			emitStep("after", "hold"),
		})
		id := wf["id"].(string)

		r2 := h.runToPhase2(g, admin, tenant, id)
		run := r2.body["run"].(map[string]any)
		runID := run["id"].(string)
		st := runStepStatuses(run)
		if run["status"].(string) != runStatusRunning || st["hold"] != stepStatusWaitingGate || st["after"] != stepStatusPending {
			t.Fatalf("paused run state = %s %v", run["status"], st)
		}

		// Resolve the STEP approval (the second one the gate minted).
		var holdRef string
		for _, it := range run["steps"].([]any) {
			m := it.(map[string]any)
			if m["ref"] == "hold" {
				holdRef = m["approval_ref"].(string)
			}
		}
		if verdictApproved {
			g.set(holdRef, StatusApproved)
		} else {
			g.set(holdRef, StatusRejected)
		}
		mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))

		got := h.getRun(admin, tenant, id, runID)
		st = runStepStatuses(got)
		if verdictApproved {
			if got["status"].(string) != runStatusCompleted || st["hold"] != stepStatusGatePassed || st["after"] != stepStatusEmitted {
				t.Fatalf("approved: run = %s steps = %v", got["status"], st)
			}
		} else {
			if got["status"].(string) != runStatusFailed || st["hold"] != stepStatusBlocked || st["after"] != stepStatusSkipped {
				t.Fatalf("rejected: run = %s steps = %v", got["status"], st)
			}
		}
	}
}

// A wait step paces the run: nothing advances before not_before, the pump
// completes it after, driven by the injected clock.
func TestWorkflowWaitStepPacesRun(t *testing.T) {
	clock := newManualClock()
	g := newRoutedGate()
	h, mod := newHarness(t, WithClock(clock), WithApprovalGate(g))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wf := h.createWorkflow(admin, tenant, "paced", []map[string]any{
		step("pause", "wait", map[string]any{"seconds": 60}),
		emitStep("after", "pause"),
	})
	id := wf["id"].(string)

	r2 := h.runToPhase2(g, admin, tenant, id)
	run := r2.body["run"].(map[string]any)
	runID := run["id"].(string)
	if st := runStepStatuses(run); st["pause"] != stepStatusWaiting || st["after"] != stepStatusPending {
		t.Fatalf("initial state = %v", st)
	}

	mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))
	if st := runStepStatuses(h.getRun(admin, tenant, id, runID)); st["pause"] != stepStatusWaiting {
		t.Fatalf("advanced before the wait elapsed: %v", st)
	}

	clock.advance(61 * time.Second)
	mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))
	got := h.getRun(admin, tenant, id, runID)
	st := runStepStatuses(got)
	if got["status"].(string) != runStatusCompleted || st["pause"] != stepStatusDone || st["after"] != stepStatusEmitted {
		t.Fatalf("after wait: run = %s steps = %v", got["status"], st)
	}
}

// FIN-08 on a workflow step: an enforcing budget at cap denies the fired
// schedule BEFORE the dispatcher, the step is budget_blocked and the run fails.
func TestWorkflowScheduleFireBudgetBlocked(t *testing.T) {
	g := newRoutedGate()
	fired := false
	h, _ := newHarness(t,
		WithApprovalGate(g),
		WithDispatcher(recordingDispatcher{fired: &fired}),
		WithBudgetGate(fakeBudgetGate{decision: BudgetDecision{Allowed: false, Action: "block", BudgetRef: "b1", Reason: "budget cap reached"}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	schedID := h.createAgentSchedule(admin, tenant, "nightly", "agent-1")
	wf := h.createWorkflow(admin, tenant, "capped", []map[string]any{
		step("fire", "schedule-fire", map[string]any{"schedule_id": schedID}),
	})

	r2 := h.runToPhase2(g, admin, tenant, wf["id"].(string))
	run := r2.body["run"].(map[string]any)
	st := runStepStatuses(run)
	if run["status"].(string) != runStatusFailed || st["fire"] != stepStatusBudget {
		t.Fatalf("run = %s steps = %v", run["status"], st)
	}
	if fired {
		t.Fatal("dispatcher was reached despite the budget denial")
	}
}

// On runs: an estate stop denies BOTH phases with 423 and, engaged
// mid-run, freezes a paced run (visible paused_reason) until it lifts.
func TestWorkflowKillSwitchFreezesRun(t *testing.T) {
	clock := newManualClock()
	g := newRoutedGate()
	stop := &mutableStopGate{}
	h, mod := newHarness(t, WithClock(clock), WithApprovalGate(g), WithStopGate(stop))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wf := h.createWorkflow(admin, tenant, "frozen", []map[string]any{
		step("pause", "wait", map[string]any{"seconds": 30}),
		emitStep("after", "pause"),
	})
	id := wf["id"].(string)

	// Phase denial while stopped.
	stop.set(StopDecision{Stopped: true, StopRef: "stop-1", Scope: "estate"}, nil)
	if r := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, nil, tenantHdr(tenant)); r.code != http.StatusLocked {
		t.Fatalf("phase 1 under stop = %d, want 423", r.code)
	}
	stop.set(StopDecision{}, nil)

	r2 := h.runToPhase2(g, admin, tenant, id)
	runID := r2.body["run"].(map[string]any)["id"].(string)

	// Stop engages mid-run; the elapsed wait must NOT advance and the run
	// shows why. An unreadable gate freezes identically (fail closed).
	stop.set(StopDecision{Stopped: true, StopRef: "stop-2", Scope: "estate"}, nil)
	clock.advance(31 * time.Second)
	mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))
	got := h.getRun(admin, tenant, id, runID)
	if got["status"].(string) != runStatusRunning || got["paused_reason"].(string) != "kill_switch" {
		t.Fatalf("under stop: status = %s paused = %v", got["status"], got["paused_reason"])
	}

	// The stop lifts: the run resumes and completes, the pause marker clears.
	stop.set(StopDecision{}, nil)
	mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))
	got = h.getRun(admin, tenant, id, runID)
	if got["status"].(string) != runStatusCompleted {
		t.Fatalf("after stop lift: status = %s", got["status"])
	}
	if pr, ok := got["paused_reason"].(string); ok && pr != "" {
		t.Fatalf("paused_reason not cleared: %q", pr)
	}
}

// A step that fails at execution time (the dispatcher errors) fails honestly
// and its dependents skip rather than running on a broken predecessor.
func TestWorkflowFailurePropagatesSkips(t *testing.T) {
	g := newRoutedGate()
	h, _ := newHarness(t,
		WithApprovalGate(g),
		WithDispatcher(fakeDispatcher{err: errors.New("synthetic dispatcher outage")}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	schedID := h.createAgentSchedule(admin, tenant, "doomed", "agent-1")
	wf := h.createWorkflow(admin, tenant, "cascade", []map[string]any{
		step("fire", "schedule-fire", map[string]any{"schedule_id": schedID}),
		emitStep("after", "fire"),
	})
	r2 := h.runToPhase2(g, admin, tenant, wf["id"].(string))
	run := r2.body["run"].(map[string]any)
	st := runStepStatuses(run)
	if run["status"].(string) != runStatusFailed || st["fire"] != stepStatusFailed || st["after"] != stepStatusSkipped {
		t.Fatalf("run = %s steps = %v", run["status"], st)
	}
}

// Skip propagation must depend on the GRAPH, not on what the dependency refs
// happen to be called. A doomed step whose failed upstream sorts AFTER a
// still-running one must still skip immediately, instead of sitting "pending"
// behind a wait that may not elapse for hours.
func TestWorkflowSkipIgnoresDependencyRefOrder(t *testing.T) {
	for _, names := range []struct{ wait, fire string }{{"aaa", "zzz"}, {"zzz", "aaa"}} {
		clock := newManualClock()
		g := newRoutedGate()
		h, _ := newHarness(t, WithClock(clock), WithApprovalGate(g),
			WithDispatcher(fakeDispatcher{err: errors.New("synthetic dispatcher outage")}))
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "acme")
		schedID := h.createAgentSchedule(admin, tenant, "sch", "agent-1")
		wf := h.createWorkflow(admin, tenant, "order-"+names.wait, []map[string]any{
			step(names.wait, "wait", map[string]any{"seconds": 86400}),
			step(names.fire, "schedule-fire", map[string]any{"schedule_id": schedID}),
			emitStep("sink", names.wait, names.fire),
		})
		r2 := h.runToPhase2(g, admin, tenant, wf["id"].(string))
		st := runStepStatuses(r2.body["run"].(map[string]any))
		if st[names.fire] != stepStatusFailed {
			t.Fatalf("deps %s/%s: fire = %v, want failed", names.wait, names.fire, st[names.fire])
		}
		if st["sink"] != stepStatusSkipped {
			t.Fatalf("deps %s/%s: sink = %v, want skipped regardless of ref order (steps=%v)",
				names.wait, names.fire, st["sink"], st)
		}
	}
}

// ANTI-TOCTOU: the approval binds what the graph ACTUATES, not merely the id it
// names. Re-pointing a schedule between the two phases — something a write-tier
// principal can do, one tier below the admin a direct fire needs — must void the
// approval, exactly as it does on the direct fire path.
func TestWorkflowRetargetedScheduleVoidsApproval(t *testing.T) {
	g := newRoutedGate()
	fired := false
	h, _ := newHarness(t, WithApprovalGate(g), WithDispatcher(recordingDispatcher{fired: &fired}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@x.io", "editor")
	schedID := h.createAgentSchedule(admin, tenant, "nightly", "agent-prod-safe")
	wf := h.createWorkflow(admin, tenant, "retarget", []map[string]any{
		step("fire", "schedule-fire", map[string]any{"schedule_id": schedID}),
	})
	id := wf["id"].(string)

	r1 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, nil, tenantHdr(tenant))
	if r1.code != http.StatusAccepted {
		t.Fatalf("phase 1 = %d %s", r1.code, r1.raw)
	}
	ref := r1.body["approval_ref"].(string)
	g.set(ref, StatusApproved)

	// A WRITE-tier principal re-points the schedule at another agent.
	if r := h.do("PATCH", "/v1/m/orchestration/schedules/"+schedID, editor,
		map[string]any{"subject_ref": "agent-somewhere-else"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("retarget = %d %s", r.code, r.raw)
	}
	r2 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin,
		map[string]any{"approval_ref": ref}, tenantHdr(tenant))
	if r2.code != http.StatusForbidden {
		t.Fatalf("phase 2 after retarget = %d %s, want 403 (the approved plan no longer describes what would run)", r2.code, r2.raw)
	}
	if fired {
		t.Fatal("the re-pointed schedule was dispatched under the old approval")
	}
}

// An approval authorizes exactly ONE run. The gate's status check is a pure
// read, so without an explicit claim an approved reference could be re-POSTed
// to mint run after run — one human "yes" becoming unbounded actuation.
func TestWorkflowRunApprovalIsSingleUse(t *testing.T) {
	g := newRoutedGate()
	h, _ := newHarness(t, WithApprovalGate(g), WithDispatcher(fakeDispatcher{ref: "d"}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wf := h.createWorkflow(admin, tenant, "once", []map[string]any{emitStep("a")})
	id := wf["id"].(string)

	r1 := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, nil, tenantHdr(tenant))
	ref := r1.body["approval_ref"].(string)
	g.set(ref, StatusApproved)
	if r := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin,
		map[string]any{"approval_ref": ref}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("first run = %d %s", r.code, r.raw)
	}
	for i := 0; i < 2; i++ {
		r := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin,
			map[string]any{"approval_ref": ref}, tenantHdr(tenant))
		if r.code != http.StatusConflict {
			t.Fatalf("replay %d = %d %s, want 409", i+1, r.code, r.raw)
		}
	}
	runs := h.do("GET", "/v1/m/orchestration/workflows/"+id+"/runs", admin, nil, tenantHdr(tenant))
	if n := len(runs.body["items"].([]any)); n != 1 {
		t.Fatalf("runs = %d, want exactly 1 from one approval", n)
	}
}

// A mid-graph checkpoint's approval must NOT be usable as the authorization to
// start a whole new run: the two decisions answer different questions, so they
// are bound to different hashes.
func TestWorkflowGateApprovalCannotStartARun(t *testing.T) {
	g := newRoutedGate()
	h, _ := newHarness(t, WithApprovalGate(g))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wf := h.createWorkflow(admin, tenant, "checkpoint", []map[string]any{
		step("hold", "approval-gate", map[string]any{"reason": "manual check"}),
		emitStep("after", "hold"),
	})
	id := wf["id"].(string)
	r2 := h.runToPhase2(g, admin, tenant, id)
	run := r2.body["run"].(map[string]any)

	var holdRef string
	for _, it := range run["steps"].([]any) {
		if m := it.(map[string]any); m["ref"] == "hold" {
			holdRef, _ = m["approval_ref"].(string)
		}
	}
	if holdRef == "" {
		t.Fatal("the gate step opened no approval")
	}
	// The human answers the CHECKPOINT...
	g.set(holdRef, StatusApproved)
	// ...and that answer must not double as permission to start another run.
	r := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin,
		map[string]any{"approval_ref": holdRef}, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("gate approval replayed as a run approval = %d %s, want 403", r.code, r.raw)
	}
}

// A kill-switch freeze of an IN-FLIGHT run must leave durable evidence. The
// pause marker is mutable state that later passes clear, so without a ledger row
// the fact that an emergency stop halted a governed run would vanish with it.
func TestWorkflowFreezeLeavesDurableEvidence(t *testing.T) {
	clock := newManualClock()
	g := newRoutedGate()
	stop := &mutableStopGate{}
	h, mod := newHarness(t, WithClock(clock), WithApprovalGate(g), WithStopGate(stop))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wf := h.createWorkflow(admin, tenant, "frozen-evidence", []map[string]any{
		step("pause", "wait", map[string]any{"seconds": 30}),
		emitStep("after", "pause"),
	})
	id := wf["id"].(string)
	h.runToPhase2(g, admin, tenant, id)

	stop.set(StopDecision{Stopped: true, StopRef: "stop-9", Scope: "estate"}, nil)
	clock.advance(31 * time.Second)
	mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))
	// Lift it and let the run finish: the marker goes, the evidence must not.
	stop.set(StopDecision{}, nil)
	mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))

	dec := h.do("GET", "/v1/m/orchestration/decisions?limit=100", admin, nil, tenantHdr(tenant))
	frozen := 0
	for _, it := range dec.body["items"].([]any) {
		m := it.(map[string]any)
		if res, _ := m["result"].(string); strings.Contains(res, "frozen: kill_switch") {
			frozen++
		}
	}
	if frozen != 1 {
		t.Fatalf("freeze evidence rows = %d, want exactly 1 (not flooded, not lost)", frozen)
	}
}

// A disabled workflow accepts no new run (the check precedes both phases).
func TestWorkflowDisabledRefusesRuns(t *testing.T) {
	h, _ := newHarness(t, WithApprovalGate(fakeGate{status: StatusApproved}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wf := h.createWorkflow(admin, tenant, "off", []map[string]any{emitStep("a")})
	id := wf["id"].(string)
	if r := h.do("PATCH", "/v1/m/orchestration/workflows/"+id, admin,
		map[string]any{"enabled": false}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("disable = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/orchestration/workflows/"+id+"/run", admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("run of disabled = %d, want 409", r.code)
	}
}

// An orphaned executing claim (the advancer died between claim and resolve)
// fails after the timeout — at-most-once, never a blind retry of a possibly-
// performed side effect. The orphan state is written directly (white-box):
// it is exactly what a crash leaves behind.
func TestWorkflowOrphanedClaimFailsAfterTimeout(t *testing.T) {
	clock := newManualClock()
	g := newRoutedGate()
	h, mod := newHarness(t, WithClock(clock), WithApprovalGate(g))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wf := h.createWorkflow(admin, tenant, "orphan", []map[string]any{
		step("pause", "wait", map[string]any{"seconds": 30}),
	})
	id := wf["id"].(string)
	r2 := h.runToPhase2(g, admin, tenant, id)
	runID := r2.body["run"].(map[string]any)["id"].(string)

	ctx := context.Background()
	mc := h.moduleCtx(tenant)
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(runID))
		if err != nil {
			return err
		}
		steps, err := decodeRunSteps(rec.String(colWrSteps))
		if err != nil {
			return err
		}
		steps[0].Status = stepStatusExecuting
		steps[0].At = clock.Now().String()
		steps[0].NotBefore = ""
		rec[colWrSteps] = encodeRunSteps(steps)
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Within the timeout the claim is respected (another advancer may own it).
	mod.AdvanceWorkflowRuns(ctx, mc)
	if st := runStepStatuses(h.getRun(admin, tenant, id, runID)); st["pause"] != stepStatusExecuting {
		t.Fatalf("claim not respected inside the timeout: %v", st)
	}

	clock.advance(executingTimeout + time.Second)
	mod.AdvanceWorkflowRuns(ctx, mc)
	got := h.getRun(admin, tenant, id, runID)
	st := runStepStatuses(got)
	if got["status"].(string) != runStatusFailed || st["pause"] != stepStatusFailed {
		t.Fatalf("orphan not failed: run = %s steps = %v", got["status"], st)
	}
}

// movableClock is a Clock a test can advance from another goroutine (the
// manualClock's advance is fine, but a dispatcher hook needs its own handle).
type movableClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *movableClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.t)
}

func (c *movableClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// hookDispatcher runs a hook AFTER recording the fire — a dispatcher that is
// slow but ALIVE, so another advancer can orphan-fail the very step whose side
// effect is still in flight.
type hookDispatcher struct {
	mu    sync.Mutex
	fires int
	hook  func()
}

func (d *hookDispatcher) Fire(context.Context, FireRequest) (DispatchResult, error) {
	d.mu.Lock()
	d.fires++
	d.mu.Unlock()
	if d.hook != nil {
		d.hook()
	}
	return DispatchResult{Ref: "real-dispatch-ref"}, nil
}

// A side effect that lands AFTER the orphan timeout already failed its claim
// must never leave the ledger asserting something untrue. The step's own state
// stays failure-terminal (dependents skip — cascading on an unknown is the
// unsafe direction) and its wording states the outcome is UNKNOWN rather than
// claiming the step did not run; the append-only ledger then carries a
// reconciliation row with what actually happened, including the real dispatch
// reference an operator can correlate against the target system.
func TestWorkflowLateOutcomeIsReconciledIntoTheLedger(t *testing.T) {
	clk := &movableClock{t: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)}
	disp := &hookDispatcher{}
	gate := newRoutedGate()
	h, mod := newHarness(t, WithApprovalGate(gate), WithDispatcher(disp), WithClock(clk))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	mc := h.moduleCtx(tenant)

	// While pass B is inside the dispatcher, time jumps past the executing
	// timeout and a second advancer orphan-fails the claimed step.
	var once sync.Once
	disp.hook = func() {
		once.Do(func() {
			clk.advance(executingTimeout + time.Minute)
			mod.AdvanceWorkflowRuns(context.Background(), mc)
		})
	}

	schedID := h.createAgentSchedule(admin, tenant, "sch", "agent-a")
	wf := h.createWorkflow(admin, tenant, "late-outcome", []map[string]any{
		step("s0", "schedule-fire", map[string]any{"schedule_id": schedID}),
	})
	wfID := wf["id"].(string)
	r2 := h.runToPhase2(gate, admin, tenant, wfID)
	runID := r2.body["run"].(map[string]any)["id"].(string)

	disp.mu.Lock()
	fires := disp.fires
	disp.mu.Unlock()
	if fires != 1 {
		t.Fatalf("dispatcher fired %d times, want exactly 1 (at-most-once)", fires)
	}

	// The step state is honestly uncertain — never a claim that it did not run.
	run := h.getRun(admin, tenant, wfID, runID)
	st := run["steps"].([]any)[0].(map[string]any)
	if st["status"].(string) != stepStatusFailed {
		t.Fatalf("step status = %v, want the conservative failure-terminal state", st["status"])
	}
	if detail, _ := st["detail"].(string); !strings.Contains(detail, "UNKNOWN") {
		t.Errorf("step detail = %q, want it to state the outcome is UNKNOWN, not that the step did not run", detail)
	}

	// The ledger carries the truth: a reconciled row with the REAL dispatch ref.
	var reconciled []model.Record
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(decisionKind)
		if err != nil {
			return err
		}
		recs, _, lerr := repo.List(context.Background(), model.Query{Limit: 200})
		if lerr != nil {
			return lerr
		}
		for _, rec := range recs {
			if rec.String(colOp) == opRunStep && rec.String(colOpStatus) == opStatusReconciled {
				reconciled = append(reconciled, rec)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(reconciled) != 1 {
		t.Fatalf("reconciliation rows = %d, want exactly 1 (a dropped outcome is evidence lost)", len(reconciled))
	}
	if got := reconciled[0].String(colDispatchRef); got != "real-dispatch-ref" {
		t.Errorf("reconciled dispatch_ref = %q, want the actuation's real reference", got)
	}
	if got := reconciled[0].String(colResult); !strings.Contains(got, "late dispatched") {
		t.Errorf("reconciled result = %q, want it to name the late outcome", got)
	}
	// Attribution stays the accountable principal, never the system actor.
	if got := reconciled[0].String(colActor); got == model.ActorSystem || got == "" {
		t.Errorf("reconciled actor = %q, want the run's accountable principal", got)
	}
}
