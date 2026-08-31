// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	handoffOfferMethod          = http.MethodPost
	handoffOfferOperation       = "handoff.offer"
	handoffOfferAuditAction     = "sessions.communication.handoff.offer"
	handoffOfferEventType       = "work.handoff.offered"
	handoffResponseMethod       = http.MethodPost
	handoffResponseOperation    = "handoff.respond"
	handoffResponseAuditAction  = "sessions.communication.handoff.respond"
	handoffAcceptedEventType    = "work.handoff.accepted"
	handoffRejectedEventType    = "work.handoff.rejected"
	handoffCancelMethod         = http.MethodPost
	handoffCancelOperation      = "handoff.cancel"
	handoffCancelAuditAction    = "sessions.communication.handoff.cancel"
	handoffWithdrawnEventType   = "work.handoff.withdrawn"
	handoffDeadlineOperation    = "handoff.expire"
	handoffDeadlineAuditAction  = "sessions.communication.handoff.expire"
	handoffExpiredEventType     = "work.handoff.expired"
	handoffWithdrawnCode        = "handoff_withdrawn"
	handoffDeadlineElapsedCode  = "ack_deadline_elapsed"
	handoffLeaseEndReason       = "handoff_accepted"
	handoffMaxReceiptLookupRows = 2
)

var (
	errHandoffVersionRequired   = errors.New("sessions: Handoff version_required")
	errHandoffVersionMismatch   = fmt.Errorf("%w: Handoff version_mismatch", store.ErrConflict)
	errHandoffIdempotencyReused = fmt.Errorf(
		"%w: Handoff idempotency_key_reused", store.ErrConflict,
	)
	errHandoffStaleOffer = fmt.Errorf("%w: Handoff stale_offer", store.ErrConflict)
)

// HandoffOfferCommand materializes the Handoff aggregate for an already
// published handoff_offer carrier. Message and Delivery are server-created K3
// rows; this command cannot replace their sender, target or WorkItem lineage.
type HandoffOfferCommand struct {
	ChannelID      model.ID       `json:"channel_id"`
	WorkItemID     model.ID       `json:"work_item_id"`
	MessageID      model.ID       `json:"message_id"`
	DeliveryID     model.ID       `json:"delivery_id"`
	Content        HandoffContent `json:"handoff"`
	IfMatch        string         `json:"-"`
	IdempotencyKey string         `json:"-"`
}

// HandoffOfferResult is content-free and safe to retain in a command receipt.
type HandoffOfferResult struct {
	CommandID  model.ID     `json:"command_id"`
	HandoffID  model.ID     `json:"handoff_id"`
	MessageID  model.ID     `json:"message_id"`
	DeliveryID model.ID     `json:"delivery_id"`
	WorkItemID model.ID     `json:"work_item_id"`
	EventID    model.ID     `json:"event_id"`
	Version    int64        `json:"version"`
	ETag       string       `json:"etag"`
	State      HandoffState `json:"state"`
	AuditSeq   int64        `json:"audit_seq"`
	Replayed   bool         `json:"-"`
}

// HandoffResponseCommand is the target-authored accept/reject transition.
// Withdrawal and expiry use separate owner and worker seams, so their local
// actor constraints cannot be confused with target response authority.
type HandoffResponseCommand struct {
	Transition     HandoffTransition           `json:"transition"`
	Reason         *CommunicationReasonContent `json:"reason,omitempty"`
	IfMatch        string                      `json:"-"`
	IdempotencyKey string                      `json:"-"`
}

// HandoffResponseResult is the durable receipt projection for accept/reject.
type HandoffResponseResult struct {
	CommandID           model.ID     `json:"command_id"`
	HandoffID           model.ID     `json:"handoff_id"`
	AckID               model.ID     `json:"ack_id,omitempty"`
	MessageID           model.ID     `json:"message_id"`
	DeliveryID          model.ID     `json:"delivery_id"`
	WorkItemID          model.ID     `json:"work_item_id"`
	EventID             model.ID     `json:"event_id"`
	Version             int64        `json:"version"`
	ETag                string       `json:"etag"`
	State               HandoffState `json:"state"`
	OwnerEpoch          int64        `json:"owner_epoch"`
	ResultingLeaseFence int64        `json:"resulting_lease_fence,omitempty"`
	AuditSeq            int64        `json:"audit_seq"`
	Replayed            bool         `json:"-"`
}

// HandoffCancelCommand withdraws an offered Handoff on behalf of its current
// source owner. The reason is protected using the carrier's content policy.
type HandoffCancelCommand struct {
	Reason         CommunicationReasonContent `json:"reason"`
	IfMatch        string                     `json:"-"`
	IdempotencyKey string                     `json:"-"`
}

