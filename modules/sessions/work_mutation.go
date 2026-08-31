// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var errDependencyGuardRaced = errors.New("sessions: dependency graph guard raced")

func (m *Module) applyDomain(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	principal WorkPrincipal,
	cmd WorkCommand,
	current model.Record,
	now model.Timestamp,
	commandID model.ID,
	plan Plan,
	audit model.AuditEvent,
) (CommandResult, WorkEventEnvelope, error) {
	var item model.Record
	var resultKind string
	var resultID model.ID
	var materializedEnd workLeaseEndFact
	var err error
	switch cmd.Command {
	case "item.create":
		item, err = m.applyWorkCreate(ctx, sc, cmd, now)
		resultKind, resultID = string(workItemKind), recordID(item)
	case "item.update", "item.ready", "item.block", "item.unblock", "item.submit",
		"item.complete", "item.fail", "item.cancel", "item.archive", "item.assign":
		item, err = m.applyItemCommand(ctx, sc, principal, cmd, current, now)
		resultKind, resultID = string(workItemKind), recordID(item)
	case "dependency.add", "dependency.remove":
		item, resultID, err = m.applyDependencyCommand(ctx, sc, principal, cmd, current, now)
		resultKind = string(workDependencyKind)
	case "acceptance.add", "acceptance.update", "acceptance.evaluate":
		item, resultID, err = m.applyAcceptanceCommand(ctx, sc, principal, cmd, current, now)
		resultKind = string(workAcceptanceKind)
	case "decision.set", "decision.supersede", "decision.revoke":
		item, resultID, err = m.applyDecisionCommand(ctx, sc, principal, cmd, current, now)
		resultKind = string(workDecisionKind)
	case "lease.acquire", "lease.renew", "lease.release", "lease.takeover", "lease.revoke",
		"lease.expire", "lease.owner_died", "lease.clock_rebase":
		item, resultID, err = m.applyLeaseCommand(ctx, sc, cmd, current, now, &materializedEnd)
		if cmd.Command == "lease.clock_rebase" {
			resultKind = string(workGuardKind)
		} else {
			resultKind = string(workLeaseKind)
		}
	default:
		err = broken(http.StatusBadRequest, "invalid_command")
	}
	if err != nil {
		return CommandResult{}, WorkEventEnvelope{}, err
	}
	if materializedEnd.present {
		if _, err := appendMaterializedLeaseEndEvent(
			ctx, sc, tenant, principal, item, materializedEnd, now, commandID, audit,
		); err != nil {
			return CommandResult{}, WorkEventEnvelope{}, err
		}
	}

	event, err := appendWorkEvent(ctx, sc, tenant, principal, cmd, item, resultKind, resultID, now, commandID, audit)
	if err != nil {
		return CommandResult{}, WorkEventEnvelope{}, err
	}
	leaseFence := int64(0)
	if resultKind == string(workLeaseKind) {
		leases, err := sc.Ext(workLeaseKind)
		if err != nil {
			return CommandResult{}, WorkEventEnvelope{}, err
		}
		lease, err := leases.Get(ctx, resultID)
		if err != nil {
			return CommandResult{}, WorkEventEnvelope{}, err
		}
		leaseFence = lease.Int(colLeaseFence)
	}
	verdict, code := VerdictClean, "applied"
	if cmd.postCommitRefusal != nil && *cmd.postCommitRefusal != nil {
		if we := asWorkError(*cmd.postCommitRefusal); we != nil {
			verdict, code = we.verdict, we.code
		}
	}
	return CommandResult{
		Verdict: verdict, Code: code, CommandID: commandID,
		ResultKind: resultKind, ResultID: resultID, Version: item.Int(model.ColVersion),
		Status: item.String(colWorkStatus), EventID: event.EventID,
		EventSeq: item.Int(colWorkLastEventSeq), OwnerEpoch: item.Int(colWorkOwnerEpoch),
		LeaseFence: leaseFence,
		PlanHash:   plan.PlanHash, AuditSeq: audit.Seq,
	}, event, nil
}

