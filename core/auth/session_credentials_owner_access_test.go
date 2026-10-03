// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type ownerAccessFixture struct {
	st              store.Store
	a               *auth.Authenticator
	admin, launcher auth.Principal
	user            model.User
	tenants         [2]model.TenantID
	scopes          [2]auth.SessionScope
	tokens          [2]string
	issuer          *auth.SessionCredentials
}

func newOwnerAccessFixture(t *testing.T, wrap func(store.Store) store.Store) ownerAccessFixture {
	t.Helper()
	ctx := t.Context()
	st := testStore(t)
	if wrap != nil {
		st = wrap(st)
	}
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenants := [2]model.TenantID{provisionTenant(t, st, "owner-access-one"), provisionTenant(t, st, "owner-access-two")}
	user, err := a.CreateUser(ctx, admin, auth.NewUser{Email: "owner-access@example.invalid", DisplayName: "Alex", Password: "owner-access-password", Tenant: tenants[0], Role: auth.RoleEditor})
	if err != nil {
		t.Fatal(err)
	}
	// Seed both tenant grants before login; authority under test is reconstructed
	// from the real auth store, never from a synthetic principal.
	if err = st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Memberships().Create(ctx, model.Membership{UserID: user.ID, TargetTenantID: tenants[1], Role: auth.RoleEditor})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	login, _, err := a.Login(ctx, user.Email, "owner-access-password", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := a.Authenticate(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	f := ownerAccessFixture{st: st, a: a, admin: admin, user: user, launcher: launcher, tenants: tenants, issuer: auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })}
	for i, tenant := range tenants {
		var workspace model.ID
		if err = st.View(ctx, tenant, func(sc store.Scope) error { w, err := sc.DefaultWorkspace(ctx); workspace = w.ID; return err }); err != nil {
			t.Fatal(err)
		}
		f.scopes[i] = auth.SessionScope{TenantID: tenant, WorkspaceID: workspace, FolderRef: "folder", SessionRef: "session", RunRef: "run", Holder: user.ID.String(), Fence: 1}
		f.tokens[i], err = f.issuer.Mint(ctx, launcher, f.scopes[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestSessionOwnerAccessCheckMissingBindingIsNotWithdrawal(t *testing.T) {
	f := newOwnerAccessFixture(t, nil)
	scope, user, err := f.issuer.CheckOwnerAccess(t.Context(), f.tenants[0], "ordinary-unbound-run")
	if !errors.Is(err, auth.ErrSessionOwnerUnbound) || !errors.Is(err, auth.ErrUnauthenticated) ||
		errors.Is(err, auth.ErrSessionAccessEnded) || scope.RunRef != "" || scope.TenantID != "" || scope.Fence != 0 || user != "" {
		t.Fatalf("missing owner binding must convey no authority or withdrawal: scope=%+v user=%q err=%v", scope, user, err)
	}
	if _, _, err = f.issuer.ResolveRun(t.Context(), f.tenants[0], "ordinary-unbound-run"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("unbound run obtained credential authority: %v", err)
	}
	if _, _, err = f.issuer.Resolve(t.Context(), f.tokens[0]); err != nil {
		t.Fatalf("checking an unbound run affected the bound run: %v", err)
	}
}

func TestSessionOwnerAccessCheckRetainsExpiredBindingAfterMintCleanup(t *testing.T) {
	f := newOwnerAccessFixture(t, nil)
	clock := &stepClock{t: time.Now()}
	a := auth.NewAuthenticator(f.st, clock)
	login, _, err := a.Login(t.Context(), f.user.Email, "owner-access-password", "long-lived launcher fixture")
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := a.Authenticate(t.Context(), login)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.AuthMutate(t.Context(), func(as store.AuthScope) error {
		row, err := as.Sessions().Get(t.Context(), launcher.CredID)
		if err != nil {
			return err
		}
		row.ExpiresAt = model.NewTimestamp(clock.Now().Time().Add(72 * time.Hour))
		_, err = as.Sessions().Update(t.Context(), row)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	launcher, err = a.Authenticate(t.Context(), login)
	if err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	expired, err := issuer.Mint(t.Context(), launcher, f.scopes[0])
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(25 * time.Hour)
	current, err := issuer.Mint(t.Context(), launcher, f.scopes[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = issuer.Resolve(t.Context(), expired); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expired bearer granted authority: %v", err)
	}
	if _, _, err = issuer.ResolveRun(t.Context(), f.tenants[0], "run"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("expired run granted in-process authority: %v", err)
	}
	if _, _, err = issuer.CheckOwnerAccess(t.Context(), f.tenants[0], "run"); err != nil {
		t.Fatalf("expired bearer cleanup lost the live owner: %v", err)
	}
	if err = f.st.AuthMutate(t.Context(), func(as store.AuthScope) error {
		_, err := f.a.OffboardFromTenant(t.Context(), as, f.admin, f.user.ID, f.tenants[0], "expired-owner-withdrawal")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope, _, err := issuer.CheckOwnerAccess(t.Context(), f.tenants[0], "run")
	if !errors.Is(err, auth.ErrSessionAccessEnded) || scope.TenantID != f.tenants[0] || scope.Fence != f.scopes[0].Fence {
		t.Fatalf("expired live generation lost withdrawal attribution: scope=%+v err=%v", scope, err)
	}
	if _, _, err = issuer.Resolve(t.Context(), current); err != nil {
		t.Fatalf("withdrawal affected another tenant's current bearer: %v", err)
	}
}

func TestSessionOwnerAccessCheckScopedWithdrawalKeepsOtherTenant(t *testing.T) {
	f := newOwnerAccessFixture(t, nil)
	if err := f.st.AuthMutate(t.Context(), func(as store.AuthScope) error {
		_, err := f.a.OffboardFromTenant(t.Context(), as, f.admin, f.user.ID, f.tenants[0], "owner-standing-test")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope, user, err := f.issuer.CheckOwnerAccess(t.Context(), f.tenants[0], "run")
	if !errors.Is(err, auth.ErrSessionAccessEnded) || scope.TenantID != f.tenants[0] || scope.Fence != 1 || user != "Alex" {
		t.Fatalf("ended standing scope=%v label=%q err=%v", scope.TenantID, user, err)
	}
	if _, _, err = f.issuer.Resolve(t.Context(), f.tokens[0]); err == nil {
		t.Fatal("withdrawn generation retained authority")
	}
	if _, _, err = f.issuer.CheckOwnerAccess(t.Context(), f.tenants[1], "run"); err != nil {
		t.Fatal("other tenant standing was withdrawn", err)
	}
	if _, _, err = f.issuer.Resolve(t.Context(), f.tokens[1]); err != nil {
		t.Fatal("other tenant bearer was revoked", err)
	}
}

func TestSessionOwnerAccessCheckFindsDisabledOwnerAfterBearerWasAlreadyRefused(t *testing.T) {
	f := newOwnerAccessFixture(t, nil)
	if err := f.st.AuthMutate(t.Context(), func(as store.AuthScope) error {
		u, err := as.Users().Get(t.Context(), f.user.ID)
		if err != nil {
			return err
		}
		u.Status = model.StatusInactive
		_, err = as.Users().Update(t.Context(), u)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.issuer.Resolve(t.Context(), f.tokens[0]); err == nil {
		t.Fatal("disabled owner retained native authority")
	}
	for i, tenant := range f.tenants {
		scope, _, err := f.issuer.CheckOwnerAccess(t.Context(), tenant, "run")
		if !errors.Is(err, auth.ErrSessionAccessEnded) || scope.TenantID != tenant {
			t.Fatalf("disabled owner in tenant%d err=%v", i, err)
		}
	}
}

type ownerAccessReadStore struct {
	store.Store
	paused           atomic.Bool
	fail             atomic.Bool
	entered, release chan struct{}
}

func (s *ownerAccessReadStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	if s.fail.Load() {
		return errors.New("owner standing unavailable")
	}
	err := s.Store.AuthView(ctx, fn)
	if err == nil && s.paused.CompareAndSwap(true, false) {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func TestSessionOwnerAccessCheckCannotRevokeSuccessorAfterLateStandingRead(t *testing.T) {
	var observed *ownerAccessReadStore
	f := newOwnerAccessFixture(t, func(st store.Store) store.Store {
		observed = &ownerAccessReadStore{Store: st, entered: make(chan struct{}), release: make(chan struct{})}
		return observed
	})
	observed.paused.Store(true)
	type result struct {
		scope auth.SessionScope
		err   error
	}
	done := make(chan result, 1)
	go func() {
		scope, _, err := f.issuer.CheckOwnerAccess(t.Context(), f.tenants[0], "run")
		done <- result{scope, err}
	}()
	<-observed.entered
	successorScope := f.scopes[0]
	successorScope.Fence++
	successor, err := f.issuer.Mint(t.Context(), f.launcher, successorScope)
	close(observed.release)
	if err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err == nil || got.scope.Fence != 1 {
		t.Fatalf("late standing read lost original fence: %d err=%v", got.scope.Fence, got.err)
	}
	if _, scope, err := f.issuer.Resolve(t.Context(), successor); err != nil || scope.Fence != 2 {
		t.Fatalf("old owner check damaged successor: %d err=%v", scope.Fence, err)
	}
}

func TestSessionOwnerAccessCheckPropagatesUnavailableStanding(t *testing.T) {
	var observed *ownerAccessReadStore
	f := newOwnerAccessFixture(t, func(st store.Store) store.Store { observed = &ownerAccessReadStore{Store: st}; return observed })
	observed.fail.Store(true)
	if _, _, err := f.issuer.CheckOwnerAccess(t.Context(), f.tenants[0], "run"); err == nil || errors.Is(err, auth.ErrSessionAccessEnded) {
		t.Fatalf("unavailable standing misclassified: %v", err)
	}
	observed.fail.Store(false)
	if _, _, err := f.issuer.Resolve(t.Context(), f.tokens[0]); err != nil {
		t.Fatal("unproven owner removal revoked the generation", err)
	}
}

func TestSessionStandingWithdrawalIsNotAResumableGroupChange(t *testing.T) {
	f := newOwnerAccessFixture(t, nil)
	if _, err := f.a.SCIMCreateGroup(t.Context(), f.admin, f.tenants[0], auth.SCIMGroupInput{DisplayName: "Directory subjects", Members: []model.ID{f.user.ID}}); err != nil {
		t.Fatal(err)
	}
	// Re-mint with that exact current closure before the standing is removed.

	login, _, err := f.a.Login(t.Context(), f.user.Email, "owner-access-password", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := f.a.Authenticate(t.Context(), login)
	if err != nil {
		t.Fatal(err)
	}
	token, err := f.issuer.Mint(t.Context(), launcher, f.scopes[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.AuthMutate(t.Context(), func(as store.AuthScope) error {
		rows, _, err := as.Memberships().List(t.Context(), model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: f.user.ID.String()}}, Limit: 10})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.TargetTenantID == f.tenants[0] {
				return as.Memberships().Delete(t.Context(), row.ID)
			}
		}
		return store.ErrNotFound
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.issuer.Resolve(t.Context(), token); !errors.Is(err, auth.ErrSessionAccessEnded) {
		t.Fatalf("standing removal must not suggest group-change resume: %v", err)
	}
}

func TestSessionOwnerAccessCheckKeepsActiveStandaloneTokenLauncher(t *testing.T) {
	f := newOwnerAccessFixture(t, nil)

	credential, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.AuthMutate(t.Context(), func(as store.AuthScope) error {
		_, err := as.Tokens().Create(t.Context(), model.APIToken{Name: "automated launcher", IsSuperadmin: true, Selector: credential.Selector, SecretHash: credential.SecretHash})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	token := credential.Token

	launcher, err := f.a.Authenticate(t.Context(), token)
	if err != nil || !launcher.UserID.IsZero() {
		t.Fatal("fixture is not a standalone token", err)
	}
	session, err := f.issuer.Mint(t.Context(), launcher, f.scopes[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.issuer.CheckOwnerAccess(t.Context(), f.tenants[0], "run"); err != nil {
		t.Fatal("active token standing was mistaken for a deleted user", err)
	}
	if _, _, err = f.issuer.Resolve(t.Context(), session); err != nil {
		t.Fatal("active token run was revoked", err)
	}
}
