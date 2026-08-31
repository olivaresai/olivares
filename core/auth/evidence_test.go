// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const evidenceTenant model.TenantID = "11111111-1111-7111-8111-111111111111"

var (
	evidenceObserved = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	evidenceFresh    = evidenceObserved.Add(time.Minute)
)

type legacyEvidenceScoped struct {
	calls int
}

func (engine *legacyEvidenceScoped) Scoped(context.Context, auth.Request) (auth.ScopedDecision, error) {
	engine.calls++
	return auth.ScopedDecision{Effect: auth.EffectGrant, Reason: "looks permitted"}, nil
}

type legacyEvidencePolicy struct {
	calls int
}

func (engine *legacyEvidencePolicy) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	engine.calls++
	return auth.Decision{Allow: true, Reason: "policy: evaluation error but text is not state"}, nil
}

type typedEvidenceScoped struct {
	decision auth.ScopedEvidenceDecision
	err      error
	panics   bool
	legacy   int
}

func (engine *typedEvidenceScoped) Scoped(context.Context, auth.Request) (auth.ScopedDecision, error) {
	engine.legacy++
	return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "legacy method must not run"}, nil
}

func (engine *typedEvidenceScoped) ScopedEvidence(
	context.Context,
	auth.Request,
) (auth.ScopedEvidenceDecision, error) {
	if engine.panics {
		panic("scoped evidence panic")
	}
	return engine.decision, engine.err
}

type typedEvidencePolicy struct {
	decision auth.PolicyEvidenceDecision
	err      error
	panics   bool
	legacy   int
}

func (engine *typedEvidencePolicy) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	engine.legacy++
	return auth.Decision{Allow: false, Reason: "legacy method must not run"}, nil
}

func (engine *typedEvidencePolicy) EvaluateEvidence(
	context.Context,
	auth.Request,
) (auth.PolicyEvidenceDecision, error) {
	if engine.panics {
		panic("policy evidence panic")
	}
	return engine.decision, engine.err
}

var (
	_ auth.ScopedAuthorizer         = (*legacyEvidenceScoped)(nil)
	_ auth.PolicyEvaluator          = (*legacyEvidencePolicy)(nil)
	_ auth.ScopedAuthorizer         = (*typedEvidenceScoped)(nil)
	_ auth.ScopedEvidenceAuthorizer = (*typedEvidenceScoped)(nil)
	_ auth.PolicyEvaluator          = (*typedEvidencePolicy)(nil)
	_ auth.PolicyEvidenceEvaluator  = (*typedEvidencePolicy)(nil)
)

func evidenceRequest(role string, permission auth.Permission) auth.Request {
	return auth.Request{
		Principal:  auth.ScopedPrincipal("subject", "subject", evidenceTenant, role),
		Permission: permission,
		Tenant:     evidenceTenant,
		Resource: auth.ResourceAttrs{
			Kind: "sessions.message", ID: model.NewID().String(), WorkspaceID: model.NewID(),
		},
	}
}

func cleanScopedEvidence(facts ...store.AuthorizationFactRef) auth.ScopedEvidenceDecision {
	return auth.ScopedEvidenceDecision{
		Effect:        auth.EffectAbstain,
		ResourceGuard: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "resource_guard_clean"},
		ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "scoped_forbid_absent"},
		Facts:         facts,
		ObservedAt:    evidenceObserved,
		FreshUntil:    evidenceFresh,
	}
}

func cleanPolicyEvidence(facts ...store.AuthorizationFactRef) auth.PolicyEvidenceDecision {
	return auth.PolicyEvidenceDecision{
		ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "policy_forbid_absent"},
		Facts:         facts,
		ObservedAt:    evidenceObserved.Add(5 * time.Second),
		FreshUntil:    evidenceFresh.Add(-5 * time.Second),
	}
}