func (m *Module) applyWorkCreate(ctx context.Context, sc store.Scope, cmd WorkCommand, _ model.Timestamp) (model.Record, error) {
	refs, err := canonicalJSON(cmd.ContextRefs)
	if err != nil {
		return nil, err
	}
	provHash, err := decodeHash(cmd.ProvenanceHash, false)
	if err != nil {
		return nil, err
	}
	record := model.Record{
		colWorkWorkspaceID: cmd.WorkspaceID.String(), colWorkKind: cmd.WorkKind,
		colWorkTitle: cmd.Title, colWorkBrief: cmd.BriefMD, colWorkBriefHash: hashBytes([]byte(cmd.BriefMD)),
		colWorkContextRefs: string(refs), colWorkStatus: "draft", colWorkPriority: cmd.Priority,
		colWorkOwnerKind: cmd.OwnerKind, colWorkOwnerRef: cmd.OwnerRef, colWorkOwnerEpoch: int64(1),
		colWorkProvKind: cmd.ProvenanceKind, colWorkProvRef: cmd.ProvenanceRef,
		colWorkProvHash: nullableBytes(provHash), colWorkParentID: nullableID(cmd.ParentID),
		colWorkSupersedesID: nullableID(cmd.SupersedesID), colWorkAcceptanceRevision: int64(1),
		colWorkBlockedCode: nil, colWorkBlockedReason: nil, colWorkTerminalCode: nil,
		colWorkTerminalReason: nil, colWorkDueAt: nullableString(cmd.DueAt),
		colWorkReadyAt: nil, colWorkStartedAt: nil, colWorkReviewAt: nil,
		colWorkTerminalAt: nil, colWorkArchivedAt: nil, colWorkLastEventSeq: int64(1),
	}
	items, err := sc.Ext(workItemKind)
	if err != nil {
		return nil, err
	}
	item, err := items.Create(ctx, record)
	if err != nil {
		return nil, err
	}
	if err := createVacantWorkLease(ctx, sc, cmd.WorkspaceID, recordID(item)); err != nil {
		return nil, err
	}
	criteria, err := sc.Ext(workAcceptanceKind)
	if err != nil {
		return nil, err
	}
	for _, input := range cmd.Acceptance {
		if _, err := criteria.Create(ctx, acceptanceRecord(cmd.WorkspaceID, recordID(item), input)); err != nil {
			return nil, err
		}
	}
	return item, nil
}

func acceptanceRecord(workspace, itemID model.ID, input AcceptanceInput) model.Record {
	return model.Record{
		colWorkWorkspaceID: workspace.String(), colWorkItemID: itemID.String(),
		colAccKey: input.Key, colAccOrdinal: input.Ordinal, colAccStatement: input.Statement,
		colAccRequired: input.Required, colAccState: "pending", colAccEvidenceRef: nil,
		colAccEvidenceHash: nil, colAccVerifiedByKind: nil, colAccVerifiedByRef: nil,
		colAccVerifiedAt: nil, colAccWaiverDecisionID: nil,
	}
}

