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
	"net/http"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type messageLifecycleLocked struct {
	message       Message
	deliveries    []MessageDelivery
	decision      *DecisionRequest
	handoff       *Handoff
	requiredCount int64
	workItem      model.Record
	workRepo      store.TransactionStampedGenericRepo
}

type messageLifecycleEventProjection struct {
	SchemaVersion int64                  `json:"schema_version"`
	Command       messageLifecycleAction `json:"command"`
	MessageID     model.ID               `json:"message_id"`
	WorkItemID    model.ID               `json:"work_item_id,omitempty"`
	State         MessageState           `json:"state"`
	Version       int64                  `json:"version"`
	DeliveryCount int64                  `json:"delivery_count"`
	DecisionID    model.ID               `json:"decision_request_id,omitempty"`
	HandoffID     model.ID               `json:"handoff_id,omitempty"`
	Fulfillment   *FulfillmentProjection `json:"fulfillment,omitempty"`
	PlanHash      string                 `json:"plan_hash"`
}

func (m *Module) mutateMessageLifecycle(
	ctx context.Context,
	scope DirectoryScopeRef,
	authority messageLifecycleAuthority,
	fn func(*communicationTx, store.Scope) error,
) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if fn == nil {
		return communicationTransactionUnavailable("Message lifecycle mutation callback", nil)
	}
	binding := &communicationRequestAuthorityBindingID{marker: 1}
	request := communicationRequestAuthoritySnapshot{
		facts:      append([]store.AuthorizationFactRef(nil), authority.Facts...),
		observedAt: authority.ObservedAt.UTC(), freshUntil: authority.FreshUntil.UTC(),
		bindingID: binding,
	}
	if err := request.validate(); err != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle authority snapshot is malformed",
		)
	}
	var callbackAttempted atomic.Bool
	return m.communicationData(scope.TenantID).Mutate(ctx, func(sc store.Scope) error {
		if !callbackAttempted.CompareAndSwap(false, true) {
			return communicationTransactionUnavailable(
				"Message lifecycle mutation callback was already entered", nil,
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

func messageLifecycleLockKey(
	scope DirectoryScopeRef,
	commandScope string,
	actorFingerprint []byte,
	idempotencyKeyHash []byte,
) string {
	return fmt.Sprintf(
		"sessions.communication.message.lifecycle:%s:%s:%x:%x",
		scope.WorkspaceID, commandScope, actorFingerprint, idempotencyKeyHash,
	)
}

func lockMessageLifecycleCarrier(
	ctx context.Context,
	tx *communicationTx,
	sc store.Scope,
	messageID model.ID,
) (messageLifecycleLocked, error) {
	messageRecord, err := tx.lockRecord(ctx, messageKind, messageID)
	if err != nil {
		return messageLifecycleLocked{}, err
	}
	deliveryRepo, err := tx.repo(messageDeliveryKind)
	if err != nil {
		return messageLifecycleLocked{}, err
	}
	rows, page, err := deliveryRepo.List(ctx, model.Query{
		Filters: []model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()}},
		Sort:    []model.Sort{{Column: colCommDeliverySeq}}, Limit: messageLifecycleCarrierBound,
	})
	if err != nil || page.HasMore {
		return messageLifecycleLocked{}, communicationError(
			ErrCommunicationEvidenceUnknown, "locked Message Delivery set is unavailable",
		)
	}
	locked := messageLifecycleLocked{deliveries: make([]MessageDelivery, 0, len(rows))}
	seen := make(map[model.ID]struct{}, len(rows))
	for _, row := range rows {
		id, parseErr := model.ParseID(row.String(model.ColID))
		if parseErr != nil || !validCanonicalCommunicationID(id) {
			return messageLifecycleLocked{}, communicationError(
				ErrCommunicationEvidenceUnknown, "locked Message Delivery identity is malformed",
			)
		}
		if _, duplicate := seen[id]; duplicate {
			return messageLifecycleLocked{}, communicationError(
				ErrCommunicationEvidenceUnknown, "locked Message Delivery set contains a duplicate",
			)
		}
		seen[id] = struct{}{}
		row, err = tx.lockRecord(ctx, messageDeliveryKind, id)
		if err != nil {
			return messageLifecycleLocked{}, err
		}
		delivery, decodeErr := messageDeliveryFromRecord(row)
		if decodeErr != nil || delivery.MessageID != messageID {
			return messageLifecycleLocked{}, communicationError(
				ErrCommunicationEvidenceUnknown, "locked Message Delivery set is malformed",
			)
		}
		locked.deliveries = append(locked.deliveries, delivery)
		if delivery.Required {
			locked.requiredCount++
		}
	}
	locked.message, err = messageFromRecord(messageRecord, locked.requiredCount)
	if err != nil {
		return messageLifecycleLocked{}, communicationError(
			ErrCommunicationEvidenceUnknown, "locked Message is malformed",
		)
	}
	if locked.message.Kind == MessageDecisionRequest {
		request, found, lockErr := lockLifecycleDecision(ctx, tx, messageID)
		if lockErr != nil || !found {
			return messageLifecycleLocked{}, communicationError(
				ErrCommunicationEvidenceUnknown, "linked DecisionRequest is unavailable",
			)
		}
		locked.decision = &request
	}
	if locked.message.Kind == MessageHandoffOffer {
		handoff, found, lockErr := lockLifecycleHandoff(ctx, tx, messageID)
		if lockErr != nil || !found {
			return messageLifecycleLocked{}, communicationError(
				ErrCommunicationEvidenceUnknown, "linked Handoff is unavailable",
			)
		}
		locked.handoff = &handoff
	}
	if locked.message.WorkItemID != "" {
		repo, err := lifecycleWorkItemRepository(sc)
		if err != nil {
			return messageLifecycleLocked{}, err
		}
		locker, ok := repo.(store.RowLocker[model.Record])
		if !ok {
			return messageLifecycleLocked{}, communicationTransactionUnavailable(
				"Message lifecycle WorkItem row lock", nil,
			)
		}
		item, err := runCommunicationBoundAuthorityObservation(
			tx.boundAuthorityState,
			func() (model.Record, error) { return locker.Lock(ctx, locked.message.WorkItemID) },
		)
		if err != nil {
			return messageLifecycleLocked{}, err
		}
		if recordID(item) != locked.message.WorkItemID ||
			item.String(colWorkWorkspaceID) != locked.message.WorkspaceID.String() {
			return messageLifecycleLocked{}, communicationError(
				ErrCommunicationEvidenceUnknown, "Message lifecycle WorkItem crossed lineage",
			)
		}
		locked.workItem, locked.workRepo = item, repo
	}
	return locked, nil
}

func lifecycleWorkItemRepository(sc store.Scope) (store.TransactionStampedGenericRepo, error) {
	repo, err := sc.Ext(workItemKind)
	if err != nil {
		return nil, err
	}
	stamped, ok := repo.(store.TransactionStampedGenericRepo)
	if !ok {
		return nil, communicationTransactionUnavailable(
			"Message lifecycle WorkItem transaction-stamped repository", nil,
		)
	}
	return stamped, nil
}

func lockLifecycleDecision(
	ctx context.Context,
	tx *communicationTx,
	messageID model.ID,
) (DecisionRequest, bool, error) {
	repo, err := tx.repo(decisionRequestKind)
	if err != nil {
		return DecisionRequest{}, false, err
	}
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()}},
		Limit:   2,
	})
	if err != nil || page.HasMore || len(rows) > 1 {
		return DecisionRequest{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "linked DecisionRequest lookup is ambiguous",
		)
	}
	if len(rows) == 0 {
		return DecisionRequest{}, false, nil
	}
	id, err := model.ParseID(rows[0].String(model.ColID))
	if err != nil {
		return DecisionRequest{}, false, err
	}
	row, err := tx.lockRecord(ctx, decisionRequestKind, id)
	if err != nil {
		return DecisionRequest{}, false, err
	}
	request, err := decisionRequestFromRecord(row)
	return request, err == nil, err
}

