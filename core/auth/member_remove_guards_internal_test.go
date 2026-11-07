// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// guardMember creates an active account with a tenant-wide membership of role in tenant.
func guardMember(t *testing.T, ctx context.Context, st store.Store, tenant model.TenantID, email, role string) model.ID {
	t.Helper()
	var id model.ID
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Create(ctx, model.User{Email: email, Status: model.StatusActive})
		if err != nil {
			return err
		}
		id = u.ID
		_, err = as.Memberships().Create(ctx, model.Membership{UserID: u.ID, TargetTenantID: tenant, Role: role})
		return err
	}); err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	return id
}

// TestCheckMemberRemovableIsEveryGuardOfARemoval: checkMemberRemovable is the one
// check a removal runs inside its offboard transaction, whoever asks: the member
// route and the directory (SCIM) removals the Business build adds. Each refusal is
// its own error, and an allowed removal returns nil.
func TestCheckMemberRemovableIsEveryGuardOfARemoval(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		tenant := consentTenant(t, st, "acme")
		soleOwner := guardMember(t, ctx, st, tenant, "sole@acme.com", RoleOwner)
		viewer := guardMember(t, ctx, st, tenant, "viewer@acme.com", RoleViewer)

		// A SCIM bearer is a token principal: its owner is the account it was minted for.
		bearer := func(owner model.ID, role string) Principal {
			return Principal{Kind: KindToken, UserID: owner, grants: map[model.TenantID]string{tenant: role}}
		}
		confined := bearer(model.NewID(), RoleOwner).withConfinements(map[model.TenantID]model.ID{tenant: model.NewID()})

		check := func(actor Principal, target model.ID) error {
			return st.AuthView(ctx, func(as store.AuthScope) error {
				return checkMemberRemovable(ctx, as, actor, tenant, target)
			})
		}
		for _, tc := range []struct {
			name   string
			actor  Principal
			target model.ID
			want   error
		}{
			{"workspace-confined owner token", confined, viewer, ErrWorkspaceConfined},
			{"token owned by the target", bearer(viewer, RoleOwner), viewer, ErrSelfRemoval},
			{"admin token removing the owner", bearer(model.NewID(), RoleAdmin), soleOwner, ErrRoleCeiling},
			{"owner token removing the last active owner", bearer(model.NewID(), RoleOwner), soleOwner, ErrLastOwner},
			{"admin token removing a viewer", bearer(model.NewID(), RoleAdmin), viewer, nil},
		} {
			if err := check(tc.actor, tc.target); !errors.Is(err, tc.want) {
				t.Errorf("%s: checkMemberRemovable = %v, want %v", tc.name, err, tc.want)
			}
		}

		// A second active owner makes the first removable by an owner.
		guardMember(t, ctx, st, tenant, "second@acme.com", RoleOwner)
		if err := check(bearer(model.NewID(), RoleOwner), soleOwner); err != nil {
			t.Errorf("owner token removing an owner while another is active: checkMemberRemovable = %v, want nil", err)
		}
	})
}
