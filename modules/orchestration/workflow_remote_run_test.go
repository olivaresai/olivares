// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type recordingRemoteWorkExecutor struct {
	mu sync.Mutex

	resultKind       RemoteWorkResultKind
	startOutcome     RemoteWorkOutcome
	observeOutcome   RemoteWorkOutcome
	observeState     string
	observeTerminal  bool
	observeWorkState string
	cancelOutcome    RemoteWorkOutcome
	cancelState      string
	cancelTerminal   bool

	plans         []RemoteWorkPlanRequest
	tests         []RemoteWorkTestRequest
	starts        []RemoteWorkStartRequest
	observes      []RemoteWorkObserveRequest
	cancels       []RemoteWorkCancelRequest
	receipts      map[string]RemoteWorkResult
	startsEmitted int
	nextSeq       int64
	lastStart     RemoteWorkResult
}

func (f *recordingRemoteWorkExecutor) now() string {
	return model.NewTimestamp(time.Date(2026, 8, 18, 20, 0, 0, 0, time.UTC)).String()
}

func (f *recordingRemoteWorkExecutor) nextReceipt(key string, result RemoteWorkResult) RemoteWorkResult {
	if f.receipts == nil {
		f.receipts = make(map[string]RemoteWorkResult)
	}
	if prior, ok := f.receipts[key]; ok {
		return prior
	}
	f.nextSeq++
	result.CommandID, result.EventID, result.EventSeq = model.NewID(), model.NewID(), f.nextSeq
	f.receipts[key] = result
	return result
}

func (f *recordingRemoteWorkExecutor) Plan(
	_ context.Context,
	_ model.TenantID,
	req RemoteWorkPlanRequest,
) (RemoteWorkResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans = append(f.plans, req)
	return RemoteWorkResult{
		Outcome: RemoteWorkClean, Code: "plan_ready", ObservedAt: f.now(),
		PlanHash:      canonicalHash("k5-remote-plan", req.WorkItemID.String(), req.BriefHash),
		BindingSpecID: req.BindingSpecID, BindingSpecGeneration: req.BindingSpecGeneration,
		WorkItemID: req.WorkItemID, OwnerEpoch: req.OwnerEpoch, LeaseFence: req.LeaseFence,
	}, nil
}

func (f *recordingRemoteWorkExecutor) Test(
	_ context.Context,
	_ model.TenantID,
	req RemoteWorkTestRequest,
) (RemoteWorkResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tests = append(f.tests, req)
	return RemoteWorkResult{
		Outcome: RemoteWorkClean, Code: "peer_capable", ObservedAt: f.now(), PlanHash: req.PlanHash,
		BindingSpecID:         req.Plan.BindingSpecID,
		BindingSpecGeneration: req.Plan.BindingSpecGeneration,
		WorkItemID:            req.Plan.WorkItemID, OwnerEpoch: req.Plan.OwnerEpoch, LeaseFence: req.Plan.LeaseFence,
		Checks: []RemoteWorkCheck{{Name: "capability", Outcome: RemoteWorkClean, EvidenceRef: "card:test"}},
	}, nil
}

func (f *recordingRemoteWorkExecutor) Start(
	_ context.Context,
	_ model.TenantID,
	req RemoteWorkStartRequest,
) (RemoteWorkResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, req)
	if prior, ok := f.receipts["start:"+req.IdempotencyKey]; ok {
		return prior, nil
	}
	f.startsEmitted++
	outcome := f.startOutcome
	if outcome == "" {
		outcome = RemoteWorkClean
	}
	kind := f.resultKind
	if kind == "" && outcome == RemoteWorkClean {
		kind = RemoteWorkResultTask
	}
	result := RemoteWorkResult{
		Outcome: outcome, Code: "start_settled", ObservedAt: f.now(),
		PlanHash: req.PlanHash, ApprovalRef: req.ApprovalRef,
		BindingID: model.NewID(), BindingSpecID: req.Plan.BindingSpecID,
		BindingSpecGeneration: req.Plan.BindingSpecGeneration,
		WorkItemID:            req.Plan.WorkItemID, AttemptID: model.NewID(), Generation: 1,
		SyntheticSID: "osn_a2a_synthetic", OwnerEpoch: req.Plan.OwnerEpoch,
		LeaseFence: req.Plan.LeaseFence + 1, ResultKind: kind,
		ExternalContextID: "ctx-1", RemoteState: "working", WireHash: "wire:sha256",
		DetailHash: "detail:sha256", WorkState: "active",
	}
	if kind == RemoteWorkResultTask {
		result.ExternalTaskID = "task-1"
	} else if kind == RemoteWorkResultMessage {
		result.ExternalMessageID = "message-1"
		result.RemoteState, result.Terminal, result.WorkState = "completed", true, "review"
	}
	result = f.nextReceipt("start:"+req.IdempotencyKey, result)
	f.lastStart = result
	return result, nil
}

