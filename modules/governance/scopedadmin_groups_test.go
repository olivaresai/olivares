// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/auth/authtest"
	"github.com/olivaresai/olivares/core/model"
)

// --- group-subject helpers (the auth partition is reached through the real
// Authenticator the harness wires; the governance module never touches it) ----------

// createGroup stores a directory group in tenant with the given member user ids
// (the members must already hold a tenant membership) and returns its id.
func (h *harness) createGroup(super auth.Principal, tenant model.TenantID, name, externalID string, members ...string) model.ID {
	h.t.Helper()
	ids := make([]model.ID, len(members))
	for i, m := range members {
		ids[i] = model.ID(m)
	}
	return authtest.Group(h.t, h.st, tenant, name, externalID, ids...).Group.ID
}

// nestGroup nests child under parent.
func (h *harness) nestGroup(super auth.Principal, tenant model.TenantID, child, parent model.ID) {
	h.t.Helper()
	authtest.Nest(h.t, h.st, child, parent)
}

// --- e2e: a group-subject grant authorizes every member within the scope -------------

// --- e2e: a grant on a PARENT group reaches a member of a CHILD group -----------------

// --- the group subject feeds the delegation ceiling (grantAppliesToActor=group) -------

// --- validation + the group hierarchy endpoint ---------------------------------------

// --- the group-hierarchy REST endpoint is owner-gated and acyclic --------------------

// --- e2e (U7): delegated admin DIRECTED by an IdP/directory group ----------------
//
// The wire-proof for U7: an admin-capable group-subject grant now opens the delegation
// console to the group's GATED members. This flips a real Authorize(governance:rbac:admin)
// decision — before U7 the sub-delegation permit was emitted only for USER subjects, so a
// group member (however privileged the group grant) hit 403 at the rbac API. The delta
// stays doubly safe: deny-closed admission (only a direct tenant member carries the group,
// so a stranger in the group gets nothing) and a bounded ceiling (canDelegate clamps the
// sub-grant to the group grant's scope). A ROLE subject stays access-only.
