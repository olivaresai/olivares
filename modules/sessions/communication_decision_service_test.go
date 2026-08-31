// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type decisionResponseFixture struct {
	directNoticeFixture
	workItem WorkItem
	message  Message
	delivery MessageDelivery
	request  DecisionRequest
}

func newDecisionResponseFixture(t *testing.T) decisionResponseFixture {
	return newDecisionResponseFixtureWithDueDelay(t, 4*time.Minute)
}

func newDecisionResponseFixtureWithDueDelay(
	t *testing.T,
	dueDelay time.Duration,
) decisionResponseFixture {
	t.Helper()
	if dueDelay <= 0 {
		t.Fatal("DecisionRequest due delay must be positive")
	}
	fixture := newDirectNoticeExactAuthorityFixture(t)
	fixture.m.communicationDirectoryResolver = &directNoticeReadDirectoryResolver{
		now: fixture.now, epoch: fixture.epoch,
	}
	fixture.m.communicationGrantClosure = &directNoticeReadClosureResolver{
		now: fixture.now, epoch: fixture.epoch,
	}
	ctx := context.Background()
	workItemID := model.NewID()
	workRecord := workSchemaItem(fixture.workspace, "K3 decision response")
	workRecord[colWorkOwnerKind] = "user"
	workRecord[colWorkOwnerRef] = fixture.sender.String()
	// The test seeds the already-created K1 aggregate at its event-sequence
	// boundary. Decision response then has to advance it and create seq=2.
	workRecord[colWorkLastEventSeq] = int64(1)
	createdWork, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, workItemKind, workItemID, workRecord,
	)
	if err != nil {
		t.Fatalf("create DecisionRequest WorkItem: %v", err)
	}

	messageID := model.NewID()
	audienceID := model.NewID()
	deliveryID := model.NewID()
	contributionID := model.NewID()
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
	var durableNow model.Timestamp
	if err := fixture.m.viewCommunication(ctx, fixture.scope, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return errors.New("DecisionRequest fixture lacks durable transaction clock")
		}
		var clockErr error
		durableNow, clockErr = clock.TransactionNow(ctx)
		return clockErr
	}); err != nil {
		t.Fatalf("read durable DecisionRequest deadline clock: %v", err)
	}
	dueAt := durableNow.Time().Add(dueDelay)
	expiresAt := dueAt.Add(time.Minute)
	message := Message{
		MutableCommunicationEntity: baseMutable(messageID),
		ChannelID:                  fixture.channel.ID, WorkItemID: workItemID, ThreadID: messageID,
		Kind: MessageDecisionRequest, State: MessageDraft,
		Sender:  CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
		Payload: communicationTestPayloadForSlot(t, PayloadSlotMessage),
		Urgency: UrgencyNormal, AckPolicy: AckPolicyNone,
		AvailableAt: fixture.now, ExpiresAt: &expiresAt,
	}
	selector := AudienceSelector{
		Kind: AudienceUser, Ref: fixture.sender.String(), Required: false, WakePolicy: WakeNone,
	}
	selectorRaw, err := canonicalJSON(selector)
	if err != nil {
		t.Fatalf("canonical DecisionRequest selector: %v", err)
	}
	selectorHash := sha256.Sum256(selectorRaw)
	audience := MessageAudience{
		AppendOnlyCommunicationEntity: baseAppendOnly(audienceID), MessageID: messageID,
		Ordinal: 1, Selector: selector, ChannelACLRevision: fixture.channel.ACLRevision,
		RouteRevision:        fixture.channel.RouteRevision,
		SubscriptionRevision: fixture.channel.SubscriptionRevision,
		DirectoryEpoch:       fixture.epoch, DirectorySnapshotAt: fixture.now,
		ResolvedCount: 1, SelectorHash: selectorHash[:], ResolvedHash: make([]byte, sha256.Size),
	}
	delivery := MessageDelivery{
		MutableCommunicationEntity: baseMutable(deliveryID), MessageID: messageID,
		Recipient:      RecipientRef{Kind: RecipientUser, Ref: fixture.sender.String()},
		RecipientEpoch: 1, DeliverySeq: 1, Required: false,
		RouteReasons: []RouteReason{"direct"}, WakePolicy: WakeNone,
		State: DeliveryAvailable, AvailableAt: fixture.now, ExpiresAt: &expiresAt,
	}
	contribution := communicationStateTestSealCausalArc(MessageAudienceRecipient{
		AppendOnlyCommunicationEntity: baseAppendOnly(contributionID),
		MessageAudienceID:             audienceID, MessageDeliveryID: deliveryID,
		Recipient: delivery.Recipient, RecipientEpoch: delivery.RecipientEpoch,
		Required: false, WakePolicy: WakeNone, RouteReasons: []RouteReason{"direct"},
		Selector: selector, DirectoryEpoch: fixture.epoch,
		ChannelACLRevision:   fixture.channel.ACLRevision,
		RouteRevision:        fixture.channel.RouteRevision,
		SubscriptionRevision: fixture.channel.SubscriptionRevision,
		CausalKind:           CausalDirect, CausalRef: fixture.sender.String(),
	})
	audience.ResolvedHash, err = canonicalResolvedAudienceHash(
		audience, []MessageAudienceRecipient{contribution},
	)
	if err != nil {
		t.Fatalf("DecisionRequest resolved audience hash: %v", err)
	}
	message.AudienceHash, err = CanonicalMessageAudienceHash(
		message, []MessageAudience{audience}, []MessageAudienceRecipient{contribution},
	)
	if err != nil {
		t.Fatalf("DecisionRequest audience hash: %v", err)
	}
	audienceHash := append([]byte(nil), message.AudienceHash...)
	message.AudienceHash = nil

	messageRecord, err := messageToRecord(message, 0)
	if err != nil {
		t.Fatalf("encode draft DecisionRequest Message: %v", err)
	}
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, messageKind, message.ID, messageRecord,
	); err != nil {
		t.Fatalf("create draft DecisionRequest Message: %v", err)
	}
	audienceRecord, err := messageAudienceToRecord(audience)
	if err != nil {
		t.Fatalf("encode DecisionRequest Audience: %v", err)
	}
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, messageAudienceKind, audience.ID, audienceRecord,
	); err != nil {
		t.Fatalf("create DecisionRequest Audience: %v", err)
	}
	deliveryRecord, err := messageDeliveryToRecord(delivery)
	if err != nil {
		t.Fatalf("encode DecisionRequest Delivery: %v", err)
	}
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, messageDeliveryKind, delivery.ID, deliveryRecord,
	); err != nil {
		t.Fatalf("create DecisionRequest Delivery: %v", err)
	}
	contributionRecord, err := messageAudienceRecipientToRecord(contribution)
	if err != nil {
		t.Fatalf("encode DecisionRequest audience contribution: %v", err)
	}
	if _, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, messageAudienceRecipientKind,
		contribution.ID, contributionRecord,
	); err != nil {
		t.Fatalf("create DecisionRequest audience contribution: %v", err)
	}
	publishedAt := fixture.now
	message.State = MessagePublished
	message.PublishedAt = &publishedAt
	message.AudienceHash = audienceHash
	publishedRecord, err := messageToRecord(message, 0)
	if err != nil {
		t.Fatalf("encode published DecisionRequest Message: %v", err)
	}
	updatedMessage, err := communicationUpdate(
		ctx, fixture.m, fixture.tenant, messageKind, publishedRecord,
	)
	if err != nil {
		t.Fatalf("publish DecisionRequest Message: %v", err)
	}
	message, err = messageFromRecord(updatedMessage, 0)
	if err != nil {
		t.Fatalf("decode published DecisionRequest Message: %v", err)
	}

	requestID := model.NewID()
	request := DecisionRequest{
		MutableCommunicationEntity: baseMutable(requestID), MessageID: messageID,
		WorkItemID: workItemID, DecisionKey: "release_approval",
		Requester:            message.Sender,
		Owner:                CommunicationSubjectRef{Kind: SubjectUser, Ref: fixture.sender.String()},
		State:                DecisionPending,
		Request:              communicationTestPayloadForSlot(t, PayloadSlotDecisionRequest),
		AuthorityRequirement: "security_review", DueAt: dueAt,
	}
	requestRecord, err := decisionRequestToRecord(request)
	if err != nil {
		t.Fatalf("encode DecisionRequest: %v", err)
	}
	createdRequest, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, decisionRequestKind, request.ID, requestRecord,
	)
	if err != nil {
		t.Fatalf("create DecisionRequest: %v", err)
	}
	request, err = decisionRequestFromRecord(createdRequest)
	if err != nil {
		t.Fatalf("decode DecisionRequest: %v", err)
	}
	return decisionResponseFixture{
		directNoticeFixture: fixture,
		workItem: WorkItem{ID: workItemID, WorkspaceID: fixture.workspace,
			Version: createdWork.Int(model.ColVersion), LastEventSeq: createdWork.Int(colWorkLastEventSeq)},
		message: message, delivery: delivery, request: request,
	}
}

func decisionResponseTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func decisionResolveCommand(version int64, idempotency model.ID, text string) DecisionRequestResponseCommand {
	return DecisionRequestResponseCommand{
		Transition: DecisionResolve,
		Response: DecisionResponseContent{
			ChoiceKey: "yes",
			Reason:    CommunicationReasonContent{Code: "resolved", Text: text},
		},
		IfMatch: fmtDecisionETag(version), IdempotencyKey: idempotency.String(),
	}
}

func fmtDecisionETag(version int64) string {
	return "\"v" + strconv.FormatInt(version, 10) + "\""
}

func decisionRequestForTest(t *testing.T, fixture decisionResponseFixture) DecisionRequest {
	t.Helper()
	rows := communicationRowsForTest(t, fixture.directNoticeFixture, decisionRequestKind)
	if len(rows) != 1 {
		t.Fatalf("DecisionRequest rows = %d, want 1", len(rows))
	}
	request, err := decisionRequestFromRecord(rows[0])
	if err != nil {
		t.Fatalf("decode stored DecisionRequest: %v", err)
	}
	return request
}

func workItemRecordForDecisionTest(t *testing.T, fixture decisionResponseFixture) model.Record {
	t.Helper()
	rows := communicationRowsForTest(t, fixture.directNoticeFixture, workItemKind)
	for _, row := range rows {
		if recordID(row) == fixture.workItem.ID {
			return row
		}
	}
	t.Fatal("DecisionRequest WorkItem is absent")
	return nil
}

