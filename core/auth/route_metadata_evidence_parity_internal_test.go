// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Route-metadata parity between the boolean decision and the typed evidence decision.
//
// ⛔ THE PROPERTY IS THE RBAC TERM, NOT THE OUTCOME VALUE. Authorize answers a boolean and
// AuthorizeEvidence answers a tri-state, so demanding identical results would be wrong: an
// unavailable producer is UNKNOWN on one side and a fail-closed deny on the other, and that
// difference is the design. What must agree is the term route metadata governs — the RBAC
// one. Authorize computes it as Route.rbacPermitted(req, rbacAllows(req)); authorizeEvidence
// computed it as rbacAllows(baseRequest) alone, so RequireScopedGrant and RBACMinimumRole
// were declared, sealed at registration, digested into the witness's QuestionDigest, and
// then ignored by the only evaluation a governed route runs.
//
// Every principal below is reconstructed by ResolvePrincipalScope from the real SQLite
// fixture, so its authority, its directory-epoch fact and its finite window are the ones
// the engine establishes. No test here fabricates private provenance.

// parityScopedEngine answers BOTH the legacy and the typed scoped question, consistently,
// so one authorizer can be asked both ways about ONE request. The existing producers in
// this package deliberately fail their legacy method to prove it is never called; that is
// the right assertion for those tests and the wrong instrument for this one.
type parityScopedEngine struct {
	legacy      ScopedDecision
	legacyErr   error
	typed       ScopedEvidenceDecision
	typedErr    error
	legacyCalls int
	typedCalls  int
}

func (e *parityScopedEngine) Scoped(context.Context, Request) (ScopedDecision, error) {
	e.legacyCalls++
	return e.legacy, e.legacyErr
}

func (e *parityScopedEngine) ScopedEvidence(context.Context, Request) (ScopedEvidenceDecision, error) {
	e.typedCalls++
	return e.typed, e.typedErr
}

type parityPolicyEngine struct {
	legacy      Decision
	legacyErr   error
	typed       PolicyEvidenceDecision
	typedErr    error
	legacyCalls int
	typedCalls  int
}

func (e *parityPolicyEngine) Evaluate(context.Context, Request) (Decision, error) {
	e.legacyCalls++
	return e.legacy, e.legacyErr
}

func (e *parityPolicyEngine) EvaluateEvidence(context.Context, Request) (PolicyEvidenceDecision, error) {
	e.typedCalls++
	return e.typed, e.typedErr
}

var (
	_ ScopedAuthorizer         = (*parityScopedEngine)(nil)
	_ ScopedEvidenceAuthorizer = (*parityScopedEngine)(nil)
	_ PolicyEvaluator          = (*parityPolicyEngine)(nil)
	_ PolicyEvidenceEvaluator  = (*parityPolicyEngine)(nil)
)

// routeParityPrincipal reconstructs the fixture's session principal with the tenant role
// the case needs. The role is changed in the DURABLE store before reconstruction, so the
// principal carries the authority the directory really holds rather than an edited copy.
func routeParityPrincipal(t *testing.T, role string) (*principalEvidenceFixture, Principal) {
	t.Helper()
	f := newPrincipalEvidenceFixture(t)
	ref := f.sessionRef()
	if role != RoleViewer {
		if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
			member, err := as.Memberships().Get(f.ctx, f.member.ID)
			if err != nil {
				return err
			}
			member.Role = role
			updated, err := as.Memberships().Update(f.ctx, member)
			f.member = updated
			return err
		}); err != nil {
			t.Fatalf("set fixture membership role to %q: %v", role, err)
		}
	}
	principal, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), ref, f.tenant)
	if err != nil {
		t.Fatalf("ResolvePrincipalScope: %v", err)
	}
	if got, ok := principal.RoleIn(f.tenant); !ok || got != role {
		t.Fatalf("reconstructed role = %q/%t, want %q", got, ok, role)
	}
	if !validPrincipalAuthoritySeal(principal) {
		t.Fatal("reconstructed principal has no valid authority seal")
	}
	return f, principal
}

func routeParityRequest(principal Principal, tenant model.TenantID, meta RouteMetadata) Request {
	const permission Permission = "agent:read"
	return Request{
		Principal:  principal,
		Permission: permission,
		Tenant:     tenant,
		Resource:   ResourceFor(permission),
		Route:      meta,
	}
}

