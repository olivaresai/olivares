// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"testing"
)

// AuthorizeBatch must be a verbatim aggregation of Authorize: decisions[i] is exactly
// Authorize(reqs[i]). This is the anti-divergence guarantee — an enumeration can
// never allow a pair the request path denies (or vice versa).
func TestAuthorizeBatchMatchesAuthorize(t *testing.T) {
	ctx := context.Background()
	// A scoped engine that grants only when the resource id is "grant-me" (a positive
	// scoped grant the flat RBAC layer would deny for a viewer's write).
	az := NewAuthorizer(nil, WithScopedGrants(scopedFunc(func(_ context.Context, req Request) (ScopedDecision, error) {
		if req.Resource.ID == "grant-me" {
			return ScopedDecision{Effect: EffectGrant}, nil
		}
		return ScopedDecision{Effect: EffectAbstain}, nil
	})))
	viewer := ScopedPrincipal("cred-1", "viewer", grantTenant, RoleViewer)
	reqs := []Request{
		{Principal: viewer, Permission: "agent:write", Tenant: grantTenant, Resource: ResourceAttrs{Kind: "agent", ID: "grant-me"}}, // scoped grant ⇒ allow
		{Principal: viewer, Permission: "agent:write", Tenant: grantTenant, Resource: ResourceAttrs{Kind: "agent", ID: "other"}},    // no grant, viewer ⇒ deny
		{Principal: viewer, Permission: "agent:read", Tenant: grantTenant, Resource: ResourceAttrs{Kind: "agent"}},                  // viewer RBAC ⇒ allow
	}
	got, err := az.AuthorizeBatch(ctx, reqs)
	if err != nil {
		t.Fatalf("AuthorizeBatch: %v", err)
	}
	if len(got) != len(reqs) {
		t.Fatalf("len(decisions) = %d, want %d", len(got), len(reqs))
	}
	for i, req := range reqs {
		want := az.Authorize(ctx, req)
		if got[i].Allow != want.Allow || got[i].Reason != want.Reason {
			t.Errorf("decisions[%d] = %+v, Authorize = %+v (DIVERGENCE)", i, got[i], want)
		}
	}
	if !got[0].Allow {
		t.Error("[0] scoped grant should allow")
	}
	if got[1].Allow {
		t.Error("[1] no grant + viewer write should deny")
	}
	if !got[2].Allow {
		t.Error("[2] viewer read should allow by RBAC")
	}
}

func TestAuthorizeBatchEmpty(t *testing.T) {
	got, err := NewAuthorizer(nil).AuthorizeBatch(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty batch = (%v, %v), want ([], nil)", got, err)
	}
}

// A canceled context aborts before the first Authorize, returning the context error
// (so an unbounded enumeration a caller forgot to page can still be stopped).
func TestAuthorizeBatchHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reqs := []Request{{
		Principal:  ScopedPrincipal("c", "c", grantTenant, RoleViewer),
		Permission: "agent:read", Tenant: grantTenant, Resource: ResourceAttrs{Kind: "agent"},
	}}
	got, err := NewAuthorizer(nil).AuthorizeBatch(ctx, reqs)
	if err == nil {
		t.Error("want a context error on a canceled context")
	}
	if len(got) != 0 {
		t.Errorf("want 0 decisions on a pre-canceled context, got %d", len(got))
	}
}