func TestDecisionRequestResolveIsAtomicAndIdempotent(t *testing.T) {
	t.Parallel()

	fixture := newDecisionResponseFixture(t)
	ctx := decisionResponseTestContext(t)
	command := decisionResolveCommand(fixture.request.Version, model.NewID(), "authorized release")

	if _, err := fixture.m.RespondDecisionRequest(
		ctx, fixture.scope, fixture.ref, fixture.request.ID, command,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("public DecisionResponse while K3 OFF = %v, want UNKNOWN", err)
	}
	if got := decisionRequestForTest(t, fixture); got.State != DecisionPending {
		t.Fatalf("public OFF request state = %s, want pending", got.State)
	}

	result, err := fixture.m.respondDecisionRequestWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.request.ID, command,
	)
	if err != nil {
		t.Fatalf("resolve DecisionRequest: %v", err)
	}
	if result.State != DecisionResolved || result.Version != 2 || result.ETag != "\"v2\"" ||
		result.WorkDecisionID == "" || result.ResponseID == "" || result.EventID == "" ||
		result.Replayed {
		t.Fatalf("resolve result = %+v", result)
	}
	stored := decisionRequestForTest(t, fixture)
	if stored.State != DecisionResolved || stored.ResolvedDecisionID != result.WorkDecisionID ||
		stored.AcceptedDeliveryID != "" || stored.LastResponseSeq != 1 {
		t.Fatalf("resolved DecisionRequest = %+v", stored)
	}
	if got := communicationRowsForTest(t, fixture.directNoticeFixture, decisionResponseKind); len(got) != 1 {
		t.Fatalf("DecisionResponse rows = %d, want 1", len(got))
	}
	decisions := communicationRowsForTest(t, fixture.directNoticeFixture, workDecisionKind)
	heads := communicationRowsForTest(t, fixture.directNoticeFixture, workDecisionHeadKind)
	if len(decisions) != 1 || len(heads) != 1 || recordID(decisions[0]) != result.WorkDecisionID ||
		heads[0].String(colDecisionCurrentID) != result.WorkDecisionID.String() ||
		heads[0].String(colDecisionHeadState) != "effective" ||
		decisions[0].String(colDecisionStatement) != "yes" ||
		!bytes.Contains([]byte(decisions[0].String(colDecisionRationale)), []byte(result.ResponseID.String())) {
		t.Fatalf("WorkDecision/DecisionHead = %#v / %#v", decisions, heads)
	}
	for _, kind := range []model.Kind{
		workEventKind, workOutboxKind, communicationCommandKind,
	} {
		if rows := communicationRowsForTest(t, fixture.directNoticeFixture, kind); len(rows) != 1 {
			t.Fatalf("%s rows = %d, want 1", kind, len(rows))
		}
	}
	item := workItemRecordForDecisionTest(t, fixture)
	if item.Int(colWorkLastEventSeq) != 2 || item.Int(model.ColVersion) != fixture.workItem.Version+1 {
		t.Fatalf("WorkItem event/version = %d/%d", item.Int(colWorkLastEventSeq), item.Int(model.ColVersion))
	}

	replayed, err := fixture.m.respondDecisionRequestWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.request.ID, command,
	)
	if err != nil || !replayed.Replayed {
		t.Fatalf("replay DecisionResponse = (%+v, %v)", replayed, err)
	}
	replayed.Replayed = false
	if replayed != result {
		t.Fatalf("replay = %+v, want %+v", replayed, result)
	}
	if len(communicationRowsForTest(t, fixture.directNoticeFixture, decisionResponseKind)) != 1 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, workDecisionKind)) != 1 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, communicationCommandKind)) != 1 {
		t.Fatal("DecisionResponse replay duplicated durable effects")
	}

	reused := command
	reused.Response.Reason.Text = "different semantic request"
	if _, err := fixture.m.respondDecisionRequestWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.request.ID, reused,
	); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("reused DecisionResponse idempotency key = %v, want conflict", err)
	}
}