func TestAuthorizeEvidenceLegacyEnginesAreUnknownAndNeverCalled(t *testing.T) {
	scoped := &legacyEvidenceScoped{}
	policy := &legacyEvidencePolicy{}
	authorizer := auth.NewAuthorizer(policy, auth.WithScopedGrants(scoped))

	got := authorizer.AuthorizeEvidence(
		context.Background(), evidenceRequest(auth.RoleOwner, "sessions:message:read"),
	)
	if got.Outcome != auth.EvidenceUnknown {
		t.Fatalf("legacy engines outcome = %v, want UNKNOWN: %+v", got.Outcome, got)
	}
	if got.ResourceGuard.Verdict != auth.CheckUnknown ||
		got.ForbidAbsence.Verdict != auth.CheckUnknown {
		t.Fatalf("legacy engines fabricated evidence: %+v", got)
	}
	if scoped.calls != 0 || policy.calls != 0 {
		t.Fatalf("legacy methods called: scoped=%d policy=%d", scoped.calls, policy.calls)
	}
}

func TestAuthorizeEvidenceErrorsAndPanicsRemainUnknown(t *testing.T) {
	tests := []struct {
		name         string
		scoped       *typedEvidenceScoped
		policy       *typedEvidencePolicy
		wantGuard    auth.CheckVerdict
		wantForbid   auth.CheckVerdict
		wantFailCode string
	}{
		{
			name:         "scoped error",
			scoped:       &typedEvidenceScoped{err: errors.New("scope unavailable")},
			wantGuard:    auth.CheckUnknown,
			wantForbid:   auth.CheckUnknown,
			wantFailCode: "scoped_evidence_unavailable",
		},
		{
			name:         "scoped panic",
			scoped:       &typedEvidenceScoped{panics: true},
			wantGuard:    auth.CheckUnknown,
			wantForbid:   auth.CheckUnknown,
			wantFailCode: "scoped_evidence_unavailable",
		},
		{
			name:         "policy error",
			policy:       &typedEvidencePolicy{err: errors.New("PDP unavailable")},
			wantGuard:    auth.CheckClean,
			wantForbid:   auth.CheckUnknown,
			wantFailCode: "policy_evidence_unavailable",
		},
		{
			name:         "policy panic",
			policy:       &typedEvidencePolicy{panics: true},
			wantGuard:    auth.CheckClean,
			wantForbid:   auth.CheckUnknown,
			wantFailCode: "policy_evidence_unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var options []auth.Option
			if test.scoped != nil {
				options = append(options, auth.WithScopedGrants(test.scoped))
			}
			// Assign through an interface only when this case really configures a
			// policy. Passing test.policy directly would turn a nil pointer into a
			// non-nil PolicyEvaluator and add a second UNKNOWN that masks the scoped
			// error/panic this row is meant to discriminate.
			var policy auth.PolicyEvaluator
			if test.policy != nil {
				policy = test.policy
			}
			authorizer := auth.NewAuthorizer(policy, options...)
			got := authorizer.AuthorizeEvidence(
				context.Background(), evidenceRequest(auth.RoleOwner, "sessions:message:read"),
			)
			if got.Outcome != auth.EvidenceUnknown {
				t.Fatalf("outcome = %v, want UNKNOWN: %+v", got.Outcome, got)
			}
			if got.CorePermission.Verdict != auth.CheckUnknown ||
				got.CorePermission.Code != "principal_authority_unverified" {
				t.Fatalf("synthetic core permission = %+v, want provenance UNKNOWN", got.CorePermission)
			}
			if got.ResourceGuard.Verdict != test.wantGuard ||
				got.ForbidAbsence.Verdict != test.wantForbid {
				t.Fatalf("causal components = guard %+v forbid %+v, want %v/%v",
					got.ResourceGuard, got.ForbidAbsence, test.wantGuard, test.wantForbid)
			}
			if test.scoped != nil {
				if got.ResourceGuard.Code != test.wantFailCode ||
					got.ForbidAbsence.Code != test.wantFailCode {
					t.Fatalf("scoped failure codes = %q/%q, want %q",
						got.ResourceGuard.Code, got.ForbidAbsence.Code, test.wantFailCode)
				}
			} else if got.ForbidAbsence.Code != test.wantFailCode {
				t.Fatalf("policy failure code = %q, want %q",
					got.ForbidAbsence.Code, test.wantFailCode)
			}
			if test.scoped != nil && test.scoped.legacy != 0 {
				t.Fatalf("legacy scoped method called %d times", test.scoped.legacy)
			}
			if test.policy != nil && test.policy.legacy != 0 {
				t.Fatalf("legacy policy method called %d times", test.policy.legacy)
			}
		})
	}
}

