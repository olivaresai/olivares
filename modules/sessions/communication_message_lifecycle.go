// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	messageLifecycleRetractScope = "message.lifecycle.retract"
	messageLifecycleExpireScope  = "message.lifecycle.expire"
	messageLifecycleOverdueScope = "message.lifecycle.overdue"

	messageLifecycleRetractAudit = "sessions.communication.message.retract"
	messageLifecycleExpireAudit  = "sessions.communication.message.expire"
	messageLifecycleOverdueAudit = "sessions.communication.message.overdue"

	messageLifecycleRetractedEvent = "work.message.retracted"
	messageLifecycleExpiredEvent   = "work.message.expired"
	messageLifecycleOverdueEvent   = "work.message.overdue"
	messageLifecycleCarrierBound   = 4096
)

// messageLifecycleAction is private while the K3 composition conjunction is
// OFF. It is deliberately distinct from MessageTransition because overdue
// terminalizes eligible Deliveries without terminalizing their Message.
type messageLifecycleAction string

const (
	messageLifecycleRetract messageLifecycleAction = "retract"
	messageLifecycleExpire  messageLifecycleAction = "expire"
	messageLifecycleOverdue messageLifecycleAction = "overdue"
)

// messageLifecycleAuthorizer is the narrow future composition port. Its
// implementation must answer the exact Message/action question and return the
// current versioned authorization facts. The service never accepts authority
// evidence in a wire command.
type messageLifecycleAuthorizer interface {
	AuthorizeMessageLifecycle(
		context.Context,
		DirectoryScopeRef,
		model.ID,
		messageLifecycleAction,
	) (messageLifecycleAuthority, error)
}

type messageLifecycleAuthority struct {
	Actor      CommunicationActorRef
	Facts      []store.AuthorizationFactRef
	ObservedAt time.Time
	FreshUntil time.Time
	Evidence   AuthorityEvidence
}

type messageLifecycleService struct {
	module     *Module
	authorizer messageLifecycleAuthorizer
	newID      func() model.ID
}

// newMessageLifecycleService is intentionally private and unwired. Merely
// compiling this vertical cannot make K3 reachable while readiness is OFF.
func newMessageLifecycleService(
	module *Module,
	authorizer messageLifecycleAuthorizer,
	newID func() model.ID,
) (*messageLifecycleService, error) {
	if module == nil || authorizer == nil {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"Message lifecycle ports are unavailable",
		)
	}
	if newID == nil {
		newID = model.NewID
	}
	return &messageLifecycleService{module: module, authorizer: authorizer, newID: newID}, nil
}

type messageLifecycleCommand struct {
	MessageID       model.ID
	ExpectedVersion int64
	TerminalCode    string
	Reason          CommunicationReasonContent
	IdempotencyKey  string
}