// HandoffLifecycleResult is the content-free durable projection shared by
// owner withdrawal and deadline expiration. Neither transition changes Work
// ownership, lease state, Delivery state or Ack state.
type HandoffLifecycleResult struct {
	CommandID  model.ID     `json:"command_id"`
	HandoffID  model.ID     `json:"handoff_id"`
	MessageID  model.ID     `json:"message_id"`
	DeliveryID model.ID     `json:"delivery_id"`
	WorkItemID model.ID     `json:"work_item_id"`
	EventID    model.ID     `json:"event_id"`
	Version    int64        `json:"version"`
	ETag       string       `json:"etag"`
	State      HandoffState `json:"state"`
	AuditSeq   int64        `json:"audit_seq"`
	Replayed   bool         `json:"-"`
}

// handoffDeadlineAuthorizer is the narrow future reaper-composition port. It
// answers only the exact Handoff deadline question and returns current,
// versioned facts. The worker authority never comes from a wire command.
type handoffDeadlineAuthorizer interface {
	AuthorizeHandoffDeadline(
		context.Context,
		DirectoryScopeRef,
		model.ID,
	) (handoffDeadlineAuthority, error)
}

type handoffDeadlineAuthority struct {
	Actor      CommunicationActorRef
	Facts      []store.AuthorizationFactRef
	ObservedAt time.Time
	FreshUntil time.Time
	Evidence   AuthorityEvidence
}

type handoffDeadlineService struct {
	module     *Module
	authorizer handoffDeadlineAuthorizer
	newID      func() model.ID
}

// handoffDeadlineCommand is private because the deadline reaper is not wired
// while the K3 readiness conjunction is OFF.
type handoffDeadlineCommand struct {
	HandoffID       model.ID
	ExpectedVersion int64
	IdempotencyKey  string
}

type handoffCommandIdentity struct {
	scope              DirectoryScopeRef
	principal          CommunicationPrincipal
	actor              CommunicationActorRef
	actorFingerprint   []byte
	idempotencyKeyHash []byte
	requestDigest      []byte
	commandScope       string
	expectedVersion    int64
}

type handoffOfferNormalized struct {
	handoffCommandIdentity
	command HandoffOfferCommand
}

type handoffResponseNormalized struct {
	handoffCommandIdentity
	handoffID model.ID
	command   HandoffResponseCommand
}

type handoffCancelNormalized struct {
	handoffCommandIdentity
	handoffID model.ID
	command   HandoffCancelCommand
}

type handoffDeadlineNormalized struct {
	handoffCommandIdentity
	command   handoffDeadlineCommand
	authority handoffDeadlineAuthority
}

type handoffOfferIDs struct {
	Handoff model.ID
	Command model.ID
	Receipt model.ID
	Event   model.ID
}

type handoffResponseIDs struct {
	Ack     model.ID
	Command model.ID
	Receipt model.ID
	Event   model.ID
}

type handoffLifecycleIDs struct {
	Command model.ID
	Receipt model.ID
	Event   model.ID
}

type handoffOfferPrepared struct {
	message  Message
	delivery MessageDelivery
	channel  Channel
	payload  ProtectedPayload
}

type handoffResponsePrepared struct {
	terminalReason *ProtectedPayload
}

func newHandoffOfferIDs() handoffOfferIDs {
	return handoffOfferIDs{
		Handoff: model.NewID(), Command: model.NewID(), Receipt: model.NewID(), Event: model.NewID(),
	}
}

func newHandoffResponseIDs() handoffResponseIDs {
	return handoffResponseIDs{
		Ack: model.NewID(), Command: model.NewID(), Receipt: model.NewID(), Event: model.NewID(),
	}
}

func newHandoffLifecycleIDs(newID func() model.ID) (handoffLifecycleIDs, error) {
	if newID == nil {
		newID = model.NewID
	}
	ids := handoffLifecycleIDs{Command: newID(), Receipt: newID(), Event: newID()}
	all := [...]model.ID{ids.Command, ids.Receipt, ids.Event}
	for index, id := range all {
		if !validCanonicalCommunicationID(id) {
			return handoffLifecycleIDs{}, communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff lifecycle identities are unavailable",
			)
		}
		for prior := range index {
			if all[prior] == id {
				return handoffLifecycleIDs{}, communicationError(
					ErrCommunicationEvidenceUnknown, "Handoff lifecycle identities are not unique",
				)
			}
		}
	}
	return ids, nil
}

// OfferHandoff is the future handler-facing boundary. The readiness
// conjunction intentionally keeps K3 public traffic OFF until composition is
// complete.
func (m *Module) OfferHandoff(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd HandoffOfferCommand,
) (HandoffOfferResult, error) {
	return m.offerHandoffWithCurrentAuthority(ctx, scope, ref, cmd, true)
}

