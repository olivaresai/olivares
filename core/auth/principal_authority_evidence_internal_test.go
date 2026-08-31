// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type principalAuthorityScopedProducer struct {
	decision    ScopedEvidenceDecision
	err         error
	inspect     func(Request)
	typedCalls  int
	legacyCalls int
}

func (p *principalAuthorityScopedProducer) Scoped(
	context.Context,
	Request,
) (ScopedDecision, error) {
	p.legacyCalls++
	return ScopedDecision{Effect: EffectForbid}, errors.New("legacy scoped method called")
}

func (p *principalAuthorityScopedProducer) ScopedEvidence(
	_ context.Context,
	req Request,
) (ScopedEvidenceDecision, error) {
	p.typedCalls++
	if p.inspect != nil {
		p.inspect(req)
	}
	return p.decision, p.err
}

type principalAuthorityPolicyProducer struct {
	decision    PolicyEvidenceDecision
	err         error
	inspect     func(Request)
	typedCalls  int
	legacyCalls int
}

var (
	principalAuthorityNilScopedCalls int
	principalAuthorityNilPolicyCalls int
)

type principalAuthorityNilScopedProducer struct{}

func (*principalAuthorityNilScopedProducer) Scoped(
	context.Context,
	Request,
) (ScopedDecision, error) {
	principalAuthorityNilScopedCalls++
	return ScopedDecision{}, nil
}

func (*principalAuthorityNilScopedProducer) ScopedEvidence(
	context.Context,
	Request,
) (ScopedEvidenceDecision, error) {
	principalAuthorityNilScopedCalls++
	return ScopedEvidenceDecision{}, nil
}

type principalAuthorityNilPolicyProducer struct{}

func (*principalAuthorityNilPolicyProducer) Evaluate(
	context.Context,
	Request,
) (Decision, error) {
	principalAuthorityNilPolicyCalls++
	return Decision{}, nil
}

func (*principalAuthorityNilPolicyProducer) EvaluateEvidence(
	context.Context,
	Request,
) (PolicyEvidenceDecision, error) {
	principalAuthorityNilPolicyCalls++
	return PolicyEvidenceDecision{}, nil
}

func (p *principalAuthorityPolicyProducer) Evaluate(
	context.Context,
	Request,
) (Decision, error) {
	p.legacyCalls++
	return Decision{}, errors.New("legacy policy method called")
}

func (p *principalAuthorityPolicyProducer) EvaluateEvidence(
	_ context.Context,
	req Request,
) (PolicyEvidenceDecision, error) {
	p.typedCalls++
	if p.inspect != nil {
		p.inspect(req)
	}
	return p.decision, p.err
}

func principalAuthorityCleanScoped(
	observedAt, freshUntil time.Time,
	facts ...store.AuthorizationFactRef,
) ScopedEvidenceDecision {
	return ScopedEvidenceDecision{
		Effect:        EffectAbstain,
		ResourceGuard: CheckEvidence{Verdict: CheckClean, Code: "resource_guard_clean"},
		ForbidAbsence: CheckEvidence{Verdict: CheckClean, Code: "scoped_forbid_absent"},
		Facts:         facts,
		ObservedAt:    observedAt,
		FreshUntil:    freshUntil,
	}
}

func principalAuthorityCleanPolicy(
	observedAt, freshUntil time.Time,
	facts ...store.AuthorizationFactRef,
) PolicyEvidenceDecision {
	return PolicyEvidenceDecision{
		ForbidAbsence: CheckEvidence{Verdict: CheckClean, Code: "policy_forbid_absent"},
		Facts:         facts,
		ObservedAt:    observedAt,
		FreshUntil:    freshUntil,
	}
}

func principalAuthorityBrokenScoped(
	observedAt, freshUntil time.Time,
	facts ...store.AuthorizationFactRef,
) ScopedEvidenceDecision {
	return ScopedEvidenceDecision{
		Effect:        EffectForbid,
		ResourceGuard: CheckEvidence{Verdict: CheckBroken, Code: "resource_guard_broken"},
		ForbidAbsence: CheckEvidence{Verdict: CheckClean, Code: "scoped_forbid_absent"},
		Facts:         facts,
		ObservedAt:    observedAt,
		FreshUntil:    freshUntil,
	}
}

func principalAuthorityBrokenPolicy(
	observedAt, freshUntil time.Time,
	facts ...store.AuthorizationFactRef,
) PolicyEvidenceDecision {
	return PolicyEvidenceDecision{
		ForbidAbsence: CheckEvidence{Verdict: CheckBroken, Code: "policy_forbid_matched"},
		Facts:         facts,
		ObservedAt:    observedAt,
		FreshUntil:    freshUntil,
	}
}

func resolvedPrincipalAuthorityEvidence(t *testing.T) (*principalEvidenceFixture, Principal) {
	t.Helper()
	f := newPrincipalEvidenceFixture(t)
	ref := f.sessionRef()
	principal, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), ref, f.tenant)
	if err != nil {
		t.Fatalf("ResolvePrincipalScope: %v", err)
	}
	if !validPrincipalAuthoritySeal(principal) {
		t.Fatal("resolved principal has no valid authority seal")
	}
	return f, principal
}

func principalAuthorityEvidenceRequest(principal Principal, tenant model.TenantID) Request {
	permission := Permission("agent:read")
	return Request{
		Principal:  principal,
		Tenant:     tenant,
		Permission: permission,
		Resource:   ResourceFor(permission),
	}
}

func requireUnknownPrincipalAuthorityEvidence(t *testing.T, got AuthorizationEvidence) {
	t.Helper()
	if got.Outcome != EvidenceUnknown || got.CorePermission.Verdict != CheckUnknown {
		t.Fatalf("authority mutation = %+v, want Core UNKNOWN", got)
	}
	if len(got.Facts) != 0 || !got.ObservedAt.IsZero() || !got.FreshUntil.IsZero() {
		t.Fatalf("UNKNOWN retained facts/window: %+v", got)
	}
}

