// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var errDecisionResponseReplayRaced = fmt.Errorf(
	"%w: DecisionResponse replay raced apply", store.ErrConflict,
)

type decisionRequestLockedCarrier struct {
	channel       Channel
	grants        []ChannelGrant
	message       Message
	deliveries    []MessageDelivery
	audiences     []MessageAudience
	contributions []MessageAudienceRecipient
	request       DecisionRequest
	delivery      MessageDelivery
	epoch         model.DirectoryEpoch
	requiredCount int64
}

type decisionRequestAuthorizedCarrier struct {
	channel         Channel
	message         Message
	delivery        MessageDelivery
	request         DecisionRequest
	requestOpenPlan ProtectedPayloadOpenPlan
	observedAt      time.Time
	freshUntil      time.Time
}

type decisionResponseWorkState struct {
	item model.Record
	head model.Record
}

type decisionResponsePlanHashV1 struct {
	SchemaVersion int64                        `json:"schema_version"`
	Operation     string                       `json:"operation"`
	Method        string                       `json:"method"`
	Path          string                       `json:"path"`
	Permission    CommunicationOperation       `json:"permission"`
	Scope         DirectoryScopeRef            `json:"scope"`
	Actor         CommunicationActorRef        `json:"actor"`
	RequestDigest []byte                       `json:"request_digest"`
	Before        DecisionRequest              `json:"before"`
	After         DecisionRequest              `json:"after"`
	Response      DecisionResponse             `json:"response"`
	WorkCommand   *WorkCommand                 `json:"work_command,omitempty"`
	WorkItemID    model.ID                     `json:"work_item_id"`
	WorkVersion   int64                        `json:"work_version"`
	Facts         []store.AuthorizationFactRef `json:"facts"`
	RowEffects    []string                     `json:"row_effects"`
}

func (m *Module) authorizeDecisionRequestCarrier(
	ctx context.Context,
	question communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	inspected communicationRequestAuthorityInspection,
	identity directNoticeReaderIdentityPreflight,
	window communicationAuthorityWindow,
	normalized decisionResponseNormalizedCommand,
	requireTransition bool,
) (decisionRequestAuthorizedCarrier, error) {
	var authorized decisionRequestAuthorizedCarrier
	err := m.mutateCommunicationWithNarrowedAuthority(
		ctx, question, bound, CommunicationClaimAuthoritySnapshot{}, window,
		func(tx *communicationTx, consumed communicationRequestAuthorityContext) error {
			reader, err := decisionResponseReaderPreflight(
				question, inspected, consumed, identity, normalized,
			)
			if err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, reader.Facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			if err := tx.lockTransaction(
				ctx, decisionResponseRequestLockKey(normalized.scope, normalized.requestID),
			); err != nil {
				return err
			}
			locked, err := lockDecisionRequestCarrier(ctx, tx, reader, normalized.requestID)
			if err != nil {
				return err
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			decision, err := evaluateDecisionRequestCarrierAuthority(tx, reader, locked)
			if err != nil {
				return err
			}
			if requireTransition {
				if err := validateDecisionResponseActor(
					locked.request, locked.delivery, normalized,
				); err != nil {
					return err
				}
			}
			if grantFreshUntil, constrained, err := directNoticeReadGrantFreshUntil(
				locked.grants, reader.Closure, tx.now.Time(),
			); err != nil {
				return err
			} else if constrained {
				if err := tx.narrowRequestAuthorityFreshUntil(grantFreshUntil); err != nil {
					return err
				}
			}
			if len(decision.RequiredClaims) != 0 ||
				len(decision.SurvivingContributionIDs) != 1 ||
				decision.SurvivingContributionIDs[0] != locked.contributions[0].ID {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"DecisionRequest direct authority widened beyond its carrier",
				)
			}
			aad := ContentAAD{
				TenantID:    normalized.scope.TenantID,
				WorkspaceID: normalized.scope.WorkspaceID,
				ChannelID:   locked.channel.ID, EntityKind: decisionRequestKind,
				EntityID: locked.request.ID, Schema: locked.request.Request.Schema,
				ProtectionGeneration: locked.request.Request.ProtectionGeneration,
			}
			openPlan, err := PlanProtectedPayloadRead(
				locked.request.Request, PayloadSlotDecisionRequest,
				protectedPayloadPolicyFrom(locked.request.Request), aad, aad,
			)
			if err != nil {
				return err
			}
			authorized = decisionRequestAuthorizedCarrier{
				channel: locked.channel, message: locked.message,
				delivery: locked.delivery, request: locked.request,
				requestOpenPlan: openPlan, observedAt: tx.now.Time(),
				freshUntil: tx.requestFreshUntil,
			}
			return nil
		},
	)
	return authorized, err
}

func decisionResponseReaderPreflight(
	question communicationAuthorityQuestion,
	inspected communicationRequestAuthorityInspection,
	consumed communicationRequestAuthorityContext,
	identity directNoticeReaderIdentityPreflight,
	normalized decisionResponseNormalizedCommand,
) (directNoticeReaderPreflight, error) {
	if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
		return directNoticeReaderPreflight{}, err
	}
	if consumed.question != question || consumed.bindingID == nil ||
		consumed.bindingID != inspected.bindingID ||
		consumed.question.entity != (EntityRef{
			TenantID: normalized.scope.TenantID, Kind: decisionRequestKind,
			ID: normalized.requestID, WorkspaceID: normalized.scope.WorkspaceID,
		}) || consumed.question.operation != CommunicationDecisionRequestWrite ||
		consumed.principal != normalized.principal {
		return directNoticeReaderPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"decision-write authority crossed DecisionResponse preflight",
		)
	}
	return directNoticeReaderPreflightWithCore(identity, consumed.witness)
}

