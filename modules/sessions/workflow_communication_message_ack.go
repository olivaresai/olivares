// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"sync/atomic"

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
		return m.bindWorkflowCommunicationOperationAuthority(bindCtx, principal, question)
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

func (m *Module) bindWorkflowCommunicationOperationAuthority(
	ctx context.Context,
	principal CommunicationPrincipal,
	question communicationAuthorityQuestion,
) (communicationRequestAuthority, error) {
	if ctx == nil || question.validate() != nil ||
		!communicationPortBound(m.communicationOperationAuthorizer) {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication operation authority is unavailable",
		)
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.IsZero() {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication authority requires a finite deadline",
		)
	}
	witness, err := m.communicationOperationAuthorizer.AuthorizeEntityOperation(
		ctx, principal, question.entity, question.operation,
	)
	if err != nil || ValidateReadWitness(witness) != nil || witness.Outcome != ReadAllow ||
		witness.Entity != question.entity || witness.Operation != question.operation ||
		witness.Principal != principal {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication operation authority is unavailable",
		)
	}
	// The operation authorizer proves an upper bound for freshness. The private
	// workflow binder may conservatively narrow that proof to its shorter request
	// lifetime, but must never seal authority that survives the request context.
	if witness.FreshUntil.After(deadline) {
		witness.FreshUntil = deadline
	}
	if ValidateReadWitness(witness) != nil {
		return communicationRequestAuthority{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication operation authority is unavailable",
		)
	}
	if err := ValidateCommunicationPrincipalForScope(principal, DirectoryScopeRef{
		TenantID: question.entity.TenantID, WorkspaceID: question.entity.WorkspaceID,
	}); err != nil {
		return communicationRequestAuthority{}, err
	}

	consumed := &atomic.Bool{}
	sealedQuestion := question
	sealedBindingID := &communicationRequestAuthorityBindingID{marker: 1}
	sealedSnapshot := communicationRequestAuthoritySnapshot{
		facts:      append([]store.AuthorizationFactRef(nil), witness.Facts...),
		observedAt: witness.ObservedAt, freshUntil: witness.FreshUntil,
		bindingID: sealedBindingID,
	}
	sealedContext := communicationRequestAuthorityContext{
		question: question, principal: principal, bindingID: sealedBindingID,
	}
	sealedWitness := cloneCommunicationRequestAuthorityWitness(witness)
	return communicationRequestAuthority{access: func(
		access communicationRequestAuthorityAccess,
		expected communicationAuthorityQuestion,
		claims CommunicationClaimAuthoritySnapshot,
	) (communicationRequestAuthorityAccessResult, error) {
		if expected.validate() != nil || expected != sealedQuestion {
			return communicationRequestAuthorityAccessResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "workflow communication authority binding is malformed",
			)
		}
		switch access {
		case communicationRequestAuthorityInspect:
			return communicationRequestAuthorityAccessResult{inspection: communicationRequestAuthorityInspection{
				question: sealedContext.question, principal: sealedContext.principal,
				bindingID: sealedContext.bindingID,
			}}, nil
		case communicationRequestAuthorityConsume:
			if err := requireCommunicationSessionClaim(sealedContext.principal, claims); err != nil {
				return communicationRequestAuthorityAccessResult{}, err
			}
		default:
			return communicationRequestAuthorityAccessResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "workflow communication authority access is malformed",
			)
		}
		if !consumed.CompareAndSwap(false, true) {
			return communicationRequestAuthorityAccessResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "workflow communication authority was already consumed",
			)
		}
		return communicationRequestAuthorityAccessResult{
			snapshot: communicationRequestAuthoritySnapshot{
				facts:      append([]store.AuthorizationFactRef(nil), sealedSnapshot.facts...),
				observedAt: sealedSnapshot.observedAt, freshUntil: sealedSnapshot.freshUntil,
				bindingID: sealedSnapshot.bindingID,
			},
			context: communicationRequestAuthorityContext{
				question: sealedContext.question, principal: sealedContext.principal,
				bindingID: sealedContext.bindingID,
				witness:   cloneCommunicationRequestAuthorityWitness(sealedWitness),
			},
		}, nil
	}}, nil
}
