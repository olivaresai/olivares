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

// The login-enforcement seam (login_policy.go): the open build wires NO policy,
// so Login/CompleteSSO behave exactly as today (proven nil-safe here); the enterprise
// engine (enterprise/ssoenforce, tested under -tags enterprise) implements the real
// require-SSO + CIDR decisions. Here a test double stands in for it to prove the
// Authenticator HONORS the capability at the two decision points and — critically —
// that require-SSO is consulted ONLY on the password path, never on SSO completion.

// fakeLoginPolicy is a controllable auth.LoginPolicy: AllowNetwork returns networkErr,
// RequireSSO returns ssoErr, and both record how many times they were consulted (so a
// test can prove require-SSO is never reached on the SSO path).
type fakeLoginPolicy struct {
	networkErr error
	ssoErr     error
	sawNetwork int
	sawSSO     int
}

func (f *fakeLoginPolicy) AllowNetwork(context.Context, string) error {
	f.sawNetwork++
	return f.networkErr
}

func (f *fakeLoginPolicy) RequireSSO(context.Context, model.User) error {
	f.sawSSO++
	return f.ssoErr
}

// compile-time proof the double satisfies the seam (the real engine does the same).
var _ auth.LoginPolicy = (*fakeLoginPolicy)(nil)

func TestLoginPolicy_NilIsNoOp(t *testing.T) {
	ctx := context.Background()
	a := auth.NewAuthenticator(testStore(t), nil) // no login policy wired
	if a.EnforcesLogin() {
		t.Fatal("a nil login policy must report EnforcesLogin()=false")
	}
	if _, err := a.BootstrapSuperadmin(ctx, "root@x.io", "bootstrap-pass-123"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// Login behaves exactly as before the seam: no enforcement, password login works.
	if _, _, err := a.Login(ctx, "root@x.io", "bootstrap-pass-123", "9.9.9.9"); err != nil {
		t.Fatalf("login under a nil policy must succeed unchanged: %v", err)
	}
}

func TestLoginPolicy_EnforcesLoginReflectsWiring(t *testing.T) {
	a := auth.NewAuthenticator(testStore(t), nil)
	if a.EnforcesLogin() {
		t.Fatal("nil policy: EnforcesLogin must be false (enforced_by=unavailable)")
	}
	a.WithLoginPolicy(&fakeLoginPolicy{})
	if !a.EnforcesLogin() {
		t.Fatal("wired policy: EnforcesLogin must be true (enforced_by=enterprise)")
	}
}

func TestLoginPolicy_NetworkBlocksPasswordLogin(t *testing.T) {
	ctx := context.Background()
	a := auth.NewAuthenticator(testStore(t), nil)
	if _, err := a.BootstrapSuperadmin(ctx, "root@x.io", "bootstrap-pass-123"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// Wire the policy AFTER bootstrap so the account exists; now deny the network.
	f := &fakeLoginPolicy{networkErr: auth.ErrNetworkNotAllowed}
	a.WithLoginPolicy(f)

	_, _, err := a.Login(ctx, "root@x.io", "bootstrap-pass-123", "203.0.113.7")
	if !errors.Is(err, auth.ErrNetworkNotAllowed) {
		t.Fatalf("password login from a blocked network err = %v, want ErrNetworkNotAllowed", err)
	}
	if f.sawNetwork != 1 {
		t.Fatalf("AllowNetwork consulted %d times, want 1", f.sawNetwork)
	}
	// The network check is FIRST: a credential is never even verified, so require-SSO
	// is not consulted on a network-blocked attempt.
	if f.sawSSO != 0 {
		t.Fatalf("RequireSSO consulted %d times on a network-blocked login, want 0", f.sawSSO)
	}
}

func TestLoginPolicy_RequireSSOBlocksPasswordOnly(t *testing.T) {
	ctx := context.Background()
	a := auth.NewAuthenticator(testStore(t), nil)
	if _, err := a.BootstrapSuperadmin(ctx, "root@x.io", "bootstrap-pass-123"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// require-SSO blocks (network allows). A CORRECT password reaches the rule.
	f := &fakeLoginPolicy{ssoErr: auth.ErrSSORequired}
	a.WithLoginPolicy(f)

	_, _, err := a.Login(ctx, "root@x.io", "bootstrap-pass-123", "203.0.113.7")
	if !errors.Is(err, auth.ErrSSORequired) {
		t.Fatalf("password login under require-SSO err = %v, want ErrSSORequired", err)
	}
	if f.sawSSO != 1 {
		t.Fatalf("RequireSSO consulted %d times, want 1", f.sawSSO)
	}
}

func TestLoginPolicy_RequireSSOIsNoOracle(t *testing.T) {
	ctx := context.Background()
	a := auth.NewAuthenticator(testStore(t), nil)
	if _, err := a.BootstrapSuperadmin(ctx, "root@x.io", "bootstrap-pass-123"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	f := &fakeLoginPolicy{ssoErr: auth.ErrSSORequired}
	a.WithLoginPolicy(f)

	// A WRONG password returns ErrInvalidCredentials, NOT ErrSSORequired — require-SSO
	// is checked only AFTER the credential verifies, so it never tells an attacker
	// without the password that the account exists / requires SSO.
	_, _, err := a.Login(ctx, "root@x.io", "WRONG-password", "203.0.113.7")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("wrong password under require-SSO err = %v, want ErrInvalidCredentials (no oracle)", err)
	}
	if f.sawSSO != 0 {
		t.Fatalf("RequireSSO consulted %d times on a wrong password, want 0", f.sawSSO)
	}
}

func TestLoginPolicy_NetworkBlocksSSOCompletion(t *testing.T) {
	ctx := context.Background()
	a := auth.NewAuthenticator(testStore(t), nil)
	f := &fakeLoginPolicy{networkErr: auth.ErrNetworkNotAllowed}
	a.WithLoginPolicy(f)

	// The allow-list applies to EVERY login: an SSO completion from a blocked network
	// is refused BEFORE the local user is found/provisioned.
	_, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{Email: "sso@x.io", Subject: "ext-1"}, "203.0.113.7", "", false)
	if !errors.Is(err, auth.ErrNetworkNotAllowed) {
		t.Fatalf("SSO completion from a blocked network err = %v, want ErrNetworkNotAllowed", err)
	}
	if f.sawNetwork != 1 || f.sawSSO != 0 {
		t.Fatalf("AllowNetwork=%d (want 1), RequireSSO=%d (want 0) on SSO completion", f.sawNetwork, f.sawSSO)
	}
}

func TestLoginPolicy_RequireSSONeverBlocksSSOCompletion(t *testing.T) {
	ctx := context.Background()
	a := auth.NewAuthenticator(testStore(t), nil)
	// A policy that WOULD block on require-SSO, but allows the network. The SSO path
	// must succeed: require-SSO blocks password logins, and permitting SSO is its
	// entire point — CompleteSSO is never routed through RequireSSO.
	f := &fakeLoginPolicy{ssoErr: auth.ErrSSORequired}
	a.WithLoginPolicy(f)

	tok, _, err := a.CompleteSSO(ctx, auth.FederatedIdentity{Email: "sso@x.io", Subject: "ext-1"}, "203.0.113.7", "", false)
	if err != nil {
		t.Fatalf("SSO completion under require-SSO must succeed (the permitted path), got %v", err)
	}
	if tok == "" {
		t.Fatal("SSO completion returned an empty session token")
	}
	if f.sawSSO != 0 {
		t.Fatalf("RequireSSO was consulted %d times on the SSO path, want 0 (must never block SSO)", f.sawSSO)
	}
	if f.sawNetwork != 1 {
		t.Fatalf("AllowNetwork consulted %d times on SSO completion, want 1", f.sawNetwork)
	}
}

// --- the stored posture the enterprise engine reads (FederationService.Posture) -----

func TestFederationPosture_StoredNormalizedAndResolved(t *testing.T) {
	svc := auth.NewFederationService(testStore(t), fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
	ctx := context.Background()

	// No config row yet → the zero posture (no enforcement).
	if p, err := svc.Posture(ctx); err != nil || p.Configured() || p.HasActiveIdP {
		t.Fatalf("empty posture = %+v err=%v, want unconfigured/no-active-IdP", p, err)
	}

	// Store an ACTIVE OIDC config carrying the posture (CIDRs with stray whitespace).
	in := oidcInput("https://idp.example", true)
	in.RequireSSO = true
	in.NetworkAllowCIDRs = []string{"10.0.0.0/8", "  192.168.0.0/16  ", ""}
	mustPut(t, svc, auth.GlobalFederationScope, in)

	p, err := svc.Posture(ctx)
	if err != nil {
		t.Fatalf("posture: %v", err)
	}
	if !p.RequireSSO || !p.HasActiveIdP || !p.Configured() {
		t.Fatalf("posture = %+v, want require-SSO + active IdP + configured", p)
	}
	if want := []string{"10.0.0.0/8", "192.168.0.0/16"}; len(p.NetworkAllowCIDRs) != 2 ||
		p.NetworkAllowCIDRs[0] != want[0] || p.NetworkAllowCIDRs[1] != want[1] {
		t.Fatalf("CIDRs = %q, want normalized %q (trimmed, blanks dropped)", p.NetworkAllowCIDRs, want)
	}

	// The console read view carries the same posture for display.
	v, err := svc.GetConfig(ctx, auth.GlobalFederationScope)
	if err != nil || !v.RequireSSO || len(v.NetworkAllowCIDRs) != 2 {
		t.Fatalf("view posture = require-SSO:%v cidrs:%q err:%v, want require-SSO + 2 CIDRs", v.RequireSSO, v.NetworkAllowCIDRs, err)
	}
}

func TestFederationPosture_RequireSSOWithoutActiveIdP(t *testing.T) {
	svc := auth.NewFederationService(testStore(t), fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
	ctx := context.Background()

	// require-SSO stored on a DISABLED config: the intent persists, but HasActiveIdP is
	// false — the enterprise engine uses this to refuse to lock everyone out when SSO is
	// not actually available (anti-lockout).
	in := oidcInput("https://idp.example", false) // disabled
	in.RequireSSO = true
	mustPut(t, svc, auth.GlobalFederationScope, in)

	p, err := svc.Posture(ctx)
	if err != nil {
		t.Fatalf("posture: %v", err)
	}
	if !p.RequireSSO {
		t.Fatal("require-SSO intent must persist on a disabled config")
	}
	if p.HasActiveIdP {
		t.Fatal("HasActiveIdP must be false when the IdP is disabled (anti-lockout signal)")
	}
}

func TestFederationPosture_RejectsMalformedCIDR(t *testing.T) {
	svc := auth.NewFederationService(testStore(t), fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
	ctx, actor := context.Background(), fedTestActor()

	in := oidcInput("https://idp.example", true)
	in.NetworkAllowCIDRs = []string{"10.0.0.0/8", "not-a-cidr"}
	_, err := svc.PutConfig(ctx, actor, auth.GlobalFederationScope, in)
	if !errors.Is(err, auth.ErrBadFederationConfig) {
		t.Fatalf("PutConfig with a malformed CIDR err = %v, want ErrBadFederationConfig", err)
	}
	// And nothing was persisted (the write was refused before the store mutation).
	if p, _ := svc.Posture(ctx); p.Configured() {
		t.Fatalf("a rejected write must not persist a posture, got %+v", p)
	}
}
