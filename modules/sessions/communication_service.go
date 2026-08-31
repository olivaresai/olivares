// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	directNoticePublishPath        = "/v1/m/sessions/messages/send"
	directNoticePublishOperation   = "message.publish.direct"
	directNoticePublishAuditAction = "sessions.communication.message.publish"
	communicationMessageAvailable  = "work.message.available"
)

var errDirectNoticeReplayNeedsFreshAudit = errors.New(
	"sessions: direct notice replay requires a fresh audit view",
)

// DirectNoticePublishCommand is the deliberately narrow first WP-2 write
// vertical. Tenant, workspace and sender are server-authored arguments to the
// service and have no representation in this command body.
type DirectNoticePublishCommand struct {
	ChannelID        model.ID       `json:"channel_id"`
	Recipient        RecipientRef   `json:"recipient"`
	Content          MessageContent `json:"content"`
	Urgency          MessageUrgency `json:"urgency,omitempty"`
	IdempotencyKey   string         `json:"-"`
	ExpectedPlanHash string         `json:"-"`
	HTTPMethod       string         `json:"-"`
	CommandScope     string         `json:"-"`
}

// DirectNoticePublishResult is reconstructible from the durable receipt. Raw
// message content and recipient identity are intentionally absent.
type DirectNoticePublishResult struct {
	Verdict       AssessmentVerdict     `json:"verdict"`
	Code          string                `json:"code"`
	CommandID     model.ID              `json:"command_id"`
	ChannelID     model.ID              `json:"channel_id"`
	MessageID     model.ID              `json:"message_id"`
	DeliveryID    model.ID              `json:"delivery_id"`
	EventID       model.ID              `json:"event_id"`
	Version       int64                 `json:"version"`
	State         MessageState          `json:"state"`
	DeliveryCount int64                 `json:"delivery_count"`
	RequiredCount int64                 `json:"required_count"`
	AckQuorum     int64                 `json:"ack_quorum"`
	Fulfillment   FulfillmentProjection `json:"fulfillment"`
	AudienceHash  string                `json:"audience_hash"`
	PayloadDigest string                `json:"payload_digest"`
	PlanHash      string                `json:"plan_hash"`
	AuditSeq      int64                 `json:"audit_seq"`
	Replayed      bool                  `json:"-"`
}

type directNoticePublishIDs struct {
	Message      model.ID
	Audience     model.ID
	Delivery     model.ID
	Contribution model.ID
	Command      model.ID
	Event        model.ID
	Receipt      model.ID
}

func newDirectNoticePublishIDs() directNoticePublishIDs {
	return directNoticePublishIDs{
		Message: model.NewID(), Audience: model.NewID(), Delivery: model.NewID(),
		Contribution: model.NewID(), Command: model.NewID(), Event: model.NewID(),
		Receipt: model.NewID(),
	}
}

type directNoticePublishPreflight struct {
	Command               DirectNoticePublishCommand
	Scope                 DirectoryScopeRef
	Principal             CommunicationPrincipal
	Sender                CommunicationActorRef
	Channel               Channel
	IDs                   directNoticePublishIDs
	Payload               ProtectedPayload
	AudienceRequest       PublicationAudienceRequest
	AudienceAttestation   PublicationAudienceAttestation
	Snapshot              DirectorySnapshot
	GrantClosure          ChannelGrantSubjectClosure
	RecipientGrantClosure ChannelGrantSubjectClosure
	CoreWitness           ReadWitness
	ActorFingerprint      []byte
	IdempotencyHash       []byte
	RequestDigest         []byte
	bindingID             *communicationRequestAuthorityBindingID
}

type directNoticeRequestDigestInput struct {
	Operation        string         `json:"operation"`
	Method           string         `json:"method"`
	CommandScope     string         `json:"command_scope"`
	ChannelID        model.ID       `json:"channel_id"`
	Recipient        RecipientRef   `json:"recipient"`
	Content          MessageContent `json:"content"`
	Urgency          MessageUrgency `json:"urgency"`
	ExpectedPlanHash string         `json:"expected_plan_hash,omitempty"`
}

// directNoticeApplyCommitmentV1 is the versioned pre-audit binding between one
// idempotent command and the immutable effect identifiers/result projection it
// is about to create. The semantic plan intentionally excludes random IDs and
// the idempotency key; this separate audit-chain commitment prevents a retained
// receipt or Event from being relabelled onto another key/ID without a coherent
// rewrite of the corresponding audit anchor. Runtime authentication of the
// anchor's detached signature is a distinct store/composition capability; the
// structural reader verifies its canonical hash and predecessor link.
type directNoticeApplyCommitmentV1 struct {
	SchemaVersion      int64                               `json:"schema_version"`
	TenantID           model.TenantID                      `json:"tenant_id"`
	WorkspaceID        model.ID                            `json:"workspace_id"`
	ActorFingerprint   []byte                              `json:"actor_fingerprint"`
	CommandScope       string                              `json:"command_scope"`
	IdempotencyKeyHash []byte                              `json:"idempotency_key_hash"`
	RequestDigest      []byte                              `json:"request_digest"`
	PlanHash           []byte                              `json:"plan_hash"`
	ReceiptID          model.ID                            `json:"receipt_id"`
	CommandID          model.ID                            `json:"command_id"`
	ChannelID          model.ID                            `json:"channel_id"`
	MessageID          model.ID                            `json:"message_id"`
	DeliveryID         model.ID                            `json:"delivery_id"`
	EventID            model.ID                            `json:"event_id"`
	MessageVersion     int64                               `json:"message_version"`
	MessageState       string                              `json:"message_state"`
	DeliveryCount      int64                               `json:"delivery_count"`
	Fulfillment        directNoticeFulfillmentProjectionV1 `json:"fulfillment"`
	AudienceHash       []byte                              `json:"audience_hash"`
	AudienceGraphIDs   []byte                              `json:"audience_graph_ids"`
	DeliveryEvidence   []byte                              `json:"delivery_evidence"`
	PayloadDigest      []byte                              `json:"payload_digest"`
	CompletedAt        string                              `json:"completed_at"`
}

const (
	// directNoticeApplyCommitmentV1Version is the retained interpretation of
	// apply_commitment metadata. Never relabel it when a later writer adds V2.
	directNoticeApplyCommitmentV1Version int64 = 1
	// directNoticeCurrentApplyCommitmentVersion selects new audit metadata.
	directNoticeCurrentApplyCommitmentVersion = directNoticeApplyCommitmentV1Version
)

// directNoticeDeliveryEvidenceV1 seals the immutable delivery envelope that is
// not fully represented by the public response. In particular DeliverySeq and
// AckDueAt affect inbox ordering and deadline semantics, so a coherent receipt
// replay must not accept either being relabelled in a retained graph.
type directNoticeDeliveryEvidenceV1 struct {
	SchemaVersion int64         `json:"schema_version"`
	DeliveryID    model.ID      `json:"delivery_id"`
	DeliverySeq   int64         `json:"delivery_seq"`
	Required      bool          `json:"required"`
	RouteReasons  []RouteReason `json:"route_reasons"`
	WakePolicy    WakePolicy    `json:"wake_policy"`
	AckDueAt      string        `json:"ack_due_at,omitempty"`
	ExpiresAt     string        `json:"expires_at,omitempty"`
	AvailableAt   string        `json:"available_at"`
}

const directNoticeDeliveryEvidenceV1Version int64 = 1

type directNoticeFulfillmentProjectionV1 struct {
	State        FulfillmentState `json:"state"`
	Required     int64            `json:"required"`
	Acknowledged int64            `json:"acknowledged"`
	Viable       int64            `json:"viable"`
	Unmet        int64            `json:"unmet"`
	Quorum       int64            `json:"quorum"`
}

func directNoticeFulfillmentV1(value FulfillmentProjection) directNoticeFulfillmentProjectionV1 {
	return directNoticeFulfillmentProjectionV1{
		State: value.State, Required: value.Required, Acknowledged: value.Acknowledged,
		Viable: value.Viable, Unmet: value.Unmet, Quorum: value.Quorum,
	}
}

