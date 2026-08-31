// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func communicationStateTestScope() DirectoryScopeRef {
	return DirectoryScopeRef{TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID()}
}

func communicationStateTestMutable(scope DirectoryScopeRef, at time.Time) MutableCommunicationEntity {
	return MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
		ID: model.NewID(), TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		Version: 1, CreatedAt: at,
	}, UpdatedAt: at}
}

func communicationStateTestAppendOnly(scope DirectoryScopeRef, at time.Time) AppendOnlyCommunicationEntity {
	return AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
		ID: model.NewID(), TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		Version: 1, CreatedAt: at,
	}}
}

func communicationStateTestSealCausalArc(row MessageAudienceRecipient) MessageAudienceRecipient {
	hash, err := CanonicalAudienceCausalArcHash(row)
	if err != nil {
		panic(err)
	}
	row.CausalArcHash = hash
	return row
}

func communicationStateTestMessage(
	t *testing.T,
	scope DirectoryScopeRef,
	policy AckPolicy,
	requiredCount int64,
	at time.Time,
) Message {
	t.Helper()
	entity := communicationStateTestMutable(scope, at)
	publishedAt := at
	message := Message{
		MutableCommunicationEntity: entity,
		ChannelID:                  model.NewID(),
		ThreadID:                   entity.ID,
		Kind:                       MessageNotice,
		State:                      MessagePublished,
		Sender:                     CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()},
		Payload:                    communicationTestPayload(t),
		Urgency:                    UrgencyNormal,
		AckPolicy:                  policy,
		AvailableAt:                at,
		PublishedAt:                &publishedAt,
		AudienceHash:               bytes.Repeat([]byte{0x31}, sha256.Size),
		LastEventSeq:               1,
	}
	if policy != AckPolicyNone {
		due := at.Add(10 * time.Minute)
		message.AckDueAt = &due
		if policy == AckPolicyQuorum {
			message.AckQuorum = 2
		}
	}
	if err := ValidateMessage(message, requiredCount); err != nil {
		t.Fatalf("message fixture: %v", err)
	}
	return message
}

func communicationStateTestDelivery(
	message Message,
	recipient RecipientRef,
	sequence int64,
	required bool,
) MessageDelivery {
	entity := communicationStateTestMutable(
		DirectoryScopeRef{TenantID: message.TenantID, WorkspaceID: message.WorkspaceID},
		message.CreatedAt,
	)
	delivery := MessageDelivery{
		MutableCommunicationEntity: entity,
		MessageID:                  message.ID,
		Recipient:                  recipient,
		RecipientEpoch:             3,
		DeliverySeq:                sequence,
		Required:                   required,
		RouteReasons:               []RouteReason{"direct"},
		WakePolicy:                 WakeNone,
		State:                      DeliveryAvailable,
		AvailableAt:                message.AvailableAt,
		ExpiresAt:                  message.ExpiresAt,
	}
	if required {
		due := *message.AckDueAt
		delivery.AckDueAt = &due
	}
	return delivery
}

func communicationStateTestFulfillmentWitness(
	t *testing.T,
	message Message,
	deliveries []MessageDelivery,
	dbNow time.Time,
) FulfillmentDeliverySetWitness {
	t.Helper()
	digest, err := CanonicalFulfillmentDeliverySetDigest(deliveries)
	if err != nil {
		t.Fatalf("fulfillment digest: %v", err)
	}
	required := int64(0)
	for _, delivery := range deliveries {
		if delivery.Required {
			required++
		}
	}
	return FulfillmentDeliverySetWitness{
		Scope:     DirectoryScopeRef{TenantID: message.TenantID, WorkspaceID: message.WorkspaceID},
		MessageID: message.ID, DeliveryCount: int64(len(deliveries)), RequiredCount: required,
		Digest: digest, ObservedAt: dbNow,
		Evidence:    AuthorityEvidence{Verdict: VerdictClean, Code: "complete", EvidenceRef: "same_tx_query"},
		EvidenceRef: "same_tx_query",
	}
}

func TestOverlappingAudienceRouteSurvives(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	recipient := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	deliveryID := model.NewID()
	groupID := model.NewID()
	makeContribution := func(selector AudienceSelector, causalKind AudienceCausalKind) MessageAudienceRecipient {
		return MessageAudienceRecipient{
			AppendOnlyCommunicationEntity: communicationStateTestAppendOnly(scope, communicationTestNow),
			MessageAudienceID:             model.NewID(), MessageDeliveryID: deliveryID, Recipient: recipient,
			RecipientEpoch: 3, Required: selector.Required, WakePolicy: selector.WakePolicy,
			RouteReasons: []RouteReason{"route"}, Selector: selector, DirectoryEpoch: 42,
			ChannelACLRevision: 5, RouteRevision: 6, SubscriptionRevision: 7,
			CausalKind: causalKind, CausalRef: selector.Ref,
		}
	}
	direct := makeContribution(AudienceSelector{
		Kind: AudienceUser, Ref: recipient.Ref, Required: false, WakePolicy: WakeNone,
	}, CausalDirect)
	group := makeContribution(AudienceSelector{
		Kind: AudienceUserGroup, Ref: groupID.String(), Required: true, WakePolicy: WakePrimary,
	}, CausalUserGroup)
	group.CausalFactKind = model.Kind("core.user_group_member")
	group.CausalFactID = model.NewID()
	group.CausalFactVersion = 8
	workspace := makeContribution(AudienceSelector{
		Kind: AudienceWorkspaceMembers, Required: false, WakePolicy: WakePrimary,
	}, CausalWorkspaceMember)
	workspace.CausalRef = scope.WorkspaceID.String()
	workspace.CausalFactKind = model.Kind("core.membership")
	workspace.CausalFactID = model.NewID()
	workspace.CausalFactVersion = 5
	subscriber := makeContribution(AudienceSelector{
		Kind: AudienceSubscribers, Required: false, WakePolicy: WakeAll,
	}, CausalSubscriber)
	subscriber.CausalRef = groupID.String()
	subscriber.CausalFactKind = model.Kind("core.user_group_member")
	subscriber.CausalFactID = group.CausalFactID
	subscriber.CausalFactVersion = 8
	subscriber.OriginalSubscriber = &CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: groupID.String()}
	subscriber.SubscriptionID = model.NewID()
	subscriber.SubscriptionGeneration = 2
	direct = communicationStateTestSealCausalArc(direct)
	group = communicationStateTestSealCausalArc(group)
	workspace = communicationStateTestSealCausalArc(workspace)
	subscriber = communicationStateTestSealCausalArc(subscriber)

	fold, err := FoldAudienceContributions([]MessageAudienceRecipient{subscriber, workspace, direct, group})
	if err != nil {
		t.Fatalf("fold overlapping selectors: %v", err)
	}
	if !fold.Required || fold.WakePolicy != WakeAll || len(fold.Contributions) != 4 {
		t.Fatalf("overlap collapsed provenance: %#v", fold)
	}

	duplicate := group
	duplicate.ID = model.NewID()
	if _, err := FoldAudienceContributions([]MessageAudienceRecipient{group, duplicate}); err == nil {
		t.Fatal("duplicate selector-recipient arc accepted")
	}
	regranted := group
	regranted.ID = model.NewID()
	regranted.CausalFactID = model.NewID()
	regranted.CausalFactVersion++
	regranted = communicationStateTestSealCausalArc(regranted)
	if !bytes.Equal(regranted.CausalArcHash, group.CausalArcHash) {
		t.Fatal("historic fact replacement changed the stable causal relation identity")
	}
	if _, err := FoldAudienceContributions([]MessageAudienceRecipient{group, regranted}); err == nil {
		t.Fatal("same causal relation with a re-created fact was duplicated")
	}
	mixedEpoch := direct
	mixedEpoch.DirectoryEpoch++
	if _, err := FoldAudienceContributions([]MessageAudienceRecipient{direct, mixedEpoch}); err == nil {
		t.Fatal("mixed directory epochs folded")
	}
	badFact := group
	badFact.CausalFactKind = model.Kind("opaque.attacker_fact")
	if _, err := FoldAudienceContributions([]MessageAudienceRecipient{badFact}); err == nil {
		t.Fatal("untyped group causal fact accepted")
	}
}

func TestDirectorySnapshotSeparatesRecipientAndDirectoryEpochs(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	recipient := RecipientSnapshot{
		Scope: scope, Recipient: RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		RecipientEpoch: 3, DirectoryEpoch: 42, Eligible: true,
	}
	selector := AudienceSelector{Kind: AudienceUser, Ref: recipient.Recipient.Ref, WakePolicy: WakeNone}
	hash, err := CanonicalDirectoryRosterHash(scope, 42, []RecipientSnapshot{recipient})
	if err != nil {
		t.Fatalf("roster hash with distinct epochs: %v", err)
	}
	snapshot := DirectorySnapshot{
		Scope: scope, Epoch: 42, Selectors: []AudienceSelector{selector},
		Recipients: []RecipientSnapshot{recipient}, Contributions: []ResolvedAudienceContribution{{
			SelectorOrdinal: 1, Selector: selector, Recipient: recipient,
			WakePolicy: WakeNone, RouteReasons: []RouteReason{"direct"},
			CausalKind: CausalDirect, CausalRef: recipient.Recipient.Ref,
		}}, RosterHash: hash, ObservedAt: communicationTestNow,
		FreshUntil: communicationTestNow.Add(time.Minute),
	}
	if err := ValidateDirectorySnapshot(snapshot); err != nil {
		t.Fatalf("directory=42 recipient=3 rejected: %v", err)
	}
	snapshot.Contributions = nil
	if err := ValidateDirectorySnapshot(snapshot); err == nil {
		t.Fatal("explicit direct selector without recipient coverage accepted")
	}
}

func TestUndeliverableRequiresTombstone(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	message := communicationStateTestMessage(t, scope, AckPolicyEachRequired, 1, communicationTestNow)
	recipient := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	delivery := communicationStateTestDelivery(message, recipient, 1, true)
	witness := store.DirectoryTombstoneWitness{
		TombstoneKind: model.UserTombstoneKind, TombstoneID: model.NewID(), TombstoneVersion: 1,
		Principal: store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser, PrincipalRef: model.ID(recipient.Ref),
		}, RetirementEpoch: 41,
	}
	snapshot := RecipientSnapshot{
		Scope: scope, Recipient: recipient, RecipientEpoch: 9, DirectoryEpoch: 42,
		Eligible: false, Tombstone: &witness,
	}
	dbNow := communicationTestNow.Add(time.Minute)
	plan, err := PlanUndeliverable(message, delivery, snapshot, dbNow, "principal_retired")
	if err != nil {
		t.Fatalf("tombstone retirement plan: %v", err)
	}
	if plan.CreatesAck || plan.After.State != DeliveryUndeliverable || plan.After.AckID != "" {
		t.Fatalf("retirement fabricated Ack or wrong state: %#v", plan)
	}

	missing := snapshot
	missing.Tombstone = nil
	if _, err := PlanUndeliverable(message, delivery, missing, dbNow, "principal_missing"); err == nil {
		t.Fatal("reversible absence terminalized Delivery")
	}
	future := snapshot
	futureWitness := witness
	futureWitness.RetirementEpoch = 43
	future.Tombstone = &futureWitness
	if _, err := PlanUndeliverable(message, delivery, future, dbNow, "future_tombstone"); err == nil {
		t.Fatal("future tombstone accepted under older directory epoch")
	}
	ackNow := dbNow.Add(time.Second)
	principal := CommunicationPrincipal{UserID: model.ID(recipient.Ref)}
	authority := communicationStateTestReadEvidence(scope, message, plan.After, principal, ackNow)
	authority.Operation = CommunicationDeliveryWrite
	authority.Core.Operation = CommunicationDeliveryWrite
	if _, err := PlanMessageAck(plan.After, model.NewID(), CommunicationActorRef{
		Kind: ActorUser, Ref: recipient.Ref,
	}, nil, &authority, nil, ackNow); !errors.Is(err, ErrCommunicationTerminal) {
		t.Fatalf("undeliverable Ack error = %v, want terminal with valid authority", err)
	}
	if _, err := PlanUndeliverable(message, delivery, snapshot, *delivery.AckDueAt, "late_retirement"); err == nil {
		t.Fatal("retirement bypassed required deadline expiry")
	}
}

func TestMessageFulfillmentVectors(t *testing.T) {
	t.Parallel()

	type vector struct {
		name   string
		policy AckPolicy
		states []MessageDeliveryState
		want   FulfillmentState
	}
	vectors := []vector{
		{name: "quorum_met_with_viable", policy: AckPolicyQuorum,
			states: []MessageDeliveryState{DeliveryAcknowledged, DeliveryAcknowledged, DeliveryAvailable},
			want:   FulfillmentMet},
		{name: "each_unmet", policy: AckPolicyEachRequired,
			states: []MessageDeliveryState{DeliveryAcknowledged, DeliveryAcknowledged, DeliveryExpired},
			want:   FulfillmentUnmet},
		{name: "quorum_three_unmet", policy: AckPolicyQuorum,
			states: []MessageDeliveryState{DeliveryAcknowledged, DeliveryAcknowledged, DeliveryExpired},
			want:   FulfillmentUnmet},
		{name: "quorum_two_stays_met", policy: AckPolicyQuorum,
			states: []MessageDeliveryState{DeliveryAcknowledged, DeliveryAcknowledged, DeliveryExpired},
			want:   FulfillmentMet},
	}
	for _, test := range vectors {
		t.Run(test.name, func(t *testing.T) {
			scope := communicationStateTestScope()
			message := communicationStateTestMessage(t, scope, test.policy, int64(len(test.states)), communicationTestNow)
			if test.name == "quorum_three_unmet" {
				message.AckQuorum = 3
			}
			deliveries := make([]MessageDelivery, 0, len(test.states))
			for i, state := range test.states {
				delivery := communicationStateTestDelivery(message, RecipientRef{
					Kind: RecipientUser, Ref: model.NewID().String(),
				}, int64(i+1), true)
				delivery.State = state
				if state == DeliveryAcknowledged {
					ackAt := communicationTestNow.Add(time.Minute)
					delivery.UpdatedAt = ackAt
					delivery.AckID = model.NewID()
					delivery.AcknowledgedAt = &ackAt
				}
				deliveries = append(deliveries, delivery)
			}
			dbNow := communicationTestNow.Add(5 * time.Minute)
			witness := communicationStateTestFulfillmentWitness(t, message, deliveries, dbNow)
			projection, err := ProjectMessageFulfillment(message, deliveries, witness, dbNow)
			if err != nil {
				t.Fatalf("project vector: %v", err)
			}
			if projection.State != test.want ||
				projection.Required != projection.Acknowledged+projection.Viable+projection.Unmet {
				t.Fatalf("projection = %#v, want %s and exhaustive sets", projection, test.want)
			}
			if len(deliveries) > 1 {
				if _, err := ProjectMessageFulfillment(message, deliveries[:len(deliveries)-1], witness, dbNow); err == nil {
					t.Fatal("truncated Delivery set satisfied authoritative witness")
				}
			}
		})
	}
}

func TestAckDueBeforeMessageExpiryIsAlreadyUnmet(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	message := communicationStateTestMessage(t, scope, AckPolicyEachRequired, 1, communicationTestNow)
	expires := communicationTestNow.Add(time.Hour)
	message.ExpiresAt = &expires
	delivery := communicationStateTestDelivery(message,
		RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}, 1, true)
	dbNow := message.AckDueAt.Add(5 * time.Minute)
	witness := communicationStateTestFulfillmentWitness(t, message, []MessageDelivery{delivery}, dbNow)
	projection, err := ProjectMessageFulfillment(message, []MessageDelivery{delivery}, witness, dbNow)
	if err != nil {
		t.Fatalf("due < now < expires projection: %v", err)
	}
	if !dbNow.Before(expires) || projection.State != FulfillmentUnmet || projection.Unmet != 1 ||
		projection.Viable != 0 {
		t.Fatalf("elapsed Ack due remained viable until Message expiry: %#v", projection)
	}
	expiry, err := PlanMessageDeliveryExpiry(delivery, dbNow)
	if err != nil || expiry.After.State != DeliveryExpired {
		t.Fatalf("elapsed Ack due did not materialize Delivery expiry: %#v, %v", expiry, err)
	}
	seen, err := PlanMessageDeliverySeen(delivery, *delivery.AckDueAt)
	if err != nil || !seen.MaterializesExpiry || seen.ExpiryCode != "ack_deadline_elapsed" ||
		seen.After.State != DeliveryExpired || seen.After.FirstSeenAt != nil {
		t.Fatalf("Seen observed an overdue available Delivery without expiring it first: %#v, %v", seen, err)
	}
	retracted, err := PlanMessageDeliveryRetraction(delivery, *delivery.AckDueAt)
	if err != nil || !retracted.MaterializesExpiry || retracted.ExpiryCode != "ack_deadline_elapsed" ||
		retracted.After.State != DeliveryExpired {
		t.Fatalf("retract observed an overdue available Delivery without expiring it first: %#v, %v",
			retracted, err)
	}
	beforeDue := delivery.AckDueAt.Add(-time.Nanosecond)
	seenBeforeDue, err := PlanMessageDeliverySeen(delivery, beforeDue)
	if err != nil || seenBeforeDue.MaterializesExpiry || seenBeforeDue.After.State != DeliveryAvailable ||
		seenBeforeDue.After.FirstSeenAt == nil || *seenBeforeDue.After.FirstSeenAt != beforeDue {
		t.Fatalf("timely Seen incorrectly materialized expiry: %#v, %v", seenBeforeDue, err)
	}
}

func communicationStateTestDirectContribution(
	scope DirectoryScopeRef,
	delivery MessageDelivery,
) MessageAudienceRecipient {
	selector := AudienceSelector{
		Kind: AudienceSelectorKind(delivery.Recipient.Kind), Ref: delivery.Recipient.Ref,
		Required: delivery.Required, WakePolicy: delivery.WakePolicy,
	}
	return MessageAudienceRecipient{
		AppendOnlyCommunicationEntity: communicationStateTestAppendOnly(scope, delivery.CreatedAt),
		MessageAudienceID:             model.NewID(), MessageDeliveryID: delivery.ID, Recipient: delivery.Recipient,
		RecipientEpoch: delivery.RecipientEpoch, Required: delivery.Required, WakePolicy: delivery.WakePolicy,
		RouteReasons: append([]RouteReason(nil), delivery.RouteReasons...),
		Selector:     selector, DirectoryEpoch: 7, ChannelACLRevision: 1, RouteRevision: 1,
		SubscriptionRevision: 1, CausalKind: CausalDirect, CausalRef: delivery.Recipient.Ref,
	}
}

func communicationStateTestRebindCurrentAudienceSet(
	current *CurrentAudienceEvidence,
	messageVersion int64,
) []byte {
	audienceByID := make(map[model.ID]MessageAudience, len(current.Contributions))
	rows := make([]MessageAudienceRecipient, 0, len(current.Contributions))
	for _, contribution := range current.Contributions {
		audienceByID[contribution.Audience.ID] = contribution.Audience
		rows = append(rows, contribution.Contribution)
	}
	audiences := make([]MessageAudience, 0, len(audienceByID))
	for _, audience := range audienceByID {
		audiences = append(audiences, audience)
	}
	sort.Slice(audiences, func(i, j int) bool { return audiences[i].Ordinal < audiences[j].Ordinal })
	scope := DirectoryScopeRef{TenantID: current.TenantID, WorkspaceID: current.WorkspaceID}
	message := Message{MutableCommunicationEntity: MutableCommunicationEntity{
		CommunicationEntity: CommunicationEntity{
			ID: current.MessageID, TenantID: current.TenantID, WorkspaceID: current.WorkspaceID,
			Version: messageVersion,
		},
	}}
	audienceHash, err := CanonicalMessageAudienceHash(message, audiences, rows)
	if err != nil {
		panic(err)
	}
	digest, err := CanonicalCurrentAudienceSetDigest(scope, current.MessageID, messageVersion, audiences, rows)
	if err != nil {
		panic(err)
	}
	current.SetWitness = CurrentAudienceSetWitness{
		Scope: scope, MessageID: current.MessageID, MessageVersion: messageVersion,
		DeliveryID: current.DeliveryID, Recipient: current.Recipient,
		MessageAudienceHash: audienceHash, AudienceCount: int64(len(audiences)),
		ContributionCount: int64(len(rows)), Audiences: audiences, Contributions: rows,
		SetDigest: digest, ObservedAt: current.ObservedAt,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "complete", EvidenceRef: "same_tx_audience_set",
		},
	}
	return audienceHash
}

func communicationStateTestReadEvidence(
	scope DirectoryScopeRef,
	message Message,
	delivery MessageDelivery,
	principal CommunicationPrincipal,
	observedAt time.Time,
) ProtectedReadEvidence {
	entity := EntityRef{
		TenantID: scope.TenantID, Kind: model.Kind("sessions.message_delivery"),
		ID: delivery.ID, WorkspaceID: scope.WorkspaceID,
	}
	contribution := communicationStateTestDirectContribution(scope, delivery)
	if delivery.Recipient.Kind == RecipientSession {
		contribution.ObservedSessionSID = delivery.Recipient.Ref
		contribution.ObservedClaimFence = principal.SessionFence
	}
	contribution = communicationStateTestSealCausalArc(contribution)
	selectorJSON, err := canonicalJSON(contribution.Selector)
	if err != nil {
		panic(err)
	}
	selectorHash := sha256.Sum256(selectorJSON)
	audience := MessageAudience{
		AppendOnlyCommunicationEntity: communicationStateTestAppendOnly(scope, delivery.CreatedAt),
		MessageID:                     message.ID, Ordinal: 1, Selector: contribution.Selector,
		ChannelACLRevision: contribution.ChannelACLRevision,
		RouteRevision:      contribution.RouteRevision, SubscriptionRevision: contribution.SubscriptionRevision,
		DirectoryEpoch: contribution.DirectoryEpoch, DirectorySnapshotAt: observedAt,
		ResolvedCount: 1, SelectorHash: selectorHash[:],
	}
	audience.ID = contribution.MessageAudienceID
	resolvedHash, err := canonicalResolvedAudienceHash(audience, []MessageAudienceRecipient{contribution})
	if err != nil {
		panic(err)
	}
	audience.ResolvedHash = resolvedHash
	message.AudienceHash, err = CanonicalMessageAudienceHash(
		message, []MessageAudience{audience}, []MessageAudienceRecipient{contribution},
	)
	if err != nil {
		panic(err)
	}
	setDigest, err := CanonicalCurrentAudienceSetDigest(
		scope, message.ID, message.Version,
		[]MessageAudience{audience}, []MessageAudienceRecipient{contribution},
	)
	if err != nil {
		panic(err)
	}
	clean := AuthorityEvidence{Verdict: VerdictClean, Code: "current", EvidenceRef: "current"}
	boundRecipient := func(check RecipientAuthorityCheck) BoundRecipientAuthorityEvidence {
		return BoundRecipientAuthorityEvidence{
			Scope: scope, Recipient: delivery.Recipient, DirectoryEpoch: 7, Check: check,
			ObservedAt: observedAt, Evidence: clean,
		}
	}
	resolution := communicationStateTestPrincipalResolution(scope, principal, delivery.Recipient)
	requiredCount := int64(0)
	if delivery.Required {
		requiredCount = 1
	}
	carrier := ProtectedCarrierRef{
		Entity: entity, ChannelID: message.ChannelID, MessageID: message.ID, DeliveryID: delivery.ID,
	}
	causalWitness := CausalAuthorityWitness{
		Kind: CausalAuthorityDirectPrincipal, ContributionID: contribution.ID,
		Scope: scope, Recipient: contribution.Recipient, CausalKind: contribution.CausalKind,
		CausalRef: contribution.CausalRef, DirectoryEpoch: 7,
		ObservedAt: observedAt, Evidence: clean,
	}
	if delivery.Recipient.Kind == RecipientSession {
		causalWitness.Kind = CausalAuthoritySessionClaim
		causalWitness.ObservedSessionSID = delivery.Recipient.Ref
		causalWitness.ObservedClaimFence = principal.SessionFence
	}
	return ProtectedReadEvidence{
		Scope: scope, ChannelID: message.ChannelID, ChannelACLRevision: 1,
		DBNow: observedAt, Operation: CommunicationRead,
		Carrier: carrier,
		CarrierState: ProtectedCarrierSnapshot{
			Message: message, Delivery: delivery, RequiredDeliveryCount: requiredCount,
			ObservedAt: observedAt, Evidence: clean,
		},
		Core: ReadWitness{
			Outcome: ReadAllow, Code: "allowed", Entity: entity, Operation: CommunicationRead,
			Principal: principal, ObservedAt: observedAt,
			FreshUntil: observedAt.Add(10 * time.Minute), CorePermission: clean,
			ResourceGuard: clean, ForbidAbsence: clean,
		},
		Principal: principal, PrincipalResolution: resolution, Recipient: delivery.Recipient,
		DirectoryEpoch: store.AuthorizationFactRef{
			Kind: model.DirectoryEpochKind, ID: model.ID(scope.TenantID), Version: 7,
		},
		CurrentChannelGrant: BoundChannelReadEvidence{
			TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, ChannelID: message.ChannelID,
			Principal: principal, Bit: ChannelGrantRead, GrantID: model.NewID(), GrantVersion: 1,
			DirectoryEpoch:     7,
			ChannelACLRevision: 1, EvaluatedAt: observedAt, Evidence: clean,
		},
		EntityRecipientGuard: BoundEntityRecipientEvidence{
			Scope: scope, Carrier: carrier, Principal: principal, Recipient: delivery.Recipient,
			DirectoryEpoch: 7, EvaluatedAt: observedAt, Evidence: clean,
		},
		CurrentAudience: CurrentAudienceEvidence{
			TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			Recipient: delivery.Recipient, DeliveryID: delivery.ID, MessageID: message.ID, DirectoryEpoch: 7,
			ObservedAt:             observedAt,
			FreshUntil:             observedAt.Add(10 * time.Minute),
			RecipientExists:        boundRecipient(RecipientCheckExists),
			RecipientEligible:      boundRecipient(RecipientCheckEligible),
			RecipientNotTombstoned: boundRecipient(RecipientCheckNotTombstoned),
			SetWitness: CurrentAudienceSetWitness{
				Scope: scope, MessageID: message.ID, MessageVersion: message.Version,
				DeliveryID: delivery.ID, Recipient: delivery.Recipient,
				MessageAudienceHash: append([]byte(nil), message.AudienceHash...),
				AudienceCount:       1, ContributionCount: 1,
				Audiences:     []MessageAudience{audience},
				Contributions: []MessageAudienceRecipient{contribution},
				SetDigest:     setDigest, ObservedAt: observedAt,
				Evidence: AuthorityEvidence{
					Verdict: VerdictClean, Code: "complete", EvidenceRef: "same_tx_audience_set",
				},
			},
			Contributions: []CausalContributionEvidence{{
				Audience: audience, Contribution: contribution, Witness: causalWitness,
			}},
		},
	}
}

func communicationStateTestCursorReadEvidence(
	scope DirectoryScopeRef,
	message *Message,
	delivery MessageDelivery,
	principal CommunicationPrincipal,
	observedAt time.Time,
) ProtectedReadEvidence {
	evidence := communicationStateTestReadEvidence(scope, *message, delivery, principal, observedAt)
	*message = evidence.CarrierState.Message
	return evidence
}