type messageOverdueCommand struct {
	MessageID       model.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

type messageLifecycleResult struct {
	CommandID         model.ID
	MessageID         model.ID
	EventID           model.ID
	Version           int64
	State             MessageState
	DeliveryChanges   int64
	DecisionRequestID model.ID
	HandoffID         model.ID
	AuditSeq          int64
	Replayed          bool
}

type messageOverdueResult struct {
	CommandID    model.ID
	MessageID    model.ID
	EventID      model.ID
	Version      int64
	ExpiredCount int64
	Fulfillment  FulfillmentProjection
	AuditSeq     int64
	Replayed     bool
}

type messageLifecycleNormalized struct {
	action             messageLifecycleAction
	command            messageLifecycleCommand
	scope              DirectoryScopeRef
	authority          messageLifecycleAuthority
	actorFingerprint   []byte
	idempotencyKeyHash []byte
	requestDigest      []byte
	commandScope       string
}

type messageOverdueNormalized struct {
	command            messageOverdueCommand
	scope              DirectoryScopeRef
	authority          messageLifecycleAuthority
	actorFingerprint   []byte
	idempotencyKeyHash []byte
	requestDigest      []byte
	commandScope       string
}

type messageLifecyclePrepared struct {
	messageReason  ProtectedPayload
	decisionReason ProtectedPayload
	handoffReason  ProtectedPayload
	messageHash    []byte
	decisionID     model.ID
	handoffID      model.ID
}

type messageLifecycleIDs struct {
	Command          model.ID
	Event            model.ID
	Receipt          model.ID
	DecisionResponse model.ID
}

func (ids messageLifecycleIDs) valid() bool {
	all := [...]model.ID{ids.Command, ids.Event, ids.Receipt, ids.DecisionResponse}
	for index, id := range all {
		if !validCanonicalCommunicationID(id) {
			return false
		}
		for prior := range index {
			if id == all[prior] {
				return false
			}
		}
	}
	return true
}

func (s *messageLifecycleService) Transition(
	ctx context.Context,
	scope DirectoryScopeRef,
	action messageLifecycleAction,
	command messageLifecycleCommand,
) (messageLifecycleResult, error) {
	if s == nil || s.module == nil || s.authorizer == nil || ctx == nil {
		return messageLifecycleResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle service is unavailable",
		)
	}
	if action != messageLifecycleRetract && action != messageLifecycleExpire {
		return messageLifecycleResult{}, communicationError(
			ErrInvalidCommunicationModel, "unsupported Message lifecycle action",
		)
	}
	normalized, err := s.normalizeLifecycle(ctx, scope, action, command)
	if err != nil {
		return messageLifecycleResult{}, err
	}
	ids := messageLifecycleIDs{
		Command: s.newID(), Event: s.newID(), Receipt: s.newID(), DecisionResponse: s.newID(),
	}
	if !ids.valid() {
		return messageLifecycleResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle identities are unavailable",
		)
	}
	prepared, err := s.prepareLifecycle(ctx, normalized, ids)
	if err != nil {
		return messageLifecycleResult{}, normalizeMessageLifecycleError(err)
	}
	result, err := s.applyLifecycle(ctx, normalized, prepared, ids)
	return result, normalizeMessageLifecycleError(err)
}

// MaterializeOverdue expires only required available Deliveries whose Ack
// deadline has elapsed. The Message stays published, so late Ack evidence and
// thread history remain available exactly as required by C3.
func (s *messageLifecycleService) MaterializeOverdue(
	ctx context.Context,
	scope DirectoryScopeRef,
	command messageOverdueCommand,
) (messageOverdueResult, error) {
	if s == nil || s.module == nil || s.authorizer == nil || ctx == nil {
		return messageOverdueResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Message overdue service is unavailable",
		)
	}
	normalized, err := s.normalizeOverdue(ctx, scope, command)
	if err != nil {
		return messageOverdueResult{}, err
	}
	ids := messageLifecycleIDs{
		Command: s.newID(), Event: s.newID(), Receipt: s.newID(), DecisionResponse: s.newID(),
	}
	if !ids.valid() {
		return messageOverdueResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Message overdue identities are unavailable",
		)
	}
	result, err := s.applyOverdue(ctx, normalized, ids)
	return result, normalizeMessageLifecycleError(err)
}

