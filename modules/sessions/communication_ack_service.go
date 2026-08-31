// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	directNoticeAckMethod            = http.MethodPost
	directNoticeAckPathPrefix        = "/v1/m/sessions/deliveries/"
	directNoticeAckPathSuffix        = "/ack"
	directNoticeAckOperation         = "message.delivery.ack"
	directNoticeAckAuditAction       = "sessions.communication.message_delivery.ack"
	communicationMessageAcknowledged = "work.message.acknowledged"
	directNoticeAckApplyCommitmentV1 = int64(1)
	directNoticeAckReplayDomain      = "olivares.sessions.direct-notice-ack.replay-candidate.v1"
	directNoticeAckWorkflowDomain    = "olivares.sessions.workflow-work-task-ack.request.v1"
)

var (
	errDirectNoticeAckVersionRequired = errors.New(
		"sessions: direct notice Ack version_required",
	)
	errDirectNoticeAckVersionMismatch = fmt.Errorf(
		"%w: direct notice Ack version_mismatch", store.ErrConflict,
	)
	errDirectNoticeAckIdempotencyReused = fmt.Errorf(
		"%w: direct notice Ack idempotency_key_reused", store.ErrConflict,
	)
	errDirectNoticeAckAlreadyAcknowledged = fmt.Errorf(
		"%w: direct notice Ack already_acknowledged", store.ErrConflict,
	)
	errDirectNoticeAckReplayNeedsFreshAudit = errors.New(
		"sessions: direct notice Ack replay requires a fresh audit view",
	)
)

// DirectNoticeDeliveryAckCommand is the narrow service envelope for one
// user-owned DirectNotice delivery acknowledgement. Tenant, workspace, actor
// and carrier identity are server-authored arguments rather than body fields.
type DirectNoticeDeliveryAckCommand struct {
	IfMatch        string `json:"-"`
	IdempotencyKey string `json:"-"`
}

// DirectNoticeDeliveryAckResult is reconstructible from a durable command
// receipt. It deliberately carries no recipient or authorization evidence.
type DirectNoticeDeliveryAckResult struct {
	CommandID       model.ID              `json:"command_id"`
	AckID           model.ID              `json:"ack_id"`
	DeliveryID      model.ID              `json:"delivery_id"`
	MessageID       model.ID              `json:"message_id"`
	EventID         model.ID              `json:"event_id"`
	Version         int64                 `json:"version"`
	ETag            string                `json:"etag"`
	State           MessageDeliveryState  `json:"state"`
	Late            bool                  `json:"late"`
	Fulfillment     FulfillmentProjection `json:"fulfillment"`
	AuditSeq        int64                 `json:"audit_seq"`
	Replayed        bool                  `json:"-"`
	messageEventSeq int64
}

type directNoticeAckNormalizedCommand struct {
	command            DirectNoticeDeliveryAckCommand
	scope              DirectoryScopeRef
	principal          CommunicationPrincipal
	deliveryID         model.ID
	expectedVersion    int64
	method             string
	path               string
	commandScope       string
	actorFingerprint   []byte
	idempotencyKeyHash []byte
	requestDigest      []byte
	carrier            directNoticeAckCarrierSelector
}

type directNoticeAckCarrierClass string

const directNoticeAckCarrierWorkflowWorkTask directNoticeAckCarrierClass = "workflow_work_task"

// directNoticeAckCarrierSelector is private, server-authored input to the K3
// Ack transaction. Its zero value preserves the public DirectNotice carrier;
// the only alternate class is one exact workflow WorkTask tuple.
type directNoticeAckCarrierSelector struct {
	class      directNoticeAckCarrierClass
	workItemID model.ID
	channelID  model.ID
	userID     model.ID
}