func lockDecisionRequestCarrier(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticeReaderPreflight,
	requestID model.ID,
) (decisionRequestLockedCarrier, error) {
	if tx == nil || !validCanonicalCommunicationID(requestID) {
		return decisionRequestLockedCarrier{}, communicationTransactionUnavailable(
			"DecisionRequest carrier transaction", nil,
		)
	}
	requestRepo, err := tx.repo(decisionRequestKind)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	requestObservation, err := requestRepo.Get(ctx, requestID)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	messageID, err := directNoticeRecordID(requestObservation, colCommMessageID)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	messageRepo, err := tx.repo(messageKind)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	messageObservation, err := messageRepo.Get(ctx, messageID)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	channelID, err := directNoticeRecordID(messageObservation, colCommChannelID)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	if err := tx.lockTransaction(
		ctx, directNoticeMessageLockKey(preflight.Scope, messageID),
	); err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	channelRecord, err := tx.lockRecord(ctx, channelKind, channelID)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	channel, err := channelFromRecord(channelRecord)
	if err != nil || channel.ID != channelID || channel.TenantID != preflight.Scope.TenantID ||
		channel.WorkspaceID != preflight.Scope.WorkspaceID {
		return decisionRequestLockedCarrier{}, communicationError(
			ErrCommunicationEvidenceUnknown, "locked DecisionRequest Channel is unavailable",
		)
	}
	grants, err := lockCurrentChannelGrants(ctx, tx, channel.ID)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	messageRecord, err := tx.lockRecord(ctx, messageKind, messageID)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	deliveryRecords, err := lockDirectNoticeRecordSet(
		ctx, tx, messageDeliveryKind,
		[]model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()}},
		directNoticeReadSetBound,
	)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	deliveries := make([]MessageDelivery, 0, len(deliveryRecords))
	requiredCount := int64(0)
	for _, record := range deliveryRecords {
		delivery, decodeErr := messageDeliveryFromRecord(record)
		if decodeErr != nil || delivery.MessageID != messageID {
			return decisionRequestLockedCarrier{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"locked DecisionRequest Delivery set is malformed",
			)
		}
		if delivery.Required {
			requiredCount++
		}
		deliveries = append(deliveries, delivery)
	}
	message, err := messageFromRecord(messageRecord, requiredCount)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	audienceRecords, err := lockDirectNoticeRecordSet(
		ctx, tx, messageAudienceKind,
		[]model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()}},
		64,
	)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	audiences := make([]MessageAudience, 0, len(audienceRecords))
	for _, record := range audienceRecords {
		audience, decodeErr := messageAudienceFromRecord(record)
		if decodeErr != nil || audience.MessageID != messageID {
			return decisionRequestLockedCarrier{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"locked DecisionRequest Audience set is malformed",
			)
		}
		audiences = append(audiences, audience)
	}
	contributionRecords, err := lockDirectNoticeContributionSet(ctx, tx, audiences)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	contributions := make([]MessageAudienceRecipient, 0, len(contributionRecords))
	for _, record := range contributionRecords {
		contribution, decodeErr := messageAudienceRecipientFromRecord(record)
		if decodeErr != nil {
			return decisionRequestLockedCarrier{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"locked DecisionRequest contribution set is malformed",
			)
		}
		contributions = append(contributions, contribution)
	}
	requestRecord, err := tx.lockRecord(ctx, decisionRequestKind, requestID)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	request, err := decisionRequestFromRecord(requestRecord)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	delivery, err := exactDirectDecisionRequestGraph(
		preflight, channel, message, deliveries, audiences, contributions, request,
	)
	if err != nil {
		return decisionRequestLockedCarrier{}, err
	}
	epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
	if err != nil || epoch.Validate() != nil || epoch.TenantID != preflight.Scope.TenantID ||
		preflight.Resolution.Recipient == nil ||
		epoch.Version != preflight.Resolution.Recipient.DirectoryEpoch {
		return decisionRequestLockedCarrier{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"locked DecisionRequest directory epoch is unavailable",
		)
	}
	return decisionRequestLockedCarrier{
		channel: channel, grants: grants, message: message, deliveries: deliveries,
		audiences: audiences, contributions: contributions, request: request,
		delivery: delivery, epoch: epoch, requiredCount: requiredCount,
	}, nil
}

func exactDirectDecisionRequestGraph(
	preflight directNoticeReaderPreflight,
	channel Channel,
	message Message,
	deliveries []MessageDelivery,
	audiences []MessageAudience,
	contributions []MessageAudienceRecipient,
	request DecisionRequest,
) (MessageDelivery, error) {
	if len(deliveries) != 1 || len(audiences) != 1 || len(contributions) != 1 {
		return MessageDelivery{}, communicationError(
			ErrCommunicationForbidden,
			"DecisionResponse currently requires one direct User owner",
		)
	}
	delivery, audience, contribution := deliveries[0], audiences[0], contributions[0]
	wantOwner := CommunicationSubjectRef{Kind: SubjectUser, Ref: preflight.Principal.UserID.String()}
	if message.ChannelID != channel.ID || message.TenantID != preflight.Scope.TenantID ||
		message.WorkspaceID != preflight.Scope.WorkspaceID ||
		message.Kind != MessageDecisionRequest || !validCanonicalCommunicationID(message.WorkItemID) ||
		request.MessageID != message.ID || request.WorkItemID != message.WorkItemID ||
		request.Owner != wantOwner || request.Requester != message.Sender ||
		delivery.Recipient != preflight.Recipient || delivery.Recipient.Kind != RecipientUser ||
		audience.MessageID != message.ID || audience.Ordinal != 1 || audience.RouteRuleID != "" ||
		audience.Selector.Kind != AudienceUser || audience.Selector.Ref != delivery.Recipient.Ref ||
		audience.ResolvedCount != 1 || contribution.MessageAudienceID != audience.ID ||
		contribution.MessageDeliveryID != delivery.ID || contribution.Recipient != delivery.Recipient ||
		contribution.RecipientEpoch != delivery.RecipientEpoch ||
		contribution.Selector != audience.Selector || contribution.CausalKind != CausalDirect ||
		contribution.CausalRef != delivery.Recipient.Ref || contribution.CausalFactKind != "" ||
		contribution.CausalFactID != "" || contribution.CausalFactVersion != 0 ||
		contribution.ObservedSessionSID != "" || contribution.ObservedClaimFence != 0 ||
		contribution.OriginalSubscriber != nil || contribution.SubscriptionID != "" ||
		contribution.SubscriptionGeneration != 0 || contribution.RouteRuleID != "" ||
		contribution.RouteRuleGeneration != 0 {
		return MessageDelivery{}, communicationError(
			ErrCommunicationForbidden,
			"principal is not the exact direct DecisionRequest owner",
		)
	}
	if err := ValidateMessageDeliveryLineage(message, delivery); err != nil {
		return MessageDelivery{}, err
	}
	if err := ValidateDecisionRequestLineage(message, request); err != nil {
		return MessageDelivery{}, err
	}
	if audience.DirectoryEpoch != contribution.DirectoryEpoch ||
		audience.ChannelACLRevision != contribution.ChannelACLRevision ||
		audience.RouteRevision != contribution.RouteRevision ||
		audience.SubscriptionRevision != contribution.SubscriptionRevision {
		return MessageDelivery{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest audience provenance diverges",
		)
	}
	fold, err := FoldAudienceContributions(contributions)
	if err != nil || fold.Recipient != delivery.Recipient || fold.Required != delivery.Required ||
		fold.WakePolicy != delivery.WakePolicy ||
		!canonicalCommunicationValueEqual(fold.RouteReasons, delivery.RouteReasons) {
		return MessageDelivery{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest Delivery diverges from its audience fold",
		)
	}
	audienceHash, err := CanonicalMessageAudienceHash(message, audiences, contributions)
	if err != nil || !bytes.Equal(audienceHash, message.AudienceHash) {
		return MessageDelivery{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest audience seal is unavailable",
		)
	}
	return delivery, nil
}

