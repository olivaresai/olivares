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
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type directNoticeLockedState struct {
	Channel       Channel
	RouteGuard    CommunicationGuard
	DeliveryGuard CommunicationGuard
	Grants        []ChannelGrant
	Labels        []ChannelLabelDefinition
	WriteEvidence BoundChannelReadEvidence
	WriteGrant    ChannelGrant
	ReadGrant     ChannelGrant
}

// directNoticePublishAuthorityLock is a one-shot proof that this exact
// transaction pinned the fact set derived from this exact preflight. Its only
// constructor performs the lock, so apply cannot accept a caller-supplied fact
// slice after an unrelated lockAuthoritySnapshot(nil).
type directNoticePublishAuthorityLock struct {
	consume func(
		*communicationTx,
		directNoticePublishPreflight,
		[]store.AuthorizationFactRef,
	) bool
}

type directNoticePublishAuthorityIdentity struct {
	BindingID     *communicationRequestAuthorityBindingID
	Scope         DirectoryScopeRef
	Principal     CommunicationPrincipal
	ChannelID     model.ID
	CoreEntity    EntityRef
	CoreOperation CommunicationOperation
	CorePrincipal CommunicationPrincipal
	IDs           directNoticePublishIDs
	Actor         [sha256.Size]byte
	Idempotency   [sha256.Size]byte
	Request       [sha256.Size]byte
	Preflight     [sha256.Size]byte
}

// directNoticePublishAuthorityPrincipal is the explicit canonical projection
// used only by the ephemeral one-shot authority token. CommunicationPrincipal
// deliberately omits every field from JSON, so embedding it directly would
// leave principal changes outside the commitment.
type directNoticePublishAuthorityPrincipal struct {
	UserID             model.ID `json:"user_id,omitempty"`
	AgentExternalID    string   `json:"agent_external_id,omitempty"`
	SessionID          string   `json:"session_id,omitempty"`
	SessionRunRef      string   `json:"session_run_ref,omitempty"`
	SessionFence       int64    `json:"session_fence,omitempty"`
	SessionWorkspaceID model.ID `json:"session_workspace_id,omitempty"`
	PurposeRestricted  bool     `json:"purpose_restricted"`
	System             bool     `json:"system"`
	SystemActorRef     string   `json:"system_actor_ref,omitempty"`
	SystemGrantAgentID model.ID `json:"system_grant_agent_id,omitempty"`
}

func directNoticePublishAuthorityPrincipalFrom(
	principal CommunicationPrincipal,
) directNoticePublishAuthorityPrincipal {
	return directNoticePublishAuthorityPrincipal{
		UserID: principal.UserID, AgentExternalID: principal.AgentExternalID,
		SessionID: principal.SessionID, SessionRunRef: principal.SessionRunRef,
		SessionFence: principal.SessionFence, SessionWorkspaceID: principal.SessionWorkspaceID,
		PurposeRestricted: principal.PurposeRestricted, System: principal.System,
		SystemActorRef: principal.SystemActorRef, SystemGrantAgentID: principal.SystemGrantAgentID,
	}
}

type directNoticePublishAuthorityCommand struct {
	ChannelID        model.ID       `json:"channel_id"`
	Recipient        RecipientRef   `json:"recipient"`
	Content          MessageContent `json:"content"`
	Urgency          MessageUrgency `json:"urgency"`
	IdempotencyKey   string         `json:"idempotency_key"`
	ExpectedPlanHash string         `json:"expected_plan_hash,omitempty"`
	HTTPMethod       string         `json:"http_method"`
	CommandScope     string         `json:"command_scope"`
}

func directNoticePublishAuthorityCommandFrom(
	command DirectNoticePublishCommand,
) directNoticePublishAuthorityCommand {
	return directNoticePublishAuthorityCommand{
		ChannelID: command.ChannelID, Recipient: command.Recipient,
		Content: command.Content, Urgency: command.Urgency,
		IdempotencyKey: command.IdempotencyKey, ExpectedPlanHash: command.ExpectedPlanHash,
		HTTPMethod: command.HTTPMethod, CommandScope: command.CommandScope,
	}
}

type directNoticePublishAuthorityIDs struct {
	Message      model.ID `json:"message"`
	Audience     model.ID `json:"audience"`
	Delivery     model.ID `json:"delivery"`
	Contribution model.ID `json:"contribution"`
	Command      model.ID `json:"command"`
	Event        model.ID `json:"event"`
	Receipt      model.ID `json:"receipt"`
}

func directNoticePublishAuthorityIDsFrom(ids directNoticePublishIDs) directNoticePublishAuthorityIDs {
	return directNoticePublishAuthorityIDs{
		Message: ids.Message, Audience: ids.Audience, Delivery: ids.Delivery,
		Contribution: ids.Contribution, Command: ids.Command, Event: ids.Event,
		Receipt: ids.Receipt,
	}
}

type directNoticePublishAuthorityClosure struct {
	Closure   ChannelGrantSubjectClosure            `json:"closure"`
	Principal directNoticePublishAuthorityPrincipal `json:"principal"`
}

type directNoticePublishAuthorityWitness struct {
	Witness   ReadWitness                           `json:"witness"`
	Principal directNoticePublishAuthorityPrincipal `json:"principal"`
}

type directNoticePublishAuthorityPreflight struct {
	Command               directNoticePublishAuthorityCommand   `json:"command"`
	Scope                 DirectoryScopeRef                     `json:"scope"`
	Principal             directNoticePublishAuthorityPrincipal `json:"principal"`
	Sender                CommunicationActorRef                 `json:"sender"`
	Channel               Channel                               `json:"channel"`
	IDs                   directNoticePublishAuthorityIDs       `json:"ids"`
	Payload               ProtectedPayload                      `json:"payload"`
	AudienceRequest       PublicationAudienceRequest            `json:"audience_request"`
	AudienceAttestation   PublicationAudienceAttestation        `json:"audience_attestation"`
	Snapshot              DirectorySnapshot                     `json:"snapshot"`
	GrantClosure          directNoticePublishAuthorityClosure   `json:"grant_closure"`
	RecipientGrantClosure directNoticePublishAuthorityClosure   `json:"recipient_grant_closure"`
	CoreWitness           directNoticePublishAuthorityWitness   `json:"core_witness"`
	ActorFingerprint      []byte                                `json:"actor_fingerprint"`
	IdempotencyHash       []byte                                `json:"idempotency_hash"`
	RequestDigest         []byte                                `json:"request_digest"`
}

const directNoticePublishAuthorityPreflightDomain = "olivares.sessions.direct-notice-publish.authority-preflight.v1"

func directNoticePublishAuthorityPreflightCommitment(
	preflight directNoticePublishPreflight,
) ([sha256.Size]byte, error) {
	projection := directNoticePublishAuthorityPreflight{
		Command:             directNoticePublishAuthorityCommandFrom(preflight.Command),
		Scope:               preflight.Scope,
		Principal:           directNoticePublishAuthorityPrincipalFrom(preflight.Principal),
		Sender:              preflight.Sender,
		Channel:             preflight.Channel,
		IDs:                 directNoticePublishAuthorityIDsFrom(preflight.IDs),
		Payload:             preflight.Payload,
		AudienceRequest:     preflight.AudienceRequest,
		AudienceAttestation: preflight.AudienceAttestation,
		Snapshot:            preflight.Snapshot,
		GrantClosure: directNoticePublishAuthorityClosure{
			Closure:   preflight.GrantClosure,
			Principal: directNoticePublishAuthorityPrincipalFrom(preflight.GrantClosure.Principal),
		},
		RecipientGrantClosure: directNoticePublishAuthorityClosure{
			Closure: preflight.RecipientGrantClosure,
			Principal: directNoticePublishAuthorityPrincipalFrom(
				preflight.RecipientGrantClosure.Principal,
			),
		},
		CoreWitness: directNoticePublishAuthorityWitness{
			Witness:   preflight.CoreWitness,
			Principal: directNoticePublishAuthorityPrincipalFrom(preflight.CoreWitness.Principal),
		},
		ActorFingerprint: preflight.ActorFingerprint,
		IdempotencyHash:  preflight.IdempotencyHash,
		RequestDigest:    preflight.RequestDigest,
	}
	raw, err := canonicalJSON(projection)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, directNoticePublishAuthorityPreflightDomain)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(raw)
	var commitment [sha256.Size]byte
	copy(commitment[:], hash.Sum(nil))
	return commitment, nil
}