func TestPrincipalAuthorityEvidenceResolvedPrincipalAllows(t *testing.T) {
	f, principal := resolvedPrincipalAuthorityEvidence(t)
	got := NewAuthorizer(nil).AuthorizeEvidence(
		context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
	)
	if got.Outcome != EvidenceAllow || got.CorePermission.Verdict != CheckClean ||
		got.ResourceGuard.Verdict != CheckClean || got.ForbidAbsence.Verdict != CheckClean {
		t.Fatalf("resolved authority evidence = %+v, want ALLOW", got)
	}
	wantFact := principal.evidence.directoryEpoch
	if len(got.Facts) != 1 || got.Facts[0] != wantFact {
		t.Fatalf("facts = %+v, want exact directory epoch %+v", got.Facts, wantFact)
	}
	if !got.ObservedAt.Equal(principal.evidence.observedAt) ||
		!got.FreshUntil.Equal(principal.evidence.freshUntil) {
		t.Fatalf("window = [%s,%s], want principal provenance [%s,%s]",
			got.ObservedAt, got.FreshUntil, principal.evidence.observedAt, principal.evidence.freshUntil)
	}
}

func TestPrincipalAuthorityEvidenceMergesFactsAndIntersectsWindows(t *testing.T) {
	f, principal := resolvedPrincipalAuthorityEvidence(t)
	authorizationFact := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: 23,
	}
	scopedObserved := principal.evidence.observedAt.Add(2 * time.Second)
	scopedFresh := principal.evidence.freshUntil.Add(-2 * time.Minute)
	policyObserved := principal.evidence.observedAt.Add(5 * time.Second)
	policyFresh := principal.evidence.freshUntil.Add(-5 * time.Minute)
	scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
		scopedObserved, scopedFresh, authorizationFact,
	)}
	policy := &principalAuthorityPolicyProducer{decision: principalAuthorityCleanPolicy(
		policyObserved, policyFresh, authorizationFact,
	)}
	got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
		context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
	)
	if got.Outcome != EvidenceAllow || got.CorePermission.Verdict != CheckClean {
		t.Fatalf("merged evidence = %+v, want ALLOW", got)
	}
	if len(got.Facts) != 2 {
		t.Fatalf("merged facts = %+v, want principal + deduplicated authorization epochs", got.Facts)
	}
	want := map[store.AuthorizationFactRef]bool{
		principal.evidence.directoryEpoch: false,
		authorizationFact:                 false,
	}
	for _, fact := range got.Facts {
		if _, ok := want[fact]; !ok {
			t.Fatalf("unexpected merged fact %+v in %+v", fact, got.Facts)
		}
		want[fact] = true
	}
	for fact, seen := range want {
		if !seen {
			t.Fatalf("merged facts omitted %+v: %+v", fact, got.Facts)
		}
	}
	if !got.ObservedAt.Equal(policyObserved) || !got.FreshUntil.Equal(policyFresh) {
		t.Fatalf("merged window = [%s,%s], want [%s,%s]",
			got.ObservedAt, got.FreshUntil, policyObserved, policyFresh)
	}
	if scoped.typedCalls != 1 || policy.typedCalls != 1 ||
		scoped.legacyCalls != 0 || policy.legacyCalls != 0 {
		t.Fatalf("producer calls scoped=%d/%d policy=%d/%d",
			scoped.typedCalls, scoped.legacyCalls, policy.typedCalls, policy.legacyCalls)
	}

	t.Run("conflicting versions erase all reusable evidence", func(t *testing.T) {
		conflict := authorizationFact
		conflict.Version++
		conflictingPolicy := &principalAuthorityPolicyProducer{decision: principalAuthorityCleanPolicy(
			policyObserved, policyFresh, conflict,
		)}
		got := NewAuthorizer(conflictingPolicy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		if got.Outcome != EvidenceUnknown || len(got.Facts) != 0 ||
			!got.ObservedAt.IsZero() || !got.FreshUntil.IsZero() {
			t.Fatalf("conflicting versions retained reusable evidence: %+v", got)
		}
	})
}

func TestPrincipalAuthorityEvidenceFactLimitCountsPrincipalEpoch(t *testing.T) {
	for _, test := range []struct {
		name        string
		external    int
		wantOutcome EvidenceOutcome
		wantFacts   int
	}{
		{name: "63 external plus principal is 64", external: 63, wantOutcome: EvidenceAllow, wantFacts: 64},
		{name: "64 external plus principal exceeds limit", external: 64, wantOutcome: EvidenceUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, principal := resolvedPrincipalAuthorityEvidence(t)
			facts := make([]store.AuthorizationFactRef, 0, test.external)
			for i := 0; i < test.external; i++ {
				facts = append(facts, store.AuthorizationFactRef{
					Kind: model.Kind("test.authority_fact"), ID: model.NewID(), Version: 1,
				})
			}
			scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
				principal.evidence.observedAt,
				principal.evidence.freshUntil,
				facts...,
			)}
			got := NewAuthorizer(nil, WithScopedGrants(scoped)).AuthorizeEvidence(
				context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
			)
			if got.Outcome != test.wantOutcome || len(got.Facts) != test.wantFacts {
				t.Fatalf("fact boundary outcome=%v facts=%d, want %v/%d: %+v",
					got.Outcome, len(got.Facts), test.wantOutcome, test.wantFacts, got)
			}
			if got.Outcome == EvidenceUnknown &&
				(!got.ObservedAt.IsZero() || !got.FreshUntil.IsZero()) {
				t.Fatalf("fact overflow retained evidence window: %+v", got)
			}
		})
	}
}

func TestPrincipalAuthorityEvidenceFactsAreCanonicallyOrdered(t *testing.T) {
	f, principal := resolvedPrincipalAuthorityEvidence(t)
	lower := store.AuthorizationFactRef{
		Kind: model.Kind("test.order_fact"),
		ID:   model.ID("11111111-1111-7111-8111-111111111112"), Version: 1,
	}
	higher := store.AuthorizationFactRef{
		Kind: lower.Kind,
		ID:   model.ID("99999999-9999-7999-8999-999999999999"), Version: 1,
	}
	scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
		principal.evidence.observedAt,
		principal.evidence.freshUntil,
		higher,
		lower,
	)}
	got := NewAuthorizer(nil, WithScopedGrants(scoped)).AuthorizeEvidence(
		context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
	)
	want := []store.AuthorizationFactRef{principal.evidence.directoryEpoch, lower, higher}
	if got.Outcome != EvidenceAllow || len(got.Facts) != len(want) {
		t.Fatalf("canonical-order result = %+v, want ALLOW with %d facts", got, len(want))
	}
	for i := range want {
		if got.Facts[i] != want[i] {
			t.Fatalf("canonical fact %d = %+v, want %+v; all=%+v", i, got.Facts[i], want[i], got.Facts)
		}
	}
}