// offerHandoffWithAuthority bypasses only aggregate readiness for focused
// integration tests. Exact Core authority and every local condition remain.
func (m *Module) offerHandoffWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd HandoffOfferCommand,
) (HandoffOfferResult, error) {
	return m.offerHandoffWithCurrentAuthority(ctx, scope, ref, cmd, false)
}

func (m *Module) offerHandoffWithCurrentAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd HandoffOfferCommand,
	requireReadiness bool,
) (HandoffOfferResult, error) {
	question, bound, inspected, identity, normalized, window, err :=
		m.prepareHandoffOfferAuthority(ctx, scope, ref, cmd)
	if err != nil {
		return HandoffOfferResult{}, err
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return HandoffOfferResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	ids := newHandoffOfferIDs()
	prepared, err := m.prepareHandoffOfferPayload(ctx, normalized, ids.Handoff)
	if err != nil {
		return HandoffOfferResult{}, err
	}
	return m.applyHandoffOffer(
		ctx, question, bound, inspected, identity, window, normalized, ids, prepared,
	)
}

// RespondHandoff is the future target response boundary. K3 remains OFF until
// its complete readiness conjunction becomes effective.
func (m *Module) RespondHandoff(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	handoffID model.ID,
	cmd HandoffResponseCommand,
) (HandoffResponseResult, error) {
	return m.respondHandoffWithCurrentAuthority(ctx, scope, ref, handoffID, cmd, true)
}

func (m *Module) respondHandoffWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	handoffID model.ID,
	cmd HandoffResponseCommand,
) (HandoffResponseResult, error) {
	return m.respondHandoffWithCurrentAuthority(ctx, scope, ref, handoffID, cmd, false)
}

func (m *Module) respondHandoffWithCurrentAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	handoffID model.ID,
	cmd HandoffResponseCommand,
	requireReadiness bool,
) (HandoffResponseResult, error) {
	question, bound, inspected, identity, normalized, window, err :=
		m.prepareHandoffResponseAuthority(ctx, scope, ref, handoffID, cmd)
	if err != nil {
		return HandoffResponseResult{}, err
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return HandoffResponseResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	prepared, err := m.prepareHandoffResponseContent(ctx, normalized)
	if err != nil {
		return HandoffResponseResult{}, err
	}
	return m.applyHandoffResponse(
		ctx, question, bound, inspected, identity, window, normalized,
		newHandoffResponseIDs(), prepared,
	)
}

// CancelHandoff is the future owner-withdrawal boundary. It remains deny-closed
// behind the same K3 readiness conjunction as offer and response.
func (m *Module) CancelHandoff(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	handoffID model.ID,
	cmd HandoffCancelCommand,
) (HandoffLifecycleResult, error) {
	return m.cancelHandoffWithCurrentAuthority(ctx, scope, ref, handoffID, cmd, true)
}

func (m *Module) cancelHandoffWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	handoffID model.ID,
	cmd HandoffCancelCommand,
) (HandoffLifecycleResult, error) {
	return m.cancelHandoffWithCurrentAuthority(ctx, scope, ref, handoffID, cmd, false)
}

func (m *Module) cancelHandoffWithCurrentAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	handoffID model.ID,
	cmd HandoffCancelCommand,
	requireReadiness bool,
) (HandoffLifecycleResult, error) {
	question, bound, inspected, identity, normalized, window, err :=
		m.prepareHandoffCancelAuthority(ctx, scope, ref, handoffID, cmd)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return HandoffLifecycleResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	prepared, err := m.prepareHandoffTerminalContent(
		ctx, normalized.scope, normalized.handoffID, normalized.command.Reason,
	)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	ids, err := newHandoffLifecycleIDs(nil)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	return m.applyHandoffCancel(
		ctx, question, bound, inspected, identity, window, normalized, ids, prepared,
	)
}

// newHandoffDeadlineService is intentionally private and unwired. The reaper
// can be composed later without opening public K3 traffic prematurely.
func newHandoffDeadlineService(
	module *Module,
	authorizer handoffDeadlineAuthorizer,
	newID func() model.ID,
) (*handoffDeadlineService, error) {
	if module == nil || authorizer == nil {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff deadline ports are unavailable",
		)
	}
	if newID == nil {
		newID = model.NewID
	}
	return &handoffDeadlineService{module: module, authorizer: authorizer, newID: newID}, nil
}

