// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type messageDerivedIDs struct {
	Message      model.ID
	Audience     model.ID
	Delivery     model.ID
	Contribution model.ID
	Command      model.ID
	Event        model.ID
	Receipt      model.ID
}

func (ids messageDerivedIDs) valid() bool {
	values := [...]model.ID{
		ids.Message, ids.Audience, ids.Delivery, ids.Contribution,
		ids.Command, ids.Event, ids.Receipt,
	}
	for index, id := range values {
		if !validCanonicalCommunicationID(id) {
			return false
		}
		for prior := range index {
			if id == values[prior] {
				return false
			}
		}
	}
	return true
}

func (s *messageDerivedService) allocateDerivedIDs() messageDerivedIDs {
	return messageDerivedIDs{
		Message: s.newID(), Audience: s.newID(), Delivery: s.newID(),
		Contribution: s.newID(), Command: s.newID(), Event: s.newID(), Receipt: s.newID(),
	}
}

type messageDerivedEventProjection struct {
	SchemaVersion   int64                `json:"schema_version"`
	Command         messageDerivedAction `json:"command"`
	SourceMessageID model.ID             `json:"source_message_id"`
	MessageID       model.ID             `json:"message_id"`
	WorkItemID      model.ID             `json:"work_item_id,omitempty"`
	OriginEventID   model.ID             `json:"origin_event_id,omitempty"`
	Step            int64                `json:"step,omitempty"`
	Recipient       RecipientRef         `json:"recipient"`
	AutomationDepth int64                `json:"automation_depth"`
	State           MessageState         `json:"state"`
	Version         int64                `json:"version"`
	PlanHash        string               `json:"plan_hash"`
}

func canonicalMessageDerivedSourceHash(
	message Message,
	deliveries []MessageDelivery,
) ([]byte, error) {
	raw, err := canonicalJSON(struct {
		Message    Message           `json:"message"`
		Deliveries []MessageDelivery `json:"deliveries"`
	}{message, deliveries})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func messageDerivedLockKey(normalized messageDerivedNormalized) string {
	return fmt.Sprintf(
		"sessions.communication.message.derived:%s:%s:%x",
		normalized.scope.WorkspaceID, normalized.commandScope, normalized.idempotencyKeyHash,
	)
}

func messageDerivedLockPreflight(
	normalized messageDerivedNormalized,
	ids messageDerivedIDs,
) directNoticePublishPreflight {
	return directNoticePublishPreflight{
		Command: DirectNoticePublishCommand{
			ChannelID: normalized.channel.ID, Recipient: normalized.command.Recipient,
			Urgency: normalized.source.Urgency,
		},
		Scope: normalized.scope, Principal: normalized.authority.Principal,
		Sender: normalized.authority.Actor, Channel: normalized.channel,
		IDs: directNoticePublishIDs{
			Message: ids.Message, Audience: ids.Audience, Delivery: ids.Delivery,
			Contribution: ids.Contribution, Command: ids.Command, Event: ids.Event, Receipt: ids.Receipt,
		},
		Payload:               normalized.source.Payload,
		AudienceRequest:       normalized.audienceRequest,
		AudienceAttestation:   normalized.audienceAttestation,
		Snapshot:              normalized.snapshot,
		GrantClosure:          normalized.authority.SenderGrantClosure,
		RecipientGrantClosure: normalized.authority.RecipientGrantClosure,
		CoreWitness:           normalized.authority.CoreWitness,
		ActorFingerprint:      normalized.actorFingerprint,
		IdempotencyHash:       normalized.idempotencyKeyHash,
		RequestDigest:         normalized.requestDigest,
	}
}

func messageDerivedResultFromReceipt(
	receipt CommunicationCommandReceipt,
	normalized messageDerivedNormalized,
) (messageDerivedResult, error) {
	projection := receipt.ResponseProjectionJSON
	messageID := projection.IDs["message_id"]
	deliveryID := projection.IDs["delivery_id"]
	if receipt.ResultKind != string(messageKind) || receipt.ResultID != messageID ||
		!validCanonicalCommunicationID(messageID) || !validCanonicalCommunicationID(deliveryID) ||
		projection.IDs["event_id"] != receipt.EventID || projection.Version < 1 ||
		projection.State != string(MessagePublished) || projection.Counts["delivery_count"] != 1 ||
		receipt.AuditSeq < 1 || len(receipt.AuditHash) != sha256.Size {
		return messageDerivedResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message receipt projection is unavailable",
		)
	}
	return messageDerivedResult{
		CommandID: receipt.CommandID, SourceMessageID: normalized.command.MessageID,
		MessageID: messageID, DeliveryID: deliveryID, EventID: receipt.EventID,
		Version: projection.Version, State: MessagePublished,
		AutomationDepth: normalized.automationDepth, AuditSeq: receipt.AuditSeq, Replayed: true,
	}, nil
}

