// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type recordingK4WorkControl struct {
	root       model.ID
	create     []WorkCreateRequest
	created    map[string]WorkCommandResult
	assign     []WorkAssignRequest
	claim      []WorkClaimRequest
	transition []WorkTransitionRequest
	cancel     []WorkCancelRequest
	nextSeq    int64
}

func (f *recordingK4WorkControl) result(ownerEpoch, fence int64, kind, output string) WorkCommandResult {
	f.nextSeq++
	if output == "" {
		output = f.root.String()
	}
	return WorkCommandResult{
		WorkItemID: f.root, CommandID: model.NewID(), EventID: model.NewID(), EventSeq: f.nextSeq,
		OutputKind: kind, OutputID: output, Version: f.nextSeq,
		OwnerEpoch: ownerEpoch, LeaseFence: fence, State: "active",
	}
}

func (f *recordingK4WorkControl) Create(_ context.Context, _ model.TenantID, req WorkCreateRequest) (WorkCommandResult, error) {
	f.create = append(f.create, req)
	if result, ok := f.created[req.IdempotencyKey]; ok {
		return result, nil
	}
	if f.created == nil {
		f.created = make(map[string]WorkCommandResult)
	}
	result := f.result(1, 0, "work_item", "")
	f.created[req.IdempotencyKey] = result
	return result, nil
}

func (f *recordingK4WorkControl) Assign(_ context.Context, _ model.TenantID, req WorkAssignRequest) (WorkCommandResult, error) {
	f.assign = append(f.assign, req)
	return f.result(2, 0, "work_item", ""), nil
}

func (f *recordingK4WorkControl) Claim(_ context.Context, _ model.TenantID, req WorkClaimRequest) (WorkCommandResult, error) {
	f.claim = append(f.claim, req)
	return f.result(2, 9, "lease", model.NewID().String()), nil
}

func (f *recordingK4WorkControl) Transition(_ context.Context, _ model.TenantID, req WorkTransitionRequest) (WorkCommandResult, error) {
	f.transition = append(f.transition, req)
	return f.result(2, 0, "work_item", ""), nil
}

func (f *recordingK4WorkControl) Cancel(_ context.Context, _ model.TenantID, req WorkCancelRequest) (WorkCommandResult, error) {
	f.cancel = append(f.cancel, req)
	return f.result(2, 0, "work_item", ""), nil
}

type recordingK4Runtime struct {
	requests []WorkLaunchRequest
}

func (f *recordingK4Runtime) LaunchForWork(_ context.Context, _ model.TenantID, req WorkLaunchRequest) (ManagedWorkRun, error) {
	f.requests = append(f.requests, req)
	ownerEpoch := req.OwnerEpoch
	if ownerEpoch == 0 {
		ownerEpoch = 2
	}
	fence := req.Fence
	if fence == 0 {
		fence = 11
	}
	return ManagedWorkRun{
		WorkItemID: req.WorkItemID, OwnerEpoch: ownerEpoch, LeaseFence: fence,
		RunRef: "managed-run-1", SID: "osn_managed", DispatchKey: "dispatch-key-1",
	}, nil
}

type recordingK4MessageControl struct {
	messageID model.ID
	requests  []WorkMessageRequest
}

type recordingK4HandoffControl struct {
	handoffID model.ID
	requests  []WorkHandoffRequest
}

func (f *recordingK4HandoffControl) OfferWorkHandoff(
	_ context.Context,
	_ model.TenantID,
	req WorkHandoffRequest,
) (WorkHandoffResult, error) {
	f.requests = append(f.requests, req)
	ownerEpoch := req.ExpectedOwnerEpoch
	if ownerEpoch == 0 {
		ownerEpoch = 1
	}
	return WorkHandoffResult{
		WorkItemID: req.WorkItemID, HandoffID: f.handoffID, MessageID: model.NewID(),
		CommandID: model.NewID(), EventID: model.NewID(), EventSeq: 7,
		OwnerEpoch: ownerEpoch,
	}, nil
}