func canonicalDirectNoticeApplyCommitment(value directNoticeApplyCommitmentV1) ([]byte, error) {
	raw, err := canonicalJSON(value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func canonicalDirectNoticeAudienceGraphIDs(
	audienceID model.ID,
	contributionID model.ID,
) ([]byte, error) {
	raw, err := canonicalJSON(struct {
		AudienceID     model.ID `json:"audience_id"`
		ContributionID model.ID `json:"contribution_id"`
	}{AudienceID: audienceID, ContributionID: contributionID})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func canonicalDirectNoticeDeliveryEvidence(delivery MessageDelivery) ([]byte, error) {
	var ackDueAt, expiresAt string
	if delivery.AckDueAt != nil {
		ackDueAt = delivery.AckDueAt.UTC().Format(time.RFC3339Nano)
	}
	if delivery.ExpiresAt != nil {
		expiresAt = delivery.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	raw, err := canonicalJSON(directNoticeDeliveryEvidenceV1{
		SchemaVersion: directNoticeDeliveryEvidenceV1Version,
		DeliveryID:    delivery.ID, DeliverySeq: delivery.DeliverySeq,
		Required:     delivery.Required,
		RouteReasons: append([]RouteReason(nil), delivery.RouteReasons...),
		WakePolicy:   delivery.WakePolicy, AckDueAt: ackDueAt, ExpiresAt: expiresAt,
		AvailableAt: delivery.AvailableAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func directNoticeApplyCommitmentFromPlan(
	preflight directNoticePublishPreflight,
	plan MessagePublishPlan,
	planHash []byte,
	fulfillment FulfillmentProjection,
	dbNow time.Time,
) ([]byte, error) {
	audienceGraphIDs, err := canonicalDirectNoticeAudienceGraphIDs(
		preflight.IDs.Audience, preflight.IDs.Contribution,
	)
	if err != nil {
		return nil, err
	}
	deliveryEvidence, err := canonicalDirectNoticeDeliveryEvidence(plan.Deliveries[0])
	if err != nil {
		return nil, err
	}
	return canonicalDirectNoticeApplyCommitment(directNoticeApplyCommitmentV1{
		SchemaVersion:      directNoticeCurrentApplyCommitmentVersion,
		TenantID:           preflight.Scope.TenantID,
		WorkspaceID:        preflight.Scope.WorkspaceID,
		ActorFingerprint:   cloneDirectNoticeBytes(preflight.ActorFingerprint),
		CommandScope:       preflight.Command.CommandScope,
		IdempotencyKeyHash: cloneDirectNoticeBytes(preflight.IdempotencyHash),
		RequestDigest:      cloneDirectNoticeBytes(preflight.RequestDigest),
		PlanHash:           cloneDirectNoticeBytes(planHash), ReceiptID: preflight.IDs.Receipt,
		CommandID: preflight.IDs.Command, ChannelID: preflight.Channel.ID,
		MessageID: plan.After.ID, DeliveryID: plan.Deliveries[0].ID,
		EventID: preflight.IDs.Event, MessageVersion: plan.After.Version,
		MessageState: string(plan.After.State), DeliveryCount: 1,
		Fulfillment:      directNoticeFulfillmentV1(fulfillment),
		AudienceHash:     cloneDirectNoticeBytes(plan.After.AudienceHash),
		AudienceGraphIDs: cloneDirectNoticeBytes(audienceGraphIDs),
		DeliveryEvidence: cloneDirectNoticeBytes(deliveryEvidence),
		PayloadDigest:    cloneDirectNoticeBytes(plan.After.Payload.Digest),
		CompletedAt:      dbNow.UTC().Format(time.RFC3339Nano),
	})
}

func directNoticeApplyCommitmentFromReceipt(
	receipt CommunicationCommandReceipt,
) ([]byte, error) {
	projection := receipt.ResponseProjectionJSON
	fulfillment, err := directNoticeInitialFulfillmentFromProjection(projection)
	if err != nil {
		return nil, err
	}
	return canonicalDirectNoticeApplyCommitment(directNoticeApplyCommitmentV1{
		SchemaVersion: directNoticeApplyCommitmentV1Version,
		TenantID:      receipt.TenantID, WorkspaceID: receipt.WorkspaceID,
		ActorFingerprint:   cloneDirectNoticeBytes(receipt.ActorFingerprint),
		CommandScope:       receipt.CommandScope,
		IdempotencyKeyHash: cloneDirectNoticeBytes(receipt.IdempotencyKeyHash),
		RequestDigest:      cloneDirectNoticeBytes(receipt.RequestDigest),
		PlanHash:           cloneDirectNoticeBytes(receipt.PlanHash), ReceiptID: receipt.ID,
		CommandID: receipt.CommandID, ChannelID: projection.IDs["channel_id"],
		MessageID: receipt.ResultID, DeliveryID: projection.IDs["delivery_id"],
		EventID: receipt.EventID, MessageVersion: projection.Version,
		MessageState: projection.State, DeliveryCount: projection.Counts["delivery_count"],
		Fulfillment:      directNoticeFulfillmentV1(fulfillment),
		AudienceHash:     cloneDirectNoticeBytes(projection.Digests["audience"]),
		AudienceGraphIDs: cloneDirectNoticeBytes(projection.Digests["contributions"]),
		DeliveryEvidence: cloneDirectNoticeBytes(projection.Digests["route_reasons"]),
		PayloadDigest:    cloneDirectNoticeBytes(projection.Digests["payload"]),
		CompletedAt:      receipt.CompletedAt.UTC().Format(time.RFC3339Nano),
	})
}

// PublishDirectNotice is the future handler-facing boundary. The private
// vertical below remains directly testable while WP-3 deliberately keeps the
// production readiness conjunction OFF: permissions, resolver and pump are not
// all wired (and the sealer remains an optional readiness witness).
func (m *Module) PublishDirectNotice(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd DirectNoticePublishCommand,
) (DirectNoticePublishResult, error) {
	return m.publishDirectNoticeWithCurrentAuthority(ctx, scope, ref, cmd, true)
}

// publishDirectNoticeWithAuthority is the private exact-authority test seam. It
// deliberately bypasses only the still-OFF aggregate readiness conjunction; it
// retains the same current credential resolution, authorization and transaction
// binding as the future handler-facing boundary above.
func (m *Module) publishDirectNoticeWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd DirectNoticePublishCommand,
) (DirectNoticePublishResult, error) {
	return m.publishDirectNoticeWithCurrentAuthority(ctx, scope, ref, cmd, false)
}

func (m *Module) publishDirectNoticeWithCurrentAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd DirectNoticePublishCommand,
	requireReadiness bool,
) (DirectNoticePublishResult, error) {
	question, err := newCommunicationAuthorityQuestion(
		scope, channelKind, cmd.ChannelID, CommunicationMessageSend,
	)
	if err != nil {
		return DirectNoticePublishResult{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return DirectNoticePublishResult{}, err
	}
	boundContext, err := bound.contextFor(question)
	if err != nil || boundContext.question != question {
		return DirectNoticePublishResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"message-send authority context crossed its exact request",
		)
	}
	if err := requireDirectNoticeUserBackedPrincipal(boundContext); err != nil {
		return DirectNoticePublishResult{}, err
	}
	principal := boundContext.principal
	normalized, actorFingerprint, idempotencyHash, requestDigest, err :=
		normalizeDirectNoticePublishCommand(scope, principal, cmd)
	if err != nil {
		return DirectNoticePublishResult{}, err
	}
	if requireReadiness {
		readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
		if readinessErr != nil || !readiness.Effective {
			return DirectNoticePublishResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
			)
		}
	}
	// The verified View produces only an internal candidate. Returning it requires
	// consuming this same binding in a short transaction that pins the request
	// facts and re-reads the exact receipt under its idempotency lock.
	if replay, found, replayErr := m.lookupDirectNoticeReplay(
		ctx, scope, principal, normalized,
		actorFingerprint, idempotencyHash, requestDigest,
	); errors.Is(replayErr, store.ErrConflict) {
		if confirmErr := m.confirmDirectNoticeIdempotencyConflictWithAuthority(
			ctx, scope, question, bound, boundContext, normalized,
			actorFingerprint, idempotencyHash, requestDigest,
		); confirmErr != nil {
			return DirectNoticePublishResult{}, confirmErr
		}
		return DirectNoticePublishResult{}, fmt.Errorf(
			"%w: idempotency_key_reused", store.ErrConflict,
		)
	} else if replayErr != nil {
		return DirectNoticePublishResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication replay candidate is unavailable",
		)
	} else if found {
		if confirmErr := m.confirmDirectNoticeReplayWithAuthority(
			ctx, scope, question, bound, boundContext, normalized,
			actorFingerprint, idempotencyHash, requestDigest, replay,
		); confirmErr != nil {
			return DirectNoticePublishResult{}, confirmErr
		}
		replay.Replayed = true
		return replay, nil
	}
	preflight, err := m.preflightDirectNoticePublishWithoutCore(
		ctx, scope, principal, normalized,
		actorFingerprint, idempotencyHash, requestDigest,
	)
	if err != nil {
		return DirectNoticePublishResult{}, err
	}

	var result DirectNoticePublishResult
	var auditGap bool
	mutateErr := m.mutateCommunicationWithAuthority(
		ctx,
		question,
		bound,
		CommunicationClaimAuthoritySnapshot{},
		func(tx *communicationTx, mutationContext communicationRequestAuthorityContext) error {
			boundPreflight, err := directNoticePublishPreflightWithBoundAuthority(
				preflight, boundContext, mutationContext,
			)
			if err != nil {
				return err
			}
			// Request facts are merged by communicationTx. Pin the complete
			// request+local snapshot before every idempotency or domain lock.
			authorityLock, err := lockDirectNoticePublishAuthoritySnapshot(
				ctx, tx, boundPreflight,
			)
			if err != nil {
				return err
			}
			if err := tx.lockTransaction(ctx, directNoticeIdempotencyLockKey(preflight)); err != nil {
				return err
			}
			_, found, err := readDirectNoticeReplayReceipt(
				ctx,
				func(kind model.Kind) (communicationReadRepository, error) { return tx.repo(kind) },
				scope, normalized,
				actorFingerprint, idempotencyHash, requestDigest,
			)
			if err != nil {
				return err
			}
			if found {
				// Roll this one-shot attempt back. The outer path must rebind,
				// verify the graph in a fresh View, and confirm the receipt under
				// that new binding before returning anything.
				return errDirectNoticeReplayNeedsFreshAudit
			}
			if err := tx.lockTransaction(
				ctx, directNoticeMessageLockKey(scope, boundPreflight.IDs.Message),
			); err != nil {
				return err
			}
			var applyErr error
			result, auditGap, applyErr = applyDirectNoticePublishAfterAuthoritySnapshot(
				ctx, tx, boundPreflight, authorityLock,
			)
			return applyErr
		},
	)
	if mutateErr != nil {
		if errors.Is(mutateErr, store.ErrConflict) ||
			errors.Is(mutateErr, errDirectNoticeReplayNeedsFreshAudit) {
			replay, found, replayErr := m.lookupDirectNoticeReplayAfterAuthorityRace(
				ctx, scope, ref, question, normalized,
				actorFingerprint, idempotencyHash, requestDigest,
			)
			if replayErr != nil {
				return DirectNoticePublishResult{}, replayErr
			}
			if found {
				replay.Replayed = true
				return replay, nil
			}
		}
		return DirectNoticePublishResult{}, mutateErr
	}
	if auditGap {
		return DirectNoticePublishResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "audit evidence could not be persisted",
		)
	}
	return result, nil
}

func sameCommunicationRequestAuthorityIdentity(
	left communicationRequestAuthorityInspection,
	right communicationRequestAuthorityContext,
) bool {
	return left.bindingID != nil && left.bindingID == right.bindingID &&
		left.question == right.question && left.principal == right.principal
}

func requireDirectNoticeUserBackedPrincipal(
	inspected communicationRequestAuthorityInspection,
) error {
	principal := inspected.principal
	if principal.UserID == "" || principal.AgentExternalID != "" || principal.SessionID != "" ||
		principal.SessionRunRef != "" || principal.SessionFence != 0 ||
		principal.SessionWorkspaceID != "" || principal.PurposeRestricted ||
		principal.System || principal.SystemActorRef != "" || principal.SystemGrantAgentID != "" {
		return communicationError(
			ErrCommunicationForbidden,
			"direct notice publish requires a user-backed credential",
		)
	}
	return nil
}

func validateConsumedDirectNoticeAuthority(
	inspected communicationRequestAuthorityInspection,
	consumed communicationRequestAuthorityContext,
) error {
	if !sameCommunicationRequestAuthorityIdentity(inspected, consumed) ||
		ValidateReadWitness(consumed.witness) != nil ||
		consumed.witness.Outcome != ReadAllow ||
		consumed.witness.Entity != consumed.question.entity ||
		consumed.witness.Operation != consumed.question.operation ||
		consumed.witness.Principal != consumed.principal {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"message-send authority changed before mutation",
		)
	}
	return nil
}

func directNoticePublishPreflightWithBoundAuthority(
	preflight directNoticePublishPreflight,
	inspected communicationRequestAuthorityInspection,
	consumed communicationRequestAuthorityContext,
) (directNoticePublishPreflight, error) {
	if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
		return directNoticePublishPreflight{}, err
	}
	if consumed.question.entity != (EntityRef{
		TenantID: preflight.Scope.TenantID, Kind: channelKind,
		ID: preflight.Command.ChannelID, WorkspaceID: preflight.Scope.WorkspaceID,
	}) || consumed.question.operation != CommunicationMessageSend ||
		consumed.principal != preflight.Principal {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"message-send authority crossed publish preflight",
		)
	}
	preflight.CoreWitness = cloneCommunicationRequestAuthorityWitness(consumed.witness)
	preflight.bindingID = consumed.bindingID
	return preflight, nil
}

func (m *Module) confirmDirectNoticeReplayWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	question communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	inspected communicationRequestAuthorityInspection,
	cmd DirectNoticePublishCommand,
	actorFingerprint []byte,
	idempotencyHash []byte,
	requestDigest []byte,
	candidate DirectNoticePublishResult,
) error {
	return m.mutateCommunicationWithAuthority(
		ctx,
		question,
		bound,
		CommunicationClaimAuthoritySnapshot{},
		func(tx *communicationTx, consumed communicationRequestAuthorityContext) error {
			if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
				return err
			}
			if err := tx.lockTransaction(
				ctx,
				directNoticeIdempotencyAuthorityLockKey(
					scope, actorFingerprint, idempotencyHash,
				),
			); err != nil {
				return err
			}
			receipt, found, err := readDirectNoticeReplayReceipt(
				ctx,
				func(kind model.Kind) (communicationReadRepository, error) { return tx.repo(kind) },
				scope, cmd, actorFingerprint, idempotencyHash, requestDigest,
			)
			if err != nil {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"communication receipt changed before authority confirmation",
				)
			}
			if !found {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"verified communication receipt disappeared before confirmation",
				)
			}
			confirmed, _, err := directNoticeResultProjectionFromReceipt(cmd, receipt)
			if err != nil || confirmed != candidate {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"communication receipt changed before authority confirmation",
				)
			}
			return tx.refreshNow(ctx)
		},
	)
}

func (m *Module) confirmDirectNoticeIdempotencyConflictWithAuthority(
	ctx context.Context,
	scope DirectoryScopeRef,
	question communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	inspected communicationRequestAuthorityInspection,
	cmd DirectNoticePublishCommand,
	actorFingerprint []byte,
	idempotencyHash []byte,
	requestDigest []byte,
) error {
	return m.mutateCommunicationWithAuthority(
		ctx,
		question,
		bound,
		CommunicationClaimAuthoritySnapshot{},
		func(tx *communicationTx, consumed communicationRequestAuthorityContext) error {
			if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
				return err
			}
			if err := tx.lockTransaction(
				ctx,
				directNoticeIdempotencyAuthorityLockKey(
					scope, actorFingerprint, idempotencyHash,
				),
			); err != nil {
				return err
			}
			receipt, found, err := readDirectNoticeReplayReceiptCandidate(
				ctx,
				func(kind model.Kind) (communicationReadRepository, error) { return tx.repo(kind) },
				scope, cmd, actorFingerprint, idempotencyHash,
			)
			if err != nil || !found || bytes.Equal(receipt.RequestDigest, requestDigest) {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"communication idempotency conflict changed before authority confirmation",
				)
			}
			return tx.refreshNow(ctx)
		},
	)
}

func (m *Module) lookupDirectNoticeReplayAfterAuthorityRace(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	question communicationAuthorityQuestion,
	cmd DirectNoticePublishCommand,
	actorFingerprint []byte,
	idempotencyHash []byte,
	requestDigest []byte,
) (DirectNoticePublishResult, bool, error) {
	rebound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	reboundContext, err := rebound.contextFor(question)
	if err != nil || reboundContext.question != question {
		return DirectNoticePublishResult{}, false, communicationError(
			ErrCommunicationEvidenceUnknown,
			"replay authority context crossed its exact request",
		)
	}
	if err := requireDirectNoticeUserBackedPrincipal(reboundContext); err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	normalized, reboundActor, reboundIdempotency, reboundRequest, err :=
		normalizeDirectNoticePublishCommand(scope, reboundContext.principal, cmd)
	if err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	if !bytes.Equal(reboundActor, actorFingerprint) ||
		!bytes.Equal(reboundIdempotency, idempotencyHash) ||
		!bytes.Equal(reboundRequest, requestDigest) {
		return DirectNoticePublishResult{}, false, communicationError(
			ErrCommunicationEvidenceUnknown,
			"replay authority changed request identity",
		)
	}
	candidate, found, err := m.lookupDirectNoticeReplay(
		ctx, scope, reboundContext.principal, normalized,
		reboundActor, reboundIdempotency, reboundRequest,
	)
	if errors.Is(err, store.ErrConflict) {
		if confirmErr := m.confirmDirectNoticeIdempotencyConflictWithAuthority(
			ctx, scope, question, rebound, reboundContext, normalized,
			reboundActor, reboundIdempotency, reboundRequest,
		); confirmErr != nil {
			return DirectNoticePublishResult{}, false, confirmErr
		}
		return DirectNoticePublishResult{}, false, fmt.Errorf(
			"%w: idempotency_key_reused", store.ErrConflict,
		)
	}
	if err != nil {
		return DirectNoticePublishResult{}, false, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication replay candidate is unavailable",
		)
	}
	if !found {
		return DirectNoticePublishResult{}, false, nil
	}
	if err := m.confirmDirectNoticeReplayWithAuthority(
		ctx, scope, question, rebound, reboundContext, normalized,
		reboundActor, reboundIdempotency, reboundRequest, candidate,
	); err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	return candidate, true, nil
}

// publishDirectNotice is the legacy private test seam. Production callers do
// not use it; it intentionally preserves pre-exact authorizer semantics until
// its remaining fixtures migrate behind request-bound authority.
func (m *Module) publishDirectNotice(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	cmd DirectNoticePublishCommand,
) (DirectNoticePublishResult, error) {
	cmd, actorFingerprint, idempotencyHash, requestDigest, err :=
		normalizeDirectNoticePublishCommand(scope, principal, cmd)
	if err != nil {
		return DirectNoticePublishResult{}, err
	}

	if replay, found, replayErr := m.lookupDirectNoticeReplay(
		ctx, scope, principal, cmd, actorFingerprint, idempotencyHash, requestDigest,
	); replayErr != nil {
		return DirectNoticePublishResult{}, replayErr
	} else if found {
		replay.Replayed = true
		return replay, nil
	}

	preflight, err := m.preflightDirectNoticePublish(
		ctx, scope, principal, cmd, actorFingerprint, idempotencyHash, requestDigest,
	)
	if err != nil {
		return DirectNoticePublishResult{}, err
	}

	var result DirectNoticePublishResult
	var replayed bool
	var auditGap bool
	err = m.mutateCommunication(ctx, scope, func(tx *communicationTx) error {
		if err := tx.lockTransaction(ctx, directNoticeIdempotencyLockKey(preflight)); err != nil {
			return err
		}
		replay, found, err := lookupDirectNoticeReplay(
			ctx,
			func(kind model.Kind) (communicationReadRepository, error) { return tx.repo(kind) },
			nil,
			nil,
			scope, principal, cmd,
			actorFingerprint, idempotencyHash, requestDigest,
		)
		if err != nil {
			return err
		}
		if found {
			result, replayed = replay, true
			return nil
		}
		if err := tx.lockTransaction(ctx, directNoticeMessageLockKey(scope, preflight.IDs.Message)); err != nil {
			return err
		}
		var applyErr error
		result, auditGap, applyErr = applyDirectNoticePublish(ctx, tx, preflight)
		return applyErr
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) || errors.Is(err, errDirectNoticeReplayNeedsFreshAudit) {
			if replay, found, replayErr := m.lookupDirectNoticeReplay(
				ctx, scope, principal, cmd, actorFingerprint, idempotencyHash, requestDigest,
			); replayErr != nil {
				return DirectNoticePublishResult{}, replayErr
			} else if found {
				replay.Replayed = true
				return replay, nil
			}
		}
		return DirectNoticePublishResult{}, err
	}
	if auditGap {
		return DirectNoticePublishResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "audit evidence could not be persisted",
		)
	}
	if replayed {
		result.Replayed = true
	}
	return result, nil
}

