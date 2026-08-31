// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	workflowWorkTaskCommandScope = "workflow.message.work-task"
	workflowHandoffCarrierScope  = "workflow.message.handoff-carrier"
	workflowWorkTaskAuditAction  = "sessions.communication.workflow.work-task.send"
	workflowWorkTaskEventType    = "work.message.available"
	workflowHandoffCarrierAudit  = "sessions.communication.workflow.handoff-carrier.send"
	workflowHandoffCarrierEvent  = "work.handoff.carrier.available"
	workflowCommunicationTimeout = 30 * time.Second
)

// MessageWorkTask is the K3 actionable request vocabulary used by a workflow.
// It is an intentional semantic alias: the durable Message kind remains the
// already validated request kind rather than introducing a second wire value.
const MessageWorkTask MessageKind = MessageRequest

// WorkflowCommunicationActor is reconstructed only from the immutable
// workflow-run initiator snapshot. AuditKind/AuditRef retain attribution while
// the remaining fields retain server-authored NHI/session dimensions. A
// workflow config has no field from which this value can be constructed.
type WorkflowCommunicationActor struct {
	AuditKind         string
	AuditRef          string
	AgentExternalID   string
	SessionID         string
	SessionRunRef     string
	SessionFence      int64
	PurposeRestricted bool
}

type WorkflowWorkTaskCommand struct {
	Actor          WorkflowCommunicationActor
	WorkItemID     model.ID
	ChannelID      model.ID
	Recipient      RecipientRef
	Content        MessageContent
	Urgency        MessageUrgency
	AckDueAt       *time.Time
	IdempotencyKey string
}

type WorkflowWorkTaskResult struct {
	WorkItemID model.ID
	CommandID  model.ID
	MessageID  model.ID
	DeliveryID model.ID
	ThreadID   model.ID
	ReplyToID  model.ID
	EventID    model.ID
	EventSeq   int64
	Version    int64
	State      MessageState
	Replayed   bool
}

type WorkflowHandoffCommand struct {
	Actor              WorkflowCommunicationActor
	WorkItemID         model.ID
	ChannelID          model.ID
	Target             RecipientRef
	Content            HandoffContent
	AckDeadline        time.Time
	ExpectedOwnerEpoch int64
	IdempotencyKey     string
}

type WorkflowHandoffResult struct {
	WorkItemID model.ID
	CommandID  model.ID
	HandoffID  model.ID
	MessageID  model.ID
	DeliveryID model.ID
	EventID    model.ID
	EventSeq   int64
	Version    int64
	State      HandoffState
	OwnerEpoch int64
	Replayed   bool
}

type WorkflowAckTargetKind string

const (
	WorkflowAckTargetMessage WorkflowAckTargetKind = "message"
	WorkflowAckTargetHandoff WorkflowAckTargetKind = "handoff"
)

type WorkflowAckStatus string

const (
	WorkflowAckPending      WorkflowAckStatus = "pending"
	WorkflowAckAcknowledged WorkflowAckStatus = "acknowledged"
	WorkflowAckRejected     WorkflowAckStatus = "rejected"
	WorkflowAckExpired      WorkflowAckStatus = "expired"
	WorkflowAckUnknown      WorkflowAckStatus = "unknown"
)

type WorkflowAckQuery struct {
	Actor         WorkflowCommunicationActor
	TargetKind    WorkflowAckTargetKind
	TargetID      model.ID
	AfterEventSeq int64
}

type WorkflowAckObservation struct {
	Status   WorkflowAckStatus
	AckID    model.ID
	EventID  model.ID
	EventSeq int64
	Detail   string
}

type workflowCommunicationScope struct {
	ref     DirectoryScopeRef
	work    model.Record
	channel Channel
}

type workflowCommunicationPreflight struct {
	direct        directNoticePublishPreflight
	identity      directNoticeReaderIdentityPreflight
	workItemID    model.ID
	messageKind   MessageKind
	sourceKind    ChannelRouteSourceKind
	sourceEvent   string
	ackDueAt      *time.Time
	commandScope  string
	auditRef      string
	request       []byte
	protocolReply *protocolReplyPublishPreflight
}

func workflowCommunicationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= workflowCommunicationTimeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, workflowCommunicationTimeout)
}

func workflowCommunicationStableID(key, purpose string) (model.ID, error) {
	key, purpose = strings.TrimSpace(key), strings.TrimSpace(purpose)
	if key == "" || len(key) > 1024 || purpose == "" || len(purpose) > 128 {
		return "", communicationError(
			ErrInvalidCommunicationModel, "workflow communication idempotency key is invalid",
		)
	}
	digest := sha256.Sum256([]byte("olivares.sessions.workflow-communication.v1\x00" + purpose + "\x00" + key))
	raw := append([]byte(nil), digest[:16]...)
	raw[6] = raw[6]&0x0f | 0x70
	raw[8] = raw[8]&0x3f | 0x80
	return model.ID(uuid.UUID(raw).String()), nil
}

