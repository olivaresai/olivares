// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const workItemHandoffOfferPath = "/v1/m/sessions/handoffs"

// WorkItemHandoffOfferCommand creates the governed handoff carrier and offered
// Handoff in one transaction. Sender, tenant and workspace remain exclusively
// server-derived from the authenticated request and stored Channel.
type WorkItemHandoffOfferCommand struct {
	ChannelID          model.ID       `json:"channel_id"`
	WorkItemID         model.ID       `json:"work_item_id"`
	Recipient          RecipientRef   `json:"recipient"`
	Content            HandoffContent `json:"handoff"`
	AckDeadline        time.Time      `json:"ack_deadline"`
	ExpectedOwnerEpoch int64          `json:"expected_owner_epoch,omitempty"`
	IfMatch            string         `json:"-"`
	IdempotencyKey     string         `json:"-"`
}

// OfferWorkItemHandoff is the public current-authority boundary. It does not
// accept an existing carrier ID: every carrier identity is server-derived from
// the idempotency key and remains inseparable from the Handoff receipt.
//
// A Session sender's own Claim can be renewed by another transaction of the
// same Session between this request's observation and its authority lock. The
// original offer of the same key does exactly that while a resend waits behind
// it. That failure alone is followed by one recognition admission, as a Handoff
// response is; every other failure is returned as it is.
func (m *Module) OfferWorkItemHandoff(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd WorkItemHandoffOfferCommand,
) (HandoffOfferResult, error) {
	admission, prepared, err := m.prepareWorkItemHandoffOffer(ctx, scope, ref, cmd, handoffOfferApply)
	if err != nil {
		return HandoffOfferResult{}, err
	}
	result, err := m.applyAtomicWorkItemHandoffOffer(ctx, admission, prepared, handoffOfferApply)
	if err == nil {
		return result, nil
	}
	stale, eligible := handoffResponseRecognitionFailure(err)
	if !eligible {
		return HandoffOfferResult{}, err
	}
	return m.recognizeWorkItemHandoffOffer(ctx, scope, ref, cmd, admission, stale.fact)
}

// handoffOfferMode separates the ordinary offer from its one recognition
// admission, which may only return a committed receipt's result.
type handoffOfferMode uint8

const (
	handoffOfferApply handoffOfferMode = iota + 1
	handoffOfferRecognize
)

// workItemHandoffOfferAdmission is what one admission observed before its
// transaction. A recognition observes all of it again; it never reuses the
// admission that failed.
type workItemHandoffOfferAdmission struct {
	question           communicationAuthorityQuestion
	bound              communicationRequestAuthority
	inspected          communicationRequestAuthorityInspection
	identity           directNoticeReaderIdentityPreflight
	window             communicationAuthorityWindow
	claims             CommunicationClaimAuthoritySnapshot
	directPreflight    directNoticePublishPreflight
	normalized         handoffOfferNormalized
	ids                handoffOfferIDs
	target             RecipientRef
	ackDeadline        time.Time
	expectedOwnerEpoch int64
}