func TestPrincipalAuthorityEvidenceIsolatesProducerInputsAndTypedNil(t *testing.T) {
	f, principal := resolvedPrincipalAuthorityEvidence(t)
	authorizationFact := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: 31,
	}
	req := principalAuthorityEvidenceRequest(principal, f.tenant)
	req.Resource.Extra = map[string]string{"source": "original"}
	originalAMR := append([]string(nil), principal.AMR...)

	scoped := &principalAuthorityScopedProducer{
		decision: principalAuthorityCleanScoped(
			principal.evidence.observedAt, principal.evidence.freshUntil, authorizationFact,
		),
		inspect: func(seen Request) {
			seen.Principal.AMR[0] = "mutated-by-scoped"
			seen.Principal.grants[f.tenant] = RoleOwner
			seen.Resource.Extra["source"] = "mutated-by-scoped"
		},
	}
	policy := &principalAuthorityPolicyProducer{
		decision: principalAuthorityCleanPolicy(
			principal.evidence.observedAt, principal.evidence.freshUntil, authorizationFact,
		),
		inspect: func(seen Request) {
			if len(seen.Principal.AMR) != len(originalAMR) ||
				seen.Principal.AMR[0] != originalAMR[0] ||
				seen.Principal.grants[f.tenant] != RoleViewer ||
				seen.Resource.Extra["source"] != "original" {
				t.Fatalf("policy saw aliases mutated by scoped producer: %+v", seen)
			}
			seen.Principal.AMR[0] = "mutated-by-policy"
			seen.Principal.grants[f.tenant] = RoleOwner
			seen.Resource.Extra["source"] = "mutated-by-policy"
		},
	}
	got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
		context.Background(), req,
	)
	if got.Outcome != EvidenceAllow {
		t.Fatalf("hostile producer isolation outcome = %+v, want ALLOW", got)
	}
	if req.Principal.AMR[0] != originalAMR[0] ||
		req.Principal.grants[f.tenant] != RoleViewer ||
		req.Resource.Extra["source"] != "original" || !validPrincipalAuthoritySeal(req.Principal) {
		t.Fatalf("producers mutated caller snapshot: %+v / %+v", req.Principal, req.Resource)
	}

	t.Run("normalized facts do not alias a later producer", func(t *testing.T) {
		first := store.AuthorizationFactRef{
			Kind: model.Kind("test.alias_fact"), ID: model.NewID(), Version: 1,
		}
		second := store.AuthorizationFactRef{
			Kind: first.Kind, ID: model.NewID(), Version: 1,
		}
		shared := []store.AuthorizationFactRef{first}
		factScoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
			principal.evidence.observedAt, principal.evidence.freshUntil, shared...,
		)}
		factPolicy := &principalAuthorityPolicyProducer{
			decision: principalAuthorityCleanPolicy(
				principal.evidence.observedAt, principal.evidence.freshUntil, shared...,
			),
			inspect: func(Request) { shared[0] = second },
		}
		got := NewAuthorizer(factPolicy, WithScopedGrants(factScoped)).AuthorizeEvidence(
			context.Background(), req,
		)
		if got.Outcome != EvidenceAllow || len(got.Facts) != 3 {
			t.Fatalf("fact alias result = %+v, want principal+first+second", got)
		}
		seen := map[store.AuthorizationFactRef]bool{}
		for _, fact := range got.Facts {
			seen[fact] = true
		}
		if !seen[first] || !seen[second] || !seen[principal.evidence.directoryEpoch] {
			t.Fatalf("fact alias lost an observed snapshot: %+v", got.Facts)
		}
	})

	principalAuthorityNilScopedCalls = 0
	principalAuthorityNilPolicyCalls = 0
	var nilScoped *principalAuthorityNilScopedProducer
	var nilPolicy *principalAuthorityNilPolicyProducer
	unknown := NewAuthorizer(nilPolicy, WithScopedGrants(nilScoped)).AuthorizeEvidence(
		context.Background(), req,
	)
	if unknown.Outcome != EvidenceUnknown || principalAuthorityNilScopedCalls != 0 ||
		principalAuthorityNilPolicyCalls != 0 {
		t.Fatalf("typed-nil producers result=%+v calls=%d/%d, want UNKNOWN and zero calls",
			unknown, principalAuthorityNilScopedCalls, principalAuthorityNilPolicyCalls)
	}
}