func evaluateDecisionRequestCarrierAuthority(
	tx *communicationTx,
	preflight directNoticeReaderPreflight,
	locked decisionRequestLockedCarrier,
) (ProtectedReadDecision, error) {
	dbNow := tx.now.Time()
	if preflight.Core.Operation != CommunicationDecisionRequestWrite ||
		preflight.Core.Entity != (EntityRef{
			TenantID: preflight.Scope.TenantID, Kind: decisionRequestKind,
			ID: locked.request.ID, WorkspaceID: preflight.Scope.WorkspaceID,
		}) || directNoticeReadRowsCarryFutureDBTime(
		locked.channel, locked.grants, locked.message, locked.deliveries,
		locked.audiences, locked.contributions, dbNow,
	) || locked.request.CreatedAt.After(dbNow) || locked.request.UpdatedAt.After(dbNow) ||
		!communicationEvidenceCurrent(preflight.Core.ObservedAt, preflight.Core.FreshUntil, dbNow) ||
		!communicationEvidenceCurrent(
			preflight.Resolution.ObservedAt, preflight.Resolution.FreshUntil, dbNow,
		) || !communicationEvidenceCurrent(
		preflight.Closure.ObservedAt, preflight.Closure.FreshUntil, dbNow,
	) {
		return ProtectedReadDecision{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest authority expired while waiting for locks",
		)
	}
	grant := EvaluateCurrentChannelGrant(
		ChannelGrantSnapshot{
			Verdict: VerdictClean, Code: "channel_grants_locked",
			ACLRevision: locked.channel.ACLRevision, ObservedAt: dbNow,
			Grants: locked.grants,
		},
		preflight.Scope.TenantID, preflight.Scope.WorkspaceID, locked.channel.ID,
		preflight.Closure, ChannelGrantRead, dbNow,
	)
	carrier := ProtectedCarrierRef{
		Entity: preflight.Core.Entity, ChannelID: locked.channel.ID,
		MessageID: locked.message.ID, DeliveryID: locked.delivery.ID,
	}
	currentAudience, err := buildDirectNoticeCurrentAudience(
		preflight, locked.message, locked.delivery, locked.audiences,
		locked.contributions, dbNow,
	)
	if err != nil {
		return ProtectedReadDecision{}, err
	}
	clean := func(code, ref string) AuthorityEvidence {
		return AuthorityEvidence{Verdict: VerdictClean, Code: code, EvidenceRef: ref}
	}
	evidence := ReadGateEvidence{
		Scope: preflight.Scope, ChannelID: locked.channel.ID,
		ChannelACLRevision: locked.channel.ACLRevision, DBNow: dbNow,
		Operation: CommunicationDecisionRequestWrite, Carrier: carrier,
		CarrierState: ProtectedCarrierSnapshot{
			Message: locked.message, Delivery: locked.delivery,
			DecisionRequest: &locked.request, RequiredDeliveryCount: locked.requiredCount,
			ObservedAt: dbNow,
			Evidence:   clean("carrier_rows_locked", "same_tx:decision_request_carrier"),
		},
		Core: preflight.Core, Principal: preflight.Principal,
		PrincipalResolution: preflight.Resolution, Recipient: preflight.Recipient,
		DirectoryEpoch: store.AuthorizationFactRef{
			Kind: model.DirectoryEpochKind, ID: model.ID(preflight.Scope.TenantID),
			Version: locked.epoch.Version,
		},
		CurrentChannelGrant: grant,
		EntityRecipientGuard: BoundEntityRecipientEvidence{
			Scope: preflight.Scope, Carrier: carrier, Principal: preflight.Principal,
			Recipient: preflight.Recipient, DirectoryEpoch: locked.epoch.Version,
			EvaluatedAt: dbNow,
			Evidence:    clean("entity_recipient_current", "same_tx:decision_request_recipient"),
		},
		CurrentAudience: currentAudience,
	}
	decision, err := EvaluateCarrierGate(evidence)
	if err != nil {
		return ProtectedReadDecision{}, err
	}
	switch decision.Verdict {
	case VerdictClean:
		if !equalDirectNoticeAuthorityFacts(preflight.Facts, decision.Facts) {
			return ProtectedReadDecision{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"DecisionRequest carrier returned a different authority fact set",
			)
		}
		return decision, nil
	case VerdictBroken:
		return ProtectedReadDecision{}, communicationError(
			ErrCommunicationForbidden, "DecisionRequest carrier authority denied",
		)
	default:
		return ProtectedReadDecision{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DecisionRequest carrier authority is unavailable",
		)
	}
}

func validateDecisionResponseActor(
	request DecisionRequest,
	delivery MessageDelivery,
	normalized decisionResponseNormalizedCommand,
) error {
	if _, err := NextDecisionRequestState(request.State, normalized.command.Transition); err != nil {
		return err
	}
	actor := normalized.actor
	owner := CommunicationSubjectRef{Kind: SubjectUser, Ref: actor.Ref}
	isOwner := request.Owner == owner && delivery.Recipient == (RecipientRef{
		Kind: RecipientUser, Ref: actor.Ref,
	})
	isCustodian := isOwner && request.AcceptedDeliveryID == delivery.ID
	isRequester := request.Requester == actor
	allowed := false
	switch normalized.command.Transition {
	case DecisionAccept:
		allowed = isOwner && (request.State == DecisionPending ||
			(request.State == DecisionBlocked && isCustodian))
	case DecisionBlock:
		allowed = request.State == DecisionAccepted && isCustodian
	case DecisionResolve:
		allowed = isOwner && (request.State == DecisionPending ||
			((request.State == DecisionAccepted || request.State == DecisionBlocked) && isCustodian))
	case DecisionReject:
		allowed = isOwner && (request.State == DecisionPending ||
			((request.State == DecisionAccepted || request.State == DecisionBlocked) && isCustodian))
	case DecisionCancel:
		allowed = isRequester
	}
	if !allowed {
		return communicationError(
			ErrCommunicationForbidden,
			"actor is not authorized for this DecisionRequest transition",
		)
	}
	return nil
}