func lockLifecycleHandoff(
	ctx context.Context,
	tx *communicationTx,
	messageID model.ID,
) (Handoff, bool, error) {
	repo, err := tx.repo(handoffKind)
	if err != nil {
		return Handoff{}, false, err
	}
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()}},
		Limit:   2,
	})
	if err != nil || page.HasMore || len(rows) > 1 {
		return Handoff{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "linked Handoff lookup is ambiguous",
		)
	}
	if len(rows) == 0 {
		return Handoff{}, false, nil
	}
	id, err := model.ParseID(rows[0].String(model.ColID))
	if err != nil {
		return Handoff{}, false, err
	}
	row, err := tx.lockRecord(ctx, handoffKind, id)
	if err != nil {
		return Handoff{}, false, err
	}
	handoff, err := handoffFromRecord(row)
	return handoff, err == nil, err
}

func messageLifecycleCarrierWitness(
	locked messageLifecycleLocked,
	dbNow time.Time,
) (MessageTerminalCarrierSetWitness, error) {
	digest, err := CanonicalFulfillmentDeliverySetDigest(locked.deliveries)
	if err != nil {
		return MessageTerminalCarrierSetWitness{}, err
	}
	witness := MessageTerminalCarrierSetWitness{
		Scope: DirectoryScopeRef{
			TenantID: locked.message.TenantID, WorkspaceID: locked.message.WorkspaceID,
		},
		MessageID: locked.message.ID, DeliveryCount: int64(len(locked.deliveries)),
		DeliveryDigest: digest, ObservedAt: dbNow,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "terminal_carriers_locked",
			EvidenceRef: "message-carriers:" + locked.message.ID.String(),
		},
	}
	if locked.decision != nil {
		witness.DecisionRequestID = locked.decision.ID
	}
	if locked.handoff != nil {
		witness.HandoffID = locked.handoff.ID
	}
	return witness, nil
}

