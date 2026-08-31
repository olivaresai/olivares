// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func (m *Module) applyHandoffOffer(
	ctx context.Context,
	question communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	inspected communicationRequestAuthorityInspection,
	identity directNoticeReaderIdentityPreflight,
	window communicationAuthorityWindow,
	normalized handoffOfferNormalized,
	ids handoffOfferIDs,
	prepared handoffOfferPrepared,
) (HandoffOfferResult, error) {
	var result HandoffOfferResult
	err := m.mutateHandoffWithAuthority(
		ctx, question, bound, window,
		func(
			tx *communicationTx,
			repositories handoffWorkRepositories,
			consumed communicationRequestAuthorityContext,
		) error {
			reader, err := handoffOfferReaderPreflight(
				question, inspected, consumed, identity, normalized,
			)
			if err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, reader.Facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			if err := tx.lockTransaction(ctx, handoffIdempotencyLockKey(normalized.handoffCommandIdentity)); err != nil {
				return err
			}
			receipt, replay, err := findHandoffReceipt(ctx, tx, normalized.handoffCommandIdentity)
			if err != nil {
				return fmt.Errorf("Handoff offer receipt lookup: %w", err)
			}
			if replay && !bytes.Equal(receipt.RequestDigest, normalized.requestDigest) {
				return errHandoffIdempotencyReused
			}
			work, err := lockHandoffWorkState(
				ctx, tx, repositories, normalized.scope, normalized.command.WorkItemID, false,
			)
			if err != nil {
				return fmt.Errorf("Handoff offer Work lock: %w", err)
			}
			carrier, err := lockHandoffCarrier(
				ctx, tx, normalized.scope, normalized.command.ChannelID,
				normalized.command.MessageID, normalized.command.DeliveryID,
			)
			if err != nil {
				return fmt.Errorf("Handoff offer carrier lock: %w", err)
			}
			if !replay {
				handoffs, err := tx.repo(handoffKind)
				if err != nil {
					return err
				}
				rows, page, err := handoffs.List(ctx, model.Query{
					Filters: []model.Filter{{
						Column: colCommMessageID, Op: model.OpEq,
						Value: normalized.command.MessageID.String(),
					}},
					Limit: 2,
				})
				if err != nil {
					return err
				}
				if page.HasMore || len(rows) != 0 {
					return fmt.Errorf("%w: Handoff carrier already materialized", store.ErrConflict)
				}
			}
			if err := tx.lockAuditAppends(ctx); err != nil {
				return err
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			if err := evaluateHandoffOfferAuthority(tx, reader, carrier); err != nil {
				return fmt.Errorf("Handoff offer authority evaluation: %w", err)
			}
			if err := validatePreparedHandoffOffer(prepared, carrier); err != nil {
				return err
			}
			if replay {
				result, err = handoffOfferResultFromReceipt(receipt)
				return err
			}
			result, err = applyLockedHandoffOffer(
				ctx, tx, repositories, reader, normalized, ids, prepared, carrier, work,
			)
			return err
		},
	)
	return result, err
}

func (m *Module) applyHandoffResponse(
	ctx context.Context,
	question communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	inspected communicationRequestAuthorityInspection,
	identity directNoticeReaderIdentityPreflight,
	window communicationAuthorityWindow,
	normalized handoffResponseNormalized,
	ids handoffResponseIDs,
	prepared handoffResponsePrepared,
) (HandoffResponseResult, error) {
	var result HandoffResponseResult
	err := m.mutateHandoffWithAuthority(
		ctx, question, bound, window,
		func(
			tx *communicationTx,
			repositories handoffWorkRepositories,
			consumed communicationRequestAuthorityContext,
		) error {
			reader, err := handoffResponseReaderPreflight(
				question, inspected, consumed, identity, normalized,
			)
			if err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, reader.Facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			if err := tx.lockTransaction(ctx, handoffIdempotencyLockKey(normalized.handoffCommandIdentity)); err != nil {
				return err
			}
			receipt, replay, err := findHandoffReceipt(ctx, tx, normalized.handoffCommandIdentity)
			if err != nil {
				return err
			}
			if replay && !bytes.Equal(receipt.RequestDigest, normalized.requestDigest) {
				return errHandoffIdempotencyReused
			}
			handoffs, err := tx.repo(handoffKind)
			if err != nil {
				return err
			}
			observedRecord, err := handoffs.Get(ctx, normalized.handoffID)
			if err != nil {
				return err
			}
			observed, err := handoffFromRecord(observedRecord)
			if err != nil {
				return err
			}
			messages, err := tx.repo(messageKind)
			if err != nil {
				return err
			}
			messageObservation, err := messages.Get(ctx, observed.MessageID)
			if err != nil {
				return err
			}
			channelID, err := directNoticeRecordID(messageObservation, colCommChannelID)
			if err != nil {
				return err
			}
			work, err := lockHandoffWorkState(
				ctx, tx, repositories, normalized.scope, observed.WorkItemID,
				normalized.command.Transition == HandoffAccept,
			)
			if err != nil {
				return err
			}
			lockedRecord, err := tx.lockRecord(ctx, handoffKind, normalized.handoffID)
			if err != nil {
				return err
			}
			locked, err := handoffFromRecord(lockedRecord)
			if err != nil || locked.WorkItemID != observed.WorkItemID ||
				locked.MessageID != observed.MessageID || locked.DeliveryID != observed.DeliveryID {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "Handoff lineage changed while locking",
				)
			}
			carrier, err := lockHandoffCarrier(
				ctx, tx, normalized.scope, channelID,
				locked.MessageID, locked.DeliveryID,
			)
			if err != nil {
				return err
			}
			if err := tx.lockAuditAppends(ctx); err != nil {
				return err
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			if _, err := evaluateHandoffResponseAuthority(tx, reader, carrier, locked); err != nil {
				return err
			}
			if replay {
				result, err = handoffResponseResultFromReceipt(receipt, locked)
				return err
			}
			result, err = applyLockedHandoffResponse(
				ctx, tx, repositories, reader, normalized, ids, prepared, carrier, locked, work,
			)
			return err
		},
	)
	return result, err
}

func (m *Module) applyHandoffCancel(
	ctx context.Context,
	question communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	inspected communicationRequestAuthorityInspection,
	identity directNoticeReaderIdentityPreflight,
	window communicationAuthorityWindow,
	normalized handoffCancelNormalized,
	ids handoffLifecycleIDs,
	prepared handoffResponsePrepared,
) (HandoffLifecycleResult, error) {
	var result HandoffLifecycleResult
	err := m.mutateHandoffWithAuthority(
		ctx, question, bound, window,
		func(
			tx *communicationTx,
			repositories handoffWorkRepositories,
			consumed communicationRequestAuthorityContext,
		) error {
			reader, err := handoffCancelReaderPreflight(
				question, inspected, consumed, identity, normalized,
			)
			if err != nil {
				return err
			}
			if err := tx.lockAuthoritySnapshot(ctx, reader.Facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			if err := tx.lockTransaction(
				ctx, handoffIdempotencyLockKey(normalized.handoffCommandIdentity),
			); err != nil {
				return err
			}
			receipt, replay, err := findHandoffReceipt(
				ctx, tx, normalized.handoffCommandIdentity,
			)
			if err != nil {
				return err
			}
			if replay && !bytes.Equal(receipt.RequestDigest, normalized.requestDigest) {
				return errHandoffIdempotencyReused
			}
			locked, carrier, work, err := lockHandoffLifecycleTarget(
				ctx, tx, repositories, normalized.scope, normalized.handoffID,
			)
			if err != nil {
				return err
			}
			if err := tx.lockAuditAppends(ctx); err != nil {
				return err
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			if err := evaluateHandoffCancelAuthority(tx, reader, carrier, locked); err != nil {
				return err
			}
			if replay {
				result, err = handoffLifecycleResultFromReceipt(receipt, locked)
				return err
			}
			result, err = applyLockedHandoffLifecycle(
				ctx, tx, repositories, normalized.handoffCommandIdentity, reader.Facts,
				ids, prepared, carrier, locked, work, HandoffWithdraw,
				handoffCancelOperation, handoffCancelAuditAction,
				handoffWithdrawnEventType, handoffWithdrawnCode,
			)
			return err
		},
	)
	return result, err
}

func (m *Module) applyHandoffDeadline(
	ctx context.Context,
	normalized handoffDeadlineNormalized,
	ids handoffLifecycleIDs,
	prepared handoffResponsePrepared,
) (HandoffLifecycleResult, error) {
	var result HandoffLifecycleResult
	err := m.mutateHandoffDeadline(
		ctx, normalized.scope, normalized.authority,
		func(tx *communicationTx, repositories handoffWorkRepositories) error {
			if err := tx.lockAuthoritySnapshot(ctx, normalized.authority.Facts); err != nil {
				return normalizeDirectNoticeAuthorityLockError(err)
			}
			if err := tx.lockTransaction(
				ctx, handoffIdempotencyLockKey(normalized.handoffCommandIdentity),
			); err != nil {
				return err
			}
			receipt, replay, err := findHandoffReceipt(
				ctx, tx, normalized.handoffCommandIdentity,
			)
			if err != nil {
				return err
			}
			if replay && !bytes.Equal(receipt.RequestDigest, normalized.requestDigest) {
				return errHandoffIdempotencyReused
			}
			locked, carrier, work, err := lockHandoffLifecycleTarget(
				ctx, tx, repositories, normalized.scope, normalized.command.HandoffID,
			)
			if err != nil {
				return err
			}
			if err := tx.lockAuditAppends(ctx); err != nil {
				return err
			}
			if err := tx.refreshNow(ctx); err != nil {
				return err
			}
			if err := evaluateHandoffDeadlineAuthority(
				tx, normalized, carrier, locked,
			); err != nil {
				return err
			}
			if replay {
				result, err = handoffLifecycleResultFromReceipt(receipt, locked)
				return err
			}
			result, err = applyLockedHandoffLifecycle(
				ctx, tx, repositories, normalized.handoffCommandIdentity,
				normalized.authority.Facts, ids, prepared, carrier, locked, work,
				HandoffExpire, handoffDeadlineOperation, handoffDeadlineAuditAction,
				handoffExpiredEventType, handoffDeadlineElapsedCode,
			)
			return err
		},
	)
	return result, err
}

// handoffWorkRepositories is the narrow K2 side of the K2/K3 atomic seam. It
// deliberately exposes only the three Work repositories Handoff needs; the
// general communication transaction inventory remains unchanged.
type handoffWorkRepositories struct {
	item  store.TransactionStampedGenericRepo
	lease store.TransactionStampedGenericRepo
	guard store.TransactionStampedGenericRepo
}

type handoffLockedWork struct {
	item       model.Record
	lease      model.Record
	leaseState fenceState
	clockGuard model.Record
}

type handoffLockedCarrier struct {
	channel       Channel
	grants        []ChannelGrant
	message       Message
	delivery      MessageDelivery
	deliveries    []MessageDelivery
	audiences     []MessageAudience
	contributions []MessageAudienceRecipient
	requiredCount int64
	epoch         model.DirectoryEpoch
}

// mutateHandoffWithAuthority extends the exact one-shot communication
// transaction with the existing K2 WorkItem/WorkLease rows without widening
// communicationTx's repository inventory. All raw Work operations are routed
// through the same phase machine below.
func (m *Module) mutateHandoffWithAuthority(
	ctx context.Context,
	expected communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	window communicationAuthorityWindow,
	fn func(
		*communicationTx,
		handoffWorkRepositories,
		communicationRequestAuthorityContext,
	) error,
) error {
	if err := expected.validate(); err != nil {
		return err
	}
	if err := window.validate(); err != nil {
		return err
	}
	if fn == nil {
		return communicationTransactionUnavailable("Handoff mutation callback", nil)
	}
	request, consumed, err := bound.transactionSnapshot(
		expected, CommunicationClaimAuthoritySnapshot{},
	)
	if err != nil {
		return err
	}
	request, err = request.narrowTo(window)
	if err != nil {
		return err
	}
	scope := DirectoryScopeRef{
		TenantID: expected.entity.TenantID, WorkspaceID: expected.entity.WorkspaceID,
	}
	var callbackAttempted atomic.Bool
	return m.communicationData(scope.TenantID).Mutate(ctx, func(sc store.Scope) error {
		if !callbackAttempted.CompareAndSwap(false, true) {
			return communicationTransactionUnavailable(
				"Handoff mutation callback was already entered", nil,
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
		repositories, err := handoffWorkRepositoriesFromScope(confined)
		if err != nil {
			return err
		}
		if err := fn(tx, repositories, consumed); err != nil {
			return err
		}
		return tx.finalizeAuthority(ctx)
	})
}

func (m *Module) mutateHandoffDeadline(
	ctx context.Context,
	scope DirectoryScopeRef,
	authority handoffDeadlineAuthority,
	fn func(*communicationTx, handoffWorkRepositories) error,
) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if err := validateHandoffDeadlineAuthority(scope, authority); err != nil {
		return err
	}
	if fn == nil {
		return communicationTransactionUnavailable("Handoff deadline mutation callback", nil)
	}
	binding := &communicationRequestAuthorityBindingID{marker: 1}
	request := communicationRequestAuthoritySnapshot{
		facts:      append([]store.AuthorizationFactRef(nil), authority.Facts...),
		observedAt: authority.ObservedAt.UTC(), freshUntil: authority.FreshUntil.UTC(),
		bindingID: binding,
	}
	if err := request.validate(); err != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff deadline authority snapshot is malformed",
		)
	}
	var callbackAttempted atomic.Bool
	return m.communicationData(scope.TenantID).Mutate(ctx, func(sc store.Scope) error {
		if !callbackAttempted.CompareAndSwap(false, true) {
			return communicationTransactionUnavailable(
				"Handoff deadline mutation callback was already entered", nil,
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
		repositories, err := handoffWorkRepositoriesFromScope(confined)
		if err != nil {
			return err
		}
		if err := fn(tx, repositories); err != nil {
			return err
		}
		return tx.finalizeAuthority(ctx)
	})
}

func handoffWorkRepositoriesFromScope(sc store.Scope) (handoffWorkRepositories, error) {
	resolve := func(kind model.Kind) (store.TransactionStampedGenericRepo, error) {
		repo, err := sc.Ext(kind)
		if err != nil {
			return nil, err
		}
		stamped, ok := repo.(store.TransactionStampedGenericRepo)
		if !ok {
			return nil, communicationTransactionUnavailable(
				fmt.Sprintf("Handoff transaction-stamped repository %q", kind), nil,
			)
		}
		if _, ok := repo.(store.RowLocker[model.Record]); !ok {
			return nil, communicationTransactionUnavailable(
				fmt.Sprintf("Handoff row locker %q", kind), nil,
			)
		}
		return stamped, nil
	}
	item, err := resolve(workItemKind)
	if err != nil {
		return handoffWorkRepositories{}, err
	}
	lease, err := resolve(workLeaseKind)
	if err != nil {
		return handoffWorkRepositories{}, err
	}
	guard, err := resolve(workGuardKind)
	if err != nil {
		return handoffWorkRepositories{}, err
	}
	return handoffWorkRepositories{item: item, lease: lease, guard: guard}, nil
}

func handoffLockRawRecord(
	ctx context.Context,
	tx *communicationTx,
	repo store.TransactionStampedGenericRepo,
	id model.ID,
) (model.Record, error) {
	locker := repo.(store.RowLocker[model.Record])
	var record model.Record
	err := runCommunicationBoundAuthorityLocalLock(tx.boundAuthorityState, func() error {
		var err error
		record, err = locker.Lock(ctx, id)
		return err
	})
	return record, err
}

func handoffListRawRecords(
	ctx context.Context,
	tx *communicationTx,
	repo store.TransactionStampedGenericRepo,
	query model.Query,
) ([]model.Record, model.Page, error) {
	type result struct {
		rows []model.Record
		page model.Page
	}
	value, err := runCommunicationBoundAuthorityObservation(
		tx.boundAuthorityState,
		func() (result, error) {
			rows, page, listErr := repo.List(ctx, query)
			return result{rows: rows, page: page}, listErr
		},
	)
	return value.rows, value.page, err
}

func handoffUpdateRawRecord(
	ctx context.Context,
	tx *communicationTx,
	repo store.TransactionStampedGenericRepo,
	record model.Record,
) (model.Record, error) {
	return runCommunicationBoundAuthorityEffect(
		tx.boundAuthorityState,
		func() (model.Record, error) {
			return repo.UpdateAtTransactionTime(ctx, record)
		},
	)
}

func lockHandoffWorkState(
	ctx context.Context,
	tx *communicationTx,
	repositories handoffWorkRepositories,
	scope DirectoryScopeRef,
	workItemID model.ID,
	lockClock bool,
) (handoffLockedWork, error) {
	item, err := handoffLockRawRecord(ctx, tx, repositories.item, workItemID)
	if err != nil {
		return handoffLockedWork{}, err
	}
	if recordID(item) != workItemID || item.String(colWorkWorkspaceID) != scope.WorkspaceID.String() {
		return handoffLockedWork{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff WorkItem crossed workspace lineage",
		)
	}
	var guard model.Record
	if lockClock {
		if err := tx.lockTransaction(ctx, leaseClockCoordinationKey(scope.TenantID, scope.WorkspaceID)); err != nil {
			return handoffLockedWork{}, err
		}
		rows, page, err := handoffListRawRecords(ctx, tx, repositories.guard, model.Query{
			Filters: []model.Filter{
				{Column: colWorkWorkspaceID, Op: model.OpEq, Value: scope.WorkspaceID.String()},
				{Column: colGuardKind, Op: model.OpEq, Value: "lease_clock"},
			},
			Limit: 2,
		})
		if err != nil || page.HasMore || len(rows) != 1 {
			return handoffLockedWork{}, communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff lease clock guard is unavailable",
			)
		}
		guard, err = handoffLockRawRecord(ctx, tx, repositories.guard, recordID(rows[0]))
		if err != nil {
			return handoffLockedWork{}, err
		}
	}
	if err := tx.lockTransaction(
		ctx, "sessions.work_lease:"+scope.TenantID.String()+":"+
			scope.WorkspaceID.String()+":"+workItemID.String(),
	); err != nil {
		return handoffLockedWork{}, err
	}
	rows, page, err := handoffListRawRecords(ctx, tx, repositories.lease, model.Query{
		Filters: []model.Filter{{Column: colWorkItemID, Op: model.OpEq, Value: workItemID.String()}},
		Limit:   2,
	})
	if err != nil || page.HasMore || len(rows) != 1 {
		return handoffLockedWork{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff WorkLease is unavailable",
		)
	}
	lease, err := handoffLockRawRecord(ctx, tx, repositories.lease, recordID(rows[0]))
	if err != nil {
		return handoffLockedWork{}, err
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return handoffLockedWork{}, err
	}
	return handoffLockedWork{
		item: item, lease: lease, leaseState: state, clockGuard: guard,
	}, nil
}

func lockHandoffCarrier(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	channelID model.ID,
	messageID model.ID,
	deliveryID model.ID,
) (handoffLockedCarrier, error) {
	channelRecord, err := tx.lockRecord(ctx, channelKind, channelID)
	if err != nil {
		return handoffLockedCarrier{}, err
	}
	channel, err := channelFromRecord(channelRecord)
	if err != nil || channel.TenantID != scope.TenantID ||
		channel.WorkspaceID != scope.WorkspaceID {
		return handoffLockedCarrier{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff Channel is unavailable",
		)
	}
	grants, err := lockCurrentChannelGrants(ctx, tx, channel.ID)
	if err != nil {
		return handoffLockedCarrier{}, err
	}
	messageRecord, err := tx.lockRecord(ctx, messageKind, messageID)
	if err != nil {
		return handoffLockedCarrier{}, err
	}
	deliveryRecords, err := lockDirectNoticeRecordSet(
		ctx, tx, messageDeliveryKind,
		[]model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()}},
		directNoticeReadSetBound,
	)
	if err != nil {
		return handoffLockedCarrier{}, err
	}
	deliveries := make([]MessageDelivery, 0, len(deliveryRecords))
	required := int64(0)
	var target MessageDelivery
	for _, record := range deliveryRecords {
		delivery, decodeErr := messageDeliveryFromRecord(record)
		if decodeErr != nil || delivery.MessageID != messageID {
			return handoffLockedCarrier{}, communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff Delivery set is malformed",
			)
		}
		if delivery.Required {
			required++
		}
		if delivery.ID == deliveryID {
			target = delivery
		}
		deliveries = append(deliveries, delivery)
	}
	if target.ID == "" {
		return handoffLockedCarrier{}, communicationError(
			ErrCommunicationNotFound, "Handoff Delivery is absent",
		)
	}
	message, err := messageFromRecord(messageRecord, required)
	if err != nil {
		return handoffLockedCarrier{}, err
	}
	audienceRecords, err := lockDirectNoticeRecordSet(
		ctx, tx, messageAudienceKind,
		[]model.Filter{{Column: colCommMessageID, Op: model.OpEq, Value: messageID.String()}},
		64,
	)
	if err != nil {
		return handoffLockedCarrier{}, err
	}
	audiences := make([]MessageAudience, 0, len(audienceRecords))
	for _, record := range audienceRecords {
		audience, decodeErr := messageAudienceFromRecord(record)
		if decodeErr != nil || audience.MessageID != messageID {
			return handoffLockedCarrier{}, communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff Audience set is malformed",
			)
		}
		audiences = append(audiences, audience)
	}
	contributionRecords, err := lockDirectNoticeContributionSet(ctx, tx, audiences)
	if err != nil {
		return handoffLockedCarrier{}, err
	}
	contributions := make([]MessageAudienceRecipient, 0, len(contributionRecords))
	for _, record := range contributionRecords {
		contribution, decodeErr := messageAudienceRecipientFromRecord(record)
		if decodeErr != nil {
			return handoffLockedCarrier{}, communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff audience contribution is malformed",
			)
		}
		contributions = append(contributions, contribution)
	}
	if required != 1 || len(audiences) != 1 || len(contributions) != 1 ||
		contributions[0].MessageDeliveryID != target.ID ||
		contributions[0].Recipient != target.Recipient {
		return handoffLockedCarrier{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff requires one exact required audience contribution",
		)
	}
	audienceHash, err := CanonicalMessageAudienceHash(message, audiences, contributions)
	if err != nil || !bytes.Equal(audienceHash, message.AudienceHash) {
		return handoffLockedCarrier{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff audience seal is unavailable",
		)
	}
	epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
	if err != nil || epoch.Validate() != nil || epoch.TenantID != scope.TenantID {
		return handoffLockedCarrier{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff directory epoch is unavailable",
		)
	}
	return handoffLockedCarrier{
		channel: channel, grants: grants, message: message, delivery: target,
		deliveries: deliveries, audiences: audiences, contributions: contributions,
		requiredCount: required, epoch: epoch,
	}, nil
}

