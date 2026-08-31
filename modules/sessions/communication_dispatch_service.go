// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

const (
	deliveryDispatchClaimAuditAction     = "sessions.communication.dispatch.claim"
	deliveryDispatchFinishAuditAction    = "sessions.communication.dispatch.finish"
	deliveryDispatchAbandonAuditAction   = "sessions.communication.dispatch.abandon"
	deliveryDispatchReconcileAuditAction = "sessions.communication.dispatch.reconcile"

	deliveryDispatchDefaultClaimTTL      = time.Minute
	deliveryDispatchDefaultResolutionTTL = 15 * time.Minute
	deliveryDispatchMaxTTL               = 30 * 24 * time.Hour
	deliveryDispatchDefaultScanBound     = 32
	deliveryDispatchMaxScanBound         = 256
)

// deliveryDispatchPumpClaim is supplied afresh by the composition root for
// every pump invocation. Persisting node+epoch prevents a process that is
// promoted twice from reusing authority from its former leadership epoch.
type deliveryDispatchPumpClaim struct {
	NodeID string
	Epoch  int64
}

func (c deliveryDispatchPumpClaim) owner() (string, error) {
	if !boundedToken(c.NodeID, 96) || c.Epoch < 1 {
		return "", communicationError(
			ErrInvalidCommunicationModel, "invalid delivery pump leadership claim",
		)
	}
	owner := c.NodeID + ".e" + strconv.FormatInt(c.Epoch, 10)
	if !boundedToken(owner, 128) {
		return "", communicationError(
			ErrInvalidCommunicationModel, "delivery pump leadership owner is not bounded",
		)
	}
	return owner, nil
}

// deliveryDispatchPumpFence is deliberately private. The composition root
// implements it with the current leadership evidence. It runs after the
// durable claim commits and immediately before Notify; failure proves that no
// provider call was made and is settled as a pre-boundary refusal.
type deliveryDispatchPumpFence interface {
	BeforeNotify(context.Context, DirectoryScopeRef, deliveryDispatchPumpClaim) error
}

type deliveryDispatchServiceConfig struct {
	ClaimTTL      time.Duration
	ResolutionTTL time.Duration
	ScanBound     int
	NewID         func() model.ID
}

type deliveryDispatchService struct {
	module   *Module
	notifier sdk.DeliveryNotifier
	fence    deliveryDispatchPumpFence
	config   deliveryDispatchServiceConfig
}

func newDeliveryDispatchService(
	module *Module,
	notifier sdk.DeliveryNotifier,
	fence deliveryDispatchPumpFence,
	config deliveryDispatchServiceConfig,
) (*deliveryDispatchService, error) {
	if module == nil || notifier == nil || fence == nil {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch ports are unavailable",
		)
	}
	if config.ClaimTTL == 0 {
		config.ClaimTTL = deliveryDispatchDefaultClaimTTL
	}
	if config.ResolutionTTL == 0 {
		config.ResolutionTTL = deliveryDispatchDefaultResolutionTTL
	}
	if config.ScanBound == 0 {
		config.ScanBound = deliveryDispatchDefaultScanBound
	}
	if config.NewID == nil {
		config.NewID = model.NewID
	}
	if config.ClaimTTL <= 0 || config.ClaimTTL > deliveryDispatchMaxTTL ||
		config.ResolutionTTL <= 0 || config.ResolutionTTL > deliveryDispatchMaxTTL ||
		config.ScanBound < 1 || config.ScanBound > deliveryDispatchMaxScanBound {
		return nil, communicationError(
			ErrInvalidCommunicationModel, "invalid delivery dispatch service bounds",
		)
	}
	return &deliveryDispatchService{
		module: module, notifier: notifier, fence: fence, config: config,
	}, nil
}

// deliveryDispatchPumpResult is a closed, payload-free service projection.
// Claimed=false means the scan was empty or another worker won the claim.
type deliveryDispatchPumpResult struct {
	DispatchID model.ID
	AttemptID  model.ID
	State      DeliveryDispatchState
	AuditSeq   int64
	Claimed    bool
	Notified   bool
}

type deliveryDispatchClaimed struct {
	dispatch sdk.DeliveryDispatch
	witness  sdk.DeliveryCapabilityWitness
	owner    string
	auditSeq int64
}

type deliveryDispatchCandidate struct {
	dispatch DeliveryDispatch
	endpoint sdk.DeliveryEndpointIdentity
}

// PumpOne performs exactly one durable single-assignment attempt. The only
// external effect, Notify, is between two committed transactions. UNKNOWN is
// quiescent and is never selected by the pending scan.
func (s *deliveryDispatchService) PumpOne(
	ctx context.Context,
	scope DirectoryScopeRef,
	claim deliveryDispatchPumpClaim,
) (deliveryDispatchPumpResult, error) {
	if s == nil || s.module == nil || s.notifier == nil || s.fence == nil {
		return deliveryDispatchPumpResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch service is unavailable",
		)
	}
	owner, err := claim.owner()
	if err != nil {
		return deliveryDispatchPumpResult{}, err
	}
	candidate, found, err := s.scanPendingCandidate(ctx, scope)
	if err != nil || !found {
		return deliveryDispatchPumpResult{}, err
	}
	witness, capabilityErr := s.notifier.Capabilities(ctx, candidate.endpoint)
	if capabilityErr != nil {
		return deliveryDispatchPumpResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery capability witness is unavailable",
		)
	}
	witness = sdk.NormalizeDeliveryCapabilityWitness(witness, capabilityErr)
	if _, generationErr := deliverySDKGeneration(witness.Endpoint().EndpointGeneration()); generationErr != nil || !witness.Matches(candidate.endpoint) {
		return deliveryDispatchPumpResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery capability witness is not exact",
		)
	}
	claimed, won, err := s.claimCandidate(ctx, scope, candidate, witness, owner)
	if err != nil || !won {
		return deliveryDispatchPumpResult{}, err
	}
	result := deliveryDispatchPumpResult{
		DispatchID: model.ID(claimed.dispatch.DispatchID()),
		AttemptID:  model.ID(claimed.dispatch.AttemptID()),
		State:      DispatchInFlight,
		AuditSeq:   claimed.auditSeq,
		Claimed:    true,
	}

	capabilities := claimed.witness.Capabilities()
	preBoundaryCode := ""
	if !claimed.witness.Matches(claimed.dispatch.EndpointIdentity()) ||
		!capabilities.Wake() || !capabilities.Idempotency() || capabilities.ActiveTurn() {
		preBoundaryCode = "delivery_capability_refused"
	} else if fenceErr := s.fence.BeforeNotify(ctx, scope, claim); fenceErr != nil {
		preBoundaryCode = "delivery_epoch_fence_refused"
	}

	var attemptResult sdk.DeliveryAttemptResult
	if preBoundaryCode == "" {
		result.Notified = true
		observed, notifyErr := s.notifier.Notify(ctx, claimed.dispatch)
		attemptResult = sdk.NormalizeDeliveryAttemptResult(observed, notifyErr)
	} else {
		attemptResult, err = sdk.NewDeliveryAttemptResult(
			sdk.DeliveryAttemptRefusedBeforeBoundary,
			sdk.DeliveryBoundaryNotCrossed,
			nil,
		)
		if err != nil {
			return result, communicationError(
				ErrCommunicationEvidenceUnknown, "closed pre-boundary result is unavailable",
			)
		}
	}
	state, auditSeq, err := s.settleAttempt(
		ctx, scope, claimed, attemptResult, preBoundaryCode,
	)
	result.State = state
	result.AuditSeq = auditSeq
	return result, err
}