func normalizeDirectNoticePublishCommand(
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	cmd DirectNoticePublishCommand,
) (DirectNoticePublishCommand, []byte, []byte, []byte, error) {
	// Take ownership before validating or hashing. Request decoders normally
	// allocate these objects, but this service boundary must not let a caller
	// retain a block/reference alias and rewrite the normalized command later.
	cmd = cloneDirectNoticePublishCommand(cmd)
	if err := scope.Validate(); err != nil {
		return DirectNoticePublishCommand{}, nil, nil, nil, err
	}
	if err := ValidateCommunicationPrincipalForScope(principal, scope); err != nil {
		return DirectNoticePublishCommand{}, nil, nil, nil, err
	}
	if principal.UserID == "" {
		return DirectNoticePublishCommand{}, nil, nil, nil, communicationError(
			ErrInvalidCommunicationModel, "direct notice sender must be an authenticated User",
		)
	}
	_, idempotencyErr := model.ParseID(cmd.IdempotencyKey)
	if !validCanonicalCommunicationID(cmd.ChannelID) || cmd.Recipient.Kind != RecipientUser ||
		cmd.Recipient.Validate() != nil || cmd.IdempotencyKey == "" || idempotencyErr != nil {
		return DirectNoticePublishCommand{}, nil, nil, nil, communicationError(
			ErrInvalidCommunicationModel, "invalid direct notice command envelope",
		)
	}
	if _, err := CanonicalMessageContent(cmd.Content); err != nil {
		return DirectNoticePublishCommand{}, nil, nil, nil, err
	}
	if cmd.Urgency == "" {
		cmd.Urgency = UrgencyNormal
	}
	if !cmd.Urgency.Valid() {
		return DirectNoticePublishCommand{}, nil, nil, nil, communicationError(
			ErrInvalidCommunicationModel, "invalid direct notice urgency",
		)
	}
	if cmd.HTTPMethod == "" {
		cmd.HTTPMethod = http.MethodPost
	}
	if cmd.HTTPMethod != http.MethodPost {
		return DirectNoticePublishCommand{}, nil, nil, nil, communicationError(
			ErrInvalidCommunicationModel, "direct notice method must be POST",
		)
	}
	wantScope := fmt.Sprintf("%s %s;workspace=%s;channel=%s",
		cmd.HTTPMethod, directNoticePublishPath, scope.WorkspaceID, cmd.ChannelID)
	if cmd.CommandScope != "" && cmd.CommandScope != wantScope {
		return DirectNoticePublishCommand{}, nil, nil, nil, communicationError(
			ErrInvalidCommunicationModel, "direct notice command scope is not server-derived",
		)
	}
	cmd.CommandScope = wantScope
	if !validateOpaqueRef(cmd.CommandScope) {
		return DirectNoticePublishCommand{}, nil, nil, nil, communicationError(
			ErrInvalidCommunicationModel, "invalid direct notice command scope",
		)
	}
	if cmd.ExpectedPlanHash != "" {
		decoded, err := decodeHash(cmd.ExpectedPlanHash, true)
		if err != nil {
			return DirectNoticePublishCommand{}, nil, nil, nil, communicationError(
				ErrInvalidCommunicationModel, "invalid expected plan hash",
			)
		}
		// Hash spellings are aliases at the HTTP boundary. Normalize before
		// binding the idempotency request digest so an exact retry cannot be
		// rejected merely for changing prefix or hex case.
		cmd.ExpectedPlanHash = hex.EncodeToString(decoded)
	}
	actorBytes, err := canonicalJSON(struct {
		Kind CommunicationActorKind `json:"kind"`
		Ref  string                 `json:"ref"`
	}{Kind: ActorUser, Ref: principal.UserID.String()})
	if err != nil {
		return DirectNoticePublishCommand{}, nil, nil, nil, err
	}
	actorFingerprint := sha256.Sum256(actorBytes)
	idempotencyHash := sha256.Sum256([]byte(cmd.IdempotencyKey))
	requestBytes, err := canonicalJSON(directNoticeRequestDigestInput{
		Operation: directNoticePublishOperation, Method: cmd.HTTPMethod,
		CommandScope: cmd.CommandScope, ChannelID: cmd.ChannelID,
		Recipient: cmd.Recipient, Content: cmd.Content, Urgency: cmd.Urgency,
		ExpectedPlanHash: cmd.ExpectedPlanHash,
	})
	if err != nil {
		return DirectNoticePublishCommand{}, nil, nil, nil, err
	}
	requestDigest := sha256.Sum256(requestBytes)
	return cmd, actorFingerprint[:], idempotencyHash[:], requestDigest[:], nil
}

func (m *Module) preflightDirectNoticePublish(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	cmd DirectNoticePublishCommand,
	actorFingerprint []byte,
	idempotencyHash []byte,
	requestDigest []byte,
) (directNoticePublishPreflight, error) {
	if !communicationPortBound(m.communicationOperationAuthorizer) {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "message-send authorizer is unavailable",
		)
	}
	wantEntity := EntityRef{TenantID: scope.TenantID, Kind: channelKind,
		ID: cmd.ChannelID, WorkspaceID: scope.WorkspaceID}
	coreWitness, err := m.communicationOperationAuthorizer.AuthorizeEntityOperation(
		ctx, principal, wantEntity, CommunicationMessageSend,
	)
	if err != nil {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "message-send authorization is unavailable",
		)
	}
	return m.preflightDirectNoticePublishWithCore(
		ctx, scope, principal, coreWitness, cmd,
		actorFingerprint, idempotencyHash, requestDigest,
	)
}

// preflightDirectNoticePublishWithCore preserves the private legacy adapter's
// operation-authorizer witness. The exact path does not call this helper: it
// runs WithoutCore, then attaches the consumed binding witness only post-CAS in
// directNoticePublishPreflightWithBoundAuthority.
func (m *Module) preflightDirectNoticePublishWithCore(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	coreWitness ReadWitness,
	cmd DirectNoticePublishCommand,
	actorFingerprint []byte,
	idempotencyHash []byte,
	requestDigest []byte,
) (directNoticePublishPreflight, error) {
	return m.preflightDirectNoticePublishBody(
		ctx, scope, principal, coreWitness, true, cmd,
		actorFingerprint, idempotencyHash, requestDigest,
	)
}

func (m *Module) preflightDirectNoticePublishWithoutCore(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	cmd DirectNoticePublishCommand,
	actorFingerprint []byte,
	idempotencyHash []byte,
	requestDigest []byte,
) (directNoticePublishPreflight, error) {
	return m.preflightDirectNoticePublishBody(
		ctx, scope, principal, ReadWitness{}, false, cmd,
		actorFingerprint, idempotencyHash, requestDigest,
	)
}

func (m *Module) preflightDirectNoticePublishBody(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	coreWitness ReadWitness,
	requireCoreWitness bool,
	cmd DirectNoticePublishCommand,
	actorFingerprint []byte,
	idempotencyHash []byte,
	requestDigest []byte,
) (directNoticePublishPreflight, error) {
	cmd = cloneDirectNoticePublishCommand(cmd)
	coreWitness = cloneCommunicationRequestAuthorityWitness(coreWitness)
	sender := CommunicationActorRef{Kind: ActorUser, Ref: principal.UserID.String()}
	if !communicationPortBound(m.communicationAudienceAttestor) ||
		!communicationPortBound(m.communicationGrantClosure) {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice preflight ports are unavailable",
		)
	}

	// Core authorization is the first observer. A denied caller must not learn
	// whether the Channel exists, how it is protected, or whether a proposed
	// recipient is directory-eligible through the audience attestor.
	wantEntity := EntityRef{TenantID: scope.TenantID, Kind: channelKind,
		ID: cmd.ChannelID, WorkspaceID: scope.WorkspaceID}
	if requireCoreWitness && ValidateReadWitness(coreWitness) != nil {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "message-send authorization is unavailable",
		)
	}
	if requireCoreWitness && (coreWitness.Entity != wantEntity ||
		coreWitness.Operation != CommunicationMessageSend ||
		coreWitness.Principal != principal) {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "message-send authorization crosses request scope",
		)
	}
	coreObservedAt := m.clock.Now().Time()
	if requireCoreWitness && !communicationEvidenceCurrent(
		coreWitness.ObservedAt, coreWitness.FreshUntil, coreObservedAt,
	) {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "message-send authorization is stale",
		)
	}
	if requireCoreWitness {
		switch coreWitness.Outcome {
		case ReadUnknown:
			return directNoticePublishPreflight{}, communicationError(
				ErrCommunicationEvidenceUnknown, "message-send authorization is unavailable",
			)
		case ReadDeny:
			return directNoticePublishPreflight{}, communicationError(
				ErrCommunicationForbidden, "message-send authorization denied",
			)
		}
	}
	closure, err := m.communicationGrantClosure.ResolveChannelGrantSubjects(ctx, scope, principal)
	if err != nil {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "ChannelGrant subject closure failed",
		)
	}
	closure = cloneDirectNoticeChannelGrantSubjectClosure(closure)
	channel, grants, err := m.readDirectNoticePreflightState(ctx, scope, cmd.ChannelID)
	if err != nil {
		return directNoticePublishPreflight{}, err
	}
	if channel.TenantID != scope.TenantID || channel.WorkspaceID != scope.WorkspaceID ||
		channel.ID != cmd.ChannelID || channel.State != ChannelActive ||
		channel.ContentProtection != ContentProtectionStorage {
		return directNoticePublishPreflight{}, communicationError(
			ErrInvalidCommunicationTransition,
			"direct notice requires an active storage-protected Channel",
		)
	}
	// The closure is allowed to timestamp its evidence while resolving it. Take
	// a fresh evaluation instant after that call and the local read instead of
	// comparing a healthy closure against the earlier core-authorization sample.
	observedAt := m.clock.Now().Time()
	if (requireCoreWitness && !communicationEvidenceCurrent(
		coreWitness.ObservedAt, coreWitness.FreshUntil, observedAt,
	)) || !communicationEvidenceCurrent(
		closure.ObservedAt, closure.FreshUntil, observedAt,
	) {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "message-send authority changed during preflight",
		)
	}
	writeEvidence := EvaluateCurrentChannelGrant(ChannelGrantSnapshot{
		Verdict: VerdictClean, Code: "channel_grants_observed",
		ACLRevision: channel.ACLRevision, ObservedAt: observedAt, Grants: grants,
	}, scope.TenantID, scope.WorkspaceID, channel.ID, closure, ChannelGrantWrite, observedAt)
	if ValidateAuthorityEvidence(writeEvidence.Evidence) != nil {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "sender ChannelGrant.write evidence is malformed",
		)
	}
	switch evidenceVerdict(writeEvidence.Evidence) {
	case VerdictUnknown:
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "sender ChannelGrant.write is unavailable",
		)
	case VerdictBroken:
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationForbidden, "sender lacks ChannelGrant.write",
		)
	}

	ids := newDirectNoticePublishIDs()
	policy, err := ProtectedPayloadPolicyForChannel(channel)
	if err != nil {
		return directNoticePublishPreflight{}, err
	}
	schema, ok := PayloadSlotMessage.schema()
	if !ok {
		return directNoticePublishPreflight{}, communicationError(
			ErrInvalidCommunicationModel, "message content schema is unavailable",
		)
	}
	payload, err := PrepareProtectedPayload(ctx, nil, PayloadSlotMessage, policy, ContentAAD{
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, ChannelID: channel.ID,
		EntityKind: messageKind, EntityID: ids.Message, Schema: schema,
		ProtectionGeneration: channel.ProtectionGeneration,
	}, cmd.Content)
	if err != nil {
		return directNoticePublishPreflight{}, err
	}
	payload = cloneProtectedPayload(payload)

	required := channel.DefaultAckPolicy != AckPolicyNone
	selector := AudienceSelector{
		Kind: AudienceUser, Ref: cmd.Recipient.Ref, Required: required,
		WakePolicy: channel.DefaultWake,
	}
	requestedAt := observedAt
	audienceRequest := PublicationAudienceRequest{
		Scope: scope, ChannelID: channel.ID, ChannelACLRevision: channel.ACLRevision,
		RouteRevision: channel.RouteRevision, SubscriptionRevision: channel.SubscriptionRevision,
		MessageKind: MessageNotice, Urgency: cmd.Urgency, Sender: sender,
		SourceKind: RouteSourceUserMessage, ChannelDefaultWake: channel.DefaultWake,
		ContentProtection:    channel.ContentProtection,
		ProtectionGeneration: channel.ProtectionGeneration, RequestedAt: requestedAt,
		Selectors: []AudienceSelector{selector},
	}
	if err := ValidatePublicationAudienceRequest(audienceRequest); err != nil {
		return directNoticePublishPreflight{}, err
	}
	snapshot, attestation, err := m.communicationAudienceAttestor.AttestPublicationAudience(
		ctx, cloneDirectNoticePublicationAudienceRequest(audienceRequest),
	)
	if err != nil {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "publication audience attestation failed",
		)
	}
	snapshot = cloneDirectNoticeDirectorySnapshot(snapshot)
	attestation = cloneDirectNoticePublicationAudienceAttestation(attestation)
	if err := validateDirectNoticeSnapshot(audienceRequest, snapshot, attestation, cmd.Recipient); err != nil {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "publication audience evidence is malformed",
		)
	}
	if closure.DirectoryEpoch != snapshot.Epoch {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"ChannelGrant subject closure is stale against the audience snapshot",
		)
	}
	if requireCoreWitness &&
		!directNoticeCoreWitnessBindsDirectoryEpoch(coreWitness, scope.TenantID, snapshot.Epoch) {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"message-send authorization is not bound to the attested directory epoch",
		)
	}
	recipientPrincipal := CommunicationPrincipal{UserID: model.ID(cmd.Recipient.Ref)}
	recipientClosure, err := m.communicationGrantClosure.ResolveChannelGrantSubjects(
		ctx, scope, recipientPrincipal,
	)
	recipientClosure = cloneDirectNoticeChannelGrantSubjectClosure(recipientClosure)
	if err != nil || recipientClosure.Scope != scope ||
		recipientClosure.Principal != recipientPrincipal ||
		recipientClosure.DirectoryEpoch != snapshot.Epoch {
		return directNoticePublishPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"recipient ChannelGrant subject closure is unavailable or stale",
		)
	}
	return cloneDirectNoticePublishPreflight(directNoticePublishPreflight{
		Command: cmd, Scope: scope, Principal: principal, Sender: sender, Channel: channel,
		IDs: ids, Payload: payload, AudienceRequest: audienceRequest,
		AudienceAttestation: attestation, Snapshot: snapshot, GrantClosure: closure,
		RecipientGrantClosure: recipientClosure,
		CoreWitness:           coreWitness, ActorFingerprint: append([]byte(nil), actorFingerprint...),
		IdempotencyHash: append([]byte(nil), idempotencyHash...),
		RequestDigest:   append([]byte(nil), requestDigest...),
	}), nil
}

