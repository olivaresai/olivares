// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// commandReceiptFence names the transaction lock key under which the receipt
// set of one command identity is closed for the rest of the surrounding
// Mutate.
//
// A CommunicationCommandReceipt is append-only evidence: the application role
// holds SELECT and INSERT on its table and boot refuses to serve if it also
// holds UPDATE, DELETE or TRUNCATE. PostgreSQL's SELECT ... FOR UPDATE requires
// the UPDATE privilege, so the row-update lock the idempotent replay paths used
// to take was not something the application role may take, and it fenced
// nothing: an immutable row has no update to wait for. What a replay needs is
// that the receipt set of its identity cannot GROW between the lookup and the
// decision to execute or replay. That is what the command's own transaction
// key proves:
//
//   - the receipt set of an identity is bounded to one row by the unique
//     index (tenant, actor_fingerprint, command_scope, idempotency_key_hash),
//     and the confined scope pins the workspace as well;
//   - every writer that inserts a receipt of a given command scope first
//     acquires, in the same transaction, the transaction-scoped advisory key
//     derived from the identity by that scope's key function, and inserts the
//     receipt before releasing it at commit. The table below is closed over
//     exactly those scopes and calls the writer's own key function, so the
//     reader and the writer cannot derive different keys for one identity;
//   - a transaction holding that key therefore sees either the committed
//     receipt or a set that cannot grow until it commits itself. Two of the
//     keys are coarser than the identity (the derived key omits the actor,
//     the successor key fixes the scope); a coarser key still closes the
//     subset the identity names.
//
// The fence is proved against the transaction's own lock witness
// (communicationTx.lockedTransactionKey), never against the key string a
// caller could pass, so a key that was only named — or one that a different
// transaction holds — is never a fence.
type commandReceiptFence struct {
	scope              DirectoryScopeRef
	commandScope       string
	actorFingerprint   []byte
	idempotencyKeyHash []byte
	key                string
}

// newCommandReceiptFence derives the fence for one command identity. The key
// is computed here from the identity by the closed scope table; it is never
// supplied.
func newCommandReceiptFence(
	scope DirectoryScopeRef,
	commandScope string,
	actorFingerprint []byte,
	idempotencyKeyHash []byte,
) (commandReceiptFence, error) {
	if !validCanonicalCommunicationID(scope.WorkspaceID) || scope.TenantID == "" ||
		commandScope == "" || len(actorFingerprint) != sha256.Size ||
		len(idempotencyKeyHash) != sha256.Size {
		return commandReceiptFence{}, communicationTransactionUnavailable(
			"command receipt fence identity is malformed", nil,
		)
	}
	key, err := commandReceiptFenceKey(scope, commandScope, actorFingerprint, idempotencyKeyHash)
	if err != nil {
		return commandReceiptFence{}, err
	}
	return commandReceiptFence{
		scope:              scope,
		commandScope:       commandScope,
		actorFingerprint:   append([]byte(nil), actorFingerprint...),
		idempotencyKeyHash: append([]byte(nil), idempotencyKeyHash...),
		key:                key,
	}, nil
}

// commandReceiptFenceKey is the closed table of which transaction key the
// writer of a receipt of commandScope acquires before inserting it. Each arm
// calls the writer's own key function. A scope outside the table has no
// proven fence and is refused rather than read.
func commandReceiptFenceKey(
	scope DirectoryScopeRef,
	commandScope string,
	actorFingerprint []byte,
	idempotencyKeyHash []byte,
) (string, error) {
	switch commandScope {
	case messageLifecycleRetractScope, messageLifecycleExpireScope, messageLifecycleOverdueScope,
		workflowWorkTaskCommandScope, workflowHandoffCarrierScope,
		protocolReplyCommandScope, protocolInboundCommandScope:
		// applyLifecycle, applyOverdue and applyWorkflowCommunicationMessage.
		return messageLifecycleLockKey(scope, commandScope, actorFingerprint, idempotencyKeyHash), nil
	case messageDerivedRerouteScope, messageDerivedEscalateScope:
		// applyDerived.
		return messageDerivedReceiptLockKey(scope, commandScope, idempotencyKeyHash), nil
	case deliveryDispatchSuccessorScope:
		// deliveryDispatchSuccessorService.apply.
		return deliveryDispatchSuccessorReceiptLockKey(scope, actorFingerprint, idempotencyKeyHash), nil
	}
	return "", communicationTransactionUnavailable(
		fmt.Sprintf("command receipt observation of scope %q has no proven transaction fence", commandScope),
		nil,
	)
}