func lockMessageDerivedOrigin(
	ctx context.Context,
	tx *communicationTx,
	normalized messageDerivedNormalized,
	locked messageLifecycleLocked,
) error {
	receipts, err := tx.repo(communicationCommandKind)
	if err != nil {
		return err
	}
	rows, page, err := receipts.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colCommCommandScope, Op: model.OpEq, Value: messageLifecycleOverdueScope},
			{Column: colEventID, Op: model.OpEq, Value: normalized.command.OriginEventID.String()},
		},
		Limit: 2,
	})
	if err != nil {
		return err
	}
	if page.HasMore || len(rows) != 1 {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation origin receipt is unavailable",
		)
	}
	receiptID, err := model.ParseID(rows[0].String(model.ColID))
	if err != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation receipt identity is malformed",
		)
	}
	record, err := tx.lockRecord(ctx, communicationCommandKind, receiptID)
	if err != nil {
		return err
	}
	receipt, err := communicationCommandReceiptFromRecord(record)
	if err != nil || receipt.CommandScope != messageLifecycleOverdueScope ||
		receipt.ResultKind != string(messageKind) || receipt.ResultID != normalized.command.MessageID ||
		receipt.EventID != normalized.command.OriginEventID ||
		receipt.ResponseProjectionJSON.IDs["message_id"] != normalized.command.MessageID ||
		receipt.ResponseProjectionJSON.Counts["delivery_count"] < 1 {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation receipt crosses Message lineage",
		)
	}
	events, err := tx.repo(workEventKind)
	if err != nil {
		return err
	}
	eventRows, eventPage, err := events.List(ctx, model.Query{
		Filters: []model.Filter{{
			Column: colEventID, Op: model.OpEq, Value: normalized.command.OriginEventID.String(),
		}},
		Limit: 2,
	})
	if err != nil {
		return err
	}
	if eventPage.HasMore || len(eventRows) != 1 {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation origin Event is unavailable",
		)
	}
	eventRowID, err := model.ParseID(eventRows[0].String(model.ColID))
	if err != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation Event identity is malformed",
		)
	}
	eventRecord, err := tx.lockRecord(ctx, workEventKind, eventRowID)
	if err != nil {
		return err
	}
	var payload messageLifecycleEventProjection
	if eventRecord.String(colEventType) != messageLifecycleOverdueEvent ||
		json.Unmarshal([]byte(eventRecord.String(colEventPayload)), &payload) != nil ||
		payload.SchemaVersion != 1 || payload.Command != messageLifecycleOverdue ||
		payload.MessageID != normalized.command.MessageID || payload.DeliveryCount < 1 {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation Event crosses Message lineage",
		)
	}
	overdue := false
	for _, delivery := range locked.deliveries {
		if delivery.Required && delivery.State == DeliveryExpired && delivery.AckDueAt != nil &&
			!tx.now.Time().Before(*delivery.AckDueAt) {
			overdue = true
			break
		}
	}
	if !overdue {
		return communicationError(
			ErrInvalidCommunicationTransition,
			"overdue escalation requires an unacknowledged expired required Delivery",
		)
	}
	return nil
}

