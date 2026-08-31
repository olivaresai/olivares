// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type workflowCommunicationFixture struct {
	directNoticeFixture
	workID    model.ID
	actor     WorkflowCommunicationActor
	target    RecipientRef
	targetRef auth.PrincipalRef
}

func newWorkflowCommunicationFixture(t *testing.T, withLease bool) workflowCommunicationFixture {
	t.Helper()
	return newWorkflowCommunicationFixtureFromDirect(
		t, withLease, newDirectNoticeExactAuthorityFixture(t),
	)
}

func newWorkflowCommunicationFixtureFromDirect(
	t *testing.T,
	withLease bool,
	fixture directNoticeFixture,
) workflowCommunicationFixture {
	t.Helper()
	ctx := context.Background()
	targetUser, err := fixture.authr.OnboardMember(
		ctx, fixture.authUser, fixture.tenant, auth.OnboardInput{
			Email: "workflow-target@communication.test", DisplayName: "Workflow target",
			Role: auth.RoleViewer, Password: "workflow-target-password",
		},
	)
	if err != nil {
		t.Fatalf("onboard workflow target: %v", err)
	}
	token, _, err := fixture.authr.Login(
		ctx, "workflow-target@communication.test", "workflow-target-password", "127.0.0.8",
	)
	if err != nil {
		t.Fatalf("login workflow target: %v", err)
	}
	targetPrincipal, err := fixture.authr.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("authenticate workflow target: %v", err)
	}
	targetRef, ok := targetPrincipal.Ref()
	if !ok {
		t.Fatal("workflow target has no opaque principal reference")
	}
	var authorizationFact store.AuthorizationFactRef
	err = fixture.m.viewCommunication(ctx, fixture.scope, func(sc store.Scope) error {
		directory := sc.(store.DirectorySnapshotReader)
		epoch, readErr := directory.ReadDirectoryEpoch(ctx)
		if readErr != nil {
			return readErr
		}
		fixture.epoch = epoch.Version
		authorization := sc.(store.AuthorizationEpochReader)
		authorizationFact, readErr = authorization.ReadAuthorizationEpoch(ctx)
		return readErr
	})
	if err != nil {
		t.Fatalf("refresh workflow authority epochs: %v", err)
	}
	authorityFacts := []store.AuthorizationFactRef{
		authorizationFact,
		{Kind: model.DirectoryEpochKind, ID: model.ID(fixture.tenant), Version: fixture.epoch},
	}
	fixture.attestor.epoch = fixture.epoch
	fixture.authorizer.facts = append([]store.AuthorizationFactRef(nil), authorityFacts...)
	fixture.source.evidence.Facts = append([]store.AuthorizationFactRef(nil), authorityFacts...)
	fixture.source.evidence.ObservedAt = fixture.now
	fixture.source.evidence.FreshUntil = fixture.now.Add(5 * time.Minute)
	fixture.m.communicationDirectoryResolver = &directNoticeReadDirectoryResolver{
		now: fixture.now, epoch: fixture.epoch,
	}
	fixture.m.communicationGrantClosure = &directNoticeReadClosureResolver{
		now: fixture.now, epoch: fixture.epoch,
	}
	grantID := model.NewID()
	grant := ChannelGrant{
		MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: grantID, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
			Version: 1, CreatedAt: fixture.now,
		}, UpdatedAt: fixture.now},
		ChannelID:  fixture.channel.ID,
		Subject:    CommunicationSubjectRef{Kind: SubjectUser, Ref: targetUser.User.ID.String()},
		Generation: 1, CanRead: true, State: ChannelGrantActive,
		GrantedBy: CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
	}
	grantRecord, err := channelGrantToRecord(grant)
	if err != nil {
		t.Fatalf("encode workflow target grant: %v", err)
	}
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, channelGrantKind, grantID, grantRecord,
	); err != nil {
		t.Fatalf("create workflow target grant: %v", err)
	}
	workID := model.NewID()
	work := workSchemaItem(fixture.workspace, "K4 workflow communication")
	work[colWorkOwnerKind] = string(RecipientUser)
	work[colWorkOwnerRef] = fixture.sender.String()
	work[colWorkLastEventSeq] = int64(1)
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, workItemKind, workID, work,
	); err != nil {
		t.Fatalf("create workflow WorkItem: %v", err)
	}
	event := workSchemaEvent(
		fixture.workspace, workID.String(), model.NewID().String(), 1, "workflow-created",
	)
	event[colEventActorRef] = fixture.sender.String()
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, workEventKind, model.NewID(), event,
	); err != nil {
		t.Fatalf("create workflow context Event: %v", err)
	}
	if withLease {
		if _, err := communicationCreateWithID(
			ctx, fixture.m, fixture.tenant, workLeaseKind, model.NewID(), model.Record{
				colWorkWorkspaceID: fixture.workspace.String(), colWorkItemID: workID.String(),
				colLeaseHolderSID:      "osn_" + model.NewID().String(),
				colLeaseHolderRunRef:   "run:workflow-source",
				colLeaseHolderAgentRef: "agent:workflow-source",
				colLeaseFence:          int64(9), colLeaseState: workLeaseActive,
				colLeaseAcquiredAt:   model.NewTimestamp(fixture.now.Add(-time.Minute)).String(),
				colLeaseExpiresAt:    model.NewTimestamp(fixture.now.Add(10 * time.Minute)).String(),
				colLeaseRenewalCount: int64(0),
			},
		); err != nil {
			t.Fatalf("create workflow WorkLease: %v", err)
		}
		if _, err := communicationCreateWithID(
			ctx, fixture.m, fixture.tenant, workGuardKind, model.NewID(), model.Record{
				colWorkWorkspaceID: fixture.workspace.String(), colGuardKind: "lease_clock",
				colGuardEpoch:          int64(1),
				colGuardLastDBTime:     model.NewTimestamp(fixture.now).String(),
				colGuardRebaseDecision: nil, colGuardRebaseEvidence: nil,
			},
		); err != nil {
			t.Fatalf("create workflow lease clock guard: %v", err)
		}
	}
	return workflowCommunicationFixture{
		directNoticeFixture: fixture, workID: workID,
		actor: WorkflowCommunicationActor{
			AuditKind: model.ActorUser, AuditRef: "user:" + fixture.sender.String(),
		},
		target:    RecipientRef{Kind: RecipientUser, Ref: targetUser.User.ID.String()},
		targetRef: targetRef,
	}
}

