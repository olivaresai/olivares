// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// R5 caller slice (login_capability.go). The state and predicate tests are pure. The
// store-backed tests drive the real auth seams over the real SQLite store and its login
// capability port (migration 13). Component states are declared the way boot declares
// them: wire the actual policy, ClassifyLoginComponent, then InstallLoginComponentState.

const (
	loginCapabilityTestArtifact = "r5-test-artifact"
	r5Email                     = "root@x.io"
	r5Password                  = "bootstrap-pass-123"
	r5IP                        = "198.51.100.4"
)

func TestLoginComponentState_ClassifyAndCheck(t *testing.T) {
	// Host break-glass has precedence over the linked-component fact
	// (ROOT-R5-BOOT-CLARIFICATION-1), so recovery survives an artifact change.
	for _, tc := range []struct {
		linked, disabled bool
		want             auth.LoginComponentState
	}{
		{linked: false, disabled: false, want: auth.LoginComponentAbsent},
		{linked: false, disabled: true, want: auth.LoginComponentDisabledByOperator},
		{linked: true, disabled: true, want: auth.LoginComponentDisabledByOperator},
		{linked: true, disabled: false, want: auth.LoginComponentWired},
	} {
		if got := auth.ClassifyLoginComponent(tc.linked, tc.disabled); got != tc.want {
			t.Errorf("ClassifyLoginComponent(linked=%t, disabled=%t) = %s, want %s", tc.linked, tc.disabled, got, tc.want)
		}
	}
	for _, tc := range []struct {
		state    auth.LoginComponentState
		enforces bool
		ok       bool
	}{
		{auth.LoginComponentUnset, false, false},
		{auth.LoginComponentUnset, true, false},
		{auth.LoginComponentWired, true, true},
		{auth.LoginComponentWired, false, false},
		{auth.LoginComponentAbsent, false, true},
		{auth.LoginComponentAbsent, true, false},
		{auth.LoginComponentDisabledByOperator, false, true},
		{auth.LoginComponentDisabledByOperator, true, false},
		{auth.LoginComponentState(99), false, false},
	} {
		err := auth.CheckLoginComponentState(tc.state, tc.enforces)
		if tc.ok && err != nil {
			t.Errorf("CheckLoginComponentState(%s, %t) = %v, want nil", tc.state, tc.enforces, err)
		}
		if !tc.ok && !errors.Is(err, auth.ErrLoginComponentState) {
			t.Errorf("CheckLoginComponentState(%s, %t) = %v, want ErrLoginComponentState", tc.state, tc.enforces, err)
		}
	}
}

