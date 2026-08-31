// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	protocolReplyCommandScope   = "workflow.message.protocol-reply"
	protocolReplyAuditAction    = "sessions.communication.protocol.reply.project"
	protocolReplyEventType      = "work.protocol.reply.available"
	protocolReplySourceEvent    = "a2a.reply"
	protocolInboundCommandScope = "workflow.message.protocol-inbound"
	protocolInboundAuditAction  = "sessions.communication.protocol.inbound.project"
	protocolInboundEventType    = "work.protocol.message.received"
	protocolInboundSourceEvent  = "a2a.message.received"
	maxProtocolReplyParts       = 62
)

// ProtocolReplyCommunication is the private K5 projection seam. It can only
// create an ordinary K3 notice; it has no Ack, Decision or WorkItem lifecycle
// operation. Composition invokes ProjectProtocolReply inside ApplyProtocolReplay.
type ProtocolReplyCommunication interface {
	ProjectProtocolReply(context.Context, model.TenantID, ProtocolReplyCommand) (ProtocolReplyResult, error)
	GetProtocolReply(context.Context, model.TenantID, ProtocolReplyRef) (ProtocolReplyResult, error)
}

var _ ProtocolReplyCommunication = (*Module)(nil)

type ProtocolReplyKind string

const (
	ProtocolReplyMessage  ProtocolReplyKind = "message"
	ProtocolReplyArtifact ProtocolReplyKind = "artifact"
)

// ProtocolReplyFlow distinguishes a response observed on an outbound binding
// from a Message accepted on an inbound binding. The zero value retains the
// original response projection for existing composition callers.
type ProtocolReplyFlow string

const (
	ProtocolReplyFlowResponse ProtocolReplyFlow = "reply"
	ProtocolReplyFlowInbound  ProtocolReplyFlow = "inbound"
)

type ProtocolReplyPartKind string

const (
	ProtocolReplyPartText ProtocolReplyPartKind = "text"
	ProtocolReplyPartData ProtocolReplyPartKind = "data"
	ProtocolReplyPartFile ProtocolReplyPartKind = "file"
)

// ProtocolReplyPart is already connector-sanitized. Sessions validates that
// text remains bounded plain text and that non-text values are reference/hash
// projections; it never accepts raw JSON or artifact bytes.
type ProtocolReplyPart struct {
	Kind      ProtocolReplyPartKind `json:"kind"`
	Text      string                `json:"text,omitempty"`
	Reference string                `json:"reference,omitempty"`
	Digest    string                `json:"digest"`
}

type ProtocolReplyCommand struct {
	Flow          ProtocolReplyFlow      `json:"flow,omitempty"`
	BindingID     model.ID               `json:"binding_id"`
	Generation    int64                  `json:"generation"`
	Route         ProtocolInterruptRoute `json:"route"`
	PeerAuthority string                 `json:"peer_authority"`
	Kind          ProtocolReplyKind      `json:"kind"`
	TaskID        string                 `json:"task_id,omitempty"`
	ContextID     string                 `json:"context_id"`
	MessageID     string                 `json:"message_id,omitempty"`
	ArtifactID    string                 `json:"artifact_id,omitempty"`
	Parts         []ProtocolReplyPart    `json:"parts"`
	SourceDigest  string                 `json:"source_digest"`
}

// ProtocolReplyRef contains only the semantic identity needed to reload the
// durable reply after ApplyProtocolReplay reports an exact replay.
type ProtocolReplyRef struct {
	Flow          ProtocolReplyFlow `json:"flow,omitempty"`
	BindingID     model.ID          `json:"binding_id"`
	Generation    int64             `json:"generation"`
	PeerAuthority string            `json:"peer_authority"`
	Kind          ProtocolReplyKind `json:"kind"`
	TaskID        string            `json:"task_id,omitempty"`
	ContextID     string            `json:"context_id"`
	MessageID     string            `json:"message_id,omitempty"`
	ArtifactID    string            `json:"artifact_id,omitempty"`
	SourceDigest  string            `json:"source_digest"`
}

type ProtocolReplyResult struct {
	BindingID  model.ID     `json:"binding_id"`
	Generation int64        `json:"generation"`
	WorkItemID model.ID     `json:"work_item_id"`
	CommandID  model.ID     `json:"command_id"`
	MessageID  model.ID     `json:"message_id"`
	DeliveryID model.ID     `json:"delivery_id"`
	ThreadID   model.ID     `json:"thread_id"`
	ReplyToID  model.ID     `json:"reply_to_id,omitempty"`
	EventID    model.ID     `json:"event_id"`
	EventSeq   int64        `json:"event_seq"`
	Version    int64        `json:"version"`
	State      MessageState `json:"state"`
	Replayed   bool         `json:"-"`
}