// Expire closes one offered Handoff at or after its acknowledgement deadline.
// It is a private worker seam and therefore cannot bypass K3's public OFF gate.
func (s *handoffDeadlineService) Expire(
	ctx context.Context,
	scope DirectoryScopeRef,
	cmd handoffDeadlineCommand,
) (HandoffLifecycleResult, error) {
	if s == nil || s.module == nil || s.authorizer == nil || ctx == nil {
		return HandoffLifecycleResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff deadline service is unavailable",
		)
	}
	authority, err := s.authorizer.AuthorizeHandoffDeadline(ctx, scope, cmd.HandoffID)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	normalized, err := normalizeHandoffDeadlineCommand(scope, authority, cmd)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	prepared, err := s.module.prepareHandoffTerminalContent(
		ctx, scope, cmd.HandoffID,
		CommunicationReasonContent{
			Code: handoffDeadlineElapsedCode,
			Text: "Handoff acknowledgement deadline elapsed",
		},
	)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	ids, err := newHandoffLifecycleIDs(s.newID)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	return s.module.applyHandoffDeadline(ctx, normalized, ids, prepared)
}

func (m *Module) prepareHandoffOfferAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd HandoffOfferCommand,
) (
	communicationAuthorityQuestion,
	communicationRequestAuthority,
	communicationRequestAuthorityInspection,
	directNoticeReaderIdentityPreflight,
	handoffOfferNormalized,
	communicationAuthorityWindow,
	error,
) {
	question, err := newCommunicationAuthorityQuestion(
		scope, channelKind, cmd.ChannelID, CommunicationMessageSend,
	)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffOfferNormalized{}, communicationAuthorityWindow{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffOfferNormalized{}, communicationAuthorityWindow{}, err
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffOfferNormalized{}, communicationAuthorityWindow{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"handoff offer authority crossed its exact Channel request",
			)
	}
	if err := requireDirectNoticeUserBackedPrincipal(inspected); err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffOfferNormalized{}, communicationAuthorityWindow{}, err
	}
	normalized, err := normalizeHandoffOfferCommand(scope, inspected.principal, cmd)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffOfferNormalized{}, communicationAuthorityWindow{}, err
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(ctx, scope, inspected.principal, nil)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffOfferNormalized{}, communicationAuthorityWindow{}, err
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	return question, bound, inspected, identity, normalized, window, err
}

func (m *Module) prepareHandoffResponseAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	handoffID model.ID,
	cmd HandoffResponseCommand,
) (
	communicationAuthorityQuestion,
	communicationRequestAuthority,
	communicationRequestAuthorityInspection,
	directNoticeReaderIdentityPreflight,
	handoffResponseNormalized,
	communicationAuthorityWindow,
	error,
) {
	if !validCanonicalCommunicationID(handoffID) {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffResponseNormalized{}, communicationAuthorityWindow{},
			communicationError(ErrInvalidCommunicationModel, "invalid Handoff target")
	}
	question, err := newCommunicationAuthorityQuestion(
		scope, handoffKind, handoffID, CommunicationHandoffResponse,
	)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffResponseNormalized{}, communicationAuthorityWindow{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffResponseNormalized{}, communicationAuthorityWindow{}, err
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffResponseNormalized{}, communicationAuthorityWindow{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"handoff response authority crossed its exact request",
			)
	}
	if err := requireDirectNoticeUserBackedPrincipal(inspected); err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffResponseNormalized{}, communicationAuthorityWindow{}, err
	}
	normalized, err := normalizeHandoffResponseCommand(
		scope, inspected.principal, handoffID, cmd,
	)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffResponseNormalized{}, communicationAuthorityWindow{}, err
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(ctx, scope, inspected.principal, nil)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffResponseNormalized{}, communicationAuthorityWindow{}, err
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	return question, bound, inspected, identity, normalized, window, err
}

func (m *Module) prepareHandoffCancelAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	handoffID model.ID,
	cmd HandoffCancelCommand,
) (
	communicationAuthorityQuestion,
	communicationRequestAuthority,
	communicationRequestAuthorityInspection,
	directNoticeReaderIdentityPreflight,
	handoffCancelNormalized,
	communicationAuthorityWindow,
	error,
) {
	if !validCanonicalCommunicationID(handoffID) {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffCancelNormalized{}, communicationAuthorityWindow{},
			communicationError(ErrInvalidCommunicationModel, "invalid Handoff cancel target")
	}
	question, err := newCommunicationAuthorityQuestion(
		scope, handoffKind, handoffID, CommunicationHandoffResponse,
	)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffCancelNormalized{}, communicationAuthorityWindow{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffCancelNormalized{}, communicationAuthorityWindow{}, err
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffCancelNormalized{}, communicationAuthorityWindow{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"Handoff cancel authority crossed its exact request",
			)
	}
	if err := requireDirectNoticeUserBackedPrincipal(inspected); err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffCancelNormalized{}, communicationAuthorityWindow{}, err
	}
	normalized, err := normalizeHandoffCancelCommand(
		scope, inspected.principal, handoffID, cmd,
	)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffCancelNormalized{}, communicationAuthorityWindow{}, err
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(ctx, scope, inspected.principal, nil)
	if err != nil {
		return communicationAuthorityQuestion{}, communicationRequestAuthority{},
			communicationRequestAuthorityInspection{}, directNoticeReaderIdentityPreflight{},
			handoffCancelNormalized{}, communicationAuthorityWindow{}, err
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	return question, bound, inspected, identity, normalized, window, err
}