func equalDirectNoticePublishAuthorityCommand(
	left DirectNoticePublishCommand,
	right DirectNoticePublishCommand,
) bool {
	leftRaw, leftErr := canonicalJSON(directNoticePublishAuthorityCommandFrom(left))
	rightRaw, rightErr := canonicalJSON(directNoticePublishAuthorityCommandFrom(right))
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}

func cloneDirectNoticeMessageContent(content MessageContent) MessageContent {
	result := content
	if content.Blocks == nil {
		return result
	}
	result.Blocks = make([]MessageContentBlock, len(content.Blocks))
	copy(result.Blocks, content.Blocks)
	for index := range result.Blocks {
		if content.Blocks[index].Reference == nil {
			continue
		}
		reference := *content.Blocks[index].Reference
		result.Blocks[index].Reference = &reference
	}
	return result
}

func cloneDirectNoticePublishCommand(command DirectNoticePublishCommand) DirectNoticePublishCommand {
	result := command
	result.Content = cloneDirectNoticeMessageContent(command.Content)
	return result
}

func cloneDirectNoticePublicationAudienceRequest(
	request PublicationAudienceRequest,
) PublicationAudienceRequest {
	result := request
	result.LabelsJSON = append(json.RawMessage(nil), request.LabelsJSON...)
	result.LabelsHash = cloneDirectNoticeBytes(request.LabelsHash)
	result.MentionedRecipients = append([]RecipientRef(nil), request.MentionedRecipients...)
	result.Selectors = append([]AudienceSelector(nil), request.Selectors...)
	return result
}

func cloneDirectNoticePublicationAudienceAttestation(
	attestation PublicationAudienceAttestation,
) PublicationAudienceAttestation {
	result := attestation
	result.RequestHash = cloneDirectNoticeBytes(attestation.RequestHash)
	result.SnapshotHash = cloneDirectNoticeBytes(attestation.SnapshotHash)
	return result
}

func cloneDirectNoticeRecipientSnapshot(snapshot RecipientSnapshot) RecipientSnapshot {
	result := snapshot
	if snapshot.Tombstone != nil {
		tombstone := *snapshot.Tombstone
		result.Tombstone = &tombstone
	}
	return result
}

func cloneDirectNoticeResolvedAudienceContribution(
	contribution ResolvedAudienceContribution,
) ResolvedAudienceContribution {
	result := contribution
	result.Recipient = cloneDirectNoticeRecipientSnapshot(contribution.Recipient)
	result.RouteReasons = append([]RouteReason(nil), contribution.RouteReasons...)
	if contribution.CausalFact != nil {
		causalFact := *contribution.CausalFact
		result.CausalFact = &causalFact
	}
	if contribution.OriginalSubscriber != nil {
		originalSubscriber := *contribution.OriginalSubscriber
		result.OriginalSubscriber = &originalSubscriber
	}
	return result
}

func cloneDirectNoticeDirectorySnapshot(snapshot DirectorySnapshot) DirectorySnapshot {
	result := snapshot
	result.Selectors = append([]AudienceSelector(nil), snapshot.Selectors...)
	result.Recipients = make([]RecipientSnapshot, len(snapshot.Recipients))
	for index := range snapshot.Recipients {
		result.Recipients[index] = cloneDirectNoticeRecipientSnapshot(snapshot.Recipients[index])
	}
	result.Contributions = make([]ResolvedAudienceContribution, len(snapshot.Contributions))
	for index := range snapshot.Contributions {
		result.Contributions[index] = cloneDirectNoticeResolvedAudienceContribution(
			snapshot.Contributions[index],
		)
	}
	result.RosterHash = cloneDirectNoticeBytes(snapshot.RosterHash)
	return result
}

func cloneDirectNoticeChannelGrantSubjectClosure(
	closure ChannelGrantSubjectClosure,
) ChannelGrantSubjectClosure {
	result := closure
	result.Subjects = append([]CommunicationSubjectRef(nil), closure.Subjects...)
	return result
}

func cloneDirectNoticePublishPreflight(
	preflight directNoticePublishPreflight,
) directNoticePublishPreflight {
	result := preflight
	result.Command = cloneDirectNoticePublishCommand(preflight.Command)
	result.Payload = cloneProtectedPayload(preflight.Payload)
	result.AudienceRequest = cloneDirectNoticePublicationAudienceRequest(preflight.AudienceRequest)
	result.AudienceAttestation = cloneDirectNoticePublicationAudienceAttestation(
		preflight.AudienceAttestation,
	)
	result.Snapshot = cloneDirectNoticeDirectorySnapshot(preflight.Snapshot)
	result.GrantClosure = cloneDirectNoticeChannelGrantSubjectClosure(preflight.GrantClosure)
	result.RecipientGrantClosure = cloneDirectNoticeChannelGrantSubjectClosure(
		preflight.RecipientGrantClosure,
	)
	result.CoreWitness = cloneCommunicationRequestAuthorityWitness(preflight.CoreWitness)
	result.ActorFingerprint = cloneDirectNoticeBytes(preflight.ActorFingerprint)
	result.IdempotencyHash = cloneDirectNoticeBytes(preflight.IdempotencyHash)
	result.RequestDigest = cloneDirectNoticeBytes(preflight.RequestDigest)
	return result
}

func directNoticePublishAuthorityIdentityFor(
	preflight directNoticePublishPreflight,
) (directNoticePublishAuthorityIdentity, error) {
	wantEntity := EntityRef{
		TenantID: preflight.Scope.TenantID, Kind: channelKind,
		ID: preflight.Command.ChannelID, WorkspaceID: preflight.Scope.WorkspaceID,
	}
	if preflight.Scope.Validate() != nil ||
		ValidateCommunicationPrincipalForScope(preflight.Principal, preflight.Scope) != nil ||
		preflight.CoreWitness.Entity != wantEntity ||
		preflight.CoreWitness.Operation != CommunicationMessageSend ||
		preflight.CoreWitness.Principal != preflight.Principal ||
		preflight.Channel.ID != preflight.Command.ChannelID ||
		preflight.Channel.TenantID != preflight.Scope.TenantID ||
		preflight.Channel.WorkspaceID != preflight.Scope.WorkspaceID ||
		!validDirectNoticePublishAuthorityIDs(preflight.IDs) {
		return directNoticePublishAuthorityIdentity{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"publish authority identity crosses its exact preflight",
		)
	}
	wantCommand, wantActor, wantIdempotency, wantRequest, err := normalizeDirectNoticePublishCommand(
		preflight.Scope, preflight.Principal, preflight.Command,
	)
	if err != nil || !equalDirectNoticePublishAuthorityCommand(wantCommand, preflight.Command) ||
		!bytes.Equal(wantActor, preflight.ActorFingerprint) ||
		!bytes.Equal(wantIdempotency, preflight.IdempotencyHash) ||
		!bytes.Equal(wantRequest, preflight.RequestDigest) {
		return directNoticePublishAuthorityIdentity{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"publish authority identity does not match the normalized request",
		)
	}
	if len(preflight.ActorFingerprint) != sha256.Size ||
		len(preflight.IdempotencyHash) != sha256.Size ||
		len(preflight.RequestDigest) != sha256.Size {
		return directNoticePublishAuthorityIdentity{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"publish authority identity has malformed request digests",
		)
	}
	preflightCommitment, err := directNoticePublishAuthorityPreflightCommitment(preflight)
	if err != nil {
		return directNoticePublishAuthorityIdentity{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"publish authority preflight cannot be committed",
		)
	}
	identity := directNoticePublishAuthorityIdentity{
		BindingID: preflight.bindingID,
		Scope:     preflight.Scope, Principal: preflight.Principal,
		ChannelID:     preflight.Command.ChannelID,
		CoreEntity:    preflight.CoreWitness.Entity,
		CoreOperation: preflight.CoreWitness.Operation,
		CorePrincipal: preflight.CoreWitness.Principal,
		IDs:           preflight.IDs,
	}
	copy(identity.Actor[:], preflight.ActorFingerprint)
	copy(identity.Idempotency[:], preflight.IdempotencyHash)
	copy(identity.Request[:], preflight.RequestDigest)
	identity.Preflight = preflightCommitment
	return identity, nil
}

