// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/sessions"
)

type workflowWorkKernel interface {
	Apply(context.Context, model.TenantID, sessions.WorkPrincipal, sessions.WorkCommand) (sessions.CommandResult, error)
	Get(context.Context, model.TenantID, sessions.WorkPrincipal, model.ID) (sessions.WorkSnapshot, error)
}

type workflowRuntimeKernel interface {
	LaunchForWork(context.Context, model.TenantID, sessions.WorkLaunchSpec) (sessions.ManagedRunRef, error)
}

type workflowKernelAdapter struct {
	work    workflowWorkKernel
	runtime workflowRuntimeKernel
}

func newWorkflowKernelAdapter(module *sessions.Module) *workflowKernelAdapter {
	return &workflowKernelAdapter{work: module, runtime: module}
}

var (
	_ orchestration.WorkflowWorkControl    = (*workflowKernelAdapter)(nil)
	_ orchestration.WorkflowRuntimeControl = (*workflowKernelAdapter)(nil)
)

func workflowWorkPrincipal(actor orchestration.WorkActor) (sessions.WorkPrincipal, error) {
	if strings.TrimSpace(actor.Ref) == "" {
		return sessions.WorkPrincipal{}, fmt.Errorf("workflow actor is not attributable")
	}
	principal := sessions.WorkPrincipal{
		Actor: actor.Ref, Admin: actor.Admin,
		SessionID: actor.SessionIdentity, SessionRunRef: actor.SessionRunRef,
		SessionFence: actor.SessionFence, PurposeRestricted: actor.PurposeRestricted,
	}
	switch {
	case actor.AgentIdentity != "":
		principal.ActorKind, principal.ActorRef = model.ActorAgent, actor.AgentIdentity
	case !actor.UserIdentity.IsZero():
		principal.ActorKind, principal.ActorRef = model.ActorUser, actor.UserIdentity.String()
	case actor.Kind == model.ActorSystem:
		principal.ActorKind, principal.ActorRef = model.ActorSystem, actor.Ref
	default:
		return sessions.WorkPrincipal{}, fmt.Errorf("workflow actor has no durable user or agent identity")
	}
	return principal, nil
}

func workflowSemanticID(operation, raw string) string {
	sum := sha256.Sum256([]byte("olivares.workflow.command.v1\x00" + operation + "\x00" + raw))
	var id uuid.UUID
	copy(id[:], sum[:16])
	id[6] = id[6]&0x0f | 0x70
	id[8] = id[8]&0x3f | 0x80
	return id.String()
}

func workflowCommandScope(operation, runRef, stepRef string) string {
	return "workflow:" + operation + ":" + runRef + ":" + stepRef
}

func workflowCommandEnvelope(operation, runRef, stepRef, rawKey string) sessions.WorkCommand {
	return sessions.WorkCommand{
		IdempotencyKey: workflowSemanticID(operation, rawKey),
		CommandScope:   workflowCommandScope(operation, runRef, stepRef),
		HTTPMethod:     http.MethodPost,
	}
}

func (a *workflowKernelAdapter) Create(
	ctx context.Context,
	tenant model.TenantID,
	req orchestration.WorkCreateRequest,
) (orchestration.WorkCommandResult, error) {
	principal, err := workflowWorkPrincipal(req.Actor)
	if err != nil {
		return orchestration.WorkCommandResult{}, err
	}
	cmd := workflowCommandEnvelope("work-create", req.RunRef, req.StepRef, req.IdempotencyKey)
	cmd.Command, cmd.WorkspaceID = "item.create", req.WorkspaceID
	cmd.WorkKind, cmd.Title, cmd.BriefMD = req.WorkKind, req.Title, req.BriefMD
	cmd.Priority, cmd.OwnerKind, cmd.OwnerRef = req.Priority, req.Owner.Kind, req.Owner.Ref
	cmd.ProvenanceKind, cmd.ProvenanceRef, cmd.ProvenanceHash = req.Provenance.Kind, req.Provenance.Ref, req.Provenance.Hash
	cmd.DueAt = req.DueAt
	cmd.ContextRefs = []sessions.ContextRef{}
	if req.BriefRef != "" {
		cmd.BriefMD = req.BriefRef
		cmd.ContextRefs = append(cmd.ContextRefs, sessions.ContextRef{Kind: "brief", Ref: req.BriefRef})
	}
	cmd.Acceptance = make([]sessions.AcceptanceInput, 0, len(req.Criteria))
	for _, criterion := range req.Criteria {
		cmd.Acceptance = append(cmd.Acceptance, sessions.AcceptanceInput{
			Key: criterion.Key, Ordinal: criterion.Ordinal,
			Statement: criterion.Statement, Required: criterion.Required,
		})
	}
	result, err := a.work.Apply(ctx, tenant, principal, cmd)
	if err != nil {
		return orchestration.WorkCommandResult{}, err
	}
	return projectWorkflowWorkResult(result.ResultID, result)
}

