// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// clsEval adapts a func to auth.PolicyEvaluator (the deny-overlay).
type clsEval func(context.Context, auth.Request) (auth.Decision, error)

func (f clsEval) Evaluate(ctx context.Context, req auth.Request) (auth.Decision, error) {
	return f(ctx, req)
}

// clsScoped adapts a func to auth.ScopedAuthorizer (positive-grant / forbid seam).
type clsScoped func(context.Context, auth.Request) (auth.ScopedDecision, error)

func (f clsScoped) Scoped(ctx context.Context, req auth.Request) (auth.ScopedDecision, error) {
	return f(ctx, req)
}

func clsReq() auth.Request {
	return auth.Request{
		Principal:  auth.Principal{Kind: auth.KindUser, UserID: "u-1"},
		Permission: "agent:write",
		Tenant:     model.NewTenantID(),
	}
}

// E1b — the Authorizer must PROPAGATE the scoped forbid's provenance, never
// assume it. A ClassPolicy scoped forbid stays shadowable; a ClassInvariant one
// (workspace confinement / errored / fail-closed) stays non-shadowable.
func TestAuthorizePropagatesScopedForbidClass(t *testing.T) {
	ctx := context.Background()

	policyForbid := auth.NewAuthorizer(nil, auth.WithScopedGrants(
		clsScoped(func(context.Context, auth.Request) (auth.ScopedDecision, error) {
			return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "scoped policy", Class: auth.ClassPolicy}, nil
		})))
	if dec := policyForbid.Authorize(ctx, clsReq()); dec.Allow || dec.Class != auth.ClassPolicy {
		t.Fatalf("scoped policy forbid must deny and propagate ClassPolicy, got allow=%v class=%d", dec.Allow, dec.Class)
	}

	confinement := auth.NewAuthorizer(nil, auth.WithScopedGrants(
		clsScoped(func(context.Context, auth.Request) (auth.ScopedDecision, error) {
			return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "workspace confinement", Class: auth.ClassInvariant}, nil
		})))
	if dec := confinement.Authorize(ctx, clsReq()); dec.Allow || dec.Class != auth.ClassInvariant {
		t.Fatalf("confinement forbid must deny and stay ClassInvariant, got allow=%v class=%d", dec.Allow, dec.Class)
	}

	// A scoped forbid with no explicit class (the fail-safe default) must stay invariant.
	unclassified := auth.NewAuthorizer(nil, auth.WithScopedGrants(
		clsScoped(func(context.Context, auth.Request) (auth.ScopedDecision, error) {
			return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "unclassified"}, nil
		})))
	if dec := unclassified.Authorize(ctx, clsReq()); dec.Class != auth.ClassInvariant {
		t.Fatalf("an unclassified scoped forbid must default to ClassInvariant, got %d", dec.Class)
	}
}

// E1b — the Authorizer must propagate the deny-overlay's provenance. A grant
// carries the request past RBAC so the overlay runs; its ClassPolicy deny must survive.
func TestAuthorizePropagatesOverlayDenyClass(t *testing.T) {
	ctx := context.Background()
	grant := clsScoped(func(context.Context, auth.Request) (auth.ScopedDecision, error) {
		return auth.ScopedDecision{Effect: auth.EffectGrant}, nil
	})

	policyOverlay := auth.NewAuthorizer(
		clsEval(func(context.Context, auth.Request) (auth.Decision, error) {
			return auth.Decision{Allow: false, Reason: "abac policy", Class: auth.ClassPolicy}, nil
		}), auth.WithScopedGrants(grant))
	if dec := policyOverlay.Authorize(ctx, clsReq()); dec.Allow || dec.Class != auth.ClassPolicy {
		t.Fatalf("overlay policy deny must deny and propagate ClassPolicy, got allow=%v class=%d", dec.Allow, dec.Class)
	}

	// An overlay deny with no explicit class stays invariant (fail-safe default).
	invariantOverlay := auth.NewAuthorizer(
		clsEval(func(context.Context, auth.Request) (auth.Decision, error) {
			return auth.Decision{Allow: false, Reason: "tamper"}, nil
		}), auth.WithScopedGrants(grant))
	if dec := invariantOverlay.Authorize(ctx, clsReq()); dec.Class != auth.ClassInvariant {
		t.Fatalf("an unclassified overlay deny must default to ClassInvariant, got %d", dec.Class)
	}

	// A fail-closed overlay ERROR must remain a non-shadowable invariant deny.
	failClosed := auth.NewAuthorizer(
		clsEval(func(context.Context, auth.Request) (auth.Decision, error) {
			return auth.Decision{}, context.DeadlineExceeded
		}), auth.WithScopedGrants(grant))
	if dec := failClosed.Authorize(ctx, clsReq()); dec.Allow || dec.Class != auth.ClassInvariant {
		t.Fatalf("overlay evaluation error must deny closed as ClassInvariant, got allow=%v class=%d", dec.Allow, dec.Class)
	}
}
