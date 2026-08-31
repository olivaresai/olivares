// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

func workStepActor(run model.Record) WorkActor {
	return WorkActor{
		Kind: run.String(colWrActorKind), Ref: run.String(colWrActor), Admin: run.Bool(colWrActorAdmin),
		UserIdentity:  model.ID(run.String(colWrUserIdentity)),
		AgentIdentity: run.String(colWrAgentIdentity), SessionIdentity: run.String(colWrSessionIdentity),
		SessionRunRef: run.String(colWrSessionRunRef), SessionFence: run.Int(colWrSessionFence),
		PurposeRestricted: run.Bool(colWrPurposeRestricted),
	}
}

// workStepIdempotency is semantic, not transport-attempt based. A retry after
// restart reaches the same neighbor command receipt instead of producing a new
// effect merely because another worker transported it.
func workStepIdempotency(run model.Record, step runStepState) string {
	semantic := step.AttemptSemantic
	if semantic == "" {
		semantic = "primary"
	}
	return run.String(model.ColID) + ":" + step.Ref + ":" + semantic
}

func parseRequiredWorkID(raw, field string) (model.ID, error) {
	id, err := model.ParseID(raw)
	if err != nil || id.IsZero() {
		return "", fmt.Errorf("orchestration: %s is not a durable ID", field)
	}
	return id, nil
}

func runStepByRef(run model.Record, ref string) (runStepState, bool, error) {
	steps, err := decodeRunSteps(run.String(colWrSteps))
	if err != nil {
		return runStepState{}, false, err
	}
	for _, step := range steps {
		if step.Ref == ref {
			return step, true, nil
		}
	}
	return runStepState{}, false, nil
}

func resolveRunWorkItem(run model.Record, step runStepState) (model.ID, error) {
	if step.WorkItemID != "" {
		return parseRequiredWorkID(step.WorkItemID, "step work_item_id")
	}
	selector := stepWorkItemStepRef(stepDTO{Kind: step.Kind, Config: step.Config})
	if selector != "" {
		producer, found, err := runStepByRef(run, selector)
		if err != nil {
			return "", err
		}
		if !found || !stepOK(producer.Status) || producer.WorkItemID == "" {
			return "", fmt.Errorf("orchestration: work_item_step_ref %s has no completed WorkItem output", selector)
		}
		return parseRequiredWorkID(producer.WorkItemID, "upstream work_item_id")
	}
	return parseRequiredWorkID(run.String(colWrRootWork), "root_work_item_id")
}

func resolveRunFence(run model.Record, cfg sessionLaunchConfig) (int64, error) {
	if cfg.Fence > 0 {
		return cfg.Fence, nil
	}
	if cfg.FenceStepRef == "" {
		return 0, nil
	}
	producer, found, err := runStepByRef(run, cfg.FenceStepRef)
	if err != nil {
		return 0, err
	}
	if !found || producer.Status != stepStatusWorkApplied || producer.LeaseFence < 1 {
		return 0, fmt.Errorf("orchestration: fence_step_ref %s has no completed lease fence", cfg.FenceStepRef)
	}
	return producer.LeaseFence, nil
}

func baseK4Outcome(step runStepState) runOutcome {
	return runOutcome{
		ref: step.Ref, fromStatus: stepStatusExecuting, ledger: true,
		ledgerOp: opStatusDispatched,
	}
}

func failK4Outcome(step runStepState, detail string) runOutcome {
	out := baseK4Outcome(step)
	out.status, out.detail, out.ledgerOp = stepStatusFailed, detail, opStatusFailed
	return out
}

func commandOutcome(step runStepState, result WorkCommandResult, detail string) runOutcome {
	if result.WorkItemID.IsZero() || result.CommandID.IsZero() || result.EventID.IsZero() || result.EventSeq < 1 {
		return failK4Outcome(step, "work command returned incomplete durable evidence")
	}
	out := baseK4Outcome(step)
	out.status, out.detail = stepStatusWorkApplied, detail
	out.workItemID, out.commandID = result.WorkItemID.String(), result.CommandID.String()
	out.eventSeq, out.outputKind, out.outputID = result.EventSeq, result.OutputKind, result.OutputID
	out.ownerEpoch, out.leaseFence = result.OwnerEpoch, result.LeaseFence
	out.dispatchRef = result.CommandID.String()
	if out.outputKind == "" {
		out.outputKind = "work_item"
	}
	if out.outputID == "" {
		out.outputID = result.WorkItemID.String()
	}
	return out
}