func lockHandoffLifecycleTarget(
	ctx context.Context,
	tx *communicationTx,
	repositories handoffWorkRepositories,
	scope DirectoryScopeRef,
	handoffID model.ID,
) (Handoff, handoffLockedCarrier, handoffLockedWork, error) {
	handoffs, err := tx.repo(handoffKind)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, handoffLockedWork{}, err
	}
	observedRecord, err := handoffs.Get(ctx, handoffID)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, handoffLockedWork{}, err
	}
	observed, err := handoffFromRecord(observedRecord)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, handoffLockedWork{}, err
	}
	messages, err := tx.repo(messageKind)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, handoffLockedWork{}, err
	}
	messageObservation, err := messages.Get(ctx, observed.MessageID)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, handoffLockedWork{}, err
	}
	channelID, err := directNoticeRecordID(messageObservation, colCommChannelID)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, handoffLockedWork{}, err
	}
	work, err := lockHandoffWorkState(ctx, tx, repositories, scope, observed.WorkItemID, false)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, handoffLockedWork{}, err
	}
	lockedRecord, err := tx.lockRecord(ctx, handoffKind, handoffID)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, handoffLockedWork{}, err
	}
	locked, err := handoffFromRecord(lockedRecord)
	if err != nil || locked.WorkItemID != observed.WorkItemID ||
		locked.MessageID != observed.MessageID || locked.DeliveryID != observed.DeliveryID {
		return Handoff{}, handoffLockedCarrier{}, handoffLockedWork{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff lineage changed while locking",
		)
	}
	carrier, err := lockHandoffCarrier(
		ctx, tx, scope, channelID, locked.MessageID, locked.DeliveryID,
	)
	if err != nil {
		return Handoff{}, handoffLockedCarrier{}, handoffLockedWork{}, err
	}
	return locked, carrier, work, nil
}