func (selector directNoticeAckCarrierSelector) validate(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) error {
	if selector.class == "" {
		if !selector.workItemID.IsZero() || !selector.channelID.IsZero() || !selector.userID.IsZero() {
			return communicationError(
				ErrInvalidCommunicationModel, "DirectNotice Ack carrier selector is malformed",
			)
		}
		return nil
	}
	if selector.class != directNoticeAckCarrierWorkflowWorkTask ||
		!validCanonicalCommunicationID(selector.workItemID) ||
		!validCanonicalCommunicationID(selector.channelID) ||
		!validCanonicalCommunicationID(selector.userID) ||
		principal.UserID != selector.userID ||
		ValidateCommunicationPrincipalForScope(principal, scope) != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "workflow WorkTask Ack carrier selector is unavailable",
		)
	}
	return nil
}

func bindDirectNoticeAckCarrier(
	normalized directNoticeAckNormalizedCommand,
	selector directNoticeAckCarrierSelector,
) (directNoticeAckNormalizedCommand, error) {
	if err := selector.validate(normalized.scope, normalized.principal); err != nil {
		return directNoticeAckNormalizedCommand{}, err
	}
	normalized.carrier = selector
	if selector.class == "" {
		return normalized, nil
	}
	normalized.commandScope = fmt.Sprintf(
		"%s;workspace=%s;delivery=%s",
		workflowMessageAckCommandScope, normalized.scope.WorkspaceID, normalized.deliveryID,
	)
	if !validateOpaqueRef(normalized.commandScope) {
		return directNoticeAckNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel, "workflow WorkTask Ack command scope is invalid",
		)
	}
	carrier, err := canonicalJSON(struct {
		SchemaVersion int64                       `json:"schema_version"`
		Class         directNoticeAckCarrierClass `json:"class"`
		WorkItemID    model.ID                    `json:"work_item_id"`
		ChannelID     model.ID                    `json:"channel_id"`
		UserID        model.ID                    `json:"user_id"`
	}{
		SchemaVersion: 1, Class: selector.class, WorkItemID: selector.workItemID,
		ChannelID: selector.channelID, UserID: selector.userID,
	})
	if err != nil {
		return directNoticeAckNormalizedCommand{}, err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(directNoticeAckWorkflowDomain))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(normalized.requestDigest)
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(carrier)
	normalized.requestDigest = digest.Sum(nil)
	return normalized, nil
}

type directNoticeAckIDs struct {
	Ack     model.ID
	Command model.ID
	Event   model.ID
	Receipt model.ID
}

func newDirectNoticeAckIDs() directNoticeAckIDs {
	return directNoticeAckIDs{
		Ack: model.NewID(), Command: model.NewID(), Event: model.NewID(), Receipt: model.NewID(),
	}
}

type directNoticeAckPreflight struct {
	normalized         directNoticeAckNormalizedCommand
	identity           directNoticeReaderIdentityPreflight
	identityCommitment [sha256.Size]byte
	core               ReadWitness
	ids                directNoticeAckIDs
	bindingID          *communicationRequestAuthorityBindingID
}

type directNoticeAckReplayCandidate struct {
	receipt  CommunicationCommandReceipt
	result   DirectNoticeDeliveryAckResult
	found    bool
	conflict bool
	seal     [sha256.Size]byte
}

type directNoticeAckReplayProjection struct {
	Receipt         CommunicationCommandReceipt   `json:"receipt"`
	Result          DirectNoticeDeliveryAckResult `json:"result"`
	MessageEventSeq int64                         `json:"message_event_seq"`
	Replayed        bool                          `json:"replayed"`
	Found           bool                          `json:"found"`
	Conflict        bool                          `json:"conflict"`
}

func directNoticeAckReplayCandidateCommitment(
	candidate directNoticeAckReplayCandidate,
) ([sha256.Size]byte, error) {
	raw, err := canonicalJSON(directNoticeAckReplayProjection{
		Receipt: candidate.receipt, Result: candidate.result,
		MessageEventSeq: candidate.result.messageEventSeq,
		Replayed:        candidate.result.Replayed, Found: candidate.found, Conflict: candidate.conflict,
	})
	if err != nil {
		return [sha256.Size]byte{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack replay candidate cannot be committed",
		)
	}
	digest := sha256.Sum256(append(
		append([]byte(directNoticeAckReplayDomain), 0), raw...,
	))
	return digest, nil
}

