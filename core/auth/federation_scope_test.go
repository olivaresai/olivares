// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The scope of a federated sign-in. A tenant's identity provider vouches for the
// accounts of its own tenant whose address lies in a domain it claims, and for
// nothing else: CompleteSSO under a tenant scope returns a session only for a member
// of that tenant whose address the tenant's provider claims, and provisions a new
// account only under such a claim, with its membership in that tenant. The
// deployment-wide provider is the deployment's own authority and is unchanged.
//
// Providers are written straight to the store (seedConfig) so each case holds
// whatever the store holds, including a provider activated before activation checked
// for a claimed domain.

const scopeIP = "10.0.0.9"

// scopeFixture is a deployment with two tenants and a superadmin principal.
type scopeFixture struct {
	st     store.Store
	a      *auth.Authenticator
	super  auth.Principal
	tA, tB model.TenantID
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	return &scopeFixture{
		st: st, a: a, super: super,
		tA: provisionTenant(t, st, "tenant-a"), tB: provisionTenant(t, st, "tenant-b"),
	}
}

// account reads the account holding email; ok=false when there is none.
func (f *scopeFixture) account(t *testing.T, email string) (model.User, bool) {
	t.Helper()
	us := usersWithEmail(t, context.Background(), f.st, email)
	if len(us) == 0 {
		return model.User{}, false
	}
	return us[0], true
}

// sessions counts the sessions held by an account.
func (f *scopeFixture) sessions(t *testing.T, userID model.ID) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		ss, err := rowsWhere(ctx, as.Sessions().List, "user_id", userID.String())
		n = len(ss)
		return err
	}); err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	return n
}

// memberships lists the tenant memberships held by an account.
func (f *scopeFixture) memberships(t *testing.T, userID model.ID) []model.Membership {
	t.Helper()
	ctx := context.Background()
	var ms []model.Membership
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		ms, err = rowsWhere(ctx, as.Memberships().List, "user_id", userID.String())
		return err
	}); err != nil {
		t.Fatalf("list memberships: %v", err)
	}
	return ms
}

// link sets the issuer-qualified subject an account signs in as.
func (f *scopeFixture) link(t *testing.T, userID model.ID, issuer, subject string) {
	t.Helper()
	ctx := context.Background()
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, userID)
		if err != nil {
			return err
		}
		u.SsoSubject = qualified(issuer, subject)
		_, err = as.Users().Update(ctx, u)
		return err
	}); err != nil {
		t.Fatalf("link account: %v", err)
	}
}

