// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The compliance retirement step. A legal hold that names an account states a
// preservation duty over the tenant's records about that person, ordered and
// released only through the hold's own governed lifecycle. Removing the account
// from the tenant neither discharges the duty nor lets the account act, so the
// step preserves every hold and reports active covering holds as blockers until
// their governed release. A store fault also refuses the proof. The step's
// transaction first pins the tenant's authorization epoch and the account's
// authority version the pass read, like every other step.

// retirementModule is the declared module name of this step.
const retirementModule = "compliance"

// retirementCovers are the counted columns this module's step reads.
var retirementCovers = []string{string(legalHoldKind) + "." + colSubjectRef}

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
		return auth.RetirementOutcome{}, errors.New("compliance: the retirement step has no data handle")
	}
	var out auth.RetirementOutcome
	err := m.data.Mutate(ctx, req.Tenant, func(sc store.Scope) error {
		out = auth.RetirementOutcome{}
		fact, err := auth.PinRetirement(ctx, sc, req)
		if err != nil {
			return err
		}
		holds, err := holdsNaming(ctx, sc, req)
		if err != nil {
			return err
		}
		state := auth.RetirementStoreState{Store: string(legalHoldKind), State: "clean"}
		for _, rec := range holds {
			hold := holdRefOf(rec)
			state.State = "legal_hold"
			state.Holds = append(state.Holds, auth.RetirementHoldRef{ID: hold.ID, MatterRef: hold.MatterRef})
			out.Blocking = append(out.Blocking, string(legalHoldKind)+":"+hold.ID)
		}
		out.Stores = []auth.RetirementStoreState{state}
		out.FactVersion = fact
		return nil
	})
	if err != nil {
		return auth.RetirementOutcome{}, err
	}
	return out, nil
}

// holdsNaming returns active tenant holds and subject holds naming the account
// by one of its aliases: its id, "user:<id>", its email (compared
// case-insensitively) or its external id. Released holds remain in custody but
// no longer block. Content erasers separately check each erased data class.
func holdsNaming(ctx context.Context, sc store.Scope, req auth.RetirementRequest) ([]model.Record, error) {
	repo, err := sc.Ext(legalHoldKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo, eq(colStatus, holdStatusActive))
	if err != nil {
		return nil, err
	}
	aliases := map[string]bool{req.User.String(): true, "user:" + req.User.String(): true}
	if req.Email != "" {
		aliases[strings.ToLower(req.Email)] = true
	}
	if req.ExternalID != "" {
		aliases[req.ExternalID] = true
	}
	var out []model.Record
	for _, rec := range recs {
		subject := HoldSubject{Kind: "user", Ref: req.User.String()}
		ref := strings.TrimSpace(rec.String(colSubjectRef))
		if ref != "" && (aliases[ref] || aliases[strings.ToLower(ref)]) {
			subject.Ref = rec.String(colSubjectRef)
		}
		if holdCovers(rec, subject) {
			out = append(out, rec)
		}
	}
	return out, nil
}
