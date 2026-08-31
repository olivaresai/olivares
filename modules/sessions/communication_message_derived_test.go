// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type messageDerivedTestAuthorizer struct {
	authority  messageDerivedAuthority
	wantScope  DirectoryScopeRef
	wantID     model.ID
	wantTarget RecipientRef
	wantAction messageDerivedAction
	calls      int
}

func (a *messageDerivedTestAuthorizer) AuthorizeMessageDerived(
	_ context.Context,
	scope DirectoryScopeRef,
	messageID model.ID,
	target RecipientRef,
	action messageDerivedAction,
) (messageDerivedAuthority, error) {
	a.calls++
	if scope != a.wantScope || messageID != a.wantID || target != a.wantTarget || action != a.wantAction {
		return messageDerivedAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "test derived authority crossed its exact request",
		)
	}
	return cloneMessageDerivedAuthority(a.authority), nil
}

func messageDerivedTestClean(code string) AuthorityEvidence {
	return AuthorityEvidence{Verdict: VerdictClean, Code: code, EvidenceRef: "test:" + code}
}

func messageDerivedAuthorityForTest(
	fixture directNoticeFixture,
	actor CommunicationActorRef,
	principal CommunicationPrincipal,
	senderSubject CommunicationSubjectRef,
	target RecipientRef,
	action messageDerivedAction,
) messageDerivedAuthority {
	targetID := model.ID(target.Ref)
	targetPrincipal := CommunicationPrincipal{UserID: targetID}
	facts := append([]store.AuthorizationFactRef(nil), fixture.authorizer.facts...)
	return messageDerivedAuthority{
		Actor: actor, Principal: principal,
		SenderGrantClosure: ChannelGrantSubjectClosure{
			Scope: fixture.scope, Principal: principal, DirectoryEpoch: fixture.epoch,
			Outcome: ReadAllow, Code: "sender_subjects_resolved",
			Subjects: []CommunicationSubjectRef{senderSubject}, ObservedAt: fixture.now,
			FreshUntil: fixture.now.Add(5 * time.Minute), EvidenceRef: "test:derived-sender-closure",
		},
		RecipientGrantClosure: ChannelGrantSubjectClosure{
			Scope: fixture.scope, Principal: targetPrincipal, DirectoryEpoch: fixture.epoch,
			Outcome: ReadAllow, Code: "recipient_subjects_resolved",
			Subjects:   []CommunicationSubjectRef{{Kind: SubjectUser, Ref: target.Ref}},
			ObservedAt: fixture.now, FreshUntil: fixture.now.Add(5 * time.Minute),
			EvidenceRef: "test:derived-recipient-closure",
		},
		CoreWitness: ReadWitness{
			Outcome: ReadAllow, Code: "message_send_decided",
			Entity: EntityRef{
				TenantID: fixture.scope.TenantID, WorkspaceID: fixture.scope.WorkspaceID,
				Kind: channelKind, ID: fixture.channel.ID,
			},
			Operation: CommunicationMessageSend, Principal: principal,
			ObservedAt: fixture.now, FreshUntil: fixture.now.Add(5 * time.Minute),
			CorePermission: messageDerivedTestClean("message_send_permitted"),
			ResourceGuard:  messageDerivedTestClean("resource_guard_clean"),
			ForbidAbsence:  messageDerivedTestClean("forbid_absence_clean"),
			Facts:          facts, EvidenceRef: "test:derived-core-witness",
		},
		Facts: facts, ObservedAt: fixture.now, FreshUntil: fixture.now.Add(5 * time.Minute),
		ActionEvidence: messageDerivedTestClean("derived_" + string(action) + "_authorized"),
	}
}

func retractMessageForDerivedTest(
	t *testing.T,
	fixture directNoticeFixture,
	published DirectNoticePublishResult,
) messageLifecycleResult {
	t.Helper()
	authorizer := &messageLifecycleTestAuthorizer{
		wantScope: fixture.scope, wantID: published.MessageID, want: messageLifecycleRetract,
		authority: messageLifecycleAuthorityForTest(
			fixture, CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
		),
	}
	service, err := newMessageLifecycleService(fixture.m, authorizer, nil)
	if err != nil {
		t.Fatalf("new lifecycle service for reroute: %v", err)
	}
	result, err := service.Transition(messageLifecycleTestContext(t), fixture.scope,
		messageLifecycleRetract, messageLifecycleCommand{
			MessageID: published.MessageID, ExpectedVersion: published.Version,
			TerminalCode:   "reroute_predecessor",
			Reason:         CommunicationReasonContent{Code: "reroute_predecessor", Text: "recipient changed"},
			IdempotencyKey: model.NewID().String(),
		})
	if err != nil {
		t.Fatalf("retract reroute predecessor: %v", err)
	}
	return result
}