// blockedSince returns the blocked-login audit events recorded after seq for reason.
func (f *scopeFixture) blockedSince(t *testing.T, seq int64, reason string) []model.AuditEvent {
	t.Helper()
	ctx := context.Background()
	var out []model.AuditEvent
	if err := f.st.AuthView(ctx, func(as store.AuthScope) error {
		walker, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("audit lacks canonical metadata")
		}
		return walker.WalkCanonical(ctx, seq+1, func(e model.AuditEvent, meta string, _ []byte) error {
			if err := json.Unmarshal([]byte(meta), &e.Meta); err != nil {
				return err
			}
			if e.Action == "auth.login.blocked" && e.Meta["reason"] == reason {
				out = append(out, e)
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit events: %v", err)
	}
	return out
}

// rowsWhere reads the rows whose column equals value (a fixture holds a handful).
func rowsWhere[T any](ctx context.Context, list func(context.Context, model.Query) ([]T, model.Page, error), column, value string) ([]T, error) {
	rows, _, err := list(ctx, model.Query{
		Filters: []model.Filter{{Column: column, Op: model.OpEq, Value: value}}, Limit: 1000,
	})
	return rows, err
}

// refused runs one sign-in that must be refused and checks what a refusal leaves:
// the uniform error, no token, no new session and no link on the account (when it
// exists), and exactly one audit record naming the scope.
func (f *scopeFixture) refused(t *testing.T, id auth.FederatedIdentity, scope model.TenantID, email string) {
	t.Helper()
	ctx := context.Background()
	before, existed := f.account(t, email)
	var sessionsBefore int
	if existed {
		sessionsBefore = f.sessions(t, before.ID)
	}
	seq := auditHead(t, ctx, f.st)

	tok, sess, err := f.a.CompleteSSO(ctx, id, scopeIP, scope, false)
	if err == nil || tok != "" || !sess.ID.IsZero() {
		t.Fatalf("a tenant's provider obtained a session for an account it has no claim on (err=%v, token issued=%t)", err, tok != "")
	}
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("refusal = %v, want ErrUnauthenticated (the answer every refused assertion gets)", err)
	}
	after, exists := f.account(t, email)
	switch {
	case existed && after.SsoSubject != before.SsoSubject:
		t.Fatalf("a refused sign-in changed the account's link %q -> %q", before.SsoSubject, after.SsoSubject)
	case existed && f.sessions(t, after.ID) != sessionsBefore:
		t.Fatal("a refused sign-in left a session behind")
	case !existed && exists:
		t.Fatal("a refused sign-in provisioned an account")
	}
	blocked := f.blockedSince(t, seq, "sso_outside_provider_scope")
	if len(blocked) != 1 {
		t.Fatalf("refusal left %d scope audit records, want exactly 1", len(blocked))
	}
	if got := blocked[0].Meta["scope"]; got != scope.String() {
		t.Fatalf("audit record scope = %v, want %s", got, scope)
	}
}

func TestCompleteSSORefusesAnIdentityOutsideTheSelectedIdPScope(t *testing.T) {
	t.Run("a provider with no claimed domain vouches for no existing account", func(t *testing.T) {
		f := newScopeFixture(t)
		seedConfig(t, f.st, f.tA, "default", "https://idp.a.test")
		mustMember(t, context.Background(), f.a, f.super, f.tB, "member@b.test", auth.RoleViewer)

		f.refused(t, auth.FederatedIdentity{Issuer: "https://idp.a.test", Subject: "a-1", Email: "member@b.test"}, f.tA, "member@b.test")
	})
	t.Run("the answer does not depend on whether the address exists", func(t *testing.T) {
		f := newScopeFixture(t)
		seedConfig(t, f.st, f.tA, "default", "https://idp.a.test")

		f.refused(t, auth.FederatedIdentity{Issuer: "https://idp.a.test", Subject: "a-2", Email: "nobody@b.test"}, f.tA, "nobody@b.test")
	})
	t.Run("an address outside the provider's claimed domains", func(t *testing.T) {
		f := newScopeFixture(t)
		seedConfig(t, f.st, f.tA, "default", "https://idp.a.test", "a.test")
		seedConfig(t, f.st, f.tB, "default", "https://idp.b.test", "b.test")
		mustMember(t, context.Background(), f.a, f.super, f.tB, "member@b.test", auth.RoleViewer)

		f.refused(t, auth.FederatedIdentity{Issuer: "https://idp.a.test", Subject: "a-3", Email: "member@b.test"}, f.tA, "member@b.test")
	})
}

// A member of two tenants carries both tenants' grants in every session, so a
// membership in the provider's tenant is not enough: the provider must also claim
// the account's address, however the account was correlated.
func TestCompleteSSORefusesAMemberOfTwoTenantsOutsideTheProvidersClaim(t *testing.T) {
	setup := func(t *testing.T) (*scopeFixture, model.ID) {
		t.Helper()
		ctx := context.Background()
		f := newScopeFixture(t)
		seedConfig(t, f.st, f.tA, "default", "https://idp.a.test", "a.test")
		seedConfig(t, f.st, f.tB, "default", "https://idp.b.test", "b.test")
		uid, _ := mustMember(t, ctx, f.a, f.super, f.tB, "shared@b.test", auth.RoleAdmin)
		if _, err := f.a.GrantMembership(ctx, f.super, uid, f.tA, auth.RoleViewer, model.ID("")); err != nil {
			t.Fatalf("grant the second membership: %v", err)
		}
		return f, uid
	}
	t.Run("correlated by address", func(t *testing.T) {
		f, _ := setup(t)
		f.refused(t, auth.FederatedIdentity{Issuer: "https://idp.a.test", Subject: "a-4", Email: "shared@b.test"}, f.tA, "shared@b.test")
	})
	t.Run("correlated by subject", func(t *testing.T) {
		f, uid := setup(t)
		f.link(t, uid, "https://idp.b.test", "shared-1")
		f.refused(t, auth.FederatedIdentity{Issuer: "https://idp.b.test", Subject: "shared-1", Email: "someone@a.test"}, f.tA, "shared@b.test")
	})
}

// Just-in-time provisioning under a tenant's provider lands the new account in that
// tenant, and only there; an address the provider does not claim provisions nothing.
func TestCompleteSSOProvisionsIntoTheProvidersTenantOnly(t *testing.T) {
	t.Run("an address in the claimed domain", func(t *testing.T) {
		f := newScopeFixture(t)
		seedConfig(t, f.st, f.tA, "default", "https://idp.a.test", "a.test")

		tok, sess, err := f.a.CompleteSSO(context.Background(), auth.FederatedIdentity{
			Issuer: "https://idp.a.test", Subject: "new-1", Email: "new@a.test",
		}, scopeIP, f.tA, false)
		if err != nil || tok == "" {
			t.Fatalf("just-in-time sign-in = %v (token issued %t), want a session", err, tok != "")
		}
		u, ok := f.account(t, "new@a.test")
		if !ok || u.ID != sess.UserID {
			t.Fatalf("session user %s, account %+v: want the provisioned account", sess.UserID, u)
		}
		ms := f.memberships(t, u.ID)
		if len(ms) != 1 || ms[0].TargetTenantID != f.tA || ms[0].Role != auth.RoleViewer {
			t.Fatalf("provisioned account memberships = %+v, want exactly one viewer membership in the provider's tenant", ms)
		}
	})
	t.Run("an address outside the claimed domains", func(t *testing.T) {
		f := newScopeFixture(t)
		seedConfig(t, f.st, f.tA, "default", "https://idp.a.test", "a.test")

		f.refused(t, auth.FederatedIdentity{Issuer: "https://idp.a.test", Subject: "new-2", Email: "new@elsewhere.test"}, f.tA, "new@elsewhere.test")
	})
}

// Activating a tenant's provider requires a claimed domain wherever a tenant's
// provider can be selected at sign-in (the multi-provider capability). Staging it
// inactive stays allowed, and the open build, which never selects a tenant's
// provider, is unchanged.
func TestPutConfigIdPRefusesActivatingATenantProviderWithoutAClaimedDomain(t *testing.T) {
	ctx, actor := context.Background(), fedTestActor()

	svc := u4Svc(t, fedTestMultiIDP{})
	tenant := model.NewTenantID()
	if _, err := svc.PutConfigIdP(ctx, actor, tenant, "default", oidcInput("idp-t", true)); !errors.Is(err, auth.ErrBadFederationConfig) {
		t.Fatalf("activating a tenant's provider with no claimed domain err = %v, want ErrBadFederationConfig", err)
	}
	if v, err := svc.GetConfigIdP(ctx, tenant, "default"); err != nil || v.Status == string(model.StatusActive) {
		t.Fatalf("after the refusal the provider reads %+v (err %v), want it not active", v, err)
	}
	if _, err := svc.PutConfigIdP(ctx, actor, tenant, "default", oidcInput("idp-t", false)); err != nil {
		t.Fatalf("staging it inactive must stay allowed: %v", err)
	}
	if _, err := svc.PutConfigIdP(ctx, actor, tenant, "default", oidcDomains("idp-t", true, "t.test")); err != nil {
		t.Fatalf("activating it with a claimed domain must be allowed: %v", err)
	}
	if _, err := svc.PutConfigIdP(ctx, actor, auth.GlobalFederationScope, "default", oidcInput("idp-g", true)); err != nil {
		t.Fatalf("the deployment-wide provider needs no claimed domain: %v", err)
	}

	open := u4Svc(t, nil)
	if _, err := open.PutConfigIdP(ctx, actor, model.NewTenantID(), "default", oidcInput("idp-o", true)); err != nil {
		t.Fatalf("the open build's activation is unchanged: %v", err)
	}
}

// Controls: what the binding must not change.

// The deployment-wide provider signs an existing account in by address exactly as
// before, binds its subject, and provisions a new account with no membership.
func TestCompleteSSOWithTheDeploymentWideProviderIsUnchanged(t *testing.T) {
	ctx := context.Background()
	f := newScopeFixture(t)
	putGlobalSSOConfig(t, f.st, model.FederationConfig{OIDCIssuer: "https://idp.global.test"})
	uid, _ := mustMember(t, ctx, f.a, f.super, f.tA, "member@one.test", auth.RoleViewer)

	for _, scope := range []model.TenantID{auth.GlobalFederationScope, ""} {
		tok, sess, err := f.a.CompleteSSO(ctx, auth.FederatedIdentity{
			Issuer: "https://idp.global.test", Subject: "g-1", Email: "member@one.test",
		}, scopeIP, scope, false)
		if err != nil || tok == "" || sess.UserID != uid {
			t.Fatalf("scope %q: deployment-wide sign-in = %v (token issued %t, user %s), want the member's session", scope, err, tok != "", sess.UserID)
		}
	}
	if u, _ := f.account(t, "member@one.test"); u.SsoSubject != qualified("https://idp.global.test", "g-1") {
		t.Fatalf("link after the first sign-in = %q, want the provider's subject", u.SsoSubject)
	}
	if ms := f.memberships(t, uid); len(ms) != 1 {
		t.Fatalf("memberships after sign-in = %+v, want the one granted", ms)
	}

	if _, _, err := f.a.CompleteSSO(ctx, auth.FederatedIdentity{
		Issuer: "https://idp.global.test", Subject: "g-2", Email: "new@anywhere.test",
	}, scopeIP, auth.GlobalFederationScope, false); err != nil {
		t.Fatalf("deployment-wide just-in-time sign-in: %v", err)
	}
	u, ok := f.account(t, "new@anywhere.test")
	if !ok {
		t.Fatal("the deployment-wide provider no longer provisions")
	}
	if ms := f.memberships(t, u.ID); len(ms) != 0 {
		t.Fatalf("a deployment-wide provisioned account holds %+v, want no membership", ms)
	}
}

// An account linked to the tenant's provider, inside its claim and a member of its
// tenant, signs in.
func TestCompleteSSOSignsInALinkedMemberInsideTheProvidersClaim(t *testing.T) {
	ctx := context.Background()
	f := newScopeFixture(t)
	seedConfig(t, f.st, f.tA, "default", "https://idp.a.test", "a.test")
	uid, _ := mustMember(t, ctx, f.a, f.super, f.tA, "alice@a.test", auth.RoleViewer)
	f.link(t, uid, "https://idp.a.test", "alice-1")

	tok, sess, err := f.a.CompleteSSO(ctx, auth.FederatedIdentity{
		Issuer: "https://idp.a.test", Subject: "alice-1", Email: "alice@a.test",
	}, scopeIP, f.tA, false)
	if err != nil || tok == "" || sess.UserID != uid {
		t.Fatalf("linked member sign-in = %v (token issued %t, user %s), want the member's session", err, tok != "", sess.UserID)
	}
}

// A superadmin is never signed in through federation, under any scope — even one whose
// provider claims its address and whose tenant it belongs to: the superadmin guard itself
// refuses it and records so.
func TestCompleteSSOStillRefusesASuperadmin(t *testing.T) {
	ctx := context.Background()
	f := newScopeFixture(t)
	seedConfig(t, f.st, f.tA, "default", "https://idp.a.test", "example.com")
	root, ok := f.account(t, "root@example.com")
	if !ok {
		t.Fatal("fixture has no superadmin account")
	}
	if _, err := f.a.GrantMembership(ctx, f.super, root.ID, f.tA, auth.RoleOwner, model.ID("")); err != nil {
		t.Fatalf("grant the superadmin a membership: %v", err)
	}
	before := f.sessions(t, root.ID)
	for _, scope := range []model.TenantID{auth.GlobalFederationScope, f.tA} {
		seq := auditHead(t, ctx, f.st)
		if _, _, err := f.a.CompleteSSO(ctx, auth.FederatedIdentity{
			Issuer: "https://idp.a.test", Subject: "root-1", Email: "root@example.com",
		}, scopeIP, scope, false); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("scope %q: superadmin sign-in err = %v, want ErrUnauthenticated", scope, err)
		}
		if n := len(f.blockedSince(t, seq, "sso_into_superadmin_refused")); n != 1 {
			t.Fatalf("scope %q: %d superadmin-guard audit records, want 1 (the guard itself refused)", scope, n)
		}
	}
	if f.sessions(t, root.ID) != before {
		t.Fatal("a refused superadmin sign-in left a session")
	}
	if u, _ := f.account(t, "root@example.com"); u.SsoSubject != "" {
		t.Fatalf("a refused superadmin sign-in linked the account: %q", u.SsoSubject)
	}
}