func TestDecisionRequestAcceptIsOnlyCustodyAndInvalidTransitionsRollback(t *testing.T) {
	t.Parallel()

	fixture := newDecisionResponseFixture(t)
	ctx := decisionResponseTestContext(t)
	command := DecisionRequestResponseCommand{
		Transition: DecisionAccept,
		Response: DecisionResponseContent{Reason: CommunicationReasonContent{
			Code: "accepted", Text: "taking custody",
		}},
		IfMatch: "\"v1\"", IdempotencyKey: model.NewID().String(),
	}
	result, err := fixture.m.respondDecisionRequestWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.request.ID, command,
	)
	if err != nil {
		t.Fatalf("accept DecisionRequest custody: %v", err)
	}
	request := decisionRequestForTest(t, fixture)
	if result.State != DecisionAccepted || request.AcceptedDeliveryID != fixture.delivery.ID ||
		request.ResolvedDecisionID != "" || result.WorkDecisionID != "" ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, workDecisionKind)) != 0 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, workDecisionHeadKind)) != 0 {
		t.Fatalf("accept fabricated a decision: result=%+v request=%+v", result, request)
	}

	beforeResponses := len(communicationRowsForTest(t, fixture.directNoticeFixture, decisionResponseKind))
	beforeEvents := len(communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind))
	invalid := command
	invalid.IfMatch = "\"v2\""
	invalid.IdempotencyKey = model.NewID().String()
	if _, err := fixture.m.respondDecisionRequestWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.request.ID, invalid,
	); !errors.Is(err, ErrInvalidCommunicationTransition) {
		t.Fatalf("accepted -> accepted = %v, want invalid transition", err)
	}
	if got := decisionRequestForTest(t, fixture); got.Version != request.Version || got.State != request.State {
		t.Fatalf("invalid transition changed DecisionRequest: before=%+v after=%+v", request, got)
	}
	if len(communicationRowsForTest(t, fixture.directNoticeFixture, decisionResponseKind)) != beforeResponses ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind)) != beforeEvents {
		t.Fatal("invalid DecisionRequest transition persisted effects")
	}
}