func TestPrincipalAuthorityEvidenceDeepCopiesNestedAuthorityMaps(t *testing.T) {
	t.Run("session groups and confinement", func(t *testing.T) {
		f := newPrincipalEvidenceFixture(t)
		workspace := model.NewID()
		if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
			membership, err := as.Memberships().Get(f.ctx, f.member.ID)
			if err != nil {
				return err
			}
			membership.WorkspaceID = workspace
			_, err = as.Memberships().Update(f.ctx, membership)
			return err
		}); err != nil {
			t.Fatalf("confine fixture membership: %v", err)
		}
		group := f.addGroupWithMember(f.tenant, "authority-copy", "")
		ref := f.sessionRef()
		principal, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), ref, f.tenant)
		if err != nil {
			t.Fatalf("ResolvePrincipalScope: %v", err)
		}
		originalGroups := principal.GroupsIn(f.tenant)
		originalWorkspace, confined := principal.ConfinedWorkspaceIn(f.tenant)
		if len(originalGroups) != 1 || originalGroups[0] != group.ID.String() ||
			!confined || originalWorkspace != workspace {
			t.Fatalf("nested authority fixture = groups %v confinement %s/%t",
				originalGroups, originalWorkspace, confined)
		}
		scoped := &principalAuthorityScopedProducer{
			decision: principalAuthorityCleanScoped(
				principal.evidence.observedAt, principal.evidence.freshUntil,
			),
			inspect: func(seen Request) {
				seen.Principal.groups[f.tenant][0] = model.NewID().String()
				seen.Principal.confined[f.tenant] = model.NewID()
			},
		}
		policy := &principalAuthorityPolicyProducer{
			decision: principalAuthorityCleanPolicy(
				principal.evidence.observedAt, principal.evidence.freshUntil,
			),
			inspect: func(seen Request) {
				groups := seen.Principal.GroupsIn(f.tenant)
				gotWorkspace, gotConfined := seen.Principal.ConfinedWorkspaceIn(f.tenant)
				if len(groups) != 1 || groups[0] != group.ID.String() ||
					!gotConfined || gotWorkspace != workspace {
					t.Fatalf("policy saw nested aliases mutated by scoped: %v / %s/%t",
						groups, gotWorkspace, gotConfined)
				}
			},
		}
		req := principalAuthorityEvidenceRequest(principal, f.tenant)
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), req,
		)
		if got.Outcome != EvidenceAllow || !validPrincipalAuthoritySeal(req.Principal) ||
			req.Principal.groups[f.tenant][0] != group.ID.String() ||
			req.Principal.confined[f.tenant] != workspace {
			t.Fatalf("nested session authority aliases escaped: %+v / %+v", got, req.Principal)
		}
	})

	t.Run("purpose restriction inner set", func(t *testing.T) {
		f := newPrincipalEvidenceFixture(t)
		system, err := NewSystemOperator("test:authority-copy", "nested restriction copy")
		if err != nil {
			t.Fatalf("system operator: %v", err)
		}
		issued, err := NewAuthenticator(f.st, model.SystemClock{}).IssueCommunicationSessionCredential(
			f.ctx,
			system,
			CommunicationSessionCredentialSpec{
				Tenant: f.tenant, WorkspaceID: model.NewID(),
				SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(),
				ClaimFence: 5,
			},
		)
		if err != nil {
			t.Fatalf("issue communication credential: %v", err)
		}
		authenticated, err := f.a.Authenticate(f.ctx, issued.Token)
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		ref, ok := authenticated.Ref()
		if !ok {
			t.Fatal("communication credential has no PrincipalRef")
		}
		principal, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), ref, f.tenant)
		if err != nil {
			t.Fatalf("ResolvePrincipalScope: %v", err)
		}
		scoped := &principalAuthorityScopedProducer{
			decision: principalAuthorityCleanScoped(
				principal.evidence.observedAt, principal.evidence.freshUntil,
			),
			inspect: func(seen Request) {
				delete(seen.Principal.restricted[f.tenant], CommunicationSessionDeliveryRead)
				seen.Principal.restricted[f.tenant][Permission("agent:write")] = struct{}{}
			},
		}
		policy := &principalAuthorityPolicyProducer{
			decision: principalAuthorityCleanPolicy(
				principal.evidence.observedAt, principal.evidence.freshUntil,
			),
			inspect: func(seen Request) {
				permissions, restricted := seen.Principal.PurposePermissionsIn(f.tenant)
				if !restricted || len(permissions) != 4 {
					t.Fatalf("policy saw mutated restriction set: %v/%t", permissions, restricted)
				}
			},
		}
		req := Request{
			Principal: principal, Tenant: f.tenant,
			Permission: CommunicationSessionDeliveryRead,
			Resource:   ResourceFor(CommunicationSessionDeliveryRead),
		}
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), req,
		)
		if got.Outcome != EvidenceAllow || !validPrincipalAuthoritySeal(req.Principal) {
			t.Fatalf("nested restriction alias escaped: %+v", got)
		}
	})
}

func TestPrincipalAuthorityEvidenceDenySelectsOneDeterministicProof(t *testing.T) {
	f, principal := resolvedPrincipalAuthorityEvidence(t)
	scopedFact := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: 41,
	}
	policyFact := scopedFact
	policyFact.Version++
	scopedObserved := principal.evidence.observedAt.Add(time.Second)
	scopedFresh := principal.evidence.freshUntil.Add(-time.Minute)
	policyObserved := principal.evidence.observedAt.Add(2 * time.Second)
	policyFresh := principal.evidence.freshUntil.Add(-2 * time.Minute)

	assertProof := func(
		t *testing.T,
		got AuthorizationEvidence,
		wantFact store.AuthorizationFactRef,
		wantObserved, wantFresh time.Time,
	) {
		t.Helper()
		if got.Outcome != EvidenceDeny || len(got.Facts) != 1 || got.Facts[0] != wantFact ||
			!got.ObservedAt.Equal(wantObserved) || !got.FreshUntil.Equal(wantFresh) {
			t.Fatalf("deny proof = %+v, want fact=%+v window=[%s,%s]",
				got, wantFact, wantObserved, wantFresh)
		}
	}

	t.Run("scoped negative dominates conflicting policy negative", func(t *testing.T) {
		scoped := &principalAuthorityScopedProducer{decision: principalAuthorityBrokenScoped(
			scopedObserved, scopedFresh, scopedFact,
		)}
		policy := &principalAuthorityPolicyProducer{decision: principalAuthorityBrokenPolicy(
			policyObserved, policyFresh, policyFact,
		)}
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		assertProof(t, got, scopedFact, scopedObserved, scopedFresh)
	})

	t.Run("core RBAC negative uses typed no-grant witness before policy", func(t *testing.T) {
		scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
			scopedObserved, scopedFresh, scopedFact,
		)}
		policy := &principalAuthorityPolicyProducer{decision: principalAuthorityBrokenPolicy(
			policyObserved, policyFresh, policyFact,
		)}
		req := principalAuthorityEvidenceRequest(principal, f.tenant)
		req.Permission = Permission("agent:write")
		req.Resource = ResourceFor(req.Permission)
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), req,
		)
		if got.CorePermission.Verdict != CheckBroken || got.ForbidAbsence.Verdict != CheckBroken {
			t.Fatalf("dual negative components = %+v, want core and policy BROKEN", got)
		}
		assertProof(t, got, scopedFact, scopedObserved, scopedFresh)
	})

	t.Run("scoped clean conflict cannot erase policy negative", func(t *testing.T) {
		scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
			scopedObserved, scopedFresh, scopedFact,
		)}
		policy := &principalAuthorityPolicyProducer{decision: principalAuthorityBrokenPolicy(
			policyObserved, policyFresh, policyFact,
		)}
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		if got.CorePermission.Verdict != CheckClean || got.ForbidAbsence.Verdict != CheckBroken {
			t.Fatalf("policy-only negative components = %+v, want core CLEAN/policy BROKEN", got)
		}
		assertProof(t, got, policyFact, policyObserved, policyFresh)
	})

	t.Run("policy negative survives malformed scoped producer", func(t *testing.T) {
		scoped := &principalAuthorityScopedProducer{decision: ScopedEvidenceDecision{
			Effect: Effect(99),
		}}
		policy := &principalAuthorityPolicyProducer{decision: principalAuthorityBrokenPolicy(
			policyObserved, policyFresh, policyFact,
		)}
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		assertProof(t, got, policyFact, policyObserved, policyFresh)
	})

	t.Run("scoped negative survives malformed policy producer", func(t *testing.T) {
		scoped := &principalAuthorityScopedProducer{decision: principalAuthorityBrokenScoped(
			scopedObserved, scopedFresh, scopedFact,
		)}
		policy := &principalAuthorityPolicyProducer{decision: PolicyEvidenceDecision{
			ForbidAbsence: CheckEvidence{Verdict: CheckVerdict(99), Code: "malformed"},
		}}
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		assertProof(t, got, scopedFact, scopedObserved, scopedFresh)
	})

	t.Run("credential ceiling is self-contained and skips producers", func(t *testing.T) {
		scoped := &principalAuthorityScopedProducer{decision: principalAuthorityBrokenScoped(
			scopedObserved, scopedFresh, scopedFact,
		)}
		policy := &principalAuthorityPolicyProducer{decision: principalAuthorityBrokenPolicy(
			policyObserved, policyFresh, policyFact,
		)}
		restricted := ScopedPrincipal(model.NewID(), "restricted", f.tenant, RoleOwner).
			withRestrictedPermissions(f.tenant, Permission("agent:read"))
		req := principalAuthorityEvidenceRequest(restricted, f.tenant)
		req.Permission = Permission("agent:write")
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), req,
		)
		if got.Outcome != EvidenceDeny || got.CorePermission.Code != "credential_ceiling_denied" ||
			len(got.Facts) != 0 || !got.ObservedAt.IsZero() || !got.FreshUntil.IsZero() ||
			scoped.typedCalls != 0 || policy.typedCalls != 0 {
			t.Fatalf("credential ceiling proof/calls = %+v / %d/%d",
				got, scoped.typedCalls, policy.typedCalls)
		}
	})
}

