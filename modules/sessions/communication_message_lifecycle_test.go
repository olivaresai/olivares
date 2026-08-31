// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type messageLifecycleTestAuthorizer struct {
	authority messageLifecycleAuthority
	wantScope DirectoryScopeRef
	wantID    model.ID
	want      messageLifecycleAction
	calls     int
}

func (a *messageLifecycleTestAuthorizer) AuthorizeMessageLifecycle(
	_ context.Context,
	scope DirectoryScopeRef,
	messageID model.ID,
	action messageLifecycleAction,
) (messageLifecycleAuthority, error) {
	a.calls++
	if scope != a.wantScope || messageID != a.wantID || action != a.want {
		return messageLifecycleAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "test lifecycle authority crossed its exact request",
		)
	}
	return a.authority, nil
}

func messageLifecycleTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(10*time.Minute))
	t.Cleanup(cancel)
	return ctx
}

func messageLifecycleAuthorityForTest(
	fixture directNoticeFixture,
	actor CommunicationActorRef,
) messageLifecycleAuthority {
	return messageLifecycleAuthority{
		Actor:      actor,
		Facts:      append([]store.AuthorizationFactRef(nil), fixture.authorizer.facts...),
		ObservedAt: fixture.now, FreshUntil: fixture.now.Add(5 * time.Minute),
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "lifecycle_authorized",
			EvidenceRef: "test:message-lifecycle-authority",
		},
	}
}

func lifecycleMessageAndDeliveriesForTest(
	t *testing.T,
	fixture directNoticeFixture,
	messageID model.ID,
) (Message, []MessageDelivery) {
	t.Helper()
	var message Message
	var deliveries []MessageDelivery
	err := fixture.m.viewCommunication(context.Background(), fixture.scope, func(sc store.Scope) error {
		var err error
		message, deliveries, err = readMessageLifecycleCarrierView(context.Background(), sc, messageID)
		return err
	})
	if err != nil {
		t.Fatalf("read lifecycle carrier: %v", err)
	}
	return message, deliveries
}

func TestMessageLifecycleRetractCommitsCascadeAndReplays(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAuthorityFixture(t)
	ctx := messageLifecycleTestContext(t)
	published, err := fixture.m.publishDirectNoticeWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.command(model.NewID(), "retract me"),
	)
	if err != nil {
		t.Fatalf("publish DirectNotice: %v", err)
	}
	authorizer := &messageLifecycleTestAuthorizer{
		wantScope: fixture.scope, wantID: published.MessageID, want: messageLifecycleRetract,
		authority: messageLifecycleAuthorityForTest(
			fixture, CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
		),
	}
	service, err := newMessageLifecycleService(fixture.m, authorizer, nil)
	if err != nil {
		t.Fatalf("new lifecycle service: %v", err)
	}
	beforeEvents := len(communicationRowsForTest(t, fixture, workEventKind))
	beforeOutbox := len(communicationRowsForTest(t, fixture, workOutboxKind))
	beforeReceipts := len(communicationRowsForTest(t, fixture, communicationCommandKind))
	command := messageLifecycleCommand{
		MessageID: published.MessageID, ExpectedVersion: published.Version,
		TerminalCode:   "sender_retracted",
		Reason:         CommunicationReasonContent{Code: "sender_retracted", Text: "obsolete notice"},
		IdempotencyKey: model.NewID().String(),
	}
	result, err := service.Transition(ctx, fixture.scope, messageLifecycleRetract, command)
	if err != nil {
		t.Fatalf("retract Message: %v", err)
	}
	message, deliveries := lifecycleMessageAndDeliveriesForTest(t, fixture, published.MessageID)
	if result.State != MessageRetracted || message.State != MessageRetracted ||
		result.Version != published.Version+1 || message.Version != result.Version ||
		result.DeliveryChanges != 1 || len(deliveries) != 1 ||
		deliveries[0].State != DeliveryRetracted || result.AuditSeq < 1 {
		t.Fatalf("retract result/carrier = %#v / %#v / %#v", result, message, deliveries)
	}
	if len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
		len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
		len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
		t.Fatal("retract did not commit exactly one event/outbox/receipt")
	}
	replay, err := service.Transition(ctx, fixture.scope, messageLifecycleRetract, command)
	if err != nil {
		t.Fatalf("replay retract: %v", err)
	}
	if !replay.Replayed || replay.CommandID != result.CommandID || replay.EventID != result.EventID ||
		replay.AuditSeq != result.AuditSeq || replay.Version != result.Version {
		t.Fatalf("retract replay diverged: first=%#v replay=%#v", result, replay)
	}
	if len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
		len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
		len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
		t.Fatal("retract replay duplicated a durable effect")
	}
}