func (f *recordingK4MessageControl) SendWorkMessage(_ context.Context, _ model.TenantID, req WorkMessageRequest) (WorkMessageResult, error) {
	f.requests = append(f.requests, req)
	return WorkMessageResult{
		WorkItemID: req.WorkItemID, MessageID: f.messageID,
		CommandID: model.NewID(), EventID: model.NewID(), EventSeq: 10,
	}, nil
}

type recordingK4AckReader struct {
	acknowledged bool
	queries      []WorkAckQuery
}

func (f *recordingK4AckReader) ObserveWorkAck(_ context.Context, _ model.TenantID, query WorkAckQuery) (WorkAckObservation, error) {
	f.queries = append(f.queries, query)
	if f.acknowledged {
		return WorkAckObservation{
			Status: WorkAckAcknowledged, AckID: model.NewID(), EventID: model.NewID(), EventSeq: 12,
		}, nil
	}
	if query.AfterEventSeq < 11 {
		return WorkAckObservation{Status: WorkAckPending, EventSeq: 11}, nil
	}
	return WorkAckObservation{Status: WorkAckPending}, nil
}

func k4CreateStepConfig() map[string]any {
	return map[string]any{
		"workspace_id": model.NewID().String(), "work_kind": "implementation",
		"title": "Dynamic root", "brief_ref": "briefs/root", "priority": "p1",
		"owner": map[string]any{"kind": "session", "ref": "osn_worker"},
		"criteria": []map[string]any{{
			"key": "complete", "ordinal": 1, "statement": "The work completes.", "required": true,
		}},
		"provenance": map[string]any{"kind": "workflow", "ref": "k4-chain"},
	}
}

func k4RunStep(t *testing.T, run map[string]any, ref string) map[string]any {
	t.Helper()
	for _, raw := range run["steps"].([]any) {
		step := raw.(map[string]any)
		if step["ref"] == ref {
			return step
		}
	}
	t.Fatalf("step %s not found in run", ref)
	return nil
}