func normalizeHandoffOfferCommand(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	cmd HandoffOfferCommand,
) (handoffOfferNormalized, error) {
	if err := scope.Validate(); err != nil {
		return handoffOfferNormalized{}, err
	}
	if err := ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return handoffOfferNormalized{}, err
	}
	for _, id := range []model.ID{cmd.ChannelID, cmd.WorkItemID, cmd.MessageID, cmd.DeliveryID} {
		if !validCanonicalCommunicationID(id) {
			return handoffOfferNormalized{}, communicationError(
				ErrInvalidCommunicationModel, "Handoff offer carries an invalid target",
			)
		}
	}
	if _, err := CanonicalProtectedPayloadSlot(PayloadSlotHandoff, cmd.Content); err != nil {
		return handoffOfferNormalized{}, err
	}
	expected, err := parseHandoffETag(cmd.IfMatch)
	if err != nil {
		return handoffOfferNormalized{}, err
	}
	actor := CommunicationActorRef{Kind: ActorUser, Ref: principal.UserID.String()}
	path := "/v1/m/sessions/handoffs"
	identity, err := normalizeHandoffCommandIdentity(
		scope, principal, actor, cmd.IdempotencyKey, cmd.IfMatch,
		handoffOfferMethod, path, expected, struct {
			Operation  string         `json:"operation"`
			ChannelID  model.ID       `json:"channel_id"`
			WorkItemID model.ID       `json:"work_item_id"`
			MessageID  model.ID       `json:"message_id"`
			DeliveryID model.ID       `json:"delivery_id"`
			Content    HandoffContent `json:"handoff"`
			IfMatch    string         `json:"if_match"`
		}{
			Operation: handoffOfferOperation, ChannelID: cmd.ChannelID,
			WorkItemID: cmd.WorkItemID, MessageID: cmd.MessageID,
			DeliveryID: cmd.DeliveryID, Content: cmd.Content, IfMatch: cmd.IfMatch,
		},
	)
	if err != nil {
		return handoffOfferNormalized{}, err
	}
	return handoffOfferNormalized{handoffCommandIdentity: identity, command: cmd}, nil
}

func normalizeHandoffResponseCommand(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	handoffID model.ID,
	cmd HandoffResponseCommand,
) (handoffResponseNormalized, error) {
	if err := scope.Validate(); err != nil {
		return handoffResponseNormalized{}, err
	}
	if err := ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return handoffResponseNormalized{}, err
	}
	if !validCanonicalCommunicationID(handoffID) ||
		(cmd.Transition != HandoffAccept && cmd.Transition != HandoffReject) {
		return handoffResponseNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "invalid Handoff target response",
		)
	}
	if cmd.Transition == HandoffAccept {
		if cmd.Reason != nil {
			return handoffResponseNormalized{}, communicationError(
				ErrInvalidCommunicationModel, "accepted Handoff cannot carry a terminal reason",
			)
		}
	} else {
		if cmd.Reason == nil {
			return handoffResponseNormalized{}, communicationError(
				ErrInvalidCommunicationModel, "rejected Handoff requires a protected reason",
			)
		}
		if _, err := CanonicalProtectedPayloadSlot(PayloadSlotHandoffTerminalReason, *cmd.Reason); err != nil {
			return handoffResponseNormalized{}, err
		}
	}
	expected, err := parseHandoffETag(cmd.IfMatch)
	if err != nil {
		return handoffResponseNormalized{}, err
	}
	actor := CommunicationActorRef{Kind: ActorUser, Ref: principal.UserID.String()}
	path := "/v1/m/sessions/handoffs/" + handoffID.String() + "/responses"
	identity, err := normalizeHandoffCommandIdentity(
		scope, principal, actor, cmd.IdempotencyKey, cmd.IfMatch,
		handoffResponseMethod, path, expected, struct {
			Operation  string                      `json:"operation"`
			HandoffID  model.ID                    `json:"handoff_id"`
			Transition HandoffTransition           `json:"transition"`
			Reason     *CommunicationReasonContent `json:"reason,omitempty"`
			IfMatch    string                      `json:"if_match"`
		}{
			Operation: handoffResponseOperation, HandoffID: handoffID,
			Transition: cmd.Transition, Reason: cmd.Reason, IfMatch: cmd.IfMatch,
		},
	)
	if err != nil {
		return handoffResponseNormalized{}, err
	}
	return handoffResponseNormalized{
		handoffCommandIdentity: identity, handoffID: handoffID, command: cmd,
	}, nil
}

