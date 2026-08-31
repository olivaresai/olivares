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
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	deliveryDispatchSuccessorAuditAction  = "sessions.communication.dispatch.successor"
	deliveryDispatchDeadLetterAuditAction = "sessions.communication.dispatch.dead_letter"
	deliveryDispatchSuccessorScope        = "delivery.dispatch.successor"
	deliveryDispatchRootHistoryBound      = 4096
)

// deliveryDispatchSuccessorAuthorizer is the private operations authority
// port. K3 does not wire this service while the aggregate readiness conjunction
// is OFF. A future composition root must provide current, closed evidence for
// this exact predecessor and successor route.
type deliveryDispatchSuccessorAuthorizer interface {
	AuthorizeDeliveryDispatchSuccessor(
		context.Context,
		DirectoryScopeRef,
		model.ID,
		DispatchRouteIdentity,
	) (AuthorityEvidence, error)
}

type deliveryDispatchSuccessorService struct {
	module     *Module
	authorizer deliveryDispatchSuccessorAuthorizer
	attestor   DispatchRouteAttestor
	newID      func() model.ID
}

func newDeliveryDispatchSuccessorService(
	module *Module,
	authorizer deliveryDispatchSuccessorAuthorizer,
	attestor DispatchRouteAttestor,
	newID func() model.ID,
) (*deliveryDispatchSuccessorService, error) {
	if module == nil || authorizer == nil || attestor == nil {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"delivery dispatch successor ports are unavailable",
		)
	}
	if newID == nil {
		newID = model.NewID
	}
	return &deliveryDispatchSuccessorService{
		module: module, authorizer: authorizer, attestor: attestor, newID: newID,
	}, nil
}

type deliveryDispatchSuccessorCommand struct {
	PredecessorID   model.ID
	ExpectedVersion int64
	SuccessorRoute  DispatchRouteIdentity
	IdempotencyKey  string
}

type deliveryDispatchSuccessorResult struct {
	CommandID           model.ID
	PredecessorID       model.ID
	PredecessorVersion  int64
	SuccessorID         model.ID
	SuccessorGeneration int64
	RerouteRung         int64
	AuditSeq            int64
	Replayed            bool
}

type deliveryDispatchDeadLetterResult struct {
	DispatchID model.ID
	AttemptID  model.ID
	Version    int64
	State      DeliveryDispatchState
	AuditSeq   int64
	Changed    bool
}

type deliveryDispatchSuccessorNormalized struct {
	command              deliveryDispatchSuccessorCommand
	scope                DirectoryScopeRef
	actorFingerprint     []byte
	idempotencyKeyHash   []byte
	requestDigest        []byte
	commandAuthorization AuthorityEvidence
	priorRoute           DispatchRouteAttestation
}

type deliveryDispatchSuccessorRequestDigest struct {
	Command         string                `json:"command"`
	TenantID        model.TenantID        `json:"tenant_id"`
	WorkspaceID     model.ID              `json:"workspace_id"`
	PredecessorID   model.ID              `json:"predecessor_id"`
	ExpectedVersion int64                 `json:"expected_version"`
	SuccessorRoute  DispatchRouteIdentity `json:"successor_route"`
}

func (s *deliveryDispatchSuccessorService) CreateSuccessor(
	ctx context.Context,
	scope DirectoryScopeRef,
	command deliveryDispatchSuccessorCommand,
) (deliveryDispatchSuccessorResult, error) {
	if s == nil || s.module == nil || s.authorizer == nil || s.attestor == nil {
		return deliveryDispatchSuccessorResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"delivery dispatch successor service is unavailable",
		)
	}
	normalized, err := s.normalize(ctx, scope, command)
	if err != nil {
		return deliveryDispatchSuccessorResult{}, err
	}
	ids := deliveryDispatchSuccessorIDs{
		Successor: s.newID(), Command: s.newID(), Receipt: s.newID(),
	}
	if !ids.valid() {
		return deliveryDispatchSuccessorResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"delivery dispatch successor identities are unavailable",
		)
	}
	result, err := s.apply(ctx, normalized, ids)
	return result, normalizeDeliveryDispatchSuccessorError(err)
}

