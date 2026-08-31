// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/sessions"
)

type fakeRemoteWorkStore struct {
	work          sessions.WorkSnapshot
	spec          sessions.ProtocolBindingSpec
	binding       sessions.ProtocolBinding
	reserveReplay bool
	cancelReplay  bool
	events        *[]string
	interrupts    []sessions.ProtocolInterruptCommand
	replyCommand  sessions.ProtocolReplyCommand
	reply         sessions.ProtocolReplyResult
}

func (s *fakeRemoteWorkStore) GetProtocolBindingSpec(
	_ context.Context,
	tenant model.TenantID,
	id model.ID,
) (sessions.ProtocolBindingSpec, error) {
	if id != s.spec.ID {
		return sessions.ProtocolBindingSpec{}, sessions.ErrProtocolBindingNotFound
	}
	result := s.spec
	result.TenantID = tenant
	return result, nil
}

func (s *fakeRemoteWorkStore) event(value string) {
	if s.events != nil {
		*s.events = append(*s.events, value)
	}
}

func (s *fakeRemoteWorkStore) Get(
	context.Context,
	model.TenantID,
	sessions.WorkPrincipal,
	model.ID,
) (sessions.WorkSnapshot, error) {
	return s.work, nil
}

func (s *fakeRemoteWorkStore) RecordProtocolInterrupt(
	_ context.Context,
	_ model.TenantID,
	request sessions.ProtocolInterruptCommand,
) (sessions.ProtocolInterruptResult, error) {
	s.event("interrupt")
	s.interrupts = append(s.interrupts, request)
	messages := make([]sessions.ProtocolInterruptMessage, 0, len(request.Requests))
	for _, item := range request.Requests {
		messages = append(messages, sessions.ProtocolInterruptMessage{
			KeyDigest: item.KeyDigest, MessageID: model.NewID(), DeliveryID: model.NewID(),
		})
	}
	return sessions.ProtocolInterruptResult{
		BindingID: request.BindingID, Generation: request.Generation, Messages: messages,
	}, nil
}

func (*fakeRemoteWorkStore) PrepareProtocolInputResponses(
	context.Context,
	model.TenantID,
	sessions.ProtocolInputResponseCommand,
) (sessions.ProtocolInputResponseResult, error) {
	return sessions.ProtocolInputResponseResult{}, errors.New("unexpected protocol input response")
}

func (*fakeRemoteWorkStore) PlanProtocolBindingSpec(
	context.Context,
	model.TenantID,
	sessions.ProtocolBindingSpecCommand,
) (sessions.ProtocolBindingSpecPlan, error) {
	return sessions.ProtocolBindingSpecPlan{}, errors.New("unexpected plan spec")
}

func (*fakeRemoteWorkStore) ApplyProtocolBindingSpec(
	context.Context,
	model.TenantID,
	sessions.ProtocolBindingSpecCommand,
) (sessions.ProtocolBindingSpecResult, error) {
	return sessions.ProtocolBindingSpecResult{}, errors.New("unexpected apply spec")
}

func (s *fakeRemoteWorkStore) ReserveProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	request sessions.ProtocolBindingReservation,
) (sessions.ProtocolBinding, error) {
	s.event("reserve")
	if s.binding.ID.IsZero() {
		s.binding = remoteBindingFixture(request.WorkspaceID, request.WorkItemID, request.BindingSpecID)
	}
	s.binding.WorkspaceID = request.WorkspaceID
	s.binding.WorkItemID = request.WorkItemID
	s.binding.BindingSpecID = request.BindingSpecID
	s.binding.BindingSpecGeneration = request.BindingSpecGeneration
	s.binding.AttemptID = request.AttemptID
	s.binding.Generation = request.Generation
	s.binding.OwnerKind = request.OwnerKind
	s.binding.OwnerRef = request.OwnerRef
	s.binding.OwnerEpoch = request.OwnerEpoch
	s.binding.LeaseFence = request.LeaseFence + 1
	s.binding.Replayed = s.reserveReplay
	return s.binding, nil
}

func (s *fakeRemoteWorkStore) SettleProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	request sessions.ProtocolBindingSettlement,
) (sessions.ProtocolBinding, error) {
	s.event("settle")
	s.binding.Version++
	s.binding.ExternalKind = string(request.ResultKind)
	s.binding.ExternalID = request.ExternalID
	s.binding.ContextID = request.ContextID
	s.binding.ExternalMessageID = request.ExternalMessageID
	s.binding.LocalState = request.LocalState
	s.binding.RemoteState = request.RemoteState
	s.binding.RemoteRevision = request.RemoteRevision
	s.binding.ObservationVerdict = request.Verdict
	s.binding.ObservationCode = request.Code
	s.binding.Terminal = request.Terminal
	s.binding.DetailHash = append([]byte(nil), request.DetailHash...)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	s.binding.LastObservedAt = &now
	s.bumpReceipt()
	return s.binding, nil
}