// scopedGrantRoute is the shape a governed route that requires a scoped grant actually
// registers: core/api's RouteMetadata.Validate refuses RequireScopedGrant without a Cedar
// action, so a bare flag would be metadata no route could mount.
func scopedGrantRoute() RouteMetadata {
	return RouteMetadata{RequireScopedGrant: true, CedarAction: "agent:open"}
}

// TestEvidenceAndLegacyAgreeOnTheRBACTermForEveryRouteMetadata is the causal core.
//
// It configures NO producers at all: with a nil scoped engine and a nil evaluator the two
// paths differ in exactly one term — the RBAC one — so a disagreement cannot be blamed on
// a producer's tri-state. Under the defect every RequireScopedGrant cell, and every cell
// whose role floor sits above the principal's role, allows in evidence and denies in
// Authorize.
func TestEvidenceAndLegacyAgreeOnTheRBACTermForEveryRouteMetadata(t *testing.T) {
	metas := []struct {
		name string
		meta RouteMetadata
	}{
		{"zero", RouteMetadata{}},
		{"cedar action only", RouteMetadata{CedarAction: "agent:open"}},
		{"require scoped grant", scopedGrantRoute()},
		{"floor viewer", RouteMetadata{RBACMinimumRole: RoleViewer}},
		{"floor editor", RouteMetadata{RBACMinimumRole: RoleEditor}},
		{"floor admin", RouteMetadata{RBACMinimumRole: RoleAdmin}},
		{"floor owner", RouteMetadata{RBACMinimumRole: RoleOwner}},
		{"require scoped grant and floor", RouteMetadata{
			RequireScopedGrant: true, CedarAction: "agent:open", RBACMinimumRole: RoleViewer,
		}},
	}
	for _, role := range []string{RoleViewer, RoleEditor, RoleAdmin, RoleOwner} {
		t.Run(role, func(t *testing.T) {
			f, principal := routeParityPrincipal(t, role)
			az := NewAuthorizer(nil)
			for _, tc := range metas {
				t.Run(tc.name, func(t *testing.T) {
					req := routeParityRequest(principal, f.tenant, tc.meta)
					legacy := az.Authorize(context.Background(), req)
					evidence := az.AuthorizeEvidence(context.Background(), req)

					// The expectation is derived from the metadata algebra itself
					// (routemetadata.go) and not from either caller, so moving BOTH call
					// sites in the same direction still fails here. It does NOT pin
					// rbacPermitted's own rule — that is routemetadata_test.go's battery,
					// which asserts against literals, and this test rests on it.
					wantTerm := tc.meta.rbacPermitted(req, az.rbacAllows(req))
					if legacy.Allow != wantTerm {
						t.Fatalf("Authorize.Allow = %t, want the metadata algebra's %t (%+v)",
							legacy.Allow, wantTerm, legacy)
					}
					wantOutcome := EvidenceDeny
					if wantTerm {
						wantOutcome = EvidenceAllow
					}
					if evidence.Outcome != wantOutcome {
						t.Fatalf("AuthorizeEvidence outcome = %v, want %v (legacy Allow=%t): %+v",
							evidence.Outcome, wantOutcome, legacy.Allow, evidence)
					}
					// And the RBAC term is the reason, not a coincidence of some other
					// predicate: with no engines wired both guards are established clean.
					if evidence.ResourceGuard.Verdict != CheckClean ||
						evidence.ForbidAbsence.Verdict != CheckClean {
						t.Fatalf("unwired engines must establish both guards clean: %+v", evidence)
					}
					wantCore := CheckBroken
					if wantTerm {
						wantCore = CheckClean
					}
					if evidence.CorePermission.Verdict != wantCore {
						t.Fatalf("CorePermission = %+v, want verdict %v", evidence.CorePermission, wantCore)
					}
				})
			}
		})
	}
}