func materializeMessageDerivedPublish(
	dbNow time.Time,
	normalized messageDerivedNormalized,
	lockedSource messageLifecycleLocked,
	lockedPublish directNoticeLockedState,
	ids messageDerivedIDs,
) (MessagePublishPlan, error) {
	required := lockedPublish.Channel.DefaultAckPolicy != AckPolicyNone
	ackQuorum := int64(0)
	var ackDueAt *time.Time
	if required {
		if lockedPublish.Channel.DefaultAckTimeoutMS <= 0 ||
			lockedPublish.Channel.DefaultAckTimeoutMS > math.MaxInt64/int64(time.Millisecond) {
			return MessagePublishPlan{}, communicationError(
				ErrInvalidCommunicationModel, "derived Message Channel Ack timeout is invalid",
			)
		}
		due := dbNow.Add(time.Duration(lockedPublish.Channel.DefaultAckTimeoutMS) * time.Millisecond)
		if !due.After(dbNow) {
			return MessagePublishPlan{}, communicationError(
				ErrInvalidCommunicationModel, "derived Message Ack deadline overflows DB time",
			)
		}
		ackDueAt = &due
		if lockedPublish.Channel.DefaultAckPolicy == AckPolicyQuorum {
			ackQuorum = 1
		}
	}
	entity := CommunicationEntity{
		ID: ids.Message, TenantID: normalized.scope.TenantID,
		WorkspaceID: normalized.scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
	}
	draft := Message{
		MutableCommunicationEntity: MutableCommunicationEntity{
			CommunicationEntity: entity, UpdatedAt: dbNow,
		},
		ChannelID: lockedPublish.Channel.ID, WorkItemID: lockedSource.message.WorkItemID,
		ThreadID: ids.Message, Kind: lockedSource.message.Kind, State: MessageDraft,
		Sender: normalized.authority.Actor, Payload: cloneProtectedPayload(lockedSource.message.Payload),
		LabelsJSON: append([]byte(nil), lockedSource.message.LabelsJSON...),
		LabelsHash: append([]byte(nil), lockedSource.message.LabelsHash...),
		Urgency:    lockedSource.message.Urgency, AckPolicy: lockedPublish.Channel.DefaultAckPolicy,
		AckQuorum: ackQuorum, AvailableAt: dbNow, AckDueAt: ackDueAt,
		AutomationDepth: normalized.automationDepth,
	}
	var parent *Message
	if normalized.action == messageDerivedEscalate {
		draft.ThreadID = lockedSource.message.ThreadID
		draft.Kind = MessageSystem
		draft.ReplyToID = lockedSource.message.ID
		draft.OriginEventID = normalized.command.OriginEventID
		value := lockedSource.message
		parent = &value
	} else {
		draft.SupersedesID = lockedSource.message.ID
		draft.OriginEventID = lockedSource.message.OriginEventID
	}
	resolved := normalized.snapshot.Contributions[0]
	audience := MessageAudience{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: ids.Audience, TenantID: normalized.scope.TenantID,
			WorkspaceID: normalized.scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
		}},
		MessageID: draft.ID, Ordinal: 1, Selector: resolved.Selector,
		ChannelACLRevision:   lockedPublish.Channel.ACLRevision,
		RouteRevision:        lockedPublish.Channel.RouteRevision,
		SubscriptionRevision: lockedPublish.Channel.SubscriptionRevision,
		DirectoryEpoch:       normalized.snapshot.Epoch,
		DirectorySnapshotAt:  normalized.snapshot.ObservedAt, ResolvedCount: 1,
	}
	selectorBytes, err := canonicalJSON(resolved.Selector)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	selectorHash := sha256.Sum256(selectorBytes)
	audience.SelectorHash = selectorHash[:]
	contribution := MessageAudienceRecipient{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: ids.Contribution, TenantID: normalized.scope.TenantID,
			WorkspaceID: normalized.scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
		}},
		MessageAudienceID: audience.ID, MessageDeliveryID: ids.Delivery,
		Recipient: resolved.Recipient.Recipient, RecipientEpoch: resolved.Recipient.RecipientEpoch,
		Required: resolved.Required, WakePolicy: resolved.WakePolicy,
		RouteReasons: append([]RouteReason(nil), resolved.RouteReasons...), Selector: resolved.Selector,
		DirectoryEpoch:       normalized.snapshot.Epoch,
		ChannelACLRevision:   lockedPublish.Channel.ACLRevision,
		RouteRevision:        lockedPublish.Channel.RouteRevision,
		SubscriptionRevision: lockedPublish.Channel.SubscriptionRevision,
		CausalKind:           resolved.CausalKind, CausalRef: resolved.CausalRef,
		ObservedSessionSID:     resolved.ObservedSessionSID,
		ObservedClaimFence:     resolved.ObservedClaimFence,
		SubscriptionID:         resolved.SubscriptionID,
		SubscriptionGeneration: resolved.SubscriptionGeneration,
		RouteRuleID:            resolved.RouteRuleID, RouteRuleGeneration: resolved.RouteRuleGeneration,
	}
	if resolved.CausalFact != nil {
		contribution.CausalFactKind = resolved.CausalFact.Kind
		contribution.CausalFactID = resolved.CausalFact.ID
		contribution.CausalFactVersion = resolved.CausalFact.Version
	}
	if resolved.OriginalSubscriber != nil {
		original := *resolved.OriginalSubscriber
		contribution.OriginalSubscriber = &original
	}
	contribution.CausalArcHash, err = CanonicalAudienceCausalArcHash(contribution)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	audience.ResolvedHash, err = canonicalResolvedAudienceHash(
		audience, []MessageAudienceRecipient{contribution},
	)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	fold, err := FoldAudienceContributions([]MessageAudienceRecipient{contribution})
	if err != nil {
		return MessagePublishPlan{}, err
	}
	delivery := MessageDelivery{
		MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: ids.Delivery, TenantID: normalized.scope.TenantID,
			WorkspaceID: normalized.scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
		}, UpdatedAt: dbNow},
		MessageID: draft.ID, Recipient: fold.Recipient,
		RecipientEpoch: resolved.Recipient.RecipientEpoch, Required: fold.Required,
		RouteReasons: append([]RouteReason(nil), fold.RouteReasons...), WakePolicy: fold.WakePolicy,
		State: DeliveryAvailable, AvailableAt: dbNow,
	}
	if required {
		delivery.AckDueAt = ackDueAt
	}
	directoryFact, err := DirectorySnapshotAuthorityFact(normalized.snapshot)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	labels := ChannelLabelSnapshot{
		Scope: normalized.scope, ChannelID: lockedPublish.Channel.ID,
		RouteRevision: lockedPublish.Channel.RouteRevision, ObservedAt: dbNow,
		Definitions: append([]ChannelLabelDefinition(nil), lockedPublish.Labels...), SameTransaction: true,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "channel_labels_locked",
			EvidenceRef: "same_tx_channel_labels",
		},
	}
	return PlanMessagePublish(MessagePublishInput{
		Draft: draft, Channel: lockedPublish.Channel,
		AudienceRequest:     normalized.audienceRequest,
		AudienceAttestation: normalized.audienceAttestation, Snapshot: normalized.snapshot,
		Audiences:     []MessageAudience{audience},
		Contributions: []MessageAudienceRecipient{contribution},
		Deliveries:    []MessageDelivery{delivery}, Labels: labels,
		SendGate: SendGateEvidence{
			Scope: normalized.scope, ChannelID: lockedPublish.Channel.ID,
			ChannelACLRevision: lockedPublish.Channel.ACLRevision, DBNow: dbNow,
			Principal: normalized.authority.Principal, Core: normalized.authority.CoreWitness,
			DirectoryEpoch: directoryFact, CurrentChannelWriteGrant: lockedPublish.WriteEvidence,
		},
		Principal: normalized.authority.Principal, Sender: normalized.authority.Actor,
		SenderResolution: normalized.authority.SenderResolution,
		SourceKind:       normalized.sourceKind, EventType: normalized.eventType,
		Parent: parent, DeliverySequenceGuard: lockedPublish.DeliveryGuard, DBNow: dbNow,
	})
}

