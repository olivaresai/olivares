// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

func remotePlanRequest(run model.Record, step runStepState) (RemoteWorkPlanRequest, error) {
	var cfg remotePlanConfig
	if err := json.Unmarshal(step.Config, &cfg); err != nil {
		return RemoteWorkPlanRequest{}, fmt.Errorf("orchestration: stored remote-plan config is malformed")
	}
	workID, err := resolveRunWorkItem(run, step)
	if err != nil {
		return RemoteWorkPlanRequest{}, err
	}
	workspaceID, err := parseRequiredWorkID(cfg.WorkspaceID, "workspace_id")
	if err != nil {
		return RemoteWorkPlanRequest{}, err
	}
	bindingSpecID, err := parseRequiredWorkID(cfg.BindingSpecID, "binding_spec_id")
	if err != nil {
		return RemoteWorkPlanRequest{}, err
	}
	return RemoteWorkPlanRequest{
		RunRef: run.String(model.ColID), StepRef: step.Ref, Actor: workStepActor(run),
		WorkspaceID: workspaceID, WorkItemID: workID, BindingSpecID: bindingSpecID,
		BindingSpecGeneration: cfg.BindingSpecGeneration,
		Protocol:              cfg.Protocol, ProtocolVersion: cfg.ProtocolVersion,
		Authority: cfg.Authority, AgentRef: cfg.AgentRef, Skill: cfg.Skill, Scope: cfg.Scope,
		OwnerEpoch: cfg.OwnerEpoch, LeaseFence: cfg.LeaseFence,
		BriefHash: cfg.BriefHash, CriteriaRevision: cfg.CriteriaRevision,
	}, nil
}

func remotePlanFromProducer(run model.Record, ref string) (RemoteWorkPlanRequest, runStepState, error) {
	producer, found, err := runStepByRef(run, ref)
	if err != nil {
		return RemoteWorkPlanRequest{}, runStepState{}, err
	}
	if !found || producer.Kind != stepRemotePlan || producer.Status != stepStatusRemotePlanned ||
		producer.RemotePlanHash == "" {
		return RemoteWorkPlanRequest{}, runStepState{}, fmt.Errorf("orchestration: plan_step_ref %s has no completed remote plan", ref)
	}
	plan, err := remotePlanRequest(run, producer)
	if err != nil {
		return RemoteWorkPlanRequest{}, runStepState{}, err
	}
	return plan, producer, nil
}

func resolveRemoteBinding(run model.Record, id, stepRef string) (model.ID, error) {
	if id != "" {
		return parseRequiredWorkID(id, "binding_id")
	}
	producer, found, err := runStepByRef(run, stepRef)
	if err != nil {
		return "", err
	}
	if !found || !stepOK(producer.Status) || producer.RemoteBindingID == "" {
		return "", fmt.Errorf("orchestration: binding_step_ref %s has no completed remote binding", stepRef)
	}
	return parseRequiredWorkID(producer.RemoteBindingID, "upstream binding_id")
}

func projectRemoteResult(out *runOutcome, result RemoteWorkResult) {
	out.remoteOutcome = string(result.Outcome)
	out.remoteCode = result.Code
	out.remoteObservedAt = result.ObservedAt
	out.remotePlanHash = result.PlanHash
	out.remoteApprovalRef = result.ApprovalRef
	out.remoteBindingID = result.BindingID.String()
	out.remoteBindingSpecID = result.BindingSpecID.String()
	out.remoteBindingSpecGeneration = result.BindingSpecGeneration
	out.remoteAttemptID = result.AttemptID.String()
	out.remoteGeneration = result.Generation
	out.remoteSyntheticSID = result.SyntheticSID
	out.remoteResultKind = string(result.ResultKind)
	out.remoteTaskID = result.ExternalTaskID
	out.remoteContextID = result.ExternalContextID
	out.remoteMessageID = result.ExternalMessageID
	out.remoteState = result.RemoteState
	out.remoteRevision = result.RemoteRevision
	out.remoteTerminal = result.Terminal
	out.remoteWireHash = result.WireHash
	out.remoteDetailHash = result.DetailHash
	out.remoteCommandID = result.CommandID.String()
	out.remoteEventID = result.EventID.String()
	out.remoteEventSeq = result.EventSeq
	out.remoteWorkState = result.WorkState
	if !result.WorkItemID.IsZero() {
		out.workItemID = result.WorkItemID.String()
	}
	if result.OwnerEpoch > 0 {
		out.ownerEpoch = result.OwnerEpoch
	}
	if result.LeaseFence > 0 {
		out.leaseFence = result.LeaseFence
	}
	if !result.CommandID.IsZero() {
		out.dispatchRef = result.CommandID.String()
	}
	if !result.BindingID.IsZero() {
		out.outputKind, out.outputID = "binding", result.BindingID.String()
	}
}

