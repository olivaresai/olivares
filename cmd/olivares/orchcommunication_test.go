// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/sessions"
)

type recordingWorkflowCommunicationKernel struct {
	tenant model.TenantID

	messageCalls  int
	message       sessions.WorkflowWorkTaskCommand
	messageResult sessions.WorkflowWorkTaskResult
	messageErr    error

	handoffCalls  int
	handoff       sessions.WorkflowHandoffCommand
	handoffResult sessions.WorkflowHandoffResult
	handoffErr    error

	ackCalls  int
	ack       sessions.WorkflowAckQuery
	ackResult sessions.WorkflowAckObservation
	ackErr    error
}

func (k *recordingWorkflowCommunicationKernel) SendWorkflowWorkTask(
	_ context.Context,
	tenant model.TenantID,
	cmd sessions.WorkflowWorkTaskCommand,
) (sessions.WorkflowWorkTaskResult, error) {
	k.tenant = tenant
	k.messageCalls++
	k.message = cmd
	return k.messageResult, k.messageErr
}

func (k *recordingWorkflowCommunicationKernel) OfferWorkflowHandoff(
	_ context.Context,
	tenant model.TenantID,
	cmd sessions.WorkflowHandoffCommand,
) (sessions.WorkflowHandoffResult, error) {
	k.tenant = tenant
	k.handoffCalls++
	k.handoff = cmd
	return k.handoffResult, k.handoffErr
}

func (k *recordingWorkflowCommunicationKernel) ObserveWorkflowAck(
	_ context.Context,
	tenant model.TenantID,
	query sessions.WorkflowAckQuery,
) (sessions.WorkflowAckObservation, error) {
	k.tenant = tenant
	k.ackCalls++
	k.ack = query
	return k.ackResult, k.ackErr
}

func completeWorkflowMessageResult(workItemID model.ID) sessions.WorkflowWorkTaskResult {
	return sessions.WorkflowWorkTaskResult{
		WorkItemID: workItemID, CommandID: model.NewID(), MessageID: model.NewID(),
		DeliveryID: model.NewID(), EventID: model.NewID(), EventSeq: 17,
		Version: 1, State: sessions.MessagePublished,
	}
}

func completeWorkflowHandoffResult(
	workItemID model.ID,
	ownerEpoch int64,
) sessions.WorkflowHandoffResult {
	return sessions.WorkflowHandoffResult{
		WorkItemID: workItemID, CommandID: model.NewID(), HandoffID: model.NewID(),
		MessageID: model.NewID(), DeliveryID: model.NewID(), EventID: model.NewID(),
		EventSeq: 23, Version: 1, State: sessions.HandoffOffered, OwnerEpoch: ownerEpoch,
	}
}

func workflowCommunicationTestActor(userID model.ID) orchestration.WorkActor {
	return orchestration.WorkActor{
		Kind: "token", Ref: "token:workflow-credential", UserIdentity: userID,
	}
}