func (m *Module) applyDecisionRequestResponse(
	ctx context.Context,
	question communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	inspected communicationRequestAuthorityInspection,
	identity directNoticeReaderIdentityPreflight,
	window communicationAuthorityWindow,
	normalized decisionResponseNormalizedCommand,
	ids decisionResponseIDs,
	prepared decisionResponsePreparedContent,
) (DecisionRequestResponseResult, error) {
	var result DecisionRequestResponseResult
	err := m.mutateDecisionWithNarrowedAuthority(
		ctx, question, bound, window,
		func(
			tx *communicationTx,
			sc store.Scope,
			consumed communicationRequestAuthorityContext,
		) error {
			reader, err := decisionResponseReaderPreflight(
				question, inspected, consumed, identity, normalized,
			)
			if err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, reader.Facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			if err := tx.lockTransaction(
				ctx, decisionResponseIdempotencyLockKey(normalized),
			); err != nil {
				return err
			}
			receipt, found, err := findDecisionResponseReceipt(
				ctx, func(kind model.Kind) (communicationReadRepository, error) {
					return tx.repo(kind)
				}, normalized,
			)
			if err != nil {
				return err
			}
			if found {
				if !bytes.Equal(receipt.RequestDigest, normalized.requestDigest) {
					return errDecisionResponseIdempotencyReused
				}
				return errDecisionResponseReplayRaced
			}
			if err := tx.lockTransaction(
				ctx, decisionResponseRequestLockKey(normalized.scope, normalized.requestID),
			); err != nil {
				return err
			}
			locked, err := lockDecisionRequestCarrier(ctx, tx, reader, normalized.requestID)
			if err != nil {
				return err
			}
			workState, err := lockDecisionResponseWorkState(
				ctx, tx, sc, locked.request, normalized.command.BlockerWorkItemID,
			)
			if err != nil {
				return err
			}
			if err := tx.lockAuditAppends(ctx); err != nil {
				return err
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			decision, err := evaluateDecisionRequestCarrierAuthority(tx, reader, locked)
			if err != nil {
				return err
			}
			if grantFreshUntil, constrained, err := directNoticeReadGrantFreshUntil(
				locked.grants, reader.Closure, tx.now.Time(),
			); err != nil {
				return err
			} else if constrained {
				if err := tx.narrowRequestAuthorityFreshUntil(grantFreshUntil); err != nil {
					return err
				}
			}
			if err := validateDecisionResponseActor(
				locked.request, locked.delivery, normalized,
			); err != nil {
				return err
			}
			if locked.request.Version != normalized.expectedVersion {
				return errDecisionResponseVersionMismatch
			}
			if !bytes.Equal(prepared.requestHash, mustDecisionPayloadHash(locked.request.Request)) ||
				!bytes.Equal(prepared.responseHash, mustDecisionPayloadHash(prepared.response)) {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"DecisionResponse content preparation crossed carrier lineage",
				)
			}
			result, err = m.applyLockedDecisionRequestResponse(
				ctx, tx, sc, reader, decision, locked, workState,
				normalized, ids, prepared,
			)
			return err
		},
	)
	return result, err
}

func (m *Module) applyDecisionRequestDeadline(
	ctx context.Context,
	normalized decisionDeadlineNormalized,
	ids decisionResponseIDs,
	prepared decisionResponsePreparedContent,
) (DecisionRequestResponseResult, error) {
	var result DecisionRequestResponseResult
	var replayReceipt *CommunicationCommandReceipt
	err := m.mutateDecisionDeadline(
		ctx, normalized.scope, normalized.authority,
		func(tx *communicationTx, sc store.Scope) error {
			if err := tx.lockAuthoritySnapshot(ctx, normalized.authority.Facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			if err := tx.lockTransaction(
				ctx, decisionResponseIdempotencyLockKey(normalized.decisionResponseNormalizedCommand),
			); err != nil {
				return err
			}
			receipt, replay, err := findDecisionResponseReceipt(
				ctx, func(kind model.Kind) (communicationReadRepository, error) {
					return tx.repo(kind)
				}, normalized.decisionResponseNormalizedCommand,
			)
			if err != nil {
				return err
			}
			if replay && !bytes.Equal(receipt.RequestDigest, normalized.requestDigest) {
				return errDecisionResponseIdempotencyReused
			}
			if err := tx.lockTransaction(
				ctx, decisionResponseRequestLockKey(normalized.scope, normalized.requestID),
			); err != nil {
				return err
			}
			locked, err := lockDecisionRequestCarrier(
				ctx, tx, normalized.authority.Reader, normalized.requestID,
			)
			if err != nil {
				return err
			}
			workState, err := lockDecisionResponseWorkState(
				ctx, tx, sc, locked.request, "",
			)
			if err != nil {
				return err
			}
			if err := tx.lockAuditAppends(ctx); err != nil {
				return err
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			decision, err := evaluateDecisionRequestCarrierAuthority(
				tx, normalized.authority.Reader, locked,
			)
			if err != nil {
				return err
			}
			if grantFreshUntil, constrained, err := directNoticeReadGrantFreshUntil(
				locked.grants, normalized.authority.Reader.Closure, tx.now.Time(),
			); err != nil {
				return err
			} else if constrained {
				if err := tx.narrowRequestAuthorityFreshUntil(grantFreshUntil); err != nil {
					return err
				}
			}
			if err := evaluateDecisionDeadlineAuthority(tx, normalized, locked); err != nil {
				return err
			}
			if !bytes.Equal(prepared.requestHash, mustDecisionPayloadHash(locked.request.Request)) ||
				!bytes.Equal(prepared.responseHash, mustDecisionPayloadHash(prepared.response)) {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"DecisionRequest deadline content crossed carrier lineage",
				)
			}
			if replay {
				copyReceipt := receipt
				replayReceipt = &copyReceipt
				return nil
			}
			result, err = m.applyLockedDecisionRequestResponse(
				ctx, tx, sc, normalized.authority.Reader, decision, locked, workState,
				normalized.decisionResponseNormalizedCommand, ids, prepared,
			)
			return err
		},
	)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	if replayReceipt != nil {
		result, err = m.decisionResponseResultFromReceipt(
			ctx, normalized.decisionResponseNormalizedCommand, *replayReceipt,
		)
		if err != nil {
			return DecisionRequestResponseResult{}, err
		}
		result.Replayed = true
	}
	return result, nil
}

func evaluateDecisionDeadlineAuthority(
	tx *communicationTx,
	normalized decisionDeadlineNormalized,
	locked decisionRequestLockedCarrier,
) error {
	if tx == nil || validateDecisionDeadlineAuthority(
		normalized.scope, normalized.requestID, normalized.authority,
	) != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest deadline authority is unavailable",
		)
	}
	dbNow := tx.now.Time()
	if !communicationEvidenceCurrent(
		normalized.authority.ObservedAt, normalized.authority.FreshUntil, dbNow,
	) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest deadline authority expired while waiting for locks",
		)
	}
	foundDirectory := false
	for _, fact := range normalized.authority.Facts {
		if fact.Kind == model.DirectoryEpochKind &&
			fact.ID == model.ID(normalized.scope.TenantID) &&
			fact.Version == locked.epoch.Version {
			foundDirectory = true
		}
	}
	if !foundDirectory {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest deadline crossed directory epoch",
		)
	}
	return nil
}

