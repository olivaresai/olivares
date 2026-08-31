// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SendWorkflowWorkTask publishes one actionable, WorkItem-bound Message using
// the workflow initiator captured by the durable run. It intentionally enters
// below the public communication-readiness boundary: every current C5 witness,
// row lock, audit append, event, outbox row and receipt is still mandatory.
func (m *Module) SendWorkflowWorkTask(
	ctx context.Context,
	tenant model.TenantID,
	cmd WorkflowWorkTaskCommand,
) (WorkflowWorkTaskResult, error) {
	ctx, cancel := workflowCommunicationContext(ctx)
	defer cancel()
	preflight, err := m.prepareWorkflowCommunicationPublish(
		ctx, tenant, cmd.Actor, cmd.WorkItemID, cmd.ChannelID, cmd.Recipient,
		cmd.Content, cmd.Urgency, cmd.AckDueAt, cmd.IdempotencyKey,
		MessageWorkTask, workflowWorkTaskCommandScope,
	)
	if err != nil {
		return WorkflowWorkTaskResult{}, workflowCommunicationError("send work task", err)
	}
	result, err := m.applyWorkflowCommunicationMessage(
		ctx, preflight, workflowWorkTaskAuditAction, workflowWorkTaskEventType, 0,
	)
	if err != nil {
		return WorkflowWorkTaskResult{}, workflowCommunicationError("send work task", err)
	}
	return result, nil
}

