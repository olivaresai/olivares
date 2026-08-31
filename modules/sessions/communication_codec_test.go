// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

type communicationCodecRoundTripCase struct {
	name   string
	kind   model.Kind
	want   any
	encode func() (model.Record, error)
	decode func(model.Record) (any, error)
}

func communicationCodecCanonicalHash(t *testing.T, value any) (json.RawMessage, []byte) {
	t.Helper()
	raw, err := canonicalJSON(value)
	if err != nil {
		t.Fatalf("canonical fixture JSON: %v", err)
	}
	digest := sha256.Sum256(raw)
	return raw, digest[:]
}

func communicationCodecAssertColumns(
	t *testing.T,
	descriptor model.EntityDescriptor,
	record model.Record,
) {
	t.Helper()
	want := map[string]bool{
		model.ColID: true, model.ColTenantID: true, model.ColVersion: true,
		model.ColCreatedAt: true, model.ColUpdatedAt: true,
	}
	for _, field := range descriptor.Fields {
		want[field.Name] = true
	}
	if len(record) != len(want) {
		t.Fatalf("record columns = %d, descriptor columns = %d\nrecord: %#v\nwant: %#v",
			len(record), len(want), record, want)
	}
	for column := range want {
		if _, ok := record[column]; !ok {
			t.Fatalf("record omits descriptor column %q", column)
		}
	}
	for column := range record {
		if !want[column] {
			t.Fatalf("record adds undeclared column %q", column)
		}
	}
}

