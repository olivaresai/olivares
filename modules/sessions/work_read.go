// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func (m *Module) Get(ctx context.Context, tenant model.TenantID, _ WorkPrincipal, id model.ID) (WorkSnapshot, error) {
	return m.getWorkWithData(ctx, m.workData(tenant), id)
}

func (m *Module) getWorkWithData(ctx context.Context, data workData, id model.ID) (WorkSnapshot, error) {
	var out WorkSnapshot
	err := data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		out.Item, err = workItemFromRecord(ctx, sc, rec)
		if err != nil {
			return err
		}
		out.Acceptance, err = rawChildren(ctx, sc, workAcceptanceKind, id)
		if err != nil {
			return err
		}
		out.Dependencies, err = rawChildren(ctx, sc, workDependencyKind, id)
		return err
	})
	return out, classifyWorkStoreError(err)
}

func (m *Module) List(ctx context.Context, tenant model.TenantID, _ WorkPrincipal, q WorkQuery) (WorkPage, error) {
	return m.listWorkWithData(ctx, m.workData(tenant), q)
}

func (m *Module) listWorkWithData(ctx context.Context, data workData, q WorkQuery) (WorkPage, error) {
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Limit < 1 || q.Limit > 200 || !validWorkCursor(q.Cursor) {
		return WorkPage{}, broken(http.StatusBadRequest, "invalid_cursor")
	}
	filters, err := workQueryFilters(q.Filters)
	if err != nil {
		return WorkPage{}, err
	}
	out := WorkPage{Items: []WorkItem{}}
	err = data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(ctx, model.Query{Filters: filters, Limit: q.Limit, Cursor: q.Cursor})
		if err != nil {
			return err
		}
		for _, row := range rows {
			item, err := workItemFromRecord(ctx, sc, row)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, item)
		}
		out.NextCursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	return out, classifyWorkStoreError(err)
}

func validWorkCursor(cursor string) bool {
	if cursor == "" {
		return true
	}
	u, err := uuid.Parse(cursor)
	return err == nil && u.Version() == 7 && u.String() == cursor
}

func workQueryFilters(values map[string]string) ([]model.Filter, error) {
	columns := map[string]string{
		"status": colWorkStatus, "priority": colWorkPriority, "work_kind": colWorkKind,
		"owner_kind": colWorkOwnerKind, "owner_ref": colWorkOwnerRef,
		"provenance_kind": colWorkProvKind, "provenance_ref": colWorkProvRef,
		"parent_id": colWorkParentID,
	}
	var filters []model.Filter
	for name, value := range values {
		if col := columns[name]; col != "" {
			filters = append(filters, model.Filter{Column: col, Op: model.OpEq, Value: value})
			continue
		}
		switch name {
		case "archived":
			archived, err := strconv.ParseBool(value)
			if err != nil {
				return nil, broken(http.StatusBadRequest, "invalid_command")
			}
			op := model.OpIsNull
			if archived {
				op = model.OpNotNull
			}
			filters = append(filters, model.Filter{Column: colWorkArchivedAt, Op: op})
		case "due_before":
			if _, err := model.ParseTimestamp(value); err != nil {
				return nil, broken(http.StatusBadRequest, "invalid_command")
			}
			filters = append(filters, model.Filter{Column: colWorkDueAt, Op: model.OpLt, Value: value})
		case "updated_after":
			if _, err := model.ParseTimestamp(value); err != nil {
				return nil, broken(http.StatusBadRequest, "invalid_command")
			}
			filters = append(filters, model.Filter{Column: model.ColUpdatedAt, Op: model.OpGt, Value: value})
		default:
			return nil, broken(http.StatusBadRequest, "invalid_command")
		}
	}
	return filters, nil
}