func remoteResultDetail(operation string, result RemoteWorkResult) string {
	detail := "remote " + operation + " " + string(result.Outcome) + " (" + result.Code + ")"
	if strings.TrimSpace(result.Detail) != "" {
		detail += ": " + clamp(strings.TrimSpace(result.Detail), 120)
	}
	return detail
}

func remoteErrorOutcome(step runStepState, operation string, err error) runOutcome {
	out := failK4Outcome(step, "remote "+operation+" outcome UNKNOWN: "+clamp(err.Error(), 120))
	out.remoteOutcome, out.remoteCode = string(RemoteWorkUnknown), "executor_error"
	return out
}

func remoteResultOutcome(
	step runStepState,
	operation string,
	result RemoteWorkResult,
	successStatus string,
	requireReceipt bool,
) runOutcome {
	if err := validateRemoteResult(operation, result, requireReceipt); err != nil {
		return remoteErrorOutcome(step, operation, err)
	}
	out := baseK4Outcome(step)
	projectRemoteResult(&out, result)
	out.detail = remoteResultDetail(operation, result)
	switch result.Outcome {
	case RemoteWorkClean:
		out.status, out.ledgerOp = successStatus, opStatusDispatched
	case RemoteWorkBroken:
		out.status, out.ledgerOp = stepStatusFailed, opStatusFailed
	case RemoteWorkUnknown:
		// UNKNOWN is terminal for this semantic workflow attempt. A new
		// work-reconcile step may Observe the binding, but this effect is never
		// blindly repeated after the ambiguous point.
		out.status, out.ledgerOp = stepStatusFailed, opStatusFailed
	}
	return out
}

