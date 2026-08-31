// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// the open-core single-IdP cap and the reserved enterprise multi-IdP
// capability, tested at the FederationService layer (the DEFAULT build gate). The
// real per-tenant SELECTION logic of the enterprise resolver is tested separately
// under -tags enterprise (enterprise/federation/multitenant_test.go); here a test
// double stands in for it to prove the service HONORS the capability (cap lift +
// per-tenant resolve) and ENFORCES the cap when it is absent.

// --- test doubles ------------------------------------------------------------

type fedTestSealer struct{}

func (fedTestSealer) Seal(_ context.Context, scope model.TenantID, pt []byte) (string, error) {
	return "sealed:" + scope.String() + ":" + base64.StdEncoding.EncodeToString(pt), nil
}

func (fedTestSealer) Open(_ context.Context, scope model.TenantID, sealed string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, "sealed:"+scope.String()+":")
	if !ok {
		return nil, errors.New("fedTestSealer: wrong scope")
	}
	return base64.StdEncoding.DecodeString(rest)
}

// fedTestFed is a stub provider that remembers the issuer it was built from, so a
// test can tell WHICH config produced the resolved provider.
type fedTestFed struct{ proto, issuer string }

func (f *fedTestFed) Protocol() string { return f.proto }

func (f *fedTestFed) BeginAuth(context.Context, auth.AuthParams) (string, error) {
	return "https://idp.example/auth", nil
}

func (f *fedTestFed) ValidateAssertion(context.Context, auth.Assertion) (auth.FederatedIdentity, error) {
	return auth.FederatedIdentity{Subject: "s", Email: "u@" + f.issuer}, nil
}

// fedTestBuilder builds a stub provider carrying the issuer (so per-tenant
// resolution can be asserted) and proves the opened client secret flows through.
func fedTestBuilder(_ context.Context, p auth.FederationParams) (auth.Federation, error) {
	if p.Protocol == auth.ProtocolOIDC && p.OIDCClientSecret == "" {
		return nil, errors.New("fedTestBuilder: oidc build without client secret")
	}
	return &fedTestFed{proto: p.Protocol, issuer: p.OIDCIssuer}, nil
}

// fedTestMultiIDP stands in for the reserved enterprise capability: it selects a
// tenant's own active config by TargetTenantID (mirrors enterprise/federation).
type fedTestMultiIDP struct{ refuse error }

// AllowsAdditionalActiveIdP is the entitlement question PutConfig asks at call time. The
// zero value allows, so every existing case keeps its old meaning; a case that wants the
// enterprise-side refusal sets refuse.
func (m fedTestMultiIDP) AllowsAdditionalActiveIdP(context.Context) error { return m.refuse }

func (fedTestMultiIDP) SelectActive(in auth.SelectionInput, active []model.FederationConfig) (model.FederationConfig, bool) {
	if in.Tenant == "" || in.Tenant == auth.GlobalFederationScope {
		// Home-realm: match by email domain across scopes (U5).
		if in.EmailDomain != "" {
			for _, c := range active {
				for _, d := range c.ClaimedDomains {
					if d == in.EmailDomain {
						return c, true
					}
				}
			}
		}
		return model.FederationConfig{}, false
	}
	for _, c := range active {
		if c.TargetTenantID == in.Tenant {
			return c, true
		}
	}
	return model.FederationConfig{}, false
}

func fedTestActor() auth.Principal {
	return auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), Superadmin: true, DisplayName: "test-admin"}
}

// oidcInput is a complete OIDC config input (a brand-new config needs a secret).
func oidcInput(issuer string, enabled bool) auth.FederationConfigInput {
	return auth.FederationConfigInput{
		Protocol: auth.ProtocolOIDC, Enabled: enabled,
		OIDCIssuer: issuer, OIDCClientID: "client-" + issuer, OIDCClientSecret: "secret-" + issuer,
	}
}

func mustPut(t *testing.T, svc *auth.FederationService, scope model.TenantID, in auth.FederationConfigInput) {
	t.Helper()
	if _, err := svc.PutConfig(context.Background(), fedTestActor(), scope, in); err != nil {
		t.Fatalf("put %s: %v", scope, err)
	}
}

// --- the entitlement question, asked AT CALL TIME ----------------------------