func (s *fakeRemoteWorkStore) ObserveProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	request sessions.ProtocolBindingObservation,
) (sessions.ProtocolBinding, error) {
	s.event("observe")
	s.binding.Version++
	s.binding.ExternalID = request.ExternalID
	s.binding.ContextID = request.ContextID
	s.binding.ExternalMessageID = request.ExternalMessageID
	s.binding.LocalState = request.LocalState
	s.binding.RemoteState = request.RemoteState
	s.binding.RemoteRevision = request.RemoteRevision
	s.binding.ObservationVerdict = request.Verdict
	s.binding.ObservationCode = request.Code
	s.binding.Terminal = request.Terminal
	s.binding.DetailHash = append([]byte(nil), request.DetailHash...)
	now := time.Date(2026, 8, 18, 12, 0, 1, 0, time.UTC)
	s.binding.LastObservedAt = &now
	s.bumpReceipt()
	return s.binding, nil
}

func (s *fakeRemoteWorkStore) RequestProtocolBindingCancel(
	_ context.Context,
	_ model.TenantID,
	_ sessions.ProtocolBindingCancelIntent,
) (sessions.ProtocolBinding, error) {
	s.event("cancel_intent")
	s.binding.Version++
	s.binding.CancelRequested = true
	now := time.Date(2026, 8, 18, 12, 0, 2, 0, time.UTC)
	s.binding.CancelRequestedAt = &now
	s.binding.CancelReasonCode = "workflow_cancel"
	s.binding.Replayed = s.cancelReplay
	s.bumpReceipt()
	return s.binding, nil
}

func (s *fakeRemoteWorkStore) GetProtocolBinding(
	context.Context,
	model.TenantID,
	sessions.ProtocolBindingRef,
) (sessions.ProtocolBinding, error) {
	s.event("get")
	return s.binding, nil
}

func (s *fakeRemoteWorkStore) ApplyProtocolReplay(
	ctx context.Context,
	_ model.TenantID,
	claim sessions.ProtocolReplayClaim,
	mutation sessions.ProtocolReplayMutation,
) (sessions.ProtocolReplayResult, error) {
	s.event("replay")
	settlement, err := mutation(ctx)
	if err != nil {
		return sessions.ProtocolReplayResult{}, err
	}
	return sessions.ProtocolReplayResult{Guard: sessions.ProtocolReplayGuard{
		AppendOnlyCommunicationEntity: sessions.AppendOnlyCommunicationEntity{
			CommunicationEntity: sessions.CommunicationEntity{ID: model.NewID()},
		},
		Protocol: claim.Protocol, PeerAuthority: claim.PeerAuthority,
		ReplayKind: claim.Kind, BindingID: settlement.BindingID,
	}}, nil
}

func (s *fakeRemoteWorkStore) ProjectProtocolReply(
	_ context.Context,
	_ model.TenantID,
	command sessions.ProtocolReplyCommand,
) (sessions.ProtocolReplyResult, error) {
	s.event("reply")
	s.replyCommand = command
	s.reply = sessions.ProtocolReplyResult{
		BindingID: command.BindingID, Generation: command.Generation,
		WorkItemID: s.binding.WorkItemID, CommandID: model.NewID(),
		MessageID: model.NewID(), DeliveryID: model.NewID(), ThreadID: model.NewID(),
		EventID: model.NewID(), EventSeq: s.binding.LastEventSeq + 1,
		Version: 2, State: sessions.MessagePublished,
	}
	return s.reply, nil
}

func (s *fakeRemoteWorkStore) GetProtocolReply(
	context.Context,
	model.TenantID,
	sessions.ProtocolReplyRef,
) (sessions.ProtocolReplyResult, error) {
	s.event("get_reply")
	if s.reply.MessageID.IsZero() {
		return sessions.ProtocolReplyResult{}, errors.New("reply absent")
	}
	s.reply.Replayed = true
	return s.reply, nil
}

func (*fakeRemoteWorkStore) ListProtocolBindings(
	context.Context,
	model.TenantID,
	sessions.ProtocolBindingQuery,
) (sessions.ProtocolBindingPage, error) {
	return sessions.ProtocolBindingPage{}, errors.New("unexpected list")
}