type normalizedProtocolReply struct {
	ref      ProtocolReplyRef
	route    ProtocolInterruptRoute
	parts    []ProtocolReplyPart
	semantic string
	content  MessageContent
}

// protocolReplyPublishPreflight carries protocol lineage into the locked K3
// publish path. Route is operator-owned and never derived from a peer payload.
type protocolReplyPublishPreflight struct {
	flow          ProtocolReplyFlow
	bindingID     model.ID
	generation    int64
	route         ProtocolInterruptRoute
	peerAuthority string
	contextID     string
	taskID        string
	messageID     string
	artifactID    string
	sourceDigest  string
	semantic      string
	rootID        model.ID
	parent        *Message
	threadID      model.ID
	replyToID     model.ID
}

func (cmd ProtocolReplyCommand) Ref() ProtocolReplyRef {
	return ProtocolReplyRef{
		Flow:      cmd.Flow,
		BindingID: cmd.BindingID, Generation: cmd.Generation,
		PeerAuthority: cmd.PeerAuthority, Kind: cmd.Kind, TaskID: cmd.TaskID,
		ContextID: cmd.ContextID, MessageID: cmd.MessageID,
		ArtifactID: cmd.ArtifactID, SourceDigest: cmd.SourceDigest,
	}
}

func normalizeProtocolReplyCommand(cmd ProtocolReplyCommand) (normalizedProtocolReply, error) {
	ref, semantic, err := normalizeProtocolReplyRef(cmd.Ref())
	if err != nil {
		return normalizedProtocolReply{}, err
	}
	route, _, err := cmd.Route.normalize()
	if err != nil {
		return normalizedProtocolReply{}, err
	}
	if len(cmd.Parts) < 1 || len(cmd.Parts) > maxProtocolReplyParts {
		return normalizedProtocolReply{}, communicationError(
			ErrInvalidCommunicationModel, "protocol reply Part set is empty or exceeds its bound",
		)
	}
	parts := append([]ProtocolReplyPart(nil), cmd.Parts...)
	for index := range parts {
		if err := validateProtocolReplyPart(parts[index]); err != nil {
			return normalizedProtocolReply{}, err
		}
	}
	content := protocolReplyMessageContent(ref, parts)
	if _, err := CanonicalMessageContent(content); err != nil {
		return normalizedProtocolReply{}, err
	}
	return normalizedProtocolReply{
		ref: ref, route: route, parts: parts, semantic: semantic, content: content,
	}, nil
}