func verifyMessageLifecyclePrepared(
	locked messageLifecycleLocked,
	prepared messageLifecyclePrepared,
	normalized messageLifecycleNormalized,
) error {
	hash, err := CanonicalProtectedPayloadEnvelopeHash(locked.message.Payload)
	if err != nil || !bytes.Equal(hash, prepared.messageHash) ||
		ValidateProtectedPayloadSlot(
			prepared.messageReason, PayloadSlotMessageTerminalReason,
			protectedPayloadPolicyFrom(locked.message.Payload),
		) != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Message terminal content changed after preparation",
		)
	}
	if locked.decision != nil {
		if prepared.decisionID != locked.decision.ID ||
			ValidateProtectedPayloadSlot(
				prepared.decisionReason, PayloadSlotDecisionResponse,
				protectedPayloadPolicyFrom(locked.decision.Request),
			) != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "Decision terminal content changed after preparation",
			)
		}
	} else if prepared.decisionID != "" {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "prepared Decision terminal carrier disappeared",
		)
	}
	if locked.handoff != nil {
		if prepared.handoffID != locked.handoff.ID ||
			ValidateProtectedPayloadSlot(
				prepared.handoffReason, PayloadSlotHandoffTerminalReason,
				protectedPayloadPolicyFrom(locked.handoff.Payload),
			) != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff terminal content changed after preparation",
			)
		}
	} else if prepared.handoffID != "" {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "prepared Handoff terminal carrier disappeared",
		)
	}
	if locked.message.ID != normalized.command.MessageID ||
		locked.message.TenantID != normalized.scope.TenantID ||
		locked.message.WorkspaceID != normalized.scope.WorkspaceID {
		return communicationError(ErrCommunicationNotFound, "Message lifecycle target is not visible")
	}
	return nil
}

func lookupMessageLifecycleReceipt(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	commandScope string,
	actorFingerprint []byte,
	idempotencyKeyHash []byte,
	requestDigest []byte,
) (CommunicationCommandReceipt, bool, error) {
	repo, err := tx.repo(communicationCommandKind)
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colCommCommandScope, Op: model.OpEq, Value: commandScope},
			{Column: colCommActorFingerprint, Op: model.OpEq, Value: actorFingerprint},
			{Column: colCommIdempotencyKeyHash, Op: model.OpEq, Value: idempotencyKeyHash},
		},
		Limit: 2,
	})
	if err != nil || page.HasMore || len(rows) > 1 {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle receipt lookup is ambiguous",
		)
	}
	if len(rows) == 0 {
		return CommunicationCommandReceipt{}, false, nil
	}
	id, err := model.ParseID(rows[0].String(model.ColID))
	if err != nil || !validCanonicalCommunicationID(id) {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle receipt identity is malformed",
		)
	}
	record, err := tx.lockRecord(ctx, communicationCommandKind, id)
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	receipt, err := communicationCommandReceiptFromRecord(record)
	if err != nil {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle receipt is malformed",
		)
	}
	if !bytes.Equal(receipt.RequestDigest, requestDigest) {
		return CommunicationCommandReceipt{}, false, fmt.Errorf(
			"%w: Message lifecycle idempotency key was reused", store.ErrConflict,
		)
	}
	if receipt.TenantID != scope.TenantID || receipt.WorkspaceID != scope.WorkspaceID ||
		receipt.CommandScope != commandScope ||
		!bytes.Equal(receipt.ActorFingerprint, actorFingerprint) ||
		!bytes.Equal(receipt.IdempotencyKeyHash, idempotencyKeyHash) {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle receipt crossed scope",
		)
	}
	return receipt, true, nil
}

func messageLifecycleResultFromReceipt(
	receipt CommunicationCommandReceipt,
	messageID model.ID,
) (messageLifecycleResult, error) {
	projection := receipt.ResponseProjectionJSON
	if receipt.ResultKind != string(messageKind) || receipt.ResultID != messageID ||
		projection.IDs["message_id"] != messageID || projection.IDs["event_id"] != receipt.EventID ||
		projection.Version < 1 || !MessageState(projection.State).Valid() ||
		receipt.AuditSeq < 1 || len(receipt.AuditHash) != sha256.Size {
		return messageLifecycleResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle receipt projection is unavailable",
		)
	}
	return messageLifecycleResult{
		CommandID: receipt.CommandID, MessageID: messageID, EventID: receipt.EventID,
		Version: projection.Version, State: MessageState(projection.State),
		DeliveryChanges:   projection.Counts["delivery_count"],
		DecisionRequestID: projection.IDs["request_id"],
		HandoffID:         projection.IDs["handoff_id"], AuditSeq: receipt.AuditSeq, Replayed: true,
	}, nil
}

