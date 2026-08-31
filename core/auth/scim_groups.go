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

// SCIM group provisioning (RFC 7643 §4.2 / RFC 7644) — the engine-side store
// operations the SCIM /Groups handler drives, plus the operator-only group→role
// mapping. Like the user half (scim.go), a SCIM connection is bound to ONE
// tenant, and group rows live in the auth partition under the system tenant
// with the granted tenant as a column (model.UserGroup.TargetTenantID). RLS
// therefore does NOT separate one business tenant's groups from another's here
// — every query in this file filters target_tenant_id == tenant, and a group
// that exists in another tenant is indistinguishable from one that does not
// exist (not-found == not-in-tenant, the same cross-tenant oracle guard as
// SCIMGetMember).
//
// The privilege boundary, in two deny-closed rules:
//   - MappedRole is NEVER writable through any SCIM inbound path. The IdP
//     pushes rosters; only a tenant operator (ConfigureGroupRole, ceiling-
//     checked) decides what role a roster confers. Otherwise the directory
//     team, not the tenant owner, would mint owners.
//   - A mapping only ELEVATES an existing direct membership (see loadGrants):
//     group membership alone grants no access in the target tenant.
//
// Members that are not members of the bound tenant are SKIPPED, not rejected:
// a hard 400 would wedge an IdP's batched PATCH cycles (Entra retries the whole
// batch), while skipping grants nothing. Skips are counted in the result and in
// the audit meta so the divergence is never silent.

// Group provisioning errors. The handler maps them to SCIM responses
// (invalidValue 400 / uniqueness 409 / 403).
var (
	// ErrInvalidScimGroup means a SCIM group resource is missing its required
	// displayName.
	ErrInvalidScimGroup = errors.New("auth: invalid SCIM group (displayName required)")
	// ErrInvalidGroupRole means a group→role mapping names an unknown role.
	ErrInvalidGroupRole = errors.New("auth: unknown role for group mapping")
	// ErrGroupVersionChanged means a replace carried an expectVersion that no
	// longer matches the stored row: the caller folded its changes over a state
	// another write replaced in between (the PATCH read-fold-write spans two
	// transactions). The caller re-reads and re-folds rather than silently
	// overwriting the concurrent write.
	ErrGroupVersionChanged = errors.New("auth: group changed since it was read")
	// ErrGroupCycle means a group-nesting change would make a group its own
	// ancestor (a self-parent or a longer cycle). The persisted group graph is
	// always a forest — loadGrants' chain walk stops a cycle defensively, but one
	// must never reach the store. The handler maps it to 409/400.
	ErrGroupCycle = errors.New("auth: group nesting would create a cycle")
)

// SCIMGroupInput is the attribute set a SCIM group create/replace carries.
// There is deliberately no role field: see model.UserGroup.MappedRole.
type SCIMGroupInput struct {
	// DisplayName is the SCIM displayName (required).
	DisplayName string
	// ExternalID is the IdP's stable id (SCIM externalId); optional. Entra always
	// sends one (and legally reuses displayNames); Okta may omit it.
	ExternalID string
	// Members are the member user ids (each must already be a member of the
	// bound tenant; others are skipped and counted).
	Members []model.ID
}

// SCIMGroup is a stored group with its resolved member users. SkippedMembers
// counts input members that were NOT applied because they are not members of
// the bound tenant (including users of other tenants — same count, no oracle).
type SCIMGroup struct {
	Group          model.UserGroup
	Members        []model.User
	SkippedMembers int
}