func directNoticeCoreWitnessBindsDirectoryEpoch(
	witness ReadWitness,
	tenantID model.TenantID,
	epoch int64,
) bool {
	wantID := model.ID(tenantID)
	for _, fact := range witness.Facts {
		if fact.Kind == model.DirectoryEpochKind && fact.ID == wantID && fact.Version == epoch {
			return true
		}
	}
	return false
}

func (m *Module) readDirectNoticePreflightState(
	ctx context.Context,
	scope DirectoryScopeRef,
	channelID model.ID,
) (Channel, []ChannelGrant, error) {
	var channel Channel
	var grants []ChannelGrant
	err := m.viewCommunication(ctx, scope, func(sc store.Scope) error {
		channelRepo, err := sc.Ext(channelKind)
		if err != nil {
			return err
		}
		record, err := channelRepo.Get(ctx, channelID)
		if err != nil {
			return err
		}
		channel, err = channelFromRecord(record)
		if err != nil {
			return err
		}
		grantRepo, err := sc.Ext(channelGrantKind)
		if err != nil {
			return err
		}
		rows, listErr := listDirectNoticeActiveGrantRecords(ctx, grantRepo, channelID)
		if listErr != nil {
			return listErr
		}
		for _, row := range rows {
			grant, decodeErr := channelGrantFromRecord(row)
			if decodeErr != nil || grant.ChannelID != channelID {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "ChannelGrant preflight snapshot is malformed",
				)
			}
			grants = append(grants, grant)
		}
		return nil
	})
	return channel, grants, err
}

func validateDirectNoticeSnapshot(
	request PublicationAudienceRequest,
	snapshot DirectorySnapshot,
	attestation PublicationAudienceAttestation,
	recipient RecipientRef,
) error {
	if err := ValidateDirectorySnapshotForSelectors(snapshot, request.Selectors); err != nil {
		return err
	}
	requestHash, err := CanonicalPublicationAudienceRequestHash(request)
	if err != nil {
		return err
	}
	snapshotHash, err := CanonicalPublicationAudienceSnapshotHash(snapshot)
	if err != nil {
		return err
	}
	if attestation.Scope != request.Scope || attestation.DirectoryEpoch != snapshot.Epoch ||
		attestation.ObservedAt != snapshot.ObservedAt || attestation.FreshUntil != snapshot.FreshUntil ||
		!bytes.Equal(attestation.RequestHash, requestHash) ||
		!bytes.Equal(attestation.SnapshotHash, snapshotHash) ||
		ValidateAuthorityEvidence(attestation.Evidence) != nil ||
		evidenceVerdict(attestation.Evidence) != VerdictClean ||
		len(snapshot.Recipients) != 1 || len(snapshot.Contributions) != 1 {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice audience attestation is not exact",
		)
	}
	resolved := snapshot.Contributions[0]
	if snapshot.Recipients[0].Recipient != recipient || resolved.Recipient != snapshot.Recipients[0] ||
		resolved.SelectorOrdinal != 1 || resolved.Selector != request.Selectors[0] ||
		resolved.CausalKind != CausalDirect || resolved.CausalRef != recipient.Ref ||
		resolved.CausalFact != nil || resolved.RouteRuleID != "" ||
		resolved.RouteRuleGeneration != 0 || resolved.OriginalSubscriber != nil ||
		resolved.SubscriptionID != "" || resolved.SubscriptionGeneration != 0 ||
		resolved.ObservedSessionSID != "" || resolved.ObservedClaimFence != 0 ||
		len(resolved.RouteReasons) != 1 || resolved.RouteReasons[0] != RouteReason("direct") ||
		resolved.Required != request.Selectors[0].Required ||
		resolved.WakePolicy != request.Selectors[0].WakePolicy {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "audience attestor returned a non-direct expansion",
		)
	}
	return nil
}

func directNoticeIdempotencyLockKey(preflight directNoticePublishPreflight) string {
	return directNoticeIdempotencyAuthorityLockKey(
		preflight.Scope, preflight.ActorFingerprint, preflight.IdempotencyHash,
	)
}

func directNoticeIdempotencyAuthorityLockKey(
	scope DirectoryScopeRef,
	actorFingerprint []byte,
	idempotencyHash []byte,
) string {
	return fmt.Sprintf("sessions_communication_command|%s|%x|%x",
		scope.TenantID, actorFingerprint, idempotencyHash)
}

func directNoticeMessageLockKey(scope DirectoryScopeRef, messageID model.ID) string {
	return fmt.Sprintf("sessions_message_publish|%s|%s", scope.TenantID, messageID)
}

type communicationReadRepository interface {
	Get(context.Context, model.ID) (model.Record, error)
	List(context.Context, model.Query) ([]model.Record, model.Page, error)
}

type directNoticeRecordLister interface {
	List(context.Context, model.Query) ([]model.Record, model.Page, error)
}

const (
	directNoticeGrantPageSize = 200
	directNoticeGrantSetBound = 4096
)

func listDirectNoticeActiveGrantRecords(
	ctx context.Context,
	repo directNoticeRecordLister,
	channelID model.ID,
) ([]model.Record, error) {
	query := model.Query{Filters: []model.Filter{
		{Column: colCommChannelID, Op: model.OpEq, Value: channelID.String()},
		{Column: colCommState, Op: model.OpEq, Value: string(ChannelGrantActive)},
	}, Limit: directNoticeGrantPageSize}
	var result []model.Record
	for {
		rows, page, err := repo.List(ctx, query)
		if err != nil {
			return nil, err
		}
		result = append(result, rows...)
		if len(result) > directNoticeGrantSetBound {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "active ChannelGrant snapshot exceeds bound",
			)
		}
		if !page.HasMore {
			return result, nil
		}
		if page.Cursor == "" {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"active ChannelGrant pagination lost its continuation",
			)
		}
		query.Cursor = page.Cursor
	}
}

type communicationReadRepositoryResolver func(model.Kind) (communicationReadRepository, error)

type directNoticeAuditVerifier func(
	context.Context,
	CommunicationPrincipal,
	CommunicationCommandReceipt,
) error

func (m *Module) lookupDirectNoticeReplay(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	cmd DirectNoticePublishCommand,
	actorFingerprint []byte,
	idempotencyHash []byte,
	requestDigest []byte,
) (DirectNoticePublishResult, bool, error) {
	var result DirectNoticePublishResult
	var found bool
	if err := scope.Validate(); err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	err := m.communicationData(scope.TenantID).View(ctx, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, scope.WorkspaceID)
		if err != nil {
			return err
		}
		audit := confined.Audit()
		if audit == nil {
			return communicationError(ErrCommunicationEvidenceUnknown, "audit evidence reader is unavailable")
		}
		directory, ok := confined.(store.DirectorySnapshotReader)
		if !ok {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "directory evidence reader is unavailable",
			)
		}
		var lookupErr error
		result, found, lookupErr = lookupDirectNoticeReplay(
			ctx,
			func(kind model.Kind) (communicationReadRepository, error) { return confined.Ext(kind) },
			directory,
			func(
				verifyCtx context.Context,
				verifyPrincipal CommunicationPrincipal,
				receipt CommunicationCommandReceipt,
			) error {
				return verifyDirectNoticeAuditAnchor(verifyCtx, audit, scope, verifyPrincipal, receipt)
			},
			scope, principal, cmd, actorFingerprint, idempotencyHash, requestDigest,
		)
		return lookupErr
	})
	return result, found, err
}