func workflowCommunicationRecord(
	t *testing.T,
	fixture workflowCommunicationFixture,
	kind model.Kind,
	id model.ID,
) model.Record {
	t.Helper()
	for _, row := range communicationRowsForTest(t, fixture.directNoticeFixture, kind) {
		if recordID(row) == id || (kind == workEventKind && row.String(colEventID) == id.String()) {
			return row
		}
	}
	t.Fatalf("%s %s is absent", kind, id)
	return nil
}

func TestWorkflowWorkTaskPersistsAndReplaysWithDurableCursor(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, false)
	ctx := context.Background()
	cmd := WorkflowWorkTaskCommand{
		Actor: fixture.actor, WorkItemID: fixture.workID, ChannelID: fixture.channel.ID,
		Recipient: fixture.target,
		Content: MessageContent{Subject: "Continue K4", Blocks: []MessageContentBlock{{
			Type: ContentBlockText, Format: TextPlain, Text: "Apply the next owned work step.",
		}}},
		IdempotencyKey: "workflow-task:" + model.NewID().String(),
	}
	result, err := fixture.m.SendWorkflowWorkTask(ctx, fixture.tenant, cmd)
	if err != nil {
		t.Fatalf("send workflow WorkTask: %v", err)
	}
	if result.WorkItemID != fixture.workID || result.MessageID.IsZero() ||
		result.DeliveryID.IsZero() || result.EventID.IsZero() || result.CommandID.IsZero() ||
		result.EventSeq != 2 || result.Version != 2 || result.State != MessagePublished || result.Replayed {
		t.Fatalf("workflow WorkTask result = %+v", result)
	}
	messageRecord := workflowCommunicationRecord(t, fixture, messageKind, result.MessageID)
	message, err := messageFromRecord(messageRecord, 0)
	if err != nil {
		t.Fatalf("decode workflow WorkTask: %v", err)
	}
	if message.Kind != MessageWorkTask || message.WorkItemID != fixture.workID ||
		message.Sender != (CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()}) {
		t.Fatalf("stored workflow WorkTask = %+v", message)
	}
	event := workflowCommunicationRecord(t, fixture, workEventKind, result.EventID)
	if event.String(colEventAggregateKind) != string(workItemKind) ||
		event.String(colEventAggregateID) != fixture.workID.String() ||
		event.Int(colEventSeq) != result.EventSeq ||
		event.String(colEventType) != workflowWorkTaskEventType {
		t.Fatalf("workflow WorkTask Event = %+v", event)
	}
	replayed, err := fixture.m.SendWorkflowWorkTask(ctx, fixture.tenant, cmd)
	if err != nil {
		t.Fatalf("replay workflow WorkTask: %v", err)
	}
	if !replayed.Replayed || replayed.MessageID != result.MessageID ||
		replayed.DeliveryID != result.DeliveryID || replayed.EventID != result.EventID ||
		replayed.EventSeq != result.EventSeq {
		t.Fatalf("workflow WorkTask replay = %+v, want %+v", replayed, result)
	}
	observation, err := fixture.m.ObserveWorkflowAck(ctx, fixture.tenant, WorkflowAckQuery{
		Actor: fixture.actor, TargetKind: WorkflowAckTargetMessage, TargetID: result.MessageID,
	})
	if err != nil {
		t.Fatalf("observe workflow WorkTask: %v", err)
	}
	if observation.Status != WorkflowAckPending || observation.EventID != result.EventID ||
		observation.EventSeq != result.EventSeq || observation.Detail != "message_available" {
		t.Fatalf("workflow WorkTask observation = %+v", observation)
	}
	consumed, err := fixture.m.ObserveWorkflowAck(ctx, fixture.tenant, WorkflowAckQuery{
		Actor: fixture.actor, TargetKind: WorkflowAckTargetMessage,
		TargetID: result.MessageID, AfterEventSeq: result.EventSeq,
	})
	if err != nil {
		t.Fatalf("observe consumed workflow WorkTask cursor: %v", err)
	}
	if consumed.Status != WorkflowAckPending || consumed.EventID != "" || consumed.EventSeq != 0 {
		t.Fatalf("consumed workflow WorkTask observation = %+v", consumed)
	}
}