func handoffOfferReaderPreflight(
	question communicationAuthorityQuestion,
	inspected communicationRequestAuthorityInspection,
	consumed communicationRequestAuthorityContext,
	identity directNoticeReaderIdentityPreflight,
	normalized handoffOfferNormalized,
) (directNoticeReaderPreflight, error) {
	if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
		return directNoticeReaderPreflight{}, err
	}
	want := EntityRef{
		TenantID: normalized.scope.TenantID, WorkspaceID: normalized.scope.WorkspaceID,
		Kind: channelKind, ID: normalized.command.ChannelID,
	}
	if consumed.question != question || consumed.bindingID == nil ||
		consumed.bindingID != inspected.bindingID || consumed.question.entity != want ||
		consumed.question.operation != CommunicationMessageSend ||
		consumed.principal != normalized.principal {
		return directNoticeReaderPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff offer authority crossed its exact Channel request",
		)
	}
	return directNoticeReaderPreflightWithCore(identity, consumed.witness)
}

func handoffResponseReaderPreflight(
	question communicationAuthorityQuestion,
	inspected communicationRequestAuthorityInspection,
	consumed communicationRequestAuthorityContext,
	identity directNoticeReaderIdentityPreflight,
	normalized handoffResponseNormalized,
) (directNoticeReaderPreflight, error) {
	if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
		return directNoticeReaderPreflight{}, err
	}
	want := EntityRef{
		TenantID: normalized.scope.TenantID, WorkspaceID: normalized.scope.WorkspaceID,
		Kind: handoffKind, ID: normalized.handoffID,
	}
	if consumed.question != question || consumed.bindingID == nil ||
		consumed.bindingID != inspected.bindingID || consumed.question.entity != want ||
		consumed.question.operation != CommunicationHandoffResponse ||
		consumed.principal != normalized.principal {
		return directNoticeReaderPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff response authority crossed its exact request",
		)
	}
	return directNoticeReaderPreflightWithCore(identity, consumed.witness)
}

func handoffCancelReaderPreflight(
	question communicationAuthorityQuestion,
	inspected communicationRequestAuthorityInspection,
	consumed communicationRequestAuthorityContext,
	identity directNoticeReaderIdentityPreflight,
	normalized handoffCancelNormalized,
) (directNoticeReaderPreflight, error) {
	if err := validateConsumedDirectNoticeAuthority(inspected, consumed); err != nil {
		return directNoticeReaderPreflight{}, err
	}
	want := EntityRef{
		TenantID: normalized.scope.TenantID, WorkspaceID: normalized.scope.WorkspaceID,
		Kind: handoffKind, ID: normalized.handoffID,
	}
	if consumed.question != question || consumed.bindingID == nil ||
		consumed.bindingID != inspected.bindingID || consumed.question.entity != want ||
		consumed.question.operation != CommunicationHandoffResponse ||
		consumed.principal != normalized.principal {
		return directNoticeReaderPreflight{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff cancel authority crossed its exact request",
		)
	}
	return directNoticeReaderPreflightWithCore(identity, consumed.witness)
}

func evaluateHandoffCancelAuthority(
	tx *communicationTx,
	reader directNoticeReaderPreflight,
	carrier handoffLockedCarrier,
	handoff Handoff,
) error {
	if tx == nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff cancel transaction is unavailable",
		)
	}
	dbNow := tx.now.Time()
	entity := EntityRef{
		TenantID: reader.Scope.TenantID, WorkspaceID: reader.Scope.WorkspaceID,
		Kind: handoffKind, ID: handoff.ID,
	}
	if ValidateReadWitness(reader.Core) != nil || reader.Core.Outcome != ReadAllow ||
		reader.Core.Operation != CommunicationHandoffResponse || reader.Core.Entity != entity ||
		reader.Core.Principal != reader.Principal || reader.Resolution.Recipient == nil ||
		reader.Recipient != handoff.From ||
		reader.Recipient != (RecipientRef{Kind: RecipientUser, Ref: reader.Principal.UserID.String()}) ||
		carrier.epoch.Version != reader.Resolution.Recipient.DirectoryEpoch ||
		directNoticeReadRowsCarryFutureDBTime(
			carrier.channel, carrier.grants, carrier.message, carrier.deliveries,
			carrier.audiences, carrier.contributions, dbNow,
		) || handoff.CreatedAt.After(dbNow) || handoff.UpdatedAt.After(dbNow) ||
		!communicationEvidenceCurrent(reader.Core.ObservedAt, reader.Core.FreshUntil, dbNow) ||
		!communicationEvidenceCurrent(
			reader.Resolution.ObservedAt, reader.Resolution.FreshUntil, dbNow,
		) || !communicationEvidenceCurrent(reader.Closure.ObservedAt, reader.Closure.FreshUntil, dbNow) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff cancel authority expired while waiting for locks",
		)
	}
	if err := ValidateHandoffLineage(
		carrier.message, carrier.delivery, handoff, carrier.requiredCount,
	); err != nil {
		return err
	}
	grant := EvaluateCurrentChannelGrant(
		ChannelGrantSnapshot{
			Verdict: VerdictClean, Code: "channel_grants_locked",
			ACLRevision: carrier.channel.ACLRevision, ObservedAt: dbNow, Grants: carrier.grants,
		},
		reader.Scope.TenantID, reader.Scope.WorkspaceID, carrier.channel.ID,
		reader.Closure, ChannelGrantWrite, dbNow,
	)
	if ValidateAuthorityEvidence(grant.Evidence) != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff owner write grant is unavailable",
		)
	}
	switch evidenceVerdict(grant.Evidence) {
	case VerdictBroken:
		return communicationError(ErrCommunicationForbidden, "Handoff owner lacks ChannelGrant.write")
	case VerdictUnknown:
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff owner write grant is unavailable",
		)
	case VerdictClean:
	default:
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff owner write grant has no verdict",
		)
	}
	deadline, constrained, err := handoffGrantFreshUntil(
		carrier.grants, reader.Closure, ChannelGrantWrite, dbNow,
	)
	if err != nil {
		return err
	}
	if constrained {
		return tx.narrowRequestAuthorityFreshUntil(deadline)
	}
	return nil
}

func evaluateHandoffDeadlineAuthority(
	tx *communicationTx,
	normalized handoffDeadlineNormalized,
	carrier handoffLockedCarrier,
	handoff Handoff,
) error {
	if tx == nil || validateHandoffDeadlineAuthority(
		normalized.scope, normalized.authority,
	) != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff deadline authority is unavailable",
		)
	}
	dbNow := tx.now.Time()
	if !communicationEvidenceCurrent(
		normalized.authority.ObservedAt, normalized.authority.FreshUntil, dbNow,
	) || directNoticeReadRowsCarryFutureDBTime(
		carrier.channel, carrier.grants, carrier.message, carrier.deliveries,
		carrier.audiences, carrier.contributions, dbNow,
	) || handoff.CreatedAt.After(dbNow) || handoff.UpdatedAt.After(dbNow) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff deadline authority expired while waiting for locks",
		)
	}
	foundDirectory := false
	for _, fact := range normalized.authority.Facts {
		if fact.Kind == model.DirectoryEpochKind && fact.ID == model.ID(normalized.scope.TenantID) &&
			fact.Version == carrier.epoch.Version {
			foundDirectory = true
		}
	}
	if !foundDirectory {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff deadline crossed directory epoch",
		)
	}
	return ValidateHandoffLineage(
		carrier.message, carrier.delivery, handoff, carrier.requiredCount,
	)
}