// A wired capability is no longer the whole answer: PutConfig asks it, every time.
//
// WHY THIS EXISTS. The cap used to be a nil-check, so wiring a MultiIDP lifted it outright.
// The enterprise composition root wires one UNCONDITIONALLY (wire_enterprise.go:159-160), so
// a customer who bought a different pack received per-tenant multi-IdP for free: one binary
// carries the capability and entitlement is per pack, so "compiled in" and "paid for" stopped
// being the same thing. Requested by another lane, who owns the enterprise side of the seam.
//
// The refusal is returned UNWRAPPED so it carries its own name. Folding it into
// ErrMultiIDPRequiresEnterprise would tell an enterprise customer to buy the build they are
// already running.
var errNotInThisPack = errors.New("federation: multi_idp_not_in_pack: this deployment's packs do not include per-tenant multi-IdP")

func TestFederationCap_WiredCapabilityIsAskedAtCallTime(t *testing.T) {
	svc := auth.NewFederationService(testStore(t), fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, fedTestMultiIDP{refuse: errNotInThisPack})
	ctx, actor := context.Background(), fedTestActor()

	// The FIRST active IdP never reaches the question: the loop only runs once another
	// active config exists, so an entitlement refusal cannot break single-IdP SSO, which is
	// open-core and paid for by nobody.
	if _, err := svc.PutConfig(ctx, actor, auth.GlobalFederationScope, oidcInput("https://idp.example", true)); err != nil {
		t.Fatalf("the first active IdP must not consult entitlement at all: %v", err)
	}

	// The SECOND one does, and the capability's own refusal comes back verbatim.
	_, err := svc.PutConfig(ctx, actor, model.NewTenantID(), oidcInput("https://other-idp.example", true))
	if !errors.Is(err, errNotInThisPack) {
		t.Fatalf("second active IdP err = %v, want the capability's own refusal", err)
	}
	if errors.Is(err, auth.ErrMultiIDPRequiresEnterprise) {
		t.Fatal("the refusal was folded into the open-core cap error: an entitled build would be told to buy the build it is running")
	}
	// IDENTITY, not errors.Is, and the difference is the whole assertion. errors.Is walks
	// wrappers by design, so `fmt.Errorf("...: %w", err)` satisfies every check above while
	// changing exactly what this PR promises: that the capability's refusal arrives as ITSELF.
	// Measured by the adversarial contrast (P1-1) and reproduced here before fixing: that
	// mutant compiled and SURVIVED the battery. A test that asserts "unwrapped" with errors.Is
	// is a test that cannot see wrapping.
	if err != errNotInThisPack { //nolint:errorlint // identity is the property under test
		t.Fatalf("the refusal arrived wrapped (%T: %v); PutConfig must return the capability's error as-is", err, err)
	}

	// Staging it INACTIVE still costs nothing — the question guards ACTIVATION, not authoring.
	if _, err := svc.PutConfig(ctx, actor, model.NewTenantID(), oidcInput("https://third.example", false)); err != nil {
		t.Fatalf("staging a disabled config must not consult entitlement: %v", err)
	}
}

func TestFederationCap_EntitledCapabilityStillLiftsTheCap(t *testing.T) {
	// The not-refusing direction, because a gate tested in one direction only is half a gate:
	// with the question answering nil the behavior is exactly what it was before this change.
	svc := auth.NewFederationService(testStore(t), fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, fedTestMultiIDP{})
	ctx, actor := context.Background(), fedTestActor()
	if _, err := svc.PutConfig(ctx, actor, auth.GlobalFederationScope, oidcInput("https://idp.example", true)); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := svc.PutConfig(ctx, actor, model.NewTenantID(), oidcInput("https://other-idp.example", true)); err != nil {
		t.Fatalf("an entitled capability must still lift the single-IdP cap: %v", err)
	}
}

// --- the single-IdP cap (open build) -----------------------------------------