func validDirectNoticePublishAuthorityIDs(ids directNoticePublishIDs) bool {
	values := [...]model.ID{
		ids.Message, ids.Audience, ids.Delivery, ids.Contribution,
		ids.Command, ids.Event, ids.Receipt,
	}
	for i, value := range values {
		if !validCanonicalCommunicationID(value) {
			return false
		}
		for j := range i {
			if value == values[j] {
				return false
			}
		}
	}
	return true
}

func lockDirectNoticePublishAuthoritySnapshot(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticePublishPreflight,
) (directNoticePublishAuthorityLock, error) {
	if (preflight.bindingID == nil) != (tx == nil || tx.requestBindingID == nil) ||
		(preflight.bindingID != nil && preflight.bindingID != tx.requestBindingID) {
		return directNoticePublishAuthorityLock{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"publish authority binding crosses its transaction",
		)
	}
	authorityIdentity, err := directNoticePublishAuthorityIdentityFor(preflight)
	if err != nil {
		return directNoticePublishAuthorityLock{}, err
	}
	authorityFacts, err := directNoticePublishAuthorityFacts(preflight)
	if err != nil {
		return directNoticePublishAuthorityLock{}, err
	}
	if tx == nil {
		return directNoticePublishAuthorityLock{},
			communicationTransactionUnavailable("publish authority snapshot", nil)
	}
	if err := tx.lockAuthoritySnapshot(ctx, authorityFacts); err != nil {
		return directNoticePublishAuthorityLock{}, err
	}
	sealedTx := tx
	sealedIdentity := authorityIdentity
	sealedFacts := append([]store.AuthorizationFactRef(nil), authorityFacts...)
	var consumed atomic.Bool
	return directNoticePublishAuthorityLock{consume: func(
		candidateTx *communicationTx,
		candidatePreflight directNoticePublishPreflight,
		candidateFacts []store.AuthorizationFactRef,
	) bool {
		candidateIdentity, err := directNoticePublishAuthorityIdentityFor(candidatePreflight)
		if err != nil {
			return false
		}
		return candidateTx == sealedTx &&
			candidateIdentity == sealedIdentity &&
			equalDirectNoticeAuthorityFacts(candidateFacts, sealedFacts) &&
			consumed.CompareAndSwap(false, true)
	}}, nil
}

func applyDirectNoticePublish(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticePublishPreflight,
) (DirectNoticePublishResult, bool, error) {
	authorityLock, err := lockDirectNoticePublishAuthoritySnapshot(ctx, tx, preflight)
	if err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	// K3 fixes the authority snapshot before Channel, guard and grant row locks so
	// their lock order cannot invert with a concurrent authority writer. The
	// authoritative clock is resampled after those potentially blocking locks,
	// before any temporal evidence is accepted or any effect is written.
	return applyDirectNoticePublishAfterAuthoritySnapshot(ctx, tx, preflight, authorityLock)
}

// applyDirectNoticePublishAfterAuthoritySnapshot is shared by the legacy
// private seam above and the exact request-bound service. The caller must have
// pinned this exact fact set before taking any local lock; the checks here make
// the split incapable of silently accepting a weaker or omitted snapshot.
func applyDirectNoticePublishAfterAuthoritySnapshot(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticePublishPreflight,
	authorityLock directNoticePublishAuthorityLock,
) (DirectNoticePublishResult, bool, error) {
	authorityFacts, err := directNoticePublishAuthorityFacts(preflight)
	if err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	if authorityLock.consume == nil || !authorityLock.consume(tx, preflight, authorityFacts) {
		return DirectNoticePublishResult{}, false,
			communicationTransactionUnavailable("publish authority snapshot", nil)
	}
	locked, err := lockDirectNoticePublishState(ctx, tx, preflight)
	if err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	plan, err := materializeDirectNoticePublish(tx.now.Time(), preflight, locked)
	if err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	if len(plan.RequiredClaims) != 0 || plan.GuardAdvance == nil ||
		len(plan.Audiences) != 1 || len(plan.Contributions) != 1 || len(plan.Deliveries) != 1 ||
		plan.Before.ID != preflight.IDs.Message || plan.After.ID != preflight.IDs.Message ||
		plan.Before.Version != 1 || plan.After.Version != 2 ||
		plan.Before.State != MessageDraft || plan.After.State != MessagePublished ||
		plan.Before.LastEventSeq != 0 || plan.After.LastEventSeq != 1 {
		return DirectNoticePublishResult{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice planner returned a non-atomic shape",
		)
	}
	if !equalDirectNoticeAuthorityFacts(authorityFacts, plan.Facts) {
		return DirectNoticePublishResult{}, false, communicationError(
			ErrCommunicationEvidenceUnknown,
			"publish planner authority facts differ from the locked snapshot",
		)
	}
	planHash, err := canonicalDirectNoticePublishPlanHash(preflight, locked, plan)
	if err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	if preflight.Command.ExpectedPlanHash != "" {
		expected, decodeErr := decodeHash(preflight.Command.ExpectedPlanHash, true)
		if decodeErr != nil || !bytes.Equal(expected, planHash) {
			return DirectNoticePublishResult{}, false, communicationError(
				ErrCommunicationPlanChanged,
				"expected plan hash does not match the locked publish plan",
			)
		}
	}
	fulfillment, err := projectInitialDirectNoticeFulfillment(plan, tx.now.Time())
	if err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	applyCommitment, err := directNoticeApplyCommitmentFromPlan(
		preflight, plan, planHash, fulfillment, tx.now.Time(),
	)
	if err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	audit, err := tx.appendAudit(ctx, model.AuditDraft{
		Actor: directNoticeActor(preflight.Principal), ActorKind: model.ActorUser,
		Action: directNoticePublishAuditAction, TargetKind: communicationCommandKind,
		TargetID: preflight.IDs.Command, PayloadHash: cloneDirectNoticeBytes(planHash),
		Meta: map[string]any{
			"workspace_id":             preflight.Scope.WorkspaceID.String(),
			"channel_id":               preflight.Channel.ID.String(),
			"command_scope":            preflight.Command.CommandScope,
			"delivery_count":           int64(1),
			"required_count":           plan.RequiredCount,
			"apply_commitment_version": directNoticeCurrentApplyCommitmentVersion,
			"apply_commitment":         hex.EncodeToString(applyCommitment),
		},
	})
	if err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	if audit.Seq == 0 {
		return DirectNoticePublishResult{}, true, nil
	}
	if len(audit.Hash) != sha256.Size {
		return DirectNoticePublishResult{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "audit append returned an invalid anchor",
		)
	}
	result := DirectNoticePublishResult{
		Verdict: VerdictClean, Code: "accepted", CommandID: preflight.IDs.Command,
		ChannelID: preflight.Channel.ID, MessageID: plan.After.ID,
		DeliveryID: plan.Deliveries[0].ID, EventID: preflight.IDs.Event,
		Version: plan.After.Version, State: plan.After.State, DeliveryCount: 1,
		RequiredCount: plan.RequiredCount, AckQuorum: plan.After.AckQuorum,
		Fulfillment:   fulfillment,
		AudienceHash:  hex.EncodeToString(plan.After.AudienceHash),
		PayloadDigest: hex.EncodeToString(plan.After.Payload.Digest),
		PlanHash:      hex.EncodeToString(planHash), AuditSeq: audit.Seq,
	}
	if err := persistDirectNoticePublish(ctx, tx, preflight, plan, planHash, audit, result); err != nil {
		return DirectNoticePublishResult{}, false, err
	}
	return result, false, nil
}