func materializeWorkflowCommunicationMessage(
	dbNow time.Time,
	preflight workflowCommunicationPreflight,
	locked directNoticeLockedState,
) (MessagePublishPlan, error) {
	required := preflight.ackDueAt != nil
	ackPolicy := AckPolicyNone
	if required {
		if !preflight.ackDueAt.After(dbNow) {
			return MessagePublishPlan{}, communicationError(
				ErrInvalidCommunicationTransition,
				"workflow communication acknowledgement deadline has elapsed",
			)
		}
		ackPolicy = AckPolicyEachRequired
	}
	ids := preflight.direct.IDs
	threadID := ids.Message
	var replyToID model.ID
	var parent *Message
	if preflight.protocolReply != nil {
		threadID = preflight.protocolReply.threadID
		replyToID = preflight.protocolReply.replyToID
		parent = preflight.protocolReply.parent
		if !validCanonicalCommunicationID(threadID) ||
			(!replyToID.IsZero() && !validCanonicalCommunicationID(replyToID)) {
			return MessagePublishPlan{}, communicationError(
				ErrCommunicationEvidenceUnknown, "protocol reply lineage is unavailable",
			)
		}
	}
	entity := CommunicationEntity{
		ID: ids.Message, TenantID: preflight.direct.Scope.TenantID,
		WorkspaceID: preflight.direct.Scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
	}
	draft := Message{
		MutableCommunicationEntity: MutableCommunicationEntity{
			CommunicationEntity: entity, UpdatedAt: dbNow,
		},
		ChannelID: locked.Channel.ID, WorkItemID: preflight.workItemID,
		ThreadID: threadID, ReplyToID: replyToID,
		Kind: preflight.messageKind, State: MessageDraft,
		Sender: preflight.direct.Sender, Payload: cloneProtectedPayload(preflight.direct.Payload),
		Urgency: preflight.direct.Command.Urgency, AckPolicy: ackPolicy,
		AvailableAt: dbNow, AckDueAt: cloneWorkflowCommunicationTime(preflight.ackDueAt),
	}
	resolved := preflight.direct.Snapshot.Contributions[0]
	audience := MessageAudience{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: ids.Audience, TenantID: preflight.direct.Scope.TenantID,
			WorkspaceID: preflight.direct.Scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
		}},
		MessageID: draft.ID, Ordinal: 1, Selector: resolved.Selector,
		ChannelACLRevision:   locked.Channel.ACLRevision,
		RouteRevision:        locked.Channel.RouteRevision,
		SubscriptionRevision: locked.Channel.SubscriptionRevision,
		DirectoryEpoch:       preflight.direct.Snapshot.Epoch,
		DirectorySnapshotAt:  preflight.direct.Snapshot.ObservedAt, ResolvedCount: 1,
	}
	selectorBytes, err := canonicalJSON(resolved.Selector)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	selectorHash := sha256.Sum256(selectorBytes)
	audience.SelectorHash = selectorHash[:]
	contribution := MessageAudienceRecipient{
		AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: ids.Contribution, TenantID: preflight.direct.Scope.TenantID,
			WorkspaceID: preflight.direct.Scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
		}},
		MessageAudienceID: audience.ID, MessageDeliveryID: ids.Delivery,
		Recipient: resolved.Recipient.Recipient, RecipientEpoch: resolved.Recipient.RecipientEpoch,
		Required: resolved.Required, WakePolicy: resolved.WakePolicy,
		RouteReasons: append([]RouteReason(nil), resolved.RouteReasons...), Selector: resolved.Selector,
		DirectoryEpoch:       preflight.direct.Snapshot.Epoch,
		ChannelACLRevision:   locked.Channel.ACLRevision,
		RouteRevision:        locked.Channel.RouteRevision,
		SubscriptionRevision: locked.Channel.SubscriptionRevision,
		CausalKind:           resolved.CausalKind, CausalRef: resolved.CausalRef,
		ObservedSessionSID:     resolved.ObservedSessionSID,
		ObservedClaimFence:     resolved.ObservedClaimFence,
		SubscriptionID:         resolved.SubscriptionID,
		SubscriptionGeneration: resolved.SubscriptionGeneration,
		RouteRuleID:            resolved.RouteRuleID, RouteRuleGeneration: resolved.RouteRuleGeneration,
	}
	if resolved.CausalFact != nil {
		contribution.CausalFactKind = resolved.CausalFact.Kind
		contribution.CausalFactID = resolved.CausalFact.ID
		contribution.CausalFactVersion = resolved.CausalFact.Version
	}
	if resolved.OriginalSubscriber != nil {
		value := *resolved.OriginalSubscriber
		contribution.OriginalSubscriber = &value
	}
	contribution.CausalArcHash, err = CanonicalAudienceCausalArcHash(contribution)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	audience.ResolvedHash, err = canonicalResolvedAudienceHash(
		audience, []MessageAudienceRecipient{contribution},
	)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	fold, err := FoldAudienceContributions([]MessageAudienceRecipient{contribution})
	if err != nil {
		return MessagePublishPlan{}, err
	}
	delivery := MessageDelivery{
		MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: ids.Delivery, TenantID: preflight.direct.Scope.TenantID,
			WorkspaceID: preflight.direct.Scope.WorkspaceID, Version: 1, CreatedAt: dbNow,
		}, UpdatedAt: dbNow},
		MessageID: draft.ID, Recipient: fold.Recipient,
		RecipientEpoch: resolved.Recipient.RecipientEpoch, Required: fold.Required,
		RouteReasons: append([]RouteReason(nil), fold.RouteReasons...), WakePolicy: fold.WakePolicy,
		State: DeliveryAvailable, AvailableAt: dbNow,
		AckDueAt: cloneWorkflowCommunicationTime(preflight.ackDueAt),
	}
	directoryFact, err := DirectorySnapshotAuthorityFact(preflight.direct.Snapshot)
	if err != nil {
		return MessagePublishPlan{}, err
	}
	labels := ChannelLabelSnapshot{
		Scope: preflight.direct.Scope, ChannelID: locked.Channel.ID,
		RouteRevision: locked.Channel.RouteRevision, ObservedAt: dbNow,
		Definitions: append([]ChannelLabelDefinition(nil), locked.Labels...), SameTransaction: true,
		Evidence: AuthorityEvidence{
			Verdict: VerdictClean, Code: "channel_labels_locked",
			EvidenceRef: "same_tx_channel_labels",
		},
	}
	return PlanMessagePublish(MessagePublishInput{
		Draft: draft, Channel: locked.Channel,
		AudienceRequest:     preflight.direct.AudienceRequest,
		AudienceAttestation: preflight.direct.AudienceAttestation,
		Snapshot:            preflight.direct.Snapshot,
		Audiences:           []MessageAudience{audience},
		Contributions:       []MessageAudienceRecipient{contribution},
		Deliveries:          []MessageDelivery{delivery}, Labels: labels,
		SendGate: SendGateEvidence{
			Scope: preflight.direct.Scope, ChannelID: locked.Channel.ID,
			ChannelACLRevision: locked.Channel.ACLRevision, DBNow: dbNow,
			Principal: preflight.direct.Principal, Core: preflight.direct.CoreWitness,
			DirectoryEpoch: directoryFact, CurrentChannelWriteGrant: locked.WriteEvidence,
		},
		Principal: preflight.direct.Principal, Sender: preflight.direct.Sender,
		SourceKind: preflight.sourceKind, EventType: preflight.sourceEvent, Parent: parent,
		DeliverySequenceGuard: locked.DeliveryGuard, DBNow: dbNow,
	})
}

func cloneWorkflowCommunicationTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func lockWorkflowCommunicationWorkItem(
	ctx context.Context,
	tx *communicationTx,
	sc store.Scope,
	preflight workflowCommunicationPreflight,
) (model.Record, store.TransactionStampedGenericRepo, error) {
	repo, err := lifecycleWorkItemRepository(sc)
	if err != nil {
		return nil, nil, err
	}
	locker, ok := repo.(store.RowLocker[model.Record])
	if !ok {
		return nil, nil, communicationTransactionUnavailable(
			"workflow communication WorkItem row lock", nil,
		)
	}
	var item model.Record
	err = runCommunicationBoundAuthorityLocalLock(tx.boundAuthorityState, func() error {
		var lockErr error
		item, lockErr = locker.Lock(ctx, preflight.workItemID)
		return lockErr
	})
	if err != nil {
		return nil, nil, err
	}
	if recordID(item) != preflight.workItemID ||
		item.String(colWorkWorkspaceID) != preflight.direct.Scope.WorkspaceID.String() ||
		item.Int(model.ColVersion) < 1 || item.Int(colWorkLastEventSeq) < 1 ||
		item.Int(colWorkLastEventSeq) == math.MaxInt64 {
		return nil, nil, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow communication WorkItem changed lineage",
		)
	}
	return item, repo, nil
}

func workflowCommunicationResultFromReceipt(
	receipt CommunicationCommandReceipt,
	preflight workflowCommunicationPreflight,
) (WorkflowWorkTaskResult, error) {
	projection := receipt.ResponseProjectionJSON
	if receipt.ResultKind != string(messageKind) || receipt.ResultID != projection.IDs["message_id"] ||
		projection.IDs["work_item_id"] != preflight.workItemID ||
		projection.IDs["delivery_id"].IsZero() || projection.IDs["event_id"] != receipt.EventID ||
		projection.Version < 1 || MessageState(projection.State) != MessagePublished ||
		receipt.AuditSeq < 1 || len(receipt.AuditHash) != sha256.Size {
		return WorkflowWorkTaskResult{}, communicationError(
			ErrCommunicationEvidenceUnknown, "workflow Message receipt projection is unavailable",
		)
	}
	threadID := projection.IDs["thread_id"]
	if threadID.IsZero() {
		threadID = receipt.ResultID
	}
	return WorkflowWorkTaskResult{
		WorkItemID: preflight.workItemID, CommandID: receipt.CommandID,
		MessageID: receipt.ResultID, DeliveryID: projection.IDs["delivery_id"],
		ThreadID: threadID, ReplyToID: projection.IDs["reply_to_id"],
		EventID: receipt.EventID,
		Version: projection.Version, State: MessageState(projection.State), Replayed: true,
	}, nil
}

