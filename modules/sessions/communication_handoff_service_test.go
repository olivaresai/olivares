// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type handoffServiceFixture struct {
	directNoticeFixture
	targetRef auth.PrincipalRef
	workID    model.ID
	leaseID   model.ID
	message   Message
	delivery  MessageDelivery
}

func newHandoffServiceFixture(t *testing.T) handoffServiceFixture {
	return newHandoffServiceFixtureWithDurableAckDelay(t, 0)
}

func newHandoffServiceFixtureWithDurableAckDelay(
	t *testing.T,
	durableAckDelay time.Duration,
) handoffServiceFixture {
	t.Helper()
	fixture := newDirectNoticeExactAuthorityFixture(t)
	ctx := context.Background()
	target, err := fixture.authr.OnboardMember(
		ctx, fixture.authUser, fixture.tenant, auth.OnboardInput{
			Email: "handoff-target@direct-notice.test", DisplayName: "Handoff target",
			Role: auth.RoleViewer, Password: "handoff-target-password",
		},
	)
	if err != nil {
		t.Fatalf("onboard Handoff target: %v", err)
	}
	token, _, err := fixture.authr.Login(
		ctx, "handoff-target@direct-notice.test", "handoff-target-password", "127.0.0.3",
	)
	if err != nil {
		t.Fatalf("login Handoff target: %v", err)
	}
	targetPrincipal, err := fixture.authr.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("authenticate Handoff target: %v", err)
	}
	targetRef, ok := targetPrincipal.Ref()
	if !ok {
		t.Fatal("Handoff target has no opaque principal reference")
	}

	var directoryEpoch int64
	var authorizationFact store.AuthorizationFactRef
	err = fixture.m.viewCommunication(ctx, fixture.scope, func(sc store.Scope) error {
		directory, ok := sc.(store.DirectorySnapshotReader)
		if !ok {
			return fmt.Errorf("directory snapshot reader unavailable")
		}
		epoch, err := directory.ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		directoryEpoch = epoch.Version
		authorization, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return fmt.Errorf("authorization epoch reader unavailable")
		}
		authorizationFact, err = authorization.ReadAuthorizationEpoch(ctx)
		return err
	})
	if err != nil {
		t.Fatalf("refresh Handoff authority epochs: %v", err)
	}
	fixture.epoch = directoryEpoch
	fixture.source.evidence.Facts = []store.AuthorizationFactRef{
		authorizationFact,
		{Kind: model.DirectoryEpochKind, ID: model.ID(fixture.tenant), Version: directoryEpoch},
	}
	fixture.source.evidence.ObservedAt = fixture.now
	fixture.source.evidence.FreshUntil = fixture.now.Add(5 * time.Minute)
	fixture.m.communicationDirectoryResolver = &directNoticeReadDirectoryResolver{
		now: fixture.now, epoch: directoryEpoch,
	}
	fixture.m.communicationGrantClosure = &directNoticeReadClosureResolver{
		now: fixture.now, epoch: directoryEpoch,
	}

	grantID := model.NewID()
	grant := ChannelGrant{
		MutableCommunicationEntity: MutableCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: grantID, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
				Version: 1, CreatedAt: fixture.now,
			},
			UpdatedAt: fixture.now,
		},
		ChannelID:  fixture.channel.ID,
		Subject:    CommunicationSubjectRef{Kind: SubjectUser, Ref: target.User.ID.String()},
		Generation: 1, CanRead: true, State: ChannelGrantActive,
		GrantedBy: CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
	}
	grantRecord, err := channelGrantToRecord(grant)
	if err != nil {
		t.Fatalf("encode target ChannelGrant: %v", err)
	}
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, channelGrantKind, grantID, grantRecord,
	); err != nil {
		t.Fatalf("create target ChannelGrant: %v", err)
	}

	workID := model.NewID()
	workRecord := workSchemaItem(fixture.workspace, "K3 handoff")
	workRecord[colWorkOwnerKind] = string(RecipientUser)
	workRecord[colWorkOwnerRef] = fixture.sender.String()
	workRecord[colWorkLastEventSeq] = int64(1)
	createdWork, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, workItemKind, workID, workRecord,
	)
	if err != nil {
		t.Fatalf("create Handoff WorkItem: %v", err)
	}
	contextEvent := workSchemaEvent(
		fixture.workspace, workID.String(), model.NewID().String(), 1, "handoff-context",
	)
	contextEvent[colEventActorRef] = fixture.sender.String()
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, workEventKind, model.NewID(), contextEvent,
	); err != nil {
		t.Fatalf("create Handoff context WorkEvent: %v", err)
	}

	leaseID := model.NewID()
	leaseRecord := model.Record{
		colWorkWorkspaceID: fixture.workspace.String(), colWorkItemID: workID.String(),
		colLeaseHolderSID:      "osn_" + model.NewID().String(),
		colLeaseHolderRunRef:   "run:handoff-source",
		colLeaseHolderAgentRef: "agent:handoff-source",
		colLeaseFence:          int64(7), colLeaseState: workLeaseActive,
		colLeaseAcquiredAt:   model.NewTimestamp(fixture.now.Add(-time.Minute)).String(),
		colLeaseExpiresAt:    model.NewTimestamp(fixture.now.Add(10 * time.Minute)).String(),
		colLeaseRenewalCount: int64(0),
	}
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, workLeaseKind, leaseID, leaseRecord,
	); err != nil {
		t.Fatalf("create Handoff WorkLease: %v", err)
	}
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, workGuardKind, model.NewID(), model.Record{
			colWorkWorkspaceID: fixture.workspace.String(), colGuardKind: "lease_clock",
			colGuardEpoch: int64(1), colGuardLastDBTime: model.NewTimestamp(fixture.now).String(),
			colGuardRebaseDecision: nil, colGuardRebaseEvidence: nil,
		},
	); err != nil {
		t.Fatalf("create Handoff lease clock guard: %v", err)
	}

	messageID, audienceID := model.NewID(), model.NewID()
	deliveryID, contributionID := model.NewID(), model.NewID()
	baseMutable := func(id model.ID) MutableCommunicationEntity {
		return MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: id, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
			Version: 1, CreatedAt: fixture.now,
		}, UpdatedAt: fixture.now}
	}
	baseAppendOnly := func(id model.ID) AppendOnlyCommunicationEntity {
		return AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: id, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
			Version: 1, CreatedAt: fixture.now,
		}}
	}
	ackDue := fixture.now.Add(4 * time.Minute)
	if durableAckDelay > 0 {
		var durableNow model.Timestamp
		err := fixture.m.viewCommunication(ctx, fixture.scope, func(sc store.Scope) error {
			clock, ok := sc.(store.TransactionClock)
			if !ok {
				return errors.New("Handoff fixture lacks durable transaction clock")
			}
			var clockErr error
			durableNow, clockErr = clock.TransactionNow(ctx)
			return clockErr
		})
		if err != nil {
			t.Fatalf("read durable Handoff deadline clock: %v", err)
		}
		ackDue = durableNow.Time().Add(durableAckDelay)
	}
	expiresAt := ackDue.Add(time.Minute)
	message := Message{
		MutableCommunicationEntity: baseMutable(messageID),
		ChannelID:                  fixture.channel.ID, WorkItemID: workID, ThreadID: messageID,
		Kind: MessageHandoffOffer, State: MessageDraft,
		Sender:  CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
		Payload: communicationTestPayloadForSlot(t, PayloadSlotMessage),
		Urgency: UrgencyNormal, AckPolicy: AckPolicyEachRequired,
		AvailableAt: fixture.now, AckDueAt: &ackDue, ExpiresAt: &expiresAt,
	}
	selector := AudienceSelector{
		Kind: AudienceUser, Ref: target.User.ID.String(), Required: true, WakePolicy: WakeNone,
	}
	selectorRaw, err := canonicalJSON(selector)
	if err != nil {
		t.Fatalf("canonical Handoff selector: %v", err)
	}
	selectorHash := sha256.Sum256(selectorRaw)
	audience := MessageAudience{
		AppendOnlyCommunicationEntity: baseAppendOnly(audienceID), MessageID: messageID,
		Ordinal: 1, Selector: selector, ChannelACLRevision: fixture.channel.ACLRevision,
		RouteRevision:        fixture.channel.RouteRevision,
		SubscriptionRevision: fixture.channel.SubscriptionRevision,
		DirectoryEpoch:       directoryEpoch, DirectorySnapshotAt: fixture.now,
		ResolvedCount: 1, SelectorHash: selectorHash[:], ResolvedHash: make([]byte, sha256.Size),
	}
	delivery := MessageDelivery{
		MutableCommunicationEntity: baseMutable(deliveryID), MessageID: messageID,
		Recipient:      RecipientRef{Kind: RecipientUser, Ref: target.User.ID.String()},
		RecipientEpoch: 1, DeliverySeq: 1, Required: true,
		RouteReasons: []RouteReason{"direct"}, WakePolicy: WakeNone,
		State: DeliveryAvailable, AvailableAt: fixture.now,
		AckDueAt: &ackDue, ExpiresAt: &expiresAt,
	}
	contribution := communicationStateTestSealCausalArc(MessageAudienceRecipient{
		AppendOnlyCommunicationEntity: baseAppendOnly(contributionID),
		MessageAudienceID:             audienceID, MessageDeliveryID: deliveryID,
		Recipient: delivery.Recipient, RecipientEpoch: delivery.RecipientEpoch,
		Required: true, WakePolicy: WakeNone, RouteReasons: []RouteReason{"direct"},
		Selector: selector, DirectoryEpoch: directoryEpoch,
		ChannelACLRevision:   fixture.channel.ACLRevision,
		RouteRevision:        fixture.channel.RouteRevision,
		SubscriptionRevision: fixture.channel.SubscriptionRevision,
		CausalKind:           CausalDirect, CausalRef: target.User.ID.String(),
	})
	audience.ResolvedHash, err = canonicalResolvedAudienceHash(
		audience, []MessageAudienceRecipient{contribution},
	)
	if err != nil {
		t.Fatalf("Handoff resolved audience hash: %v", err)
	}
	message.AudienceHash, err = CanonicalMessageAudienceHash(
		message, []MessageAudience{audience}, []MessageAudienceRecipient{contribution},
	)
	if err != nil {
		t.Fatalf("Handoff audience hash: %v", err)
	}
	audienceHash := append([]byte(nil), message.AudienceHash...)
	message.AudienceHash = nil

	messageRecord, err := messageToRecord(message, 1)
	if err != nil {
		t.Fatalf("encode draft Handoff Message: %v", err)
	}
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, messageKind, message.ID, messageRecord,
	); err != nil {
		t.Fatalf("create draft Handoff Message: %v", err)
	}
	audienceRecord, _ := messageAudienceToRecord(audience)
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, messageAudienceKind, audience.ID, audienceRecord,
	); err != nil {
		t.Fatalf("create Handoff Audience: %v", err)
	}
	deliveryRecord, _ := messageDeliveryToRecord(delivery)
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, messageDeliveryKind, delivery.ID, deliveryRecord,
	); err != nil {
		t.Fatalf("create Handoff Delivery: %v", err)
	}
	contributionRecord, _ := messageAudienceRecipientToRecord(contribution)
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, messageAudienceRecipientKind,
		contribution.ID, contributionRecord,
	); err != nil {
		t.Fatalf("create Handoff contribution: %v", err)
	}
	publishedAt := fixture.now
	message.State, message.PublishedAt, message.AudienceHash = MessagePublished, &publishedAt, audienceHash
	publishedRecord, err := messageToRecord(message, 1)
	if err != nil {
		t.Fatalf("encode published Handoff Message: %v", err)
	}
	updatedMessage, err := communicationUpdate(
		ctx, fixture.m, fixture.tenant, messageKind, publishedRecord,
	)
	if err != nil {
		t.Fatalf("publish Handoff Message: %v", err)
	}
	message, err = messageFromRecord(updatedMessage, 1)
	if err != nil {
		t.Fatalf("decode published Handoff Message: %v", err)
	}
	if createdWork.Int(model.ColVersion) != 1 {
		t.Fatalf("seed WorkItem version = %d, want 1", createdWork.Int(model.ColVersion))
	}
	return handoffServiceFixture{
		directNoticeFixture: fixture, targetRef: targetRef,
		workID: workID, leaseID: leaseID, message: message, delivery: delivery,
	}
}