// TestRequireScopedGrantRemovesTheRBACTermInTypedEvidence is the named reproducer for the
// first half of the finding: breadth of role must not reach a route that declared it does
// not take breadth, on the ONE evaluation a governed route runs.
func TestRequireScopedGrantRemovesTheRBACTermInTypedEvidence(t *testing.T) {
	f, principal := routeParityPrincipal(t, RoleAdmin)
	az := NewAuthorizer(nil)
	req := routeParityRequest(principal, f.tenant, scopedGrantRoute())

	// CONTROL: the same principal, permission and resource with the route's flag cleared
	// is allowed on both paths. Without it, a blanket deny would pass the assertions below.
	open := routeParityRequest(principal, f.tenant, RouteMetadata{CedarAction: "agent:open"})
	if !az.Authorize(context.Background(), open).Allow {
		t.Fatal("control: a tenant admin must reach the same route without the flag")
	}
	if got := az.AuthorizeEvidence(context.Background(), open); got.Outcome != EvidenceAllow {
		t.Fatalf("control: evidence without the flag = %+v, want ALLOW", got)
	}
	if _, err := az.AuthorizeRoute(context.Background(), open); err != nil {
		t.Fatalf("control: AuthorizeRoute without the flag = %v, want a minted witness", err)
	}

	if az.Authorize(context.Background(), req).Allow {
		t.Fatal("Authorize: RequireScopedGrant must remove the RBAC term for a tenant admin")
	}
	got := az.AuthorizeEvidence(context.Background(), req)
	if got.Outcome != EvidenceDeny || got.CorePermission.Verdict != CheckBroken {
		t.Fatalf("AuthorizeEvidence = %+v, want DENY with a broken core permission: breadth of "+
			"role reached a route that removed the RBAC term", got)
	}
	w, err := az.AuthorizeRoute(context.Background(), req)
	if err == nil {
		t.Fatal("AuthorizeRoute minted a witness for a route whose RBAC term was removed")
	}
	if errors.Is(err, ErrRouteUndecided) {
		t.Fatalf("AuthorizeRoute = %v, want a DENIAL: nothing was unavailable here", err)
	}
	if w.Allows(time.Now()) || !reflect.DeepEqual(w, RouteAuthorizationWitness{}) {
		t.Fatalf("a denied route returned a non-zero witness: %+v", w)
	}
}

// TestRBACMinimumRoleNarrowsTheRBACTermInTypedEvidence is the second half: the floor is a
// degree where RequireScopedGrant is an absolute, and it was equally absent.
func TestRBACMinimumRoleNarrowsTheRBACTermInTypedEvidence(t *testing.T) {
	f, principal := routeParityPrincipal(t, RoleEditor)
	az := NewAuthorizer(nil)
	req := routeParityRequest(principal, f.tenant, RouteMetadata{RBACMinimumRole: RoleAdmin})

	if az.Authorize(context.Background(), req).Allow {
		t.Fatal("Authorize: an editor must not satisfy a route floor of admin")
	}
	got := az.AuthorizeEvidence(context.Background(), req)
	if got.Outcome != EvidenceDeny || got.CorePermission.Verdict != CheckBroken {
		t.Fatalf("AuthorizeEvidence = %+v, want DENY: the route's role floor was ignored", got)
	}
	if _, err := az.AuthorizeRoute(context.Background(), req); err == nil {
		t.Fatal("AuthorizeRoute minted a witness for a principal below the route's role floor")
	}
}

// TestRouteRoleFloorSatisfiedStillAllows is the positive control for the floor: it is a
// floor and not a wall, and the correction must not turn every declared floor into a deny.
func TestRouteRoleFloorSatisfiedStillAllows(t *testing.T) {
	for _, role := range []string{RoleAdmin, RoleOwner} {
		t.Run(role, func(t *testing.T) {
			f, principal := routeParityPrincipal(t, role)
			az := NewAuthorizer(nil)
			req := routeParityRequest(principal, f.tenant, RouteMetadata{RBACMinimumRole: RoleAdmin})

			if !az.Authorize(context.Background(), req).Allow {
				t.Fatalf("Authorize: %s satisfies a floor of admin", role)
			}
			got := az.AuthorizeEvidence(context.Background(), req)
			if got.Outcome != EvidenceAllow || got.CorePermission.Verdict != CheckClean {
				t.Fatalf("AuthorizeEvidence = %+v, want ALLOW for %s over a floor of admin", got, role)
			}
			w, err := az.AuthorizeRoute(context.Background(), req)
			if err != nil {
				t.Fatalf("AuthorizeRoute = %v, want a minted witness for %s", err, role)
			}
			if !w.Allows(time.Now()) {
				t.Fatalf("witness does not allow: %+v", w)
			}
			// The witness rests on the principal's own directory-epoch fact and window.
			if len(w.Decision.Facts) != 1 || w.Decision.Facts[0] != principal.evidence.directoryEpoch {
				t.Fatalf("facts = %+v, want the reconstructed directory epoch %+v",
					w.Decision.Facts, principal.evidence.directoryEpoch)
			}
		})
	}
}