func communicationStateTestCursorWorkspaceEvidence(
	t *testing.T,
	scope DirectoryScopeRef,
	message *Message,
	delivery MessageDelivery,
	principal CommunicationPrincipal,
	observedAt time.Time,
) ProtectedReadEvidence {
	t.Helper()
	evidence := communicationStateTestCursorReadEvidence(
		scope, message, delivery, principal, observedAt,
	)
	audience := evidence.CurrentAudience.Contributions[0].Audience
	row := evidence.CurrentAudience.Contributions[0].Contribution
	selector := AudienceSelector{
		Kind: AudienceWorkspaceMembers, Required: delivery.Required, WakePolicy: delivery.WakePolicy,
	}
	audience.Selector = selector
	selectorRaw, err := canonicalJSON(selector)
	if err != nil {
		t.Fatalf("workspace selector: %v", err)
	}
	selectorHash := sha256.Sum256(selectorRaw)
	audience.SelectorHash = selectorHash[:]
	row.Selector = selector
	row.CausalKind = CausalWorkspaceMember
	row.CausalRef = scope.WorkspaceID.String()
	row.CausalFactKind = model.Kind("core.membership")
	row.CausalFactID = model.NewID()
	row.CausalFactVersion = 1
	row = communicationStateTestSealCausalArc(row)
	audience.ResolvedHash, err = canonicalResolvedAudienceHash(
		audience, []MessageAudienceRecipient{row},
	)
	if err != nil {
		t.Fatalf("workspace audience: %v", err)
	}
	currentFact := store.AuthorizationFactRef{
		Kind: model.Kind("core.membership"), ID: model.NewID(), Version: 8,
	}
	relation := CausalRelationWitness{
		Scope: scope, Recipient: delivery.Recipient,
		Subject:    CommunicationSubjectRef{Kind: SubjectUser, Ref: delivery.Recipient.Ref},
		CausalKind: CausalWorkspaceMember, CausalRef: scope.WorkspaceID.String(),
		DirectoryEpoch: evidence.CurrentAudience.DirectoryEpoch, CurrentFact: &currentFact,
	}
	witness := CausalAuthorityWitness{
		Kind: CausalAuthorityWorkspaceUser, Scope: scope, ContributionID: row.ID,
		Recipient: delivery.Recipient, CausalKind: CausalWorkspaceMember,
		CausalRef: scope.WorkspaceID.String(), CurrentFact: &currentFact, CurrentRelation: &relation,
		DirectoryEpoch: evidence.CurrentAudience.DirectoryEpoch, ObservedAt: observedAt,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "current", EvidenceRef: "workspace-membership-current",
		},
	}
	evidence.CurrentAudience.Contributions = []CausalContributionEvidence{{
		Audience: audience, Contribution: row, Witness: witness,
	}}
	message.AudienceHash = communicationStateTestRebindCurrentAudienceSet(
		&evidence.CurrentAudience, message.Version,
	)
	evidence.CarrierState.Message = *message
	evidence.Core.Facts = append(evidence.Core.Facts, currentFact)
	return evidence
}

func communicationStateTestCursorRebindAudience(
	t *testing.T,
	message *Message,
	evidence *ProtectedReadEvidence,
	mutate func(*MessageAudience, *MessageAudienceRecipient),
) {
	t.Helper()
	item := evidence.CurrentAudience.Contributions[0]
	mutate(&item.Audience, &item.Contribution)
	selectorRaw, err := canonicalJSON(item.Audience.Selector)
	if err != nil {
		t.Fatalf("cursor selector: %v", err)
	}
	selectorHash := sha256.Sum256(selectorRaw)
	item.Audience.SelectorHash = selectorHash[:]
	item.Contribution = communicationStateTestSealCausalArc(item.Contribution)
	item.Audience.ResolvedHash, err = canonicalResolvedAudienceHash(
		item.Audience, []MessageAudienceRecipient{item.Contribution},
	)
	if err != nil {
		t.Fatalf("cursor resolved audience: %v", err)
	}
	evidence.CurrentAudience.Contributions = []CausalContributionEvidence{item}
	message.AudienceHash = communicationStateTestRebindCurrentAudienceSet(
		&evidence.CurrentAudience, message.Version,
	)
	evidence.CarrierState.Message = *message
}

func communicationStateTestCursor(
	t *testing.T,
	scope DirectoryScopeRef,
	reader RecipientRef,
	filter CursorFilter,
) InboxCursor {
	t.Helper()
	if filter.CarrierClass == "" && filter.MailboxKind == "" && len(filter.ChannelIDs) == 0 &&
		len(filter.WorkItemIDs) == 0 && len(filter.MessageKinds) == 0 && len(filter.Urgencies) == 0 {
		filter = communicationStateTestDirectNoticeCursorFilter()
	}
	_, hash, err := CanonicalCursorFilter(filter)
	if err != nil {
		t.Fatalf("cursor filter: %v", err)
	}
	return InboxCursor{
		MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
		Reader:                     reader, MailboxKind: MailboxPersonal, MailboxRef: reader.Ref,
		LastSeenAt: communicationTestNow, FilterHash: hash,
	}
}

func communicationStateTestDirectNoticeCursorFilter() CursorFilter {
	return CursorFilter{
		CarrierClass: CursorCarrierDirectNoticeV1,
		MailboxKind:  MailboxPersonal,
	}
}

func communicationStateTestCursorCarrierSet(
	scope DirectoryScopeRef,
	message Message,
	delivery MessageDelivery,
	dbNow time.Time,
) *CursorCarrierSetWitness {
	return &CursorCarrierSetWitness{
		Scope: scope, MessageID: message.ID, DeliveryID: delivery.ID, DeliveryCount: 1,
		ObservedAt: dbNow,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "complete", EvidenceRef: "same_tx_delivery_set",
		},
	}
}

func communicationStateTestCursorScanWitness(
	t *testing.T,
	cursor InboxCursor,
	requestedSeq int64,
	entries []CursorScanEntry,
	dbNow time.Time,
) CursorMailboxScanWitness {
	t.Helper()
	witness := CursorMailboxScanWitness{
		Scope:  DirectoryScopeRef{TenantID: cursor.TenantID, WorkspaceID: cursor.WorkspaceID},
		Reader: cursor.Reader, MailboxKind: cursor.MailboxKind, MailboxRef: cursor.MailboxRef,
		FilterHash: append([]byte(nil), cursor.FilterHash...), FromExclusive: cursor.LastSeenSeq,
		ToInclusive: requestedSeq, EntryCount: int64(len(entries)), ObservedAt: dbNow,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "complete", EvidenceRef: "same_tx_mailbox_scan",
		},
	}
	if requestedSeq > cursor.LastSeenSeq && len(entries) != 0 {
		witness.TargetDeliveryID = entries[len(entries)-1].DeliveryID
	}
	digest, err := CanonicalCursorMailboxScanDigest(witness, entries)
	if err != nil {
		t.Fatalf("cursor mailbox scan digest: %v", err)
	}
	witness.Digest = digest
	return witness
}

func communicationStateTestBarrierSetWitness(
	t *testing.T,
	scope DirectoryScopeRef,
	cursor InboxCursor,
	barriers []InboxCursorBarrier,
	dbNow time.Time,
) CursorBarrierSetWitness {
	t.Helper()
	digest, err := CanonicalCursorBarrierSetDigest(cursor, barriers)
	if err != nil {
		t.Fatalf("cursor barrier digest: %v", err)
	}
	return CursorBarrierSetWitness{
		Scope: scope, CursorID: cursor.ID, BarrierCount: int64(len(barriers)), Digest: digest,
		ObservedAt: dbNow,
		Evidence:   AuthorityEvidence{Verdict: VerdictClean, Code: "complete", EvidenceRef: "same_tx_barriers"},
	}
}

func communicationStateTestPrincipalResolution(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	recipient RecipientRef,
) PrincipalResolution {
	return PrincipalResolution{
		Outcome: PrincipalResolved, Code: "resolved", Scope: scope, Principal: principal,
		Recipient: &RecipientSnapshot{
			Scope: scope, Recipient: recipient, RecipientEpoch: 3, DirectoryEpoch: 7, Eligible: true,
		}, ObservedAt: communicationTestNow, FreshUntil: communicationTestNow.Add(10 * time.Minute),
	}
}

func TestCursorStopsAtVisibilityBarrier(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	scheduled := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	scheduled.AvailableAt = dbNow.Add(time.Minute)
	scheduledDelivery := communicationStateTestDelivery(scheduled, reader, 1, false)
	visible := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	visibleDelivery := communicationStateTestDelivery(visible, reader, 2, false)
	resolution := communicationStateTestPrincipalResolution(scope, principal, reader)
	scheduledEvidence := communicationStateTestCursorReadEvidence(
		scope, &scheduled, scheduledDelivery, principal, dbNow,
	)
	visibleEvidence := communicationStateTestCursorReadEvidence(
		scope, &visible, visibleDelivery, principal, dbNow,
	)
	scan := []CursorScanEntry{
		{TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			DeliveryID: scheduledDelivery.ID, Sequence: 1, Delivery: &scheduledDelivery, Message: &scheduled,
			CarrierSet:   communicationStateTestCursorCarrierSet(scope, scheduled, scheduledDelivery, dbNow),
			ReadEvidence: &scheduledEvidence},
		{TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			DeliveryID: visibleDelivery.ID, Sequence: 2, Delivery: &visibleDelivery, Message: &visible,
			CarrierSet:   communicationStateTestCursorCarrierSet(scope, visible, visibleDelivery, dbNow),
			ReadEvidence: &visibleEvidence},
	}
	plan, err := PlanCursorAdvance(CursorAdvanceInput{
		Scope: scope, Principal: resolution, Cursor: cursor, ExpectedVersion: cursor.Version,
		Filter: filter, RequestedSeq: 2, DBNow: dbNow,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
		ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 2, scan, dbNow),
		Scan:              scan,
	})
	if err != nil {
		t.Fatalf("cursor advance: %v", err)
	}
	if plan.Verdict != VerdictClean || plan.EffectiveSeq != 0 || len(plan.Create) != 1 ||
		plan.Create[0].BarrierSeq != 1 || plan.Create[0].Cause != BarrierNotYetAvailable ||
		plan.After.Version != cursor.Version+1 || plan.After.LastSeenSeq != cursor.LastSeenSeq ||
		!plan.Changed {
		t.Fatalf("cursor crossed scheduled barrier: %#v", plan)
	}
}

func TestCursorUnknownChangesNothing(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, reader, 1, false)
	dbNow := communicationTestNow.Add(time.Minute)
	evidence := communicationStateTestCursorReadEvidence(scope, &message, delivery, principal, dbNow)
	evidence.Core.CorePermission = AuthorityEvidence{
		Verdict: VerdictUnknown, Code: "core_unavailable", EvidenceRef: "core-unavailable",
	}
	evidence.Core.Outcome = ReadUnknown
	scan := []CursorScanEntry{{
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		DeliveryID: delivery.ID, Sequence: 1, Delivery: &delivery, Message: &message,
		CarrierSet:   communicationStateTestCursorCarrierSet(scope, message, delivery, dbNow),
		ReadEvidence: &evidence,
	}}
	plan, err := PlanCursorAdvance(CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 1, DBNow: dbNow,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
		ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 1, scan, dbNow),
		Scan:              scan,
	})
	if err != nil {
		t.Fatalf("unknown cursor evidence: %v", err)
	}
	if plan.Verdict != VerdictUnknown || plan.EffectiveSeq != cursor.LastSeenSeq ||
		len(plan.Create) != 0 || len(plan.Resolve) != 0 || len(plan.Expire) != 0 ||
		len(plan.Facts) != 0 || len(plan.RequiredClaims) != 0 || len(plan.ChannelFences) != 0 ||
		!reflect.DeepEqual(plan.Before, cursor) || !reflect.DeepEqual(plan.After, cursor) {
		t.Fatalf("UNKNOWN mutated cursor plan: %#v", plan)
	}
}

func TestCursorBarrierSetIsCompleteAndOnlyExplicitPutResolves(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, reader, 1, false)
	barrier := InboxCursorBarrier{
		MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
		Reader:                     reader, MailboxKind: cursor.MailboxKind, MailboxRef: cursor.MailboxRef,
		FilterHash: append([]byte(nil), cursor.FilterHash...), DeliveryID: delivery.ID, BarrierSeq: 1,
		Cause: BarrierTemporarilyInvisible, State: CursorBarrierActive, ReasonCode: "grant_revoked",
	}
	completeWitness := communicationStateTestBarrierSetWitness(t, scope, cursor,
		[]InboxCursorBarrier{barrier}, dbNow)
	evidence := communicationStateTestCursorReadEvidence(scope, &message, delivery, principal, dbNow)
	scan := []CursorScanEntry{{
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		DeliveryID: delivery.ID, Sequence: 1, Delivery: &delivery, Message: &message,
		CarrierSet:   communicationStateTestCursorCarrierSet(scope, message, delivery, dbNow),
		ReadEvidence: &evidence,
	}}
	scanWitness := communicationStateTestCursorScanWitness(t, cursor, 1, scan, dbNow)
	omitted, err := PlanCursorAdvance(CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 1, DBNow: dbNow,
		BarrierSetWitness: completeWitness,
		ScanWitness:       scanWitness,
		Scan:              scan,
	})
	if err != nil || omitted.Verdict != VerdictUnknown || !reflect.DeepEqual(omitted.After, cursor) {
		t.Fatalf("omitted durable barrier was not deny-closed: %#v, %v", omitted, err)
	}
	duplicate := barrier
	duplicate.MutableCommunicationEntity = communicationStateTestMutable(scope, communicationTestNow)
	duplicateSet := []InboxCursorBarrier{barrier, duplicate}
	duplicated, err := PlanCursorAdvance(CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 1, DBNow: dbNow,
		ActiveBarriers:    duplicateSet,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, duplicateSet, dbNow),
		ScanWitness:       scanWitness,
		Scan:              scan,
	})
	if err != nil || duplicated.Verdict != VerdictUnknown || !reflect.DeepEqual(duplicated.After, cursor) {
		t.Fatalf("duplicate active barrier was not deny-closed: %#v, %v", duplicated, err)
	}

	regranted, err := PlanCursorAdvance(CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 1, DBNow: dbNow,
		ActiveBarriers: []InboxCursorBarrier{barrier}, BarrierSetWitness: completeWitness,
		ScanWitness: scanWitness,
		Scan:        scan,
	})
	if err != nil || regranted.Verdict != VerdictClean || len(regranted.Resolve) != 1 ||
		regranted.EffectiveSeq != 1 || regranted.After.LastSeenSeq != 1 ||
		regranted.After.Version != cursor.Version+1 || regranted.After.LastSeenAt != dbNow {
		t.Fatalf("explicit regrant PUT did not resolve barrier atomically: %#v, %v", regranted, err)
	}

	resolved := barrier
	resolved.State = CursorBarrierResolved
	resolved.Version++
	resolved.UpdatedAt = dbNow
	resolved.ResolvedAt = &dbNow
	if err := ValidateInboxCursorBarrier(cursor, resolved); err != nil {
		t.Fatalf("valid resolved barrier: %v", err)
	}
	resolved.ResolvedAt = nil
	if err := ValidateInboxCursorBarrier(cursor, resolved); err == nil {
		t.Fatal("resolved barrier without ResolvedAt accepted")
	}
}

func TestCursorSparseMailboxGapAndTemporaryInvisibility(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	hiddenMessage := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	hiddenDelivery := communicationStateTestDelivery(hiddenMessage, reader, 2, false)
	hiddenEvidence := communicationStateTestCursorReadEvidence(
		scope, &hiddenMessage, hiddenDelivery, principal, dbNow,
	)
	hiddenEvidence.CurrentChannelGrant.Evidence = AuthorityEvidence{
		Verdict: VerdictBroken, Code: "grant_revoked", EvidenceRef: "grant-current",
	}
	scan := []CursorScanEntry{{
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		DeliveryID: hiddenDelivery.ID, Sequence: 2, Delivery: &hiddenDelivery, Message: &hiddenMessage,
		CarrierSet:   communicationStateTestCursorCarrierSet(scope, hiddenMessage, hiddenDelivery, dbNow),
		ReadEvidence: &hiddenEvidence,
	}}
	plan, err := PlanCursorAdvance(CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 2, DBNow: dbNow,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
		ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 2, scan, dbNow),
		Scan:              scan,
	})
	if err != nil || plan.Verdict != VerdictClean || plan.EffectiveSeq != 1 || len(plan.Create) != 1 ||
		plan.Create[0].BarrierSeq != 2 || plan.Create[0].Cause != BarrierTemporarilyInvisible {
		t.Fatalf("foreign gap or temporary barrier classified incorrectly: %#v, %v", plan, err)
	}
}

func TestDirectNoticeCursorFilterHasFixedHash(t *testing.T) {
	t.Parallel()

	filter := communicationStateTestDirectNoticeCursorFilter()
	canonical, got, err := CanonicalCursorFilter(filter)
	if err != nil {
		t.Fatalf("canonical DirectNotice filter: %v", err)
	}
	want := sha256.Sum256([]byte(
		`{"carrier_class":"direct_notice_v1","mailbox_kind":"personal",` +
			`"schema":"sessions.direct_notice_cursor_filter.v1"}`,
	))
	fixed, err := directNoticeCursorFilterHash()
	if err != nil {
		t.Fatalf("fixed DirectNotice hash: %v", err)
	}
	if !reflect.DeepEqual(canonical, filter) || fixed != want || !bytes.Equal(got, want[:]) {
		t.Fatalf("fixed DirectNotice filter mismatch: canonical=%#v hash=%x want=%x", canonical, got, want)
	}
	mutated := filter
	mutated.ChannelIDs = []model.ID{model.NewID()}
	if _, _, err := CanonicalCursorFilter(mutated); err == nil {
		t.Fatal("DirectNotice filter accepted an independent mutable term")
	}
}

func TestCursorSparseMailboxScanCrossesWorkspaceSequenceGaps(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	cursor.LastSeenSeq = 10
	dbNow := communicationTestNow.Add(time.Minute)
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, reader, 12, false)
	evidence := communicationStateTestCursorReadEvidence(scope, &message, delivery, principal, dbNow)
	scan := []CursorScanEntry{{
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		DeliveryID: delivery.ID, Sequence: 12, Delivery: &delivery, Message: &message,
		CarrierSet:   communicationStateTestCursorCarrierSet(scope, message, delivery, dbNow),
		ReadEvidence: &evidence,
	}}
	plan, err := PlanInboxCursorAdvance(CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 12, DBNow: dbNow,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
		ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 12, scan, dbNow),
		Scan:              scan,
	})
	if err != nil || plan.Verdict != VerdictClean || plan.EffectiveSeq != 12 ||
		plan.After.LastSeenSeq != 12 || !plan.Changed || len(plan.Create) != 0 {
		t.Fatalf("sparse reader scan did not cross workspace-only gap: %#v, %v", plan, err)
	}
}

func TestCursorOCCAndTrueNoOpPreserveVersion(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	input := CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter,
		RequestedSeq: cursor.LastSeenSeq, DBNow: dbNow,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
		ScanWitness: communicationStateTestCursorScanWitness(
			t, cursor, cursor.LastSeenSeq, nil, dbNow,
		),
	}
	plan, err := PlanInboxCursorAdvance(input)
	if err != nil || plan.Verdict != VerdictClean || plan.Changed || plan.CreateCursor ||
		plan.Code != "cursor_unchanged" || !reflect.DeepEqual(plan.Before, cursor) ||
		!reflect.DeepEqual(plan.After, cursor) {
		t.Fatalf("true no-op changed cursor ETag/state: %#v, %v", plan, err)
	}
	input.ExpectedVersion++
	if _, err := PlanInboxCursorAdvance(input); !errors.Is(err, ErrInvalidCommunicationTransition) {
		t.Fatalf("stale cursor OCC = %v, want version mismatch transition", err)
	}
	wantFilterHash := append([]byte(nil), cursor.FilterHash...)
	input.Cursor.FilterHash[0] ^= 0xff
	if !bytes.Equal(plan.Before.FilterHash, wantFilterHash) ||
		!bytes.Equal(plan.After.FilterHash, wantFilterHash) {
		t.Fatal("cursor plan aliases the caller's FilterHash")
	}
	plan.Before.FilterHash[1] ^= 0xff
	if !bytes.Equal(plan.After.FilterHash, wantFilterHash) {
		t.Fatal("cursor plan Before and After alias the same FilterHash")
	}
}

func TestInitialInboxCursorAdvanceCreatesV1FromExactAbsence(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	_, filterHash, err := CanonicalCursorFilter(filter)
	if err != nil {
		t.Fatalf("cursor filter: %v", err)
	}
	dbNow := communicationTestNow.Add(time.Minute)
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, reader, 12, false)
	evidence := communicationStateTestCursorReadEvidence(scope, &message, delivery, principal, dbNow)
	scan := []CursorScanEntry{{
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		DeliveryID: delivery.ID, Sequence: 12, Delivery: &delivery, Message: &message,
		CarrierSet:   communicationStateTestCursorCarrierSet(scope, message, delivery, dbNow),
		ReadEvidence: &evidence,
	}}
	virtual := communicationStateTestCursor(t, scope, reader, filter)
	virtual.LastSeenSeq = 0
	virtual.FilterHash = append([]byte(nil), filterHash...)
	input := InitialInboxCursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		CursorID: model.NewID(), Reader: reader, MailboxKind: MailboxPersonal, MailboxRef: reader.Ref,
		ExpectedVersion: 0, Filter: filter, RequestedSeq: 12, DBNow: dbNow,
		Absence: InboxCursorAbsenceWitness{
			Scope: scope, Reader: reader, MailboxKind: MailboxPersonal, MailboxRef: reader.Ref,
			FilterHash: filterHash, ObservedAt: dbNow,
			Evidence: AuthorityEvidence{
				Verdict: VerdictClean, Code: "absent", EvidenceRef: "same_tx_unique_lookup",
			},
		},
		ScanWitness: communicationStateTestCursorScanWitness(t, virtual, 12, scan, dbNow),
		Scan:        scan,
	}
	plan, err := PlanInitialInboxCursorAdvance(input)
	if err != nil || plan.Verdict != VerdictClean || !plan.CreateCursor || !plan.Changed ||
		plan.Code != "cursor_created" || plan.After.ID != input.CursorID || plan.After.Version != 1 ||
		plan.After.CreatedAt != dbNow || plan.After.UpdatedAt != dbNow || plan.After.LastSeenAt != dbNow ||
		plan.After.LastSeenSeq != 12 || !reflect.DeepEqual(plan.Before, InboxCursor{}) {
		t.Fatalf("virtual-v0 cursor plan is not one deterministic v1 create: %#v, %v", plan, err)
	}
	input.Absence.ObservedAt = dbNow.Add(-time.Nanosecond)
	unknown, err := PlanInitialInboxCursorAdvance(input)
	if err != nil || unknown.Verdict != VerdictUnknown || unknown.CreateCursor || unknown.Changed ||
		!reflect.DeepEqual(unknown.After, InboxCursor{}) {
		t.Fatalf("stale absence witness created a cursor: %#v, %v", unknown, err)
	}
	input.ExpectedVersion = 1
	if _, err := PlanInitialInboxCursorAdvance(input); !errors.Is(err, ErrInvalidCommunicationTransition) {
		t.Fatalf("non-v0 initial precondition = %v, want version mismatch transition", err)
	}
}

func TestCursorRequiredClaimsRemainUnsupported(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	sid := "osn_" + model.NewID().String()
	reader := RecipientRef{Kind: RecipientSession, Ref: sid}
	principal := CommunicationPrincipal{
		SessionID: sid, SessionRunRef: model.NewID().String(), SessionFence: 4,
		SessionWorkspaceID: scope.WorkspaceID, PurposeRestricted: true,
	}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	plan, err := PlanInboxCursorAdvance(CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter,
		RequestedSeq: cursor.LastSeenSeq, DBNow: dbNow,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
		ScanWitness: communicationStateTestCursorScanWitness(
			t, cursor, cursor.LastSeenSeq, nil, dbNow,
		),
	})
	if err != nil || plan.Verdict != VerdictUnknown ||
		plan.Code != "direct_notice_claim_authority_unsupported" || plan.Changed ||
		!reflect.DeepEqual(plan.After, cursor) {
		t.Fatalf("unsupported session Claim produced cursor effects: %#v, %v", plan, err)
	}
}

func TestCursorCoreBrokenCreatesBarrierWithoutCarrierObservation(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, reader, 1, false)
	evidence := communicationStateTestCursorReadEvidence(scope, &message, delivery, principal, dbNow)
	core := evidence.Core
	core.Outcome = ReadDeny
	core.Code = "core_denied"
	core.CorePermission = AuthorityEvidence{
		Verdict: VerdictBroken, Code: "core_denied", EvidenceRef: "core_authorizer",
	}
	core.Facts = []store.AuthorizationFactRef{{
		Kind: model.Kind("core.membership"), ID: model.NewID(), Version: 4,
	}}
	scan := []CursorScanEntry{{
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		DeliveryID: delivery.ID, Sequence: 1, Core: &core,
	}}
	plan, err := PlanInboxCursorAdvance(CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 1, DBNow: dbNow,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
		ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 1, scan, dbNow),
		Scan:              scan,
	})
	if err != nil || plan.Verdict != VerdictClean || len(plan.Create) != 1 ||
		plan.Create[0].Cause != BarrierTemporarilyInvisible || plan.EffectiveSeq != 0 ||
		len(plan.Facts) != 2 {
		t.Fatalf("core BROKEN did not conservatively barrier without carrier: %#v, %v", plan, err)
	}
}