func (f *recordingRemoteWorkExecutor) Observe(
	_ context.Context,
	_ model.TenantID,
	req RemoteWorkObserveRequest,
) (RemoteWorkResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observes = append(f.observes, req)
	outcome := f.observeOutcome
	if outcome == "" {
		outcome = RemoteWorkClean
	}
	state := f.observeState
	if state == "" {
		state = "working"
	}
	base := f.lastStart
	if base.BindingID.IsZero() {
		base = RemoteWorkResult{
			BindingID: req.BindingID, BindingSpecID: model.NewID(), BindingSpecGeneration: 1,
			WorkItemID: model.NewID(), AttemptID: model.NewID(), Generation: 1,
			SyntheticSID: "osn_a2a_observe", OwnerEpoch: 3, LeaseFence: 7,
			ResultKind: RemoteWorkResultTask, ExternalTaskID: "task-observed",
		}
	}
	base.Outcome, base.Code, base.ObservedAt = outcome, "state_observed", f.now()
	base.RemoteState, base.RemoteRevision = state, "rev-2"
	base.Terminal, base.WorkState = f.observeTerminal, f.observeWorkState
	base.DetailHash = "observe:sha256"
	return f.nextReceipt("observe:"+req.IdempotencyKey, base), nil
}

func (f *recordingRemoteWorkExecutor) Cancel(
	_ context.Context,
	_ model.TenantID,
	req RemoteWorkCancelRequest,
) (RemoteWorkResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, req)
	outcome := f.cancelOutcome
	if outcome == "" {
		outcome = RemoteWorkClean
	}
	state := f.cancelState
	if state == "" {
		state = "working"
	}
	result := f.lastStart
	result.Outcome, result.Code, result.ObservedAt = outcome, "cancel_requested", f.now()
	result.BindingID, result.WorkItemID = req.BindingID, req.WorkItemID
	result.RemoteState, result.Terminal = state, f.cancelTerminal
	result.DetailHash = "cancel:sha256"
	if result.AttemptID.IsZero() {
		result.AttemptID, result.Generation = model.NewID(), 1
		result.SyntheticSID, result.OwnerEpoch, result.LeaseFence = "osn_cancel", 3, 7
		result.BindingSpecID, result.BindingSpecGeneration = model.NewID(), 1
		result.ResultKind, result.ExternalTaskID = RemoteWorkResultTask, "task-cancel"
	}
	return f.nextReceipt("cancel:"+req.IdempotencyKey, result), nil
}

func remotePlanStepConfig(workID, workspaceID, specID model.ID) map[string]any {
	return map[string]any{
		"workspace_id": workspaceID.String(), "work_item_id": workID.String(),
		"binding_spec_id": specID.String(), "binding_spec_generation": 4,
		"protocol": "a2a", "protocol_version": "1.0.1",
		"authority": "peer:reports", "agent_ref": "agent:reporter",
		"skill": "report", "scope": "workspace:read",
		"owner_epoch": 3, "lease_fence": 7, "brief_hash": "sha256:brief",
		"criteria_revision": 2,
	}
}