// DeadLetterExpired closes one failed generation after its DB-time resolution
// deadline. It performs no provider I/O and never mutates a successor or turns
// the failed row back into pending.
func (s *deliveryDispatchSuccessorService) DeadLetterExpired(
	ctx context.Context,
	scope DirectoryScopeRef,
	dispatchID model.ID,
) (deliveryDispatchDeadLetterResult, error) {
	if s == nil || s.module == nil || ctx == nil || !validCanonicalCommunicationID(dispatchID) {
		return deliveryDispatchDeadLetterResult{}, communicationError(
			ErrInvalidCommunicationModel, "invalid delivery dispatch dead-letter request",
		)
	}
	if err := scope.Validate(); err != nil {
		return deliveryDispatchDeadLetterResult{}, err
	}
	result := deliveryDispatchDeadLetterResult{DispatchID: dispatchID}
	err := s.module.mutateCommunication(ctx, scope, func(tx *communicationTx) error {
		if err := tx.lockTransaction(ctx, deliveryDispatchIDLockKey(dispatchID)); err != nil {
			return err
		}
		record, err := tx.lockRecord(ctx, deliveryDispatchKind, dispatchID)
		if err != nil {
			return err
		}
		dispatch, err := deliveryDispatchFromRecord(record)
		if err != nil || dispatch.TenantID != scope.TenantID ||
			dispatch.WorkspaceID != scope.WorkspaceID {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "failed delivery dispatch is unavailable",
			)
		}
		if dispatch.State == DispatchDeadLetter {
			result.Version = dispatch.Version
			result.State = dispatch.State
			return nil
		}
		if dispatch.State != DispatchFailed {
			return communicationError(
				ErrInvalidCommunicationTransition, "delivery dispatch is not failed",
			)
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
			return communicationError(
				ErrCommunicationEvidenceUnknown, "failed delivery dispatch Attempt is malformed",
			)
		}
		if err := tx.lockAuditAppends(ctx); err != nil {
			return err
		}
		if err := tx.refreshNow(ctx); err != nil {
			return err
		}
		dbNow := tx.now.Time()
		plan, err := PlanFailedDispatchDeadLetter(
			dispatch, attempt,
			DispatchDeadLetterWitness{
				DispatchID: dispatch.ID, AttemptID: attempt.ID, ObservedAt: dbNow,
				Evidence: AuthorityEvidence{
					Verdict: VerdictClean, Code: "dispatch_deadline_observed",
					EvidenceRef: "dispatch-deadline:" + dispatch.ID.String(),
				},
			},
			"resolution_deadline_elapsed", dbNow,
		)
		if err != nil {
			return err
		}
		after, err := deliveryDispatchToRecord(plan.After)
		if err != nil {
			return err
		}
		after[model.ColVersion] = plan.Before.Version
		if _, err = tx.update(ctx, deliveryDispatchKind, after); err != nil {
			return err
		}
		auditSeq, err := appendDeliveryDispatchAudit(
			ctx, tx, deliveryDispatchDeadLetterAuditAction, dispatch.ID, plan,
			map[string]any{
				"workspace_id": scope.WorkspaceID.String(),
				"attempt_id":   attempt.ID.String(),
				"state":        string(DispatchDeadLetter),
			},
		)
		if err != nil {
			return err
		}
		result.AttemptID = attempt.ID
		result.Version = plan.After.Version
		result.State = plan.After.State
		result.AuditSeq = auditSeq
		result.Changed = true
		return nil
	})
	return result, normalizeDeliveryDispatchSuccessorError(err)
}