type handoffDeadlineAuthorizerStub struct {
	authority handoffDeadlineAuthority
	scope     DirectoryScopeRef
	handoffID model.ID
	calls     int
}

func (a *handoffDeadlineAuthorizerStub) AuthorizeHandoffDeadline(
	_ context.Context,
	scope DirectoryScopeRef,
	handoffID model.ID,
) (handoffDeadlineAuthority, error) {
	a.calls++
	a.scope, a.handoffID = scope, handoffID
	result := a.authority
	result.Facts = append([]store.AuthorizationFactRef(nil), a.authority.Facts...)
	return result, nil
}

func handoffStoredRecord(t *testing.T, fixture handoffServiceFixture, kind model.Kind, id model.ID) model.Record {
	t.Helper()
	for _, row := range communicationRowsForTest(t, fixture.directNoticeFixture, kind) {
		if recordID(row) == id || (kind == workEventKind && row.String(colEventID) == id.String()) {
			return row
		}
	}
	t.Fatalf("%s %s is absent", kind, id)
	return nil
}

func TestHandoffOfferAndAcceptPersistAtomicTransfer(t *testing.T) {
	t.Parallel()

	fixture := newHandoffServiceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	offerCommand := HandoffOfferCommand{
		ChannelID: fixture.channel.ID, WorkItemID: fixture.workID,
		MessageID: fixture.message.ID, DeliveryID: fixture.delivery.ID,
		Content: HandoffContent{Summary: "Transfer K3 ownership", NextAction: "Continue implementation"},
		IfMatch: "\"v1\"", IdempotencyKey: model.NewID().String(),
	}
	if _, err := fixture.m.OfferHandoff(
		ctx, fixture.scope, fixture.ref, offerCommand,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, handoffKind)) != 0 {
		t.Fatalf("public Handoff boundary opened before K3 readiness: %v", err)
	}
	offer, err := fixture.m.offerHandoffWithAuthority(ctx, fixture.scope, fixture.ref, offerCommand)
	if err != nil {
		t.Fatalf("offer Handoff: %v", err)
	}
	if offer.State != HandoffOffered || offer.Version != 1 || offer.ETag != "\"v1\"" {
		t.Fatalf("offer result = %+v", offer)
	}
	workAfterOffer := handoffStoredRecord(t, fixture, workItemKind, fixture.workID)
	leaseAfterOffer := handoffStoredRecord(t, fixture, workLeaseKind, fixture.leaseID)
	if workAfterOffer.String(colWorkOwnerRef) != fixture.sender.String() ||
		workAfterOffer.Int(colWorkOwnerEpoch) != 1 ||
		leaseAfterOffer.Int(colLeaseFence) != 7 ||
		leaseAfterOffer.String(colLeaseState) != workLeaseActive ||
		leaseAfterOffer.Int(model.ColVersion) != 1 {
		t.Fatalf("offer changed owner/lease: work=%v lease=%v", workAfterOffer, leaseAfterOffer)
	}
	replay, err := fixture.m.offerHandoffWithAuthority(ctx, fixture.scope, fixture.ref, offerCommand)
	if err != nil || !replay.Replayed || replay.CommandID != offer.CommandID || replay.HandoffID != offer.HandoffID {
		t.Fatalf("offer replay = %+v, err %v", replay, err)
	}

	response, err := fixture.m.respondHandoffWithAuthority(
		ctx, fixture.scope, fixture.targetRef, offer.HandoffID,
		HandoffResponseCommand{
			Transition: HandoffAccept, IfMatch: offer.ETag,
			IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("accept Handoff: %v", err)
	}
	if response.State != HandoffAccepted || response.Version != 2 || response.AckID == "" ||
		response.OwnerEpoch != 2 || response.ResultingLeaseFence != 8 {
		t.Fatalf("accept result = %+v", response)
	}
	storedHandoff, err := handoffFromRecord(
		handoffStoredRecord(t, fixture, handoffKind, offer.HandoffID),
	)
	if err != nil {
		t.Fatalf("decode accepted Handoff: %v", err)
	}
	workAfterAccept := handoffStoredRecord(t, fixture, workItemKind, fixture.workID)
	leaseAfterAccept := handoffStoredRecord(t, fixture, workLeaseKind, fixture.leaseID)
	deliveryAfterAccept, err := messageDeliveryFromRecord(
		handoffStoredRecord(t, fixture, messageDeliveryKind, fixture.delivery.ID),
	)
	if err != nil {
		t.Fatalf("decode acknowledged Delivery: %v", err)
	}
	if storedHandoff.State != HandoffAccepted || storedHandoff.AckID != response.AckID ||
		storedHandoff.ResultingLeaseFence != 8 ||
		workAfterAccept.String(colWorkOwnerRef) != fixture.delivery.Recipient.Ref ||
		workAfterAccept.Int(colWorkOwnerEpoch) != 2 ||
		leaseAfterAccept.String(colLeaseState) != workLeaseRevoked ||
		leaseAfterAccept.Int(colLeaseFence) != 8 ||
		deliveryAfterAccept.State != DeliveryAcknowledged ||
		deliveryAfterAccept.AckID != response.AckID {
		t.Fatalf(
			"atomic transfer mismatch: handoff=%+v work=%v lease=%v delivery=%+v",
			storedHandoff, workAfterAccept, leaseAfterAccept, deliveryAfterAccept,
		)
	}
	ack, err := messageAckFromRecord(
		handoffStoredRecord(t, fixture, messageAckKind, response.AckID),
	)
	if err != nil || ack.Late || ack.DeliveryID != fixture.delivery.ID {
		t.Fatalf("stored Handoff Ack = %+v, err %v", ack, err)
	}
	event := handoffStoredRecord(t, fixture, workEventKind, response.EventID)
	if event.String(colEventCommandID) != response.CommandID.String() ||
		event.Int(colEventAuditSeq) != response.AuditSeq ||
		!bytes.Equal(event.Bytes(colEventAuditHash), storedReceiptAuditHash(t, fixture, response.CommandID)) {
		t.Fatalf("accepted Handoff Event anchor is incomplete: %v", event)
	}
}

