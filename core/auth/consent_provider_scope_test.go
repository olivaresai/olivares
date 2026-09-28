// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// openScopeStore opens a fresh store on engine; the PostgreSQL leg skips without
// a configured server.
func openScopeStore(t *testing.T, engine store.Engine) store.Store {
	t.Helper()
	// The test signs an account in by password: it takes the test argon2id
	// parameters and restores the production ones when it ends.
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	t.Cleanup(func() {
		auth.SetTestHashParams(auth.DefaultArgonMemKiB, auth.DefaultArgonTime, auth.DefaultArgonThreads)
	})
	if engine == store.EngineSQLite {
		return testStore(t)
	}
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st, err := sqlstore.Open(ctx, store.Config{
		Engine: engine, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin, Debug: true, MaxConns: 8,
	}, nil)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// joinThroughStore writes a membership through the store; it models one that
// predates the consent rule.
func joinThroughStore(t *testing.T, st store.Store, user model.ID, tenant model.TenantID) {
	t.Helper()
	ctx := context.Background()
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Memberships().Create(ctx, model.Membership{UserID: user, TargetTenantID: tenant, Role: auth.RoleViewer})
		return err
	}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

// TestAScopedSessionRefreshKeepsItsScope: refreshing a session a tenant's
// provider minted rotates it under the scoped prefix, and the new token still
// carries only that tenant.
func TestAScopedSessionRefreshKeepsItsScope(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			st := openScopeStore(t, engine)
			a := auth.NewAuthenticator(st, nil)
			tA := provisionTenant(t, st, "refresh-a")
			seedConfig(t, st, tA, "default", "https://idp.a.test", "a.test")
			identity := auth.FederatedIdentity{Issuer: "https://idp.a.test", Subject: "refresh-1", Email: "refresh@a.test"}
			tok, sess, err := a.CompleteSSO(ctx, identity, scopeIP, tA, false)
			if err != nil || sess.TenantScope != tA {
				t.Fatalf("sign-in through the tenant's provider = scope %q, %v; want scope %q", sess.TenantScope, err, tA)
			}
			p, err := a.Authenticate(ctx, tok)
			if err != nil {
				t.Fatalf("authenticate the scoped session: %v", err)
			}
			refreshed, rs, err := a.RefreshSession(ctx, p)
			if err != nil {
				t.Fatalf("refresh the scoped session: %v", err)
			}
			if !strings.HasPrefix(refreshed, auth.PrefixScopedSession+"_") || rs.TenantScope != tA {
				t.Errorf("the refreshed session = scope %q, scoped prefix %t; want scope %q under the scoped prefix",
					rs.TenantScope, strings.HasPrefix(refreshed, auth.PrefixScopedSession+"_"), tA)
			}
			if q, err := a.Authenticate(ctx, refreshed); err != nil || q.SessionScope() != tA {
				t.Errorf("the refreshed token = scope %q, %v; want it to authenticate scoped to %q", q.SessionScope(), err, tA)
			}
		})
	}
}

// TestATenantProviderSessionCarriesOnlyItsTenant: a sign-in through a tenant's
// identity provider mints a session scoped to that tenant, which carries no
// other tenant the account belongs to, and a just-in-time account it creates is
// recorded as that tenant's.
func TestATenantProviderSessionCarriesOnlyItsTenant(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			st := openScopeStore(t, engine)
			a := auth.NewAuthenticator(st, nil)
			super := mustSuperadmin(t, ctx, a)
			tA := provisionTenant(t, st, "provider-a")
			tB := provisionTenant(t, st, "provider-b")
			seedConfig(t, st, tA, "default", "https://idp.a.test", "a.test")

			jit := auth.FederatedIdentity{Issuer: "https://idp.a.test", Subject: "jit-1", Email: "jit@a.test"}
			tok, sess, err := a.CompleteSSO(ctx, jit, scopeIP, tA, false)
			if err != nil || tok == "" {
				t.Fatalf("just-in-time sign-in through the tenant's provider = %v", err)
			}
			if sess.TenantScope != tA {
				t.Errorf("the provider's session scope = %q, want %q", sess.TenantScope, tA)
			}
			if !strings.HasPrefix(tok, auth.PrefixScopedSession+"_") {
				t.Errorf("the provider's session token lacks the scoped prefix")
			}
			users := usersWithEmail(t, ctx, st, "jit@a.test")
			if len(users) != 1 {
				t.Fatalf("just-in-time sign-in left %d accounts, want 1", len(users))
			}
			u := users[0]
			if u.CredentialCustody != model.CustodyTenant || u.CustodyTenantID != tA {
				t.Errorf("the just-in-time account's custody = %q/%q, want tenant/%s", u.CredentialCustody, u.CustodyTenantID, tA)
			}

			// A second tenant's membership is never carried by the provider's session.
			joinThroughStore(t, st, u.ID, tB)
			tok2, _, err := a.CompleteSSO(ctx, jit, scopeIP, tA, false)
			if err != nil {
				t.Fatalf("second sign-in: %v", err)
			}
			p, err := a.Authenticate(ctx, tok2)
			if err != nil {
				t.Fatalf("authenticate the provider's session: %v", err)
			}
			if !p.IsMember(tA) || p.IsMember(tB) || p.SessionScope() != tA {
				t.Errorf("the provider's session carries tenants %v with scope %q, want only %s", p.Tenants(), p.SessionScope(), tA)
			}

			// An account the deployment created is scoped to the tenant too when it
			// signs in through that tenant's provider; its password session is not.
			dep, err := a.CreateUser(ctx, super, auth.NewUser{Email: "dep@a.test", Password: "deployment-pass-1"})
			if err != nil {
				t.Fatal(err)
			}
			joinThroughStore(t, st, dep.ID, tA)
			joinThroughStore(t, st, dep.ID, tB)
			tok3, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{Issuer: "https://idp.a.test", Subject: "dep-1", Email: "dep@a.test"}, scopeIP, tA, false)
			if err != nil {
				t.Fatalf("deployment account through the tenant's provider: %v", err)
			}
			p3, err := a.Authenticate(ctx, tok3)
			if err != nil {
				t.Fatalf("authenticate: %v", err)
			}
			if p3.IsMember(tB) || p3.SessionScope() != tA {
				t.Errorf("a deployment account's session from a tenant's provider carries tenants %v", p3.Tenants())
			}
			if u := usersWithEmail(t, ctx, st, "dep@a.test")[0]; u.CredentialCustody != model.CustodyDeployment {
				t.Errorf("a sign-in through a tenant's provider changed a deployment account's custody to %q", u.CredentialCustody)
			}
			ptok, _, err := a.Login(ctx, "dep@a.test", "deployment-pass-1", scopeIP)
			if err != nil {
				t.Fatalf("password login: %v", err)
			}
			pp, err := a.Authenticate(ctx, ptok)
			if err != nil {
				t.Fatal(err)
			}
			if !pp.IsMember(tB) || pp.SessionScope() != "" {
				t.Errorf("a deployment account's password session lost account scope")
			}
		})
	}
}