func (s *deliveryDispatchService) scanPendingCandidate(
	ctx context.Context,
	scope DirectoryScopeRef,
) (deliveryDispatchCandidate, bool, error) {
	if err := scope.Validate(); err != nil {
		return deliveryDispatchCandidate{}, false, err
	}
	var candidate DeliveryDispatch
	var found bool
	var endpoint CommunicationEndpoint
	err := s.module.viewCommunication(ctx, scope, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return communicationTransactionUnavailable("dispatch scan database clock", nil)
		}
		now, err := clock.TransactionNow(ctx)
		if err != nil {
			return communicationTransactionUnavailable("dispatch scan database clock", err)
		}
		repo, err := sc.Ext(deliveryDispatchKind)
		if err != nil {
			return err
		}
		rows := make([]model.Record, 0, 2)
		for _, query := range []model.Query{
			{
				Filters: []model.Filter{
					{Column: colCommState, Op: model.OpEq, Value: string(DispatchPending)},
					{Column: colCommNextAttemptAt, Op: model.OpIsNull},
				},
				Sort: []model.Sort{{Column: model.ColCreatedAt}}, Limit: s.config.ScanBound,
			},
			{
				Filters: []model.Filter{
					{Column: colCommState, Op: model.OpEq, Value: string(DispatchPending)},
					{Column: colCommNextAttemptAt, Op: model.OpLte, Value: now.String()},
				},
				Sort: []model.Sort{{Column: colCommNextAttemptAt}}, Limit: s.config.ScanBound,
			},
		} {
			pageRows, page, listErr := repo.List(ctx, query)
			if listErr != nil {
				return listErr
			}
			if page.HasMore && len(pageRows) == 0 {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "delivery dispatch candidate scan is incomplete",
				)
			}
			rows = append(rows, pageRows...)
		}
		if len(rows) == 0 {
			return nil
		}
		decoded := make([]DeliveryDispatch, 0, len(rows))
		seen := make(map[model.ID]struct{}, len(rows))
		for _, row := range rows {
			dispatch, decodeErr := deliveryDispatchFromRecord(row)
			if decodeErr != nil || dispatch.TenantID != scope.TenantID ||
				dispatch.WorkspaceID != scope.WorkspaceID {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "delivery dispatch candidate cannot be decoded",
				)
			}
			if _, duplicate := seen[dispatch.ID]; duplicate {
				continue
			}
			seen[dispatch.ID] = struct{}{}
			decoded = append(decoded, dispatch)
		}
		sort.Slice(decoded, func(i, j int) bool {
			left, right := decoded[i].CreatedAt, decoded[j].CreatedAt
			if decoded[i].NextAttemptAt != nil {
				left = *decoded[i].NextAttemptAt
			}
			if decoded[j].NextAttemptAt != nil {
				right = *decoded[j].NextAttemptAt
			}
			if !left.Equal(right) {
				return left.Before(right)
			}
			return decoded[i].ID.String() < decoded[j].ID.String()
		})
		candidate = decoded[0]
		endpointRepo, repoErr := sc.Ext(communicationEndpointKind)
		if repoErr != nil {
			return repoErr
		}
		endpointRecord, getErr := endpointRepo.Get(ctx, candidate.EndpointID)
		if getErr != nil {
			return getErr
		}
		endpoint, getErr = communicationEndpointFromRecord(endpointRecord)
		if getErr != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "delivery endpoint cannot be decoded",
			)
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return deliveryDispatchCandidate{}, false, err
	}
	identity, err := deliveryEndpointIdentity(scope, candidate, endpoint)
	if err != nil {
		return deliveryDispatchCandidate{}, false, err
	}
	return deliveryDispatchCandidate{dispatch: candidate, endpoint: identity}, true, nil
}

func deliveryEndpointIdentity(
	scope DirectoryScopeRef,
	dispatch DeliveryDispatch,
	endpoint CommunicationEndpoint,
) (sdk.DeliveryEndpointIdentity, error) {
	if endpoint.TenantID != scope.TenantID || endpoint.WorkspaceID != scope.WorkspaceID ||
		endpoint.ID != dispatch.EndpointID || endpoint.Generation != dispatch.EndpointGeneration ||
		endpoint.TransportFingerprint == "" || endpoint.Generation < 1 {
		return sdk.DeliveryEndpointIdentity{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery endpoint identity does not match dispatch",
		)
	}
	fingerprint := sha256.Sum256([]byte(endpoint.TransportFingerprint))
	identity, err := sdk.NewDeliveryEndpointIdentity(sdk.DeliveryEndpointParams{
		TenantID: scope.TenantID.String(), WorkspaceID: scope.WorkspaceID.String(),
		EndpointID: endpoint.ID.String(), EndpointGeneration: uint64(endpoint.Generation),
		EndpointFingerprint: fingerprint[:], Provider: endpoint.ProviderKey,
		Transport: endpoint.Transport,
	})
	if err != nil {
		return sdk.DeliveryEndpointIdentity{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery endpoint identity is not representable",
		)
	}
	return identity, nil
}

