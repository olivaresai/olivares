// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

type explanationEngine struct {
	decision ScopedDecision
	reason   DecisionExplanation
	err      error
	panics   bool
	calls    int
}

func (e *explanationEngine) Scoped(context.Context, Request) (ScopedDecision, error) {
	return e.decision, e.err
}

func (e *explanationEngine) ScopedExplanation(context.Context, Request) (ScopedDecision, DecisionExplanation, error) {
	e.calls++
	if e.panics {
		panic("unavailable")
	}
	return e.decision, e.reason, e.err
}

func TestAuthorizeExplanationPreservesDecisionAndEvaluatesOnce(t *testing.T) {
	tenant := model.TenantID("11111111-1111-1111-1111-111111111111")
	req := Request{Principal: ScopedPrincipal("u", "test", tenant, RoleViewer), Permission: Permission("agent:write"), Tenant: tenant}
	for _, tc := range []struct {
		name     string
		decision ScopedDecision
		err      error
	}{
		{name: "grant", decision: ScopedDecision{Effect: EffectGrant}},
		{name: "forbid", decision: ScopedDecision{Effect: EffectForbid, Class: ClassPolicy}},
		{name: "filter", decision: ScopedDecision{Effect: EffectAbstain, InheritanceFiltered: true}},
		{name: "abstain"},
		{name: "error", err: errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &explanationEngine{decision: tc.decision, err: tc.err}
			az := NewAuthorizer(nil, WithScopedGrants(engine))
			want := az.Authorize(context.Background(), req)
			got, _ := az.AuthorizeExplanation(context.Background(), req)
			if !reflect.DeepEqual(got, want) || engine.calls != 1 {
				t.Fatalf("explained = %+v, ordinary = %+v, evaluations = %d", got, want, engine.calls)
			}
		})
	}
}

func TestAuthorizeExplanationPanicDiscardsProvenance(t *testing.T) {
	tenant := model.TenantID("11111111-1111-1111-1111-111111111111")
	engine := &explanationEngine{panics: true, reason: DecisionExplanation{Because: []DecisionGrant{{GrantID: "stale"}}}}
	az := NewAuthorizer(nil, WithScopedGrants(engine))
	decision, why := az.AuthorizeExplanation(context.Background(), Request{Principal: ScopedPrincipal("u", "test", tenant, RoleOwner), Permission: Permission("agent:write"), Tenant: tenant})
	if decision.Allow || decision.Class != ClassInvariant || len(why.Because) != 0 || len(why.RemovedBy) != 0 {
		t.Fatalf("panic must fail closed without stale provenance: %+v %+v", decision, why)
	}
}