func (m *Module) executeK4WorkStep(ctx context.Context, mc api.ModuleContext, run model.Record, step runStepState) runOutcome {
	if isRemoteStepKind(step.Kind) {
		return m.executeRemoteWorkStep(ctx, mc, run, step)
	}
	runRef := run.String(model.ColID)
	actor := workStepActor(run)
	idempotency := workStepIdempotency(run, step)

	switch step.Kind {
	case stepWorkCreate:
		var cfg workCreateConfig
		if err := json.Unmarshal(step.Config, &cfg); err != nil {
			return failK4Outcome(step, "stored work-create config is malformed")
		}
		workspaceID, err := parseRequiredWorkID(cfg.WorkspaceID, "workspace_id")
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		criteria := make([]WorkCriterion, 0, len(cfg.Criteria))
		for _, criterion := range cfg.Criteria {
			criteria = append(criteria, WorkCriterion{
				Key: criterion.Key, Ordinal: criterion.Ordinal,
				Statement: criterion.Statement, Required: criterion.Required,
			})
		}
		result, err := m.workflowWork.Create(ctx, mc.Tenant, WorkCreateRequest{
			RunRef: runRef, StepRef: step.Ref, Actor: actor, IdempotencyKey: idempotency,
			WorkspaceID: workspaceID, WorkKind: cfg.WorkKind, Title: cfg.Title,
			BriefMD: cfg.BriefMD, BriefRef: cfg.BriefRef, Priority: cfg.Priority,
			Owner: WorkParticipant{Kind: cfg.Owner.Kind, Ref: cfg.Owner.Ref}, Criteria: criteria,
			Provenance: WorkProvenance{Kind: cfg.Provenance.Kind, Ref: cfg.Provenance.Ref, Hash: cfg.Provenance.Hash},
			DueAt:      cfg.DueAt,
		})
		if err != nil {
			return failK4Outcome(step, "work-create failed: "+clamp(err.Error(), 160))
		}
		out := commandOutcome(step, result, "WorkItem created")
		if out.status == stepStatusWorkApplied {
			out.rootWorkItemID = result.WorkItemID.String()
		}
		return out

	case stepWorkAssign:
		var cfg workAssignConfig
		_ = json.Unmarshal(step.Config, &cfg)
		workID, err := resolveRunWorkItem(run, step)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		if cfg.RequireAck {
			return m.offerK4WorkHandoff(ctx, mc, step, runRef, actor, idempotency, workID,
				cfg.ExpectedOwnerEpoch, cfg.ChannelID, cfg.Target, cfg.Context, cfg.ContextRef, cfg.AckDeadline)
		}
		result, err := m.workflowWork.Assign(ctx, mc.Tenant, WorkAssignRequest{
			RunRef: runRef, StepRef: step.Ref, Actor: actor, IdempotencyKey: idempotency,
			WorkItemID: workID, ExpectedOwnerEpoch: cfg.ExpectedOwnerEpoch,
			Target: WorkParticipant{Kind: cfg.Target.Kind, Ref: cfg.Target.Ref}, RequireAck: cfg.RequireAck,
		})
		if err != nil {
			return failK4Outcome(step, "work-assign failed: "+clamp(err.Error(), 160))
		}
		return commandOutcome(step, result, "WorkItem assigned")

	case stepWorkClaim:
		var cfg workClaimConfig
		_ = json.Unmarshal(step.Config, &cfg)
		workID, err := resolveRunWorkItem(run, step)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		result, err := m.workflowWork.Claim(ctx, mc.Tenant, WorkClaimRequest{
			RunRef: runRef, StepRef: step.Ref, Actor: actor, IdempotencyKey: idempotency,
			WorkItemID: workID, SID: cfg.SID, TTLSeconds: cfg.TTLSeconds,
		})
		if err != nil {
			return failK4Outcome(step, "work-claim failed: "+clamp(err.Error(), 160))
		}
		out := commandOutcome(step, result, "WorkItem lease acquired")
		if out.status == stepStatusWorkApplied && out.leaseFence < 1 {
			return failK4Outcome(step, "work-claim returned no durable lease fence")
		}
		return out

	case stepSessionLaunch:
		var cfg sessionLaunchConfig
		_ = json.Unmarshal(step.Config, &cfg)
		workID, err := resolveRunWorkItem(run, step)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		fence, err := resolveRunFence(run, cfg)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		managed, err := m.workflowRuntime.LaunchForWork(ctx, mc.Tenant, WorkLaunchRequest{
			RunRef: runRef, StepRef: step.Ref, Actor: actor, IdempotencyKey: idempotency,
			WorkItemID: workID, OwnerEpoch: cfg.OwnerEpoch, Fence: fence,
			RuntimeProfileRef: cfg.RuntimeProfileRef, AttemptKind: cfg.AttemptKind,
		})
		if err != nil {
			return failK4Outcome(step, "session-launch failed: "+clamp(err.Error(), 160))
		}
		if managed.WorkItemID != workID || managed.OwnerEpoch < 1 || managed.LeaseFence < 1 ||
			(cfg.OwnerEpoch > 0 && managed.OwnerEpoch != cfg.OwnerEpoch) ||
			(fence > 0 && managed.LeaseFence != fence) || managed.RunRef == "" ||
			managed.SID == "" || managed.DispatchKey == "" {
			return failK4Outcome(step, "session-launch returned an incomplete or mismatched managed run")
		}
		out := baseK4Outcome(step)
		out.status, out.detail = stepStatusLaunched, "managed session launched"
		out.workItemID, out.outputKind, out.outputID = workID.String(), "run", managed.RunRef
		out.ownerEpoch, out.leaseFence = managed.OwnerEpoch, managed.LeaseFence
		out.dispatchRef = managed.RunRef
		return out

	case stepWorkMessage:
		var cfg workMessageConfig
		_ = json.Unmarshal(step.Config, &cfg)
		workID, err := resolveRunWorkItem(run, step)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		channelID, err := parseRequiredWorkID(cfg.ChannelID, "channel_id")
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		result, err := m.workflowMessage.SendWorkMessage(ctx, mc.Tenant, WorkMessageRequest{
			RunRef: runRef, StepRef: step.Ref, Actor: actor, IdempotencyKey: idempotency,
			WorkItemID: workID, ChannelID: channelID,
			Recipient: WorkParticipant{Kind: cfg.Recipient.Kind, Ref: cfg.Recipient.Ref},
			Body:      cfg.Body, BodyRef: cfg.BodyRef, AckDueAt: cfg.AckDueAt, Urgency: cfg.Urgency,
		})
		if err != nil {
			return failK4Outcome(step, "work-message failed: "+clamp(err.Error(), 160))
		}
		if result.WorkItemID != workID || result.MessageID.IsZero() || result.CommandID.IsZero() ||
			result.EventID.IsZero() || result.EventSeq < 1 {
			return failK4Outcome(step, "work-message returned incomplete durable evidence")
		}
		out := baseK4Outcome(step)
		out.status, out.detail = stepStatusMessageSent, "durable work message sent"
		out.workItemID, out.commandID = workID.String(), result.CommandID.String()
		out.eventSeq, out.outputKind, out.outputID = result.EventSeq, "message", result.MessageID.String()
		out.dispatchRef = result.CommandID.String()
		return out

	case stepWorkWaitAck:
		var cfg workWaitAckConfig
		_ = json.Unmarshal(step.Config, &cfg)
		targetID := cfg.TargetID
		if cfg.TargetStepRef != "" {
			producer, found, err := runStepByRef(run, cfg.TargetStepRef)
			if err != nil {
				return failK4Outcome(step, err.Error())
			}
			if !found || !stepOK(producer.Status) || producer.OutputID == "" {
				return failK4Outcome(step, "target_step_ref has no completed durable output")
			}
			targetID = producer.OutputID
		}
		if _, err := parseRequiredWorkID(targetID, "ack target_id"); err != nil {
			return failK4Outcome(step, err.Error())
		}
		out := baseK4Outcome(step)
		out.status, out.detail, out.ledger = stepStatusWaitingAck, "waiting for durable acknowledgement", false
		out.waitingTargetKind, out.waitingTargetID = cfg.TargetKind, targetID
		out.waitingAfterEventSeq, out.waitingDeadline = cfg.AfterEventSeq, cfg.Deadline
		return out

	case stepWorkHandoff:
		var cfg workHandoffConfig
		_ = json.Unmarshal(step.Config, &cfg)
		workID, err := resolveRunWorkItem(run, step)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		return m.offerK4WorkHandoff(ctx, mc, step, runRef, actor, idempotency, workID,
			0, cfg.ChannelID, cfg.Target, cfg.Context, cfg.ContextRef, cfg.AckDeadline)

	case stepWorkTransition:
		var cfg workTransitionConfig
		_ = json.Unmarshal(step.Config, &cfg)
		if workTransitionReasonRequired[cfg.TargetState] && cfg.Reason == "" {
			return failK4Outcome(step, "work-transition to "+cfg.TargetState+" requires a reason")
		}
		workID, err := resolveRunWorkItem(run, step)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		result, err := m.workflowWork.Transition(ctx, mc.Tenant, WorkTransitionRequest{
			RunRef: runRef, StepRef: step.Ref, Actor: actor, IdempotencyKey: idempotency,
			WorkItemID: workID, TargetState: cfg.TargetState,
			EvidenceRef: cfg.EvidenceRef, Reason: cfg.Reason,
		})
		if err != nil {
			return failK4Outcome(step, "work-transition failed: "+clamp(err.Error(), 160))
		}
		return commandOutcome(step, result, "WorkItem transitioned to "+cfg.TargetState)

	case stepWorkCancel:
		var cfg workCancelConfig
		_ = json.Unmarshal(step.Config, &cfg)
		workID, err := resolveRunWorkItem(run, step)
		if err != nil {
			return failK4Outcome(step, err.Error())
		}
		if cfg.BindingID != "" {
			bindingID, err := parseRequiredWorkID(cfg.BindingID, "binding_id")
			if err != nil {
				return failK4Outcome(step, err.Error())
			}
			return m.executeBoundWorkCancel(ctx, mc, step, runRef, actor, idempotency,
				workID, bindingID, cfg.Reason)
		}
		result, err := m.workflowWork.Cancel(ctx, mc.Tenant, WorkCancelRequest{
			RunRef: runRef, StepRef: step.Ref, Actor: actor, IdempotencyKey: idempotency,
			WorkItemID: workID, Reason: cfg.Reason,
		})
		if err != nil {
			return failK4Outcome(step, "work-cancel failed: "+clamp(err.Error(), 160))
		}
		return commandOutcome(step, result, "WorkItem canceled")

	case stepWorkReconcile:
		var cfg workReconcileConfig
		_ = json.Unmarshal(step.Config, &cfg)
		bindingID, err := parseRequiredWorkID(cfg.BindingID, "binding_id")
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
		return remoteResultOutcome(step, "observe", result, stepStatusReconciled, true)
	}
	return failK4Outcome(step, "unknown K4 work step "+step.Kind)
}