func directNoticeAckReplayCandidateSealed(candidate directNoticeAckReplayCandidate) bool {
	commitment, err := directNoticeAckReplayCandidateCommitment(candidate)
	return err == nil && candidate.seal != ([sha256.Size]byte{}) && candidate.seal == commitment
}

// AcknowledgeDirectNoticeDelivery is the future handler-facing boundary. The
// product readiness conjunction deliberately remains OFF; the private seam
// below bypasses only that conjunction and retains exact request authority.
func (m *Module) AcknowledgeDirectNoticeDelivery(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	deliveryID model.ID,
	cmd DirectNoticeDeliveryAckCommand,
) (DirectNoticeDeliveryAckResult, error) {
	return m.acknowledgeDirectNoticeDeliveryWithCurrentAuthority(
		ctx, scope, ref, deliveryID, cmd, true,
	)
}

func (m *Module) acknowledgeDirectNoticeDeliveryWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	deliveryID model.ID,
	cmd DirectNoticeDeliveryAckCommand,
) (DirectNoticeDeliveryAckResult, error) {
	return m.acknowledgeDirectNoticeDeliveryWithCurrentAuthority(
		ctx, scope, ref, deliveryID, cmd, false,
	)
}

func (m *Module) acknowledgeDirectNoticeDeliveryWithCurrentAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	deliveryID model.ID,
	cmd DirectNoticeDeliveryAckCommand,
	requireReadiness bool,
) (DirectNoticeDeliveryAckResult, error) {
	binder := func(
		ctx context.Context,
		question communicationAuthorityQuestion,
	) (communicationRequestAuthority, error) {
		return m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	}
	return m.acknowledgeDirectNoticeDeliveryWithAuthorityBinder(
		ctx, scope, deliveryID, cmd, requireReadiness,
		directNoticeAckCarrierSelector{}, binder,
	)
}

type directNoticeAckAuthorityBinder func(
	context.Context,
	communicationAuthorityQuestion,
) (communicationRequestAuthority, error)

type directNoticeAckMutation func(
	*communicationTx,
	communicationRequestAuthorityContext,
	store.Scope,
) error