func mustDecisionPayloadHash(payload ProtectedPayload) []byte {
	digest, err := CanonicalProtectedPayloadEnvelopeHash(payload)
	if err != nil {
		return nil
	}
	return digest
}

func lockDecisionResponseWorkState(
	ctx context.Context,
	tx *communicationTx,
	sc store.Scope,
	request DecisionRequest,
	blockerID model.ID,
) (decisionResponseWorkState, error) {
	items, err := sc.Ext(workItemKind)
	if err != nil {
		return decisionResponseWorkState{}, err
	}
	itemLocker, ok := items.(store.RowLocker[model.Record])
	if !ok {
		return decisionResponseWorkState{}, communicationTransactionUnavailable(
			"WorkItem row locker", nil,
		)
	}
	var item model.Record
	err = runCommunicationBoundAuthorityLocalLock(tx.boundAuthorityState, func() error {
		var lockErr error
		item, lockErr = itemLocker.Lock(ctx, request.WorkItemID)
		return lockErr
	})
	if err != nil {
		return decisionResponseWorkState{}, err
	}
	if recordID(item) != request.WorkItemID ||
		item.String(colWorkWorkspaceID) != request.WorkspaceID.String() ||
		item.Int(model.ColVersion) < 1 || item.Int(colWorkLastEventSeq) < 1 ||
		terminalWorkStatuses[item.String(colWorkStatus)] {
		return decisionResponseWorkState{}, fmt.Errorf(
			"%w: DecisionRequest WorkItem is unavailable or terminal", store.ErrConflict,
		)
	}
	if blockerID != "" {
		var blocker model.Record
		err = runCommunicationBoundAuthorityLocalLock(tx.boundAuthorityState, func() error {
			var lockErr error
			blocker, lockErr = itemLocker.Lock(ctx, blockerID)
			return lockErr
		})
		if err != nil {
			return decisionResponseWorkState{}, err
		}
		if recordID(blocker) != blockerID ||
			blocker.String(colWorkWorkspaceID) != request.WorkspaceID.String() {
			return decisionResponseWorkState{}, communicationError(
				ErrInvalidCommunicationModel, "blocker WorkItem crosses DecisionRequest workspace",
			)
		}
	}
	heads, err := sc.Ext(workDecisionHeadKind)
	if err != nil {
		return decisionResponseWorkState{}, err
	}
	rows, err := runCommunicationBoundAuthorityObservation(
		tx.boundAuthorityState,
		func() ([]model.Record, error) {
			return listAll(
				ctx, heads,
				model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: request.WorkItemID.String()},
				model.Filter{Column: colDecisionKey, Op: model.OpEq, Value: request.DecisionKey},
			)
		},
	)
	if err != nil {
		return decisionResponseWorkState{}, err
	}
	if len(rows) > 1 {
		return decisionResponseWorkState{}, fmt.Errorf(
			"%w: DecisionHead is ambiguous", store.ErrConflict,
		)
	}
	var head model.Record
	if len(rows) == 1 {
		headLocker, ok := heads.(store.RowLocker[model.Record])
		if !ok {
			return decisionResponseWorkState{}, communicationTransactionUnavailable(
				"DecisionHead row locker", nil,
			)
		}
		err = runCommunicationBoundAuthorityLocalLock(tx.boundAuthorityState, func() error {
			var lockErr error
			head, lockErr = headLocker.Lock(ctx, recordID(rows[0]))
			return lockErr
		})
		if err != nil {
			return decisionResponseWorkState{}, err
		}
	}
	return decisionResponseWorkState{item: item, head: head}, nil
}

func decisionResponseLifecycleMetadata(
	normalized decisionResponseNormalizedCommand,
) (operation, auditAction, eventType string, err error) {
	if normalized.actor.Kind == ActorSystem && normalized.command.Transition == DecisionExpire {
		return decisionDeadlineOperation, decisionDeadlineAuditAction,
			decisionDeadlineEventType, nil
	}
	if normalized.actor.Kind == ActorUser && normalized.command.Transition != DecisionExpire {
		return decisionResponseOperation, decisionResponseAuditAction,
			decisionResponseEventType, nil
	}
	return "", "", "", communicationError(
		ErrCommunicationEvidenceUnknown,
		"DecisionResponse actor and lifecycle operation diverge",
	)
}