func TestLoginComponentState_InstallAssertsBeforeInstalling(t *testing.T) {
	a := auth.NewAuthenticator(nil, nil)
	fed := auth.NewFederationService(nil, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
	if a.LoginComponentState() != auth.LoginComponentUnset || fed.LoginComponentState() != auth.LoginComponentUnset {
		t.Fatalf("constructors must start Unset, got %s and %s", a.LoginComponentState(), fed.LoginComponentState())
	}
	if err := a.AssertLoginComponentState(); !errors.Is(err, auth.ErrLoginComponentState) {
		t.Fatalf("asserting an Unset state = %v, want ErrLoginComponentState", err)
	}

	// Wired without a wired policy disagrees and installs nothing on either service.
	if err := auth.InstallLoginComponentState(auth.ClassifyLoginComponent(true, false), a, fed); !errors.Is(err, auth.ErrLoginComponentState) {
		t.Fatalf("installing Wired without a policy = %v, want ErrLoginComponentState", err)
	}
	if err := auth.InstallLoginComponentState(auth.LoginComponentAbsent, a, nil); !errors.Is(err, auth.ErrLoginComponentState) {
		t.Fatalf("installing without the federation service = %v, want ErrLoginComponentState", err)
	}
	if a.LoginComponentState() != auth.LoginComponentUnset || fed.LoginComponentState() != auth.LoginComponentUnset {
		t.Fatalf("a refused install changed the state to %s and %s", a.LoginComponentState(), fed.LoginComponentState())
	}

	// Break-glass on an artifact without the component installs with no policy.
	if err := auth.InstallLoginComponentState(auth.ClassifyLoginComponent(false, true), a, fed); err != nil {
		t.Fatalf("installing break-glass without the component: %v", err)
	}
	if a.LoginComponentState() != auth.LoginComponentDisabledByOperator || fed.LoginComponentState() != auth.LoginComponentDisabledByOperator {
		t.Fatalf("installed states = %s and %s, want disabled_by_operator on both", a.LoginComponentState(), fed.LoginComponentState())
	}
	if err := auth.InstallLoginComponentState(auth.ClassifyLoginComponent(false, false), a, fed); err != nil {
		t.Fatalf("installing Absent without a policy: %v", err)
	}
	if a.LoginComponentState() != auth.LoginComponentAbsent || fed.LoginComponentState() != auth.LoginComponentAbsent {
		t.Fatalf("installed states = %s and %s, want absent on both", a.LoginComponentState(), fed.LoginComponentState())
	}
	if err := a.AssertLoginComponentState(); err != nil {
		t.Fatalf("asserting the installed Absent state: %v", err)
	}

	// WithLoginPolicy keeps its signature and does not change the declared state, so the
	// assertion now reports the disagreement.
	a.WithLoginPolicy(&fakeLoginPolicy{})
	if err := a.AssertLoginComponentState(); !errors.Is(err, auth.ErrLoginComponentState) {
		t.Fatalf("Absent with a wired policy = %v, want ErrLoginComponentState", err)
	}
	if err := auth.InstallLoginComponentState(auth.ClassifyLoginComponent(true, false), a, fed); err != nil {
		t.Fatalf("installing Wired with a policy: %v", err)
	}
	if a.LoginComponentState() != auth.LoginComponentWired || fed.LoginComponentState() != auth.LoginComponentWired {
		t.Fatalf("installed states = %s and %s, want wired on both", a.LoginComponentState(), fed.LoginComponentState())
	}
	// Break-glass requires a nil actual policy, even when the component is linked.
	if err := auth.InstallLoginComponentState(auth.ClassifyLoginComponent(true, true), a, fed); !errors.Is(err, auth.ErrLoginComponentState) {
		t.Fatalf("installing break-glass over a wired policy = %v, want ErrLoginComponentState", err)
	}
	if a.LoginComponentState() != auth.LoginComponentWired {
		t.Fatalf("a refused break-glass install changed the state to %s", a.LoginComponentState())
	}
}

func TestLoginComponentState_PostureConfiguredPredicateIsShared(t *testing.T) {
	for _, p := range []auth.LoginEnforcementPosture{
		{},
		{HasActiveIdP: true},
		{RequireSSO: true},
		{NetworkAllowCIDRs: []string{}},
		{NetworkAllowCIDRs: []string{"10.0.0.0/8"}},
	} {
		want := p.RequireSSO || len(p.NetworkAllowCIDRs) > 0
		if got := auth.LoginPostureConfigured(p.RequireSSO, p.NetworkAllowCIDRs); got != want {
			t.Errorf("LoginPostureConfigured(%+v) = %t, want %t", p, got, want)
		}
		if got := p.Configured(); got != want {
			t.Errorf("(%+v).Configured() = %t, want %t", p, got, want)
		}
	}
}

func TestLoginComponentAbsent_PasswordLogin(t *testing.T) {
	for _, tc := range []struct {
		name             string
		install          bool
		linked, disabled bool
		history, posture bool
		policy           *fakeLoginPolicy
		wantState        auth.LoginComponentState
		wantErr          error
	}{
		{name: "absent with history and posture refuses", install: true, history: true, posture: true, wantState: auth.LoginComponentAbsent, wantErr: auth.ErrLoginEnforcementComponentAbsent},
		{name: "absent staging without history", install: true, posture: true, wantState: auth.LoginComponentAbsent},
		{name: "absent with history and empty posture", install: true, history: true, wantState: auth.LoginComponentAbsent},
		{name: "break-glass without the component", install: true, disabled: true, history: true, posture: true, wantState: auth.LoginComponentDisabledByOperator},
		{name: "break-glass with the component", install: true, linked: true, disabled: true, history: true, posture: true, wantState: auth.LoginComponentDisabledByOperator},
		{name: "unset library constructor", history: true, posture: true, wantState: auth.LoginComponentUnset},
		{name: "wired policy refusal", install: true, linked: true, history: true, posture: true, policy: &fakeLoginPolicy{ssoErr: auth.ErrSSORequired}, wantState: auth.LoginComponentWired, wantErr: auth.ErrSSORequired},
		{name: "wired policy permit", install: true, linked: true, history: true, posture: true, policy: &fakeLoginPolicy{}, wantState: auth.LoginComponentWired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			a := auth.NewAuthenticator(st, nil)
			user, err := a.BootstrapSuperadmin(ctx, r5Email, r5Password)
			if err != nil {
				t.Fatalf("bootstrap: %v", err)
			}
			fed := auth.NewFederationService(st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
			if tc.posture {
				configureGlobalLoginPosture(t, st)
			}
			if tc.history {
				recordLoginCapability(t, st)
			}
			if tc.install {
				var policy auth.LoginPolicy
				if tc.policy != nil {
					policy = tc.policy
				}
				installClassified(t, a, fed, tc.linked, tc.disabled, policy)
			}
			if got := a.LoginComponentState(); got != tc.wantState {
				t.Fatalf("declared state = %s, want %s", got, tc.wantState)
			}

			before := authSessionCount(t, st, user.ID)
			tok, _, err := a.Login(ctx, r5Email, r5Password, r5IP)
			after := authSessionCount(t, st, user.ID)
			if tc.policy != nil && tc.policy.sawSSO != 1 {
				t.Errorf("the wired policy was consulted %d times, want 1", tc.policy.sawSSO)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("login = %v, want %v", err, tc.wantErr)
				}
				if after != before {
					t.Fatalf("a refused login changed the session count %d -> %d", before, after)
				}
				return
			}
			if err != nil || tok == "" {
				t.Fatalf("login = %v (token empty %t), want a session", err, tok == "")
			}
			if after != before+1 {
				t.Fatalf("session count %d -> %d, want one new session", before, after)
			}
		})
	}
}