func normalizeHandoffCancelCommand(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	handoffID model.ID,
	cmd HandoffCancelCommand,
) (handoffCancelNormalized, error) {
	if err := scope.Validate(); err != nil {
		return handoffCancelNormalized{}, err
	}
	if err := ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return handoffCancelNormalized{}, err
	}
	if !validCanonicalCommunicationID(handoffID) {
		return handoffCancelNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "invalid Handoff cancel target",
		)
	}
	if _, err := CanonicalProtectedPayloadSlot(
		PayloadSlotHandoffTerminalReason, cmd.Reason,
	); err != nil {
		return handoffCancelNormalized{}, err
	}
	expected, err := parseHandoffETag(cmd.IfMatch)
	if err != nil {
		return handoffCancelNormalized{}, err
	}
	actor := CommunicationActorRef{Kind: ActorUser, Ref: principal.UserID.String()}
	path := "/v1/m/sessions/handoffs/" + handoffID.String() + "/cancel"
	identity, err := normalizeHandoffCommandIdentity(
		scope, principal, actor, cmd.IdempotencyKey, cmd.IfMatch,
		handoffCancelMethod, path, expected, struct {
			Operation string                     `json:"operation"`
			Handoff   model.ID                   `json:"handoff_id"`
			Reason    CommunicationReasonContent `json:"reason"`
			IfMatch   string                     `json:"if_match"`
		}{handoffCancelOperation, handoffID, cmd.Reason, cmd.IfMatch},
	)
	if err != nil {
		return handoffCancelNormalized{}, err
	}
	return handoffCancelNormalized{
		handoffCommandIdentity: identity, handoffID: handoffID, command: cmd,
	}, nil
}

func normalizeHandoffDeadlineCommand(
	scope DirectoryScopeRef,
	authority handoffDeadlineAuthority,
	cmd handoffDeadlineCommand,
) (handoffDeadlineNormalized, error) {
	if err := scope.Validate(); err != nil {
		return handoffDeadlineNormalized{}, err
	}
	if !validCanonicalCommunicationID(cmd.HandoffID) || cmd.ExpectedVersion < 1 {
		return handoffDeadlineNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "invalid Handoff deadline command",
		)
	}
	if err := validateHandoffDeadlineAuthority(scope, authority); err != nil {
		return handoffDeadlineNormalized{}, err
	}
	identity, err := buildHandoffCommandIdentity(
		scope, CommunicationPrincipal{}, authority.Actor, cmd.IdempotencyKey,
		"REAPER", "/internal/handoffs/"+cmd.HandoffID.String()+"/deadline",
		cmd.ExpectedVersion, struct {
			Operation       string   `json:"operation"`
			HandoffID       model.ID `json:"handoff_id"`
			ExpectedVersion int64    `json:"expected_version"`
		}{handoffDeadlineOperation, cmd.HandoffID, cmd.ExpectedVersion},
	)
	if err != nil {
		return handoffDeadlineNormalized{}, err
	}
	authority.Facts = append([]store.AuthorizationFactRef(nil), authority.Facts...)
	return handoffDeadlineNormalized{
		handoffCommandIdentity: identity, command: cmd, authority: authority,
	}, nil
}

func validateHandoffDeadlineAuthority(
	scope DirectoryScopeRef,
	authority handoffDeadlineAuthority,
) error {
	if authority.Actor.Validate() != nil || authority.Actor.Kind != ActorSystem {
		return communicationError(
			ErrCommunicationForbidden, "Handoff deadline requires the system reaper",
		)
	}
	if authority.ObservedAt.IsZero() || !authority.FreshUntil.After(authority.ObservedAt) ||
		ValidateAuthorityEvidence(authority.Evidence) != nil ||
		evidenceVerdict(authority.Evidence) != VerdictClean {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff deadline authority is unavailable",
		)
	}
	facts, err := CanonicalAuthorizationFacts(authority.Facts)
	if err != nil || len(facts) == 0 || !equalCommunicationAuthorityFacts(facts, authority.Facts) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff deadline authority facts are unavailable",
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
			ErrCommunicationEvidenceUnknown, "Handoff deadline authority lacks directory fencing",
		)
	}
	return nil
}

func normalizeHandoffCommandIdentity(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	actor CommunicationActorRef,
	idempotencyKey string,
	ifMatch string,
	method string,
	path string,
	expectedVersion int64,
	request any,
) (handoffCommandIdentity, error) {
	keyID, err := model.ParseID(idempotencyKey)
	if err != nil || !validCanonicalCommunicationID(keyID) || keyID.String() != idempotencyKey {
		return handoffCommandIdentity{}, communicationError(
			ErrInvalidCommunicationModel, "Handoff idempotency key is invalid",
		)
	}
	if actor.Validate() != nil || actor.Kind != ActorUser || actor.Ref != principal.UserID.String() {
		return handoffCommandIdentity{}, communicationError(
			ErrCommunicationForbidden, "Handoff requires a claim-free authenticated User",
		)
	}
	return buildHandoffCommandIdentity(
		scope, principal, actor, idempotencyKey, method, path, expectedVersion, request,
	)
}