// prepareWorkItemHandoffOffer takes one admission's observations in the order
// the offer has always taken them. Only the ordinary admission seals the
// Handoff content: a recognition can only return a committed receipt, so it
// has no new content to protect.
func (m *Module) prepareWorkItemHandoffOffer(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd WorkItemHandoffOfferCommand,
	mode handoffOfferMode,
) (workItemHandoffOfferAdmission, handoffOfferPrepared, error) {
	if !validCanonicalCommunicationID(cmd.ChannelID) ||
		!validCanonicalCommunicationID(cmd.WorkItemID) || cmd.Recipient.Validate() != nil ||
		cmd.AckDeadline.IsZero() || cmd.ExpectedOwnerEpoch < 0 {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, communicationError(
			ErrInvalidCommunicationModel, "invalid WorkItem Handoff offer",
		)
	}
	cmd.AckDeadline = cmd.AckDeadline.UTC()
	question, err := newCommunicationAuthorityQuestion(
		scope, channelKind, cmd.ChannelID, CommunicationMessageSend,
	)
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	bound, err := m.bindCurrentCommunicationRequestAuthority(ctx, ref, question)
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	inspected, err := bound.contextFor(question)
	if err != nil || inspected.question != question {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"WorkItem Handoff authority crossed its exact Channel request",
		)
	}
	identity, err := m.preflightDirectNoticeReaderIdentity(ctx, scope, inspected.principal, nil)
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	actor, err := communicationActorForRecipient(identity.Recipient)
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	ids, err := stableWorkItemHandoffIDs(cmd.IdempotencyKey)
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	carrierKey, err := workflowCommunicationStableID(cmd.IdempotencyKey, "http.handoff.carrier")
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	carrierContent := MessageContent{
		Subject: "Work handoff offer",
		Blocks: []MessageContentBlock{
			{Type: ContentBlockStatus, Code: "handoff_offer"},
			{Type: ContentBlockActionRef, Reference: &ContentReference{
				Kind: "work_item", Ref: cmd.WorkItemID.String(),
			}},
		},
	}
	directCommand := DirectNoticePublishCommand{
		ChannelID: cmd.ChannelID, Recipient: cmd.Recipient, Content: carrierContent,
		Urgency: UrgencyHigh, IdempotencyKey: carrierKey.String(), sender: actor,
	}
	directCommand, actorFingerprint, idempotencyHash, directRequestDigest, err :=
		normalizeDirectNoticePublishCommand(scope, inspected.principal, directCommand)
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	directPreflight, err := m.preflightWorkItemHandoffCarrierWithoutCore(
		ctx, scope, inspected.principal, directCommand,
		actorFingerprint, idempotencyHash, directRequestDigest, ids.publish,
	)
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	exact := HandoffOfferCommand{
		ChannelID: cmd.ChannelID, WorkItemID: cmd.WorkItemID,
		MessageID: ids.publish.Message, DeliveryID: ids.publish.Delivery,
		Content: cmd.Content, IfMatch: cmd.IfMatch, IdempotencyKey: cmd.IdempotencyKey,
	}
	normalized, err := normalizeHandoffOfferCommandForActor(
		scope, inspected.principal, actor, exact,
	)
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	request, err := canonicalJSON(struct {
		SchemaVersion      int64          `json:"schema_version"`
		Operation          string         `json:"operation"`
		ChannelID          model.ID       `json:"channel_id"`
		WorkItemID         model.ID       `json:"work_item_id"`
		Recipient          RecipientRef   `json:"recipient"`
		Content            HandoffContent `json:"handoff"`
		AckDeadline        time.Time      `json:"ack_deadline"`
		ExpectedOwnerEpoch int64          `json:"expected_owner_epoch"`
		IfMatch            string         `json:"if_match"`
	}{
		1, handoffOfferOperation, cmd.ChannelID, cmd.WorkItemID, cmd.Recipient,
		cmd.Content, cmd.AckDeadline, cmd.ExpectedOwnerEpoch, cmd.IfMatch,
	})
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	requestDigest := sha256.Sum256(request)
	normalized.requestDigest = requestDigest[:]

	var prepared handoffOfferPrepared
	if mode == handoffOfferApply {
		policy := protectedPayloadPolicyFrom(directPreflight.Payload)
		schema, _ := PayloadSlotHandoff.schema()
		handoffPayload, err := PrepareProtectedPayload(
			ctx, m.communicationSealer, PayloadSlotHandoff, policy,
			ContentAAD{
				TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
				ChannelID: cmd.ChannelID, EntityKind: handoffKind, EntityID: ids.handoff.Handoff,
				Schema: schema, ProtectionGeneration: policy.ProtectionGeneration,
			},
			cmd.Content,
		)
		if err != nil {
			return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
		}
		prepared.payload = cloneProtectedPayload(handoffPayload)
	}
	claimRefs := communicationClaimsForPrincipal(inspected.principal)
	if cmd.Recipient.Kind == RecipientSession {
		contribution := directPreflight.Snapshot.Contributions[0]
		claimRefs = append(claimRefs, CommunicationClaimRef{
			SessionSID: contribution.ObservedSessionSID,
			Fence:      contribution.ObservedClaimFence,
		})
	}
	claims, err := m.communicationClaimAuthoritySnapshot(ctx, scope.TenantID, claimRefs)
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, err
	}
	readiness, readinessErr := m.EvaluateCommunicationReadiness(ctx)
	if readinessErr != nil || !readiness.Effective {
		return workItemHandoffOfferAdmission{}, handoffOfferPrepared{}, communicationError(
			ErrCommunicationEvidenceUnknown, "communication kernel is not ready",
		)
	}
	return workItemHandoffOfferAdmission{
		question: question, bound: bound, inspected: inspected, identity: identity,
		window: window, claims: claims, directPreflight: directPreflight,
		normalized: normalized, ids: ids.handoff, target: cmd.Recipient,
		ackDeadline: cmd.AckDeadline, expectedOwnerEpoch: cmd.ExpectedOwnerEpoch,
	}, prepared, nil
}