func workItemFromRecord(ctx context.Context, sc store.Scope, rec model.Record) (WorkItem, error) {
	brief := rec.String(colWorkBrief)
	if !bytesEqual(rec.Bytes(colWorkBriefHash), hashBytes([]byte(brief))) {
		return WorkItem{}, unknown("evidence_unavailable", nil)
	}
	blocked := false
	if err := blockersCompleted(ctx, sc, recordID(rec)); err != nil {
		if we := asWorkError(err); we != nil && we.code == "dependency_incomplete" {
			blocked = true
		} else {
			return WorkItem{}, err
		}
	}
	item := WorkItem{
		ID: recordID(rec), WorkspaceID: model.ID(rec.String(colWorkWorkspaceID)), Version: rec.Int(model.ColVersion),
		CreatedAt: rec.String(model.ColCreatedAt), UpdatedAt: rec.String(model.ColUpdatedAt),
		WorkKind: rec.String(colWorkKind), Title: rec.String(colWorkTitle), BriefMD: brief,
		BriefHash: hexHash(rec.Bytes(colWorkBriefHash)), ContextRefs: json.RawMessage(rec.String(colWorkContextRefs)),
		Status: rec.String(colWorkStatus), Priority: rec.String(colWorkPriority),
		OwnerKind: rec.String(colWorkOwnerKind), OwnerRef: rec.String(colWorkOwnerRef), OwnerEpoch: rec.Int(colWorkOwnerEpoch),
		ProvenanceKind: rec.String(colWorkProvKind), ProvenanceRef: rec.String(colWorkProvRef),
		ProvenanceHash: hexHash(rec.Bytes(colWorkProvHash)), ParentID: model.ID(rec.String(colWorkParentID)),
		SupersedesID: model.ID(rec.String(colWorkSupersedesID)), AcceptanceRevision: rec.Int(colWorkAcceptanceRevision),
		BlockedCode: rec.String(colWorkBlockedCode), BlockedReason: rec.String(colWorkBlockedReason),
		TerminalCode: rec.String(colWorkTerminalCode), TerminalReason: rec.String(colWorkTerminalReason),
		DueAt: rec.String(colWorkDueAt), ReadyAt: rec.String(colWorkReadyAt), StartedAt: rec.String(colWorkStartedAt),
		ReviewAt: rec.String(colWorkReviewAt), TerminalAt: rec.String(colWorkTerminalAt),
		ArchivedAt: rec.String(colWorkArchivedAt), LastEventSeq: rec.Int(colWorkLastEventSeq),
		DependencyBlocked: blocked,
	}
	lease, err := workLeaseProjection(ctx, sc, rec)
	if err != nil {
		return WorkItem{}, err
	}
	item.Lease = &lease
	item.Leased = lease.LivenessVerdict == VerdictClean && lease.Live
	knownLiveness := lease.LivenessVerdict == VerdictClean
	item.Claimable = knownLiveness && !item.Leased && item.Status == "ready" && !blocked &&
		(item.OwnerKind == "agent" || item.OwnerKind == "session")
	item.Orphaned = knownLiveness && !item.Leased && (item.Status == "active" || item.Status == "blocked")
	return item, nil
}

func workLeaseProjection(ctx context.Context, sc store.Scope, item model.Record) (WorkLease, error) {
	rec, found, err := findWorkLease(ctx, sc, recordID(item))
	if err != nil {
		return WorkLease{}, err
	}
	if !found {
		return WorkLease{}, unknown("evidence_unavailable", nil)
	}
	now, err := observeLeaseClock(ctx, sc, model.ID(item.String(colWorkWorkspaceID)))
	if err != nil {
		if we := asWorkError(err); we != nil && we.verdict == VerdictUnknown {
			return workLeaseFromRecord(rec, time.Time{}, VerdictUnknown, we.code)
		}
		return WorkLease{}, err
	}
	return workLeaseFromRecord(rec, now.Time(), VerdictClean, "ok")
}

func rawChildren(ctx context.Context, sc store.Scope, kind model.Kind, itemID model.ID) ([]json.RawMessage, error) {
	repo, err := sc.Ext(kind)
	if err != nil {
		return nil, err
	}
	parentColumn := colWorkItemID
	filters := []model.Filter{{Column: parentColumn, Op: model.OpEq, Value: itemID.String()}}
	if kind == workEventKind {
		parentColumn = colEventAggregateID
		filters = []model.Filter{
			{Column: parentColumn, Op: model.OpEq, Value: itemID.String()},
			{Column: colEventAggregateKind, Op: model.OpEq, Value: string(workItemKind)},
		}
	}
	rows, err := listAll(ctx, repo, filters...)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}
