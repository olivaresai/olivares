// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/sessions"
)

type recordingWorkflowWorkKernel struct {
	commands []sessions.WorkCommand
	actors   []sessions.WorkPrincipal
	result   sessions.CommandResult
	snapshot sessions.WorkSnapshot
	lease    sessions.WorkLease
}

func (k *recordingWorkflowWorkKernel) Apply(
	_ context.Context,
	_ model.TenantID,
	principal sessions.WorkPrincipal,
	cmd sessions.WorkCommand,
) (sessions.CommandResult, error) {
	k.actors = append(k.actors, principal)
	k.commands = append(k.commands, cmd)
	return k.result, nil
}

func (k *recordingWorkflowWorkKernel) Get(
	_ context.Context,
	_ model.TenantID,
	_ sessions.WorkPrincipal,
	_ model.ID,
) (sessions.WorkSnapshot, error) {
	return k.snapshot, nil
}

func (k *recordingWorkflowWorkKernel) GetLease(
	_ context.Context,
	_ model.TenantID,
	_ sessions.WorkPrincipal,
	_ model.ID,
) (sessions.WorkLease, error) {
	return k.lease, nil
}

type recordingWorkflowRuntimeKernel struct {
	spec   sessions.WorkLaunchSpec
	result sessions.ManagedRunRef
}

func (k *recordingWorkflowRuntimeKernel) LaunchForWork(
	_ context.Context,
	_ model.TenantID,
	spec sessions.WorkLaunchSpec,
) (sessions.ManagedRunRef, error) {
	k.spec = spec
	return k.result, nil
}