// prove checks that this fence closes the receipt set for tx: the repository
// is the append-only command receipt, and this transaction's own witness
// holds the fence's key.
func (fence commandReceiptFence) prove(tx *communicationTx, repo communicationRepository) error {
	if tx == nil || repo == nil {
		return communicationTransactionUnavailable("command receipt observation", nil)
	}
	descriptor := repo.Descriptor()
	if descriptor.Kind != communicationCommandKind || !descriptor.AppendOnly {
		return communicationTransactionUnavailable(
			fmt.Sprintf("command receipt observation of %q", descriptor.Kind), nil,
		)
	}
	if fence.key == "" || !tx.lockedTransactionKey(fence.key) {
		return communicationTransactionUnavailable(
			fmt.Sprintf("command receipt of scope %q requires this transaction's lock on its command key",
				fence.commandScope), nil,
		)
	}
	return nil
}

// observeCommandReceipt is the point read of one command identity's receipt
// behind its proven fence. It lists at most two rows under the identity's
// exact filters (bounded to one by the unique index), refuses more than one,
// decodes the row and requires its identity and workspace to be exactly the
// fence's. It never calls Lock on the receipt row: behind the fence the row
// is the current, immutable state.
func observeCommandReceipt(
	ctx context.Context,
	tx *communicationTx,
	fence commandReceiptFence,
) (CommunicationCommandReceipt, bool, error) {
	repo, err := tx.repo(communicationCommandKind)
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	if err := fence.prove(tx, repo); err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colCommCommandScope, Op: model.OpEq, Value: fence.commandScope},
			{Column: colCommActorFingerprint, Op: model.OpEq, Value: fence.actorFingerprint},
			{Column: colCommIdempotencyKeyHash, Op: model.OpEq, Value: fence.idempotencyKeyHash},
		},
		Limit: 2,
	})
	if err != nil {
		return CommunicationCommandReceipt{}, false, err
	}
	if page.HasMore || len(rows) > 1 {
		return CommunicationCommandReceipt{}, false, directNoticeReadUnknown(
			"command receipt set exceeds its unique identity", nil,
		)
	}
	if len(rows) == 0 {
		return CommunicationCommandReceipt{}, false, nil
	}
	receipt, err := communicationCommandReceiptFromRecord(rows[0])
	if err != nil {
		return CommunicationCommandReceipt{}, false, directNoticeReadUnknown(
			"command receipt is malformed", err,
		)
	}
	if !validCanonicalCommunicationID(receipt.ID) ||
		receipt.TenantID != fence.scope.TenantID || receipt.WorkspaceID != fence.scope.WorkspaceID ||
		receipt.CommandScope != fence.commandScope ||
		!bytes.Equal(receipt.ActorFingerprint, fence.actorFingerprint) ||
		!bytes.Equal(receipt.IdempotencyKeyHash, fence.idempotencyKeyHash) {
		return CommunicationCommandReceipt{}, false, directNoticeReadUnknown(
			"command receipt left its fenced identity", nil,
		)
	}
	return receipt, true, nil
}

// messageOverdueOrigin is the immutable pair an overdue escalation reads: the
// receipt of the overdue command that produced the origin Event, and that
// Event itself.
type messageOverdueOrigin struct {
	receipt CommunicationCommandReceipt
	event   model.Record
}