func persistMessageDerivedPlan(
	ctx context.Context,
	tx *communicationTx,
	plan MessagePublishPlan,
) error {
	draftRecord, err := messageToRecord(plan.Before, plan.RequiredCount)
	if err != nil {
		return err
	}
	if _, err = tx.createWithID(ctx, messageKind, plan.Before.ID, draftRecord); err != nil {
		return err
	}
	if plan.GuardAdvance == nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message did not allocate a Delivery sequence",
		)
	}
	guardRecord, err := communicationGuardToRecord(plan.GuardAdvance.After)
	if err != nil {
		return err
	}
	guardRecord[model.ColVersion] = plan.GuardAdvance.Before.Version
	if _, err = tx.update(ctx, communicationGuardKind, guardRecord); err != nil {
		return err
	}
	for _, audience := range plan.Audiences {
		record, encodeErr := messageAudienceToRecord(audience)
		if encodeErr != nil {
			return encodeErr
		}
		if _, createErr := tx.createWithID(ctx, messageAudienceKind, audience.ID, record); createErr != nil {
			return createErr
		}
	}
	for _, delivery := range plan.Deliveries {
		record, encodeErr := messageDeliveryToRecord(delivery)
		if encodeErr != nil {
			return encodeErr
		}
		if _, createErr := tx.createWithID(ctx, messageDeliveryKind, delivery.ID, record); createErr != nil {
			return createErr
		}
	}
	for _, contribution := range plan.Contributions {
		record, encodeErr := messageAudienceRecipientToRecord(contribution)
		if encodeErr != nil {
			return encodeErr
		}
		if _, createErr := tx.createWithID(
			ctx, messageAudienceRecipientKind, contribution.ID, record,
		); createErr != nil {
			return createErr
		}
	}
	publishedRecord, err := messageToRecord(plan.After, plan.RequiredCount)
	if err != nil {
		return err
	}
	publishedRecord[model.ColVersion] = plan.Before.Version
	_, err = tx.update(ctx, messageKind, publishedRecord)
	return err
}