func TestHandoffRejectRollsBackMismatchAndLeavesLeaseUntouched(t *testing.T) {
	t.Parallel()

	fixture := newHandoffServiceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	offer, err := fixture.m.offerHandoffWithAuthority(
		ctx, fixture.scope, fixture.ref,
		HandoffOfferCommand{
			ChannelID: fixture.channel.ID, WorkItemID: fixture.workID,
			MessageID: fixture.message.ID, DeliveryID: fixture.delivery.ID,
			Content: HandoffContent{Summary: "Offer for rejection", NextAction: "Review"},
			IfMatch: "\"v1\"", IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("offer Handoff for reject: %v", err)
	}
	beforeEvents := len(communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind))
	beforeReceipts := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	))
	reason := &CommunicationReasonContent{Code: "not_ready", Text: "Target cannot continue"}
	if _, err := fixture.m.respondHandoffWithAuthority(
		ctx, fixture.scope, fixture.targetRef, offer.HandoffID,
		HandoffResponseCommand{
			Transition: HandoffReject, Reason: reason,
			IfMatch: "\"v99\"", IdempotencyKey: model.NewID().String(),
		},
	); err == nil {
		t.Fatal("Handoff reject accepted a stale If-Match")
	}
	rolledBack, err := handoffFromRecord(
		handoffStoredRecord(t, fixture, handoffKind, offer.HandoffID),
	)
	if err != nil || rolledBack.State != HandoffOffered ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind)) != beforeEvents ||
		len(communicationRowsForTest(
			t, fixture.directNoticeFixture, communicationCommandKind,
		)) != beforeReceipts {
		t.Fatalf("stale response did not roll back: Handoff=%+v err=%v", rolledBack, err)
	}

	result, err := fixture.m.respondHandoffWithAuthority(
		ctx, fixture.scope, fixture.targetRef, offer.HandoffID,
		HandoffResponseCommand{
			Transition: HandoffReject, Reason: reason,
			IfMatch: offer.ETag, IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("reject Handoff: %v", err)
	}
	stored, err := handoffFromRecord(
		handoffStoredRecord(t, fixture, handoffKind, offer.HandoffID),
	)
	if err != nil {
		t.Fatalf("decode rejected Handoff: %v", err)
	}
	work := handoffStoredRecord(t, fixture, workItemKind, fixture.workID)
	lease := handoffStoredRecord(t, fixture, workLeaseKind, fixture.leaseID)
	delivery, err := messageDeliveryFromRecord(
		handoffStoredRecord(t, fixture, messageDeliveryKind, fixture.delivery.ID),
	)
	if err != nil {
		t.Fatalf("decode Delivery after reject: %v", err)
	}
	if result.State != HandoffRejected || result.AckID != "" ||
		result.ResultingLeaseFence != 0 || stored.TerminalReason == nil ||
		work.String(colWorkOwnerRef) != fixture.sender.String() ||
		work.Int(colWorkOwnerEpoch) != 1 || lease.Int(model.ColVersion) != 1 ||
		lease.Int(colLeaseFence) != 7 || lease.String(colLeaseState) != workLeaseActive ||
		delivery.State != DeliveryAvailable || delivery.AckID != "" {
		t.Fatalf(
			"reject changed owner/lease/Ack: result=%+v work=%v lease=%v delivery=%+v",
			result, work, lease, delivery,
		)
	}
}

