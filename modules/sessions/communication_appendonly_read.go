// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/model"
)

// appendOnlySetFence names the locked mutable parent row that closes an
// append-only child set for the rest of the surrounding Mutate.
//
// K3 stores MessageAudience, MessageAudienceRecipient and MessageAck as
// append-only evidence: the application role holds SELECT and INSERT on their
// tables, and boot refuses to serve if it also holds UPDATE, DELETE or
// TRUNCATE. PostgreSQL's SELECT ... FOR UPDATE requires the UPDATE privilege,
// so a row-update lock on those rows is not something the application role may
// take, and it would fence nothing: an immutable row has no update to wait
// for. What an authorization graph needs from a child set is that it cannot
// GROW between enumeration and evaluation. That is what the parent fence
// proves, and each append-only kind has exactly one:
//
//   - MessageAudience and MessageAudienceRecipient rows can only be inserted
//     while their Message is still draft. Both engines enforce that in the
//     insert validators, and both publication paths append the graph before the
//     draft->published update of the same transaction. A Message never returns
//     to draft. So a Message row locked by this transaction and observed
//     outside draft is a closed audience graph: the publication fence.
//   - MessageAck rows are appended only by the two Ack effect paths, and both
//     lock every Delivery of the Message first; the insert validators bind the
//     Ack to that Delivery (acknowledged with ack_id, or late on a terminal
//     Delivery) and the unique (tenant, delivery) index bounds the set to one.
//     So a Delivery row locked by this transaction is a closed Ack set. This
//     fence is deliberately not publication: an Ack is live evidence that
//     arrives long after the Message was published, so the Message lock alone
//     would prove nothing about it.
//
// The fence is proved against the transaction's own lock witness
// (communicationTx.lockedRow), never against a caller-supplied record, so a
// row that was merely read cannot be presented as a fence.
type appendOnlySetFence struct {
	parentKind model.Kind
	parentID   model.ID
}

// publishedMessageSetFence closes the audience graph of a Message this
// transaction has locked and observed outside draft.
func publishedMessageSetFence(messageID model.ID) appendOnlySetFence {
	return appendOnlySetFence{parentKind: messageKind, parentID: messageID}
}

// lockedDeliverySetFence closes the Ack set of a Delivery this transaction has
// locked.
func lockedDeliverySetFence(deliveryID model.ID) appendOnlySetFence {
	return appendOnlySetFence{parentKind: messageDeliveryKind, parentID: deliveryID}
}

// appendOnlySetFenceParentKind is the closed table of which locked parent
// closes which append-only child set. Any kind outside it has no proven fence
// and is refused rather than read.
func appendOnlySetFenceParentKind(kind model.Kind) (model.Kind, error) {
	switch kind {
	case messageAudienceKind, messageAudienceRecipientKind:
		return messageKind, nil
	case messageAckKind:
		return messageDeliveryKind, nil
	}
	return "", communicationTransactionUnavailable(
		fmt.Sprintf("append-only set observation of %q has no proven parent fence", kind), nil,
	)
}

// prove checks that this fence closes kind for tx: the descriptor is
// append-only, the fence names the parent kind this child requires, this
// transaction holds the parent's row lock, and (for a Message parent) the
// locked row has left draft. It returns the locked parent row so the caller
// can validate the children against it.
func (fence appendOnlySetFence) prove(
	tx *communicationTx,
	kind model.Kind,
	repo communicationRepository,
) (model.Record, error) {
	if tx == nil || repo == nil {
		return nil, communicationTransactionUnavailable("append-only set observation", nil)
	}
	if !repo.Descriptor().AppendOnly {
		return nil, communicationTransactionUnavailable(
			fmt.Sprintf("append-only set observation of mutable %q; lock it instead", kind), nil,
		)
	}
	expectedParent, err := appendOnlySetFenceParentKind(kind)
	if err != nil {
		return nil, err
	}
	if fence.parentKind != expectedParent || !validCanonicalCommunicationID(fence.parentID) {
		return nil, communicationTransactionUnavailable(
			fmt.Sprintf("append-only %q set fence must be a locked %q", kind, expectedParent), nil,
		)
	}
	parent, locked := tx.lockedRow(fence.parentKind, fence.parentID)
	if !locked {
		return nil, communicationTransactionUnavailable(
			fmt.Sprintf("append-only %q set requires this transaction's row lock on its %q",
				kind, fence.parentKind), nil,
		)
	}
	parentID, err := directNoticeRecordID(parent, model.ColID)
	if err != nil || parentID != fence.parentID {
		return nil, directNoticeReadUnknown("locked append-only set fence has a different identity", err)
	}
	if fence.parentKind == messageKind &&
		MessageState(parent.String(colCommState)) == MessageDraft {
		return nil, directNoticeReadUnknown(
			"append-only audience set is still open: the locked Message is draft", nil,
		)
	}
	return parent, nil
}