// TestRequireScopedGrantStillAdmitsAPositiveScopedGrant is the invariant routemetadata.go
// states in prose: the flag REMOVES a path to allow and adds none, so a principal the
// policy authorized keeps its path. A correction that denied here would confine a delegate.
func TestRequireScopedGrantStillAdmitsAPositiveScopedGrant(t *testing.T) {
	f, principal := routeParityPrincipal(t, RoleViewer)
	grantFact := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: 7,
	}
	scoped := &parityScopedEngine{
		legacy: ScopedDecision{Effect: EffectGrant, Reason: "workspace grant"},
		typed: ScopedEvidenceDecision{
			Effect:        EffectGrant,
			ResourceGuard: CheckEvidence{Verdict: CheckClean, Code: "resource_guard_clean"},
			ForbidAbsence: CheckEvidence{Verdict: CheckClean, Code: "scoped_forbid_absent"},
			Facts:         []store.AuthorizationFactRef{grantFact},
			ObservedAt:    principal.evidence.observedAt,
			FreshUntil:    principal.evidence.freshUntil,
		},
	}
	az := NewAuthorizer(nil, WithScopedGrants(scoped))
	req := routeParityRequest(principal, f.tenant, scopedGrantRoute())

	if !az.Authorize(context.Background(), req).Allow {
		t.Fatal("Authorize: a positive scoped grant must reach a RequireScopedGrant route")
	}
	got := az.AuthorizeEvidence(context.Background(), req)
	if got.Outcome != EvidenceAllow || got.CorePermission.Verdict != CheckClean {
		t.Fatalf("AuthorizeEvidence = %+v, want ALLOW carried by the scoped grant", got)
	}
	// And carried by the GRANT, not by the role: the code is the audit trail's only record
	// of which base authorization paid for this allow.
	if got.CorePermission.Code != "scoped_grant_permitted" {
		t.Fatalf("core permission code = %q, want scoped_grant_permitted: on a route that "+
			"removed the RBAC term, an allow can only come from the grant",
			got.CorePermission.Code)
	}
	w, err := az.AuthorizeRoute(context.Background(), req)
	if err != nil {
		t.Fatalf("AuthorizeRoute = %v, want a minted witness", err)
	}
	if w.ScopedEffect != EffectGrant || w.CedarAction != CedarAction("agent:open") {
		t.Fatalf("witness = %+v, want the grant effect and the route's declared action", w)
	}
}

// TestRouteMetadataNeverWidensTypedEvidence pins the direction. A principal with no RBAC
// term to begin with must not be admitted by any metadata, before or after the correction.
func TestRouteMetadataNeverWidensTypedEvidence(t *testing.T) {
	f, principal := routeParityPrincipal(t, RoleViewer)
	az := NewAuthorizer(nil)
	// A viewer holds agent:read and not agent:write, so this is an RBAC miss with real
	// authority rather than an absent principal.
	const permission Permission = "agent:write"
	base := Request{
		Principal: principal, Permission: permission, Tenant: f.tenant,
		Resource: ResourceFor(permission),
	}
	for _, meta := range []RouteMetadata{
		{},
		scopedGrantRoute(),
		{RBACMinimumRole: RoleViewer},
		{RBACMinimumRole: RoleOwner},
		{CedarAction: "agent:open"},
		{RequireScopedGrant: true, CedarAction: "agent:open", RBACMinimumRole: RoleViewer, MinimumAAL: AAL1},
	} {
		req := base
		req.Route = meta
		if az.Authorize(context.Background(), req).Allow {
			t.Fatalf("Authorize: metadata %+v manufactured an allow from an RBAC miss", meta)
		}
		if got := az.AuthorizeEvidence(context.Background(), req); got.Outcome != EvidenceDeny {
			t.Fatalf("AuthorizeEvidence with metadata %+v = %+v, want DENY", meta, got)
		}
	}
}

// TestRouteMetadataZeroDecidesExactlyAsBefore is the back-compat invariant every route that
// has not opted in relies on: the zero metadata must be inert on the typed path too.
func TestRouteMetadataZeroDecidesExactlyAsBefore(t *testing.T) {
	f, principal := routeParityPrincipal(t, RoleViewer)
	az := NewAuthorizer(nil)
	req := routeParityRequest(principal, f.tenant, RouteMetadata{})
	if !req.Route.IsZero() {
		t.Fatal("fixture metadata is not the zero value")
	}
	got := az.AuthorizeEvidence(context.Background(), req)
	if got.Outcome != EvidenceAllow || got.CorePermission.Verdict != CheckClean ||
		got.CorePermission.Code != "rbac_permitted" {
		t.Fatalf("zero metadata = %+v, want the historical rbac_permitted ALLOW", got)
	}
	if !got.ObservedAt.Equal(principal.evidence.observedAt) ||
		!got.FreshUntil.Equal(principal.evidence.freshUntil) {
		t.Fatalf("window = [%s,%s], want the principal's reconstructed window [%s,%s]",
			got.ObservedAt, got.FreshUntil, principal.evidence.observedAt, principal.evidence.freshUntil)
	}
}

