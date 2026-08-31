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

// U4 — the first-class IdP entity: a scope may hold SEVERAL IdP configs (keyed by
// alias) but only ONE may be ACTIVE at a time, and the per-scope activation flip changes
// which provider a login resolves. These tests exercise the service layer with the same
// doubles as federation_config_cap_test.go (fedTestSealer/Builder/MultiIDP/fedTestFed).

func u4Svc(t *testing.T, multi auth.MultiIDP) *auth.FederationService {
	t.Helper()
	return auth.NewFederationService(testStore(t), fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, multi)
}

func mustPutIdP(t *testing.T, svc *auth.FederationService, scope model.TenantID, alias string, in auth.FederationConfigInput) {
	t.Helper()
	if _, err := svc.PutConfigIdP(context.Background(), fedTestActor(), scope, alias, in); err != nil {
		t.Fatalf("put %s/%s: %v", scope, alias, err)
	}
}

func aliasesOf(t *testing.T, svc *auth.FederationService, scope model.TenantID) []string {
	t.Helper()
	views, err := svc.ListIdPs(context.Background(), scope)
	if err != nil {
		t.Fatalf("list idps: %v", err)
	}
	out := make([]string, len(views))
	for i, v := range views {
		out[i] = v.Alias
	}
	return out
}

func strsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func u4WantIssuer(t *testing.T, svc *auth.FederationService, tenant model.TenantID, want string) {
	t.Helper()
	fed, err := svc.Resolve(context.Background(), tenant)
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

// TestFederationU4_PerScopeSingleActiveFlip is the U4 wire-of-behavior at the service
// layer: several IdPs coexist under one scope but only one is active, activating a second
// while one is active is refused, and the explicit deactivate-then-activate flip changes
// which provider Resolve returns (the decision flips).
func TestFederationU4_PerScopeSingleActiveFlip(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{}) // enterprise: deployment cap lifted, per-scope rule applies
	ctx, actor := context.Background(), fedTestActor()
	tenant := model.NewTenantID()

	mustPutIdP(t, svc, tenant, "default", oidcInput("idp-default", true))
	mustPutIdP(t, svc, tenant, "backup", oidcInput("idp-backup", false)) // staged, inactive

	// Activating the second IdP while the first is active is refused (one active per scope).
	_, err := svc.PutConfigIdP(ctx, actor, tenant, "backup", oidcInput("idp-backup", true))
	if !errors.Is(err, auth.ErrScopeActiveIdPExists) {
		t.Fatalf("activating a 2nd active IdP in the scope err = %v, want ErrScopeActiveIdPExists", err)
	}
	u4WantIssuer(t, svc, tenant, "idp-default") // the active default resolves

	// The explicit flip: deactivate default, then activate backup.
	mustPutIdP(t, svc, tenant, "default", oidcInput("idp-default", false))
	mustPutIdP(t, svc, tenant, "backup", oidcInput("idp-backup", true))
	u4WantIssuer(t, svc, tenant, "idp-backup") // the decision flipped

	if got := aliasesOf(t, svc, tenant); !strsEqual(got, []string{"default", "backup"}) {
		t.Fatalf("ListIdPs aliases = %v, want [default backup]", got)
	}
}

// TestFederationU4_OpenBuildCapByID proves the tightened open-core cap: the base build
// (no MultiIDP) admits at most ONE active IdP deployment-wide, keyed by IdP identity — so
// a second active IdP in the SAME scope (a new alias) is refused too, the hole the index
// relaxation would otherwise open. Editing the one active IdP and staging inactive
// siblings stay allowed.
func TestFederationU4_OpenBuildCapByID(t *testing.T) {
	svc := u4Svc(t, nil) // open build
	ctx, actor := context.Background(), fedTestActor()
	tenant := model.NewTenantID()

	mustPutIdP(t, svc, tenant, "default", oidcInput("idp-a", true))

	// A second ACTIVE IdP in the SAME scope (different alias) — refused.
	_, err := svc.PutConfigIdP(ctx, actor, tenant, "backup", oidcInput("idp-b", true))
	if !errors.Is(err, auth.ErrMultiIDPRequiresEnterprise) {
		t.Fatalf("2nd active same-scope (open) err = %v, want ErrMultiIDPRequiresEnterprise", err)
	}
	// A second ACTIVE IdP in a DIFFERENT scope — also refused.
	_, err = svc.PutConfigIdP(ctx, actor, model.NewTenantID(), "default", oidcInput("idp-c", true))
	if !errors.Is(err, auth.ErrMultiIDPRequiresEnterprise) {
		t.Fatalf("2nd active diff-scope (open) err = %v, want ErrMultiIDPRequiresEnterprise", err)
	}
	// Staging an inactive sibling is allowed.
	mustPutIdP(t, svc, tenant, "backup", oidcInput("idp-b", false))
	// Re-saving the one active IdP (matched by ID) is allowed.
	mustPutIdP(t, svc, tenant, "default", oidcInput("idp-a", true))
}