func messageFulfillmentFromReceiptProjection(
	projection CommunicationCommandResponseProjection,
) (FulfillmentProjection, error) {
	fulfillment := FulfillmentProjection{
		Required:     projection.Counts["required"],
		Acknowledged: projection.Counts["acknowledged"],
		Viable:       projection.Counts["viable"],
		Unmet:        projection.Counts["unmet"],
		Quorum:       projection.Counts["quorum"],
	}
	if fulfillment.Required != fulfillment.Acknowledged+fulfillment.Viable+fulfillment.Unmet {
		return FulfillmentProjection{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Message overdue receipt lost its fulfillment vector",
		)
	}
	switch {
	case fulfillment.Required == 0:
		if fulfillment.Quorum != 0 {
			return FulfillmentProjection{}, communicationError(
				ErrCommunicationEvidenceUnknown, "Message overdue receipt carries an invalid quorum",
			)
		}
		fulfillment.State = FulfillmentNotRequired
	case fulfillment.Quorum == 0:
		switch {
		case fulfillment.Unmet > 0:
			fulfillment.State = FulfillmentUnmet
		case fulfillment.Acknowledged == fulfillment.Required:
			fulfillment.State = FulfillmentMet
		default:
			fulfillment.State = FulfillmentPending
		}
	case fulfillment.Quorum > fulfillment.Required:
		return FulfillmentProjection{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Message overdue receipt carries an invalid quorum",
		)
	case fulfillment.Acknowledged >= fulfillment.Quorum:
		fulfillment.State = FulfillmentMet
	case fulfillment.Acknowledged+fulfillment.Viable < fulfillment.Quorum:
		fulfillment.State = FulfillmentUnmet
	default:
		fulfillment.State = FulfillmentPending
	}
	return fulfillment, nil
}

func messageOverdueResultFromReceipt(
	receipt CommunicationCommandReceipt,
	messageID model.ID,
) (messageOverdueResult, error) {
	projection := receipt.ResponseProjectionJSON
	fulfillment, err := messageFulfillmentFromReceiptProjection(projection)
	if err != nil {
		return messageOverdueResult{}, err
	}
	if receipt.ResultKind != string(messageKind) || receipt.ResultID != messageID ||
		projection.IDs["message_id"] != messageID || projection.IDs["event_id"] != receipt.EventID ||
		projection.Version < 1 || projection.State != string(MessagePublished) ||
		!fulfillment.State.Valid() || receipt.AuditSeq < 1 ||
		len(receipt.AuditHash) != sha256.Size {
		return messageOverdueResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Message overdue receipt projection is unavailable",
		)
	}
	return messageOverdueResult{
		CommandID: receipt.CommandID, MessageID: messageID, EventID: receipt.EventID,
		Version: projection.Version, ExpiredCount: projection.Counts["delivery_count"],
		Fulfillment: fulfillment, AuditSeq: receipt.AuditSeq, Replayed: true,
	}, nil
}

func persistMessageLifecyclePlan(
	ctx context.Context,
	tx *communicationTx,
	locked messageLifecycleLocked,
	plan MessageTransitionPlan,
) error {
	// The Delivery invariant permits available -> retracted only after the
	// parent Message is already retracted (and symmetrically for expiry). Both
	// writes remain in this transaction, so make the parent transition visible
	// to the child guard first without exposing a partial commit.
	messageRecord, err := messageToRecord(plan.After, locked.requiredCount)
	if err != nil {
		return err
	}
	messageRecord[model.ColVersion] = plan.Before.Version
	if _, err = tx.update(ctx, messageKind, messageRecord); err != nil {
		return err
	}
	for _, deliveryPlan := range plan.DeliveryPlans {
		record, err := messageDeliveryToRecord(deliveryPlan.After)
		if err != nil {
			return err
		}
		record[model.ColVersion] = deliveryPlan.Before.Version
		if _, err = tx.update(ctx, messageDeliveryKind, record); err != nil {
			return err
		}
	}
	if plan.DecisionPlan != nil {
		record, err := decisionRequestToRecord(plan.DecisionPlan.After)
		if err != nil {
			return err
		}
		record[model.ColVersion] = plan.DecisionPlan.Before.Version
		if _, err = tx.update(ctx, decisionRequestKind, record); err != nil {
			return err
		}
		response, err := decisionResponseToRecord(
			plan.DecisionPlan.Response, plan.DecisionPlan.Before, plan.DecisionPlan.After,
		)
		if err != nil {
			return err
		}
		if _, err = tx.createWithID(
			ctx, decisionResponseKind, plan.DecisionPlan.Response.ID, response,
		); err != nil {
			return err
		}
	}
	if plan.HandoffPlan != nil {
		record, err := handoffToRecord(plan.HandoffPlan.After)
		if err != nil {
			return err
		}
		record[model.ColVersion] = plan.HandoffPlan.Before.Version
		if _, err = tx.update(ctx, handoffKind, record); err != nil {
			return err
		}
	}
	return nil
}

