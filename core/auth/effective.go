// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

// EffectivePermissions returns the sorted, deduplicated permissions a built-in role
// effectively holds in ONE tenant: the set /v1/auth/whoami hands the console so the
// panel can answer "may I?" by SET MEMBERSHIP instead of mirroring the rule
// client-side. Every candidate is passed through RoleGrants — the engine decides, this
// is not a model of it — so the answer cannot drift from the one that produces the 403.
//
// WHAT IT ENUMERATES, and why that is complete enough to be honest.
//
// Module permissions are an OPEN set by design (see Permission: strings, not an enum,
// so a module declares its own without an engine release). There is therefore no such
// thing as "every permission that could exist", and this function does not pretend
// otherwise. catalog is what THIS BINARY SERVES — the permissions its mounted modules
// declare and its mounted routes require. A module that is not loaded has no routes, so
// a permission of its can never produce a 403 here and its absence costs nothing.
//
// Two forms cannot come from the catalog and are added here, because both are gated
// OUTSIDE the per-role core set and a set missing either would UNDER-report:
//
//   - the role's explicit CORE set (PermissionsForRole): never written as literals —
//     "session:read" is concatenated at init — so nothing greppable declares them;
//   - the PRIVILEGED READS (PrivilegedReadPerms): deliberately absent from every
//     per-role core set, granted from editor up by RoleGrants' first branch.
//
// WHAT IT DOES NOT AND CANNOT EXPRESS. The set is the tenant-wide RBAC floor. It does
// not carry authored scoped grants, authored scoped FORBIDs, or the ABAC/PDP
// deny-overlay. A permission-set mirror can under-report those safely — it hides an
// action the engine would allow — and it was already blind to all three before this
// existed.
//
// CORRECTED THE REASON GIVEN HERE, and the correction matters more than the wording.
// This comment used to say a scoped grant "can only ADD authority for a specific resource
// subtree", i.e. that all three omissions are decided per RESOURCE and so are simply not
// expressible in a flat set. That is true of two of them and FALSE of the third, and the
// false one is the big one:
//
//   - a grant whose scope tree is `workspace`/`agent_group`/`folder` projects a permit
//     conditioned on `resource in …` (governance/scopedadmin.go cedarScopeWhen). It is
//     per-resource, so a flat set genuinely cannot carry it: adding the permission would
//     offer the action on every resource and 403 on most — the over-offer #578 removed.
//   - a grant whose scope tree is `tenant` projects a permit with NO condition at all, so
//     it matches every request. It is FLAT. Nothing about it is per-resource, and the only
//     reason it is missing here is that this function is not given the principal's grants.
//
// Measured on the live module set (cmd/olivares/scoped_grant_console_reach_test.go): 5 of
// 659 module routes are entity-scoped, so a scope-tree grant reaches 4 module permissions
// plus the core tree entities, while a tenant-scoped grant reaches everything. Stating the
// omission as "per-resource, therefore inexpressible" made the LARGER half look like a law
// of nature instead of a missing input.
//
// confined removes what workspace confinement forbids a principal REGARDLESS of
// what the action targets: the tenant-wide access-MATRIX recon reads
// (IsAccessGraphReconPerm), which have no per-workspace view to fall back to.
//
// It does NOT remove everything confinement denies, and the boundary is narrower than
// "the rest is target-dependent" — an adversarial review corrected that claim and it is
// worth stating exactly. governance.scopedEngine.Scoped forbids an indeterminate-target
// WRITE, and a target is indeterminate whenever the authorization request carries no
// entity id and no declared workspace. Every MODULE route is authorized that way
// (api.chiRegistrar.Handle calls authzTenant, which never seeds an entity), so a
// confined principal is in practice refused every module write/admin route — and those
// permissions are still in the set this returns, so the console still offers them.
//
// They are NOT removed here because the removal is a property of the ROUTES a permission
// is reachable through, not of the permission itself: core `agent:write` is refused at
// the collection route and ALLOWED at the entity route when the target is the
// principal's own workspace, and a declared workspace does reach the resolver from at
// least one caller (modules/sourcescope/resolver.go). Deriving it needs the catalog to
// record whether a route seeds an entity, which it does not. Getting it wrong in the
// removing direction HIDES work a confined operator is entitled to do, which is worse
// than the over-offer it would fix. Declared, measured and left to a unit of its own.
//
// An unknown role yields an empty slice: RoleGrants denies everything, deny-closed.
func EffectivePermissions(role string, catalog []Permission, confined bool) []Permission {
	out := make([]Permission, 0, len(catalog)+len(privilegedReadPerms)+len(coreRolePerms[role]))
	seen := make(map[Permission]struct{}, cap(out))
	consider := func(p Permission) {
		if _, dup := seen[p]; dup {
			return
		}
		// There is deliberately NO skip for the empty permission here. It looks like it
		// belongs and it is DEAD: "" has no colon, so it is core-shaped, and no per-role
		// core set contains it — RoleGrants below already denies it for every role,
		// known or not. A mutation run caught the guard surviving its own deletion, and
		// an unreachable arm reads as coverage it is not providing.
		// TestRoleGrantsDeniesTheEmptyPermission is what keeps that true.
		// RoleGrants is the engine's own dispatch (privileged-read membership, then
		// module-vs-core shape). Running the per-role core set back through it is not
		// redundant: if the two ever disagree, the one that decides the request must
		// win here too.
		if !RoleGrants(role, p) {
			return
		}
		if confined && IsAccessGraphReconPerm(p) {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, p := range PermissionsForRole(role) {
		consider(p)
	}
	for _, p := range PrivilegedReadPerms() {
		consider(p)
	}
	for _, p := range catalog {
		consider(p)
	}
	sortPerms(out)
	return out
}
