// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
)

// TestPurposeRestrictedTokenAbsentFromReverseQueries pins F1: a purpose-restricted
// (e.g. Purpose="pep") token never authenticates as an ordinary principal, so it
// must authorize NOTHING on the simulation/reverse-query surfaces
// (PrincipalForToken, TenantPrincipals) — otherwise AuthZEN could answer "allow"
// for a credential that can never authenticate.
func TestPurposeRestrictedTokenAbsentFromReverseQueries(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	_, tok, err := a.IssueToken(ctx, admin, auth.TokenSpec{Name: "pep-cred", BoundTenant: tenant, Role: auth.RoleAdmin})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Mark it purpose-restricted, as binding it to a PEP service would.
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		got, err := as.Tokens().Get(ctx, tok.ID)
		if err != nil {
			return err
		}
		got.Purpose = "pep"
		_, err = as.Tokens().Update(ctx, got)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if _, found, err := a.PrincipalForToken(ctx, tok.ID); err != nil || found {
		t.Errorf("PrincipalForToken(purpose=pep) = (found=%v, err=%v), want (false, nil)", found, err)
	}

	pop, err := a.TenantPrincipals(ctx, tenant, auth.AAL3)
	if err != nil {
		t.Fatalf("TenantPrincipals: %v", err)
	}
	for _, p := range pop {
		if p.Kind == auth.KindToken && p.CredID == tok.ID {
			t.Fatalf("purpose-restricted token %q must not appear in TenantPrincipals", tok.ID)
		}
	}
}