func TestDecisionRequestETagConflictHasNoEffects(t *testing.T) {
	t.Parallel()

	fixture := newDecisionResponseFixture(t)
	ctx := decisionResponseTestContext(t)
	command := decisionResolveCommand(2, model.NewID(), "stale version")
	beforeAudit := directNoticeAuditHead(t, fixture.directNoticeFixture)
	if _, err := fixture.m.respondDecisionRequestWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.request.ID, command,
	); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("DecisionResponse stale ETag = %v, want conflict", err)
	}
	afterAudit := directNoticeAuditHead(t, fixture.directNoticeFixture)
	if got := decisionRequestForTest(t, fixture); got.State != DecisionPending || got.Version != 1 {
		t.Fatalf("stale ETag changed DecisionRequest: %+v", got)
	}
	for _, kind := range []model.Kind{
		decisionResponseKind, workDecisionKind, workDecisionHeadKind,
		workEventKind, workOutboxKind, communicationCommandKind,
	} {
		if rows := communicationRowsForTest(t, fixture.directNoticeFixture, kind); len(rows) != 0 {
			t.Fatalf("stale ETag created %d %s rows", len(rows), kind)
		}
	}
	if afterAudit.Seq != beforeAudit.Seq || !bytes.Equal(afterAudit.Hash, beforeAudit.Hash) {
		t.Fatal("stale ETag appended audit evidence")
	}
}

func TestDecisionRequestReceiptFailureRollsBackDecisionHeadAndResponse(t *testing.T) {
	t.Parallel()

	fixture := newDecisionResponseFixture(t)
	ctx := decisionResponseTestContext(t)
	beforeRequest := decisionRequestForTest(t, fixture)
	beforeItem := workItemRecordForDecisionTest(t, fixture)
	beforeAudit := directNoticeAuditHead(t, fixture.directNoticeFixture)
	failure := errors.New("injected DecisionResponse receipt failure")
	fixture.m.data = &directNoticeExactAckWriteFailureData{
		inner: fixture.m.data, kind: communicationCommandKind,
		operation: "create_with_id", failure: failure,
	}
	if _, err := fixture.m.respondDecisionRequestWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.request.ID,
		decisionResolveCommand(1, model.NewID(), "must roll back"),
	); !errors.Is(err, failure) {
		t.Fatalf("DecisionResponse receipt failure = %v, want injected error", err)
	}
	afterRequest := decisionRequestForTest(t, fixture)
	afterItem := workItemRecordForDecisionTest(t, fixture)
	afterAudit := directNoticeAuditHead(t, fixture.directNoticeFixture)
	if !reflect.DeepEqual(beforeRequest, afterRequest) ||
		beforeItem.Int(model.ColVersion) != afterItem.Int(model.ColVersion) ||
		beforeItem.Int(colWorkLastEventSeq) != afterItem.Int(colWorkLastEventSeq) {
		t.Fatalf("rollback changed request/item: before=%+v/%v after=%+v/%v",
			beforeRequest, beforeItem, afterRequest, afterItem)
	}
	for _, kind := range []model.Kind{
		decisionResponseKind, workDecisionKind, workDecisionHeadKind,
		workEventKind, workOutboxKind, communicationCommandKind,
	} {
		if rows := communicationRowsForTest(t, fixture.directNoticeFixture, kind); len(rows) != 0 {
			t.Fatalf("receipt rollback retained %d %s rows", len(rows), kind)
		}
	}
	if afterAudit.Seq != beforeAudit.Seq || !bytes.Equal(afterAudit.Hash, beforeAudit.Hash) {
		t.Fatal("receipt rollback retained audit append")
	}
}