func (s *deliveryDispatchSuccessorService) normalize(
	ctx context.Context,
	scope DirectoryScopeRef,
	command deliveryDispatchSuccessorCommand,
) (deliveryDispatchSuccessorNormalized, error) {
	if ctx == nil {
		return deliveryDispatchSuccessorNormalized{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor context is unavailable",
		)
	}
	if err := scope.Validate(); err != nil {
		return deliveryDispatchSuccessorNormalized{}, err
	}
	if !validCanonicalCommunicationID(command.PredecessorID) || command.ExpectedVersion < 1 {
		return deliveryDispatchSuccessorNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "invalid delivery dispatch successor target",
		)
	}
	if err := ValidateDispatchRouteIdentity(command.SuccessorRoute); err != nil {
		return deliveryDispatchSuccessorNormalized{}, err
	}
	idempotencyID, err := model.ParseID(command.IdempotencyKey)
	if err != nil || idempotencyID.String() != command.IdempotencyKey {
		return deliveryDispatchSuccessorNormalized{}, communicationError(
			ErrInvalidCommunicationModel, "delivery dispatch successor requires a canonical idempotency key",
		)
	}
	authorization, err := s.authorizer.AuthorizeDeliveryDispatchSuccessor(
		ctx, scope, command.PredecessorID, command.SuccessorRoute,
	)
	if err != nil || ValidateAuthorityEvidence(authorization) != nil ||
		evidenceVerdict(authorization) != VerdictClean {
		return deliveryDispatchSuccessorNormalized{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"delivery dispatch successor authority is unavailable",
		)
	}
	priorRoute, err := s.attestor.AttestDispatchRoute(ctx, scope, command.PredecessorID)
	if err != nil || priorRoute.DispatchID != command.PredecessorID ||
		ValidateDispatchRouteIdentity(priorRoute.Route) != nil ||
		priorRoute.ObservedAt.IsZero() || ValidateAuthorityEvidence(priorRoute.Evidence) != nil ||
		evidenceVerdict(priorRoute.Evidence) != VerdictClean {
		return deliveryDispatchSuccessorNormalized{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"delivery dispatch prior route is unavailable",
		)
	}
	requestBytes, err := canonicalJSON(deliveryDispatchSuccessorRequestDigest{
		Command:  deliveryDispatchSuccessorScope,
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID,
		PredecessorID: command.PredecessorID, ExpectedVersion: command.ExpectedVersion,
		SuccessorRoute: command.SuccessorRoute,
	})
	if err != nil {
		return deliveryDispatchSuccessorNormalized{}, err
	}
	actor := sha256.Sum256([]byte(
		"olivares.delivery-dispatch-successor\x00" + scope.TenantID.String() + "\x00" +
			scope.WorkspaceID.String(),
	))
	idempotency := sha256.Sum256([]byte(command.IdempotencyKey))
	request := sha256.Sum256(requestBytes)
	return deliveryDispatchSuccessorNormalized{
		command: command, scope: scope, actorFingerprint: actor[:],
		idempotencyKeyHash: idempotency[:], requestDigest: request[:],
		commandAuthorization: authorization, priorRoute: priorRoute,
	}, nil
}

type deliveryDispatchSuccessorIDs struct {
	Successor model.ID
	Command   model.ID
	Receipt   model.ID
}

func (ids deliveryDispatchSuccessorIDs) valid() bool {
	return validCanonicalCommunicationID(ids.Successor) &&
		validCanonicalCommunicationID(ids.Command) &&
		validCanonicalCommunicationID(ids.Receipt) &&
		ids.Successor != ids.Command && ids.Successor != ids.Receipt && ids.Command != ids.Receipt
}