func (m *Module) executeRemoteWorkStep(
	ctx context.Context,
	mc api.ModuleContext,
	run model.Record,
	step runStepState,
) runOutcome {
	runRef := run.String(model.ColID)
	actor := workStepActor(run)
	idempotency := workStepIdempotency(run, step)

	switch step.Kind {
	case stepRemotePlan:
		request, err := remotePlanRequest(run, step)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		result, err := m.remoteWork.Plan(ctx, mc.Tenant, request)
		if err != nil {
			return remoteErrorOutcome(step, "plan", err)
		}
		out := remoteResultOutcome(step, "plan", result, stepStatusRemotePlanned, false)
		if out.status == stepStatusRemotePlanned {
			if strings.TrimSpace(result.PlanHash) == "" || result.WorkItemID != request.WorkItemID ||
				result.BindingSpecID != request.BindingSpecID ||
				result.BindingSpecGeneration != request.BindingSpecGeneration ||
				result.OwnerEpoch != request.OwnerEpoch || result.LeaseFence != request.LeaseFence {
				return remoteErrorOutcome(step, "plan", fmt.Errorf("remote plan changed or omitted the requested work tuple"))
			}
			out.outputKind, out.outputID = "remote_plan", result.PlanHash
		}
		return out

	case stepRemoteTest:
		var cfg remoteTestConfig
		_ = json.Unmarshal(step.Config, &cfg)
		plan, producer, err := remotePlanFromProducer(run, cfg.PlanStepRef)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		result, err := m.remoteWork.Test(ctx, mc.Tenant, RemoteWorkTestRequest{
			RunRef: runRef, StepRef: step.Ref, Actor: actor,
			Plan: plan, PlanHash: producer.RemotePlanHash,
		})
		if err != nil {
			return remoteErrorOutcome(step, "test", err)
		}
		out := remoteResultOutcome(step, "test", result, stepStatusRemoteTested, false)
		if out.status == stepStatusRemoteTested && result.PlanHash != producer.RemotePlanHash {
			return remoteErrorOutcome(step, "test", fmt.Errorf("remote test does not match its durable plan"))
		}
		out.workItemID, out.ownerEpoch, out.leaseFence = producer.WorkItemID, producer.OwnerEpoch, producer.LeaseFence
		out.outputKind, out.outputID = "remote_test", producer.RemotePlanHash
		return out

	case stepRemoteStart:
		var cfg remoteStartConfig
		_ = json.Unmarshal(step.Config, &cfg)
		plan, producer, err := remotePlanFromProducer(run, cfg.PlanStepRef)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		request := RemoteWorkStartRequest{
			RunRef: runRef, StepRef: step.Ref, Actor: actor, IdempotencyKey: idempotency,
			Plan: plan, PlanHash: producer.RemotePlanHash, ApprovalRef: run.String(colWrApproval),
			ApprovalPlanHash: run.String(colWrPlanHash),
			ApprovalAction:   "orchestration.workflow.run", ApprovalSubjectKind: "workflow",
			ApprovalSubjectRef: run.String(colWrWorkflow),
		}
		result, err := m.remoteWork.Start(ctx, mc.Tenant, request)
		if err != nil {
			return remoteErrorOutcome(step, "start", err)
		}
		if err := validateRemoteStartResult(request, result); err != nil {
			return remoteErrorOutcome(step, "start", err)
		}
		return remoteResultOutcome(step, "start", result, stepStatusRemoteStarted, true)

	case stepRemoteObserve:
		var cfg remoteBindingConfig
		_ = json.Unmarshal(step.Config, &cfg)
		bindingID, err := resolveRemoteBinding(run, cfg.BindingID, cfg.BindingStepRef)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		result, err := m.remoteWork.Observe(ctx, mc.Tenant, RemoteWorkObserveRequest{
			RunRef: runRef, StepRef: step.Ref, Actor: actor,
			IdempotencyKey: idempotency, BindingID: bindingID,
		})
		if err != nil {
			return remoteErrorOutcome(step, "observe", err)
		}
		if result.BindingID != bindingID {
			return remoteErrorOutcome(step, "observe", fmt.Errorf("remote observation returned another binding"))
		}
		return remoteResultOutcome(step, "observe", result, stepStatusRemoteObserved, true)

	case stepRemoteCancel:
		var cfg remoteCancelConfig
		_ = json.Unmarshal(step.Config, &cfg)
		bindingID, err := resolveRemoteBinding(run, cfg.BindingID, cfg.BindingStepRef)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		workID, err := resolveRunWorkItem(run, step)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		result, err := m.remoteWork.Cancel(ctx, mc.Tenant, RemoteWorkCancelRequest{
			RunRef: runRef, StepRef: step.Ref, Actor: actor, IdempotencyKey: idempotency,
			BindingID: bindingID, WorkItemID: workID, Reason: cfg.Reason,
		})
		if err != nil {
			return remoteErrorOutcome(step, "cancel", err)
		}
		if result.BindingID != bindingID || result.WorkItemID != workID {
			return remoteErrorOutcome(step, "cancel", fmt.Errorf("remote cancellation returned another work binding"))
		}
		status := stepStatusRemoteCancelRequested
		if result.Terminal && result.RemoteState == "canceled" {
			status = stepStatusRemoteCanceled
		}
		return remoteResultOutcome(step, "cancel", result, status, true)
	}
	return failK4Outcome(step, "unknown remote work step "+step.Kind)
}