type decisionDeadlineAuthorizerStub struct {
	authority decisionDeadlineAuthority
	scope     DirectoryScopeRef
	requestID model.ID
	calls     int
}

func (a *decisionDeadlineAuthorizerStub) AuthorizeDecisionDeadline(
	_ context.Context,
	scope DirectoryScopeRef,
	requestID model.ID,
) (decisionDeadlineAuthority, error) {
	a.calls++
	a.scope, a.requestID = scope, requestID
	result := a.authority
	result.Facts = append([]store.AuthorizationFactRef(nil), a.authority.Facts...)
	result.Reader.Core = cloneCommunicationRequestAuthorityWitness(a.authority.Reader.Core)
	result.Reader.Resolution = cloneDirectNoticePrincipalResolution(a.authority.Reader.Resolution)
	result.Reader.Closure = cloneDirectNoticeChannelGrantSubjectClosure(a.authority.Reader.Closure)
	result.Reader.Facts = append([]store.AuthorizationFactRef(nil), a.authority.Reader.Facts...)
	return result, nil
}

func decisionDeadlineAuthorityForFixture(
	t *testing.T,
	ctx context.Context,
	fixture decisionResponseFixture,
) decisionDeadlineAuthority {
	t.Helper()
	question, bound, _, identity, _, _, err := fixture.m.prepareDecisionResponseAuthority(
		ctx, fixture.scope, fixture.ref, fixture.request.ID,
		DecisionRequestResponseCommand{
			Transition: DecisionAccept,
			Response: DecisionResponseContent{Reason: CommunicationReasonContent{
				Code: "accepted", Text: "deadline authority preflight",
			}},
			IfMatch: "\"v1\"", IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("prepare DecisionRequest deadline C5 authority: %v", err)
	}
	_, consumed, err := bound.transactionSnapshot(
		question, CommunicationClaimAuthoritySnapshot{},
	)
	if err != nil {
		t.Fatalf("consume DecisionRequest deadline C5 authority: %v", err)
	}
	reader, err := directNoticeReaderPreflightWithCore(identity, consumed.witness)
	if err != nil || reader.Core.Entity != question.entity {
		t.Fatalf("bind DecisionRequest deadline C5 authority: %+v, %v", reader, err)
	}
	return decisionDeadlineAuthority{
		Actor:  CommunicationActorRef{Kind: ActorSystem, Ref: "decision-deadline-reaper"},
		Reader: reader, Facts: append([]store.AuthorizationFactRef(nil), reader.Facts...),
		ObservedAt: fixture.now, FreshUntil: fixture.now.Add(10 * time.Minute),
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "decision_deadline_authorized",
			EvidenceRef: "decision-deadline:reaper",
		},
	}
}