// mutateDirectNoticeAckWithCarrier preserves the narrower K3-only transaction
// for public DirectNotice Ack. The workflow-only selector additionally exposes
// the already confined Scope so the same atomic mutation can CAS its owning K1
// WorkItem event sequence; it does not add WorkItem to K3's repository
// inventory.
func (m *Module) mutateDirectNoticeAckWithCarrier(
	ctx context.Context,
	question communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	window communicationAuthorityWindow,
	normalized directNoticeAckNormalizedCommand,
	fn directNoticeAckMutation,
) error {
	if fn == nil {
		return communicationTransactionUnavailable("delivery Ack mutation callback", nil)
	}
	if normalized.carrier.class == "" {
		return m.mutateCommunicationWithNarrowedAuthority(
			ctx, question, bound, CommunicationClaimAuthoritySnapshot{}, window,
			func(tx *communicationTx, consumed communicationRequestAuthorityContext) error {
				return fn(tx, consumed, nil)
			},
		)
	}
	if normalized.carrier.class != directNoticeAckCarrierWorkflowWorkTask {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "delivery Ack carrier mutation is unavailable",
		)
	}
	if err := window.validate(); err != nil {
		return err
	}
	request, consumed, err := bound.transactionSnapshot(
		question, CommunicationClaimAuthoritySnapshot{},
	)
	if err != nil {
		return err
	}
	request, err = request.narrowTo(window)
	if err != nil {
		return err
	}
	scope := normalized.scope
	var attempted atomic.Bool
	return m.communicationData(scope.TenantID).Mutate(ctx, func(sc store.Scope) error {
		if !attempted.CompareAndSwap(false, true) {
			return communicationTransactionUnavailable(
				"workflow WorkTask Ack mutation callback was already entered", nil,
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
		if err := fn(tx, consumed, confined); err != nil {
			return err
		}
		return tx.finalizeAuthority(ctx)
	})
}

// acknowledgeDirectNoticeDeliveryWithAuthorityBinder keeps the complete K3
// Ack transaction behind one exact authority-binding seam. Public requests
// bind an opaque credential reference; internal workflow/protocol commands bind
// an operator-selected durable User through the same C5 operation witness.
func (m *Module) acknowledgeDirectNoticeDeliveryWithAuthorityBinder(
	ctx context.Context,
	scope DirectoryScopeRef,
	deliveryID model.ID,
	cmd DirectNoticeDeliveryAckCommand,
	requireReadiness bool,
	carrier directNoticeAckCarrierSelector,
	binder directNoticeAckAuthorityBinder,
) (DirectNoticeDeliveryAckResult, error) {
	if !validCanonicalCommunicationID(deliveryID) {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrInvalidCommunicationModel, "invalid DirectNotice delivery Ack target",
		)
	}
	question, err := newCommunicationAuthorityQuestion(
		scope, messageDeliveryKind, deliveryID, CommunicationDeliveryWrite,
	)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	if binder == nil {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery-write authority binder is unavailable",
		)
	}
	bound, err := binder(ctx, question)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"delivery-write authority context crossed its exact request",
		)
	}
	if err := requireDirectNoticeUserBackedPrincipal(inspected); err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	normalized, err := normalizeDirectNoticeDeliveryAckCommand(
		scope, inspected.principal, deliveryID, cmd,
	)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	normalized, err = bindDirectNoticeAckCarrier(normalized, carrier)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return DirectNoticeDeliveryAckResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(
		ctx, scope, inspected.principal, nil,
	)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, normalizeDirectNoticeAckError(err)
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	identityCommitment, err := directNoticeAckReaderIdentityCommitment(identity, normalized)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, err
	}
	candidate, err := m.lookupDirectNoticeAckReplay(ctx, normalized)
	if err != nil {
		return DirectNoticeDeliveryAckResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack replay candidate is unavailable",
		)
	}
	if candidate.found {
		if err := m.confirmDirectNoticeAckReplayWithAuthority(
			ctx, question, bound, inspected, identity, window, normalized, candidate,
		); err != nil {
			return DirectNoticeDeliveryAckResult{}, normalizeDirectNoticeAckError(err)
		}
		if candidate.conflict {
			return DirectNoticeDeliveryAckResult{}, errDirectNoticeAckIdempotencyReused
		}
		candidate.result.Replayed = true
		return candidate.result, nil
	}

	preflight := directNoticeAckPreflight{
		normalized: normalized, identity: identity,
		identityCommitment: identityCommitment, ids: newDirectNoticeAckIDs(),
	}
	var result DirectNoticeDeliveryAckResult
	var observedOutcome error
	mutateErr := m.mutateDirectNoticeAckWithCarrier(
		ctx, question, bound, window, normalized,
		func(
			tx *communicationTx,
			consumed communicationRequestAuthorityContext,
			workflowScope store.Scope,
		) error {
			if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
				return err
			}
			if consumed.question != question || consumed.question.entity != (EntityRef{
				TenantID: scope.TenantID, Kind: messageDeliveryKind, ID: deliveryID,
				WorkspaceID: scope.WorkspaceID,
			}) || consumed.question.operation != CommunicationDeliveryWrite ||
				consumed.principal != normalized.principal {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"delivery-write authority crossed DirectNotice Ack preflight",
				)
			}
			if consumed.bindingID == nil || consumed.bindingID != tx.requestBindingID {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"DirectNotice Ack transaction crossed its request binding",
				)
			}
			readerPreflight, err := directNoticeReaderPreflightWithCore(identity, consumed.witness)
			if err != nil {
				return err
			}
			boundPreflight := preflight
			boundPreflight.bindingID = consumed.bindingID
			boundPreflight.core = cloneCommunicationRequestAuthorityWitness(consumed.witness)
			if err := tx.validateAuthorityFreshness(tx.now); err != nil {
				return err
			}
			authorityLock, err := lockDirectNoticeAckAuthoritySnapshot(
				ctx, tx, boundPreflight, readerPreflight,
			)
			if err != nil {
				return err
			}
			if err := tx.lockTransaction(
				ctx, directNoticeAckIdempotencyLockKey(normalized),
			); err != nil {
				return err
			}
			receipt, found, err := findDirectNoticeAckReceipt(
				ctx,
				func(kind model.Kind) (communicationReadRepository, error) {
					return tx.repo(kind)
				},
				normalized,
			)
			if err != nil {
				return err
			}
			if found {
				_ = receipt
				return errDirectNoticeAckReplayNeedsFreshAudit
			}
			result, err = applyDirectNoticeDeliveryAck(
				ctx, tx, workflowScope, boundPreflight, readerPreflight, authorityLock,
			)
			if directNoticeAckObservableOutcome(err) {
				observedOutcome = err
				return nil
			}
			return err
		},
	)
	if mutateErr != nil {
		if errors.Is(mutateErr, store.ErrConflict) ||
			errors.Is(mutateErr, errDirectNoticeAckReplayNeedsFreshAudit) {
			replay, found, replayErr := m.lookupDirectNoticeAckReplayAfterAuthorityRaceWithBinder(
				ctx, scope, question, normalized, binder,
			)
			if replayErr != nil {
				return DirectNoticeDeliveryAckResult{}, normalizeDirectNoticeAckError(replayErr)
			}
			if found {
				if replay.conflict {
					return DirectNoticeDeliveryAckResult{}, errDirectNoticeAckIdempotencyReused
				}
				replay.result.Replayed = true
				return replay.result, nil
			}
		}
		return DirectNoticeDeliveryAckResult{}, normalizeDirectNoticeAckError(mutateErr)
	}
	if observedOutcome != nil {
		return DirectNoticeDeliveryAckResult{}, normalizeDirectNoticeAckError(observedOutcome)
	}
	return result, nil
}

