// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// UnconditionalGrantReporter reports the permissions a principal holds in a tenant
// through an authored scoped grant that NO resource condition gates — the grants
// whose scope tree is the tenant itself.
//
// why this seam exists, and why it is deliberately narrow.
//
// EffectivePermissions answers from the ROLE alone, so any authority a principal holds by
// grant is missing from the set /v1/auth/whoami hands the console. Since #578 can()
// is set membership, so the console HIDES those actions and no 403 is ever produced to
// reveal them. Measured on the live surface: for a viewer that is 81 of the 161
// permissions the console gates on, across 32 feature areas.
//
// It reports ONLY the unconditional grants, and that restriction is the whole design
// rather than a simplification:
//
//   - a grant scoped to a workspace/agent-group/folder projects `when { resource in … }`.
//     Its authority exists only for particular resources, so putting its permission into a
//     FLAT set would offer the action everywhere and 403 on everything outside the scope —
//     re-creating, by the back door, the over-offer #578 removed. A flat set is the wrong
//     TYPE for it, and no amount of care makes it the right one. That half is bounded and
//     pinned instead (cmd/olivares/scoped_grant_console_reach_test.go).
//   - a grant scoped to the tenant projects `permit(principal in <subj>, action in […],
//     resource);` with no condition at all. It applies to every request the principal
//     makes, which is exactly what a flat set means. Carrying it here cannot over-offer,
//     because there is no resource for which it fails to hold.
//
// WHAT IT CANNOT SEE, stated so nobody reads more into it than it delivers. The live
// policy is the UNION of three authored surfaces (governance reloadTenantGrants): the
// structured `cedar-managed` projection, the operator's free-form `cedar`, and the signed
// `cedar-ddil` bundle. This reports the STRUCTURED rows only. Free-form Cedar can express
// an unconditional permit too, but it can equally condition on `context.aal`, on time or
// on resource attributes, and deciding statically which of those hold for a request that
// has not happened yet is not something a permission set can do. The structured surface is
// the one the console authors, so it is the one the console can be told about; the rest
// keeps the pre-existing under-report, which is the safe direction.
//
// An implementation MUST return only permissions the grant genuinely confers — it is
// consumed as authority the ROLE does not carry, so nothing re-filters it through
// RoleGrants afterwards.
type UnconditionalGrantReporter interface {
	// UnconditionalGrantPerms returns the permissions p holds in tenant by
	// unconditional authored grant. An error means the grants could not be read; the
	// caller keeps the role-derived set, which under-reports rather than over-offers.
	UnconditionalGrantPerms(ctx context.Context, p Principal, tenant model.TenantID) ([]Permission, error)
}

// MergeUnconditionalGrants returns the sorted, deduplicated union of a role's effective
// set and the permissions an unconditional grant confers, with the two rules that hold
// regardless of how a permission was acquired:
//
//   - the SYSTEM permission is never in a tenant set. Only the superadmin flag carries it
//     and the console short-circuits on that flag, so a tenant-set entry could only ever
//     offer a system action to someone who does not hold it. The grant catalog already
//     refuses it (IsGrantablePermission rejects a non-tree core kind), so this is the
//     second lock on a door that is already shut — which is the right number of locks for
//     the one permission that crosses tenants.
//   - when the membership is workspace-CONFINED, the tenant-wide access-MATRIX recon reads
//     come out again. Confinement forbids them whatever the action targets (F2), so a
//     grant cannot restore them; leaving them in because they arrived by grant would offer
//     an action the engine refuses every time.
//
// role is assumed to be the already-confinement-filtered set from EffectivePermissions;
// granted is the raw report from an UnconditionalGrantReporter.
func MergeUnconditionalGrants(role, granted []Permission, confined bool) []Permission {
	out := make([]Permission, 0, len(role)+len(granted))
	seen := make(map[Permission]struct{}, cap(out))
	add := func(p Permission) {
		if p == "" || p == PermSystemAdmin {
			return
		}
		if confined && IsAccessGraphReconPerm(p) {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, p := range role {
		add(p)
	}
	for _, p := range granted {
		add(p)
	}
	sortPerms(out)
	return out
}