// appendOnlySetRowCheck validates one observed child row against the locked
// parent: same workspace, and the row's parent column names the fence (or, for
// contributions, one of the audience IDs the caller enumerated behind the same
// fence).
type appendOnlySetRowCheck struct {
	kind         model.Kind
	workspace    string
	parentColumn string
	parents      map[string]struct{}
}

func newAppendOnlySetRowCheck(
	kind model.Kind,
	parent model.Record,
	fence appendOnlySetFence,
	queries [][]model.Filter,
) (appendOnlySetRowCheck, error) {
	check := appendOnlySetRowCheck{
		kind:      kind,
		workspace: parent.String(colWorkWorkspaceID),
		parents:   make(map[string]struct{}),
	}
	if check.workspace == "" {
		return appendOnlySetRowCheck{}, directNoticeReadUnknown(
			"locked append-only set fence has no workspace", nil,
		)
	}
	switch kind {
	case messageAudienceKind:
		check.parentColumn = colCommMessageID
		check.parents[fence.parentID.String()] = struct{}{}
	case messageAckKind:
		check.parentColumn = colCommDeliveryID
		check.parents[fence.parentID.String()] = struct{}{}
	case messageAudienceRecipientKind:
		check.parentColumn = colCommMessageAudienceID
		for _, filters := range queries {
			for _, filter := range filters {
				if filter.Column != colCommMessageAudienceID || filter.Op != model.OpEq {
					continue
				}
				value, ok := filter.Value.(string)
				if !ok {
					return appendOnlySetRowCheck{}, directNoticeReadUnknown(
						"audience contribution query names a non-string audience", nil,
					)
				}
				check.parents[value] = struct{}{}
			}
		}
		if len(check.parents) == 0 && len(queries) != 0 {
			return appendOnlySetRowCheck{}, directNoticeReadUnknown(
				"audience contribution query does not name its audience", nil,
			)
		}
	default:
		return appendOnlySetRowCheck{}, communicationTransactionUnavailable(
			fmt.Sprintf("append-only set observation of %q has no row check", kind), nil,
		)
	}
	return check, nil
}

func (check appendOnlySetRowCheck) validate(row model.Record) error {
	if row.String(colWorkWorkspaceID) != check.workspace {
		return directNoticeReadUnknown(
			fmt.Sprintf("append-only %s row crosses the fenced workspace", check.kind), nil,
		)
	}
	if _, ok := check.parents[row.String(check.parentColumn)]; !ok {
		return directNoticeReadUnknown(
			fmt.Sprintf("append-only %s row left its fenced parent set", check.kind), nil,
		)
	}
	return nil
}

// appendOnlySetPage lists one query completely through the observation phase,
// paging with the same cursor-advance guard as the locked sets, deduplicating
// by ID across the whole set and refusing to exceed bound. It returns the
// listed rows themselves: behind a proven fence they are the current state, so
// re-reading each one would only spend a statement per row.
func appendOnlySetPage(
	ctx context.Context,
	repo communicationRepository,
	filters []model.Filter,
	bound int,
	check appendOnlySetRowCheck,
	seen map[model.ID]struct{},
	count *int,
	overBound string,
) ([]model.Record, error) {
	query := model.Query{
		Filters: append([]model.Filter(nil), filters...),
		Limit:   directNoticeReadSetPageSize,
	}
	out := make([]model.Record, 0)
	seenCursors := make(map[string]struct{})
	for {
		rows, page, listErr := repo.List(ctx, query)
		if listErr != nil {
			return nil, listErr
		}
		for _, row := range rows {
			id, parseErr := directNoticeRecordID(row, model.ColID)
			if parseErr != nil {
				return nil, parseErr
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, directNoticeReadUnknown("append-only row set repeats an ID", nil)
			}
			if err := check.validate(row); err != nil {
				return nil, err
			}
			seen[id] = struct{}{}
			*count++
			if *count > bound {
				return nil, directNoticeReadUnknown(overBound, nil)
			}
			out = append(out, row)
		}
		if !page.HasMore {
			break
		}
		next, cursorErr := advanceDirectNoticeReadCursor(
			query.Cursor, page.Cursor, len(rows), seenCursors,
		)
		if cursorErr != nil {
			return nil, cursorErr
		}
		query.Cursor = next
	}
	return out, nil
}

func sortAppendOnlySet(records []model.Record) {
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].String(model.ColID) < records[j].String(model.ColID)
	})
}

// observeAppendOnlyRecordSet is the single-parent immutable set read: it
// proves fence closes kind, then lists the rows matching filters in ID order
// with the bound, duplicate, workspace and parent checks of the locked sets.
// It never calls Lock on the append-only rows.
func observeAppendOnlyRecordSet(
	ctx context.Context,
	tx *communicationTx,
	kind model.Kind,
	fence appendOnlySetFence,
	filters []model.Filter,
	bound int,
) ([]model.Record, error) {
	if bound < 1 {
		return nil, directNoticeReadUnknown("append-only row-set bound is malformed", nil)
	}
	repo, err := tx.repo(kind)
	if err != nil {
		return nil, err
	}
	parent, err := fence.prove(tx, kind, repo)
	if err != nil {
		return nil, err
	}
	check, err := newAppendOnlySetRowCheck(kind, parent, fence, [][]model.Filter{filters})
	if err != nil {
		return nil, err
	}
	count := 0
	records, err := appendOnlySetPage(
		ctx, repo, filters, bound, check, make(map[model.ID]struct{}), &count,
		"append-only row set exceeds bound",
	)
	if err != nil {
		return nil, err
	}
	sortAppendOnlySet(records)
	return records, nil
}