func normalizeDirectNoticeDeliveryAckCommand(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	deliveryID model.ID,
	cmd DirectNoticeDeliveryAckCommand,
) (directNoticeAckNormalizedCommand, error) {
	if err := scope.Validate(); err != nil {
		return directNoticeAckNormalizedCommand{}, err
	}
	if err := ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return directNoticeAckNormalizedCommand{}, err
	}
	if principal.UserID == "" || principal.AgentExternalID != "" || principal.SessionID != "" ||
		principal.SessionRunRef != "" || principal.SessionFence != 0 ||
		principal.SessionWorkspaceID != "" || principal.PurposeRestricted || principal.System ||
		principal.SystemActorRef != "" || principal.SystemGrantAgentID != "" ||
		!validCanonicalCommunicationID(deliveryID) {
		return directNoticeAckNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel,
			"DirectNotice Ack requires a claim-free authenticated User and exact Delivery",
		)
	}
	expectedVersion, err := parseDirectNoticeAckETag(cmd.IfMatch)
	if err != nil {
		return directNoticeAckNormalizedCommand{}, err
	}
	idempotencyID, err := model.ParseID(cmd.IdempotencyKey)
	if err != nil || !validCanonicalCommunicationID(idempotencyID) ||
		idempotencyID.String() != cmd.IdempotencyKey {
		return directNoticeAckNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel, "DirectNotice Ack idempotency key is invalid",
		)
	}
	path := directNoticeAckPathPrefix + deliveryID.String() + directNoticeAckPathSuffix
	commandScope := fmt.Sprintf(
		"%s %s;workspace=%s", directNoticeAckMethod, path, scope.WorkspaceID,
	)
	if !validateOpaqueRef(commandScope) {
		return directNoticeAckNormalizedCommand{}, communicationError(
			ErrInvalidCommunicationModel, "DirectNotice Ack command scope is invalid",
		)
	}
	actorRaw, err := canonicalJSON(CommunicationActorRef{
		Kind: ActorUser, Ref: principal.UserID.String(),
	})
	if err != nil {
		return directNoticeAckNormalizedCommand{}, err
	}
	actorFingerprint := sha256.Sum256(actorRaw)
	idempotencyKeyHash := sha256.Sum256([]byte(cmd.IdempotencyKey))
	requestRaw, err := canonicalJSON(struct {
		Operation  string   `json:"operation"`
		Method     string   `json:"method"`
		Path       string   `json:"path"`
		DeliveryID model.ID `json:"delivery_id"`
		IfMatch    string   `json:"if_match"`
	}{
		Operation: directNoticeAckOperation, Method: directNoticeAckMethod,
		Path: path, DeliveryID: deliveryID, IfMatch: cmd.IfMatch,
	})
	if err != nil {
		return directNoticeAckNormalizedCommand{}, err
	}
	requestDigest := sha256.Sum256(requestRaw)
	return directNoticeAckNormalizedCommand{
		command: cmd, scope: scope, principal: principal, deliveryID: deliveryID,
		expectedVersion: expectedVersion, method: directNoticeAckMethod, path: path,
		commandScope:       commandScope,
		actorFingerprint:   actorFingerprint[:],
		idempotencyKeyHash: idempotencyKeyHash[:], requestDigest: requestDigest[:],
	}, nil
}