func (s *deliveryDispatchSuccessorService) apply(
	ctx context.Context,
	normalized deliveryDispatchSuccessorNormalized,
	ids deliveryDispatchSuccessorIDs,
) (deliveryDispatchSuccessorResult, error) {
	var result deliveryDispatchSuccessorResult
	err := s.module.mutateCommunication(ctx, normalized.scope, func(tx *communicationTx) error {
		if err := tx.lockTransaction(ctx, deliveryDispatchSuccessorLockKey(normalized)); err != nil {
			return err
		}
		receipt, found, err := lookupDeliveryDispatchSuccessorReceipt(ctx, tx, normalized)
		if err != nil {
			return err
		}
		if found {
			replayed, replayErr := replayDeliveryDispatchSuccessor(
				ctx, tx, normalized, receipt,
			)
			if replayErr != nil {
				return replayErr
			}
			result = replayed
			result.Replayed = true
			return nil
		}

		predecessorRecord, err := tx.lockRecord(
			ctx, deliveryDispatchKind, normalized.command.PredecessorID,
		)
		if err != nil {
			return err
		}
		predecessor, err := deliveryDispatchFromRecord(predecessorRecord)
		if err != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "locked predecessor dispatch is malformed",
			)
		}
		if predecessor.TenantID != normalized.scope.TenantID ||
			predecessor.WorkspaceID != normalized.scope.WorkspaceID {
			return communicationError(ErrCommunicationNotFound, "delivery dispatch is not visible")
		}
		if predecessor.State == DispatchSuperseded {
			return fmt.Errorf("%w: delivery dispatch already has a successor", store.ErrConflict)
		}
		if predecessor.Version != normalized.command.ExpectedVersion {
			return fmt.Errorf("%w: delivery dispatch version changed", store.ErrConflict)
		}
		if predecessor.State != DispatchFailed {
			return communicationError(
				ErrInvalidCommunicationTransition,
				"delivery dispatch must be failed before successor creation",
			)
		}
		attemptID, err := deliveryAttemptIDForDispatch(ctx, tx, predecessor.ID)
		if err != nil {
			return err
		}
		attemptRecord, err := tx.lockRecord(ctx, deliveryAttemptKind, attemptID)
		if err != nil {
			return err
		}
		attempt, err := deliveryAttemptFromRecord(attemptRecord)
		if err != nil {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "locked predecessor Attempt is malformed",
			)
		}
		history, err := lockDeliveryDispatchRootHistory(ctx, tx, predecessor)
		if err != nil {
			return err
		}
		if err := validateDeliveryDispatchSuccessorRoute(
			ctx, tx, normalized.scope, predecessor, normalized.command.SuccessorRoute,
		); err != nil {
			return err
		}
		if err := tx.lockAuditAppends(ctx); err != nil {
			return err
		}
		if err := tx.refreshNow(ctx); err != nil {
			return err
		}
		dbNow := tx.now.Time()
		if normalized.priorRoute.ObservedAt.After(dbNow) ||
			normalized.priorRoute.ObservedAt.Before(predecessor.UpdatedAt) ||
			!normalized.priorRoute.Route.Equal(dispatchRouteIdentity(predecessor)) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "delivery dispatch prior route changed",
			)
		}
		history.ObservedAt = dbNow
		history.Evidence = AuthorityEvidence{
			Verdict: VerdictClean, Code: "dispatch_root_history_locked",
			EvidenceRef: "dispatch-root:" + predecessor.RootDispatchID.String(),
		}
		authority := DispatchSuccessorAuthority{
			PredecessorID: predecessor.ID, AttemptID: attempt.ID,
			RootHistory: history, CommandAuthorization: normalized.commandAuthorization,
			EvidenceRef: normalized.commandAuthorization.EvidenceRef,
		}
		plan, err := PlanDispatchSuccessor(
			predecessor, attempt, authority, normalized.priorRoute,
			ids.Successor, normalized.command.SuccessorRoute,
			normalized.idempotencyKeyHash, dbNow,
		)
		if err != nil {
			return err
		}
		planBytes, err := canonicalJSON(plan)
		if err != nil {
			return err
		}
		planHash := sha256.Sum256(planBytes)
		predecessorAfter, err := deliveryDispatchToRecord(plan.PredecessorAfter)
		if err != nil {
			return err
		}
		predecessorAfter[model.ColVersion] = plan.PredecessorBefore.Version
		if _, err = tx.update(ctx, deliveryDispatchKind, predecessorAfter); err != nil {
			return err
		}
		successorRecord, err := deliveryDispatchToRecord(plan.Successor)
		if err != nil {
			return err
		}
		if _, err = tx.createWithID(
			ctx, deliveryDispatchKind, plan.Successor.ID, successorRecord,
		); err != nil {
			return err
		}
		audit, err := tx.appendAudit(ctx, model.AuditDraft{
			Actor: "olivares.delivery-dispatch", ActorKind: model.ActorSystem,
			Action:     deliveryDispatchSuccessorAuditAction,
			TargetKind: deliveryDispatchKind, TargetID: predecessor.ID,
			PayloadHash: planHash[:],
			Meta: map[string]any{
				"workspace_id":        normalized.scope.WorkspaceID.String(),
				"successor_id":        plan.Successor.ID.String(),
				"dispatch_generation": plan.Successor.DispatchGeneration,
				"reroute_rung":        plan.Successor.RerouteRung,
			},
		})
		if err != nil {
			return err
		}
		if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "delivery dispatch successor audit is unavailable",
			)
		}
		receipt, err = newDeliveryDispatchSuccessorReceipt(
			normalized, ids, plan, planHash[:], audit,
		)
		if err != nil {
			return err
		}
		receiptRecord, err := communicationCommandReceiptToRecord(receipt)
		if err != nil {
			return err
		}
		if _, err = tx.createWithID(
			ctx, communicationCommandKind, receipt.ID, receiptRecord,
		); err != nil {
			return err
		}
		result = deliveryDispatchSuccessorResult{
			CommandID: ids.Command, PredecessorID: predecessor.ID,
			PredecessorVersion:  plan.PredecessorAfter.Version,
			SuccessorID:         plan.Successor.ID,
			SuccessorGeneration: plan.Successor.DispatchGeneration,
			RerouteRung:         plan.Successor.RerouteRung, AuditSeq: audit.Seq,
		}
		return nil
	})
	return result, err
}