// executeBoundWorkCancel preserves work-cancel's local decision and adds the
// external lifecycle request. The local command and the remote cancel each use
// a stable child key, so a crash in either half replays durable receipts rather
// than repeating an effect. A remote UNKNOWN leaves the WorkItem locally
// canceled but fails the workflow step with the binding outcome visible.
func (m *Module) executeBoundWorkCancel(
	ctx context.Context,
	mc api.ModuleContext,
	step runStepState,
	runRef string,
	actor WorkActor,
	idempotency string,
	workID, bindingID model.ID,
	reason string,
) runOutcome {
	if _, unwired := m.remoteWork.(unwiredRemoteWorkExecutor); unwired {
		return failK4Outcome(step, "work-cancel cannot complete external binding cancellation: "+ErrRemoteWorkExecutorUnwired.Error())
	}
	local, err := m.workflowWork.Cancel(ctx, mc.Tenant, WorkCancelRequest{
		RunRef: runRef, StepRef: step.Ref, Actor: actor,
		IdempotencyKey: idempotency + ":local", WorkItemID: workID, Reason: reason,
	})
	if err != nil {
		return failK4Outcome(step, "work-cancel failed: "+clamp(err.Error(), 160))
	}
	out := commandOutcome(step, local, "WorkItem canceled locally")
	if out.status != stepStatusWorkApplied {
		return out
	}
	localOutputKind, localOutputID := out.outputKind, out.outputID
	localDispatchRef := out.dispatchRef
	remote, err := m.remoteWork.Cancel(ctx, mc.Tenant, RemoteWorkCancelRequest{
		RunRef: runRef, StepRef: step.Ref, Actor: actor,
		IdempotencyKey: idempotency + ":remote", BindingID: bindingID,
		WorkItemID: workID, Reason: reason,
	})
	if err != nil {
		out.status, out.ledgerOp = stepStatusFailed, opStatusFailed
		out.detail = "WorkItem canceled locally; remote cancel outcome UNKNOWN: " + clamp(err.Error(), 120)
		out.remoteOutcome, out.remoteCode = string(RemoteWorkUnknown), "executor_error"
		return out
	}
	if remote.BindingID != bindingID || remote.WorkItemID != workID {
		out.status, out.ledgerOp = stepStatusFailed, opStatusFailed
		out.detail = "WorkItem canceled locally; remote cancellation returned another work binding"
		out.remoteOutcome, out.remoteCode = string(RemoteWorkUnknown), "binding_mismatch"
		return out
	}
	if err := validateRemoteResult("cancel", remote, true); err != nil {
		out.status, out.ledgerOp = stepStatusFailed, opStatusFailed
		out.detail = "WorkItem canceled locally; " + err.Error()
		out.remoteOutcome, out.remoteCode = string(RemoteWorkUnknown), "invalid_receipt"
		return out
	}
	projectRemoteResult(&out, remote)
	// The standard output remains the local WorkItem command. The remote receipt
	// has dedicated snapshot fields and must not hide which local command made
	// the cancellation decision.
	out.outputKind, out.outputID, out.dispatchRef = localOutputKind, localOutputID, localDispatchRef
	switch remote.Outcome {
	case RemoteWorkClean:
		out.status, out.ledgerOp = stepStatusWorkApplied, opStatusDispatched
		if remote.Terminal && remote.RemoteState == "canceled" {
			out.detail = "WorkItem canceled locally and remote cancellation confirmed"
		} else {
			out.detail = "WorkItem canceled locally; remote cancellation requested"
		}
	case RemoteWorkBroken, RemoteWorkUnknown:
		out.status, out.ledgerOp = stepStatusFailed, opStatusFailed
		out.detail = "WorkItem canceled locally; " + remoteResultDetail("cancel", remote)
	}
	return out
}
