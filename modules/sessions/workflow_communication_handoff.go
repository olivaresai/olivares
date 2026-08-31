// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"sync/atomic"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// OfferWorkflowHandoff publishes the exact MessageHandoffOffer carrier and
// materializes the existing HandoffOfferCommand. The public K3 readiness gate
// remains untouched and OFF; this internal boundary consumes the workflow's
// persisted initiator and current C5 evidence directly.
func (m *Module) OfferWorkflowHandoff(
	ctx context.Context,
	tenant model.TenantID,
	cmd WorkflowHandoffCommand,
) (WorkflowHandoffResult, error) {
	ctx, cancel := workflowCommunicationContext(ctx)
	defer cancel()
	if cmd.ExpectedOwnerEpoch < 0 || cmd.AckDeadline.IsZero() {
		return WorkflowHandoffResult{}, workflowCommunicationError(
			"offer handoff",
			communicationError(ErrInvalidCommunicationModel, "workflow Handoff precondition is invalid"),
		)
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
	deadline := cmd.AckDeadline.UTC()
	carrier, err := m.prepareWorkflowCommunicationPublish(
		ctx, tenant, cmd.Actor, cmd.WorkItemID, cmd.ChannelID, cmd.Target,
		carrierContent, UrgencyHigh, &deadline, cmd.IdempotencyKey,
		MessageHandoffOffer, workflowHandoffCarrierScope,
	)
	if err != nil {
		return WorkflowHandoffResult{}, workflowCommunicationError("offer handoff carrier", err)
	}
	carrierResult, err := m.applyWorkflowCommunicationMessage(
		ctx, carrier, workflowHandoffCarrierAudit, workflowHandoffCarrierEvent,
		cmd.ExpectedOwnerEpoch,
	)
	if err != nil {
		return WorkflowHandoffResult{}, workflowCommunicationError("offer handoff carrier", err)
	}
	if replay, found, err := m.lookupWorkflowHandoffReplay(
		ctx, carrier, cmd, carrierResult,
	); err != nil {
		return WorkflowHandoffResult{}, workflowCommunicationError("replay handoff", err)
	} else if found {
		return replay, nil
	}
	workVersion, ownerEpoch, err := m.workflowHandoffWorkVersion(
		ctx, carrier.direct.Scope, cmd.WorkItemID,
	)
	if err != nil {
		return WorkflowHandoffResult{}, workflowCommunicationError("read handoff work", err)
	}
	if cmd.ExpectedOwnerEpoch > 0 && ownerEpoch != cmd.ExpectedOwnerEpoch {
		return WorkflowHandoffResult{}, workflowCommunicationError("offer handoff", store.ErrConflict)
	}
	offerKey, err := workflowCommunicationStableID(cmd.IdempotencyKey, "workflow.handoff.offer")
	if err != nil {
		return WorkflowHandoffResult{}, workflowCommunicationError("offer handoff", err)
	}
	exact := HandoffOfferCommand{
		ChannelID: cmd.ChannelID, WorkItemID: cmd.WorkItemID,
		MessageID: carrierResult.MessageID, DeliveryID: carrierResult.DeliveryID,
		Content: cmd.Content, IfMatch: fmt.Sprintf("\"v%d\"", workVersion),
		IdempotencyKey: offerKey.String(),
	}
	normalized, err := normalizeHandoffOfferCommand(
		carrier.direct.Scope, carrier.direct.Principal, exact,
	)
	if err != nil {
		return WorkflowHandoffResult{}, workflowCommunicationError("normalize handoff", err)
	}
	ids := newHandoffOfferIDs()
	prepared, err := m.prepareHandoffOfferPayload(ctx, normalized, ids.Handoff)
	if err != nil {
		return WorkflowHandoffResult{}, workflowCommunicationError("prepare handoff", err)
	}
	result, err := m.applyWorkflowHandoffAggregate(
		ctx, carrier, normalized, ids, prepared, cmd.ExpectedOwnerEpoch,
	)
	if err != nil {
		return WorkflowHandoffResult{}, workflowCommunicationError("offer handoff", err)
	}
	return result, nil
}

func (m *Module) mutateWorkflowHandoff(
	ctx context.Context,
	scope DirectoryScopeRef,
	authority messageLifecycleAuthority,
	fn func(*communicationTx, handoffWorkRepositories) error,
) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if fn == nil {
		return communicationTransactionUnavailable("workflow Handoff mutation callback", nil)
	}
	binding := &communicationRequestAuthorityBindingID{marker: 1}
	request := communicationRequestAuthoritySnapshot{
		facts:      append([]store.AuthorizationFactRef(nil), authority.Facts...),
		observedAt: authority.ObservedAt.UTC(), freshUntil: authority.FreshUntil.UTC(),
		bindingID: binding,
	}
	if err := request.validate(); err != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Handoff authority snapshot is malformed",
		)
	}
	var callbackAttempted atomic.Bool
	return m.communicationData(scope.TenantID).Mutate(ctx, func(sc store.Scope) error {
		if !callbackAttempted.CompareAndSwap(false, true) {
			return communicationTransactionUnavailable(
				"workflow Handoff mutation callback was already entered", nil,
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

func (m *Module) applyWorkflowHandoffAggregate(
	ctx context.Context,
	carrier workflowCommunicationPreflight,
	normalized handoffOfferNormalized,
	ids handoffOfferIDs,
	prepared handoffOfferPrepared,
	expectedOwnerEpoch int64,
) (WorkflowHandoffResult, error) {
	reader, err := directNoticeReaderPreflightWithCore(
		carrier.identity, carrier.direct.CoreWitness,
	)
	if err != nil {
		return WorkflowHandoffResult{}, err
	}
	authority, facts, err := workflowCommunicationAuthority(carrier)
	if err != nil {
		return WorkflowHandoffResult{}, err
	}
	if !equalDirectNoticeAuthorityFacts(facts, reader.Facts) ||
		normalized.actor != carrier.direct.Sender {
		return WorkflowHandoffResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Handoff authority crossed carrier lineage",
		)
	}
	var exact HandoffOfferResult
	var ownerEpoch int64
	err = m.mutateWorkflowHandoff(
		ctx, normalized.scope, authority,
		func(tx *communicationTx, repositories handoffWorkRepositories) error {
			if err := tx.lockAuthoritySnapshot(ctx, reader.Facts); err != nil {
				return err
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
			work, err := lockHandoffWorkState(
				ctx, tx, repositories, normalized.scope, normalized.command.WorkItemID, false,
			)
			if err != nil {
				return err
			}
			ownerEpoch = work.item.Int(colWorkOwnerEpoch)
			if expectedOwnerEpoch > 0 && ownerEpoch != expectedOwnerEpoch {
				return store.ErrConflict
			}
			carrierLocked, err := lockHandoffCarrier(
				ctx, tx, normalized.scope, normalized.command.ChannelID,
				normalized.command.MessageID, normalized.command.DeliveryID,
			)
			if err != nil {
				return err
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
					}}, Limit: 2,
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
			if err := evaluateHandoffOfferAuthority(tx, reader, carrierLocked); err != nil {
				return err
			}
			if err := validatePreparedHandoffOffer(prepared, carrierLocked); err != nil {
				return err
			}
			if replay {
				exact, err = handoffOfferResultFromReceipt(receipt)
				return err
			}
			exact, err = applyLockedHandoffOffer(
				ctx, tx, repositories, reader, normalized, ids, prepared, carrierLocked, work,
			)
			return err
		},
	)
	if err != nil {
		return WorkflowHandoffResult{}, err
	}
	eventSeq, persistedOwnerEpoch, err := m.workflowHandoffProjection(
		ctx, normalized.scope, exact.HandoffID, exact.EventID,
	)
	if err != nil {
		return WorkflowHandoffResult{}, err
	}
	if persistedOwnerEpoch != ownerEpoch {
		return WorkflowHandoffResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Handoff owner epoch projection changed",
		)
	}
	return WorkflowHandoffResult{
		WorkItemID: exact.WorkItemID, CommandID: exact.CommandID,
		HandoffID: exact.HandoffID, MessageID: exact.MessageID, DeliveryID: exact.DeliveryID,
		EventID: exact.EventID, EventSeq: eventSeq, Version: exact.Version,
		State: exact.State, OwnerEpoch: ownerEpoch, Replayed: exact.Replayed,
	}, nil
}

func (m *Module) workflowHandoffWorkVersion(
	ctx context.Context,
	scope DirectoryScopeRef,
	workItemID model.ID,
) (int64, int64, error) {
	var version, ownerEpoch int64
	err := m.communicationData(scope.TenantID).View(ctx, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, scope.WorkspaceID)
		if err != nil {
			return err
		}
		repo, err := confined.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err := repo.Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if recordID(item) != workItemID || item.String(colWorkWorkspaceID) != scope.WorkspaceID.String() {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Handoff WorkItem crossed scope",
			)
		}
		version, ownerEpoch = item.Int(model.ColVersion), item.Int(colWorkOwnerEpoch)
		if version < 1 || ownerEpoch < 1 {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Handoff WorkItem version is unavailable",
			)
		}
		return nil
	})
	return version, ownerEpoch, err
}