func (s *deliveryDispatchService) claimCandidate(
	ctx context.Context,
	scope DirectoryScopeRef,
	candidate deliveryDispatchCandidate,
	witness sdk.DeliveryCapabilityWitness,
	owner string,
) (deliveryDispatchClaimed, bool, error) {
	attemptID := s.config.NewID()
	if !validCanonicalCommunicationID(attemptID) {
		return deliveryDispatchClaimed{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery attempt identity is unavailable",
		)
	}
	var claimed deliveryDispatchClaimed
	var won bool
	err := s.module.mutateCommunication(ctx, scope, func(tx *communicationTx) error {
		if err := tx.lockTransaction(ctx, deliveryDispatchPumpLockKey(scope)); err != nil {
			return err
		}
		dispatchRecord, err := tx.lockRecord(ctx, deliveryDispatchKind, candidate.dispatch.ID)
		if err != nil {
			return err
		}
		dispatch, err := deliveryDispatchFromRecord(dispatchRecord)
		if err != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "locked delivery dispatch cannot be decoded",
			)
		}
		if dispatch.State != DispatchPending || dispatch.AttemptCount != 0 {
			return nil
		}
		deliveryRecord, err := tx.lockRecord(ctx, messageDeliveryKind, dispatch.DeliveryID)
		if err != nil {
			return err
		}
		delivery, err := messageDeliveryFromRecord(deliveryRecord)
		if err != nil {
			return err
		}
		messageRecord, err := tx.lockRecord(ctx, messageKind, delivery.MessageID)
		if err != nil {
			return err
		}
		message, err := deliveryMessageFromRecord(messageRecord)
		if err != nil {
			return err
		}
		endpointRecord, err := tx.lockRecord(ctx, communicationEndpointKind, dispatch.EndpointID)
		if err != nil {
			return err
		}
		endpoint, err := communicationEndpointFromRecord(endpointRecord)
		if err != nil {
			return err
		}
		if err = tx.lockAuditAppends(ctx); err != nil {
			return err
		}
		if err = tx.refreshNow(ctx); err != nil {
			return err
		}
		dbNow := tx.now.Time()
		if dispatch.NextAttemptAt != nil && dbNow.Before(*dispatch.NextAttemptAt) {
			return nil
		}
		identity, err := deliveryEndpointIdentity(scope, dispatch, endpoint)
		if err != nil || !identity.Equal(candidate.endpoint) || !witness.Matches(identity) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "delivery endpoint drifted before claim",
			)
		}
		if err := validateDeliveryDriverLineage(dispatch, delivery, message, endpoint, dbNow); err != nil {
			return err
		}
		request, requestHash, err := buildDeliveryDriverDispatch(
			scope, dispatch, attemptID, delivery, message, endpoint,
		)
		if err != nil {
			return err
		}
		claimUntil, err := deliveryDispatchFuture(dbNow, s.config.ClaimTTL)
		if err != nil {
			return err
		}
		plan, err := PlanDispatchAttemptClaim(
			dispatch,
			MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
				ID: attemptID, TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
				Version: 1, CreatedAt: dbNow,
			}, UpdatedAt: dbNow},
			owner, claimUntil, requestHash, dbNow,
		)
		if err != nil {
			return err
		}
		dispatchAfter, err := deliveryDispatchToRecord(plan.DispatchAfter)
		if err != nil {
			return err
		}
		dispatchAfter[model.ColVersion] = plan.DispatchBefore.Version
		if _, err = tx.update(ctx, deliveryDispatchKind, dispatchAfter); err != nil {
			return err
		}
		attemptRecord, err := deliveryAttemptToRecord(plan.Attempt)
		if err != nil {
			return err
		}
		if _, err = tx.createWithID(ctx, deliveryAttemptKind, plan.Attempt.ID, attemptRecord); err != nil {
			return err
		}
		auditSeq, err := appendDeliveryDispatchAudit(
			ctx, tx, deliveryDispatchClaimAuditAction, dispatch.ID,
			struct {
				Plan         DispatchAttemptClaimPlan `json:"plan"`
				EndpointHash []byte                   `json:"endpoint_hash"`
			}{Plan: plan, EndpointHash: identity.EndpointFingerprint()},
			map[string]any{
				"workspace_id": scope.WorkspaceID.String(), "attempt_id": plan.Attempt.ID.String(),
				"dispatch_generation": dispatch.DispatchGeneration,
			},
		)
		if err != nil {
			return err
		}
		claimed = deliveryDispatchClaimed{
			dispatch: request, witness: witness.Clone(), owner: owner, auditSeq: auditSeq,
		}
		won = true
		return nil
	})
	return claimed, won, err
}

func validateDeliveryDriverLineage(
	dispatch DeliveryDispatch,
	delivery MessageDelivery,
	message Message,
	endpoint CommunicationEndpoint,
	dbNow time.Time,
) error {
	if delivery.ID != dispatch.DeliveryID || message.ID != delivery.MessageID ||
		delivery.TenantID != dispatch.TenantID || delivery.WorkspaceID != dispatch.WorkspaceID ||
		message.TenantID != dispatch.TenantID || message.WorkspaceID != dispatch.WorkspaceID ||
		endpoint.Owner != delivery.Recipient || endpoint.ID != dispatch.EndpointID ||
		endpoint.Generation != dispatch.EndpointGeneration {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch crosses durable lineage",
		)
	}
	if err := ValidateMessageDeliveryLineage(message, delivery); err != nil {
		return err
	}
	if delivery.Recipient.Kind != RecipientUser || delivery.WakePolicy == WakeNone ||
		delivery.State != DeliveryAvailable || message.State != MessagePublished ||
		dbNow.Before(delivery.AvailableAt) ||
		(delivery.ExpiresAt != nil && !dbNow.Before(*delivery.ExpiresAt)) ||
		endpoint.State != EndpointActive || endpoint.SupportLevel != EndpointStable ||
		(endpoint.HeartbeatExpiresAt != nil && !dbNow.Before(*endpoint.HeartbeatExpiresAt)) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch is not currently eligible",
		)
	}
	return nil
}