func TestCursorScheduledAndBrokenEntriesContributeCompleteFactUnion(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	factA := store.AuthorizationFactRef{
		Kind: model.Kind("core.membership"), ID: model.NewID(), Version: 3,
	}
	factB := store.AuthorizationFactRef{
		Kind: model.Kind("core.user_group_member"), ID: model.NewID(), Version: 5,
	}
	scheduled := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	scheduled.AvailableAt = dbNow.Add(time.Minute)
	scheduledDelivery := communicationStateTestDelivery(scheduled, reader, 1, false)
	scheduledEvidence := communicationStateTestCursorReadEvidence(
		scope, &scheduled, scheduledDelivery, principal, dbNow,
	)
	scheduledEvidence.Core.Facts = []store.AuthorizationFactRef{factA}
	hidden := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	hiddenDelivery := communicationStateTestDelivery(hidden, reader, 3, false)
	hiddenEvidence := communicationStateTestCursorReadEvidence(scope, &hidden, hiddenDelivery, principal, dbNow)
	hiddenEvidence.Core.Facts = []store.AuthorizationFactRef{factB}
	hiddenEvidence.CurrentChannelGrant.Evidence = AuthorityEvidence{
		Verdict: VerdictBroken, Code: "grant_revoked", EvidenceRef: "grant_current",
	}
	scan := []CursorScanEntry{
		{TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			DeliveryID: scheduledDelivery.ID, Sequence: 1, Delivery: &scheduledDelivery, Message: &scheduled,
			CarrierSet:   communicationStateTestCursorCarrierSet(scope, scheduled, scheduledDelivery, dbNow),
			ReadEvidence: &scheduledEvidence},
		{TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			DeliveryID: hiddenDelivery.ID, Sequence: 3, Delivery: &hiddenDelivery, Message: &hidden,
			CarrierSet:   communicationStateTestCursorCarrierSet(scope, hidden, hiddenDelivery, dbNow),
			ReadEvidence: &hiddenEvidence},
	}
	base := CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 3, DBNow: dbNow,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
		ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 3, scan, dbNow),
		Scan:              scan,
	}
	plan, err := PlanInboxCursorAdvance(base)
	if err != nil || plan.Verdict != VerdictClean || len(plan.Create) != 2 || len(plan.Facts) != 3 {
		t.Fatalf("scheduled/BROKEN fact union was incomplete: %#v, %v", plan, err)
	}
	unknownScheduled := append([]CursorScanEntry(nil), scan...)
	unknownScheduledEvidence := scheduledEvidence
	unknownScheduledEvidence.CurrentChannelGrant.Evidence = AuthorityEvidence{
		Verdict: VerdictUnknown, Code: "grant_unavailable", EvidenceRef: "grant_current",
	}
	unknownScheduled[0].ReadEvidence = &unknownScheduledEvidence
	base.Scan = unknownScheduled
	unknown, err := PlanInboxCursorAdvance(base)
	if err != nil || unknown.Verdict != VerdictUnknown || len(unknown.Create) != 0 ||
		!reflect.DeepEqual(unknown.After, cursor) {
		t.Fatalf("scheduled UNKNOWN authority produced effects: %#v, %v", unknown, err)
	}
	conflicting := append([]CursorScanEntry(nil), scan...)
	conflictingEvidence := hiddenEvidence
	conflictingFact := factA
	conflictingFact.Version++
	conflictingEvidence.Core.Facts = []store.AuthorizationFactRef{conflictingFact}
	conflicting[1].ReadEvidence = &conflictingEvidence
	base.Scan = conflicting
	unknown, err = PlanInboxCursorAdvance(base)
	if err != nil || unknown.Verdict != VerdictUnknown || len(unknown.Create) != 0 ||
		!reflect.DeepEqual(unknown.After, cursor) {
		t.Fatalf("conflicting authority versions produced effects: %#v, %v", unknown, err)
	}
}

func TestCursorAuthorityFactUnionIsBoundedBeforeEffects(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, reader, 1, false)
	evidence := communicationStateTestCursorReadEvidence(scope, &message, delivery, principal, dbNow)
	core := evidence.Core
	core.Outcome = ReadDeny
	core.Code = "core_denied"
	core.CorePermission = AuthorityEvidence{
		Verdict: VerdictBroken, Code: "core_denied", EvidenceRef: "core_authorizer",
	}
	core.Facts = make([]store.AuthorizationFactRef, 64)
	for index := range core.Facts {
		core.Facts[index] = store.AuthorizationFactRef{
			Kind: model.Kind("core.membership"), ID: model.NewID(), Version: 1,
		}
	}
	scan := []CursorScanEntry{{
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		DeliveryID: delivery.ID, Sequence: 1, Core: &core,
	}}
	plan, err := PlanInboxCursorAdvance(CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 1, DBNow: dbNow,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
		ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 1, scan, dbNow),
		Scan:              scan,
	})
	if err != nil || plan.Verdict != VerdictUnknown || len(plan.Create) != 0 ||
		!reflect.DeepEqual(plan.After, cursor) {
		t.Fatalf("65-fact union produced cursor effects: %#v, %v", plan, err)
	}
}

func TestCursorUndeliverableRequiresExactCurrentTombstone(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, reader, 1, false)
	tombstone := store.DirectoryTombstoneWitness{
		TombstoneKind: model.UserTombstoneKind, TombstoneID: model.NewID(), TombstoneVersion: 1,
		Principal: store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser, PrincipalRef: model.ID(reader.Ref),
		},
		RetirementEpoch: 7,
	}
	delivery.Version++
	delivery.UpdatedAt = dbNow
	delivery.State = DeliveryUndeliverable
	delivery.RetirementTombstoneKind = tombstone.TombstoneKind
	delivery.RetirementTombstoneID = tombstone.TombstoneID
	delivery.RetirementTombstoneVersion = tombstone.TombstoneVersion
	delivery.RetirementEpoch = tombstone.RetirementEpoch
	delivery.UndeliverableAt = &dbNow
	delivery.UndeliverableCode = "principal_retired"
	if err := ValidateMessageDelivery(delivery); err != nil {
		t.Fatalf("undeliverable fixture: %v", err)
	}
	evidence := communicationStateTestCursorReadEvidence(scope, &message, delivery, principal, dbNow)
	entry := CursorScanEntry{
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		DeliveryID: delivery.ID, Sequence: 1, Delivery: &delivery, Message: &message,
		CarrierSet:   communicationStateTestCursorCarrierSet(scope, message, delivery, dbNow),
		ReadEvidence: &evidence, Tombstone: &tombstone,
	}
	planFor := func(candidate CursorScanEntry) CursorAdvancePlan {
		t.Helper()
		scan := []CursorScanEntry{candidate}
		plan, err := PlanInboxCursorAdvance(CursorAdvanceInput{
			Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
			Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 1, DBNow: dbNow,
			BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
			ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 1, scan, dbNow),
			Scan:              scan,
		})
		if err != nil {
			t.Fatalf("cursor tombstone plan: %v", err)
		}
		return plan
	}
	clean := planFor(entry)
	if clean.Verdict != VerdictClean || clean.EffectiveSeq != 1 || len(clean.Create) != 0 {
		t.Fatalf("exact current tombstone did not cross: %#v", clean)
	}
	entry.Tombstone = nil
	missing := planFor(entry)
	if missing.Verdict != VerdictUnknown || !reflect.DeepEqual(missing.After, cursor) {
		t.Fatalf("missing tombstone crossed terminal Delivery: %#v", missing)
	}
	mismatch := tombstone
	mismatch.TombstoneID = model.NewID()
	entry.Tombstone = &mismatch
	changed := planFor(entry)
	if changed.Verdict != VerdictUnknown || !reflect.DeepEqual(changed.After, cursor) {
		t.Fatalf("mismatched tombstone crossed terminal Delivery: %#v", changed)
	}
	newer := tombstone
	newer.RetirementEpoch = 8
	futureDelivery := delivery
	futureDelivery.RetirementEpoch = newer.RetirementEpoch
	futureEvidence := evidence
	futureEvidence.CarrierState.Delivery = futureDelivery
	futureEntry := entry
	futureEntry.Delivery = &futureDelivery
	futureEntry.ReadEvidence = &futureEvidence
	futureEntry.Tombstone = &newer
	changed = planFor(futureEntry)
	if changed.Verdict != VerdictUnknown || !reflect.DeepEqual(changed.After, cursor) {
		t.Fatalf("future tombstone epoch crossed terminal Delivery: %#v", changed)
	}
}

func TestCursorGenericNoticeIsAnExplicitForeignGap(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	baseMessage := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(baseMessage, reader, 1, false)
	planFor := func(candidateMessage Message, evidence ProtectedReadEvidence) CursorAdvancePlan {
		t.Helper()
		scan := []CursorScanEntry{{
			TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			DeliveryID: delivery.ID, Sequence: 1, Delivery: &delivery, Message: &candidateMessage,
			CarrierSet:   communicationStateTestCursorCarrierSet(scope, candidateMessage, delivery, dbNow),
			ReadEvidence: &evidence,
		}}
		plan, err := PlanInboxCursorAdvance(CursorAdvanceInput{
			Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
			Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 1, DBNow: dbNow,
			BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
			ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 1, scan, dbNow),
			Scan:              scan,
		})
		if err != nil {
			t.Fatalf("generic notice plan: %v", err)
		}
		return plan
	}
	assertForeign := func(name string, candidateMessage Message, evidence ProtectedReadEvidence) {
		t.Helper()
		plan := planFor(candidateMessage, evidence)
		if plan.Verdict != VerdictClean || plan.EffectiveSeq != 1 || len(plan.Create) != 0 {
			t.Fatalf("%s silently matched DirectNotice carrier: %#v", name, plan)
		}
	}

	senderMessage := baseMessage
	senderMessage.Sender = CommunicationActorRef{Kind: ActorAgent, Ref: model.NewID().String()}
	senderEvidence := communicationStateTestCursorReadEvidence(
		scope, &senderMessage, delivery, principal, dbNow,
	)
	if decision, err := EvaluateProtectedRead(senderEvidence); err != nil || decision.Verdict != VerdictClean {
		t.Fatalf("integral non-User graph authority: %#v, %v", decision, err)
	}
	assertForeign("non-User sender", senderMessage, senderEvidence)
	directMessage := baseMessage
	directEvidence := communicationStateTestCursorReadEvidence(
		scope, &directMessage, delivery, principal, dbNow,
	)
	mixedSnapshot := planFor(
		senderMessage,
		directEvidence,
	)
	if mixedSnapshot.Verdict != VerdictUnknown || !reflect.DeepEqual(mixedSnapshot.After, cursor) {
		t.Fatalf("mixed carrier snapshots produced cursor effects: %#v", mixedSnapshot)
	}

	workspaceMessage := baseMessage
	workspaceEvidence := communicationStateTestCursorWorkspaceEvidence(
		t, scope, &workspaceMessage, delivery, principal, dbNow,
	)
	if decision, err := EvaluateProtectedRead(workspaceEvidence); err != nil || decision.Verdict != VerdictClean {
		t.Fatalf("integral workspace graph authority: %#v, %v", decision, err)
	}
	assertForeign("non-direct causal graph", workspaceMessage, workspaceEvidence)

	unknownMessage := baseMessage
	unknownEvidence := communicationStateTestCursorReadEvidence(
		scope, &unknownMessage, delivery, principal, dbNow,
	)
	unknownEvidence.CurrentAudience.SetWitness.Contributions[0].ObservedSessionSID =
		"osn_" + model.NewID().String()
	unknownEvidence.CurrentAudience.SetWitness.Contributions[0].ObservedClaimFence = 2
	unknownEvidence.CurrentAudience.Contributions[0].Contribution.ObservedSessionSID =
		unknownEvidence.CurrentAudience.SetWitness.Contributions[0].ObservedSessionSID
	unknownEvidence.CurrentAudience.Contributions[0].Contribution.ObservedClaimFence = 2
	unknown := planFor(unknownMessage, unknownEvidence)
	if unknown.Verdict != VerdictUnknown || !reflect.DeepEqual(unknown.After, cursor) {
		t.Fatalf("UNKNOWN authority became a foreign gap: %#v", unknown)
	}
}

func TestCursorGenericCarrierConstituentUnknownDoesNotBecomeForeign(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	planFor := func(audienceVerdict AssessmentVerdict) (CursorAdvancePlan, ProtectedReadDecision) {
		t.Helper()
		message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
		message.Sender = CommunicationActorRef{Kind: ActorAgent, Ref: model.NewID().String()}
		delivery := communicationStateTestDelivery(message, reader, 1, false)
		evidence := communicationStateTestCursorReadEvidence(scope, &message, delivery, principal, dbNow)
		audienceCode := "audience_unknown"
		if audienceVerdict == VerdictBroken {
			audienceCode = "audience_broken"
		}
		evidence.CurrentAudience.Contributions[0].Witness.Evidence = AuthorityEvidence{
			Verdict: audienceVerdict, Code: audienceCode,
			EvidenceRef: "audience-current",
		}
		evidence.CurrentChannelGrant.Evidence = AuthorityEvidence{
			Verdict: VerdictBroken, Code: "grant_revoked", EvidenceRef: "grant-revoked",
		}
		decision, err := EvaluateProtectedRead(evidence)
		if err != nil || decision.Verdict != VerdictBroken {
			t.Fatalf("BROKEN grant did not dominate audience constituent: %#v, %v", decision, err)
		}
		scan := []CursorScanEntry{{
			TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			DeliveryID: delivery.ID, Sequence: 1, Delivery: &delivery, Message: &message,
			CarrierSet:   communicationStateTestCursorCarrierSet(scope, message, delivery, dbNow),
			ReadEvidence: &evidence,
		}}
		plan, err := PlanInboxCursorAdvance(CursorAdvanceInput{
			Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
			Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 1, DBNow: dbNow,
			BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
			ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 1, scan, dbNow),
			Scan:              scan,
		})
		if err != nil {
			t.Fatalf("generic constituent cursor plan: %v", err)
		}
		return plan, decision
	}
	hasUnknownCheck := func(decision ProtectedReadDecision) bool {
		for _, check := range decision.Checks {
			if evidenceVerdict(check.Evidence) == VerdictUnknown {
				return true
			}
		}
		return false
	}
	unknown, unknownDecision := planFor(VerdictUnknown)
	if !hasUnknownCheck(unknownDecision) || unknown.Verdict != VerdictUnknown || unknown.Changed ||
		unknown.EffectiveSeq != 0 || len(unknown.Create) != 0 ||
		!reflect.DeepEqual(unknown.Before, cursor) || !reflect.DeepEqual(unknown.After, cursor) {
		t.Fatalf("masked constituent UNKNOWN produced cursor effects: %#v", unknown)
	}
	broken, brokenDecision := planFor(VerdictBroken)
	if hasUnknownCheck(brokenDecision) || broken.Verdict != VerdictClean || broken.EffectiveSeq != 1 ||
		len(broken.Create) != 0 {
		t.Fatalf("fully BROKEN generic control did not cross as foreign: %#v", broken)
	}
}

func TestCursorDirectNoticeCarrierClassIsExact(t *testing.T) {
	t.Parallel()

	type carrierCase struct {
		name                 string
		policy               AckPolicy
		requiredCount        int64
		required             bool
		carrierDeliveryCount int64
		mutateCarrier        func(*Message, *MessageDelivery)
		mutateAudience       func(*testing.T, *Message, *ProtectedReadEvidence)
		wantBarrier          bool
		wantUnknown          bool
	}
	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	cases := []carrierCase{
		{name: "exact direct notice", policy: AckPolicyNone, wantBarrier: true},
		{name: "work item", policy: AckPolicyNone, mutateCarrier: func(message *Message, _ *MessageDelivery) {
			message.WorkItemID = model.NewID()
			message.LastEventSeq = 0
		}},
		{name: "reply", policy: AckPolicyNone, mutateCarrier: func(message *Message, _ *MessageDelivery) {
			message.ReplyToID = model.NewID()
		}},
		{name: "supersedes", policy: AckPolicyNone, mutateCarrier: func(message *Message, _ *MessageDelivery) {
			message.SupersedesID = model.NewID()
		}},
		{name: "origin", policy: AckPolicyNone, mutateCarrier: func(message *Message, _ *MessageDelivery) {
			message.OriginEventID = model.NewID()
		}},
		{name: "automation depth", policy: AckPolicyNone, mutateCarrier: func(message *Message, _ *MessageDelivery) {
			message.AutomationDepth = 1
		}},
		{name: "labels", policy: AckPolicyNone, mutateCarrier: func(message *Message, _ *MessageDelivery) {
			message.LabelsJSON = json.RawMessage(`{"tier":"critical"}`)
			digest := sha256.Sum256(message.LabelsJSON)
			message.LabelsHash = digest[:]
		}},
		{name: "expiry", policy: AckPolicyNone, mutateCarrier: func(message *Message, delivery *MessageDelivery) {
			expiresAt := dbNow.Add(time.Hour)
			message.ExpiresAt = &expiresAt
			delivery.ExpiresAt = &expiresAt
		}},
		{name: "sealed payload", policy: AckPolicyNone, mutateCarrier: func(message *Message, _ *MessageDelivery) {
			message.Payload = communicationStateTestSealedPayload(PayloadSlotMessage, 3)
		}},
		{name: "multiple route reasons", policy: AckPolicyNone,
			mutateCarrier: func(_ *Message, delivery *MessageDelivery) {
				delivery.RouteReasons = []RouteReason{"direct", "workspace"}
			}},
		{name: "incomplete two-delivery witness", policy: AckPolicyNone,
			carrierDeliveryCount: 2, wantUnknown: true},
		{name: "required count exceeds complete set", policy: AckPolicyEachRequired,
			requiredCount: 1, required: true, wantUnknown: true,
			mutateAudience: func(_ *testing.T, _ *Message, evidence *ProtectedReadEvidence) {
				evidence.CarrierState.RequiredDeliveryCount = 2
			}},
		{name: "optional delivery claims required count", policy: AckPolicyEachRequired,
			requiredCount: 1, required: false, wantUnknown: true,
			mutateAudience: func(_ *testing.T, _ *Message, evidence *ProtectedReadEvidence) {
				evidence.CarrierState.RequiredDeliveryCount = 1
			}},
		{name: "selector required differs", policy: AckPolicyEachRequired,
			requiredCount: 1, required: true,
			mutateAudience: func(t *testing.T, message *Message, evidence *ProtectedReadEvidence) {
				communicationStateTestCursorRebindAudience(t, message, evidence,
					func(audience *MessageAudience, row *MessageAudienceRecipient) {
						audience.Selector.Required = false
						row.Selector.Required = false
					})
			}},
		{name: "contribution required differs", policy: AckPolicyEachRequired,
			requiredCount: 1, required: true, wantUnknown: true,
			mutateAudience: func(t *testing.T, message *Message, evidence *ProtectedReadEvidence) {
				communicationStateTestCursorRebindAudience(t, message, evidence,
					func(_ *MessageAudience, row *MessageAudienceRecipient) {
						row.Required = false
					})
			}},
		{name: "selector wake differs", policy: AckPolicyNone,
			mutateCarrier: func(_ *Message, delivery *MessageDelivery) {
				delivery.WakePolicy = WakePrimary
			},
			mutateAudience: func(t *testing.T, message *Message, evidence *ProtectedReadEvidence) {
				communicationStateTestCursorRebindAudience(t, message, evidence,
					func(audience *MessageAudience, row *MessageAudienceRecipient) {
						audience.Selector.WakePolicy = WakeNone
						row.Selector.WakePolicy = WakeNone
					})
			}},
		{name: "contribution wake differs", policy: AckPolicyNone,
			wantUnknown: true,
			mutateCarrier: func(_ *Message, delivery *MessageDelivery) {
				delivery.WakePolicy = WakePrimary
			},
			mutateAudience: func(t *testing.T, message *Message, evidence *ProtectedReadEvidence) {
				communicationStateTestCursorRebindAudience(t, message, evidence,
					func(_ *MessageAudience, row *MessageAudienceRecipient) {
						row.WakePolicy = WakeNone
					})
			}},
		{name: "contribution route differs", policy: AckPolicyNone,
			wantUnknown: true,
			mutateAudience: func(t *testing.T, message *Message, evidence *ProtectedReadEvidence) {
				communicationStateTestCursorRebindAudience(t, message, evidence,
					func(_ *MessageAudience, row *MessageAudienceRecipient) {
						row.RouteReasons = []RouteReason{"direct", "workspace"}
					})
			}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			message := communicationStateTestMessage(
				t, scope, test.policy, test.requiredCount, communicationTestNow,
			)
			delivery := communicationStateTestDelivery(message, reader, 1, test.required)
			if test.mutateCarrier != nil {
				test.mutateCarrier(&message, &delivery)
			}
			evidence := communicationStateTestCursorReadEvidence(
				scope, &message, delivery, principal, dbNow,
			)
			if test.mutateAudience != nil {
				test.mutateAudience(t, &message, &evidence)
			}
			if decision, err := EvaluateProtectedRead(evidence); err != nil || decision.Verdict != VerdictClean {
				t.Fatalf("integral carrier graph is not CLEAN: %#v, %v", decision, err)
			}
			evidence.CurrentChannelGrant.Evidence = AuthorityEvidence{
				Verdict: VerdictBroken, Code: "grant_revoked", EvidenceRef: "grant-revoked",
			}
			carrierSet := communicationStateTestCursorCarrierSet(scope, message, delivery, dbNow)
			if test.carrierDeliveryCount != 0 {
				carrierSet.DeliveryCount = test.carrierDeliveryCount
			}
			scan := []CursorScanEntry{{
				TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
				DeliveryID: delivery.ID, Sequence: 1, Delivery: &delivery, Message: &message,
				CarrierSet: carrierSet, ReadEvidence: &evidence,
			}}
			plan, err := PlanInboxCursorAdvance(CursorAdvanceInput{
				Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
				Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter,
				RequestedSeq: 1, DBNow: dbNow,
				BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
				ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 1, scan, dbNow),
				Scan:              scan,
			})
			if err != nil {
				t.Fatalf("cursor carrier classification: %#v, %v", plan, err)
			}
			if test.wantUnknown {
				if plan.Verdict != VerdictUnknown || plan.Changed || plan.EffectiveSeq != 0 ||
					len(plan.Create) != 0 || !reflect.DeepEqual(plan.Before, cursor) ||
					!reflect.DeepEqual(plan.After, cursor) {
					t.Fatalf("incomplete carrier witness produced cursor effects: %#v", plan)
				}
				return
			}
			if plan.Verdict != VerdictClean {
				t.Fatalf("integral carrier did not classify cleanly: %#v", plan)
			}
			if test.wantBarrier {
				if plan.EffectiveSeq != 0 || len(plan.Create) != 1 ||
					plan.Create[0].Cause != BarrierTemporarilyInvisible {
					t.Fatalf("exact DirectNotice did not retain BROKEN visibility barrier: %#v", plan)
				}
				return
			}
			if plan.EffectiveSeq != 1 || len(plan.Create) != 0 {
				t.Fatalf("generic carrier silently matched DirectNotice: %#v", plan)
			}
		})
	}
}

func TestCursorAudienceContributionIDsAreGloballyUnique(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	planFor := func(duplicateContributionID bool) (CursorAdvancePlan, bool) {
		t.Helper()
		message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
		delivery := communicationStateTestDelivery(message, reader, 1, false)
		evidence := communicationStateTestCursorReadEvidence(scope, &message, delivery, principal, dbNow)
		target := evidence.CurrentAudience.Contributions[0]

		otherRecipient := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
		otherDelivery := communicationStateTestDelivery(message, otherRecipient, 2, false)
		otherRow := communicationStateTestDirectContribution(scope, otherDelivery)
		if duplicateContributionID {
			otherRow.ID = target.Contribution.ID
		}
		otherRow = communicationStateTestSealCausalArc(otherRow)
		otherAudience := target.Audience
		otherAudience.ID = otherRow.MessageAudienceID
		otherAudience.Ordinal = 2
		otherAudience.Selector = otherRow.Selector
		selectorRaw, err := canonicalJSON(otherAudience.Selector)
		if err != nil {
			t.Fatalf("second cursor selector: %v", err)
		}
		selectorHash := sha256.Sum256(selectorRaw)
		otherAudience.SelectorHash = selectorHash[:]
		otherAudience.ResolvedHash, err = canonicalResolvedAudienceHash(
			otherAudience, []MessageAudienceRecipient{otherRow},
		)
		if err != nil {
			t.Fatalf("second cursor audience: %v", err)
		}
		audiences := []MessageAudience{target.Audience, otherAudience}
		rows := []MessageAudienceRecipient{target.Contribution, otherRow}
		message.AudienceHash, err = CanonicalMessageAudienceHash(message, audiences, rows)
		if err != nil {
			t.Fatalf("two-delivery audience hash: %v", err)
		}
		setDigest, err := CanonicalCurrentAudienceSetDigest(
			scope, message.ID, message.Version, audiences, rows,
		)
		if err != nil {
			t.Fatalf("two-delivery set digest: %v", err)
		}
		evidence.CurrentAudience.SetWitness.MessageAudienceHash = append(
			[]byte(nil), message.AudienceHash...,
		)
		evidence.CurrentAudience.SetWitness.AudienceCount = 2
		evidence.CurrentAudience.SetWitness.ContributionCount = 2
		evidence.CurrentAudience.SetWitness.Audiences = audiences
		evidence.CurrentAudience.SetWitness.Contributions = rows
		evidence.CurrentAudience.SetWitness.SetDigest = setDigest
		evidence.CarrierState.Message = message
		setValid := validateCurrentAudienceSetWitness(evidence.CurrentAudience)
		evidence.CurrentChannelGrant.Evidence = AuthorityEvidence{
			Verdict: VerdictBroken, Code: "grant_revoked", EvidenceRef: "grant-revoked",
		}
		if decision, err := EvaluateProtectedRead(evidence); err != nil || decision.Verdict != VerdictBroken {
			t.Fatalf("BROKEN grant did not dominate set classification: %#v, %v", decision, err)
		}
		carrierSet := communicationStateTestCursorCarrierSet(scope, message, delivery, dbNow)
		carrierSet.DeliveryCount = 2
		scan := []CursorScanEntry{{
			TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			DeliveryID: delivery.ID, Sequence: 1, Delivery: &delivery, Message: &message,
			CarrierSet: carrierSet, ReadEvidence: &evidence,
		}}
		plan, err := PlanInboxCursorAdvance(CursorAdvanceInput{
			Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
			Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 1, DBNow: dbNow,
			BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
			ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 1, scan, dbNow),
			Scan:              scan,
		})
		if err != nil {
			t.Fatalf("two-delivery cursor plan: %v", err)
		}
		return plan, setValid
	}
	control, controlSetValid := planFor(false)
	if !controlSetValid || control.Verdict != VerdictClean || control.EffectiveSeq != 1 ||
		len(control.Create) != 0 {
		t.Fatalf("globally unique two-delivery control did not cross as foreign: %#v", control)
	}
	duplicate, duplicateSetValid := planFor(true)
	if duplicateSetValid || duplicate.Verdict != VerdictUnknown || duplicate.Changed ||
		duplicate.EffectiveSeq != 0 || len(duplicate.Create) != 0 ||
		!reflect.DeepEqual(duplicate.Before, cursor) || !reflect.DeepEqual(duplicate.After, cursor) {
		t.Fatalf("duplicate global contribution ID produced cursor effects: %#v", duplicate)
	}
}

func TestCursorAudienceHashSnapshotIsExact(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, reader, 1, false)
	evidence := communicationStateTestCursorReadEvidence(scope, &message, delivery, principal, dbNow)
	message.AudienceHash = append([]byte(nil), message.AudienceHash...)
	message.AudienceHash[0] ^= 0xff
	scan := []CursorScanEntry{{
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		DeliveryID: delivery.ID, Sequence: 1, Delivery: &delivery, Message: &message,
		CarrierSet:   communicationStateTestCursorCarrierSet(scope, message, delivery, dbNow),
		ReadEvidence: &evidence,
	}}
	plan, err := PlanInboxCursorAdvance(CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 1, DBNow: dbNow,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
		ScanWitness:       communicationStateTestCursorScanWitness(t, cursor, 1, scan, dbNow),
		Scan:              scan,
	})
	if err != nil || plan.Verdict != VerdictUnknown || !reflect.DeepEqual(plan.After, cursor) {
		t.Fatalf("mismatched durable AudienceHash produced cursor effects: %#v, %v", plan, err)
	}
}