func (a *workflowKernelAdapter) Assign(
	ctx context.Context,
	tenant model.TenantID,
	req orchestration.WorkAssignRequest,
) (orchestration.WorkCommandResult, error) {
	if req.RequireAck {
		return orchestration.WorkCommandResult{}, fmt.Errorf("acknowledged assignment requires the workflow handoff port")
	}
	principal, current, err := a.currentWork(ctx, tenant, req.Actor, req.WorkItemID)
	if err != nil {
		return orchestration.WorkCommandResult{}, err
	}
	if current.Item.OwnerEpoch != req.ExpectedOwnerEpoch {
		return orchestration.WorkCommandResult{}, fmt.Errorf("work owner epoch changed")
	}
	cmd := workflowCommandEnvelope("work-assign", req.RunRef, req.StepRef, req.IdempotencyKey)
	cmd.Command, cmd.WorkItemID, cmd.WorkspaceID = "item.assign", req.WorkItemID, current.Item.WorkspaceID
	cmd.OwnerKind, cmd.OwnerRef, cmd.ExpectedVersion = req.Target.Kind, req.Target.Ref, current.Item.Version
	result, err := a.work.Apply(ctx, tenant, principal, cmd)
	if err != nil {
		return orchestration.WorkCommandResult{}, err
	}
	return projectWorkflowWorkResult(req.WorkItemID, result)
}

func (a *workflowKernelAdapter) Claim(
	ctx context.Context,
	tenant model.TenantID,
	req orchestration.WorkClaimRequest,
) (orchestration.WorkCommandResult, error) {
	principal, current, err := a.currentWork(ctx, tenant, req.Actor, req.WorkItemID)
	if err != nil {
		return orchestration.WorkCommandResult{}, err
	}
	cmd := workflowCommandEnvelope("work-claim", req.RunRef, req.StepRef, req.IdempotencyKey)
	cmd.Command, cmd.WorkItemID, cmd.WorkspaceID = "lease.acquire", req.WorkItemID, current.Item.WorkspaceID
	cmd.ExpectedVersion, cmd.HolderSID, cmd.TTLSeconds = current.Item.Version, req.SID, req.TTLSeconds
	if req.Actor.SessionIdentity == req.SID {
		cmd.HolderRunRef = req.Actor.SessionRunRef
	}
	cmd.HolderAgentRef = req.Actor.AgentIdentity
	result, err := a.work.Apply(ctx, tenant, principal, cmd)
	if err != nil {
		return orchestration.WorkCommandResult{}, err
	}
	return projectWorkflowWorkResult(req.WorkItemID, result)
}

func (a *workflowKernelAdapter) Transition(
	ctx context.Context,
	tenant model.TenantID,
	req orchestration.WorkTransitionRequest,
) (orchestration.WorkCommandResult, error) {
	principal, current, err := a.currentWork(ctx, tenant, req.Actor, req.WorkItemID)
	if err != nil {
		return orchestration.WorkCommandResult{}, err
	}
	command, code, err := workflowTransitionCommand(req.TargetState)
	if err != nil {
		return orchestration.WorkCommandResult{}, err
	}
	cmd := workflowCommandEnvelope("work-transition-"+req.TargetState, req.RunRef, req.StepRef, req.IdempotencyKey)
	cmd.Command, cmd.WorkItemID, cmd.WorkspaceID = command, req.WorkItemID, current.Item.WorkspaceID
	cmd.ExpectedVersion, cmd.Code, cmd.Reason = current.Item.Version, code, req.Reason
	cmd.EvidenceRef = req.EvidenceRef
	cmd.HolderSID, cmd.HolderRunRef = req.Actor.SessionIdentity, req.Actor.SessionRunRef
	cmd.HolderAgentRef, cmd.Fence = req.Actor.AgentIdentity, req.Actor.SessionFence
	result, err := a.work.Apply(ctx, tenant, principal, cmd)
	if err != nil {
		return orchestration.WorkCommandResult{}, err
	}
	return projectWorkflowWorkResult(req.WorkItemID, result)
}

