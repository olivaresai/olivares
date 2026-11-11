// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"slices"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CancelSessionApprovals closes pending work and session allowances when a session stops.
// Governance controls are retained by the same rule as an estate stop. The
// trusted composition root supplies the session identities and audit actor.
func (m *Module) CancelSessionApprovals(ctx context.Context, tenant model.TenantID, sessionRefs []string, actor, actorKind string) error {
	if len(sessionRefs) == 0 {
		return nil
	}
	ctx = api.DetachRequestContext(ctx)
	var closed []approvalDTO
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		closed = nil
		now := m.clock.Now()
		ids, err := m.cancelPendingApprovals(ctx, sc, now, true, func(rec model.Record) bool {
			return slices.Contains(sessionRefs, rec.String(colApprovalSessionRef)) && ksSessionRequest(rec)
		})
		if err != nil {
			return err
		}
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		for _, sid := range sessionRefs {
			grants, err := listAll(ctx, repo, eq(colApprovalSessionRef, sid), eq(colStatus, statusApproved), eq(colSessionScopeAllowed, true))
			if err != nil {
				return err
			}
			for _, grant := range grants {
				if !ksSessionRequest(grant) {
					continue
				}
				allowed, err := sessionDecisionsAllow(ctx, sc, model.ID(grant.String(model.ColID)))
				if err != nil {
					return err
				}
				if !allowed {
					continue
				}
				grant[colStatus], grant[colDecidedAt] = statusCanceled, now.String()
				if _, err := repo.Update(ctx, grant); err != nil {
					return err
				}
				ids = append(ids, grant.String(model.ColID))
			}
		}
		for _, id := range ids {
			rec, err := repo.Get(ctx, model.ID(id))
			if err != nil {
				return err
			}
			if _, err = sc.Audit().Append(ctx, model.AuditDraft{Actor: actor, ActorKind: actorKind, Action: "governance.approval.cancel", TargetKind: approvalKind, TargetID: model.ID(id)}); err != nil {
				return err
			}
			policies, err := m.approvalPolicies(ctx, sc, approvalPolicySubject(rec))
			if err != nil {
				return err
			}
			closed = append(closed, toApprovalDTO(rec, now, liveRiskTier(policies, rec)))
		}
		return nil
	})
	if err == nil {
		for _, approval := range closed {
			m.emitApprovalResolved(ctx, tenant, approval)
		}
	}
	return err
}
