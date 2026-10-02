// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSessionCredentialsBindImmutableLaunchPresetAndRotateIt(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	launcher := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "session-presets")
	var workspace model.ID
	if err := st.View(ctx, tenant, func(sc store.Scope) error { ws, err := sc.DefaultWorkspace(ctx); workspace = ws.ID; return err }); err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewSessionCredentials(a, func(_ context.Context, scope auth.SessionScope) error {
		// A validator, launch caller or resolver caller cannot mutate stored tools.
		if len(scope.AllowedTools) > 0 {
			scope.AllowedTools[0] = "*"
		}
		return nil
	})
	scope := auth.SessionScope{TenantID: tenant, WorkspaceID: workspace, FolderRef: "folder", SessionRef: "session", RunRef: "run", Preset: "custom", AllowedTools: []string{"Read"}}
	old, err := issuer.Mint(ctx, launcher, scope)
	if err != nil {
		t.Fatal(err)
	}
	scope.AllowedTools[0] = "*"
	_, first, err := issuer.Resolve(ctx, old)
	if err != nil || first.Preset != "custom" || !slices.Equal(first.AllowedTools, []string{"Read"}) {
		t.Fatalf("launch preset or tool surface changed: %+v, %v", first, err)
	}
	first.AllowedTools[0] = "*"
	_, again, err := issuer.ResolveRun(ctx, tenant, "run")
	if err != nil || !slices.Equal(again.AllowedTools, []string{"Read"}) {
		t.Fatalf("resolved copy changed the stored surface: %+v, %v", again, err)
	}
	scope.Preset, scope.AllowedTools = "read_only", nil
	fresh, err := issuer.Mint(ctx, launcher, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := issuer.Resolve(ctx, old); err == nil {
		t.Fatal("previous launch generation retained authority")
	}
	_, resumed, err := issuer.Resolve(ctx, fresh)
	if err != nil || resumed.Preset != "read_only" || len(resumed.AllowedTools) != 0 {
		t.Fatalf("new launch did not bind its current preset: %+v, %v", resumed, err)
	}
}