func TestPrincipalAuthorityEvidenceIgnoresValidDecisionsReturnedWithErrors(t *testing.T) {
	f, principal := resolvedPrincipalAuthorityEvidence(t)
	fact := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: 47,
	}
	observed := principal.evidence.observedAt.Add(time.Second)
	fresh := principal.evidence.freshUntil.Add(-time.Minute)
	producerErr := errors.New("producer returned a value and an error")

	t.Run("scoped broken value is unavailable", func(t *testing.T) {
		scoped := &principalAuthorityScopedProducer{
			decision: principalAuthorityBrokenScoped(observed, fresh, fact),
			err:      producerErr,
		}
		got := NewAuthorizer(nil, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		if got.Outcome != EvidenceUnknown || got.ResourceGuard.Verdict != CheckUnknown ||
			len(got.Facts) != 0 || !got.ObservedAt.IsZero() || !got.FreshUntil.IsZero() {
			t.Fatalf("scoped value+error was trusted: %+v", got)
		}
	})

	t.Run("policy broken value is unavailable", func(t *testing.T) {
		policy := &principalAuthorityPolicyProducer{
			decision: principalAuthorityBrokenPolicy(observed, fresh, fact),
			err:      producerErr,
		}
		got := NewAuthorizer(policy).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		if got.Outcome != EvidenceUnknown || got.ForbidAbsence.Verdict != CheckUnknown ||
			len(got.Facts) != 0 || !got.ObservedAt.IsZero() || !got.FreshUntil.IsZero() {
			t.Fatalf("policy value+error was trusted: %+v", got)
		}
	})

	t.Run("errored scoped value cannot erase independent policy deny", func(t *testing.T) {
		scoped := &principalAuthorityScopedProducer{
			decision: principalAuthorityCleanScoped(observed, fresh, fact),
			err:      producerErr,
		}
		policy := &principalAuthorityPolicyProducer{
			decision: principalAuthorityBrokenPolicy(observed, fresh, fact),
		}
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		if got.Outcome != EvidenceDeny || len(got.Facts) != 1 || got.Facts[0] != fact {
			t.Fatalf("independent policy deny was erased by scoped error: %+v", got)
		}
	})
}

func TestPrincipalAuthorityEvidenceRejectsLeasedEpochsLeaseConflictsAndEmptyWindows(t *testing.T) {
	f, principal := resolvedPrincipalAuthorityEvidence(t)
	observed := principal.evidence.observedAt.Add(time.Second)
	fresh := principal.evidence.freshUntil.Add(-time.Minute)
	leaseDeadline := model.NewTimestamp(fresh)

	for _, kind := range []model.Kind{model.DirectoryEpochKind, model.AuthorizationEpochKind} {
		t.Run("leased "+string(kind), func(t *testing.T) {
			leasedEpoch, err := store.NewLeaseFenceAuthorizationFactRef(
				kind,
				model.ID(f.tenant),
				51,
				"tenant-policy",
				1,
				leaseDeadline,
			)
			if err != nil {
				t.Fatalf("leased epoch: %v", err)
			}
			scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
				observed, fresh, leasedEpoch,
			)}
			got := NewAuthorizer(nil, WithScopedGrants(scoped)).AuthorizeEvidence(
				context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
			)
			if got.Outcome != EvidenceUnknown || len(got.Facts) != 0 ||
				!got.ObservedAt.IsZero() || !got.FreshUntil.IsZero() {
				t.Fatalf("leased epoch was reusable: %+v", got)
			}
		})
	}

	genericID := model.NewID()
	plain := store.AuthorizationFactRef{Kind: model.Kind("test.leased_fact"), ID: genericID, Version: 4}
	leased, err := store.NewLeaseFenceAuthorizationFactRef(
		plain.Kind, plain.ID, plain.Version, "holder", 7, leaseDeadline,
	)
	if err != nil {
		t.Fatalf("generic leased fact: %v", err)
	}
	t.Run("same fact key cannot mix plain and leased witnesses", func(t *testing.T) {
		scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
			observed, fresh, plain,
		)}
		policy := &principalAuthorityPolicyProducer{decision: principalAuthorityCleanPolicy(
			observed, fresh, leased,
		)}
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		if got.Outcome != EvidenceUnknown || len(got.Facts) != 0 {
			t.Fatalf("plain/leased conflict was reusable: %+v", got)
		}
	})

	for _, test := range []struct {
		name     string
		fence    int64
		deadline model.Timestamp
	}{
		{name: "different fence", fence: 8, deadline: leaseDeadline},
		{name: "different deadline", fence: 7, deadline: model.NewTimestamp(fresh.Add(time.Second))},
	} {
		t.Run("same key rejects "+test.name, func(t *testing.T) {
			different, err := store.NewLeaseFenceAuthorizationFactRef(
				plain.Kind, plain.ID, plain.Version, "holder", test.fence, test.deadline,
			)
			if err != nil {
				t.Fatalf("different lease witness: %v", err)
			}
			scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
				observed, fresh, leased,
			)}
			policy := &principalAuthorityPolicyProducer{decision: principalAuthorityCleanPolicy(
				observed, fresh, different,
			)}
			got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
				context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
			)
			if got.Outcome != EvidenceUnknown || len(got.Facts) != 0 {
				t.Fatalf("different lease witness was reusable: %+v", got)
			}
		})
	}

	otherLeased, err := store.NewLeaseFenceAuthorizationFactRef(
		plain.Kind, model.NewID(), plain.Version, "holder", 7, leaseDeadline,
	)
	if err != nil {
		t.Fatalf("second generic leased fact: %v", err)
	}
	t.Run("one lease subject cannot name two fact identities", func(t *testing.T) {
		scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
			observed, fresh, leased,
		)}
		policy := &principalAuthorityPolicyProducer{decision: principalAuthorityCleanPolicy(
			observed, fresh, otherLeased,
		)}
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		if got.Outcome != EvidenceUnknown || len(got.Facts) != 0 {
			t.Fatalf("ambiguous lease subject was reusable: %+v", got)
		}
	})

	t.Run("exact duplicate leased witness deduplicates", func(t *testing.T) {
		scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
			observed, fresh, leased,
		)}
		policy := &principalAuthorityPolicyProducer{decision: principalAuthorityCleanPolicy(
			observed, fresh, leased,
		)}
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		if got.Outcome != EvidenceAllow || len(got.Facts) != 2 {
			t.Fatalf("exact leased witness did not deduplicate with principal fact: %+v", got)
		}
	})

	t.Run("touching contribution windows have an empty intersection", func(t *testing.T) {
		middle := observed.Add(10 * time.Second)
		scoped := &principalAuthorityScopedProducer{decision: principalAuthorityCleanScoped(
			observed, middle, plain,
		)}
		policy := &principalAuthorityPolicyProducer{decision: principalAuthorityCleanPolicy(
			middle, middle.Add(10*time.Second), plain,
		)}
		got := NewAuthorizer(policy, WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), principalAuthorityEvidenceRequest(principal, f.tenant),
		)
		if got.Outcome != EvidenceUnknown || len(got.Facts) != 0 ||
			!got.ObservedAt.IsZero() || !got.FreshUntil.IsZero() {
			t.Fatalf("empty window intersection was reusable: %+v", got)
		}
	})
}