func evaluateHandoffOfferAuthority(
	tx *communicationTx,
	reader directNoticeReaderPreflight,
	carrier handoffLockedCarrier,
) error {
	dbNow := tx.now.Time()
	entity := EntityRef{
		TenantID: reader.Scope.TenantID, WorkspaceID: reader.Scope.WorkspaceID,
		Kind: channelKind, ID: carrier.channel.ID,
	}
	if reader.Core.Operation != CommunicationMessageSend || reader.Core.Entity != entity ||
		reader.Resolution.Recipient == nil ||
		carrier.epoch.Version != reader.Resolution.Recipient.DirectoryEpoch ||
		directNoticeReadRowsCarryFutureDBTime(
			carrier.channel, carrier.grants, carrier.message, carrier.deliveries,
			carrier.audiences, carrier.contributions, dbNow,
		) || !communicationEvidenceCurrent(reader.Core.ObservedAt, reader.Core.FreshUntil, dbNow) ||
		!communicationEvidenceCurrent(
			reader.Resolution.ObservedAt, reader.Resolution.FreshUntil, dbNow,
		) || !communicationEvidenceCurrent(reader.Closure.ObservedAt, reader.Closure.FreshUntil, dbNow) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff offer authority expired while waiting for locks",
		)
	}
	grant := EvaluateCurrentChannelGrant(
		ChannelGrantSnapshot{
			Verdict: VerdictClean, Code: "channel_grants_locked",
			ACLRevision: carrier.channel.ACLRevision, ObservedAt: dbNow, Grants: carrier.grants,
		},
		reader.Scope.TenantID, reader.Scope.WorkspaceID, carrier.channel.ID,
		reader.Closure, ChannelGrantWrite, dbNow,
	)
	decision, err := EvaluateSendGate(SendGateEvidence{
		Scope: reader.Scope, ChannelID: carrier.channel.ID,
		ChannelACLRevision: carrier.channel.ACLRevision, DBNow: dbNow,
		Principal: reader.Principal, Core: reader.Core,
		DirectoryEpoch: store.AuthorizationFactRef{
			Kind: model.DirectoryEpochKind, ID: model.ID(reader.Scope.TenantID),
			Version: carrier.epoch.Version,
		},
		CurrentChannelWriteGrant: grant,
	})
	if err != nil {
		return err
	}
	switch decision.Verdict {
	case VerdictBroken:
		return communicationError(ErrCommunicationForbidden, "Handoff sender lacks ChannelGrant.write")
	case VerdictUnknown:
		return communicationError(ErrCommunicationEvidenceUnknown, "Handoff send authority is unavailable")
	case VerdictClean:
	default:
		return communicationError(ErrCommunicationEvidenceUnknown, "Handoff send authority has no verdict")
	}
	if len(decision.RequiredClaims) != 0 ||
		!equalDirectNoticeAuthorityFacts(reader.Facts, decision.Facts) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff offer returned a different exact authority set",
		)
	}
	deadline, constrained, err := handoffGrantFreshUntil(
		carrier.grants, reader.Closure, ChannelGrantWrite, dbNow,
	)
	if err != nil {
		return err
	}
	if constrained {
		return tx.narrowRequestAuthorityFreshUntil(deadline)
	}
	return nil
}

func evaluateHandoffResponseAuthority(
	tx *communicationTx,
	reader directNoticeReaderPreflight,
	carrier handoffLockedCarrier,
	handoff Handoff,
) (ProtectedReadDecision, error) {
	dbNow := tx.now.Time()
	entity := EntityRef{
		TenantID: reader.Scope.TenantID, WorkspaceID: reader.Scope.WorkspaceID,
		Kind: handoffKind, ID: handoff.ID,
	}
	if reader.Core.Operation != CommunicationHandoffResponse || reader.Core.Entity != entity ||
		reader.Resolution.Recipient == nil ||
		carrier.epoch.Version != reader.Resolution.Recipient.DirectoryEpoch ||
		handoff.To != reader.Recipient || carrier.delivery.Recipient != reader.Recipient ||
		directNoticeReadRowsCarryFutureDBTime(
			carrier.channel, carrier.grants, carrier.message, carrier.deliveries,
			carrier.audiences, carrier.contributions, dbNow,
		) || handoff.CreatedAt.After(dbNow) || handoff.UpdatedAt.After(dbNow) ||
		!communicationEvidenceCurrent(reader.Core.ObservedAt, reader.Core.FreshUntil, dbNow) ||
		!communicationEvidenceCurrent(
			reader.Resolution.ObservedAt, reader.Resolution.FreshUntil, dbNow,
		) || !communicationEvidenceCurrent(reader.Closure.ObservedAt, reader.Closure.FreshUntil, dbNow) {
		return ProtectedReadDecision{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff response authority expired while waiting for locks",
		)
	}
	if err := ValidateHandoffLineage(
		carrier.message, carrier.delivery, handoff, carrier.requiredCount,
	); err != nil {
		return ProtectedReadDecision{}, err
	}
	grant := EvaluateCurrentChannelGrant(
		ChannelGrantSnapshot{
			Verdict: VerdictClean, Code: "channel_grants_locked",
			ACLRevision: carrier.channel.ACLRevision, ObservedAt: dbNow, Grants: carrier.grants,
		},
		reader.Scope.TenantID, reader.Scope.WorkspaceID, carrier.channel.ID,
		reader.Closure, ChannelGrantRead, dbNow,
	)
	currentAudience, err := buildDirectNoticeCurrentAudience(
		reader, carrier.message, carrier.delivery, carrier.audiences,
		carrier.contributions, dbNow,
	)
	if err != nil {
		return ProtectedReadDecision{}, err
	}
	clean := func(code, ref string) AuthorityEvidence {
		return AuthorityEvidence{Verdict: VerdictClean, Code: code, EvidenceRef: ref}
	}
	carrierRef := ProtectedCarrierRef{
		Entity: entity, ChannelID: carrier.channel.ID,
		MessageID: carrier.message.ID, DeliveryID: carrier.delivery.ID,
	}
	evidence := ReadGateEvidence{
		Scope: reader.Scope, ChannelID: carrier.channel.ID,
		ChannelACLRevision: carrier.channel.ACLRevision, DBNow: dbNow,
		Operation: CommunicationHandoffResponse, Carrier: carrierRef,
		CarrierState: ProtectedCarrierSnapshot{
			Message: carrier.message, Delivery: carrier.delivery, Handoff: &handoff,
			RequiredDeliveryCount: carrier.requiredCount, ObservedAt: dbNow,
			Evidence: clean("carrier_rows_locked", "same_tx:handoff_carrier"),
		},
		Core: reader.Core, Principal: reader.Principal,
		PrincipalResolution: reader.Resolution, Recipient: reader.Recipient,
		DirectoryEpoch: store.AuthorizationFactRef{
			Kind: model.DirectoryEpochKind, ID: model.ID(reader.Scope.TenantID),
			Version: carrier.epoch.Version,
		},
		CurrentChannelGrant: grant,
		EntityRecipientGuard: BoundEntityRecipientEvidence{
			Scope: reader.Scope, Carrier: carrierRef, Principal: reader.Principal,
			Recipient: reader.Recipient, DirectoryEpoch: carrier.epoch.Version,
			EvaluatedAt: dbNow,
			Evidence:    clean("entity_recipient_current", "same_tx:handoff_recipient"),
		},
		CurrentAudience: currentAudience,
	}
	decision, err := EvaluateCarrierGate(evidence)
	if err != nil {
		return ProtectedReadDecision{}, err
	}
	switch decision.Verdict {
	case VerdictBroken:
		return ProtectedReadDecision{}, communicationError(
			ErrCommunicationForbidden, "Handoff target authority denied",
		)
	case VerdictUnknown:
		return ProtectedReadDecision{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff target authority is unavailable",
		)
	case VerdictClean:
	default:
		return ProtectedReadDecision{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff target authority has no verdict",
		)
	}
	if len(decision.RequiredClaims) != 0 || len(decision.SurvivingContributionIDs) != 1 ||
		decision.SurvivingContributionIDs[0] != carrier.contributions[0].ID ||
		!equalDirectNoticeAuthorityFacts(reader.Facts, decision.Facts) {
		return ProtectedReadDecision{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff target returned non-direct authority effects",
		)
	}
	deadline, constrained, err := directNoticeReadGrantFreshUntil(
		carrier.grants, reader.Closure, dbNow,
	)
	if err != nil {
		return ProtectedReadDecision{}, err
	}
	if constrained {
		if err := tx.narrowRequestAuthorityFreshUntil(deadline); err != nil {
			return ProtectedReadDecision{}, err
		}
	}
	return decision, nil
}

func handoffGrantFreshUntil(
	grants []ChannelGrant,
	closure ChannelGrantSubjectClosure,
	bit ChannelGrantBit,
	dbNow time.Time,
) (time.Time, bool, error) {
	if dbNow.IsZero() || closure.Outcome != ReadAllow || !bit.Valid() {
		return time.Time{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff ChannelGrant horizon is unavailable",
		)
	}
	subjects := make(map[CommunicationSubjectRef]struct{}, len(closure.Subjects))
	for _, subject := range closure.Subjects {
		if subject.Validate() != nil {
			return time.Time{}, false, communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff ChannelGrant closure is malformed",
			)
		}
		subjects[subject] = struct{}{}
	}
	found := false
	var latest time.Time
	for _, grant := range grants {
		if grant.State != ChannelGrantActive || !grantHasBit(grant, bit) {
			continue
		}
		if _, ok := subjects[grant.Subject]; !ok {
			continue
		}
		if grant.ExpiresAt != nil && !dbNow.Before(*grant.ExpiresAt) {
			continue
		}
		found = true
		if grant.ExpiresAt == nil {
			return time.Time{}, false, nil
		}
		if grant.ExpiresAt.After(latest) {
			latest = grant.ExpiresAt.UTC()
		}
	}
	if !found || latest.IsZero() {
		return time.Time{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff ChannelGrant horizon is unavailable",
		)
	}
	return latest, true, nil
}

func validatePreparedHandoffOffer(
	prepared handoffOfferPrepared,
	carrier handoffLockedCarrier,
) error {
	preparedMessage, messageErr := canonicalJSON(prepared.message)
	lockedMessage, lockedMessageErr := canonicalJSON(carrier.message)
	preparedDelivery, deliveryErr := canonicalJSON(prepared.delivery)
	lockedDelivery, lockedDeliveryErr := canonicalJSON(carrier.delivery)
	preparedChannel, channelErr := canonicalJSON(prepared.channel)
	lockedChannel, lockedChannelErr := canonicalJSON(carrier.channel)
	if messageErr != nil || lockedMessageErr != nil || deliveryErr != nil ||
		lockedDeliveryErr != nil || channelErr != nil || lockedChannelErr != nil ||
		!bytes.Equal(preparedMessage, lockedMessage) ||
		!bytes.Equal(preparedDelivery, lockedDelivery) ||
		!bytes.Equal(preparedChannel, lockedChannel) {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff protected content preparation crossed carrier lineage",
		)
	}
	return nil
}