func TestCursorScanWitnessRejectsTruncationOrderTargetAndBound(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reader := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(reader.Ref)}
	filter := communicationStateTestDirectNoticeCursorFilter()
	cursor := communicationStateTestCursor(t, scope, reader, filter)
	dbNow := communicationTestNow.Add(time.Minute)
	makeEntry := func(sequence int64) CursorScanEntry {
		message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
		delivery := communicationStateTestDelivery(message, reader, sequence, false)
		evidence := communicationStateTestCursorReadEvidence(scope, &message, delivery, principal, dbNow)
		return CursorScanEntry{
			TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			DeliveryID: delivery.ID, Sequence: sequence, Delivery: &delivery, Message: &message,
			CarrierSet:   communicationStateTestCursorCarrierSet(scope, message, delivery, dbNow),
			ReadEvidence: &evidence,
		}
	}
	ordered := []CursorScanEntry{makeEntry(1), makeEntry(2)}
	witness := communicationStateTestCursorScanWitness(t, cursor, 2, ordered, dbNow)
	base := CursorAdvanceInput{
		Scope: scope, Principal: communicationStateTestPrincipalResolution(scope, principal, reader),
		Cursor: cursor, ExpectedVersion: cursor.Version, Filter: filter, RequestedSeq: 2, DBNow: dbNow,
		BarrierSetWitness: communicationStateTestBarrierSetWitness(t, scope, cursor, nil, dbNow),
		ScanWitness:       witness, Scan: ordered,
	}
	assertUnknown := func(name string, input CursorAdvanceInput) {
		t.Helper()
		plan, err := PlanInboxCursorAdvance(input)
		if err != nil || plan.Verdict != VerdictUnknown || !reflect.DeepEqual(plan.After, cursor) {
			t.Fatalf("%s scan shape was not UNKNOWN: %#v, %v", name, plan, err)
		}
	}
	truncated := base
	truncated.ScanWitness.HasMore = true
	assertUnknown("truncated", truncated)
	wrongTarget := base
	wrongTarget.ScanWitness.TargetDeliveryID = model.NewID()
	assertUnknown("wrong target", wrongTarget)
	outOfOrder := base
	outOfOrder.Scan = []CursorScanEntry{ordered[1], ordered[0]}
	assertUnknown("out of order", outOfOrder)
	tooManyEntries := make([]CursorScanEntry, maxCursorMailboxScanEntries+1)
	for index := range tooManyEntries {
		tooManyEntries[index] = CursorScanEntry{
			TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			DeliveryID: model.NewID(), Sequence: int64(index + 1),
		}
	}
	overBound := base
	overBound.RequestedSeq = int64(len(tooManyEntries))
	overBound.Scan = tooManyEntries
	overBound.ScanWitness.ToInclusive = overBound.RequestedSeq
	overBound.ScanWitness.TargetDeliveryID = tooManyEntries[len(tooManyEntries)-1].DeliveryID
	overBound.ScanWitness.EntryCount = int64(len(tooManyEntries))
	overBound.ScanWitness.Digest = bytes.Repeat([]byte{0x7a}, sha256.Size)
	assertUnknown("over bound", overBound)
}

func TestProtectedPayloadRequiresAllGates(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	recipient := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	principal := CommunicationPrincipal{UserID: model.ID(recipient.Ref)}
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, recipient, 1, false)
	base := communicationStateTestReadEvidence(scope, message, delivery, principal, communicationTestNow)
	decision, err := EvaluateProtectedRead(base)
	if err != nil || decision.Verdict != VerdictClean {
		t.Fatalf("all current gates did not authorize: %#v, %v", decision, err)
	}

	mutations := map[string]func(*ProtectedReadEvidence){
		"core": func(e *ProtectedReadEvidence) {
			e.Core.CorePermission.Verdict = VerdictBroken
			e.Core.Outcome = ReadDeny
		},
		"grant": func(e *ProtectedReadEvidence) { e.CurrentChannelGrant.Evidence.Verdict = VerdictBroken },
		"guard": func(e *ProtectedReadEvidence) { e.EntityRecipientGuard.Evidence.Verdict = VerdictBroken },
		"causality": func(e *ProtectedReadEvidence) {
			e.CurrentAudience.Contributions[0].Witness.Evidence.Verdict = VerdictBroken
		},
		"forbid": func(e *ProtectedReadEvidence) {
			e.Core.ForbidAbsence.Verdict = VerdictBroken
			e.Core.Outcome = ReadDeny
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			input := base
			input.CurrentAudience.Contributions = append([]CausalContributionEvidence(nil),
				base.CurrentAudience.Contributions...)
			mutate(&input)
			got, err := EvaluateProtectedRead(input)
			if err != nil {
				t.Fatalf("gate evaluation: %v", err)
			}
			if got.Verdict != VerdictBroken {
				t.Fatalf("missing %s gate authorized: %#v", name, got)
			}
		})
	}

	crossChannel := base
	crossChannel.ChannelID = model.NewID()
	if _, err := EvaluateProtectedRead(crossChannel); err == nil {
		t.Fatal("ChannelGrant evidence replayed across channels")
	}
	crossDelivery := base
	crossDelivery.CurrentAudience.DeliveryID = model.NewID()
	if _, err := EvaluateProtectedRead(crossDelivery); err == nil {
		t.Fatal("audience evidence replayed across Deliveries")
	}
}

func TestAudienceCausalityRequiresCurrentRecipient(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	recipient := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, recipient, 1, false)
	principal := CommunicationPrincipal{UserID: model.ID(recipient.Ref)}
	evidence := communicationStateTestReadEvidence(
		scope, message, delivery, principal, communicationTestNow).CurrentAudience
	evidence.RecipientEligible.Evidence = AuthorityEvidence{
		Verdict: VerdictBroken, Code: "recipient_inactive", EvidenceRef: "recipient_inactive",
	}
	if got := EvaluateCurrentAudience(evidence); got.Verdict != VerdictBroken {
		t.Fatalf("inactive recipient retained causal access: %#v", got)
	}
	copyFromOther := evidence
	copyFromOther.RecipientEligible.Evidence = AuthorityEvidence{
		Verdict: VerdictClean, Code: "current", EvidenceRef: "current",
	}
	copyFromOther.Contributions = append([]CausalContributionEvidence(nil), evidence.Contributions...)
	copyFromOther.Contributions[0].Witness.ContributionID = model.NewID()
	if got := EvaluateCurrentAudience(copyFromOther); got.Verdict != VerdictUnknown {
		t.Fatalf("unbound causal witness was reused: %#v", got)
	}
}

func TestCommunicationSessionGrantBitsByRoute(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	principal := CommunicationPrincipal{UserID: model.NewID()}
	channelID := model.NewID()
	clean := AuthorityEvidence{Verdict: VerdictClean, Code: "current", EvidenceRef: "current"}
	entity := EntityRef{
		TenantID: scope.TenantID, Kind: model.Kind("sessions.channel"),
		ID: channelID, WorkspaceID: scope.WorkspaceID,
	}
	input := SendGateEvidence{
		Scope: scope, ChannelID: channelID, ChannelACLRevision: 1,
		DBNow: communicationTestNow, Principal: principal,
		Core: ReadWitness{
			Outcome: ReadAllow, Code: "send_allowed", Entity: entity,
			Operation: CommunicationMessageSend, Principal: principal, ObservedAt: communicationTestNow,
			FreshUntil:     communicationTestNow.Add(10 * time.Minute),
			CorePermission: clean, ResourceGuard: clean, ForbidAbsence: clean,
		},
		DirectoryEpoch: store.AuthorizationFactRef{
			Kind: model.DirectoryEpochKind, ID: model.ID(scope.TenantID), Version: 7,
		},
		CurrentChannelWriteGrant: BoundChannelReadEvidence{
			TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, ChannelID: channelID,
			Principal: principal, Bit: ChannelGrantWrite, GrantID: model.NewID(), GrantVersion: 1,
			DirectoryEpoch:     7,
			ChannelACLRevision: 1, EvaluatedAt: communicationTestNow, Evidence: clean,
		},
	}
	input.Core.Facts = []store.AuthorizationFactRef{input.DirectoryEpoch}
	decision, err := EvaluateSendGate(input)
	if err != nil || decision.Verdict != VerdictClean {
		t.Fatalf("write-specific send denied: %#v, %v", decision, err)
	}
	if len(decision.Facts) != 1 || decision.Facts[0] != input.DirectoryEpoch {
		t.Fatalf("send gate did not deduplicate its exact directory witness: %#v", decision.Facts)
	}
	disagreedEpoch := input
	disagreedEpoch.Core.Facts = append([]store.AuthorizationFactRef(nil), input.Core.Facts...)
	disagreedEpoch.Core.Facts[0].Version++
	if _, err := EvaluateSendGate(disagreedEpoch); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("disagreed directory witnesses = %v, want UNKNOWN", err)
	}
	readOnly := input
	readOnly.CurrentChannelWriteGrant.Bit = ChannelGrantRead
	if _, err := EvaluateSendGate(readOnly); err == nil {
		t.Fatal("read-only ChannelGrant authorized send")
	}
	alias := input
	alias.Core.Operation = CommunicationOperation("sessions:message:send")
	if _, err := EvaluateSendGate(alias); err == nil {
		t.Fatal("invented message-send permission alias authorized")
	}
	crossChannel := input
	crossChannel.Core.Entity.ID = model.NewID()
	if _, err := EvaluateSendGate(crossChannel); err == nil {
		t.Fatal("write authority replayed across channels")
	}
}

func TestCommunicationSessionReadWriteBitsStayIndependent(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	sid := "osn_" + model.NewID().String()
	principal := CommunicationPrincipal{
		SessionID: sid, SessionRunRef: model.NewID().String(), SessionFence: 11,
		SessionWorkspaceID: scope.WorkspaceID, PurposeRestricted: true,
	}
	channelID := model.NewID()
	dbNow := communicationTestNow.Add(time.Minute)
	closure := ChannelGrantSubjectClosure{
		Scope: scope, Principal: principal, DirectoryEpoch: 7, Outcome: ReadAllow, Code: "resolved",
		Subjects:   []CommunicationSubjectRef{{Kind: SubjectSession, Ref: sid}},
		ObservedAt: dbNow, FreshUntil: dbNow.Add(time.Minute), EvidenceRef: "session-subjects",
	}
	makeGrant := func(canRead, canWrite bool) ChannelGrant {
		return ChannelGrant{
			MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
			ChannelID:                  channelID, Subject: CommunicationSubjectRef{Kind: SubjectSession, Ref: sid},
			Generation: 1, CanRead: canRead, CanWrite: canWrite, State: ChannelGrantActive,
			GrantedBy: CommunicationActorRef{Kind: ActorSystem, Ref: "test-authority"},
		}
	}
	snapshot := func(grant ChannelGrant) ChannelGrantSnapshot {
		return ChannelGrantSnapshot{
			Verdict: VerdictClean, Code: "complete", ACLRevision: 1, ObservedAt: dbNow,
			Grants: []ChannelGrant{grant},
		}
	}
	writeOnly := makeGrant(false, true)
	writeEvidence := EvaluateCurrentChannelGrant(
		snapshot(writeOnly), scope.TenantID, scope.WorkspaceID, channelID,
		closure, ChannelGrantWrite, dbNow,
	)
	readDenied := EvaluateCurrentChannelGrant(
		snapshot(writeOnly), scope.TenantID, scope.WorkspaceID, channelID,
		closure, ChannelGrantRead, dbNow,
	)
	if evidenceVerdict(writeEvidence.Evidence) != VerdictClean ||
		evidenceVerdict(readDenied.Evidence) != VerdictBroken {
		t.Fatalf("write-only session grant collapsed bits: write=%#v read=%#v", writeEvidence, readDenied)
	}
	clean := AuthorityEvidence{Verdict: VerdictClean, Code: "current", EvidenceRef: "core-current"}
	send := SendGateEvidence{
		Scope: scope, ChannelID: channelID, ChannelACLRevision: 1, DBNow: dbNow, Principal: principal,
		Core: ReadWitness{
			Outcome: ReadAllow, Code: "send_allowed",
			Entity: EntityRef{TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
				Kind: model.Kind("sessions.channel"), ID: channelID},
			Operation: CommunicationMessageSend, Principal: principal, ObservedAt: dbNow,
			FreshUntil: dbNow.Add(time.Minute), CorePermission: clean, ResourceGuard: clean, ForbidAbsence: clean,
		},
		DirectoryEpoch: store.AuthorizationFactRef{
			Kind: model.DirectoryEpochKind, ID: model.ID(scope.TenantID), Version: 7,
		},
		CurrentChannelWriteGrant: writeEvidence,
	}
	sendDecision, err := EvaluateSendGate(send)
	if err != nil || sendDecision.Verdict != VerdictClean ||
		!reflect.DeepEqual(sendDecision.RequiredClaims, []CommunicationClaimRef{{SessionSID: sid, Fence: 11}}) {
		t.Fatalf("write-only session could not send with exact Claim: %#v, %v", sendDecision, err)
	}

	readOnly := makeGrant(true, false)
	readEvidence := EvaluateCurrentChannelGrant(
		snapshot(readOnly), scope.TenantID, scope.WorkspaceID, channelID,
		closure, ChannelGrantRead, dbNow,
	)
	writeDenied := EvaluateCurrentChannelGrant(
		snapshot(readOnly), scope.TenantID, scope.WorkspaceID, channelID,
		closure, ChannelGrantWrite, dbNow,
	)
	if evidenceVerdict(readEvidence.Evidence) != VerdictClean ||
		evidenceVerdict(writeDenied.Evidence) != VerdictBroken {
		t.Fatalf("read-only session grant collapsed bits: read=%#v write=%#v", readEvidence, writeDenied)
	}
	send.CurrentChannelWriteGrant = writeDenied
	deniedSend, err := EvaluateSendGate(send)
	if err != nil || deniedSend.Verdict != VerdictBroken {
		t.Fatalf("read-only session unexpectedly sent: %#v, %v", deniedSend, err)
	}
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	message.ChannelID = channelID
	recipient := RecipientRef{Kind: RecipientSession, Ref: sid}
	delivery := communicationStateTestDelivery(message, recipient, 1, false)
	carrier := communicationStateTestReadEvidence(scope, message, delivery, principal, dbNow)
	carrier.CurrentChannelGrant = readEvidence
	decision, err := EvaluateReadGate(carrier)
	if err != nil || decision.Verdict != VerdictClean || len(decision.RequiredClaims) != 1 ||
		decision.RequiredClaims[0] != (CommunicationClaimRef{SessionSID: sid, Fence: 11}) {
		t.Fatalf("read-only session could not read its carrier: %#v, %v", decision, err)
	}
	admin := carrier
	admin.Operation = CommunicationDeliveryAdmin
	admin.Core.Operation = CommunicationDeliveryAdmin
	if _, err := EvaluateCarrierGate(admin); !errors.Is(err, ErrCommunicationForbidden) {
		t.Fatalf("communication-session admin ceiling error = %v", err)
	}
}

func communicationStateTestDispatch(
	t *testing.T,
	scope DirectoryScopeRef,
	route DispatchRouteIdentity,
	at time.Time,
) DeliveryDispatch {
	t.Helper()
	key := sha256.Sum256([]byte("dispatch-1"))
	dispatch, err := NewInitialDeliveryDispatch(
		communicationStateTestMutable(scope, at), model.NewID(), route, key[:],
	)
	if err != nil {
		t.Fatalf("initial dispatch: %v", err)
	}
	return dispatch
}

func communicationStateTestDispatchRootHistory(
	scope DirectoryScopeRef,
	observedAt time.Time,
	dispatches ...DeliveryDispatch,
) DispatchRootHistoryWitness {
	entries := make([]DispatchRootHistoryEntry, 0, len(dispatches))
	for _, dispatch := range dispatches {
		entries = append(entries, DispatchRootHistoryEntry{
			DispatchID: dispatch.ID, DispatchGeneration: dispatch.DispatchGeneration,
			IdempotencyKeyHash: append([]byte(nil), dispatch.IdempotencyKeyHash...),
		})
	}
	return DispatchRootHistoryWitness{
		Scope: scope, RootDispatchID: dispatches[0].RootDispatchID, Entries: entries,
		ObservedAt: observedAt,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "root_history_locked", EvidenceRef: "dispatch-root-history",
		},
	}
}

func communicationStateTestClaim(
	t *testing.T,
	dispatch DeliveryDispatch,
	at time.Time,
) DispatchAttemptClaimPlan {
	t.Helper()
	scope := DirectoryScopeRef{TenantID: dispatch.TenantID, WorkspaceID: dispatch.WorkspaceID}
	requestHash := sha256.Sum256([]byte(dispatch.ID.String()))
	plan, err := PlanDispatchAttemptClaim(
		dispatch, communicationStateTestMutable(scope, at), "worker-1", at.Add(time.Minute), requestHash[:], at,
	)
	if err != nil {
		t.Fatalf("claim dispatch: %v", err)
	}
	return plan
}

func communicationStateTestFail(
	t *testing.T,
	claim DispatchAttemptClaimPlan,
	at time.Time,
) DispatchAttemptFinishPlan {
	t.Helper()
	deadline := at.Add(time.Minute)
	plan, err := PlanDispatchAttemptFinish(claim.DispatchAfter, claim.Attempt, "worker-1",
		DispatchAttemptFinishInput{
			TargetState: DispatchFailed, TransmitBoundary: TransmitNotCrossed,
			Verdict: VerdictBroken, Code: "pre_transmit_failure", ResolutionDeadlineAt: &deadline,
		}, at)
	if err != nil {
		t.Fatalf("finish failed dispatch: %v", err)
	}
	return plan
}

func TestDispatchSuccessorLineage(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	route1 := DispatchRouteIdentity{
		EndpointID: model.NewID(), EndpointGeneration: 1, RouteRuleID: model.NewID(),
		RouteRuleGeneration: 1, PolicyGeneration: 1,
	}
	route2 := route1
	route2.RouteRuleID = model.NewID()
	route2.RouteRuleGeneration = 1
	initial := communicationStateTestDispatch(t, scope, route1, communicationTestNow)
	claim1 := communicationStateTestClaim(t, initial, communicationTestNow.Add(time.Second))
	failure1 := communicationStateTestFail(t, claim1, communicationTestNow.Add(2*time.Second))
	authority1 := DispatchSuccessorAuthority{
		PredecessorID: failure1.DispatchAfter.ID, AttemptID: failure1.AttemptAfter.ID,
		RootHistory: communicationStateTestDispatchRootHistory(
			scope, communicationTestNow.Add(3*time.Second), failure1.DispatchAfter,
		),
		CommandAuthorization: AuthorityEvidence{
			Verdict: VerdictClean, Code: "retry_authorized", EvidenceRef: "retry-command-1",
		},
		EvidenceRef: "retry-command-1",
	}
	key2 := sha256.Sum256([]byte("dispatch-2"))
	attestation1 := DispatchRouteAttestation{
		DispatchID: failure1.DispatchAfter.ID, Route: route1,
		ObservedAt: communicationTestNow.Add(3 * time.Second),
		Evidence:   AuthorityEvidence{Verdict: VerdictClean, Code: "route_attested", EvidenceRef: "route-1"},
	}
	successor1, err := PlanDispatchSuccessor(
		failure1.DispatchAfter, failure1.AttemptAfter, authority1, attestation1, model.NewID(), route1,
		key2[:], communicationTestNow.Add(3*time.Second),
	)
	if err != nil {
		t.Fatalf("same-route successor: %v", err)
	}
	if successor1.Successor.DispatchGeneration != 2 || successor1.Successor.RerouteRung != 0 {
		t.Fatalf("same route changed rung: %#v", successor1.Successor)
	}
	claim2 := communicationStateTestClaim(t, successor1.Successor, communicationTestNow.Add(4*time.Second))
	failure2 := communicationStateTestFail(t, claim2, communicationTestNow.Add(5*time.Second))
	authority2 := DispatchSuccessorAuthority{
		PredecessorID: failure2.DispatchAfter.ID, AttemptID: failure2.AttemptAfter.ID,
		RootHistory: communicationStateTestDispatchRootHistory(
			scope, communicationTestNow.Add(6*time.Second),
			successor1.PredecessorAfter, failure2.DispatchAfter,
		),
		CommandAuthorization: AuthorityEvidence{
			Verdict: VerdictClean, Code: "reroute_authorized", EvidenceRef: "reroute-command-2",
		},
		EvidenceRef: "reroute-command-2",
	}
	key3 := sha256.Sum256([]byte("dispatch-3"))
	attestation2 := DispatchRouteAttestation{
		DispatchID: failure2.DispatchAfter.ID, Route: route1,
		ObservedAt: communicationTestNow.Add(6 * time.Second),
		Evidence:   AuthorityEvidence{Verdict: VerdictClean, Code: "route_attested", EvidenceRef: "route-2"},
	}
	if effects, err := PlanDispatchSuccessor(
		failure2.DispatchAfter, failure2.AttemptAfter, authority2, attestation2,
		model.NewID(), route2, initial.IdempotencyKeyHash, communicationTestNow.Add(6*time.Second),
	); err == nil || !reflect.DeepEqual(effects, DispatchSuccessorPlan{}) {
		t.Fatalf("dispatch lineage cycled A -> B -> A idempotency key: %#v, %v", effects, err)
	}
	successor2, err := PlanDispatchSuccessor(
		failure2.DispatchAfter, failure2.AttemptAfter, authority2, attestation2, model.NewID(), route2,
		key3[:], communicationTestNow.Add(6*time.Second),
	)
	if err != nil {
		t.Fatalf("reroute successor: %v", err)
	}
	if successor2.Successor.DispatchGeneration != 3 || successor2.Successor.RerouteRung != 1 {
		t.Fatalf("reroute generation/rung = (%d,%d)",
			successor2.Successor.DispatchGeneration, successor2.Successor.RerouteRung)
	}
	if err := ValidateDispatchLineage([]DeliveryDispatch{
		successor1.PredecessorAfter, successor2.PredecessorAfter, successor2.Successor,
	}); err != nil {
		t.Fatalf("valid no-fork lineage: %v", err)
	}
	if err := ValidateDispatchLineage([]DeliveryDispatch{successor1.PredecessorAfter}); err == nil {
		t.Fatal("superseded predecessor validated without its atomically created child")
	}
	lateChild := successor2.Successor
	lateChild.CreatedAt = lateChild.CreatedAt.Add(time.Nanosecond)
	lateChild.UpdatedAt = lateChild.CreatedAt
	if err := ValidateDispatchLineage([]DeliveryDispatch{
		successor1.PredecessorAfter, successor2.PredecessorAfter, lateChild,
	}); err == nil {
		t.Fatal("successor validated after predecessor supersession transaction")
	}
	mutated := successor2.Successor
	mutated.RerouteRung = 0
	mutated.EndpointID = model.NewID()
	if err := ValidateDispatchLineage([]DeliveryDispatch{
		successor1.PredecessorAfter, successor2.PredecessorAfter, mutated,
	}); err == nil {
		t.Fatal("route change without rung increment accepted")
	}
}

func TestDispatchClaimFinishArePaired(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	route := DispatchRouteIdentity{EndpointID: model.NewID(), EndpointGeneration: 1, PolicyGeneration: 1}
	dispatch := communicationStateTestDispatch(t, scope, route, communicationTestNow)
	nextAttemptAt := communicationTestNow.Add(2 * time.Second)
	scheduled := dispatch
	scheduled.NextAttemptAt = &nextAttemptAt
	requestHash := sha256.Sum256([]byte("scheduled-dispatch"))
	if effects, err := PlanDispatchAttemptClaim(
		scheduled, communicationStateTestMutable(scope, communicationTestNow.Add(time.Second)),
		"worker-1", communicationTestNow.Add(time.Minute), requestHash[:],
		communicationTestNow.Add(time.Second),
	); !errors.Is(err, ErrInvalidCommunicationTransition) ||
		!reflect.DeepEqual(effects, DispatchAttemptClaimPlan{}) {
		t.Fatalf("future scheduled dispatch was claimed early: %#v, %v", effects, err)
	}
	scheduledClaim, err := PlanDispatchAttemptClaim(
		scheduled, communicationStateTestMutable(scope, nextAttemptAt),
		"worker-1", nextAttemptAt.Add(time.Minute), requestHash[:], nextAttemptAt,
	)
	if err != nil || scheduledClaim.DispatchAfter.NextAttemptAt != nil {
		t.Fatalf("due scheduled dispatch was not claimed and cleared atomically: %#v, %v", scheduledClaim, err)
	}
	claim := communicationStateTestClaim(t, dispatch, communicationTestNow.Add(time.Second))
	if claim.DispatchAfter.State != DispatchInFlight || claim.DispatchAfter.AttemptCount != 1 ||
		claim.Attempt.State != AttemptReserved || claim.Attempt.TransmitBoundary != TransmitUnknown {
		t.Fatalf("claim split Dispatch/Attempt: %#v", claim)
	}
	receipt := sha256.Sum256([]byte("provider receipt"))
	finish, err := PlanDispatchAttemptFinish(
		claim.DispatchAfter, claim.Attempt, "worker-1", DispatchAttemptFinishInput{
			TargetState: DispatchSucceeded, TransmitBoundary: TransmitCrossed,
			Verdict: VerdictClean, Code: "provider_accepted", ProviderReceiptHash: receipt[:],
		}, communicationTestNow.Add(2*time.Second),
	)
	if err != nil {
		t.Fatalf("finish dispatch: %v", err)
	}
	if finish.DispatchAfter.State != DispatchSucceeded || finish.AttemptAfter.State != AttemptFinished {
		t.Fatalf("finish split Dispatch/Attempt: %#v", finish)
	}
	bad := DispatchAttemptFinishInput{
		TargetState: DispatchFailed, TransmitBoundary: TransmitUnknown,
		Verdict: VerdictBroken, Code: "contradiction",
	}
	deadline := communicationTestNow.Add(3 * time.Minute)
	bad.ResolutionDeadlineAt = &deadline
	if _, err := PlanDispatchAttemptFinish(
		claim.DispatchAfter, claim.Attempt, "worker-1", bad,
		communicationTestNow.Add(2*time.Second),
	); err == nil {
		t.Fatal("failed Dispatch accepted unknown transmit boundary")
	}
}

