// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The account reads and helpers the Community sign-in, member removal and session
// paths share. The inbound SCIM server's provisioning engine (create, update and
// deprovision a user) is Business.

// Compound callbacks declare their complete H set before their first G write
// or audit. Repeating that set in a shared helper cannot acquire a later H.
func prepareUserAuthorityWrite(ctx context.Context, as store.AuthScope, ids ...model.ID) error {
	writer, ok := as.(store.AuthUserAuthorityWriter)
	if !ok {
		return store.ErrDirectoryUnavailable
	}
	return writer.PrepareUserAuthorityWrite(ctx, ids)
}

// SCIMGetMember returns a tenant member by id, or ErrNotFound when the user does
// not exist OR is not a member of tenant (so a SCIM token cannot probe the global
// user table — the not-found==not-a-member rule mirrors the cross-tenant oracle
// guard).
func (a *Authenticator) SCIMGetMember(ctx context.Context, tenant model.TenantID, id model.ID) (model.User, error) {
	var out model.User
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, id)
		if err != nil {
			return err
		}
		if _, ok, err := membershipOf(ctx, as, id, tenant); err != nil {
			return err
		} else if !ok {
			return store.ErrNotFound
		}
		out = u
		return nil
	})
	return out, err
}

// membershipOf returns the user's membership in tenant and whether one exists.
func membershipOf(ctx context.Context, as store.AuthScope, userID model.ID, tenant model.TenantID) (model.Membership, bool, error) {
	ms, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{
		{Column: "user_id", Op: model.OpEq, Value: userID.String()},
		{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
	}, Limit: 1})
	if err != nil {
		return model.Membership{}, false, err
	}
	if len(ms) == 0 {
		return model.Membership{}, false, nil
	}
	return ms[0], true, nil
}