func TestWorkflowCommunicationAdapterMapsWorkMessage(t *testing.T) {
	tenant, workItemID, channelID := model.NewTenantID(), model.NewID(), model.NewID()
	userID, recipientID := model.NewID(), model.NewID()
	sessionID := "osn_" + model.NewID().String()
	deadline := model.NewTimestamp(time.Date(2026, time.August, 18, 20, 0, 0, 0, time.UTC))
	kernel := &recordingWorkflowCommunicationKernel{
		messageResult: completeWorkflowMessageResult(workItemID),
	}
	adapter := &workflowCommunicationAdapter{kernel: kernel}
	actor := workflowCommunicationTestActor(userID)
	actor.AgentIdentity = "provider-agent-17"
	actor.SessionIdentity = sessionID
	actor.SessionRunRef = "run-provider-17"
	actor.SessionFence = 9
	actor.PurposeRestricted = true
	semanticKey := "workflow-run:message:primary"

	got, err := adapter.SendWorkMessage(context.Background(), tenant, orchestration.WorkMessageRequest{
		RunRef: "workflow-run", StepRef: "message", Actor: actor,
		IdempotencyKey: semanticKey, WorkItemID: workItemID, ChannelID: channelID,
		Recipient: orchestration.WorkParticipant{Kind: "user", Ref: recipientID.String()},
		Body:      "Complete the K4 communication adapter.", AckDueAt: deadline.String(),
		Urgency: "high",
	})
	if err != nil {
		t.Fatalf("SendWorkMessage: %v", err)
	}
	if kernel.messageCalls != 1 || kernel.tenant != tenant {
		t.Fatalf("message calls/tenant = %d/%s", kernel.messageCalls, kernel.tenant)
	}
	cmd := kernel.message
	if cmd.Actor.AuditKind != model.ActorUser || cmd.Actor.AuditRef != "user:"+userID.String() ||
		cmd.Actor.AgentExternalID != actor.AgentIdentity || cmd.Actor.SessionID != sessionID ||
		cmd.Actor.SessionRunRef != actor.SessionRunRef || cmd.Actor.SessionFence != 9 ||
		!cmd.Actor.PurposeRestricted {
		t.Fatalf("communication actor = %+v", cmd.Actor)
	}
	if cmd.WorkItemID != workItemID || cmd.ChannelID != channelID ||
		cmd.Recipient != (sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: recipientID.String()}) ||
		cmd.Urgency != sessions.UrgencyHigh || cmd.AckDueAt == nil ||
		!cmd.AckDueAt.Equal(deadline.Time()) || cmd.IdempotencyKey != semanticKey {
		t.Fatalf("message command = %+v", cmd)
	}
	if cmd.Content.Subject != workflowMessageSubject || len(cmd.Content.Blocks) != 1 ||
		cmd.Content.Blocks[0].Type != sessions.ContentBlockText ||
		cmd.Content.Blocks[0].Format != sessions.TextPlain ||
		cmd.Content.Blocks[0].Text != "Complete the K4 communication adapter." {
		t.Fatalf("message content = %+v", cmd.Content)
	}
	want := kernel.messageResult
	if got.WorkItemID != workItemID || got.MessageID != want.MessageID ||
		got.CommandID != want.CommandID || got.EventID != want.EventID || got.EventSeq != want.EventSeq {
		t.Fatalf("message result = %+v", got)
	}
}

func TestWorkflowCommunicationAdapterMapsMessageAndHandoffReferences(t *testing.T) {
	message, err := workflowMessageContent(model.NewID(), "", "artifact:brief:k4")
	if err != nil {
		t.Fatalf("workflowMessageContent reference: %v", err)
	}
	if len(message.Blocks) != 1 || message.Blocks[0].Type != sessions.ContentBlockReference ||
		message.Blocks[0].Reference == nil ||
		*message.Blocks[0].Reference != (sessions.ContentReference{
			Kind: workflowMessageBodyRefKind, Ref: "artifact:brief:k4",
		}) {
		t.Fatalf("message reference = %+v", message.Blocks)
	}

	handoff, err := workflowHandoffContent("", "artifact:context:k4")
	if err != nil {
		t.Fatalf("workflowHandoffContent reference: %v", err)
	}
	if handoff.Summary != "Handoff context is attached by reference." ||
		handoff.NextAction != workflowHandoffNextAction || len(handoff.ArtifactRefs) != 1 ||
		handoff.ArtifactRefs[0] != (sessions.ContentReference{
			Kind: workflowHandoffContextKind, Ref: "artifact:context:k4",
		}) {
		t.Fatalf("handoff reference = %+v", handoff)
	}
	inline, err := workflowHandoffContent("Continue from the durable checkpoint.", "")
	if err != nil || inline.Summary != "Continue from the durable checkpoint." ||
		inline.NextAction != workflowHandoffNextAction || len(inline.ArtifactRefs) != 0 {
		t.Fatalf("inline handoff = %+v (err=%v)", inline, err)
	}
}

func TestWorkflowCommunicationRecipientMapsEveryExactKind(t *testing.T) {
	tests := []struct {
		kind string
		ref  string
		want sessions.RecipientKind
	}{
		{kind: "user", ref: model.NewID().String(), want: sessions.RecipientUser},
		{kind: "agent", ref: model.NewID().String(), want: sessions.RecipientAgent},
		{kind: "session", ref: "osn_" + model.NewID().String(), want: sessions.RecipientSession},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			got, err := workflowCommunicationRecipient(orchestration.WorkParticipant{
				Kind: test.kind, Ref: test.ref,
			})
			if err != nil || got.Kind != test.want || got.Ref != test.ref {
				t.Fatalf("recipient = %+v (err=%v)", got, err)
			}
		})
	}
}

