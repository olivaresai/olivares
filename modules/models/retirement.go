// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The model-governance retirement step. When a tenant removes an account, this
// step deletes every model-access allow whose subject is that account: an allow
// is what let the account use a model. A forbid naming the account only keeps it
// from one, so it stays, and still restricts the account if the tenant admits it
// again. The step's transaction first pins the tenant's authorization epoch and
// the account's authority version the pass read, so a pass that read before a
// lift or a new offboard conflicts and changes nothing.

// retirementModule is the declared module name of this step.
const retirementModule = "models"

// retirementCovers are the counted columns this module's step reads.
var retirementCovers = []string{string(modelAccessKind) + "." + colMASubjectRef}

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
	m := s.m
	if m == nil || m.data == nil {
		return auth.RetirementOutcome{}, errors.New("models: the retirement step has no data handle")
	}
	var out auth.RetirementOutcome
	err := m.data.Mutate(ctx, req.Tenant, func(sc store.Scope) error {
		out = auth.RetirementOutcome{}
		fact, err := auth.PinRetirement(ctx, sc, req)
		if err != nil {
			return err
		}
		if err := retireUserAccess(ctx, sc, req.User); err != nil {
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

// retireUserAccess deletes the user-subject model-access allows that name user,
// and records each delete in the ledger as the system's. Forbids stay.
func retireUserAccess(ctx context.Context, sc store.Scope, user model.ID) error {
	repo, err := sc.Ext(modelAccessKind)
	if err != nil {
		return err
	}
	named, _, err := auth.RowsNaming(ctx, repo, pdeclModelAccessSubject, colMASubjectRef, user, eq(colMASubjectKind, subjectUser))
	if err != nil {
		return err
	}
	for _, rec := range named {
		if normalizeEffect(rec.String(colMAEffect)) == effectForbid {
			continue
		}
		id := model.ID(rec.String(model.ColID))
		if err := repo.Delete(ctx, id); err != nil {
			return err
		}
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: string(modelAccessKind) + ".retire", TargetKind: modelAccessKind, TargetID: id,
			Meta: map[string]any{"subject_kind": subjectUser, "effect": normalizeEffect(rec.String(colMAEffect))},
		}); err != nil {
			return err
		}
	}
	return nil
}