func deliveryMessageFromRecord(record model.Record) (Message, error) {
	policy := AckPolicy(record.String(colCommAckPolicy))
	required := int64(0)
	if policy == AckPolicyEachRequired {
		required = 1
	} else if policy == AckPolicyQuorum {
		required = record.Int(colCommAckQuorum)
	}
	return messageFromRecord(record, required)
}

type deliveryDriverDigestInput struct {
	TenantID            string     `json:"tenant_id"`
	WorkspaceID         string     `json:"workspace_id"`
	DeliveryID          string     `json:"delivery_id"`
	MessageID           string     `json:"message_id"`
	DispatchID          string     `json:"dispatch_id"`
	AttemptID           string     `json:"attempt_id"`
	EndpointID          string     `json:"endpoint_id"`
	EndpointGeneration  int64      `json:"endpoint_generation"`
	EndpointFingerprint []byte     `json:"endpoint_fingerprint"`
	Provider            string     `json:"provider"`
	Transport           string     `json:"transport"`
	OperationID         string     `json:"operation_id"`
	MessageKind         string     `json:"message_kind"`
	Urgency             string     `json:"urgency"`
	WorkItemID          string     `json:"work_item_id,omitempty"`
	AckDueAt            *time.Time `json:"ack_due_at,omitempty"`
}

func buildDeliveryDriverDispatch(
	scope DirectoryScopeRef,
	dispatch DeliveryDispatch,
	attemptID model.ID,
	delivery MessageDelivery,
	message Message,
	endpoint CommunicationEndpoint,
) (sdk.DeliveryDispatch, []byte, error) {
	identity, err := deliveryEndpointIdentity(scope, dispatch, endpoint)
	if err != nil {
		return sdk.DeliveryDispatch{}, nil, err
	}
	input := deliveryDriverDigestInput{
		TenantID: scope.TenantID.String(), WorkspaceID: scope.WorkspaceID.String(),
		DeliveryID: delivery.ID.String(), MessageID: message.ID.String(),
		DispatchID: dispatch.ID.String(), AttemptID: attemptID.String(),
		EndpointID: endpoint.ID.String(), EndpointGeneration: endpoint.Generation,
		EndpointFingerprint: identity.EndpointFingerprint(), Provider: endpoint.ProviderKey,
		Transport: endpoint.Transport, OperationID: dispatch.ID.String(),
		MessageKind: string(message.Kind), Urgency: string(message.Urgency),
		WorkItemID: message.WorkItemID.String(), AckDueAt: delivery.AckDueAt,
	}
	raw, err := canonicalJSON(input)
	if err != nil {
		return sdk.DeliveryDispatch{}, nil, err
	}
	digest := sha256.Sum256(raw)
	params := sdk.DeliveryDispatchParams{
		TenantID: input.TenantID, WorkspaceID: input.WorkspaceID,
		DeliveryID: input.DeliveryID, MessageID: input.MessageID,
		DispatchID: input.DispatchID, AttemptID: input.AttemptID,
		EndpointID: input.EndpointID, EndpointGeneration: uint64(input.EndpointGeneration),
		EndpointFingerprint: input.EndpointFingerprint, Provider: input.Provider,
		Transport: input.Transport, OperationID: sdk.OperationID(input.OperationID),
		RequestDigest: digest[:], MessageKind: sdk.DeliveryMessageKind(input.MessageKind),
		Urgency: sdk.DeliveryUrgency(input.Urgency), WorkItemID: input.WorkItemID,
	}
	if input.AckDueAt != nil {
		params.AckDueAt, err = deliverySDKTime(*input.AckDueAt)
		if err != nil {
			return sdk.DeliveryDispatch{}, nil, err
		}
	}
	request, err := sdk.NewDeliveryDispatch(params)
	if err != nil {
		return sdk.DeliveryDispatch{}, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery driver request is not representable",
		)
	}
	return request, digest[:], nil
}