func (m *Module) applyItemCommand(
	ctx context.Context,
	sc store.Scope,
	principal WorkPrincipal,
	cmd WorkCommand,
	item model.Record,
	now model.Timestamp,
) (model.Record, error) {
	fromStatus := item.String(colWorkStatus)
	if fromStatus == "active" {
		switch cmd.Command {
		case "item.submit":
			if err := m.endExecutionLease(ctx, sc, cmd, item, now, fenceReleased, "submitted_for_review"); err != nil {
				return nil, err
			}
		case "item.block", "item.fail":
			if principal.Admin {
				if err := m.revokeLiveLeaseForAdmin(ctx, sc, cmd, item, now, cmd.Command); err != nil {
					return nil, err
				}
			} else if err := m.endExecutionLease(ctx, sc, cmd, item, now, fenceRevoked, cmd.Command); err != nil {
				return nil, err
			}
		case "item.cancel", "item.assign":
			if err := m.revokeLiveLeaseForAdmin(ctx, sc, cmd, item, now, cmd.Command); err != nil {
				return nil, err
			}
		}
	}
	switch cmd.Command {
	case "item.update":
		changed := false
		if cmd.Title != "" && cmd.Title != item.String(colWorkTitle) {
			item[colWorkTitle] = cmd.Title
			changed = true
		}
		if cmd.BriefMD != "" && cmd.BriefMD != item.String(colWorkBrief) {
			item[colWorkBrief] = cmd.BriefMD
			item[colWorkBriefHash] = hashBytes([]byte(cmd.BriefMD))
			changed = true
		}
		if cmd.ContextRefs != nil {
			refs, err := canonicalJSON(cmd.ContextRefs)
			if err != nil {
				return nil, err
			}
			if string(refs) != item.String(colWorkContextRefs) {
				item[colWorkContextRefs] = string(refs)
				changed = true
			}
		}
		if cmd.Priority != "" && cmd.Priority != item.String(colWorkPriority) {
			item[colWorkPriority] = cmd.Priority
			changed = true
		}
		if cmd.DueAt != "" && cmd.DueAt != item.String(colWorkDueAt) {
			if _, err := model.ParseTimestamp(cmd.DueAt); err != nil {
				return nil, broken(http.StatusBadRequest, "invalid_command")
			}
			item[colWorkDueAt] = cmd.DueAt
			changed = true
		}
		if !changed {
			return nil, broken(http.StatusConflict, "state_conflict")
		}
		if item.String(colWorkStatus) == "ready" {
			item[colWorkStatus] = "draft"
			item[colWorkReadyAt] = nil
			item[colWorkAcceptanceRevision] = item.Int(colWorkAcceptanceRevision) + 1
		}
	case "item.ready":
		item[colWorkStatus], item[colWorkReadyAt] = "ready", now.String()
	case "item.block":
		item[colWorkStatus], item[colWorkBlockedCode], item[colWorkBlockedReason] = "blocked", cmd.Code, cmd.Reason
	case "item.unblock":
		item[colWorkStatus], item[colWorkBlockedCode], item[colWorkBlockedReason] = "ready", nil, nil
		if item.IsNull(colWorkReadyAt) {
			item[colWorkReadyAt] = now.String()
		}
	case "item.submit":
		item[colWorkStatus], item[colWorkReviewAt] = "review", now.String()
	case "item.complete":
		item[colWorkStatus], item[colWorkTerminalAt] = "completed", now.String()
		item[colWorkTerminalCode], item[colWorkTerminalReason] = nullableString(cmd.Code), nullableString(cmd.Reason)
	case "item.fail":
		item[colWorkStatus], item[colWorkTerminalAt] = "failed", now.String()
		item[colWorkTerminalCode], item[colWorkTerminalReason] = cmd.Code, cmd.Reason
		item[colWorkBlockedCode], item[colWorkBlockedReason] = nil, nil
	case "item.cancel":
		item[colWorkStatus], item[colWorkTerminalAt] = "canceled", now.String()
		item[colWorkTerminalCode], item[colWorkTerminalReason] = cmd.Code, cmd.Reason
	case "item.archive":
		item[colWorkArchivedAt] = now.String()
	case "item.assign":
		if item.String(colWorkOwnerKind) == cmd.OwnerKind && item.String(colWorkOwnerRef) == cmd.OwnerRef {
			return nil, broken(http.StatusConflict, "owner_unchanged")
		}
		item[colWorkOwnerKind], item[colWorkOwnerRef] = cmd.OwnerKind, cmd.OwnerRef
		item[colWorkOwnerEpoch] = item.Int(colWorkOwnerEpoch) + 1
	}
	return updateWorkItemWithEvent(ctx, sc, item)
}