func newRerouteDerivedFixture(
	t *testing.T,
) (directNoticeFixture, DirectNoticePublishResult, messageLifecycleResult, RecipientRef) {
	t.Helper()
	fixture := newDirectNoticeExactAuthorityFixture(t)
	ctx := messageLifecycleTestContext(t)
	published, err := fixture.m.publishDirectNoticeWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.command(model.NewID(), "reroute payload"),
	)
	if err != nil {
		t.Fatalf("publish reroute predecessor: %v", err)
	}
	retracted := retractMessageForDerivedTest(t, fixture, published)
	target := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	createDirectNoticeGrantForTest(
		t, fixture, CommunicationSubjectRef{Kind: SubjectUser, Ref: target.Ref}, true, false,
	)
	return fixture, published, retracted, target
}

func TestMessageDerivedReroutePreservesPredecessorReplaysConflictsAndRollsBack(t *testing.T) {
	t.Parallel()

	t.Run("success_replay_and_conflict", func(t *testing.T) {
		fixture, published, retracted, target := newRerouteDerivedFixture(t)
		ctx := messageLifecycleTestContext(t)
		authority := messageDerivedAuthorityForTest(
			fixture,
			CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
			CommunicationPrincipal{UserID: fixture.sender},
			CommunicationSubjectRef{Kind: SubjectUser, Ref: fixture.sender.String()},
			target, messageDerivedReroute,
		)
		authorizer := &messageDerivedTestAuthorizer{
			authority: authority, wantScope: fixture.scope, wantID: published.MessageID,
			wantTarget: target, wantAction: messageDerivedReroute,
		}
		service, err := newMessageDerivedService(fixture.m, authorizer, nil)
		if err != nil {
			t.Fatalf("new derived service: %v", err)
		}
		sourceBefore, sourceDeliveriesBefore := lifecycleMessageAndDeliveriesForTest(
			t, fixture, published.MessageID,
		)
		beforeMessages := len(communicationRowsForTest(t, fixture, messageKind))
		beforeEvents := len(communicationRowsForTest(t, fixture, workEventKind))
		beforeOutbox := len(communicationRowsForTest(t, fixture, workOutboxKind))
		beforeReceipts := len(communicationRowsForTest(t, fixture, communicationCommandKind))
		command := messageRerouteCommand{
			MessageID: published.MessageID, ExpectedVersion: retracted.Version,
			Recipient: target, IdempotencyKey: model.NewID().String(),
		}
		result, err := service.Reroute(ctx, fixture.scope, command)
		if err != nil {
			t.Fatalf("reroute Message: %v", err)
		}
		successor, deliveries := lifecycleMessageAndDeliveriesForTest(t, fixture, result.MessageID)
		sourceAfter, sourceDeliveriesAfter := lifecycleMessageAndDeliveriesForTest(
			t, fixture, published.MessageID,
		)
		if result.State != MessagePublished || result.Version != 2 || successor.State != MessagePublished ||
			successor.SupersedesID != published.MessageID || successor.ReplyToID != "" ||
			successor.OriginEventID != sourceBefore.OriginEventID || successor.AutomationDepth != 0 ||
			len(deliveries) != 1 || deliveries[0].ID != result.DeliveryID ||
			deliveries[0].Recipient != target || deliveries[0].State != DeliveryAvailable || result.AuditSeq < 1 {
			t.Fatalf("reroute result/successor = %#v / %#v / %#v", result, successor, deliveries)
		}
		if !canonicalCommunicationValueEqual(sourceBefore, sourceAfter) ||
			!canonicalCommunicationValueEqual(sourceDeliveriesBefore, sourceDeliveriesAfter) {
			t.Fatal("reroute mutated its predecessor or original Delivery evidence")
		}
		if len(communicationRowsForTest(t, fixture, messageKind)) != beforeMessages+1 ||
			len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
			len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
			len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
			t.Fatal("reroute did not commit exactly one Message/event/outbox/receipt")
		}
		replay, err := service.Reroute(ctx, fixture.scope, command)
		if err != nil {
			t.Fatalf("replay reroute: %v", err)
		}
		if !replay.Replayed || replay.CommandID != result.CommandID || replay.MessageID != result.MessageID ||
			replay.DeliveryID != result.DeliveryID || replay.EventID != result.EventID ||
			replay.AuditSeq != result.AuditSeq {
			t.Fatalf("reroute replay diverged: first=%#v replay=%#v", result, replay)
		}
		conflict := command
		conflict.ExpectedVersion++
		if _, err = service.Reroute(ctx, fixture.scope, conflict); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("reroute idempotency conflict = %v, want store conflict", err)
		}
		if len(communicationRowsForTest(t, fixture, messageKind)) != beforeMessages+1 ||
			len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
			len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
			len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
			t.Fatal("reroute replay/conflict duplicated a durable effect")
		}
	})

	t.Run("receipt_failure_rolls_back", func(t *testing.T) {
		fixture, published, retracted, target := newRerouteDerivedFixture(t)
		authority := messageDerivedAuthorityForTest(
			fixture,
			CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
			CommunicationPrincipal{UserID: fixture.sender},
			CommunicationSubjectRef{Kind: SubjectUser, Ref: fixture.sender.String()},
			target, messageDerivedReroute,
		)
		service, err := newMessageDerivedService(fixture.m, &messageDerivedTestAuthorizer{
			authority: authority, wantScope: fixture.scope, wantID: published.MessageID,
			wantTarget: target, wantAction: messageDerivedReroute,
		}, nil)
		if err != nil {
			t.Fatalf("new rollback derived service: %v", err)
		}
		sourceBefore, deliveriesBefore := lifecycleMessageAndDeliveriesForTest(
			t, fixture, published.MessageID,
		)
		beforeMessages := len(communicationRowsForTest(t, fixture, messageKind))
		beforeEvents := len(communicationRowsForTest(t, fixture, workEventKind))
		beforeOutbox := len(communicationRowsForTest(t, fixture, workOutboxKind))
		beforeReceipts := len(communicationRowsForTest(t, fixture, communicationCommandKind))
		fixture.m.data = &directNoticeExactAckWriteFailureData{
			inner: fixture.m.data, kind: communicationCommandKind,
			operation: "create_with_id", failure: errors.New("derived receipt unavailable"),
		}
		_, err = service.Reroute(messageLifecycleTestContext(t), fixture.scope, messageRerouteCommand{
			MessageID: published.MessageID, ExpectedVersion: retracted.Version,
			Recipient: target, IdempotencyKey: model.NewID().String(),
		})
		if err == nil {
			t.Fatal("reroute succeeded despite receipt write failure")
		}
		sourceAfter, deliveriesAfter := lifecycleMessageAndDeliveriesForTest(t, fixture, published.MessageID)
		if !canonicalCommunicationValueEqual(sourceBefore, sourceAfter) ||
			!canonicalCommunicationValueEqual(deliveriesBefore, deliveriesAfter) ||
			len(communicationRowsForTest(t, fixture, messageKind)) != beforeMessages ||
			len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents ||
			len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox ||
			len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts {
			t.Fatal("reroute receipt failure left a partial durable effect")
		}
	})
}

