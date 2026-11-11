// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var (
	// ErrSelfRemoval means an administrator asked to remove its own membership. The API
	// maps it to 409: it is a request the caller can still make about someone else.
	ErrSelfRemoval = errors.New("auth: a member cannot remove itself from the organization")
	// ErrLastOwner means the removal would leave the tenant without an active owner. The
	// API maps it to 409: make another member an owner first.
	ErrLastOwner = errors.New("auth: the organization's last active owner cannot be removed")
)

// RemoveMember takes a member out of tenant: the scoped offboard of its membership,
// recorded as an administrator's removal (memberRemoveCause), never as a directory
// event. The account itself, its other organizations and its account-scope sessions
// are untouched, as in every scoped offboard.
//
// The checks (checkMemberRemovable) run in the offboard's own transaction, under the
// directory writer lock, so two removals cannot each see the other owner still there:
//
//   - a workspace-confined caller is refused (ErrWorkspaceConfined), as onboarding
//     and the TOTP reset do: removing a member is a tenant-wide act;
//   - the caller cannot remove itself (ErrSelfRemoval);
//   - the caller cannot remove a member above its own role (ErrRoleCeiling), the
//     ceiling a demotion already has. The member's role is its effective one, the
//     membership's raised by a group's mapped role; a superadmin outranks every role;
//   - the tenant keeps an active owner (ErrLastOwner). Only a tenant-wide membership
//     with the owner role counts: an owner whose account the deployment suspended, or
//     who is confined to a workspace, cannot run the organization. A member a group
//     maps to owner does not count either, which can only refuse more, never less.
//
// An id that is not a member of tenant is store.ErrNotFound, and a second removal too.
func (a *Authenticator) RemoveMember(ctx context.Context, actor Principal, tenant model.TenantID, id model.ID) error {
	if tenant.IsZero() || tenant.IsSystem() {
		return ErrInvalidToken
	}
	// Answered before the membership read, so a confined caller learns nothing of who
	// is a member; checkMemberRemovable asks again under the lock.
	if _, confined := actor.ConfinedWorkspaceIn(tenant); confined {
		return ErrWorkspaceConfined
	}
	// An id that is no member of tenant answers before any writer lock is taken: the
	// directory refuses to lock an account it holds no authority row for.
	if _, err := a.SCIMGetMember(ctx, tenant, id); err != nil {
		return err
	}
	var left bool
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		left = false
		if err := prepareUserAuthorityWrite(ctx, as, id); err != nil {
			return err
		}
		// The preflight may have raced a departure; the lock holds the answer until commit.
		if _, member, err := membershipOf(ctx, as, id, tenant); err != nil {
			return err
		} else if !member {
			return store.ErrNotFound
		}
		if err := checkMemberRemovable(ctx, as, actor, tenant, id); err != nil {
			return err
		}
		if _, err := a.scopedOffboard(ctx, as, actor, id, tenant, memberRemoveCause); err != nil {
			return err
		}
		left = true
		return nil
	})
	if err == nil && left {
		a.recordLeaver(ctx, actor, tenant, id, memberRemoveCause)
	}
	return err
}

// checkMemberRemovable applies every guard of a removal to a member of tenant: the
// workspace confinement, self-removal, the role ceiling and the last active owner.
// It is the one check a principal's removal of a member runs inside its offboard
// transaction, whichever route asks: the member route and the Business build's
// directory (SCIM) DELETE and active=false, which an IdP sends with a bearer token
// holding the same user:write permission. A caller that offboards a member for a
// principal without it skips the guards (docs/contracts-identity.md).
func checkMemberRemovable(ctx context.Context, as store.AuthScope, actor Principal, tenant model.TenantID, id model.ID) error {
	if _, confined := actor.ConfinedWorkspaceIn(tenant); confined {
		return ErrWorkspaceConfined
	}
	if actor.UserID == id {
		return ErrSelfRemoval
	}
	target, err := as.Users().Get(ctx, id)
	if err != nil {
		return err
	}
	grants, _, _, err := loadGrants(ctx, as, id, target.IsSuperadmin)
	if err != nil {
		return err
	}
	role := grants[tenant]
	ceiling := role
	if target.IsSuperadmin {
		ceiling = RoleOwner
	}
	if err := checkRoleCeiling(actor, tenant, ceiling); err != nil {
		return err
	}
	if role != RoleOwner {
		return nil
	}
	owners, err := drainList(ctx, as.Memberships().List, model.Query{Filters: []model.Filter{
		{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
		{Column: "role", Op: model.OpEq, Value: RoleOwner},
	}})
	if err != nil {
		return err
	}
	for _, m := range owners {
		if m.UserID == id || !m.WorkspaceID.IsZero() {
			continue
		}
		other, err := as.Users().Get(ctx, m.UserID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if other.Status != model.StatusActive {
			continue
		}
		if suspended, err := suspensionInForce(ctx, as, m.UserID, tenant); err != nil {
			return err
		} else if !suspended {
			return nil
		}
	}
	return ErrLastOwner
}

// suspensionInForce reports whether tenant has suspended the account: a suspended
// owner cannot run the organization, so it never counts as the active owner a
// removal must leave behind.
func suspensionInForce(ctx context.Context, as store.AuthScope, user model.ID, tenant model.TenantID) (bool, error) {
	rows, err := drainList(ctx, as.TenantExclusions().List, byEq("user_id", user.String(), 0))
	if err != nil {
		return false, err
	}
	for _, x := range rows {
		if x.Kind == model.ExclusionSuspend && x.TargetTenantID == tenant {
			return true, nil
		}
	}
	return false, nil
}

// liftSuspension deletes the suspension exclusions of user in tenant, if any.
func liftSuspension(ctx context.Context, as store.AuthScope, user model.ID, tenant model.TenantID) error {
	rows, err := drainList(ctx, as.TenantExclusions().List, byEq("user_id", user.String(), 0))
	if err != nil {
		return err
	}
	for _, x := range rows {
		if x.Kind == model.ExclusionSuspend && x.TargetTenantID == tenant {
			if err := as.TenantExclusions().Delete(ctx, x.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