// observeAppendOnlyBatchRecordSets is the batch shape used by the inbox and
// cursor graphs: every spec's OwnerID is the locked parent that fences that
// spec's rows, and each parent is proved separately before its rows are
// listed. IDs must be unique across the whole batch, as with the locked sets.
func observeAppendOnlyBatchRecordSets(
	ctx context.Context,
	tx *communicationTx,
	kind model.Kind,
	specs []directNoticeReadSetSpec,
) (map[model.ID][]model.Record, error) {
	repo, err := tx.repo(kind)
	if err != nil {
		return nil, err
	}
	parentKind, err := appendOnlySetFenceParentKind(kind)
	if err != nil {
		return nil, err
	}
	ordered := append([]directNoticeReadSetSpec(nil), specs...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].OwnerID.String() < ordered[j].OwnerID.String()
	})
	sets := make(map[model.ID][]model.Record, len(ordered))
	seen := make(map[model.ID]struct{})
	seenOwners := make(map[model.ID]struct{}, len(ordered))
	for _, spec := range ordered {
		if !validCanonicalCommunicationID(spec.OwnerID) || spec.Bound < 1 {
			return nil, directNoticeReadUnknown("append-only batch row-set spec is malformed", nil)
		}
		if _, duplicate := seenOwners[spec.OwnerID]; duplicate {
			return nil, directNoticeReadUnknown("append-only batch repeats row-set owner", nil)
		}
		seenOwners[spec.OwnerID] = struct{}{}
		fence := appendOnlySetFence{parentKind: parentKind, parentID: spec.OwnerID}
		parent, proveErr := fence.prove(tx, kind, repo)
		if proveErr != nil {
			return nil, proveErr
		}
		check, checkErr := newAppendOnlySetRowCheck(kind, parent, fence, spec.Queries)
		if checkErr != nil {
			return nil, checkErr
		}
		records := make([]model.Record, 0)
		count := 0
		for _, filters := range spec.Queries {
			rows, pageErr := appendOnlySetPage(
				ctx, repo, filters, spec.Bound, check, seen, &count,
				"append-only batch row set exceeds bound",
			)
			if pageErr != nil {
				return nil, pageErr
			}
			records = append(records, rows...)
		}
		sortAppendOnlySet(records)
		sets[spec.OwnerID] = records
	}
	return sets, nil
}

// observeAppendOnlyContributionSet reads the MessageAudienceRecipient rows of
// audiences, all of which must belong to the Message named by fence. The
// audiences were themselves enumerated behind the same fence, so the
// contribution set is closed by the same locked, non-draft Message.
func observeAppendOnlyContributionSet(
	ctx context.Context,
	tx *communicationTx,
	fence appendOnlySetFence,
	audiences []MessageAudience,
) ([]model.Record, error) {
	repo, err := tx.repo(messageAudienceRecipientKind)
	if err != nil {
		return nil, err
	}
	parent, err := fence.prove(tx, messageAudienceRecipientKind, repo)
	if err != nil {
		return nil, err
	}
	ordered := append([]MessageAudience(nil), audiences...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID.String() < ordered[j].ID.String() })
	queries := make([][]model.Filter, 0, len(ordered))
	seenAudiences := make(map[model.ID]struct{}, len(ordered))
	for _, audience := range ordered {
		if !validCanonicalCommunicationID(audience.ID) || audience.MessageID != fence.parentID {
			return nil, directNoticeReadUnknown(
				"audience contribution set names an audience outside the fenced Message", nil,
			)
		}
		if _, duplicate := seenAudiences[audience.ID]; duplicate {
			return nil, directNoticeReadUnknown("audience contribution set repeats an audience", nil)
		}
		seenAudiences[audience.ID] = struct{}{}
		queries = append(queries, []model.Filter{{
			Column: colCommMessageAudienceID, Op: model.OpEq, Value: audience.ID.String(),
		}})
	}
	check, err := newAppendOnlySetRowCheck(messageAudienceRecipientKind, parent, fence, queries)
	if err != nil {
		return nil, err
	}
	records := make([]model.Record, 0)
	seen := make(map[model.ID]struct{})
	count := 0
	for _, filters := range queries {
		rows, pageErr := appendOnlySetPage(
			ctx, repo, filters, directNoticeReadSetBound, check, seen, &count,
			"audience contribution set exceeds bound",
		)
		if pageErr != nil {
			return nil, pageErr
		}
		records = append(records, rows...)
	}
	sortAppendOnlySet(records)
	return records, nil
}