// TestFederationU4_AliasCRUDAndHardDelete proves a non-default IdP is a discrete entity:
// it is listed, hard-deleted (freeing its alias), and re-creatable under the same alias.
func TestFederationU4_AliasCRUDAndHardDelete(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{})
	ctx, actor := context.Background(), fedTestActor()
	tenant := model.NewTenantID()

	mustPutIdP(t, svc, tenant, "default", oidcInput("idp-default", true))
	mustPutIdP(t, svc, tenant, "backup", oidcInput("idp-backup", false))
	if got := aliasesOf(t, svc, tenant); !strsEqual(got, []string{"default", "backup"}) {
		t.Fatalf("aliases = %v, want [default backup]", got)
	}

	// Hard-delete the non-default IdP → dropped from the list, alias freed.
	if err := svc.DeleteConfigIdP(ctx, actor, tenant, "backup"); err != nil {
		t.Fatalf("delete backup: %v", err)
	}
	if got := aliasesOf(t, svc, tenant); !strsEqual(got, []string{"default"}) {
		t.Fatalf("after delete aliases = %v, want [default]", got)
	}
	// Re-create the same alias (it was freed).
	mustPutIdP(t, svc, tenant, "backup", oidcInput("idp-backup2", false))
	if got := aliasesOf(t, svc, tenant); !strsEqual(got, []string{"default", "backup"}) {
		t.Fatalf("after recreate aliases = %v, want [default backup]", got)
	}
}

// TestFederationU4_AliasValidation proves a malformed alias is refused deny-closed and a
// valid one is normalized (case/whitespace folded) so it cannot fork the same IdP.
func TestFederationU4_AliasValidation(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{})
	ctx, actor := context.Background(), fedTestActor()
	for _, bad := range []string{"Has Space", "up!per", "-lead", "toolong-toolong-toolong-toolong-x"} {
		if _, err := svc.PutConfigIdP(ctx, actor, model.NewTenantID(), bad, oidcInput("idp", false)); !errors.Is(err, auth.ErrBadFederationAlias) {
			t.Fatalf("PutConfigIdP(alias=%q) err = %v, want ErrBadFederationAlias", bad, err)
		}
	}
	// "Okta " normalizes to "okta" and is accepted.
	tenant := model.NewTenantID()
	mustPutIdP(t, svc, tenant, "Okta ", oidcInput("idp", false))
	if got := aliasesOf(t, svc, tenant); !strsEqual(got, []string{"okta"}) {
		t.Fatalf("normalized alias = %v, want [okta]", got)
	}
}

// TestFederationU4_GlobalScopeDefaultOnly proves the deployment-wide login IdP must use
// the "default" alias: activating a non-default IdP in the GLOBAL scope is refused (until
// U5 routes global IdPs by domain), while STAGING one inactive is allowed.
func TestFederationU4_GlobalScopeDefaultOnly(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{})
	ctx, actor := context.Background(), fedTestActor()

	if _, err := svc.PutConfigIdP(ctx, actor, auth.GlobalFederationScope, "backup", oidcInput("g-backup", true)); !errors.Is(err, auth.ErrGlobalIdPMustBeDefault) {
		t.Fatalf("activating a non-default GLOBAL IdP err = %v, want ErrGlobalIdPMustBeDefault", err)
	}
	// Staging it inactive is allowed (config staged for U5 home-realm routing).
	mustPutIdP(t, svc, auth.GlobalFederationScope, "backup", oidcInput("g-backup", false))
	// The "default" global IdP activates normally.
	mustPutIdP(t, svc, auth.GlobalFederationScope, "default", oidcInput("g-default", true))
	u4WantIssuer(t, svc, auth.GlobalFederationScope, "g-default")
}

// TestFederationU4_DefaultTombstoneKept proves the DEFAULT primary is tombstoned (kept,
// Configured=false, SSO authoritatively off) rather than physically removed — the pre-U4
// semantics that stop a delete from silently re-enabling an env IdP.
func TestFederationU4_DefaultTombstoneKept(t *testing.T) {
	svc := u4Svc(t, fedTestMultiIDP{})
	ctx, actor := context.Background(), fedTestActor()
	mustPutIdP(t, svc, auth.GlobalFederationScope, "default", oidcInput("g", true))

	if err := svc.DeleteConfig(ctx, actor, auth.GlobalFederationScope); err != nil {
		t.Fatalf("delete default: %v", err)
	}
	v, err := svc.GetConfig(ctx, auth.GlobalFederationScope)
	if err != nil {
		t.Fatal(err)
	}
	if v.Configured {
		t.Fatal("tombstoned default must read Configured=false")
	}
	// The row is KEPT (tombstone), so it still lists under its alias.
	if got := aliasesOf(t, svc, auth.GlobalFederationScope); !strsEqual(got, []string{"default"}) {
		t.Fatalf("tombstone aliases = %v, want [default] (row kept)", got)
	}
	fed, err := svc.Resolve(ctx, auth.GlobalFederationScope)
	if err != nil {
		t.Fatal(err)
	}
	if _, isNo := fed.(auth.NoFederation); !isNo {
		t.Fatalf("resolve after default delete = %T, want NoFederation (authoritative off)", fed)
	}
}
