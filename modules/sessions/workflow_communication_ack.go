// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const workflowAckObserveScope = "workflow.communication.ack.observe"

type workflowAckTarget struct {
	workItemID model.ID
	channelID  model.ID
	messageID  model.ID
	recipient  RecipientRef
	kind       MessageKind
	version    int64
	state      string
	deliveryID model.ID
	ackID      model.ID
	handoffID  model.ID
}

// ObserveWorkflowAck returns the durable current status and, when it is newer
// than AfterEventSeq, the exact WorkEvent cursor that proves the observation.
// EventSeq zero means there is no newer durable transition; it never means a
// synthetic acknowledgement.
func (m *Module) ObserveWorkflowAck(
	ctx context.Context,
	tenant model.TenantID,
	query WorkflowAckQuery,
) (WorkflowAckObservation, error) {
	ctx, cancel := workflowCommunicationContext(ctx)
	defer cancel()
	if query.AfterEventSeq < 0 || !validCanonicalCommunicationID(query.TargetID) ||
		(query.TargetKind != WorkflowAckTargetMessage && query.TargetKind != WorkflowAckTargetHandoff) {
		return WorkflowAckObservation{}, workflowCommunicationError(
			"observe acknowledgement",
			communicationError(ErrInvalidCommunicationModel, "workflow Ack query is invalid"),
		)
	}
	target, err := m.readWorkflowAckTarget(ctx, tenant, query)
	if err != nil {
		return WorkflowAckObservation{}, workflowCommunicationError("read acknowledgement target", err)
	}
	content := MessageContent{
		Subject: "Observe workflow acknowledgement",
		Blocks:  []MessageContentBlock{{Type: ContentBlockStatus, Code: "observe_ack"}},
	}
	preflight, err := m.prepareWorkflowCommunicationPublish(
		ctx, tenant, query.Actor, target.workItemID, target.channelID,
		target.recipient, content, UrgencyNormal, nil, query.TargetID.String(),
		target.kind, workflowAckObserveScope,
	)
	if err != nil {
		return WorkflowAckObservation{}, workflowCommunicationError("authorize acknowledgement observation", err)
	}
	observation, err := m.projectWorkflowAck(
		ctx, tenant, query, target, preflight.direct.ActorFingerprint,
	)
	if err != nil {
		return WorkflowAckObservation{}, workflowCommunicationError("project acknowledgement", err)
	}
	if observation.EventSeq <= query.AfterEventSeq {
		observation.EventID = ""
		observation.EventSeq = 0
	}
	return observation, nil
}

func (m *Module) readWorkflowAckTarget(
	ctx context.Context,
	tenant model.TenantID,
	query WorkflowAckQuery,
) (workflowAckTarget, error) {
	var target workflowAckTarget
	err := m.communicationData(tenant).View(ctx, func(sc store.Scope) error {
		var err error
		switch query.TargetKind {
		case WorkflowAckTargetMessage:
			target, err = workflowAckMessageTarget(ctx, sc, query.TargetID)
		case WorkflowAckTargetHandoff:
			target, err = workflowAckHandoffTarget(ctx, sc, query.TargetID)
		}
		return err
	})
	return target, err
}

func workflowAckMessageTarget(
	ctx context.Context,
	sc store.Scope,
	messageID model.ID,
) (workflowAckTarget, error) {
	messages, err := sc.Ext(messageKind)
	if err != nil {
		return workflowAckTarget{}, err
	}
	deliveries, err := sc.Ext(messageDeliveryKind)
	if err != nil {
		return workflowAckTarget{}, err
	}
	rows, page, err := deliveries.List(ctx, model.Query{
		Filters: []model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()}},
		Limit:   2,
	})
	if err != nil || page.HasMore || len(rows) != 1 {
		return workflowAckTarget{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Message Delivery is unavailable",
		)
	}
	delivery, err := messageDeliveryFromRecord(rows[0])
	if err != nil {
		return workflowAckTarget{}, err
	}
	required := int64(0)
	if delivery.Required {
		required = 1
	}
	record, err := messages.Get(ctx, messageID)
	if err != nil {
		return workflowAckTarget{}, err
	}
	message, err := messageFromRecord(record, required)
	if err != nil || message.ID != messageID || message.WorkItemID.IsZero() ||
		(message.Kind != MessageWorkTask && message.Kind != MessageHandoffOffer) ||
		delivery.MessageID != messageID {
		return workflowAckTarget{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Message Ack lineage is unavailable",
		)
	}
	return workflowAckTarget{
		workItemID: message.WorkItemID, channelID: message.ChannelID, messageID: message.ID,
		recipient: delivery.Recipient, kind: message.Kind, version: message.Version,
		state: string(delivery.State), deliveryID: delivery.ID, ackID: delivery.AckID,
	}, nil
}