func workflowCommunicationUserPrincipal(
	actor WorkflowCommunicationActor,
	scope DirectoryScopeRef,
) (CommunicationPrincipal, error) {
	actor.AuditKind, actor.AuditRef = strings.TrimSpace(actor.AuditKind), strings.TrimSpace(actor.AuditRef)
	if actor.AgentExternalID != "" || actor.SessionID != "" || actor.SessionRunRef != "" ||
		actor.SessionFence != 0 || actor.PurposeRestricted {
		return CommunicationPrincipal{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"workflow communication actor cannot be reconstructed by the current C5 user vertical",
		)
	}
	const prefix = "user:"
	if actor.AuditKind != model.ActorUser || !strings.HasPrefix(actor.AuditRef, prefix) {
		return CommunicationPrincipal{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication requires a durable user initiator",
		)
	}
	userID, err := model.ParseID(strings.TrimPrefix(actor.AuditRef, prefix))
	if err != nil || userID.IsZero() || actor.AuditRef != prefix+userID.String() {
		return CommunicationPrincipal{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication user initiator is malformed",
		)
	}
	principal := CommunicationPrincipal{UserID: userID}
	if err := ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return CommunicationPrincipal{}, err
	}
	return principal, nil
}

func (m *Module) workflowCommunicationScope(
	ctx context.Context,
	tenant model.TenantID,
	workItemID, channelID model.ID,
) (workflowCommunicationScope, error) {
	if tenant.IsZero() || tenant.IsSystem() || !validCanonicalCommunicationID(workItemID) ||
		!validCanonicalCommunicationID(channelID) {
		return workflowCommunicationScope{}, communicationError(
			ErrInvalidCommunicationModel, "workflow communication target is invalid",
		)
	}
	var result workflowCommunicationScope
	err := m.communicationData(tenant).View(ctx, func(sc store.Scope) error {
		items, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		work, err := items.Get(ctx, workItemID)
		if err != nil {
			return err
		}
		workspaceID, err := model.ParseID(work.String(colWorkWorkspaceID))
		if err != nil || workspaceID.IsZero() {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow WorkItem workspace is unavailable",
			)
		}
		channels, err := sc.Ext(channelKind)
		if err != nil {
			return err
		}
		channelRecord, err := channels.Get(ctx, channelID)
		if err != nil {
			return err
		}
		channel, err := channelFromRecord(channelRecord)
		if err != nil {
			return err
		}
		if channel.TenantID != tenant || channel.WorkspaceID != workspaceID ||
			channel.State != ChannelActive || channel.ContentProtection != ContentProtectionStorage {
			return communicationError(
				ErrInvalidCommunicationTransition,
				"workflow WorkItem and Channel do not share one active storage-protected workspace",
			)
		}
		result = workflowCommunicationScope{
			ref:  DirectoryScopeRef{TenantID: tenant, WorkspaceID: workspaceID},
			work: work, channel: channel,
		}
		return nil
	})
	if err != nil {
		return workflowCommunicationScope{}, normalizeMessageDerivedError(err)
	}
	return result, nil
}

func workflowCommunicationSelector(recipient RecipientRef, required bool, wake WakePolicy) (AudienceSelector, error) {
	selector, err := messageDerivedSelector(recipient, required, wake)
	if err != nil {
		return AudienceSelector{}, err
	}
	return selector, nil
}

func (m *Module) prepareWorkflowCommunicationPublish(
	ctx context.Context,
	tenant model.TenantID,
	actor WorkflowCommunicationActor,
	workItemID, channelID model.ID,
	recipient RecipientRef,
	content MessageContent,
	urgency MessageUrgency,
	ackDueAt *time.Time,
	idempotencyKey string,
	kind MessageKind,
	commandScope string,
) (workflowCommunicationPreflight, error) {
	return m.prepareWorkflowCommunicationPublishSource(
		ctx, tenant, actor, workItemID, channelID, recipient, content, urgency,
		ackDueAt, idempotencyKey, kind, commandScope, RouteSourceUserMessage, "",
	)
}

