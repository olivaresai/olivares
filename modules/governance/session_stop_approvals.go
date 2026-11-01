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

// CancelSessionApprovals closes the stopped session's pending work requests.
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
		if len(ids) == 0 {
			return nil
		}
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		policies, err := loadApprovalPolicies(ctx, sc)
		if err != nil {
			return err
		}
		for _, id := range ids {
			rec, err := repo.Get(ctx, model.ID(id))
			if err != nil {
				return err
			}
			if _, err = sc.Audit().Append(ctx, model.AuditDraft{Actor: actor, ActorKind: actorKind, Action: "governance.approval.cancel", TargetKind: approvalKind, TargetID: model.ID(id)}); err != nil {
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
