// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSessionCredentialsNeverExpandLauncherAuthority(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "session-credentials")
	user, err := a.CreateUser(ctx, admin, auth.NewUser{Email: "session@example.invalid", Password: "session-password-1", Tenant: tenant, Role: auth.RoleEditor})
	if err != nil {
		t.Fatal(err)
	}
	login, _, err := a.Login(ctx, user.Email, "session-password-1", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := a.Authenticate(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	var workspace model.ID
	if err := st.View(ctx, tenant, func(sc store.Scope) error { ws, err := sc.DefaultWorkspace(ctx); workspace = ws.ID; return err }); err != nil {
		t.Fatal(err)
	}
	var stopped atomic.Bool
	service := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error {
		if stopped.Load() {
			return errors.New("stopped")
		}
		return nil
	})
	scope := auth.SessionScope{TenantID: tenant, WorkspaceID: workspace, FolderRef: "folder-one", SessionRef: "session-one", RunRef: "run-one"}
	token, err := service.Mint(ctx, launcher, scope)
	if err != nil {
		t.Fatal(err)
	}
	principal, got, err := service.Resolve(ctx, token)
	if err != nil || !reflect.DeepEqual(got, scope) {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	if principal.Superadmin || len(principal.Tenants()) != 1 || principal.SessionRunRef != scope.RunRef {
		t.Fatalf("unconfined session principal: %+v", principal)
	}
	if ws, ok := principal.ConfinedWorkspaceIn(tenant); !ok || ws != workspace {
		t.Fatal("workspace confinement missing")
	}
	if _, ok := principal.Ref(); ok {
		t.Fatal("child retained a launcher credential reference")
	}
	// A later membership promotion cannot enlarge a running session's launch ceiling.
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		list, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: user.ID.String()}}, Limit: 10})
		if err != nil {
			return err
		}
		for _, row := range list {
			if row.TargetTenantID == tenant {
				row.Role = auth.RoleOwner
				_, err = as.Memberships().Update(ctx, row)
				return err
			}
		}
		return store.ErrNotFound
	}); err != nil {
		t.Fatal(err)
	}
	principal, _, err = service.Resolve(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if role, _ := principal.RoleIn(tenant); role != auth.RoleEditor {
		t.Fatalf("launch role widened to %s", role)
	}
	stopped.Store(true)
	if _, _, err := service.Resolve(ctx, token); err == nil {
		t.Fatal("stopped session retained authority")
	}
	stopped.Store(false)
	if _, _, err := service.Resolve(ctx, token); err == nil {
		t.Fatal("stopped generation revived")
	}
	if _, _, err := service.ResolveRun(ctx, tenant, "run-one"); err == nil {
		t.Fatal("in-process resolver revived stopped authority")
	}
}