func TestUnknownDispatchNeedsDurableReconciliation(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	route := DispatchRouteIdentity{
		EndpointID: model.NewID(), EndpointGeneration: 2,
		RouteRuleID: model.NewID(), RouteRuleGeneration: 3, PolicyGeneration: 4,
	}
	initial := communicationStateTestDispatch(t, scope, route, communicationTestNow)
	claim := communicationStateTestClaim(t, initial, communicationTestNow.Add(time.Second))
	deadline := communicationTestNow.Add(time.Minute)
	unknown, err := PlanDispatchAttemptFinish(
		claim.DispatchAfter, claim.Attempt, "worker-1", DispatchAttemptFinishInput{
			TargetState: DispatchUnknown, TransmitBoundary: TransmitUnknown,
			Verdict: VerdictUnknown, Code: "ambiguous", ResolutionDeadlineAt: &deadline,
		}, communicationTestNow.Add(2*time.Second),
	)
	if err != nil {
		t.Fatalf("finish UNKNOWN: %v", err)
	}
	dbNow := communicationTestNow.Add(3 * time.Second)
	commandRef := "retry-unknown"
	authority := DispatchSuccessorAuthority{
		PredecessorID: unknown.DispatchAfter.ID, AttemptID: unknown.AttemptAfter.ID,
		RootHistory: communicationStateTestDispatchRootHistory(scope, dbNow, unknown.DispatchAfter),
		CommandAuthorization: AuthorityEvidence{
			Verdict: VerdictClean, Code: "retry_allowed", EvidenceRef: commandRef,
		},
		EvidenceRef: commandRef,
	}
	priorRoute := DispatchRouteAttestation{
		DispatchID: unknown.DispatchAfter.ID, Route: route, ObservedAt: dbNow,
		Evidence: AuthorityEvidence{Verdict: VerdictClean, Code: "current", EvidenceRef: "route-current"},
	}
	key := sha256.Sum256([]byte("unknown-successor"))
	if _, err := PlanDispatchSuccessor(
		unknown.DispatchAfter, unknown.AttemptAfter, authority, priorRoute,
		model.NewID(), route, key[:], dbNow,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("UNKNOWN successor without no-acceptance = %v", err)
	}
	noAcceptance := DispatchProviderAcceptanceWitness{
		DispatchID: unknown.DispatchAfter.ID, AttemptID: unknown.AttemptAfter.ID,
		EndpointID: route.EndpointID, EndpointGeneration: route.EndpointGeneration, ObservedAt: dbNow,
		Acceptance: AuthorityEvidence{
			Verdict: VerdictBroken, Code: "not_accepted", EvidenceRef: "provider-query-1",
		},
	}
	authority.ProviderNoAcceptance = noAcceptance
	plan, err := PlanDispatchSuccessor(
		unknown.DispatchAfter, unknown.AttemptAfter, authority, priorRoute,
		model.NewID(), route, key[:], dbNow,
	)
	if err != nil {
		t.Fatalf("UNKNOWN successor with no-acceptance: %v", err)
	}
	if plan.PredecessorAfter.State != DispatchSuperseded ||
		plan.PredecessorAfter.ReconciliationVerdict != VerdictBroken ||
		plan.PredecessorAfter.ReconciledAttemptID != unknown.AttemptAfter.ID ||
		len(plan.PredecessorAfter.ProviderAcceptanceHash) != sha256.Size ||
		plan.Successor.RouteRuleID != route.RouteRuleID ||
		plan.Successor.RouteRuleGeneration != route.RouteRuleGeneration {
		t.Fatalf("successor lost route/reconciliation evidence: %#v", plan)
	}
	if err := ValidateDispatchAttempts(
		[]DeliveryDispatch{plan.PredecessorAfter, plan.Successor},
		[]DeliveryAttempt{unknown.AttemptAfter}, nil,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("reconciled dispatch without provider witness error = %v", err)
	}
	if err := ValidateDispatchAttempts(
		[]DeliveryDispatch{plan.PredecessorAfter, plan.Successor},
		[]DeliveryAttempt{unknown.AttemptAfter}, []DispatchProviderAcceptanceWitness{noAcceptance},
	); err != nil {
		t.Fatalf("durable reconciliation with exact witness did not survive restart validation: %v", err)
	}
	mutated := plan.PredecessorAfter
	mutated.ReconciledAttemptID = ""
	if err := ValidateDeliveryDispatch(mutated); err == nil {
		t.Fatal("UNKNOWN superseded state accepted half reconciliation")
	}
	if err := ValidateDispatchLineage([]DeliveryDispatch{unknown.DispatchAfter, plan.Successor}); err == nil {
		t.Fatal("successor without atomic predecessor supersession validated")
	}

	tooEarly := deadline.Add(-time.Nanosecond)
	unknownWitness := noAcceptance
	unknownWitness.ObservedAt = tooEarly
	unknownWitness.Acceptance = AuthorityEvidence{
		Verdict: VerdictUnknown, Code: "still_unknown", EvidenceRef: "provider-query-2",
	}
	if _, err := PlanDispatchReconcile(
		unknown.DispatchAfter, unknown.AttemptAfter, unknownWitness,
		DispatchDeadLetter, "deadline", nil, tooEarly,
	); err == nil {
		t.Fatal("UNKNOWN dispatch dead-lettered before exact deadline")
	}
	unknownWitness.ObservedAt = deadline
	dead, err := PlanDispatchReconcile(
		unknown.DispatchAfter, unknown.AttemptAfter, unknownWitness,
		DispatchDeadLetter, "deadline", nil, deadline,
	)
	if err != nil || dead.After.ReconciliationVerdict != VerdictUnknown ||
		dead.After.State != DispatchDeadLetter {
		t.Fatalf("UNKNOWN deadline reconciliation: %#v, %v", dead, err)
	}
}

func communicationStateTestDecisionResponsePayload(t *testing.T, choiceKey string) ProtectedPayload {
	t.Helper()
	content := DecisionResponseContent{
		ChoiceKey: choiceKey,
		Reason:    CommunicationReasonContent{Code: "resolved", Text: "resolved in test"},
	}
	raw, err := CanonicalProtectedPayloadSlot(PayloadSlotDecisionResponse, content)
	if err != nil {
		t.Fatalf("canonical DecisionResponse payload: %v", err)
	}
	digest := sha256.Sum256(raw)
	schema, _ := PayloadSlotDecisionResponse.schema()
	return ProtectedPayload{
		Encoding: PayloadPlainJSON, PlainJSON: raw, Schema: schema,
		Digest: digest[:], ProtectionGeneration: 1,
	}
}

func communicationStateTestDecisionChoiceWitness(
	t *testing.T,
	request DecisionRequest,
	response ProtectedPayload,
	choiceKey string,
	observedAt time.Time,
) *DecisionChoiceWitness {
	t.Helper()
	requestHash, err := CanonicalProtectedPayloadEnvelopeHash(request.Request)
	if err != nil {
		t.Fatalf("DecisionRequest envelope hash: %v", err)
	}
	responseHash, err := CanonicalProtectedPayloadEnvelopeHash(response)
	if err != nil {
		t.Fatalf("DecisionResponse envelope hash: %v", err)
	}
	return &DecisionChoiceWitness{
		Scope:     DirectoryScopeRef{TenantID: request.TenantID, WorkspaceID: request.WorkspaceID},
		RequestID: request.ID, RequestEnvelopeHash: requestHash, ResponseEnvelopeHash: responseHash,
		ChoiceKey: choiceKey, ObservedAt: observedAt, FreshUntil: observedAt.Add(time.Minute),
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "choice_in_request", EvidenceRef: "decision-choice-open",
		},
	}
}

func TestDecisionAcceptOnlyTakesCustody(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	request := DecisionRequest{
		MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
		MessageID:                  model.NewID(), WorkItemID: model.NewID(), DecisionKey: "deploy_choice",
		Requester: CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()},
		Owner:     CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()},
		State:     DecisionPending, Request: communicationTestPayloadForSlot(t, PayloadSlotDecisionRequest),
		AuthorityRequirement: "work_decide",
		DueAt:                communicationTestNow.Add(time.Hour),
	}
	responseEntity := communicationStateTestAppendOnly(scope, communicationTestNow.Add(time.Minute))
	plan, err := PlanDecisionRequestTransition(
		request, DecisionAccept, responseEntity,
		CommunicationActorRef{Kind: ActorUser, Ref: request.Owner.Ref},
		communicationTestPayloadForSlot(t, PayloadSlotDecisionResponse),
		nil, model.NewID(), "", "", "", communicationTestNow.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("accept custody: %v", err)
	}
	if plan.After.State != DecisionAccepted || plan.After.ResolvedDecisionID != "" ||
		plan.Response.WorkDecisionID != "" {
		t.Fatalf("accept fabricated decision: %#v", plan)
	}
	workDecisionID := model.NewID()
	responsePayload := communicationStateTestDecisionResponsePayload(t, "yes")
	choiceWitness := communicationStateTestDecisionChoiceWitness(
		t, request, responsePayload, "yes", communicationTestNow.Add(time.Minute),
	)
	resolved, err := PlanDecisionRequestTransition(
		request, DecisionResolve, responseEntity,
		CommunicationActorRef{Kind: ActorUser, Ref: request.Owner.Ref},
		responsePayload, choiceWitness, "", "", workDecisionID, "resolved", communicationTestNow.Add(time.Minute),
	)
	if err != nil || resolved.After.ResolvedDecisionID != workDecisionID ||
		resolved.Response.WorkDecisionID != workDecisionID ||
		ValidateDecisionResponse(resolved.Response, resolved.Before, resolved.After) != nil {
		t.Fatalf("exact resolved WorkDecision lineage denied: %#v, %v", resolved, err)
	}
	mismatchedResponse := resolved.Response
	mismatchedResponse.WorkDecisionID = model.NewID()
	if err := ValidateDecisionResponse(mismatchedResponse, resolved.Before, resolved.After); err == nil {
		t.Fatal("DecisionResponse linked a WorkDecision other than DecisionRequest.resolved_decision_id")
	}
	fabricatedAccepted := plan.After
	fabricatedAccepted.ResolvedDecisionID = model.NewID()
	fabricatedAcceptedResponse := plan.Response
	fabricatedAcceptedResponse.WorkDecisionID = fabricatedAccepted.ResolvedDecisionID
	if err := ValidateDecisionResponse(fabricatedAcceptedResponse, plan.Before, fabricatedAccepted); err == nil {
		t.Fatal("non-resolved DecisionRequest/Response retained a WorkDecision")
	}
	unknownChoice := *choiceWitness
	unknownChoice.Evidence = AuthorityEvidence{
		Verdict: VerdictUnknown, Code: "choice_open_unavailable", EvidenceRef: "decision-choice-open",
	}
	if effects, err := PlanDecisionRequestTransition(
		request, DecisionResolve, responseEntity,
		CommunicationActorRef{Kind: ActorUser, Ref: request.Owner.Ref}, responsePayload,
		&unknownChoice, "", "", workDecisionID, "resolved", communicationTestNow.Add(time.Minute),
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(effects, DecisionRequestTransitionPlan{}) {
		t.Fatalf("UNKNOWN choice witness planned a resolution: %#v, %v", effects, err)
	}
	emptyResponse := communicationStateTestDecisionResponsePayload(t, "")
	emptyWitness := communicationStateTestDecisionChoiceWitness(
		t, request, emptyResponse, "", communicationTestNow.Add(time.Minute),
	)
	if _, err := PlanDecisionRequestTransition(
		request, DecisionResolve, responseEntity,
		CommunicationActorRef{Kind: ActorUser, Ref: request.Owner.Ref}, emptyResponse,
		emptyWitness, "", "", workDecisionID, "resolved", communicationTestNow.Add(time.Minute),
	); err == nil {
		t.Fatal("Decision resolve accepted an empty choice key")
	}
	unknownResponse := communicationStateTestDecisionResponsePayload(t, "maybe")
	unknownWitness := communicationStateTestDecisionChoiceWitness(
		t, request, unknownResponse, "maybe", communicationTestNow.Add(time.Minute),
	)
	if _, err := PlanDecisionRequestTransition(
		request, DecisionResolve, responseEntity,
		CommunicationActorRef{Kind: ActorUser, Ref: request.Owner.Ref}, unknownResponse,
		unknownWitness, "", "", workDecisionID, "resolved", communicationTestNow.Add(time.Minute),
	); !errors.Is(err, ErrCommunicationForbidden) {
		t.Fatalf("Decision resolve unknown choice error = %v", err)
	}
	sealedRequest := request
	sealedRequest.Request = communicationStateTestSealedPayload(PayloadSlotDecisionRequest, 3)
	sealedResponse := communicationStateTestSealedPayload(PayloadSlotDecisionResponse, 3)
	sealedWitness := communicationStateTestDecisionChoiceWitness(
		t, sealedRequest, sealedResponse, "yes", communicationTestNow.Add(time.Minute),
	)
	if _, err := PlanDecisionRequestTransition(
		sealedRequest, DecisionResolve, responseEntity,
		CommunicationActorRef{Kind: ActorUser, Ref: request.Owner.Ref}, sealedResponse,
		sealedWitness, "", "", workDecisionID, "resolved", communicationTestNow.Add(time.Minute),
	); err != nil {
		t.Fatalf("sealed exact choice witness denied: %v", err)
	}
	if _, err := PlanDecisionRequestTransition(
		request, DecisionAccept, responseEntity,
		CommunicationActorRef{Kind: ActorUser, Ref: request.Owner.Ref},
		communicationTestPayloadForSlot(t, PayloadSlotDecisionResponse),
		nil, model.NewID(), "", model.NewID(), "", communicationTestNow.Add(time.Minute),
	); err == nil {
		t.Fatal("accept linked WorkDecision")
	}
	if _, err := PlanDecisionRequestTransition(
		request, DecisionExpire, responseEntity,
		CommunicationActorRef{Kind: ActorSystem, Ref: "deadline-worker"},
		communicationTestPayloadForSlot(t, PayloadSlotDecisionResponse),
		nil, "", "", "", "deadline_elapsed", communicationTestNow.Add(time.Minute),
	); err == nil {
		t.Fatal("DecisionRequest expired before due_at")
	}
}

func TestHandoffOfferDoesNotTransferAuthority(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	offer := Handoff{
		MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
		WorkItemID:                 model.NewID(), MessageID: model.NewID(), DeliveryID: model.NewID(),
		From: RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}, FromOwnerEpoch: 4,
		To: RecipientRef{Kind: RecipientAgent, Ref: model.NewID().String()}, OfferedLeaseFence: 9,
		ContextEventSeq: 12,
		Payload:         communicationTestPayloadForSlot(t, PayloadSlotHandoff), State: HandoffOffered,
		AckDeadline: communicationTestNow.Add(time.Hour),
	}
	contextHash, err := CanonicalHandoffContextHash(offer)
	if err != nil {
		t.Fatalf("Handoff context hash: %v", err)
	}
	offer.ContextHash = contextHash
	if err := ValidateHandoff(offer); err != nil {
		t.Fatalf("offered Handoff: %v", err)
	}
	mutated := offer
	mutated.ResultingLeaseFence = 10
	if err := ValidateHandoff(mutated); err == nil {
		t.Fatal("offer changed lease fence")
	}
	plan, err := PlanHandoffTransition(
		offer, HandoffAccept, model.NewID(), 10, "", nil, communicationTestNow.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("accept Handoff: %v", err)
	}
	if !plan.CreatesAck || !plan.ChangesLease || plan.After.State != HandoffAccepted ||
		plan.After.ResultingLeaseFence != 10 {
		t.Fatalf("accepted Handoff missed atomic effects: %#v", plan)
	}
}

func communicationStateTestTerminalCarrierSet(
	t *testing.T,
	message Message,
	deliveries []MessageDelivery,
	decisionID model.ID,
	handoffID model.ID,
	dbNow time.Time,
) MessageTerminalCarrierSetWitness {
	t.Helper()
	digest, err := CanonicalFulfillmentDeliverySetDigest(deliveries)
	if err != nil {
		t.Fatalf("terminal carrier digest: %v", err)
	}
	return MessageTerminalCarrierSetWitness{
		Scope:     DirectoryScopeRef{TenantID: message.TenantID, WorkspaceID: message.WorkspaceID},
		MessageID: message.ID, DeliveryCount: int64(len(deliveries)), DeliveryDigest: digest,
		DecisionRequestID: decisionID, HandoffID: handoffID, ObservedAt: dbNow,
		Evidence: AuthorityEvidence{Verdict: VerdictClean, Code: "complete", EvidenceRef: "same_tx_carriers"},
	}
}

func TestMessageTerminalCascadeIsOnePlan(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	reason := communicationTestPayloadForSlot(t, PayloadSlotMessageTerminalReason)
	t.Run("discards actionable drafts without nonexistent children", func(t *testing.T) {
		for _, kind := range []MessageKind{MessageDecisionRequest, MessageHandoffOffer} {
			t.Run(string(kind), func(t *testing.T) {
				draft := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
				draft.Kind = kind
				draft.WorkItemID = model.NewID()
				draft.State = MessageDraft
				draft.PublishedAt = nil
				draft.AudienceHash = nil
				draft.LastEventSeq = 0
				dbNow := communicationTestNow.Add(time.Minute)
				plan, err := PlanMessageTransition(MessageTransitionInput{
					Before: draft, Transition: MessageDiscard,
					CarrierSet:   communicationStateTestTerminalCarrierSet(t, draft, nil, "", "", dbNow),
					TerminalCode: "discarded", TerminalReason: reason, DBNow: dbNow,
				})
				if err != nil || plan.After.State != MessageDiscarded || plan.ExpectedEffects != 1 ||
					len(plan.DeliveryPlans) != 0 || plan.DecisionPlan != nil || plan.HandoffPlan != nil {
					t.Fatalf("actionable draft discard required nonexistent child: %#v, %v", plan, err)
				}
			})
		}
	})
	t.Run("retracts every available Delivery", func(t *testing.T) {
		message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
		deliveries := []MessageDelivery{
			communicationStateTestDelivery(message, RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}, 1, false),
			communicationStateTestDelivery(message, RecipientRef{Kind: RecipientAgent, Ref: model.NewID().String()}, 2, false),
		}
		dbNow := communicationTestNow.Add(time.Minute)
		input := MessageTransitionInput{
			Before: message, Transition: MessageRetract, Deliveries: deliveries,
			CarrierSet:   communicationStateTestTerminalCarrierSet(t, message, deliveries, "", "", dbNow),
			TerminalCode: "retracted", TerminalReason: reason, DBNow: dbNow,
		}
		plan, err := PlanMessageTransition(input)
		if err != nil {
			t.Fatalf("terminal cascade: %v", err)
		}
		if plan.After.State != MessageRetracted || plan.After.LastEventSeq != 2 ||
			len(plan.DeliveryPlans) != 2 || plan.ExpectedEffects != 3 {
			t.Fatalf("terminal plan is partial: %#v", plan)
		}
		for _, deliveryPlan := range plan.DeliveryPlans {
			if deliveryPlan.After.State != DeliveryRetracted {
				t.Fatalf("available Delivery survived retract: %#v", deliveryPlan)
			}
		}
		truncated := input
		truncated.Deliveries = deliveries[:1]
		if _, err := PlanMessageTransition(truncated); err == nil {
			t.Fatal("truncated carrier set produced a partial cascade")
		}
	})

	t.Run("expires linked DecisionRequest", func(t *testing.T) {
		message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
		message.Kind = MessageDecisionRequest
		message.WorkItemID = model.NewID()
		message.LastEventSeq = 0
		due := communicationTestNow.Add(2 * time.Minute)
		expires := communicationTestNow.Add(3 * time.Minute)
		message.ExpiresAt = &expires
		delivery := communicationStateTestDelivery(message,
			RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}, 1, false)
		request := DecisionRequest{
			MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
			MessageID:                  message.ID, WorkItemID: message.WorkItemID, DecisionKey: "approve",
			Requester: CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()},
			Owner:     CommunicationSubjectRef{Kind: SubjectUser, Ref: delivery.Recipient.Ref},
			State:     DecisionPending, Request: communicationTestPayloadForSlot(t, PayloadSlotDecisionRequest),
			AuthorityRequirement: "work_decide", DueAt: due,
		}
		input := MessageTransitionInput{
			Before: message, Transition: MessageExpire, Deliveries: []MessageDelivery{delivery},
			Decision: &MessageDecisionCascadeInput{
				Request: request, ResponseEntity: communicationStateTestAppendOnly(scope, expires),
				Actor:    CommunicationActorRef{Kind: ActorSystem, Ref: "message-expiry"},
				Response: communicationTestPayloadForSlot(t, PayloadSlotDecisionResponse),
			},
			CarrierSet: communicationStateTestTerminalCarrierSet(
				t, message, []MessageDelivery{delivery}, request.ID, "", expires),
			TerminalCode: "expired", TerminalReason: reason, DBNow: expires,
		}
		plan, err := PlanMessageTransition(input)
		if err != nil {
			t.Fatalf("Decision cascade: %v", err)
		}
		if plan.DecisionPlan == nil || plan.DecisionPlan.After.State != DecisionExpired ||
			plan.DecisionPlan.Response.ToState != DecisionExpired ||
			plan.DeliveryPlans[0].After.State != DeliveryExpired || plan.ExpectedEffects != 4 {
			t.Fatalf("DecisionRequest survived Message expiry: %#v", plan)
		}
	})

	t.Run("withdraws linked Handoff", func(t *testing.T) {
		message := communicationStateTestMessage(t, scope, AckPolicyEachRequired, 1, communicationTestNow)
		message.Kind = MessageHandoffOffer
		message.WorkItemID = model.NewID()
		message.LastEventSeq = 0
		delivery := communicationStateTestDelivery(message,
			RecipientRef{Kind: RecipientAgent, Ref: model.NewID().String()}, 1, true)
		handoff := Handoff{
			MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
			WorkItemID:                 message.WorkItemID, MessageID: message.ID, DeliveryID: delivery.ID,
			From: RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}, FromOwnerEpoch: 1,
			To: delivery.Recipient, ContextEventSeq: 1,
			Payload: communicationTestPayloadForSlot(t, PayloadSlotHandoff), State: HandoffOffered,
			AckDeadline: *message.AckDueAt,
		}
		handoff.ContextHash, _ = CanonicalHandoffContextHash(handoff)
		dbNow := communicationTestNow.Add(time.Minute)
		plan, err := PlanMessageTransition(MessageTransitionInput{
			Before: message, Transition: MessageRetract, Deliveries: []MessageDelivery{delivery},
			Handoff: &MessageHandoffCascadeInput{
				Handoff: handoff, TerminalCode: "message_retracted",
				Reason: communicationTestPayloadForSlot(t, PayloadSlotHandoffTerminalReason),
			},
			CarrierSet: communicationStateTestTerminalCarrierSet(
				t, message, []MessageDelivery{delivery}, "", handoff.ID, dbNow),
			TerminalCode: "retracted", TerminalReason: reason, DBNow: dbNow,
		})
		if err != nil {
			t.Fatalf("Handoff cascade: %v", err)
		}
		if plan.HandoffPlan == nil || plan.HandoffPlan.After.State != HandoffWithdrawn ||
			plan.HandoffPlan.ChangesLease || plan.ExpectedEffects != 3 {
			t.Fatalf("Handoff survived Message retract: %#v", plan)
		}
		secondRequired := communicationStateTestDelivery(message,
			RecipientRef{Kind: RecipientAgent, Ref: model.NewID().String()}, 2, true)
		twoRequired := []MessageDelivery{delivery, secondRequired}
		if _, err := PlanMessageTransition(MessageTransitionInput{
			Before: message, Transition: MessageRetract, Deliveries: twoRequired,
			Handoff: &MessageHandoffCascadeInput{
				Handoff: handoff, TerminalCode: "message_retracted",
				Reason: communicationTestPayloadForSlot(t, PayloadSlotHandoffTerminalReason),
			},
			CarrierSet: communicationStateTestTerminalCarrierSet(
				t, message, twoRequired, "", handoff.ID, dbNow),
			TerminalCode: "retracted", TerminalReason: reason, DBNow: dbNow,
		}); err == nil {
			t.Fatal("Handoff cascade accepted two complete required deliveries")
		}
	})
}

func communicationStateTestSealedPayload(slot ProtectedPayloadSlot, generation int64) ProtectedPayload {
	schema, _ := slot.schema()
	return ProtectedPayload{
		Encoding: PayloadSealedV1,
		Sealed:   &SealedPayload{Ciphertext: []byte("opaque-ciphertext"), KeyVersion: "seal-v3"},
		Schema:   schema, Digest: bytes.Repeat([]byte{0x51}, sha256.Size),
		SealKeyVersion: "seal-v3", DigestKeyVersion: "digest-v8", ProtectionGeneration: generation,
	}
}

func TestSealedCarrierRejectsPlainSecondaryPayloads(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	message.Payload = communicationStateTestSealedPayload(PayloadSlotMessage, 3)
	delivery := communicationStateTestDelivery(message,
		RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}, 1, false)
	dbNow := communicationTestNow.Add(time.Minute)
	plainReason := communicationTestPayloadForSlot(t, PayloadSlotMessageTerminalReason)
	if _, err := PlanMessageTransition(MessageTransitionInput{
		Before: message, Transition: MessageRetract, Deliveries: []MessageDelivery{delivery},
		CarrierSet:   communicationStateTestTerminalCarrierSet(t, message, []MessageDelivery{delivery}, "", "", dbNow),
		TerminalCode: "retracted", TerminalReason: plainReason, DBNow: dbNow,
	}); err == nil {
		t.Fatal("sealed Message accepted a plain terminal reason")
	}

	principal := CommunicationPrincipal{UserID: model.ID(delivery.Recipient.Ref)}
	authority := communicationStateTestReadEvidence(scope, message, delivery, principal, dbNow)
	authority.Operation = CommunicationDeliveryWrite
	authority.Core.Operation = CommunicationDeliveryWrite
	note := communicationTestPayloadForSlot(t, PayloadSlotAckNote)
	if _, err := PlanMessageAck(delivery, model.NewID(), CommunicationActorRef{
		Kind: ActorUser, Ref: delivery.Recipient.Ref,
	}, nil, &authority, &note, dbNow); err == nil {
		t.Fatal("sealed Message accepted a plain Ack note")
	}

	request := DecisionRequest{
		MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
		MessageID:                  model.NewID(), WorkItemID: model.NewID(), DecisionKey: "approve",
		Requester:            CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()},
		Owner:                CommunicationSubjectRef{Kind: SubjectUser, Ref: delivery.Recipient.Ref},
		State:                DecisionPending,
		Request:              communicationStateTestSealedPayload(PayloadSlotDecisionRequest, 3),
		AuthorityRequirement: "work_decide", DueAt: communicationTestNow.Add(time.Hour),
	}
	if _, err := PlanDecisionRequestTransition(
		request, DecisionReject, communicationStateTestAppendOnly(scope, dbNow),
		CommunicationActorRef{Kind: ActorUser, Ref: delivery.Recipient.Ref},
		communicationTestPayloadForSlot(t, PayloadSlotDecisionResponse),
		nil, "", "", "", "rejected", dbNow,
	); err == nil {
		t.Fatal("sealed DecisionRequest accepted a plain response")
	}

	handoff := Handoff{
		MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
		WorkItemID:                 model.NewID(), MessageID: model.NewID(), DeliveryID: model.NewID(),
		From: RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}, FromOwnerEpoch: 1,
		To: RecipientRef{Kind: RecipientAgent, Ref: model.NewID().String()}, ContextEventSeq: 1,
		Payload: communicationStateTestSealedPayload(PayloadSlotHandoff, 3), State: HandoffOffered,
		AckDeadline: communicationTestNow.Add(time.Hour),
	}
	handoff.ContextHash, _ = CanonicalHandoffContextHash(handoff)
	handoffReason := communicationTestPayloadForSlot(t, PayloadSlotHandoffTerminalReason)
	if _, err := PlanHandoffTransition(
		handoff, HandoffWithdraw, "", 0, "withdrawn", &handoffReason, dbNow,
	); err == nil {
		t.Fatal("sealed Handoff accepted a plain terminal reason")
	}
}

func TestCommunicationGuardAdvanceIsMonotonicAndPure(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	guard := CommunicationGuard{
		MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
		Kind:                       CommunicationGuardDeliverySequence, NextSeq: 9, LastDBTime: communicationTestNow,
	}
	before := guard
	plan, err := PlanCommunicationGuardAdvance(guard, 3, communicationTestNow.Add(time.Second))
	if err != nil {
		t.Fatalf("guard allocation: %v", err)
	}
	if !reflect.DeepEqual(guard, before) || plan.After.Version != before.Version+1 ||
		plan.After.NextSeq != 12 || !reflect.DeepEqual(plan.AllocatedSeq, []int64{9, 10, 11}) {
		t.Fatalf("guard planner mutated input or allocated wrong range: %#v", plan)
	}
	if _, err := PlanCommunicationGuardAdvance(guard, 1, communicationTestNow.Add(-time.Second)); err == nil {
		t.Fatal("guard accepted DB time rollback")
	}
	guard.NextSeq = math.MaxInt64
	if _, err := PlanCommunicationGuardAdvance(guard, 1, communicationTestNow.Add(time.Second)); err == nil {
		t.Fatal("guard sequence overflow accepted")
	}
}