func decisionResponseWorkPrincipal(
	normalized decisionResponseNormalizedCommand,
) (WorkPrincipal, error) {
	switch normalized.actor.Kind {
	case ActorUser:
		if normalized.principal.UserID == "" ||
			normalized.actor.Ref != normalized.principal.UserID.String() {
			return WorkPrincipal{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"DecisionResponse User actor crossed its authenticated principal",
			)
		}
		return WorkPrincipal{
			ActorKind: string(model.ActorUser), ActorRef: normalized.actor.Ref,
			Actor: directNoticeActor(normalized.principal),
		}, nil
	case ActorSystem:
		return WorkPrincipal{
			ActorKind: string(model.ActorSystem), ActorRef: normalized.actor.Ref,
			Actor: string(ActorSystem) + ":" + normalized.actor.Ref,
		}, nil
	default:
		return WorkPrincipal{}, communicationError(
			ErrCommunicationForbidden,
			"DecisionResponse actor kind is not authorized",
		)
	}
}

func (m *Module) applyLockedDecisionRequestResponse(
	ctx context.Context,
	tx *communicationTx,
	sc store.Scope,
	reader directNoticeReaderPreflight,
	authority ProtectedReadDecision,
	locked decisionRequestLockedCarrier,
	workState decisionResponseWorkState,
	normalized decisionResponseNormalizedCommand,
	ids decisionResponseIDs,
	prepared decisionResponsePreparedContent,
) (DecisionRequestResponseResult, error) {
	if tx == nil || authority.Verdict != VerdictClean ||
		!equalDirectNoticeAuthorityFacts(reader.Facts, authority.Facts) ||
		locked.request.Version == math.MaxInt64 ||
		workState.item.Int(model.ColVersion) == math.MaxInt64 ||
		workState.item.Int(colWorkLastEventSeq) == math.MaxInt64 {
		return DecisionRequestResponseResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionResponse locked apply evidence is unavailable",
		)
	}
	now := tx.now
	operation, auditAction, eventType, err := decisionResponseLifecycleMetadata(normalized)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	workPrincipal, err := decisionResponseWorkPrincipal(normalized)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	var workCommand *WorkCommand
	var workDecisionID model.ID
	itemAfter := workState.item
	if normalized.command.Transition == DecisionResolve {
		commandName := "decision.set"
		if workState.head != nil && workState.head.String(colDecisionHeadState) == "effective" {
			commandName = "decision.supersede"
		} else if workState.head != nil && workState.head.String(colDecisionHeadState) != "revoked" {
			return DecisionRequestResponseResult{}, fmt.Errorf(
				"%w: DecisionHead has invalid state", store.ErrConflict,
			)
		}
		cmd := WorkCommand{
			Command: commandName, WorkspaceID: normalized.scope.WorkspaceID,
			WorkItemID: locked.request.WorkItemID, DecisionKey: locked.request.DecisionKey,
			SubjectKind: string(locked.request.Owner.Kind), SubjectRef: locked.request.Owner.Ref,
			StatementMD: normalized.command.Response.ChoiceKey,
			RationaleMD: fmt.Sprintf(
				"k3-response:%s#%s", ids.Response, hex.EncodeToString(prepared.response.Digest),
			),
			AuthorityRef: locked.request.AuthorityRequirement,
		}
		if err := validateCommandSyntax(cmd); err != nil {
			return DecisionRequestResponseResult{}, err
		}
		// The internal K1 validator and helper operate on the already-confined
		// transaction. No public K1 API or nested transaction is invoked.
		if err := m.validateStateCommand(
			ctx, sc, normalized.scope.TenantID, workPrincipal, cmd, workState.item,
		); err != nil {
			return DecisionRequestResponseResult{}, err
		}
		workCommand = &cmd
		applied, err := runCommunicationBoundAuthorityEffect(
			tx.boundAuthorityState,
			func() (struct {
				item       model.Record
				decisionID model.ID
			}, error) {
				item, decisionID, applyErr := m.applyDecisionCommand(
					ctx, sc, workPrincipal, cmd, workState.item, now,
				)
				return struct {
					item       model.Record
					decisionID model.ID
				}{item: item, decisionID: decisionID}, applyErr
			},
		)
		if err != nil {
			return DecisionRequestResponseResult{}, err
		}
		itemAfter, workDecisionID = applied.item, applied.decisionID
	}
	acceptedDeliveryID := model.ID("")
	if normalized.command.Transition == DecisionAccept {
		acceptedDeliveryID = locked.delivery.ID
	}
	responseEntity := AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
		ID: ids.Response, TenantID: normalized.scope.TenantID,
		WorkspaceID: normalized.scope.WorkspaceID, Version: 1, CreatedAt: now.Time(),
	}}
	transitionCode := normalized.command.Response.Reason.Code
	if normalized.command.Transition == DecisionAccept {
		transitionCode = ""
	}
	plan, err := PlanDecisionRequestTransition(
		locked.request, normalized.command.Transition, responseEntity, normalized.actor,
		prepared.response, prepared.choiceWitness, acceptedDeliveryID,
		normalized.command.BlockerWorkItemID, workDecisionID,
		transitionCode, now.Time(),
	)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	planHash, err := canonicalDecisionResponsePlanHash(
		normalized, reader, plan, workCommand, itemAfter,
	)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	audit, err := tx.appendAudit(ctx, model.AuditDraft{
		Actor: workPrincipal.Actor, ActorKind: workPrincipal.ActorKind,
		Action: auditAction, TargetKind: communicationCommandKind,
		TargetID: ids.Command, PayloadHash: append([]byte(nil), planHash...),
		Meta: map[string]any{
			"workspace_id":     normalized.scope.WorkspaceID.String(),
			"command_scope":    normalized.commandScope,
			"request_id":       locked.request.ID.String(),
			"response_id":      ids.Response.String(),
			"work_item_id":     locked.request.WorkItemID.String(),
			"transition":       string(normalized.command.Transition),
			"response_digest":  hex.EncodeToString(prepared.response.Digest),
			"work_decision_id": workDecisionID.String(),
		},
	})
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
		return DecisionRequestResponseResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionResponse audit append returned no durable anchor",
		)
	}
	var event WorkEventEnvelope
	if workCommand != nil {
		event, err = runCommunicationBoundAuthorityEffect(
			tx.boundAuthorityState,
			func() (WorkEventEnvelope, error) {
				return appendWorkEvent(
					ctx, sc, normalized.scope.TenantID, workPrincipal, *workCommand,
					itemAfter, string(workDecisionKind), workDecisionID, now, ids.Command, audit,
				)
			},
		)
	} else {
		itemAfter, err = runCommunicationBoundAuthorityEffect(
			tx.boundAuthorityState,
			func() (model.Record, error) {
				return updateWorkItemWithEvent(ctx, sc, workState.item)
			},
		)
		if err == nil {
			event, err = runCommunicationBoundAuthorityEffect(
				tx.boundAuthorityState,
				func() (WorkEventEnvelope, error) {
					return persistWorkEvent(
						ctx, sc, normalized.scope.TenantID, workPrincipal, itemAfter,
						eventType, itemAfter.Int(colWorkLastEventSeq),
						map[string]any{
							"command":         operation,
							"transition":      string(normalized.command.Transition),
							"request_id":      locked.request.ID.String(),
							"response_id":     ids.Response.String(),
							"work_item_id":    locked.request.WorkItemID.String(),
							"state":           string(plan.After.State),
							"response_digest": hex.EncodeToString(prepared.response.Digest),
						},
						now, ids.Command, audit,
					)
				},
			)
		}
	}
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	requestRecord, err := decisionRequestToRecord(plan.After)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	// Store.Update consumes the currently observed version and increments it;
	// the planner's After version is the closed projection returned to callers.
	requestRecord[model.ColVersion] = plan.Before.Version
	if _, err := tx.update(ctx, decisionRequestKind, requestRecord); err != nil {
		return DecisionRequestResponseResult{}, err
	}
	responseRecord, err := decisionResponseToRecord(plan.Response, plan.Before, plan.After)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	if _, err := tx.createWithID(
		ctx, decisionResponseKind, ids.Response, responseRecord,
	); err != nil {
		return DecisionRequestResponseResult{}, err
	}
	result := DecisionRequestResponseResult{
		CommandID: ids.Command, RequestID: plan.After.ID, ResponseID: ids.Response,
		MessageID: plan.After.MessageID, WorkItemID: plan.After.WorkItemID,
		WorkDecisionID: workDecisionID, EventID: event.EventID,
		Version: plan.After.Version, ETag: fmt.Sprintf("\"v%d\"", plan.After.Version),
		State: plan.After.State, AuditSeq: audit.Seq,
	}
	receipt, err := buildDecisionResponseReceipt(
		normalized, ids, prepared, planHash, audit, result, now.Time(),
	)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	receiptRecord, err := communicationCommandReceiptToRecord(receipt)
	if err != nil {
		return DecisionRequestResponseResult{}, err
	}
	if _, err := tx.createWithID(
		ctx, communicationCommandKind, ids.Receipt, receiptRecord,
	); err != nil {
		return DecisionRequestResponseResult{}, err
	}
	return result, nil
}