func (m *Module) offerK4WorkHandoff(
	ctx context.Context,
	mc api.ModuleContext,
	step runStepState,
	runRef string,
	actor WorkActor,
	idempotency string,
	workID model.ID,
	expectedOwnerEpoch int64,
	channelRef string,
	target workParticipantConfig,
	contextText, contextRef, ackDeadline string,
) runOutcome {
	channelID, err := parseRequiredWorkID(channelRef, "channel_id")
	if err != nil {
		return failK4Outcome(step, err.Error())
	}
	result, err := m.workflowHandoff.OfferWorkHandoff(ctx, mc.Tenant, WorkHandoffRequest{
		RunRef: runRef, StepRef: step.Ref, Actor: actor, IdempotencyKey: idempotency,
		WorkItemID: workID, ExpectedOwnerEpoch: expectedOwnerEpoch, ChannelID: channelID,
		Target:  WorkParticipant{Kind: target.Kind, Ref: target.Ref},
		Context: contextText, ContextRef: contextRef, AckDeadline: ackDeadline,
	})
	if err != nil {
		return failK4Outcome(step, "work-handoff failed: "+clamp(err.Error(), 160))
	}
	if result.WorkItemID != workID || result.HandoffID.IsZero() || result.CommandID.IsZero() ||
		result.EventID.IsZero() || result.EventSeq < 1 || result.OwnerEpoch < 1 ||
		(expectedOwnerEpoch > 0 && result.OwnerEpoch != expectedOwnerEpoch) {
		return failK4Outcome(step, "work-handoff returned incomplete or mismatched durable evidence")
	}
	out := baseK4Outcome(step)
	out.status, out.detail = stepStatusHandoff, "durable handoff offered"
	out.workItemID, out.commandID = workID.String(), result.CommandID.String()
	out.eventSeq, out.outputKind, out.outputID = result.EventSeq, "handoff", result.HandoffID.String()
	out.ownerEpoch, out.dispatchRef = result.OwnerEpoch, result.CommandID.String()
	return out
}