func lookupDirectNoticeReplay(
	ctx context.Context,
	resolve communicationReadRepositoryResolver,
	directory store.DirectorySnapshotReader,
	verifyAudit directNoticeAuditVerifier,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	cmd DirectNoticePublishCommand,
	actorFingerprint []byte,
	idempotencyHash []byte,
	requestDigest []byte,
) (DirectNoticePublishResult, bool, error) {
	receipt, found, err := readDirectNoticeReplayReceipt(
		ctx, resolve, scope, cmd, actorFingerprint, idempotencyHash, requestDigest,
	)
	if err != nil || !found {
		return DirectNoticePublishResult{}, false, err
	}
	// The same-transaction lookup only closes the idempotency race. Its View of
	// a graph that may be changing lifecycle is not guaranteed to be a stable
	// snapshot on every engine. Once a receipt exists, force reconstruction and
	// audit verification through the outer consistent View before reading any
	// mutable Message or Delivery row.
	if verifyAudit == nil {
		return DirectNoticePublishResult{}, false, errDirectNoticeReplayNeedsFreshAudit
	}
	result, err := directNoticeResultFromReceipt(ctx, resolve, directory, principal, cmd, receipt)
	if err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	if err := verifyAudit(ctx, principal, receipt); err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	return result, true, nil
}

func readDirectNoticeReplayReceipt(
	ctx context.Context,
	resolve communicationReadRepositoryResolver,
	scope DirectoryScopeRef,
	cmd DirectNoticePublishCommand,
	actorFingerprint []byte,
	idempotencyHash []byte,
	requestDigest []byte,
) (CommunicationCommandReceipt, bool, error) {
	receipt, found, err := readDirectNoticeReplayReceiptCandidate(
		ctx, resolve, scope, cmd, actorFingerprint, idempotencyHash,
	)
	if err != nil || !found {
		return CommunicationCommandReceipt{}, found, err
	}
	if !bytes.Equal(receipt.RequestDigest, requestDigest) {
		return CommunicationCommandReceipt{}, false, fmt.Errorf(
			"%w: idempotency_key_reused", store.ErrConflict,
		)
	}
	return receipt, true, nil
}

func readDirectNoticeReplayReceiptCandidate(
	ctx context.Context,
	resolve communicationReadRepositoryResolver,
	scope DirectoryScopeRef,
	cmd DirectNoticePublishCommand,
	actorFingerprint []byte,
	idempotencyHash []byte,
) (CommunicationCommandReceipt, bool, error) {
	repo, err := resolve(communicationCommandKind)
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		{Column: colCommActorFingerprint, Op: model.OpEq, Value: actorFingerprint},
		{Column: colCommCommandScope, Op: model.OpEq, Value: cmd.CommandScope},
		{Column: colCommIdempotencyKeyHash, Op: model.OpEq, Value: idempotencyHash},
	}, Limit: 2})
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	if len(rows) == 0 {
		return CommunicationCommandReceipt{}, false, nil
	}
	if len(rows) != 1 {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "communication receipt uniqueness is unavailable",
		)
	}
	receipt, err := communicationCommandReceiptFromRecord(rows[0])
	if err != nil {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "communication receipt cannot be decoded",
		)
	}
	if !bytes.Equal(receipt.ActorFingerprint, actorFingerprint) ||
		!bytes.Equal(receipt.IdempotencyKeyHash, idempotencyHash) ||
		receipt.CommandScope != cmd.CommandScope || receipt.TenantID != scope.TenantID ||
		receipt.WorkspaceID != scope.WorkspaceID {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "communication receipt crosses command scope",
		)
	}
	return receipt, true, nil
}

func verifyDirectNoticeAuditAnchor(
	ctx context.Context,
	audit store.AuditLog,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
	receipt CommunicationCommandReceipt,
) error {
	reader, ok := audit.(store.VerifiedAuditAnchorReader)
	if !ok {
		return communicationError(ErrCommunicationEvidenceUnknown, "verified audit reader is unavailable")
	}
	found, metaCanonical, present, err := reader.ReadVerifiedAuditAnchor(ctx, receipt.AuditSeq)
	if err != nil || !present {
		return communicationError(ErrCommunicationEvidenceUnknown, "receipt audit evidence is unavailable")
	}
	if found.Seq != receipt.AuditSeq || found.TenantID != scope.TenantID ||
		found.Actor != directNoticeActor(principal) || found.ActorKind != model.ActorUser ||
		found.Action != directNoticePublishAuditAction ||
		found.TargetKind != communicationCommandKind || found.TargetID != receipt.CommandID ||
		!bytes.Equal(found.PayloadHash, receipt.PlanHash) ||
		!bytes.Equal(found.Hash, receipt.AuditHash) ||
		!validateDirectNoticeAuditMeta(metaCanonical, scope, receipt) {
		return communicationError(ErrCommunicationEvidenceUnknown, "receipt audit anchor does not match")
	}
	return nil
}

func validateDirectNoticeAuditMeta(
	metaCanonical string,
	scope DirectoryScopeRef,
	receipt CommunicationCommandReceipt,
) bool {
	decoder := json.NewDecoder(bytes.NewBufferString(metaCanonical))
	decoder.UseNumber()
	var meta map[string]any
	if err := decoder.Decode(&meta); err != nil {
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return false
	}
	allowed := map[string]struct{}{
		"workspace_id": {}, "channel_id": {}, "command_scope": {},
		"workspace_binding_version": {}, "apply_commitment_version": {}, "apply_commitment": {},
		"delivery_count": {}, "required_count": {}, "trace_id": {}, "span_id": {},
	}
	for key := range meta {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	channelID := receipt.ResponseProjectionJSON.IDs["channel_id"]
	deliveryCount, deliveryOK := auditMetaInt64(meta["delivery_count"])
	requiredCount, requiredOK := auditMetaInt64(meta["required_count"])
	workspaceBinding, workspaceBindingOK := auditMetaInt64(meta["workspace_binding_version"])
	commitmentVersion, commitmentVersionOK := auditMetaInt64(meta["apply_commitment_version"])
	commitment, commitmentErr := directNoticeApplyCommitmentFromReceipt(receipt)
	commitmentText, commitmentOK := meta["apply_commitment"].(string)
	if meta["workspace_id"] != scope.WorkspaceID.String() ||
		meta["channel_id"] != channelID.String() ||
		meta["command_scope"] != receipt.CommandScope ||
		!workspaceBindingOK || workspaceBinding != 1 ||
		!commitmentVersionOK || commitmentVersion != directNoticeApplyCommitmentV1Version ||
		commitmentErr != nil ||
		!commitmentOK || commitmentText != hex.EncodeToString(commitment) ||
		!deliveryOK || deliveryCount != 1 || !requiredOK ||
		requiredCount != receipt.ResponseProjectionJSON.Counts["required"] {
		return false
	}
	trace, hasTrace := meta["trace_id"]
	span, hasSpan := meta["span_id"]
	return hasTrace == hasSpan && (!hasTrace ||
		(validAuditCorrelationID(trace, 32) && validAuditCorrelationID(span, 16)))
}

func auditMetaInt64(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	return parsed, err == nil
}

func validAuditCorrelationID(value any, size int) bool {
	text, ok := value.(string)
	if !ok || len(text) != size {
		return false
	}
	for _, char := range text {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func directNoticeResultProjectionFromReceipt(
	cmd DirectNoticePublishCommand,
	receipt CommunicationCommandReceipt,
) (DirectNoticePublishResult, FulfillmentProjection, error) {
	projection := receipt.ResponseProjectionJSON
	fulfillment, fulfillmentErr := directNoticeInitialFulfillmentFromProjection(projection)
	if receipt.ResultKind != string(messageKind) || receipt.HTTPStatus != http.StatusAccepted ||
		receipt.SealKeyVersion != "" || receipt.DigestKeyVersion != "" ||
		fulfillmentErr != nil || len(projection.IDs) != 4 || len(projection.Counts) != 7 ||
		len(projection.Digests) != 6 ||
		projection.IDs["channel_id"] != cmd.ChannelID ||
		projection.IDs["message_id"] != receipt.ResultID ||
		projection.IDs["event_id"] != receipt.EventID || projection.Version != 2 ||
		projection.State != string(MessagePublished) || projection.Counts["delivery_count"] != 1 ||
		projection.Counts["resolved_count"] != 1 ||
		len(projection.Digests["request"]) != sha256.Size ||
		len(projection.Digests["plan"]) != sha256.Size ||
		len(projection.Digests["audience"]) != sha256.Size ||
		len(projection.Digests["contributions"]) != sha256.Size ||
		len(projection.Digests["route_reasons"]) != sha256.Size ||
		len(projection.Digests["payload"]) != sha256.Size ||
		!bytes.Equal(projection.Digests["request"], receipt.RequestDigest) ||
		!bytes.Equal(projection.Digests["plan"], receipt.PlanHash) {
		return DirectNoticePublishResult{}, FulfillmentProjection{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication receipt projection is incomplete",
		)
	}
	return DirectNoticePublishResult{
		Verdict: VerdictClean, Code: "accepted", CommandID: receipt.CommandID,
		ChannelID: cmd.ChannelID, MessageID: receipt.ResultID,
		DeliveryID: projection.IDs["delivery_id"], EventID: receipt.EventID,
		Version: projection.Version, State: MessagePublished,
		DeliveryCount: 1, RequiredCount: fulfillment.Required,
		AckQuorum: fulfillment.Quorum, Fulfillment: fulfillment,
		AudienceHash:  hex.EncodeToString(projection.Digests["audience"]),
		PayloadDigest: hex.EncodeToString(projection.Digests["payload"]),
		PlanHash:      hex.EncodeToString(projection.Digests["plan"]), AuditSeq: receipt.AuditSeq,
	}, fulfillment, nil
}

func directNoticeResultFromReceipt(
	ctx context.Context,
	resolve communicationReadRepositoryResolver,
	directory store.DirectorySnapshotReader,
	principal CommunicationPrincipal,
	cmd DirectNoticePublishCommand,
	receipt CommunicationCommandReceipt,
) (DirectNoticePublishResult, error) {
	result, fulfillment, err := directNoticeResultProjectionFromReceipt(cmd, receipt)
	if err != nil {
		return DirectNoticePublishResult{}, err
	}
	projection := receipt.ResponseProjectionJSON
	messageRepo, err := resolve(messageKind)
	if err != nil {
		return DirectNoticePublishResult{}, err
	}
	messageRecord, err := messageRepo.Get(ctx, receipt.ResultID)
	if err != nil {
		return DirectNoticePublishResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "receipt Message anchor is unavailable",
		)
	}
	message, err := messageFromRecord(messageRecord, projection.Counts["required"])
	expectedContent, contentErr := CanonicalMessageContent(cmd.Content)
	expectedContentDigest := sha256.Sum256(expectedContent)
	if err != nil || message.ChannelID != cmd.ChannelID || message.Kind != MessageNotice ||
		contentErr != nil || message.WorkItemID != "" || message.ThreadID != message.ID ||
		message.ReplyToID != "" || message.SupersedesID != "" || message.OriginEventID != "" ||
		message.AutomationDepth != 0 || message.ExpiresAt != nil ||
		len(message.LabelsJSON) != 0 || len(message.LabelsHash) != 0 ||
		message.Urgency != cmd.Urgency ||
		message.Payload.Encoding != PayloadPlainJSON || len(message.Payload.PlainJSON) == 0 ||
		message.Payload.Sealed != nil || message.Payload.SealKeyVersion != "" ||
		message.Payload.DigestKeyVersion != "" ||
		!bytes.Equal(message.Payload.PlainJSON, expectedContent) ||
		!bytes.Equal(message.Payload.Digest, expectedContentDigest[:]) ||
		!oneOf(message.State, MessagePublished, MessageRetracted) ||
		message.Version < projection.Version || message.LastEventSeq < 1 ||
		message.Version != message.LastEventSeq+1 || message.UpdatedAt.Before(receipt.CompletedAt) ||
		!bytes.Equal(message.AudienceHash, projection.Digests["audience"]) ||
		!bytes.Equal(message.Payload.Digest, projection.Digests["payload"]) ||
		message.AckQuorum != fulfillment.Quorum ||
		!message.CreatedAt.Equal(receipt.CompletedAt) || message.PublishedAt == nil ||
		!message.PublishedAt.Equal(receipt.CompletedAt) ||
		!message.AvailableAt.Equal(receipt.CompletedAt) ||
		message.Sender != (CommunicationActorRef{Kind: ActorUser, Ref: principal.UserID.String()}) {
		return DirectNoticePublishResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "receipt Message anchor does not match",
		)
	}
	deliveryID := projection.IDs["delivery_id"]
	deliveryRepo, err := resolve(messageDeliveryKind)
	if err != nil {
		return DirectNoticePublishResult{}, err
	}
	deliveryRecord, err := deliveryRepo.Get(ctx, deliveryID)
	if err != nil {
		return DirectNoticePublishResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "receipt Delivery anchor is unavailable",
		)
	}
	delivery, err := messageDeliveryFromRecord(deliveryRecord)
	deliveryEvidence, deliveryEvidenceErr := canonicalDirectNoticeDeliveryEvidence(delivery)
	if err != nil || delivery.MessageID != message.ID || delivery.Recipient != cmd.Recipient ||
		delivery.Version < 1 || delivery.UpdatedAt.Before(receipt.CompletedAt) ||
		deliveryEvidenceErr != nil ||
		!bytes.Equal(deliveryEvidence, projection.Digests["route_reasons"]) ||
		delivery.ExpiresAt != nil || len(delivery.RouteReasons) != 1 ||
		delivery.RouteReasons[0] != RouteReason("direct") ||
		delivery.Required != (fulfillment.Required == 1) ||
		!delivery.CreatedAt.Equal(receipt.CompletedAt) ||
		!delivery.AvailableAt.Equal(receipt.CompletedAt) ||
		!directNoticeAckEvidenceMatches(message, delivery, fulfillment) {
		return DirectNoticePublishResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "receipt Delivery anchor does not match",
		)
	}
	deliveryRows, _, listErr := deliveryRepo.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colCommMessageID, Op: model.OpEq, Value: message.ID.String(),
	}}, Limit: 2})
	if listErr != nil || len(deliveryRows) != 1 ||
		deliveryRows[0].String(model.ColID) != delivery.ID.String() ||
		ValidateMessageDeliveryLineage(message, delivery) != nil {
		return DirectNoticePublishResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "receipt Delivery lineage is unavailable",
		)
	}
	if err := validateDirectNoticeReplayLifecycle(ctx, resolve, directory, message, delivery); err != nil {
		return DirectNoticePublishResult{}, err
	}
	if err := validateDirectNoticeAudienceAnchors(
		ctx, resolve, receipt, message, delivery, fulfillment,
	); err != nil {
		return DirectNoticePublishResult{}, err
	}
	if err := validateDirectNoticeEventAnchors(ctx, resolve, principal, receipt, result); err != nil {
		return DirectNoticePublishResult{}, err
	}
	return result, nil
}

