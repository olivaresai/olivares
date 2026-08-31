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

// U5 — home-realm routing by verified email domain (ratifies fork D7). These tests
// exercise the SERVICE layer (ResolveLogin/ResolveByAlias plumbing, global domain
// uniqueness, the relaxed per-scope rule, domain validation, and the resolved SCIM flag).
// The SELECTION LADDER itself (tenant-confined vs cross-tenant domain, deny-closed
// ambiguity, alias hint) is tested against the REAL resolver in enterprise/federation.

func oidcDomains(issuer string, enabled bool, domains ...string) auth.FederationConfigInput {
	in := oidcInput(issuer, enabled)
	in.ClaimedDomains = domains
	return in
}

// u5Issuer resolves a login and returns the built provider's issuer ("" if NoFederation).
func u5Issuer(t *testing.T, svc *auth.FederationService, in auth.SelectionInput) string {
	t.Helper()
	fed, _ := svc.ResolveLogin(context.Background(), in)
	if f, ok := fed.(*fedTestFed); ok {
		return f.issuer
	}
	return ""
}

// TestFederationU5_DomainRoutingPlumbing proves ResolveLogin threads the SelectionInput
// through the MultiIDP selector and builds the chosen IdP: a home-realm (no tenant) login
// routes by email domain, an unknown domain falls back to the global "default" IdP.
func TestFederationU5_DomainRoutingPlumbing(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{}) // enterprise capability wired
	ctx, actor := context.Background(), fedTestActor()
	tenant := model.NewTenantID()

	mustPutIdP(t, svc, auth.GlobalFederationScope, "default", oidcInput("global", true))
	if _, err := svc.PutConfigIdP(ctx, actor, tenant, "corp", oidcDomains("idp-corp", true, "corp.com")); err != nil {
		t.Fatalf("put corp: %v", err)
	}
	if _, err := svc.PutConfigIdP(ctx, actor, tenant, "eu", oidcDomains("idp-eu", true, "corp.eu")); err != nil {
		t.Fatalf("put eu (a 2nd domain-bearing active in the scope must be allowed): %v", err)
	}

	if iss := u5Issuer(t, svc, auth.SelectionInput{EmailDomain: "corp.com"}); iss != "idp-corp" {
		t.Fatalf("home-realm corp.com → %q, want idp-corp", iss)
	}
	if iss := u5Issuer(t, svc, auth.SelectionInput{EmailDomain: "corp.eu"}); iss != "idp-eu" {
		t.Fatalf("home-realm corp.eu → %q, want idp-eu", iss)
	}
	if iss := u5Issuer(t, svc, auth.SelectionInput{EmailDomain: "unknown.example"}); iss != "global" {
		t.Fatalf("unknown domain → %q, want the global fallback", iss)
	}
}

// TestFederationU5_DomainGlobalUniqueness proves a claimed domain belongs to at most one
// IdP across every scope, and that an edit of the SAME config keeping its own domain is
// allowed (self-exclusion).
func TestFederationU5_DomainGlobalUniqueness(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{})
	ctx, actor := context.Background(), fedTestActor()
	tA, tB := model.NewTenantID(), model.NewTenantID()

	mustPutIdP(t, svc, tA, "corp", oidcDomains("idp-a", false, "corp.com"))
	// Another config (different scope) claiming the same domain → refused.
	if _, err := svc.PutConfigIdP(ctx, actor, tB, "corp", oidcDomains("idp-b", false, "corp.com")); !errors.Is(err, auth.ErrDomainClaimed) {
		t.Fatalf("duplicate domain err = %v, want ErrDomainClaimed", err)
	}
	// Editing the SAME config, keeping its own domain → allowed (self-exclusion).
	mustPutIdP(t, svc, tA, "corp", oidcDomains("idp-a2", false, "corp.com"))
	// A different domain → allowed.
	mustPutIdP(t, svc, tB, "corp", oidcDomains("idp-b", false, "corp.co.uk"))
	// Case/whitespace can't defeat uniqueness: "Corp.COM " normalizes to the taken domain.
	if _, err := svc.PutConfigIdP(ctx, actor, tB, "eu", oidcDomains("idp-c", false, "Corp.COM ")); !errors.Is(err, auth.ErrDomainClaimed) {
		t.Fatalf("case/space variant err = %v, want ErrDomainClaimed", err)
	}
}

// TestFederationU5_RelaxedPerScopeRule proves U5 relaxes U4's one-active-per-scope: a
// scope may run several ACTIVE IdPs when each is disambiguated by a domain, but a second
// active DOMAINLESS IdP (two fallbacks) is still refused.
func TestFederationU5_RelaxedPerScopeRule(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{})
	ctx, actor := context.Background(), fedTestActor()
	tenant := model.NewTenantID()

	mustPutIdP(t, svc, tenant, "default", oidcInput("idp-default", true))         // domainless fallback
	mustPutIdP(t, svc, tenant, "corp", oidcDomains("idp-corp", true, "corp.com")) // domain-bearing, active OK
	mustPutIdP(t, svc, tenant, "eu", oidcDomains("idp-eu", true, "corp.eu"))      // another domain-bearing active OK
	if _, err := svc.PutConfigIdP(ctx, actor, tenant, "backup", oidcInput("idp-backup", true)); !errors.Is(err, auth.ErrScopeActiveIdPExists) {
		t.Fatalf("2nd domainless active err = %v, want ErrScopeActiveIdPExists", err)
	}
}

