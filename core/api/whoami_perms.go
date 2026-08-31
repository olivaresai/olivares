// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"sort"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// builtinRoles are the roles whoami can report an effective set for. Custom
// per-tenant roles are a documented later extension; an unknown role finds no entry
// in rolePerms and whoami emits an EMPTY set, which denies every action in the
// console — the same deny-closed answer RoleGrants gives it.
var builtinRoles = []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner}

// buildPermCatalog collects the module permissions THIS BINARY SERVES: what each
// mounted module DECLARES (Permissions()), what each mounted route REQUIRES, and what
// each registered search kind gates on.
//
// The three forms are collected together on purpose, because they are distinct facts
// and any one of them alone under-reports:
//
//   - Permissions() is the declaration; a route may require a permission the module
//     forgot to declare, and that permission still produces a real 403.
//   - a route requirement is captured by RUNNING APIRoutes against a recording
//     registrar — the permission is an ARGUMENT to Handle, so nothing short of running
//     the registration observes a conditionally mounted route.
//   - a search kind is gated by its own permission in handleSearch (search.go), which
//     is a 403 source with no route of its own in the module's APIRoutes.
//
// Core permissions are deliberately NOT here: they are concatenated at init and never
// exist as literals, so they are read out of the built per-role sets instead
// (auth.EffectivePermissions adds them, together with the privileged reads).
func buildPermCatalog(modules []Module, kinds []SearchKind) []auth.Permission {
	seen := map[auth.Permission]struct{}{}
	add := func(p auth.Permission) {
		if p != "" {
			seen[p] = struct{}{}
		}
	}
	for _, m := range modules {
		for _, p := range m.Permissions() {
			add(p)
		}
	}
	for _, rt := range collectModuleRoutes(modules) {
		add(rt.perm)
	}
	for _, k := range kinds {
		add(k.Permission)
	}
	out := make([]auth.Permission, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// rolePermKey indexes the precomputed sets by role AND by whether the membership is
// workspace-confined. Confinement is a property of the MEMBERSHIP, not of the role — one
// principal can be confined in one tenant and tenant-wide in another — so it cannot be
// folded into the role.
type rolePermKey struct {
	role     string
	confined bool
}

// buildRolePerms precomputes the effective set of every built-in role in both
// confinement states over the catalog: eight slices, computed once, so whoami does no
// set arithmetic per request and never hands out a slice a caller could filter in
// place. A permission set that mutates under a neighboring request is the worst
// possible defect to debug, so nothing is shared by reference past effectivePermsFor.
func buildRolePerms(catalog []auth.Permission) map[rolePermKey][]auth.Permission {
	out := make(map[rolePermKey][]auth.Permission, len(builtinRoles)*2)
	for _, r := range builtinRoles {
		for _, confined := range []bool{false, true} {
			out[rolePermKey{role: r, confined: confined}] = auth.EffectivePermissions(r, catalog, confined)
		}
	}
	return out
}

// effectivePermsFor returns the permission strings whoami reports for one grant: the
// precomputed set for the role, minus the access-matrix recon reads when the membership
// is workspace-confined (F2). An unknown role has no entry and yields an empty
// (never nil) slice — deny-closed, and it marshals as [] rather than null.
func (s *Server) effectivePermsFor(role string, confined bool) []string {
	perms := s.rolePerms[rolePermKey{role: role, confined: confined}]
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = string(p)
	}
	return out
}

// grantedPermsFor is what whoami actually reports: the role's precomputed effective set,
// UNIONED with the permissions the principal holds in this tenant through an authored
// grant that no resource condition gates.
//
// Why the union is safe here and would not be for a scoped grant: an unconditional grant
// projects a Cedar permit with no `when` clause, so it holds for every request the
// principal makes. There is no resource for which reporting it over-offers. A
// workspace/agent-group/folder grant is the opposite — true only of particular resources
// — and is deliberately NOT reported; see auth.UnconditionalGrantReporter.
//
// FAILURE IS AN UNDER-REPORT, ON PURPOSE, AND IT IS LOGGED. If the grants cannot be read
// the role-derived set stands. That is the pre answer, and its error direction is the
// safe one: it hides an action the principal may be entitled to, where the alternative —
// failing whoami — would black out the console for every user on a governance store
// hiccup, and pretending is not on the menu. It is logged at WARN so "the console is
// missing my delegated actions" is diagnosable rather than mysterious.
func (s *Server) grantedPermsFor(ctx context.Context, p auth.Principal, tenant model.TenantID, role string, confined bool) []string {
	// A purpose-specific bearer has a server-authored hard ceiling that replaces
	// RBAC as its base authority. Report exactly that set: neither an unknown
	// internal role nor an unconditional Cedar grant may under/overstate it.
	if restricted, ok := p.PurposePermissionsIn(tenant); ok {
		out := make([]string, len(restricted))
		for i, perm := range restricted {
			out[i] = string(perm)
		}
		return out
	}
	rolePerms := s.rolePerms[rolePermKey{role: role, confined: confined}]
	if s.unconditionalGrants == nil {
		out := make([]string, len(rolePerms))
		for i, perm := range rolePerms {
			out[i] = string(perm)
		}
		return out
	}
	granted, err := s.unconditionalGrants.UnconditionalGrantPerms(ctx, p, tenant)
	if err != nil {
		s.log.Warn("api: could not read the tenant's unconditional grants for whoami; reporting the role-derived set only (the console will hide actions a grant confers)",
			"err", err, "tenant", tenant.String(), "request_id", requestID(ctx))
		granted = nil
	}
	merged := auth.MergeUnconditionalGrants(rolePerms, granted, confined)
	out := make([]string, len(merged))
	for i, perm := range merged {
		out[i] = string(perm)
	}
	return out
}