func TestPlanMessagePublishBindsCompleteAudienceSnapshot(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	createdAt := communicationTestNow
	dbNow := createdAt.Add(time.Minute)
	channel := Channel{
		MutableCommunicationEntity: communicationStateTestMutable(scope, createdAt),
		Slug:                       "coordination", Name: "Coordination", Kind: ChannelCoordination, State: ChannelActive,
		Sensitivity: ChannelInternal, ContentProtection: ContentProtectionStorage, ProtectionGeneration: 1,
		DefaultAckPolicy: AckPolicyNone, DefaultWake: WakeNone, MaxFanout: 16,
		MaxAutomationDepth: 4, ACLRevision: 4, RouteRevision: 5, SubscriptionRevision: 6,
	}
	senderID := model.NewID()
	principal := CommunicationPrincipal{UserID: senderID}
	draftEntity := communicationStateTestMutable(scope, createdAt)
	ackDue := dbNow.Add(10 * time.Minute)
	expires := dbNow.Add(time.Hour)
	draft := Message{
		MutableCommunicationEntity: draftEntity, ChannelID: channel.ID, ThreadID: draftEntity.ID,
		Kind: MessageNotice, State: MessageDraft,
		Sender:  CommunicationActorRef{Kind: ActorUser, Ref: senderID.String()},
		Payload: communicationTestPayload(t), Urgency: UrgencyNormal,
		AckPolicy: AckPolicyEachRequired, AvailableAt: dbNow, AckDueAt: &ackDue, ExpiresAt: &expires,
	}
	recipientA := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	recipientB := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	groupID := model.NewID()
	subscriberGroupID := model.NewID()
	secondSubscriberGroupID := model.NewID()
	subscriptionID := model.NewID()
	secondSubscriptionID := model.NewID()
	subscriberRouteRuleID := model.NewID()
	selectors := []AudienceSelector{
		{Kind: AudienceUser, Ref: recipientA.Ref, WakePolicy: WakeNone},
		{Kind: AudienceUserGroup, Ref: groupID.String(), Required: true, WakePolicy: WakePrimary},
		{Kind: AudienceWorkspaceMembers, WakePolicy: WakeAll},
		{Kind: AudienceSubscribers, WakePolicy: WakeNone},
	}
	recipients := []RecipientSnapshot{
		{Scope: scope, Recipient: recipientA, RecipientEpoch: 3, DirectoryEpoch: 7, Eligible: true},
		{Scope: scope, Recipient: recipientB, RecipientEpoch: 9, DirectoryEpoch: 7, Eligible: true},
	}
	groupFact := store.AuthorizationFactRef{
		Kind: model.Kind("core.user_group_member"), ID: model.NewID(), Version: 2,
	}
	workspaceFact := store.AuthorizationFactRef{
		Kind: model.Kind("core.membership"), ID: model.NewID(), Version: 3,
	}
	subscriberFact := store.AuthorizationFactRef{
		Kind: model.Kind("core.user_group_member"), ID: model.NewID(), Version: 4,
	}
	secondSubscriberFact := store.AuthorizationFactRef{
		Kind: model.Kind("core.user_group_member"), ID: model.NewID(), Version: 5,
	}
	snapshotContributions := []ResolvedAudienceContribution{
		{SelectorOrdinal: 1, Selector: selectors[0], Recipient: recipients[0],
			Required: false, WakePolicy: WakeNone, RouteReasons: []RouteReason{"direct"},
			CausalKind: CausalDirect, CausalRef: recipientA.Ref},
		{SelectorOrdinal: 2, Selector: selectors[1], Recipient: recipients[1],
			Required: true, WakePolicy: WakePrimary, RouteReasons: []RouteReason{"group"},
			CausalKind: CausalUserGroup, CausalRef: groupID.String(), CausalFact: &groupFact},
		{SelectorOrdinal: 3, Selector: selectors[2], Recipient: recipients[1],
			Required: false, WakePolicy: WakeAll, RouteReasons: []RouteReason{"workspace"},
			CausalKind: CausalWorkspaceMember, CausalRef: scope.WorkspaceID.String(), CausalFact: &workspaceFact},
		{SelectorOrdinal: 4, Selector: selectors[3], Recipient: recipients[1],
			Required: true, WakePolicy: WakeAll, RouteReasons: []RouteReason{"subscriber_critical"},
			RouteRuleID: subscriberRouteRuleID, RouteRuleGeneration: 3,
			CausalKind: CausalSubscriber, CausalRef: subscriberGroupID.String(), CausalFact: &subscriberFact,
			OriginalSubscriber: &CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: subscriberGroupID.String()},
			SubscriptionID:     subscriptionID, SubscriptionGeneration: 2},
		{SelectorOrdinal: 4, Selector: selectors[3], Recipient: recipients[0],
			Required: false, WakePolicy: WakeNone, RouteReasons: []RouteReason{"subscriber_optional"},
			RouteRuleID: subscriberRouteRuleID, RouteRuleGeneration: 3,
			CausalKind: CausalSubscriber, CausalRef: subscriberGroupID.String(), CausalFact: &subscriberFact,
			OriginalSubscriber: &CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: subscriberGroupID.String()},
			SubscriptionID:     subscriptionID, SubscriptionGeneration: 2},
		{SelectorOrdinal: 4, Selector: selectors[3], Recipient: recipients[1],
			Required: false, WakePolicy: WakeNone, RouteReasons: []RouteReason{"subscriber_secondary"},
			RouteRuleID: subscriberRouteRuleID, RouteRuleGeneration: 3,
			CausalKind: CausalSubscriber, CausalRef: secondSubscriberGroupID.String(),
			CausalFact: &secondSubscriberFact,
			OriginalSubscriber: &CommunicationSubjectRef{
				Kind: SubjectUserGroup, Ref: secondSubscriberGroupID.String(),
			},
			SubscriptionID: secondSubscriptionID, SubscriptionGeneration: 1},
	}
	rosterHash, err := CanonicalDirectoryRosterHash(scope, 7, recipients)
	if err != nil {
		t.Fatalf("directory roster: %v", err)
	}
	snapshot := DirectorySnapshot{
		Scope: scope, Epoch: 7, Selectors: selectors, Recipients: recipients,
		Contributions: snapshotContributions, RosterHash: rosterHash,
		ObservedAt: dbNow.Add(-time.Second), FreshUntil: dbNow.Add(time.Minute),
	}
	deliveryIDs := map[RecipientRef]model.ID{recipientA: model.NewID(), recipientB: model.NewID()}
	audiences := make([]MessageAudience, len(selectors))
	for index, selector := range selectors {
		audienceEntity := communicationStateTestAppendOnly(scope, dbNow)
		selectorRaw, err := canonicalJSON(selector)
		if err != nil {
			t.Fatalf("selector JSON: %v", err)
		}
		selectorHash := sha256.Sum256(selectorRaw)
		resolvedRecipients := make(map[RecipientRef]struct{})
		for _, resolved := range snapshotContributions {
			if resolved.SelectorOrdinal == int64(index+1) {
				resolvedRecipients[resolved.Recipient.Recipient] = struct{}{}
			}
		}
		audiences[index] = MessageAudience{
			AppendOnlyCommunicationEntity: audienceEntity, MessageID: draft.ID, Ordinal: int64(index + 1),
			Selector: selector, ChannelACLRevision: channel.ACLRevision,
			RouteRevision: channel.RouteRevision, SubscriptionRevision: channel.SubscriptionRevision,
			DirectoryEpoch: snapshot.Epoch, DirectorySnapshotAt: snapshot.ObservedAt,
			ResolvedCount: int64(len(resolvedRecipients)), SelectorHash: selectorHash[:],
		}
		if index == 3 {
			audiences[index].RouteRuleID = subscriberRouteRuleID
		}
	}
	contributions := make([]MessageAudienceRecipient, 0, len(snapshotContributions))
	for _, resolved := range snapshotContributions {
		audience := audiences[resolved.SelectorOrdinal-1]
		row := MessageAudienceRecipient{
			AppendOnlyCommunicationEntity: communicationStateTestAppendOnly(scope, dbNow),
			MessageAudienceID:             audience.ID, MessageDeliveryID: deliveryIDs[resolved.Recipient.Recipient],
			Recipient: resolved.Recipient.Recipient, RecipientEpoch: resolved.Recipient.RecipientEpoch,
			Required: resolved.Required, WakePolicy: resolved.WakePolicy,
			RouteReasons: append([]RouteReason(nil), resolved.RouteReasons...),
			Selector:     resolved.Selector, DirectoryEpoch: snapshot.Epoch,
			ChannelACLRevision: channel.ACLRevision, RouteRevision: channel.RouteRevision,
			SubscriptionRevision: channel.SubscriptionRevision, CausalKind: resolved.CausalKind,
			CausalRef: resolved.CausalRef, OriginalSubscriber: resolved.OriginalSubscriber,
			SubscriptionID: resolved.SubscriptionID, SubscriptionGeneration: resolved.SubscriptionGeneration,
			RouteRuleID: resolved.RouteRuleID, RouteRuleGeneration: resolved.RouteRuleGeneration,
		}
		if resolved.CausalFact != nil {
			row.CausalFactKind = resolved.CausalFact.Kind
			row.CausalFactID = resolved.CausalFact.ID
			row.CausalFactVersion = resolved.CausalFact.Version
		}
		row = communicationStateTestSealCausalArc(row)
		contributions = append(contributions, row)
	}
	for index := range audiences {
		resolvedRows := make([]MessageAudienceRecipient, 0, audiences[index].ResolvedCount)
		for _, row := range contributions {
			if row.MessageAudienceID == audiences[index].ID {
				resolvedRows = append(resolvedRows, row)
			}
		}
		audiences[index].ResolvedHash, err = canonicalResolvedAudienceHash(audiences[index], resolvedRows)
		if err != nil {
			t.Fatalf("resolved audience %d: %v", index, err)
		}
	}
	makeDelivery := func(recipient RecipientRef, required bool, routeReasons []RouteReason) MessageDelivery {
		entity := communicationStateTestMutable(scope, dbNow)
		entity.ID = deliveryIDs[recipient]
		delivery := MessageDelivery{
			MutableCommunicationEntity: entity, MessageID: draft.ID, Recipient: recipient,
			RecipientEpoch: 3, Required: required, RouteReasons: routeReasons,
			WakePolicy: WakeNone, State: DeliveryAvailable, AvailableAt: draft.AvailableAt, ExpiresAt: draft.ExpiresAt,
		}
		if recipient == recipientB {
			delivery.RecipientEpoch = 9
			delivery.WakePolicy = WakeAll
		}
		if required {
			delivery.AckDueAt = draft.AckDueAt
		}
		return delivery
	}
	deliveries := []MessageDelivery{
		makeDelivery(recipientB, true,
			[]RouteReason{"group", "subscriber_critical", "subscriber_secondary", "workspace"}),
		makeDelivery(recipientA, false, []RouteReason{"direct", "subscriber_optional"}),
	}
	guard := CommunicationGuard{
		MutableCommunicationEntity: communicationStateTestMutable(scope, createdAt),
		Kind:                       CommunicationGuardDeliverySequence, NextSeq: 20, LastDBTime: createdAt,
	}
	clean := AuthorityEvidence{Verdict: VerdictClean, Code: "current", EvidenceRef: "publish-current"}
	audienceRequest := PublicationAudienceRequest{
		Scope: scope, ChannelID: channel.ID, ChannelACLRevision: channel.ACLRevision,
		RouteRevision: channel.RouteRevision, SubscriptionRevision: channel.SubscriptionRevision,
		MessageKind: draft.Kind, Urgency: draft.Urgency,
		Sender: draft.Sender, SourceKind: RouteSourceUserMessage,
		LabelsJSON: draft.LabelsJSON, LabelsHash: draft.LabelsHash,
		ChannelDefaultWake: channel.DefaultWake, ContentProtection: channel.ContentProtection,
		ProtectionGeneration: channel.ProtectionGeneration,
		RequestedAt:          snapshot.ObservedAt.Add(-time.Second), Selectors: selectors,
	}
	labeledRequest := audienceRequest
	labeledRequest.LabelsJSON = json.RawMessage(`{"audience":"critical"}`)
	labeledDigest := sha256.Sum256(labeledRequest.LabelsJSON)
	labeledRequest.LabelsHash = labeledDigest[:]
	if err := ValidatePublicationAudienceRequest(labeledRequest); err != nil {
		t.Fatalf("attestor request cannot inspect canonical label values: %v", err)
	}
	labeledRequest.LabelsJSON = json.RawMessage(`{"audience":"normal"}`)
	if err := ValidatePublicationAudienceRequest(labeledRequest); err == nil {
		t.Fatal("attestor request accepted label values that drifted from LabelsHash")
	}
	audienceRequestHash, err := CanonicalPublicationAudienceRequestHash(audienceRequest)
	if err != nil {
		t.Fatalf("publication audience request: %v", err)
	}
	audienceSnapshotHash, err := CanonicalPublicationAudienceSnapshotHash(snapshot)
	if err != nil {
		t.Fatalf("publication audience snapshot: %v", err)
	}
	audienceAttestation := PublicationAudienceAttestation{
		Scope: scope, DirectoryEpoch: snapshot.Epoch, RequestHash: audienceRequestHash,
		SnapshotHash: audienceSnapshotHash, ObservedAt: snapshot.ObservedAt, FreshUntil: snapshot.FreshUntil,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "attested", EvidenceRef: "publication-audience-attestor",
		},
	}
	gate := SendGateEvidence{
		Scope: scope, ChannelID: channel.ID, ChannelACLRevision: channel.ACLRevision,
		DBNow: dbNow, Principal: principal,
		Core: ReadWitness{
			Outcome: ReadAllow, Code: "allowed",
			Entity: EntityRef{TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
				Kind: model.Kind("sessions.channel"), ID: channel.ID},
			Operation: CommunicationMessageSend, Principal: principal, ObservedAt: dbNow,
			FreshUntil: dbNow.Add(time.Minute), CorePermission: clean, ResourceGuard: clean, ForbidAbsence: clean,
		},
		DirectoryEpoch: store.AuthorizationFactRef{
			Kind: model.DirectoryEpochKind, ID: model.ID(scope.TenantID), Version: snapshot.Epoch,
		},
		CurrentChannelWriteGrant: BoundChannelReadEvidence{
			TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, ChannelID: channel.ID,
			Principal: principal, Bit: ChannelGrantWrite, GrantID: model.NewID(), GrantVersion: 2,
			DirectoryEpoch: snapshot.Epoch, ChannelACLRevision: channel.ACLRevision,
			EvaluatedAt: dbNow, Evidence: clean,
		},
	}
	input := MessagePublishInput{
		Draft: draft, Channel: channel, AudienceRequest: audienceRequest,
		AudienceAttestation: audienceAttestation, Snapshot: snapshot, Audiences: audiences,
		Contributions: contributions, Deliveries: deliveries,
		Labels: ChannelLabelSnapshot{
			Scope: scope, ChannelID: channel.ID, RouteRevision: channel.RouteRevision,
			ObservedAt: dbNow, SameTransaction: true, Evidence: clean,
		},
		SendGate: gate, Principal: principal, Sender: draft.Sender,
		SourceKind:            RouteSourceUserMessage,
		DeliverySequenceGuard: guard, DBNow: dbNow,
	}
	inputBefore, err := canonicalJSON(input)
	if err != nil {
		t.Fatalf("canonical publish input: %v", err)
	}
	plan, err := PlanMessagePublish(input)
	if err != nil {
		t.Fatalf("authoritative publish: %v", err)
	}
	if plan.After.State != MessagePublished || len(plan.Deliveries) != 2 ||
		len(plan.Contributions) != 6 || plan.RequiredCount != 1 || plan.GuardAdvance == nil ||
		!reflect.DeepEqual(plan.GuardAdvance.AllocatedSeq, []int64{20, 21}) ||
		len(plan.Facts) == 0 || plan.ChannelFence.DirectoryEpoch != snapshot.Epoch {
		t.Fatalf("publish omitted authoritative effects: %#v", plan)
	}
	if selectors[3].Required || !snapshotContributions[3].Required ||
		snapshotContributions[3].WakePolicy != WakeAll {
		t.Fatal("optional subscriber selector did not retain critical per-recipient policy")
	}
	missingAttestation := input
	missingAttestation.AudienceAttestation = PublicationAudienceAttestation{}
	if effects, err := PlanMessagePublish(missingAttestation); err == nil ||
		!reflect.DeepEqual(effects, MessagePublishPlan{}) {
		t.Fatalf("publish accepted missing audience attestation: %#v, %v", effects, err)
	}
	missingPolicyInput := input
	missingPolicyInput.AudienceRequest.MessageKind = ""
	if _, err := PlanMessagePublish(missingPolicyInput); err == nil {
		t.Fatal("publication audience request omitted server-bound Message kind")
	}
	mentionDrift := input
	mentionDrift.AudienceRequest.MentionedRecipients = []RecipientRef{recipientA}
	mentionDrift.MentionedRecipients = []RecipientRef{recipientA}
	if _, err := PlanMessagePublish(mentionDrift); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("unattested mention drift error = %v", err)
	}
	senderDrift := input
	senderDrift.AudienceRequest.Sender = CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()}
	if _, err := PlanMessagePublish(senderDrift); err == nil {
		t.Fatal("publication audience request replayed across senders")
	}
	sourceDrift := input
	sourceDrift.SourceKind = RouteSourceSystemEvent
	sourceDrift.EventType = "work.escalated"
	sourceDrift.AudienceRequest.SourceKind = sourceDrift.SourceKind
	sourceDrift.AudienceRequest.EventType = sourceDrift.EventType
	if _, err := PlanMessagePublish(sourceDrift); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("unattested publication source/event drift error = %v", err)
	}
	labelContentDrift := input
	labelContentDrift.Draft.LabelsJSON = json.RawMessage(`{"audience":"critical"}`)
	labelDigest := sha256.Sum256(labelContentDrift.Draft.LabelsJSON)
	labelContentDrift.Draft.LabelsHash = labelDigest[:]
	labelContentDrift.AudienceRequest.LabelsJSON = labelContentDrift.Draft.LabelsJSON
	labelContentDrift.AudienceRequest.LabelsHash = labelContentDrift.Draft.LabelsHash
	allowedValues := json.RawMessage(`["critical"]`)
	allowedValuesDigest := sha256.Sum256(allowedValues)
	labelContentDrift.Labels.Definitions = []ChannelLabelDefinition{{
		MutableCommunicationEntity: communicationStateTestMutable(scope, dbNow),
		ChannelID:                  channel.ID,
		Key:                        "audience",
		Generation:                 1,
		AllowedValuesJSON:          allowedValues,
		ValuesHash:                 allowedValuesDigest[:],
		Classification:             ChannelLabelNonSensitive,
		State:                      ChannelLabelActive,
	}}
	if _, err := PlanMessagePublish(labelContentDrift); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("locally rehashed but unattested label drift error = %v", err)
	}
	postdatedRequest := input
	postdatedRequest.AudienceRequest.RequestedAt = snapshot.ObservedAt.Add(time.Nanosecond)
	postdatedRequest.AudienceAttestation.RequestHash, err = CanonicalPublicationAudienceRequestHash(
		postdatedRequest.AudienceRequest,
	)
	if err != nil {
		t.Fatalf("canonical postdated request: %v", err)
	}
	if _, err := PlanMessagePublish(postdatedRequest); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("postdated preflight request error = %v", err)
	}
	protectionDrift := input
	protectionDrift.AudienceRequest.ProtectionGeneration++
	if _, err := PlanMessagePublish(protectionDrift); err == nil {
		t.Fatal("publication audience request crossed Channel protection generation")
	}
	plannedDelivery := make(map[RecipientRef]MessageDelivery, len(plan.Deliveries))
	for _, delivery := range plan.Deliveries {
		plannedDelivery[delivery.Recipient] = delivery
	}
	if got := plannedDelivery[recipientA]; got.Required || got.WakePolicy != WakeNone ||
		!reflect.DeepEqual(got.RouteReasons, []RouteReason{"direct", "subscriber_optional"}) {
		t.Fatalf("optional subscriber A folded incorrectly: %#v", got)
	}
	if got := plannedDelivery[recipientB]; !got.Required || got.WakePolicy != WakeAll ||
		!reflect.DeepEqual(got.RouteReasons,
			[]RouteReason{"group", "subscriber_critical", "subscriber_secondary", "workspace"}) {
		t.Fatalf("critical subscriber B folded incorrectly: %#v", got)
	}
	requiredDegrade := input
	requiredDegrade.Snapshot.Contributions = append(
		[]ResolvedAudienceContribution(nil), input.Snapshot.Contributions...,
	)
	for index := range requiredDegrade.Snapshot.Contributions {
		if requiredDegrade.Snapshot.Contributions[index].SelectorOrdinal == 2 {
			requiredDegrade.Snapshot.Contributions[index].Required = false
		}
	}
	requiredDegrade.Contributions = append(
		[]MessageAudienceRecipient(nil), input.Contributions...,
	)
	for index := range requiredDegrade.Contributions {
		if requiredDegrade.Contributions[index].MessageAudienceID == input.Audiences[1].ID {
			requiredDegrade.Contributions[index].Required = false
		}
	}
	requiredDegrade.Audiences = append([]MessageAudience(nil), input.Audiences...)
	degradedRows := make([]MessageAudienceRecipient, 0, 1)
	for _, contribution := range requiredDegrade.Contributions {
		if contribution.MessageAudienceID == requiredDegrade.Audiences[1].ID {
			degradedRows = append(degradedRows, contribution)
		}
	}
	requiredDegrade.Audiences[1].ResolvedHash, err = canonicalResolvedAudienceHash(
		requiredDegrade.Audiences[1], degradedRows,
	)
	if err != nil {
		t.Fatalf("canonical required-degrade control: %v", err)
	}
	degradedSnapshotRaw, err := canonicalJSON(requiredDegrade.Snapshot)
	if err != nil {
		t.Fatalf("canonical degraded snapshot: %v", err)
	}
	degradedSnapshotHash := sha256.Sum256(degradedSnapshotRaw)
	requiredDegrade.AudienceAttestation.SnapshotHash = degradedSnapshotHash[:]
	if _, err := PlanMessagePublish(requiredDegrade); err == nil {
		t.Fatal("required selector accepted an effective optional recipient contribution")
	}
	boundaryFresh := input
	boundaryFresh.Snapshot.FreshUntil = dbNow
	boundaryFresh.AudienceAttestation.FreshUntil = dbNow
	boundaryFresh.AudienceAttestation.SnapshotHash, err = CanonicalPublicationAudienceSnapshotHash(
		boundaryFresh.Snapshot,
	)
	if err != nil {
		t.Fatalf("canonical freshness-boundary snapshot: %v", err)
	}
	if _, err := PlanMessagePublish(boundaryFresh); err != nil {
		t.Fatalf("snapshot fresh exactly through mutation DB time denied: %v", err)
	}
	overDepth := input
	overDepth.Draft.AutomationDepth = channel.MaxAutomationDepth + 1
	if effects, err := PlanMessagePublish(overDepth); !errors.Is(err, ErrInvalidCommunicationTransition) ||
		!reflect.DeepEqual(effects, MessagePublishPlan{}) {
		t.Fatalf("automation loop above Channel ceiling planned effects: %#v, %v", effects, err)
	}
	atDepthCeiling := input
	atDepthCeiling.Draft.AutomationDepth = channel.MaxAutomationDepth
	if _, err := PlanMessagePublish(atDepthCeiling); err != nil {
		t.Fatalf("automation depth at exact Channel ceiling denied: %v", err)
	}
	deadlineCases := []struct {
		name   string
		mutate func(*MessagePublishInput)
	}{
		{name: "availability before DB time", mutate: func(candidate *MessagePublishInput) {
			staleAt := dbNow.Add(-time.Nanosecond)
			candidate.Draft.AvailableAt = staleAt
			candidate.Deliveries = append([]MessageDelivery(nil), input.Deliveries...)
			for index := range candidate.Deliveries {
				candidate.Deliveries[index].AvailableAt = staleAt
			}
		}},
		{name: "ack deadline before DB time", mutate: func(candidate *MessagePublishInput) {
			staleAvailableAt := dbNow.Add(-2 * time.Nanosecond)
			staleAckDueAt := dbNow.Add(-time.Nanosecond)
			candidate.Draft.AvailableAt = staleAvailableAt
			candidate.Draft.AckDueAt = &staleAckDueAt
			candidate.Deliveries = append([]MessageDelivery(nil), input.Deliveries...)
			for index := range candidate.Deliveries {
				candidate.Deliveries[index].AvailableAt = staleAvailableAt
				if candidate.Deliveries[index].Required {
					candidate.Deliveries[index].AckDueAt = &staleAckDueAt
				}
			}
		}},
		{name: "expiry at DB time", mutate: func(candidate *MessagePublishInput) {
			boundaryAckDueAt := dbNow
			candidate.Draft.AckDueAt = &boundaryAckDueAt
			candidate.Draft.ExpiresAt = &dbNow
			candidate.Deliveries = append([]MessageDelivery(nil), input.Deliveries...)
			for index := range candidate.Deliveries {
				candidate.Deliveries[index].ExpiresAt = &dbNow
				if candidate.Deliveries[index].Required {
					candidate.Deliveries[index].AckDueAt = &boundaryAckDueAt
				}
			}
		}},
	}
	for _, testCase := range deadlineCases {
		t.Run("publish temporal window/"+testCase.name, func(t *testing.T) {
			candidate := input
			testCase.mutate(&candidate)
			if effects, err := PlanMessagePublish(candidate); !errors.Is(err, ErrInvalidCommunicationTransition) ||
				!reflect.DeepEqual(effects, MessagePublishPlan{}) {
				t.Fatalf("elapsed publish window planned effects: %#v, %v", effects, err)
			}
		})
	}
	ackBoundary := input
	ackBoundary.Draft.AckDueAt = &dbNow
	ackBoundary.Deliveries = append([]MessageDelivery(nil), input.Deliveries...)
	for index := range ackBoundary.Deliveries {
		if ackBoundary.Deliveries[index].Required {
			ackBoundary.Deliveries[index].AckDueAt = &dbNow
		}
	}
	if effects, err := PlanMessagePublish(ackBoundary); !errors.Is(err, ErrInvalidCommunicationTransition) ||
		!reflect.DeepEqual(effects, MessagePublishPlan{}) {
		t.Fatalf("publish materialized required Delivery at elapsed ack_due_at boundary: %#v, %v", effects, err)
	}
	inputAfter, err := canonicalJSON(input)
	if err != nil || !bytes.Equal(inputBefore, inputAfter) {
		t.Fatalf("publish planner mutated locked input: before=%x after=%x err=%v", inputBefore, inputAfter, err)
	}
	parent := communicationStateTestMessage(t, scope, AckPolicyNone, 0, createdAt)
	parent.ChannelID = channel.ID
	reply := input
	reply.Draft.ReplyToID = parent.ID
	reply.Draft.ThreadID = parent.ThreadID
	reply.Draft.WorkItemID = parent.WorkItemID
	reply.Parent = &parent
	if replyPlan, err := PlanMessagePublish(reply); err != nil ||
		replyPlan.After.ReplyToID != parent.ID || replyPlan.After.ThreadID != parent.ThreadID {
		t.Fatalf("exact reply lineage denied: %#v, %v", replyPlan, err)
	}
	rootWithParent := input
	rootWithParent.Parent = &parent
	if _, err := PlanMessagePublish(rootWithParent); err == nil {
		t.Fatal("root publish accepted an unrelated parent witness")
	}
	missingParent := reply
	missingParent.Parent = nil
	if _, err := PlanMessagePublish(missingParent); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("reply without locked parent error = %v", err)
	}
	crossChannel := reply
	crossChannelParent := parent
	crossChannelParent.ChannelID = model.NewID()
	crossChannel.Parent = &crossChannelParent
	if _, err := PlanMessagePublish(crossChannel); err == nil {
		t.Fatal("reply crossed parent Channel")
	}
	crossWorkspace := reply
	crossWorkspaceParent := parent
	crossWorkspaceParent.WorkspaceID = model.NewID()
	crossWorkspace.Parent = &crossWorkspaceParent
	if _, err := PlanMessagePublish(crossWorkspace); err == nil {
		t.Fatal("reply crossed parent workspace")
	}
	crossThread := reply
	crossThread.Draft.ThreadID = model.NewID()
	if _, err := PlanMessagePublish(crossThread); err == nil {
		t.Fatal("reply selected a thread other than its parent")
	}
	crossWorkItem := reply
	crossWorkItem.Draft.WorkItemID = model.NewID()
	if _, err := PlanMessagePublish(crossWorkItem); err == nil {
		t.Fatal("reply changed the parent's NULL WorkItem lineage")
	}
	wrongParentID := reply
	otherParent := parent
	otherParent.ID = model.NewID()
	otherParent.ThreadID = otherParent.ID
	wrongParentID.Parent = &otherParent
	if _, err := PlanMessagePublish(wrongParentID); err == nil {
		t.Fatal("reply accepted a parent whose ID differs from ReplyToID")
	}
	notLegible := reply
	futureParent := parent
	futureParent.AvailableAt = dbNow.Add(time.Minute)
	notLegible.Parent = &futureParent
	if _, err := PlanMessagePublish(notLegible); !errors.Is(err, ErrCommunicationNotFound) {
		t.Fatalf("reply to non-legible parent error = %v", err)
	}
	recipientBRows := make([]MessageAudienceRecipient, 0, 4)
	for _, contribution := range plan.Contributions {
		if contribution.Recipient == recipientB {
			recipientBRows = append(recipientBRows, contribution)
		}
	}
	fold, err := FoldAudienceContributions(recipientBRows)
	if err != nil {
		t.Fatalf("overlapping publish fold: %v", err)
	}
	if !fold.Required || fold.WakePolicy != WakeAll ||
		!reflect.DeepEqual(fold.RouteReasons,
			[]RouteReason{"group", "subscriber_critical", "subscriber_secondary", "workspace"}) ||
		len(fold.RouteReasonsHash) != sha256.Size || len(fold.ContributionsHash) != sha256.Size {
		t.Fatalf("publish fold lost reasons or provenance digests: %#v", fold)
	}
	wantAudienceHash, err := CanonicalMessageAudienceHash(plan.After, plan.Audiences, plan.Contributions)
	if err != nil || !bytes.Equal(wantAudienceHash, plan.After.AudienceHash) {
		t.Fatalf("publish audience seal mismatch: %x, %v", plan.After.AudienceHash, err)
	}
	agentPrincipal := CommunicationPrincipal{AgentExternalID: "provider-agent-42"}
	agentRecipient := RecipientSnapshot{
		Scope: scope, Recipient: RecipientRef{Kind: RecipientAgent, Ref: model.NewID().String()},
		RecipientEpoch: 5, DirectoryEpoch: snapshot.Epoch, Eligible: true,
	}
	agentPublishInput := func(outcome PrincipalResolutionOutcome) MessagePublishInput {
		candidate := input
		actor := CommunicationActorRef{Kind: ActorAgent, Ref: agentRecipient.Recipient.Ref}
		candidate.Principal = agentPrincipal
		candidate.Sender = actor
		candidate.Draft.Sender = actor
		candidate.AudienceRequest.Sender = actor
		requestHash, err := CanonicalPublicationAudienceRequestHash(candidate.AudienceRequest)
		if err != nil {
			t.Fatalf("canonical Agent publication audience request: %v", err)
		}
		candidate.AudienceAttestation.RequestHash = requestHash
		candidate.SendGate.Principal = agentPrincipal
		candidate.SendGate.Core.Principal = agentPrincipal
		candidate.SendGate.CurrentChannelWriteGrant.Principal = agentPrincipal
		resolution := PrincipalResolution{
			Outcome: outcome, Code: string(outcome), Scope: scope, Principal: agentPrincipal,
			ObservedAt: dbNow, FreshUntil: dbNow.Add(time.Minute),
		}
		if outcome == PrincipalResolved {
			resolution.Recipient = &agentRecipient
		}
		candidate.SenderResolution = &resolution
		return candidate
	}
	if agentPlan, err := PlanMessagePublish(agentPublishInput(PrincipalResolved)); err != nil ||
		agentPlan.After.Sender != (CommunicationActorRef{Kind: ActorAgent, Ref: agentRecipient.Recipient.Ref}) {
		t.Fatalf("resolved external Agent sender denied: %#v, %v", agentPlan, err)
	}
	unknownPlan, err := PlanMessagePublish(agentPublishInput(PrincipalUnknown))
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(unknownPlan, MessagePublishPlan{}) {
		t.Fatalf("unknown Agent sender resolution planned effects: %#v, %v", unknownPlan, err)
	}
	notFoundPlan, err := PlanMessagePublish(agentPublishInput(PrincipalNotFound))
	if !errors.Is(err, ErrCommunicationNotFound) ||
		!reflect.DeepEqual(notFoundPlan, MessagePublishPlan{}) {
		t.Fatalf("not-found Agent sender resolution planned effects: %#v, %v", notFoundPlan, err)
	}

	systemPrincipal := CommunicationPrincipal{
		System: true, SystemActorRef: "automation-deadline-worker", SystemGrantAgentID: model.NewID(),
	}
	if err := ValidateCommunicationPrincipal(CommunicationPrincipal{System: true}); err == nil {
		t.Fatal("system principal without canonical Actor/Agent backing was accepted")
	}
	if err := ValidateCommunicationPrincipal(CommunicationPrincipal{
		UserID: model.NewID(), SystemActorRef: systemPrincipal.SystemActorRef,
		SystemGrantAgentID: systemPrincipal.SystemGrantAgentID,
	}); err == nil {
		t.Fatal("non-system principal carried a system authority binding")
	}
	systemActor := CommunicationActorRef{Kind: ActorSystem, Ref: systemPrincipal.SystemActorRef}
	systemSubject := CommunicationSubjectRef{Kind: SubjectAgent, Ref: systemPrincipal.SystemGrantAgentID.String()}
	systemGrant := ChannelGrant{
		MutableCommunicationEntity: communicationStateTestMutable(scope, createdAt),
		ChannelID:                  channel.ID, Subject: systemSubject, Generation: 1, CanWrite: true,
		State: ChannelGrantActive, GrantedBy: CommunicationActorRef{Kind: ActorSystem, Ref: "grant-authority"},
	}
	systemClosure := ChannelGrantSubjectClosure{
		Scope: scope, Principal: systemPrincipal, DirectoryEpoch: snapshot.Epoch,
		Outcome: ReadAllow, Code: "resolved", Subjects: []CommunicationSubjectRef{systemSubject},
		ObservedAt: dbNow, FreshUntil: dbNow.Add(time.Minute), EvidenceRef: "system-agent-closure",
	}
	systemWrite := EvaluateCurrentChannelGrant(ChannelGrantSnapshot{
		Verdict: VerdictClean, Code: "complete", ACLRevision: channel.ACLRevision,
		ObservedAt: dbNow, Grants: []ChannelGrant{systemGrant},
	}, scope.TenantID, scope.WorkspaceID, channel.ID, systemClosure, ChannelGrantWrite, dbNow)
	if evidenceVerdict(systemWrite.Evidence) != VerdictClean || systemWrite.GrantID != systemGrant.ID {
		t.Fatalf("Agent/NHI-backed system grant denied: %#v", systemWrite)
	}
	systemGroup := CommunicationSubjectRef{Kind: SubjectAgentGroup, Ref: model.NewID().String()}
	systemGroupGrant := systemGrant
	systemGroupGrant.ID = model.NewID()
	systemGroupGrant.Subject = systemGroup
	systemClosureWithGroup := systemClosure
	systemClosureWithGroup.Subjects = []CommunicationSubjectRef{systemSubject, systemGroup}
	derivedGroupWrite := EvaluateCurrentChannelGrant(ChannelGrantSnapshot{
		Verdict: VerdictClean, Code: "complete", ACLRevision: channel.ACLRevision,
		ObservedAt: dbNow, Grants: []ChannelGrant{systemGroupGrant},
	}, scope.TenantID, scope.WorkspaceID, channel.ID,
		systemClosureWithGroup, ChannelGrantWrite, dbNow)
	if evidenceVerdict(derivedGroupWrite.Evidence) != VerdictClean ||
		derivedGroupWrite.GrantID != systemGroupGrant.ID {
		t.Fatalf("system-backed Agent lost derived group grant: %#v", derivedGroupWrite)
	}
	systemInput := input
	systemInput.Principal = systemPrincipal
	systemInput.Sender = systemActor
	systemInput.Draft.Sender = systemActor
	systemInput.AudienceRequest.Sender = systemActor
	systemInput.AudienceAttestation.RequestHash, err = CanonicalPublicationAudienceRequestHash(
		systemInput.AudienceRequest,
	)
	if err != nil {
		t.Fatalf("canonical system publication audience request: %v", err)
	}
	systemInput.SendGate.Principal = systemPrincipal
	systemInput.SendGate.Core.Principal = systemPrincipal
	systemInput.SendGate.CurrentChannelWriteGrant = systemWrite
	if systemPlan, err := PlanMessagePublish(systemInput); err != nil || systemPlan.After.Sender != systemActor {
		t.Fatalf("Agent/NHI-backed system publish denied: %#v, %v", systemPlan, err)
	}
	missingDirect := systemClosure
	missingDirect.Subjects = []CommunicationSubjectRef{{Kind: SubjectAgentGroup, Ref: model.NewID().String()}}
	missingSystemBinding := EvaluateCurrentChannelGrant(ChannelGrantSnapshot{
		Verdict: VerdictClean, Code: "complete", ACLRevision: channel.ACLRevision,
		ObservedAt: dbNow, Grants: []ChannelGrant{systemGrant},
	}, scope.TenantID, scope.WorkspaceID, channel.ID, missingDirect, ChannelGrantWrite, dbNow)
	if evidenceVerdict(missingSystemBinding.Evidence) != VerdictUnknown {
		t.Fatalf("system closure without direct backing Agent was accepted: %#v", missingSystemBinding)
	}
	wrongSystemActor := systemInput
	wrongSystemActor.Sender = CommunicationActorRef{Kind: ActorSystem, Ref: "different-system-worker"}
	wrongSystemActor.Draft.Sender = wrongSystemActor.Sender
	wrongSystemActor.AudienceRequest.Sender = wrongSystemActor.Sender
	wrongSystemActor.AudienceAttestation.RequestHash, err = CanonicalPublicationAudienceRequestHash(
		wrongSystemActor.AudienceRequest,
	)
	if err != nil {
		t.Fatalf("canonical wrong-system publication request: %v", err)
	}
	if effects, err := PlanMessagePublish(wrongSystemActor); err == nil ||
		!reflect.DeepEqual(effects, MessagePublishPlan{}) {
		t.Fatalf("system publish accepted a non-bound ActorSystem ref: %#v, %v", effects, err)
	}

	stale := input
	stale.Snapshot.FreshUntil = dbNow.Add(-500 * time.Millisecond)
	stale.AudienceAttestation.FreshUntil = stale.Snapshot.FreshUntil
	stale.AudienceAttestation.SnapshotHash, err = CanonicalPublicationAudienceSnapshotHash(stale.Snapshot)
	if err != nil {
		t.Fatalf("canonical stale snapshot: %v", err)
	}
	if _, err := PlanMessagePublish(stale); !errors.Is(err, ErrCommunicationSnapshotStale) {
		t.Fatalf("stale snapshot error = %v, want SnapshotStale", err)
	}
	futureSnapshot := input
	futureSnapshot.Snapshot.ObservedAt = dbNow.Add(time.Nanosecond)
	futureSnapshot.Snapshot.FreshUntil = dbNow.Add(time.Minute)
	futureSnapshot.AudienceAttestation.ObservedAt = futureSnapshot.Snapshot.ObservedAt
	futureSnapshot.AudienceAttestation.FreshUntil = futureSnapshot.Snapshot.FreshUntil
	futureSnapshot.AudienceAttestation.SnapshotHash, err = CanonicalPublicationAudienceSnapshotHash(
		futureSnapshot.Snapshot,
	)
	if err != nil {
		t.Fatalf("canonical future snapshot: %v", err)
	}
	if _, err := PlanMessagePublish(futureSnapshot); !errors.Is(err, ErrCommunicationSnapshotStale) {
		t.Fatalf("future audience attestation error = %v, want SnapshotStale", err)
	}
	epochAfter := input
	epochAfter.SendGate.DirectoryEpoch.Version = input.Snapshot.Epoch + 1
	epochAfter.SendGate.CurrentChannelWriteGrant.DirectoryEpoch = input.Snapshot.Epoch + 1
	if _, err := PlanMessagePublish(epochAfter); !errors.Is(err, ErrCommunicationSnapshotStale) {
		t.Fatalf("directory epoch advanced after roster error = %v, want SnapshotStale", err)
	}
	missingArc := input
	missingArc.Contributions = append(
		[]MessageAudienceRecipient(nil), input.Contributions[:len(input.Contributions)-1]...,
	)
	if _, err := PlanMessagePublish(missingArc); err == nil {
		t.Fatal("publish accepted one missing authoritative arc while the same recipient still had another")
	}
	routeDrift := input
	routeDrift.Contributions = append([]MessageAudienceRecipient(nil), input.Contributions...)
	for index := range routeDrift.Contributions {
		if routeDrift.Contributions[index].MessageAudienceID == input.Audiences[3].ID &&
			routeDrift.Contributions[index].Recipient == recipientB {
			routeDrift.Contributions[index].RouteRuleGeneration++
			routeDrift.Contributions[index] = communicationStateTestSealCausalArc(
				routeDrift.Contributions[index],
			)
		}
	}
	routeDrift.Audiences = append([]MessageAudience(nil), input.Audiences...)
	driftRows := make([]MessageAudienceRecipient, 0, 2)
	for _, contribution := range routeDrift.Contributions {
		if contribution.MessageAudienceID == routeDrift.Audiences[3].ID {
			driftRows = append(driftRows, contribution)
		}
	}
	routeDrift.Audiences[3].ResolvedHash, err = canonicalResolvedAudienceHash(
		routeDrift.Audiences[3], driftRows,
	)
	if err != nil {
		t.Fatalf("canonical route-drift control: %v", err)
	}
	if _, err := PlanMessagePublish(routeDrift); err == nil {
		t.Fatal("publish accepted route generation not present in authoritative snapshot")
	}
	wrongDelivery := input
	wrongDelivery.Deliveries = append([]MessageDelivery(nil), input.Deliveries...)
	wrongDelivery.Deliveries[0].RouteReasons = []RouteReason{"group", "workspace"}
	if _, err := PlanMessagePublish(wrongDelivery); err == nil {
		t.Fatal("publish accepted Delivery with incomplete folded route reasons")
	}
}