func advanceMessageLifecycleWorkAggregate(
	ctx context.Context,
	tx *communicationTx,
	locked messageLifecycleLocked,
) (model.Record, error) {
	if locked.message.WorkItemID == "" {
		return nil, nil
	}
	if locked.workRepo == nil || locked.workItem == nil {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle WorkItem aggregate is unavailable",
		)
	}
	before := cloneMessageLifecycleRecord(locked.workItem)
	before[colWorkLastEventSeq] = before.Int(colWorkLastEventSeq) + 1
	return runCommunicationBoundAuthorityEffect(
		tx.boundAuthorityState,
		func() (model.Record, error) {
			return locked.workRepo.UpdateAtTransactionTime(ctx, before)
		},
	)
}

func cloneMessageLifecycleRecord(record model.Record) model.Record {
	cloned := make(model.Record, len(record))
	for key, value := range record {
		cloned[key] = value
	}
	return cloned
}

func persistMessageLifecycleEvent(
	ctx context.Context,
	tx *communicationTx,
	locked messageLifecycleLocked,
	message Message,
	action messageLifecycleAction,
	eventType string,
	actor CommunicationActorRef,
	ids messageLifecycleIDs,
	audit model.AuditEvent,
	planHash []byte,
	deliveryCount int64,
	fulfillment *FulfillmentProjection,
) error {
	aggregateKind := messageKind
	aggregateID := message.ID
	sequence := message.LastEventSeq
	if message.WorkItemID != "" {
		item, err := advanceMessageLifecycleWorkAggregate(ctx, tx, locked)
		if err != nil {
			return err
		}
		aggregateKind = workItemKind
		aggregateID = message.WorkItemID
		sequence = item.Int(colWorkLastEventSeq)
	}
	payload, err := canonicalJSON(messageLifecycleEventProjection{
		SchemaVersion: 1, Command: action, MessageID: message.ID,
		WorkItemID: message.WorkItemID, State: message.State, Version: message.Version,
		DeliveryCount: deliveryCount, DecisionID: func() model.ID {
			if locked.decision != nil {
				return locked.decision.ID
			}
			return ""
		}(), HandoffID: func() model.ID {
			if locked.handoff != nil {
				return locked.handoff.ID
			}
			return ""
		}(), Fulfillment: fulfillment, PlanHash: hex.EncodeToString(planHash),
	})
	if err != nil || len(payload) > 16*1024 || sequence < 1 {
		return communicationError(
			ErrInvalidCommunicationModel, "Message lifecycle Event payload is invalid",
		)
	}
	if _, err = tx.create(ctx, workEventKind, model.Record{
		colWorkWorkspaceID: message.WorkspaceID.String(),
		colEventID:         ids.Event.String(), colEventAggregateKind: string(aggregateKind),
		colEventAggregateID: aggregateID.String(), colEventSeq: sequence,
		colEventType: eventType, colEventActorKind: string(actor.Kind), colEventActorRef: actor.Ref,
		colEventOccurredAt: tx.now.String(), colEventPayload: string(payload),
		colEventPayloadHash: hashBytes(payload), colEventCommandID: ids.Command.String(),
		colEventAuditSeq: audit.Seq, colEventAuditHash: append([]byte(nil), audit.Hash...),
	}); err != nil {
		return err
	}
	_, err = tx.create(ctx, workOutboxKind, model.Record{
		colWorkWorkspaceID: message.WorkspaceID.String(), colOutboxEventID: ids.Event.String(),
		colOutboxState: "pending", colOutboxAttempts: int64(0),
		colOutboxNextAttemptAt: tx.now.String(), colOutboxClaimOwner: nil,
		colOutboxClaimUntil: nil, colOutboxPublishedAt: nil, colOutboxLastOutcome: nil,
	})
	return err
}

func buildMessageLifecycleReceipt(
	scope DirectoryScopeRef,
	actorFingerprint []byte,
	idempotencyKeyHash []byte,
	requestDigest []byte,
	commandScope string,
	ids messageLifecycleIDs,
	planHash []byte,
	audit model.AuditEvent,
	result messageLifecycleResult,
	completedAt time.Time,
) (CommunicationCommandReceipt, error) {
	projectionIDs := map[string]model.ID{
		"message_id": result.MessageID, "event_id": result.EventID,
	}
	if result.DecisionRequestID != "" {
		projectionIDs["request_id"] = result.DecisionRequestID
	}
	if result.HandoffID != "" {
		projectionIDs["handoff_id"] = result.HandoffID
	}
	return newMessageLifecycleReceipt(
		scope, actorFingerprint, idempotencyKeyHash, requestDigest, commandScope,
		ids, planHash, audit,
		CommunicationCommandResponseProjection{
			IDs: projectionIDs, Version: result.Version, State: string(result.State),
			Counts: map[string]int64{"delivery_count": result.DeliveryChanges},
			Digests: map[string][]byte{
				"request": append([]byte(nil), requestDigest...),
				"plan":    append([]byte(nil), planHash...),
			},
		},
		result.MessageID, completedAt,
	)
}