// SCIMCreateGroup creates a group in tenant. Dedupe is application-level (the
// table has no unique index — Entra legally pushes duplicate displayNames): a
// second group with the same externalId, or — when no externalId is sent (the
// Okta race path) — the same displayName, surfaces as store.ErrConflict (the
// handler maps it to SCIM 409 uniqueness). MappedRole is always "" on create.
func (a *Authenticator) SCIMCreateGroup(ctx context.Context, actor Principal, tenant model.TenantID, in SCIMGroupInput) (SCIMGroup, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return SCIMGroup{}, ErrInvalidToken
	}
	if in.DisplayName == "" {
		return SCIMGroup{}, ErrInvalidScimGroup
	}
	var out SCIMGroup
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		dedupeCol, dedupeVal := "external_id", in.ExternalID
		if in.ExternalID == "" {
			dedupeCol, dedupeVal = "display_name", in.DisplayName
		}
		if _, ok, err := groupBy(ctx, as, tenant, dedupeCol, dedupeVal); err != nil {
			return err
		} else if ok {
			return store.ErrConflict
		}
		g, err := as.Groups().Create(ctx, model.UserGroup{
			TargetTenantID: tenant, DisplayName: in.DisplayName, ExternalID: in.ExternalID,
		})
		if err != nil {
			return err
		}
		valid, skipped, err := validMembers(ctx, as, tenant, in.Members)
		if err != nil {
			return err
		}
		for _, uid := range valid {
			if _, err := as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: g.ID, UserID: uid}); err != nil {
				return err
			}
		}
		members, err := usersByID(ctx, as, valid)
		if err != nil {
			return err
		}
		out = SCIMGroup{Group: g, Members: members, SkippedMembers: skipped}
		return metaAudit(ctx, as, actor, "scim.group.create", "core.user_group", g.ID,
			map[string]any{"members": len(valid), "skipped_members": skipped})
	})
	if err != nil {
		return SCIMGroup{}, err
	}
	return out, nil
}

// SCIMGetGroup returns a tenant's group with its resolved members, or
// ErrNotFound when the group does not exist OR belongs to another tenant.
func (a *Authenticator) SCIMGetGroup(ctx context.Context, tenant model.TenantID, id model.ID) (SCIMGroup, error) {
	var out SCIMGroup
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		g, err := groupInTenant(ctx, as, tenant, id)
		if err != nil {
			return err
		}
		members, err := groupMemberUsers(ctx, as, g.ID)
		if err != nil {
			return err
		}
		out = SCIMGroup{Group: g, Members: members}
		return nil
	})
	if err != nil {
		return SCIMGroup{}, err
	}
	return out, nil
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