func workflowAckHandoffTarget(
	ctx context.Context,
	sc store.Scope,
	handoffID model.ID,
) (workflowAckTarget, error) {
	handoffs, err := sc.Ext(handoffKind)
	if err != nil {
		return workflowAckTarget{}, err
	}
	record, err := handoffs.Get(ctx, handoffID)
	if err != nil {
		return workflowAckTarget{}, err
	}
	handoff, err := handoffFromRecord(record)
	if err != nil || handoff.ID != handoffID {
		return workflowAckTarget{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Handoff Ack target is malformed",
		)
	}
	messages, err := sc.Ext(messageKind)
	if err != nil {
		return workflowAckTarget{}, err
	}
	deliveries, err := sc.Ext(messageDeliveryKind)
	if err != nil {
		return workflowAckTarget{}, err
	}
	messageRecord, err := messages.Get(ctx, handoff.MessageID)
	if err != nil {
		return workflowAckTarget{}, err
	}
	deliveryRecord, err := deliveries.Get(ctx, handoff.DeliveryID)
	if err != nil {
		return workflowAckTarget{}, err
	}
	delivery, err := messageDeliveryFromRecord(deliveryRecord)
	if err != nil {
		return workflowAckTarget{}, err
	}
	message, err := messageFromRecord(messageRecord, 1)
	if err != nil || ValidateHandoffLineage(message, delivery, handoff, 1) != nil {
		return workflowAckTarget{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Handoff Ack lineage is unavailable",
		)
	}
	return workflowAckTarget{
		workItemID: handoff.WorkItemID, channelID: message.ChannelID,
		messageID: handoff.MessageID, recipient: handoff.To, kind: MessageHandoffOffer,
		version: handoff.Version, state: string(handoff.State),
		deliveryID: handoff.DeliveryID, ackID: handoff.AckID, handoffID: handoff.ID,
	}, nil
}

func (m *Module) projectWorkflowAck(
	ctx context.Context,
	tenant model.TenantID,
	query WorkflowAckQuery,
	observed workflowAckTarget,
	actorFingerprint []byte,
) (WorkflowAckObservation, error) {
	var result WorkflowAckObservation
	err := m.communicationData(tenant).View(ctx, func(sc store.Scope) error {
		current, err := workflowAckMessageTarget(ctx, sc, observed.messageID)
		if err != nil {
			return err
		}
		if current.workItemID != observed.workItemID || current.channelID != observed.channelID ||
			current.recipient != observed.recipient || current.deliveryID != observed.deliveryID ||
			current.kind != observed.kind {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Ack target changed lineage",
			)
		}
		if query.TargetKind == WorkflowAckTargetHandoff {
			current, err = workflowAckHandoffTarget(ctx, sc, query.TargetID)
			if err != nil {
				return err
			}
		}
		if err := verifyWorkflowCommunicationOrigin(
			ctx, sc, current, actorFingerprint,
		); err != nil {
			return err
		}
		status, detail, err := workflowAckStatusFor(current, query.TargetKind)
		if err != nil {
			return err
		}
		receipt, err := workflowAckStateReceipt(ctx, sc, current, status)
		if err != nil {
			return err
		}
		event, err := workflowAckReceiptEvent(ctx, sc, current, receipt)
		if err != nil {
			return err
		}
		result = WorkflowAckObservation{
			Status: status, AckID: current.ackID, EventID: receipt.EventID,
			EventSeq: event.Int(colEventSeq), Detail: detail,
		}
		return nil
	})
	return result, err
}

func verifyWorkflowCommunicationOrigin(
	ctx context.Context,
	sc store.Scope,
	target workflowAckTarget,
	actorFingerprint []byte,
) error {
	commandScope := workflowWorkTaskCommandScope
	if target.kind == MessageHandoffOffer {
		commandScope = workflowHandoffCarrierScope
	}
	repo, err := sc.Ext(communicationCommandKind)
	if err != nil {
		return err
	}
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colCommCommandScope, Op: model.OpEq, Value: commandScope},
			{Column: colCommResultKind, Op: model.OpEq, Value: string(messageKind)},
			{Column: colCommResultID, Op: model.OpEq, Value: target.messageID.String()},
		}, Limit: 2,
	})
	if err != nil || page.HasMore || len(rows) != 1 {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Message origin receipt is unavailable",
		)
	}
	receipt, err := communicationCommandReceiptFromRecord(rows[0])
	if err != nil || ValidateCommunicationCommandReceipt(receipt) != nil ||
		!bytes.Equal(receipt.ActorFingerprint, actorFingerprint) ||
		receipt.ResponseProjectionJSON.IDs["work_item_id"] != target.workItemID ||
		receipt.ResponseProjectionJSON.IDs["delivery_id"] != target.deliveryID {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Message origin is not actor-bound",
		)
	}
	return nil
}