type workItemHandoffStableIDs struct {
	publish directNoticePublishIDs
	handoff handoffOfferIDs
}

func stableWorkItemHandoffIDs(key string) (workItemHandoffStableIDs, error) {
	purposes := []string{
		"http.handoff.message", "http.handoff.audience", "http.handoff.delivery",
		"http.handoff.contribution", "http.handoff.carrier-command",
		"http.handoff.carrier-event", "http.handoff.carrier-receipt",
		"http.handoff.aggregate", "http.handoff.command", "http.handoff.receipt",
		"http.handoff.event",
	}
	values := make([]model.ID, len(purposes))
	for i, purpose := range purposes {
		id, err := workflowCommunicationStableID(key, purpose)
		if err != nil {
			return workItemHandoffStableIDs{}, err
		}
		values[i] = id
	}
	return workItemHandoffStableIDs{
		publish: directNoticePublishIDs{
			Message: values[0], Audience: values[1], Delivery: values[2],
			Contribution: values[3], Command: values[4], Event: values[5], Receipt: values[6],
		},
		handoff: handoffOfferIDs{
			Handoff: values[7], Command: values[8], Receipt: values[9], Event: values[10],
		},
	}, nil
}

func (m *Module) applyAtomicWorkItemHandoffOffer(
	ctx context.Context,
	admission workItemHandoffOfferAdmission,
	prepared handoffOfferPrepared,
	mode handoffOfferMode,
) (HandoffOfferResult, error) {
	if mode != handoffOfferApply && mode != handoffOfferRecognize {
		return HandoffOfferResult{}, handoffRecognitionUnknown()
	}
	question, bound, inspected, identity := admission.question, admission.bound,
		admission.inspected, admission.identity
	window, claims, directPreflight := admission.window, admission.claims, admission.directPreflight
	normalized, ids, target := admission.normalized, admission.ids, admission.target
	ackDeadline, expectedOwnerEpoch := admission.ackDeadline, admission.expectedOwnerEpoch
	var result HandoffOfferResult
	err := m.mutateHandoffWithAuthority(
		ctx, question, bound, window, claims,
		func(
			tx *communicationTx,
			repositories handoffWorkRepositories,
			consumed communicationRequestAuthorityContext,
		) error {
			boundPreflight, err := directNoticePublishPreflightWithBoundAuthority(
				directPreflight, inspected, consumed,
			)
			if err != nil {
				return err
			}
			reader, err := handoffOfferReaderPreflight(
				question, inspected, consumed, identity, normalized,
			)
			if err != nil {
				return err
			}
			authorityLock, err := lockDirectNoticePublishAuthoritySnapshot(ctx, tx, boundPreflight)
			if err != nil {
				if mode == handoffOfferApply {
					return captureHandoffOfferAuthorityFailure(err, normalized, claims)
				}
				return err
			}
			authorityFacts, err := directNoticePublishAuthorityFacts(boundPreflight)
			if err != nil || !authorityLock.consume(tx, boundPreflight, authorityFacts) {
				return communicationTransactionUnavailable("Handoff carrier authority", err)
			}
			if err := tx.lockTransaction(
				ctx, handoffIdempotencyLockKey(normalized.handoffCommandIdentity),
			); err != nil {
				return err
			}
			receipt, replay, err := findHandoffReceipt(ctx, tx, normalized.handoffCommandIdentity)
			if err != nil {
				return err
			}
			if mode == handoffOfferRecognize && !replay {
				return handoffRecognitionUnknown()
			}
			if replay && !bytes.Equal(receipt.RequestDigest, normalized.requestDigest) {
				return errHandoffIdempotencyReused
			}
			work, err := lockHandoffWorkState(
				ctx, tx, repositories, normalized.scope, normalized.command.WorkItemID, false,
			)
			if err != nil {
				return err
			}
			if expectedOwnerEpoch > 0 && work.item.Int(colWorkOwnerEpoch) != expectedOwnerEpoch {
				return store.ErrConflict
			}
			if replay {
				carrier, err := lockHandoffCarrier(
					ctx, tx, normalized.scope, normalized.command.ChannelID,
					normalized.command.MessageID, normalized.command.DeliveryID,
				)
				if err != nil {
					return err
				}
				if carrier.delivery.Recipient != target || carrier.delivery.AckDueAt == nil ||
					!carrier.delivery.AckDueAt.Equal(ackDeadline) {
					return errHandoffIdempotencyReused
				}
				if err := tx.lockAuditAppends(ctx); err != nil {
					return err
				}
				if err := tx.refreshNow(ctx); err != nil {
					return err
				}
				if err := evaluateHandoffOfferAuthority(tx, reader, carrier); err != nil {
					return err
				}
				prepared.message, prepared.delivery, prepared.channel =
					carrier.message, carrier.delivery, carrier.channel
				if err := validatePreparedHandoffOffer(prepared, carrier); err != nil {
					return err
				}
				result, err = handoffOfferResultFromReceipt(receipt)
				return err
			}
			// A recognition never becomes a fresh offer: the absent receipt above
			// refuses it, and so does the effect seam.
			if mode != handoffOfferApply {
				return handoffRecognitionUnknown()
			}

			locked, err := lockDirectNoticePublishState(ctx, tx, boundPreflight)
			if err != nil {
				return err
			}
			workflowPreflight := workflowCommunicationPreflight{
				direct: boundPreflight, workItemID: normalized.command.WorkItemID,
				messageKind: MessageHandoffOffer, sourceKind: RouteSourceUserMessage,
				ackDueAt: &ackDeadline,
			}
			plan, err := materializeWorkflowCommunicationMessage(tx.now.Time(), workflowPreflight, locked)
			if err != nil {
				return err
			}
			if !communicationClaimsEqualSnapshot(plan.RequiredClaims, claims) ||
				plan.GuardAdvance == nil || len(plan.Audiences) != 1 ||
				len(plan.Contributions) != 1 || len(plan.Deliveries) != 1 ||
				plan.After.ID != normalized.command.MessageID ||
				plan.Deliveries[0].ID != normalized.command.DeliveryID ||
				plan.After.WorkItemID != normalized.command.WorkItemID ||
				plan.After.Kind != MessageHandoffOffer || plan.After.State != MessagePublished ||
				plan.After.LastEventSeq != 0 || plan.Deliveries[0].Recipient != target ||
				!equalDirectNoticeAuthorityFacts(reader.Facts, plan.Facts) {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"WorkItem Handoff carrier planner returned a non-atomic shape",
				)
			}
			if err := persistMessageDerivedPlan(ctx, tx, plan); err != nil {
				return err
			}
			epoch := model.DirectoryEpoch{BaseFields: model.BaseFields{
				ID: model.ID(normalized.scope.TenantID), TenantID: normalized.scope.TenantID,
				Version: boundPreflight.Snapshot.Epoch,
			}}
			carrier := handoffLockedCarrier{
				channel: locked.Channel, grants: locked.Grants, message: plan.After,
				delivery: plan.Deliveries[0], deliveries: plan.Deliveries,
				audiences: plan.Audiences, contributions: plan.Contributions,
				requiredCount: plan.RequiredCount, epoch: epoch,
			}
			prepared.message, prepared.delivery, prepared.channel =
				carrier.message, carrier.delivery, carrier.channel
			result, err = applyLockedHandoffOffer(
				ctx, tx, repositories, reader, normalized, ids, prepared, carrier, work, true,
			)
			return err
		},
	)
	return result, err
}