func normalizeProtocolReplyRef(ref ProtocolReplyRef) (ProtocolReplyRef, string, error) {
	if ref.Flow == "" {
		ref.Flow = ProtocolReplyFlowResponse
	}
	peer, err := normalizeProtocolAuthority(ref.PeerAuthority)
	if err != nil || peer != ref.PeerAuthority || !validCanonicalCommunicationID(ref.BindingID) ||
		(ref.Flow != ProtocolReplyFlowResponse && ref.Flow != ProtocolReplyFlowInbound) ||
		ref.Generation < 1 || (ref.Kind != ProtocolReplyMessage && ref.Kind != ProtocolReplyArtifact) ||
		!validProtocolReplyExternalID(ref.ContextID) ||
		(ref.TaskID != "" && !validProtocolReplyExternalID(ref.TaskID)) ||
		(ref.MessageID != "" && !validProtocolReplyExternalID(ref.MessageID)) ||
		(ref.ArtifactID != "" && !validProtocolReplyExternalID(ref.ArtifactID)) {
		return ProtocolReplyRef{}, "", communicationError(
			ErrInvalidCommunicationModel, "protocol reply identity is invalid",
		)
	}
	if (ref.Kind == ProtocolReplyMessage && (ref.MessageID == "" || ref.ArtifactID != "")) ||
		(ref.Kind == ProtocolReplyArtifact && (ref.TaskID == "" || ref.ArtifactID == "" || ref.MessageID != "")) {
		return ProtocolReplyRef{}, "", communicationError(
			ErrInvalidCommunicationModel, "protocol reply kind and external identity disagree",
		)
	}
	if _, err := parseProtocolReplyDigest(ref.SourceDigest); err != nil {
		return ProtocolReplyRef{}, "", err
	}
	if ref.Flow == ProtocolReplyFlowInbound &&
		(ref.Kind != ProtocolReplyMessage || ref.TaskID == "") {
		return ProtocolReplyRef{}, "", communicationError(
			ErrInvalidCommunicationModel, "inbound protocol Message identity is incomplete",
		)
	}
	if ref.Flow == ProtocolReplyFlowResponse {
		identity, err := canonicalJSON(struct {
			SchemaVersion int64             `json:"schema_version"`
			BindingID     model.ID          `json:"binding_id"`
			Generation    int64             `json:"generation"`
			PeerAuthority string            `json:"peer_authority"`
			Kind          ProtocolReplyKind `json:"kind"`
			TaskID        string            `json:"task_id,omitempty"`
			ContextID     string            `json:"context_id"`
			MessageID     string            `json:"message_id,omitempty"`
			ArtifactID    string            `json:"artifact_id,omitempty"`
			SourceDigest  string            `json:"source_digest"`
		}{1, ref.BindingID, ref.Generation, ref.PeerAuthority, ref.Kind, ref.TaskID,
			ref.ContextID, ref.MessageID, ref.ArtifactID, ref.SourceDigest})
		if err != nil {
			return ProtocolReplyRef{}, "", err
		}
		digest := sha256.Sum256(identity)
		return ref, "protocol-reply:" + hex.EncodeToString(digest[:]), nil
	}
	identity, err := canonicalJSON(struct {
		SchemaVersion int64             `json:"schema_version"`
		Flow          ProtocolReplyFlow `json:"flow"`
		BindingID     model.ID          `json:"binding_id"`
		Generation    int64             `json:"generation"`
		PeerAuthority string            `json:"peer_authority"`
		Kind          ProtocolReplyKind `json:"kind"`
		TaskID        string            `json:"task_id,omitempty"`
		ContextID     string            `json:"context_id"`
		MessageID     string            `json:"message_id,omitempty"`
		ArtifactID    string            `json:"artifact_id,omitempty"`
		SourceDigest  string            `json:"source_digest"`
	}{2, ref.Flow, ref.BindingID, ref.Generation, ref.PeerAuthority, ref.Kind, ref.TaskID,
		ref.ContextID, ref.MessageID, ref.ArtifactID, ref.SourceDigest})
	if err != nil {
		return ProtocolReplyRef{}, "", err
	}
	digest := sha256.Sum256(identity)
	return ref, "protocol-reply:" + hex.EncodeToString(digest[:]), nil
}

func validProtocolReplyExternalID(value string) bool {
	return validateOpaqueRef(value) && value == strings.TrimSpace(value)
}

func parseProtocolReplyDigest(value string) ([]byte, error) {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return nil, communicationError(
			ErrInvalidCommunicationModel, "protocol reply digest is not canonical SHA-256",
		)
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != sha256.Size || hex.EncodeToString(raw) != value {
		return nil, communicationError(
			ErrInvalidCommunicationModel, "protocol reply digest is not canonical SHA-256",
		)
	}
	return raw, nil
}

func validateProtocolReplyPart(part ProtocolReplyPart) error {
	if _, err := parseProtocolReplyDigest(part.Digest); err != nil {
		return err
	}
	switch part.Kind {
	case ProtocolReplyPartText:
		if part.Reference != "" || !validProtocolReplyPlainText(part.Text) {
			return communicationError(ErrInvalidCommunicationModel, "protocol reply text Part is invalid")
		}
	case ProtocolReplyPartData, ProtocolReplyPartFile:
		if part.Text != "" || !validateOpaqueRef(part.Reference) {
			return communicationError(ErrInvalidCommunicationModel, "protocol reply reference Part is invalid")
		}
	default:
		return communicationError(ErrInvalidCommunicationModel, "protocol reply Part kind is invalid")
	}
	return nil
}

func validProtocolReplyPlainText(value string) bool {
	if !boundedText(value, 1, maxMessageTextBytes) || !utf8.ValidString(value) ||
		strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\r') {
		return false
	}
	for _, r := range value {
		if r != '\n' && r != '\t' && (r < 0x20 || r == 0x7f) {
			return false
		}
	}
	return true
}

