// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A spend limit scoped to a user is that account's own allowance, so its writer is
// fenced: it reads the named account's standing through the module's standing
// port and opens its transaction by pinning that account's authority version with
// the tenant's directory epoch (auth.FencedWrite). The writer runs outside a
// request, so the composition hands the module its port at boot; without one, a
// write that names an account is refused.

var _ auth.StandingConsumer = (*Module)(nil)

// UseStanding receives the standing port from the composition, before Start.
func (m *Module) UseStanding(r auth.StandingReader) { m.standing = r }

// fencedMutate runs write in tenant's transaction as a fenced write over
// subjects: the named accounts' authority versions are pinned first, and none is
// pinned when no subject names an account.
func (m *Module) fencedMutate(ctx context.Context, tenant model.TenantID, subjects []model.ID, write func(store.Scope) error) error {
	mutate := func(fn func(store.Scope) error) error { return m.mutate(ctx, tenant, fn) }
	return auth.FencedWrite(ctx, m.standing, tenant, subjects, auth.FenceDirectory, mutate,
		func(sc store.Scope, _ bool) error { return write(sc) })
}

// spendLimitSubjects returns the account a normalized spend-limit spec names: the
// scope key of a user-scoped limit, when it is "user:<id>" with a canonical id.
// The fence skips an id that names no account. A credential-scoped key
// ("token:<id>") names one of an account's tokens, which the fence does not
// resolve to its account: a token bound to the tenant is revoked when the
// tenant removes its owner or the account it acts as (core/auth/tenant_authority.go),
// so no such limit can apply to a removed member there, and a token bound to no
// tenant is a superadmin's (core/model/auth.go), whose authority does not come
// from a membership a tenant could remove.
func spendLimitSubjects(s storedSpendLimitSpec) []model.ID {
	if s.ScopeType != "user" {
		return nil
	}
	rest, ok := strings.CutPrefix(s.ScopeKey, "user:")
	if !ok {
		return nil
	}
	id, err := model.ParseID(rest)
	if err != nil || id.IsZero() {
		return nil
	}
	return []model.ID{id}
}

// mutate is the module's ONE write funnel (CUTS B2): every successful commit
// invalidates the tenant's HasAdmissionTargets memo, so a budget, spend-limit or
// lifecycle write takes effect on the very next launch probe. Handlers reach it
// as m.mutate(r.Context(), mc.Tenant, fn) exactly where they used mc.Data.Mutate.
func (m *Module) mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := m.data.Mutate(ctx, tenant, fn)
	if err == nil {
		m.targetsCache.Delete(tenant)
	}
	return err
}