func (m *Module) applyDependencyCommand(ctx context.Context, sc store.Scope, principal WorkPrincipal, cmd WorkCommand, item model.Record, now model.Timestamp) (model.Record, model.ID, error) {
	repo, err := sc.Ext(workDependencyKind)
	if err != nil {
		return nil, "", err
	}
	if cmd.Command == "dependency.remove" {
		dep, err := repo.Get(ctx, cmd.TargetID)
		if err != nil {
			return nil, "", err
		}
		if dep.String(colWorkItemID) != item.String(model.ColID) || dep.String(colWorkWorkspaceID) != item.String(colWorkWorkspaceID) {
			return nil, "", broken(http.StatusNotFound, "not_found")
		}
		if !dep.Bool(colDepActive) {
			return nil, "", broken(http.StatusConflict, "target_closed")
		}
		dep[colDepActive] = false
		dep[colDepRemovedByKind], dep[colDepRemovedByRef], dep[colDepRemovedAt] = principal.ActorKind, principal.ActorRef, now.String()
		if dep, err = repo.Update(ctx, dep); err != nil {
			return nil, "", err
		}
		item, err = updateWorkItemWithEvent(ctx, sc, item)
		return item, recordID(dep), err
	}
	if err := dependencyWouldCycle(ctx, sc, recordID(item), cmd.DependsOnID, item.String(colWorkWorkspaceID)); err != nil {
		return nil, "", err
	}
	rows, err := listAll(ctx, repo,
		model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: item.String(model.ColID)},
		model.Filter{Column: colDepDependsOnID, Op: model.OpEq, Value: cmd.DependsOnID.String()},
		model.Filter{Column: colDepRelation, Op: model.OpEq, Value: "blocks"},
	)
	if err != nil {
		return nil, "", err
	}
	var dep model.Record
	if len(rows) == 1 {
		dep = rows[0]
		if dep.Bool(colDepActive) {
			return nil, "", broken(http.StatusConflict, "dependency_exists")
		}
		dep[colDepActive] = true
		dep[colDepAddedByKind], dep[colDepAddedByRef] = principal.ActorKind, principal.ActorRef
		dep[colDepRemovedByKind], dep[colDepRemovedByRef], dep[colDepRemovedAt] = nil, nil, nil
		dep, err = repo.Update(ctx, dep)
	} else {
		dep, err = repo.Create(ctx, model.Record{
			colWorkWorkspaceID: item.String(colWorkWorkspaceID), colWorkItemID: item.String(model.ColID),
			colDepDependsOnID: cmd.DependsOnID.String(), colDepRelation: "blocks", colDepActive: true,
			colDepAddedByKind: principal.ActorKind, colDepAddedByRef: principal.ActorRef,
			colDepRemovedByKind: nil, colDepRemovedByRef: nil, colDepRemovedAt: nil,
		})
	}
	if err != nil {
		return nil, "", err
	}
	items, err := sc.Ext(workItemKind)
	if err != nil {
		return nil, "", err
	}
	predecessor, err := items.Get(ctx, cmd.DependsOnID)
	if err != nil {
		return nil, "", err
	}
	if item.String(colWorkStatus) == "ready" && predecessor.String(colWorkStatus) != "completed" {
		item[colWorkStatus], item[colWorkBlockedCode], item[colWorkBlockedReason] = "blocked", "dependency_incomplete", "an active predecessor is not completed"
	}
	item, err = updateWorkItemWithEvent(ctx, sc, item)
	return item, recordID(dep), err
}

func touchDependencyGuard(ctx context.Context, sc store.Scope, workspace model.ID) error {
	repo, err := sc.Ext(workGuardKind)
	if err != nil {
		return err
	}
	rows, err := listAll(ctx, repo,
		model.Filter{Column: colWorkWorkspaceID, Op: model.OpEq, Value: workspace.String()},
		model.Filter{Column: colGuardKind, Op: model.OpEq, Value: "dependency_graph"},
	)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		_, err = repo.Create(ctx, model.Record{colWorkWorkspaceID: workspace.String(), colGuardKind: "dependency_graph", colGuardEpoch: int64(1), colGuardLastDBTime: nil})
		if errors.Is(err, store.ErrConflict) {
			return errDependencyGuardRaced
		}
		return err
	}
	rows[0][colGuardEpoch] = rows[0].Int(colGuardEpoch) + 1
	_, err = repo.Update(ctx, rows[0])
	if errors.Is(err, store.ErrConflict) {
		return errDependencyGuardRaced
	}
	return err
}

func dependencyWouldCycle(ctx context.Context, sc store.Scope, itemID, dependsOnID model.ID, workspace string) error {
	repo, err := sc.Ext(workDependencyKind)
	if err != nil {
		return err
	}
	rows, err := listAll(ctx, repo,
		model.Filter{Column: colWorkWorkspaceID, Op: model.OpEq, Value: workspace},
		model.Filter{Column: colDepActive, Op: model.OpEq, Value: true},
	)
	if err != nil {
		return err
	}
	graph := make(map[string][]string)
	for _, row := range rows {
		graph[row.String(colWorkItemID)] = append(graph[row.String(colWorkItemID)], row.String(colDepDependsOnID))
	}
	graph[itemID.String()] = append(graph[itemID.String()], dependsOnID.String())
	seen := map[string]bool{}
	var visit func(string) bool
	visit = func(node string) bool {
		if node == itemID.String() && seen[node] {
			return true
		}
		if seen[node] {
			return false
		}
		seen[node] = true
		for _, next := range graph[node] {
			if next == itemID.String() || visit(next) {
				return true
			}
		}
		return false
	}
	if visit(dependsOnID.String()) {
		return broken(http.StatusConflict, "dependency_cycle")
	}
	return nil
}