func protocolReplyMessageContent(ref ProtocolReplyRef, parts []ProtocolReplyPart) MessageContent {
	subject, externalKind, externalRef := "Remote agent reply", "a2a_message", ref.MessageID
	if ref.Flow == ProtocolReplyFlowInbound {
		subject = "Remote agent message"
	}
	if ref.Kind == ProtocolReplyArtifact {
		subject, externalKind, externalRef = "Remote artifact update", "a2a_artifact", ref.ArtifactID
	}
	blocks := make([]MessageContentBlock, 0, len(parts)+2)
	blocks = append(blocks,
		MessageContentBlock{Type: ContentBlockReference, Reference: &ContentReference{
			Kind: "protocol_binding", Ref: ref.BindingID.String(),
		}},
		MessageContentBlock{Type: ContentBlockReference, Reference: &ContentReference{
			Kind: externalKind, Ref: externalRef, Hash: ref.SourceDigest,
		}},
	)
	for _, part := range parts {
		if part.Kind == ProtocolReplyPartText {
			blocks = append(blocks, MessageContentBlock{
				Type: ContentBlockText, Format: TextPlain, Text: part.Text,
			})
			continue
		}
		kind := "a2a_data"
		if part.Kind == ProtocolReplyPartFile {
			kind = "a2a_file"
		}
		blocks = append(blocks, MessageContentBlock{
			Type: ContentBlockReference, Reference: &ContentReference{
				Kind: kind, Ref: part.Reference, Hash: part.Digest,
			},
		})
	}
	return MessageContent{Subject: subject, Blocks: blocks}
}

func protocolReplyStableID(semantic, purpose string) (model.ID, error) {
	return workflowCommunicationStableID(semantic, "protocol.reply."+purpose)
}

func protocolReplyStableIDs(semantic string) (directNoticePublishIDs, error) {
	var result directNoticePublishIDs
	fields := []struct {
		purpose string
		target  *model.ID
	}{
		{"message", &result.Message}, {"audience", &result.Audience},
		{"delivery", &result.Delivery}, {"contribution", &result.Contribution},
		{"command", &result.Command}, {"event", &result.Event}, {"receipt", &result.Receipt},
	}
	for _, field := range fields {
		id, err := protocolReplyStableID(semantic, field.purpose)
		if err != nil {
			return directNoticePublishIDs{}, err
		}
		*field.target = id
	}
	if !validDirectNoticePublishAuthorityIDs(result) {
		return directNoticePublishIDs{}, communicationError(
			ErrCommunicationEvidenceUnknown, "protocol reply stable identities are invalid",
		)
	}
	return result, nil
}

func (m *Module) ProjectProtocolReply(
	ctx context.Context,
	tenant model.TenantID,
	cmd ProtocolReplyCommand,
) (ProtocolReplyResult, error) {
	ctx, cancel := workflowCommunicationContext(ctx)
	defer cancel()
	normalized, err := normalizeProtocolReplyCommand(cmd)
	if err != nil {
		return ProtocolReplyResult{}, err
	}
	binding, err := m.protocolReplyBinding(ctx, tenant, normalized.ref)
	if err != nil {
		return ProtocolReplyResult{}, err
	}
	actor := workflowProtocolUserActor(normalized.route.SenderUserID)
	commandScope, auditAction, eventType, sourceEvent := protocolReplyOperation(normalized.ref.Flow)
	preflight, err := m.prepareWorkflowCommunicationPublishSource(
		ctx, tenant, actor, binding.WorkItemID, normalized.route.ChannelID,
		RecipientRef{Kind: RecipientUser, Ref: normalized.route.RecipientUserID.String()},
		normalized.content, UrgencyNormal, nil, normalized.semantic,
		MessageNotice, commandScope, RouteSourceProtocol, sourceEvent,
	)
	if err != nil {
		return ProtocolReplyResult{}, workflowCommunicationError("preflight protocol reply", err)
	}
	ids, err := protocolReplyStableIDs(normalized.semantic)
	if err != nil {
		return ProtocolReplyResult{}, err
	}
	threadKind := "protocol-reply-thread"
	if normalized.ref.Flow == ProtocolReplyFlowInbound {
		threadKind = "protocol-inbound-thread"
	}
	rootID, err := workflowCommunicationStableID(
		fmt.Sprintf("%s:%s:%d:%s", threadKind, binding.ID, binding.Generation, binding.WorkItemID),
		"protocol.reply.thread",
	)
	if err != nil {
		return ProtocolReplyResult{}, err
	}
	preflight.direct.IDs = ids
	preflight.protocolReply = &protocolReplyPublishPreflight{
		flow:      normalized.ref.Flow,
		bindingID: binding.ID, generation: binding.Generation, route: normalized.route,
		peerAuthority: normalized.ref.PeerAuthority, contextID: normalized.ref.ContextID,
		taskID: normalized.ref.TaskID, messageID: normalized.ref.MessageID,
		artifactID: normalized.ref.ArtifactID, sourceDigest: normalized.ref.SourceDigest,
		semantic: normalized.semantic, rootID: rootID,
	}
	published, err := m.applyWorkflowCommunicationMessage(
		ctx, preflight, auditAction, eventType, 0,
	)
	if err != nil {
		return ProtocolReplyResult{}, workflowCommunicationError("project protocol reply", err)
	}
	if published.Replayed {
		return m.GetProtocolReply(ctx, tenant, normalized.ref)
	}
	return protocolReplyResult(binding, published), nil
}

