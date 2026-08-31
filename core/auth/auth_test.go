// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func testStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Debug: true,
	}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func provisionTenant(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	var id model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		o, err := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		id = o.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision %q: %v", slug, err)
	}
	return id
}

func TestPasswordHashRoundTrip(t *testing.T) {
	h, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("hash format = %q", h)
	}
	ok, err := auth.VerifyPassword("correct horse battery staple", h)
	if err != nil || !ok {
		t.Fatalf("verify correct = (%v,%v)", ok, err)
	}
	ok, _ = auth.VerifyPassword("wrong", h)
	if ok {
		t.Fatal("verify wrong password returned true")
	}
	if _, err := auth.VerifyPassword("x", "not-a-hash"); !errors.Is(err, auth.ErrMalformedHash) {
		t.Fatalf("malformed err = %v", err)
	}
}

func TestCredentialFormat(t *testing.T) {
	c, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatal(err)
	}
	prefix, selector, secret, ok := auth.ParseToken(c.Token)
	if !ok || prefix != auth.PrefixToken || selector != c.Selector {
		t.Fatalf("parse = (%q,%q,%v)", prefix, selector, ok)
	}
	if !auth.SecretMatches(secret, c.SecretHash) {
		t.Fatal("secret does not match stored hash")
	}
	if auth.SecretMatches("tampered", c.SecretHash) {
		t.Fatal("tampered secret matched")
	}
	if _, _, _, ok := auth.ParseToken("garbage"); ok {
		t.Fatal("malformed token parsed ok")
	}
}

func TestOrdinaryTokenCannotCarrySessionIdentity(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st, "session-bound-token")
	cred, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatalf("mint credential: %v", err)
	}
	wantSID := "osn_" + model.NewID().String()
	wantAgent := "agent:" + model.NewID().String()
	var tokenID model.ID
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		stored, err := as.Tokens().Create(ctx, model.APIToken{
			Name: "runtime-session", Selector: cred.Selector, SecretHash: cred.SecretHash,
			BoundTenantID: tenant, Role: auth.RoleEditor,
			AgentRef: wantAgent, SessionRef: wantSID,
		})
		tokenID = stored.ID
		return err
	}); err != nil {
		t.Fatalf("store credential: %v", err)
	}

	a := auth.NewAuthenticator(st, nil)
	if _, err := a.Authenticate(ctx, cred.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("ordinary token with session_ref authenticated: %v", err)
	}
	if _, found, err := a.PrincipalForToken(ctx, tokenID); err != nil || found {
		t.Fatalf("PrincipalForToken = found %v, err %v; want malformed row hidden", found, err)
	}
}

func TestRoleGrants(t *testing.T) {
	cases := []struct {
		role string
		perm auth.Permission
		want bool
	}{
		{auth.RoleViewer, "agent:read", true},
		{auth.RoleViewer, "agent:write", false},
		{auth.RoleViewer, "user:read", false}, // viewers cannot enumerate accounts
		{auth.RoleEditor, "agent:write", true},
		{auth.RoleEditor, "user:write", false},
		{auth.RoleAdmin, "user:write", true},
		{auth.RoleAdmin, "token:write", true},
		{auth.RoleOwner, "agent:admin", true},
		{auth.RoleViewer, auth.PermSystemAdmin, false},
		{auth.RoleOwner, auth.PermSystemAdmin, false}, // only the superadmin flag holds it
		// Access-graph reads are PRIVILEGED (docs/SECURITY-HARDENING.md): viewer is denied, editor+
		// allowed — both the core surface and the access-map module surface, so the
		// recon map is never readable by the lowest role.
		{auth.RoleViewer, "accessgraph:read", false},
		{auth.RoleEditor, "accessgraph:read", true},
		{auth.RoleAdmin, "accessgraph:read", true},
		{auth.RoleOwner, "accessgraph:read", true},
		{auth.RoleViewer, "accessmap:graph:read", false},
		{auth.RoleViewer, "accessmap:drift:read", false},
		{auth.RoleEditor, "accessmap:graph:read", true},
		{auth.RoleEditor, "accessmap:drift:read", true},
		// other module permissions are still granted by the generic verb tier
		{auth.RoleViewer, "rrw:access_path:read", true},
		{auth.RoleViewer, "rrw:access_path:write", false},
		{auth.RoleEditor, "rrw:access_path:write", true},
		{auth.RoleAdmin, "rrw:access_path:admin", true},
	}
	for _, c := range cases {
		if got := auth.RoleGrants(c.role, c.perm); got != c.want {
			t.Errorf("RoleGrants(%q,%q) = %v, want %v", c.role, c.perm, got, c.want)
		}
	}
}