func parseDirectNoticeAckETag(value string) (int64, error) {
	if value == "" {
		return 0, errDirectNoticeAckVersionRequired
	}
	if len(value) < 4 || value[:2] != "\"v" || value[len(value)-1] != '"' {
		return 0, communicationError(
			ErrInvalidCommunicationModel,
			"DirectNotice Ack If-Match is not a strong version tag",
		)
	}
	version, err := strconv.ParseInt(value[2:len(value)-1], 10, 64)
	if err != nil || version < 1 || value != fmt.Sprintf("\"v%d\"", version) {
		return 0, communicationError(
			ErrInvalidCommunicationModel, "DirectNotice Ack If-Match is not canonical",
		)
	}
	return version, nil
}

func directNoticeAckIdempotencyLockKey(normalized directNoticeAckNormalizedCommand) string {
	digest := sha256.Sum256(bytes.Join([][]byte{
		[]byte(normalized.scope.TenantID), normalized.actorFingerprint,
		[]byte(normalized.commandScope), normalized.idempotencyKeyHash,
	}, []byte{0}))
	return "sessions:communication:ack:idem:" +
		base64.RawURLEncoding.EncodeToString(digest[:])
}

func (m *Module) lookupDirectNoticeAckReplay(
	ctx context.Context,
	normalized directNoticeAckNormalizedCommand,
) (directNoticeAckReplayCandidate, error) {
	var candidate directNoticeAckReplayCandidate
	err := m.viewCommunication(ctx, normalized.scope, func(sc store.Scope) error {
		resolve := func(kind model.Kind) (communicationReadRepository, error) {
			return sc.Ext(kind)
		}
		receipt, found, err := findDirectNoticeAckReceipt(ctx, resolve, normalized)
		if err != nil || !found {
			return err
		}
		candidate.receipt = receipt
		candidate.found = true
		candidate.conflict = !bytes.Equal(receipt.RequestDigest, normalized.requestDigest)
		candidate.result, err = directNoticeAckResultFromReceipt(
			ctx, resolve, normalized, receipt,
		)
		if err != nil {
			return err
		}
		reader, ok := sc.Audit().(store.VerifiedAuditAnchorReader)
		if !ok {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"DirectNotice Ack verified audit reader is unavailable",
			)
		}
		if err := verifyDirectNoticeAckAuditAnchor(
			ctx, reader, normalized, receipt, candidate.result,
		); err != nil {
			return err
		}
		candidate.seal, err = directNoticeAckReplayCandidateCommitment(candidate)
		return err
	})
	return candidate, err
}