func protocolReplyOperation(flow ProtocolReplyFlow) (commandScope, auditAction, eventType, sourceEvent string) {
	if flow == ProtocolReplyFlowInbound {
		return protocolInboundCommandScope, protocolInboundAuditAction,
			protocolInboundEventType, protocolInboundSourceEvent
	}
	return protocolReplyCommandScope, protocolReplyAuditAction,
		protocolReplyEventType, protocolReplySourceEvent
}

func protocolReplyResult(binding ProtocolBinding, published WorkflowWorkTaskResult) ProtocolReplyResult {
	return ProtocolReplyResult{
		BindingID: binding.ID, Generation: binding.Generation, WorkItemID: published.WorkItemID,
		CommandID: published.CommandID, MessageID: published.MessageID,
		DeliveryID: published.DeliveryID, ThreadID: published.ThreadID,
		ReplyToID: published.ReplyToID, EventID: published.EventID,
		EventSeq: published.EventSeq, Version: published.Version,
		State: published.State, Replayed: published.Replayed,
	}
}

func (m *Module) protocolReplyBinding(
	ctx context.Context,
	tenant model.TenantID,
	ref ProtocolReplyRef,
) (ProtocolBinding, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return ProtocolBinding{}, communicationError(ErrInvalidCommunicationModel, "protocol reply tenant is invalid")
	}
	binding, err := m.GetProtocolBinding(ctx, tenant, ProtocolBindingRef{ID: ref.BindingID})
	if err != nil {
		return ProtocolBinding{}, err
	}
	if err := validateProtocolReplyBinding(binding, ref); err != nil {
		return ProtocolBinding{}, err
	}
	return binding, nil
}

func validateProtocolReplyBinding(binding ProtocolBinding, ref ProtocolReplyRef) error {
	if binding.ID != ref.BindingID || binding.Generation != ref.Generation ||
		binding.Protocol != BindingProtocolA2A ||
		!validCanonicalCommunicationID(binding.WorkItemID) ||
		binding.PeerAuthority != ref.PeerAuthority || binding.ContextID != ref.ContextID {
		return communicationError(
			ErrInvalidCommunicationTransition, "protocol reply binding does not match the response lineage",
		)
	}
	if ref.Flow == ProtocolReplyFlowInbound {
		if (binding.Direction != BindingInbound && binding.Direction != BindingBidirectional) ||
			binding.ExternalKind != string(ProtocolBindingResultTask) ||
			binding.ExternalID != ref.TaskID || !binding.MessageID.IsZero() ||
			!binding.DeliveryID.IsZero() || binding.ExternalMessageID != "" {
			return communicationError(
				ErrInvalidCommunicationTransition, "inbound protocol Message changed binding lineage",
			)
		}
		return nil
	}
	if (binding.Direction != BindingOutbound && binding.Direction != BindingBidirectional) ||
		(binding.MessageID.IsZero() != binding.DeliveryID.IsZero()) {
		return communicationError(
			ErrInvalidCommunicationTransition, "protocol reply binding does not match the response lineage",
		)
	}
	switch binding.ExternalKind {
	case string(ProtocolBindingResultMessage):
		if ref.Kind != ProtocolReplyMessage || binding.ExternalMessageID != ref.MessageID || ref.TaskID != "" {
			return communicationError(ErrInvalidCommunicationTransition, "protocol Message reply changed identity")
		}
	case string(ProtocolBindingResultTask):
		if ref.TaskID == "" || binding.ExternalID != ref.TaskID {
			return communicationError(ErrInvalidCommunicationTransition, "protocol Task reply changed identity")
		}
	default:
		return communicationError(ErrInvalidCommunicationTransition, "protocol reply binding is not settled")
	}
	return nil
}