func buildMessageOverdueReceipt(
	normalized messageOverdueNormalized,
	ids messageLifecycleIDs,
	planHash []byte,
	audit model.AuditEvent,
	result messageOverdueResult,
	completedAt time.Time,
) (CommunicationCommandReceipt, error) {
	return newMessageLifecycleReceipt(
		normalized.scope, normalized.actorFingerprint, normalized.idempotencyKeyHash,
		normalized.requestDigest, normalized.commandScope, ids, planHash, audit,
		CommunicationCommandResponseProjection{
			IDs:     map[string]model.ID{"message_id": result.MessageID, "event_id": result.EventID},
			Version: result.Version, State: string(MessagePublished),
			Counts: map[string]int64{
				"delivery_count": result.ExpiredCount, "required": result.Fulfillment.Required,
				"acknowledged": result.Fulfillment.Acknowledged,
				"viable":       result.Fulfillment.Viable, "unmet": result.Fulfillment.Unmet,
				"quorum": result.Fulfillment.Quorum,
			},
			Digests: map[string][]byte{
				"request": append([]byte(nil), normalized.requestDigest...),
				"plan":    append([]byte(nil), planHash...),
			},
		},
		result.MessageID, completedAt,
	)
}

func newMessageLifecycleReceipt(
	scope DirectoryScopeRef,
	actorFingerprint []byte,
	idempotencyKeyHash []byte,
	requestDigest []byte,
	commandScope string,
	ids messageLifecycleIDs,
	planHash []byte,
	audit model.AuditEvent,
	projection CommunicationCommandResponseProjection,
	messageID model.ID,
	completedAt time.Time,
) (CommunicationCommandReceipt, error) {
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: ids.Receipt, TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
			Version: 1, CreatedAt: completedAt,
		}},
		CommandID: ids.Command, ActorFingerprint: append([]byte(nil), actorFingerprint...),
		CommandScope:       commandScope,
		IdempotencyKeyHash: append([]byte(nil), idempotencyKeyHash...),
		RequestDigest:      append([]byte(nil), requestDigest...), PlanHash: append([]byte(nil), planHash...),
		ResultKind: string(messageKind), ResultID: messageID, HTTPStatus: http.StatusOK,
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

func persistMessageLifecycleReceipt(
	ctx context.Context,
	tx *communicationTx,
	receipt CommunicationCommandReceipt,
) error {
	record, err := communicationCommandReceiptToRecord(receipt)
	if err != nil {
		return err
	}
	_, err = tx.createWithID(ctx, communicationCommandKind, receipt.ID, record)
	return err
}

func messageLifecycleAuditActor(actor CommunicationActorRef) (string, string) {
	kind := string(actor.Kind)
	if actor.Kind == ActorSession {
		kind = string(model.ActorAgent)
	}
	return string(actor.Kind) + ":" + actor.Ref, kind
}