func (m *Module) prepareWorkflowCommunicationPublishSource(
	ctx context.Context,
	tenant model.TenantID,
	actor WorkflowCommunicationActor,
	workItemID, channelID model.ID,
	recipient RecipientRef,
	content MessageContent,
	urgency MessageUrgency,
	ackDueAt *time.Time,
	idempotencyKey string,
	kind MessageKind,
	commandScope string,
	sourceKind ChannelRouteSourceKind,
	sourceEvent string,
) (workflowCommunicationPreflight, error) {
	target, err := m.workflowCommunicationScope(ctx, tenant, workItemID, channelID)
	if err != nil {
		return workflowCommunicationPreflight{}, err
	}
	principal, err := workflowCommunicationUserPrincipal(actor, target.ref)
	if err != nil {
		return workflowCommunicationPreflight{}, err
	}
	if recipient.Kind != RecipientUser || recipient.Validate() != nil ||
		(kind != MessageWorkTask && kind != MessageHandoffOffer && kind != MessageNotice) ||
		!validateOpaqueRef(commandScope) || !sourceKind.Valid() ||
		(kind == MessageNotice && (sourceKind != RouteSourceProtocol ||
			(sourceEvent != protocolReplySourceEvent && sourceEvent != protocolInboundSourceEvent))) ||
		(kind != MessageNotice && (sourceKind != RouteSourceUserMessage || sourceEvent != "")) {
		return workflowCommunicationPreflight{}, communicationError(
			ErrInvalidCommunicationModel,
			"workflow communication requires a valid user recipient, Message kind and source",
		)
	}
	if urgency == "" {
		urgency = UrgencyNormal
	}
	if !urgency.Valid() {
		return workflowCommunicationPreflight{}, communicationError(
			ErrInvalidCommunicationModel, "workflow communication urgency is invalid",
		)
	}
	if _, err := CanonicalMessageContent(content); err != nil {
		return workflowCommunicationPreflight{}, err
	}
	if ackDueAt != nil {
		due := ackDueAt.UTC()
		if due.IsZero() {
			return workflowCommunicationPreflight{}, communicationError(
				ErrInvalidCommunicationModel, "workflow acknowledgement deadline is invalid",
			)
		}
		ackDueAt = &due
	}
	idempotencyID, err := workflowCommunicationStableID(idempotencyKey, commandScope)
	if err != nil {
		return workflowCommunicationPreflight{}, err
	}
	request, err := canonicalJSON(struct {
		SchemaVersion int64          `json:"schema_version"`
		CommandScope  string         `json:"command_scope"`
		WorkItemID    model.ID       `json:"work_item_id"`
		ChannelID     model.ID       `json:"channel_id"`
		Recipient     RecipientRef   `json:"recipient"`
		Content       MessageContent `json:"content"`
		Kind          MessageKind    `json:"kind"`
		Urgency       MessageUrgency `json:"urgency"`
		AckDueAt      *time.Time     `json:"ack_due_at,omitempty"`
		IdempotencyID model.ID       `json:"idempotency_id"`
	}{
		SchemaVersion: 1, CommandScope: commandScope, WorkItemID: workItemID,
		ChannelID: channelID, Recipient: recipient, Content: content, Kind: kind,
		Urgency: urgency, AckDueAt: ackDueAt, IdempotencyID: idempotencyID,
	})
	if err != nil {
		return workflowCommunicationPreflight{}, err
	}
	requestDigest := sha256.Sum256(request)
	directCommand := DirectNoticePublishCommand{
		ChannelID: channelID, Recipient: recipient, Content: content,
		Urgency: urgency, IdempotencyKey: idempotencyID.String(),
	}
	normalized, actorFingerprint, idempotencyHash, _, err :=
		normalizeDirectNoticePublishCommand(target.ref, principal, directCommand)
	if err != nil {
		return workflowCommunicationPreflight{}, err
	}
	normalized.CommandScope = commandScope
	preflight, err := m.preflightDirectNoticePublishWithoutCore(
		ctx, target.ref, principal, normalized,
		actorFingerprint, idempotencyHash, requestDigest[:],
	)
	if err != nil {
		return workflowCommunicationPreflight{}, err
	}
	if !communicationPortBound(m.communicationOperationAuthorizer) {
		return workflowCommunicationPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication C5 operation authorizer is unavailable",
		)
	}
	entity := EntityRef{
		TenantID: tenant, WorkspaceID: target.ref.WorkspaceID, Kind: channelKind, ID: channelID,
	}
	core, err := m.communicationOperationAuthorizer.AuthorizeEntityOperation(
		ctx, principal, entity, CommunicationMessageSend,
	)
	if err != nil || ValidateReadWitness(core) != nil || core.Outcome != ReadAllow ||
		core.Entity != entity || core.Operation != CommunicationMessageSend || core.Principal != principal {
		return workflowCommunicationPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication C5 send authority is unavailable",
		)
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(ctx, target.ref, principal, func(at time.Time) bool {
		return communicationEvidenceCurrent(core.ObservedAt, core.FreshUntil, at)
	})
	if err != nil {
		return workflowCommunicationPreflight{}, err
	}
	required := ackDueAt != nil
	selector, err := workflowCommunicationSelector(recipient, required, preflight.Channel.DefaultWake)
	if err != nil {
		return workflowCommunicationPreflight{}, err
	}
	requestedAt := m.clock.Now().Time()
	audienceRequest := PublicationAudienceRequest{
		Scope: target.ref, ChannelID: channelID,
		ChannelACLRevision:   preflight.Channel.ACLRevision,
		RouteRevision:        preflight.Channel.RouteRevision,
		SubscriptionRevision: preflight.Channel.SubscriptionRevision,
		MessageKind:          kind, Urgency: urgency, Sender: preflight.Sender,
		SourceKind: sourceKind, EventType: sourceEvent,
		ChannelDefaultWake:   preflight.Channel.DefaultWake,
		ContentProtection:    preflight.Channel.ContentProtection,
		ProtectionGeneration: preflight.Channel.ProtectionGeneration,
		RequestedAt:          requestedAt, Selectors: []AudienceSelector{selector},
	}
	if err := ValidatePublicationAudienceRequest(audienceRequest); err != nil {
		return workflowCommunicationPreflight{}, err
	}
	if !communicationPortBound(m.communicationAudienceAttestor) {
		return workflowCommunicationPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication audience attestor is unavailable",
		)
	}
	snapshot, attestation, err := m.communicationAudienceAttestor.AttestPublicationAudience(
		ctx, cloneDirectNoticePublicationAudienceRequest(audienceRequest),
	)
	if err != nil {
		return workflowCommunicationPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication audience attestation failed",
		)
	}
	snapshot = cloneDirectNoticeDirectorySnapshot(snapshot)
	attestation = cloneDirectNoticePublicationAudienceAttestation(attestation)
	if err := validateDirectNoticeSnapshot(audienceRequest, snapshot, attestation, recipient); err != nil {
		return workflowCommunicationPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication audience evidence is malformed",
		)
	}
	if preflight.GrantClosure.DirectoryEpoch != snapshot.Epoch ||
		preflight.RecipientGrantClosure.DirectoryEpoch != snapshot.Epoch ||
		identity.Resolution.Recipient == nil || identity.Resolution.Recipient.DirectoryEpoch != snapshot.Epoch ||
		!directNoticeCoreWitnessBindsDirectoryEpoch(core, tenant, snapshot.Epoch) {
		return workflowCommunicationPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication authority crossed directory epoch",
		)
	}
	preflight.Command = normalized
	preflight.CoreWitness = cloneCommunicationRequestAuthorityWitness(core)
	preflight.AudienceRequest = cloneDirectNoticePublicationAudienceRequest(audienceRequest)
	preflight.AudienceAttestation = attestation
	preflight.Snapshot = snapshot
	preflight.RequestDigest = requestDigest[:]
	return workflowCommunicationPreflight{
		direct: preflight, identity: identity, workItemID: workItemID,
		messageKind: kind, sourceKind: sourceKind, sourceEvent: sourceEvent,
		ackDueAt: ackDueAt, commandScope: commandScope,
		auditRef: actor.AuditRef, request: request,
	}, nil
}

func workflowCommunicationAuthority(
	preflight workflowCommunicationPreflight,
) (messageLifecycleAuthority, []store.AuthorizationFactRef, error) {
	facts, err := directNoticePublishAuthorityFacts(preflight.direct)
	if err != nil {
		return messageLifecycleAuthority{}, nil, err
	}
	window, err := directNoticeReaderAuthorityWindow(preflight.identity)
	if err != nil {
		return messageLifecycleAuthority{}, nil, err
	}
	observedAt := preflight.direct.CoreWitness.ObservedAt
	if window.observedAt.After(observedAt) {
		observedAt = window.observedAt
	}
	freshUntil := preflight.direct.CoreWitness.FreshUntil
	if window.freshUntil.Before(freshUntil) {
		freshUntil = window.freshUntil
	}
	if !freshUntil.After(observedAt) {
		return messageLifecycleAuthority{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication authority windows do not overlap",
		)
	}
	return messageLifecycleAuthority{
		Actor: preflight.direct.Sender, Facts: facts,
		ObservedAt: observedAt, FreshUntil: freshUntil,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "workflow_communication_current",
			EvidenceRef: "workflow-communication-c5",
		},
	}, facts, nil
}

func workflowCommunicationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("workflow communication %s: %w", operation, err)
}
