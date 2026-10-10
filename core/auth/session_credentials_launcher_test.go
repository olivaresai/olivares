// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The launcher's rights are the session principal without the session's own
// workspace confinement: a tenant-wide launcher acts tenant-wide, a confined one
// stays in its workspace, and every launch bound still holds (role, no superadmin,
// one tenant, no credential reference, the session's lifetime).
func TestSessionCredentialsLauncherRightsKeepOnlyTheLaunchersConfinement(t *testing.T) {
	ctx := t.Context()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "session-launcher-rights")
	var workspace model.ID
	if err := st.View(ctx, tenant, func(sc store.Scope) error { ws, err := sc.DefaultWorkspace(ctx); workspace = ws.ID; return err }); err != nil {
		t.Fatal(err)
	}
	launch := func(email string, confinedTo model.ID) auth.Principal {
		t.Helper()
		if _, err := a.CreateUser(ctx, admin, auth.NewUser{Email: email, Password: "launcher-password-1", Tenant: tenant, Role: auth.RoleEditor, WorkspaceID: confinedTo}); err != nil {
			t.Fatal(err)
		}
		login, _, err := a.Login(ctx, email, "launcher-password-1", "fixture")
		if err != nil {
			t.Fatal(err)
		}
		p, err := a.Authenticate(ctx, login)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	var stopped atomic.Bool
	service := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error {
		if stopped.Load() {
			return errors.New("stopped")
		}
		return nil
	})
	for _, tc := range []struct {
		name       string
		confinedTo model.ID
	}{{"tenant-wide launcher", ""}, {"workspace-confined launcher", workspace}} {
		t.Run(tc.name, func(t *testing.T) {
			stopped.Store(false)
			run := model.NewID().String()
			token, err := service.Mint(ctx, launch(run+"@example.invalid", tc.confinedTo), auth.SessionScope{
				TenantID: tenant, WorkspaceID: workspace, FolderRef: "folder", SessionRef: "session-" + run, RunRef: run})
			if err != nil {
				t.Fatal(err)
			}
			session, err := service.Authenticate(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			if ws, ok := session.ConfinedWorkspaceIn(tenant); !ok || ws != workspace {
				t.Fatal("the session principal lost its workspace confinement")
			}
			launcher, err := service.AuthenticateLauncher(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			ws, confined := launcher.ConfinedWorkspaceIn(tenant)
			if want := !tc.confinedTo.IsZero(); confined != want || (want && ws != tc.confinedTo) {
				t.Fatalf("launcher confinement = %v %s, want %v %s", confined, ws, want, tc.confinedTo)
			}
			role, _ := launcher.RoleIn(tenant)
			if _, ref := launcher.Ref(); launcher.Superadmin || role != auth.RoleEditor || len(launcher.Tenants()) != 1 || launcher.SessionScope() != tenant || ref || launcher.SessionRunRef != run {
				t.Fatalf("launcher rights left a launch bound: %+v", launcher)
			}
			stopped.Store(true)
			if _, err := service.AuthenticateLauncher(ctx, token); err == nil {
				t.Fatal("a stopped session kept its launcher's rights")
			}
		})
	}
}

// The launcher of a live run, as its own exact credential: the principal a
// module that refuses session credentials admits as that person. It is refused
// for another generation, an unknown, stopped or revoked run, and a launcher
// whose authority changed since the launch: promoted, demoted, confined, made
// superadmin or stepped up.
func TestSessionCredentialsLauncherForRunIsTheLaunchersExactCredential(t *testing.T) {
	ctx := t.Context()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "session-launcher-exact")
	var workspace model.ID
	if err := st.View(ctx, tenant, func(sc store.Scope) error { ws, err := sc.DefaultWorkspace(ctx); workspace = ws.ID; return err }); err != nil {
		t.Fatal(err)
	}
	user, err := a.CreateUser(ctx, admin, auth.NewUser{Email: "exact@example.invalid", Password: "launcher-password-1", Tenant: tenant, Role: auth.RoleEditor})
	if err != nil {
		t.Fatal(err)
	}
	login, _, err := a.Login(ctx, "exact@example.invalid", "launcher-password-1", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := a.Authenticate(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	var stopped atomic.Bool
	service := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error {
		if stopped.Load() {
			return errors.New("stopped")
		}
		return nil
	})
	run := model.NewID().String()
	token, err := service.Mint(ctx, launcher, auth.SessionScope{TenantID: tenant, WorkspaceID: workspace, FolderRef: "folder", SessionRef: "session-" + run, RunRef: run, Fence: 1})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.LauncherForRun(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := launcher.Ref()
	if ref, ok := got.Ref(); !ok || ref != want || got.UserID != launcher.UserID || got.SessionIdentity != "" || got.SessionRunRef != "" {
		t.Fatalf("launcher for run = %+v (ref %v), want the launcher's exact credential", got, ok)
	}
	for name, other := range map[string]func(auth.Principal) auth.Principal{
		"unknown run":      func(p auth.Principal) auth.Principal { p.SessionRunRef = model.NewID().String(); return p },
		"another session":  func(p auth.Principal) auth.Principal { p.SessionIdentity = "session-other"; return p },
		"older generation": func(p auth.Principal) auth.Principal { p.SessionFence = 0; return p },
	} {
		if _, err := service.LauncherForRun(ctx, other(session)); err == nil {
			t.Errorf("%s resolved a launcher", name)
		}
	}
	// Each change is checked on the live generation: the run resolves just
	// before it, so the refusal is the change's and not a revocation's.
	for _, change := range []struct {
		name      string
		role      string
		workspace model.ID
	}{{"promoted", auth.RoleAdmin, ""}, {"demoted", auth.RoleViewer, ""}, {"confined", auth.RoleEditor, workspace}} {
		if _, err := a.GrantMembership(ctx, admin, user.ID, tenant, auth.RoleEditor, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := service.LauncherForRun(ctx, session); err != nil {
			t.Fatalf("before the %s launcher: the live run did not resolve: %v", change.name, err)
		}
		if _, err := a.GrantMembership(ctx, admin, user.ID, tenant, change.role, change.workspace); err != nil {
			t.Fatal(err)
		}
		if got, err := service.LauncherForRun(ctx, session); !errors.Is(err, auth.ErrSessionAccessEnded) {
			role, _ := got.RoleIn(tenant)
			t.Errorf("a launcher %s since the launch resolved: role %q, err %v", change.name, role, err)
		}
	}
	if _, err := a.GrantMembership(ctx, admin, user.ID, tenant, auth.RoleEditor, ""); err != nil {
		t.Fatal(err)
	}
	setSuperadmin := func(on bool) {
		t.Helper()
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			u, err := as.Users().Get(ctx, user.ID)
			if err != nil {
				return err
			}
			u.IsSuperadmin = on
			_, err = as.Users().Update(ctx, u)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.LauncherForRun(ctx, session); err != nil {
		t.Fatalf("the restored launcher did not resolve: %v", err)
	}
	setSuperadmin(true)
	if got, err := service.LauncherForRun(ctx, session); !errors.Is(err, auth.ErrSessionAccessEnded) {
		t.Errorf("a launcher made superadmin since the launch resolved: superadmin %v, err %v", got.Superadmin, err)
	}
	setSuperadmin(false)
	if _, err := service.LauncherForRun(ctx, session); err != nil {
		t.Fatalf("the launcher back to its launch authority did not resolve: %v", err)
	}
	// A step-up after the launch moves the launch credential's revision, which
	// ends the generation before the assurance comparison is reached.
	if _, err := a.ElevateSession(ctx, launcher, "webauthn", auth.AAL3); err != nil {
		t.Fatal(err)
	}
	if got, err := service.LauncherForRun(ctx, session); !errors.Is(err, auth.ErrSessionAccessEnded) {
		t.Errorf("a launcher stepped up since the launch resolved: AAL %d, err %v", got.AAL, err)
	}
	stopped.Store(true)
	if _, err := service.LauncherForRun(ctx, session); err == nil {
		t.Fatal("a stopped run resolved its launcher")
	}
	stopped.Store(false)
	service.Revoke(tenant, run)
	if _, err := service.LauncherForRun(ctx, session); err == nil {
		t.Fatal("a revoked run resolved its launcher")
	}
}