func TestK5RemoteWorkflowPlanTestStartObserveTaskAndMessage(t *testing.T) {
	for _, kind := range []RemoteWorkResultKind{RemoteWorkResultTask, RemoteWorkResultMessage} {
		t.Run(string(kind), func(t *testing.T) {
			gate := newRoutedGate()
			remote := &recordingRemoteWorkExecutor{resultKind: kind}
			h, _ := newHarness(t, WithApprovalGate(gate), WithRemoteWorkExecutor(remote))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "k5-chain-"+string(kind))
			workID, workspaceID, specID := model.NewID(), model.NewID(), model.NewID()
			steps := []map[string]any{
				step("plan", stepRemotePlan, remotePlanStepConfig(workID, workspaceID, specID)),
				step("test", stepRemoteTest, map[string]any{"plan_step_ref": "plan"}, "plan"),
				step("start", stepRemoteStart, map[string]any{"plan_step_ref": "plan"}, "plan", "test"),
				step("observe", stepRemoteObserve, map[string]any{"binding_step_ref": "start"}, "start"),
			}
			if kind == RemoteWorkResultTask {
				steps = append(steps, step("cancel", stepRemoteCancel, map[string]any{
					"binding_step_ref": "observe", "work_item_id": workID.String(),
					"reason": "Stop the remote task after the lifecycle test.",
				}, "observe"))
			}
			wf := h.createWorkflow(admin, tenant, "remote-chain", steps)
			run := h.runToPhase2(gate, admin, tenant, wf["id"].(string)).body["run"].(map[string]any)
			if run["status"] != runStatusCompleted || run["root_work_item_id"] != workID.String() {
				t.Fatalf("remote chain did not complete: %+v", run)
			}
			plan, testStep := k4RunStep(t, run, "plan"), k4RunStep(t, run, "test")
			start, observe := k4RunStep(t, run, "start"), k4RunStep(t, run, "observe")
			if plan["status"] != stepStatusRemotePlanned || testStep["status"] != stepStatusRemoteTested ||
				start["status"] != stepStatusRemoteStarted || observe["status"] != stepStatusRemoteObserved {
				t.Fatalf("remote statuses plan=%+v test=%+v start=%+v observe=%+v", plan, testStep, start, observe)
			}
			if start["remote_result_kind"] != string(kind) || start["remote_binding_id"] == nil ||
				start["remote_attempt_id"] == nil || start["remote_synthetic_sid"] != "osn_a2a_synthetic" ||
				start["remote_plan_hash"] != plan["remote_plan_hash"] ||
				start["owner_epoch"] != float64(3) || start["lease_fence"] != float64(8) {
				t.Fatalf("remote start tuple was not persisted: plan=%+v start=%+v", plan, start)
			}
			if kind == RemoteWorkResultTask {
				if start["remote_task_id"] != "task-1" || start["remote_message_id"] != nil {
					t.Fatalf("task union projection = %+v", start)
				}
			} else if start["remote_message_id"] != "message-1" || start["remote_task_id"] != nil {
				t.Fatalf("message union projection = %+v", start)
			}
			if len(remote.plans) != 1 || len(remote.tests) != 1 || len(remote.starts) != 1 ||
				len(remote.observes) != 1 || remote.startsEmitted != 1 {
				t.Fatalf("remote calls plan/test/start/observe=%d/%d/%d/%d effects=%d",
					len(remote.plans), len(remote.tests), len(remote.starts), len(remote.observes), remote.startsEmitted)
			}
			if remote.starts[0].PlanHash == "" || remote.starts[0].ApprovalRef == "" ||
				remote.starts[0].ApprovalPlanHash == "" ||
				remote.starts[0].ApprovalAction != "orchestration.workflow.run" ||
				remote.starts[0].ApprovalSubjectKind != "workflow" ||
				remote.starts[0].ApprovalSubjectRef != wf["id"].(string) ||
				remote.starts[0].Plan.Authority != "peer:reports" || remote.starts[0].Plan.WorkItemID != workID {
				t.Fatalf("start did not carry approved plan: %+v", remote.starts[0])
			}
			if kind == RemoteWorkResultTask {
				cancel := k4RunStep(t, run, "cancel")
				if len(remote.cancels) != 1 || cancel["status"] != stepStatusRemoteCancelRequested ||
					cancel["remote_binding_id"] != start["remote_binding_id"] {
					t.Fatalf("task cancellation did not close the chain: calls=%d step=%+v", len(remote.cancels), cancel)
				}
			} else if len(remote.cancels) != 0 {
				t.Fatalf("direct message result was treated as a cancelable Task: %+v", remote.cancels)
			}
		})
	}
}