func projectInitialDirectNoticeFulfillment(
	plan MessagePublishPlan,
	dbNow time.Time,
) (FulfillmentProjection, error) {
	digest, err := CanonicalFulfillmentDeliverySetDigest(plan.Deliveries)
	if err != nil {
		return FulfillmentProjection{}, err
	}
	scope := DirectoryScopeRef{
		TenantID: plan.After.TenantID, WorkspaceID: plan.After.WorkspaceID,
	}
	const evidenceRef = "same_tx_initial_fulfillment"
	return ProjectMessageFulfillment(plan.After, plan.Deliveries, FulfillmentDeliverySetWitness{
		Scope: scope, MessageID: plan.After.ID, DeliveryCount: int64(len(plan.Deliveries)),
		RequiredCount: plan.RequiredCount, Digest: digest, ObservedAt: dbNow,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "deliveries_locked", EvidenceRef: evidenceRef,
		},
		EvidenceRef: evidenceRef,
	}, dbNow)
}

func lockDirectNoticePublishState(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticePublishPreflight,
) (directNoticeLockedState, error) {
	channelRecord, err := tx.lockRecord(ctx, channelKind, preflight.Channel.ID)
	if err != nil {
		return directNoticeLockedState{}, err
	}
	channel, err := channelFromRecord(channelRecord)
	if err != nil {
		return directNoticeLockedState{}, err
	}
	if !equalDirectNoticeChannel(channel, preflight.Channel) || channel.State != ChannelActive ||
		channel.ContentProtection != ContentProtectionStorage {
		return directNoticeLockedState{}, fmt.Errorf("%w: channel_publish_fence_changed", store.ErrConflict)
	}
	routeGuard, err := lockCommunicationGuardByKind(ctx, tx, CommunicationGuardRouteRevision)
	if err != nil {
		return directNoticeLockedState{}, err
	}
	deliveryGuard, err := lockCommunicationGuardByKind(ctx, tx, CommunicationGuardDeliverySequence)
	if err != nil {
		return directNoticeLockedState{}, err
	}
	if channel.RouteRevision == math.MaxInt64 || routeGuard.NextSeq <= channel.RouteRevision {
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication guards do not match Channel revisions",
		)
	}
	epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
	if err != nil {
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationEvidenceUnknown, "directory epoch is unavailable in mutation",
		)
	}
	if err := epoch.Validate(); err != nil || epoch.TenantID != preflight.Scope.TenantID ||
		epoch.Version != preflight.Snapshot.Epoch {
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationSnapshotStale, "directory epoch changed after preflight",
		)
	}
	grants, err := lockCurrentChannelGrants(ctx, tx, channel.ID)
	if err != nil {
		return directNoticeLockedState{}, err
	}
	labels, err := lockCurrentChannelLabelDefinitions(ctx, tx, channel.ID)
	if err != nil {
		return directNoticeLockedState{}, err
	}
	if err := tx.lockAuditAppends(ctx); err != nil {
		return directNoticeLockedState{}, err
	}
	if err := tx.refreshNow(ctx); err != nil {
		return directNoticeLockedState{}, err
	}
	if channel.UpdatedAt.After(tx.now.Time()) || routeGuard.LastDBTime.After(tx.now.Time()) ||
		deliveryGuard.LastDBTime.After(tx.now.Time()) {
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication locks carry future database time",
		)
	}
	if !communicationEvidenceCurrent(
		preflight.CoreWitness.ObservedAt, preflight.CoreWitness.FreshUntil, tx.now.Time(),
	) || !communicationEvidenceCurrent(
		preflight.GrantClosure.ObservedAt, preflight.GrantClosure.FreshUntil, tx.now.Time(),
	) || !communicationEvidenceCurrent(
		preflight.RecipientGrantClosure.ObservedAt,
		preflight.RecipientGrantClosure.FreshUntil,
		tx.now.Time(),
	) || !communicationEvidenceCurrent(
		preflight.Snapshot.ObservedAt, preflight.Snapshot.FreshUntil, tx.now.Time(),
	) {
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"direct notice authority evidence expired while waiting for locks",
		)
	}
	grantSnapshot := ChannelGrantSnapshot{
		Verdict: VerdictClean, Code: "channel_grants_locked", ACLRevision: channel.ACLRevision,
		ObservedAt: tx.now.Time(), Grants: grants,
	}
	writeEvidence := EvaluateCurrentChannelGrant(
		grantSnapshot, preflight.Scope.TenantID, preflight.Scope.WorkspaceID,
		channel.ID, preflight.GrantClosure, ChannelGrantWrite, tx.now.Time(),
	)
	if err := ValidateAuthorityEvidence(writeEvidence.Evidence); err != nil {
		return directNoticeLockedState{}, err
	}
	switch evidenceVerdict(writeEvidence.Evidence) {
	case VerdictUnknown:
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationEvidenceUnknown, "sender ChannelGrant.write is unavailable",
		)
	case VerdictBroken:
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationForbidden, "sender lacks ChannelGrant.write",
		)
	}
	var writeGrant ChannelGrant
	for _, grant := range grants {
		if grant.ID == writeEvidence.GrantID && grant.Version == writeEvidence.GrantVersion {
			writeGrant = grant
			break
		}
	}
	if writeGrant.ID == "" {
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationEvidenceUnknown, "selected sender grant is absent from locked set",
		)
	}
	recipientReadEvidence := EvaluateCurrentChannelGrant(
		grantSnapshot, preflight.Scope.TenantID, preflight.Scope.WorkspaceID,
		channel.ID, preflight.RecipientGrantClosure, ChannelGrantRead, tx.now.Time(),
	)
	if err := ValidateAuthorityEvidence(recipientReadEvidence.Evidence); err != nil {
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationEvidenceUnknown, "recipient ChannelGrant.read evidence is malformed",
		)
	}
	switch evidenceVerdict(recipientReadEvidence.Evidence) {
	case VerdictUnknown:
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationEvidenceUnknown, "recipient ChannelGrant.read is unavailable",
		)
	case VerdictBroken:
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationForbidden, "recipient lacks ChannelGrant.read",
		)
	}
	var readGrant ChannelGrant
	for _, grant := range grants {
		if grant.ID == recipientReadEvidence.GrantID && grant.Version == recipientReadEvidence.GrantVersion {
			readGrant = grant
			break
		}
	}
	if readGrant.ID == "" {
		return directNoticeLockedState{}, communicationError(
			ErrCommunicationEvidenceUnknown, "selected recipient grant is absent from locked set",
		)
	}
	return directNoticeLockedState{
		Channel: channel, RouteGuard: routeGuard, DeliveryGuard: deliveryGuard,
		Grants: grants, Labels: labels, WriteEvidence: writeEvidence, WriteGrant: writeGrant,
		ReadGrant: readGrant,
	}, nil
}