func TestWorkflowCommunicationAdapterMapsHandoffAndOwnerEpoch(t *testing.T) {
	tenant, workItemID, channelID := model.NewTenantID(), model.NewID(), model.NewID()
	userID, targetID := model.NewID(), model.NewID()
	deadline := model.NewTimestamp(time.Date(2026, time.August, 18, 21, 0, 0, 0, time.UTC))
	kernel := &recordingWorkflowCommunicationKernel{
		handoffResult: completeWorkflowHandoffResult(workItemID, 7),
	}
	adapter := &workflowCommunicationAdapter{kernel: kernel}
	semanticKey := "workflow-run:handoff:primary"

	got, err := adapter.OfferWorkHandoff(context.Background(), tenant, orchestration.WorkHandoffRequest{
		RunRef: "workflow-run", StepRef: "handoff", Actor: workflowCommunicationTestActor(userID),
		IdempotencyKey: semanticKey, WorkItemID: workItemID, ExpectedOwnerEpoch: 7,
		ChannelID:  channelID,
		Target:     orchestration.WorkParticipant{Kind: "user", Ref: targetID.String()},
		ContextRef: "artifact:context:k4", AckDeadline: deadline.String(),
	})
	if err != nil {
		t.Fatalf("OfferWorkHandoff: %v", err)
	}
	if kernel.handoffCalls != 1 || kernel.tenant != tenant {
		t.Fatalf("handoff calls/tenant = %d/%s", kernel.handoffCalls, kernel.tenant)
	}
	cmd := kernel.handoff
	if cmd.Actor.AuditKind != model.ActorUser || cmd.Actor.AuditRef != "user:"+userID.String() ||
		cmd.WorkItemID != workItemID || cmd.ChannelID != channelID ||
		cmd.Target != (sessions.RecipientRef{Kind: sessions.RecipientUser, Ref: targetID.String()}) ||
		cmd.ExpectedOwnerEpoch != 7 || !cmd.AckDeadline.Equal(deadline.Time()) ||
		cmd.IdempotencyKey != semanticKey || len(cmd.Content.ArtifactRefs) != 1 ||
		cmd.Content.ArtifactRefs[0].Ref != "artifact:context:k4" {
		t.Fatalf("handoff command = %+v", cmd)
	}
	want := kernel.handoffResult
	if got.WorkItemID != workItemID || got.HandoffID != want.HandoffID ||
		got.MessageID != want.MessageID || got.CommandID != want.CommandID ||
		got.EventID != want.EventID || got.EventSeq != want.EventSeq || got.OwnerEpoch != 7 {
		t.Fatalf("handoff result = %+v", got)
	}
}

func TestWorkflowCommunicationAdapterMapsEveryAckStatus(t *testing.T) {
	statusCases := []struct {
		from sessions.WorkflowAckStatus
		want orchestration.WorkAckStatus
	}{
		{sessions.WorkflowAckPending, orchestration.WorkAckPending},
		{sessions.WorkflowAckAcknowledged, orchestration.WorkAckAcknowledged},
		{sessions.WorkflowAckRejected, orchestration.WorkAckRejected},
		{sessions.WorkflowAckExpired, orchestration.WorkAckExpired},
		{sessions.WorkflowAckUnknown, orchestration.WorkAckUnknown},
	}
	for _, test := range statusCases {
		t.Run(string(test.from), func(t *testing.T) {
			tenant, userID, targetID := model.NewTenantID(), model.NewID(), model.NewID()
			eventID, ackID := model.NewID(), model.NewID()
			kernel := &recordingWorkflowCommunicationKernel{ackResult: sessions.WorkflowAckObservation{
				Status: test.from, AckID: ackID, EventID: eventID, EventSeq: 31,
				Detail: "durable acknowledgement state",
			}}
			adapter := &workflowCommunicationAdapter{kernel: kernel}
			got, err := adapter.ObserveWorkAck(context.Background(), tenant, orchestration.WorkAckQuery{
				Actor: workflowCommunicationTestActor(userID), TargetKind: "handoff",
				TargetID: targetID, AfterEventSeq: 19,
			})
			if err != nil {
				t.Fatalf("ObserveWorkAck: %v", err)
			}
			if kernel.ackCalls != 1 || kernel.ack.TargetKind != sessions.WorkflowAckTargetHandoff ||
				kernel.ack.TargetID != targetID || kernel.ack.AfterEventSeq != 19 ||
				kernel.ack.Actor.AuditRef != "user:"+userID.String() {
				t.Fatalf("ack query = %+v", kernel.ack)
			}
			if got.Status != test.want || got.AckID != ackID || got.EventID != eventID ||
				got.EventSeq != 31 || got.Detail != "durable acknowledgement state" {
				t.Fatalf("ack observation = %+v", got)
			}
		})
	}
}