func deliveryDispatchSuccessorLockKey(
	normalized deliveryDispatchSuccessorNormalized,
) string {
	return "sessions.communication.dispatch.successor:" + normalized.scope.WorkspaceID.String() +
		":" + fmt.Sprintf("%x", normalized.actorFingerprint) +
		":" + fmt.Sprintf("%x", normalized.idempotencyKeyHash)
}

func lockDeliveryDispatchRootHistory(
	ctx context.Context,
	tx *communicationTx,
	predecessor DeliveryDispatch,
) (DispatchRootHistoryWitness, error) {
	if tx == nil || predecessor.DispatchGeneration < 1 ||
		predecessor.DispatchGeneration > deliveryDispatchRootHistoryBound {
		return DispatchRootHistoryWitness{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch root history exceeds bound",
		)
	}
	repo, err := tx.repo(deliveryDispatchKind)
	if err != nil {
		return DispatchRootHistoryWitness{}, err
	}
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{{
			Column: colCommRootDispatchID, Op: model.OpEq,
			Value: predecessor.RootDispatchID.String(),
		}},
		Sort:  []model.Sort{{Column: colCommDispatchGeneration}},
		Limit: int(predecessor.DispatchGeneration) + 1,
	})
	if err != nil {
		return DispatchRootHistoryWitness{}, err
	}
	if page.HasMore || len(rows) != int(predecessor.DispatchGeneration) {
		return DispatchRootHistoryWitness{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch root history is incomplete",
		)
	}
	history := make([]DeliveryDispatch, 0, len(rows))
	for _, row := range rows {
		dispatch, decodeErr := deliveryDispatchFromRecord(row)
		if decodeErr != nil || dispatch.RootDispatchID != predecessor.RootDispatchID ||
			dispatch.TenantID != predecessor.TenantID ||
			dispatch.WorkspaceID != predecessor.WorkspaceID {
			return DispatchRootHistoryWitness{}, communicationError(
				ErrCommunicationEvidenceUnknown, "delivery dispatch root history is malformed",
			)
		}
		if dispatch.ID == predecessor.ID {
			dispatch = predecessor
		}
		history = append(history, dispatch)
	}
	sort.Slice(history, func(i, j int) bool {
		return history[i].DispatchGeneration < history[j].DispatchGeneration
	})
	entries := make([]DispatchRootHistoryEntry, len(history))
	for index, dispatch := range history {
		if dispatch.DispatchGeneration != int64(index+1) {
			return DispatchRootHistoryWitness{}, communicationError(
				ErrCommunicationEvidenceUnknown, "delivery dispatch root history is not contiguous",
			)
		}
		entries[index] = DispatchRootHistoryEntry{
			DispatchID: dispatch.ID, DispatchGeneration: dispatch.DispatchGeneration,
			IdempotencyKeyHash: append([]byte(nil), dispatch.IdempotencyKeyHash...),
		}
	}
	return DispatchRootHistoryWitness{
		Scope: DirectoryScopeRef{
			TenantID: predecessor.TenantID, WorkspaceID: predecessor.WorkspaceID,
		},
		RootDispatchID: predecessor.RootDispatchID, Entries: entries,
	}, nil
}