func (m *Module) applyAcceptanceCommand(ctx context.Context, sc store.Scope, principal WorkPrincipal, cmd WorkCommand, item model.Record, now model.Timestamp) (model.Record, model.ID, error) {
	repo, err := sc.Ext(workAcceptanceKind)
	if err != nil {
		return nil, "", err
	}
	input := cmd.Acceptance[0]
	var criterion model.Record
	if cmd.Command == "acceptance.add" {
		rows, err := listAll(ctx, repo,
			model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: item.String(model.ColID)},
			model.Filter{Column: colAccKey, Op: model.OpEq, Value: input.Key},
		)
		if err != nil {
			return nil, "", err
		}
		if len(rows) != 0 {
			return nil, "", broken(http.StatusConflict, "acceptance_duplicate")
		}
		criterion, err = repo.Create(ctx, acceptanceRecord(model.ID(item.String(colWorkWorkspaceID)), recordID(item), input))
		item[colWorkAcceptanceRevision] = item.Int(colWorkAcceptanceRevision) + 1
	} else if cmd.Command == "acceptance.update" {
		criterion, err = repo.Get(ctx, cmd.CriterionID)
		if err == nil && (criterion.String(colWorkItemID) != item.String(model.ColID) ||
			criterion.String(colWorkWorkspaceID) != item.String(colWorkWorkspaceID)) {
			err = broken(http.StatusNotFound, "not_found")
		}
		if err == nil {
			criterion[colAccStatement], criterion[colAccOrdinal], criterion[colAccRequired] =
				input.Statement, input.Ordinal, input.Required
			criterion, err = repo.Update(ctx, criterion)
			item[colWorkAcceptanceRevision] = item.Int(colWorkAcceptanceRevision) + 1
		}
	} else {
		criterion, err = repo.Get(ctx, cmd.CriterionID)
		if err == nil && (criterion.String(colWorkItemID) != item.String(model.ColID) || criterion.String(colWorkWorkspaceID) != item.String(colWorkWorkspaceID)) {
			err = broken(http.StatusNotFound, "not_found")
		}
		if err == nil {
			from, to := criterion.String(colAccState), input.State
			legal := from == "pending" && to != "pending" || from == "failed" && (to == "pending" || to == "passed" || to == "waived")
			if !legal {
				err = broken(http.StatusConflict, "illegal_transition")
			}
		}
		if err == nil {
			criterion[colAccState] = input.State
			if input.State == "pending" {
				criterion[colAccEvidenceRef], criterion[colAccEvidenceHash] = nil, nil
				criterion[colAccVerifiedByKind], criterion[colAccVerifiedByRef], criterion[colAccVerifiedAt] = nil, nil, nil
				criterion[colAccWaiverDecisionID] = nil
			} else {
				criterion[colAccEvidenceRef] = nullableString(input.EvidenceRef)
				evidenceHash, hashErr := decodeHash(input.EvidenceHash, input.State == "passed")
				if hashErr != nil {
					return nil, "", hashErr
				}
				criterion[colAccEvidenceHash] = nullableBytes(evidenceHash)
				criterion[colAccVerifiedByKind], criterion[colAccVerifiedByRef], criterion[colAccVerifiedAt] = principal.ActorKind, principal.ActorRef, now.String()
				criterion[colAccWaiverDecisionID] = nullableID(input.WaiverDecisionID)
			}
			criterion, err = repo.Update(ctx, criterion)
		}
	}
	if err != nil {
		return nil, "", err
	}
	item, err = updateWorkItemWithEvent(ctx, sc, item)
	return item, recordID(criterion), err
}