func persistMessageDerivedEvent(
	ctx context.Context,
	tx *communicationTx,
	lockedSource messageLifecycleLocked,
	normalized messageDerivedNormalized,
	plan MessagePublishPlan,
	ids messageDerivedIDs,
	audit model.AuditEvent,
	planHash []byte,
) error {
	aggregateKind := messageKind
	aggregateID := plan.After.ID
	sequence := plan.After.LastEventSeq
	if plan.After.WorkItemID != "" {
		item, err := advanceMessageLifecycleWorkAggregate(ctx, tx, lockedSource)
		if err != nil {
			return err
		}
		aggregateKind = workItemKind
		aggregateID = plan.After.WorkItemID
		sequence = item.Int(colWorkLastEventSeq)
	}
	payload, err := canonicalJSON(messageDerivedEventProjection{
		SchemaVersion: 1, Command: normalized.action,
		SourceMessageID: normalized.command.MessageID, MessageID: plan.After.ID,
		WorkItemID: plan.After.WorkItemID, OriginEventID: plan.After.OriginEventID,
		Step: normalized.command.Step, Recipient: normalized.command.Recipient,
		AutomationDepth: plan.After.AutomationDepth, State: plan.After.State,
		Version: plan.After.Version, PlanHash: hex.EncodeToString(planHash),
	})
	if err != nil || len(payload) > 16*1024 || sequence < 1 {
		return communicationError(
			ErrInvalidCommunicationModel, "derived Message Event payload is invalid",
		)
	}
	eventType := messageDerivedReroutedEvent
	if normalized.action == messageDerivedEscalate {
		eventType = messageDerivedEscalatedEvent
	}
	if _, err = tx.create(ctx, workEventKind, model.Record{
		colWorkWorkspaceID: plan.After.WorkspaceID.String(), colEventID: ids.Event.String(),
		colEventAggregateKind: string(aggregateKind), colEventAggregateID: aggregateID.String(),
		colEventSeq: sequence, colEventType: eventType,
		colEventActorKind: string(normalized.authority.Actor.Kind),
		colEventActorRef:  normalized.authority.Actor.Ref, colEventOccurredAt: tx.now.String(),
		colEventPayload: string(payload), colEventPayloadHash: hashBytes(payload),
		colEventCommandID: ids.Command.String(), colEventAuditSeq: audit.Seq,
		colEventAuditHash: append([]byte(nil), audit.Hash...),
	}); err != nil {
		return err
	}
	_, err = tx.create(ctx, workOutboxKind, model.Record{
		colWorkWorkspaceID: plan.After.WorkspaceID.String(), colOutboxEventID: ids.Event.String(),
		colOutboxState: "pending", colOutboxAttempts: int64(0),
		colOutboxNextAttemptAt: tx.now.String(), colOutboxClaimOwner: nil,
		colOutboxClaimUntil: nil, colOutboxPublishedAt: nil, colOutboxLastOutcome: nil,
	})
	return err
}