// observeMessageOverdueOrigin reads the overdue receipt and the work Event
// named by originEventID behind the source Message this transaction has
// locked. Neither row is under the escalation command's own transaction key —
// they were written by a different command — so that key is not claimed as
// their fence. What closes them is stated and checked here:
//
//   - immutability: both descriptors are append-only, so an existing row
//     cannot change;
//   - owner: the overdue writer holds the source Message's row-update lock
//     when it appends its Event and its receipt, in one transaction, and both
//     name that Message (receipt ResultID, Event payload message_id). A
//     transaction holding that same row lock therefore observes the closed set
//     of overdue receipts and Events of the Message: a writer either committed
//     before the lock or is serialized behind it. The lock is proved against
//     this transaction's own witness, never a caller argument;
//   - point identity: the work Event is bounded to one row by the unique
//     (tenant, event_id) index; the receipt is bounded to one row because the
//     overdue writer appends its Event with the same event_id in the same
//     transaction, so a second overdue receipt naming an already-used event_id
//     cannot commit.
//
// The exact receipt and Event relations the escalation requires are checked
// by the caller on the returned values; this function proves the fence and
// binds both rows to the locked owner's identity and workspace.
func observeMessageOverdueOrigin(
	ctx context.Context,
	tx *communicationTx,
	messageID model.ID,
	originEventID model.ID,
) (messageOverdueOrigin, error) {
	if tx == nil || !validCanonicalCommunicationID(messageID) ||
		!validCanonicalCommunicationID(originEventID) {
		return messageOverdueOrigin{}, communicationTransactionUnavailable(
			"overdue escalation origin observation", nil,
		)
	}
	receipts, err := tx.repo(communicationCommandKind)
	if err != nil {
		return messageOverdueOrigin{}, err
	}
	events, err := tx.repo(workEventKind)
	if err != nil {
		return messageOverdueOrigin{}, err
	}
	if !receipts.Descriptor().AppendOnly || !events.Descriptor().AppendOnly {
		return messageOverdueOrigin{}, communicationTransactionUnavailable(
			"overdue escalation origin observation of a mutable descriptor; lock it instead", nil,
		)
	}
	owner, locked := tx.lockedRow(messageKind, messageID)
	if !locked {
		return messageOverdueOrigin{}, communicationTransactionUnavailable(
			"overdue escalation origin requires this transaction's row lock on its source Message", nil,
		)
	}
	ownerID, err := directNoticeRecordID(owner, model.ColID)
	if err != nil || ownerID != messageID {
		return messageOverdueOrigin{}, directNoticeReadUnknown(
			"locked overdue escalation source has a different identity", err,
		)
	}
	ownerWorkspace := owner.String(colWorkWorkspaceID)
	if ownerWorkspace == "" {
		return messageOverdueOrigin{}, directNoticeReadUnknown(
			"locked overdue escalation source has no workspace", nil,
		)
	}

	rows, page, err := receipts.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colCommCommandScope, Op: model.OpEq, Value: messageLifecycleOverdueScope},
			{Column: colEventID, Op: model.OpEq, Value: originEventID.String()},
		},
		Limit: 2,
	})
	if err != nil {
		return messageOverdueOrigin{}, err
	}
	if page.HasMore || len(rows) != 1 {
		return messageOverdueOrigin{}, communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation origin receipt is unavailable",
		)
	}
	receipt, err := communicationCommandReceiptFromRecord(rows[0])
	if err != nil || !validCanonicalCommunicationID(receipt.ID) {
		return messageOverdueOrigin{}, communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation receipt identity is malformed",
		)
	}
	if receipt.WorkspaceID.String() != ownerWorkspace || receipt.ResultKind != string(messageKind) ||
		receipt.ResultID != messageID || receipt.EventID != originEventID ||
		receipt.CommandScope != messageLifecycleOverdueScope {
		return messageOverdueOrigin{}, communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation receipt crosses Message lineage",
		)
	}

	eventRows, eventPage, err := events.List(ctx, model.Query{
		Filters: []model.Filter{{
			Column: colEventID, Op: model.OpEq, Value: originEventID.String(),
		}},
		Limit: 2,
	})
	if err != nil {
		return messageOverdueOrigin{}, err
	}
	if eventPage.HasMore || len(eventRows) != 1 {
		return messageOverdueOrigin{}, communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation origin Event is unavailable",
		)
	}
	event := eventRows[0]
	if _, err := directNoticeRecordID(event, model.ColID); err != nil {
		return messageOverdueOrigin{}, communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation Event identity is malformed",
		)
	}
	if event.String(colWorkWorkspaceID) != ownerWorkspace ||
		event.String(colEventID) != originEventID.String() {
		return messageOverdueOrigin{}, communicationError(
			ErrCommunicationEvidenceUnknown, "overdue escalation Event crosses Message lineage",
		)
	}
	return messageOverdueOrigin{receipt: receipt, event: event}, nil
}