func communicationCodecFixtures(t *testing.T) []communicationCodecRoundTripCase {
	t.Helper()
	at := communicationTestNow
	scope := communicationStateTestScope()
	user := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	agent := RecipientRef{Kind: RecipientAgent, Ref: model.NewID().String()}
	actor := CommunicationActorRef{Kind: ActorUser, Ref: user.Ref}

	channel := Channel{
		MutableCommunicationEntity: communicationStateTestMutable(scope, at),
		Slug:                       "kernel", Name: "Kernel coordination", Description: "K3 codec fixture",
		Kind: ChannelCoordination, State: ChannelActive, Sensitivity: ChannelInternal,
		ContentProtection: ContentProtectionStorage, ProtectionGeneration: 1,
		DefaultAckPolicy: AckPolicyNone, DefaultWake: WakeNone,
		RetentionPolicyRef: "retention/default", MaxFanout: 100, MaxAutomationDepth: 2,
		ACLRevision: 1, RouteRevision: 1, SubscriptionRevision: 1,
	}

	grant := ChannelGrant{
		MutableCommunicationEntity: communicationStateTestMutable(scope, at),
		ChannelID:                  channel.ID, Subject: CommunicationSubjectRef{Kind: SubjectUser, Ref: user.Ref},
		Generation: 1, CanRead: true, CanWrite: true, State: ChannelGrantActive, GrantedBy: actor,
	}

	filterJSON, filterHash := communicationCodecCanonicalHash(t, map[string]string{"urgency": "critical"})
	subscription := ChannelSubscription{
		MutableCommunicationEntity: communicationStateTestMutable(scope, at),
		ChannelID:                  channel.ID, Subscriber: CommunicationSubjectRef{Kind: SubjectAgent, Ref: agent.Ref},
		Generation: 1, Mode: SubscriptionAll, Wake: WakePrimary, RequiredForCritical: true,
		State: SubscriptionActive, FilterJSON: filterJSON, FilterHash: filterHash,
	}

	allowedValuesJSON, valuesHash := communicationCodecCanonicalHash(t, []string{"critical", "normal"})
	label := ChannelLabelDefinition{
		MutableCommunicationEntity: communicationStateTestMutable(scope, at),
		ChannelID:                  channel.ID, Key: "severity", Generation: 1,
		AllowedValuesJSON: allowedValuesJSON, ValuesHash: valuesHash,
		Classification: ChannelLabelNonSensitive, State: ChannelLabelActive,
	}

	labelMatchJSON, _ := communicationCodecCanonicalHash(t, map[string]string{"severity": "critical"})
	route := ChannelRouteRule{
		MutableCommunicationEntity: communicationStateTestMutable(scope, at),
		RouteKey:                   "critical-notices", Generation: 1, Priority: 10,
		SourceKind: RouteSourceUserMessage, MessageKind: MessageNotice,
		MinimumUrgency: UrgencyHigh, LabelMatchJSON: labelMatchJSON,
		TargetChannelID: channel.ID, AudienceKind: RouteAudienceSubscribers,
		AckPolicy: AckPolicyNone, WakePolicy: WakePrimary, State: ChannelRouteActive,
	}

	capabilitiesJSON, _ := communicationCodecCanonicalHash(t, map[string]any{
		"formats": []string{"json", "markdown"}, "wake": true,
	})
	heartbeat := at.Add(5 * time.Minute)
	endpoint := CommunicationEndpoint{
		MutableCommunicationEntity: communicationStateTestMutable(scope, at), Owner: agent,
		ProviderKey: "mcp", Transport: "mcp", EndpointRef: "endpoint/k3-agent",
		CapabilitiesJSON: capabilitiesJSON, TransportFingerprint: "mcp/k3-agent/v1",
		SupportLevel: EndpointStable, Priority: 1, State: EndpointActive,
		HeartbeatExpiresAt: &heartbeat, Generation: 1,
	}

	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, at)
	message.ChannelID = channel.ID
	message.Payload = communicationStateTestSealedPayload(PayloadSlotMessage, 1)
	message.LabelsJSON, message.LabelsHash = communicationCodecCanonicalHash(
		t, map[string]string{"severity": "critical"},
	)

	selector := AudienceSelector{Kind: AudienceUser, Ref: user.Ref, WakePolicy: WakeNone}
	_, selectorHash := communicationCodecCanonicalHash(t, selector)
	audience := MessageAudience{
		AppendOnlyCommunicationEntity: communicationStateTestAppendOnly(scope, at),
		MessageID:                     message.ID, Ordinal: 1, Selector: selector,
		ChannelACLRevision: 1, RouteRevision: 1, SubscriptionRevision: 1,
		DirectoryEpoch: 1, DirectorySnapshotAt: at, ResolvedCount: 1,
		SelectorHash: selectorHash, ResolvedHash: bytes.Repeat([]byte{0x21}, sha256.Size),
	}

	delivery := communicationStateTestDelivery(message, user, 1, false)
	contribution := communicationStateTestDirectContribution(scope, delivery)
	contribution.MessageAudienceID = audience.ID
	contribution.Selector = selector
	contribution = communicationStateTestSealCausalArc(contribution)

	cursor := communicationStateTestCursor(t, scope, user, CursorFilter{})
	barrier := InboxCursorBarrier{
		MutableCommunicationEntity: communicationStateTestMutable(scope, at),
		Reader:                     user, MailboxKind: MailboxPersonal, MailboxRef: user.Ref,
		FilterHash: bytes.Clone(cursor.FilterHash), DeliveryID: delivery.ID, BarrierSeq: 1,
		Cause: BarrierTemporarilyInvisible, State: CursorBarrierActive, ReasonCode: "grant_revoked",
	}

	note := communicationTestPayloadForSlot(t, PayloadSlotAckNote)
	ack := MessageAck{
		AppendOnlyCommunicationEntity: communicationStateTestAppendOnly(scope, at),
		DeliveryID:                    delivery.ID, Kind: MessageAckReceived, Actor: actor,
		OnBehalfOf: &user, Note: &note, AcknowledgedAt: at,
	}

	guard := CommunicationGuard{
		MutableCommunicationEntity: communicationStateTestMutable(scope, at),
		Kind:                       CommunicationGuardDeliverySequence, NextSeq: 1, LastDBTime: at,
	}

	request := DecisionRequest{
		MutableCommunicationEntity: communicationStateTestMutable(scope, at),
		MessageID:                  model.NewID(), WorkItemID: model.NewID(), DecisionKey: "deployment-approval",
		Requester: actor, Owner: CommunicationSubjectRef{Kind: SubjectUser, Ref: user.Ref},
		State: DecisionPending, Request: communicationTestPayloadForSlot(t, PayloadSlotDecisionRequest),
		AuthorityRequirement: "work_decide", DueAt: at.Add(time.Hour),
	}
	responseAt := at.Add(time.Minute)
	decisionPlan, err := PlanDecisionRequestTransition(
		request, DecisionAccept, communicationStateTestAppendOnly(scope, responseAt), actor,
		communicationTestPayloadForSlot(t, PayloadSlotDecisionResponse), nil,
		model.NewID(), "", "", "", responseAt,
	)
	if err != nil {
		t.Fatalf("decision response fixture: %v", err)
	}

	handoff := Handoff{
		MutableCommunicationEntity: communicationStateTestMutable(scope, at),
		WorkItemID:                 model.NewID(), MessageID: model.NewID(), DeliveryID: model.NewID(),
		From: user, FromOwnerEpoch: 1, To: agent, ContextEventSeq: 1,
		Payload: communicationTestPayloadForSlot(t, PayloadSlotHandoff),
		State:   HandoffOffered, AckDeadline: at.Add(time.Hour),
	}
	handoff.ContextHash, err = CanonicalHandoffContextHash(handoff)
	if err != nil {
		t.Fatalf("handoff context fixture: %v", err)
	}

	dispatch := communicationStateTestDispatch(t, scope, DispatchRouteIdentity{
		EndpointID: endpoint.ID, EndpointGeneration: endpoint.Generation, PolicyGeneration: 1,
	}, at)
	attempt := DeliveryAttempt{
		MutableCommunicationEntity: communicationStateTestMutable(scope, at), DispatchID: dispatch.ID,
		AttemptSeq: 1, State: AttemptReserved, StartedAt: at,
		TransmitBoundary: TransmitUnknown, RequestHash: bytes.Repeat([]byte{0x41}, sha256.Size),
	}

	resultID := model.NewID()
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: communicationStateTestAppendOnly(scope, at),
		CommandID:                     model.NewID(), ActorFingerprint: bytes.Repeat([]byte{0x51}, sha256.Size),
		CommandScope: "message.publish", IdempotencyKeyHash: bytes.Repeat([]byte{0x52}, sha256.Size),
		RequestDigest: bytes.Repeat([]byte{0x53}, sha256.Size),
		PlanHash:      bytes.Repeat([]byte{0x54}, sha256.Size), ResultKind: "message",
		ResultID: resultID, HTTPStatus: 201,
		ResponseProjectionJSON: CommunicationCommandResponseProjection{
			IDs: map[string]model.ID{"message_id": resultID}, Version: 1,
			State: string(MessagePublished), Counts: map[string]int64{"delivery_count": 1},
			Digests: map[string][]byte{"audience": bytes.Repeat([]byte{0x55}, sha256.Size)},
		},
		EventID: model.NewID(), CompletedAt: at,
	}
	binding, err := CanonicalCommunicationReceiptResponseBinding(receipt)
	if err != nil {
		t.Fatalf("command receipt binding fixture: %v", err)
	}
	responseDigest := sha256.Sum256(binding)
	receipt.ResponseDigest = responseDigest[:]

	return []communicationCodecRoundTripCase{
		{
			name: "channel", kind: channelKind, want: channel,
			encode: func() (model.Record, error) { return channelToRecord(channel) },
			decode: func(rec model.Record) (any, error) { return channelFromRecord(rec) },
		},
		{
			name: "channel_grant", kind: channelGrantKind, want: grant,
			encode: func() (model.Record, error) { return channelGrantToRecord(grant) },
			decode: func(rec model.Record) (any, error) { return channelGrantFromRecord(rec) },
		},
		{
			name: "channel_subscription", kind: channelSubscriptionKind, want: subscription,
			encode: func() (model.Record, error) { return channelSubscriptionToRecord(subscription) },
			decode: func(rec model.Record) (any, error) { return channelSubscriptionFromRecord(rec) },
		},
		{
			name: "channel_label_definition", kind: channelLabelDefinitionKind, want: label,
			encode: func() (model.Record, error) { return channelLabelDefinitionToRecord(label) },
			decode: func(rec model.Record) (any, error) { return channelLabelDefinitionFromRecord(rec) },
		},
		{
			name: "channel_route_rule", kind: channelRouteKind, want: route,
			encode: func() (model.Record, error) { return channelRouteRuleToRecord(route) },
			decode: func(rec model.Record) (any, error) { return channelRouteRuleFromRecord(rec) },
		},
		{
			name: "communication_endpoint", kind: communicationEndpointKind, want: endpoint,
			encode: func() (model.Record, error) { return communicationEndpointToRecord(endpoint) },
			decode: func(rec model.Record) (any, error) { return communicationEndpointFromRecord(rec) },
		},
		{
			name: "message", kind: messageKind, want: message,
			encode: func() (model.Record, error) { return messageToRecord(message, 0) },
			decode: func(rec model.Record) (any, error) { return messageFromRecord(rec, 0) },
		},
		{
			name: "message_audience", kind: messageAudienceKind, want: audience,
			encode: func() (model.Record, error) { return messageAudienceToRecord(audience) },
			decode: func(rec model.Record) (any, error) { return messageAudienceFromRecord(rec) },
		},
		{
			name: "message_audience_recipient", kind: messageAudienceRecipientKind, want: contribution,
			encode: func() (model.Record, error) { return messageAudienceRecipientToRecord(contribution) },
			decode: func(rec model.Record) (any, error) { return messageAudienceRecipientFromRecord(rec) },
		},
		{
			name: "message_delivery", kind: messageDeliveryKind, want: delivery,
			encode: func() (model.Record, error) { return messageDeliveryToRecord(delivery) },
			decode: func(rec model.Record) (any, error) { return messageDeliveryFromRecord(rec) },
		},
		{
			name: "inbox_cursor", kind: inboxCursorKind, want: cursor,
			encode: func() (model.Record, error) { return inboxCursorToRecord(cursor) },
			decode: func(rec model.Record) (any, error) { return inboxCursorFromRecord(rec) },
		},
		{
			name: "inbox_cursor_barrier", kind: inboxCursorBarrierKind, want: barrier,
			encode: func() (model.Record, error) { return inboxCursorBarrierToRecord(barrier, cursor) },
			decode: func(rec model.Record) (any, error) { return inboxCursorBarrierFromRecord(rec, cursor) },
		},
		{
			name: "message_ack", kind: messageAckKind, want: ack,
			encode: func() (model.Record, error) { return messageAckToRecord(ack) },
			decode: func(rec model.Record) (any, error) { return messageAckFromRecord(rec) },
		},
		{
			name: "communication_guard", kind: communicationGuardKind, want: guard,
			encode: func() (model.Record, error) { return communicationGuardToRecord(guard) },
			decode: func(rec model.Record) (any, error) { return communicationGuardFromRecord(rec) },
		},
		{
			name: "decision_request", kind: decisionRequestKind, want: request,
			encode: func() (model.Record, error) { return decisionRequestToRecord(request) },
			decode: func(rec model.Record) (any, error) { return decisionRequestFromRecord(rec) },
		},
		{
			name: "decision_response", kind: decisionResponseKind, want: decisionPlan.Response,
			encode: func() (model.Record, error) {
				return decisionResponseToRecord(decisionPlan.Response, decisionPlan.Before, decisionPlan.After)
			},
			decode: func(rec model.Record) (any, error) {
				return decisionResponseFromRecord(rec, decisionPlan.Before, decisionPlan.After)
			},
		},
		{
			name: "handoff", kind: handoffKind, want: handoff,
			encode: func() (model.Record, error) { return handoffToRecord(handoff) },
			decode: func(rec model.Record) (any, error) { return handoffFromRecord(rec) },
		},
		{
			name: "delivery_dispatch", kind: deliveryDispatchKind, want: dispatch,
			encode: func() (model.Record, error) { return deliveryDispatchToRecord(dispatch) },
			decode: func(rec model.Record) (any, error) { return deliveryDispatchFromRecord(rec) },
		},
		{
			name: "delivery_attempt", kind: deliveryAttemptKind, want: attempt,
			encode: func() (model.Record, error) { return deliveryAttemptToRecord(attempt) },
			decode: func(rec model.Record) (any, error) { return deliveryAttemptFromRecord(rec) },
		},
		{
			name: "communication_command_receipt", kind: communicationCommandKind, want: receipt,
			encode: func() (model.Record, error) { return communicationCommandReceiptToRecord(receipt) },
			decode: func(rec model.Record) (any, error) { return communicationCommandReceiptFromRecord(rec) },
		},
	}
}