func lockWorkflowProtocolReplyLineage(
	ctx context.Context,
	tx *communicationTx,
	sc store.Scope,
	preflight *workflowCommunicationPreflight,
) error {
	if preflight == nil || preflight.protocolReply == nil {
		return communicationError(ErrCommunicationEvidenceUnknown, "protocol reply preflight is absent")
	}
	reply := preflight.protocolReply
	repo, err := sc.Ext(protocolBindingKind)
	if err != nil {
		return err
	}
	locker, ok := repo.(store.RowLocker[model.Record])
	if !ok {
		return communicationTransactionUnavailable("protocol reply binding row lock", nil)
	}
	var record model.Record
	record, err = runCommunicationBoundAuthorityObservation(
		tx.boundAuthorityState,
		func() (model.Record, error) { return locker.Lock(ctx, reply.bindingID) },
	)
	if err != nil {
		return err
	}
	stored, err := decodeProtocolBinding(record)
	if err != nil {
		return err
	}
	ref := ProtocolReplyRef{
		Flow:      reply.flow,
		BindingID: reply.bindingID, Generation: reply.generation,
		PeerAuthority: reply.peerAuthority, ContextID: reply.contextID,
		TaskID: reply.taskID, MessageID: reply.messageID, ArtifactID: reply.artifactID,
		SourceDigest: reply.sourceDigest,
	}
	if reply.artifactID != "" {
		ref.Kind = ProtocolReplyArtifact
	} else {
		ref.Kind = ProtocolReplyMessage
	}
	if err := validateProtocolReplyBinding(stored.ProtocolBinding, ref); err != nil {
		return err
	}
	if stored.WorkspaceID != preflight.direct.Scope.WorkspaceID ||
		stored.WorkItemID != preflight.workItemID ||
		reply.route.ChannelID != preflight.direct.Channel.ID ||
		preflight.direct.Sender != (CommunicationActorRef{
			Kind: ActorUser, Ref: reply.route.SenderUserID.String(),
		}) || preflight.direct.Command.Recipient != (RecipientRef{
		Kind: RecipientUser, Ref: reply.route.RecipientUserID.String(),
	}) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "protocol reply local route changed before publication",
		)
	}

	var parent *messageLifecycleLocked
	if !stored.MessageID.IsZero() {
		locked, lockErr := lockMessageLifecycleCarrier(ctx, tx, sc, stored.MessageID)
		if lockErr != nil {
			return lockErr
		}
		if err := validateProtocolReplyCarrier(stored.ProtocolBinding, reply.route, locked); err != nil {
			return err
		}
		parent = &locked
	} else {
		messages, repoErr := tx.repo(messageKind)
		if repoErr != nil {
			return repoErr
		}
		_, getErr := messages.Get(ctx, reply.rootID)
		switch {
		case getErr == nil:
			locked, lockErr := lockMessageLifecycleCarrier(ctx, tx, sc, reply.rootID)
			if lockErr != nil {
				return lockErr
			}
			if err := validateProtocolReplyRoot(stored.ProtocolBinding, reply.route, locked, reply.rootID); err != nil {
				return err
			}
			parent = &locked
		case !errors.Is(getErr, store.ErrNotFound):
			return getErr
		}
	}
	if parent == nil {
		preflight.direct.IDs.Message = reply.rootID
		reply.threadID = reply.rootID
		reply.replyToID = ""
		reply.parent = nil
		return nil
	}
	if preflight.direct.IDs.Message == parent.message.ID {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "protocol reply identity collides with its durable parent",
		)
	}
	value := parent.message
	reply.parent = &value
	reply.threadID = value.ThreadID
	reply.replyToID = value.ID
	return nil
}