func TestMessageDerivedEscalateOverdueCreatesOneOriginBoundStep(t *testing.T) {
	t.Parallel()

	ackFixture := newDirectNoticeExactAckFixtureWithTimeout(t, 1)
	fixture := ackFixture.directNoticeFixture
	ctx := messageLifecycleTestContext(t)
	messageBefore, _ := lifecycleMessageAndDeliveriesForTest(t, fixture, ackFixture.published.MessageID)
	if messageBefore.AckDueAt == nil {
		t.Fatal("overdue escalation fixture has no Ack deadline")
	}
	waitDirectNoticeExactAckDBTime(t, fixture, *messageBefore.AckDueAt)
	overdueAuthorizer := &messageLifecycleTestAuthorizer{
		wantScope: fixture.scope, wantID: ackFixture.published.MessageID, want: messageLifecycleOverdue,
		authority: messageLifecycleAuthorityForTest(
			fixture, CommunicationActorRef{Kind: ActorSystem, Ref: "message-deadline-worker"},
		),
	}
	lifecycle, err := newMessageLifecycleService(fixture.m, overdueAuthorizer, nil)
	if err != nil {
		t.Fatalf("new overdue lifecycle service: %v", err)
	}
	overdue, err := lifecycle.MaterializeOverdue(ctx, fixture.scope, messageOverdueCommand{
		MessageID: ackFixture.published.MessageID, ExpectedVersion: ackFixture.published.Version,
		IdempotencyKey: model.NewID().String(),
	})
	if err != nil {
		t.Fatalf("materialize escalation origin overdue: %v", err)
	}
	target := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	createDirectNoticeGrantForTest(
		t, fixture, CommunicationSubjectRef{Kind: SubjectUser, Ref: target.Ref}, true, false,
	)
	backingAgent := model.NewID()
	createDirectNoticeGrantForTest(
		t, fixture, CommunicationSubjectRef{Kind: SubjectAgent, Ref: backingAgent.String()}, false, true,
	)
	actor := CommunicationActorRef{Kind: ActorSystem, Ref: messageDerivedEscalatorRef}
	principal := CommunicationPrincipal{
		System: true, SystemActorRef: messageDerivedEscalatorRef, SystemGrantAgentID: backingAgent,
	}
	authority := messageDerivedAuthorityForTest(
		fixture, actor, principal,
		CommunicationSubjectRef{Kind: SubjectAgent, Ref: backingAgent.String()},
		target, messageDerivedEscalate,
	)
	service, err := newMessageDerivedService(fixture.m, &messageDerivedTestAuthorizer{
		authority: authority, wantScope: fixture.scope, wantID: ackFixture.published.MessageID,
		wantTarget: target, wantAction: messageDerivedEscalate,
	}, nil)
	if err != nil {
		t.Fatalf("new overdue escalation service: %v", err)
	}
	sourceBefore, deliveriesBefore := lifecycleMessageAndDeliveriesForTest(
		t, fixture, ackFixture.published.MessageID,
	)
	beforeMessages := len(communicationRowsForTest(t, fixture, messageKind))
	beforeEvents := len(communicationRowsForTest(t, fixture, workEventKind))
	beforeOutbox := len(communicationRowsForTest(t, fixture, workOutboxKind))
	beforeReceipts := len(communicationRowsForTest(t, fixture, communicationCommandKind))
	command := messageEscalateOverdueCommand{
		MessageID: ackFixture.published.MessageID, ExpectedVersion: overdue.Version,
		OriginEventID: overdue.EventID, Step: 1, Recipient: target,
	}
	result, err := service.EscalateOverdue(ctx, fixture.scope, command)
	if err != nil {
		t.Fatalf("escalate overdue: %v", err)
	}
	successor, deliveries := lifecycleMessageAndDeliveriesForTest(t, fixture, result.MessageID)
	sourceAfter, deliveriesAfter := lifecycleMessageAndDeliveriesForTest(
		t, fixture, ackFixture.published.MessageID,
	)
	if successor.Kind != MessageSystem || successor.State != MessagePublished ||
		successor.ReplyToID != ackFixture.published.MessageID ||
		successor.ThreadID != sourceBefore.ThreadID || successor.SupersedesID != "" ||
		successor.OriginEventID != overdue.EventID || successor.AutomationDepth != 1 ||
		result.AutomationDepth != 1 || len(deliveries) != 1 ||
		deliveries[0].Recipient != target || deliveries[0].State != DeliveryAvailable || result.AuditSeq < 1 {
		t.Fatalf("escalation result/successor = %#v / %#v / %#v", result, successor, deliveries)
	}
	if !canonicalCommunicationValueEqual(sourceBefore, sourceAfter) ||
		!canonicalCommunicationValueEqual(deliveriesBefore, deliveriesAfter) ||
		len(deliveriesAfter) != 1 || deliveriesAfter[0].State != DeliveryExpired {
		t.Fatal("overdue escalation mutated its source Message or original Delivery")
	}
	if len(communicationRowsForTest(t, fixture, messageKind)) != beforeMessages+1 ||
		len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
		len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
		len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
		t.Fatal("overdue escalation did not commit exactly one Message/event/outbox/receipt")
	}
	replay, err := service.EscalateOverdue(ctx, fixture.scope, command)
	if err != nil {
		t.Fatalf("replay overdue escalation: %v", err)
	}
	if !replay.Replayed || replay.CommandID != result.CommandID || replay.MessageID != result.MessageID ||
		replay.DeliveryID != result.DeliveryID || replay.EventID != result.EventID ||
		replay.AuditSeq != result.AuditSeq {
		t.Fatalf("overdue escalation replay diverged: first=%#v replay=%#v", result, replay)
	}
	conflict := command
	conflict.ExpectedVersion++
	if _, err = service.EscalateOverdue(ctx, fixture.scope, conflict); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("overdue escalation origin-step conflict = %v, want store conflict", err)
	}
	if len(communicationRowsForTest(t, fixture, messageKind)) != beforeMessages+1 ||
		len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
		len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
		len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
		t.Fatal("overdue escalation replay/conflict duplicated a durable effect")
	}
}