func applyLockedHandoffOffer(
	ctx context.Context,
	tx *communicationTx,
	repositories handoffWorkRepositories,
	reader directNoticeReaderPreflight,
	normalized handoffOfferNormalized,
	ids handoffOfferIDs,
	prepared handoffOfferPrepared,
	carrier handoffLockedCarrier,
	work handoffLockedWork,
) (HandoffOfferResult, error) {
	if tx == nil || work.item.Int(model.ColVersion) < 1 ||
		work.item.Int(model.ColVersion) == math.MaxInt64 ||
		work.item.Int(colWorkLastEventSeq) < 1 ||
		work.item.Int(colWorkLastEventSeq) == math.MaxInt64 ||
		work.item.Int(colWorkOwnerEpoch) < 1 ||
		work.item.Int(colWorkOwnerEpoch) == math.MaxInt64 ||
		terminalWorkStatuses[work.item.String(colWorkStatus)] {
		return HandoffOfferResult{}, errHandoffStaleOffer
	}
	if work.item.Int(model.ColVersion) != normalized.expectedVersion {
		return HandoffOfferResult{}, errHandoffVersionMismatch
	}
	if recordID(work.lease) == "" || work.lease.String(colWorkWorkspaceID) != normalized.scope.WorkspaceID.String() ||
		work.lease.String(colWorkItemID) != normalized.command.WorkItemID.String() ||
		work.leaseState.Fence < 0 {
		return HandoffOfferResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff WorkLease lineage is unavailable",
		)
	}
	from := RecipientRef{
		Kind: RecipientKind(work.item.String(colWorkOwnerKind)),
		Ref:  work.item.String(colWorkOwnerRef),
	}
	actorRecipient := RecipientRef{Kind: RecipientUser, Ref: normalized.actor.Ref}
	if from.Validate() != nil || from != actorRecipient || carrier.delivery.Recipient == from ||
		carrier.message.Sender != normalized.actor || carrier.message.ChannelID != carrier.channel.ID ||
		carrier.message.WorkItemID != normalized.command.WorkItemID ||
		carrier.message.Kind != MessageHandoffOffer || carrier.message.State != MessagePublished ||
		carrier.delivery.ID != normalized.command.DeliveryID || !carrier.delivery.Required ||
		carrier.delivery.State != DeliveryAvailable || carrier.delivery.AckDueAt == nil ||
		!tx.now.Time().Before(*carrier.delivery.AckDueAt) ||
		carrier.audiences[0].DirectoryEpoch != carrier.epoch.Version ||
		carrier.contributions[0].DirectoryEpoch != carrier.epoch.Version ||
		carrier.contributions[0].RecipientEpoch != carrier.delivery.RecipientEpoch ||
		tx.now.Time().Before(carrier.delivery.AvailableAt) ||
		(carrier.delivery.ExpiresAt != nil && !tx.now.Time().Before(*carrier.delivery.ExpiresAt)) {
		return HandoffOfferResult{}, errHandoffStaleOffer
	}
	now := tx.now.Time()
	handoff := Handoff{
		MutableCommunicationEntity: MutableCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: ids.Handoff, TenantID: normalized.scope.TenantID,
				WorkspaceID: normalized.scope.WorkspaceID, Version: 1, CreatedAt: now,
			},
			UpdatedAt: now,
		},
		WorkItemID: normalized.command.WorkItemID,
		MessageID:  normalized.command.MessageID, DeliveryID: normalized.command.DeliveryID,
		From: from, FromOwnerEpoch: work.item.Int(colWorkOwnerEpoch),
		To: carrier.delivery.Recipient, OfferedLeaseFence: work.leaseState.Fence,
		ContextEventSeq: work.item.Int(colWorkLastEventSeq),
		Payload:         cloneProtectedPayload(prepared.payload), State: HandoffOffered,
		AckDeadline: *carrier.delivery.AckDueAt,
	}
	contextHash, err := CanonicalHandoffContextHash(handoff)
	if err != nil {
		return HandoffOfferResult{}, err
	}
	handoff.ContextHash = contextHash
	if err := ValidateHandoff(handoff); err != nil {
		return HandoffOfferResult{}, err
	}
	if err := ValidateHandoffLineage(
		carrier.message, carrier.delivery, handoff, carrier.requiredCount,
	); err != nil {
		return HandoffOfferResult{}, err
	}
	itemAfter := cloneHandoffRecord(work.item)
	itemAfter[colWorkLastEventSeq] = work.item.Int(colWorkLastEventSeq) + 1
	planHash, err := canonicalHandoffPlanHash(
		handoffOfferOperation, normalized.handoffCommandIdentity, reader.Facts,
		Handoff{}, handoff, carrier.delivery, carrier.delivery, nil,
		workProjectionFromRecords(itemAfter, work.lease),
		[]string{
			"handoff:insert", "work_item:cas", "work_event:append",
			"work_outbox:insert", "command_receipt:append", "audit:append",
		},
	)
	if err != nil {
		return HandoffOfferResult{}, err
	}
	audit, err := tx.appendAudit(ctx, model.AuditDraft{
		Actor: directNoticeActor(normalized.principal), ActorKind: model.ActorUser,
		Action: handoffOfferAuditAction, TargetKind: communicationCommandKind,
		TargetID: ids.Command, PayloadHash: append([]byte(nil), planHash...),
		Meta: map[string]any{
			"workspace_id":  normalized.scope.WorkspaceID.String(),
			"command_scope": normalized.commandScope,
			"handoff_id":    ids.Handoff.String(), "message_id": handoff.MessageID.String(),
			"delivery_id": handoff.DeliveryID.String(), "work_item_id": handoff.WorkItemID.String(),
			"to_kind": string(handoff.To.Kind), "to_ref": handoff.To.Ref,
			"offered_lease_fence": handoff.OfferedLeaseFence,
		},
	})
	if err != nil {
		return HandoffOfferResult{}, err
	}
	if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
		return HandoffOfferResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff offer audit append returned no durable anchor",
		)
	}
	if _, err := handoffUpdateRawRecord(ctx, tx, repositories.item, itemAfter); err != nil {
		return HandoffOfferResult{}, err
	}
	handoffRecord, err := handoffToRecord(handoff)
	if err != nil {
		return HandoffOfferResult{}, err
	}
	if _, err := tx.createWithID(ctx, handoffKind, ids.Handoff, handoffRecord); err != nil {
		return HandoffOfferResult{}, err
	}
	if err := persistHandoffWorkEvent(
		ctx, tx, normalized.scope, ids.Event, ids.Command, audit,
		itemAfter, handoffOfferEventType, normalized.actor,
		map[string]any{
			"schema_version": int64(1), "command": handoffOfferOperation,
			"handoff_id": handoff.ID.String(), "message_id": handoff.MessageID.String(),
			"delivery_id": handoff.DeliveryID.String(), "work_item_id": handoff.WorkItemID.String(),
			"state": string(handoff.State), "owner_epoch": handoff.FromOwnerEpoch,
			"lease_fence": handoff.OfferedLeaseFence,
			"plan_hash":   hex.EncodeToString(planHash),
		},
	); err != nil {
		return HandoffOfferResult{}, err
	}
	result := HandoffOfferResult{
		CommandID: ids.Command, HandoffID: handoff.ID, MessageID: handoff.MessageID,
		DeliveryID: handoff.DeliveryID, WorkItemID: handoff.WorkItemID,
		EventID: ids.Event, Version: handoff.Version,
		ETag: fmt.Sprintf("\"v%d\"", handoff.Version), State: handoff.State,
		AuditSeq: audit.Seq,
	}
	receipt, err := buildHandoffReceipt(
		normalized.handoffCommandIdentity, ids.Receipt, ids.Command, planHash,
		audit, result.EventID, handoff.ID, result.Version, string(result.State),
		map[string]model.ID{
			"handoff_id": result.HandoffID, "message_id": result.MessageID,
			"delivery_id": result.DeliveryID, "work_item_id": result.WorkItemID,
			"event_id": result.EventID,
		},
		map[string][]byte{
			"request": normalized.requestDigest, "plan": planHash,
			"payload": handoff.Payload.Digest,
		},
		now,
	)
	if err != nil {
		return HandoffOfferResult{}, err
	}
	receiptRecord, err := communicationCommandReceiptToRecord(receipt)
	if err != nil {
		return HandoffOfferResult{}, err
	}
	if _, err := tx.createWithID(
		ctx, communicationCommandKind, ids.Receipt, receiptRecord,
	); err != nil {
		return HandoffOfferResult{}, err
	}
	return result, nil
}