func TestPublishSenderExclusionUsesIdentityNamespace(t *testing.T) {
	t.Parallel()

	sharedID := model.NewID().String()
	if !actorMatchesRecipient(
		CommunicationActorRef{Kind: ActorUser, Ref: sharedID},
		RecipientRef{Kind: RecipientUser, Ref: sharedID},
	) {
		t.Fatal("exact User sender was not recognized")
	}
	for _, vector := range []struct {
		actor     CommunicationActorRef
		recipient RecipientRef
	}{
		{actor: CommunicationActorRef{Kind: ActorUser, Ref: sharedID},
			recipient: RecipientRef{Kind: RecipientAgent, Ref: sharedID}},
		{actor: CommunicationActorRef{Kind: ActorAgent, Ref: sharedID},
			recipient: RecipientRef{Kind: RecipientUser, Ref: sharedID}},
		{actor: CommunicationActorRef{Kind: ActorSystem, Ref: sharedID},
			recipient: RecipientRef{Kind: RecipientAgent, Ref: sharedID}},
	} {
		if actorMatchesRecipient(vector.actor, vector.recipient) {
			t.Fatalf("sender exclusion crossed identity namespaces: %#v", vector)
		}
	}
}

func TestCausalWitnessBindsExactCurrentRelation(t *testing.T) {
	t.Parallel()

	type causalCase struct {
		name          string
		recipientKind RecipientKind
		selector      func(DirectoryScopeRef) AudienceSelector
		causalKind    AudienceCausalKind
		causalRef     func(DirectoryScopeRef, AudienceSelector) string
		factKind      model.Kind
		authorityKind CausalAuthorityKind
		subject       func(RecipientRef, AudienceSelector) CommunicationSubjectRef
		subscriber    bool
	}
	cases := []causalCase{
		{name: "user_group", recipientKind: RecipientUser,
			selector: func(_ DirectoryScopeRef) AudienceSelector {
				return AudienceSelector{Kind: AudienceUserGroup, Ref: model.NewID().String(), WakePolicy: WakeNone}
			}, causalKind: CausalUserGroup,
			causalRef: func(_ DirectoryScopeRef, selector AudienceSelector) string { return selector.Ref },
			factKind:  model.Kind("core.user_group_member"), authorityKind: CausalAuthorityUserGroup,
			subject: func(_ RecipientRef, selector AudienceSelector) CommunicationSubjectRef {
				return CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: selector.Ref}
			}},
		{name: "agent_group", recipientKind: RecipientAgent,
			selector: func(_ DirectoryScopeRef) AudienceSelector {
				return AudienceSelector{Kind: AudienceAgentGroup, Ref: model.NewID().String(), WakePolicy: WakeNone}
			}, causalKind: CausalAgentGroup,
			causalRef: func(_ DirectoryScopeRef, selector AudienceSelector) string { return selector.Ref },
			factKind:  model.Kind("core.agent_group_member"), authorityKind: CausalAuthorityAgentGroup,
			subject: func(_ RecipientRef, selector AudienceSelector) CommunicationSubjectRef {
				return CommunicationSubjectRef{Kind: SubjectAgentGroup, Ref: selector.Ref}
			}},
		{name: "workspace_user", recipientKind: RecipientUser,
			selector: func(_ DirectoryScopeRef) AudienceSelector {
				return AudienceSelector{Kind: AudienceWorkspaceMembers, WakePolicy: WakeNone}
			}, causalKind: CausalWorkspaceMember,
			causalRef: func(scope DirectoryScopeRef, _ AudienceSelector) string { return scope.WorkspaceID.String() },
			factKind:  model.Kind("core.membership"), authorityKind: CausalAuthorityWorkspaceUser,
			subject: func(recipient RecipientRef, _ AudienceSelector) CommunicationSubjectRef {
				return CommunicationSubjectRef{Kind: SubjectUser, Ref: recipient.Ref}
			}},
		{name: "workspace_agent", recipientKind: RecipientAgent,
			selector: func(_ DirectoryScopeRef) AudienceSelector {
				return AudienceSelector{Kind: AudienceWorkspaceMembers, WakePolicy: WakeNone}
			}, causalKind: CausalWorkspaceMember,
			causalRef: func(scope DirectoryScopeRef, _ AudienceSelector) string { return scope.WorkspaceID.String() },
			factKind:  model.Kind("core.agent"), authorityKind: CausalAuthorityWorkspaceAgent,
			subject: func(recipient RecipientRef, _ AudienceSelector) CommunicationSubjectRef {
				return CommunicationSubjectRef{Kind: SubjectAgent, Ref: recipient.Ref}
			}},
		{name: "subscriber_group", recipientKind: RecipientUser,
			selector: func(_ DirectoryScopeRef) AudienceSelector {
				return AudienceSelector{Kind: AudienceSubscribers, WakePolicy: WakeNone}
			}, causalKind: CausalSubscriber,
			causalRef: func(_ DirectoryScopeRef, _ AudienceSelector) string { return model.NewID().String() },
			factKind:  model.Kind("core.user_group_member"), authorityKind: CausalAuthoritySubscriber,
			subject: func(_ RecipientRef, selector AudienceSelector) CommunicationSubjectRef {
				return CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: selector.Ref}
			}, subscriber: true},
		{name: "subscriber_agent_group", recipientKind: RecipientAgent,
			selector: func(_ DirectoryScopeRef) AudienceSelector {
				return AudienceSelector{Kind: AudienceSubscribers, WakePolicy: WakeNone}
			}, causalKind: CausalSubscriber,
			causalRef: func(_ DirectoryScopeRef, _ AudienceSelector) string { return model.NewID().String() },
			factKind:  model.Kind("core.agent_group_member"), authorityKind: CausalAuthoritySubscriber,
			subject: func(_ RecipientRef, selector AudienceSelector) CommunicationSubjectRef {
				return CommunicationSubjectRef{Kind: SubjectAgentGroup, Ref: selector.Ref}
			}, subscriber: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			scope := communicationStateTestScope()
			recipient := RecipientRef{Kind: test.recipientKind, Ref: model.NewID().String()}
			message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
			delivery := communicationStateTestDelivery(message, recipient, 1, false)
			principal := CommunicationPrincipal{UserID: model.NewID()}
			current := communicationStateTestReadEvidence(
				scope, message, delivery, principal, communicationTestNow,
			).CurrentAudience
			selector := test.selector(scope)
			causalRef := test.causalRef(scope, selector)
			if test.subscriber {
				selector.Ref = ""
			}
			audience := current.Contributions[0].Audience
			audience.Selector = selector
			selectorRaw, _ := canonicalJSON(selector)
			selectorHash := sha256.Sum256(selectorRaw)
			audience.SelectorHash = selectorHash[:]
			row := current.Contributions[0].Contribution
			row.Selector = selector
			row.CausalKind = test.causalKind
			row.CausalRef = causalRef
			row.CausalFactKind = test.factKind
			row.CausalFactID = model.NewID()
			row.CausalFactVersion = 1
			var originalSubject CommunicationSubjectRef
			if test.subscriber {
				originalKind := SubjectUserGroup
				if test.recipientKind == RecipientAgent {
					originalKind = SubjectAgentGroup
				}
				originalSubject = CommunicationSubjectRef{Kind: originalKind, Ref: causalRef}
				row.OriginalSubscriber = &originalSubject
				row.SubscriptionID = model.NewID()
				row.SubscriptionGeneration = 1
			}
			row = communicationStateTestSealCausalArc(row)
			audience.ResolvedHash, _ = canonicalResolvedAudienceHash(
				audience, []MessageAudienceRecipient{row},
			)
			currentFact := store.AuthorizationFactRef{Kind: test.factKind, ID: model.NewID(), Version: 8}
			subject := test.subject(recipient, selector)
			if test.subscriber {
				subject = originalSubject
			}
			relation := CausalRelationWitness{
				Scope: scope, Recipient: recipient, Subject: subject, CausalKind: test.causalKind,
				CausalRef: causalRef, DirectoryEpoch: current.DirectoryEpoch,
				SubscriptionID: row.SubscriptionID, SubscriptionGeneration: row.SubscriptionGeneration,
				CurrentFact: &currentFact,
			}
			witness := CausalAuthorityWitness{
				Kind: test.authorityKind, Scope: scope, ContributionID: row.ID, Recipient: recipient,
				CausalKind: test.causalKind, CausalRef: causalRef, CurrentFact: &currentFact,
				CurrentRelation: &relation, DirectoryEpoch: current.DirectoryEpoch,
				ObservedAt: current.ObservedAt,
				Evidence:   AuthorityEvidence{Verdict: VerdictClean, Code: "current", EvidenceRef: "relation-current"},
			}
			current.Contributions = []CausalContributionEvidence{{
				Audience: audience, Contribution: row, Witness: witness,
			}}
			communicationStateTestRebindCurrentAudienceSet(&current, message.Version)
			if got := EvaluateCurrentAudienceDetailed(current); got.Verdict != VerdictClean ||
				!reflect.DeepEqual(got.SurvivingContributionIDs, []model.ID{row.ID}) {
				t.Fatalf("re-added exact current relation denied: %#v", got)
			}
			unrelated := current
			unrelated.Contributions = append([]CausalContributionEvidence(nil), current.Contributions...)
			wrong := relation
			wrong.Subject.Ref = model.NewID().String()
			unrelated.Contributions[0].Witness.CurrentRelation = &wrong
			if got := EvaluateCurrentAudienceDetailed(unrelated); got.Verdict == VerdictClean {
				t.Fatalf("unrelated same-kind fact authorized exact relation: %#v", got)
			}
			ineligible := current
			ineligible.RecipientEligible.Evidence = AuthorityEvidence{
				Verdict: VerdictBroken, Code: "ineligible", EvidenceRef: "directory-current",
			}
			if got := EvaluateCurrentAudienceDetailed(ineligible); got.Verdict != VerdictBroken {
				t.Fatalf("ineligible indirect recipient retained authority: %#v", got)
			}
		})
	}
}