func canonicalDecisionResponsePlanHash(
	normalized decisionResponseNormalizedCommand,
	reader directNoticeReaderPreflight,
	plan DecisionRequestTransitionPlan,
	workCommand *WorkCommand,
	item model.Record,
) ([]byte, error) {
	operation, _, _, err := decisionResponseLifecycleMetadata(normalized)
	if err != nil {
		return nil, err
	}
	effects := []string{
		"work_item:cas", "work_event:append", "work_outbox:insert",
		"decision_request:cas", "decision_response:append",
		"command_receipt:append", "audit:append",
	}
	if workCommand != nil {
		effects = append(effects, "work_decision:append", "decision_head:cas")
	}
	sort.Strings(effects)
	raw, err := canonicalJSON(decisionResponsePlanHashV1{
		SchemaVersion: 1, Operation: operation,
		Method: normalized.method, Path: normalized.path,
		Permission: CommunicationDecisionRequestWrite, Scope: normalized.scope,
		Actor: normalized.actor, RequestDigest: append([]byte(nil), normalized.requestDigest...),
		Before: plan.Before, After: plan.After, Response: plan.Response,
		WorkCommand: workCommand, WorkItemID: plan.After.WorkItemID,
		WorkVersion: item.Int(model.ColVersion),
		Facts:       sortedDirectNoticeFacts(reader.Facts), RowEffects: effects,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func buildDecisionResponseReceipt(
	normalized decisionResponseNormalizedCommand,
	ids decisionResponseIDs,
	prepared decisionResponsePreparedContent,
	planHash []byte,
	audit model.AuditEvent,
	result DecisionRequestResponseResult,
	completedAt time.Time,
) (CommunicationCommandReceipt, error) {
	projectionIDs := map[string]model.ID{
		"request_id": result.RequestID, "response_id": result.ResponseID,
		"message_id": result.MessageID, "work_item_id": result.WorkItemID,
		"event_id": result.EventID,
	}
	if result.WorkDecisionID != "" {
		projectionIDs["result_id"] = result.WorkDecisionID
	}
	projection := CommunicationCommandResponseProjection{
		IDs: projectionIDs, Version: result.Version, State: string(result.State),
		Digests: map[string][]byte{
			"request":  append([]byte(nil), normalized.requestDigest...),
			"plan":     append([]byte(nil), planHash...),
			"response": append([]byte(nil), prepared.response.Digest...),
		},
	}
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: ids.Receipt, TenantID: normalized.scope.TenantID,
				WorkspaceID: normalized.scope.WorkspaceID, Version: 1,
				CreatedAt: completedAt,
			},
		},
		CommandID:          ids.Command,
		ActorFingerprint:   append([]byte(nil), normalized.actorFingerprint...),
		CommandScope:       normalized.commandScope,
		IdempotencyKeyHash: append([]byte(nil), normalized.idempotencyKeyHash...),
		RequestDigest:      append([]byte(nil), normalized.requestDigest...),
		PlanHash:           append([]byte(nil), planHash...), ResultKind: string(decisionResponseKind),
		ResultID: result.ResponseID, HTTPStatus: http.StatusOK,
		ResponseProjectionJSON: projection, EventID: result.EventID,
		AuditSeq: audit.Seq, AuditHash: append([]byte(nil), audit.Hash...),
		CompletedAt: completedAt,
	}
	binding, err := CanonicalCommunicationReceiptResponseBinding(receipt)
	if err != nil {
		return CommunicationCommandReceipt{}, err
	}
	digest := sha256.Sum256(binding)
	receipt.ResponseDigest = digest[:]
	if err := ValidateCommunicationCommandReceipt(receipt); err != nil {
		return CommunicationCommandReceipt{}, err
	}
	return receipt, nil
}