func (s *fakeRemoteWorkStore) bumpReceipt() {
	s.binding.LastCommandID = model.NewID()
	s.binding.LastEventID = model.NewID()
	s.binding.LastEventSeq++
}

type fakeRemoteA2A struct {
	gate          a2a.DelegationGate
	events        *[]string
	testResult    a2a.DelegationTestResult
	testErr       error
	delegate      a2a.TaskResult
	delegateErr   error
	reconcile     a2a.TaskResult
	reconcileOK   bool
	reconcileErr  error
	cancel        a2a.TaskResult
	cancelErr     error
	delegateCalls int
	reconcileCall int
	cancelCalls   int
}

func (c *fakeRemoteA2A) Test(_ context.Context, spec a2a.DelegateSpec) (a2a.DelegationTestResult, error) {
	if c.testResult.PlanHash == "" {
		c.testResult = a2a.DelegationTestResult{
			PlanHash: a2a.DelegationPlanHash(spec), AgentName: spec.AgentName,
			Skill: spec.Skill, Scope: spec.Scope, Trust: "verified",
		}
	}
	return c.testResult, c.testErr
}

func (c *fakeRemoteA2A) Delegate(ctx context.Context, spec a2a.DelegateSpec) (a2a.TaskResult, error) {
	c.delegateCalls++
	if c.gate != nil {
		decision, err := c.gate.Authorize(ctx, a2a.DelegationRequest{
			Tenant: spec.Tenant, AgentName: spec.AgentName, Skill: spec.Skill,
			Scope: spec.Scope, PlanHash: a2a.DelegationPlanHash(spec), RequestedBy: spec.RequestedBy,
		})
		if err != nil {
			return a2a.TaskResult{}, err
		}
		if !decision.Allowed() {
			return a2a.TaskResult{}, &a2a.DenyError{Reason: "test gate denied", PlanHash: decision.PlanHash}
		}
	}
	if c.events != nil {
		*c.events = append(*c.events, "delegate")
	}
	return c.delegate, c.delegateErr
}

func (c *fakeRemoteA2A) Reconcile(
	context.Context,
	a2a.TaskResult,
	a2a.TaskRef,
) (a2a.TaskResult, bool, error) {
	c.reconcileCall++
	if c.events != nil {
		*c.events = append(*c.events, "reconcile")
	}
	return c.reconcile, c.reconcileOK, c.reconcileErr
}

func (c *fakeRemoteA2A) CancelTask(context.Context, a2a.TaskRef) (a2a.TaskResult, error) {
	c.cancelCalls++
	if c.events != nil {
		*c.events = append(*c.events, "cancel_rpc")
	}
	return c.cancel, c.cancelErr
}

type fakeRemoteApprovalGate struct {
	events *[]string
	check  orchestration.ApprovalCheck
}

func (g *fakeRemoteApprovalGate) Request(
	context.Context,
	orchestration.ApprovalRequest,
) (orchestration.GateDecision, error) {
	return orchestration.GateDecision{}, errors.New("unexpected approval request")
}

func (g *fakeRemoteApprovalGate) Status(
	_ context.Context,
	check orchestration.ApprovalCheck,
) (orchestration.GateDecision, error) {
	g.check = check
	if g.events != nil {
		*g.events = append(*g.events, "approval")
	}
	return orchestration.GateDecision{
		ApprovalRef: check.ApprovalRef, Status: orchestration.StatusApproved, PlanHash: check.PlanHash,
	}, nil
}

