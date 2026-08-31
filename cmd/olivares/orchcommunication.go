// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/sessions"
)

const (
	workflowMessageSubject     = "Workflow work task"
	workflowMessageBodyRefKind = "workflow_body"
	workflowHandoffContextKind = "workflow_context"
	workflowHandoffNextAction  = "Continue the assigned WorkItem."
)

type workflowCommunicationKernel interface {
	SendWorkflowWorkTask(
		context.Context,
		model.TenantID,
		sessions.WorkflowWorkTaskCommand,
	) (sessions.WorkflowWorkTaskResult, error)
	OfferWorkflowHandoff(
		context.Context,
		model.TenantID,
		sessions.WorkflowHandoffCommand,
	) (sessions.WorkflowHandoffResult, error)
	ObserveWorkflowAck(
		context.Context,
		model.TenantID,
		sessions.WorkflowAckQuery,
	) (sessions.WorkflowAckObservation, error)
}

type workflowCommunicationAdapter struct {
	kernel workflowCommunicationKernel
}

func newWorkflowCommunicationAdapter(module *sessions.Module) *workflowCommunicationAdapter {
	return &workflowCommunicationAdapter{kernel: module}
}

var (
	_ orchestration.WorkflowMessageControl = (*workflowCommunicationAdapter)(nil)
	_ orchestration.WorkflowHandoffControl = (*workflowCommunicationAdapter)(nil)
	_ orchestration.WorkflowAckReader      = (*workflowCommunicationAdapter)(nil)
)

func workflowCommunicationActor(
	actor orchestration.WorkActor,
) (sessions.WorkflowCommunicationActor, error) {
	result := sessions.WorkflowCommunicationActor{
		AuditKind: strings.TrimSpace(actor.Kind), AuditRef: strings.TrimSpace(actor.Ref),
		AgentExternalID: actor.AgentIdentity, SessionID: actor.SessionIdentity,
		SessionRunRef: actor.SessionRunRef, SessionFence: actor.SessionFence,
		PurposeRestricted: actor.PurposeRestricted,
	}
	if !actor.UserIdentity.IsZero() {
		result.AuditKind = model.ActorUser
		result.AuditRef = "user:" + actor.UserIdentity.String()
	}
	if result.AuditKind == "" || result.AuditRef == "" || result.SessionFence < 0 ||
		(actor.UserIdentity.IsZero() && strings.TrimSpace(actor.AgentIdentity) == "" &&
			strings.TrimSpace(actor.SessionIdentity) == "") {
		return sessions.WorkflowCommunicationActor{}, fmt.Errorf(
			"workflow communication actor has no durable attributable identity",
		)
	}
	return result, nil
}

func workflowCommunicationRecipient(
	participant orchestration.WorkParticipant,
) (sessions.RecipientRef, error) {
	if participant.Kind != strings.TrimSpace(participant.Kind) ||
		participant.Ref != strings.TrimSpace(participant.Ref) {
		return sessions.RecipientRef{}, fmt.Errorf("workflow communication recipient is not canonical")
	}
	var kind sessions.RecipientKind
	switch participant.Kind {
	case string(sessions.RecipientUser):
		kind = sessions.RecipientUser
	case string(sessions.RecipientAgent):
		kind = sessions.RecipientAgent
	case string(sessions.RecipientSession):
		kind = sessions.RecipientSession
	default:
		return sessions.RecipientRef{}, fmt.Errorf(
			"workflow communication recipient kind %q is unsupported", participant.Kind,
		)
	}
	recipient := sessions.RecipientRef{Kind: kind, Ref: participant.Ref}
	if err := recipient.Validate(); err != nil {
		return sessions.RecipientRef{}, fmt.Errorf("workflow communication recipient is invalid: %w", err)
	}
	return recipient, nil
}

func workflowMessageContent(
	workItemID model.ID,
	body, bodyRef string,
) (sessions.MessageContent, error) {
	if workItemID.IsZero() || (body == "") == (bodyRef == "") ||
		bodyRef != strings.TrimSpace(bodyRef) {
		return sessions.MessageContent{}, fmt.Errorf(
			"workflow work message requires exactly one canonical body or body reference",
		)
	}
	content := sessions.MessageContent{Subject: workflowMessageSubject}
	if body != "" {
		content.Blocks = []sessions.MessageContentBlock{{
			Type: sessions.ContentBlockText, Format: sessions.TextPlain, Text: body,
		}}
	} else {
		content.Blocks = []sessions.MessageContentBlock{{
			Type: sessions.ContentBlockReference,
			Reference: &sessions.ContentReference{
				Kind: workflowMessageBodyRefKind, Ref: bodyRef,
			},
		}}
	}
	if _, err := sessions.CanonicalMessageContent(content); err != nil {
		return sessions.MessageContent{}, fmt.Errorf("workflow work message content is invalid: %w", err)
	}
	return content, nil
}