func applyLockedHandoffResponse(
	ctx context.Context,
	tx *communicationTx,
	repositories handoffWorkRepositories,
	reader directNoticeReaderPreflight,
	normalized handoffResponseNormalized,
	ids handoffResponseIDs,
	prepared handoffResponsePrepared,
	carrier handoffLockedCarrier,
	before Handoff,
	work handoffLockedWork,
) (HandoffResponseResult, error) {
	if tx == nil || before.Version == math.MaxInt64 || work.item.Int(model.ColVersion) < 1 ||
		work.item.Int(model.ColVersion) == math.MaxInt64 ||
		work.item.Int(colWorkLastEventSeq) == math.MaxInt64 ||
		work.item.Int(colWorkOwnerEpoch) == math.MaxInt64 ||
		carrier.delivery.Version == math.MaxInt64 {
		return HandoffResponseResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff response locked evidence is unavailable",
		)
	}
	if before.Version != normalized.expectedVersion {
		return HandoffResponseResult{}, errHandoffVersionMismatch
	}
	if before.State != HandoffOffered || !tx.now.Time().Before(before.AckDeadline) ||
		before.To != (RecipientRef{Kind: RecipientUser, Ref: normalized.actor.Ref}) ||
		terminalWorkStatuses[work.item.String(colWorkStatus)] ||
		work.item.String(colWorkWorkspaceID) != normalized.scope.WorkspaceID.String() ||
		recordID(work.item) != before.WorkItemID ||
		work.item.String(colWorkOwnerKind) != string(before.From.Kind) ||
		work.item.String(colWorkOwnerRef) != before.From.Ref ||
		work.item.Int(colWorkOwnerEpoch) != before.FromOwnerEpoch ||
		work.item.Int(colWorkLastEventSeq) != before.ContextEventSeq+1 ||
		work.lease.String(colWorkWorkspaceID) != normalized.scope.WorkspaceID.String() ||
		work.lease.String(colWorkItemID) != before.WorkItemID.String() ||
		work.leaseState.Fence != before.OfferedLeaseFence {
		return HandoffResponseResult{}, errHandoffStaleOffer
	}
	now := tx.now.Time()
	itemAfter := cloneHandoffRecord(work.item)
	itemAfter[colWorkLastEventSeq] = work.item.Int(colWorkLastEventSeq) + 1
	leaseAfter := cloneHandoffRecord(work.lease)
	guardAfter := cloneHandoffRecord(work.clockGuard)
	deliveryAfter := carrier.delivery
	var ack *MessageAck
	resultingFence := int64(0)
	terminalCode := "handoff_rejected"
	terminalReason := prepared.terminalReason
	if normalized.command.Transition == HandoffAccept {
		if prepared.terminalReason != nil || work.leaseState.Lifecycle != fenceActive ||
			carrier.delivery.State != DeliveryAvailable ||
			now.Before(carrier.delivery.AvailableAt) || carrier.delivery.AckDueAt == nil ||
			!now.Before(*carrier.delivery.AckDueAt) ||
			(carrier.delivery.ExpiresAt != nil && !now.Before(*carrier.delivery.ExpiresAt)) {
			return HandoffResponseResult{}, errHandoffStaleOffer
		}
		next, err := fenceRelease(
			work.leaseState,
			fenceToken{Holder: work.leaseState.Holder, Fence: work.leaseState.Fence},
			now, handoffLeaseEndReason,
			fenceEndPolicy{Lifecycle: fenceRevoked, Bump: true},
		)
		if err != nil {
			return HandoffResponseResult{}, errHandoffStaleOffer
		}
		resultingFence = next.Fence
		applyWorkLeaseFenceState(
			leaseAfter, next,
			work.lease.String(colLeaseHolderSID),
			work.lease.String(colLeaseHolderRunRef),
			work.lease.String(colLeaseHolderAgentRef),
		)
		if len(work.clockGuard) == 0 || work.clockGuard.Int(colGuardEpoch) < 1 ||
			work.clockGuard.Int(colGuardEpoch) == math.MaxInt64 ||
			work.clockGuard.IsNull(colGuardLastDBTime) {
			return HandoffResponseResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff lease clock guard is unavailable",
			)
		}
		last, err := model.ParseTimestamp(work.clockGuard.String(colGuardLastDBTime))
		if err != nil || now.Before(last.Time()) {
			return HandoffResponseResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff lease clock moved backwards",
			)
		}
		guardAfter[colGuardEpoch] = work.clockGuard.Int(colGuardEpoch) + 1
		guardAfter[colGuardLastDBTime] = tx.now.String()
		itemAfter[colWorkOwnerKind] = string(before.To.Kind)
		itemAfter[colWorkOwnerRef] = before.To.Ref
		itemAfter[colWorkOwnerEpoch] = before.FromOwnerEpoch + 1
		deliveryAfter.Version++
		deliveryAfter.UpdatedAt = now
		deliveryAfter.State = DeliveryAcknowledged
		deliveryAfter.AckID = ids.Ack
		deliveryAfter.AcknowledgedAt = &now
		createdAck := MessageAck{
			AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: ids.Ack, TenantID: normalized.scope.TenantID,
					WorkspaceID: normalized.scope.WorkspaceID, Version: 1, CreatedAt: now,
				},
			},
			DeliveryID: carrier.delivery.ID, Kind: MessageAckReceived,
			Actor: normalized.actor, AcknowledgedAt: now, Late: false,
		}
		if err := ValidateMessageDelivery(deliveryAfter); err != nil {
			return HandoffResponseResult{}, err
		}
		if err := ValidateMessageAck(createdAck); err != nil {
			return HandoffResponseResult{}, err
		}
		ack = &createdAck
		terminalCode, terminalReason = "", nil
	} else if normalized.command.Transition != HandoffReject || terminalReason == nil {
		return HandoffResponseResult{}, communicationError(
			ErrInvalidCommunicationModel, "invalid Handoff response transition",
		)
	}
	plan, err := PlanHandoffTransition(
		before, normalized.command.Transition, ids.Ack, resultingFence,
		terminalCode, terminalReason, now,
	)
	if normalized.command.Transition == HandoffReject {
		plan, err = PlanHandoffTransition(
			before, normalized.command.Transition, "", 0,
			terminalCode, terminalReason, now,
		)
	}
	if err != nil {
		return HandoffResponseResult{}, err
	}
	if err := ValidateHandoffLineage(
		carrier.message, deliveryAfter, plan.After, carrier.requiredCount,
	); err != nil {
		return HandoffResponseResult{}, err
	}
	effects := []string{
		"handoff:cas", "work_item:cas", "work_event:append",
		"work_outbox:insert", "command_receipt:append", "audit:append",
	}
	if normalized.command.Transition == HandoffAccept {
		effects = append(effects, "work_lease:cas", "work_guard:cas", "delivery:cas", "ack:append")
	}
	planHash, err := canonicalHandoffPlanHash(
		handoffResponseOperation, normalized.handoffCommandIdentity, reader.Facts,
		plan.Before, plan.After, carrier.delivery, deliveryAfter, ack,
		workProjectionFromRecords(itemAfter, leaseAfter), effects,
	)
	if err != nil {
		return HandoffResponseResult{}, err
	}
	audit, err := tx.appendAudit(ctx, model.AuditDraft{
		Actor: directNoticeActor(normalized.principal), ActorKind: model.ActorUser,
		Action: handoffResponseAuditAction, TargetKind: communicationCommandKind,
		TargetID: ids.Command, PayloadHash: append([]byte(nil), planHash...),
		Meta: map[string]any{
			"workspace_id":  normalized.scope.WorkspaceID.String(),
			"command_scope": normalized.commandScope,
			"handoff_id":    plan.After.ID.String(), "message_id": plan.After.MessageID.String(),
			"delivery_id": plan.After.DeliveryID.String(), "work_item_id": plan.After.WorkItemID.String(),
			"transition":            string(normalized.command.Transition),
			"owner_epoch":           itemAfter.Int(colWorkOwnerEpoch),
			"resulting_lease_fence": resultingFence,
		},
	})
	if err != nil {
		return HandoffResponseResult{}, err
	}
	if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
		return HandoffResponseResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff response audit append returned no durable anchor",
		)
	}
	if _, err := handoffUpdateRawRecord(ctx, tx, repositories.item, itemAfter); err != nil {
		return HandoffResponseResult{}, err
	}
	if normalized.command.Transition == HandoffAccept {
		if _, err := handoffUpdateRawRecord(ctx, tx, repositories.lease, leaseAfter); err != nil {
			return HandoffResponseResult{}, err
		}
		if _, err := handoffUpdateRawRecord(ctx, tx, repositories.guard, guardAfter); err != nil {
			return HandoffResponseResult{}, err
		}
	}
	if ack != nil {
		deliveryRecord, err := messageDeliveryToRecord(deliveryAfter)
		if err != nil {
			return HandoffResponseResult{}, err
		}
		deliveryRecord[model.ColVersion] = carrier.delivery.Version
		if _, err := tx.update(ctx, messageDeliveryKind, deliveryRecord); err != nil {
			return HandoffResponseResult{}, err
		}
		ackRecord, err := messageAckToRecord(*ack)
		if err != nil {
			return HandoffResponseResult{}, err
		}
		if _, err := tx.createWithID(ctx, messageAckKind, ids.Ack, ackRecord); err != nil {
			return HandoffResponseResult{}, err
		}
	}
	handoffRecord, err := handoffToRecord(plan.After)
	if err != nil {
		return HandoffResponseResult{}, err
	}
	handoffRecord[model.ColVersion] = plan.Before.Version
	if _, err := tx.update(ctx, handoffKind, handoffRecord); err != nil {
		return HandoffResponseResult{}, err
	}
	eventType := handoffRejectedEventType
	if normalized.command.Transition == HandoffAccept {
		eventType = handoffAcceptedEventType
	}
	if err := persistHandoffWorkEvent(
		ctx, tx, normalized.scope, ids.Event, ids.Command, audit, itemAfter,
		eventType, normalized.actor,
		map[string]any{
			"schema_version": int64(1), "command": handoffResponseOperation,
			"transition": string(normalized.command.Transition),
			"handoff_id": plan.After.ID.String(), "message_id": plan.After.MessageID.String(),
			"delivery_id": plan.After.DeliveryID.String(), "work_item_id": plan.After.WorkItemID.String(),
			"state": string(plan.After.State), "owner_epoch": itemAfter.Int(colWorkOwnerEpoch),
			"lease_fence": resultingFence, "plan_hash": hex.EncodeToString(planHash),
		},
	); err != nil {
		return HandoffResponseResult{}, err
	}
	result := HandoffResponseResult{
		CommandID: ids.Command, HandoffID: plan.After.ID, MessageID: plan.After.MessageID,
		DeliveryID: plan.After.DeliveryID, WorkItemID: plan.After.WorkItemID,
		EventID: ids.Event, Version: plan.After.Version,
		ETag: fmt.Sprintf("\"v%d\"", plan.After.Version), State: plan.After.State,
		OwnerEpoch: itemAfter.Int(colWorkOwnerEpoch), ResultingLeaseFence: resultingFence,
		AuditSeq: audit.Seq,
	}
	if ack != nil {
		result.AckID = ack.ID
	}
	projectionIDs := map[string]model.ID{
		"handoff_id": result.HandoffID, "message_id": result.MessageID,
		"delivery_id": result.DeliveryID, "work_item_id": result.WorkItemID,
		"event_id": result.EventID,
	}
	if result.AckID != "" {
		projectionIDs["ack_id"] = result.AckID
	}
	receiptDigests := map[string][]byte{
		"request": normalized.requestDigest, "plan": planHash,
		"payload": plan.After.Payload.Digest,
	}
	if plan.After.TerminalReason != nil {
		receiptDigests["response"] = plan.After.TerminalReason.Digest
	}
	receipt, err := buildHandoffReceipt(
		normalized.handoffCommandIdentity, ids.Receipt, ids.Command, planHash,
		audit, result.EventID, plan.After.ID, result.Version, string(result.State),
		projectionIDs, receiptDigests,
		now,
	)
	if err != nil {
		return HandoffResponseResult{}, err
	}
	receiptRecord, err := communicationCommandReceiptToRecord(receipt)
	if err != nil {
		return HandoffResponseResult{}, err
	}
	if _, err := tx.createWithID(ctx, communicationCommandKind, ids.Receipt, receiptRecord); err != nil {
		return HandoffResponseResult{}, err
	}
	return result, nil
}

type handoffWorkProjection struct {
	WorkItemID   model.ID `json:"work_item_id"`
	WorkVersion  int64    `json:"work_version"`
	OwnerKind    string   `json:"owner_kind"`
	OwnerRef     string   `json:"owner_ref"`
	OwnerEpoch   int64    `json:"owner_epoch"`
	LastEventSeq int64    `json:"last_event_seq"`
	LeaseID      model.ID `json:"lease_id"`
	LeaseVersion int64    `json:"lease_version"`
	LeaseState   string   `json:"lease_state"`
	LeaseFence   int64    `json:"lease_fence"`
}