func (s *deliveryDispatchService) settleAttempt(
	ctx context.Context,
	scope DirectoryScopeRef,
	claimed deliveryDispatchClaimed,
	observed sdk.DeliveryAttemptResult,
	preBoundaryCode string,
) (DeliveryDispatchState, int64, error) {
	dispatchID := model.ID(claimed.dispatch.DispatchID())
	attemptID := model.ID(claimed.dispatch.AttemptID())
	state := DispatchInFlight
	var auditSeq int64
	err := s.module.mutateCommunication(ctx, scope, func(tx *communicationTx) error {
		if err := tx.lockTransaction(ctx, deliveryDispatchIDLockKey(dispatchID)); err != nil {
			return err
		}
		dispatchRecord, err := tx.lockRecord(ctx, deliveryDispatchKind, dispatchID)
		if err != nil {
			return err
		}
		dispatch, err := deliveryDispatchFromRecord(dispatchRecord)
		if err != nil {
			return err
		}
		attemptRecord, err := tx.lockRecord(ctx, deliveryAttemptKind, attemptID)
		if err != nil {
			return err
		}
		attempt, err := deliveryAttemptFromRecord(attemptRecord)
		if err != nil {
			return err
		}
		deliveryRecord, err := tx.lockRecord(ctx, messageDeliveryKind, dispatch.DeliveryID)
		if err != nil {
			return err
		}
		delivery, err := messageDeliveryFromRecord(deliveryRecord)
		if err != nil {
			return err
		}
		if err = tx.lockAuditAppends(ctx); err != nil {
			return err
		}
		if err = tx.refreshNow(ctx); err != nil {
			return err
		}
		dbNow := tx.now.Time()
		finish, code, err := deliveryAttemptFinishInput(
			observed, preBoundaryCode, dbNow, s.config.ResolutionTTL,
		)
		if err != nil {
			return err
		}
		plan, err := PlanDispatchAttemptFinish(dispatch, attempt, claimed.owner, finish, dbNow)
		if err != nil {
			return err
		}
		attemptAfter, err := deliveryAttemptToRecord(plan.AttemptAfter)
		if err != nil {
			return err
		}
		attemptAfter[model.ColVersion] = plan.AttemptBefore.Version
		if _, err = tx.update(ctx, deliveryAttemptKind, attemptAfter); err != nil {
			return err
		}
		dispatchAfter, err := deliveryDispatchToRecord(plan.DispatchAfter)
		if err != nil {
			return err
		}
		dispatchAfter[model.ColVersion] = plan.DispatchBefore.Version
		if _, err = tx.update(ctx, deliveryDispatchKind, dispatchAfter); err != nil {
			return err
		}
		deliveryAfter, err := deliveryWithWakeEvidence(delivery, finish.Verdict, code, dbNow)
		if err != nil {
			return err
		}
		deliveryAfterRecord, err := messageDeliveryToRecord(deliveryAfter)
		if err != nil {
			return err
		}
		deliveryAfterRecord[model.ColVersion] = delivery.Version
		if _, err = tx.update(ctx, messageDeliveryKind, deliveryAfterRecord); err != nil {
			return err
		}
		auditSeq, err = appendDeliveryDispatchAudit(
			ctx, tx, deliveryDispatchFinishAuditAction, dispatch.ID, plan,
			map[string]any{
				"workspace_id": scope.WorkspaceID.String(), "attempt_id": attempt.ID.String(),
				"state": string(plan.DispatchAfter.State), "code": code,
			},
		)
		if err != nil {
			return err
		}
		state = plan.DispatchAfter.State
		return nil
	})
	return state, auditSeq, err
}

func deliveryAttemptFinishInput(
	result sdk.DeliveryAttemptResult,
	preBoundaryCode string,
	dbNow time.Time,
	resolutionTTL time.Duration,
) (DispatchAttemptFinishInput, string, error) {
	result = sdk.NormalizeDeliveryAttemptResult(result, nil)
	deadline, err := deliveryDispatchFuture(dbNow, resolutionTTL)
	if err != nil {
		return DispatchAttemptFinishInput{}, "", err
	}
	switch result.Outcome() {
	case sdk.DeliveryAttemptAccepted:
		return DispatchAttemptFinishInput{
			TargetState: DispatchSucceeded, TransmitBoundary: TransmitCrossed,
			Verdict: VerdictClean, Code: "provider_accepted",
			ProviderReceiptHash: result.ProviderReceiptHash(),
		}, "provider_accepted", nil
	case sdk.DeliveryAttemptRefusedBeforeBoundary:
		code := preBoundaryCode
		if code == "" {
			code = "provider_refused_before_boundary"
		}
		return DispatchAttemptFinishInput{
			TargetState: DispatchFailed, TransmitBoundary: TransmitNotCrossed,
			Verdict: VerdictBroken, Code: code, ResolutionDeadlineAt: &deadline,
		}, code, nil
	default:
		return DispatchAttemptFinishInput{
			TargetState: DispatchUnknown, TransmitBoundary: TransmitUnknown,
			Verdict: VerdictUnknown, Code: "provider_outcome_indeterminate",
			ResolutionDeadlineAt: &deadline,
		}, "provider_outcome_indeterminate", nil
	}
}

func deliveryWithWakeEvidence(
	delivery MessageDelivery,
	verdict AssessmentVerdict,
	code string,
	dbNow time.Time,
) (MessageDelivery, error) {
	if err := ValidateMessageDelivery(delivery); err != nil {
		return MessageDelivery{}, err
	}
	if !validAssessmentVerdict(verdict) || !boundedToken(code, 128) ||
		dbNow.Before(delivery.UpdatedAt) || dbNow.Before(delivery.AvailableAt) {
		return MessageDelivery{}, communicationError(
			ErrInvalidCommunicationModel, "invalid delivery wake evidence",
		)
	}
	after := delivery
	after.Version++
	after.UpdatedAt = dbNow
	after.LastWakeVerdict = verdict
	after.LastWakeCode = code
	after.LastWakeAt = &dbNow
	if err := ValidateMessageDelivery(after); err != nil {
		return MessageDelivery{}, err
	}
	return after, nil
}

