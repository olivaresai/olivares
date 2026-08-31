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

// RosterMember is one row of a tenant's member roster: a user together with the
// tenant-resident facts a console operator manages them by. It is the enriched
// counterpart of SCIMListMembers (which returns bare users): the console members
// grid needs the ROLE — which lives on the membership, not the user — plus the
// workspace scoping and the directory groups, none of which the SCIM user shape
// carries.
type RosterMember struct {
	// User is the account record (email, display name, status, external id).
	User model.User
	// Role is the effective built-in role for display: the HIGHEST-rank role across
	// every membership the user holds in the tenant (a user may hold a tenant-wide
	// row and workspace-scoped rows at once).
	Role string
	// WorkspaceIDs are the workspace-scoped membership targets; empty means
	// the user's membership is tenant-wide (acts across every workspace).
	WorkspaceIDs []model.ID
	// Groups are the display names of the tenant directory groups the user belongs
	// to (SCIM-provisioned rosters). Empty when the user is in no group.
	Groups []string
}

// TenantRoster returns the active member roster of tenant: every user with a
// membership in it, enriched with their effective (highest-rank) role, any
// workspace scoping, and the directory groups they belong to. It is the read the
// console members grid consumes.
//
// The roster is membership-based — the same "who is a member of this tenant" set
// as SCIMListMembers — so a directory-group-only principal with no membership row
// does not appear (consistent, and never a fabricated membership). A membership
// whose user record is gone is skipped, not invented.
//
// SECURITY: this returns the tenant's full user set (reconnaissance-sensitive PII),
// so the CALLER MUST have gated a tenant-scoped read (user:read) on tenant before
// calling. The method itself performs no authorization; it is a system-tenant join
// over the auth store (memberships and groups all live in the system tenant and are
// isolated only by their target_tenant_id column, so every list here filters on it).
func (a *Authenticator) TenantRoster(ctx context.Context, tenant model.TenantID) ([]RosterMember, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return nil, nil
	}
	var out []RosterMember
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		// Drained, not single-page: the roster must be complete (a truncated grid
		// silently hides members). Every membership in the tenant, aggregated per
		// user so one row carries the effective role and all workspace scopes.
		ms, err := drainList(ctx, as.Memberships().List, byEq("target_tenant_id", tenant.String(), 0))
		if err != nil {
			return err
		}
		idx := make(map[model.ID]int, len(ms)) // userID -> index into out
		for _, m := range ms {
			pos, seen := idx[m.UserID]
			if !seen {
				u, uerr := as.Users().Get(ctx, m.UserID)
				if uerr != nil {
					if errors.Is(uerr, store.ErrNotFound) {
						continue // membership whose user was removed — skip, never fabricate
					}
					return uerr
				}
				out = append(out, RosterMember{User: u, Role: m.Role})
				pos = len(out) - 1
				idx[m.UserID] = pos
			} else if RoleRank(m.Role) > RoleRank(out[pos].Role) {
				out[pos].Role = m.Role
			}
			if !m.WorkspaceID.IsZero() {
				out[pos].WorkspaceIDs = append(out[pos].WorkspaceIDs, m.WorkspaceID)
			}
		}
		// Attach directory groups: invert each group's member set into user->groups.
		// Only users already in the roster (membership-backed) receive group labels.
		gs, err := drainList(ctx, as.Groups().List, byEq("target_tenant_id", tenant.String(), 0))
		if err != nil {
			return err
		}
		for _, g := range gs {
			members, gerr := groupMemberUsers(ctx, as, g.ID)
			if gerr != nil {
				return gerr
			}
			for _, mu := range members {
				if pos, ok := idx[mu.ID]; ok {
					out[pos].Groups = append(out[pos].Groups, g.DisplayName)
				}
			}
		}
		return nil
	})
	return out, err
}