// SCIMReplaceGroup applies a full attribute set to a tenant's group (PUT, and
// the handler-side PATCH after it folds operations into the full resource):
// DisplayName, ExternalID and the member set are replaced; MappedRole is
// PRESERVED — it never travels an inbound SCIM path. Member validation is as in
// Create (skip-and-count); removing an absent member is idempotent.
//
// expectVersion is the optimistic-concurrency guard for callers whose fold
// happened OUTSIDE this transaction (the PATCH handler reads, folds in memory,
// then replaces): a non-zero value that no longer matches the stored row
// returns ErrGroupVersionChanged so the caller re-reads and re-folds instead of
// silently erasing a concurrent write. Zero skips the check (a PUT carries the
// IdP's full intended state — last writer wins by design).
//
// Vertical-privesc guard: when the group carries a role mapping, ADDING a
// member is granting that role, so any net-new member requires the ACTOR to
// pass the role ceiling for the mapped role (ErrRoleCeiling → 403) BEFORE
// anything is written. Removals and attribute edits never need it (they only
// narrow).
func (a *Authenticator) SCIMReplaceGroup(ctx context.Context, actor Principal, tenant model.TenantID, id model.ID, in SCIMGroupInput, expectVersion int64) (SCIMGroup, error) {
	if in.DisplayName == "" {
		return SCIMGroup{}, ErrInvalidScimGroup
	}
	var out SCIMGroup
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		g, err := groupInTenant(ctx, as, tenant, id)
		if err != nil {
			return err
		}
		if expectVersion != 0 && g.Version != expectVersion {
			return ErrGroupVersionChanged
		}
		// Rename dedupe, mirroring Create: a replace must not steal another
		// group's correlation key. The externalId collision is also backstopped
		// by the unique index; the displayName probe covers the no-externalId
		// (Okta) family only — Entra duplicates names legally.
		if in.ExternalID != "" && in.ExternalID != g.ExternalID {
			if other, ok, err := groupBy(ctx, as, tenant, "external_id", in.ExternalID); err != nil {
				return err
			} else if ok && other.ID != g.ID {
				return store.ErrConflict
			}
		}
		if in.ExternalID == "" && g.ExternalID == "" && in.DisplayName != g.DisplayName {
			if other, ok, err := groupBy(ctx, as, tenant, "display_name", in.DisplayName); err != nil {
				return err
			} else if ok && other.ID != g.ID {
				return store.ErrConflict
			}
		}
		rows, err := groupMemberRows(ctx, as, g.ID)
		if err != nil {
			return err
		}
		current := make(map[model.ID]model.UserGroupMember, len(rows))
		for _, r := range rows {
			current[r.UserID] = r
		}
		valid, skipped, err := validMembers(ctx, as, tenant, in.Members)
		if err != nil {
			return err
		}
		keep := make(map[model.ID]bool, len(valid))
		var adds []model.ID
		for _, uid := range valid {
			keep[uid] = true
			if _, ok := current[uid]; !ok {
				adds = append(adds, uid)
			}
		}
		// The ceiling, before any write: a net-new member of a role-mapped group
		// is a role grant by the ACTOR (the SCIM credential or operator), not by
		// the IdP.
		if g.MappedRole != "" && len(adds) > 0 {
			if err := checkRoleCeiling(actor, g.TargetTenantID, g.MappedRole); err != nil {
				return err
			}
		}
		g.DisplayName = in.DisplayName
		g.ExternalID = in.ExternalID
		if g, err = as.Groups().Update(ctx, g); err != nil {
			return err
		}
		removed := 0
		for uid, row := range current {
			if keep[uid] {
				continue
			}
			if err := as.GroupMembers().Delete(ctx, row.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			removed++
		}
		for _, uid := range adds {
			if _, err := as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: g.ID, UserID: uid}); err != nil {
				return err
			}
		}
		members, err := usersByID(ctx, as, valid)
		if err != nil {
			return err
		}
		out = SCIMGroup{Group: g, Members: members, SkippedMembers: skipped}
		return metaAudit(ctx, as, actor, "scim.group.update", "core.user_group", g.ID,
			map[string]any{"members": len(valid), "added": len(adds), "removed": removed, "skipped_members": skipped})
	})
	if err != nil {
		return SCIMGroup{}, err
	}
	return out, nil
}

// SCIMDeleteGroup removes a tenant's group and its member rows (idempotent on
// the member rows). Deleting the group also retires its role mapping — members
// keep only their direct memberships.
func (a *Authenticator) SCIMDeleteGroup(ctx context.Context, actor Principal, tenant model.TenantID, id model.ID) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		g, err := groupInTenant(ctx, as, tenant, id)
		if err != nil {
			return err
		}
		rows, err := groupMemberRows(ctx, as, g.ID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := as.GroupMembers().Delete(ctx, r.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		if err := as.Groups().Delete(ctx, g.ID); err != nil {
			return err
		}
		return auditAct(ctx, as, actor, "scim.group.delete", "core.user_group", g.ID)
	})
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

