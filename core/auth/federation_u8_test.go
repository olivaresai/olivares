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
	"github.com/olivaresai/olivares/core/store"
)

// U8 — deterministic + INDEXED IdP selection. ResolveLogin now narrows the candidate set
// with an indexed per-domain lookup (federation_domain_claims) instead of draining every config,
// and a UNIQUE(domain) constraint hardens U5's app-level uniqueness scan into a commit-time
// guarantee. The equivalence of the indexed path with the old drain is proven by the ENTIRE
// U4/U5 suite (they route through ResolveLogin); these tests cover the NEW behavior: the index
// is maintained on write, a delete frees the domain, the boot reconcile backfills + quarantines,
// and the DB constraint rejects a duplicate the app-scan could miss under a race.

// u8Svc builds a federation service with the enterprise selector wired AND returns the store, so
// a test can seed rows directly (simulating a pre-U8 upgrade or a raced duplicate).
func u8Svc(t *testing.T) (*auth.FederationService, store.Store) {
	t.Helper()
	st := testStore(t)
	return auth.NewFederationService(st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, fedTestMultiIDP{}), st
}

// seedConfig writes an ACTIVE OIDC config DIRECTLY through the store, bypassing PutConfigIdP —
// so it carries NO derived domain-index rows, exactly like a config written by a pre-U8 binary.
func seedConfig(t *testing.T, st store.Store, scope model.TenantID, alias, issuer string, domains ...string) model.ID {
	t.Helper()
	var id model.ID
	err := st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		c, e := as.FederationConfigs().Create(context.Background(), model.FederationConfig{
			TargetTenantID: scope, Alias: alias, Protocol: auth.ProtocolOIDC, Status: model.StatusActive,
			OIDCIssuer: issuer, OIDCClientSecretSealed: "sealed-" + issuer, ClaimedDomains: domains,
		})
		id = c.ID
		return e
	})
	if err != nil {
		t.Fatalf("seed config %s/%s: %v", scope, alias, err)
	}
	return id
}

// TestFederationU8_DomainIndexMaintainedOnWrite proves PutConfigIdP maintains the derived domain
// index transactionally: a home-realm login routes by the indexed claim, and CHANGING a config's
// domains re-points the index (the old domain stops routing, the new one starts).
func TestFederationU8_DomainIndexMaintainedOnWrite(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{})
	tenant := model.NewTenantID()

	mustPutIdP(t, svc, tenant, "corp", oidcDomains("idp-corp", true, "corp.com"))
	if iss := u5Issuer(t, svc, auth.SelectionInput{EmailDomain: "corp.com"}); iss != "idp-corp" {
		t.Fatalf("home-realm corp.com → %q via the index, want idp-corp", iss)
	}
	// Re-write the SAME IdP claiming a DIFFERENT domain: the index must follow.
	mustPutIdP(t, svc, tenant, "corp", oidcDomains("idp-corp", true, "corp.io"))
	if iss := u5Issuer(t, svc, auth.SelectionInput{EmailDomain: "corp.io"}); iss != "idp-corp" {
		t.Fatalf("after re-claim, corp.io → %q, want idp-corp", iss)
	}
	if iss := u5Issuer(t, svc, auth.SelectionInput{EmailDomain: "corp.com"}); iss != "" {
		t.Fatalf("the dropped domain corp.com must no longer route (got %q) — a stale index row", iss)
	}
}

// TestFederationU8_DeleteFreesDomain is the regression for the DeleteConfigIdP index-cleanup
// blocker: deleting an IdP (physical delete of a non-default AND the default tombstone) must drop
// its domain-index rows, or the UNIQUE(domain) row would outlive the claim and permanently block
// any other IdP from re-claiming the domain while the app-scan reports it free.
func TestFederationU8_DeleteFreesDomain(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{})
	ctx, actor := context.Background(), fedTestActor()
	tA, tB := model.NewTenantID(), model.NewTenantID()

	// (1) Physical delete of a non-default IdP frees its domain.
	mustPutIdP(t, svc, tA, "corp", oidcDomains("idp-a", true, "corp.com"))
	if err := svc.DeleteConfigIdP(ctx, actor, tA, "corp"); err != nil {
		t.Fatalf("delete corp: %v", err)
	}
	if _, err := svc.PutConfigIdP(ctx, actor, tB, "corp", oidcDomains("idp-b", true, "corp.com")); err != nil {
		t.Fatalf("corp.com must be re-claimable after delete, got %v", err)
	}
	if iss := u5Issuer(t, svc, auth.SelectionInput{EmailDomain: "corp.com"}); iss != "idp-b" {
		t.Fatalf("corp.com must now route to the re-claiming IdP, got %q", iss)
	}

	// (2) The DEFAULT tombstone (which clears ClaimedDomains) also frees the domain.
	tC, tD := model.NewTenantID(), model.NewTenantID()
	mustPutIdP(t, svc, tC, "default", oidcDomains("idp-c", true, "eu.com"))
	if err := svc.DeleteConfigIdP(ctx, actor, tC, "default"); err != nil {
		t.Fatalf("delete default: %v", err)
	}
	if _, err := svc.PutConfigIdP(ctx, actor, tD, "default", oidcDomains("idp-d", true, "eu.com")); err != nil {
		t.Fatalf("eu.com must be re-claimable after the default tombstone, got %v", err)
	}
}