type handoffPlanHashV1 struct {
	SchemaVersion  int64                        `json:"schema_version"`
	Operation      string                       `json:"operation"`
	Scope          DirectoryScopeRef            `json:"scope"`
	Actor          CommunicationActorRef        `json:"actor"`
	CommandScope   string                       `json:"command_scope"`
	RequestDigest  []byte                       `json:"request_digest"`
	IfMatch        int64                        `json:"if_match"`
	Before         Handoff                      `json:"before"`
	After          Handoff                      `json:"after"`
	DeliveryBefore MessageDelivery              `json:"delivery_before"`
	DeliveryAfter  MessageDelivery              `json:"delivery_after"`
	Ack            *MessageAck                  `json:"ack,omitempty"`
	Work           handoffWorkProjection        `json:"work"`
	Facts          []store.AuthorizationFactRef `json:"facts"`
	RowEffects     []string                     `json:"row_effects"`
}

func canonicalHandoffPlanHash(
	operation string,
	identity handoffCommandIdentity,
	facts []store.AuthorizationFactRef,
	before Handoff,
	after Handoff,
	deliveryBefore MessageDelivery,
	deliveryAfter MessageDelivery,
	ack *MessageAck,
	work handoffWorkProjection,
	effects []string,
) ([]byte, error) {
	effects = append([]string(nil), effects...)
	sort.Strings(effects)
	raw, err := canonicalJSON(handoffPlanHashV1{
		SchemaVersion: 1, Operation: operation, Scope: identity.scope,
		Actor: identity.actor, CommandScope: identity.commandScope,
		RequestDigest: append([]byte(nil), identity.requestDigest...),
		IfMatch:       identity.expectedVersion, Before: before, After: after,
		DeliveryBefore: deliveryBefore, DeliveryAfter: deliveryAfter, Ack: ack,
		Work: work, Facts: sortedDirectNoticeFacts(facts), RowEffects: effects,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func workProjectionFromRecords(item, lease model.Record) handoffWorkProjection {
	return handoffWorkProjection{
		WorkItemID: recordID(item), WorkVersion: item.Int(model.ColVersion),
		OwnerKind: item.String(colWorkOwnerKind), OwnerRef: item.String(colWorkOwnerRef),
		OwnerEpoch: item.Int(colWorkOwnerEpoch), LastEventSeq: item.Int(colWorkLastEventSeq),
		LeaseID: recordID(lease), LeaseVersion: lease.Int(model.ColVersion),
		LeaseState: lease.String(colLeaseState), LeaseFence: lease.Int(colLeaseFence),
	}
}

func cloneHandoffRecord(record model.Record) model.Record {
	if record == nil {
		return nil
	}
	clone := make(model.Record, len(record))
	for key, value := range record {
		switch typed := value.(type) {
		case []byte:
			clone[key] = append([]byte(nil), typed...)
		default:
			clone[key] = value
		}
	}
	return clone
}

func persistHandoffWorkEvent(
	ctx context.Context,
	tx *communicationTx,
	scope DirectoryScopeRef,
	eventID model.ID,
	commandID model.ID,
	audit model.AuditEvent,
	item model.Record,
	eventType string,
	actor CommunicationActorRef,
	payloadDocument map[string]any,
) error {
	if tx == nil || !validCanonicalCommunicationID(eventID) ||
		!validCanonicalCommunicationID(commandID) || audit.Seq < 1 ||
		len(audit.Hash) != sha256.Size || recordID(item) == "" ||
		item.String(colWorkWorkspaceID) != scope.WorkspaceID.String() ||
		item.Int(colWorkLastEventSeq) < 1 || actor.Validate() != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff WorkEvent anchor is unavailable",
		)
	}
	payload, err := canonicalJSON(payloadDocument)
	if err != nil || len(payload) > 16*1024 {
		return communicationError(ErrInvalidCommunicationModel, "Handoff WorkEvent payload is invalid")
	}
	if _, err := tx.create(ctx, workEventKind, model.Record{
		colWorkWorkspaceID: scope.WorkspaceID.String(), colEventID: eventID.String(),
		colEventAggregateKind: string(workItemKind), colEventAggregateID: recordID(item).String(),
		colEventSeq: item.Int(colWorkLastEventSeq), colEventType: eventType,
		colEventActorKind: string(actor.Kind), colEventActorRef: actor.Ref,
		colEventOccurredAt: tx.now.String(), colEventPayload: string(payload),
		colEventPayloadHash: hashBytes(payload), colEventCommandID: commandID.String(),
		colEventAuditSeq: audit.Seq, colEventAuditHash: append([]byte(nil), audit.Hash...),
	}); err != nil {
		return err
	}
	_, err = tx.create(ctx, workOutboxKind, model.Record{
		colWorkWorkspaceID: scope.WorkspaceID.String(), colOutboxEventID: eventID.String(),
		colOutboxState: "pending", colOutboxAttempts: int64(0),
		colOutboxNextAttemptAt: tx.now.String(), colOutboxClaimOwner: nil,
		colOutboxClaimUntil: nil, colOutboxPublishedAt: nil, colOutboxLastOutcome: nil,
	})
	return err
}

func buildHandoffReceipt(
	identity handoffCommandIdentity,
	receiptID model.ID,
	commandID model.ID,
	planHash []byte,
	audit model.AuditEvent,
	eventID model.ID,
	handoffID model.ID,
	version int64,
	state string,
	ids map[string]model.ID,
	digests map[string][]byte,
	completedAt time.Time,
) (CommunicationCommandReceipt, error) {
	projectionIDs := make(map[string]model.ID, len(ids))
	for key, value := range ids {
		projectionIDs[key] = value
	}
	projectionDigests := make(map[string][]byte, len(digests))
	for key, value := range digests {
		projectionDigests[key] = append([]byte(nil), value...)
	}
	status := http.StatusOK
	if state == string(HandoffOffered) {
		status = http.StatusCreated
	}
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: receiptID, TenantID: identity.scope.TenantID,
				WorkspaceID: identity.scope.WorkspaceID, Version: 1, CreatedAt: completedAt,
			},
		},
		CommandID: commandID, ActorFingerprint: append([]byte(nil), identity.actorFingerprint...),
		CommandScope:       identity.commandScope,
		IdempotencyKeyHash: append([]byte(nil), identity.idempotencyKeyHash...),
		RequestDigest:      append([]byte(nil), identity.requestDigest...),
		PlanHash:           append([]byte(nil), planHash...), ResultKind: string(handoffKind),
		ResultID: handoffID, HTTPStatus: status,
		ResponseProjectionJSON: CommunicationCommandResponseProjection{
			IDs: projectionIDs, Version: version, State: state, Digests: projectionDigests,
		},
		EventID: eventID, AuditSeq: audit.Seq,
		AuditHash: append([]byte(nil), audit.Hash...), CompletedAt: completedAt,
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

func findHandoffReceipt(
	ctx context.Context,
	tx *communicationTx,
	identity handoffCommandIdentity,
) (CommunicationCommandReceipt, bool, error) {
	repo, err := tx.repo(communicationCommandKind)
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	rows, page, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		{Column: colCommActorFingerprint, Op: model.OpEq, Value: identity.actorFingerprint},
		{Column: colCommCommandScope, Op: model.OpEq, Value: identity.commandScope},
		{Column: colCommIdempotencyKeyHash, Op: model.OpEq, Value: identity.idempotencyKeyHash},
	}, Limit: handoffMaxReceiptLookupRows})
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	if len(rows) == 0 && !page.HasMore {
		return CommunicationCommandReceipt{}, false, nil
	}
	if len(rows) != 1 || page.HasMore {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff receipt uniqueness is unavailable",
		)
	}
	receipt, err := communicationCommandReceiptFromRecord(rows[0])
	if err != nil {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff receipt cannot be decoded",
		)
	}
	if receipt.TenantID != identity.scope.TenantID ||
		receipt.WorkspaceID != identity.scope.WorkspaceID ||
		receipt.CommandScope != identity.commandScope ||
		!bytes.Equal(receipt.ActorFingerprint, identity.actorFingerprint) ||
		!bytes.Equal(receipt.IdempotencyKeyHash, identity.idempotencyKeyHash) {
		return CommunicationCommandReceipt{}, false, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff receipt crosses command scope",
		)
	}
	return receipt, true, nil
}

func handoffIdempotencyLockKey(identity handoffCommandIdentity) string {
	return fmt.Sprintf(
		"sessions.communication.handoff.idempotency/%s/%s/%x",
		identity.scope.WorkspaceID, identity.commandScope, identity.idempotencyKeyHash,
	)
}

func handoffOfferResultFromReceipt(
	receipt CommunicationCommandReceipt,
) (HandoffOfferResult, error) {
	projection := receipt.ResponseProjectionJSON
	ids := projection.IDs
	if ValidateCommunicationCommandReceipt(receipt) != nil ||
		receipt.ResultKind != string(handoffKind) || receipt.HTTPStatus != http.StatusCreated ||
		projection.Version != 1 || projection.State != string(HandoffOffered) ||
		receipt.ResultID != ids["handoff_id"] || receipt.EventID != ids["event_id"] ||
		ids["message_id"] == "" || ids["delivery_id"] == "" || ids["work_item_id"] == "" {
		return HandoffOfferResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff offer receipt projection is unavailable",
		)
	}
	return HandoffOfferResult{
		CommandID: receipt.CommandID, HandoffID: ids["handoff_id"],
		MessageID: ids["message_id"], DeliveryID: ids["delivery_id"],
		WorkItemID: ids["work_item_id"], EventID: receipt.EventID,
		Version: projection.Version, ETag: fmt.Sprintf("\"v%d\"", projection.Version),
		State: HandoffOffered, AuditSeq: receipt.AuditSeq, Replayed: true,
	}, nil
}

func handoffResponseResultFromReceipt(
	receipt CommunicationCommandReceipt,
	handoff Handoff,
) (HandoffResponseResult, error) {
	projection := receipt.ResponseProjectionJSON
	ids := projection.IDs
	state := HandoffState(projection.State)
	if ValidateCommunicationCommandReceipt(receipt) != nil || ValidateHandoff(handoff) != nil ||
		receipt.ResultKind != string(handoffKind) || receipt.HTTPStatus != http.StatusOK ||
		!oneOf(state, HandoffAccepted, HandoffRejected) ||
		projection.Version != handoff.Version || state != handoff.State ||
		receipt.ResultID != handoff.ID || receipt.ResultID != ids["handoff_id"] ||
		receipt.EventID != ids["event_id"] || ids["message_id"] != handoff.MessageID ||
		ids["delivery_id"] != handoff.DeliveryID || ids["work_item_id"] != handoff.WorkItemID {
		return HandoffResponseResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff response receipt projection is unavailable",
		)
	}
	ownerEpoch := handoff.FromOwnerEpoch
	if state == HandoffAccepted {
		ownerEpoch++
		if ids["ack_id"] != handoff.AckID {
			return HandoffResponseResult{}, communicationError(
				ErrCommunicationEvidenceUnknown, "Handoff response Ack projection is unavailable",
			)
		}
	} else if ids["ack_id"] != "" {
		return HandoffResponseResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "rejected Handoff receipt carries an Ack",
		)
	}
	return HandoffResponseResult{
		CommandID: receipt.CommandID, HandoffID: handoff.ID, AckID: ids["ack_id"],
		MessageID: handoff.MessageID, DeliveryID: handoff.DeliveryID,
		WorkItemID: handoff.WorkItemID, EventID: receipt.EventID,
		Version: projection.Version, ETag: fmt.Sprintf("\"v%d\"", projection.Version),
		State: state, OwnerEpoch: ownerEpoch,
		ResultingLeaseFence: handoff.ResultingLeaseFence,
		AuditSeq:            receipt.AuditSeq, Replayed: true,
	}, nil
}

