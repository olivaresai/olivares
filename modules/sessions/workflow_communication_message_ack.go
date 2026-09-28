// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const workflowMessageAckCommandScope = "workflow.message.delivery.ack"

// WorkflowMessageAckCommand is the private durable-work Ack seam used by
// workflow and protocol adapters. Actor must be the exact User recipient; the
// WorkItem and Channel tuple prevents a same-workspace delivery substitution.
type WorkflowMessageAckCommand struct {
	Actor           WorkflowCommunicationActor
	WorkItemID      model.ID
	ChannelID       model.ID
	DeliveryID      model.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

type WorkflowMessageAckResult struct {
	WorkItemID model.ID
	CommandID  model.ID
	AckID      model.ID
	MessageID  model.ID
	DeliveryID model.ID
	EventID    model.ID
	Version    int64
	State      MessageDeliveryState
	Replayed   bool
}

// AcknowledgeWorkflowMessage records the exact recipient Ack through the same
// K3 transaction used by the public delivery endpoint. It bypasses only opaque
// HTTP credential reconstruction: current C5 operation, directory, grant,
// freshness, row-lock, audit, event, outbox and receipt evidence remain
// mandatory.
func (m *Module) AcknowledgeWorkflowMessage(
	ctx context.Context,
	tenant model.TenantID,
	cmd WorkflowMessageAckCommand,
) (WorkflowMessageAckResult, error) {
	ctx, cancel := workflowCommunicationContext(ctx)
	defer cancel()
	if !validCanonicalCommunicationID(cmd.WorkItemID) ||
		!validCanonicalCommunicationID(cmd.ChannelID) ||
		!validCanonicalCommunicationID(cmd.DeliveryID) || cmd.ExpectedVersion < 1 {
		return WorkflowMessageAckResult{}, workflowCommunicationError(
			"acknowledge message",
			communicationError(ErrInvalidCommunicationModel, "workflow Message Ack target is invalid"),
		)
	}
	target, err := m.workflowCommunicationScope(ctx, tenant, cmd.WorkItemID, cmd.ChannelID)
	if err != nil {
		return WorkflowMessageAckResult{}, workflowCommunicationError("resolve acknowledgement scope", err)
	}
	principal, err := workflowCommunicationUserPrincipal(cmd.Actor, target.ref)
	if err != nil {
		return WorkflowMessageAckResult{}, workflowCommunicationError("resolve acknowledgement actor", err)
	}
	messageID, err := m.validateWorkflowMessageAckLineage(ctx, target.ref, principal, cmd)
	if err != nil {
		return WorkflowMessageAckResult{}, workflowCommunicationError("validate acknowledgement lineage", err)
	}
	idempotencyID, err := workflowCommunicationStableID(cmd.IdempotencyKey, workflowMessageAckCommandScope)
	if err != nil {
		return WorkflowMessageAckResult{}, workflowCommunicationError("normalize acknowledgement", err)
	}
	binder := func(
		bindCtx context.Context,
		question communicationAuthorityQuestion,
	) (communicationRequestAuthority, error) {
		return m.bindWorkflowCommunicationOperationAuthority(bindCtx, cmd.Actor, principal, question)
	}
	result, err := m.acknowledgeDirectNoticeDeliveryWithAuthorityBinder(
		ctx, target.ref, cmd.DeliveryID,
		DirectNoticeDeliveryAckCommand{
			IfMatch:        fmt.Sprintf("\"v%d\"", cmd.ExpectedVersion),
			IdempotencyKey: idempotencyID.String(),
		},
		false,
		directNoticeAckCarrierSelector{
			class:      directNoticeAckCarrierWorkflowWorkTask,
			workItemID: cmd.WorkItemID, channelID: cmd.ChannelID,
			userID: principal.UserID,
		},
		binder,
	)
	if err != nil {
		return WorkflowMessageAckResult{}, workflowCommunicationError("acknowledge message", err)
	}
	if result.MessageID != messageID || result.DeliveryID != cmd.DeliveryID ||
		result.CommandID.IsZero() || result.AckID.IsZero() || result.EventID.IsZero() ||
		result.Version < 2 || result.State != DeliveryAcknowledged {
		return WorkflowMessageAckResult{}, workflowCommunicationError(
			"acknowledge message",
			communicationError(ErrCommunicationEvidenceUnknown, "workflow Message Ack result is incomplete"),
		)
	}
	return WorkflowMessageAckResult{
		WorkItemID: cmd.WorkItemID, CommandID: result.CommandID, AckID: result.AckID,
		MessageID: result.MessageID, DeliveryID: result.DeliveryID, EventID: result.EventID,
		Version: result.Version, State: result.State, Replayed: result.Replayed,
	}, nil
}

func (m *Module) validateWorkflowMessageAckLineage(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	cmd WorkflowMessageAckCommand,
) (model.ID, error) {
	var messageID model.ID
	err := m.communicationData(scope.TenantID).View(ctx, func(sc store.Scope) error {
		deliveries, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		deliveryRecord, err := deliveries.Get(ctx, cmd.DeliveryID)
		if err != nil {
			return err
		}
		delivery, err := messageDeliveryFromRecord(deliveryRecord)
		if err != nil || delivery.ID != cmd.DeliveryID || delivery.WorkspaceID != scope.WorkspaceID ||
			delivery.Recipient != (RecipientRef{Kind: RecipientUser, Ref: principal.UserID.String()}) {
			return communicationError(
				ErrInvalidCommunicationTransition, "workflow Message Ack recipient or workspace changed",
			)
		}
		messages, err := sc.Ext(messageKind)
		if err != nil {
			return err
		}
		messageRecord, err := messages.Get(ctx, delivery.MessageID)
		if err != nil {
			return err
		}
		message, err := messageFromRecord(messageRecord, 0)
		if err != nil || message.ID != delivery.MessageID || message.WorkItemID != cmd.WorkItemID ||
			message.ChannelID != cmd.ChannelID || message.Kind != MessageWorkTask {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Message Ack lineage is unavailable",
			)
		}
		messageID = message.ID
		return nil
	})
	return messageID, err
}

// bindWorkflowCommunicationOperationAuthority binds the Ack authority from the
// actor's run credential binding through the exact pair; the returned authority
// is consumed by the Ack's own transaction. The identity-only operation port is
// not consulted, so an actor without a binding (the protocol paths) is refused.
func (m *Module) bindWorkflowCommunicationOperationAuthority(
	ctx context.Context,
	actor WorkflowCommunicationActor,
	principal CommunicationPrincipal,
	question communicationAuthorityQuestion,
) (communicationRequestAuthority, error) {
	if ctx == nil || question.validate() != nil {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication operation authority is unavailable",
		)
	}
	if deadline, ok := ctx.Deadline(); !ok || deadline.IsZero() {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication authority requires a finite deadline",
		)
	}
	if err := ValidateCommunicationPrincipalForScope(principal, DirectoryScopeRef{
		TenantID: question.entity.TenantID, WorkspaceID: question.entity.WorkspaceID,
	}); err != nil {
		return communicationRequestAuthority{}, err
	}
	return m.bindWorkflowCredentialAuthority(ctx, actor, principal, question)
}