func validateProtocolReplyCarrier(
	binding ProtocolBinding,
	route ProtocolInterruptRoute,
	locked messageLifecycleLocked,
) error {
	parent := locked.message
	if parent.ID != binding.MessageID || parent.WorkItemID != binding.WorkItemID ||
		parent.WorkspaceID != binding.WorkspaceID || parent.ChannelID != route.ChannelID ||
		parent.State != MessagePublished || parent.Sender != (CommunicationActorRef{
		Kind: ActorUser, Ref: route.RecipientUserID.String(),
	}) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "protocol reply carrier crossed Message lineage",
		)
	}
	found := false
	for _, delivery := range locked.deliveries {
		if delivery.ID != binding.DeliveryID {
			continue
		}
		if delivery.MessageID != parent.ID || delivery.Recipient != (RecipientRef{
			Kind: RecipientUser, Ref: route.SenderUserID.String(),
		}) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "protocol reply carrier Delivery changed route",
			)
		}
		found = true
	}
	if !found {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "protocol reply carrier Delivery is unavailable",
		)
	}
	return nil
}

func validateProtocolReplyRoot(
	binding ProtocolBinding,
	route ProtocolInterruptRoute,
	locked messageLifecycleLocked,
	rootID model.ID,
) error {
	root := locked.message
	if root.ID != rootID || root.WorkItemID != binding.WorkItemID ||
		root.WorkspaceID != binding.WorkspaceID || root.ChannelID != route.ChannelID ||
		root.ThreadID != rootID || !root.ReplyToID.IsZero() || root.Kind != MessageNotice ||
		root.State != MessagePublished || root.AckPolicy != AckPolicyNone ||
		root.Sender != (CommunicationActorRef{Kind: ActorUser, Ref: route.SenderUserID.String()}) ||
		len(locked.deliveries) != 1 || locked.deliveries[0].MessageID != root.ID ||
		locked.deliveries[0].Required || locked.deliveries[0].Recipient != (RecipientRef{
		Kind: RecipientUser, Ref: route.RecipientUserID.String(),
	}) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "protocol reply local thread root changed identity",
		)
	}
	return nil
}

func (m *Module) GetProtocolReply(
	ctx context.Context,
	tenant model.TenantID,
	ref ProtocolReplyRef,
) (ProtocolReplyResult, error) {
	ref, semantic, err := normalizeProtocolReplyRef(ref)
	if err != nil {
		return ProtocolReplyResult{}, err
	}
	binding, err := m.protocolReplyBinding(ctx, tenant, ref)
	if err != nil {
		return ProtocolReplyResult{}, err
	}
	receiptID, err := protocolReplyStableID(semantic, "receipt")
	if err != nil {
		return ProtocolReplyResult{}, err
	}
	var published WorkflowWorkTaskResult
	commandScope, _, _, _ := protocolReplyOperation(ref.Flow)
	err = m.communicationData(tenant).View(ctx, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, binding.WorkspaceID)
		if err != nil {
			return err
		}
		receipts, err := confined.Ext(communicationCommandKind)
		if err != nil {
			return err
		}
		record, err := receipts.Get(ctx, receiptID)
		if err != nil {
			return err
		}
		receipt, err := communicationCommandReceiptFromRecord(record)
		if err != nil || receipt.ID != receiptID || receipt.CommandScope != commandScope {
			return communicationError(ErrCommunicationEvidenceUnknown, "protocol reply receipt is malformed")
		}
		published, err = workflowCommunicationResultFromReceipt(
			receipt, workflowCommunicationPreflight{workItemID: binding.WorkItemID},
		)
		if err != nil {
			return err
		}
		messages, err := confined.Ext(messageKind)
		if err != nil {
			return err
		}
		messageRecord, err := messages.Get(ctx, published.MessageID)
		if err != nil {
			return err
		}
		message, err := messageFromRecord(messageRecord, 0)
		published.ThreadID, published.ReplyToID = message.ThreadID, message.ReplyToID
		if err != nil || message.Kind != MessageNotice || message.WorkItemID != binding.WorkItemID ||
			message.AckPolicy != AckPolicyNone {
			return communicationError(ErrCommunicationEvidenceUnknown, "protocol reply Message is malformed")
		}
		deliveries, err := confined.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		deliveryRecord, err := deliveries.Get(ctx, published.DeliveryID)
		if err != nil {
			return err
		}
		delivery, err := messageDeliveryFromRecord(deliveryRecord)
		if err != nil || delivery.MessageID != message.ID || delivery.Required || delivery.AckDueAt != nil {
			return communicationError(ErrCommunicationEvidenceUnknown, "protocol reply Delivery is malformed")
		}
		return nil
	})
	if err != nil {
		return ProtocolReplyResult{}, normalizeMessageDerivedError(err)
	}
	published.Replayed = true
	return protocolReplyResult(binding, published), nil
}
