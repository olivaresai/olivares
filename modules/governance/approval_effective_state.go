// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The native cursor is retained after the last matching row. Filling the page
// and looking ahead keep Limit/HasMore truthful even across skipped lazy expiry.
// Both callers use the repository's default id ordering, within one View.
func listEffectiveApprovals(ctx context.Context, repo store.GenericRepo, q model.Query, status string, now model.Timestamp, policies []model.Policy) ([]model.Record, model.Page, error) {
	if status != "" && status != statusExpired {
		q.Filters = append(q.Filters, eq(colStatus, status))
	}
	if status == statusPending {
		q.Filters = append(q.Filters, model.Filter{Column: colExpiresAt, Op: model.OpUnsetOrGt, Value: now.String()})
	}
	rows, page, err := repo.List(ctx, q)
	if err != nil || (status != statusExpired && status != statusApproved) {
		return rows, page, err
	}
	limit := len(rows) // The repository has normalized the requested/default/max limit.
	out := make([]model.Record, 0, limit)
	cursor := ""
	for {
		for i, rec := range rows {
			if approvalGrantStatus(rec, now, liveRiskTier(policies, rec)) != status {
				continue
			}
			if len(out) == limit {
				return out, model.Page{Cursor: cursor, HasMore: true}, nil
			}
			out = append(out, rec)
			if len(out) == limit {
				if i == len(rows)-1 {
					cursor = page.Cursor
				} else {
					// Obtain a native cursor at the matching page boundary. A
					// fixed scan batch may contain more matches than this page.
					prefix := q
					prefix.Limit = i + 1
					_, boundary, err := repo.List(ctx, prefix)
					if err != nil {
						return nil, model.Page{}, err
					}
					cursor = boundary.Cursor
				}
			}
		}
		if !page.HasMore {
			return out, model.Page{}, nil
		}
		if page.Cursor == "" || limit == 0 {
			return nil, model.Page{}, errors.New("approval list has no keyset continuation")
		}
		q.Cursor = page.Cursor
		q.Limit = max(limit, 200) // Scan skipped rows in batches, even for a one-item page.
		rows, page, err = repo.List(ctx, q)
		if err != nil {
			return nil, model.Page{}, err
		}
	}
}

// A wait deadline withdraws its still-pending request as expired, including when
// that deadline precedes the request TTL. Natural expiry uses the same transition.
// The terminal denial and its bounded reason commit with the row and audit. A
// racing human decision stays terminal, and its original review facts stay intact.
func (a *EngineApprovals) persistWaitExpiry(ctx context.Context, tenant model.TenantID, ref string, deadline bool) (Approval, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	data, mc, err := a.scope(tenant)
	if err != nil {
		return Approval{}, err
	}
	reason := "approval request expired without a decision"
	if deadline {
		reason = "approval wait deadline reached without a decision"
	}
	var out Approval
	changed := false
	transition := func(sc store.Scope) error {
		changed = false
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(cleanup, model.ID(ref))
		if err != nil {
			return err
		}
		now := a.module.clock.Now()
		policies, err := loadApprovalPolicies(cleanup, sc)
		if err != nil {
			return err
		}
		out = toApprovalDTO(rec, now, liveRiskTier(policies, rec))
		if rec.String(colStatus) != statusPending || (!deadline && out.Status != statusExpired) {
			return nil
		}
		rec[colStatus], rec[colDecidedAt] = statusExpired, now.String()
		rec, err = repo.Update(cleanup, rec)
		if err != nil {
			return err
		}
		if err = auditEvent(cleanup, sc, mc, "governance.approval.expire", approvalKind, model.ID(ref), map[string]any{"decision": "deny", "reason": reason}); err != nil {
			return err
		}
		out = toApprovalDTO(rec, now, liveRiskTier(policies, rec))
		changed = true
		return nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		err = data.Mutate(cleanup, transition)
		if err == nil || cleanup.Err() != nil || errors.Is(err, store.ErrCommitOutcomeUnknown) {
			break
		}
		// A determinate rollback may recover within this same bounded expiry
		// budget. Re-read under the writer fence; never switch it to Cancel.
	}
	if err == nil && changed {
		a.module.emitApprovalResolved(cleanup, tenant, out)
		a.module.emitApprovalFinding(cleanup, tenant, pendingFinding{kind: findingExpired, approval: out.ID, action: out.Action, severity: sdkmodel.SeverityMedium})
	}
	return out, err
}