func TestK5RemoteStartOrphanReusesDurableAttempt(t *testing.T) {
	clock := newManualClock()
	gate := newRoutedGate()
	remote := &recordingRemoteWorkExecutor{resultKind: RemoteWorkResultTask}
	h, mod := newHarness(t, WithClock(clock), WithApprovalGate(gate), WithRemoteWorkExecutor(remote))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k5-remote-restart")
	workID := model.NewID()
	wf := h.createWorkflow(admin, tenant, "restart-remote", []map[string]any{
		step("plan", stepRemotePlan, remotePlanStepConfig(workID, model.NewID(), model.NewID())),
		step("start", stepRemoteStart, map[string]any{"plan_step_ref": "plan"}, "plan"),
	})
	first := h.runToPhase2(gate, admin, tenant, wf["id"].(string)).body["run"].(map[string]any)
	runID := model.ID(first["id"].(string))
	before := k4RunStep(t, first, "start")

	ctx := context.Background()
	mc := h.moduleCtx(tenant)
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(wfRunKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, runID)
		if err != nil {
			return err
		}
		steps, err := decodeRunSteps(rec.String(colWrSteps))
		if err != nil {
			return err
		}
		for i := range steps {
			if steps[i].Ref == "start" {
				steps[i].Status, steps[i].At = stepStatusExecuting, clock.Now().String()
			}
		}
		rec[colWrStatus], rec[colWrFinished], rec[colWrSteps] = runStatusRunning, nil, encodeRunSteps(steps)
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	clock.advance(executingTimeout + time.Second)
	mod.AdvanceWorkflowRuns(ctx, mc)
	afterRun := h.getRun(admin, tenant, wf["id"].(string), runID.String())
	after := k4RunStep(t, afterRun, "start")
	if afterRun["status"] != runStatusCompleted || len(remote.starts) != 2 || remote.startsEmitted != 1 ||
		remote.starts[0].IdempotencyKey != remote.starts[1].IdempotencyKey {
		t.Fatalf("remote restart replayed transport: run=%+v calls=%d effects=%d keys=%q/%q",
			afterRun, len(remote.starts), remote.startsEmitted,
			remote.starts[0].IdempotencyKey, remote.starts[1].IdempotencyKey)
	}
	for _, field := range []string{"remote_binding_id", "remote_attempt_id", "remote_command_id", "remote_event_id", "remote_event_seq", "remote_plan_hash"} {
		if after[field] != before[field] {
			t.Fatalf("restarted %s = %v, want durable value %v", field, after[field], before[field])
		}
	}
}

func TestK5WorkReconcilePersistsThreeWayObservation(t *testing.T) {
	tests := []struct {
		name      string
		outcome   RemoteWorkOutcome
		state     string
		terminal  bool
		workState string
		wantStep  string
		wantRun   string
	}{
		{name: "working", outcome: RemoteWorkClean, state: "working", workState: "active", wantStep: stepStatusReconciled, wantRun: runStatusCompleted},
		{name: "completed_to_review", outcome: RemoteWorkClean, state: "completed", terminal: true, workState: "review", wantStep: stepStatusReconciled, wantRun: runStatusCompleted},
		{name: "remote_failed", outcome: RemoteWorkBroken, state: "failed", terminal: true, workState: "blocked", wantStep: stepStatusFailed, wantRun: runStatusFailed},
		{name: "partition", outcome: RemoteWorkUnknown, state: "unknown", wantStep: stepStatusFailed, wantRun: runStatusFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gate := newRoutedGate()
			bindingID := model.NewID()
			remote := &recordingRemoteWorkExecutor{
				observeOutcome: tc.outcome, observeState: tc.state,
				observeTerminal: tc.terminal, observeWorkState: tc.workState,
			}
			h, _ := newHarness(t, WithApprovalGate(gate), WithRemoteWorkExecutor(remote))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "k5-observe-"+tc.name)
			wf := h.createWorkflow(admin, tenant, "observe-binding", []map[string]any{
				step("observe", stepWorkReconcile, map[string]any{"binding_id": bindingID.String()}),
			})
			run := h.runToPhase2(gate, admin, tenant, wf["id"].(string)).body["run"].(map[string]any)
			observed := k4RunStep(t, run, "observe")
			if run["status"] != tc.wantRun || observed["status"] != tc.wantStep ||
				observed["remote_outcome"] != string(tc.outcome) ||
				observed["remote_binding_id"] != bindingID.String() ||
				observed["remote_state"] != tc.state ||
				(tc.workState != "" && observed["remote_work_state"] != tc.workState) ||
				observed["remote_command_id"] == nil || observed["remote_event_seq"] == nil {
				t.Fatalf("three-way observation was not durable: run=%+v step=%+v", run, observed)
			}
		})
	}
}