func TestHandoffOwnerCancelIsDurableAndLeavesOwnerLeaseAndAckUntouched(t *testing.T) {
	t.Parallel()

	fixture := newHandoffServiceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	offer, err := fixture.m.offerHandoffWithAuthority(
		ctx, fixture.scope, fixture.ref,
		HandoffOfferCommand{
			ChannelID: fixture.channel.ID, WorkItemID: fixture.workID,
			MessageID: fixture.message.ID, DeliveryID: fixture.delivery.ID,
			Content: HandoffContent{
				Summary: "Cancelable ownership offer", NextAction: "Wait for owner decision",
			},
			IfMatch: "\"v1\"", IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("offer Handoff for cancel: %v", err)
	}
	command := HandoffCancelCommand{
		Reason: CommunicationReasonContent{
			Code: "source_changed", Text: "Source owner retained the work",
		},
		IfMatch: offer.ETag, IdempotencyKey: model.NewID().String(),
	}
	beforeEvents := len(communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind))
	beforeReceipts := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	))
	if _, err := fixture.m.CancelHandoff(
		ctx, fixture.scope, fixture.ref, offer.HandoffID, command,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("public Handoff cancel opened before K3 readiness: %v", err)
	}
	stillOffered, err := handoffFromRecord(
		handoffStoredRecord(t, fixture, handoffKind, offer.HandoffID),
	)
	if err != nil || stillOffered.State != HandoffOffered ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind)) != beforeEvents ||
		len(communicationRowsForTest(
			t, fixture.directNoticeFixture, communicationCommandKind,
		)) != beforeReceipts {
		t.Fatalf("OFF cancel boundary wrote state: Handoff=%+v err=%v", stillOffered, err)
	}
	stale := command
	stale.IfMatch = "\"v99\""
	stale.IdempotencyKey = model.NewID().String()
	if _, err := fixture.m.cancelHandoffWithAuthority(
		ctx, fixture.scope, fixture.ref, offer.HandoffID, stale,
	); !errors.Is(err, errHandoffVersionMismatch) {
		t.Fatalf("cancel stale If-Match = %v, want version mismatch", err)
	}
	stillOffered, err = handoffFromRecord(
		handoffStoredRecord(t, fixture, handoffKind, offer.HandoffID),
	)
	if err != nil || stillOffered.State != HandoffOffered ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind)) != beforeEvents ||
		len(communicationRowsForTest(
			t, fixture.directNoticeFixture, communicationCommandKind,
		)) != beforeReceipts {
		t.Fatalf("stale cancel did not roll back: Handoff=%+v err=%v", stillOffered, err)
	}

	result, err := fixture.m.cancelHandoffWithAuthority(
		ctx, fixture.scope, fixture.ref, offer.HandoffID, command,
	)
	if err != nil {
		t.Fatalf("cancel Handoff: %v", err)
	}
	if result.State != HandoffWithdrawn || result.Version != 2 || result.ETag != "\"v2\"" {
		t.Fatalf("cancel result = %+v", result)
	}
	replay, err := fixture.m.cancelHandoffWithAuthority(
		ctx, fixture.scope, fixture.ref, offer.HandoffID, command,
	)
	if err != nil || !replay.Replayed || replay.CommandID != result.CommandID ||
		replay.EventID != result.EventID {
		t.Fatalf("cancel replay = %+v, err %v", replay, err)
	}
	stored, err := handoffFromRecord(
		handoffStoredRecord(t, fixture, handoffKind, offer.HandoffID),
	)
	if err != nil {
		t.Fatalf("decode withdrawn Handoff: %v", err)
	}
	work := handoffStoredRecord(t, fixture, workItemKind, fixture.workID)
	lease := handoffStoredRecord(t, fixture, workLeaseKind, fixture.leaseID)
	delivery, err := messageDeliveryFromRecord(
		handoffStoredRecord(t, fixture, messageDeliveryKind, fixture.delivery.ID),
	)
	if err != nil {
		t.Fatalf("decode Delivery after cancel: %v", err)
	}
	if stored.State != HandoffWithdrawn || stored.WithdrawnAt == nil ||
		stored.TerminalCode != handoffWithdrawnCode || stored.TerminalReason == nil ||
		stored.AckID != "" || stored.ResultingLeaseFence != 0 ||
		work.String(colWorkOwnerRef) != fixture.sender.String() ||
		work.Int(colWorkOwnerEpoch) != 1 || lease.Int(model.ColVersion) != 1 ||
		lease.Int(colLeaseFence) != 7 || lease.String(colLeaseState) != workLeaseActive ||
		delivery.State != DeliveryAvailable || delivery.AckID != "" {
		t.Fatalf(
			"cancel changed owner/lease/Ack: Handoff=%+v work=%v lease=%v delivery=%+v",
			stored, work, lease, delivery,
		)
	}
	event := handoffStoredRecord(t, fixture, workEventKind, result.EventID)
	if event.String(colEventType) != handoffWithdrawnEventType ||
		event.String(colEventCommandID) != result.CommandID.String() ||
		event.Int(colEventAuditSeq) != result.AuditSeq ||
		!bytes.Equal(event.Bytes(colEventAuditHash), storedReceiptAuditHash(t, fixture, result.CommandID)) {
		t.Fatalf("withdrawn Handoff Event anchor is incomplete: %v", event)
	}
}

