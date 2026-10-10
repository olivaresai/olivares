// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestWorkspaceReadOnlyFoldersPersistAndReloadOnResume(t *testing.T) {
	runner := &fakeRunner{}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithConfinement([]string{t.TempDir()}, true))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "read-folders")
	folder, tools := t.TempDir(), t.TempDir()
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{
		"root_path": folder, "read_only_folders": []string{tools},
	}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register read-only folders=%d %s", created.code, created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	stored := h.do("GET", "/v1/m/sessions/workspaces/"+ref, admin, tenantHdr(tenant))
	folders, ok := stored.body["read_only_folders"].([]any)
	if stored.code != http.StatusOK || !ok || len(folders) != 1 || folders[0] != tools {
		t.Fatalf("saved folders=%d %s", stored.code, stored.raw)
	}
	run := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, h.m, tenant),
		"transport": "stream-json", "permission_mode": "default", "isolation": "native", "workspace_ref": ref,
	}, tenantHdr(tenant))
	if run.code != http.StatusCreated {
		t.Fatalf("launch=%d %s", run.code, run.raw)
	}
	policy := runner.lastSpec().Confinement
	if policy == nil || !workspaceFolderHandleNamed(runner.lastSpec(), tools) || slices.Contains(policy.ReadWrite, tools) {
		t.Fatalf("read-only workspace grant missing/writable: %+v", policy)
	}
	for _, file := range runner.lastSpec().ConfinementFiles {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("launch left a resolver-owned directory handle open: %v", err)
		}
	}
	runRef := run.body["run_ref"].(string)
	if stopped := h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/stop", admin, nil, tenantHdr(tenant)); stopped.code != http.StatusOK {
		t.Fatalf("stop=%d %s", stopped.code, stopped.raw)
	}
	cleared := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+ref, admin, map[string]any{"read_only_folders": []string{}}, tenantHdr(tenant))
	if cleared.code != http.StatusOK {
		t.Fatalf("clear=%d %s", cleared.code, cleared.raw)
	}
	resumed := h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/resume", admin, nil, tenantHdr(tenant))
	if resumed.code != http.StatusOK {
		t.Fatalf("resume=%d %s", resumed.code, resumed.raw)
	}
	if p := runner.lastSpec().Confinement; p == nil || workspaceFolderHandleNamed(runner.lastSpec(), tools) {
		t.Fatalf("resume retained removed grant: %+v", p)
	}
	want := workspacePayloadHash(wsMutationInput{op: "configure", workspaceRef: ref, path: folder, contentHash: sha256Hex([]byte("[]"))})
	found := false
	if err := h.st.View(t.Context(), model.TenantID(tenant), func(sc store.Scope) error {
		if err := sc.Audit().Walk(t.Context(), 0, func(ev model.AuditEvent) error {
			if ev.Action == "sessions.workspace.configure" && bytes.Equal(ev.PayloadHash, want[:]) {
				found = true
			}
			return nil
		}); err != nil {
			return err
		}
		rep, err := sc.Audit().Verify(t.Context(), 0)
		if err == nil && !rep.OK {
			t.Errorf("audit chain broken: %s", rep.Reason)
		}
		return err
	}); err != nil || !found {
		t.Fatalf("folder edit has no verified configuration anchor: found=%v err=%v", found, err)
	}
}