func findDirectNoticeAckReceipt(
	ctx context.Context,
	resolve communicationReadRepositoryResolver,
	normalized directNoticeAckNormalizedCommand,
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
	if len(rows) == 0 {
		if page.HasMore {
			return CommunicationCommandReceipt{}, false, communicationError(
				ErrCommunicationEvidenceUnknown,
				"DirectNotice Ack receipt lookup is incomplete",
			)
		}
		return CommunicationCommandReceipt{}, false, nil
	}
	if len(rows) != 1 || page.HasMore {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack receipt uniqueness is unavailable",
		)
	}
	receipt, err := communicationCommandReceiptFromRecord(rows[0])
	if err != nil {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack receipt cannot be decoded",
		)
	}
	if receipt.TenantID != normalized.scope.TenantID ||
		receipt.WorkspaceID != normalized.scope.WorkspaceID ||
		receipt.CommandScope != normalized.commandScope ||
		!bytes.Equal(receipt.ActorFingerprint, normalized.actorFingerprint) ||
		!bytes.Equal(receipt.IdempotencyKeyHash, normalized.idempotencyKeyHash) {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack receipt crosses command scope",
		)
	}
	return receipt, true, nil
}

func (m *Module) confirmDirectNoticeAckReplayWithAuthority(
	ctx context.Context,
	question communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	inspected communicationRequestAuthorityInspection,
	identity directNoticeReaderIdentityPreflight,
	window communicationAuthorityWindow,
	normalized directNoticeAckNormalizedCommand,
	candidate directNoticeAckReplayCandidate,
) error {
	if !candidate.found || !validCanonicalCommunicationID(candidate.receipt.ID) ||
		!directNoticeAckReplayCandidateSealed(candidate) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack replay receipt is unavailable",
		)
	}
	identityCommitment, err := directNoticeAckReaderIdentityCommitment(identity, normalized)
	if err != nil {
		return err
	}
	var observedOutcome error
	mutateErr := m.mutateDirectNoticeAckWithCarrier(
		ctx, question, bound, window, normalized,
		func(
			tx *communicationTx,
			consumed communicationRequestAuthorityContext,
			workflowScope store.Scope,
		) error {
			if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
				return err
			}
			if consumed.question != question || consumed.question.entity != (EntityRef{
				TenantID: normalized.scope.TenantID, Kind: messageDeliveryKind,
				ID: normalized.deliveryID, WorkspaceID: normalized.scope.WorkspaceID,
			}) || consumed.question.operation != CommunicationDeliveryWrite ||
				consumed.principal != normalized.principal {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"delivery-write authority crossed DirectNotice Ack replay",
				)
			}
			if consumed.bindingID == nil || consumed.bindingID != tx.requestBindingID {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"DirectNotice Ack replay crossed its request binding",
				)
			}
			readerPreflight, err := directNoticeReaderPreflightWithCore(identity, consumed.witness)
			if err != nil {
				return err
			}
			replayPreflight := directNoticeAckPreflight{
				normalized:         normalized,
				identity:           identity,
				identityCommitment: identityCommitment,
				core:               cloneCommunicationRequestAuthorityWitness(consumed.witness),
				ids: directNoticeAckIDs{
					Ack: candidate.result.AckID, Command: candidate.result.CommandID,
					Event: candidate.result.EventID, Receipt: candidate.receipt.ID,
				},
				bindingID: consumed.bindingID,
			}
			if err := tx.validateAuthorityFreshness(tx.now); err != nil {
				return err
			}
			authorityLock, err := lockDirectNoticeAckAuthoritySnapshot(
				ctx, tx, replayPreflight, readerPreflight,
			)
			if err != nil {
				return err
			}
			if err := tx.lockTransaction(
				ctx, directNoticeAckIdempotencyLockKey(normalized),
			); err != nil {
				return err
			}
			current, found, err := findDirectNoticeAckReceipt(ctx,
				func(kind model.Kind) (communicationReadRepository, error) {
					return tx.repo(kind)
				}, normalized)
			if err != nil || !found ||
				candidate.conflict != !bytes.Equal(current.RequestDigest, normalized.requestDigest) ||
				!canonicalCommunicationValueEqual(current, candidate.receipt) {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"DirectNotice Ack receipt changed before authority confirmation",
				)
			}
			confirmErr := confirmDirectNoticeAckReplayLockedWithScope(
				ctx, tx, workflowScope, replayPreflight, readerPreflight, current, candidate.result,
				authorityLock,
			)
			if directNoticeAckObservableOutcome(confirmErr) {
				observedOutcome = confirmErr
				return nil
			}
			return confirmErr
		},
	)
	if mutateErr != nil {
		return mutateErr
	}
	return observedOutcome
}