// TestRequireScopedGrantWithUnavailableScopedEngineStaysUnknown is the tri-state guarantee.
// Once the RBAC term is removed, the positive-grant question is the only one left, and an
// engine that could not answer it makes the decision UNAVAILABLE — not a denial. Answering
// DENY here would tell an operator their policy refused something nothing evaluated.
func TestRequireScopedGrantWithUnavailableScopedEngineStaysUnknown(t *testing.T) {
	f, principal := routeParityPrincipal(t, RoleAdmin)
	scoped := &parityScopedEngine{
		legacy:    ScopedDecision{Effect: EffectAbstain},
		typed:     ScopedEvidenceDecision{},
		typedErr:  errors.New("scope runtime unavailable"),
		legacyErr: nil,
	}
	az := NewAuthorizer(nil, WithScopedGrants(scoped))
	req := routeParityRequest(principal, f.tenant, scopedGrantRoute())

	got := az.AuthorizeEvidence(context.Background(), req)
	if got.Outcome != EvidenceUnknown || got.CorePermission.Verdict != CheckUnknown ||
		got.CorePermission.Code != "scoped_grant_unavailable" {
		t.Fatalf("AuthorizeEvidence = %+v, want UNKNOWN/scoped_grant_unavailable", got)
	}
	if _, err := az.AuthorizeRoute(context.Background(), req); !errors.Is(err, ErrRouteUndecided) {
		t.Fatalf("AuthorizeRoute = %v, want ErrRouteUndecided", err)
	}
	if scoped.legacyCalls != 0 {
		t.Fatalf("the legacy scoped method ran %d times on the typed path", scoped.legacyCalls)
	}
}

// TestScopedForbidStillDominatesUnderRouteMetadata: a forbid overrides everything, and the
// correction touches only the RBAC term. The floor here is SATISFIED, so the deny can only
// come from the forbid.
func TestScopedForbidStillDominatesUnderRouteMetadata(t *testing.T) {
	f, principal := routeParityPrincipal(t, RoleOwner)
	confinementFact := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: 11,
	}
	scoped := &parityScopedEngine{
		legacy: ScopedDecision{
			Effect: EffectForbid, Reason: "workspace confinement", Class: ClassInvariant,
		},
		typed: principalAuthorityBrokenScoped(
			principal.evidence.observedAt, principal.evidence.freshUntil, confinementFact,
		),
	}
	az := NewAuthorizer(nil, WithScopedGrants(scoped))
	req := routeParityRequest(principal, f.tenant, RouteMetadata{RBACMinimumRole: RoleAdmin})

	if dec := az.Authorize(context.Background(), req); dec.Allow || dec.Class != ClassInvariant {
		t.Fatalf("Authorize = %+v, want a non-shadowable forbid", dec)
	}
	got := az.AuthorizeEvidence(context.Background(), req)
	if got.Outcome != EvidenceDeny || got.ResourceGuard.Verdict != CheckBroken {
		t.Fatalf("AuthorizeEvidence = %+v, want DENY proved by the broken resource guard", got)
	}
	if len(got.Facts) != 1 || got.Facts[0] != confinementFact {
		t.Fatalf("deny proof facts = %+v, want the scoped engine's own fact", got.Facts)
	}
}

// TestBrokenPolicyProducerStaysUnknownUnderASatisfiedFloor keeps the deny-overlay's own
// third answer intact under metadata the correction now reads.
func TestBrokenPolicyProducerStaysUnknownUnderASatisfiedFloor(t *testing.T) {
	f, principal := routeParityPrincipal(t, RoleAdmin)
	policy := &parityPolicyEngine{
		legacy:   Decision{Allow: true},
		typedErr: errors.New("PDP unavailable"),
	}
	az := NewAuthorizer(policy)
	req := routeParityRequest(principal, f.tenant, RouteMetadata{RBACMinimumRole: RoleAdmin})

	got := az.AuthorizeEvidence(context.Background(), req)
	if got.Outcome != EvidenceUnknown || got.CorePermission.Verdict != CheckClean ||
		got.ForbidAbsence.Verdict != CheckUnknown {
		t.Fatalf("AuthorizeEvidence = %+v, want a clean core permission and UNKNOWN forbid absence", got)
	}
	if policy.legacyCalls != 0 {
		t.Fatalf("the legacy policy method ran %d times on the typed path", policy.legacyCalls)
	}
}