func (s *messageLifecycleService) normalizeLifecycle(
	ctx context.Context,
	scope DirectoryScopeRef,
	action messageLifecycleAction,
	command messageLifecycleCommand,
) (messageLifecycleNormalized, error) {
	if err := scope.Validate(); err != nil {
		return messageLifecycleNormalized{}, err
	}
	if !validCanonicalCommunicationID(command.MessageID) || command.ExpectedVersion < 1 ||
		!boundedToken(command.TerminalCode, 128) {
		return messageLifecycleNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "invalid Message lifecycle command",
		)
	}
	if _, err := CanonicalProtectedPayloadSlot(PayloadSlotMessageTerminalReason, command.Reason); err != nil {
		return messageLifecycleNormalized{}, err
	}
	keyID, err := model.ParseID(command.IdempotencyKey)
	if err != nil || !validCanonicalCommunicationID(keyID) || keyID.String() != command.IdempotencyKey {
		return messageLifecycleNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "Message lifecycle requires a canonical UUIDv7 idempotency key",
		)
	}
	authority, err := s.authorizer.AuthorizeMessageLifecycle(
		ctx, scope, command.MessageID, action,
	)
	if err != nil {
		return messageLifecycleNormalized{}, err
	}
	if err := validateMessageLifecycleAuthority(scope, authority, action); err != nil {
		return messageLifecycleNormalized{}, err
	}
	actorFingerprint, err := messageLifecycleActorFingerprint(scope, authority.Actor)
	if err != nil {
		return messageLifecycleNormalized{}, err
	}
	idempotency := sha256.Sum256([]byte(command.IdempotencyKey))
	commandScope := messageLifecycleRetractScope
	if action == messageLifecycleExpire {
		commandScope = messageLifecycleExpireScope
	}
	raw, err := canonicalJSON(struct {
		SchemaVersion   int64                      `json:"schema_version"`
		Action          messageLifecycleAction     `json:"action"`
		Scope           DirectoryScopeRef          `json:"scope"`
		MessageID       model.ID                   `json:"message_id"`
		ExpectedVersion int64                      `json:"expected_version"`
		TerminalCode    string                     `json:"terminal_code"`
		Reason          CommunicationReasonContent `json:"reason"`
	}{1, action, scope, command.MessageID, command.ExpectedVersion, command.TerminalCode, command.Reason})
	if err != nil {
		return messageLifecycleNormalized{}, err
	}
	request := sha256.Sum256(raw)
	return messageLifecycleNormalized{
		action: action, command: command, scope: scope, authority: authority,
		actorFingerprint: actorFingerprint, idempotencyKeyHash: idempotency[:],
		requestDigest: request[:], commandScope: commandScope,
	}, nil
}

func (s *messageLifecycleService) normalizeOverdue(
	ctx context.Context,
	scope DirectoryScopeRef,
	command messageOverdueCommand,
) (messageOverdueNormalized, error) {
	if err := scope.Validate(); err != nil {
		return messageOverdueNormalized{}, err
	}
	if !validCanonicalCommunicationID(command.MessageID) || command.ExpectedVersion < 1 {
		return messageOverdueNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "invalid Message overdue command",
		)
	}
	keyID, err := model.ParseID(command.IdempotencyKey)
	if err != nil || !validCanonicalCommunicationID(keyID) || keyID.String() != command.IdempotencyKey {
		return messageOverdueNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "Message overdue requires a canonical UUIDv7 idempotency key",
		)
	}
	authority, err := s.authorizer.AuthorizeMessageLifecycle(
		ctx, scope, command.MessageID, messageLifecycleOverdue,
	)
	if err != nil {
		return messageOverdueNormalized{}, err
	}
	if err := validateMessageLifecycleAuthority(scope, authority, messageLifecycleOverdue); err != nil {
		return messageOverdueNormalized{}, err
	}
	actorFingerprint, err := messageLifecycleActorFingerprint(scope, authority.Actor)
	if err != nil {
		return messageOverdueNormalized{}, err
	}
	idempotency := sha256.Sum256([]byte(command.IdempotencyKey))
	raw, err := canonicalJSON(struct {
		SchemaVersion   int64                  `json:"schema_version"`
		Action          messageLifecycleAction `json:"action"`
		Scope           DirectoryScopeRef      `json:"scope"`
		MessageID       model.ID               `json:"message_id"`
		ExpectedVersion int64                  `json:"expected_version"`
	}{1, messageLifecycleOverdue, scope, command.MessageID, command.ExpectedVersion})
	if err != nil {
		return messageOverdueNormalized{}, err
	}
	request := sha256.Sum256(raw)
	return messageOverdueNormalized{
		command: command, scope: scope, authority: authority,
		actorFingerprint: actorFingerprint, idempotencyKeyHash: idempotency[:],
		requestDigest: request[:], commandScope: messageLifecycleOverdueScope,
	}, nil
}