func (m *Module) lookupDirectNoticeAckReplayAfterAuthorityRace(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	question communicationAuthorityQuestion,
	normalized directNoticeAckNormalizedCommand,
) (directNoticeAckReplayCandidate, bool, error) {
	binder := func(
		ctx context.Context,
		question communicationAuthorityQuestion,
	) (communicationRequestAuthority, error) {
		return m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	}
	return m.lookupDirectNoticeAckReplayAfterAuthorityRaceWithBinder(
		ctx, scope, question, normalized, binder,
	)
}

func (m *Module) lookupDirectNoticeAckReplayAfterAuthorityRaceWithBinder(
	ctx context.Context,
	scope DirectoryScopeRef,
	question communicationAuthorityQuestion,
	normalized directNoticeAckNormalizedCommand,
	binder directNoticeAckAuthorityBinder,
) (directNoticeAckReplayCandidate, bool, error) {
	if binder == nil {
		return directNoticeAckReplayCandidate{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "DirectNotice Ack race authority binder is unavailable",
		)
	}
	bound, err := binder(ctx, question)
	if err != nil {
		return directNoticeAckReplayCandidate{}, false, err
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question || inspected.principal != normalized.principal {
		return directNoticeAckReplayCandidate{}, false, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack race authority changed identity",
		)
	}
	if err := requireDirectNoticeUserBackedPrincipal(inspected); err != nil {
		return directNoticeAckReplayCandidate{}, false, err
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(ctx, scope, inspected.principal, nil)
	if err != nil {
		return directNoticeAckReplayCandidate{}, false, err
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil {
		return directNoticeAckReplayCandidate{}, false, err
	}
	candidate, err := m.lookupDirectNoticeAckReplay(ctx, normalized)
	if err != nil {
		return directNoticeAckReplayCandidate{}, false, communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack race replay is unavailable",
		)
	}
	if !candidate.found {
		return directNoticeAckReplayCandidate{}, false, nil
	}
	if err := m.confirmDirectNoticeAckReplayWithAuthority(
		ctx, question, bound, inspected, identity, window, normalized, candidate,
	); err != nil {
		return directNoticeAckReplayCandidate{}, false, err
	}
	return candidate, true, nil
}

func normalizeDirectNoticeAckError(err error) error {
	if errors.Is(err, errDirectNoticeAckReplayNeedsFreshAudit) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack replay receipt is unavailable in the outer audit view",
		)
	}
	if errors.Is(err, ErrCommunicationEvidenceUnknown) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack authority or durable evidence is unavailable",
		)
	}
	if errors.Is(err, errDirectNoticeAckVersionRequired) ||
		errors.Is(err, errDirectNoticeAckVersionMismatch) ||
		errors.Is(err, errDirectNoticeAckIdempotencyReused) ||
		errors.Is(err, errDirectNoticeAckAlreadyAcknowledged) ||
		errors.Is(err, ErrCommunicationForbidden) ||
		errors.Is(err, ErrCommunicationNotFound) ||
		errors.Is(err, ErrCommunicationTerminal) {
		return err
	}
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"DirectNotice Ack state changed while locking",
		)
	}
	return err
}

func directNoticeAckObservableOutcome(err error) bool {
	return err != nil && !errors.Is(err, ErrCommunicationEvidenceUnknown) &&
		(errors.Is(err, errDirectNoticeAckVersionMismatch) ||
			errors.Is(err, errDirectNoticeAckAlreadyAcknowledged) ||
			errors.Is(err, ErrCommunicationNotFound) ||
			errors.Is(err, ErrCommunicationForbidden) ||
			errors.Is(err, ErrCommunicationTerminal))
}