// TestFederationU5_DomainValidation proves a malformed domain is refused (400) and a valid
// one is normalized (case/whitespace folded).
func TestFederationU5_DomainValidation(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{})
	ctx, actor := context.Background(), fedTestActor()
	for _, bad := range []string{"nodot", "has space.com", "under_score.com", "-lead.com", "trail-.com"} {
		if _, err := svc.PutConfigIdP(ctx, actor, model.NewTenantID(), "x", oidcDomains("idp", false, bad)); !errors.Is(err, auth.ErrBadFederationConfig) {
			t.Fatalf("domain %q err = %v, want ErrBadFederationConfig", bad, err)
		}
	}
	// Valid domains normalize (lowercased/trimmed) and store.
	view, err := svc.PutConfigIdP(ctx, actor, model.NewTenantID(), "x", oidcDomains("idp", false, "Corp.COM ", "sub.corp.com"))
	if err != nil {
		t.Fatalf("valid domains: %v", err)
	}
	if len(view.ClaimedDomains) != 2 || view.ClaimedDomains[0] != "corp.com" {
		t.Fatalf("normalized domains = %v, want [corp.com sub.corp.com]", view.ClaimedDomains)
	}
}

// TestFederationU5_ResolveSurfacesScimAuthoritative proves the resolver surfaces the
// RESOLVED IdP's D4 flag, so CompleteSSO reads SCIM authority from the config that
// authenticated the user — not a scope-LIMIT-1 lookup that could read a sibling's flag.
func TestFederationU5_ResolveSurfacesScimAuthoritative(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{})
	tenant := model.NewTenantID()
	// The scope's "default" is NOT SCIM-authoritative; the domain-bearing "corp" IS.
	mustPutIdP(t, svc, tenant, "default", oidcInput("idp-default", true))
	corp := oidcDomains("idp-corp", true, "corp.com")
	corp.SCIMAuthoritative = true
	mustPutIdP(t, svc, tenant, "corp", corp)

	_, rDefault := svc.ResolveByAlias(context.Background(), tenant, "default")
	if rDefault.SCIMAuthoritative {
		t.Fatal("default IdP is not SCIM-authoritative")
	}
	_, rCorp := svc.ResolveByAlias(context.Background(), tenant, "corp")
	if !rCorp.SCIMAuthoritative {
		t.Fatal("ResolveByAlias must surface the corp IdP's SCIMAuthoritative=true (D4 keys on the resolved config)")
	}
	if rCorp.Scope != tenant || rCorp.Alias != "corp" || len(rCorp.ClaimedDomains) != 1 {
		t.Fatalf("resolved corp = %+v, want scope=%s alias=corp domains=[corp.com]", rCorp, tenant)
	}
}

// TestFederationU5_ResolvedAllowsEmail proves the domain boundary: an IdP that claims
// domains may only vouch for identities in those domains (case-insensitively), while an IdP
// with no claimed domains (the global/default) is unconstrained — the invariant the callback
// enforces after ValidateAssertion to close the cross-IdP email-fallback takeover.
func TestFederationU5_ResolvedAllowsEmail(t *testing.T) {
	if !(auth.ResolvedIdP{}).AllowsEmail("x@anywhere.example") {
		t.Fatal("an IdP with no claimed domains must be unconstrained")
	}
	r := auth.ResolvedIdP{ClaimedDomains: []string{"corp.com", "corp.eu"}}
	if !r.AllowsEmail("alice@corp.com") {
		t.Fatal("an in-domain email must be allowed")
	}
	if !r.AllowsEmail("bob@CORP.EU") {
		t.Fatal("in-domain match must be case-insensitive")
	}
	if r.AllowsEmail("eve@evil.example") {
		t.Fatal("an out-of-domain email must be refused (cross-IdP takeover surface)")
	}
	if r.AllowsEmail("noatsign") || r.AllowsEmail("trailing@") {
		t.Fatal("an address with no usable domain must be refused when domains are claimed")
	}
}

// TestFederationU5_CallbackEnvFallback is the regression for the callback env-fallback: a
// deployment with env-configured SSO (a fallback provider) and NO managed config row must
// COMPLETE its callback, not 501. The callback re-resolves (global, "default") via
// ResolveByAlias, which must return the env fallback exactly as the start leg's global
// resolution does.
func TestFederationU5_CallbackEnvFallback(t *testing.T) {
	fallback := &fedTestFed{proto: auth.ProtocolOIDC, issuer: "env-idp"}
	svc := auth.NewFederationService(testStore(t), fedTestSealer{}, fedTestBuilder, fallback, fedTestMultiIDP{})
	// No managed config row exists.
	fed, resolved := svc.ResolveByAlias(context.Background(), auth.GlobalFederationScope, "default")
	if f, ok := fed.(*fedTestFed); !ok || f.issuer != "env-idp" {
		t.Fatalf("callback resolve = %#v, want the env fallback provider", fed)
	}
	if resolved.Scope != auth.GlobalFederationScope || resolved.Alias != "default" {
		t.Fatalf("resolved = %+v, want global/default", resolved)
	}
}