func TestLoginComponentAbsent_SSOCompletionRefusedAfterJIT(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	fed := auth.NewFederationService(st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
	configureGlobalLoginPosture(t, st)
	recordLoginCapability(t, st)
	installClassified(t, a, fed, false, false, nil)

	id := auth.FederatedIdentity{Email: "sso@x.io", Subject: "ext-1"}
	_, _, err := a.CompleteSSO(ctx, id, r5IP, "", false)
	if !errors.Is(err, auth.ErrLoginEnforcementComponentAbsent) {
		t.Fatalf("SSO completion = %v, want ErrLoginEnforcementComponentAbsent", err)
	}
	// Documented residual: JIT provisioning commits in its own earlier transaction, so
	// the user exists. The session is still refused.
	user, found := userByEmail(t, st, "sso@x.io")
	if !found {
		t.Fatal("the JIT-provisioned user is missing; the documented residual changed")
	}
	if n := authSessionCount(t, st, user.ID); n != 0 {
		t.Fatalf("a refused SSO completion left %d sessions, want 0", n)
	}

	// Host break-glass on the same artifact without the component completes the login.
	installClassified(t, a, fed, false, true, nil)
	tok, sess, err := a.CompleteSSO(ctx, id, r5IP, "", false)
	if err != nil || tok == "" || sess.UserID != user.ID {
		t.Fatalf("SSO completion under break-glass = %v (token empty %t, session user %s), want the user's session", err, tok == "", sess.UserID)
	}
	if n := authSessionCount(t, st, user.ID); n != 1 {
		t.Fatalf("break-glass SSO completion left %d sessions, want 1", n)
	}
}

func TestLoginComponentAbsent_AcceptInviteRefusedBeforeAnyWrite(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprintf("break-glass linked=%t", linked), func(t *testing.T) {
			ctx := context.Background()
			f := newInviteExpiryFixture(t)
			fed := auth.NewFederationService(f.st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
			configureGlobalLoginPosture(t, f.st)
			recordLoginCapability(t, f.st)
			installClassified(t, f.a, fed, false, false, nil)

			before := f.state(t)
			if _, _, err := f.a.AcceptInvite(ctx, f.token, inviteExpiryPassword, inviteExpiryIP); !errors.Is(err, auth.ErrLoginEnforcementComponentAbsent) {
				t.Fatalf("AcceptInvite = %v, want ErrLoginEnforcementComponentAbsent", err)
			}
			assertInviteStateUnchanged(t, before, f.state(t))

			// Host break-glass on the same database accepts the same invitation.
			installClassified(t, f.a, fed, linked, true, nil)
			tok, sess, err := f.a.AcceptInvite(ctx, f.token, inviteExpiryPassword, inviteExpiryIP)
			if err != nil || tok == "" || sess.UserID != f.userID {
				t.Fatalf("AcceptInvite under break-glass = %v (token empty %t, session user %s), want the invitee's session", err, tok == "", sess.UserID)
			}
		})
	}
}