func TestCommunicationCodecRoundTripAllTwenty(t *testing.T) {
	t.Parallel()

	registry := communicationCaptureSchema(t)
	fixtures := communicationCodecFixtures(t)
	if len(fixtures) != 20 {
		t.Fatalf("codec fixtures = %d, want 20", len(fixtures))
	}
	seen := make(map[model.Kind]bool, len(fixtures))
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			if seen[fixture.kind] {
				t.Fatalf("duplicate codec kind %s", fixture.kind)
			}
			seen[fixture.kind] = true
			record, err := fixture.encode()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			communicationCodecAssertColumns(t, communicationDescriptor(t, registry, fixture.kind), record)
			got, err := fixture.decode(record)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !reflect.DeepEqual(got, fixture.want) {
				t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, fixture.want)
			}
		})
	}
	for _, entity := range communicationSchemaEntities {
		if !seen[entity.kind] {
			t.Errorf("registered communication kind %s lacks codec round trip", entity.kind)
		}
	}
}

func TestCommunicationCodecPreservesNullCanonicalJSONAndBytes(t *testing.T) {
	t.Parallel()

	fixtures := communicationCodecFixtures(t)
	byKind := make(map[model.Kind]communicationCodecRoundTripCase, len(fixtures))
	for _, fixture := range fixtures {
		byKind[fixture.kind] = fixture
	}

	grantRecord, err := byKind[channelGrantKind].encode()
	if err != nil {
		t.Fatalf("encode grant: %v", err)
	}
	for _, column := range []string{
		colCommRevokedByKind, colCommRevokedByRef, colCommExpiresAt, colCommSupersedesID,
	} {
		if value, ok := grantRecord[column]; !ok || value != nil {
			t.Errorf("optional grant column %q = %#v, want explicit NULL", column, value)
		}
	}
	grantRecord[colCommSupersedesID] = ""
	if _, err := channelGrantFromRecord(grantRecord); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("empty optional ID decoded as NULL: %v", err)
	}

	handoffRecord, err := byKind[handoffKind].encode()
	if err != nil {
		t.Fatalf("encode handoff: %v", err)
	}
	if handoffRecord[colCommOfferedLeaseFence] != nil {
		t.Fatalf("zero optional fence = %#v, want NULL", handoffRecord[colCommOfferedLeaseFence])
	}
	handoffRecord[colCommOfferedLeaseFence] = int64(0)
	if _, err := byKind[handoffKind].decode(handoffRecord); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("stored zero optional fence decoded as NULL: %v", err)
	}

	deliveryRecord, err := byKind[messageDeliveryKind].encode()
	if err != nil {
		t.Fatalf("encode delivery: %v", err)
	}
	if got := deliveryRecord[colCommRouteReasonsJSON]; got != `["direct"]` {
		t.Fatalf("route reasons JSON = %q, want canonical array", got)
	}
	labelRecord, err := byKind[channelLabelDefinitionKind].encode()
	if err != nil {
		t.Fatalf("encode label: %v", err)
	}
	if got := labelRecord[colCommAllowedValuesJSON]; got != `["critical","normal"]` {
		t.Fatalf("allowed values JSON = %q, want canonical sorted array", got)
	}

	encodedHash := bytes.Clone(labelRecord[colCommValuesHash].([]byte))
	labelFixture := byKind[channelLabelDefinitionKind].want.(ChannelLabelDefinition)
	labelFixture.ValuesHash[0] ^= 0xff
	if !bytes.Equal(labelRecord[colCommValuesHash].([]byte), encodedHash) {
		t.Fatal("encoded record aliases domain byte slice")
	}
	decodedAny, err := byKind[channelLabelDefinitionKind].decode(labelRecord)
	if err != nil {
		t.Fatalf("decode label: %v", err)
	}
	decoded := decodedAny.(ChannelLabelDefinition)
	labelRecord[colCommValuesHash].([]byte)[0] ^= 0xff
	if !bytes.Equal(decoded.ValuesHash, encodedHash) {
		t.Fatal("decoded domain aliases record byte slice")
	}

	subscription := byKind[channelSubscriptionKind].want.(ChannelSubscription)
	subscription.FilterJSON = nil
	subscription.FilterHash = []byte{}
	subscriptionRecord, err := channelSubscriptionToRecord(subscription)
	if err != nil {
		t.Fatalf("encode subscription with absent filter: %v", err)
	}
	if subscriptionRecord[colCommFilterJSON] != nil || subscriptionRecord[colCommFilterHash] != nil {
		t.Fatalf("absent subscription filter persisted as %#v/%#v, want NULL/NULL",
			subscriptionRecord[colCommFilterJSON], subscriptionRecord[colCommFilterHash])
	}
	subscriptionRecord[colCommFilterHash] = []byte{}
	if _, err := channelSubscriptionFromRecord(subscriptionRecord); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("empty subscription filter hash decoded as NULL: %v", err)
	}

	attempt := byKind[deliveryAttemptKind].want.(DeliveryAttempt)
	attempt.ProviderReceiptHash = []byte{}
	attemptRecord, err := deliveryAttemptToRecord(attempt)
	if err != nil {
		t.Fatalf("encode attempt with absent provider receipt: %v", err)
	}
	if attemptRecord[colCommProviderReceiptHash] != nil {
		t.Fatalf("absent provider receipt persisted as %#v, want NULL",
			attemptRecord[colCommProviderReceiptHash])
	}
	attemptRecord[colCommProviderReceiptHash] = []byte{}
	if _, err := deliveryAttemptFromRecord(attemptRecord); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("empty provider receipt hash decoded as NULL: %v", err)
	}

	receipt := byKind[communicationCommandKind].want.(CommunicationCommandReceipt)
	receipt.AuditSeq = 0
	receipt.AuditHash = []byte{}
	binding, err := CanonicalCommunicationReceiptResponseBinding(receipt)
	if err != nil {
		t.Fatalf("bind receipt with absent audit hash: %v", err)
	}
	digest := sha256.Sum256(binding)
	receipt.ResponseDigest = digest[:]
	receiptRecord, err := communicationCommandReceiptToRecord(receipt)
	if err != nil {
		t.Fatalf("encode receipt with absent audit hash: %v", err)
	}
	if receiptRecord[colCommAuditHash] != nil {
		t.Fatalf("absent audit hash persisted as %#v, want NULL", receiptRecord[colCommAuditHash])
	}
	receiptRecord[colCommAuditHash] = []byte{}
	if _, err := communicationCommandReceiptFromRecord(receiptRecord); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("empty audit hash decoded as NULL: %v", err)
	}
}