func TestFederationCap_SingleIDP_RejectsSecondActiveIdP(t *testing.T) {
	svc := auth.NewFederationService(testStore(t), fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil) // nil = open build
	ctx, actor := context.Background(), fedTestActor()
	tenantX := model.NewTenantID()

	// The first ACTIVE IdP (global) is allowed.
	if _, err := svc.PutConfig(ctx, actor, auth.GlobalFederationScope, oidcInput("https://idp.example", true)); err != nil {
		t.Fatalf("first active config: %v", err)
	}
	// A SECOND active IdP (a per-tenant config) is the reserved enterprise line.
	_, err := svc.PutConfig(ctx, actor, tenantX, oidcInput("https://other-idp.example", true))
	if !errors.Is(err, auth.ErrMultiIDPRequiresEnterprise) {
		t.Fatalf("second active IdP err = %v, want ErrMultiIDPRequiresEnterprise", err)
	}
	// Staging the second IdP DISABLED is allowed (one ACTIVE per deployment, may
	// stage inactive configs and switch — the chosen cap semantics).
	if _, err := svc.PutConfig(ctx, actor, tenantX, oidcInput("https://other-idp.example", false)); err != nil {
		t.Fatalf("staging a disabled second config must be allowed: %v", err)
	}
	// Re-saving the already-active global scope is allowed (same scope, not a 2nd).
	if _, err := svc.PutConfig(ctx, actor, auth.GlobalFederationScope, oidcInput("https://idp.example", true)); err != nil {
		t.Fatalf("re-saving the active scope must be allowed: %v", err)
	}
}

func TestFederationCap_LiftedByMultiIDP(t *testing.T) {
	svc := auth.NewFederationService(testStore(t), fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, fedTestMultiIDP{}) // enterprise
	ctx, actor := context.Background(), fedTestActor()

	if _, err := svc.PutConfig(ctx, actor, auth.GlobalFederationScope, oidcInput("https://idp.example", true)); err != nil {
		t.Fatalf("global active: %v", err)
	}
	// With the multi-IdP capability wired the cap is lifted: a 2nd active IdP is OK.
	if _, err := svc.PutConfig(ctx, actor, model.NewTenantID(), oidcInput("https://idp-x.example", true)); err != nil {
		t.Fatalf("second active IdP under enterprise must be allowed: %v", err)
	}
}

// --- multi-IdP resolution (enterprise) vs single-IdP (open) ------------------

func TestFederationResolve_MultiIDP_PerTenant(t *testing.T) {
	svc := auth.NewFederationService(testStore(t), fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, fedTestMultiIDP{})
	ctx := context.Background()
	tenantA, tenantB := model.NewTenantID(), model.NewTenantID()

	mustPut(t, svc, auth.GlobalFederationScope, oidcInput("global", true))
	mustPut(t, svc, tenantA, oidcInput("idp-a", true))
	mustPut(t, svc, tenantB, oidcInput("idp-b", true))

	wantIssuer := func(tenant model.TenantID, want string) {
		t.Helper()
		fed, err := svc.Resolve(ctx, tenant)
		if err != nil {
			t.Fatalf("resolve %s: %v", tenant, err)
		}
		f, ok := fed.(*fedTestFed)
		if !ok {
			t.Fatalf("resolve %s = %T, want *fedTestFed", tenant, fed)
		}
		if f.issuer != want {
			t.Fatalf("resolve %s issuer = %q, want %q", tenant, f.issuer, want)
		}
	}
	wantIssuer(tenantA, "idp-a")                     // per-tenant IdP
	wantIssuer(tenantB, "idp-b")                     // a DIFFERENT per-tenant IdP (two IdPs resolved)
	wantIssuer(auth.GlobalFederationScope, "global") // the global IdP
	wantIssuer(model.NewTenantID(), "global")        // unknown tenant → global fallback
	wantIssuer("", "global")                         // pre-tenant login → global
}

func TestFederationResolve_SingleIDP_IgnoresTenantHint(t *testing.T) {
	svc := auth.NewFederationService(testStore(t), fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil) // open build
	ctx := context.Background()
	mustPut(t, svc, auth.GlobalFederationScope, oidcInput("global", true))

	// In the open build a tenant hint is ignored: every login resolves the global IdP.
	for _, tenant := range []model.TenantID{"", model.NewTenantID(), auth.GlobalFederationScope} {
		fed, err := svc.Resolve(ctx, tenant)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		f, ok := fed.(*fedTestFed)
		if !ok || f.issuer != "global" {
			t.Fatalf("resolve(%q) = %#v, want the global provider", tenant, fed)
		}
	}
}
