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

// A policy permit cannot override any native deny, but each disclosure check
// must still consult that policy. Ordinary action authorization keeps its gate.
func TestDisclosureAuthorizationKeepsEveryDenyTerm(t *testing.T) {
	const tenant model.TenantID = "11111111-1111-1111-1111-111111111111"
	const read Permission = "sessions:run:read"
	viewer := ScopedPrincipal("viewer", "disclosure", tenant, RoleViewer)
	foreign := ScopedPrincipal("foreign", "disclosure", "22222222-2222-2222-2222-222222222222", RoleViewer).withSessionScope("22222222-2222-2222-2222-222222222222")
	excluded := viewer
	excluded.excluded = map[model.TenantID]struct{}{tenant: {}}
	restricted := viewer
	restricted.restricted = map[model.TenantID]map[Permission]struct{}{tenant: {"sessions:run:write": {}}}
	for _, tc := range []struct {
		name          string
		principal     Principal
		scoped        scopedFunc
		route         RouteMetadata
		wantAllow     bool
		ordinaryCalls int
	}{
		{"readable", viewer, effect(EffectAbstain), RouteMetadata{}, true, 1},
		{"scoped forbid", viewer, effect(EffectForbid), RouteMetadata{}, false, 0},
		{"scoped error", viewer, func(context.Context, Request) (ScopedDecision, error) {
			return ScopedDecision{}, errors.New("unavailable")
		}, RouteMetadata{}, false, 0},
		{"scoped panic", viewer, func(context.Context, Request) (ScopedDecision, error) { panic("unavailable") }, RouteMetadata{}, false, 0},
		{"RBAC miss", ScopedPrincipal("unknown", "disclosure", tenant, "unknown"), effect(EffectAbstain), RouteMetadata{}, false, 0},
		{"tenant exclusion with grant", excluded, effect(EffectGrant), RouteMetadata{}, false, 0},
		{"session tenant with grant", foreign, effect(EffectGrant), RouteMetadata{}, false, 0},
		{"credential ceiling with grant", restricted, effect(EffectGrant), RouteMetadata{}, false, 0},
		{"route requires scoped grant", viewer, effect(EffectAbstain), RouteMetadata{RequireScopedGrant: true}, false, 0},
		{"positive scoped grant", ScopedPrincipal("unknown", "disclosure", tenant, "unknown"), effect(EffectGrant), RouteMetadata{}, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			az := NewAuthorizer(evalFunc(func(_ context.Context, req Request) (Decision, error) {
				calls++
				if req.Permission != read || req.Tenant != tenant || req.Resource.ID != "row-or-reference" {
					t.Errorf("policy did not receive the exact read request: %+v", req.Resource)
				}
				return Decision{Allow: true}, nil
			}), WithScopedGrants(tc.scoped))
			req := Request{Principal: tc.principal, Permission: read, Tenant: tenant, Resource: ResourceAttrs{Kind: "run", ID: "row-or-reference"}, Route: tc.route}
			ordinary := az.Authorize(context.Background(), req)
			if ordinary.Allow != tc.wantAllow || calls != tc.ordinaryCalls {
				t.Fatalf("ordinary authorization changed: allow=%t calls=%d", ordinary.Allow, calls)
			}
			calls = 0
			got := az.AuthorizeDisclosure(context.Background(), req)
			if got != ordinary || got.Allow != tc.wantAllow || calls != 1 {
				t.Fatalf("disclosure must preserve every term and evaluate policy once: got=%+v ordinary=%+v calls=%d", got, ordinary, calls)
			}
		})
	}
}

func TestDisclosureAuthorizationOverlayFailuresStayClosed(t *testing.T) {
	const tenant model.TenantID = "11111111-1111-1111-1111-111111111111"
	req := Request{Principal: ScopedPrincipal("viewer", "disclosure", tenant, RoleViewer), Permission: "sessions:run:read", Tenant: tenant}
	for _, tc := range []struct {
		name      string
		evaluator evalFunc
		class     DecisionClass
	}{
		{"policy forbid", func(context.Context, Request) (Decision, error) {
			return Decision{Allow: false, Class: ClassPolicy, Reason: "hidden"}, nil
		}, ClassPolicy},
		{"policy error", func(context.Context, Request) (Decision, error) { return Decision{}, errors.New("unavailable") }, ClassInvariant},
		{"policy panic", func(context.Context, Request) (Decision, error) { panic("unavailable") }, ClassInvariant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			az := NewAuthorizer(tc.evaluator)
			got := az.AuthorizeDisclosure(context.Background(), req)
			if got.Allow || got.Class != tc.class || got != az.Authorize(context.Background(), req) {
				t.Fatalf("disclosure must remain fully restricted and retain deny provenance: %+v", got)
			}
		})
	}
	// No policy is also a supported configuration; scoped grants still work.
	req.Principal = ScopedPrincipal("unknown", "disclosure", tenant, "unknown")
	if !NewAuthorizer(nil, WithScopedGrants(effect(EffectGrant))).AuthorizeDisclosure(context.Background(), req).Allow {
		t.Fatal("nil overlay lost independent scoped read authority")
	}
}