func workflowAckStatusFor(
	target workflowAckTarget,
	kind WorkflowAckTargetKind,
) (WorkflowAckStatus, string, error) {
	if kind == WorkflowAckTargetHandoff {
		switch HandoffState(target.state) {
		case HandoffOffered:
			return WorkflowAckPending, "handoff_offered", nil
		case HandoffAccepted:
			return WorkflowAckAcknowledged, "handoff_accepted", nil
		case HandoffRejected:
			return WorkflowAckRejected, "handoff_rejected", nil
		case HandoffWithdrawn:
			return WorkflowAckRejected, "handoff_withdrawn", nil
		case HandoffExpired:
			return WorkflowAckExpired, "handoff_expired", nil
		}
	} else {
		switch MessageDeliveryState(target.state) {
		case DeliveryAvailable:
			return WorkflowAckPending, "message_available", nil
		case DeliveryAcknowledged:
			return WorkflowAckAcknowledged, "message_acknowledged", nil
		case DeliveryExpired:
			return WorkflowAckExpired, "message_expired", nil
		case DeliveryRetracted:
			return WorkflowAckRejected, "message_retracted", nil
		case DeliveryUndeliverable:
			return WorkflowAckRejected, "message_undeliverable", nil
		}
	}
	return WorkflowAckUnknown, "", communicationError(
		ErrCommunicationEvidenceUnknown, "workflow Ack state is unknown",
	)
}

func workflowAckStateReceipt(
	ctx context.Context,
	sc store.Scope,
	target workflowAckTarget,
	status WorkflowAckStatus,
) (CommunicationCommandReceipt, error) {
	repo, err := sc.Ext(communicationCommandKind)
	if err != nil {
		return CommunicationCommandReceipt{}, err
	}
	resultKind, resultID := string(messageKind), target.messageID
	if target.handoffID != "" {
		resultKind, resultID = string(handoffKind), target.handoffID
	}
	if status == WorkflowAckAcknowledged && target.ackID != "" && target.handoffID == "" {
		resultKind, resultID = string(messageAckKind), target.ackID
	}
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colCommResultKind, Op: model.OpEq, Value: resultKind},
			{Column: colCommResultID, Op: model.OpEq, Value: resultID.String()},
		}, Limit: 16,
	})
	if err != nil || page.HasMore {
		return CommunicationCommandReceipt{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Ack receipt set is unavailable",
		)
	}
	var selected CommunicationCommandReceipt
	for _, row := range rows {
		receipt, decodeErr := communicationCommandReceiptFromRecord(row)
		if decodeErr != nil || ValidateCommunicationCommandReceipt(receipt) != nil {
			return CommunicationCommandReceipt{}, communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Ack receipt is malformed",
			)
		}
		projection := receipt.ResponseProjectionJSON
		if projection.IDs["message_id"] != target.messageID ||
			projection.IDs["delivery_id"] != target.deliveryID {
			continue
		}
		if target.handoffID != "" &&
			(projection.Version != target.version || projection.State != target.state) {
			continue
		}
		if selected.ID != "" {
			return CommunicationCommandReceipt{}, communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Ack current receipt is ambiguous",
			)
		}
		selected = receipt
	}
	if selected.ID == "" {
		return CommunicationCommandReceipt{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Ack current receipt is unavailable",
		)
	}
	return selected, nil
}

func workflowAckReceiptEvent(
	ctx context.Context,
	sc store.Scope,
	target workflowAckTarget,
	receipt CommunicationCommandReceipt,
) (model.Record, error) {
	repo, err := sc.Ext(workEventKind)
	if err != nil {
		return nil, err
	}
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{{Column: colEventID, Op: model.OpEq, Value: receipt.EventID.String()}},
		Limit:   2,
	})
	if err != nil || page.HasMore || len(rows) != 1 ||
		rows[0].String(colEventAggregateKind) != string(workItemKind) ||
		rows[0].String(colEventAggregateID) != target.workItemID.String() ||
		rows[0].Int(colEventSeq) < 1 || rows[0].Int(colEventAuditSeq) != receipt.AuditSeq ||
		len(rows[0].Bytes(colEventAuditHash)) != sha256.Size ||
		!bytes.Equal(rows[0].Bytes(colEventAuditHash), receipt.AuditHash) {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Ack WorkEvent is unavailable",
		)
	}
	return rows[0], nil
}