func TestWorkflowKernelAdapterMapsCreateToDurableWorkCommand(t *testing.T) {
	tenant := model.NewTenantID()
	itemID, commandID, eventID := model.NewID(), model.NewID(), model.NewID()
	userID := model.NewID()
	kernel := &recordingWorkflowWorkKernel{
		result: sessions.CommandResult{
			CommandID: commandID, EventID: eventID, EventSeq: 1,
			ResultID: itemID, ResultKind: "sessions.work_item", Version: 1,
			OwnerEpoch: 1, Status: "draft",
		},
		snapshot: sessions.WorkSnapshot{Item: sessions.WorkItem{
			ID: itemID, Version: 1, OwnerEpoch: 1, LastEventSeq: 1, Status: "draft",
		}},
	}
	adapter := &workflowKernelAdapter{work: kernel}
	got, err := adapter.Create(context.Background(), tenant, orchestration.WorkCreateRequest{
		RunRef: "run-a", StepRef: "create", IdempotencyKey: "run-a:create:primary",
		Actor: orchestration.WorkActor{
			Kind: "token", Ref: "token:credential", Admin: true, UserIdentity: userID,
		},
		WorkspaceID: model.NewID(), WorkKind: "implementation", Title: "Implement K4",
		BriefRef: "brief:kernel-k4", Priority: "p1",
		Owner:      orchestration.WorkParticipant{Kind: "user", Ref: userID.String()},
		Criteria:   []orchestration.WorkCriterion{{Key: "done", Ordinal: 1, Statement: "K4 is complete", Required: true}},
		Provenance: orchestration.WorkProvenance{Kind: "workflow", Ref: "workflow:k4"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(kernel.commands) != 1 || len(kernel.actors) != 1 {
		t.Fatalf("calls = commands:%d actors:%d", len(kernel.commands), len(kernel.actors))
	}
	cmd, principal := kernel.commands[0], kernel.actors[0]
	parsed, parseErr := uuid.Parse(cmd.IdempotencyKey)
	if parseErr != nil || parsed.Version() != 7 || cmd.Command != "item.create" ||
		cmd.BriefMD != "brief:kernel-k4" || len(cmd.ContextRefs) != 1 ||
		cmd.ContextRefs[0].Ref != "brief:kernel-k4" || len(cmd.Acceptance) != 1 ||
		principal.Actor != "token:credential" || principal.ActorKind != model.ActorUser ||
		principal.ActorRef != userID.String() || !principal.Admin {
		t.Fatalf("mapped command/principal = %+v / %+v (parse=%v)", cmd, principal, parseErr)
	}
	if got.WorkItemID != itemID || got.CommandID != commandID || got.EventID != eventID ||
		got.EventSeq != 1 || got.Version != 1 || got.OwnerEpoch != 1 || got.State != "draft" {
		t.Fatalf("result = %+v", got)
	}
}

func TestWorkflowKernelAdapterEnforcesOwnerEpochAndBoundCancel(t *testing.T) {
	itemID := model.NewID()
	kernel := &recordingWorkflowWorkKernel{snapshot: sessions.WorkSnapshot{Item: sessions.WorkItem{
		ID: itemID, WorkspaceID: model.NewID(), Version: 7, OwnerEpoch: 4,
	}}}
	adapter := &workflowKernelAdapter{work: kernel}
	actor := orchestration.WorkActor{Ref: "user:" + itemID.String(), UserIdentity: itemID, Admin: true}
	if _, err := adapter.Assign(context.Background(), model.NewTenantID(), orchestration.WorkAssignRequest{
		Actor: actor, WorkItemID: itemID, ExpectedOwnerEpoch: 3,
	}); err == nil {
		t.Fatal("Assign with stale owner epoch succeeded")
	}
	if _, err := adapter.Cancel(context.Background(), model.NewTenantID(), orchestration.WorkCancelRequest{
		Actor: actor, WorkItemID: itemID, BindingID: model.NewID(), Reason: "cancel",
	}); err == nil {
		t.Fatal("Cancel with an external binding succeeded locally")
	}
	if len(kernel.commands) != 0 {
		t.Fatalf("mutating commands after rejected preconditions = %d", len(kernel.commands))
	}
}

func TestWorkflowKernelAdapterMapsManagedRuntimeIdentity(t *testing.T) {
	tenant, itemID := model.NewTenantID(), model.NewID()
	runtime := &recordingWorkflowRuntimeKernel{result: sessions.ManagedRunRef{
		WorkItemID: itemID, OwnerEpoch: 3, WorkLeaseFence: 9,
		RunRef: "run-managed", SessionID: "osn_managed", DispatchKey: "dispatch",
	}}
	adapter := &workflowKernelAdapter{runtime: runtime}
	got, err := adapter.LaunchForWork(context.Background(), tenant, orchestration.WorkLaunchRequest{
		RunRef: "workflow-run", StepRef: "launch", IdempotencyKey: "workflow-run:launch:primary",
		Actor: orchestration.WorkActor{
			Kind: "token", Ref: "token:agent-credential", AgentIdentity: "agent:builder",
		},
		WorkItemID: itemID, OwnerEpoch: 3, Fence: 9,
		RuntimeProfileRef: model.NewID().String(), AttemptKind: sessions.WorkLaunchAttemptLeaseBind,
	})
	if err != nil {
		t.Fatalf("LaunchForWork: %v", err)
	}
	if runtime.spec.WorkItemID != itemID || runtime.spec.OwnerEpoch != 3 ||
		runtime.spec.WorkLeaseFence != 9 || runtime.spec.Runtime.Actor != "token:agent-credential" ||
		runtime.spec.Runtime.ActorKind != model.ActorAgent || runtime.spec.Runtime.AgentRef != "agent:builder" ||
		runtime.spec.AuditActorRef != "agent:builder" ||
		got.WorkItemID != itemID || got.OwnerEpoch != 3 || got.LeaseFence != 9 ||
		got.RunRef != "run-managed" || got.SID != "osn_managed" || got.DispatchKey != "dispatch" {
		t.Fatalf("runtime spec/result = %+v / %+v", runtime.spec, got)
	}
}

func TestWorkflowSemanticIDIsStableAndDomainSeparated(t *testing.T) {
	a := workflowSemanticID("work-create", "run:step:primary")
	b := workflowSemanticID("work-create", "run:step:primary")
	c := workflowSemanticID("work-cancel", "run:step:primary")
	u, err := uuid.Parse(a)
	if err != nil || u.Version() != 7 || a != b || a == c {
		t.Fatalf("semantic IDs = %q %q %q (parse=%v version=%v)", a, b, c, err, u.Version())
	}
}