func TestPrincipalAuthorityEvidenceAllowsEveryResolvedTokenShape(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	system, err := NewSystemOperator("test:authority-seal", "resolve every sealed token shape")
	if err != nil {
		t.Fatalf("system operator: %v", err)
	}
	live := NewAuthenticator(f.st, model.SystemClock{})
	work, err := live.IssueWorkSessionCredential(f.ctx, system, WorkSessionCredentialSpec{
		Tenant: f.tenant, SessionRef: "osn_" + model.NewID().String(),
		RunRef: model.NewID().String(), ClaimFence: 3,
	})
	if err != nil {
		t.Fatalf("issue work-session credential: %v", err)
	}
	communication, err := live.IssueCommunicationSessionCredential(
		f.ctx,
		system,
		CommunicationSessionCredentialSpec{
			Tenant: f.tenant, WorkspaceID: model.NewID(),
			SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(),
			ClaimFence: 4,
		},
	)
	if err != nil {
		t.Fatalf("issue communication-session credential: %v", err)
	}

	for _, test := range []struct {
		name       string
		raw        string
		permission Permission
		mutations  []struct {
			name   string
			mutate func(*Principal)
		}
	}{
		{
			name: "ordinary token", raw: f.tokRaw, permission: Permission("agent:read"),
			mutations: []struct {
				name   string
				mutate func(*Principal)
			}{
				{name: "display name", mutate: func(p *Principal) { p.DisplayName += " changed" }},
				{name: "owner", mutate: func(p *Principal) { p.UserID = model.NewID() }},
				{name: "role", mutate: func(p *Principal) { p.grants[f.tenant] = RoleEditor }},
			},
		},
		{
			name: "work-session token", raw: work.Token, permission: WorkSessionLeaseWrite,
			mutations: []struct {
				name   string
				mutate func(*Principal)
			}{
				{name: "agent", mutate: func(p *Principal) { p.AgentIdentity = "agent-mutated" }},
				{name: "session", mutate: func(p *Principal) {
					p.SessionIdentity = "osn_" + model.NewID().String()
				}},
				{name: "run and fence", mutate: func(p *Principal) {
					p.SessionRunRef = model.NewID().String()
					p.SessionFence++
					p.DisplayName = workSessionCredentialName(p.SessionRunRef, p.SessionFence)
				}},
			},
		},
		{
			name: "communication-session token", raw: communication.Token,
			permission: CommunicationSessionDeliveryRead,
			mutations: []struct {
				name   string
				mutate func(*Principal)
			}{
				{name: "agent", mutate: func(p *Principal) { p.AgentIdentity = "agent-mutated" }},
				{name: "session", mutate: func(p *Principal) {
					p.SessionIdentity = "osn_" + model.NewID().String()
				}},
				{name: "run", mutate: func(p *Principal) { p.SessionRunRef = model.NewID().String() }},
				{name: "fence", mutate: func(p *Principal) { p.SessionFence++ }},
				{name: "workspace and confinement", mutate: func(p *Principal) {
					workspace := model.NewID()
					p.SessionWorkspaceID = workspace
					p.confined[f.tenant] = workspace
				}},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			authenticated, err := f.a.Authenticate(f.ctx, test.raw)
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			ref, ok := authenticated.Ref()
			if !ok {
				t.Fatal("authenticated credential has no PrincipalRef")
			}
			resolved, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), ref, f.tenant)
			if err != nil {
				t.Fatalf("ResolvePrincipalScope: %v", err)
			}
			if !validPrincipalAuthoritySeal(resolved) {
				t.Fatal("resolved token has no valid authority seal")
			}
			req := Request{
				Principal: resolved, Tenant: f.tenant, Permission: test.permission,
				Resource: ResourceFor(test.permission),
			}
			if got := NewAuthorizer(nil).AuthorizeEvidence(context.Background(), req); got.Outcome != EvidenceAllow {
				t.Fatalf("resolved token evidence = %+v, want ALLOW", got)
			}

			for _, mutation := range test.mutations {
				t.Run(mutation.name, func(t *testing.T) {
					mutated := cloneEvidencePrincipal(resolved)
					mutation.mutate(&mutated)
					if !validPrincipalAuthorityShape(mutated) {
						t.Fatal("authority mutation unexpectedly left the credential shape")
					}
					if validPrincipalAuthoritySeal(mutated) {
						t.Fatal("valid-shape token mutation retained the old seal")
					}
					req.Principal = mutated
					requireUnknownPrincipalAuthorityEvidence(t,
						NewAuthorizer(nil).AuthorizeEvidence(context.Background(), req))
				})
			}
		})
	}
}