func waitDecisionRequestDurableDeadline(
	t *testing.T,
	fixture decisionResponseFixture,
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
					return errors.New("DecisionRequest deadline wait lacks transaction clock")
				}
				var clockErr error
				observed, clockErr = clock.TransactionNow(context.Background())
				return clockErr
			},
		)
		if err != nil {
			t.Fatalf("observe DecisionRequest durable deadline: %v", err)
		}
		if !observed.Time().Before(deadline) {
			return
		}
		if time.Now().After(waitUntil) {
			t.Fatalf("durable time %s did not reach DecisionRequest deadline %s", observed, deadline)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDecisionRequestDeadlineExpiresAtDBDeadlineAndReplays(t *testing.T) {
	t.Parallel()

	fixture := newDecisionResponseFixtureWithDueDelay(t, 3*time.Second)
	ctx := decisionResponseTestContext(t)
	authorizer := &decisionDeadlineAuthorizerStub{
		authority: decisionDeadlineAuthorityForFixture(t, ctx, fixture),
	}
	service, err := newDecisionDeadlineService(fixture.m, authorizer, nil)
	if err != nil {
		t.Fatalf("new DecisionRequest deadline service: %v", err)
	}
	command := decisionDeadlineCommand{
		RequestID: fixture.request.ID, ExpectedVersion: fixture.request.Version,
		IdempotencyKey: model.NewID().String(),
	}
	beforeItem := workItemRecordForDecisionTest(t, fixture)
	beforeAudit := directNoticeAuditHead(t, fixture.directNoticeFixture)
	if _, err := service.Expire(ctx, fixture.scope, command); !errors.Is(
		err, ErrInvalidCommunicationTransition,
	) {
		t.Fatalf("DecisionRequest expiry before due_at = %v, want invalid transition", err)
	}
	if got := decisionRequestForTest(t, fixture); got.State != DecisionPending || got.Version != 1 {
		t.Fatalf("early DecisionRequest expiry changed request: %+v", got)
	}
	for _, kind := range []model.Kind{
		decisionResponseKind, workDecisionKind, workDecisionHeadKind,
		workEventKind, workOutboxKind, communicationCommandKind,
	} {
		if rows := communicationRowsForTest(t, fixture.directNoticeFixture, kind); len(rows) != 0 {
			t.Fatalf("early DecisionRequest expiry created %d %s rows", len(rows), kind)
		}
	}
	afterEarlyItem := workItemRecordForDecisionTest(t, fixture)
	afterEarlyAudit := directNoticeAuditHead(t, fixture.directNoticeFixture)
	if afterEarlyItem.Int(model.ColVersion) != beforeItem.Int(model.ColVersion) ||
		afterEarlyItem.Int(colWorkLastEventSeq) != beforeItem.Int(colWorkLastEventSeq) ||
		afterEarlyAudit.Seq != beforeAudit.Seq ||
		!bytes.Equal(afterEarlyAudit.Hash, beforeAudit.Hash) {
		t.Fatal("early DecisionRequest expiry retained transaction effects")
	}

	waitDecisionRequestDurableDeadline(t, fixture, fixture.request.DueAt)
	result, err := service.Expire(ctx, fixture.scope, command)
	if err != nil {
		t.Fatalf("expire DecisionRequest at due_at: %v", err)
	}
	if authorizer.calls != 2 || authorizer.scope != fixture.scope ||
		authorizer.requestID != fixture.request.ID || result.State != DecisionExpired ||
		result.Version != 2 || result.ETag != "\"v2\"" || result.ResponseID == "" ||
		result.EventID == "" || result.WorkDecisionID != "" || result.Replayed {
		t.Fatalf("DecisionRequest deadline result/authority = %+v / %+v", result, authorizer)
	}
	stored := decisionRequestForTest(t, fixture)
	if stored.State != DecisionExpired || stored.TerminalCode != decisionDeadlineElapsedCode ||
		stored.ResolvedDecisionID != "" || stored.AcceptedDeliveryID != "" ||
		stored.UpdatedAt.Before(stored.DueAt) || stored.LastResponseSeq != 1 {
		t.Fatalf("expired DecisionRequest = %+v", stored)
	}
	responses := communicationRowsForTest(t, fixture.directNoticeFixture, decisionResponseKind)
	if len(responses) != 1 || recordID(responses[0]) != result.ResponseID ||
		responses[0].String(colCommFromState) != string(DecisionPending) ||
		responses[0].String(colCommToState) != string(DecisionExpired) ||
		responses[0].String(colCommActorKind) != string(ActorSystem) ||
		responses[0].String(colCommActorRef) != authorizer.authority.Actor.Ref ||
		responses[0].String(colCommWorkDecisionID) != "" {
		t.Fatalf("deadline DecisionResponse = %v", responses)
	}
	if len(communicationRowsForTest(t, fixture.directNoticeFixture, workDecisionKind)) != 0 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, workDecisionHeadKind)) != 0 {
		t.Fatal("DecisionRequest expiry fabricated an effective WorkDecision")
	}
	item := workItemRecordForDecisionTest(t, fixture)
	if item.Int(model.ColVersion) != beforeItem.Int(model.ColVersion)+1 ||
		item.Int(colWorkLastEventSeq) != beforeItem.Int(colWorkLastEventSeq)+1 {
		t.Fatalf("deadline WorkItem version/event = %d/%d", item.Int(model.ColVersion), item.Int(colWorkLastEventSeq))
	}
	events := communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind)
	if len(events) != 1 || events[0].String(colEventType) != decisionDeadlineEventType ||
		events[0].String(colEventActorKind) != string(model.ActorSystem) ||
		events[0].String(colEventActorRef) != authorizer.authority.Actor.Ref {
		t.Fatalf("DecisionRequest deadline Event = %v", events)
	}
	if len(communicationRowsForTest(t, fixture.directNoticeFixture, workOutboxKind)) != 1 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, communicationCommandKind)) != 1 {
		t.Fatal("DecisionRequest deadline lacks Outbox or receipt")
	}

	replayed, err := service.Expire(ctx, fixture.scope, command)
	if err != nil || !replayed.Replayed || replayed.CommandID != result.CommandID ||
		replayed.ResponseID != result.ResponseID || replayed.EventID != result.EventID {
		t.Fatalf("DecisionRequest deadline replay = %+v, %v", replayed, err)
	}
	if len(communicationRowsForTest(t, fixture.directNoticeFixture, decisionResponseKind)) != 1 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind)) != 1 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, communicationCommandKind)) != 1 {
		t.Fatal("DecisionRequest deadline replay duplicated effects")
	}
}