func TestMessageLifecycleOverdueExpiresDeliveryOnceAndKeepsMessagePublished(t *testing.T) {
	t.Parallel()

	ackFixture := newDirectNoticeExactAckFixtureWithTimeout(t, 1)
	fixture := ackFixture.directNoticeFixture
	ctx := messageLifecycleTestContext(t)
	published := ackFixture.published
	messageBefore, deliveriesBefore := lifecycleMessageAndDeliveriesForTest(t, fixture, published.MessageID)
	if messageBefore.AckDueAt == nil || len(deliveriesBefore) != 1 ||
		deliveriesBefore[0].AckDueAt == nil || !deliveriesBefore[0].Required {
		t.Fatalf("required publish lacks Ack deadline: %#v / %#v", messageBefore, deliveriesBefore)
	}
	waitDirectNoticeExactAckDBTime(t, fixture, *messageBefore.AckDueAt)
	authorizer := &messageLifecycleTestAuthorizer{
		wantScope: fixture.scope, wantID: published.MessageID, want: messageLifecycleOverdue,
		authority: messageLifecycleAuthorityForTest(
			fixture, CommunicationActorRef{Kind: ActorSystem, Ref: "message-deadline-worker"},
		),
	}
	service, err := newMessageLifecycleService(fixture.m, authorizer, nil)
	if err != nil {
		t.Fatalf("new lifecycle service: %v", err)
	}
	beforeEvents := len(communicationRowsForTest(t, fixture, workEventKind))
	beforeOutbox := len(communicationRowsForTest(t, fixture, workOutboxKind))
	beforeReceipts := len(communicationRowsForTest(t, fixture, communicationCommandKind))
	command := messageOverdueCommand{
		MessageID: published.MessageID, ExpectedVersion: published.Version,
		IdempotencyKey: model.NewID().String(),
	}
	result, err := service.MaterializeOverdue(ctx, fixture.scope, command)
	if err != nil {
		t.Fatalf("materialize overdue: %v", err)
	}
	message, deliveries := lifecycleMessageAndDeliveriesForTest(t, fixture, published.MessageID)
	if message.State != MessagePublished || result.ExpiredCount != 1 || len(deliveries) != 1 ||
		deliveries[0].State != DeliveryExpired || result.Fulfillment.State != FulfillmentUnmet ||
		result.Fulfillment.Unmet != 1 || result.AuditSeq < 1 {
		t.Fatalf("overdue result/carrier = %#v / %#v / %#v", result, message, deliveries)
	}
	if len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
		len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
		len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
		t.Fatal("overdue did not commit exactly one event/outbox/receipt")
	}
	replay, err := service.MaterializeOverdue(ctx, fixture.scope, command)
	if err != nil {
		t.Fatalf("replay overdue: %v", err)
	}
	if !replay.Replayed || replay.CommandID != result.CommandID || replay.EventID != result.EventID ||
		replay.AuditSeq != result.AuditSeq || replay.Fulfillment != result.Fulfillment {
		t.Fatalf("overdue replay diverged: first=%#v replay=%#v", result, replay)
	}
	if len(communicationRowsForTest(t, fixture, workEventKind)) != beforeEvents+1 ||
		len(communicationRowsForTest(t, fixture, workOutboxKind)) != beforeOutbox+1 ||
		len(communicationRowsForTest(t, fixture, communicationCommandKind)) != beforeReceipts+1 {
		t.Fatal("overdue replay duplicated a durable effect")
	}
}
