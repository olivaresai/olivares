// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"errors"
	"sort"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The eventing retirement step. When a tenant removes an account, every
// subscription the account owns, itself or through one of its tokens, blocks the
// retirement, listed by id, until the tenant deletes it or it is otherwise
// resolved: the step removes nothing,
// because a subscription is a destination the tenant configured and only the
// tenant may decide to stop delivering to it. The step's transaction first pins
// the tenant's authorization epoch and the account's authority version the pass
// read, so a pass that read before a lift or a new offboard conflicts.

// retirementModule is the declared module name of this step.
const retirementModule = "eventing"

// retirementCovers are the counted columns this module's step reads.
var retirementCovers = []string{string(subscriptionKind) + "." + colSubOwnerActor}

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
		return auth.RetirementOutcome{}, errors.New("eventing: the retirement step has no data handle")
	}
	var out auth.RetirementOutcome
	err := m.data.Mutate(ctx, req.Tenant, func(sc store.Scope) error {
		out = auth.RetirementOutcome{}
		fact, err := auth.PinRetirement(ctx, sc, req)
		if err != nil {
			return err
		}
		out.FactVersion = fact
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		owned, _, err := auth.RowsNamingAccount(ctx, repo, pdeclSubscriptionOwner, colSubOwnerActor, req)
		if err != nil {
			return err
		}
		for _, rec := range owned {
			out.Blocking = append(out.Blocking, string(subscriptionKind)+":"+rec.String(model.ColID))
		}
		sort.Strings(out.Blocking)
		return nil
	})
	if err != nil {
		return auth.RetirementOutcome{}, err
	}
	return out, nil
}
