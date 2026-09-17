// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

// Route metadata: the sealed, per-route facts an authorization decision consults
// (V269 / docs/contracts/COCKPIT-02-authz.md §5).
//
// It is SEALED in the sense that matters: it is declared where the route is
// registered, by the engine, and never derived from anything a caller sends. A module
// states it once; a request cannot introduce or change it.
//
// ⛔ THE ONE SENTENCE TO READ TWICE, because inverting it is a whole class of defect
// this repository has already measured (finding 07): RequireScopedGrant does NOT narrow
// a permit. The algebra is Allow = (RBAC ∨ Grant) ∧ ¬Forbid, and in it a permit is
// POSITIVE — it can authorize, it can never confine. Every confinement is either a
// Forbid or the REMOVAL of the RBAC term, and this flag is the removal. An earlier
// design used a permit to confine an editor and it did not confine at all, because the
// editor already passed through RBAC.
type RouteMetadata struct {
	// CedarAction is the registered Cedar Action ID this route's permission maps to.
	//
	// It exists because the permission parser is a GRAMMAR: the last segment must be
	// read, write or admin (permission.go), and it grants by verb tier. An action like
	// `shell:open` cannot be spelled inside that grammar without breaking it for every
	// other module, so the route names its Action ID here instead. Empty keeps the
	// existing behaviour exactly: the action is derived from the permission.
	//
	// A module cannot invent one: the value is registered with the route, and the
	// decision's evidence records which action was evaluated.
	CedarAction string

	// RequireScopedGrant fixes the RBAC term of the algebra to FALSE for this route,
	// leaving Grant ∧ ¬Forbid ∧ ¬deny-overlay.
	//
	// It is how a route says "breadth of role is not enough here": no role, tenant
	// admin included, reaches a governed terminal merely by being broad. It removes a
	// path to allow; it adds none, and it cannot turn an allow into a deny for anyone
	// who had a positive scoped grant.
	RequireScopedGrant bool

	// RBACMinimumRole, when set, requires the principal's role in the tenant to rank
	// at or above it BEFORE RBAC can contribute its term. It narrows only the RBAC
	// path, exactly like RequireScopedGrant but by degree instead of absolutely, and
	// it never touches a scoped grant: a delegate authorized by policy is unaffected.
	//
	// Empty leaves the verb tier alone, which is what every existing route wants.
	RBACMinimumRole string

	// SessionInheritsAgentGroups lets THIS ROUTE resolve a Session's owning agent's
	// AgentGroups as scope parents, so a grant or forbid written over an AgentGroup
	// reaches the sessions that agent runs.
	//
	// ⛔ IT IS OPT-IN BECAUSE THE RESOLVER IS GLOBAL. modules/governance's scope
	// resolver is wired once for the whole engine and is consumed by request
	// authorization, by AuthZEN per row and by access-review. Doing this
	// unconditionally moved authorization for every caller, which an adversarial
	// contrast measured as a real widening — including a permit scoped to one
	// workspace reaching a session that lives in another. With the flag, a route that
	// does not ask decides bit-for-bit as it did before the capability existed.
	//
	// The resolver additionally confines the inherited groups to the session's OWN
	// workspace; the flag alone does not lift that.
	SessionInheritsAgentGroups bool

	// MinimumAAL is the assurance level the route requires.
	//
	// ⛔ IT IS A PRECONDITION OF AUTHENTICATION AND NOT A TERM OF THE ALGEBRA, and the
	// Authorizer deliberately does NOT read it. Folding it in would make a step-up
	// look like an authorization outcome, so a caller could not tell "you may not do
	// this" from "prove who you are again" — two answers with different remedies. The
	// route wrapper enforces it and answers with a step-up, before the decision.
	MinimumAAL int
}

// IsZero reports whether the metadata says nothing, which is the state of every route
// that has not opted in. The Authorizer's behaviour for such a request is bit-for-bit
// what it was before this type existed.
func (m RouteMetadata) IsZero() bool {
	return m.CedarAction == "" && !m.RequireScopedGrant && m.RBACMinimumRole == "" &&
		m.MinimumAAL == 0 && !m.SessionInheritsAgentGroups
}

// rbacPermitted applies the RBAC-side metadata to a base RBAC answer.
//
// Both arms only ever REMOVE the RBAC term. There is no branch here that turns a false
// into a true, and that is the invariant a reader should be able to confirm at a
// glance: metadata cannot grant.
func (m RouteMetadata) rbacPermitted(req Request, rbac bool) bool {
	if !rbac {
		return false
	}
	if m.RequireScopedGrant {
		return false
	}
	if m.RBACMinimumRole != "" {
		role, ok := req.Principal.RoleIn(req.Tenant)
		if !ok || RoleRank(role) < RoleRank(m.RBACMinimumRole) {
			return false
		}
	}
	return true
}
