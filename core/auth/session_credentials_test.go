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
	"time"

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
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Source reconstruction ignores caller-editable identity and privilege fields.
	configure, err := service.AuthenticateLauncher(bounded, token)
	if err != nil {
		t.Fatal(err)
	}
	configure.Superadmin = true
	configure.SessionRunRef = "forged-run"
	resolved, bundle, expiry, err := a.ResolveSessionLauncherScope(bounded, configure, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if role, _ := resolved.RoleIn(tenant); role != auth.RoleEditor || resolved.Superadmin || resolved.SessionRunRef != scope.RunRef {
		t.Fatalf("caller fields widened the launch ceiling: role=%s superadmin=%t run=%s", role, resolved.Superadmin, resolved.SessionRunRef)
	}
	if _, confined := resolved.ConfinedWorkspaceIn(tenant); confined {
		t.Fatal("unconfined launcher acquired the session's workspace confinement")
	}
	if len(bundle.Facts) == 0 || len(bundle.UserAuthorities) != 1 || !time.Now().Before(expiry) {
		t.Fatalf("missing finite complete human source evidence: %+v expiry=%v", bundle, expiry)
	}
	if _, ok := resolved.Ref(); ok {
		t.Fatal("session launcher became a native credential")
	}
	az := auth.NewAuthorizer(nil)
	question := auth.Request{Principal: configure, Tenant: tenant, Permission: "tenant:read"}
	decision, retained, err := a.AuthorizeSessionLauncher(bounded, az, question)
	if err != nil || decision.Outcome != auth.EvidenceAllow || len(retained.UserAuthorities) != 1 || len(retained.Facts) == 0 || decision.FreshUntil.After(expiry) {
		t.Fatalf("issuer-backed typed launcher decision: %+v %+v %v", decision, retained, err)
	}
	question.Permission = "tenant:admin"
	if _, _, err := a.AuthorizeSessionLauncher(bounded, az, question); !errors.Is(err, auth.ErrRouteDenied) {
		t.Fatalf("typed decision widened a promoted or caller-edited launcher: %v", err)
	}
	question.Principal = resolved
	question.Permission = "tenant:read"
	if _, err := az.AuthorizeRouteMutation(bounded, question); !errors.Is(err, auth.ErrRouteUndecided) {
		t.Fatalf("typed launcher gained native mutation authority: %v", err)
	}
	fake := auth.Principal{SessionIdentity: scope.SessionRef, SessionRunRef: scope.RunRef}
	if fake.IsSessionCredential() {
		t.Fatal("public run identity forged issuer provenance")
	}
	if _, _, _, err := a.ResolveSessionLauncherScope(bounded, fake, tenant); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("synthetic launcher = %v, want unauthenticated", err)
	}
	if _, _, _, err := auth.NewAuthenticator(st, nil).ResolveSessionLauncherScope(bounded, configure, tenant); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("foreign authenticator = %v, want unauthenticated", err)
	}
	question.Principal = fake
	if _, _, err := a.AuthorizeSessionLauncher(bounded, az, question); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("synthetic typed launcher = %v, want unauthenticated", err)
	}
	question.Principal = configure
	if _, _, err := auth.NewAuthenticator(st, nil).AuthorizeSessionLauncher(bounded, az, question); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("foreign typed launcher = %v, want unauthenticated", err)
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