func TestDecisionRequestDeadlineReceiptFailureRollsBack(t *testing.T) {
	t.Parallel()

	fixture := newDecisionResponseFixtureWithDueDelay(t, 2*time.Second)
	ctx := decisionResponseTestContext(t)
	authorizer := &decisionDeadlineAuthorizerStub{
		authority: decisionDeadlineAuthorityForFixture(t, ctx, fixture),
	}
	service, err := newDecisionDeadlineService(fixture.m, authorizer, nil)
	if err != nil {
		t.Fatalf("new DecisionRequest deadline service: %v", err)
	}
	waitDecisionRequestDurableDeadline(t, fixture, fixture.request.DueAt)
	beforeRequest := decisionRequestForTest(t, fixture)
	beforeItem := workItemRecordForDecisionTest(t, fixture)
	beforeAudit := directNoticeAuditHead(t, fixture.directNoticeFixture)
	failure := errors.New("injected DecisionRequest deadline receipt failure")
	fixture.m.data = &directNoticeExactAckWriteFailureData{
		inner: fixture.m.data, kind: communicationCommandKind,
		operation: "create_with_id", failure: failure,
	}
	if _, err := service.Expire(
		ctx, fixture.scope, decisionDeadlineCommand{
			RequestID: fixture.request.ID, ExpectedVersion: fixture.request.Version,
			IdempotencyKey: model.NewID().String(),
		},
	); !errors.Is(err, failure) {
		t.Fatalf("DecisionRequest deadline receipt failure = %v, want injected error", err)
	}
	afterRequest := decisionRequestForTest(t, fixture)
	afterItem := workItemRecordForDecisionTest(t, fixture)
	afterAudit := directNoticeAuditHead(t, fixture.directNoticeFixture)
	if !reflect.DeepEqual(beforeRequest, afterRequest) ||
		beforeItem.Int(model.ColVersion) != afterItem.Int(model.ColVersion) ||
		beforeItem.Int(colWorkLastEventSeq) != afterItem.Int(colWorkLastEventSeq) ||
		afterAudit.Seq != beforeAudit.Seq || !bytes.Equal(afterAudit.Hash, beforeAudit.Hash) {
		t.Fatal("DecisionRequest deadline write failure retained mutable effects")
	}
	for _, kind := range []model.Kind{
		decisionResponseKind, workDecisionKind, workDecisionHeadKind,
		workEventKind, workOutboxKind, communicationCommandKind,
	} {
		if rows := communicationRowsForTest(t, fixture.directNoticeFixture, kind); len(rows) != 0 {
			t.Fatalf("deadline rollback retained %d %s rows", len(rows), kind)
		}
	}
}
