// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "github.com/olivaresai/olivares/core/model"

// ScopedPrincipal builds a synthetic, non-credentialed principal carrying
// exactly one (tenant, role) grant — never superadmin and never a credential.
// It exists so a background dispatcher can evaluate the SAME deny-closed
// RBAC+ABAC pipeline (Authorizer.Allowed) as a live request when no
// authenticated request principal is available: the eventing platform
// authorizes each outbound event delivery under the subscription's recorded
// role this way, keeping the ABAC PolicyEvaluator in the loop.
//
// A scoped principal authenticates NOTHING: it carries no secret, must never be
// stored in a request context as if it had passed the authenticate middleware,
// and its grants are exactly what the caller asserts. It is an in-process
// authorization SUBJECT, not an identity. The id names the carrying resource
// (e.g. an event-subscription id) so an ABAC policy or log can refer to it;
// Actor() renders it as "token:<id>".
//
// An unknown role is not an error: RoleGrants returns false for it, so the
// principal simply authorizes nothing (deny-closed).
func ScopedPrincipal(id model.ID, displayName string, tenant model.TenantID, role string) Principal {
	return newPrincipal(KindToken, "", id, false, displayName,
		map[model.TenantID]string{tenant: role}, nil)
}