// validateDirectNoticeReplayLifecycle distinguishes the immutable publish
// effect sealed by the receipt from legal state that may have accumulated
// afterwards. Replaying message.send always returns the original published/v2
// projection, but it must not accept a locally well-shaped effective Ack that
// has lost its append-only MessageAck evidence or an impossible expiry.
func validateDirectNoticeReplayLifecycle(
	ctx context.Context,
	resolve communicationReadRepositoryResolver,
	directory store.DirectorySnapshotReader,
	message Message,
	delivery MessageDelivery,
) error {
	acks, err := resolve(messageAckKind)
	if err != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "MessageAck evidence repository is unavailable",
		)
	}
	ackRows, _, err := acks.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colCommDeliveryID, Op: model.OpEq, Value: delivery.ID.String(),
	}}, Limit: 2})
	if err != nil || len(ackRows) > 1 {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "MessageAck evidence set is unavailable",
		)
	}
	var ack *MessageAck
	if len(ackRows) == 1 {
		decoded, decodeErr := messageAckFromRecord(ackRows[0])
		if decodeErr != nil || decoded.TenantID != delivery.TenantID ||
			decoded.WorkspaceID != delivery.WorkspaceID || decoded.DeliveryID != delivery.ID {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "MessageAck evidence crosses Delivery lineage",
			)
		}
		ack = &decoded
	}
	if ack != nil && !directNoticeAckTargetsRecipient(*ack, delivery.Recipient) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "MessageAck actor does not match Delivery recipient",
		)
	}
	switch delivery.State {
	case DeliveryAvailable:
		if ack != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "available Delivery carries MessageAck evidence",
			)
		}
		return nil
	case DeliveryAcknowledged:
		if ack == nil || ack.ID != delivery.AckID || ack.Late ||
			delivery.AcknowledgedAt == nil || ack.AcknowledgedAt != *delivery.AcknowledgedAt {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "effective MessageAck evidence does not match Delivery",
			)
		}
		return nil
	case DeliveryExpired:
		if !directNoticeDeliveryDeadlineElapsedAt(delivery, delivery.UpdatedAt) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "expired Delivery has no elapsed deadline",
			)
		}
		if ack != nil && (!ack.Late ||
			!directNoticeDeliveryDeadlineElapsedAt(delivery, ack.AcknowledgedAt)) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "expired Delivery carries a non-late MessageAck",
			)
		}
		return nil
	case DeliveryRetracted:
		if message.State != MessageRetracted || message.TerminalAt == nil ||
			delivery.UpdatedAt.Before(*message.TerminalAt) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "retracted Delivery lacks Message terminal lineage",
			)
		}
		if ack != nil && (!ack.Late || ack.AcknowledgedAt.Before(*message.TerminalAt)) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "retracted Delivery carries a non-late MessageAck",
			)
		}
		return nil
	case DeliveryUndeliverable:
		if ack != nil || directory == nil || delivery.Recipient.Kind != RecipientUser {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "undeliverable Delivery evidence is unavailable",
			)
		}
		principalID, parseErr := model.ParseID(delivery.Recipient.Ref)
		if parseErr != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "undeliverable recipient is non-canonical",
			)
		}
		principalRef := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser, PrincipalRef: principalID,
		}
		witness, found, readErr := directory.ReadDirectoryTombstone(ctx, principalRef)
		if readErr != nil || !found || witness.Principal != principalRef ||
			witness.TombstoneKind != delivery.RetirementTombstoneKind ||
			witness.TombstoneID != delivery.RetirementTombstoneID ||
			witness.TombstoneVersion != delivery.RetirementTombstoneVersion ||
			witness.RetirementEpoch != delivery.RetirementEpoch ||
			validateUndeliverableWitness(delivery, witness) != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "undeliverable tombstone evidence does not match",
			)
		}
		return nil
	default:
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Delivery lifecycle is unavailable",
		)
	}
}

func directNoticeAckTargetsRecipient(ack MessageAck, recipient RecipientRef) bool {
	if ack.OnBehalfOf != nil {
		return *ack.OnBehalfOf == recipient
	}
	return RecipientRef{Kind: RecipientKind(ack.Actor.Kind), Ref: ack.Actor.Ref} == recipient
}

func directNoticeDeliveryDeadlineElapsedAt(delivery MessageDelivery, observedAt time.Time) bool {
	return delivery.AckDueAt != nil && !observedAt.Before(*delivery.AckDueAt) ||
		delivery.ExpiresAt != nil && !observedAt.Before(*delivery.ExpiresAt)
}

