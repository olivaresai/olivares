// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// AskPrincipal is the principal Ask Olivares calls one tool under
// (docs/design/ask-olivares-agent-v1.md, D1): the signed-in person, reloaded
// from their credential now, so a role revoked a moment ago is gone, and
// narrowed to one tenant under a hard ceiling of the given permissions. The
// ceiling never holds an admin verb, whatever the caller passes, so Ask can
// never decide an approval or administer anything. The person's own role,
// scoped grants, forbids and workspace confinement still apply under it, so
// the result can only be narrower than the person. It carries no credential
// reference and is marked as Ask's, which the approval decision refuses.
func (a *Authenticator) AskPrincipal(ctx context.Context, person Principal, tenant model.TenantID, permissions []Permission) (Principal, error) {
	if a == nil || !askablePerson(person, tenant) {
		return Principal{}, ErrUnauthenticated
	}
	queued, ok := QueuedCredentialFrom(person)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	queued, err := a.BindQueuedCredential(ctx, queued)
	if err != nil {
		return Principal{}, err
	}
	current, err := a.RevalidateQueuedCredential(ctx, queued)
	if err != nil {
		return Principal{}, err
	}
	return current.ForAsk(tenant, permissions...)
}

// ForAsk derives the Ask principal from p as it is: one tenant, a hard ceiling of the
// given permissions without any admin verb, no credential reference, marked as Ask's. It
// can only narrow p, and refuses a principal that already carries a ceiling, a session
// identity or a local operator, so no other credential becomes Ask's. AskPrincipal reloads
// the person first; use that for a live call.
func (p Principal) ForAsk(tenant model.TenantID, permissions ...Permission) (Principal, error) {
	role, member := p.RoleIn(tenant)
	if !askablePerson(p, tenant) || p.ExcludedFrom(tenant) || (!member && !p.Superadmin) {
		return Principal{}, ErrUnauthenticated
	}
	if !member {
		role = RoleOwner // a superadmin enters a tenant as its owner, as a session does
	}
	ceiling := make([]Permission, 0, len(permissions))
	for _, permission := range permissions {
		if permission.Verb() != VerbAdmin && permission != PermSystemAdmin {
			ceiling = append(ceiling, permission)
		}
	}
	p = p.withSessionScope(tenant)
	p.grants = map[model.TenantID]string{tenant: role}
	p.credentialRef = PrincipalRef{}
	p.askOrigin = true
	p = p.withRestrictedPermissions(tenant, ceiling...)
	// The person's seal does not describe this principal, so recompute it. Ask's shape
	// (no credential reference) is outside the sealed shapes, so the result is the zero
	// seal, which every path that needs sealed provenance refuses.
	p.evidence.seal, _ = computePrincipalAuthoritySeal(p)
	return p, nil
}

// IsAskOrigin reports whether Ask Olivares acts under this principal.
func (p Principal) IsAskOrigin() bool { return p.askOrigin }

// askablePerson reports whether Ask may act for p in tenant: a person, not a purpose-specific,
// session or local credential.
func askablePerson(p Principal, tenant model.TenantID) bool {
	return !p.IsPurposeRestricted() && !p.IsLocalOperator() && p.SessionIdentity == "" &&
		!p.UserID.IsZero() && !tenant.IsZero() && !tenant.IsSystem()
}
