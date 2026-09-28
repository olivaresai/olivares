// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The FinOps retirement step. When a tenant removes an account, this step removes
// nothing and reports, by id, what the tenant itself must resolve:
//   - a policy whose spec names the account through a counted declaration (a
//     spend limit scoped to the account) blocks the retirement until the tenant
//     deletes it: the limit is the tenant's own spending decision;
//   - an attempt not yet terminal whose binding or target set names the account
//     blocks it until the attempt settles, is released or expires; a terminal
//     attempt holds nothing and is kept as the record of what happened;
//   - a stored policy whose kind the policy-kind registry does not know blocks it
//     too. The whole policy table is read for these, not only the kinds this
//     module writes.
// The step's transaction first pins the tenant's authorization epoch and the
// account's authority version the pass read, so a pass that read before a lift or
// a new offboard conflicts.

// retirementModule is the declared module name of this step.
const retirementModule = "finops"

// corePolicyKind is the core policy entity, whose spec this step reads.
const corePolicyKind model.Kind = "core.policy"

// retirementCovers are the counted columns this module's step reads.
var retirementCovers = []string{
	string(corePolicyKind) + ".spec",
	string(attemptKind) + "." + colAttemptBinding,
	string(attemptKind) + "." + colAttemptTargets,
}

// policySpecDecl is the core policy spec's declaration: the variant the policy's
// kind selects from the policy-kind registry, read as the write seam reads it.
var policySpecDecl = model.Union("kind", model.PolicyKinds)

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
		return auth.RetirementOutcome{}, errors.New("finops: the retirement step has no data handle")
	}
	var out auth.RetirementOutcome
	err := m.data.Mutate(ctx, req.Tenant, func(sc store.Scope) error {
		out = auth.RetirementOutcome{}
		fact, err := auth.PinRetirement(ctx, sc, req)
		if err != nil {
			return err
		}
		policies, unknownPolicy, err := policiesNaming(ctx, sc, req.User)
		if err != nil {
			return err
		}
		attempts, unknownAttempt, err := attemptsNaming(ctx, sc, req.User)
		if err != nil {
			return err
		}
		out.Blocking = append(policies, attempts...)
		if unknownPolicy {
			out.UnknownKinds = append(out.UnknownKinds, string(corePolicyKind))
		}
		if unknownAttempt {
			out.UnknownKinds = append(out.UnknownKinds, string(attemptKind))
		}
		out.FactVersion = fact
		return nil
	})
	if err != nil {
		return auth.RetirementOutcome{}, err
	}
	return out, nil
}

// policiesNaming returns, as blocking references, the policies whose spec names
// user through a counted declaration, and whether the policy table holds a row
// whose kind the registry does not know. It reads every policy, whatever its kind,
// and prefers the stored spec text, which is what the write seam reads.
func policiesNaming(ctx context.Context, sc store.Scope, user model.ID) ([]string, bool, error) {
	var (
		blocking []string
		unknown  bool
	)
	check := func(id model.ID, kind string, spec any) error {
		if _, known := model.PolicyKinds.Variant(kind); !known {
			unknown = true
			return nil
		}
		rec := model.Record{"kind": kind}
		if spec != nil {
			rec["spec"] = spec
		}
		ids, err := policySpecDecl.CountedUserIDs(rec, "spec")
		if errors.Is(err, model.ErrUnknownKind) {
			unknown = true
			return nil
		}
		if err != nil {
			return err
		}
		for _, n := range ids {
			if n == user {
				blocking = append(blocking, string(corePolicyKind)+":"+id.String())
				break
			}
		}
		return nil
	}
	q := model.Query{Limit: listCap}
	if snaps, ok := sc.Policies().(store.PolicySnapshotRepository); ok {
		for {
			rows, page, err := snaps.ListPolicySnapshots(ctx, q)
			if err != nil {
				return nil, false, err
			}
			for _, p := range rows {
				var spec any
				if p.Spec != nil {
					spec = *p.Spec
				}
				if err := check(p.ID, p.Kind, spec); err != nil {
					return nil, false, err
				}
			}
			if !page.HasMore || page.Cursor == "" {
				return blocking, unknown, nil
			}
			q.Cursor = page.Cursor
		}
	}
	for {
		rows, page, err := sc.Policies().List(ctx, q)
		if err != nil {
			return nil, false, err
		}
		for _, p := range rows {
			var spec any
			if p.Spec != nil {
				spec = p.Spec
			}
			if err := check(p.ID, p.Kind, spec); err != nil {
				return nil, false, err
			}
		}
		if !page.HasMore || page.Cursor == "" {
			return blocking, unknown, nil
		}
		q.Cursor = page.Cursor
	}
}

// attemptsNaming returns, as blocking references, the attempts not yet terminal
// whose binding or target set names user, and whether the attempt table holds a
// row whose kind no registry knows. A phase outside the closed vocabulary is not
// terminal.
func attemptsNaming(ctx context.Context, sc store.Scope, user model.ID) ([]string, bool, error) {
	repo, err := sc.Ext(attemptKind)
	if err != nil {
		return nil, false, err
	}
	var (
		blocking []string
		unknown  bool
	)
	seen := map[string]bool{}
	for _, c := range []struct {
		column string
		decl   *model.ColumnDecl
	}{{colAttemptBinding, pdeclAttemptBinding}, {colAttemptTargets, pdeclAttemptTargets}} {
		named, u, err := auth.RowsNaming(ctx, repo, c.decl, c.column, user)
		if err != nil {
			return nil, false, err
		}
		unknown = unknown || u
		for _, rec := range named {
			id := rec.String(model.ColID)
			phase := AttemptPhase(rec.String(colAttemptPhase))
			if seen[id] || (knownPhase(phase) && !heldPhase(phase)) {
				continue
			}
			seen[id] = true
			blocking = append(blocking, string(attemptKind)+":"+id)
		}
	}
	return blocking, unknown, nil
}