// denyEvaluator denies, errors or panics depending on mode, to test the ABAC seam.
type denyEvaluator struct{ mode string }

func (d denyEvaluator) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	switch d.mode {
	case "deny":
		return auth.Decision{Allow: false, Reason: "blocked"}, nil
	case "error":
		return auth.Decision{}, errors.New("boom")
	case "panic":
		panic("evaluator exploded")
	}
	return auth.Decision{Allow: true}, nil
}

func TestAuthorizer(t *testing.T) {
	ctx := context.Background()
	tenant := model.NewTenantID()
	other := model.NewTenantID()

	// Build principals via the package by issuing/authenticating below would be
	// heavy; instead exercise Authorize through a real Authenticator-derived
	// principal in TestAuthenticatorLoginAndAuthorize. Here, test RBAC + ABAC with
	// the exported Authorize using a token principal we construct through the store.
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)

	// Issue a viewer-bound token and authenticate it to get a real Principal.
	sysActor := auth.Principal{} // bootstrap uses system actor internally
	_ = sysActor
	tok, _, err := a.IssueToken(ctx, mustSuperadmin(t, ctx, a), auth.TokenSpec{
		Name: "viewer", BoundTenant: tenant, Role: auth.RoleViewer,
	})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	p, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("authenticate token: %v", err)
	}

	rbac := auth.NewAuthorizer(nil)
	if !rbac.Allowed(ctx, p, "agent:read", tenant) {
		t.Error("viewer should read agents in its tenant")
	}
	if rbac.Allowed(ctx, p, "agent:write", tenant) {
		t.Error("viewer should not write agents")
	}
	if rbac.Allowed(ctx, p, "agent:read", other) {
		t.Error("viewer must be denied in a tenant it is not a member of")
	}

	// ABAC may only further restrict.
	if auth.NewAuthorizer(denyEvaluator{"deny"}).Allowed(ctx, p, "agent:read", tenant) {
		t.Error("ABAC deny must override an RBAC allow")
	}
	if auth.NewAuthorizer(denyEvaluator{"error"}).Allowed(ctx, p, "agent:read", tenant) {
		t.Error("ABAC error must fail closed (deny)")
	}
	if auth.NewAuthorizer(denyEvaluator{"panic"}).Allowed(ctx, p, "agent:read", tenant) {
		t.Error("ABAC panic must fail closed (deny)")
	}
}

// mustSuperadmin bootstraps a superadmin and returns a Principal for it.
func mustSuperadmin(t *testing.T, ctx context.Context, a *auth.Authenticator) auth.Principal {
	t.Helper()
	if _, err := a.BootstrapSuperadmin(ctx, "root@example.com", "bootstrap-pass-123"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	tok, _, err := a.Login(ctx, "root@example.com", "bootstrap-pass-123", "127.0.0.1")
	if err != nil {
		t.Fatalf("login bootstrap: %v", err)
	}
	p, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("authenticate bootstrap: %v", err)
	}
	if !p.Superadmin {
		t.Fatal("bootstrap principal is not superadmin")
	}
	return p
}

func TestBootstrapIsOneShot(t *testing.T) {
	ctx := context.Background()
	a := auth.NewAuthenticator(testStore(t), nil)
	if _, err := a.BootstrapSuperadmin(ctx, "a@b.c", "first-password-1"); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	if _, err := a.BootstrapSuperadmin(ctx, "c@d.e", "second-password-1"); !errors.Is(err, auth.ErrSetupComplete) {
		t.Fatalf("second bootstrap err = %v, want ErrSetupComplete", err)
	}
}