func findDecisionResponseReceipt(
	ctx context.Context,
	resolve communicationReadRepositoryResolver,
	normalized decisionResponseNormalizedCommand,
) (CommunicationCommandReceipt, bool, error) {
	repo, err := resolve(communicationCommandKind)
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	rows, page, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		{Column: colCommActorFingerprint, Op: model.OpEq, Value: normalized.actorFingerprint},
		{Column: colCommCommandScope, Op: model.OpEq, Value: normalized.commandScope},
		{Column: colCommIdempotencyKeyHash, Op: model.OpEq, Value: normalized.idempotencyKeyHash},
	}, Limit: 2})
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	if len(rows) == 0 && !page.HasMore {
		return CommunicationCommandReceipt{}, false, nil
	}
	if len(rows) != 1 || page.HasMore {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionResponse receipt uniqueness is unavailable",
		)
	}
	receipt, err := communicationCommandReceiptFromRecord(rows[0])
	if err != nil {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "DecisionResponse receipt cannot be decoded",
		)
	}
	if receipt.TenantID != normalized.scope.TenantID ||
		receipt.WorkspaceID != normalized.scope.WorkspaceID ||
		receipt.CommandScope != normalized.commandScope ||
		!bytes.Equal(receipt.ActorFingerprint, normalized.actorFingerprint) ||
		!bytes.Equal(receipt.IdempotencyKeyHash, normalized.idempotencyKeyHash) {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "DecisionResponse receipt crosses command scope",
		)
	}
	return receipt, true, nil
}

func decisionResponseIdempotencyLockKey(normalized decisionResponseNormalizedCommand) string {
	return fmt.Sprintf(
		"sessions.communication.decision-response.idempotency/%s/%s/%x",
		normalized.scope.WorkspaceID, normalized.commandScope, normalized.idempotencyKeyHash,
	)
}

func decisionResponseRequestLockKey(scope DirectoryScopeRef, requestID model.ID) string {
	return fmt.Sprintf(
		"sessions.communication.decision-request/%s/%s", scope.WorkspaceID, requestID,
	)
}

func decisionDeadlineAuthorityWindow(
	authority decisionDeadlineAuthority,
) (communicationAuthorityWindow, error) {
	observedAt := authority.ObservedAt
	freshUntil := authority.FreshUntil
	for _, window := range [][2]time.Time{
		{authority.Reader.Core.ObservedAt, authority.Reader.Core.FreshUntil},
		{authority.Reader.Resolution.ObservedAt, authority.Reader.Resolution.FreshUntil},
		{authority.Reader.Closure.ObservedAt, authority.Reader.Closure.FreshUntil},
	} {
		if window[0].After(observedAt) {
			observedAt = window[0]
		}
		if window[1].Before(freshUntil) {
			freshUntil = window[1]
		}
	}
	return newCommunicationAuthorityWindow(observedAt, freshUntil)
}

func (m *Module) mutateDecisionDeadline(
	ctx context.Context,
	scope DirectoryScopeRef,
	authority decisionDeadlineAuthority,
	fn func(*communicationTx, store.Scope) error,
) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if err := validateDecisionDeadlineAuthority(
		scope, authority.Reader.Core.Entity.ID, authority,
	); err != nil {
		return err
	}
	if fn == nil {
		return communicationTransactionUnavailable(
			"DecisionRequest deadline mutation callback", nil,
		)
	}
	window, err := decisionDeadlineAuthorityWindow(authority)
	if err != nil {
		return err
	}
	binding := &communicationRequestAuthorityBindingID{marker: 1}
	request := communicationRequestAuthoritySnapshot{
		facts:      append([]store.AuthorizationFactRef(nil), authority.Facts...),
		observedAt: window.observedAt, freshUntil: window.freshUntil,
		bindingID: binding,
	}
	if err := request.validate(); err != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DecisionRequest deadline authority snapshot is malformed",
		)
	}
	var callbackAttempted atomic.Bool
	return m.communicationData(scope.TenantID).Mutate(ctx, func(sc store.Scope) error {
		if !callbackAttempted.CompareAndSwap(false, true) {
			return communicationTransactionUnavailable(
				"DecisionRequest deadline mutation callback was already entered", nil,
			)
		}
		confined, err := store.ConfineWorkspace(ctx, sc, scope.WorkspaceID)
		if err != nil {
			return err
		}
		tx, err := newCommunicationTxWithAuthority(
			ctx, confined, request, CommunicationClaimAuthoritySnapshot{},
		)
		if err != nil {
			return err
		}
		if err := fn(tx, confined); err != nil {
			return err
		}
		return tx.finalizeAuthority(ctx)
	})
}

// mutateDecisionWithNarrowedAuthority is the one narrow seam that exposes the
// already workspace-confined Scope to the internal K1 decision helper. It does
// not retain the scope, open AuthScope or invoke the public K1 Apply boundary.
func (m *Module) mutateDecisionWithNarrowedAuthority(
	ctx context.Context,
	expected communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	window communicationAuthorityWindow,
	fn func(*communicationTx, store.Scope, communicationRequestAuthorityContext) error,
) error {
	if err := expected.validate(); err != nil {
		return err
	}
	if err := window.validate(); err != nil {
		return err
	}
	if fn == nil {
		return communicationTransactionUnavailable("DecisionResponse mutation callback", nil)
	}
	request, boundContext, err := bound.transactionSnapshot(
		expected, CommunicationClaimAuthoritySnapshot{},
	)
	if err != nil {
		return err
	}
	request, err = request.narrowTo(window)
	if err != nil {
		return err
	}
	scope := DirectoryScopeRef{
		TenantID: expected.entity.TenantID, WorkspaceID: expected.entity.WorkspaceID,
	}
	var callbackAttempted atomic.Bool
	return m.communicationData(scope.TenantID).Mutate(ctx, func(sc store.Scope) error {
		if !callbackAttempted.CompareAndSwap(false, true) {
			return communicationTransactionUnavailable(
				"DecisionResponse bound mutation callback was already entered", nil,
			)
		}
		confined, err := store.ConfineWorkspace(ctx, sc, scope.WorkspaceID)
		if err != nil {
			return err
		}
		tx, err := newCommunicationTxWithAuthority(
			ctx, confined, request, CommunicationClaimAuthoritySnapshot{},
		)
		if err != nil {
			return err
		}
		if err := fn(tx, confined, boundContext); err != nil {
			return err
		}
		return tx.finalizeAuthority(ctx)
	})
}