func TestAuthorizeEvidenceTypedNilEnginesRemainUnknownIndependently(t *testing.T) {
	t.Run("typed nil scoped", func(t *testing.T) {
		var scoped *typedEvidenceScoped
		got := auth.NewAuthorizer(nil, auth.WithScopedGrants(scoped)).AuthorizeEvidence(
			context.Background(), evidenceRequest(auth.RoleOwner, "sessions:message:read"),
		)
		if got.Outcome != auth.EvidenceUnknown ||
			got.CorePermission.Verdict != auth.CheckUnknown ||
			got.ResourceGuard.Verdict != auth.CheckUnknown ||
			got.ForbidAbsence.Verdict != auth.CheckUnknown ||
			got.ResourceGuard.Code != "scoped_evidence_unavailable" ||
			got.ForbidAbsence.Code != "scoped_evidence_unavailable" {
			t.Fatalf("typed-nil scoped result = %+v, want isolated scoped UNKNOWN", got)
		}
	})

	t.Run("typed nil policy", func(t *testing.T) {
		var policy *typedEvidencePolicy
		got := auth.NewAuthorizer(policy).AuthorizeEvidence(
			context.Background(), evidenceRequest(auth.RoleOwner, "sessions:message:read"),
		)
		if got.Outcome != auth.EvidenceUnknown ||
			got.CorePermission.Verdict != auth.CheckUnknown ||
			got.ResourceGuard.Verdict != auth.CheckClean ||
			got.ForbidAbsence.Verdict != auth.CheckUnknown ||
			got.ForbidAbsence.Code != "policy_evidence_unavailable" {
			t.Fatalf("typed-nil policy result = %+v, want isolated policy UNKNOWN", got)
		}
	})
}

func TestAuthorizeEvidenceForeignEpochCoordinatesFailUnknown(t *testing.T) {
	for _, kind := range []model.Kind{model.DirectoryEpochKind, model.AuthorizationEpochKind} {
		t.Run(string(kind), func(t *testing.T) {
			fact := store.AuthorizationFactRef{Kind: kind, ID: model.NewID(), Version: 7}
			scoped := &typedEvidenceScoped{decision: cleanScopedEvidence(fact)}
			policy := &typedEvidencePolicy{decision: cleanPolicyEvidence(fact)}
			got := auth.NewAuthorizer(policy, auth.WithScopedGrants(scoped)).AuthorizeEvidence(
				context.Background(), evidenceRequest(auth.RoleOwner, "sessions:message:read"),
			)
			if got.Outcome != auth.EvidenceUnknown || len(got.Facts) != 0 {
				t.Fatalf("foreign epoch coordinate = %+v, want UNKNOWN with no usable facts", got)
			}
			if got.CorePermission.Verdict != auth.CheckUnknown ||
				got.ResourceGuard.Verdict != auth.CheckUnknown ||
				got.ForbidAbsence.Verdict != auth.CheckUnknown {
				t.Fatalf("foreign fact left a trusted component: %+v", got)
			}
		})
	}
}