func (s *messageLifecycleService) applyLifecycle(
	ctx context.Context,
	normalized messageLifecycleNormalized,
	prepared messageLifecyclePrepared,
	ids messageLifecycleIDs,
) (messageLifecycleResult, error) {
	var result messageLifecycleResult
	err := s.module.mutateMessageLifecycle(
		ctx, normalized.scope, normalized.authority,
		func(tx *communicationTx, sc store.Scope) error {
			if err := tx.lockAuthoritySnapshot(ctx, normalized.authority.Facts); err != nil {
				return err
			}
			if err := tx.lockTransaction(ctx, messageLifecycleLockKey(
				normalized.scope, normalized.commandScope, normalized.actorFingerprint,
				normalized.idempotencyKeyHash,
			)); err != nil {
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
				replayed, err := messageLifecycleResultFromReceipt(
					receipt, normalized.command.MessageID,
				)
				if err != nil {
					return err
				}
				result = replayed
				return nil
			}
			locked, err := lockMessageLifecycleCarrier(
				ctx, tx, sc, normalized.command.MessageID,
			)
			if err != nil {
				return err
			}
			if locked.message.Version != normalized.command.ExpectedVersion {
				return fmt.Errorf("%w: Message lifecycle version changed", store.ErrConflict)
			}
			if err := verifyMessageLifecyclePrepared(locked, prepared, normalized); err != nil {
				return err
			}
			if err := tx.lockAuditAppends(ctx); err != nil {
				return err
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			dbNow := tx.now.Time()
			carrierSet, err := messageLifecycleCarrierWitness(locked, dbNow)
			if err != nil {
				return err
			}
			input := MessageTransitionInput{
				Before: locked.message, Transition: MessageTransition(normalized.action),
				Deliveries: locked.deliveries, CarrierSet: carrierSet,
				TerminalCode:   normalized.command.TerminalCode,
				TerminalReason: cloneProtectedPayload(prepared.messageReason), DBNow: dbNow,
			}
			if locked.decision != nil {
				input.Decision = &MessageDecisionCascadeInput{
					Request: *locked.decision,
					ResponseEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
						ID: ids.DecisionResponse, TenantID: normalized.scope.TenantID,
						WorkspaceID: normalized.scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
					}},
					Actor:    normalized.authority.Actor,
					Response: cloneProtectedPayload(prepared.decisionReason),
				}
			}
			if locked.handoff != nil {
				input.Handoff = &MessageHandoffCascadeInput{
					Handoff:      *locked.handoff,
					TerminalCode: "message_" + string(normalized.action),
					Reason:       cloneProtectedPayload(prepared.handoffReason),
				}
			}
			plan, err := PlanMessageTransition(input)
			if err != nil {
				return err
			}
			planBytes, err := canonicalJSON(struct {
				SchemaVersion int64                        `json:"schema_version"`
				RequestDigest []byte                       `json:"request_digest"`
				Facts         []store.AuthorizationFactRef `json:"facts"`
				Plan          MessageTransitionPlan        `json:"plan"`
			}{1, normalized.requestDigest, normalized.authority.Facts, plan})
			if err != nil {
				return err
			}
			planHash := sha256.Sum256(planBytes)
			auditActor, auditKind := messageLifecycleAuditActor(normalized.authority.Actor)
			auditAction := messageLifecycleRetractAudit
			eventType := messageLifecycleRetractedEvent
			if normalized.action == messageLifecycleExpire {
				auditAction = messageLifecycleExpireAudit
				eventType = messageLifecycleExpiredEvent
			}
			audit, err := tx.appendAudit(ctx, model.AuditDraft{
				Actor: auditActor, ActorKind: auditKind, Action: auditAction,
				TargetKind: messageKind, TargetID: plan.After.ID, PayloadHash: planHash[:],
				Meta: map[string]any{
					"workspace_id":     normalized.scope.WorkspaceID.String(),
					"command_scope":    normalized.commandScope,
					"state":            string(plan.After.State),
					"delivery_changes": int64(len(plan.DeliveryPlans)),
				},
			})
			if err != nil {
				return err
			}
			if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "Message lifecycle audit anchor is unavailable",
				)
			}
			if err := persistMessageLifecyclePlan(ctx, tx, locked, plan); err != nil {
				return err
			}
			if err := persistMessageLifecycleEvent(
				ctx, tx, locked, plan.After, normalized.action, eventType,
				normalized.authority.Actor, ids, audit, planHash[:],
				int64(len(plan.DeliveryPlans)), nil,
			); err != nil {
				return err
			}
			result = messageLifecycleResult{
				CommandID: ids.Command, MessageID: plan.After.ID, EventID: ids.Event,
				Version: plan.After.Version, State: plan.After.State,
				DeliveryChanges: int64(len(plan.DeliveryPlans)), AuditSeq: audit.Seq,
			}
			if locked.decision != nil {
				result.DecisionRequestID = locked.decision.ID
			}
			if locked.handoff != nil {
				result.HandoffID = locked.handoff.ID
			}
			receipt, err = buildMessageLifecycleReceipt(
				normalized.scope, normalized.actorFingerprint, normalized.idempotencyKeyHash,
				normalized.requestDigest, normalized.commandScope, ids, planHash[:], audit,
				result, dbNow,
			)
			if err != nil {
				return err
			}
			return persistMessageLifecycleReceipt(ctx, tx, receipt)
		},
	)
	return result, err
}