func (m *Module) applyWorkflowCommunicationMessage(
	ctx context.Context,
	preflight workflowCommunicationPreflight,
	auditAction string,
	eventType string,
	expectedOwnerEpoch int64,
) (WorkflowWorkTaskResult, error) {
	if !validateOpaqueRef(eventType) || expectedOwnerEpoch < 0 {
		return WorkflowWorkTaskResult{}, communicationError(
			ErrInvalidCommunicationModel, "workflow Message event or owner epoch is invalid",
		)
	}
	authority, facts, err := workflowCommunicationAuthority(preflight)
	if err != nil {
		return WorkflowWorkTaskResult{}, err
	}
	var result WorkflowWorkTaskResult
	var replayReceipt CommunicationCommandReceipt
	err = m.mutateMessageLifecycle(
		ctx, preflight.direct.Scope, authority,
		func(tx *communicationTx, sc store.Scope) error {
			if err := tx.lockAuthoritySnapshot(ctx, facts); err != nil {
				return err
			}
			if err := tx.lockTransaction(ctx, messageLifecycleLockKey(
				preflight.direct.Scope, preflight.commandScope,
				preflight.direct.ActorFingerprint, preflight.direct.IdempotencyHash,
			)); err != nil {
				return err
			}
			receipt, found, err := lookupMessageLifecycleReceipt(
				ctx, tx, preflight.direct.Scope, preflight.commandScope,
				preflight.direct.ActorFingerprint, preflight.direct.IdempotencyHash,
				preflight.direct.RequestDigest,
			)
			if err != nil {
				return err
			}
			if found {
				replayReceipt = receipt
				if err := tx.refreshNow(ctx); err != nil {
					return err
				}
				result, err = workflowCommunicationResultFromReceipt(receipt, preflight)
				return err
			}
			if preflight.protocolReply != nil {
				if err := lockWorkflowProtocolReplyLineage(ctx, tx, sc, &preflight); err != nil {
					return err
				}
			}
			workItem, workRepo, err := lockWorkflowCommunicationWorkItem(ctx, tx, sc, preflight)
			if err != nil {
				return err
			}
			if expectedOwnerEpoch > 0 && workItem.Int(colWorkOwnerEpoch) != expectedOwnerEpoch {
				return store.ErrConflict
			}
			locked, err := lockDirectNoticePublishState(ctx, tx, preflight.direct)
			if err != nil {
				return err
			}
			plan, err := materializeWorkflowCommunicationMessage(tx.now.Time(), preflight, locked)
			if err != nil {
				return err
			}
			if len(plan.RequiredClaims) != 0 || plan.GuardAdvance == nil ||
				len(plan.Audiences) != 1 || len(plan.Contributions) != 1 || len(plan.Deliveries) != 1 ||
				plan.After.ID != preflight.direct.IDs.Message || plan.After.WorkItemID != preflight.workItemID ||
				plan.After.Kind != preflight.messageKind || plan.After.State != MessagePublished ||
				plan.After.LastEventSeq != 0 || !equalDirectNoticeAuthorityFacts(facts, plan.Facts) {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"workflow Message planner returned a non-atomic shape",
				)
			}
			fulfillment, err := projectInitialDirectNoticeFulfillment(plan, tx.now.Time())
			if err != nil {
				return err
			}
			planBytes, err := canonicalJSON(struct {
				SchemaVersion int64                        `json:"schema_version"`
				CommandScope  string                       `json:"command_scope"`
				Request       []byte                       `json:"request"`
				Facts         []store.AuthorizationFactRef `json:"facts"`
				Plan          MessagePublishPlan           `json:"plan"`
			}{1, preflight.commandScope, preflight.request, facts, plan})
			if err != nil {
				return err
			}
			planHash := sha256.Sum256(planBytes)
			auditActor, auditKind := messageLifecycleAuditActor(preflight.direct.Sender)
			audit, err := tx.appendAudit(ctx, model.AuditDraft{
				Actor: auditActor, ActorKind: auditKind, Action: auditAction,
				TargetKind: messageKind, TargetID: plan.After.ID, PayloadHash: planHash[:],
				Meta: map[string]any{
					"workspace_id":  preflight.direct.Scope.WorkspaceID.String(),
					"work_item_id":  preflight.workItemID.String(),
					"channel_id":    preflight.direct.Channel.ID.String(),
					"command_scope": preflight.commandScope,
				},
			})
			if err != nil {
				return err
			}
			if audit.Seq < 1 || len(audit.Hash) != sha256.Size {
				return communicationError(
					ErrCommunicationEvidenceUnknown, "workflow Message audit anchor is unavailable",
				)
			}
			if err := persistMessageDerivedPlan(ctx, tx, plan); err != nil {
				return err
			}
			itemAfter := cloneMessageLifecycleRecord(workItem)
			itemAfter[colWorkLastEventSeq] = workItem.Int(colWorkLastEventSeq) + 1
			itemAfter, err = runCommunicationBoundAuthorityEffect(
				tx.boundAuthorityState,
				func() (model.Record, error) { return workRepo.UpdateAtTransactionTime(ctx, itemAfter) },
			)
			if err != nil {
				return err
			}
			eventSeq := itemAfter.Int(colWorkLastEventSeq)
			eventPayload, err := canonicalJSON(struct {
				SchemaVersion int64        `json:"schema_version"`
				Command       string       `json:"command"`
				WorkItemID    model.ID     `json:"work_item_id"`
				MessageID     model.ID     `json:"message_id"`
				DeliveryID    model.ID     `json:"delivery_id"`
				MessageKind   MessageKind  `json:"message_kind"`
				State         MessageState `json:"state"`
				Version       int64        `json:"version"`
				EventSeq      int64        `json:"event_seq"`
				PlanHash      string       `json:"plan_hash"`
			}{
				1, preflight.commandScope, preflight.workItemID, plan.After.ID,
				plan.Deliveries[0].ID, plan.After.Kind, plan.After.State,
				plan.After.Version, eventSeq, hex.EncodeToString(planHash[:]),
			})
			if err != nil || len(eventPayload) > 16*1024 {
				return communicationError(
					ErrInvalidCommunicationModel, "workflow Message Event payload is invalid",
				)
			}
			ids := preflight.direct.IDs
			if _, err = tx.create(ctx, workEventKind, model.Record{
				colWorkWorkspaceID: preflight.direct.Scope.WorkspaceID.String(),
				colEventID:         ids.Event.String(), colEventAggregateKind: string(workItemKind),
				colEventAggregateID: preflight.workItemID.String(), colEventSeq: eventSeq,
				colEventType:       eventType,
				colEventActorKind:  string(preflight.direct.Sender.Kind),
				colEventActorRef:   preflight.direct.Sender.Ref,
				colEventOccurredAt: tx.now.String(), colEventPayload: string(eventPayload),
				colEventPayloadHash: hashBytes(eventPayload), colEventCommandID: ids.Command.String(),
				colEventAuditSeq: audit.Seq, colEventAuditHash: append([]byte(nil), audit.Hash...),
			}); err != nil {
				return err
			}
			if _, err = tx.create(ctx, workOutboxKind, model.Record{
				colWorkWorkspaceID: preflight.direct.Scope.WorkspaceID.String(),
				colOutboxEventID:   ids.Event.String(), colOutboxState: "pending",
				colOutboxAttempts: int64(0), colOutboxNextAttemptAt: tx.now.String(),
				colOutboxClaimOwner: nil, colOutboxClaimUntil: nil,
				colOutboxPublishedAt: nil, colOutboxLastOutcome: nil,
			}); err != nil {
				return err
			}
			projection := CommunicationCommandResponseProjection{
				IDs: map[string]model.ID{
					"work_item_id": preflight.workItemID, "channel_id": plan.After.ChannelID,
					"message_id": plan.After.ID, "delivery_id": plan.Deliveries[0].ID,
					"event_id": ids.Event,
				},
				Version: plan.After.Version, State: string(plan.After.State),
				Counts: map[string]int64{
					"delivery_count": 1,
					"required":       fulfillment.Required, "acknowledged": fulfillment.Acknowledged,
					"viable": fulfillment.Viable, "unmet": fulfillment.Unmet,
					"quorum": fulfillment.Quorum,
				},
				Digests: map[string][]byte{
					"request":  append([]byte(nil), preflight.direct.RequestDigest...),
					"plan":     append([]byte(nil), planHash[:]...),
					"audience": append([]byte(nil), plan.After.AudienceHash...),
					"payload":  append([]byte(nil), plan.After.Payload.Digest...),
				},
			}
			receipt, err = newMessageLifecycleReceipt(
				preflight.direct.Scope, preflight.direct.ActorFingerprint,
				preflight.direct.IdempotencyHash, preflight.direct.RequestDigest,
				preflight.commandScope, messageLifecycleIDs{
					Command: ids.Command, Event: ids.Event, Receipt: ids.Receipt,
					DecisionResponse: model.NewID(),
				}, planHash[:], audit, projection, plan.After.ID, tx.now.Time(),
			)
			if err != nil {
				return err
			}
			if err := persistMessageLifecycleReceipt(ctx, tx, receipt); err != nil {
				return err
			}
			result = WorkflowWorkTaskResult{
				WorkItemID: preflight.workItemID, CommandID: ids.Command,
				MessageID: plan.After.ID, DeliveryID: plan.Deliveries[0].ID,
				ThreadID: plan.After.ThreadID, ReplyToID: plan.After.ReplyToID,
				EventID: ids.Event, EventSeq: eventSeq,
				Version: plan.After.Version, State: plan.After.State,
			}
			return nil
		},
	)
	if err == nil && result.Replayed {
		result.EventSeq, err = m.workflowCommunicationReceiptEventSeq(
			ctx, preflight.direct.Scope, preflight.workItemID, replayReceipt,
		)
	}
	return result, err
}

func (m *Module) workflowCommunicationReceiptEventSeq(
	ctx context.Context,
	scope DirectoryScopeRef,
	workItemID model.ID,
	receipt CommunicationCommandReceipt,
) (int64, error) {
	var sequence int64
	err := m.communicationData(scope.TenantID).View(ctx, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, scope.WorkspaceID)
		if err != nil {
			return err
		}
		repo, err := confined.Ext(workEventKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{{Column: colEventID, Op: model.OpEq, Value: receipt.EventID.String()}},
			Limit:   2,
		})
		if err != nil || page.HasMore || len(rows) != 1 ||
			rows[0].String(colEventAggregateKind) != string(workItemKind) ||
			rows[0].String(colEventAggregateID) != workItemID.String() ||
			rows[0].Int(colEventSeq) < 1 || rows[0].Int(colEventAuditSeq) != receipt.AuditSeq ||
			!equalMessageLifecycleBytes(rows[0].Bytes(colEventAuditHash), receipt.AuditHash) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workflow Message receipt Event is unavailable",
			)
		}
		sequence = rows[0].Int(colEventSeq)
		return nil
	})
	return sequence, err
}