func TestCurrentAudienceSetBindsEveryCausalArcAndSelectsOne(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	recipient := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, recipient, 1, false)
	principal := CommunicationPrincipal{UserID: model.ID(recipient.Ref)}
	current := communicationStateTestReadEvidence(
		scope, message, delivery, principal, communicationTestNow,
	).CurrentAudience
	selector := AudienceSelector{Kind: AudienceSubscribers, WakePolicy: WakeNone}
	selectorRaw, _ := canonicalJSON(selector)
	selectorHash := sha256.Sum256(selectorRaw)
	audience := current.Contributions[0].Audience
	audience.Selector = selector
	audience.SelectorHash = selectorHash[:]
	audience.ResolvedCount = 1

	rows := make([]MessageAudienceRecipient, 2)
	witnesses := make([]CausalAuthorityWitness, 2)
	for index := range rows {
		groupID := model.NewID()
		row := current.Contributions[0].Contribution
		row.ID = model.NewID()
		row.Selector = selector
		row.CausalKind = CausalSubscriber
		row.CausalRef = groupID.String()
		row.CausalFactKind = model.Kind("core.user_group_member")
		row.CausalFactID = model.NewID()
		row.CausalFactVersion = 1
		row.OriginalSubscriber = &CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: groupID.String()}
		row.SubscriptionID = model.NewID()
		row.SubscriptionGeneration = 1
		row.Required = index == 1
		row.WakePolicy = []WakePolicy{WakeNone, WakeAll}[index]
		row.RouteReasons = []RouteReason{RouteReason(fmt.Sprintf("subscriber-g%d", index+1))}
		row = communicationStateTestSealCausalArc(row)
		rows[index] = row
		currentFact := store.AuthorizationFactRef{
			Kind: model.Kind("core.user_group_member"), ID: model.NewID(), Version: 8,
		}
		witnesses[index] = CausalAuthorityWitness{
			Kind: CausalAuthoritySubscriber, Scope: scope, ContributionID: row.ID,
			Recipient: recipient, CausalKind: row.CausalKind, CausalRef: row.CausalRef,
			CurrentFact: &currentFact,
			CurrentRelation: &CausalRelationWitness{
				Scope: scope, Recipient: recipient, Subject: *row.OriginalSubscriber,
				CausalKind: row.CausalKind, CausalRef: row.CausalRef,
				DirectoryEpoch: current.DirectoryEpoch, SubscriptionID: row.SubscriptionID,
				SubscriptionGeneration: row.SubscriptionGeneration, CurrentFact: &currentFact,
			},
			DirectoryEpoch: current.DirectoryEpoch, ObservedAt: current.ObservedAt,
			Evidence: AuthorityEvidence{
				Verdict: VerdictClean, Code: "current", EvidenceRef: fmt.Sprintf("subscriber-g%d", index+1),
			},
		}
	}
	audience.ResolvedHash, _ = canonicalResolvedAudienceHash(audience, rows)
	current.Contributions = []CausalContributionEvidence{
		{Audience: audience, Contribution: rows[0], Witness: witnesses[0]},
		{Audience: audience, Contribution: rows[1], Witness: witnesses[1]},
	}
	communicationStateTestRebindCurrentAudienceSet(&current, message.Version)
	wantSelected := rows[0].ID
	if bytes.Compare(rows[1].CausalArcHash, rows[0].CausalArcHash) < 0 {
		wantSelected = rows[1].ID
	}
	decision := EvaluateCurrentAudienceDetailed(current)
	if decision.Verdict != VerdictClean ||
		!reflect.DeepEqual(decision.SurvivingContributionIDs, []model.ID{wantSelected}) ||
		len(decision.RequiredClaims) != 0 {
		t.Fatalf("two subscriber arcs did not select one deterministic cause: %#v", decision)
	}

	oneRevoked := current
	oneRevoked.Contributions = append([]CausalContributionEvidence(nil), current.Contributions...)
	oneRevoked.Contributions[0].Witness.Evidence = AuthorityEvidence{
		Verdict: VerdictBroken, Code: "group_left", EvidenceRef: "subscriber-g1-current",
	}
	decision = EvaluateCurrentAudienceDetailed(oneRevoked)
	if decision.Verdict != VerdictClean ||
		!reflect.DeepEqual(decision.SurvivingContributionIDs, []model.ID{rows[1].ID}) {
		t.Fatalf("revoking G1 also revoked surviving G2 arc: %#v", decision)
	}
	bothRevoked := oneRevoked
	bothRevoked.Contributions = append([]CausalContributionEvidence(nil), oneRevoked.Contributions...)
	bothRevoked.Contributions[1].Witness.Evidence = AuthorityEvidence{
		Verdict: VerdictBroken, Code: "group_left", EvidenceRef: "subscriber-g2-current",
	}
	if decision = EvaluateCurrentAudienceDetailed(bothRevoked); decision.Verdict != VerdictBroken {
		t.Fatalf("all revoked subscriber arcs retained access: %#v", decision)
	}

	synthetic := current
	synthetic.Contributions = append([]CausalContributionEvidence(nil), current.Contributions...)
	synthetic.Contributions[0].Contribution.ID = model.NewID()
	synthetic.Contributions[0].Witness.ContributionID = synthetic.Contributions[0].Contribution.ID
	if decision = EvaluateCurrentAudienceDetailed(synthetic); decision.Verdict != VerdictUnknown {
		t.Fatalf("synthetic contribution outside complete set was accepted: %#v", decision)
	}
	omitted := current
	omitted.Contributions = append([]CausalContributionEvidence(nil), current.Contributions[1:]...)
	if decision = EvaluateCurrentAudienceDetailed(omitted); decision.Verdict != VerdictUnknown {
		t.Fatalf("omitted durable causal arc was accepted: %#v", decision)
	}
	replaced := current
	replaced.SetWitness.Contributions = append(
		[]MessageAudienceRecipient(nil), current.SetWitness.Contributions...,
	)
	replaced.SetWitness.Contributions[0].ID = model.NewID()
	if decision = EvaluateCurrentAudienceDetailed(replaced); decision.Verdict != VerdictUnknown {
		t.Fatalf("replacement row with stale set digest was accepted: %#v", decision)
	}
}

func TestCarrierGateUsesSameTxAudienceSetAndBoundedFacts(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	recipient := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message, recipient, 1, false)
	principal := CommunicationPrincipal{UserID: model.ID(recipient.Ref)}
	dbNow := communicationTestNow.Add(time.Minute)
	evidence := communicationStateTestReadEvidence(scope, message, delivery, principal, dbNow)

	preflightAt := dbNow.Add(-30 * time.Second)
	evidence.CurrentAudience.ObservedAt = preflightAt
	evidence.CurrentAudience.RecipientExists.ObservedAt = preflightAt
	evidence.CurrentAudience.RecipientEligible.ObservedAt = preflightAt
	evidence.CurrentAudience.RecipientNotTombstoned.ObservedAt = preflightAt
	for index := range evidence.CurrentAudience.Contributions {
		evidence.CurrentAudience.Contributions[index].Witness.ObservedAt = preflightAt
	}
	if decision, err := EvaluateCarrierGate(evidence); err != nil || decision.Verdict != VerdictClean {
		t.Fatalf("fresh preflight evidence plus same-tx audience set denied: %#v, %v", decision, err)
	}
	staleSet := evidence
	staleSet.CurrentAudience.SetWitness.ObservedAt = preflightAt
	if decision, err := EvaluateCarrierGate(staleSet); !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(decision, ProtectedReadDecision{}) {
		t.Fatalf("non-transactional audience set planned effects: %#v, %v", decision, err)
	}

	makeFacts := func(count int) []store.AuthorizationFactRef {
		facts := make([]store.AuthorizationFactRef, count)
		for index := range facts {
			facts[index] = store.AuthorizationFactRef{
				Kind: model.Kind("core.identity"), ID: model.NewID(), Version: 1,
			}
		}
		return facts
	}
	withinBound := evidence
	withinBound.Core.Facts = makeFacts(63)
	decision, err := EvaluateCarrierGate(withinBound)
	if err != nil || decision.Verdict != VerdictClean || len(decision.Facts) != 64 {
		t.Fatalf("63 Core facts plus directory epoch denied: facts=%d %#v, %v",
			len(decision.Facts), decision, err)
	}
	overBound := evidence
	overBound.Core.Facts = makeFacts(64)
	decision, err = EvaluateCarrierGate(overBound)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(decision, ProtectedReadDecision{}) {
		t.Fatalf("64 Core facts plus directory epoch was not UNKNOWN/zero: %#v, %v", decision, err)
	}
}

func TestCarrierGateValidatesActualRowsForEveryCarrier(t *testing.T) {
	t.Parallel()

	type carrierCase struct {
		name      string
		operation CommunicationOperation
		build     func(*testing.T, DirectoryScopeRef, RecipientRef, time.Time) ProtectedReadEvidence
	}
	principalForRecipient := func(scope DirectoryScopeRef, recipient RecipientRef) CommunicationPrincipal {
		if recipient.Kind == RecipientSession {
			return CommunicationPrincipal{
				SessionID: recipient.Ref, SessionRunRef: model.NewID().String(), SessionFence: 9,
				SessionWorkspaceID: scope.WorkspaceID, PurposeRestricted: true,
			}
		}
		return CommunicationPrincipal{UserID: model.ID(recipient.Ref)}
	}
	baseBuild := func(t *testing.T, scope DirectoryScopeRef, recipient RecipientRef,
		dbNow time.Time) ProtectedReadEvidence {
		message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
		delivery := communicationStateTestDelivery(message, recipient, 1, false)
		return communicationStateTestReadEvidence(
			scope, message, delivery, principalForRecipient(scope, recipient), dbNow,
		)
	}
	cases := []carrierCase{
		{name: "message", operation: CommunicationRead,
			build: func(t *testing.T, scope DirectoryScopeRef, recipient RecipientRef, dbNow time.Time) ProtectedReadEvidence {
				evidence := baseBuild(t, scope, recipient, dbNow)
				evidence.Carrier.Entity.Kind = model.Kind("sessions.message")
				evidence.Carrier.Entity.ID = evidence.Carrier.MessageID
				evidence.Core.Entity = evidence.Carrier.Entity
				evidence.EntityRecipientGuard.Carrier = evidence.Carrier
				return evidence
			}},
		{name: "delivery", operation: CommunicationDeliveryWrite, build: baseBuild},
		{name: "decision", operation: CommunicationDecisionRequestWrite,
			build: func(t *testing.T, scope DirectoryScopeRef, recipient RecipientRef, dbNow time.Time) ProtectedReadEvidence {
				message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
				message.Kind = MessageDecisionRequest
				message.WorkItemID = model.NewID()
				message.LastEventSeq = 0
				delivery := communicationStateTestDelivery(message, recipient, 1, false)
				evidence := communicationStateTestReadEvidence(
					scope, message, delivery, principalForRecipient(scope, recipient), dbNow,
				)
				request := DecisionRequest{
					MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
					MessageID:                  message.ID, WorkItemID: message.WorkItemID, DecisionKey: "approve",
					Requester: message.Sender,
					Owner: CommunicationSubjectRef{
						Kind: CommunicationSubjectKind(recipient.Kind), Ref: recipient.Ref,
					},
					State:                DecisionPending,
					Request:              communicationTestPayloadForSlot(t, PayloadSlotDecisionRequest),
					AuthorityRequirement: "work_decide", DueAt: communicationTestNow.Add(time.Hour),
				}
				evidence.Carrier.Entity.Kind = model.Kind("sessions.decision_request")
				evidence.Carrier.Entity.ID = request.ID
				evidence.Core.Entity = evidence.Carrier.Entity
				evidence.EntityRecipientGuard.Carrier = evidence.Carrier
				evidence.CarrierState.DecisionRequest = &request
				return evidence
			}},
		{name: "handoff", operation: CommunicationHandoffResponse,
			build: func(t *testing.T, scope DirectoryScopeRef, recipient RecipientRef, dbNow time.Time) ProtectedReadEvidence {
				message := communicationStateTestMessage(t, scope, AckPolicyEachRequired, 1, communicationTestNow)
				message.Kind = MessageHandoffOffer
				message.WorkItemID = model.NewID()
				message.LastEventSeq = 0
				delivery := communicationStateTestDelivery(message, recipient, 1, true)
				evidence := communicationStateTestReadEvidence(
					scope, message, delivery, principalForRecipient(scope, recipient), dbNow,
				)
				handoff := Handoff{
					MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
					WorkItemID:                 message.WorkItemID, MessageID: message.ID, DeliveryID: delivery.ID,
					From: RecipientRef{Kind: RecipientAgent, Ref: model.NewID().String()}, FromOwnerEpoch: 1,
					To: recipient, ContextEventSeq: 1,
					Payload: communicationTestPayloadForSlot(t, PayloadSlotHandoff), State: HandoffOffered,
					AckDeadline: *message.AckDueAt,
				}
				handoff.ContextHash, _ = CanonicalHandoffContextHash(handoff)
				evidence.Carrier.Entity.Kind = model.Kind("sessions.handoff")
				evidence.Carrier.Entity.ID = handoff.ID
				evidence.Core.Entity = evidence.Carrier.Entity
				evidence.EntityRecipientGuard.Carrier = evidence.Carrier
				evidence.CarrierState.Handoff = &handoff
				return evidence
			}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			scope := communicationStateTestScope()
			recipient := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
			dbNow := communicationTestNow.Add(time.Minute)
			evidence := test.build(t, scope, recipient, dbNow)
			evidence.Operation = test.operation
			evidence.Core.Operation = test.operation
			decision, err := EvaluateCarrierGate(evidence)
			if err != nil || decision.Verdict != VerdictClean || len(decision.Facts) == 0 ||
				!reflect.DeepEqual(decision.RequiredClaims,
					canonicalCommunicationClaims(append([]CommunicationClaimRef(nil), decision.RequiredClaims...))) {
				t.Fatalf("exact %s carrier denied: %#v, %v", test.name, decision, err)
			}
			withoutGrant := evidence
			withoutGrant.CurrentChannelGrant.GrantID = ""
			withoutGrant.CurrentChannelGrant.GrantVersion = 0
			withoutGrant.CurrentChannelGrant.Evidence = AuthorityEvidence{
				Verdict: VerdictBroken, Code: "grant_absent", EvidenceRef: "channel-grant-current",
			}
			grantDecision, err := EvaluateCarrierGate(withoutGrant)
			if err != nil || grantDecision.Verdict != VerdictBroken {
				t.Fatalf("%s carrier survived missing current grant: %#v, %v", test.name, grantDecision, err)
			}
			withoutCause := evidence
			withoutCause.CurrentAudience.Contributions = nil
			causeDecision, err := EvaluateCarrierGate(withoutCause)
			if err != nil || causeDecision.Verdict != VerdictUnknown || len(causeDecision.Facts) != 0 ||
				len(causeDecision.SurvivingContributionIDs) != 0 || len(causeDecision.RequiredClaims) != 0 {
				t.Fatalf("%s carrier survived missing current cause: %#v, %v", test.name, causeDecision, err)
			}
			if test.name == "decision" {
				wrong := evidence
				wrong.Operation = CommunicationDeliveryWrite
				wrong.Core.Operation = CommunicationDeliveryWrite
				if _, err := EvaluateCarrierGate(wrong); err == nil {
					t.Fatal("DecisionRequest carrier accepted Delivery write authority")
				}
			}
			if test.name == "delivery" {
				wrong := evidence
				wrong.Operation = CommunicationDecisionRequestWrite
				wrong.Core.Operation = CommunicationDecisionRequestWrite
				if _, err := EvaluateCarrierGate(wrong); err == nil {
					t.Fatal("Delivery carrier accepted DecisionRequest write authority")
				}
			}
			fabricated := evidence
			fabricated.Carrier.MessageID = model.NewID()
			fabricated.EntityRecipientGuard.Carrier = fabricated.Carrier
			fabricated.CurrentAudience.MessageID = fabricated.Carrier.MessageID
			if _, err := EvaluateCarrierGate(fabricated); err == nil {
				t.Fatal("fabricated carrier triple authorized without matching rows")
			}
		})
	}
	t.Run("communication-session carrier ceiling", func(t *testing.T) {
		scope := communicationStateTestScope()
		sid := "osn_" + model.NewID().String()
		recipient := RecipientRef{Kind: RecipientSession, Ref: sid}
		dbNow := communicationTestNow.Add(time.Minute)
		principal := CommunicationPrincipal{
			SessionID: sid, SessionRunRef: model.NewID().String(), SessionFence: 9,
			SessionWorkspaceID: scope.WorkspaceID, PurposeRestricted: true,
		}
		vectors := []struct {
			name      string
			carrier   int
			operation CommunicationOperation
			allow     bool
		}{
			{name: "delivery_read", carrier: 1, operation: CommunicationRead, allow: true},
			{name: "delivery_write", carrier: 1, operation: CommunicationDeliveryWrite, allow: true},
			{name: "handoff_response", carrier: 3, operation: CommunicationHandoffResponse, allow: true},
			{name: "message_read", carrier: 0, operation: CommunicationRead},
			{name: "decision_read", carrier: 2, operation: CommunicationRead},
			{name: "handoff_read", carrier: 3, operation: CommunicationRead},
			{name: "delivery_admin", carrier: 1, operation: CommunicationDeliveryAdmin},
			{name: "decision_write", carrier: 2, operation: CommunicationDecisionRequestWrite},
		}
		for _, vector := range vectors {
			t.Run(vector.name, func(t *testing.T) {
				evidence := cases[vector.carrier].build(t, scope, recipient, dbNow)
				evidence.Principal = principal
				evidence.Core.Principal = principal
				evidence.PrincipalResolution = communicationStateTestPrincipalResolution(scope, principal, recipient)
				evidence.CurrentChannelGrant.Principal = principal
				evidence.EntityRecipientGuard.Principal = principal
				evidence.Operation = vector.operation
				evidence.Core.Operation = vector.operation
				decision, err := EvaluateCarrierGate(evidence)
				if vector.allow {
					wantClaims := []CommunicationClaimRef{{SessionSID: sid, Fence: principal.SessionFence}}
					if err != nil || decision.Verdict != VerdictClean ||
						!reflect.DeepEqual(decision.RequiredClaims, wantClaims) {
						t.Fatalf("allowed communication-session carrier denied: %#v, %v", decision, err)
					}
					return
				}
				if !errors.Is(err, ErrCommunicationForbidden) {
					t.Fatalf("communication-session crossed carrier ceiling: %#v, %v", decision, err)
				}
			})
		}
	})
}

func TestProtectedPayloadOpenBindsExactLockedAAD(t *testing.T) {
	t.Parallel()

	payload := communicationStateTestSealedPayload(PayloadSlotMessage, 3)
	aad := ContentAAD{
		TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID(), ChannelID: model.NewID(),
		EntityKind: model.Kind("sessions.message"), EntityID: model.NewID(),
		Schema: payload.Schema, ProtectionGeneration: payload.ProtectionGeneration,
	}
	plan, err := PlanProtectedPayloadOpen(
		payload, PayloadSlotMessage, protectedPayloadPolicyFrom(payload), aad, aad,
	)
	if err != nil || !plan.RequiresSealer || plan.SealKeyVersion != payload.SealKeyVersion ||
		!bytes.Equal(plan.Ciphertext, payload.Sealed.Ciphertext) {
		t.Fatalf("valid sealed open plan: %#v, %v", plan, err)
	}
	mutations := map[string]func(*ContentAAD){
		"tenant":      func(value *ContentAAD) { value.TenantID = model.TenantID(model.NewID()) },
		"workspace":   func(value *ContentAAD) { value.WorkspaceID = model.NewID() },
		"channel":     func(value *ContentAAD) { value.ChannelID = model.NewID() },
		"entity_kind": func(value *ContentAAD) { value.EntityKind = model.Kind("sessions.message_ack") },
		"entity_id":   func(value *ContentAAD) { value.EntityID = model.NewID() },
		"schema":      func(value *ContentAAD) { value.Schema = "communication.ack-note.v1" },
		"generation":  func(value *ContentAAD) { value.ProtectionGeneration++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			wrong := aad
			mutate(&wrong)
			if _, err := PlanProtectedPayloadOpen(
				payload, PayloadSlotMessage, protectedPayloadPolicyFrom(payload), wrong, aad,
			); err == nil {
				t.Fatal("open accepted AAD different from locked carrier")
			}
		})
	}
	wrongKey := payload
	wrongKey.SealKeyVersion = "seal-v4"
	if _, err := PlanProtectedPayloadOpen(
		wrongKey, PayloadSlotMessage, protectedPayloadPolicyFrom(wrongKey), aad, aad,
	); err == nil {
		t.Fatal("open accepted envelope/column key mismatch")
	}
}

func TestCommunicationReceiptBindsClosedProjectionAndDigestMode(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	completedAt := communicationTestNow
	resultID := model.NewID()
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: communicationStateTestAppendOnly(scope, completedAt),
		CommandID:                     model.NewID(), ActorFingerprint: bytes.Repeat([]byte{0x11}, sha256.Size),
		CommandScope: "message.publish", IdempotencyKeyHash: bytes.Repeat([]byte{0x12}, sha256.Size),
		RequestDigest: bytes.Repeat([]byte{0x13}, sha256.Size),
		PlanHash:      bytes.Repeat([]byte{0x14}, sha256.Size), ResultKind: "message",
		ResultID: resultID, HTTPStatus: 201,
		ResponseProjectionJSON: CommunicationCommandResponseProjection{
			IDs: map[string]model.ID{"message_id": resultID}, Version: 2,
			State: string(MessagePublished), Counts: map[string]int64{"delivery_count": 2},
			Digests: map[string][]byte{"audience": bytes.Repeat([]byte{0x15}, sha256.Size)},
		},
		CompletedAt: completedAt,
	}
	binding, err := CanonicalCommunicationReceiptResponseBinding(receipt)
	if err != nil {
		t.Fatalf("receipt binding: %v", err)
	}
	plainDigest := sha256.Sum256(binding)
	receipt.ResponseDigest = plainDigest[:]
	if err := ValidateCommunicationCommandReceipt(receipt); err != nil {
		t.Fatalf("plain receipt: %v", err)
	}
	mutated := receipt
	mutated.ResponseProjectionJSON.Counts = map[string]int64{"delivery_count": 3}
	if err := ValidateCommunicationCommandReceipt(mutated); err == nil {
		t.Fatal("plain receipt response changed without digest failure")
	}
	canary := receipt
	canary.ResponseProjectionJSON.State = "SECRET-CANARY-CONTENT"
	if err := ValidateCommunicationCommandReceipt(canary); err == nil {
		t.Fatal("receipt projection accepted arbitrary content-bearing state")
	}
	sealOnly := receipt
	sealOnly.SealKeyVersion = "seal-v9"
	if err := ValidateCommunicationCommandReceipt(sealOnly); err == nil {
		t.Fatal("plain SHA-256 receipt accepted a seal key without a digest key")
	}
	digestOnly := receipt
	digestOnly.DigestKeyVersion = "digest-v12"
	if err := ValidateCommunicationCommandReceipt(digestOnly); err == nil {
		t.Fatal("keyed receipt accepted a digest key without a seal key")
	}

	keyed := receipt
	keyed.SealKeyVersion = "seal-v9"
	keyed.DigestKeyVersion = "digest-v12"
	keyed.ResponseDigest = bytes.Repeat([]byte{0x77}, sha256.Size)
	if err := ValidateCommunicationCommandReceipt(keyed); err != nil {
		t.Fatalf("keyed receipt shape: %v", err)
	}
	keyedBinding, _ := CanonicalCommunicationReceiptResponseBinding(keyed)
	keyedBindingHash := sha256.Sum256(keyedBinding)
	witness := CommunicationReceiptDigestWitness{
		ReceiptID: keyed.ID, CommandID: keyed.CommandID, DigestKeyVersion: keyed.DigestKeyVersion,
		ResponseDigest: keyed.ResponseDigest, BindingHash: keyedBindingHash[:], ObservedAt: keyed.CompletedAt,
		Verification: AuthorityEvidence{
			Verdict: VerdictClean, Code: "verified", EvidenceRef: "sealer-verify-digest",
		},
	}
	if err := ValidateCommunicationReceiptDigestWitness(keyed, witness); err != nil {
		t.Fatalf("keyed digest witness: %v", err)
	}
	witness.DigestKeyVersion = "active-key"
	if err := ValidateCommunicationReceiptDigestWitness(keyed, witness); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("wrong persisted digest key error = %v", err)
	}
}

func TestRouteReasonsAndLabelMatchersAreClosed(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	message := communicationStateTestMessage(t, scope, AckPolicyNone, 0, communicationTestNow)
	delivery := communicationStateTestDelivery(message,
		RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}, 1, false)
	for name, reasons := range map[string][]RouteReason{
		"empty": nil, "duplicate": {"direct", "direct"}, "unsorted": {"workspace", "direct"},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := delivery
			mutated.RouteReasons = reasons
			if err := ValidateMessageDelivery(mutated); err == nil {
				t.Fatal("non-canonical route reason set accepted")
			}
		})
	}
	route := ChannelRouteRule{
		MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
		RouteKey:                   "work-events", Generation: 1, SourceKind: RouteSourceWorkEvent,
		EventType: "work.updated", LabelMatchJSON: json.RawMessage(`{"team":"ops"}`),
		TargetChannelID: model.NewID(), AudienceKind: RouteAudienceWorkspaceMember,
		AckPolicy: AckPolicyNone, WakePolicy: WakeNone, State: ChannelRouteActive,
	}
	if err := ValidateChannelRouteRule(route); err != nil {
		t.Fatalf("exact label matcher: %v", err)
	}
	channel := Channel{
		MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
		Slug:                       "route-target", Name: "Route target", Kind: ChannelCoordination, State: ChannelActive,
		Sensitivity: ChannelInternal, ContentProtection: ContentProtectionStorage, ProtectionGeneration: 1,
		DefaultAckPolicy: AckPolicyNone, DefaultWake: WakeNone, MaxFanout: 10,
		MaxAutomationDepth: 1, ACLRevision: 1, RouteRevision: 1, SubscriptionRevision: 1,
	}
	route.TargetChannelID = channel.ID
	values := json.RawMessage(`["ops","security"]`)
	valuesHash := sha256.Sum256(values)
	definition := ChannelLabelDefinition{
		MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
		ChannelID:                  channel.ID, Key: "team", Generation: 1,
		Classification: ChannelLabelNonSensitive, AllowedValuesJSON: values, ValuesHash: valuesHash[:],
		State: ChannelLabelActive,
	}
	snapshot := ChannelLabelSnapshot{
		Scope: scope, ChannelID: channel.ID, RouteRevision: channel.RouteRevision,
		Definitions: []ChannelLabelDefinition{definition}, ObservedAt: communicationTestNow,
		SameTransaction: true,
		Evidence:        AuthorityEvidence{Verdict: VerdictClean, Code: "current", EvidenceRef: "labels-current"},
	}
	if err := ValidateChannelRouteRuleLabels(route, channel, snapshot, communicationTestNow); err != nil {
		t.Fatalf("registered exact route label: %v", err)
	}
	route.LabelMatchJSON = json.RawMessage(`{"team":"finance"}`)
	if err := ValidateChannelRouteRuleLabels(route, channel, snapshot, communicationTestNow); err == nil {
		t.Fatal("route accepted a value outside registered label vocabulary")
	}
	route.LabelMatchJSON = json.RawMessage(`{"team":{"regex":".*"}}`)
	if err := ValidateChannelRouteRule(route); err == nil {
		t.Fatal("route label matcher accepted regex/free-form object")
	}
}

func TestChannelProtectionCannotDowngrade(t *testing.T) {
	t.Parallel()

	scope := communicationStateTestScope()
	before := Channel{
		MutableCommunicationEntity: communicationStateTestMutable(scope, communicationTestNow),
		Slug:                       "restricted", Name: "Restricted", Kind: ChannelPrivate, State: ChannelActive,
		Sensitivity: ChannelRestricted, ContentProtection: ContentProtectionApplicationSealed,
		ProtectionGeneration: 3, DefaultAckPolicy: AckPolicyNone, DefaultWake: WakeNone,
		MaxFanout: 10, MaxAutomationDepth: 1, ACLRevision: 1, RouteRevision: 1, SubscriptionRevision: 1,
	}
	after := before
	after.Version++
	after.UpdatedAt = communicationTestNow.Add(time.Second)
	after.ContentProtection = ContentProtectionStorage
	if err := ValidateChannelUpdate(before, after); err == nil {
		t.Fatal("application-sealed Channel downgraded to storage protection")
	}
	after = before
	after.Version++
	after.UpdatedAt = communicationTestNow.Add(time.Second)
	after.Name = "Restricted v2"
	if err := ValidateChannelUpdate(before, after); err != nil {
		t.Fatalf("non-protection update rejected: %v", err)
	}
}