// AbandonExpired is the crash-recovery path. It never assumes the external
// boundary was not crossed, so it always transitions the exact reserved
// attempt to UNKNOWN and leaves it for explicit reconciliation.
func (s *deliveryDispatchService) AbandonExpired(
	ctx context.Context,
	scope DirectoryScopeRef,
	dispatchID model.ID,
) (deliveryDispatchPumpResult, error) {
	if s == nil || s.module == nil || !validCanonicalCommunicationID(dispatchID) {
		return deliveryDispatchPumpResult{}, communicationError(
			ErrInvalidCommunicationModel, "invalid dispatch abandonment request",
		)
	}
	result := deliveryDispatchPumpResult{DispatchID: dispatchID, State: DispatchInFlight}
	err := s.module.mutateCommunication(ctx, scope, func(tx *communicationTx) error {
		if err := tx.lockTransaction(ctx, deliveryDispatchIDLockKey(dispatchID)); err != nil {
			return err
		}
		dispatchRecord, err := tx.lockRecord(ctx, deliveryDispatchKind, dispatchID)
		if err != nil {
			return err
		}
		dispatch, err := deliveryDispatchFromRecord(dispatchRecord)
		if err != nil {
			return err
		}
		attemptID, err := deliveryAttemptIDForDispatch(ctx, tx, dispatch.ID)
		if err != nil {
			return err
		}
		attemptRecord, err := tx.lockRecord(ctx, deliveryAttemptKind, attemptID)
		if err != nil {
			return err
		}
		attempt, err := deliveryAttemptFromRecord(attemptRecord)
		if err != nil {
			return err
		}
		deliveryRecord, err := tx.lockRecord(ctx, messageDeliveryKind, dispatch.DeliveryID)
		if err != nil {
			return err
		}
		delivery, err := messageDeliveryFromRecord(deliveryRecord)
		if err != nil {
			return err
		}
		if err = tx.lockAuditAppends(ctx); err != nil {
			return err
		}
		if err = tx.refreshNow(ctx); err != nil {
			return err
		}
		dbNow := tx.now.Time()
		if dispatch.State != DispatchInFlight || dispatch.ClaimUntil == nil ||
			dbNow.Before(*dispatch.ClaimUntil) {
			return communicationError(
				ErrInvalidCommunicationTransition, "delivery dispatch claim has not expired",
			)
		}
		deadline, err := deliveryDispatchFuture(dbNow, s.config.ResolutionTTL)
		if err != nil {
			return err
		}
		plan, err := PlanDispatchAttemptAbandon(
			dispatch, attempt, dispatch.ClaimOwner,
			"claim_expired_boundary_unknown", deadline, dbNow,
		)
		if err != nil {
			return err
		}
		attemptAfter, err := deliveryAttemptToRecord(plan.AttemptAfter)
		if err != nil {
			return err
		}
		attemptAfter[model.ColVersion] = plan.AttemptBefore.Version
		if _, err = tx.update(ctx, deliveryAttemptKind, attemptAfter); err != nil {
			return err
		}
		dispatchAfter, err := deliveryDispatchToRecord(plan.DispatchAfter)
		if err != nil {
			return err
		}
		dispatchAfter[model.ColVersion] = plan.DispatchBefore.Version
		if _, err = tx.update(ctx, deliveryDispatchKind, dispatchAfter); err != nil {
			return err
		}
		deliveryAfter, err := deliveryWithWakeEvidence(
			delivery, VerdictUnknown, "claim_expired_boundary_unknown", dbNow,
		)
		if err != nil {
			return err
		}
		deliveryAfterRecord, err := messageDeliveryToRecord(deliveryAfter)
		if err != nil {
			return err
		}
		deliveryAfterRecord[model.ColVersion] = delivery.Version
		if _, err = tx.update(ctx, messageDeliveryKind, deliveryAfterRecord); err != nil {
			return err
		}
		result.AttemptID = attempt.ID
		result.State = DispatchUnknown
		result.Claimed = true
		result.AuditSeq, err = appendDeliveryDispatchAudit(
			ctx, tx, deliveryDispatchAbandonAuditAction, dispatch.ID, plan,
			map[string]any{
				"workspace_id": scope.WorkspaceID.String(), "attempt_id": attempt.ID.String(),
				"state": string(DispatchUnknown),
			},
		)
		return err
	})
	return result, err
}

type deliveryDispatchReconcileResult struct {
	DispatchID model.ID
	AttemptID  model.ID
	State      DeliveryDispatchState
	AuditSeq   int64
	Changed    bool
}

type deliveryDispatchReconcilePreflight struct {
	dispatch DeliveryDispatch
	attempt  DeliveryAttempt
	request  sdk.DeliveryDispatch
	witness  sdk.DeliveryCapabilityWitness
}

// ReconcileUnknown performs a provider observation, never a resend. The
// observation is outside the transaction; the second short transaction binds
// it to the exact UNKNOWN generation and DB observation time.
func (s *deliveryDispatchService) ReconcileUnknown(
	ctx context.Context,
	scope DirectoryScopeRef,
	dispatchID model.ID,
) (deliveryDispatchReconcileResult, error) {
	preflight, err := s.reconciliationPreflight(ctx, scope, dispatchID)
	if err != nil {
		return deliveryDispatchReconcileResult{}, err
	}
	capabilities := preflight.witness.Capabilities()
	if !preflight.witness.Matches(preflight.request.EndpointIdentity()) ||
		!capabilities.Wake() || !capabilities.Idempotency() ||
		!capabilities.Reconcile() || capabilities.ActiveTurn() {
		return deliveryDispatchReconcileResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery reconciliation capability is unavailable",
		)
	}
	reconciliation, err := sdk.NewDeliveryReconciliation(preflight.request)
	if err != nil {
		return deliveryDispatchReconcileResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery reconciliation request is unavailable",
		)
	}
	observed, reconcileErr := s.notifier.Reconcile(ctx, reconciliation)
	observed = sdk.NormalizeDeliveryReconciliationResult(observed, reconcileErr)
	return s.settleReconciliation(ctx, scope, preflight, observed)
}