func TestAuthorizeEvidenceUnknownScopedAbstentionDoesNotFabricateDenial(t *testing.T) {
	unknown := auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: "scoped_unavailable"}
	cleanGuard := auth.CheckEvidence{Verdict: auth.CheckClean, Code: "resource_guard_clean"}
	cleanForbid := auth.CheckEvidence{Verdict: auth.CheckClean, Code: "scoped_forbid_absent"}
	for _, test := range []struct {
		name     string
		decision auth.ScopedEvidenceDecision
	}{
		{
			name: "both predicates unknown",
			decision: auth.ScopedEvidenceDecision{
				Effect: auth.EffectAbstain, ResourceGuard: unknown, ForbidAbsence: unknown,
			},
		},
		{
			name: "resource guard unknown",
			decision: auth.ScopedEvidenceDecision{
				Effect: auth.EffectAbstain, ResourceGuard: unknown, ForbidAbsence: cleanForbid,
				ObservedAt: evidenceObserved, FreshUntil: evidenceFresh,
			},
		},
		{
			name: "forbid absence unknown",
			decision: auth.ScopedEvidenceDecision{
				Effect: auth.EffectAbstain, ResourceGuard: cleanGuard, ForbidAbsence: unknown,
				ObservedAt: evidenceObserved, FreshUntil: evidenceFresh,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			scoped := &typedEvidenceScoped{decision: test.decision}
			got := auth.NewAuthorizer(nil, auth.WithScopedGrants(scoped)).AuthorizeEvidence(
				context.Background(), evidenceRequest(auth.RoleViewer, "sessions:message:write"),
			)
			if got.CorePermission.Verdict != auth.CheckUnknown || got.Outcome != auth.EvidenceUnknown ||
				len(got.Facts) != 0 || !got.ObservedAt.IsZero() || !got.FreshUntil.IsZero() {
				t.Fatalf("unobserved scoped abstention fabricated reusable denial: %+v", got)
			}
		})
	}
}

func TestAuthorizeEvidenceObservedScopedAbstentionSupportsCoreDenial(t *testing.T) {
	scoped := &typedEvidenceScoped{decision: cleanScopedEvidence()}
	got := auth.NewAuthorizer(nil, auth.WithScopedGrants(scoped)).AuthorizeEvidence(
		context.Background(), evidenceRequest(auth.RoleViewer, "sessions:message:write"),
	)
	if got.CorePermission.Verdict != auth.CheckBroken || got.Outcome != auth.EvidenceDeny {
		t.Fatalf("observed no-grant did not preserve core denial: %+v", got)
	}
}

func TestAuthorizeEvidenceSyntheticPositiveRequiresProvenance(t *testing.T) {
	fact := store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.ID(evidenceTenant), Version: 12,
	}
	scoped := &typedEvidenceScoped{decision: cleanScopedEvidence(fact)}
	policy := &typedEvidencePolicy{decision: cleanPolicyEvidence(fact)}

	got := auth.NewAuthorizer(policy, auth.WithScopedGrants(scoped)).AuthorizeEvidence(
		context.Background(), evidenceRequest(auth.RoleOwner, "sessions:message:read"),
	)
	if got.Outcome != auth.EvidenceUnknown ||
		got.CorePermission.Verdict != auth.CheckUnknown ||
		got.ResourceGuard.Verdict != auth.CheckClean ||
		got.ForbidAbsence.Verdict != auth.CheckClean {
		t.Fatalf("synthetic positive did not degrade only core provenance: %+v", got)
	}
	if len(got.Facts) != 0 || !got.ObservedAt.IsZero() || !got.FreshUntil.IsZero() {
		t.Fatalf("synthetic UNKNOWN exposed reusable facts/window: %+v", got)
	}
	if scoped.legacy != 0 || policy.legacy != 0 {
		t.Fatalf("typed path invoked legacy methods: scoped=%d policy=%d", scoped.legacy, policy.legacy)
	}
}

func TestAuthorizeEvidenceExplicitForbidDeniesWithoutReasonParsing(t *testing.T) {
	policy := &typedEvidencePolicy{decision: auth.PolicyEvidenceDecision{
		ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckBroken, Code: "authored_forbid"},
		ObservedAt:    evidenceObserved,
		FreshUntil:    evidenceFresh,
	}}
	got := auth.NewAuthorizer(policy).AuthorizeEvidence(
		context.Background(), evidenceRequest(auth.RoleOwner, "sessions:message:read"),
	)
	if got.Outcome != auth.EvidenceDeny || got.ForbidAbsence.Verdict != auth.CheckBroken {
		t.Fatalf("typed forbid = %+v, want DENY", got)
	}
}