func buildHandoffCommandIdentity(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	actor CommunicationActorRef,
	idempotencyKey string,
	method string,
	path string,
	expectedVersion int64,
	request any,
) (handoffCommandIdentity, error) {
	if err := scope.Validate(); err != nil {
		return handoffCommandIdentity{}, err
	}
	if actor.Validate() != nil || expectedVersion < 1 {
		return handoffCommandIdentity{}, communicationError(
			ErrInvalidCommunicationModel, "Handoff command identity is invalid",
		)
	}
	keyID, err := model.ParseID(idempotencyKey)
	if err != nil || !validCanonicalCommunicationID(keyID) || keyID.String() != idempotencyKey {
		return handoffCommandIdentity{}, communicationError(
			ErrInvalidCommunicationModel, "Handoff idempotency key is invalid",
		)
	}
	actorRaw, err := canonicalJSON(actor)
	if err != nil {
		return handoffCommandIdentity{}, err
	}
	requestRaw, err := canonicalJSON(request)
	if err != nil {
		return handoffCommandIdentity{}, err
	}
	actorHash := sha256.Sum256(actorRaw)
	keyHash := sha256.Sum256([]byte(idempotencyKey))
	requestHash := sha256.Sum256(requestRaw)
	commandScope := fmt.Sprintf("%s %s;workspace=%s", method, path, scope.WorkspaceID)
	if !validateOpaqueRef(commandScope) {
		return handoffCommandIdentity{}, communicationError(
			ErrInvalidCommunicationModel, "Handoff command scope is invalid",
		)
	}
	return handoffCommandIdentity{
		scope: scope, principal: principal, actor: actor,
		actorFingerprint: actorHash[:], idempotencyKeyHash: keyHash[:],
		requestDigest: requestHash[:], commandScope: commandScope,
		expectedVersion: expectedVersion,
	}, nil
}

func parseHandoffETag(value string) (int64, error) {
	if value == "" {
		return 0, errHandoffVersionRequired
	}
	if len(value) < 4 || value[:2] != "\"v" || value[len(value)-1] != '"' {
		return 0, communicationError(
			ErrInvalidCommunicationModel, "Handoff If-Match is not a strong version tag",
		)
	}
	version, err := strconv.ParseInt(value[2:len(value)-1], 10, 64)
	if err != nil || version < 1 || value != fmt.Sprintf("\"v%d\"", version) {
		return 0, communicationError(
			ErrInvalidCommunicationModel, "Handoff If-Match is not canonical",
		)
	}
	return version, nil
}

func equalHandoffCommandIdentity(left, right handoffCommandIdentity) bool {
	return left.scope == right.scope && left.principal == right.principal &&
		left.actor == right.actor && left.commandScope == right.commandScope &&
		left.expectedVersion == right.expectedVersion &&
		bytes.Equal(left.actorFingerprint, right.actorFingerprint) &&
		bytes.Equal(left.idempotencyKeyHash, right.idempotencyKeyHash) &&
		bytes.Equal(left.requestDigest, right.requestDigest)
}