func (s *messageLifecycleService) applyOverdue(
	ctx context.Context,
	normalized messageOverdueNormalized,
	ids messageLifecycleIDs,
) (messageOverdueResult, error) {
	var result messageOverdueResult
	err := s.module.mutateMessageLifecycle(
		ctx, normalized.scope, normalized.authority,
		func(tx *communicationTx, sc store.Scope) error {
			if err := tx.lockAuthoritySnapshot(ctx, normalized.authority.Facts); err != nil {
				return err
			}
			if err := tx.lockTransaction(ctx, messageLifecycleLockKey(
				normalized.scope, normalized.commandScope, normalized.actorFingerprint,
				normalized.idempotencyKeyHash,
			)); err != nil {
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
				replayed, err := messageOverdueResultFromReceipt(
					receipt, normalized.command.MessageID,
				)
				if err != nil {
					return err
				}
				result = replayed
				return nil
			}
			locked, err := lockMessageLifecycleCarrier(
				ctx, tx, sc, normalized.command.MessageID,
			)
			if err != nil {
				return err
			}
			if locked.message.Version != normalized.command.ExpectedVersion {
				return fmt.Errorf("%w: Message overdue version changed", store.ErrConflict)
			}
			if locked.message.State != MessagePublished {
				return communicationError(
					ErrCommunicationTerminal, "only a published Message can become overdue",
				)
			}
			if err := tx.lockAuditAppends(ctx); err != nil {
				return err
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			dbNow := tx.now.Time()
			if locked.message.ExpiresAt != nil && !dbNow.Before(*locked.message.ExpiresAt) {
				return communicationError(
					ErrInvalidCommunicationTransition,
					"Message expiry must be materialized before overdue evaluation",
				)
			}
			afterDeliveries := append([]MessageDelivery(nil), locked.deliveries...)
			type expiryEffect struct {
				Before MessageDelivery
				After  MessageDelivery
				Code   string
			}
			effects := make([]expiryEffect, 0, len(afterDeliveries))
			for index, delivery := range afterDeliveries {
				if !delivery.Required || delivery.State != DeliveryAvailable ||
					delivery.AckDueAt == nil || dbNow.Before(*delivery.AckDueAt) {
					continue
				}
				expiry, err := PlanMessageDeliveryExpiry(delivery, dbNow)
				if err != nil {
					return err
				}
				if expiry.Code != "ack_deadline_elapsed" {
					return communicationError(
						ErrCommunicationEvidenceUnknown, "overdue Delivery lost its Ack deadline cause",
					)
				}
				afterDeliveries[index] = expiry.After
				effects = append(effects, expiryEffect{
					Before: expiry.Before, After: expiry.After, Code: expiry.Code,
				})
			}
			if len(effects) == 0 {
				return communicationError(
					ErrCommunicationTerminal, "Message has no unmaterialized overdue Deliveries",
				)
			}
			messageAfter := locked.message
			if messageAfter.WorkItemID == "" {
				messageAfter.Version++
				messageAfter.UpdatedAt = dbNow
				messageAfter.LastEventSeq++
				if err := ValidateMessage(messageAfter, locked.requiredCount); err != nil {
					return err
				}
			}
			digest, err := CanonicalFulfillmentDeliverySetDigest(afterDeliveries)
			if err != nil {
				return err
			}
			fulfillment, err := ProjectMessageFulfillment(
				messageAfter, afterDeliveries, FulfillmentDeliverySetWitness{
					Scope: normalized.scope, MessageID: messageAfter.ID,
					DeliveryCount: int64(len(afterDeliveries)), RequiredCount: locked.requiredCount,
					Digest:     digest,
					ObservedAt: dbNow, Evidence: AuthorityEvidence{
						Verdict: VerdictClean, Code: "overdue_deliveries_locked",
						EvidenceRef: "message-overdue:" + messageAfter.ID.String(),
					},
					EvidenceRef: "message-overdue:" + messageAfter.ID.String(),
				},
				dbNow,
			)
			if err != nil {
				return err
			}
			planBytes, err := canonicalJSON(struct {
				SchemaVersion int64                        `json:"schema_version"`
				RequestDigest []byte                       `json:"request_digest"`
				Facts         []store.AuthorizationFactRef `json:"facts"`
				MessageBefore Message                      `json:"message_before"`
				MessageAfter  Message                      `json:"message_after"`
				Effects       []expiryEffect               `json:"effects"`
				Fulfillment   FulfillmentProjection        `json:"fulfillment"`
			}{
				1, normalized.requestDigest, normalized.authority.Facts,
				locked.message, messageAfter, effects, fulfillment,
			})
			if err != nil {
				return err
			}
			planHash := sha256.Sum256(planBytes)
			auditActor, auditKind := messageLifecycleAuditActor(normalized.authority.Actor)
			audit, err := tx.appendAudit(ctx, model.AuditDraft{
				Actor: auditActor, ActorKind: auditKind, Action: messageLifecycleOverdueAudit,
				TargetKind: messageKind, TargetID: messageAfter.ID, PayloadHash: planHash[:],
				Meta: map[string]any{
					"workspace_id":       normalized.scope.WorkspaceID.String(),
					"command_scope":      normalized.commandScope,
					"expired_deliveries": int64(len(effects)),
					"fulfillment":        string(fulfillment.State),
				},
			})
			if err != nil {
				return err
			}
			if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "Message overdue audit anchor is unavailable",
				)
			}
			for _, effect := range effects {
				record, err := messageDeliveryToRecord(effect.After)
				if err != nil {
					return err
				}
				record[model.ColVersion] = effect.Before.Version
				if _, err = tx.update(ctx, messageDeliveryKind, record); err != nil {
					return err
				}
			}
			if messageAfter.WorkItemID == "" {
				record, err := messageToRecord(messageAfter, locked.requiredCount)
				if err != nil {
					return err
				}
				record[model.ColVersion] = locked.message.Version
				if _, err = tx.update(ctx, messageKind, record); err != nil {
					return err
				}
			}
			if err := persistMessageLifecycleEvent(
				ctx, tx, locked, messageAfter, messageLifecycleOverdue,
				messageLifecycleOverdueEvent, normalized.authority.Actor, ids, audit,
				planHash[:], int64(len(effects)), &fulfillment,
			); err != nil {
				return err
			}
			result = messageOverdueResult{
				CommandID: ids.Command, MessageID: messageAfter.ID, EventID: ids.Event,
				Version: messageAfter.Version, ExpiredCount: int64(len(effects)),
				Fulfillment: fulfillment, AuditSeq: audit.Seq,
			}
			receipt, err = buildMessageOverdueReceipt(
				normalized, ids, planHash[:], audit, result, dbNow,
			)
			if err != nil {
				return err
			}
			return persistMessageLifecycleReceipt(ctx, tx, receipt)
		},
	)
	return result, err
}