func TestLoginComponentAbsent_ExistingSessionsKeepRefreshAndRevoke(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	fed := auth.NewFederationService(st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
	if _, err := a.BootstrapSuperadmin(ctx, r5Email, r5Password); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	tok, sess, err := a.Login(ctx, r5Email, r5Password, r5IP)
	if err != nil {
		t.Fatalf("login before the posture: %v", err)
	}
	configureGlobalLoginPosture(t, st)
	recordLoginCapability(t, st)
	installClassified(t, a, fed, false, false, nil)

	if _, _, err := a.Login(ctx, r5Email, r5Password, r5IP); !errors.Is(err, auth.ErrLoginEnforcementComponentAbsent) {
		t.Fatalf("new login = %v, want ErrLoginEnforcementComponentAbsent", err)
	}
	p, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("authenticate the existing session: %v", err)
	}
	refreshed, _, err := a.RefreshSession(ctx, p)
	if err != nil {
		t.Fatalf("refresh the existing session: %v", err)
	}
	p, err = a.Authenticate(ctx, refreshed)
	if err != nil {
		t.Fatalf("authenticate the refreshed session: %v", err)
	}
	elevated, err := a.ElevateSession(ctx, p, "webauthn", auth.AAL3)
	if err != nil || elevated.ID != sess.ID || elevated.AAL != auth.AAL3 {
		t.Fatalf("elevate the existing session = %v (session %s AAL %d), want session %s at AAL3", err, elevated.ID, elevated.AAL, sess.ID)
	}
	if err := a.RevokeSession(ctx, p, sess.ID); err != nil {
		t.Fatalf("revoke the existing session: %v", err)
	}
	if _, err := a.Authenticate(ctx, refreshed); err == nil {
		t.Fatal("the revoked session still authenticates")
	}
}