func buildMessageDerivedReceipt(
	normalized messageDerivedNormalized,
	ids messageDerivedIDs,
	plan MessagePublishPlan,
	fulfillment FulfillmentProjection,
	planHash []byte,
	audit model.AuditEvent,
	completedAt time.Time,
) (CommunicationCommandReceipt, error) {
	projection := CommunicationCommandResponseProjection{
		IDs: map[string]model.ID{
			"message_id": plan.After.ID, "delivery_id": plan.Deliveries[0].ID,
			"event_id": ids.Event,
		},
		Version: plan.After.Version, State: string(plan.After.State),
		Counts: map[string]int64{
			"delivery_count": 1, "resolved_count": 1,
			"required": fulfillment.Required, "acknowledged": fulfillment.Acknowledged,
			"viable": fulfillment.Viable, "unmet": fulfillment.Unmet, "quorum": fulfillment.Quorum,
		},
		Digests: map[string][]byte{
			"request":  append([]byte(nil), normalized.requestDigest...),
			"plan":     append([]byte(nil), planHash...),
			"audience": append([]byte(nil), plan.After.AudienceHash...),
			"payload":  append([]byte(nil), plan.After.Payload.Digest...),
		},
	}
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: ids.Receipt, TenantID: normalized.scope.TenantID,
			WorkspaceID: normalized.scope.WorkspaceID, Version: 1, CreatedAt: completedAt,
		}},
		CommandID: ids.Command, ActorFingerprint: append([]byte(nil), normalized.actorFingerprint...),
		CommandScope:       normalized.commandScope,
		IdempotencyKeyHash: append([]byte(nil), normalized.idempotencyKeyHash...),
		RequestDigest:      append([]byte(nil), normalized.requestDigest...),
		PlanHash:           append([]byte(nil), planHash...), ResultKind: string(messageKind),
		ResultID: plan.After.ID, HTTPStatus: http.StatusAccepted,
		ResponseProjectionJSON: projection, EventID: ids.Event,
		AuditSeq: audit.Seq, AuditHash: append([]byte(nil), audit.Hash...), CompletedAt: completedAt,
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