func (s *deliveryDispatchService) reconciliationPreflight(
	ctx context.Context,
	scope DirectoryScopeRef,
	dispatchID model.ID,
) (deliveryDispatchReconcilePreflight, error) {
	if s == nil || s.module == nil || s.notifier == nil ||
		!validCanonicalCommunicationID(dispatchID) {
		return deliveryDispatchReconcilePreflight{}, communicationError(
			ErrInvalidCommunicationModel, "invalid delivery reconciliation request",
		)
	}
	var preflight deliveryDispatchReconcilePreflight
	var endpointIdentity sdk.DeliveryEndpointIdentity
	err := s.module.viewCommunication(ctx, scope, func(sc store.Scope) error {
		dispatchRepo, err := sc.Ext(deliveryDispatchKind)
		if err != nil {
			return err
		}
		dispatchRecord, err := dispatchRepo.Get(ctx, dispatchID)
		if err != nil {
			return err
		}
		dispatch, err := deliveryDispatchFromRecord(dispatchRecord)
		if err != nil || dispatch.State != DispatchUnknown {
			return communicationError(
				ErrInvalidCommunicationTransition, "delivery dispatch is not UNKNOWN",
			)
		}
		attemptRepo, err := sc.Ext(deliveryAttemptKind)
		if err != nil {
			return err
		}
		attemptRows, page, err := attemptRepo.List(ctx, model.Query{Filters: []model.Filter{
			{Column: colCommDispatchID, Op: model.OpEq, Value: dispatch.ID.String()},
		}, Limit: 2})
		if err != nil {
			return err
		}
		if len(attemptRows) != 1 || page.HasMore {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "delivery Attempt uniqueness is unavailable",
			)
		}
		attempt, err := deliveryAttemptFromRecord(attemptRows[0])
		if err != nil {
			return err
		}
		deliveryRepo, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		deliveryRecord, err := deliveryRepo.Get(ctx, dispatch.DeliveryID)
		if err != nil {
			return err
		}
		delivery, err := messageDeliveryFromRecord(deliveryRecord)
		if err != nil {
			return err
		}
		messageRepo, err := sc.Ext(messageKind)
		if err != nil {
			return err
		}
		messageRecord, err := messageRepo.Get(ctx, delivery.MessageID)
		if err != nil {
			return err
		}
		message, err := deliveryMessageFromRecord(messageRecord)
		if err != nil {
			return err
		}
		endpointRepo, err := sc.Ext(communicationEndpointKind)
		if err != nil {
			return err
		}
		endpointRecord, err := endpointRepo.Get(ctx, dispatch.EndpointID)
		if err != nil {
			return err
		}
		endpoint, err := communicationEndpointFromRecord(endpointRecord)
		if err != nil {
			return err
		}
		request, digest, err := buildDeliveryDriverDispatch(
			scope, dispatch, attempt.ID, delivery, message, endpoint,
		)
		if err != nil || !bytes.Equal(digest, attempt.RequestHash) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "delivery reconciliation binding drifted",
			)
		}
		endpointIdentity = request.EndpointIdentity()
		preflight = deliveryDispatchReconcilePreflight{
			dispatch: dispatch, attempt: attempt, request: request,
		}
		return nil
	})
	if err != nil {
		return deliveryDispatchReconcilePreflight{}, err
	}
	witness, err := s.notifier.Capabilities(ctx, endpointIdentity)
	if err != nil {
		return deliveryDispatchReconcilePreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery reconciliation capability is unavailable",
		)
	}
	witness = sdk.NormalizeDeliveryCapabilityWitness(witness, err)
	if _, generationErr := deliverySDKGeneration(witness.Endpoint().EndpointGeneration()); generationErr != nil || !witness.Matches(endpointIdentity) {
		return deliveryDispatchReconcilePreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery reconciliation capability is not exact",
		)
	}
	preflight.witness = witness
	return preflight, nil
}

func (s *deliveryDispatchService) settleReconciliation(
	ctx context.Context,
	scope DirectoryScopeRef,
	preflight deliveryDispatchReconcilePreflight,
	observed sdk.DeliveryReconciliationResult,
) (deliveryDispatchReconcileResult, error) {
	result := deliveryDispatchReconcileResult{
		DispatchID: preflight.dispatch.ID, AttemptID: preflight.attempt.ID,
		State: DispatchUnknown,
	}
	observed = sdk.NormalizeDeliveryReconciliationResult(observed, nil)
	err := s.module.mutateCommunication(ctx, scope, func(tx *communicationTx) error {
		if err := tx.lockTransaction(ctx, deliveryDispatchIDLockKey(preflight.dispatch.ID)); err != nil {
			return err
		}
		dispatchRecord, err := tx.lockRecord(ctx, deliveryDispatchKind, preflight.dispatch.ID)
		if err != nil {
			return err
		}
		dispatch, err := deliveryDispatchFromRecord(dispatchRecord)
		if err != nil {
			return err
		}
		if dispatch.State != DispatchUnknown {
			result.State = dispatch.State
			return nil
		}
		attemptRecord, err := tx.lockRecord(ctx, deliveryAttemptKind, preflight.attempt.ID)
		if err != nil {
			return err
		}
		attempt, err := deliveryAttemptFromRecord(attemptRecord)
		if err != nil {
			return err
		}
		deliveryRecord, err := tx.lockRecord(ctx, messageDeliveryKind, dispatch.DeliveryID)
		if err != nil {
			return err
		}
		delivery, err := messageDeliveryFromRecord(deliveryRecord)
		if err != nil {
			return err
		}
		endpointRecord, err := tx.lockRecord(ctx, communicationEndpointKind, dispatch.EndpointID)
		if err != nil {
			return err
		}
		endpoint, err := communicationEndpointFromRecord(endpointRecord)
		if err != nil {
			return err
		}
		if err = tx.lockAuditAppends(ctx); err != nil {
			return err
		}
		if err = tx.refreshNow(ctx); err != nil {
			return err
		}
		dbNow := tx.now.Time()
		identity, err := deliveryEndpointIdentity(scope, dispatch, endpoint)
		if err != nil || !preflight.witness.Matches(identity) ||
			!identity.Equal(preflight.request.EndpointIdentity()) ||
			attempt.ID != preflight.attempt.ID || !bytes.Equal(attempt.RequestHash, preflight.request.RequestDigest()) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "delivery reconciliation drifted before settlement",
			)
		}
		witness := DispatchProviderAcceptanceWitness{
			DispatchID: dispatch.ID, AttemptID: attempt.ID,
			EndpointID: dispatch.EndpointID, EndpointGeneration: dispatch.EndpointGeneration,
			ObservedAt: dbNow,
		}
		var target DeliveryDispatchState
		var deadline *time.Time
		var code string
		switch observed.Outcome() {
		case sdk.DeliveryReconciliationAccepted:
			target, code = DispatchSucceeded, "provider_reconciled_accepted"
			witness.Acceptance = AuthorityEvidence{
				Verdict: VerdictClean, Code: code, EvidenceRef: observed.EvidenceRef(),
			}
		case sdk.DeliveryReconciliationNotAccepted:
			target, code = DispatchFailed, "provider_reconciled_not_accepted"
			future, futureErr := deliveryDispatchFuture(dbNow, s.config.ResolutionTTL)
			if futureErr != nil {
				return futureErr
			}
			deadline = &future
			witness.Acceptance = AuthorityEvidence{
				Verdict: VerdictBroken, Code: code, EvidenceRef: observed.EvidenceRef(),
			}
		default:
			if dispatch.ResolutionDeadlineAt == nil || dbNow.Before(*dispatch.ResolutionDeadlineAt) {
				result.AuditSeq, err = appendDeliveryDispatchAudit(
					ctx, tx, deliveryDispatchReconcileAuditAction, dispatch.ID,
					struct {
						DispatchID model.ID `json:"dispatch_id"`
						AttemptID  model.ID `json:"attempt_id"`
						Outcome    string   `json:"outcome"`
					}{dispatch.ID, attempt.ID, "indeterminate"},
					map[string]any{
						"workspace_id": scope.WorkspaceID.String(), "attempt_id": attempt.ID.String(),
						"state": string(DispatchUnknown),
					},
				)
				return err
			}
			target, code = DispatchDeadLetter, "provider_reconcile_deadline_elapsed"
			witness.Acceptance = AuthorityEvidence{
				Verdict: VerdictUnknown, Code: code,
				EvidenceRef: "dispatch-reconcile:" + attempt.ID.String(),
			}
		}
		plan, err := PlanDispatchReconcile(dispatch, attempt, witness, target, code, deadline, dbNow)
		if err != nil {
			return err
		}
		dispatchAfter, err := deliveryDispatchToRecord(plan.After)
		if err != nil {
			return err
		}
		dispatchAfter[model.ColVersion] = plan.Before.Version
		if _, err = tx.update(ctx, deliveryDispatchKind, dispatchAfter); err != nil {
			return err
		}
		deliveryAfter, err := deliveryWithWakeEvidence(delivery, plan.After.LastVerdict, code, dbNow)
		if err != nil {
			return err
		}
		deliveryAfterRecord, err := messageDeliveryToRecord(deliveryAfter)
		if err != nil {
			return err
		}
		deliveryAfterRecord[model.ColVersion] = delivery.Version
		if _, err = tx.update(ctx, messageDeliveryKind, deliveryAfterRecord); err != nil {
			return err
		}
		result.AuditSeq, err = appendDeliveryDispatchAudit(
			ctx, tx, deliveryDispatchReconcileAuditAction, dispatch.ID,
			struct {
				Plan        DispatchReconcilePlan `json:"plan"`
				ReceiptHash []byte                `json:"receipt_hash,omitempty"`
			}{Plan: plan, ReceiptHash: observed.ProviderReceiptHash()},
			map[string]any{
				"workspace_id": scope.WorkspaceID.String(), "attempt_id": attempt.ID.String(),
				"state": string(plan.After.State), "code": code,
			},
		)
		if err != nil {
			return err
		}
		result.State = plan.After.State
		result.Changed = true
		return nil
	})
	return result, err
}