func TestPrincipalAuthorityEvidenceSyntheticAndMutatedPrincipalsAreUnknown(t *testing.T) {
	f, baseline := resolvedPrincipalAuthorityEvidence(t)
	az := NewAuthorizer(nil)

	synthetic := ScopedPrincipal(model.NewID(), baseline.DisplayName, f.tenant, RoleViewer)
	requireUnknownPrincipalAuthorityEvidence(t, az.AuthorizeEvidence(
		context.Background(), principalAuthorityEvidenceRequest(synthetic, f.tenant),
	))

	for _, test := range []struct {
		name   string
		mutate func(Principal) Principal
	}{
		{
			name: "display name",
			mutate: func(p Principal) Principal {
				p.DisplayName += " changed"
				return p
			},
		},
		{
			name: "AMR",
			mutate: func(p Principal) Principal {
				p.AMR = []string{"pwd", "sso"}
				return p
			},
		},
		{
			name: "grant",
			mutate: func(p Principal) Principal {
				p.grants[f.tenant] = RoleEditor
				return p
			},
		},
		{
			name: "group authority",
			mutate: func(p Principal) Principal {
				p.groups = map[model.TenantID][]string{f.tenant: {model.NewID().String()}}
				return p
			},
		},
		{
			name: "agent identity",
			mutate: func(p Principal) Principal {
				return p.WithAgentIdentity("agent:" + model.NewID().String())
			},
		},
		{
			name: "runtime binding",
			mutate: func(p Principal) Principal {
				p.SessionIdentity = "osn_" + model.NewID().String()
				return p
			},
		},
		{
			name: "act as",
			mutate: func(p Principal) Principal {
				return p.WithActAs(model.NewID())
			},
		},
		{
			name: "audiences",
			mutate: func(p Principal) Principal {
				p.audiences = []string{"evidence-test"}
				return p
			},
		},
		{
			name: "confinement",
			mutate: func(p Principal) Principal {
				p.confined = map[model.TenantID]model.ID{f.tenant: model.NewID()}
				return p
			},
		},
		{
			name: "restricted permissions",
			mutate: func(p Principal) Principal {
				return p.withRestrictedPermissions(f.tenant, Permission("agent:read"))
			},
		},
		{
			name: "credential ref",
			mutate: func(p Principal) Principal {
				p.credentialRef.version++
				return p
			},
		},
		{
			name: "provenance tenant",
			mutate: func(p Principal) Principal {
				p.evidence.tenant = model.NewTenantID()
				return p
			},
		},
		{
			name: "provenance epoch",
			mutate: func(p Principal) Principal {
				p.evidence.directoryEpoch.Version++
				return p
			},
		},
		{
			name: "provenance window",
			mutate: func(p Principal) Principal {
				p.evidence.freshUntil = p.evidence.freshUntil.Add(-time.Second)
				return p
			},
		},
		{
			name: "seal",
			mutate: func(p Principal) Principal {
				p.evidence.seal[0] ^= 0xff
				return p
			},
		},
		{
			name: "local attribution",
			mutate: func(p Principal) Principal {
				p.localVia = "test"
				return p
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneEvidencePrincipal(baseline)
			candidate = test.mutate(candidate)
			requireUnknownPrincipalAuthorityEvidence(t, az.AuthorizeEvidence(
				context.Background(), principalAuthorityEvidenceRequest(candidate, f.tenant),
			))
		})
	}
}

func deterministicPrincipalAuthoritySealFixture(t *testing.T) Principal {
	t.Helper()
	tenant := model.TenantID("11111111-1111-7111-8111-111111111111")
	userID := model.ID("22222222-2222-7222-8222-222222222222")
	credentialID := model.ID("33333333-3333-7333-8333-333333333333")
	groups := []string{
		"55555555-5555-7555-8555-555555555555",
		"44444444-4444-7444-8444-444444444444",
	}
	principal := newPrincipal(
		KindUser,
		userID,
		credentialID,
		false,
		"Alice Evidence",
		map[model.TenantID]string{tenant: RoleEditor},
		map[model.TenantID][]string{tenant: groups},
	).withConfinements(map[model.TenantID]model.ID{
		tenant: model.ID("66666666-6666-7666-8666-666666666666"),
	})
	principal.AAL = AAL3
	principal.AMR = []string{"webauthn", "pwd"}
	ref := PrincipalRef{kind: KindUser, credentialID: credentialID, version: 9}
	principal.credentialRef = ref
	principal.evidence = principalEvidenceProvenance{
		tenant: tenant,
		ref:    ref,
		directoryEpoch: store.AuthorizationFactRef{
			Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: 17,
		},
		observedAt: time.Date(2026, time.August, 16, 12, 34, 56, 123456789, time.UTC),
		freshUntil: time.Date(2026, time.August, 16, 12, 51, 56, 123456789, time.UTC),
	}
	seal, err := computePrincipalAuthoritySeal(principal)
	if err != nil {
		t.Fatalf("compute deterministic authority seal: %v", err)
	}
	principal.evidence.seal = seal
	return principal
}