func TestK4WorkflowCreateAssignLaunchResolvesDynamicRoot(t *testing.T) {
	gate := newRoutedGate()
	work := &recordingK4WorkControl{root: model.NewID()}
	runtime := &recordingK4Runtime{}
	h, _ := newHarness(t,
		WithApprovalGate(gate), WithWorkflowWorkControl(work), WithWorkflowRuntimeControl(runtime),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k4-chain")

	wf := h.createWorkflow(admin, tenant, "dynamic-root", []map[string]any{
		step("create", stepWorkCreate, k4CreateStepConfig()),
		step("assign", stepWorkAssign, map[string]any{
			"work_item_step_ref": "create", "expected_owner_epoch": 1,
			"target":      map[string]any{"kind": "session", "ref": "osn_managed"},
			"require_ack": false,
		}, "create"),
		step("launch", stepSessionLaunch, map[string]any{
			"work_item_step_ref": "assign", "runtime_profile_ref": "runtime/default",
		}, "assign"),
	})
	run := h.runToPhase2(gate, admin, tenant, wf["id"].(string)).body["run"].(map[string]any)

	if run["status"] != runStatusCompleted || run["root_work_item_id"] != work.root.String() {
		t.Fatalf("run did not complete with its dynamic root: %+v", run)
	}
	if len(work.create) != 1 || len(work.assign) != 1 || len(runtime.requests) != 1 {
		t.Fatalf("calls create=%d assign=%d launch=%d", len(work.create), len(work.assign), len(runtime.requests))
	}
	if work.assign[0].WorkItemID != work.root || runtime.requests[0].WorkItemID != work.root ||
		runtime.requests[0].OwnerEpoch != 0 || runtime.requests[0].Fence != 0 ||
		runtime.requests[0].AttemptKind != "lease-bind" {
		t.Fatalf("dynamic request chain mismatch: assign=%+v launch=%+v", work.assign[0], runtime.requests[0])
	}
	for ref, want := range map[string]string{
		"create": stepStatusWorkApplied, "assign": stepStatusWorkApplied, "launch": stepStatusLaunched,
	} {
		got := k4RunStep(t, run, ref)
		if got["status"] != want || got["work_item_id"] != work.root.String() {
			t.Fatalf("step %s = %+v, want status=%s and root", ref, got, want)
		}
		if !strings.HasSuffix(got["attempt_semantic"].(string), "primary") {
			t.Fatalf("step %s attempt semantic = %v", ref, got["attempt_semantic"])
		}
	}
	launch := k4RunStep(t, run, "launch")
	if launch["owner_epoch"] != float64(2) || launch["lease_fence"] != float64(11) ||
		launch["output_id"] != "managed-run-1" {
		t.Fatalf("launch output not persisted: %+v", launch)
	}
	if !strings.HasSuffix(work.create[0].IdempotencyKey, ":create:primary") ||
		!strings.HasSuffix(runtime.requests[0].IdempotencyKey, ":launch:primary") {
		t.Fatalf("semantic idempotency keys: create=%q launch=%q", work.create[0].IdempotencyKey, runtime.requests[0].IdempotencyKey)
	}
}

func TestK4WorkflowCreateClaimPersistsFenceOutput(t *testing.T) {
	gate := newRoutedGate()
	work := &recordingK4WorkControl{root: model.NewID()}
	h, _ := newHarness(t, WithApprovalGate(gate), WithWorkflowWorkControl(work))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k4-claim")
	wf := h.createWorkflow(admin, tenant, "dynamic-claim", []map[string]any{
		step("create", stepWorkCreate, k4CreateStepConfig()),
		step("claim", stepWorkClaim, map[string]any{
			"work_item_step_ref": "create", "sid": "osn_existing", "ttl_seconds": 300,
		}, "create"),
	})
	run := h.runToPhase2(gate, admin, tenant, wf["id"].(string)).body["run"].(map[string]any)
	claim := k4RunStep(t, run, "claim")
	if run["status"] != runStatusCompleted || run["root_work_item_id"] != work.root.String() ||
		claim["work_item_id"] != work.root.String() || claim["lease_fence"] != float64(9) ||
		claim["event_seq"] != float64(2) {
		t.Fatalf("claim output/root not persisted: run=%+v claim=%+v", run, claim)
	}
}

func TestK4WorkflowOrphanRecoveryReusesDurableCommandReceipt(t *testing.T) {
	clock := newManualClock()
	gate := newRoutedGate()
	work := &recordingK4WorkControl{root: model.NewID()}
	h, mod := newHarness(t,
		WithClock(clock), WithApprovalGate(gate), WithWorkflowWorkControl(work),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k4-orphan-recovery")
	wf := h.createWorkflow(admin, tenant, "recover-create", []map[string]any{
		step("create", stepWorkCreate, k4CreateStepConfig()),
	})
	first := h.runToPhase2(gate, admin, tenant, wf["id"].(string)).body["run"].(map[string]any)
	runID := model.ID(first["id"].(string))
	before := k4RunStep(t, first, "create")
	if len(work.create) != 1 || before["status"] != stepStatusWorkApplied {
		t.Fatalf("initial create did not complete exactly once: calls=%d step=%+v", len(work.create), before)
	}

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
		steps[0].Status = stepStatusExecuting
		steps[0].At = clock.Now().String()
		steps[0].Detail = "claim persisted before worker restart"
		rec[colWrStatus] = runStatusRunning
		rec[colWrFinished] = nil
		rec[colWrSteps] = encodeRunSteps(steps)
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	clock.advance(executingTimeout + time.Second)
	mod.AdvanceWorkflowRuns(ctx, mc)
	got := h.getRun(admin, tenant, wf["id"].(string), runID.String())
	after := k4RunStep(t, got, "create")
	if got["status"] != runStatusCompleted || after["status"] != stepStatusWorkApplied {
		t.Fatalf("K4 orphan did not reconcile through its durable command: run=%+v step=%+v", got, after)
	}
	if len(work.create) != 2 || work.create[0].IdempotencyKey != work.create[1].IdempotencyKey ||
		work.nextSeq != 1 {
		t.Fatalf("recovery did not reuse one semantic receipt: calls=%d keys=%q/%q effects=%d",
			len(work.create), work.create[0].IdempotencyKey, work.create[1].IdempotencyKey, work.nextSeq)
	}
	for _, field := range []string{"work_item_id", "command_id", "event_seq", "output_kind", "output_id", "owner_epoch"} {
		if after[field] != before[field] {
			t.Fatalf("recovered %s = %v, want original receipt value %v", field, after[field], before[field])
		}
	}
}

func TestK4WorkflowAssignWithAckUsesDurableHandoff(t *testing.T) {
	gate := newRoutedGate()
	workID, channelID, handoffID := model.NewID(), model.NewID(), model.NewID()
	handoff := &recordingK4HandoffControl{handoffID: handoffID}
	h, _ := newHarness(t, WithApprovalGate(gate), WithWorkflowHandoffControl(handoff))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k4-assign-handoff")
	deadline := model.NewTimestamp(time.Now().Add(time.Hour)).String()
	wf := h.createWorkflow(admin, tenant, "acknowledged-assignment", []map[string]any{
		step("assign", stepWorkAssign, map[string]any{
			"work_item_id": workID.String(), "expected_owner_epoch": 4,
			"target":      map[string]any{"kind": "agent", "ref": "agent:successor"},
			"require_ack": true, "channel_id": channelID.String(),
			"context": "Transfer the active implementation context.", "ack_deadline": deadline,
		}),
	})
	run := h.runToPhase2(gate, admin, tenant, wf["id"].(string)).body["run"].(map[string]any)
	assign := k4RunStep(t, run, "assign")
	if run["status"] != runStatusCompleted || assign["status"] != stepStatusHandoff ||
		assign["output_kind"] != "handoff" || assign["output_id"] != handoffID.String() ||
		len(handoff.requests) != 1 {
		t.Fatalf("acknowledged assignment = run:%+v step:%+v calls:%d", run, assign, len(handoff.requests))
	}
	request := handoff.requests[0]
	if request.WorkItemID != workID || request.ExpectedOwnerEpoch != 4 || request.ChannelID != channelID ||
		request.Target != (WorkParticipant{Kind: "agent", Ref: "agent:successor"}) ||
		request.Context != "Transfer the active implementation context." || request.AckDeadline != deadline {
		t.Fatalf("handoff request = %+v", request)
	}
}

func TestK4WorkflowWaitAckResumesFromDurableCursor(t *testing.T) {
	gate := newRoutedGate()
	workID, channelID, messageID := model.NewID(), model.NewID(), model.NewID()
	messages := &recordingK4MessageControl{messageID: messageID}
	acks := &recordingK4AckReader{}
	h, mod := newHarness(t,
		WithApprovalGate(gate), WithWorkflowMessageControl(messages), WithWorkflowAckReader(acks),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k4-wait")
	deadline := model.NewTimestamp(time.Now().Add(time.Hour)).String()
	wf := h.createWorkflow(admin, tenant, "durable-wait", []map[string]any{
		step("message", stepWorkMessage, map[string]any{
			"work_item_id": workID.String(), "channel_id": channelID.String(),
			"recipient": map[string]any{"kind": "session", "ref": "osn_receiver"},
			"body":      "Please acknowledge.", "ack_due_at": deadline,
		}),
		step("wait", stepWorkWaitAck, map[string]any{
			"target_kind": "message", "target_step_ref": "message", "deadline": deadline,
		}, "message"),
	})
	run := h.runToPhase2(gate, admin, tenant, wf["id"].(string)).body["run"].(map[string]any)
	wait := k4RunStep(t, run, "wait")
	if run["status"] != runStatusRunning || wait["status"] != stepStatusWaitingAck ||
		wait["waiting_target_id"] != messageID.String() || wait["waiting_after_event_seq"] != float64(11) {
		t.Fatalf("durable wait snapshot = %+v", wait)
	}

	acks.acknowledged = true
	mod.AdvanceWorkflowRuns(context.Background(), h.moduleCtx(tenant))
	completed := h.getRun(admin, tenant, wf["id"].(string), run["id"].(string))
	wait = k4RunStep(t, completed, "wait")
	if completed["status"] != runStatusCompleted || wait["status"] != stepStatusAcked ||
		wait["waiting_after_event_seq"] != float64(12) || wait["output_kind"] != "ack" {
		t.Fatalf("resumed wait = %+v run=%+v", wait, completed)
	}
	if len(acks.queries) < 2 || acks.queries[len(acks.queries)-1].AfterEventSeq != 11 ||
		acks.queries[len(acks.queries)-1].Actor.Ref == "" {
		t.Fatalf("ack cursor/actor was not resumed exactly: %+v", acks.queries)
	}
}

func TestK4WorkflowPortsFailClosedWhenUnwired(t *testing.T) {
	gate := newRoutedGate()
	h, _ := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k4-unwired")
	wf := h.createWorkflow(admin, tenant, "unwired", []map[string]any{
		step("create", stepWorkCreate, k4CreateStepConfig()),
	})
	run := h.runToPhase2(gate, admin, tenant, wf["id"].(string)).body["run"].(map[string]any)
	created := k4RunStep(t, run, "create")
	if run["status"] != runStatusFailed || created["status"] != stepStatusFailed ||
		run["root_work_item_id"] != nil || !strings.Contains(created["detail"].(string), "not wired") {
		t.Fatalf("unwired port did not fail closed: run=%+v step=%+v", run, created)
	}
}

func TestK4WorkActorProjectsOnlyDurableInitiatorAuthority(t *testing.T) {
	run := model.Record{
		colWrActorKind: "token", colWrActor: "token:credential",
		colWrActorAdmin: true, colWrUserIdentity: model.NewID().String(),
		colWrAgentIdentity: "agent:builder", colWrSessionIdentity: "osn_worker",
		colWrSessionRunRef: model.NewID().String(), colWrSessionFence: int64(17),
		colWrPurposeRestricted: true,
	}
	actor := workStepActor(run)
	if actor.Kind != "token" || actor.Ref != "token:credential" || !actor.Admin ||
		actor.UserIdentity != model.ID(run.String(colWrUserIdentity)) ||
		actor.AgentIdentity != "agent:builder" || actor.SessionIdentity != "osn_worker" ||
		actor.SessionRunRef != run.String(colWrSessionRunRef) || actor.SessionFence != 17 ||
		!actor.PurposeRestricted {
		t.Fatalf("durable actor projection = %+v", actor)
	}
}

func TestK4WorkflowCancelWithBindingDoesNotReportLocalCompletion(t *testing.T) {
	gate := newRoutedGate()
	work := &recordingK4WorkControl{root: model.NewID()}
	h, _ := newHarness(t, WithApprovalGate(gate), WithWorkflowWorkControl(work))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "k4-cancel-binding")
	wf := h.createWorkflow(admin, tenant, "cancel-bound-work", []map[string]any{
		step("cancel", stepWorkCancel, map[string]any{
			"work_item_id": work.root.String(), "binding_id": model.NewID().String(),
			"reason": "The operator canceled the bound work.",
		}),
	})
	run := h.runToPhase2(gate, admin, tenant, wf["id"].(string)).body["run"].(map[string]any)
	cancel := k4RunStep(t, run, "cancel")
	if run["status"] != runStatusFailed || cancel["status"] != stepStatusFailed ||
		len(work.cancel) != 0 || !strings.Contains(cancel["detail"].(string), "external binding cancellation") {
		t.Fatalf("bound cancellation claimed a local-only success: run=%+v step=%+v calls=%d", run, cancel, len(work.cancel))
	}
}