func remoteExecutorFixture(t *testing.T) (*orchRemoteExecutor, *fakeRemoteWorkStore, orchestration.RemoteWorkPlanRequest) {
	t.Helper()
	tenant := model.NewTenantID()
	workspace, workID, specID := model.NewID(), model.NewID(), model.NewID()
	work := sessions.WorkSnapshot{Item: sessions.WorkItem{
		ID: workID, WorkspaceID: workspace, Version: 4, WorkKind: "operations",
		Title: "Prepare governed report", BriefMD: "prepare the governed report", BriefHash: "brief-hash-1",
		Status: "active", OwnerKind: "agent", OwnerRef: "agent:local",
		OwnerEpoch: 3, AcceptanceRevision: 2, Leased: true,
		Lease: &sessions.WorkLease{Fence: 7, Live: true, LivenessVerdict: sessions.VerdictClean},
	}}
	specInput := sessions.ProtocolBindingSpecInput{
		WorkspaceID: workspace, BindingKey: "remote-report", Generation: 4,
		Protocol: sessions.BindingProtocolA2A, ProtocolVersion: a2a.ProtocolVersion,
		Direction: sessions.BindingOutbound, LocalKind: sessions.BindingLocalWorkItem,
		LocalSelector: json.RawMessage(`{"work_kind":"operations"}`),
		PeerAuthority: "https://reports.example.test", RemoteResourceKind: "agent", RemoteResourceRef: "agent:reporter",
		MappingSchema: sessions.ProtocolBindingMappingSchemaV1,
		Mapping: []sessions.ProtocolMappingRule{{
			Source: "work.brief", Target: "message.text", Cardinality: sessions.ProtocolMappingOneToOne,
			Transform: sessions.ProtocolTransformText,
		}},
		KnownLosses: []sessions.ProtocolBindingLoss{{
			Field: "work.title", ReasonCode: "brief-is-authoritative", Accepted: true,
			AcceptanceRef: "approval:remote-work",
		}},
		RuleRefs: []string{"rule:remote-work"}, PermissionProfileRef: "permission:remote-work",
		CurrencyPolicy: sessions.BindingCurrencyPinned,
		Validation: sessions.ProtocolBindingValidation{
			Verdict: sessions.ProtocolObservationClean, Code: "validated", ObservedAt: time.Now().UTC(),
		},
	}
	digests, err := sessions.ComputeProtocolBindingSpecDigests(specInput)
	if err != nil {
		t.Fatalf("runtime spec digests: %v", err)
	}
	store := &fakeRemoteWorkStore{work: work, spec: sessions.ProtocolBindingSpec{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{CommunicationEntity: sessions.CommunicationEntity{
			ID: specID, TenantID: tenant, WorkspaceID: workspace, Version: 2,
		}},
		BindingKey: specInput.BindingKey, Generation: specInput.Generation,
		Protocol: specInput.Protocol, ProtocolVersion: specInput.ProtocolVersion,
		Direction: specInput.Direction, LocalKind: specInput.LocalKind,
		LocalSelector: specInput.LocalSelector, PeerAuthority: specInput.PeerAuthority,
		RemoteResourceKind: specInput.RemoteResourceKind, RemoteResourceRef: specInput.RemoteResourceRef,
		MappingSchema: specInput.MappingSchema, Mapping: specInput.Mapping, MappingHash: digests.MappingHash,
		KnownLosses: specInput.KnownLosses, LossesHash: digests.LossesHash,
		RuleRefs: specInput.RuleRefs, PermissionProfileRef: specInput.PermissionProfileRef,
		CurrencyPolicy: specInput.CurrencyPolicy, Validation: specInput.Validation,
		State: sessions.ProtocolBindingSpecActive, SpecHash: digests.SpecHash,
	}}
	interruptChannel, interruptSender, interruptRecipient := model.NewID(), model.NewID(), model.NewID()
	var cfg orchDispatchConfig
	cfg.A2A.Agents = []orchA2AAgentJSON{{
		SubjectRef: "agent:reporter", Authority: "https://reports.example.test", Name: "reporter",
		URL: "https://reports.example.test/a2a", Skill: "report", Scopes: []string{"reports:read", "reports:write"},
		InterruptChannelID: interruptChannel.String(), InterruptSenderUserID: interruptSender.String(),
		InterruptRecipientUserID: interruptRecipient.String(),
	}}
	executor, err := newOrchRemoteExecutor(cfg, store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	executor.now = func() time.Time { return time.Date(2026, 8, 18, 11, 59, 0, 0, time.UTC) }
	actorID := model.NewID()
	plan := orchestration.RemoteWorkPlanRequest{
		RunRef: "run-1", StepRef: "remote-plan", Actor: orchestration.WorkActor{
			Kind: model.ActorUser, Ref: "user:test", UserIdentity: actorID,
		},
		WorkspaceID: workspace, WorkItemID: workID,
		BindingSpecID: specID, BindingSpecGeneration: 4,
		Protocol: "a2a", ProtocolVersion: a2a.ProtocolVersion,
		Authority: "https://reports.example.test", AgentRef: "agent:reporter", Skill: "report", Scope: "reports:read",
		OwnerEpoch: 3, LeaseFence: 7, BriefHash: "brief-hash-1", CriteriaRevision: 2,
	}
	_ = tenant
	return executor, store, plan
}

func remoteBindingFixture(workspace, workID, specID model.ID) sessions.ProtocolBinding {
	return sessions.ProtocolBinding{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{
			CommunicationEntity: sessions.CommunicationEntity{
				ID: model.NewID(), WorkspaceID: workspace, Version: 1,
			},
		},
		BindingSpecID: specID, BindingSpecGeneration: 4,
		Protocol: sessions.BindingProtocolA2A, ProtocolVersion: a2a.ProtocolVersion,
		Direction: sessions.BindingOutbound, PeerAuthority: "https://reports.example.test", RemoteResourceRef: "agent:reporter",
		AttemptID: model.NewID(), Generation: 1, SyntheticSID: "osn_" + model.NewID().String(),
		WorkItemID: workID, OwnerKind: "agent", OwnerRef: "agent:local", OwnerEpoch: 3, LeaseFence: 7,
		ExternalKind: "task_or_message", LocalState: "active", RemoteState: "reserved",
		ObservationVerdict: sessions.ProtocolObservationUnknown, ObservationCode: "reserved",
		LastCommandID: model.NewID(), LastEventID: model.NewID(), LastEventSeq: 1,
	}
}

func TestK5OrchRemotePlanAndTestUseCompleteA2APlan(t *testing.T) {
	executor, _, request := remoteExecutorFixture(t)
	tenant := model.NewTenantID()
	client := &fakeRemoteA2A{}
	executor.client = func(_ orchRemoteTarget, gate a2a.DelegationGate) remoteA2AClient {
		client.gate = gate
		return client
	}

	planned, err := executor.Plan(context.Background(), tenant, request)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if planned.Outcome != orchestration.RemoteWorkClean || planned.PlanHash == "" ||
		planned.WorkItemID != request.WorkItemID || planned.OwnerEpoch != request.OwnerEpoch ||
		planned.LeaseFence != request.LeaseFence {
		t.Fatalf("planned = %#v", planned)
	}
	tested, err := executor.Test(context.Background(), tenant, orchestration.RemoteWorkTestRequest{
		Plan: request, PlanHash: planned.PlanHash,
	})
	if err != nil || tested.Outcome != orchestration.RemoteWorkClean || tested.PlanHash != planned.PlanHash {
		t.Fatalf("test = %#v, %v", tested, err)
	}

	changed := request
	changed.Scope = "reports:write"
	replanned, err := executor.Plan(context.Background(), tenant, changed)
	if err != nil {
		t.Fatalf("replan: %v", err)
	}
	if replanned.PlanHash == planned.PlanHash {
		t.Fatal("scope change did not change A2A DelegationPlanHash")
	}
}

func TestK5OrchRemoteValidatesProtocolSpecFromSignedCapability(t *testing.T) {
	executor, store, request := remoteExecutorFixture(t)
	client := &fakeRemoteA2A{}
	executor.client = func(_ orchRemoteTarget, gate a2a.DelegationGate) remoteA2AClient {
		client.gate = gate
		return client
	}
	input := sessions.ProtocolBindingSpecInput{
		WorkspaceID: request.WorkspaceID, BindingKey: "reports", Generation: 1,
		Protocol: sessions.BindingProtocolA2A, ProtocolVersion: a2a.ProtocolVersion,
		Direction: sessions.BindingOutbound, LocalKind: sessions.BindingLocalWorkItem,
		LocalSelector: []byte(`{"work_kind":"operations"}`),
		PeerAuthority: request.Authority, RemoteResourceKind: "agent",
		RemoteResourceRef: request.AgentRef,
		MappingSchema:     store.spec.MappingSchema, Mapping: store.spec.Mapping,
		KnownLosses: store.spec.KnownLosses, RuleRefs: store.spec.RuleRefs,
		PermissionProfileRef: store.spec.PermissionProfileRef,
		CurrencyPolicy:       sessions.BindingCurrencyPinned,
	}
	validation, err := executor.ValidateProtocolBindingSpec(
		context.Background(), model.NewTenantID(), input,
	)
	if err != nil || validation.Verdict != sessions.ProtocolObservationClean ||
		validation.Code != "a2a_capability_validated" || validation.ObservedAt.IsZero() {
		t.Fatalf("validation = %#v, %v", validation, err)
	}
	input.Direction = sessions.BindingInbound
	if _, err := executor.ValidateProtocolBindingSpec(
		context.Background(), model.NewTenantID(), input,
	); err == nil {
		t.Fatal("inbound spec was validated by the outbound A2A executor")
	}
}

func TestK5OrchRemoteStartReservesThenChecksExactApprovalAndPreservesMessageUnion(t *testing.T) {
	executor, store, planRequest := remoteExecutorFixture(t)
	tenant := model.NewTenantID()
	events := []string{}
	store.events = &events
	approval := &fakeRemoteApprovalGate{events: &events}
	executor.approval = approval
	client := &fakeRemoteA2A{events: &events, delegate: a2a.TaskResult{
		TaskID: "message-9", ResultKind: "message", MessageID: "message-9",
		MessageDigest: strings.Repeat("a", 64),
		MessageParts: []a2a.MessageResultPart{{
			Kind: "text", Text: "Synchronous result", Digest: strings.Repeat("b", 64),
		}},
		ContextID: "context-4", State: a2a.TaskStateCompleted, Terminal: true,
		TrustLevel: "verified", Detail: "synchronous message reply",
	}}
	executor.client = func(_ orchRemoteTarget, gate a2a.DelegationGate) remoteA2AClient {
		client.gate = gate
		return client
	}
	planned, err := executor.Plan(context.Background(), tenant, planRequest)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Start(context.Background(), tenant, orchestration.RemoteWorkStartRequest{
		Actor: planRequest.Actor, IdempotencyKey: "start-key-1", Plan: planRequest,
		PlanHash: planned.PlanHash, ApprovalRef: "approval-1", ApprovalPlanHash: "workflow-plan-1",
		ApprovalAction: "orchestration.workflow.run", ApprovalSubjectKind: "workflow",
		ApprovalSubjectRef: "workflow-1",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	wantEvents := []string{"reserve", "approval", "delegate", "replay", "settle", "reply"}
	if len(events) != len(wantEvents) {
		t.Fatalf("events = %#v", events)
	}
	for i := range wantEvents {
		if events[i] != wantEvents[i] {
			t.Fatalf("events = %#v", events)
		}
	}
	if approval.check.PlanHash != "workflow-plan-1" || approval.check.Action != "orchestration.workflow.run" ||
		approval.check.SubjectKind != "workflow" || approval.check.SubjectRef != "workflow-1" {
		t.Fatalf("approval check = %#v", approval.check)
	}
	if result.Outcome != orchestration.RemoteWorkClean || result.PlanHash != planned.PlanHash ||
		result.ApprovalRef != "approval-1" || result.ResultKind != orchestration.RemoteWorkResultMessage ||
		result.ExternalMessageID != "message-9" || result.ExternalTaskID != "" ||
		result.WorkState != "review" || result.LeaseFence != planRequest.LeaseFence+1 ||
		result.CommandID.IsZero() || result.EventID.IsZero() || result.EventSeq < 2 {
		t.Fatalf("result = %#v", result)
	}
	if store.replyCommand.BindingID != store.binding.ID ||
		store.replyCommand.Generation != store.binding.Generation ||
		store.replyCommand.Route != executor.targets[remoteTargetKey(
			planRequest.Authority, planRequest.AgentRef,
		)].interrupt || store.replyCommand.MessageID != "message-9" ||
		store.replyCommand.ContextID != "context-4" || len(store.replyCommand.Parts) != 1 ||
		store.replyCommand.Parts[0].Text != "Synchronous result" {
		t.Fatalf("durable reply command = %#v", store.replyCommand)
	}
}

func TestK5OrchRemoteReplayedReservationNeverReemits(t *testing.T) {
	executor, store, planRequest := remoteExecutorFixture(t)
	tenant := model.NewTenantID()
	store.reserveReplay = true
	client := &fakeRemoteA2A{}
	executor.client = func(_ orchRemoteTarget, gate a2a.DelegationGate) remoteA2AClient {
		client.gate = gate
		return client
	}
	planned, err := executor.Plan(context.Background(), tenant, planRequest)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Start(context.Background(), tenant, orchestration.RemoteWorkStartRequest{
		Actor: planRequest.Actor, IdempotencyKey: "start-key-replay", Plan: planRequest,
		PlanHash: planned.PlanHash, ApprovalRef: "approval-1", ApprovalPlanHash: "workflow-plan-1",
		ApprovalAction: "orchestration.workflow.run", ApprovalSubjectKind: "workflow",
		ApprovalSubjectRef: "workflow-1",
	})
	if err != nil {
		t.Fatalf("replayed start: %v", err)
	}
	if client.delegateCalls != 0 || result.Outcome != orchestration.RemoteWorkUnknown ||
		result.Code != "dispatch_ambiguous" || store.binding.ObservationCode != "dispatch_ambiguous" {
		t.Fatalf("replay emitted=%d result=%#v binding=%#v", client.delegateCalls, result, store.binding)
	}
}

func TestK5OrchRemoteAfterTransmitIsDurableUnknownAndRetryDoesNotReemit(t *testing.T) {
	executor, store, planRequest := remoteExecutorFixture(t)
	tenant := model.NewTenantID()
	approval := &fakeRemoteApprovalGate{}
	executor.approval = approval
	client := &fakeRemoteA2A{delegateErr: a2a.ErrAfterTransmit}
	executor.client = func(_ orchRemoteTarget, gate a2a.DelegationGate) remoteA2AClient {
		client.gate = gate
		return client
	}
	planned, err := executor.Plan(context.Background(), tenant, planRequest)
	if err != nil {
		t.Fatal(err)
	}
	request := orchestration.RemoteWorkStartRequest{
		Actor: planRequest.Actor, IdempotencyKey: "start-key-ambiguous", Plan: planRequest,
		PlanHash: planned.PlanHash, ApprovalRef: "approval-1", ApprovalPlanHash: "workflow-plan-1",
		ApprovalAction: "orchestration.workflow.run", ApprovalSubjectKind: "workflow",
		ApprovalSubjectRef: "workflow-1",
	}
	result, err := executor.Start(context.Background(), tenant, request)
	if err != nil || result.Outcome != orchestration.RemoteWorkUnknown ||
		result.Code != "dispatch_ambiguous" || result.CommandID.IsZero() {
		t.Fatalf("ambiguous start = %#v, %v", result, err)
	}
	if client.delegateCalls != 1 || store.binding.ObservationVerdict != sessions.ProtocolObservationUnknown {
		t.Fatalf("calls=%d binding=%#v", client.delegateCalls, store.binding)
	}

	store.reserveReplay = true
	replayed, err := executor.Start(context.Background(), tenant, request)
	if err != nil || replayed.Outcome != orchestration.RemoteWorkUnknown || client.delegateCalls != 1 {
		t.Fatalf("ambiguous replay = %#v, %v calls=%d", replayed, err, client.delegateCalls)
	}
}

func TestK5OrchRemoteObserveAndCancelPersistBeforeReturning(t *testing.T) {
	executor, store, plan := remoteExecutorFixture(t)
	tenant := model.NewTenantID()
	store.binding = remoteBindingFixture(plan.WorkspaceID, plan.WorkItemID, plan.BindingSpecID)
	store.binding.ExternalKind = "task"
	store.binding.ExternalID = "task-1"
	store.binding.RemoteState = "working"
	store.binding.ObservationVerdict = sessions.ProtocolObservationClean
	client := &fakeRemoteA2A{
		reconcile: a2a.TaskResult{
			TaskID: "task-1", ResultKind: "task", ContextID: "context-1",
			State: a2a.TaskStateCompleted, Terminal: true, TrustLevel: "verified",
		},
		reconcileOK: true,
		cancel: a2a.TaskResult{
			TaskID: "task-1", ResultKind: "task", ContextID: "context-1",
			State: a2a.TaskStateCanceled, Terminal: true, TrustLevel: "verified",
		},
	}
	executor.client = func(_ orchRemoteTarget, gate a2a.DelegationGate) remoteA2AClient {
		client.gate = gate
		return client
	}
	observed, err := executor.Observe(context.Background(), tenant, orchestration.RemoteWorkObserveRequest{
		IdempotencyKey: "observe-1", BindingID: store.binding.ID,
	})
	if err != nil || observed.Outcome != orchestration.RemoteWorkClean ||
		observed.RemoteState != "completed" || observed.WorkState != "review" || !observed.Terminal {
		t.Fatalf("observe = %#v, %v", observed, err)
	}
	if client.reconcileCall != 1 || store.binding.ObservationCode != "remote_completed" {
		t.Fatalf("observe calls=%d binding=%#v", client.reconcileCall, store.binding)
	}

	store.binding.Terminal = false
	store.binding.RemoteState = "working"
	store.binding.LocalState = "active"
	store.binding.ObservationCode = "remote_working"
	store.binding.Replayed = false
	canceled, err := executor.Cancel(context.Background(), tenant, orchestration.RemoteWorkCancelRequest{
		IdempotencyKey: "cancel-1", BindingID: store.binding.ID,
		WorkItemID: store.binding.WorkItemID, Reason: "workflow requested stop",
	})
	if err != nil || canceled.Outcome != orchestration.RemoteWorkClean ||
		canceled.RemoteState != "canceled" || canceled.WorkState != "canceled" || !canceled.Terminal {
		t.Fatalf("cancel = %#v, %v", canceled, err)
	}
	if client.cancelCalls != 1 || !store.binding.CancelRequested || store.binding.ObservationCode != "remote_canceled" {
		t.Fatalf("cancel calls=%d binding=%#v", client.cancelCalls, store.binding)
	}

	store.cancelReplay = true
	store.binding.Terminal = false
	before := client.cancelCalls
	replayed, err := executor.Cancel(context.Background(), tenant, orchestration.RemoteWorkCancelRequest{
		IdempotencyKey: "cancel-1", BindingID: store.binding.ID,
		WorkItemID: store.binding.WorkItemID, Reason: "workflow requested stop",
	})
	if err != nil || replayed.Outcome != orchestration.RemoteWorkUnknown || client.cancelCalls != before {
		t.Fatalf("replayed cancel = %#v, %v calls=%d", replayed, err, client.cancelCalls)
	}
}

func TestK5OrchRemoteInterruptCreatesDurableOwnerMessage(t *testing.T) {
	executor, store, plan := remoteExecutorFixture(t)
	tenant := model.NewTenantID()
	store.binding = remoteBindingFixture(plan.WorkspaceID, plan.WorkItemID, plan.BindingSpecID)
	store.binding.ExternalKind = "task"
	store.binding.ExternalID = "task-input-1"
	store.binding.ContextID = "context-input-1"
	store.binding.RemoteState = "working"
	store.binding.ObservationVerdict = sessions.ProtocolObservationClean
	client := &fakeRemoteA2A{reconcileOK: true, reconcile: a2a.TaskResult{
		TaskID: "task-input-1", ResultKind: "task", ContextID: "context-input-1",
		State: a2a.TaskStateInputReq, Interrupt: true, TrustLevel: "verified",
	}}
	executor.client = func(_ orchRemoteTarget, gate a2a.DelegationGate) remoteA2AClient {
		client.gate = gate
		return client
	}

	result, err := executor.Observe(context.Background(), tenant, orchestration.RemoteWorkObserveRequest{
		IdempotencyKey: "observe-input-1", BindingID: store.binding.ID,
	})
	if err != nil {
		t.Fatalf("observe interrupt: %v", err)
	}
	if result.Outcome != orchestration.RemoteWorkClean || result.RemoteState != "input_required" ||
		result.WorkState != "blocked" || result.Terminal {
		t.Fatalf("interrupt result = %#v", result)
	}
	if len(store.interrupts) != 1 {
		t.Fatalf("interrupt commands = %#v", store.interrupts)
	}
	command := store.interrupts[0]
	if command.BindingID != store.binding.ID || command.Generation != store.binding.Generation ||
		command.RemoteState != "input_required" || command.Route.ChannelID.IsZero() ||
		command.Route.SenderUserID.IsZero() || command.Route.RecipientUserID.IsZero() ||
		len(command.Requests) != 1 || len(command.Requests[0].KeyDigest) != 64 ||
		len(command.Requests[0].ContentDigest) != 64 {
		t.Fatalf("interrupt command = %#v", command)
	}
}

func TestK5OrchRemoteRequiresOperatorInterruptRoute(t *testing.T) {
	var cfg orchDispatchConfig
	cfg.A2A.Agents = []orchA2AAgentJSON{{
		SubjectRef: "agent:reporter", Authority: "https://reports.example.test", Name: "reporter",
		URL: "https://reports.example.test/a2a", Skill: "report", Scopes: []string{"reports:read"},
	}}
	_, err := newOrchRemoteExecutor(
		cfg, &fakeRemoteWorkStore{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err == nil || !strings.Contains(err.Error(), "interrupt_channel_id") {
		t.Fatalf("missing interrupt route error = %v", err)
	}
}

func TestK5OrchRemoteCancellationIntentStaysBlockedUntilTerminal(t *testing.T) {
	for _, state := range []a2a.TaskState{
		a2a.TaskStateSubmitted,
		a2a.TaskStateWorking,
		a2a.TaskStateUnspecified,
	} {
		mapped := translateA2AResult(a2a.TaskResult{
			TaskID: "task-cancel-pending", ResultKind: "task", State: state,
		}, "cancel_requested", true)
		if mapped.localState != "blocked" || mapped.terminal {
			t.Fatalf("state %q mapped to local=%q terminal=%t, want blocked/non-terminal",
				state, mapped.localState, mapped.terminal)
		}
	}

	confirmed := translateA2AResult(a2a.TaskResult{
		TaskID: "task-cancel-pending", ResultKind: "task",
		State: a2a.TaskStateCanceled, Terminal: true,
	}, "cancel_requested", true)
	if confirmed.localState != "canceled" || !confirmed.terminal ||
		confirmed.outcome != orchestration.RemoteWorkClean {
		t.Fatalf("confirmed cancellation = %#v", confirmed)
	}
}