func (m *Module) workflowHandoffProjection(
	ctx context.Context,
	scope DirectoryScopeRef,
	handoffID, eventID model.ID,
) (int64, int64, error) {
	var eventSeq, ownerEpoch int64
	err := m.communicationData(scope.TenantID).View(ctx, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, scope.WorkspaceID)
		if err != nil {
			return err
		}
		handoffs, err := confined.Ext(handoffKind)
		if err != nil {
			return err
		}
		record, err := handoffs.Get(ctx, handoffID)
		if err != nil {
			return err
		}
		handoff, err := handoffFromRecord(record)
		if err != nil || handoff.ID != handoffID {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Handoff projection is malformed",
			)
		}
		ownerEpoch = handoff.FromOwnerEpoch
		events, err := confined.Ext(workEventKind)
		if err != nil {
			return err
		}
		rows, page, err := events.List(ctx, model.Query{
			Filters: []model.Filter{{Column: colEventID, Op: model.OpEq, Value: eventID.String()}},
			Limit:   2,
		})
		if err != nil || page.HasMore || len(rows) != 1 {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Handoff Event projection is unavailable",
			)
		}
		eventSeq = rows[0].Int(colEventSeq)
		if eventSeq < 1 || len(rows[0].Bytes(colEventAuditHash)) != sha256.Size {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Handoff Event evidence is malformed",
			)
		}
		return nil
	})
	return eventSeq, ownerEpoch, err
}

