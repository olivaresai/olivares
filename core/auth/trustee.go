// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "github.com/olivaresai/olivares/core/model"

// PermRBACAdmin gates authoring roles and scoped grants (governance module). It is the
// permission behind the Access Control trustee right; the governance module aliases it.
const PermRBACAdmin Permission = "governance:rbac:admin"

// TrusteeRight is one NetWare-style trustee right, mapped onto the verbs the
// authorization algebra already has. It is a NAME for a question Authorize answers, not a
// second algebra: Question builds the exact Request the request path would send, so
// authorizing it (Authorizer.Authorize, or AuthorizeBatch for a whole set) is the only
// way to learn whether the right is held. There is no new verb and no new decision.
type TrusteeRight struct {
	// Name is the right as an operator reads it.
	Name string
	// Verb is the permission verb asked about a resource kind (read, write or admin).
	// Ignored when Permission is set.
	Verb string
	// Permission, when set, is the one fixed permission this right asks about, whatever the
	// node's kind.
	Permission Permission
	// Entity is true when the question names the node itself (Resource.ID set), false
	// when it asks about the node's kind as a collection.
	Entity bool
	// TreeOnly restricts the right to the scope-tree kinds. Supervisor needs it: on a
	// tree kind only the owner role holds the admin verb, while on a module kind the admin
	// role holds it too (roleGrantsVerb), so the question would no longer mean "owner".
	TreeOnly bool
}

// The trustee rights, in the order an operator reads them.
//
//   - Supervisor is owner at the node: the admin verb on a tree kind (agent, session,
//     resource, ...) is held by the owner role alone (coreRolePerms), and a scoped grant of
//     the owner role projects it (RoleResourcePerms). On a module kind the admin role holds
//     the admin verb too (roleGrantsVerb), so the right is TreeOnly.
//   - Browse is the collection read (the kind, no node id); Read is the entity read.
//   - Write, Create, Erase and Modify are all the write verb on the node. They split only
//     when a route declares its own action (RouteMetadata.CedarAction); until then four
//     names for one question would be four answers that cannot disagree. Create names the
//     container the row would live in, which is also what a scoped grant is written over.
//   - Access Control is the RBAC admin permission, which authors scoped grants, asked
//     exactly as its routes ask it (ResourceFor: tenant-wide, no node). What a holder may
//     actually grant, and where, is bounded by the delegation ceiling (governance
//     canDelegate), which this table does not and must not restate.
var (
	TrusteeSupervisor    = TrusteeRight{Name: "Supervisor", Verb: VerbAdmin, Entity: true, TreeOnly: true}
	TrusteeBrowse        = TrusteeRight{Name: "Browse", Verb: VerbRead}
	TrusteeRead          = TrusteeRight{Name: "Read", Verb: VerbRead, Entity: true}
	TrusteeWrite         = TrusteeRight{Name: "Write", Verb: VerbWrite, Entity: true}
	TrusteeCreate        = TrusteeRight{Name: "Create", Verb: VerbWrite, Entity: true}
	TrusteeErase         = TrusteeRight{Name: "Erase", Verb: VerbWrite, Entity: true}
	TrusteeModify        = TrusteeRight{Name: "Modify", Verb: VerbWrite, Entity: true}
	TrusteeAccessControl = TrusteeRight{Name: "Access Control", Permission: PermRBACAdmin}
)

// TrusteeRights is the one table, in display order. The slice is a copy.
func TrusteeRights() []TrusteeRight {
	return []TrusteeRight{
		TrusteeSupervisor, TrusteeBrowse, TrusteeRead, TrusteeWrite,
		TrusteeCreate, TrusteeErase, TrusteeModify, TrusteeAccessControl,
	}
}

// Question returns the authorization Request that asks whether principal holds the right
// at node in tenant, and false when there is no honest question to ask: node.Kind is not a
// scopeable kind (IsScopeableKind), the verb is not read, write or admin, a TreeOnly right was given a non-tree kind, or an
// entity right has no node.ID. The caller must treat false as "not held" and never
// authorize a zero Request: a superadmin passes rbacAllows for any permission, so the
// refusal cannot be left to the decision.
//
// node.Kind is the kind as the scope catalog names it ("agent", or "<ns>:<res>" for a module
// kind); the Request's Resource.Kind is the one the request path derives from the permission
// (Permission.Resource). Browse asks about the kind, so its node ID is not part of the
// question. For an entity right the caller sets node.ID, and WorkspaceID where the kind is
// not in the scope tree; WorkspaceID is then the authorization scope, so it must come from
// the store and never from a request (see ResourceAttrs).
func (r TrusteeRight) Question(principal Principal, tenant model.TenantID, node ResourceAttrs) (Request, bool) {
	if r.Permission != "" {
		return Request{Principal: principal, Permission: r.Permission, Tenant: tenant, Resource: ResourceFor(r.Permission)}, true
	}
	known := r.Verb == VerbRead || r.Verb == VerbWrite || r.Verb == VerbAdmin
	known = known && IsScopeableKind(node.Kind)
	if r.TreeOnly {
		known = known && IsTreeScopeableKind(node.Kind)
	}
	if !known || (r.Entity && node.ID == "") {
		return Request{}, false
	}
	perm := Permission(node.Kind + ":" + r.Verb)
	node.Kind = perm.Resource()
	if !r.Entity {
		node.ID = ""
	}
	return Request{Principal: principal, Permission: perm, Tenant: tenant, Resource: node}, true
}