func TestWorkspaceReadOnlyFolderResumeRejectsRedirectedAndCorruptPaths(t *testing.T) {
	runner := &fakeRunner{initSID: "folder-path-fixture"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(runner), WithCredentialSource(staticCred()), WithConfinement([]string{t.TempDir()}, true))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "folder-paths")
	tools := filepath.Join(t.TempDir(), "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir(), "read_only_folders": []string{tools}}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	run := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, h.m, tenant), "workspace_ref": ref}, tenantHdr(tenant))
	if run.code != http.StatusCreated {
		t.Fatalf("launch=%d %s", run.code, run.raw)
	}
	runRef := run.body["run_ref"].(string)
	if r := h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/stop", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("stop=%d %s", r.code, r.raw)
	}
	if err := os.Rename(tools, tools+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), tools); err != nil {
		t.Fatal(err)
	}
	assertRefused := func() {
		t.Helper()
		if r := h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/resume", admin, nil, tenantHdr(tenant)); r.code != http.StatusFailedDependency {
			t.Fatalf("changed/corrupt folder allowed resume=%d %s", r.code, r.raw)
		}
		if count := launchCount(runner); count != 1 {
			t.Fatalf("refused path launched another child: %d", count)
		}
	}
	assertRefused()
	if err := os.Remove(tools); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tools+"-saved", tools); err != nil {
		t.Fatal(err)
	}
	setStored := func(value any) {
		t.Helper()
		if err := h.st.Mutate(t.Context(), model.TenantID(tenant), func(sc store.Scope) error {
			repo, err := sc.Ext(workspaceKind)
			if err != nil {
				return err
			}
			rec, err := findWorkspaceRec(t.Context(), repo, ref)
			if err != nil {
				return err
			}
			rec[colWsReadOnlyFolders] = value
			_, err = repo.Update(t.Context(), rec)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	setStored(`[null]`)
	assertRefused()
	setStored(nil) // Existing workspace rows after the additive column migration.
	if r := h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/resume", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("upgrade default refused resume=%d %s", r.code, r.raw)
	}
	if p := runner.lastSpec().Confinement; p == nil || workspaceFolderHandleNamed(runner.lastSpec(), tools) {
		t.Fatalf("NULL upgrade default retained an external grant: %+v", p)
	}
}

func TestWorkspaceReadOnlyFolderChangesRequireItsAdministrator(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "folder-admin")
	other := h.createOrg(admin, "other-folder-admin")
	viewer := h.viewerToken(admin, tenant, "folder-viewer@example.invalid")
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir()}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	body := map[string]any{"read_only_folders": []string{t.TempDir()}}
	for _, token := range []string{viewer, ""} {
		want := http.StatusForbidden
		if token == "" {
			want = http.StatusUnauthorized
		}
		if r := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+ref, token, body, tenantHdr(tenant)); r.code != want {
			t.Fatalf("non-admin change=%d %s want=%d", r.code, r.raw, want)
		}
	}
	if r := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+ref, admin, body, tenantHdr(other)); r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant change=%d %s", r.code, r.raw)
	}
	for _, invalid := range []any{[]string{"relative"}, nil, []any{nil}, []string{"/nonexistent-n1c-folder"}} {
		if r := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+ref, admin, map[string]any{"read_only_folders": invalid}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Fatalf("invalid folder list accepted=%d %s", r.code, r.raw)
		}
	}
	stored := h.do("GET", "/v1/m/sessions/workspaces/"+ref, admin, tenantHdr(tenant))
	if paths, _ := stored.body["read_only_folders"].([]any); len(paths) != 0 {
		t.Fatalf("refused request changed the default-empty grants: %s", stored.raw)
	}
}

func TestWorkspaceReadOnlyFolderEditRollsBackWithItsAudit(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "folder-rollback")
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir()}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	original := m.Data
	m.Data = metadataRollbackData{original}
	r := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+ref, admin, map[string]any{"read_only_folders": []string{t.TempDir()}}, tenantHdr(tenant))
	m.Data = original
	if r.code < 400 {
		t.Fatalf("refused transaction succeeded=%d %s", r.code, r.raw)
	}
	stored := h.do("GET", "/v1/m/sessions/workspaces/"+ref, admin, tenantHdr(tenant))
	if paths, _ := stored.body["read_only_folders"].([]any); stored.code != http.StatusOK || len(paths) != 0 {
		t.Fatalf("refused transaction left grants: %s", stored.raw)
	}
	if events := acctAuditsOf(t, m, model.TenantID(tenant), "sessions.workspace.configure"); len(events) != 0 {
		t.Fatalf("refused transaction left %d configuration audits", len(events))
	}
}

func workspaceFolderHandleNamed(spec LaunchSpec, path string) bool {
	for _, file := range spec.ConfinementFiles {
		if file.Name() == path {
			return true
		}
	}
	return false
}
