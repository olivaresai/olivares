// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// (T2) — the SYSTEMATIC principal×resource×action authorization conformance
// matrix. Where scoped_test.go spot-checks a few cells, this asserts the whole
// (built-in role × scopeable-kind × verb) grid PLUS the boundary negatives (IAM
// tier, privileged reads, sealed export, system permission, cross-tenant) AND the
// scoped-grant algebra (FORBID overrides, GRANT widens, deny-overlay narrows,
// fail-closed). Expected decisions are an INDEPENDENT hand-specification of the
// documented RBAC contract (core/auth/permission.go, docs/SECURITY-HARDENING.md), NOT re-derived
// from RoleGrants — so drift between the contract and the code is caught.

const (
	cmTenantA model.TenantID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	cmTenantB model.TenantID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

// cmScoped adapts a func to auth.ScopedAuthorizer for the algebra cases.
type cmScoped func(context.Context, auth.Request) (auth.ScopedDecision, error)

func (f cmScoped) Scoped(ctx context.Context, req auth.Request) (auth.ScopedDecision, error) {
	return f(ctx, req)
}

// cmEval adapts a func to auth.PolicyEvaluator (deny-overlay) for the algebra cases.
type cmEval func(context.Context, auth.Request) (auth.Decision, error)

func (f cmEval) Evaluate(ctx context.Context, req auth.Request) (auth.Decision, error) {
	return f(ctx, req)
}

// cmExpectScopeable is the INDEPENDENT spec for a built-in role's grant on a
// scopeable operational resource kind (the ScopeableKinds() set): read for viewer
// and up, write for editor and up, the admin verb for OWNER ONLY — the admin role
// adds IAM/tenant management, not the admin verb on operational resources.
func cmExpectScopeable(role, verb string) bool {
	switch verb {
	case auth.VerbRead:
		return auth.RoleRank(role) >= auth.RoleRank(auth.RoleViewer)
	case auth.VerbWrite:
		return auth.RoleRank(role) >= auth.RoleRank(auth.RoleEditor)
	case auth.VerbAdmin:
		return role == auth.RoleOwner
	}
	return false
}

func TestConformance_ScopeableMatrix(t *testing.T) {
	az := auth.NewAuthorizer(nil) // pure RBAC (no deny-overlay, no scoped grants)
	ctx := context.Background()
	roles := []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner}
	verbs := []string{auth.VerbRead, auth.VerbWrite, auth.VerbAdmin}
	for _, kind := range auth.ScopeableKinds() {
		for _, role := range roles {
			p := auth.ScopedPrincipal("s", "sub", cmTenantA, role)
			for _, verb := range verbs {
				perm := auth.Permission(kind + ":" + verb)
				got := az.Allowed(ctx, p, perm, cmTenantA)
				if want := cmExpectScopeable(role, verb); got != want {
					t.Errorf("%s on %q: allow=%v, want %v", role, perm, got, want)
				}
			}
		}
	}
}

func TestConformance_Boundaries(t *testing.T) {
	az := auth.NewAuthorizer(nil)
	ctx := context.Background()
	p := func(role string) auth.Principal { return auth.ScopedPrincipal("s", "sub", cmTenantA, role) }
	cases := []struct {
		name  string
		role  string
		perm  auth.Permission
		allow bool
	}{
		// IAM tier: manage accounts is admin+, never viewer/editor.
		{"viewer !user:read", auth.RoleViewer, "user:read", false},
		{"editor !user:read", auth.RoleEditor, "user:read", false},
		{"editor !user:write", auth.RoleEditor, "user:write", false},
		{"admin user:write", auth.RoleAdmin, "user:write", true},
		{"owner membership:write", auth.RoleOwner, "membership:write", true},
		{"editor !token:read", auth.RoleEditor, "token:read", false},
		// Privileged reads (recon-relevant): editor and up, NEVER the viewer tier.
		{"viewer !authz:read", auth.RoleViewer, auth.PermAuthzRead, false},
		{"editor authz:read", auth.RoleEditor, auth.PermAuthzRead, true},
		{"viewer !accessgraph:read", auth.RoleViewer, "accessgraph:read", false},
		{"editor accessgraph:read", auth.RoleEditor, "accessgraph:read", true},
		// Sealed access-review export: admin and up, never editor.
		{"editor !authz:admin", auth.RoleEditor, auth.PermAuthzAdmin, false},
		{"admin authz:admin", auth.RoleAdmin, auth.PermAuthzAdmin, true},
		// Collector ingest is an admin-tier infrastructure write.
		{"editor !ingest:write", auth.RoleEditor, auth.PermIngestWrite, false},
		{"admin ingest:write", auth.RoleAdmin, auth.PermIngestWrite, true},
		// The system permission is NEVER held by any tenant role (only superadmin).
		{"owner !system:admin", auth.RoleOwner, auth.PermSystemAdmin, false},
		{"admin !system:admin", auth.RoleAdmin, auth.PermSystemAdmin, false},
		// The evidence ledger is viewer-readable; IAM enumeration is not.
		{"viewer audit:read", auth.RoleViewer, "audit:read", true},
	}
	for _, c := range cases {
		if got := az.Allowed(ctx, p(c.role), c.perm, cmTenantA); got != c.allow {
			t.Errorf("%s: allow=%v, want %v", c.name, got, c.allow)
		}
	}
}