func TestHandoffDeadlineReaperExpiresAtDeadlineWithRollbackAndReplay(t *testing.T) {
	t.Parallel()

	fixture := newHandoffServiceFixtureWithDurableAckDelay(t, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	offer, err := fixture.m.offerHandoffWithAuthority(
		ctx, fixture.scope, fixture.ref,
		HandoffOfferCommand{
			ChannelID: fixture.channel.ID, WorkItemID: fixture.workID,
			MessageID: fixture.message.ID, DeliveryID: fixture.delivery.ID,
			Content: HandoffContent{
				Summary: "Deadline ownership offer", NextAction: "Expire when unanswered",
			},
			IfMatch: "\"v1\"", IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("offer Handoff for expiry: %v", err)
	}
	facts, err := CanonicalAuthorizationFacts(fixture.source.evidence.Facts)
	if err != nil {
		t.Fatalf("canonical deadline facts: %v", err)
	}
	authorizer := &handoffDeadlineAuthorizerStub{authority: handoffDeadlineAuthority{
		Actor: CommunicationActorRef{Kind: ActorSystem, Ref: "handoff-deadline-reaper"},
		Facts: facts, ObservedAt: fixture.now, FreshUntil: fixture.now.Add(10 * time.Minute),
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "handoff_deadline_authorized",
			EvidenceRef: "handoff-deadline:reaper",
		},
	}}
	service, err := newHandoffDeadlineService(fixture.m, authorizer, nil)
	if err != nil {
		t.Fatalf("new Handoff deadline service: %v", err)
	}
	command := handoffDeadlineCommand{
		HandoffID: offer.HandoffID, ExpectedVersion: offer.Version,
		IdempotencyKey: model.NewID().String(),
	}
	beforeEvents := len(communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind))
	beforeReceipts := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	))
	if _, err := service.Expire(ctx, fixture.scope, command); err == nil {
		t.Fatal("Handoff expired before its acknowledgement deadline")
	}
	rolledBack, err := handoffFromRecord(
		handoffStoredRecord(t, fixture, handoffKind, offer.HandoffID),
	)
	if err != nil || rolledBack.State != HandoffOffered ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind)) != beforeEvents ||
		len(communicationRowsForTest(
			t, fixture.directNoticeFixture, communicationCommandKind,
		)) != beforeReceipts {
		t.Fatalf("early deadline did not roll back: Handoff=%+v err=%v", rolledBack, err)
	}
	waitHandoffDurableDeadline(t, fixture, rolledBack.AckDeadline)
	result, err := service.Expire(ctx, fixture.scope, command)
	if err != nil {
		t.Fatalf("expire Handoff at deadline: %v", err)
	}
	if authorizer.calls != 2 || authorizer.scope != fixture.scope ||
		authorizer.handoffID != offer.HandoffID || result.State != HandoffExpired ||
		result.Version != 2 || result.ETag != "\"v2\"" {
		t.Fatalf("deadline result/authority = %+v, authorizer=%+v", result, authorizer)
	}
	replay, err := service.Expire(ctx, fixture.scope, command)
	if err != nil || !replay.Replayed || replay.CommandID != result.CommandID ||
		replay.EventID != result.EventID {
		t.Fatalf("deadline replay = %+v, err %v", replay, err)
	}
	stored, err := handoffFromRecord(
		handoffStoredRecord(t, fixture, handoffKind, offer.HandoffID),
	)
	if err != nil {
		t.Fatalf("decode expired Handoff: %v", err)
	}
	work := handoffStoredRecord(t, fixture, workItemKind, fixture.workID)
	lease := handoffStoredRecord(t, fixture, workLeaseKind, fixture.leaseID)
	delivery, err := messageDeliveryFromRecord(
		handoffStoredRecord(t, fixture, messageDeliveryKind, fixture.delivery.ID),
	)
	if err != nil {
		t.Fatalf("decode Delivery after expiry: %v", err)
	}
	if stored.State != HandoffExpired || stored.ExpiredAt == nil ||
		stored.ExpiredAt.Before(stored.AckDeadline) ||
		stored.TerminalCode != handoffDeadlineElapsedCode || stored.TerminalReason == nil ||
		stored.AckID != "" || stored.ResultingLeaseFence != 0 ||
		work.String(colWorkOwnerRef) != fixture.sender.String() ||
		work.Int(colWorkOwnerEpoch) != 1 || lease.Int(model.ColVersion) != 1 ||
		lease.Int(colLeaseFence) != 7 || lease.String(colLeaseState) != workLeaseActive ||
		delivery.State != DeliveryAvailable || delivery.AckID != "" {
		t.Fatalf(
			"expiry changed owner/lease/Ack: Handoff=%+v work=%v lease=%v delivery=%+v",
			stored, work, lease, delivery,
		)
	}
	event := handoffStoredRecord(t, fixture, workEventKind, result.EventID)
	if event.String(colEventType) != handoffExpiredEventType ||
		event.String(colEventActorKind) != string(ActorSystem) ||
		event.String(colEventActorRef) != authorizer.authority.Actor.Ref ||
		event.Int(colEventAuditSeq) != result.AuditSeq ||
		!bytes.Equal(event.Bytes(colEventAuditHash), storedReceiptAuditHash(t, fixture, result.CommandID)) {
		t.Fatalf("expired Handoff Event anchor is incomplete: %v", event)
	}
}