func lockCurrentChannelLabelDefinitions(
	ctx context.Context,
	tx *communicationTx,
	channelID model.ID,
) ([]ChannelLabelDefinition, error) {
	repo, err := tx.repo(channelLabelDefinitionKind)
	if err != nil {
		return nil, err
	}
	query := model.Query{
		Filters: []model.Filter{
			{Column: colCommChannelID, Op: model.OpEq, Value: channelID.String()},
			{Column: colCommState, Op: model.OpEq, Value: string(ChannelLabelActive)},
		},
		Limit: 64,
	}
	var rows []model.Record
	for {
		pageRows, page, listErr := repo.List(ctx, query)
		if listErr != nil {
			return nil, listErr
		}
		rows = append(rows, pageRows...)
		if len(rows) > 64 {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "active Channel label set exceeds direct publish bound",
			)
		}
		if !page.HasMore {
			break
		}
		// The declared active-label bound is exactly one page. Continuing would
		// only turn an oversized set into a long-lived transaction; a missing
		// cursor must not make the truncated first page look complete either.
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "active Channel label set exceeds direct publish bound",
		)
	}
	definitions := make([]ChannelLabelDefinition, 0, len(rows))
	for _, row := range rows {
		id, parseErr := model.ParseID(row.String(model.ColID))
		if parseErr != nil || !validCanonicalCommunicationID(id) {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "Channel label definition has an invalid identity",
			)
		}
		locked, lockErr := tx.lockRecord(ctx, channelLabelDefinitionKind, id)
		if lockErr != nil {
			return nil, lockErr
		}
		definition, decodeErr := channelLabelDefinitionFromRecord(locked)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if definition.ChannelID != channelID {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "Channel label definition changed Channel while locked",
			)
		}
		if definition.State == ChannelLabelActive {
			definitions = append(definitions, definition)
		}
	}
	return definitions, nil
}

func directNoticePublishAuthorityFacts(
	preflight directNoticePublishPreflight,
) ([]store.AuthorizationFactRef, error) {
	directoryFact, err := DirectorySnapshotAuthorityFact(preflight.Snapshot)
	if err != nil {
		return nil, err
	}
	facts, err := canonicalAuthorizationFactUnion(append(
		append([]store.AuthorizationFactRef(nil), preflight.CoreWitness.Facts...), directoryFact,
	))
	if err != nil {
		return nil, err
	}
	for _, fact := range facts {
		switch fact.Kind {
		case "core.identity", "core.agent", model.DirectoryEpochKind,
			model.AuthorizationEpochKind, "governance.nhi_lifecycle":
		default:
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"operation authority fact %q cannot be locked by this store edition",
				fact.Kind,
			)
		}
	}
	return facts, nil
}

func equalDirectNoticeAuthorityFacts(left, right []store.AuthorizationFactRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func lockCommunicationGuardByKind(
	ctx context.Context,
	tx *communicationTx,
	kind CommunicationGuardKind,
) (CommunicationGuard, error) {
	repo, err := tx.repo(communicationGuardKind)
	if err != nil {
		return CommunicationGuard{}, err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colCommGuardKind, Op: model.OpEq, Value: string(kind),
	}}, Limit: 2})
	if err != nil {
		return CommunicationGuard{}, err
	}
	if len(rows) != 1 {
		return CommunicationGuard{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication guard %q is unavailable", kind,
		)
	}
	id := model.ID(rows[0].String(model.ColID))
	locked, err := tx.lockRecord(ctx, communicationGuardKind, id)
	if err != nil {
		return CommunicationGuard{}, err
	}
	guard, err := communicationGuardFromRecord(locked)
	if err != nil {
		return CommunicationGuard{}, err
	}
	if guard.Kind != kind {
		return CommunicationGuard{}, communicationError(
			ErrCommunicationEvidenceUnknown, "locked communication guard changed identity",
		)
	}
	return guard, nil
}

func lockCurrentChannelGrants(
	ctx context.Context,
	tx *communicationTx,
	channelID model.ID,
) ([]ChannelGrant, error) {
	repo, err := tx.repo(channelGrantKind)
	if err != nil {
		return nil, err
	}
	rows, err := listDirectNoticeActiveGrantRecords(ctx, repo, channelID)
	if err != nil {
		return nil, err
	}
	var ids []model.ID
	for _, row := range rows {
		id, parseErr := model.ParseID(row.String(model.ColID))
		if parseErr != nil || !validCanonicalCommunicationID(id) {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown,
				"ChannelGrant snapshot contains invalid ID",
			)
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	grants := make([]ChannelGrant, 0, len(ids))
	seen := make(map[model.ID]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "ChannelGrant snapshot repeats an ID",
			)
		}
		seen[id] = struct{}{}
		row, lockErr := tx.lockRecord(ctx, channelGrantKind, id)
		if lockErr != nil {
			return nil, lockErr
		}
		grant, decodeErr := channelGrantFromRecord(row)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if grant.ChannelID != channelID {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "ChannelGrant changed Channel while locked",
			)
		}
		if grant.State != ChannelGrantActive {
			return nil, communicationError(
				ErrCommunicationEvidenceUnknown, "active ChannelGrant changed state while locked",
			)
		}
		grants = append(grants, grant)
	}
	return grants, nil
}