func workflowHandoffContent(
	contextText, contextRef string,
) (sessions.HandoffContent, error) {
	if (contextText == "") == (contextRef == "") || contextRef != strings.TrimSpace(contextRef) {
		return sessions.HandoffContent{}, fmt.Errorf(
			"workflow handoff requires exactly one canonical context or context reference",
		)
	}
	content := sessions.HandoffContent{NextAction: workflowHandoffNextAction}
	if contextText != "" {
		content.Summary = contextText
	} else {
		content.Summary = "Handoff context is attached by reference."
		content.ArtifactRefs = []sessions.ContentReference{{
			Kind: workflowHandoffContextKind, Ref: contextRef,
		}}
	}
	if _, err := sessions.CanonicalProtectedPayloadSlot(sessions.PayloadSlotHandoff, content); err != nil {
		return sessions.HandoffContent{}, fmt.Errorf("workflow handoff content is invalid: %w", err)
	}
	return content, nil
}

func workflowOptionalTimestamp(raw, field string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := model.ParseTimestamp(raw)
	if err != nil || parsed.IsZero() || parsed.String() != raw {
		return nil, fmt.Errorf("workflow communication %s is not canonical", field)
	}
	value := parsed.Time()
	return &value, nil
}

func workflowRequiredTimestamp(raw, field string) (time.Time, error) {
	parsed, err := workflowOptionalTimestamp(raw, field)
	if err != nil {
		return time.Time{}, err
	}
	if parsed == nil {
		return time.Time{}, fmt.Errorf("workflow communication %s is required", field)
	}
	return *parsed, nil
}