func (m *Module) pollK4WorkAck(ctx context.Context, mc api.ModuleContext, run model.Record, step runStepState) (runOutcome, bool) {
	targetID, err := parseRequiredWorkID(step.WaitingTargetID, "waiting_target_id")
	if err != nil {
		return failK4OutcomeFromWait(step, err.Error()), true
	}
	deadline, err := model.ParseTimestamp(step.WaitingDeadline)
	if err != nil {
		return failK4OutcomeFromWait(step, "stored acknowledgement deadline is malformed"), true
	}
	observation, err := m.workflowAck.ObserveWorkAck(ctx, mc.Tenant, WorkAckQuery{
		Actor:      workStepActor(run),
		TargetKind: step.WaitingTargetKind, TargetID: targetID,
		AfterEventSeq: step.WaitingAfterEventSeq,
	})
	if err != nil {
		return failK4OutcomeFromWait(step, "acknowledgement observation failed: "+clamp(err.Error(), 160)), true
	}
	if observation.EventSeq > 0 && observation.EventSeq <= step.WaitingAfterEventSeq {
		return failK4OutcomeFromWait(step, "acknowledgement reader returned a non-advancing event cursor"), true
	}
	switch observation.Status {
	case WorkAckAcknowledged:
		if observation.AckID.IsZero() || observation.EventID.IsZero() || observation.EventSeq < 1 {
			return failK4OutcomeFromWait(step, "acknowledgement returned incomplete durable evidence"), true
		}
		out := runOutcome{
			ref: step.Ref, fromStatus: stepStatusWaitingAck, status: stepStatusAcked,
			detail: "durable acknowledgement observed", ledger: true, ledgerOp: opStatusDispatched,
			outputKind: "ack", outputID: observation.AckID.String(), eventSeq: observation.EventSeq,
			waitingAfterEventSeq: observation.EventSeq,
		}
		return out, true
	case WorkAckRejected, WorkAckExpired:
		out := runOutcome{
			ref: step.Ref, fromStatus: stepStatusWaitingAck, status: stepStatusBlocked,
			detail: "acknowledgement wait ended: " + string(observation.Status),
			ledger: true, ledgerOp: opStatusBlocked, waitingAfterEventSeq: observation.EventSeq,
		}
		return out, true
	case WorkAckUnknown:
		return failK4OutcomeFromWait(step, "acknowledgement outcome is unknown"), true
	case WorkAckPending:
		if !m.clock.Now().Time().Before(deadline.Time()) {
			return failK4OutcomeFromWait(step, "acknowledgement deadline elapsed"), true
		}
		if observation.EventSeq > step.WaitingAfterEventSeq {
			return runOutcome{
				ref: step.Ref, fromStatus: stepStatusWaitingAck, status: stepStatusWaitingAck,
				detail: step.Detail, waitingAfterEventSeq: observation.EventSeq,
			}, true
		}
		return runOutcome{}, false
	default:
		return failK4OutcomeFromWait(step, "acknowledgement reader returned an invalid status"), true
	}
}

func failK4OutcomeFromWait(step runStepState, detail string) runOutcome {
	return runOutcome{
		ref: step.Ref, fromStatus: stepStatusWaitingAck, status: stepStatusFailed,
		detail: detail, ledger: true, ledgerOp: opStatusFailed,
	}
}