func storedReceiptAuditHash(
	t *testing.T,
	fixture handoffServiceFixture,
	commandID model.ID,
) []byte {
	t.Helper()
	for _, row := range communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	) {
		receipt, err := communicationCommandReceiptFromRecord(row)
		if err == nil && receipt.CommandID == commandID {
			return receipt.AuditHash
		}
	}
	t.Fatalf("receipt for command %s is absent", commandID)
	return nil
}

func waitHandoffDurableDeadline(
	t *testing.T,
	fixture handoffServiceFixture,
	deadline time.Time,
) {
	t.Helper()
	waitUntil := time.Now().Add(10 * time.Second)
	for {
		var observed model.Timestamp
		err := fixture.m.viewCommunication(
			context.Background(), fixture.scope, func(sc store.Scope) error {
				clock, ok := sc.(store.TransactionClock)
				if !ok {
					return errors.New("Handoff deadline wait lacks transaction clock")
				}
				var clockErr error
				observed, clockErr = clock.TransactionNow(context.Background())
				return clockErr
			},
		)
		if err != nil {
			t.Fatalf("observe Handoff durable deadline: %v", err)
		}
		if !observed.Time().Before(deadline) {
			return
		}
		if time.Now().After(waitUntil) {
			t.Fatalf("durable time %s did not reach Handoff deadline %s", observed, deadline)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHandoffCommandNormalizationAndRejectPlan(t *testing.T) {
	t.Parallel()

	scope := DirectoryScopeRef{TenantID: model.NewTenantID(), WorkspaceID: model.NewID()}
	principal := CommunicationPrincipal{UserID: model.NewID()}
	offer := HandoffOfferCommand{
		ChannelID: model.NewID(), WorkItemID: model.NewID(),
		MessageID: model.NewID(), DeliveryID: model.NewID(),
		Content: HandoffContent{Summary: "Summary", NextAction: "Next"},
		IfMatch: "\"v3\"", IdempotencyKey: model.NewID().String(),
	}
	normalized, err := normalizeHandoffOfferCommand(scope, principal, offer)
	if err != nil || normalized.expectedVersion != 3 || len(normalized.requestDigest) != sha256.Size {
		t.Fatalf("normalize offer = %+v, err %v", normalized, err)
	}
	if _, err := normalizeHandoffOfferCommand(
		scope, principal, HandoffOfferCommand{IfMatch: "v3", IdempotencyKey: model.NewID().String()},
	); err == nil {
		t.Fatal("non-canonical Handoff If-Match was accepted")
	}

	before := Handoff{
		MutableCommunicationEntity: communicationStateTestMutable(
			scope, communicationTestNow,
		),
		WorkItemID: model.NewID(), MessageID: model.NewID(), DeliveryID: model.NewID(),
		From:              RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		FromOwnerEpoch:    2,
		To:                RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		OfferedLeaseFence: 4, ContextEventSeq: 3,
		Payload: communicationTestPayloadForSlot(t, PayloadSlotHandoff),
		State:   HandoffOffered, AckDeadline: communicationTestNow.Add(time.Hour),
	}
	before.ContextHash, err = CanonicalHandoffContextHash(before)
	if err != nil {
		t.Fatalf("hash Handoff reject fixture: %v", err)
	}
	reason := communicationTestPayloadForSlot(t, PayloadSlotHandoffTerminalReason)
	plan, err := PlanHandoffTransition(
		before, HandoffReject, "", 0, "handoff_rejected", &reason,
		before.UpdatedAt.Add(time.Second),
	)
	if err != nil {
		t.Fatalf("plan Handoff reject: %v", err)
	}
	if plan.ChangesLease || plan.CreatesAck || plan.After.ResultingLeaseFence != 0 ||
		plan.After.AckID != "" || plan.After.State != HandoffRejected {
		t.Fatalf("reject plan changed Ack/lease: %+v", plan)
	}
}

// TestHandoffAcceptRefusesTheSenderAndLeavesOwnershipUntouched pins the PROPERTY that
// only the exact target may take ownership through a Handoff accept, and pins it as a
// property rather than as one layer's error, because two independent layers enforce it:
// the authority re-validation at communication_handoff_apply.go:1120
// (handoff.To != reader.Recipient) and the accept guard at :1496
// (before.To != RecipientRef{RecipientUser, actor.Ref}).
//
// WHY IT IS WRITTEN NOW. The census of the spec's 7.6 acceptance matrix found this
// implemented and UNTESTED: the five existing Handoff tests here drive offer, reject,
// owner-cancel, the deadline reaper and normalization — every one a path the guard lets
// through — and nothing anywhere asserted a refusal. Deleting the recipient sub-clause
// left the whole suite green. That matters more in 7.6 than anywhere else: it is the
// transfer of OWNERSHIP, so a surviving mutant lets a sender hand work to itself.
//
// WHY IT DOES NOT ASSERT A NAMED ERROR, and this is a finding in itself. The layer that
// actually fires first answers with ErrCommunicationEvidenceUnknown and the message
// "Handoff response authority expired while waiting for locks" — which is TRUE of the
// compound condition it belongs to and MISLEADING about this case: nothing expired, the
// caller simply is not the recipient. Pinning that string would freeze a message that
// names the wrong cause, and would also break the day the deeper guard at :1496 becomes
// the one that answers. The property is what must hold; which layer enforces it is an
// implementation detail that defense in depth is allowed to change.
//
// BOTH DIRECTIONS. The firing half is the sender being refused AND the work item's owner
// and epoch being unchanged afterwards — a guard that errored while still moving
// ownership would be worse than none, and only reading the stored record separates
// those. The non-firing half is the exact target accepting the same offer immediately
// afterwards and ownership actually moving: without it, an accept path that refused
// EVERYONE would satisfy the first half perfectly.
//
// ⛔ ITS MUTATION VERIFICATION IS INCONCLUSIVE, AND THAT IS STATED HERE RATHER THAN
// LEFT FOR A READER TO ASSUME. Three independent layers refuse a non-recipient, each
// found by probing the error the sender actually receives:
//
//	communication_handoff_apply.go:1120  handoff.To != reader.Recipient
//	communication_handoff_apply.go:1496  before.To != RecipientRef{RecipientUser, actor}
//	communication_state.go:5236          delivery.Recipient != recipient
//
// Removing the first leaves the test green (the second holds). Removing the first two
// leaves it green (the third holds) — which is what defense in depth is supposed to do.
// Removing all three did NOT produce a red either, and a probe explains why the
// experiment stops being informative there rather than proving the test is blind: with
// the third gone the OFFER itself starts failing, so the scenario never reaches the
// accept and the mutant no longer isolates one condition, which is exactly what the
// canon forbids a mutant from doing.
//
// So this test is honest about what it is: a regression test that asserts a real
// property and passes, NOT a mutation-verified guard. Whoever closes 7.6 properly
// should reach the accept path with a fixture whose offer survives, and only then
// claim the mutant died. Calling it verified today would be the overclaim this census
// spent the afternoon finding in other people's work.
func TestHandoffAcceptRefusesTheSenderAndLeavesOwnershipUntouched(t *testing.T) {
	t.Parallel()

	fixture := newHandoffServiceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	offer, err := fixture.m.offerHandoffWithAuthority(
		ctx, fixture.scope, fixture.ref, HandoffOfferCommand{
			ChannelID: fixture.channel.ID, WorkItemID: fixture.workID,
			MessageID: fixture.message.ID, DeliveryID: fixture.delivery.ID,
			Content: HandoffContent{
				Summary: "Transfer ownership", NextAction: "Continue implementation",
			},
			IfMatch: "\"v1\"", IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("offer Handoff: %v", err)
	}

	ownerBefore := handoffStoredRecord(t, fixture, workItemKind, fixture.workID)

	// FIRING: the sender is not the target, so its accept must not succeed.
	if _, err := fixture.m.respondHandoffWithAuthority(
		ctx, fixture.scope, fixture.ref, offer.HandoffID,
		HandoffResponseCommand{
			Transition: HandoffAccept, IfMatch: offer.ETag,
			IdempotencyKey: model.NewID().String(),
		},
	); err == nil {
		t.Fatal("the sender accepted its own Handoff: ownership transfer is not target-only")
	}

	// A refusal that still moved ownership would be worse than no guard at all.
	ownerAfterRefusal := handoffStoredRecord(t, fixture, workItemKind, fixture.workID)
	if ownerAfterRefusal.String(colWorkOwnerKind) != ownerBefore.String(colWorkOwnerKind) ||
		ownerAfterRefusal.String(colWorkOwnerRef) != ownerBefore.String(colWorkOwnerRef) ||
		ownerAfterRefusal.Int(colWorkOwnerEpoch) != ownerBefore.Int(colWorkOwnerEpoch) {
		t.Fatalf(
			"refused accept still moved ownership: before=%v after=%v",
			ownerBefore, ownerAfterRefusal,
		)
	}

	// NON-FIRING: the exact target accepts the same offer and ownership DOES move.
	// Without this, an accept path that refused everyone would pass the half above.
	response, err := fixture.m.respondHandoffWithAuthority(
		ctx, fixture.scope, fixture.targetRef, offer.HandoffID,
		HandoffResponseCommand{
			Transition: HandoffAccept, IfMatch: offer.ETag,
			IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("exact target accepting the same offer must still work: %v", err)
	}
	ownerAfterAccept := handoffStoredRecord(t, fixture, workItemKind, fixture.workID)
	if response.State != HandoffAccepted ||
		ownerAfterAccept.String(colWorkOwnerRef) != fixture.delivery.Recipient.Ref ||
		ownerAfterAccept.Int(colWorkOwnerEpoch) != ownerBefore.Int(colWorkOwnerEpoch)+1 {
		t.Fatalf(
			"target accept did not transfer ownership: response=%+v owner=%v",
			response, ownerAfterAccept,
		)
	}
}