func validateMessageLifecycleAuthority(
	scope DirectoryScopeRef,
	authority messageLifecycleAuthority,
	action messageLifecycleAction,
) error {
	if authority.Actor.Validate() != nil || authority.ObservedAt.IsZero() ||
		!authority.FreshUntil.After(authority.ObservedAt) ||
		ValidateAuthorityEvidence(authority.Evidence) != nil ||
		evidenceVerdict(authority.Evidence) != VerdictClean {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle authority is unavailable",
		)
	}
	if (action == messageLifecycleExpire || action == messageLifecycleOverdue) &&
		authority.Actor.Kind != ActorSystem {
		return communicationError(
			ErrCommunicationForbidden, "Message deadline actions require the system worker",
		)
	}
	facts, err := CanonicalAuthorizationFacts(authority.Facts)
	if err != nil || len(facts) == 0 || !equalCommunicationAuthorityFacts(facts, authority.Facts) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle authority facts are unavailable",
		)
	}
	foundDirectory := false
	for _, fact := range facts {
		if fact.Kind == model.DirectoryEpochKind && fact.ID == model.ID(scope.TenantID) {
			foundDirectory = true
		}
	}
	if !foundDirectory {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle authority lacks directory fencing",
		)
	}
	return nil
}

func messageLifecycleActorFingerprint(
	scope DirectoryScopeRef,
	actor CommunicationActorRef,
) ([]byte, error) {
	raw, err := canonicalJSON(struct {
		Domain string                `json:"domain"`
		Scope  DirectoryScopeRef     `json:"scope"`
		Actor  CommunicationActorRef `json:"actor"`
	}{"olivares.sessions.message-lifecycle.actor.v1", scope, actor})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func (s *messageLifecycleService) prepareLifecycle(
	ctx context.Context,
	normalized messageLifecycleNormalized,
	ids messageLifecycleIDs,
) (messageLifecyclePrepared, error) {
	var prepared messageLifecyclePrepared
	err := s.module.viewCommunication(ctx, normalized.scope, func(sc store.Scope) error {
		message, deliveries, err := readMessageLifecycleCarrierView(
			ctx, sc, normalized.command.MessageID,
		)
		if err != nil {
			return err
		}
		policy := protectedPayloadPolicyFrom(message.Payload)
		messageSchema, _ := PayloadSlotMessageTerminalReason.schema()
		messageReason, err := PrepareProtectedPayload(
			ctx, s.module.communicationSealer, PayloadSlotMessageTerminalReason, policy,
			ContentAAD{
				TenantID: normalized.scope.TenantID, WorkspaceID: normalized.scope.WorkspaceID,
				ChannelID: message.ChannelID, EntityKind: messageKind, EntityID: message.ID,
				Schema: messageSchema, ProtectionGeneration: policy.ProtectionGeneration,
			},
			normalized.command.Reason,
		)
		if err != nil {
			return err
		}
		prepared.messageReason = cloneProtectedPayload(messageReason)
		prepared.messageHash, err = CanonicalProtectedPayloadEnvelopeHash(message.Payload)
		if err != nil {
			return err
		}
		if message.Kind == MessageDecisionRequest {
			request, found, err := readLifecycleDecisionView(ctx, sc, message.ID)
			if err != nil || !found {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"linked DecisionRequest is unavailable before terminal preparation",
				)
			}
			prepared.decisionID = request.ID
			responseSchema, _ := PayloadSlotDecisionResponse.schema()
			response, err := PrepareProtectedPayload(
				ctx, s.module.communicationSealer, PayloadSlotDecisionResponse,
				protectedPayloadPolicyFrom(request.Request),
				ContentAAD{
					TenantID: normalized.scope.TenantID, WorkspaceID: normalized.scope.WorkspaceID,
					ChannelID: message.ChannelID, EntityKind: decisionResponseKind,
					EntityID: ids.DecisionResponse, Schema: responseSchema,
					ProtectionGeneration: request.Request.ProtectionGeneration,
				},
				DecisionResponseContent{Reason: normalized.command.Reason},
			)
			if err != nil {
				return err
			}
			prepared.decisionReason = cloneProtectedPayload(response)
		}
		if message.Kind == MessageHandoffOffer {
			handoff, found, err := readLifecycleHandoffView(ctx, sc, message.ID)
			if err != nil || !found {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"linked Handoff is unavailable before terminal preparation",
				)
			}
			prepared.handoffID = handoff.ID
			handoffSchema, _ := PayloadSlotHandoffTerminalReason.schema()
			reason, err := PrepareProtectedPayload(
				ctx, s.module.communicationSealer, PayloadSlotHandoffTerminalReason,
				protectedPayloadPolicyFrom(handoff.Payload),
				ContentAAD{
					TenantID: normalized.scope.TenantID, WorkspaceID: normalized.scope.WorkspaceID,
					ChannelID: message.ChannelID, EntityKind: handoffKind, EntityID: handoff.ID,
					Schema:               handoffSchema,
					ProtectionGeneration: handoff.Payload.ProtectionGeneration,
				},
				normalized.command.Reason,
			)
			if err != nil {
				return err
			}
			prepared.handoffReason = cloneProtectedPayload(reason)
		}
		_ = deliveries
		return nil
	})
	return prepared, err
}