func (m *Module) lookupWorkflowHandoffReplay(
	ctx context.Context,
	carrier workflowCommunicationPreflight,
	cmd WorkflowHandoffCommand,
	carrierResult WorkflowWorkTaskResult,
) (WorkflowHandoffResult, bool, error) {
	var result WorkflowHandoffResult
	found := false
	err := m.communicationData(carrier.direct.Scope.TenantID).View(ctx, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, carrier.direct.Scope.WorkspaceID)
		if err != nil {
			return err
		}
		repo, err := confined.Ext(handoffKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{{
				Column: colCommMessageID, Op: model.OpEq, Value: carrierResult.MessageID.String(),
			}}, Limit: 2,
		})
		if err != nil {
			return err
		}
		if page.HasMore || len(rows) > 1 {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Handoff replay lookup is ambiguous",
			)
		}
		if len(rows) == 0 {
			return nil
		}
		handoff, err := handoffFromRecord(rows[0])
		if err != nil || handoff.WorkItemID != cmd.WorkItemID ||
			handoff.MessageID != carrierResult.MessageID ||
			handoff.DeliveryID != carrierResult.DeliveryID || handoff.To != cmd.Target ||
			handoff.From != (RecipientRef{
				Kind: RecipientKind(carrier.direct.Sender.Kind), Ref: carrier.direct.Sender.Ref,
			}) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Handoff replay crossed carrier lineage",
			)
		}
		if cmd.ExpectedOwnerEpoch > 0 && handoff.FromOwnerEpoch != cmd.ExpectedOwnerEpoch {
			return store.ErrConflict
		}
		receipts, err := confined.Ext(communicationCommandKind)
		if err != nil {
			return err
		}
		receiptRows, receiptPage, err := receipts.List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: colCommResultKind, Op: model.OpEq, Value: string(handoffKind)},
				{Column: colCommResultID, Op: model.OpEq, Value: handoff.ID.String()},
			}, Limit: 2,
		})
		if err != nil || receiptPage.HasMore || len(receiptRows) != 1 {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Handoff replay receipt is unavailable",
			)
		}
		receipt, err := communicationCommandReceiptFromRecord(receiptRows[0])
		if err != nil || !bytes.Equal(receipt.ActorFingerprint, carrier.direct.ActorFingerprint) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Handoff replay actor is unavailable",
			)
		}
		exact, err := handoffOfferResultFromReceipt(receipt)
		if err != nil {
			return err
		}
		events, err := confined.Ext(workEventKind)
		if err != nil {
			return err
		}
		eventRows, eventPage, err := events.List(ctx, model.Query{
			Filters: []model.Filter{{
				Column: colEventID, Op: model.OpEq, Value: exact.EventID.String(),
			}}, Limit: 2,
		})
		if err != nil || eventPage.HasMore || len(eventRows) != 1 {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Handoff replay Event is unavailable",
			)
		}
		result = WorkflowHandoffResult{
			WorkItemID: exact.WorkItemID, CommandID: exact.CommandID,
			HandoffID: exact.HandoffID, MessageID: exact.MessageID,
			DeliveryID: exact.DeliveryID, EventID: exact.EventID,
			EventSeq: eventRows[0].Int(colEventSeq), Version: exact.Version,
			State: exact.State, OwnerEpoch: handoff.FromOwnerEpoch, Replayed: true,
		}
		found = true
		return nil
	})
	return result, found, err
}
