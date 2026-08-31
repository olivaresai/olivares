// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// scopedFunc adapts a func to ScopedAuthorizer for tests.
type scopedFunc func(context.Context, Request) (ScopedDecision, error)

func (f scopedFunc) Scoped(ctx context.Context, req Request) (ScopedDecision, error) {
	return f(ctx, req)
}

const grantTenant = model.TenantID("11111111-1111-1111-1111-111111111111")

func effect(e Effect) scopedFunc {
	return func(context.Context, Request) (ScopedDecision, error) { return ScopedDecision{Effect: e}, nil }
}

// A positive scoped grant authorizes a permission the flat RBAC layer denies — the
// whole point of (a viewer cannot agent:write by role, but a grant lets it).
func TestAuthorizerScopedGrantAuthorizes(t *testing.T) {
	ctx := context.Background()
	viewer := ScopedPrincipal("s1", "sub", grantTenant, RoleViewer)

	if NewAuthorizer(nil).Allowed(ctx, viewer, "agent:write", grantTenant) {
		t.Fatal("baseline: a viewer must not hold agent:write via RBAC")
	}
	az := NewAuthorizer(nil, WithScopedGrants(effect(EffectGrant)))
	if !az.Allowed(ctx, viewer, "agent:write", grantTenant) {
		t.Error("a positive scoped grant must authorize a permission RBAC denies")
	}
}

// A scoped FORBID overrides everything — a tenant-wide RBAC grant AND a positive
// scoped grant (forbid-overrides-permit, deny-by-default).
func TestAuthorizerScopedForbidOverrides(t *testing.T) {
	ctx := context.Background()
	owner := ScopedPrincipal("s1", "sub", grantTenant, RoleOwner) // RBAC grants agent:write

	if !NewAuthorizer(nil).Allowed(ctx, owner, "agent:write", grantTenant) {
		t.Fatal("baseline: an owner holds agent:write via RBAC")
	}
	if NewAuthorizer(nil, WithScopedGrants(effect(EffectForbid))).Allowed(ctx, owner, "agent:write", grantTenant) {
		t.Error("a scoped forbid must override an RBAC grant")
	}
	// A forbid must also win against a would-be grant: a scoped engine that both
	// permits and forbids resolves to forbid (Cedar's forbid-overrides-permit), which
	// the engine reports as EffectForbid — so a forbid effect denies regardless of RBAC.
	viewer := ScopedPrincipal("s2", "sub", grantTenant, RoleViewer)
	if NewAuthorizer(nil, WithScopedGrants(effect(EffectForbid))).Allowed(ctx, viewer, "agent:write", grantTenant) {
		t.Error("a scoped forbid must deny even a non-RBAC-holder")
	}
}

// EffectAbstain defers to RBAC — the back-compat invariant: with no matching
// grant the decision is exactly the historical RBAC one.
func TestAuthorizerScopedAbstainDefersToRBAC(t *testing.T) {
	ctx := context.Background()
	az := NewAuthorizer(nil, WithScopedGrants(effect(EffectAbstain)))

	owner := ScopedPrincipal("s1", "sub", grantTenant, RoleOwner)
	if !az.Allowed(ctx, owner, "agent:write", grantTenant) {
		t.Error("abstain must defer to an RBAC allow")
	}
	viewer := ScopedPrincipal("s2", "sub", grantTenant, RoleViewer)
	if az.Allowed(ctx, viewer, "agent:write", grantTenant) {
		t.Error("abstain must defer to an RBAC deny")
	}
}

// A scoped engine error or panic fails CLOSED — even for an RBAC-permitted owner.
func TestAuthorizerScopedFailsClosed(t *testing.T) {
	ctx := context.Background()
	owner := ScopedPrincipal("s1", "sub", grantTenant, RoleOwner)

	failing := scopedFunc(func(context.Context, Request) (ScopedDecision, error) {
		return ScopedDecision{}, errors.New("scope resolver unavailable")
	})
	if NewAuthorizer(nil, WithScopedGrants(failing)).Allowed(ctx, owner, "agent:write", grantTenant) {
		t.Error("a scoped engine error must fail closed")
	}
	panicky := scopedFunc(func(context.Context, Request) (ScopedDecision, error) { panic("boom") })
	if NewAuthorizer(nil, WithScopedGrants(panicky)).Allowed(ctx, owner, "agent:read", grantTenant) {
		t.Error("a scoped engine panic must fail closed")
	}
}

// The deny-overlay still narrows even a positive scoped grant (defense in depth): a
// grant is ANDed with the ABAC/PDP deny layer, never above it.
func TestAuthorizerScopedGrantNarrowedByDenyOverlay(t *testing.T) {
	ctx := context.Background()
	viewer := ScopedPrincipal("s1", "sub", grantTenant, RoleViewer)
	deny := evalFunc(func(context.Context, Request) (Decision, error) {
		return Decision{Allow: false, Reason: "abac restriction"}, nil
	})
	az := NewAuthorizer(deny, WithScopedGrants(effect(EffectGrant)))
	if az.Allowed(ctx, viewer, "agent:write", grantTenant) {
		t.Error("the deny-overlay must narrow even a positive scoped grant")
	}
}

// A nil scoped engine is exactly the historical RBAC ∩ deny-overlay Authorizer — the
// proof every existing call site keeps deciding identically.
func TestAuthorizerNilScopedIsBackCompat(t *testing.T) {
	ctx := context.Background()
	owner := ScopedPrincipal("s1", "sub", grantTenant, RoleOwner)
	viewer := ScopedPrincipal("s2", "sub", grantTenant, RoleViewer)
	az := NewAuthorizer(nil) // no WithScopedGrants

	if !az.Allowed(ctx, owner, "agent:write", grantTenant) {
		t.Error("nil scoped: an owner must still hold agent:write")
	}
	if az.Allowed(ctx, viewer, "agent:write", grantTenant) {
		t.Error("nil scoped: a viewer must still be denied agent:write")
	}
}