func materializeDirectNoticePublish(
	dbNow time.Time,
	preflight directNoticePublishPreflight,
	locked directNoticeLockedState,
) (MessagePublishPlan, error) {
	required := locked.Channel.DefaultAckPolicy != AckPolicyNone
	ackQuorum := int64(0)
	var ackDueAt *time.Time
	if required {
		if locked.Channel.DefaultAckTimeoutMS <= 0 ||
			locked.Channel.DefaultAckTimeoutMS > math.MaxInt64/int64(time.Millisecond) {
			return MessagePublishPlan{}, communicationError(
				ErrInvalidCommunicationModel, "required Channel Ack timeout is not positive",
			)
		}
		due := dbNow.Add(time.Duration(locked.Channel.DefaultAckTimeoutMS) * time.Millisecond)
		if !due.After(dbNow) {
			return MessagePublishPlan{}, communicationError(
				ErrInvalidCommunicationModel, "Channel Ack timeout overflows DB time",
			)
		}
		ackDueAt = &due
		if locked.Channel.DefaultAckPolicy == AckPolicyQuorum {
			ackQuorum = 1
		}
	}
	entity := CommunicationEntity{
		ID: preflight.IDs.Message, TenantID: preflight.Scope.TenantID,
		WorkspaceID: preflight.Scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
	}
	draft := Message{
		MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: entity, UpdatedAt: dbNow},
		ChannelID:                  locked.Channel.ID, ThreadID: preflight.IDs.Message, Kind: MessageNotice,
		State: MessageDraft, Sender: preflight.Sender, Payload: cloneProtectedPayload(preflight.Payload),
		Urgency: preflight.Command.Urgency, AckPolicy: locked.Channel.DefaultAckPolicy,
		AckQuorum: ackQuorum, AvailableAt: dbNow, AckDueAt: ackDueAt,
		AutomationDepth: 0, LastEventSeq: 0,
	}
	resolved := preflight.Snapshot.Contributions[0]
	audienceEntity := AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
		ID: preflight.IDs.Audience, TenantID: preflight.Scope.TenantID,
		WorkspaceID: preflight.Scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
	}}
	selectorBytes, err := canonicalJSON(resolved.Selector)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	selectorHash := sha256.Sum256(selectorBytes)
	audience := MessageAudience{
		AppendOnlyCommunicationEntity: audienceEntity, MessageID: draft.ID, Ordinal: 1,
		Selector: resolved.Selector, ChannelACLRevision: locked.Channel.ACLRevision,
		RouteRevision:        locked.Channel.RouteRevision,
		SubscriptionRevision: locked.Channel.SubscriptionRevision,
		DirectoryEpoch:       preflight.Snapshot.Epoch,
		DirectorySnapshotAt:  preflight.Snapshot.ObservedAt, ResolvedCount: 1,
		SelectorHash: selectorHash[:],
	}
	contribution := MessageAudienceRecipient{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: preflight.IDs.Contribution, TenantID: preflight.Scope.TenantID,
			WorkspaceID: preflight.Scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
		}},
		MessageAudienceID: audience.ID, MessageDeliveryID: preflight.IDs.Delivery,
		Recipient: resolved.Recipient.Recipient, RecipientEpoch: resolved.Recipient.RecipientEpoch,
		Required: resolved.Required, WakePolicy: resolved.WakePolicy,
		RouteReasons: append([]RouteReason(nil), resolved.RouteReasons...), Selector: resolved.Selector,
		DirectoryEpoch: preflight.Snapshot.Epoch, ChannelACLRevision: locked.Channel.ACLRevision,
		RouteRevision:        locked.Channel.RouteRevision,
		SubscriptionRevision: locked.Channel.SubscriptionRevision,
		CausalKind:           resolved.CausalKind, CausalRef: resolved.CausalRef,
	}
	contribution.CausalArcHash, err = CanonicalAudienceCausalArcHash(contribution)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	audience.ResolvedHash, err = canonicalResolvedAudienceHash(audience, []MessageAudienceRecipient{contribution})
	if err != nil {
		return MessagePublishPlan{}, err
	}
	fold, err := FoldAudienceContributions([]MessageAudienceRecipient{contribution})
	if err != nil {
		return MessagePublishPlan{}, err
	}
	delivery := MessageDelivery{
		MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: preflight.IDs.Delivery, TenantID: preflight.Scope.TenantID,
			WorkspaceID: preflight.Scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
		}, UpdatedAt: dbNow},
		MessageID: draft.ID, Recipient: fold.Recipient,
		RecipientEpoch: resolved.Recipient.RecipientEpoch, Required: fold.Required,
		RouteReasons: append([]RouteReason(nil), fold.RouteReasons...), WakePolicy: fold.WakePolicy,
		State: DeliveryAvailable, AvailableAt: dbNow,
	}
	if required {
		delivery.AckDueAt = ackDueAt
	}
	directoryFact, err := DirectorySnapshotAuthorityFact(preflight.Snapshot)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	labels := ChannelLabelSnapshot{
		Scope: preflight.Scope, ChannelID: locked.Channel.ID,
		RouteRevision: locked.Channel.RouteRevision, ObservedAt: dbNow,
		Definitions: append([]ChannelLabelDefinition(nil), locked.Labels...), SameTransaction: true,
		Evidence: AuthorityEvidence{Verdict: VerdictClean, Code: "channel_labels_locked",
			EvidenceRef: "same_tx_channel_labels"},
	}
	return PlanMessagePublish(MessagePublishInput{
		Draft: draft, Channel: locked.Channel, AudienceRequest: preflight.AudienceRequest,
		AudienceAttestation: preflight.AudienceAttestation, Snapshot: preflight.Snapshot,
		Audiences:     []MessageAudience{audience},
		Contributions: []MessageAudienceRecipient{contribution},
		Deliveries:    []MessageDelivery{delivery}, Labels: labels,
		SendGate: SendGateEvidence{
			Scope: preflight.Scope, ChannelID: locked.Channel.ID,
			ChannelACLRevision: locked.Channel.ACLRevision, DBNow: dbNow,
			Principal: preflight.Principal, Core: preflight.CoreWitness,
			DirectoryEpoch: directoryFact, CurrentChannelWriteGrant: locked.WriteEvidence,
		},
		Principal: preflight.Principal, Sender: preflight.Sender,
		SourceKind: RouteSourceUserMessage, DeliverySequenceGuard: locked.DeliveryGuard,
		DBNow: dbNow,
	})
}

func cloneProtectedPayload(payload ProtectedPayload) ProtectedPayload {
	result := payload
	result.PlainJSON = append(json.RawMessage(nil), payload.PlainJSON...)
	result.Digest = cloneDirectNoticeBytes(payload.Digest)
	if payload.Sealed != nil {
		sealed := *payload.Sealed
		sealed.Ciphertext = cloneDirectNoticeBytes(payload.Sealed.Ciphertext)
		result.Sealed = &sealed
	}
	return result
}

type directNoticeChannelPlanProjection struct {
	ID                   model.ID           `json:"id"`
	Version              int64              `json:"version"`
	State                ChannelState       `json:"state"`
	Sensitivity          ChannelSensitivity `json:"sensitivity"`
	ContentProtection    ContentProtection  `json:"content_protection"`
	ProtectionGeneration int64              `json:"protection_generation"`
	DefaultAckPolicy     AckPolicy          `json:"default_ack_policy"`
	DefaultAckTimeoutMS  int64              `json:"default_ack_timeout_ms"`
	DefaultWake          WakePolicy         `json:"default_wake"`
	MaxFanout            int64              `json:"max_fanout"`
	MaxAutomationDepth   int64              `json:"max_automation_depth"`
	ACLRevision          int64              `json:"acl_revision"`
	RouteRevision        int64              `json:"route_revision"`
	SubscriptionRevision int64              `json:"subscription_revision"`
}

type directNoticeGrantPlanProjection struct {
	ID         model.ID                `json:"id"`
	Version    int64                   `json:"version"`
	Generation int64                   `json:"generation"`
	Subject    CommunicationSubjectRef `json:"subject"`
}

type directNoticeGuardPlanProjection struct {
	ID      model.ID               `json:"id"`
	Version int64                  `json:"version"`
	Kind    CommunicationGuardKind `json:"kind"`
	NextSeq int64                  `json:"next_seq"`
}

type directNoticePublishPlanDigestInput struct {
	Operation              string                            `json:"operation"`
	Sender                 CommunicationActorRef             `json:"sender"`
	Permission             CommunicationOperation            `json:"permission"`
	AuditAction            string                            `json:"audit_action"`
	AuditTargetKind        model.Kind                        `json:"audit_target_kind"`
	RequestDigest          []byte                            `json:"request_digest"`
	PayloadDigest          []byte                            `json:"payload_digest"`
	Channel                directNoticeChannelPlanProjection `json:"channel"`
	DirectoryEpoch         int64                             `json:"directory_epoch"`
	RosterHash             []byte                            `json:"roster_hash"`
	Recipient              RecipientRef                      `json:"recipient"`
	RecipientEpoch         int64                             `json:"recipient_epoch"`
	WriteGrant             directNoticeGrantPlanProjection   `json:"write_grant"`
	ReadGrant              directNoticeGrantPlanProjection   `json:"read_grant"`
	RecipientGrantSubjects []CommunicationSubjectRef         `json:"recipient_grant_subjects"`
	DeliveryGuard          directNoticeGuardPlanProjection   `json:"delivery_guard"`
	Facts                  []store.AuthorizationFactRef      `json:"facts"`
	RequiredCount          int64                             `json:"required_count"`
	AckQuorum              int64                             `json:"ack_quorum"`
	WakePolicy             WakePolicy                        `json:"wake_policy"`
	EventType              string                            `json:"event_type"`
	EventPayloadVersion    int64                             `json:"event_payload_version"`
	RowEffects             []string                          `json:"row_effects"`
}