// recognizeWorkItemHandoffOffer owns the one new admission that follows a stale
// own-Claim failure. Every authority input is observed again, the offer must be
// the same offer, and the sender's Claim may differ only in its version. The
// transaction then judges current authority against the locked rows before it
// returns a committed receipt, and refuses when there is none. It never calls
// the ordinary offer, never joins a failed transaction, and any failure ends it
// without a result.
func (m *Module) recognizeWorkItemHandoffOffer(
	ctx context.Context,
	scope DirectoryScopeRef,
	ref auth.PrincipalRef,
	cmd WorkItemHandoffOfferCommand,
	original workItemHandoffOfferAdmission,
	originalClaim store.AuthorizationFactRef,
) (HandoffOfferResult, error) {
	deadline, finite := ctx.Deadline()
	if ctx.Err() != nil || !finite || !time.Now().Before(deadline) ||
		!handoffResponderClaim(originalClaim, original.normalized.principal) {
		return HandoffOfferResult{}, handoffRecognitionUnknown()
	}
	if _, joined := protocolReplayScopeFromContext(ctx, scope.TenantID); joined {
		return HandoffOfferResult{}, handoffRecognitionUnknown()
	}
	current, prepared, err := m.prepareWorkItemHandoffOffer(ctx, scope, ref, cmd, handoffOfferRecognize)
	if err != nil {
		return HandoffOfferResult{}, err
	}
	if !sameWorkItemHandoffOfferIdentity(original, current) {
		return HandoffOfferResult{}, handoffRecognitionUnknown()
	}
	claim, found := handoffOfferSenderClaim(current.claims, current.normalized.principal)
	if !found || !sameHandoffClaimSemantics(originalClaim, claim) {
		return HandoffOfferResult{}, handoffRecognitionUnknown()
	}
	// The replay branch fills the result before its transaction ends. A commit
	// that then fails, or whose outcome is lost, publishes no result.
	result, err := m.applyAtomicWorkItemHandoffOffer(ctx, current, prepared, handoffOfferRecognize)
	if err != nil {
		return HandoffOfferResult{}, err
	}
	return result, nil
}