func TestCommunicationCodecRejectsInvalidPayloadNullAndID(t *testing.T) {
	t.Parallel()

	fixtures := communicationCodecFixtures(t)
	byKind := make(map[model.Kind]communicationCodecRoundTripCase, len(fixtures))
	for _, fixture := range fixtures {
		byKind[fixture.kind] = fixture
	}

	t.Run("encode_invalid_domain_id", func(t *testing.T) {
		channel := byKind[channelKind].want.(Channel)
		channel.ID = model.ID("00000000-0000-4000-8000-000000000000")
		if _, err := channelToRecord(channel); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("encode invalid ID error = %v", err)
		}
	})

	t.Run("decode_invalid_record_id", func(t *testing.T) {
		record, err := byKind[channelKind].encode()
		if err != nil {
			t.Fatalf("encode channel: %v", err)
		}
		record[model.ColID] = "00000000-0000-4000-8000-000000000000"
		if _, err := channelFromRecord(record); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("decode invalid ID error = %v", err)
		}
	})

	t.Run("required_null", func(t *testing.T) {
		record, err := byKind[channelKind].encode()
		if err != nil {
			t.Fatalf("encode channel: %v", err)
		}
		record[colCommMaxFanout] = nil
		if _, err := channelFromRecord(record); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("decode required NULL error = %v", err)
		}
	})

	t.Run("partial_optional_pair", func(t *testing.T) {
		record, err := byKind[channelGrantKind].encode()
		if err != nil {
			t.Fatalf("encode grant: %v", err)
		}
		record[colCommRevokedByKind] = string(ActorUser)
		if _, err := channelGrantFromRecord(record); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("decode partial optional pair error = %v", err)
		}
	})

	t.Run("missing_optional_column", func(t *testing.T) {
		record, err := byKind[channelGrantKind].encode()
		if err != nil {
			t.Fatalf("encode grant: %v", err)
		}
		delete(record, colCommExpiresAt)
		if _, err := channelGrantFromRecord(record); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("decode missing optional column error = %v", err)
		}
	})

	payloadColumns := communicationProtectedPayloadColumns("payload")
	t.Run("required_payload_absent", func(t *testing.T) {
		record, err := byKind[messageKind].encode()
		if err != nil {
			t.Fatalf("encode message: %v", err)
		}
		setNullProtectedPayload(record, payloadColumns)
		if _, err := messageFromRecord(record, 0); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("decode absent payload error = %v", err)
		}
	})

	t.Run("partial_payload", func(t *testing.T) {
		record, err := byKind[messageKind].encode()
		if err != nil {
			t.Fatalf("encode message: %v", err)
		}
		record[payloadColumns.schema] = nil
		if _, err := messageFromRecord(record, 0); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("decode partial payload error = %v", err)
		}
	})

	t.Run("noncanonical_payload_json", func(t *testing.T) {
		message := byKind[messageKind].want.(Message)
		message.Payload = communicationTestPayload(t)
		record, err := messageToRecord(message, 0)
		if err != nil {
			t.Fatalf("encode plain message: %v", err)
		}
		record[payloadColumns.plainJSON] = "{ \"blocks\": [], \"subject\": \"x\" }"
		if _, err := messageFromRecord(record, 0); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("decode noncanonical payload error = %v", err)
		}
	})
}