func TestK5BoundWorkCancelKeepsLocalAndRemoteReceipts(t *testing.T) {
	gate := newRoutedGate()
	workID, bindingID := model.NewID(), model.NewID()
	work := &recordingK4WorkControl{root: workID}
	remote := &recordingRemoteWorkExecutor{cancelOutcome: RemoteWorkClean}
	h, _ := newHarness(t,
		WithApprovalGate(gate), WithWorkflowWorkControl(work), WithRemoteWorkExecutor(remote),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k5-bound-cancel")
	wf := h.createWorkflow(admin, tenant, "bound-cancel", []map[string]any{
		step("cancel", stepWorkCancel, map[string]any{
			"work_item_id": workID.String(), "binding_id": bindingID.String(),
			"reason": "The operator canceled the remote work.",
		}),
	})
	run := h.runToPhase2(gate, admin, tenant, wf["id"].(string)).body["run"].(map[string]any)
	canceled := k4RunStep(t, run, "cancel")
	if run["status"] != runStatusCompleted || canceled["status"] != stepStatusWorkApplied ||
		len(work.cancel) != 1 || len(remote.cancels) != 1 {
		t.Fatalf("bound cancel did not complete both halves: run=%+v step=%+v local=%d remote=%d",
			run, canceled, len(work.cancel), len(remote.cancels))
	}
	if work.cancel[0].BindingID != "" || remote.cancels[0].BindingID != bindingID ||
		remote.cancels[0].WorkItemID != workID ||
		strings.TrimSuffix(work.cancel[0].IdempotencyKey, ":local") !=
			strings.TrimSuffix(remote.cancels[0].IdempotencyKey, ":remote") {
		t.Fatalf("bound cancel requests = local:%+v remote:%+v", work.cancel[0], remote.cancels[0])
	}
	if canceled["command_id"] == nil || canceled["remote_command_id"] == nil ||
		canceled["remote_binding_id"] != bindingID.String() || canceled["remote_outcome"] != string(RemoteWorkClean) {
		t.Fatalf("bound cancel receipts were not both persisted: %+v", canceled)
	}
}

func TestK5RemoteStartValidatesTaskMessageUnionAndApproval(t *testing.T) {
	plan := RemoteWorkPlanRequest{
		WorkItemID: model.NewID(), BindingSpecID: model.NewID(), BindingSpecGeneration: 2,
		OwnerEpoch: 4, LeaseFence: 9,
	}
	req := RemoteWorkStartRequest{
		Plan: plan, PlanHash: "plan", ApprovalRef: "approval", ApprovalPlanHash: "workflow-plan",
		ApprovalAction: "orchestration.workflow.run", ApprovalSubjectKind: "workflow",
		ApprovalSubjectRef: model.NewID().String(),
	}
	base := RemoteWorkResult{
		Outcome: RemoteWorkClean, Code: "started",
		ObservedAt: model.NewTimestamp(time.Date(2026, 8, 18, 20, 0, 0, 0, time.UTC)).String(),
		PlanHash:   req.PlanHash, ApprovalRef: req.ApprovalRef,
		BindingID: model.NewID(), BindingSpecID: plan.BindingSpecID,
		BindingSpecGeneration: plan.BindingSpecGeneration,
		WorkItemID:            plan.WorkItemID, AttemptID: model.NewID(), Generation: 1,
		SyntheticSID: "osn_union", OwnerEpoch: plan.OwnerEpoch, LeaseFence: plan.LeaseFence + 1,
		CommandID: model.NewID(), EventID: model.NewID(), EventSeq: 1,
	}
	task := base
	task.ResultKind, task.ExternalTaskID = RemoteWorkResultTask, "task-1"
	if err := validateRemoteStartResult(req, task); err != nil {
		t.Fatalf("valid task result rejected: %v", err)
	}
	message := base
	message.ResultKind, message.ExternalMessageID = RemoteWorkResultMessage, "message-1"
	if err := validateRemoteStartResult(req, message); err != nil {
		t.Fatalf("valid message result rejected: %v", err)
	}
	message.ExternalTaskID = "fabricated-task"
	if err := validateRemoteStartResult(req, message); err == nil {
		t.Fatal("direct message carrying a fabricated task id was accepted")
	}
	task.ApprovalRef = "another-approval"
	if err := validateRemoteStartResult(req, task); err == nil {
		t.Fatal("start result from another approval was accepted")
	}
}
