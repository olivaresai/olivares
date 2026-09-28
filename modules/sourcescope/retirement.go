// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The source-scoping retirement step. When a tenant removes an account, this step
// deletes every user-tree binding that names the account, allow and forbid alike,
// each with the audit event a delete records; bindings naming anyone else are
// untouched. A binding that was the last enabled allow of its source leaves the
// source unconfined, as the account's own delete would; its event says so, with
// the reason the delete path gives. The step's transaction first pins the tenant's
// authorization epoch and the account's authority version the pass read, so a pass
// that read before a lift or a new offboard conflicts and changes nothing.

// retirementModule is the declared module name of this step.
const retirementModule = "sourcescope"

// retirementCovers are the counted columns this module's step reads.
var retirementCovers = []string{string(bindingKind) + "." + colScopeRef}

// RetirementStep returns this module's retirement step.
func (m *Module) RetirementStep() auth.RetirementStep { return retirementStep{m: m} }

// RetirementCovers returns the counted columns this module's step reads, as
// kind.column.
func (m *Module) RetirementCovers() []string { return append([]string(nil), retirementCovers...) }

type retirementStep struct{ m *Module }

// Module implements auth.RetirementStep.
func (s retirementStep) Module() string { return retirementModule }

// RetireUser implements auth.RetirementStep.
func (s retirementStep) RetireUser(ctx context.Context, req auth.RetirementRequest) (auth.RetirementOutcome, error) {
	if s.m == nil {
		return auth.RetirementOutcome{}, errors.New("sourcescope: the retirement step has no module")
	}
	data := s.m.moduleData()
	if data == nil {
		return auth.RetirementOutcome{}, errors.New("sourcescope: the retirement step has no data handle")
	}
	var out auth.RetirementOutcome
	err := data.Mutate(ctx, req.Tenant, func(sc store.Scope) error {
		out = auth.RetirementOutcome{}
		fact, err := auth.PinRetirement(ctx, sc, req)
		if err != nil {
			return err
		}
		if err := retireUserBindings(ctx, sc, req.User); err != nil {
			return err
		}
		out.FactVersion = fact
		return nil
	})
	if err != nil {
		return auth.RetirementOutcome{}, err
	}
	return out, nil
}

// retireUserBindings deletes the user-tree bindings whose scope reference names
// user, and records each delete in the ledger as the system's.
func retireUserBindings(ctx context.Context, sc store.Scope, user model.ID) error {
	repo, err := sc.Ext(bindingKind)
	if err != nil {
		return err
	}
	named, _, err := auth.RowsNaming(ctx, repo, pdeclBindingScopeRef, colScopeRef, user, eq(colScopeTree, scopeUser))
	if err != nil {
		return err
	}
	for _, rec := range named {
		b := toBindingDTO(rec)
		otherAllows, err := countOtherEnabledAllows(ctx, sc, b.SourceType, b.SourceRef, b.ID)
		if err != nil {
			return err
		}
		relaxing, reason := classifyDelete(b, otherAllows)
		if err := repo.Delete(ctx, model.ID(b.ID)); err != nil {
			return err
		}
		meta := map[string]any{
			"source_type": b.SourceType, "source_ref": b.SourceRef, "scope_tree": b.ScopeTree,
			"effect": normalizeEffect(b.Effect), "enabled": b.Enabled,
		}
		if relaxing {
			meta["posture_relaxed"] = reason
		}
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: "sourcescope.binding.retire", TargetKind: bindingKind, TargetID: model.ID(b.ID),
			Meta: meta,
		}); err != nil {
			return err
		}
	}
	return nil
}
