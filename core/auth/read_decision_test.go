// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"testing"
	"time"
)

func TestRouteReadDecisionKeepsDeniedDependenciesAndExactQuestion(t *testing.T) {
	f, p := resolvedPrincipalAuthorityEvidence(t)
	var lineage store.AuthorizationFactRef
	if err := f.raw.View(f.ctx, f.tenant, func(sc store.Scope) error {
		var e error
		lineage, e = store.ReadLineageFact(f.ctx, sc, "core.session")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	now := p.evidence.observedAt.Add(time.Second)
	scoped := &principalAuthorityScopedProducer{decision: principalAuthorityBrokenScoped(p.evidence.observedAt, p.evidence.freshUntil, lineage)}
	policy := &principalAuthorityPolicyProducer{decision: principalAuthorityCleanPolicy(p.evidence.observedAt, p.evidence.freshUntil)}
	az := NewAuthorizer(policy, WithScopedGrants(scoped), WithClock(func() time.Time { return now }))
	req := principalAuthorityEvidenceRequest(p, f.tenant)
	req.Resource = ResourceAttrs{Kind: "session", ID: model.NewID().String()}
	decision, err := az.DecideRouteRead(f.ctx, req)
	if err != nil || decision.Allowed() {
		t.Fatalf("denied receipt unavailable: %v", err)
	}
	if scoped.typedCalls != 1 || policy.typedCalls != 1 || scoped.legacyCalls != 0 || policy.legacyCalls != 0 {
		t.Fatal("read evaluated twice or used boolean adapter")
	}
	if decision.AllowWitness().minted {
		t.Fatal("denial became allowance")
	}
	bundle, err := decision.AuthorityFor(now, req)
	if err != nil || len(bundle.Facts) != 2 || len(bundle.UserAuthorities) != 1 {
		t.Fatalf("denial lost dependencies: %v / %v", bundle, err)
	}
	if err = f.raw.View(f.ctx, f.tenant, func(sc store.Scope) error { return store.ValidateReadAuthorityBundle(f.ctx, sc, bundle) }); err != nil {
		t.Fatal(err)
	}
	for name, alter := range map[string]func(*Request){
		"tenant":     func(r *Request) { r.Tenant = model.NewTenantID() },
		"resource":   func(r *Request) { r.Resource.ID = model.NewID().String() },
		"permission": func(r *Request) { r.Permission = "agent:write" },
		"route":      func(r *Request) { r.Route.SessionInheritsAgentGroups = true },
		"principal":  func(r *Request) { r.Principal.AAL = AAL3 },
	} {
		t.Run(name, func(t *testing.T) {
			other := req
			alter(&other)
			if _, e := decision.AuthorityFor(now, other); e == nil {
				t.Fatal("receipt transplanted to another question")
			}
		})
	}
	for _, clock := range []time.Time{p.evidence.observedAt.Add(-time.Nanosecond), p.evidence.freshUntil} {
		if _, e := decision.AuthorityFor(clock, req); e == nil {
			t.Fatal("receipt accepted outside finite interval")
		}
	}
	if _, e := (RouteReadDecision{}).AuthorityFor(now, req); e == nil {
		t.Fatal("zero receipt accepted")
	}
	if err = f.raw.Mutate(f.ctx, f.tenant, func(sc store.Scope) error {
		_, e := sc.Sessions().Create(f.ctx, model.Session{ExternalID: "absence-now-present"})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if _, e := decision.AuthorityFor(now, req); e != nil {
		t.Fatal("stable digest should still verify before current DB comparison")
	}
	if err = f.raw.View(f.ctx, f.tenant, func(sc store.Scope) error { return store.ValidateReadAuthorityBundle(f.ctx, sc, bundle) }); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("unexpired denied receipt survived current DB drift: %v", err)
	}
}

func TestRouteReadDecisionRejectsContradictionsAndUnsealedPrincipal(t *testing.T) {
	f, p := resolvedPrincipalAuthorityEvidence(t)
	now := p.evidence.observedAt.Add(time.Second)
	fact := p.evidence.directoryEpoch
	scoped := &principalAuthorityScopedProducer{decision: principalAuthorityBrokenScoped(p.evidence.observedAt, p.evidence.freshUntil, fact)}
	changed := fact
	changed.Version++
	policy := &principalAuthorityPolicyProducer{decision: principalAuthorityCleanPolicy(p.evidence.observedAt, p.evidence.freshUntil, changed)}
	az := NewAuthorizer(policy, WithScopedGrants(scoped), WithClock(func() time.Time { return now }))
	req := principalAuthorityEvidenceRequest(p, f.tenant)
	if _, e := az.DecideRouteRead(context.Background(), req); e == nil {
		t.Fatal("definite denial hid contradictory consulted facts")
	}
	req.Principal = Principal{Kind: KindUser, UserID: p.UserID, CredID: p.CredID}
	if _, e := az.DecideRouteRead(context.Background(), req); e == nil {
		t.Fatal("unsealed principal received durable read receipt")
	}
}
