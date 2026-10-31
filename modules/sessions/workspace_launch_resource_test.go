// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
)

// A session launched in a registered folder reads it, and writes it unless the
// folder is mounted read-only: the launch asks the same trustee rights the file
// routes ask for those actions, so a folder a person cannot write is not one a
// session of theirs can write.
func TestWorkspaceLaunchFolderRights(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	WithRunner(&fakeRunner{initSID: "folder-launch"})(f.m)
	WithCredentialSource(staticCred())(f.m)
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "folder-launch")
	token := f.member(t, admin, tenant, "launcher@test.io", "")
	principal, err := f.authr.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	profile := ensureRuntimeTestProfileRef(t, f.m, tenant)
	forbid := func(action string, id string) string {
		return fmt.Sprintf(`forbid(principal in User::"%s", action == Action::"%s", resource in Resource::"%s");`, principal.UserID, action, id)
	}
	for _, tc := range []struct {
		name, mount, action string
		want                int
	}{
		{"rw with every right", mountRW, "", http.StatusCreated},
		{"rw without canonical Write", mountRW, "resource:write", http.StatusForbidden},
		{"rw without module Write", mountRW, "sessions:workspace:write", http.StatusForbidden},
		{"rw without canonical Read", mountRW, "resource:read", http.StatusForbidden},
		{"no_delete without canonical Write", mountNoDelete, "resource:write", http.StatusForbidden},
		{"ro needs no Write", mountRO, "resource:write", http.StatusCreated},
		{"ro without canonical Read", mountRO, "resource:read", http.StatusForbidden},
		{"ro without module Read", mountRO, "sessions:workspace:read", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := mkWorkspace(t, f.m, tenant, CreateWorkspaceParams{RootPath: t.TempDir(), MountMode: tc.mount})
			rec, err := f.m.loadWorkspaceRec(t.Context(), tenant, ws.WorkspaceRef)
			if err != nil {
				t.Fatal(err)
			}
			policy := `permit(principal in Role::"nobody", action, resource);`
			if tc.action != "" {
				policy = forbid(tc.action, rec.String(model.ColID))
			}
			testsupport.SeedCedar(t, f.st, tenant, policy, f.gov)
			got := f.doJSON("POST", "/v1/m/sessions/runs", token, map[string]any{
				"provider_profile_ref": profile, "workspace_ref": ws.WorkspaceRef,
				"transport": "stream-json", "permission_mode": "default", "isolation": "native",
			}, tenantHdr(tenant))
			if got.code != tc.want {
				t.Errorf("launch = %d %s, want %d", got.code, got.raw, tc.want)
			}
		})
	}
}

// A launcher restored from a queue or the engine's resume is asked through the
// authorizer, as it is for its other launch questions: no authorizer is a no.
func TestWorkspaceRestoredLauncherFolderRights(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t)
	rw := mkWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: t.TempDir()})
	ro := mkWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: t.TempDir(), MountMode: mountRO})
	ask := func(ref string) error {
		return m.principalAsks(t.Context(), tenant, auth.Principal{}).workspaceRights(ref)
	}
	if err := ask(ro.WorkspaceRef); err == nil {
		t.Error("no authorizer wired: the restored launcher was admitted")
	}
	az := &questionedAuthorizer{allow: map[auth.Permission]bool{permWsRead: true}}
	WithWorkAuthorizer(az)(m)
	if err := ask(ro.WorkspaceRef); err != nil {
		t.Errorf("read-only folder with Read: %v", err)
	}
	if err := ask(rw.WorkspaceRef); err == nil {
		t.Error("read-write folder without Write: the restored launcher was admitted")
	}
	az.allow[permWsWrite] = true
	if err := ask(rw.WorkspaceRef); err != nil {
		t.Errorf("read-write folder with Read and Write: %v", err)
	}
}

// A resume is a launch in the same folder: the rights taken away after the first
// launch stop the next one, and giving them back lets it through.
func TestWorkspaceResumeFolderRights(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	WithRunner(&fakeRunner{initSID: "folder-resume"})(f.m)
	WithCredentialSource(staticCred())(f.m)
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "folder-resume")
	token := f.member(t, admin, tenant, "resumer@test.io", "")
	principal, err := f.authr.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	ws := mkWorkspace(t, f.m, tenant, CreateWorkspaceParams{RootPath: t.TempDir()})
	rec, err := f.m.loadWorkspaceRec(t.Context(), tenant, ws.WorkspaceRef)
	if err != nil {
		t.Fatal(err)
	}
	launched := f.doJSON("POST", "/v1/m/sessions/runs", token, map[string]any{
		"provider_profile_ref": ensureRuntimeTestProfileRef(t, f.m, tenant), "workspace_ref": ws.WorkspaceRef,
		"transport": "stream-json", "permission_mode": "default", "isolation": "native",
	}, tenantHdr(tenant))
	if launched.code != http.StatusCreated {
		t.Fatalf("launch: %d %s", launched.code, launched.raw)
	}
	base := "/v1/m/sessions/runs/" + launched.body["run_ref"].(string)
	if got := f.doJSON("POST", base+"/stop", token, map[string]any{}, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("stop: %d %s", got.code, got.raw)
	}
	forbid := fmt.Sprintf(`forbid(principal in User::"%s", action == Action::"resource:write", resource in Resource::"%s");`, principal.UserID, rec.String(model.ColID))
	testsupport.SeedCedar(t, f.st, tenant, forbid, f.gov)
	if got := f.doJSON("POST", base+"/resume", token, map[string]any{}, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("resume without Write: %d %s, want 403", got.code, got.raw)
	}
	testsupport.SeedCedar(t, f.st, tenant, `permit(principal in Role::"nobody", action, resource);`, f.gov)
	if got := f.doJSON("POST", base+"/resume", token, map[string]any{}, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("resume with every right: %d %s, want 200", got.code, got.raw)
	}
}