// TestFederationU8_UniqueConstraintRejectsDuplicate proves the STORAGE-level hardening: a second
// claim row for an already-claimed domain fails the UNIQUE(tenant_id, domain) constraint with
// store.ErrConflict — the commit-time guard that closes the Postgres READ-COMMITTED race the
// app-level scan cannot, independent of that scan.
func TestFederationU8_UniqueConstraintRejectsDuplicate(t *testing.T) {
	_, st := u8Svc(t)
	ctx := context.Background()
	tA := model.NewTenantID()
	cA, cB := model.NewID(), model.NewID()

	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, e := as.FederationDomainClaims().Create(ctx, model.FederationDomainClaim{TargetTenantID: tA, ConfigID: cA, Domain: "corp.com"})
		return e
	}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, e := as.FederationDomainClaims().Create(ctx, model.FederationDomainClaim{TargetTenantID: tA, ConfigID: cB, Domain: "corp.com"})
		return e
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a duplicate domain claim must violate UNIQUE(domain) → store.ErrConflict, got %v", err)
	}
}

// domainResolves reports whether a home-realm login for domain resolves to a non-global IdP
// (via the indexed loadConfigByDomain path exercised by ResolveLogin).
func domainResolves(t *testing.T, svc *auth.FederationService, domain string) bool {
	t.Helper()
	fed, resolved := svc.ResolveLogin(context.Background(), auth.SelectionInput{EmailDomain: domain})
	_ = fed
	return resolved.Scope != auth.GlobalFederationScope
}

// TestFederationU8_ReconcileBackfillsAndQuarantines proves the boot reconcile: it backfills the
// index from configs written before U8 (no index rows), and — the adversarial-review MAJOR fix —
// QUARANTINES a genuine cross-config duplicate (a legacy Postgres-race state) rather than picking
// a lowest-id winner: a uniquely-claimed domain routes; an ambiguously-claimed one deny-closes to
// the global IdP, matching SelectActive's deny-closed-on-ambiguity. It is idempotent.
func TestFederationU8_ReconcileBackfillsAndQuarantines(t *testing.T) {
	svc, st := u8Svc(t)
	ctx := context.Background()
	tA, tB, tC := model.NewTenantID(), model.NewTenantID(), model.NewTenantID()

	// Pre-U8 state: three configs with domains but NO index rows. corp.com is claimed by TWO
	// configs (a uniqueness breach that a pre-U8 Postgres race could have committed); eu.com by one.
	seedConfig(t, st, tA, "corp", "idp-a", "corp.com")
	seedConfig(t, st, tB, "corp", "idp-b", "corp.com") // duplicate claim
	seedConfig(t, st, tC, "eu", "idp-c", "eu.com")

	// Before the reconcile the index is empty → nothing routes.
	if domainResolves(t, svc, "eu.com") {
		t.Fatal("eu.com must not route before the backfill (no index rows yet)")
	}

	if err := svc.ReconcileDomainClaims(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// The uniquely-claimed domain now routes; the ambiguous one is QUARANTINED (deny-closed).
	if !domainResolves(t, svc, "eu.com") {
		t.Error("eu.com (uniquely claimed) must route after the backfill")
	}
	if domainResolves(t, svc, "corp.com") {
		t.Error("corp.com (claimed by two configs) must be QUARANTINED → deny-close to global, never a lowest-id winner")
	}

	// Idempotent: a second reconcile changes nothing and never errors.
	if err := svc.ReconcileDomainClaims(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !domainResolves(t, svc, "eu.com") || domainResolves(t, svc, "corp.com") {
		t.Error("a second reconcile must be a no-op (eu.com routes, corp.com stays quarantined)")
	}
}

// TestFederationU8_ReconcileRemovesOrphan proves the reconcile prunes a stale index row whose
// config no longer claims the domain, so loadConfigByDomain never routes to a dead/wrong IdP.
func TestFederationU8_ReconcileRemovesOrphan(t *testing.T) {
	svc, st := u8Svc(t)
	ctx := context.Background()
	tA := model.NewTenantID()
	cid := seedConfig(t, st, tA, "corp", "idp-a", "corp.com")

	// A stale/orphan index row: it points at the config but for a domain the config never claims.
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, e := as.FederationDomainClaims().Create(ctx, model.FederationDomainClaim{TargetTenantID: tA, ConfigID: cid, Domain: "stale.example"})
		return e
	}); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	if err := svc.ReconcileDomainClaims(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// The real claim is indexed; the orphan is gone.
	if !domainResolves(t, svc, "corp.com") {
		t.Error("the real claim corp.com must route after reconcile")
	}
	if domainResolves(t, svc, "stale.example") {
		t.Error("the orphan domain must have been pruned (deny-close to global)")
	}
}