func TestAuthenticatorLoginAndAuthorize(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	// Create an editor user and grant it editor on the tenant.
	u, err := a.CreateUser(ctx, admin, auth.NewUser{Email: "Dev@Acme.com", DisplayName: "Dev", Password: "dev-password-1"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if u.Email != "dev@acme.com" {
		t.Fatalf("email not normalized: %q", u.Email)
	}
	if _, err := a.GrantMembership(ctx, admin, u.ID, tenant, auth.RoleEditor, model.ID("")); err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Login with mixed-case email works (normalized).
	tok, _, err := a.Login(ctx, "DEV@acme.com", "dev-password-1", "10.0.0.1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	p, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if p.Kind != auth.KindUser || p.Superadmin {
		t.Fatalf("principal = %+v", p)
	}
	az := auth.NewAuthorizer(nil)
	if !az.Allowed(ctx, p, "agent:write", tenant) {
		t.Error("editor should write agents")
	}
	if az.Allowed(ctx, p, "user:write", tenant) {
		t.Error("editor must not manage users")
	}
}

func TestLoginWrongPasswordAndLockout(t *testing.T) {
	ctx := context.Background()
	a := auth.NewAuthenticator(testStore(t), nil)
	if _, err := a.BootstrapSuperadmin(ctx, "root@x.io", "the-real-password"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, _, err := a.Login(ctx, "root@x.io", "wrong", "9.9.9.9"); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("attempt %d err = %v, want ErrInvalidCredentials", i, err)
		}
	}
	// 6th attempt (even with the correct password) is locked out.
	if _, _, err := a.Login(ctx, "root@x.io", "the-real-password", "9.9.9.9"); !errors.Is(err, auth.ErrLockedOut) {
		t.Fatalf("post-lockout err = %v, want ErrLockedOut", err)
	}
	// An unknown email also returns invalid-credentials (no enumeration).
	if _, _, err := a.Login(ctx, "ghost@x.io", "whatever", "8.8.8.8"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("unknown user err = %v, want ErrInvalidCredentials", err)
	}
}

func TestRoleCeiling(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	// A tenant admin.
	adminUser, err := a.CreateUser(ctx, super, auth.NewUser{Email: "adm@a.com", Password: "adminpass1"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.CreateUser(ctx, super, auth.NewUser{Email: "tgt@a.com", Password: "targetpass1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.GrantMembership(ctx, super, adminUser.ID, tenant, auth.RoleAdmin, model.ID("")); err != nil {
		t.Fatal(err)
	}
	tok, _, _ := a.Login(ctx, "adm@a.com", "adminpass1", "1.1.1.1")
	admin, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}

	// An admin may grant editor (below its rank) but NOT owner (above its rank).
	if _, err := a.GrantMembership(ctx, admin, target.ID, tenant, auth.RoleEditor, model.ID("")); err != nil {
		t.Fatalf("admin granting editor: %v", err)
	}
	if _, err := a.GrantMembership(ctx, admin, target.ID, tenant, auth.RoleOwner, model.ID("")); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Fatalf("admin granting owner err = %v, want ErrRoleCeiling", err)
	}
	// An admin may not mint an owner-role or superadmin token.
	if _, _, err := a.IssueToken(ctx, admin, auth.TokenSpec{Name: "x", BoundTenant: tenant, Role: auth.RoleOwner}); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Fatalf("admin minting owner token err = %v, want ErrRoleCeiling", err)
	}
	if _, _, err := a.IssueToken(ctx, admin, auth.TokenSpec{Name: "x", Superadmin: true}); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Fatalf("admin minting superadmin token err = %v, want ErrRoleCeiling", err)
	}
	// A superadmin is exempt; granting the system tenant is rejected outright.
	if _, err := a.GrantMembership(ctx, super, target.ID, tenant, auth.RoleOwner, model.ID("")); err != nil {
		t.Fatalf("superadmin granting owner: %v", err)
	}
	if _, err := a.GrantMembership(ctx, super, target.ID, model.SystemTenantID, auth.RoleAdmin, model.ID("")); err == nil {
		t.Fatal("granting a membership in the system tenant must be rejected")
	}
}

func TestRevokedAndExpiredCredentials(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme")

	// Revoked token fails.
	tok, stored, err := a.IssueToken(ctx, admin, auth.TokenSpec{Name: "ci", BoundTenant: tenant, Role: auth.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, tok); err != nil {
		t.Fatalf("token should authenticate: %v", err)
	}
	if err := a.RevokeToken(ctx, admin, stored.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, tok); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked token err = %v, want ErrUnauthenticated", err)
	}

	// Superadmin token is unbound and authorizes everywhere.
	stok, _, err := a.IssueToken(ctx, admin, auth.TokenSpec{Name: "root-token", Superadmin: true})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := a.Authenticate(ctx, stok)
	if err != nil {
		t.Fatal(err)
	}
	if !sp.Superadmin || !auth.NewAuthorizer(nil).Allowed(ctx, sp, "agent:write", model.NewTenantID()) {
		t.Fatal("superadmin token should authorize in any tenant")
	}
}