func validateDeliveryDispatchSuccessorRoute(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	predecessor DeliveryDispatch,
	route DispatchRouteIdentity,
) error {
	if route.PolicyGeneration < predecessor.PolicyGeneration {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch route policy moved backwards",
		)
	}
	deliveryRecord, err := tx.lockRecord(ctx, messageDeliveryKind, predecessor.DeliveryID)
	if err != nil {
		return err
	}
	delivery, err := messageDeliveryFromRecord(deliveryRecord)
	if err != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor Delivery is malformed",
		)
	}
	messageRecord, err := tx.lockRecord(ctx, messageKind, delivery.MessageID)
	if err != nil {
		return err
	}
	message, err := deliveryMessageFromRecord(messageRecord)
	if err != nil || ValidateMessageDeliveryLineage(message, delivery) != nil ||
		delivery.TenantID != scope.TenantID || delivery.WorkspaceID != scope.WorkspaceID {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor lineage is unavailable",
		)
	}
	endpointRecord, err := tx.lockRecord(ctx, communicationEndpointKind, route.EndpointID)
	if err != nil {
		return err
	}
	endpoint, err := communicationEndpointFromRecord(endpointRecord)
	if err != nil || endpoint.TenantID != scope.TenantID || endpoint.WorkspaceID != scope.WorkspaceID ||
		endpoint.Generation != route.EndpointGeneration || endpoint.Owner != delivery.Recipient ||
		endpoint.State != EndpointActive || endpoint.SupportLevel != EndpointStable {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor endpoint is unavailable",
		)
	}
	if route.RouteRuleID == "" {
		return nil
	}
	routeRecord, err := tx.lockRecord(ctx, channelRouteKind, route.RouteRuleID)
	if err != nil {
		return err
	}
	rule, err := channelRouteRuleFromRecord(routeRecord)
	if err != nil || rule.TenantID != scope.TenantID || rule.WorkspaceID != scope.WorkspaceID ||
		rule.Generation != route.RouteRuleGeneration || rule.State != ChannelRouteActive ||
		rule.TargetChannelID != message.ChannelID {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor route rule is unavailable",
		)
	}
	return nil
}

func lookupDeliveryDispatchSuccessorReceipt(
	ctx context.Context,
	tx *communicationTx,
	normalized deliveryDispatchSuccessorNormalized,
) (CommunicationCommandReceipt, bool, error) {
	repo, err := tx.repo(communicationCommandKind)
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colCommCommandScope, Op: model.OpEq, Value: deliveryDispatchSuccessorScope},
			{Column: colCommActorFingerprint, Op: model.OpEq, Value: normalized.actorFingerprint},
			{Column: colCommIdempotencyKeyHash, Op: model.OpEq, Value: normalized.idempotencyKeyHash},
		},
		Limit: 2,
	})
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	if page.HasMore || len(rows) > 1 {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor receipt is ambiguous",
		)
	}
	if len(rows) == 0 {
		return CommunicationCommandReceipt{}, false, nil
	}
	id, err := model.ParseID(rows[0].String(model.ColID))
	if err != nil {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor receipt ID is malformed",
		)
	}
	locked, err := tx.lockRecord(ctx, communicationCommandKind, id)
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	receipt, err := communicationCommandReceiptFromRecord(locked)
	if err != nil {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor receipt is malformed",
		)
	}
	if !bytes.Equal(receipt.RequestDigest, normalized.requestDigest) {
		return CommunicationCommandReceipt{}, false, fmt.Errorf(
			"%w: delivery dispatch idempotency key was reused", store.ErrConflict,
		)
	}
	if receipt.CommandScope != deliveryDispatchSuccessorScope ||
		receipt.TenantID != normalized.scope.TenantID ||
		receipt.WorkspaceID != normalized.scope.WorkspaceID ||
		!bytes.Equal(receipt.ActorFingerprint, normalized.actorFingerprint) ||
		!bytes.Equal(receipt.IdempotencyKeyHash, normalized.idempotencyKeyHash) {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor receipt crossed scope",
		)
	}
	return receipt, true, nil
}

func newDeliveryDispatchSuccessorReceipt(
	normalized deliveryDispatchSuccessorNormalized,
	ids deliveryDispatchSuccessorIDs,
	plan DispatchSuccessorPlan,
	planHash []byte,
	audit model.AuditEvent,
) (CommunicationCommandReceipt, error) {
	projection := CommunicationCommandResponseProjection{
		IDs:     map[string]model.ID{"dispatch_id": plan.Successor.ID},
		Version: plan.Successor.Version, State: string(plan.Successor.State),
		Digests: map[string][]byte{
			"request": append([]byte(nil), normalized.requestDigest...),
			"plan":    append([]byte(nil), planHash...),
		},
	}
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: ids.Receipt, TenantID: normalized.scope.TenantID,
			WorkspaceID: normalized.scope.WorkspaceID, Version: 1, CreatedAt: plan.Successor.CreatedAt,
		}},
		CommandID: ids.Command, ActorFingerprint: append([]byte(nil), normalized.actorFingerprint...),
		CommandScope:       deliveryDispatchSuccessorScope,
		IdempotencyKeyHash: append([]byte(nil), normalized.idempotencyKeyHash...),
		RequestDigest:      append([]byte(nil), normalized.requestDigest...),
		PlanHash:           append([]byte(nil), planHash...), ResultKind: string(deliveryDispatchKind),
		ResultID: plan.Successor.ID, HTTPStatus: http.StatusCreated,
		ResponseProjectionJSON: projection, AuditSeq: audit.Seq,
		AuditHash: append([]byte(nil), audit.Hash...), CompletedAt: plan.Successor.CreatedAt,
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