func TestCommunicationCodecRejectsNonInjectiveCanonicalJSON(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  string
		out  func() any
	}{
		{
			name: "null projection", raw: `null`,
			out: func() any { return &CommunicationCommandResponseProjection{} },
		},
		{
			name: "null map", raw: `{"ids":null}`,
			out: func() any { return &CommunicationCommandResponseProjection{} },
		},
		{
			name: "case-insensitive field", raw: `{"VERSION":0}`,
			out: func() any { return &CommunicationCommandResponseProjection{} },
		},
		{
			name: "null scalar", raw: `{"counts":{"delivery_count":null}}`,
			out: func() any { return &CommunicationCommandResponseProjection{} },
		},
		{
			name: "null cursor projection", raw: `{"inbox_cursor":null}`,
			out: func() any { return &CommunicationCommandResponseProjection{} },
		},
		{
			name: "unknown cursor projection field",
			raw:  `{"inbox_cursor":{"last_seen_seq":0,"claim_fence":1}}`,
			out:  func() any { return &CommunicationCommandResponseProjection{} },
		},
		{
			name: "case-insensitive cursor projection field",
			raw:  `{"inbox_cursor":{"LastSeenSeq":0}}`,
			out:  func() any { return &CommunicationCommandResponseProjection{} },
		},
		{
			name: "null cursor projection scalar",
			raw:  `{"inbox_cursor":{"last_seen_seq":null}}`,
			out:  func() any { return &CommunicationCommandResponseProjection{} },
		},
		{
			name: "cursor projection version overflows int64",
			raw:  `{"version":9223372036854775808,"inbox_cursor":{"last_seen_seq":0}}`,
			out:  func() any { return &CommunicationCommandResponseProjection{} },
		},
		{
			name: "cursor sequence overflows int64",
			raw:  `{"version":1,"inbox_cursor":{"last_seen_seq":9223372036854775808}}`,
			out:  func() any { return &CommunicationCommandResponseProjection{} },
		},
		{
			name: "case-insensitive sealed envelope",
			raw:  `{"Ciphertext":"YQ==","KeyVersion":"seal-v1"}`,
			out:  func() any { return &SealedPayload{} },
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := newCommunicationRecordReader(communicationCommandKind, model.Record{})
			reader.decodeCanonicalJSON("closed_json", json.RawMessage(test.raw), test.out())
			if !errors.Is(reader.err, ErrInvalidCommunicationModel) {
				t.Fatalf("decode %s error = %v, want ErrInvalidCommunicationModel", test.raw, reader.err)
			}
		})
	}

	for _, raw := range []string{
		`{}`,
		`{"counts":{"delivery_count":0}}`,
		`{"inbox_cursor":{"last_seen_seq":0}}`,
		`{"ciphertext":"YQ==","key_version":"seal-v1"}`,
	} {
		raw := raw
		t.Run("canonical "+raw, func(t *testing.T) {
			t.Parallel()
			reader := newCommunicationRecordReader(communicationCommandKind, model.Record{})
			var out any
			if raw == `{"ciphertext":"YQ==","key_version":"seal-v1"}` {
				out = &SealedPayload{}
			} else {
				out = &CommunicationCommandResponseProjection{}
			}
			reader.decodeCanonicalJSON("closed_json", json.RawMessage(raw), out)
			if reader.err != nil {
				t.Fatalf("decode canonical %s: %v", raw, reader.err)
			}
		})
	}

	fixtures := communicationCodecFixtures(t)
	byKind := make(map[model.Kind]communicationCodecRoundTripCase, len(fixtures))
	for _, fixture := range fixtures {
		byKind[fixture.kind] = fixture
	}
	t.Run("receipt production path rejects case-insensitive key", func(t *testing.T) {
		receipt := byKind[communicationCommandKind].want.(CommunicationCommandReceipt)
		record, err := communicationCommandReceiptToRecord(receipt)
		if err != nil {
			t.Fatalf("encode receipt: %v", err)
		}
		projection := receipt.ResponseProjectionJSON
		nonInjective, err := canonicalJSON(map[string]any{
			"ids": projection.IDs, "VERSION": projection.Version, "state": projection.State,
			"counts": projection.Counts, "digests": projection.Digests,
		})
		if err != nil {
			t.Fatalf("canonicalize receipt mutator: %v", err)
		}
		record[colCommResponseProjectionJSON] = string(nonInjective)
		if _, err := communicationCommandReceiptFromRecord(record); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("case-insensitive receipt projection decoded: %v", err)
		}
	})
	t.Run("sealed production path rejects case-insensitive keys", func(t *testing.T) {
		message := byKind[messageKind].want.(Message)
		record, err := messageToRecord(message, 0)
		if err != nil {
			t.Fatalf("encode sealed message: %v", err)
		}
		sealed := message.Payload.Sealed
		nonInjective, err := canonicalJSON(map[string]any{
			"Ciphertext": sealed.Ciphertext,
			"KeyVersion": sealed.KeyVersion,
		})
		if err != nil {
			t.Fatalf("canonicalize sealed mutator: %v", err)
		}
		columns := communicationProtectedPayloadColumns("payload")
		record[columns.sealedJSON] = string(nonInjective)
		if _, err := messageFromRecord(record, 0); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("case-insensitive sealed envelope decoded: %v", err)
		}
	})
}