func (m *Module) prepareHandoffOfferPayload(
	ctx context.Context,
	normalized handoffOfferNormalized,
	handoffID model.ID,
) (handoffOfferPrepared, error) {
	var prepared handoffOfferPrepared
	err := m.viewCommunication(ctx, normalized.scope, func(sc store.Scope) error {
		messageRepo, err := sc.Ext(messageKind)
		if err != nil {
			return err
		}
		deliveryRepo, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		channelRepo, err := sc.Ext(channelKind)
		if err != nil {
			return err
		}
		deliveryRecord, err := deliveryRepo.Get(ctx, normalized.command.DeliveryID)
		if err != nil {
			return err
		}
		delivery, err := messageDeliveryFromRecord(deliveryRecord)
		if err != nil {
			return err
		}
		rows, page, err := deliveryRepo.List(ctx, model.Query{Filters: []model.Filter{{
			Column: colCommMessageID, Op: model.OpEq, Value: normalized.command.MessageID.String(),
		}}, Limit: directNoticeReadSetBound})
		if err != nil || page.HasMore {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff Delivery set is unavailable",
			)
		}
		required := int64(0)
		for _, row := range rows {
			candidate, decodeErr := messageDeliveryFromRecord(row)
			if decodeErr != nil {
				return decodeErr
			}
			if candidate.Required {
				required++
			}
		}
		messageRecord, err := messageRepo.Get(ctx, normalized.command.MessageID)
		if err != nil {
			return err
		}
		message, err := messageFromRecord(messageRecord, required)
		if err != nil {
			return err
		}
		channelRecord, err := channelRepo.Get(ctx, normalized.command.ChannelID)
		if err != nil {
			return err
		}
		channel, err := channelFromRecord(channelRecord)
		if err != nil {
			return err
		}
		if message.ChannelID != channel.ID || message.ID != delivery.MessageID ||
			message.WorkItemID != normalized.command.WorkItemID ||
			message.Kind != MessageHandoffOffer || message.State != MessagePublished ||
			delivery.ID != normalized.command.DeliveryID || !delivery.Required ||
			required != 1 || delivery.AckDueAt == nil {
			return communicationError(
				ErrInvalidCommunicationModel,
				"Handoff offer carrier is not one published required Delivery",
			)
		}
		policy := protectedPayloadPolicyFrom(message.Payload)
		schema, _ := PayloadSlotHandoff.schema()
		payload, err := PrepareProtectedPayload(
			ctx, m.communicationSealer, PayloadSlotHandoff, policy,
			ContentAAD{
				TenantID: normalized.scope.TenantID, WorkspaceID: normalized.scope.WorkspaceID,
				ChannelID: channel.ID, EntityKind: handoffKind, EntityID: handoffID,
				Schema: schema, ProtectionGeneration: policy.ProtectionGeneration,
			},
			normalized.command.Content,
		)
		if err != nil {
			return err
		}
		prepared = handoffOfferPrepared{
			message: message, delivery: delivery, channel: channel,
			payload: cloneProtectedPayload(payload),
		}
		return nil
	})
	return prepared, err
}

func (m *Module) prepareHandoffResponseContent(
	ctx context.Context,
	normalized handoffResponseNormalized,
) (handoffResponsePrepared, error) {
	if normalized.command.Transition == HandoffAccept {
		return handoffResponsePrepared{}, nil
	}
	return m.prepareHandoffTerminalContent(
		ctx, normalized.scope, normalized.handoffID, *normalized.command.Reason,
	)
}

func (m *Module) prepareHandoffTerminalContent(
	ctx context.Context,
	scope DirectoryScopeRef,
	handoffID model.ID,
	content CommunicationReasonContent,
) (handoffResponsePrepared, error) {
	if err := scope.Validate(); err != nil {
		return handoffResponsePrepared{}, err
	}
	if !validCanonicalCommunicationID(handoffID) {
		return handoffResponsePrepared{}, communicationError(
			ErrInvalidCommunicationModel, "invalid Handoff terminal target",
		)
	}
	if _, err := CanonicalProtectedPayloadSlot(
		PayloadSlotHandoffTerminalReason, content,
	); err != nil {
		return handoffResponsePrepared{}, err
	}
	var prepared handoffResponsePrepared
	err := m.viewCommunication(ctx, scope, func(sc store.Scope) error {
		handoffs, err := sc.Ext(handoffKind)
		if err != nil {
			return err
		}
		handoffRecord, err := handoffs.Get(ctx, handoffID)
		if err != nil {
			return err
		}
		handoff, err := handoffFromRecord(handoffRecord)
		if err != nil {
			return err
		}
		messages, err := sc.Ext(messageKind)
		if err != nil {
			return err
		}
		messageRecord, err := messages.Get(ctx, handoff.MessageID)
		if err != nil {
			return err
		}
		deliveries, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		rows, page, err := deliveries.List(ctx, model.Query{Filters: []model.Filter{{
			Column: colCommMessageID, Op: model.OpEq, Value: handoff.MessageID.String(),
		}}, Limit: directNoticeReadSetBound})
		if err != nil || page.HasMore {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff carrier Delivery set is unavailable",
			)
		}
		required := int64(0)
		for _, row := range rows {
			delivery, decodeErr := messageDeliveryFromRecord(row)
			if decodeErr != nil {
				return decodeErr
			}
			if delivery.Required {
				required++
			}
		}
		message, err := messageFromRecord(messageRecord, required)
		if err != nil {
			return err
		}
		policy := protectedPayloadPolicyFrom(message.Payload)
		schema, _ := PayloadSlotHandoffTerminalReason.schema()
		reason, err := PrepareProtectedPayload(
			ctx, m.communicationSealer, PayloadSlotHandoffTerminalReason, policy,
			ContentAAD{
				TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
				ChannelID: message.ChannelID, EntityKind: handoffKind,
				EntityID: handoffID, Schema: schema,
				ProtectionGeneration: policy.ProtectionGeneration,
			},
			content,
		)
		if err != nil {
			return err
		}
		prepared.terminalReason = &reason
		return nil
	})
	return prepared, err
}