func (a *workflowCommunicationAdapter) SendWorkMessage(
	ctx context.Context,
	tenant model.TenantID,
	req orchestration.WorkMessageRequest,
) (orchestration.WorkMessageResult, error) {
	actor, err := workflowCommunicationActor(req.Actor)
	if err != nil {
		return orchestration.WorkMessageResult{}, err
	}
	recipient, err := workflowCommunicationRecipient(req.Recipient)
	if err != nil {
		return orchestration.WorkMessageResult{}, err
	}
	content, err := workflowMessageContent(req.WorkItemID, req.Body, req.BodyRef)
	if err != nil {
		return orchestration.WorkMessageResult{}, err
	}
	ackDueAt, err := workflowOptionalTimestamp(req.AckDueAt, "ack_due_at")
	if err != nil {
		return orchestration.WorkMessageResult{}, err
	}
	urgency := sessions.MessageUrgency(req.Urgency)
	if urgency == "" {
		urgency = sessions.UrgencyNormal
	}
	if !urgency.Valid() {
		return orchestration.WorkMessageResult{}, fmt.Errorf(
			"workflow communication urgency %q is invalid", req.Urgency,
		)
	}
	result, err := a.kernel.SendWorkflowWorkTask(ctx, tenant, sessions.WorkflowWorkTaskCommand{
		Actor: actor, WorkItemID: req.WorkItemID, ChannelID: req.ChannelID,
		Recipient: recipient, Content: content, Urgency: urgency,
		AckDueAt: ackDueAt, IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		return orchestration.WorkMessageResult{}, err
	}
	if result.WorkItemID != req.WorkItemID || result.CommandID.IsZero() ||
		result.MessageID.IsZero() || result.DeliveryID.IsZero() || result.EventID.IsZero() ||
		result.EventSeq < 1 || result.Version < 1 || result.State != sessions.MessagePublished {
		return orchestration.WorkMessageResult{}, fmt.Errorf(
			"workflow communication kernel returned incomplete work-message evidence",
		)
	}
	return orchestration.WorkMessageResult{
		WorkItemID: result.WorkItemID, MessageID: result.MessageID,
		CommandID: result.CommandID, EventID: result.EventID, EventSeq: result.EventSeq,
	}, nil
}

func (a *workflowCommunicationAdapter) OfferWorkHandoff(
	ctx context.Context,
	tenant model.TenantID,
	req orchestration.WorkHandoffRequest,
) (orchestration.WorkHandoffResult, error) {
	actor, err := workflowCommunicationActor(req.Actor)
	if err != nil {
		return orchestration.WorkHandoffResult{}, err
	}
	target, err := workflowCommunicationRecipient(req.Target)
	if err != nil {
		return orchestration.WorkHandoffResult{}, err
	}
	content, err := workflowHandoffContent(req.Context, req.ContextRef)
	if err != nil {
		return orchestration.WorkHandoffResult{}, err
	}
	ackDeadline, err := workflowRequiredTimestamp(req.AckDeadline, "ack_deadline")
	if err != nil {
		return orchestration.WorkHandoffResult{}, err
	}
	if req.ExpectedOwnerEpoch < 0 {
		return orchestration.WorkHandoffResult{}, fmt.Errorf(
			"workflow communication expected owner epoch is invalid",
		)
	}
	result, err := a.kernel.OfferWorkflowHandoff(ctx, tenant, sessions.WorkflowHandoffCommand{
		Actor: actor, WorkItemID: req.WorkItemID, ChannelID: req.ChannelID,
		Target: target, Content: content, AckDeadline: ackDeadline,
		ExpectedOwnerEpoch: req.ExpectedOwnerEpoch, IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		return orchestration.WorkHandoffResult{}, err
	}
	if result.WorkItemID != req.WorkItemID || result.CommandID.IsZero() ||
		result.HandoffID.IsZero() || result.MessageID.IsZero() || result.DeliveryID.IsZero() ||
		result.EventID.IsZero() || result.EventSeq < 1 || result.Version < 1 ||
		result.State != sessions.HandoffOffered || result.OwnerEpoch < 1 ||
		(req.ExpectedOwnerEpoch > 0 && result.OwnerEpoch != req.ExpectedOwnerEpoch) {
		return orchestration.WorkHandoffResult{}, fmt.Errorf(
			"workflow communication kernel returned incomplete handoff evidence",
		)
	}
	return orchestration.WorkHandoffResult{
		WorkItemID: result.WorkItemID, HandoffID: result.HandoffID,
		MessageID: result.MessageID, CommandID: result.CommandID,
		EventID: result.EventID, EventSeq: result.EventSeq, OwnerEpoch: result.OwnerEpoch,
	}, nil
}

func (a *workflowCommunicationAdapter) ObserveWorkAck(
	ctx context.Context,
	tenant model.TenantID,
	query orchestration.WorkAckQuery,
) (orchestration.WorkAckObservation, error) {
	actor, err := workflowCommunicationActor(query.Actor)
	if err != nil {
		return orchestration.WorkAckObservation{}, err
	}
	var targetKind sessions.WorkflowAckTargetKind
	switch query.TargetKind {
	case string(sessions.WorkflowAckTargetMessage):
		targetKind = sessions.WorkflowAckTargetMessage
	case string(sessions.WorkflowAckTargetHandoff):
		targetKind = sessions.WorkflowAckTargetHandoff
	default:
		return orchestration.WorkAckObservation{}, fmt.Errorf(
			"workflow acknowledgement target kind %q is invalid", query.TargetKind,
		)
	}
	if query.TargetID.IsZero() || query.AfterEventSeq < 0 {
		return orchestration.WorkAckObservation{}, fmt.Errorf(
			"workflow acknowledgement target or cursor is invalid",
		)
	}
	result, err := a.kernel.ObserveWorkflowAck(ctx, tenant, sessions.WorkflowAckQuery{
		Actor: actor, TargetKind: targetKind, TargetID: query.TargetID,
		AfterEventSeq: query.AfterEventSeq,
	})
	if err != nil {
		return orchestration.WorkAckObservation{}, err
	}
	if result.EventSeq < 0 || (result.EventSeq == 0) != result.EventID.IsZero() {
		return orchestration.WorkAckObservation{}, fmt.Errorf(
			"workflow communication kernel returned inconsistent acknowledgement evidence",
		)
	}
	var status orchestration.WorkAckStatus
	switch result.Status {
	case sessions.WorkflowAckPending:
		status = orchestration.WorkAckPending
	case sessions.WorkflowAckAcknowledged:
		status = orchestration.WorkAckAcknowledged
	case sessions.WorkflowAckRejected:
		status = orchestration.WorkAckRejected
	case sessions.WorkflowAckExpired:
		status = orchestration.WorkAckExpired
	case sessions.WorkflowAckUnknown:
		status = orchestration.WorkAckUnknown
	default:
		return orchestration.WorkAckObservation{}, fmt.Errorf(
			"workflow communication kernel returned invalid acknowledgement status %q",
			result.Status,
		)
	}
	return orchestration.WorkAckObservation{
		Status: status, AckID: result.AckID, EventID: result.EventID,
		EventSeq: result.EventSeq, Detail: result.Detail,
	}, nil
}