func deliveryAttemptIDForDispatch(
	ctx context.Context,
	tx *communicationTx,
	dispatchID model.ID,
) (model.ID, error) {
	repo, err := tx.repo(deliveryAttemptKind)
	if err != nil {
		return "", err
	}
	rows, page, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		{Column: colCommDispatchID, Op: model.OpEq, Value: dispatchID.String()},
	}, Limit: 2})
	if err != nil {
		return "", err
	}
	if len(rows) != 1 || page.HasMore {
		return "", communicationError(
			ErrCommunicationEvidenceUnknown, "delivery Attempt uniqueness is unavailable",
		)
	}
	attempt, err := deliveryAttemptFromRecord(rows[0])
	if err != nil {
		return "", err
	}
	return attempt.ID, nil
}

func deliveryDispatchFuture(now time.Time, ttl time.Duration) (time.Time, error) {
	deadline := now.Add(ttl)
	if ttl <= 0 || !deadline.After(now) || deadline.Year() < 1 || deadline.Year() > 9999 {
		return time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch deadline is unavailable",
		)
	}
	return deadline.UTC(), nil
}

// deliverySDKGeneration is the only SDK uint64 -> durable int64 conversion
// seam. Keeping the check explicit prevents a future driver-supplied identity
// from wrapping a generation before it is compared with durable state.
func deliverySDKGeneration(generation uint64) (int64, error) {
	if generation == 0 || generation > uint64(math.MaxInt64) {
		return 0, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery endpoint generation is not representable",
		)
	}
	return int64(generation), nil
}

// deliverySDKTime proves that the SDK's time value has an exact canonical
// database representation. ParseTimestamp round-trips nanoseconds, so this
// adapter cannot silently truncate AckDueAt.
func deliverySDKTime(value time.Time) (time.Time, error) {
	canonical := value.UTC()
	if canonical.IsZero() || canonical.Year() < 1 || canonical.Year() > 9999 {
		return time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery AckDueAt is not representable",
		)
	}
	encoded := model.NewTimestamp(canonical).String()
	decoded, err := model.ParseTimestamp(encoded)
	if err != nil || !decoded.Time().Equal(canonical) {
		return time.Time{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery AckDueAt loses canonical precision",
		)
	}
	return decoded.Time(), nil
}

func deliveryDispatchPumpLockKey(scope DirectoryScopeRef) string {
	return "sessions.communication.dispatch.pump:" + scope.WorkspaceID.String()
}

func deliveryDispatchIDLockKey(dispatchID model.ID) string {
	return "sessions.communication.dispatch:" + dispatchID.String()
}

func appendDeliveryDispatchAudit(
	ctx context.Context,
	tx *communicationTx,
	action string,
	dispatchID model.ID,
	payload any,
	meta map[string]any,
) (int64, error) {
	if tx == nil || !boundedToken(action, 128) || !validCanonicalCommunicationID(dispatchID) {
		return 0, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch audit input is unavailable",
		)
	}
	raw, err := canonicalJSON(payload)
	if err != nil {
		return 0, err
	}
	hash := sha256.Sum256(raw)
	audit, err := tx.appendAudit(ctx, model.AuditDraft{
		Actor: "olivares.delivery-dispatch", ActorKind: model.ActorSystem,
		Action: action, TargetKind: deliveryDispatchKind, TargetID: dispatchID,
		PayloadHash: hash[:], Meta: meta,
	})
	if err != nil {
		return 0, err
	}
	if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
		return 0, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch audit returned no durable anchor",
		)
	}
	return audit.Seq, nil
}