func handoffLifecycleResultFromReceipt(
	receipt CommunicationCommandReceipt,
	handoff Handoff,
) (HandoffLifecycleResult, error) {
	projection := receipt.ResponseProjectionJSON
	ids := projection.IDs
	state := HandoffState(projection.State)
	if ValidateCommunicationCommandReceipt(receipt) != nil || ValidateHandoff(handoff) != nil ||
		receipt.ResultKind != string(handoffKind) || receipt.HTTPStatus != http.StatusOK ||
		!oneOf(state, HandoffWithdrawn, HandoffExpired) ||
		projection.Version != handoff.Version || state != handoff.State ||
		receipt.ResultID != handoff.ID || receipt.ResultID != ids["handoff_id"] ||
		receipt.EventID != ids["event_id"] || ids["message_id"] != handoff.MessageID ||
		ids["delivery_id"] != handoff.DeliveryID || ids["work_item_id"] != handoff.WorkItemID ||
		ids["ack_id"] != "" {
		return HandoffLifecycleResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff lifecycle receipt projection is unavailable",
		)
	}
	return HandoffLifecycleResult{
		CommandID: receipt.CommandID, HandoffID: handoff.ID,
		MessageID: handoff.MessageID, DeliveryID: handoff.DeliveryID,
		WorkItemID: handoff.WorkItemID, EventID: receipt.EventID,
		Version: projection.Version, ETag: fmt.Sprintf("\"v%d\"", projection.Version),
		State: state, AuditSeq: receipt.AuditSeq, Replayed: true,
	}, nil
}

func applyLockedHandoffLifecycle(
	ctx context.Context,
	tx *communicationTx,
	repositories handoffWorkRepositories,
	identity handoffCommandIdentity,
	facts []store.AuthorizationFactRef,
	ids handoffLifecycleIDs,
	prepared handoffResponsePrepared,
	carrier handoffLockedCarrier,
	before Handoff,
	work handoffLockedWork,
	transition HandoffTransition,
	operation string,
	auditAction string,
	eventType string,
	terminalCode string,
) (HandoffLifecycleResult, error) {
	if tx == nil || before.Version == math.MaxInt64 ||
		work.item.Int(model.ColVersion) < 1 || work.item.Int(model.ColVersion) == math.MaxInt64 ||
		work.item.Int(colWorkLastEventSeq) < 1 ||
		work.item.Int(colWorkLastEventSeq) == math.MaxInt64 ||
		work.item.Int(colWorkOwnerEpoch) < 1 || prepared.terminalReason == nil ||
		!oneOf(transition, HandoffWithdraw, HandoffExpire) ||
		!boundedToken(operation, 128) || !boundedToken(auditAction, 128) ||
		!boundedToken(eventType, 128) || !boundedToken(terminalCode, 128) {
		return HandoffLifecycleResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff lifecycle locked evidence is unavailable",
		)
	}
	if before.Version != identity.expectedVersion {
		return HandoffLifecycleResult{}, errHandoffVersionMismatch
	}
	if before.State != HandoffOffered ||
		before.TenantID != identity.scope.TenantID ||
		before.WorkspaceID != identity.scope.WorkspaceID ||
		recordID(work.item) != before.WorkItemID ||
		work.item.String(colWorkWorkspaceID) != identity.scope.WorkspaceID.String() ||
		work.lease.String(colWorkWorkspaceID) != identity.scope.WorkspaceID.String() ||
		work.lease.String(colWorkItemID) != before.WorkItemID.String() ||
		work.leaseState.Fence < 0 {
		return HandoffLifecycleResult{}, errHandoffStaleOffer
	}
	now := tx.now.Time()
	if ValidateProtectedPayloadSlot(
		*prepared.terminalReason, PayloadSlotHandoffTerminalReason,
		protectedPayloadPolicyFrom(before.Payload),
	) != nil {
		return HandoffLifecycleResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "Handoff terminal reason preparation is unavailable",
		)
	}
	switch transition {
	case HandoffWithdraw:
		actorRecipient := RecipientRef{Kind: RecipientUser, Ref: identity.actor.Ref}
		if identity.actor.Kind != ActorUser || actorRecipient != before.From ||
			!now.Before(before.AckDeadline) ||
			terminalWorkStatuses[work.item.String(colWorkStatus)] ||
			work.item.String(colWorkOwnerKind) != string(before.From.Kind) ||
			work.item.String(colWorkOwnerRef) != before.From.Ref ||
			work.item.Int(colWorkOwnerEpoch) != before.FromOwnerEpoch ||
			work.item.Int(colWorkLastEventSeq) != before.ContextEventSeq+1 ||
			work.leaseState.Fence != before.OfferedLeaseFence ||
			carrier.delivery.State != DeliveryAvailable || carrier.delivery.AckID != "" {
			return HandoffLifecycleResult{}, errHandoffStaleOffer
		}
	case HandoffExpire:
		if identity.actor.Kind != ActorSystem || now.Before(before.AckDeadline) {
			return HandoffLifecycleResult{}, communicationError(
				ErrInvalidCommunicationTransition, "Handoff deadline has not elapsed",
			)
		}
	}
	plan, err := PlanHandoffTransition(
		before, transition, "", 0, terminalCode, prepared.terminalReason, now,
	)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	if err := ValidateHandoffLineage(
		carrier.message, carrier.delivery, plan.After, carrier.requiredCount,
	); err != nil {
		return HandoffLifecycleResult{}, err
	}
	itemAfter := cloneHandoffRecord(work.item)
	itemAfter[colWorkLastEventSeq] = work.item.Int(colWorkLastEventSeq) + 1
	effects := []string{
		"handoff:cas", "work_item:cas", "work_event:append",
		"work_outbox:insert", "command_receipt:append", "audit:append",
	}
	planHash, err := canonicalHandoffPlanHash(
		operation, identity, facts, plan.Before, plan.After,
		carrier.delivery, carrier.delivery, nil,
		workProjectionFromRecords(itemAfter, work.lease), effects,
	)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	auditActor, auditActorKind := handoffLifecycleAuditActor(identity.actor)
	audit, err := tx.appendAudit(ctx, model.AuditDraft{
		Actor: auditActor, ActorKind: auditActorKind,
		Action: auditAction, TargetKind: communicationCommandKind,
		TargetID: ids.Command, PayloadHash: append([]byte(nil), planHash...),
		Meta: map[string]any{
			"workspace_id":  identity.scope.WorkspaceID.String(),
			"command_scope": identity.commandScope,
			"handoff_id":    plan.After.ID.String(), "message_id": plan.After.MessageID.String(),
			"delivery_id": plan.After.DeliveryID.String(), "work_item_id": plan.After.WorkItemID.String(),
			"transition": string(transition), "owner_epoch": work.item.Int(colWorkOwnerEpoch),
			"lease_fence": work.leaseState.Fence,
		},
	})
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
		return HandoffLifecycleResult{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"Handoff lifecycle audit append returned no durable anchor",
		)
	}
	if _, err := handoffUpdateRawRecord(ctx, tx, repositories.item, itemAfter); err != nil {
		return HandoffLifecycleResult{}, err
	}
	handoffRecord, err := handoffToRecord(plan.After)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	handoffRecord[model.ColVersion] = plan.Before.Version
	if _, err := tx.update(ctx, handoffKind, handoffRecord); err != nil {
		return HandoffLifecycleResult{}, fmt.Errorf("persist Handoff lifecycle: %w", err)
	}
	if err := persistHandoffWorkEvent(
		ctx, tx, identity.scope, ids.Event, ids.Command, audit, itemAfter,
		eventType, identity.actor,
		map[string]any{
			"schema_version": int64(1), "command": operation,
			"transition": string(transition),
			"handoff_id": plan.After.ID.String(), "message_id": plan.After.MessageID.String(),
			"delivery_id": plan.After.DeliveryID.String(), "work_item_id": plan.After.WorkItemID.String(),
			"state": string(plan.After.State), "owner_epoch": work.item.Int(colWorkOwnerEpoch),
			"lease_fence": work.leaseState.Fence, "plan_hash": hex.EncodeToString(planHash),
		},
	); err != nil {
		return HandoffLifecycleResult{}, err
	}
	result := HandoffLifecycleResult{
		CommandID: ids.Command, HandoffID: plan.After.ID,
		MessageID: plan.After.MessageID, DeliveryID: plan.After.DeliveryID,
		WorkItemID: plan.After.WorkItemID, EventID: ids.Event,
		Version: plan.After.Version, ETag: fmt.Sprintf("\"v%d\"", plan.After.Version),
		State: plan.After.State, AuditSeq: audit.Seq,
	}
	receipt, err := buildHandoffReceipt(
		identity, ids.Receipt, ids.Command, planHash, audit, ids.Event,
		plan.After.ID, plan.After.Version, string(plan.After.State),
		map[string]model.ID{
			"handoff_id": plan.After.ID, "message_id": plan.After.MessageID,
			"delivery_id": plan.After.DeliveryID, "work_item_id": plan.After.WorkItemID,
			"event_id": ids.Event,
		},
		map[string][]byte{
			"request": identity.requestDigest, "plan": planHash,
			"payload":  plan.After.Payload.Digest,
			"response": plan.After.TerminalReason.Digest,
		},
		now,
	)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	receiptRecord, err := communicationCommandReceiptToRecord(receipt)
	if err != nil {
		return HandoffLifecycleResult{}, err
	}
	if _, err := tx.createWithID(
		ctx, communicationCommandKind, ids.Receipt, receiptRecord,
	); err != nil {
		return HandoffLifecycleResult{}, err
	}
	return result, nil
}

func handoffLifecycleAuditActor(actor CommunicationActorRef) (string, string) {
	kind := string(actor.Kind)
	if actor.Kind == ActorSession {
		kind = model.ActorAgent
	}
	return string(actor.Kind) + ":" + actor.Ref, kind
}