func TestWorkflowCommunicationAdapterAcceptsAckWithoutNewerEvent(t *testing.T) {
	kernel := &recordingWorkflowCommunicationKernel{ackResult: sessions.WorkflowAckObservation{
		Status: sessions.WorkflowAckPending, Detail: "message_available",
	}}
	adapter := &workflowCommunicationAdapter{kernel: kernel}
	got, err := adapter.ObserveWorkAck(
		context.Background(),
		model.NewTenantID(),
		orchestration.WorkAckQuery{
			Actor: workflowCommunicationTestActor(model.NewID()), TargetKind: "message",
			TargetID: model.NewID(), AfterEventSeq: 11,
		},
	)
	if err != nil || got.Status != orchestration.WorkAckPending ||
		!got.EventID.IsZero() || got.EventSeq != 0 {
		t.Fatalf("pending observation = %+v (err=%v)", got, err)
	}
}

func TestWorkflowCommunicationAdapterRejectsInvalidActorRecipientAndEvidence(t *testing.T) {
	tenant, workItemID := model.NewTenantID(), model.NewID()
	kernel := &recordingWorkflowCommunicationKernel{
		messageResult: completeWorkflowMessageResult(workItemID),
		handoffResult: completeWorkflowHandoffResult(workItemID, 4),
	}
	adapter := &workflowCommunicationAdapter{kernel: kernel}
	valid := orchestration.WorkMessageRequest{
		Actor: workflowCommunicationTestActor(model.NewID()), WorkItemID: workItemID,
		ChannelID: model.NewID(), Recipient: orchestration.WorkParticipant{
			Kind: "user", Ref: model.NewID().String(),
		}, Body: "K4", IdempotencyKey: "workflow-run:message:primary",
	}

	invalidActor := valid
	invalidActor.Actor = orchestration.WorkActor{Kind: model.ActorUser, Ref: "user:unbound"}
	if _, err := adapter.SendWorkMessage(context.Background(), tenant, invalidActor); err == nil {
		t.Fatal("message with no durable actor identity succeeded")
	}
	invalidRecipient := valid
	invalidRecipient.Recipient = orchestration.WorkParticipant{Kind: "group", Ref: model.NewID().String()}
	if _, err := adapter.SendWorkMessage(context.Background(), tenant, invalidRecipient); err == nil {
		t.Fatal("message with unsupported recipient succeeded")
	}
	invalidTimestamp := valid
	invalidTimestamp.AckDueAt = "2026-08-18T20:00:00Z"
	if _, err := adapter.SendWorkMessage(context.Background(), tenant, invalidTimestamp); err == nil {
		t.Fatal("message with non-canonical acknowledgement timestamp succeeded")
	}
	if kernel.messageCalls != 0 {
		t.Fatalf("invalid inputs reached communication kernel %d times", kernel.messageCalls)
	}

	kernel.messageResult.DeliveryID = ""
	if _, err := adapter.SendWorkMessage(context.Background(), tenant, valid); err == nil {
		t.Fatal("message with incomplete durable evidence succeeded")
	}
	kernel.handoffResult.OwnerEpoch = 5
	deadline := model.NewTimestamp(time.Now().UTC().Add(time.Hour)).String()
	if _, err := adapter.OfferWorkHandoff(context.Background(), tenant, orchestration.WorkHandoffRequest{
		Actor: workflowCommunicationTestActor(model.NewID()), WorkItemID: workItemID,
		ExpectedOwnerEpoch: 4, ChannelID: model.NewID(),
		Target:  orchestration.WorkParticipant{Kind: "user", Ref: model.NewID().String()},
		Context: "Transfer K4.", AckDeadline: deadline, IdempotencyKey: "handoff",
	}); err == nil {
		t.Fatal("handoff with mismatched owner epoch evidence succeeded")
	}
}