func TestCommunicationCodecCanonicalizesTimestamps(t *testing.T) {
	t.Parallel()

	fixture := communicationCodecFixtures(t)[0]
	channel := fixture.want.(Channel)
	offset := time.FixedZone("codec-offset", -7*60*60)
	createdAt := time.Date(2026, 8, 14, 11, 0, 0, 123456789, offset)
	channel.CreatedAt = createdAt
	channel.UpdatedAt = createdAt
	record, err := channelToRecord(channel)
	if err != nil {
		t.Fatalf("encode offset timestamp: %v", err)
	}
	wantTimestamp := model.NewTimestamp(createdAt).String()
	if record[model.ColCreatedAt] != wantTimestamp || record[model.ColUpdatedAt] != wantTimestamp {
		t.Fatalf("timestamps = %q/%q, want %q",
			record[model.ColCreatedAt], record[model.ColUpdatedAt], wantTimestamp)
	}
	decoded, err := channelFromRecord(record)
	if err != nil {
		t.Fatalf("decode timestamp: %v", err)
	}
	if !decoded.CreatedAt.Equal(createdAt) || !decoded.UpdatedAt.Equal(createdAt) {
		t.Fatalf("decoded timestamps = %v/%v, want instant %v",
			decoded.CreatedAt, decoded.UpdatedAt, createdAt)
	}
	record[model.ColCreatedAt] = "2026-08-14T11:00:00.123456789-07:00"
	if _, err := channelFromRecord(record); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("decode noncanonical timestamp error = %v", err)
	}
}