func TestConformance_CrossTenantIsolation(t *testing.T) {
	az := auth.NewAuthorizer(nil)
	owner := auth.ScopedPrincipal("s", "sub", cmTenantA, auth.RoleOwner)
	if az.Allowed(context.Background(), owner, "agent:read", cmTenantB) {
		t.Error("an owner in tenantA must be denied in tenantB (cross-tenant isolation)")
	}
	if !az.Allowed(context.Background(), owner, "agent:read", cmTenantA) {
		t.Error("an owner in tenantA must be allowed in tenantA")
	}
}

func TestConformance_ScopedAlgebra(t *testing.T) {
	ctx := context.Background()
	viewer := auth.ScopedPrincipal("s", "sub", cmTenantA, auth.RoleViewer)
	owner := auth.ScopedPrincipal("s", "sub", cmTenantA, auth.RoleOwner)

	grant := cmScoped(func(context.Context, auth.Request) (auth.ScopedDecision, error) {
		return auth.ScopedDecision{Effect: auth.EffectGrant}, nil
	})
	forbid := cmScoped(func(context.Context, auth.Request) (auth.ScopedDecision, error) {
		return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "out of scope"}, nil
	})
	abstain := cmScoped(func(context.Context, auth.Request) (auth.ScopedDecision, error) {
		return auth.ScopedDecision{Effect: auth.EffectAbstain}, nil
	})
	failing := cmScoped(func(context.Context, auth.Request) (auth.ScopedDecision, error) {
		return auth.ScopedDecision{}, errors.New("scope store down")
	})

	// A positive scoped GRANT widens: a viewer is authorized a write RBAC denies.
	az := auth.NewAuthorizer(nil, auth.WithScopedGrants(grant))
	if !az.Allowed(ctx, viewer, "agent:write", cmTenantA) {
		t.Error("a positive scoped GRANT must authorize beyond flat RBAC")
	}
	// FORBID overrides even an owner's tenant-wide RBAC grant.
	az = auth.NewAuthorizer(nil, auth.WithScopedGrants(forbid))
	if az.Allowed(ctx, owner, "agent:read", cmTenantA) {
		t.Error("a scoped FORBID must override an RBAC grant (forbid-overrides-permit)")
	}
	// ABSTAIN defers to RBAC (owner allowed; viewer write denied).
	az = auth.NewAuthorizer(nil, auth.WithScopedGrants(abstain))
	if !az.Allowed(ctx, owner, "agent:read", cmTenantA) {
		t.Error("ABSTAIN must defer to the RBAC grant")
	}
	if az.Allowed(ctx, viewer, "agent:write", cmTenantA) {
		t.Error("ABSTAIN must defer to the RBAC deny for a viewer write")
	}
	// A GRANT is still ANDed with the deny-overlay: a restricting overlay narrows it.
	az = auth.NewAuthorizer(cmEval(func(context.Context, auth.Request) (auth.Decision, error) {
		return auth.Decision{Allow: false, Reason: "dlp"}, nil
	}), auth.WithScopedGrants(grant))
	if az.Allowed(ctx, viewer, "agent:write", cmTenantA) {
		t.Error("the deny-overlay must narrow even a positive scoped grant")
	}
	// A scoped-engine ERROR fails closed (denies), never opens.
	az = auth.NewAuthorizer(nil, auth.WithScopedGrants(failing))
	if az.Allowed(ctx, owner, "agent:read", cmTenantA) {
		t.Error("a scoped-engine error must fail closed (deny)")
	}
}