func (m *Module) applyDecisionCommand(ctx context.Context, sc store.Scope, principal WorkPrincipal, cmd WorkCommand, item model.Record, now model.Timestamp) (model.Record, model.ID, error) {
	heads, err := sc.Ext(workDecisionHeadKind)
	if err != nil {
		return nil, "", err
	}
	rows, err := listAll(ctx, heads,
		model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: item.String(model.ColID)},
		model.Filter{Column: colDecisionKey, Op: model.OpEq, Value: cmd.DecisionKey},
	)
	if err != nil {
		return nil, "", err
	}
	var head model.Record
	if len(rows) == 1 {
		head = rows[0]
	}
	seq := int64(1)
	var supersedes, revokes any
	subjectKind, subjectRef := cmd.SubjectKind, cmd.SubjectRef
	switch cmd.Command {
	case "decision.set":
		if head != nil && head.String(colDecisionHeadState) != "revoked" {
			return nil, "", broken(http.StatusConflict, "target_closed")
		}
		if head != nil {
			seq = head.Int(colDecisionCurrentSeq) + 1
		}
	case "decision.supersede":
		if head == nil || head.String(colDecisionHeadState) != "effective" {
			return nil, "", broken(http.StatusConflict, "stale_decision")
		}
		seq, supersedes = head.Int(colDecisionCurrentSeq)+1, head.String(colDecisionCurrentID)
	case "decision.revoke":
		if head == nil || head.String(colDecisionHeadState) != "effective" || head.String(colDecisionCurrentID) != cmd.DecisionID.String() {
			return nil, "", broken(http.StatusConflict, "stale_decision")
		}
		seq, revokes = head.Int(colDecisionCurrentSeq)+1, head.String(colDecisionCurrentID)
		decisions, err := sc.Ext(workDecisionKind)
		if err != nil {
			return nil, "", err
		}
		old, err := decisions.Get(ctx, cmd.DecisionID)
		if err != nil {
			return nil, "", err
		}
		subjectKind, subjectRef = old.String(colDecisionSubjectKind), old.String(colDecisionSubjectRef)
	}
	operation := cmd.Command[len("decision."):]
	hashDoc := map[string]any{
		"workspace_id": item.String(colWorkWorkspaceID), "work_item_id": item.String(model.ColID),
		"decision_key": cmd.DecisionKey, "decision_seq": seq, "subject_kind": subjectKind,
		"subject_ref": subjectRef, "operation": operation, "statement_md": cmd.StatementMD,
		"rationale_md": cmd.RationaleMD, "decided_by_kind": principal.ActorKind,
		"decided_by_ref": principal.ActorRef, "authority_ref": cmd.AuthorityRef,
		"supersedes_id": supersedes, "revokes_id": revokes, "effective_at": now.String(),
	}
	canonical, err := canonicalJSON(hashDoc)
	if err != nil {
		return nil, "", err
	}
	decisions, err := sc.Ext(workDecisionKind)
	if err != nil {
		return nil, "", err
	}
	decision, err := decisions.Create(ctx, model.Record{
		colWorkWorkspaceID: item.String(colWorkWorkspaceID), colWorkItemID: item.String(model.ColID),
		colDecisionKey: cmd.DecisionKey, colDecisionSeq: seq,
		colDecisionSubjectKind: subjectKind, colDecisionSubjectRef: subjectRef,
		colDecisionOperation: operation, colDecisionStatement: cmd.StatementMD,
		colDecisionRationale: cmd.RationaleMD, colDecisionByKind: principal.ActorKind,
		colDecisionByRef: principal.ActorRef, colDecisionAuthority: cmd.AuthorityRef,
		colDecisionSupersedesID: supersedes, colDecisionRevokesID: revokes,
		colDecisionEffectiveAt: now.String(), colDecisionHash: hashBytes(canonical),
	})
	if err != nil {
		return nil, "", err
	}
	headState := "effective"
	if operation == "revoke" {
		headState = "revoked"
	}
	if head == nil {
		head, err = heads.Create(ctx, model.Record{
			colWorkWorkspaceID: item.String(colWorkWorkspaceID), colWorkItemID: item.String(model.ColID),
			colDecisionKey: cmd.DecisionKey, colDecisionCurrentID: recordID(decision).String(),
			colDecisionCurrentSeq: seq, colDecisionHeadState: headState,
			colDecisionHeadHash: decision.Bytes(colDecisionHash),
		})
	} else {
		head[colDecisionCurrentID], head[colDecisionCurrentSeq] = recordID(decision).String(), seq
		head[colDecisionHeadState], head[colDecisionHeadHash] = headState, decision.Bytes(colDecisionHash)
		head, err = heads.Update(ctx, head)
	}
	if err != nil {
		return nil, "", err
	}
	if operation == "revoke" {
		if err := resetRevokedWaivers(ctx, sc, item, cmd.DecisionID); err != nil {
			return nil, "", err
		}
	}
	item, err = updateWorkItemWithEvent(ctx, sc, item)
	return item, recordID(decision), err
}