func workflowTransitionCommand(state string) (string, string, error) {
	switch state {
	case "ready":
		return "item.ready", "", nil
	case "blocked":
		return "item.block", "workflow_blocked", nil
	case "review":
		return "item.submit", "", nil
	case "completed":
		return "item.complete", "", nil
	case "failed":
		return "item.fail", "workflow_failed", nil
	case "canceled":
		return "item.cancel", "workflow_canceled", nil
	default:
		return "", "", fmt.Errorf("unsupported work transition %q", state)
	}
}

func (a *workflowKernelAdapter) Cancel(
	ctx context.Context,
	tenant model.TenantID,
	req orchestration.WorkCancelRequest,
) (orchestration.WorkCommandResult, error) {
	if !req.BindingID.IsZero() {
		return orchestration.WorkCommandResult{}, fmt.Errorf("bound work cancellation requires the workflow binding port")
	}
	principal, current, err := a.currentWork(ctx, tenant, req.Actor, req.WorkItemID)
	if err != nil {
		return orchestration.WorkCommandResult{}, err
	}
	cmd := workflowCommandEnvelope("work-cancel", req.RunRef, req.StepRef, req.IdempotencyKey)
	cmd.Command, cmd.WorkItemID, cmd.WorkspaceID = "item.cancel", req.WorkItemID, current.Item.WorkspaceID
	cmd.ExpectedVersion, cmd.Code, cmd.Reason = current.Item.Version, "workflow_cancel", req.Reason
	result, err := a.work.Apply(ctx, tenant, principal, cmd)
	if err != nil {
		return orchestration.WorkCommandResult{}, err
	}
	return projectWorkflowWorkResult(req.WorkItemID, result)
}

func (a *workflowKernelAdapter) currentWork(
	ctx context.Context,
	tenant model.TenantID,
	actor orchestration.WorkActor,
	itemID model.ID,
) (sessions.WorkPrincipal, sessions.WorkSnapshot, error) {
	principal, err := workflowWorkPrincipal(actor)
	if err != nil {
		return sessions.WorkPrincipal{}, sessions.WorkSnapshot{}, err
	}
	current, err := a.work.Get(ctx, tenant, principal, itemID)
	return principal, current, err
}

func projectWorkflowWorkResult(
	itemID model.ID,
	result sessions.CommandResult,
) (orchestration.WorkCommandResult, error) {
	if itemID.IsZero() || result.CommandID.IsZero() || result.EventID.IsZero() ||
		result.EventSeq < 1 || result.Version < 1 || result.OwnerEpoch < 1 {
		return orchestration.WorkCommandResult{}, fmt.Errorf("work kernel returned incomplete durable evidence")
	}
	return orchestration.WorkCommandResult{
		WorkItemID: itemID, CommandID: result.CommandID, EventID: result.EventID,
		EventSeq: result.EventSeq, OutputKind: result.ResultKind,
		OutputID: result.ResultID.String(), Version: result.Version,
		OwnerEpoch: result.OwnerEpoch, LeaseFence: result.LeaseFence, State: result.Status,
	}, nil
}

func (a *workflowKernelAdapter) LaunchForWork(
	ctx context.Context,
	tenant model.TenantID,
	req orchestration.WorkLaunchRequest,
) (orchestration.ManagedWorkRun, error) {
	principal, err := workflowWorkPrincipal(req.Actor)
	if err != nil {
		return orchestration.ManagedWorkRun{}, err
	}
	name := "workflow-" + req.StepRef
	if len(name) > 120 {
		name = name[:120]
	}
	managed, err := a.runtime.LaunchForWork(ctx, tenant, sessions.WorkLaunchSpec{
		WorkItemID: req.WorkItemID, OwnerEpoch: req.OwnerEpoch,
		WorkLeaseFence: req.Fence, AttemptKind: req.AttemptKind,
		AuditActorRef: principal.ActorRef,
		Runtime: sessions.CreateRunParams{
			Name: name, TemplateID: req.RuntimeProfileRef,
			Actor: principal.Actor, ActorKind: principal.ActorKind,
			AgentRef: req.Actor.AgentIdentity,
		},
	})
	if err != nil {
		return orchestration.ManagedWorkRun{}, err
	}
	return orchestration.ManagedWorkRun{
		WorkItemID: managed.WorkItemID, OwnerEpoch: managed.OwnerEpoch,
		LeaseFence: managed.WorkLeaseFence, RunRef: managed.RunRef,
		SID: managed.SessionID, DispatchKey: managed.DispatchKey,
	}, nil
}