// ConfigureGroupParent sets (or, with parentID zero, clears) the group this
// group is nested under — the OPERATOR path (PUT /v1/groups/{id}/parent), never
// reachable through SCIM. The IdP decides membership; the tenant operator
// decides the hierarchy, exactly like MappedRole. Nesting C under P makes every
// member of C ALSO a member of P for authorization (loadGrants materializes the
// ancestor chain as Cedar `Group::` principal parents), so a scoped grant on P
// reaches C's members.
//
// Because nesting can widen what a group's members can reach, reshaping the
// hierarchy is a STRUCTURAL privilege requiring OWNER (or superadmin) authority
// in the group's tenant: an owner's own tenant ceiling already covers any grant
// a parent could carry (resource perms top out at owner), so nesting can never
// escalate a member beyond what the actor could grant directly, while an admin —
// which cannot confer the resource `admin` verb — must not nest a group under
// one whose grants exceed its ceiling. The check is against the group's STORED
// TargetTenantID, never the caller-supplied tenant (the anti-laundering
// discipline of ConfigureGroupRole). The parent must exist in the SAME tenant,
// and the new edge must keep the graph ACYCLIC (deny-closed).
func (a *Authenticator) ConfigureGroupParent(ctx context.Context, actor Principal, tenant model.TenantID, groupID, parentID model.ID) (model.UserGroup, error) {
	var out model.UserGroup
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		g, err := groupInTenant(ctx, as, tenant, groupID)
		if err != nil {
			return err
		}
		// Owner/superadmin in the group's OWN tenant. checkRoleCeiling(_, _, RoleOwner)
		// passes exactly for a superadmin or an owner (owner is the top rank), so it is
		// precisely the "may reshape this tenant's group hierarchy" gate. Clearing also
		// narrows reach but stays owner-gated — the hierarchy is a structural artifact
		// only the tenant owner curates.
		if err := checkRoleCeiling(actor, g.TargetTenantID, RoleOwner); err != nil {
			return err
		}
		if parentID.IsZero() {
			g.ParentGroupID = model.ID("")
		} else {
			parent, err := groupInTenant(ctx, as, g.TargetTenantID, parentID)
			if err != nil {
				return err
			}
			if cyclic, err := nestingWouldCycle(ctx, as, g.ID, parent); err != nil {
				return err
			} else if cyclic {
				return ErrGroupCycle
			}
			g.ParentGroupID = parent.ID
		}
		if g, err = as.Groups().Update(ctx, g); err != nil {
			return err
		}
		out = g
		return metaAudit(ctx, as, actor, "scim.group.nest", "core.user_group", g.ID,
			map[string]any{"parent": parentID.String()})
	})
	if err != nil {
		return model.UserGroup{}, err
	}
	return out, nil
}

// nestingWouldCycle reports whether making `child` a descendant of `parent`
// would create a cycle: it walks parent's ancestor chain (parent first) and
// returns true if `child` appears — covering both the self-parent case
// (parent == child) and a longer loop. The walk is bounded by a visited set (it
// also terminates on any pre-existing cycle, which must not exist but is handled
// defensively) and stops at a dangling or cross-tenant edge, neither of which
// loadGrants would traverse.
func nestingWouldCycle(ctx context.Context, as store.AuthScope, child model.ID, parent model.UserGroup) (bool, error) {
	visited := map[model.ID]bool{}
	cur := parent
	for {
		if cur.ID == child {
			return true, nil
		}
		if visited[cur.ID] || cur.ParentGroupID.IsZero() {
			return false, nil
		}
		visited[cur.ID] = true
		next, err := as.Groups().Get(ctx, cur.ParentGroupID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return false, nil // dangling ancestor ends the chain
			}
			return false, err
		}
		if next.TargetTenantID != parent.TargetTenantID {
			return false, nil // a cross-tenant edge is never traversed
		}
		cur = next
	}
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

// groupBy returns the tenant's group matching one indexed column, and whether
// one exists (the application-level dedupe probe).
func groupBy(ctx context.Context, as store.AuthScope, tenant model.TenantID, col, val string) (model.UserGroup, bool, error) {
	gs, _, err := as.Groups().List(ctx, model.Query{Filters: []model.Filter{
		{Column: col, Op: model.OpEq, Value: val},
		{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
	}, Limit: 1})
	if err != nil {
		return model.UserGroup{}, false, err
	}
	if len(gs) == 0 {
		return model.UserGroup{}, false, nil
	}
	return gs[0], true, nil
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

// validMembers filters want down to the deduplicated, order-preserving ids that
// hold a membership in tenant (membershipOf), counting the rest as skipped. A
// non-member and another tenant's member are skipped IDENTICALLY — counting
// them differently would let a SCIM connection probe foreign user ids.
func validMembers(ctx context.Context, as store.AuthScope, tenant model.TenantID, want []model.ID) ([]model.ID, int, error) {
	valid := make([]model.ID, 0, len(want))
	seen := make(map[model.ID]bool, len(want))
	skipped := 0
	for _, uid := range want {
		if seen[uid] {
			continue // a repeated member is one member, not a skip
		}
		seen[uid] = true
		if _, ok, err := membershipOf(ctx, as, uid, tenant); err != nil {
			return nil, 0, err
		} else if !ok {
			skipped++
			continue
		}
		valid = append(valid, uid)
	}
	return valid, skipped, nil
}