func resetRevokedWaivers(ctx context.Context, sc store.Scope, item model.Record, decisionID model.ID) error {
	repo, err := sc.Ext(workAcceptanceKind)
	if err != nil {
		return err
	}
	rows, err := listAll(ctx, repo,
		model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: item.String(model.ColID)},
		model.Filter{Column: colAccWaiverDecisionID, Op: model.OpEq, Value: decisionID.String()},
	)
	if err != nil {
		return err
	}
	for _, row := range rows {
		row[colAccState], row[colAccEvidenceRef], row[colAccEvidenceHash] = "pending", nil, nil
		row[colAccVerifiedByKind], row[colAccVerifiedByRef], row[colAccVerifiedAt] = nil, nil, nil
		row[colAccWaiverDecisionID] = nil
		if _, err := repo.Update(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

func updateWorkItemWithEvent(ctx context.Context, sc store.Scope, item model.Record) (model.Record, error) {
	item[colWorkLastEventSeq] = item.Int(colWorkLastEventSeq) + 1
	repo, err := sc.Ext(workItemKind)
	if err != nil {
		return nil, err
	}
	return repo.Update(ctx, item)
}

func appendWorkEvent(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	principal WorkPrincipal,
	cmd WorkCommand,
	item model.Record,
	resultKind string,
	resultID model.ID,
	now model.Timestamp,
	commandID model.ID,
	audit model.AuditEvent,
) (WorkEventEnvelope, error) {
	eventCommand := cmd.Command
	if cmd.postCommitRefusal != nil && *cmd.postCommitRefusal != nil &&
		(cmd.Command == "lease.acquire" || cmd.Command == "lease.takeover") {
		eventCommand = "lease.expire"
	}
	payloadDoc := map[string]any{
		"command": eventCommand, "result_kind": resultKind, "result_id": resultID.String(),
		"workspace_id": item.String(colWorkWorkspaceID), "work_item_id": item.String(model.ColID),
		"status": item.String(colWorkStatus), "owner_epoch": item.Int(colWorkOwnerEpoch),
		"event_seq": item.Int(colWorkLastEventSeq),
	}
	if isWorkLeaseCommand(cmd.Command) && cmd.Command != "lease.clock_rebase" {
		leases, leaseErr := sc.Ext(workLeaseKind)
		if leaseErr != nil {
			return WorkEventEnvelope{}, leaseErr
		}
		lease, leaseErr := leases.Get(ctx, resultID)
		if leaseErr != nil {
			return WorkEventEnvelope{}, leaseErr
		}
		payloadDoc["lease_id"] = resultID.String()
		payloadDoc["lease_state"] = lease.String(colLeaseState)
		payloadDoc["holder_sid"] = lease.String(colLeaseHolderSID)
		payloadDoc["holder_run_ref"] = lease.String(colLeaseHolderRunRef)
		payloadDoc["holder_agent_ref"] = lease.String(colLeaseHolderAgentRef)
		payloadDoc["fence"] = lease.Int(colLeaseFence)
		payloadDoc["expires_at"] = lease.String(colLeaseExpiresAt)
		if workCommandEvent(eventCommand) == "work.lease.ended" {
			reason := lease.String(colLeaseEndReason)
			// end_reason is operator-authored/free text on release and revoke.
			// WorkEvent/outbox carry only a stable semantic class and a hash;
			// copying the stored text would move secrets into every subscriber.
			// The empty optional release reason still hashes to a stable SHA-256
			// value so every ended event has the same evidence shape.
			payloadDoc["end_reason_code"] = publicLeaseEndReasonCode(eventCommand)
			payloadDoc["end_reason_hash"] = hexHash(hashBytes([]byte(reason)))
		}
		if cmd.Command == "lease.takeover" && cmd.Force {
			// A live authority override is deliberately visible as a high-severity
			// fact without copying the operator-authored reason into the broadly
			// distributed event. Decision is a durable reference; the reason hash
			// permits correlation with the audited command/receipt.
			payloadDoc["forced"] = true
			payloadDoc["severity"] = "high"
			payloadDoc["decision_id"] = cmd.DecisionID.String()
			payloadDoc["takeover_reason_hash"] = hexHash(hashBytes([]byte(cmd.Reason)))
		}
	}
	if implicitWorkLeaseEndCommand(cmd.Command) {
		lease, found, leaseErr := findWorkLease(ctx, sc, recordID(item))
		if leaseErr != nil {
			return WorkEventEnvelope{}, leaseErr
		}
		if found && lease.String(colLeaseEndedAt) == now.String() {
			payloadDoc["lease_transition"] = map[string]any{
				"state": lease.String(colLeaseState),
				"code":  publicImplicitLeaseEndCode(cmd.Command, lease.String(colLeaseState)),
			}
		}
	}
	return persistWorkEvent(
		ctx, sc, tenant, principal, item, workCommandEvent(eventCommand),
		item.Int(colWorkLastEventSeq), payloadDoc, now, commandID, audit,
	)
}

func appendMaterializedLeaseEndEvent(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	principal WorkPrincipal,
	item model.Record,
	fact workLeaseEndFact,
	now model.Timestamp,
	commandID model.ID,
	audit model.AuditEvent,
) (WorkEventEnvelope, error) {
	sequence := item.Int(colWorkLastEventSeq) - 1
	status := fact.workStatus
	if status == "active" {
		status = "blocked"
	}
	payloadDoc := map[string]any{
		"command": "lease.expire", "result_kind": string(workLeaseKind),
		"result_id": fact.leaseID.String(), "workspace_id": item.String(colWorkWorkspaceID),
		"work_item_id": item.String(model.ColID), "status": status,
		"owner_epoch": item.Int(colWorkOwnerEpoch), "event_seq": sequence,
		"lease_id": fact.leaseID.String(), "lease_state": fact.state,
		"holder_sid": fact.holderSID, "holder_run_ref": fact.holderRunRef,
		"holder_agent_ref": fact.holderAgentRef, "fence": fact.fence,
		"expires_at": fact.expiresAt, "end_reason_code": "lease_expired",
		"end_reason_hash": hexHash(hashBytes([]byte(fact.endReason))),
	}
	return persistWorkEvent(
		ctx, sc, tenant, principal, item, "work.lease.ended", sequence,
		payloadDoc, now, commandID, audit,
	)
}

func persistWorkEvent(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	principal WorkPrincipal,
	item model.Record,
	eventType string,
	sequence int64,
	payloadDoc map[string]any,
	now model.Timestamp,
	commandID model.ID,
	audit model.AuditEvent,
) (WorkEventEnvelope, error) {
	eventID := model.NewID()
	payload, err := canonicalJSON(payloadDoc)
	if err != nil || len(payload) > 16*1024 {
		return WorkEventEnvelope{}, fmt.Errorf("work event payload: %w", err)
	}
	events, err := sc.Ext(workEventKind)
	if err != nil {
		return WorkEventEnvelope{}, err
	}
	_, err = events.Create(ctx, model.Record{
		colWorkWorkspaceID: item.String(colWorkWorkspaceID), colEventID: eventID.String(),
		colEventAggregateKind: string(workItemKind), colEventAggregateID: item.String(model.ColID),
		colEventSeq: sequence, colEventType: eventType,
		colEventActorKind: principal.ActorKind, colEventActorRef: principal.ActorRef,
		colEventOccurredAt: now.String(), colEventPayload: string(payload), colEventPayloadHash: hashBytes(payload),
		colEventCommandID: commandID.String(), colEventAuditSeq: audit.Seq, colEventAuditHash: audit.Hash,
	})
	if err != nil {
		return WorkEventEnvelope{}, err
	}
	outbox, err := sc.Ext(workOutboxKind)
	if err != nil {
		return WorkEventEnvelope{}, err
	}
	_, err = outbox.Create(ctx, model.Record{
		colWorkWorkspaceID: item.String(colWorkWorkspaceID), colOutboxEventID: eventID.String(),
		colOutboxState: "pending", colOutboxAttempts: int64(0), colOutboxNextAttemptAt: now.String(),
		colOutboxClaimOwner: nil, colOutboxClaimUntil: nil, colOutboxPublishedAt: nil, colOutboxLastOutcome: nil,
	})
	if err != nil {
		return WorkEventEnvelope{}, err
	}
	return WorkEventEnvelope{
		TenantID: tenant, WorkspaceID: model.ID(item.String(colWorkWorkspaceID)), EventID: eventID,
		AggregateKind: string(workItemKind), AggregateID: recordID(item), Sequence: sequence,
		Type: eventType, OccurredAt: now.String(), Payload: json.RawMessage(payload),
	}, nil
}

func publicLeaseEndReasonCode(command string) string {
	switch command {
	case "lease.release":
		return "holder_released"
	case "lease.revoke":
		return "admin_revoked"
	case "lease.expire":
		return "lease_expired"
	case "lease.owner_died":
		return "owner_session_died"
	default:
		return "lease_ended"
	}
}

func implicitWorkLeaseEndCommand(command string) bool {
	switch command {
	case "item.submit", "item.block", "item.fail", "item.cancel", "item.assign":
		return true
	default:
		return false
	}
}

func publicImplicitLeaseEndCode(command, state string) string {
	if state == workLeaseExpired {
		return "lease_expired"
	}
	switch command {
	case "item.submit":
		return "submitted_for_review"
	case "item.block":
		return "item_blocked"
	case "item.fail":
		return "item_failed"
	case "item.cancel":
		return "item_canceled"
	case "item.assign":
		return "owner_reassigned"
	default:
		return "lease_ended"
	}
}

func recordID(r model.Record) model.ID { return model.ID(r.String(model.ColID)) }

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
