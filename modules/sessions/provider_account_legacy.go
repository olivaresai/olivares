// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sort"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// AdoptUnnamedProfiles names the profiles that predate account names, so a person
// picks them by name like any other account and the first Claude profile is the
// one called `claude`. It is run by the active writer at boot, for one tenant.
//
// It adopts every ACTIVE unnamed profile of this environment whose driver is a
// session tool, oldest first, each through AdoptProviderAccount with a generated
// name: the same transaction, the same per-tenant admission lock, the same unique
// index and the same `adopt` audit event as the operator's own adopt. It touches
// no file and moves no home, so nothing the launch digest covers changes.
//
// It is idempotent. A profile that has a name is never listed, so a second run
// finds nothing; a profile that is named, retired or gone between the read and
// its turn is skipped. It returns how many profiles this run named. A refusal on
// one profile does not stop the others; the refusals are joined in the error.
func (m *Module) AdoptUnnamedProfiles(ctx context.Context, tenant model.TenantID) (int, error) {
	if m.Data == nil {
		return 0, errNoData
	}
	env, err := m.localEnvironment()
	if err != nil {
		// A node with no environment identity launches nothing, so no profile is its own to name.
		if m.log != nil {
			m.log.Debug("sessions: no execution environment identity on this node; legacy provider profiles stay unnamed", "error", err)
		}
		return 0, nil
	}
	refs, err := m.unnamedActiveProfileRefs(ctx, tenant, env)
	if err != nil {
		return 0, err
	}
	named := 0
	var refusals []error
	for _, ref := range refs {
		_, err := m.AdoptProviderAccount(ctx, auth.Principal{}, tenant, ref, "")
		switch {
		case err == nil:
			named++
		case errors.Is(err, ErrAccountAlreadyNamed), errors.Is(err, ErrProfileRetired), errors.Is(err, ErrProfileNotFound):
		default:
			refusals = append(refusals, err)
		}
	}
	return named, errors.Join(refusals...)
}

// unnamedActiveProfileRefs lists, oldest first, the references of the active
// profiles of this environment that carry no account name and belong to a session tool.
func (m *Module) unnamedActiveProfileRefs(ctx context.Context, tenant model.TenantID, env string) ([]string, error) {
	type unnamed struct{ ref, createdAt, id string }
	var found []unnamed
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		q := model.Query{
			Filters: []model.Filter{eq(colPPEnvRef, env), eq(colPPState, ProfileActive), {Column: colPPAccountName, Op: model.OpIsNull}},
			Limit:   accountNamePageSize,
		}
		for {
			recs, page, err := repo.List(ctx, q)
			if err != nil {
				return err
			}
			for _, rec := range recs {
				if facts, ok := driverfacts.Lookup(rec.String(colPPDriver)); ok && facts.Session {
					found = append(found, unnamed{rec.String(colPPRef), rec.String(model.ColCreatedAt), rec.String(model.ColID)})
				}
			}
			if !page.HasMore || page.Cursor == "" {
				return nil
			}
			q.Cursor = page.Cursor
		}
	})
	if err != nil {
		return nil, err
	}
	// Pages come in key order; creation time is the order that names them.
	sort.Slice(found, func(i, j int) bool {
		if found[i].createdAt != found[j].createdAt {
			return found[i].createdAt < found[j].createdAt
		}
		return found[i].id < found[j].id
	})
	refs := make([]string, 0, len(found))
	for _, f := range found {
		refs = append(refs, f.ref)
	}
	return refs, nil
}