// captureHandoffOfferAuthorityFailure recognizes one cause at the offer's
// authority phase: the sending Session's own Claim was locked at a newer version
// than this admission observed, and nothing else failed with it. That is a lease
// renewal by another transaction of the same Session, not a change of authority.
// Any other failure, a recipient's Claim included, is returned exactly as it was.
func captureHandoffOfferAuthorityFailure(
	err error,
	normalized handoffOfferNormalized,
	claims CommunicationClaimAuthoritySnapshot,
) error {
	fact, found := handoffOfferSenderClaim(claims, normalized.principal)
	if !found {
		return err
	}
	if handoffRecognitionErrorTree(err, true, func(node error) bool {
		conflict, ok := node.(*store.LockedLeasedVersionConflict)
		return ok && conflict.Matches(fact)
	}) {
		return &handoffStaleClaimFailure{fact: fact}
	}
	return err
}

// handoffOfferSenderClaim is the one Claim in the snapshot that belongs to the
// sending Session at its authenticated fence. A USER or agent sender has none.
func handoffOfferSenderClaim(
	claims CommunicationClaimAuthoritySnapshot,
	principal CommunicationPrincipal,
) (store.AuthorizationFactRef, bool) {
	var found store.AuthorizationFactRef
	matches := 0
	for _, fact := range claims.facts {
		if handoffResponderClaim(fact, principal) {
			found, matches = fact, matches+1
		}
	}
	return found, matches == 1
}

// sameWorkItemHandoffOfferIdentity is true only when a recognition admission
// names the very offer the ordinary admission did. The request digest covers
// content, recipient, deadline, owner epoch and If-Match; the rest of the
// identity is compared field by field.
func sameWorkItemHandoffOfferIdentity(before, current workItemHandoffOfferAdmission) bool {
	b, c := before.normalized, current.normalized
	return b.scope == c.scope && b.principal == c.principal && b.actor == c.actor &&
		b.commandScope == c.commandScope && b.expectedVersion == c.expectedVersion &&
		bytes.Equal(b.actorFingerprint, c.actorFingerprint) &&
		bytes.Equal(b.idempotencyKeyHash, c.idempotencyKeyHash) &&
		bytes.Equal(b.requestDigest, c.requestDigest) &&
		b.command.ChannelID == c.command.ChannelID && b.command.WorkItemID == c.command.WorkItemID &&
		b.command.MessageID == c.command.MessageID && b.command.DeliveryID == c.command.DeliveryID &&
		b.command.IfMatch == c.command.IfMatch && b.command.IdempotencyKey == c.command.IdempotencyKey &&
		before.target == current.target && before.ackDeadline.Equal(current.ackDeadline) &&
		before.expectedOwnerEpoch == current.expectedOwnerEpoch
}