func (s *messageDerivedService) applyDerived(
	ctx context.Context,
	normalized messageDerivedNormalized,
	ids messageDerivedIDs,
) (messageDerivedResult, error) {
	if !ids.valid() {
		return messageDerivedResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "derived Message identities are unavailable",
		)
	}
	var result messageDerivedResult
	lifecycleAuthority := messageLifecycleAuthority{
		Actor:      normalized.authority.Actor,
		Facts:      append([]store.AuthorizationFactRef(nil), normalized.authority.Facts...),
		ObservedAt: normalized.authority.ObservedAt,
		FreshUntil: normalized.authority.FreshUntil,
		Evidence:   normalized.authority.ActionEvidence,
	}
	err := s.module.mutateMessageLifecycle(
		ctx, normalized.scope, lifecycleAuthority,
		func(tx *communicationTx, sc store.Scope) error {
			if err := tx.lockAuthoritySnapshot(ctx, normalized.authority.Facts); err != nil {
				return err
			}
			if err := tx.lockTransaction(ctx, messageDerivedLockKey(normalized)); err != nil {
				return err
			}
			receipt, found, err := lookupMessageLifecycleReceipt(
				ctx, tx, normalized.scope, normalized.commandScope,
				normalized.actorFingerprint, normalized.idempotencyKeyHash,
				normalized.requestDigest,
			)
			if err != nil {
				return err
			}
			if found {
				if err := tx.refreshNow(ctx); err != nil {
					return err
				}
				replayed, err := messageDerivedResultFromReceipt(receipt, normalized)
				if err != nil {
					return err
				}
				result = replayed
				return nil
			}
			lockedSource, err := lockMessageLifecycleCarrier(
				ctx, tx, sc, normalized.command.MessageID,
			)
			if err != nil {
				return err
			}
			if lockedSource.message.Version != normalized.command.ExpectedVersion {
				return fmt.Errorf("%w: derived Message source version changed", store.ErrConflict)
			}
			lockedSourceHash, err := canonicalMessageDerivedSourceHash(
				lockedSource.message, lockedSource.deliveries,
			)
			if err != nil || !equalMessageLifecycleBytes(lockedSourceHash, normalized.sourceHash) {
				return fmt.Errorf("%w: derived Message source changed after preflight", store.ErrConflict)
			}
			if normalized.action == messageDerivedReroute {
				if !oneOf(
					lockedSource.message.State, MessageRetracted, MessageExpired, MessageDiscarded,
				) {
					return communicationError(
						ErrInvalidCommunicationTransition,
						"Message reroute predecessor is no longer terminal",
					)
				}
			} else {
				if lockedSource.message.State != MessagePublished {
					return communicationError(
						ErrInvalidCommunicationTransition,
						"overdue escalation source is no longer published",
					)
				}
				if err := lockMessageDerivedOrigin(ctx, tx, normalized, lockedSource); err != nil {
					return err
				}
			}
			lockedPublish, err := lockDirectNoticePublishState(
				ctx, tx, messageDerivedLockPreflight(normalized, ids),
			)
			if err != nil {
				return err
			}
			plan, err := materializeMessageDerivedPublish(
				tx.now.Time(), normalized, lockedSource, lockedPublish, ids,
			)
			if err != nil {
				return err
			}
			if len(plan.Deliveries) != 1 || len(plan.Audiences) != 1 ||
				len(plan.Contributions) != 1 || plan.GuardAdvance == nil ||
				plan.After.State != MessagePublished || plan.After.AutomationDepth != normalized.automationDepth ||
				plan.After.ID != ids.Message {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "derived Message planner returned a non-atomic shape",
				)
			}
			fulfillment, err := projectInitialDirectNoticeFulfillment(plan, tx.now.Time())
			if err != nil {
				return err
			}
			planBytes, err := canonicalJSON(struct {
				SchemaVersion int64                           `json:"schema_version"`
				Action        messageDerivedAction            `json:"action"`
				Command       messageDerivedCommandProjection `json:"command"`
				SourceHash    []byte                          `json:"source_hash"`
				RequestDigest []byte                          `json:"request_digest"`
				Facts         []store.AuthorizationFactRef    `json:"facts"`
				Plan          MessagePublishPlan              `json:"plan"`
			}{
				1, normalized.action, normalized.command, normalized.sourceHash,
				normalized.requestDigest, normalized.authority.Facts, plan,
			})
			if err != nil {
				return err
			}
			planHash := sha256.Sum256(planBytes)
			auditAction := messageDerivedRerouteAudit
			if normalized.action == messageDerivedEscalate {
				auditAction = messageDerivedEscalateAudit
			}
			auditActor, auditKind := messageLifecycleAuditActor(normalized.authority.Actor)
			audit, err := tx.appendAudit(ctx, model.AuditDraft{
				Actor: auditActor, ActorKind: auditKind, Action: auditAction,
				TargetKind: messageKind, TargetID: plan.After.ID, PayloadHash: planHash[:],
				Meta: map[string]any{
					"workspace_id":      normalized.scope.WorkspaceID.String(),
					"source_message_id": normalized.command.MessageID.String(),
					"command_scope":     normalized.commandScope,
					"automation_depth":  normalized.automationDepth,
					"origin_event_id":   normalized.command.OriginEventID.String(),
					"step":              normalized.command.Step,
				},
			})
			if err != nil {
				return err
			}
			if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "derived Message audit anchor is unavailable",
				)
			}
			if err := persistMessageDerivedPlan(ctx, tx, plan); err != nil {
				return err
			}
			if err := persistMessageDerivedEvent(
				ctx, tx, lockedSource, normalized, plan, ids, audit, planHash[:],
			); err != nil {
				return err
			}
			result = messageDerivedResult{
				CommandID: ids.Command, SourceMessageID: normalized.command.MessageID,
				MessageID: plan.After.ID, DeliveryID: plan.Deliveries[0].ID,
				EventID: ids.Event, Version: plan.After.Version, State: plan.After.State,
				AutomationDepth: plan.After.AutomationDepth, AuditSeq: audit.Seq,
			}
			receipt, err = buildMessageDerivedReceipt(
				normalized, ids, plan, fulfillment, planHash[:], audit, tx.now.Time(),
			)
			if err != nil {
				return err
			}
			return persistMessageLifecycleReceipt(ctx, tx, receipt)
		},
	)
	return result, err
}

func equalMessageLifecycleBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