func validateDirectNoticeAudienceAnchors(
	ctx context.Context,
	resolve communicationReadRepositoryResolver,
	receipt CommunicationCommandReceipt,
	message Message,
	delivery MessageDelivery,
	fulfillment FulfillmentProjection,
) error {
	audienceRepo, err := resolve(messageAudienceKind)
	if err != nil {
		return err
	}
	audienceRows, _, err := audienceRepo.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colCommMessageID, Op: model.OpEq, Value: message.ID.String(),
	}}, Limit: 2})
	if err != nil || len(audienceRows) != 1 {
		return communicationError(ErrCommunicationEvidenceUnknown, "receipt Audience anchor is unavailable")
	}
	audience, err := messageAudienceFromRecord(audienceRows[0])
	if err != nil {
		return communicationError(ErrCommunicationEvidenceUnknown, "receipt Audience anchor cannot be decoded")
	}
	contributionRepo, err := resolve(messageAudienceRecipientKind)
	if err != nil {
		return err
	}
	contributionRows, _, err := contributionRepo.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colCommMessageAudienceID, Op: model.OpEq, Value: audience.ID.String(),
	}}, Limit: 2})
	if err != nil || len(contributionRows) != 1 {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "receipt Audience contribution anchor is unavailable",
		)
	}
	contribution, err := messageAudienceRecipientFromRecord(contributionRows[0])
	if err != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "receipt Audience contribution cannot be decoded",
		)
	}
	wantSelector := AudienceSelector{
		Kind: AudienceUser, Ref: delivery.Recipient.Ref, Required: fulfillment.Required == 1,
		WakePolicy: delivery.WakePolicy,
	}
	if audience.MessageID != message.ID || audience.Ordinal != 1 ||
		audience.Selector != wantSelector || audience.RouteRuleID != "" ||
		audience.ResolvedCount != 1 || !audience.CreatedAt.Equal(receipt.CompletedAt) ||
		contribution.MessageAudienceID != audience.ID ||
		contribution.MessageDeliveryID != delivery.ID ||
		contribution.Recipient != delivery.Recipient ||
		contribution.RecipientEpoch != delivery.RecipientEpoch ||
		contribution.Required != delivery.Required || contribution.WakePolicy != delivery.WakePolicy ||
		contribution.Selector != wantSelector || contribution.CausalKind != CausalDirect ||
		contribution.CausalRef != delivery.Recipient.Ref || contribution.CausalFactKind != "" ||
		contribution.CausalFactID != "" || contribution.CausalFactVersion != 0 ||
		contribution.ObservedSessionSID != "" || contribution.ObservedClaimFence != 0 ||
		contribution.OriginalSubscriber != nil || contribution.SubscriptionID != "" ||
		contribution.SubscriptionGeneration != 0 || contribution.RouteRuleID != "" ||
		contribution.RouteRuleGeneration != 0 || len(contribution.RouteReasons) != 1 ||
		contribution.RouteReasons[0] != RouteReason("direct") ||
		!contribution.CreatedAt.Equal(receipt.CompletedAt) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "receipt Audience contribution is outside the direct slice",
		)
	}
	fold, err := FoldAudienceContributions([]MessageAudienceRecipient{contribution})
	if err != nil || fold.DeliveryID != delivery.ID || fold.Recipient != delivery.Recipient ||
		fold.Required != delivery.Required || fold.WakePolicy != delivery.WakePolicy ||
		!equalRouteReasons(fold.RouteReasons, normalizeRouteReasons(delivery.RouteReasons)) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "receipt Delivery does not equal the audience fold",
		)
	}
	wantHash, err := CanonicalMessageAudienceHash(
		message, []MessageAudience{audience}, []MessageAudienceRecipient{contribution},
	)
	graphIDs, graphErr := canonicalDirectNoticeAudienceGraphIDs(audience.ID, contribution.ID)
	if err != nil || graphErr != nil || !bytes.Equal(wantHash, message.AudienceHash) ||
		!bytes.Equal(graphIDs, receipt.ResponseProjectionJSON.Digests["contributions"]) ||
		!bytes.Equal(wantHash, receipt.ResponseProjectionJSON.Digests["audience"]) {
		return communicationError(ErrCommunicationEvidenceUnknown, "receipt Audience seal does not match")
	}
	return nil
}

func directNoticeInitialFulfillmentFromProjection(
	projection CommunicationCommandResponseProjection,
) (FulfillmentProjection, error) {
	for _, key := range []string{"required", "acknowledged", "viable", "unmet", "quorum"} {
		if _, present := projection.Counts[key]; !present {
			return FulfillmentProjection{}, communicationError(
				ErrCommunicationEvidenceUnknown, "receipt lacks initial fulfillment %s", key,
			)
		}
	}
	result := FulfillmentProjection{
		Required: projection.Counts["required"], Acknowledged: projection.Counts["acknowledged"],
		Viable: projection.Counts["viable"], Unmet: projection.Counts["unmet"],
		Quorum: projection.Counts["quorum"],
	}
	if result.Acknowledged != 0 || result.Unmet != 0 || result.Viable != result.Required {
		return FulfillmentProjection{}, communicationError(
			ErrCommunicationEvidenceUnknown, "receipt initial fulfillment is not an available vector",
		)
	}
	switch {
	case result.Required == 0 && result.Quorum == 0:
		result.State = FulfillmentNotRequired
	case result.Required == 1 && (result.Quorum == 0 || result.Quorum == 1):
		result.State = FulfillmentPending
	default:
		return FulfillmentProjection{}, communicationError(
			ErrCommunicationEvidenceUnknown, "receipt initial fulfillment is outside the direct slice",
		)
	}
	return result, nil
}

func directNoticeAckEvidenceMatches(
	message Message,
	delivery MessageDelivery,
	fulfillment FulfillmentProjection,
) bool {
	switch {
	case fulfillment.State == FulfillmentNotRequired:
		return message.AckPolicy == AckPolicyNone && message.AckQuorum == 0 &&
			message.AckDueAt == nil && delivery.AckDueAt == nil
	case fulfillment.State == FulfillmentPending && fulfillment.Quorum == 0:
		return message.AckPolicy == AckPolicyEachRequired && message.AckQuorum == 0 &&
			message.AckDueAt != nil && delivery.AckDueAt != nil &&
			message.AckDueAt.Equal(*delivery.AckDueAt) && message.AckDueAt.After(message.CreatedAt)
	case fulfillment.State == FulfillmentPending && fulfillment.Quorum == 1:
		return message.AckPolicy == AckPolicyQuorum && message.AckQuorum == 1 &&
			message.AckDueAt != nil && delivery.AckDueAt != nil &&
			message.AckDueAt.Equal(*delivery.AckDueAt) && message.AckDueAt.After(message.CreatedAt)
	default:
		return false
	}
}

func validateDirectNoticeEventAnchors(
	ctx context.Context,
	resolve communicationReadRepositoryResolver,
	principal CommunicationPrincipal,
	receipt CommunicationCommandReceipt,
	result DirectNoticePublishResult,
) error {
	events, err := resolve(workEventKind)
	if err != nil {
		return err
	}
	rows, _, err := events.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colEventID, Op: model.OpEq, Value: receipt.EventID.String(),
	}}, Limit: 2})
	if err != nil || len(rows) != 1 {
		return communicationError(ErrCommunicationEvidenceUnknown, "receipt Event anchor is unavailable")
	}
	event := rows[0]
	eventOccurredAt, occurredErr := model.ParseTimestamp(event.String(colEventOccurredAt))
	eventCreatedAt, createdErr := model.ParseTimestamp(event.String(model.ColCreatedAt))
	storedPayload := []byte(event.String(colEventPayload))
	decodedPayload, payloadErr := decodeDirectNoticeEventPayload(storedPayload)
	expectedPayload := directNoticeEventPayloadV1FromResult(result)
	if payloadErr != nil || occurredErr != nil || createdErr != nil ||
		!eventOccurredAt.Time().Equal(receipt.CompletedAt) ||
		!eventCreatedAt.Time().Equal(receipt.CompletedAt) ||
		event.String(colEventAggregateKind) != string(messageKind) ||
		event.String(colEventAggregateID) != result.MessageID.String() ||
		event.Int(colEventSeq) != 1 || event.String(colEventType) != communicationMessageAvailable ||
		event.String(colEventActorKind) != string(ActorUser) ||
		event.String(colEventActorRef) != principal.UserID.String() ||
		event.String(colEventCommandID) != receipt.CommandID.String() ||
		event.Int(colEventAuditSeq) != receipt.AuditSeq ||
		!bytes.Equal(event.Bytes(colEventAuditHash), receipt.AuditHash) ||
		decodedPayload != expectedPayload ||
		!bytes.Equal(event.Bytes(colEventPayloadHash), hashBytes(storedPayload)) {
		return communicationError(ErrCommunicationEvidenceUnknown, "receipt Event anchor does not match")
	}
	outboxes, err := resolve(workOutboxKind)
	if err != nil {
		return err
	}
	outboxRows, _, err := outboxes.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colOutboxEventID, Op: model.OpEq, Value: receipt.EventID.String(),
	}}, Limit: 2})
	if err != nil || len(outboxRows) != 1 || validateWorkOutboxEvidence(outboxRows[0]) != nil {
		return communicationError(ErrCommunicationEvidenceUnknown, "receipt Outbox anchor is unavailable")
	}
	outboxCreatedAt, err := model.ParseTimestamp(outboxRows[0].String(model.ColCreatedAt))
	if err != nil || !outboxCreatedAt.Time().Equal(receipt.CompletedAt) {
		return communicationError(ErrCommunicationEvidenceUnknown, "receipt Outbox time anchor does not match")
	}
	return nil
}

func directNoticeActor(principal CommunicationPrincipal) string {
	return "user:" + principal.UserID.String()
}

func cloneDirectNoticeBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

func sortedDirectNoticeFacts(facts []store.AuthorizationFactRef) []store.AuthorizationFactRef {
	result := append([]store.AuthorizationFactRef(nil), facts...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		if result[i].ID != result[j].ID {
			return result[i].ID.String() < result[j].ID.String()
		}
		return result[i].Version < result[j].Version
	})
	return result
}

func canonicalDirectNoticeSubjects(
	subjects []CommunicationSubjectRef,
) ([]CommunicationSubjectRef, error) {
	result := append([]CommunicationSubjectRef(nil), subjects...)
	seen := make(map[CommunicationSubjectRef]struct{}, len(result))
	for _, subject := range result {
		if err := subject.Validate(); err != nil {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"recipient ChannelGrant subject closure is malformed",
			)
		}
		if _, duplicate := seen[subject]; duplicate {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"recipient ChannelGrant subject closure repeats a subject",
			)
		}
		seen[subject] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Ref < result[j].Ref
	})
	return result, nil
}

func equalDirectNoticeChannel(left, right Channel) bool {
	return left.ID == right.ID && left.TenantID == right.TenantID &&
		left.WorkspaceID == right.WorkspaceID && left.Version == right.Version &&
		left.CreatedAt.Equal(right.CreatedAt) && left.UpdatedAt.Equal(right.UpdatedAt) &&
		left.Slug == right.Slug && left.Name == right.Name &&
		left.Description == right.Description && left.Kind == right.Kind &&
		left.State == right.State && left.Sensitivity == right.Sensitivity &&
		left.ContentProtection == right.ContentProtection &&
		left.ProtectionGeneration == right.ProtectionGeneration &&
		left.DefaultAckPolicy == right.DefaultAckPolicy &&
		left.DefaultAckTimeoutMS == right.DefaultAckTimeoutMS &&
		left.DefaultWake == right.DefaultWake &&
		left.RetentionPolicyRef == right.RetentionPolicyRef &&
		left.MaxFanout == right.MaxFanout &&
		left.MaxAutomationDepth == right.MaxAutomationDepth &&
		left.ACLRevision == right.ACLRevision &&
		left.RouteRevision == right.RouteRevision &&
		left.SubscriptionRevision == right.SubscriptionRevision
}