func TestWorkflowHandoffUsesExactCarrierAndOffer(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	cmd := WorkflowHandoffCommand{
		Actor: fixture.actor, WorkItemID: fixture.workID, ChannelID: fixture.channel.ID,
		Target:      fixture.target,
		Content:     HandoffContent{Summary: "Transfer K4 work", NextAction: "Continue the owned step"},
		AckDeadline: fixture.now.Add(4 * time.Minute), ExpectedOwnerEpoch: 1,
		IdempotencyKey: "workflow-handoff:" + model.NewID().String(),
	}
	result, err := fixture.m.OfferWorkflowHandoff(ctx, fixture.tenant, cmd)
	if err != nil {
		t.Fatalf("offer workflow Handoff: %v", err)
	}
	if result.WorkItemID != fixture.workID || result.HandoffID.IsZero() ||
		result.MessageID.IsZero() || result.DeliveryID.IsZero() || result.EventID.IsZero() ||
		result.EventSeq != 3 || result.Version != 1 || result.State != HandoffOffered ||
		result.OwnerEpoch != 1 || result.Replayed {
		t.Fatalf("workflow Handoff result = %+v", result)
	}
	messageRecord := workflowCommunicationRecord(t, fixture, messageKind, result.MessageID)
	message, err := messageFromRecord(messageRecord, 1)
	if err != nil {
		t.Fatalf("decode workflow Handoff carrier: %v", err)
	}
	if message.Kind != MessageHandoffOffer || message.WorkItemID != fixture.workID ||
		message.AckPolicy != AckPolicyEachRequired || message.AckDueAt == nil {
		t.Fatalf("workflow Handoff carrier = %+v", message)
	}
	handoffRecord := workflowCommunicationRecord(t, fixture, handoffKind, result.HandoffID)
	handoff, err := handoffFromRecord(handoffRecord)
	if err != nil {
		t.Fatalf("decode workflow Handoff: %v", err)
	}
	if handoff.MessageID != result.MessageID || handoff.DeliveryID != result.DeliveryID ||
		handoff.To != fixture.target || handoff.FromOwnerEpoch != 1 ||
		handoff.ContextEventSeq != 2 {
		t.Fatalf("stored workflow Handoff = %+v", handoff)
	}
	replayed, err := fixture.m.OfferWorkflowHandoff(ctx, fixture.tenant, cmd)
	if err != nil {
		t.Fatalf("replay workflow Handoff: %v", err)
	}
	if !replayed.Replayed || replayed.HandoffID != result.HandoffID ||
		replayed.MessageID != result.MessageID || replayed.EventID != result.EventID ||
		replayed.EventSeq != result.EventSeq {
		t.Fatalf("workflow Handoff replay = %+v, want %+v", replayed, result)
	}
	observation, err := fixture.m.ObserveWorkflowAck(ctx, fixture.tenant, WorkflowAckQuery{
		Actor: fixture.actor, TargetKind: WorkflowAckTargetHandoff, TargetID: result.HandoffID,
	})
	if err != nil {
		t.Fatalf("observe workflow Handoff: %v", err)
	}
	if observation.Status != WorkflowAckPending || observation.EventID != result.EventID ||
		observation.EventSeq != result.EventSeq || observation.Detail != "handoff_offered" {
		t.Fatalf("workflow Handoff observation = %+v", observation)
	}
	response, err := fixture.m.respondHandoffWithAuthority(
		ctx, fixture.scope, fixture.targetRef, result.HandoffID,
		HandoffResponseCommand{
			Transition: HandoffAccept, IfMatch: "\"v1\"",
			IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("accept workflow Handoff through exact K3 seam: %v", err)
	}
	if response.State != HandoffAccepted || response.AckID.IsZero() ||
		response.EventID.IsZero() || response.OwnerEpoch != 2 {
		t.Fatalf("workflow Handoff response = %+v", response)
	}
	accepted, err := fixture.m.ObserveWorkflowAck(ctx, fixture.tenant, WorkflowAckQuery{
		Actor: fixture.actor, TargetKind: WorkflowAckTargetHandoff,
		TargetID: result.HandoffID, AfterEventSeq: result.EventSeq,
	})
	if err != nil {
		t.Fatalf("observe accepted workflow Handoff: %v", err)
	}
	if accepted.Status != WorkflowAckAcknowledged || accepted.AckID != response.AckID ||
		accepted.EventID != response.EventID || accepted.EventSeq <= result.EventSeq ||
		accepted.Detail != "handoff_accepted" {
		t.Fatalf("accepted workflow Handoff observation = %+v", accepted)
	}
}

func TestWorkflowHandoffRejectsOwnerEpochBeforeCarrier(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, true)
	beforeMessages := len(communicationRowsForTest(t, fixture.directNoticeFixture, messageKind))
	_, err := fixture.m.OfferWorkflowHandoff(context.Background(), fixture.tenant, WorkflowHandoffCommand{
		Actor: fixture.actor, WorkItemID: fixture.workID, ChannelID: fixture.channel.ID,
		Target:      fixture.target,
		Content:     HandoffContent{Summary: "Stale transfer", NextAction: "Must not be offered"},
		AckDeadline: fixture.now.Add(4 * time.Minute), ExpectedOwnerEpoch: 2,
		IdempotencyKey: "workflow-handoff-stale:" + model.NewID().String(),
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale workflow Handoff error = %v, want conflict", err)
	}
	if len(communicationRowsForTest(t, fixture.directNoticeFixture, messageKind)) != beforeMessages ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, handoffKind)) != 0 {
		t.Fatal("stale workflow Handoff persisted a carrier or aggregate")
	}
}