func TestPrincipalAuthoritySealGoldenCanonicalOrderAndValidShapeMutations(t *testing.T) {
	baseline := deterministicPrincipalAuthoritySealFixture(t)
	const wantGolden = "4da21aa47fbbc5f860ade3eb24aec8f512691250f3a264056f4f5c038f630347"
	if got := hex.EncodeToString(baseline.evidence.seal[:]); got != wantGolden {
		t.Fatalf("authority seal golden = %s, want %s", got, wantGolden)
	}

	// AMR and group memberships are semantic sets. Their presentation order
	// cannot change the sealed authority preimage.
	reordered := cloneEvidencePrincipal(baseline)
	reordered.AMR[0], reordered.AMR[1] = reordered.AMR[1], reordered.AMR[0]
	tenant := reordered.evidence.tenant
	reordered.groups[tenant][0], reordered.groups[tenant][1] =
		reordered.groups[tenant][1], reordered.groups[tenant][0]
	if !validPrincipalAuthoritySeal(reordered) {
		t.Fatal("semantic set reordering changed the authority seal")
	}

	otherUser := model.ID("77777777-7777-7777-8777-777777777777")
	otherCredential := model.ID("88888888-8888-7888-8888-888888888888")
	otherGroup := "99999999-9999-7999-8999-999999999999"
	otherWorkspace := model.ID("aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa")
	mutations := []struct {
		name   string
		mutate func(*Principal)
	}{
		{name: "user id", mutate: func(p *Principal) { p.UserID = otherUser }},
		{name: "credential identity", mutate: func(p *Principal) {
			p.CredID = otherCredential
			p.credentialRef.credentialID = otherCredential
			p.evidence.ref.credentialID = otherCredential
		}},
		{name: "display name", mutate: func(p *Principal) { p.DisplayName = "Alice Changed" }},
		{name: "effective AAL", mutate: func(p *Principal) { p.AAL = AAL1 }},
		{name: "AMR set", mutate: func(p *Principal) { p.AMR = []string{"piv", "pwd"} }},
		{name: "role", mutate: func(p *Principal) { p.grants[tenant] = RoleAdmin }},
		{name: "group set", mutate: func(p *Principal) {
			p.groups[tenant] = []string{otherGroup, p.groups[tenant][1]}
		}},
		{name: "confinement", mutate: func(p *Principal) { p.confined[tenant] = otherWorkspace }},
		{name: "credential version", mutate: func(p *Principal) {
			p.credentialRef.version++
			p.evidence.ref.version++
		}},
		{name: "directory epoch", mutate: func(p *Principal) { p.evidence.directoryEpoch.Version++ }},
		{name: "observed at", mutate: func(p *Principal) {
			p.evidence.observedAt = p.evidence.observedAt.Add(time.Second)
		}},
		{name: "fresh until", mutate: func(p *Principal) {
			p.evidence.freshUntil = p.evidence.freshUntil.Add(-time.Second)
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneEvidencePrincipal(baseline)
			test.mutate(&candidate)
			if !validPrincipalAuthorityShape(candidate) {
				t.Fatal("mutation unexpectedly left the closed principal shape")
			}
			if validPrincipalAuthoritySeal(candidate) {
				t.Fatal("valid-shape authority mutation retained the old seal")
			}
		})
	}
}

func TestPrincipalAuthoritySealProtocolInventoryAndSemanticCanonicalization(t *testing.T) {
	wantPrincipalFields := []string{
		"Kind", "UserID", "CredID", "Superadmin", "DisplayName", "AAL", "AMR",
		"AgentIdentity", "SessionIdentity", "SessionWorkspaceID", "SessionRunRef", "SessionFence",
		"grants", "groups", "audiences", "actAs", "confined", "restricted",
		"localVia", "localSubject", "localMeta", "localSystem", "credentialRef", "evidence",
	}
	principalType := reflect.TypeOf(Principal{})
	if principalType.NumField() != len(wantPrincipalFields) {
		t.Fatalf("Principal field count = %d, want sealed v1 inventory %d; review and version the seal",
			principalType.NumField(), len(wantPrincipalFields))
	}
	for i, want := range wantPrincipalFields {
		if got := principalType.Field(i).Name; got != want {
			t.Fatalf("Principal field %d = %q, want %q; review and version the seal", i, got, want)
		}
	}
	wantEvidenceFields := []string{
		"tenant", "ref", "directoryEpoch", "observedAt", "freshUntil", "seal",
	}
	evidenceType := reflect.TypeOf(principalEvidenceProvenance{})
	if evidenceType.NumField() != len(wantEvidenceFields) {
		t.Fatalf("provenance field count = %d, want %d", evidenceType.NumField(), len(wantEvidenceFields))
	}
	for i, want := range wantEvidenceFields {
		if got := evidenceType.Field(i).Name; got != want {
			t.Fatalf("provenance field %d = %q, want %q", i, got, want)
		}
	}

	baseline := deterministicPrincipalAuthoritySealFixture(t)
	zone := time.FixedZone("semantic-offset", 5*60*60+30*60)
	sameInstants := cloneEvidencePrincipal(baseline)
	sameInstants.evidence.observedAt = baseline.evidence.observedAt.In(zone)
	sameInstants.evidence.freshUntil = baseline.evidence.freshUntil.In(zone)
	if !validPrincipalAuthoritySeal(sameInstants) {
		t.Fatal("equivalent time instants in another location changed the seal")
	}

	left := cloneEvidencePrincipal(baseline)
	left.DisplayName = "a"
	left.AAL = AAL1
	left.AMR = []string{"bc"}
	leftSeal, err := computePrincipalAuthoritySeal(left)
	if err != nil {
		t.Fatalf("seal left framing fixture: %v", err)
	}
	right := cloneEvidencePrincipal(baseline)
	right.DisplayName = "ab"
	right.AAL = AAL1
	right.AMR = []string{"c"}
	rightSeal, err := computePrincipalAuthoritySeal(right)
	if err != nil {
		t.Fatalf("seal right framing fixture: %v", err)
	}
	if leftSeal == rightSeal {
		t.Fatal("length-framed authority fields collided")
	}

	f := newPrincipalEvidenceFixture(t)
	ordinary, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), f.tokenRef(), f.tenant)
	if err != nil {
		t.Fatalf("resolve ordinary token: %v", err)
	}
	emptyEquivalent := cloneEvidencePrincipal(ordinary)
	emptyEquivalent.AMR = []string{}
	emptyEquivalent.groups = map[model.TenantID][]string{}
	emptyEquivalent.audiences = []string{}
	emptyEquivalent.confined = map[model.TenantID]model.ID{}
	emptyEquivalent.localMeta = map[string]any{}
	if !validPrincipalAuthoritySeal(emptyEquivalent) {
		t.Fatal("semantic nil/empty authority zeros changed the seal")
	}
	presentEmptyRestriction := cloneEvidencePrincipal(ordinary)
	presentEmptyRestriction.restricted = map[model.TenantID]map[Permission]struct{}{
		f.tenant: {},
	}
	if validPrincipalAuthoritySeal(presentEmptyRestriction) {
		t.Fatal("restricted=nil collapsed into a present-empty credential ceiling")
	}
}
