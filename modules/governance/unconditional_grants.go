// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// the module half of auth.UnconditionalGrantReporter: which permissions does this
// principal hold in this tenant through an authored grant that no resource condition
// gates? It is what lets /v1/auth/whoami stop hiding authority a tenant-scoped grant
// genuinely confers, without the console deriving anything itself.
//
// It is built out of the SAME predicates the enforcement path uses, deliberately and in
// both directions:
//
//   - grantAppliesToActor decides whether a grant's subject names this principal. Its
//     doc-comment obligation is that it mirrors exactly what buildPrincipalEntity makes
//     the Cedar principal `in` (user / role / gated directory group), so a grant reported
//     here is one the engine would also match, and one it would not match is not reported.
//   - effectivePermsOfGrant resolves the grant's role — built-in or custom, with its
//     permission-groups unioned and its exclusions subtracted LAST — to the same
//     permission set projectManagedCedar writes into the permit. Re-deriving either here
//     would create the second copy this whole area exists to avoid.
//
// A grant contributes to the flat answer through TWO independent paths, because
// projectManagedCedar emits two independent permits, and treating the scope as one
// property of "the grant" is what made the first version of this file wrong:
//
//   - its ACCESS permit carries the scope condition (cedarScopeWhen), so only a
//     tenant-scoped grant contributes the permissions its role confers;
//   - its DELEGATION permit — governance:rbac:{read,admin} for an admin-capable user or
//     group subject — is emitted with an EMPTY when clause at any scope, so it contributes
//     tenant-wide however narrow the grant is.
//
// The second one is not a corner case: it is how a delegated workspace admin reaches the
// Roles screen at all, and omitting it hid that screen from the exact operator this area
// exists to serve.
//
// The row report is bound to the exact compiled `cedar-managed` authority before it is
// returned. Within its one View it reads the full durable Cedar snapshot and recomputes
// the managed projection from every current row/role/group; its digest must equal the
// selected managed revision. This closes both former over-offer windows: a committed
// epoch not yet reloaded, and a catalog/row change whose projection has not been
// selected. A mismatch deliberately under-reports until a coherent projection is live.
type unconditionalGrants Module

var _ auth.UnconditionalGrantReporter = (*unconditionalGrants)(nil)

// UnconditionalGrants returns the seam core/api wires into whoami. Returning the
// interface (not the struct) keeps the direction of the dependency: core/api learns what
// a principal holds without importing this module.
func (m *Module) UnconditionalGrants() auth.UnconditionalGrantReporter {
	return (*unconditionalGrants)(m)
}

// UnconditionalGrantPerms reads the tenant's structured grant rows and returns the
// permissions the unconditional ones confer on p. All reads run in ONE read-only View, as
// on the authorization path.
//
// A principal with no applicable grant yields nil. A tenant also yields nil before a
// coherent available Cedar runtime snapshot exists: UI grants are a report of live
// authority, not an inference from rows that the evaluator has not installed.
// Once live, whoami is a per-login/per-tenant-switch call rather than a hot path, and
// a principal cache would have to be invalidated by every RBAC write to stay honest.
func (u *unconditionalGrants) UnconditionalGrantPerms(ctx context.Context, p auth.Principal, tenant model.TenantID) ([]auth.Permission, error) {
	m := (*Module)(u)
	if m.data == nil || m.grants == nil {
		return nil, nil
	}
	// Capture one runtime state around the row View. whoami must not offer a UI
	// permission from rows read while the corresponding Cedar runtime became
	// unavailable (or was replayed/replaced) before the answer returned.
	before, beforeLoaded := m.grants.tenantState(tenant)
	if !beforeLoaded || !before.available || !hasCedarCompiledBinding(before) {
		return nil, nil
	}
	if m.grants.grantExpiredState(before, beforeLoaded, m.grants.clock()) {
		return nil, nil
	}
	// ADR-0024 Q1 offline staleness: past the bound the engine turns a positive grant into
	// a deny-closed ABSTAIN (grants.go Scoped, grantExpired) and the request falls back to
	// RBAC. Reporting from the stored rows without asking would then offer actions the
	// engine has stopped authorizing — an over-offer produced by a clock rather than by a
	// policy, which is the hardest kind to diagnose from a 403. Forbids are never expired,
	// so nothing here needs to compensate in the other direction.
	var (
		out               []auth.Permission
		durable           cedarDurableSnapshot
		projectionMatches bool
	)
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		durable, err = readCedarDurableSnapshot(ctx, sc, tenant, m.grants.maxStaleness)
		if err != nil {
			return err
		}
		grants, err := loadScopedGrants(ctx, sc)
		if err != nil {
			return err
		}
		roles, err := loadCustomRoles(ctx, sc)
		if err != nil {
			return err
		}
		groups, err := loadPermGroups(ctx, sc)
		if err != nil {
			return err
		}
		// The row answer is valid only for exactly the managed bytes the live
		// runtime carries. Recompute against *all* rows, not just this actor's:
		// another actor's row or a catalog change can alter the selected union.
		if contentDigest(projectManagedCedar(grants, roles, groups)) != durable.state.managedDigest {
			return nil
		}
		projectionMatches = true
		applicable := make([]scopedGrant, 0, len(grants))
		for _, g := range grants {
			if grantAppliesToActor(g, p, tenant) {
				applicable = append(applicable, g)
			}
		}
		seen := map[auth.Permission]struct{}{}
		add := func(perm string) {
			pp := auth.Permission(perm)
			if _, dup := seen[pp]; dup {
				return
			}
			seen[pp] = struct{}{}
			out = append(out, pp)
		}
		for _, g := range applicable {
			// The grant's own permissions ride its scope condition, so only a tenant-scoped
			// grant contributes them to a flat set.
			if g.Scope.Tree == scopeTenant {
				for perm := range effectivePermsOfGrant(g, roles, groups) {
					add(perm)
				}
			}
			// ...but the DELEGATION permit is a second, separate permit, and it is emitted
			// with NO `when` clause whatever the grant's scope (projectManagedCedar →
			// writePermit(subj, acts, "")). So an admin-capable grant scoped to ONE workspace
			// still confers governance:rbac:{read,admin} TENANT-WIDE, and missing it hid the
			// Roles screen from the delegated workspace admin — the exact persona this whole
			// area exists for. Found by adversarial contrast, not by the author.
			//
			// The conditions mirror the projection exactly, including that a ROLE subject is
			// deliberately excluded (a built-in role already carries its own authority) and
			// that delegationActions honors a custom role's exclusions — a role authored as
			// "may administer, may NOT re-delegate" must not be reported as able to.
			if g.SubjectKind != subjectUser && g.SubjectKind != subjectGroup {
				continue
			}
			if !isAdminCapableGrant(g, roles, groups) {
				continue
			}
			for _, a := range delegationActions(g, roles) {
				add(a)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	after, afterLoaded := m.grants.tenantState(tenant)
	if !projectionMatches || !sameCedarRuntimeCapture(before, beforeLoaded, after, afterLoaded) ||
		!afterLoaded || !after.available || !hasCedarCompiledBinding(after) ||
		!sameCedarAuthorityState(before, durable.state) || !sameCedarAuthorityState(after, durable.state) ||
		m.grants.grantExpiredState(after, afterLoaded, m.grants.clock()) {
		return nil, nil
	}
	return out, nil
}
