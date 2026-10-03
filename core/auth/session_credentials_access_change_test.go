// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"sync/atomic"
	"testing"
)

func TestSessionCredentialAccessChangeNotifiesOnceOutsideIssuerLock(t *testing.T) {
	ctx := t.Context()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "session-access-change")
	user, err := a.CreateUser(ctx, admin, auth.NewUser{Email: "access-change@example.invalid", DisplayName: "Alex", Password: "access-password-1", Tenant: tenant, Role: auth.RoleEditor})
	if err != nil {
		t.Fatal(err)
	}
	login, _, err := a.Login(ctx, user.Email, "access-password-1", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := a.Authenticate(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	var workspace model.ID
	if err = st.View(ctx, tenant, func(sc store.Scope) error { ws, err := sc.DefaultWorkspace(ctx); workspace = ws.ID; return err }); err != nil {
		t.Fatal(err)
	}
	scope := auth.SessionScope{TenantID: tenant, WorkspaceID: workspace, FolderRef: "folder", SessionRef: "session", RunRef: "run", Fence: 1, Holder: user.ID.String()}
	var calls atomic.Int32
	var issuer *auth.SessionCredentials
	issuer = auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil }, func(ctx context.Context, got auth.SessionScope, label string) error {
		if got.RunRef != scope.RunRef || got.Fence != scope.Fence || label != "Alex" {
			t.Error("notification lost its captured scope or user")
		}
		calls.Add(1)
		// Both take the issuer mutex; this callback must run after revocation outside it.
		issuer.Revoke(got.TenantID, got.RunRef)
		if _, _, err := issuer.ResolveRun(ctx, got.TenantID, got.RunRef); err == nil {
			t.Error("callback saw unrevoked authority")
		}
		return nil
	})
	token, err := issuer.Mint(ctx, launcher, scope)
	if err != nil {
		t.Fatal(err)
	}
	group, err := a.SCIMCreateGroup(ctx, admin, tenant, auth.SCIMGroupInput{DisplayName: "Changed", Members: []model.ID{user.ID}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err = issuer.Resolve(ctx, token); err == nil {
			t.Fatal("changed generation retained authority")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("callbacks=%d", calls.Load())
	}
	launcher, err = a.Authenticate(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	scope.Fence++
	successor, err := issuer.Mint(ctx, launcher, scope)
	if err != nil {
		t.Fatal(err)
	}
	if p, _, err := issuer.Resolve(ctx, successor); err != nil || len(p.GroupsIn(tenant)) != 1 {
		t.Fatalf("successor groups=%d err=%v", len(p.GroupsIn(tenant)), err)
	}
	if err = a.SCIMDeleteGroup(ctx, admin, tenant, group.Group.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = issuer.Resolve(ctx, successor); !errors.Is(err, auth.ErrSessionAccessChanged) {
		t.Fatalf("removed group=%v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("second generation callbacks=%d", calls.Load())
	}
}