func canonicalDirectNoticePublishPlanHash(
	preflight directNoticePublishPreflight,
	locked directNoticeLockedState,
	plan MessagePublishPlan,
) ([]byte, error) {
	requestDigest, err := canonicalDirectNoticeSemanticRequestDigest(preflight.Command)
	if err != nil {
		return nil, err
	}
	recipientGrantSubjects, err := canonicalDirectNoticeSubjects(
		preflight.RecipientGrantClosure.Subjects,
	)
	if err != nil {
		return nil, err
	}
	input := directNoticePublishPlanDigestInput{
		Operation: directNoticePublishOperation, Sender: preflight.Sender,
		Permission: CommunicationMessageSend, AuditAction: directNoticePublishAuditAction,
		AuditTargetKind: communicationCommandKind,
		RequestDigest:   requestDigest,
		PayloadDigest:   cloneDirectNoticeBytes(plan.After.Payload.Digest),
		Channel: directNoticeChannelPlanProjection{
			ID: locked.Channel.ID, Version: locked.Channel.Version, State: locked.Channel.State,
			Sensitivity:          locked.Channel.Sensitivity,
			ContentProtection:    locked.Channel.ContentProtection,
			ProtectionGeneration: locked.Channel.ProtectionGeneration,
			DefaultAckPolicy:     locked.Channel.DefaultAckPolicy,
			DefaultAckTimeoutMS:  locked.Channel.DefaultAckTimeoutMS,
			DefaultWake:          locked.Channel.DefaultWake, MaxFanout: locked.Channel.MaxFanout,
			MaxAutomationDepth: locked.Channel.MaxAutomationDepth,
			ACLRevision:        locked.Channel.ACLRevision, RouteRevision: locked.Channel.RouteRevision,
			SubscriptionRevision: locked.Channel.SubscriptionRevision,
		},
		DirectoryEpoch: preflight.Snapshot.Epoch,
		RosterHash:     cloneDirectNoticeBytes(preflight.Snapshot.RosterHash),
		Recipient:      preflight.Command.Recipient,
		RecipientEpoch: preflight.Snapshot.Recipients[0].RecipientEpoch,
		WriteGrant: directNoticeGrantPlanProjection{ID: locked.WriteGrant.ID,
			Version: locked.WriteGrant.Version, Generation: locked.WriteGrant.Generation,
			Subject: locked.WriteGrant.Subject},
		ReadGrant: directNoticeGrantPlanProjection{ID: locked.ReadGrant.ID,
			Version: locked.ReadGrant.Version, Generation: locked.ReadGrant.Generation,
			Subject: locked.ReadGrant.Subject},
		RecipientGrantSubjects: recipientGrantSubjects,
		DeliveryGuard: directNoticeGuardPlanProjection{ID: locked.DeliveryGuard.ID,
			Version: locked.DeliveryGuard.Version, Kind: locked.DeliveryGuard.Kind,
			NextSeq: locked.DeliveryGuard.NextSeq},
		Facts: sortedDirectNoticeFacts(plan.Facts), RequiredCount: plan.RequiredCount,
		AckQuorum: plan.After.AckQuorum, WakePolicy: plan.Deliveries[0].WakePolicy,
		EventType: communicationMessageAvailable, EventPayloadVersion: directNoticeCurrentEventPayloadVersion,
		RowEffects: []string{
			"message:draft", "guard:delivery_sequence", "audience:create",
			"delivery:create", "contribution:create", "message:published",
			"work_event:create", "work_outbox:create", "command_receipt:create",
		},
	}
	raw, err := canonicalJSON(input)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func canonicalDirectNoticeSemanticRequestDigest(cmd DirectNoticePublishCommand) ([]byte, error) {
	raw, err := canonicalJSON(directNoticeRequestDigestInput{
		Operation: directNoticePublishOperation, Method: cmd.HTTPMethod,
		CommandScope: cmd.CommandScope, ChannelID: cmd.ChannelID,
		Recipient: cmd.Recipient, Content: cmd.Content, Urgency: cmd.Urgency,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func persistDirectNoticePublish(
	ctx context.Context,
	tx *communicationTx,
	preflight directNoticePublishPreflight,
	plan MessagePublishPlan,
	planHash []byte,
	audit model.AuditEvent,
	result DirectNoticePublishResult,
) error {
	draftRecord, err := messageToRecord(plan.Before, plan.RequiredCount)
	if err != nil {
		return err
	}
	if _, err = tx.createWithID(ctx, messageKind, plan.Before.ID, draftRecord); err != nil {
		return err
	}
	guardRecord, err := communicationGuardToRecord(plan.GuardAdvance.After)
	if err != nil {
		return err
	}
	guardRecord[model.ColVersion] = plan.GuardAdvance.Before.Version
	if _, err = tx.update(ctx, communicationGuardKind, guardRecord); err != nil {
		return err
	}
	for _, audience := range plan.Audiences {
		record, encodeErr := messageAudienceToRecord(audience)
		if encodeErr != nil {
			return encodeErr
		}
		if _, createErr := tx.createWithID(ctx, messageAudienceKind, audience.ID, record); createErr != nil {
			return createErr
		}
	}
	for _, delivery := range plan.Deliveries {
		record, encodeErr := messageDeliveryToRecord(delivery)
		if encodeErr != nil {
			return encodeErr
		}
		if _, createErr := tx.createWithID(ctx, messageDeliveryKind, delivery.ID, record); createErr != nil {
			return createErr
		}
	}
	for _, contribution := range plan.Contributions {
		record, encodeErr := messageAudienceRecipientToRecord(contribution)
		if encodeErr != nil {
			return encodeErr
		}
		if _, createErr := tx.createWithID(
			ctx, messageAudienceRecipientKind, contribution.ID, record,
		); createErr != nil {
			return createErr
		}
	}
	publishedRecord, err := messageToRecord(plan.After, plan.RequiredCount)
	if err != nil {
		return err
	}
	publishedRecord[model.ColVersion] = plan.Before.Version
	if _, err = tx.update(ctx, messageKind, publishedRecord); err != nil {
		return err
	}
	eventPayload, err := canonicalDirectNoticeEventPayload(result)
	if err != nil || len(eventPayload) > 16*1024 {
		return communicationError(ErrInvalidCommunicationModel, "direct notice Event payload is invalid")
	}
	if _, err = tx.create(ctx, workEventKind, model.Record{
		colWorkWorkspaceID: preflight.Scope.WorkspaceID.String(),
		colEventID:         preflight.IDs.Event.String(), colEventAggregateKind: string(messageKind),
		colEventAggregateID: plan.After.ID.String(), colEventSeq: int64(1),
		colEventType: communicationMessageAvailable, colEventActorKind: string(ActorUser),
		colEventActorRef:   preflight.Principal.UserID.String(),
		colEventOccurredAt: tx.now.String(), colEventPayload: string(eventPayload),
		colEventPayloadHash: hashBytes(eventPayload), colEventCommandID: preflight.IDs.Command.String(),
		colEventAuditSeq: audit.Seq, colEventAuditHash: cloneDirectNoticeBytes(audit.Hash),
	}); err != nil {
		return err
	}
	if _, err = tx.create(ctx, workOutboxKind, model.Record{
		colWorkWorkspaceID: preflight.Scope.WorkspaceID.String(),
		colOutboxEventID:   preflight.IDs.Event.String(), colOutboxState: "pending",
		colOutboxAttempts: int64(0), colOutboxNextAttemptAt: tx.now.String(),
		colOutboxClaimOwner: nil, colOutboxClaimUntil: nil,
		colOutboxPublishedAt: nil, colOutboxLastOutcome: nil,
	}); err != nil {
		return err
	}
	receipt, err := buildDirectNoticeReceipt(
		tx.now.Time(), preflight, plan, planHash, audit, result,
	)
	if err != nil {
		return err
	}
	receiptRecord, err := communicationCommandReceiptToRecord(receipt)
	if err != nil {
		return err
	}
	_, err = tx.createWithID(ctx, communicationCommandKind, receipt.ID, receiptRecord)
	return err
}

// directNoticeEventPayloadV1 and its nested projection are immutable wire
// types. Future writers dispatch on schema_version and add a new struct rather
// than editing these fields, so retained v1 outboxes/receipts remain replayable.
type directNoticeEventPayloadV1 struct {
	SchemaVersion int64                          `json:"schema_version"`
	Command       string                         `json:"command"`
	ResultKind    string                         `json:"result_kind"`
	ResultID      model.ID                       `json:"result_id"`
	ChannelID     model.ID                       `json:"channel_id"`
	MessageID     model.ID                       `json:"message_id"`
	MessageKind   MessageKind                    `json:"message_kind"`
	State         MessageState                   `json:"state"`
	Version       int64                          `json:"version"`
	EventSequence int64                          `json:"event_sequence"`
	DeliveryCount int64                          `json:"delivery_count"`
	RequiredCount int64                          `json:"required_count"`
	AckQuorum     int64                          `json:"ack_quorum"`
	Fulfillment   directNoticeEventFulfillmentV1 `json:"fulfillment"`
	AudienceHash  string                         `json:"audience_hash"`
	PayloadDigest string                         `json:"payload_digest"`
	PlanHash      string                         `json:"plan_hash"`
}

type directNoticeEventFulfillmentV1 struct {
	State        FulfillmentState `json:"state"`
	Required     int64            `json:"required"`
	Acknowledged int64            `json:"acknowledged"`
	Viable       int64            `json:"viable"`
	Unmet        int64            `json:"unmet"`
	Quorum       int64            `json:"quorum,omitempty"`
}

const (
	// directNoticeEventPayloadV1Version is part of the retained wire contract.
	// Never change it when a future writer moves to another payload version: the
	// decoder below must continue to recognize immutable v1 outboxes forever.
	directNoticeEventPayloadV1Version int64 = 1
	// directNoticeCurrentEventPayloadVersion selects the format emitted by new
	// publishes. A future v2 writer changes only this selector and adds a new
	// encoder/decoder branch; it does not relabel the v1 format.
	directNoticeCurrentEventPayloadVersion = directNoticeEventPayloadV1Version
)

func directNoticeEventPayloadV1FromResult(result DirectNoticePublishResult) directNoticeEventPayloadV1 {
	return directNoticeEventPayloadV1{
		SchemaVersion: directNoticeEventPayloadV1Version,
		Command:       directNoticePublishOperation, ResultKind: string(messageKind), ResultID: result.MessageID,
		ChannelID: result.ChannelID, MessageID: result.MessageID, MessageKind: MessageNotice,
		State: MessagePublished, Version: result.Version, EventSequence: 1,
		DeliveryCount: result.DeliveryCount, RequiredCount: result.RequiredCount,
		AckQuorum: result.AckQuorum,
		Fulfillment: directNoticeEventFulfillmentV1{
			State: result.Fulfillment.State, Required: result.Fulfillment.Required,
			Acknowledged: result.Fulfillment.Acknowledged, Viable: result.Fulfillment.Viable,
			Unmet: result.Fulfillment.Unmet, Quorum: result.Fulfillment.Quorum,
		},
		AudienceHash:  result.AudienceHash,
		PayloadDigest: result.PayloadDigest, PlanHash: result.PlanHash,
	}
}

func canonicalDirectNoticeEventPayload(result DirectNoticePublishResult) ([]byte, error) {
	switch directNoticeCurrentEventPayloadVersion {
	case directNoticeEventPayloadV1Version:
		return canonicalJSON(directNoticeEventPayloadV1FromResult(result))
	default:
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice Event payload writer version is unavailable",
		)
	}
}

func decodeDirectNoticeEventPayload(raw []byte) (directNoticeEventPayloadV1, error) {
	var envelope struct {
		SchemaVersion int64 `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return directNoticeEventPayloadV1{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice Event payload version is unavailable",
		)
	}
	if envelope.SchemaVersion != directNoticeEventPayloadV1Version {
		return directNoticeEventPayloadV1{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice Event payload version is unavailable",
		)
	}
	var payload directNoticeEventPayloadV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return directNoticeEventPayloadV1{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return directNoticeEventPayloadV1{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice Event payload has trailing values",
		)
	}
	canonical, err := canonicalJSON(payload)
	if err != nil || !bytes.Equal(canonical, raw) ||
		payload.SchemaVersion != directNoticeEventPayloadV1Version {
		return directNoticeEventPayloadV1{}, communicationError(
			ErrCommunicationEvidenceUnknown, "direct notice Event payload version is unavailable",
		)
	}
	return payload, nil
}

func buildDirectNoticeReceipt(
	dbNow time.Time,
	preflight directNoticePublishPreflight,
	plan MessagePublishPlan,
	planHash []byte,
	audit model.AuditEvent,
	result DirectNoticePublishResult,
) (CommunicationCommandReceipt, error) {
	audienceGraphIDs, err := canonicalDirectNoticeAudienceGraphIDs(
		plan.Audiences[0].ID, plan.Contributions[0].ID,
	)
	if err != nil {
		return CommunicationCommandReceipt{}, err
	}
	deliveryEvidence, err := canonicalDirectNoticeDeliveryEvidence(plan.Deliveries[0])
	if err != nil {
		return CommunicationCommandReceipt{}, err
	}
	projection := CommunicationCommandResponseProjection{
		IDs: map[string]model.ID{
			"channel_id": preflight.Channel.ID, "message_id": plan.After.ID,
			"delivery_id": plan.Deliveries[0].ID, "event_id": preflight.IDs.Event,
		},
		Version: plan.After.Version, State: string(plan.After.State),
		Counts: map[string]int64{
			"delivery_count": 1, "resolved_count": 1,
			"required":     result.Fulfillment.Required,
			"acknowledged": result.Fulfillment.Acknowledged,
			"viable":       result.Fulfillment.Viable,
			"unmet":        result.Fulfillment.Unmet,
			"quorum":       result.Fulfillment.Quorum,
		},
		Digests: map[string][]byte{
			"request":       cloneDirectNoticeBytes(preflight.RequestDigest),
			"plan":          cloneDirectNoticeBytes(planHash),
			"audience":      cloneDirectNoticeBytes(plan.After.AudienceHash),
			"contributions": cloneDirectNoticeBytes(audienceGraphIDs),
			"route_reasons": cloneDirectNoticeBytes(deliveryEvidence),
			"payload":       cloneDirectNoticeBytes(plan.After.Payload.Digest),
		},
	}
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: preflight.IDs.Receipt, TenantID: preflight.Scope.TenantID,
			WorkspaceID: preflight.Scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
		}},
		CommandID:          preflight.IDs.Command,
		ActorFingerprint:   cloneDirectNoticeBytes(preflight.ActorFingerprint),
		CommandScope:       preflight.Command.CommandScope,
		IdempotencyKeyHash: cloneDirectNoticeBytes(preflight.IdempotencyHash),
		RequestDigest:      cloneDirectNoticeBytes(preflight.RequestDigest),
		PlanHash:           cloneDirectNoticeBytes(planHash), ResultKind: string(messageKind),
		ResultID: plan.After.ID, HTTPStatus: http.StatusAccepted,
		ResponseProjectionJSON: projection, EventID: preflight.IDs.Event,
		AuditSeq: audit.Seq, AuditHash: cloneDirectNoticeBytes(audit.Hash), CompletedAt: dbNow,
	}
	binding, err := CanonicalCommunicationReceiptResponseBinding(receipt)
	if err != nil {
		return CommunicationCommandReceipt{}, err
	}
	digest := sha256.Sum256(binding)
	receipt.ResponseDigest = digest[:]
	if err := ValidateCommunicationCommandReceipt(receipt); err != nil {
		return CommunicationCommandReceipt{}, err
	}
	return receipt, nil
}