func readMessageLifecycleCarrierView(
	ctx context.Context,
	sc store.Scope,
	messageID model.ID,
) (Message, []MessageDelivery, error) {
	messages, err := sc.Ext(messageKind)
	if err != nil {
		return Message{}, nil, err
	}
	record, err := messages.Get(ctx, messageID)
	if err != nil {
		return Message{}, nil, err
	}
	deliveriesRepo, err := sc.Ext(messageDeliveryKind)
	if err != nil {
		return Message{}, nil, err
	}
	rows, page, err := deliveriesRepo.List(ctx, model.Query{
		Filters: []model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()}},
		Sort:    []model.Sort{{Column: colCommDeliverySeq}}, Limit: messageLifecycleCarrierBound,
	})
	if err != nil || page.HasMore {
		return Message{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "Message lifecycle Delivery set is unavailable",
		)
	}
	deliveries := make([]MessageDelivery, 0, len(rows))
	required := int64(0)
	for _, row := range rows {
		delivery, decodeErr := messageDeliveryFromRecord(row)
		if decodeErr != nil || delivery.MessageID != messageID {
			return Message{}, nil, communicationError(
				ErrCommunicationEvidenceUnknown, "Message lifecycle Delivery set is malformed",
			)
		}
		deliveries = append(deliveries, delivery)
		if delivery.Required {
			required++
		}
	}
	message, err := messageFromRecord(record, required)
	if err != nil {
		return Message{}, nil, err
	}
	return message, deliveries, nil
}

func readLifecycleDecisionView(
	ctx context.Context,
	sc store.Scope,
	messageID model.ID,
) (DecisionRequest, bool, error) {
	repo, err := sc.Ext(decisionRequestKind)
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
	request, err := decisionRequestFromRecord(rows[0])
	return request, err == nil, err
}

func readLifecycleHandoffView(
	ctx context.Context,
	sc store.Scope,
	messageID model.ID,
) (Handoff, bool, error) {
	repo, err := sc.Ext(handoffKind)
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
	handoff, err := handoffFromRecord(rows[0])
	return handoff, err == nil, err
}

func equalMessageLifecyclePrepared(
	left messageLifecyclePrepared,
	right messageLifecyclePrepared,
) bool {
	return left.decisionID == right.decisionID && left.handoffID == right.handoffID &&
		bytes.Equal(left.messageHash, right.messageHash) &&
		canonicalCommunicationValueEqual(left.messageReason, right.messageReason) &&
		canonicalCommunicationValueEqual(left.decisionReason, right.decisionReason) &&
		canonicalCommunicationValueEqual(left.handoffReason, right.handoffReason)
}

func messageLifecycleStatus(action messageLifecycleAction) int {
	if action == messageLifecycleRetract || action == messageLifecycleExpire ||
		action == messageLifecycleOverdue {
		return http.StatusOK
	}
	return http.StatusInternalServerError
}

func normalizeMessageLifecycleError(err error) error {
	if err == nil {
		return nil
	}
	if err == store.ErrNotFound {
		return communicationError(ErrCommunicationNotFound, "Message lifecycle target is unavailable")
	}
	return fmt.Errorf("message lifecycle: %w", err)
}