func replayDeliveryDispatchSuccessor(
	ctx context.Context,
	tx *communicationTx,
	normalized deliveryDispatchSuccessorNormalized,
	receipt CommunicationCommandReceipt,
) (deliveryDispatchSuccessorResult, error) {
	projection := receipt.ResponseProjectionJSON
	successorID := projection.IDs["dispatch_id"]
	if receipt.ResultKind != string(deliveryDispatchKind) || receipt.HTTPStatus != http.StatusCreated ||
		receipt.ResultID != successorID || receipt.EventID != "" || projection.Version != 1 ||
		projection.State != string(DispatchPending) || len(projection.IDs) != 1 ||
		len(projection.Counts) != 0 || len(projection.Digests) != 2 ||
		!bytes.Equal(projection.Digests["request"], normalized.requestDigest) ||
		!bytes.Equal(projection.Digests["plan"], receipt.PlanHash) {
		return deliveryDispatchSuccessorResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor receipt is incomplete",
		)
	}
	predecessorRecord, err := tx.lockRecord(
		ctx, deliveryDispatchKind, normalized.command.PredecessorID,
	)
	if err != nil {
		return deliveryDispatchSuccessorResult{}, err
	}
	predecessor, err := deliveryDispatchFromRecord(predecessorRecord)
	if err != nil {
		return deliveryDispatchSuccessorResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch replay predecessor is malformed",
		)
	}
	successorRecord, err := tx.lockRecord(ctx, deliveryDispatchKind, successorID)
	if err != nil {
		return deliveryDispatchSuccessorResult{}, err
	}
	successor, err := deliveryDispatchFromRecord(successorRecord)
	if err != nil || predecessor.State != DispatchSuperseded ||
		successor.PredecessorID != predecessor.ID ||
		successor.RootDispatchID != predecessor.RootDispatchID ||
		successor.DispatchGeneration != predecessor.DispatchGeneration+1 ||
		successor.DeliveryID != predecessor.DeliveryID ||
		successor.TenantID != normalized.scope.TenantID ||
		successor.WorkspaceID != normalized.scope.WorkspaceID ||
		successor.Version != projection.Version || successor.State != DispatchPending ||
		!successor.CreatedAt.Equal(receipt.CompletedAt) ||
		!bytes.Equal(successor.IdempotencyKeyHash, normalized.idempotencyKeyHash) {
		return deliveryDispatchSuccessorResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor replay lineage is unavailable",
		)
	}
	if !dispatchRouteIdentity(successor).Equal(normalized.command.SuccessorRoute) {
		return deliveryDispatchSuccessorResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "delivery dispatch successor replay route changed",
		)
	}
	return deliveryDispatchSuccessorResult{
		CommandID: receipt.CommandID, PredecessorID: predecessor.ID,
		PredecessorVersion: predecessor.Version, SuccessorID: successor.ID,
		SuccessorGeneration: successor.DispatchGeneration,
		RerouteRung:         successor.RerouteRung, AuditSeq: receipt.AuditSeq,
	}, nil
}

func normalizeDeliveryDispatchSuccessorError(err error) error {
	if err == nil || errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		errors.Is(err, store.ErrConflict) ||
		errors.Is(err, ErrInvalidCommunicationModel) ||
		errors.Is(err, ErrInvalidCommunicationTransition) ||
		errors.Is(err, ErrCommunicationNotFound) {
		return err
	}
	return communicationError(
		ErrCommunicationEvidenceUnknown, "delivery dispatch successor operation is unavailable",
	)
}