func TestLoginComponentAbsent_GlobalPostureWrites(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	newFed := func() *auth.FederationService {
		return auth.NewFederationService(st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
	}
	absent := newFed()
	installClassified(t, auth.NewAuthenticator(st, nil), absent, false, false, nil)

	reduced := oidcInput("https://idp.example", true)
	requireSSO := oidcInput("https://idp.example", true)
	requireSSO.RequireSSO = true
	allowList := oidcInput("https://idp.example", true)
	allowList.NetworkAllowCIDRs = []string{"10.0.0.0/8"}

	// Staging with no recorded history: an absent component may configure the posture.
	mustPut(t, absent, auth.GlobalFederationScope, requireSSO)
	first := recordLoginCapability(t, st)

	// Demand reduction is allowed.
	mustPut(t, absent, auth.GlobalFederationScope, reduced)
	assertGlobalPostureConfigured(t, absent, false)

	for name, in := range map[string]auth.FederationConfigInput{"require-SSO": requireSSO, "allow-list": allowList} {
		if _, err := absent.PutConfig(ctx, fedTestActor(), auth.GlobalFederationScope, in); !errors.Is(err, auth.ErrLoginEnforcementComponentAbsent) {
			t.Fatalf("absent %s write = %v, want ErrLoginEnforcementComponentAbsent", name, err)
		}
		assertGlobalPostureConfigured(t, absent, false)
	}

	// Host break-glass on an artifact without the component may configure over history.
	breakGlass := newFed()
	installClassified(t, auth.NewAuthenticator(st, nil), breakGlass, false, true, nil)
	mustPut(t, breakGlass, auth.GlobalFederationScope, requireSSO)
	assertGlobalPostureConfigured(t, absent, true)
	mustPut(t, absent, auth.GlobalFederationScope, reduced)
	assertGlobalPostureConfigured(t, absent, false)

	// A wired writer on the same database keeps its behavior.
	wired := newFed()
	installClassified(t, auth.NewAuthenticator(st, nil), wired, true, false, &fakeLoginPolicy{})
	mustPut(t, wired, auth.GlobalFederationScope, requireSSO)
	assertGlobalPostureConfigured(t, absent, true)

	// Deletion reduces demand under the absent component and keeps the history.
	if err := absent.DeleteConfigIdP(ctx, fedTestActor(), auth.GlobalFederationScope, model.DefaultFederationAlias); err != nil {
		t.Fatalf("absent delete: %v", err)
	}
	assertGlobalPostureConfigured(t, absent, false)
	after := readLoginCapability(t, st)
	if !after.Present || !after.FirstObservedAt.Equal(first.FirstObservedAt) || after.ObservationCount != first.ObservationCount {
		t.Fatalf("capability after config writes = %+v, want the recorded %+v unchanged", after, first)
	}
}

// installClassified declares the state the way boot does: wire the actual policy (nil
// unless the component is linked and not disabled), classify, then install and assert.
func installClassified(t *testing.T, a *auth.Authenticator, fed *auth.FederationService, linked, disabled bool, policy auth.LoginPolicy) auth.LoginComponentState {
	t.Helper()
	a.WithLoginPolicy(policy)
	state := auth.ClassifyLoginComponent(linked, disabled)
	if err := auth.InstallLoginComponentState(state, a, fed); err != nil {
		t.Fatalf("install ClassifyLoginComponent(linked=%t, disabled=%t) = %s: %v", linked, disabled, state, err)
	}
	return state
}

// configureGlobalLoginPosture stores an active global/default IdP with require-SSO.
func configureGlobalLoginPosture(t *testing.T, st store.Store) {
	t.Helper()
	svc := auth.NewFederationService(st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil)
	in := oidcInput("https://idp.example", true)
	in.RequireSSO = true
	mustPut(t, svc, auth.GlobalFederationScope, in)
}

func recordLoginCapability(t *testing.T, st store.Store) store.LoginCapabilityObservation {
	t.Helper()
	ctx := context.Background()
	var obs store.LoginCapabilityObservation
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, err := as.LoginCapability().Lock(ctx); err != nil {
			return err
		}
		o, err := as.LoginCapability().Observe(ctx, loginCapabilityTestArtifact)
		obs = o
		return err
	}); err != nil {
		t.Fatalf("record the login capability: %v", err)
	}
	if !obs.Present {
		t.Fatalf("recorded capability = %+v, want present", obs)
	}
	return obs
}

func readLoginCapability(t *testing.T, st store.Store) store.LoginCapabilityObservation {
	t.Helper()
	ctx := context.Background()
	var obs store.LoginCapabilityObservation
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		o, err := as.LoginCapability().Read(ctx)
		obs = o
		return err
	}); err != nil {
		t.Fatalf("read the login capability: %v", err)
	}
	return obs
}

func assertGlobalPostureConfigured(t *testing.T, svc *auth.FederationService, want bool) {
	t.Helper()
	p, err := svc.Posture(context.Background())
	if err != nil {
		t.Fatalf("posture: %v", err)
	}
	if p.Configured() != want {
		t.Fatalf("global posture %+v configured = %t, want %t", p, p.Configured(), want)
	}
}

func authSessionCount(t *testing.T, st store.Store, userID model.ID) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Sessions().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: userID.String()}},
			Limit:   100,
		})
		n = len(rows)
		return err
	}); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return n
}

func userByEmail(t *testing.T, st store.Store, email string) (model.User, bool) {
	t.Helper()
	ctx := context.Background()
	var (
		user  model.User
		found bool
	)
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Users().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "email", Op: model.OpEq, Value: email}},
			Limit:   2,
		})
		if err != nil {
			return err
		}
		if len(rows) == 1 {
			user, found = rows[0], true
		}
		return nil
	}); err != nil {
		t.Fatalf("find user by email: %v", err)
	}
	return user, found
}
