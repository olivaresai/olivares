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

func TestScopedPrincipalSingleGrant(t *testing.T) {
	tenant := model.TenantID("11111111-1111-1111-1111-111111111111")
	other := model.TenantID("22222222-2222-2222-2222-222222222222")
	p := ScopedPrincipal("sub-1", "event-subscription", tenant, RoleEditor)

	if p.Superadmin {
		t.Fatal("a scoped principal must never be superadmin")
	}
	if role, ok := p.RoleIn(tenant); !ok || role != RoleEditor {
		t.Fatalf("RoleIn(tenant) = %q,%v; want editor,true", role, ok)
	}
	if _, ok := p.RoleIn(other); ok {
		t.Fatal("a scoped principal must not hold a grant in any other tenant")
	}
	if got := p.Actor(); got != "token:sub-1" {
		t.Fatalf("Actor() = %q, want token:sub-1", got)
	}
}

func TestScopedPrincipalThroughAuthorizer(t *testing.T) {
	tenant := model.TenantID("11111111-1111-1111-1111-111111111111")
	other := model.TenantID("22222222-2222-2222-2222-222222222222")
	az := NewAuthorizer(nil)

	viewer := ScopedPrincipal("s1", "sub", tenant, RoleViewer)
	editor := ScopedPrincipal("s2", "sub", tenant, RoleEditor)

	// Module verb tier: a viewer-scoped principal reads module resources.
	if !az.Allowed(context.Background(), viewer, "security:finding:read", tenant) {
		t.Error("viewer-scoped principal should hold security:finding:read")
	}
	// Privileged reads are editor+: never granted to the viewer tier.
	for _, perm := range []Permission{"accessgraph:read", "security:observed:read"} {
		if az.Allowed(context.Background(), viewer, perm, tenant) {
			t.Errorf("viewer-scoped principal must NOT hold %s", perm)
		}
		if !az.Allowed(context.Background(), editor, perm, tenant) {
			t.Errorf("editor-scoped principal should hold %s", perm)
		}
	}
	// Deny-closed across tenants and for the system permission.
	if az.Allowed(context.Background(), editor, "security:finding:read", other) {
		t.Error("a scoped principal must not authorize in another tenant")
	}
	if az.Allowed(context.Background(), editor, PermSystemAdmin, tenant) {
		t.Error("a scoped principal must never hold system:admin")
	}
	// An unknown role authorizes nothing.
	unknown := ScopedPrincipal("s3", "sub", tenant, "superuser")
	if az.Allowed(context.Background(), unknown, "security:finding:read", tenant) {
		t.Error("an unknown role must authorize nothing (deny-closed)")
	}
}

// The ABAC layer stays in the loop for scoped principals: a restricting
// evaluator denies, and an evaluator error fails closed.
func TestScopedPrincipalABACRestricts(t *testing.T) {
	tenant := model.TenantID("11111111-1111-1111-1111-111111111111")
	p := ScopedPrincipal("s1", "sub", tenant, RoleOwner)

	deny := NewAuthorizer(evalFunc(func(context.Context, Request) (Decision, error) {
		return Decision{Allow: false, Reason: "restricted"}, nil
	}))
	if deny.Allowed(context.Background(), p, "security:finding:read", tenant) {
		t.Error("ABAC denial must override an RBAC grant for a scoped principal")
	}
	failing := NewAuthorizer(evalFunc(func(context.Context, Request) (Decision, error) {
		return Decision{}, errors.New("pdp unreachable")
	}))
	if failing.Allowed(context.Background(), p, "security:finding:read", tenant) {
		t.Error("an ABAC evaluation error must fail closed for a scoped principal")
	}
}

// evalFunc adapts a func to PolicyEvaluator for tests.
type evalFunc func(context.Context, Request) (Decision, error)

func (f evalFunc) Evaluate(ctx context.Context, req Request) (Decision, error) { return f(ctx, req) }