// TestCredentialCeilingIsDecidedBeforeRouteMetadata pins the boundary of the correction. A
// purpose-restricted credential's ceiling IS its base authorization: Authorize applies the
// metadata only under `if !restricted`, and the typed path must keep that shape. Widening
// the correction into this branch would confine a runtime credential by a route flag that
// was never about it.
func TestCredentialCeilingIsDecidedBeforeRouteMetadata(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	system, err := NewSystemOperator("test:route-metadata-parity", "communication ceiling control")
	if err != nil {
		t.Fatalf("system operator: %v", err)
	}
	issued, err := NewAuthenticator(f.st, model.SystemClock{}).IssueCommunicationSessionCredential(
		f.ctx,
		system,
		CommunicationSessionCredentialSpec{
			Tenant: f.tenant, WorkspaceID: model.NewID(),
			SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(),
			ClaimFence: 3,
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
	principal, err := f.a.ResolvePrincipalScope(f.deadline(20*time.Minute), ref, f.tenant)
	if err != nil {
		t.Fatalf("ResolvePrincipalScope: %v", err)
	}
	if _, restricted := principal.PurposePermissionsIn(f.tenant); !restricted {
		t.Fatal("the reconstructed communication principal carries no purpose ceiling")
	}
	az := NewAuthorizer(nil)
	req := Request{
		Principal:  principal,
		Permission: CommunicationSessionDeliveryRead,
		Tenant:     f.tenant,
		Resource:   ResourceFor(CommunicationSessionDeliveryRead),
		Route:      scopedGrantRoute(),
	}
	if !az.Authorize(context.Background(), req).Allow {
		t.Fatal("Authorize: the ceiling is the base authorization and metadata does not narrow it")
	}
	got := az.AuthorizeEvidence(context.Background(), req)
	if got.Outcome != EvidenceAllow || got.CorePermission.Code != "credential_ceiling_permitted" {
		t.Fatalf("AuthorizeEvidence = %+v, want the ceiling ALLOW", got)
	}

	// The other half of the ceiling, unchanged: a permission outside it denies regardless
	// of metadata, and it denies for the ceiling's reason.
	outside := req
	outside.Permission = "agent:read"
	outside.Resource = ResourceFor("agent:read")
	outside.Route = RouteMetadata{}
	if az.Authorize(context.Background(), outside).Allow {
		t.Fatal("Authorize: a permission outside the ceiling must be refused")
	}
	if denied := az.AuthorizeEvidence(context.Background(), outside); denied.Outcome != EvidenceDeny ||
		denied.CorePermission.Code != "credential_ceiling_denied" {
		t.Fatalf("AuthorizeEvidence outside the ceiling = %+v, want the ceiling DENY", denied)
	}
}

// TestDecideRouteReadHonoursRouteMetadata covers the other consumer of the same evaluation.
// DecideRouteRead is the page/row read path, and it shares authorizeEvidence, so the term
// it inherited was the same one.
func TestDecideRouteReadHonoursRouteMetadata(t *testing.T) {
	f, principal := routeParityPrincipal(t, RoleAdmin)
	az := NewAuthorizer(nil)

	// CONTROL: the same read without the flag is decided and allowed.
	open := routeParityRequest(principal, f.tenant, RouteMetadata{CedarAction: "agent:open"})
	allowed, err := az.DecideRouteRead(context.Background(), open)
	if err != nil {
		t.Fatalf("control: DecideRouteRead = %v, want a decided read", err)
	}
	if !allowed.Allowed() {
		t.Fatal("control: a tenant admin must be allowed to read without the flag")
	}

	req := routeParityRequest(principal, f.tenant, scopedGrantRoute())
	decision, err := az.DecideRouteRead(context.Background(), req)
	if err != nil {
		t.Fatalf("DecideRouteRead = %v, want a definite (denied) read decision", err)
	}
	if decision.Allowed() {
		t.Fatal("DecideRouteRead admitted a row on a route whose RBAC term was removed")
	}
	if w := decision.AllowWitness(); w.Allows(time.Now()) {
		t.Fatalf("a denied read decision produced an allowing witness: %+v", w)
	}
}
