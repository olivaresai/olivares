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

// Group reads and the operator-only group→role mapping Community keeps. A group's
// MappedRole is NEVER writable through an inbound directory path: only a tenant
// operator (ConfigureGroupRole, ceiling-checked) decides what role a roster confers,
// and a mapping only ELEVATES an existing direct membership (see loadGrants): group
// membership alone grants no access in the target tenant. Every query filters
// target_tenant_id == tenant, and a group of another tenant is indistinguishable
// from one that does not exist. Creating, replacing, deleting, nesting and placing
// groups is the Business engine.

// ErrInvalidGroupRole and ErrGroupCycle are the group refusals Community maps.
var (
	// ErrInvalidGroupRole means a group→role mapping names an unknown role.
	ErrInvalidGroupRole = errors.New("auth: unknown role for group mapping")
	// ErrGroupCycle means a group-nesting change would make a group its own
	// ancestor (a self-parent or a longer cycle). The persisted group graph is
	// always a forest — loadGrants' chain walk stops a cycle defensively, but one
	// must never reach the store. The handler maps it to 409/400.
	ErrGroupCycle = errors.New("auth: group nesting would create a cycle")
)

// SCIMGroup is a stored group with its resolved member users. SkippedMembers
// counts input members that were NOT applied because they are not members of
// the bound tenant (including users of other tenants — same count, no oracle).
type SCIMGroup struct {
	Group          model.UserGroup
	Members        []model.User
	SkippedMembers int
}

// SCIMListGroups returns the tenant's groups (the SCIM resource set for that
// connection) with their resolved members.
func (a *Authenticator) SCIMListGroups(ctx context.Context, tenant model.TenantID) ([]SCIMGroup, error) {
	var out []SCIMGroup
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		gs, err := drainList(ctx, as.Groups().List, byEq("target_tenant_id", tenant.String(), 0))
		if err != nil {
			return err
		}
		for _, g := range gs {
			members, err := groupMemberUsers(ctx, as, g.ID)
			if err != nil {
				return err
			}
			out = append(out, SCIMGroup{Group: g, Members: members})
		}
		return nil
	})
	return out, err
}

// ConfigureGroupRole sets (or, with role "", clears) the built-in role a
// group's members are elevated to in the group's tenant — the OPERATOR path
// (PUT /v1/groups/{id}/role), never reachable through SCIM. The ceiling is
// checked against the group's STORED TargetTenantID, never the caller-supplied
// tenant (which is only the visibility filter): an actor cannot launder a
// mapping into a tenant where it outranks itself by lying about the tenant.
// Clearing needs no ceiling — it only narrows.
func (a *Authenticator) ConfigureGroupRole(ctx context.Context, actor Principal, tenant model.TenantID, groupID model.ID, role string) (model.UserGroup, error) {
	var out model.UserGroup
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		g, err := groupInTenant(ctx, as, tenant, groupID)
		if err != nil {
			return err
		}
		// MappedRole is locally managed for every origin. The provisioner owns
		// the group's identity and roster; this writer changes neither.
		if role != "" {
			if !IsRole(role) {
				return ErrInvalidGroupRole
			}
			if err := checkRoleCeiling(actor, g.TargetTenantID, role); err != nil {
				return err
			}
		}
		g.MappedRole = role
		if g, err = as.Groups().Update(ctx, g); err != nil {
			return err
		}
		out = g
		return metaAudit(ctx, as, actor, "scim.group.role.map", "core.user_group", g.ID,
			map[string]any{"role": role})
	})
	if err != nil {
		return model.UserGroup{}, err
	}
	return out, nil
}

// readOnlyGroupAncestors follows the same tenant, dangling-edge and cycle
// boundaries as loadGroupClosure and collects origins this writer cannot change.
// The operator can write NULL/operator ancestry; SCIM can write NULL ancestry.
func readOnlyGroupAncestors(ctx context.Context, as store.AuthScope, tenant model.TenantID, parentID model.ID, writer string) (map[model.ID]bool, error) {
	claims := map[model.ID]bool{}
	seen := map[model.ID]bool{}
	for !parentID.IsZero() && !seen[parentID] {
		seen[parentID] = true
		g, err := as.Groups().Get(ctx, parentID)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		if g.TargetTenantID != tenant {
			break
		}
		if !mayWriteGroupOrigin(g.ProvisionedBy, writer) {
			claims[g.ID] = true
		}
		parentID = g.ParentGroupID
	}
	return claims, nil
}

// groupInTenant loads a group and enforces the tenant column filter:
// ErrNotFound for a group of another tenant, indistinguishable from a missing
// one (no cross-tenant oracle).
func groupInTenant(ctx context.Context, as store.AuthScope, tenant model.TenantID, id model.ID) (model.UserGroup, error) {
	g, err := as.Groups().Get(ctx, id)
	if err != nil {
		return model.UserGroup{}, err
	}
	if g.TargetTenantID != tenant {
		return model.UserGroup{}, store.ErrNotFound
	}
	return g, nil
}

// groupMemberRows lists a group's member rows COMPLETELY (drainList): the
// member diff in SCIMReplaceGroup and the encode path both depend on seeing
// every row — a truncated read would silently drop removals past the store's
// page cap (a >1000-member "all employees" group is exactly the role-mapped
// use case) and re-classify surviving members as adds.
func groupMemberRows(ctx context.Context, as store.AuthScope, groupID model.ID) ([]model.UserGroupMember, error) {
	return drainList(ctx, as.GroupMembers().List, byEq("group_id", groupID.String(), 0))
}

// groupMemberUsers resolves a group's member rows to users, skipping dangling
// rows (a deleted user leaves its row behind until deprovision sweeps it).
func groupMemberUsers(ctx context.Context, as store.AuthScope, groupID model.ID) ([]model.User, error) {
	rows, err := groupMemberRows(ctx, as, groupID)
	if err != nil {
		return nil, err
	}
	ids := make([]model.ID, len(rows))
	for i, r := range rows {
		ids[i] = r.UserID
	}
	return usersByID(ctx, as, ids)
}

// usersByID resolves user ids, skipping dangling ones.
func usersByID(ctx context.Context, as store.AuthScope, ids []model.ID) ([]model.User, error) {
	var out []model.User
	for _, id := range ids {
		u, err := as.Users().Get(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}
