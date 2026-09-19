// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These are real stored TEXT values, not a fake decoder result. KindJSON's
// repository currently permits malformed text as well as JSON of the wrong shape.
func TestWorkspace_CorruptPolicyBlocksFileEffects(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"malformed", `["src"`},
		{"object", `{"path":"src"}`},
		{"string", `"src"`},
		{"number", `7`},
		{"boolean", `true`},
		{"null_entry", `["src",null]`},
		{"number_entry", `["src",7]`},
		{"object_entry", `[{}]`},
		{"escaping_entry", `["../private"]`},
		{"absolute_entry", `["/private"]`},
		{"nul_entry", `["src\u0000private"]`},
		{"whitespace_only_entry", `["   "]`},
		{"padded_subtree", `[" src "]`},
		{"mixed_canonical_padded", `["docs", " src "]`},
		{"mixed_legacy_empty_whitespace", `["", "  "]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{}
			m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
			ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
			if err := os.Mkdir(filepath.Join(ws.RootPath, "src"), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, rel := range []string{"keep.txt", "src/keep.txt"} {
				if err := os.WriteFile(filepath.Join(ws.RootPath, rel), []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			setWorkspacePolicyText(t, st, tenant, ws.WorkspaceRef, tc.raw)

			// Exercise both the root and src: dropping a whitespace-only entry
			// broadens to the root, while trimming " src " retargets to src.
			for _, rel := range []string{"keep.txt", "src/keep.txt"} {
				parent := filepath.Dir(rel)
				list, err := m.listFiles(ctx, tenant, ws.WorkspaceRef, parent, 0, "", actorU, actorKindU)
				assertWorkspacePolicyUnavailable(t, err)
				if !reflect.DeepEqual(list, fileListResponse{}) {
					t.Error("unreadable policy returned directory metadata")
				}
				stat, err := m.statFile(ctx, tenant, ws.WorkspaceRef, rel, actorU, actorKindU)
				assertWorkspacePolicyUnavailable(t, err)
				if !reflect.DeepEqual(stat, fileEntry{}) {
					t.Error("unreadable policy returned file metadata")
				}
				read, err := m.readFile(ctx, tenant, ws.WorkspaceRef, rel, actorU, actorKindU)
				assertWorkspacePolicyUnavailable(t, err)
				if !reflect.DeepEqual(read, fileReadResponse{}) {
					t.Error("unreadable policy returned file content")
				}
				_, err = m.writeFile(ctx, tenant, ws.WorkspaceRef, rel, []byte("changed"), actorU, actorKindU)
				assertWorkspacePolicyUnavailable(t, err)
				_, err = m.writeFile(ctx, tenant, ws.WorkspaceRef, filepath.Join(parent, "new.txt"), []byte("new"), actorU, actorKindU)
				assertWorkspacePolicyUnavailable(t, err)
				_, err = m.mkdir(ctx, tenant, ws.WorkspaceRef, filepath.Join(parent, "new-dir"), actorU, actorKindU)
				assertWorkspacePolicyUnavailable(t, err)
				assertWorkspacePolicyUnavailable(t, m.moveFile(ctx, tenant, ws.WorkspaceRef, rel, filepath.Join(parent, "moved.txt"), actorU, actorKindU))
				assertWorkspacePolicyUnavailable(t, m.deleteFile(ctx, tenant, ws.WorkspaceRef, rel, false, actorU, actorKindU))
				assertWorkspacePolicyUnavailable(t, m.deleteFile(ctx, tenant, ws.WorkspaceRef, rel, true, actorU, actorKindU))
			}
			_, err := m.createRun(ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: ws.WorkspaceRef,
				Actor: actorU, ActorKind: actorKindU,
			})
			assertWorkspacePolicyUnavailable(t, err)
			fr.mu.Lock()
			launches := len(fr.specs)
			fr.mu.Unlock()
			if launches != 0 {
				t.Error("unreadable policy reached the runner")
			}
			for _, rel := range []string{"keep.txt", "src/keep.txt"} {
				content, err := os.ReadFile(filepath.Join(ws.RootPath, rel))
				if err != nil || string(content) != "original" {
					t.Errorf("rejected mutations changed %s: %v", rel, err)
				}
			}
			for _, dir := range []struct {
				path string
				want []string
			}{{".", []string{"keep.txt", "src"}}, {"src", []string{"keep.txt"}}} {
				entries, err := os.ReadDir(filepath.Join(ws.RootPath, dir.path))
				if err != nil {
					t.Fatal(err)
				}
				var names []string
				for _, entry := range entries {
					names = append(names, entry.Name())
				}
				if !reflect.DeepEqual(names, dir.want) {
					t.Errorf("rejected mutations changed %s entries: %v", dir.path, names)
				}
			}
			dto, err := m.getWorkspace(ctx, tenant, ws.WorkspaceRef)
			assertWorkspacePolicyUnavailable(t, err)
			if !reflect.DeepEqual(dto, workspaceDTO{}) {
				t.Fatal("corrupt workspace was reported as valid configuration")
			}
			page, err := m.listWorkspaces(ctx, tenant, model.Query{})
			assertWorkspacePolicyUnavailable(t, err)
			if !reflect.DeepEqual(page, listResponse[workspaceDTO]{}) {
				t.Fatal("corrupt policy returned a partial registry page")
			}
		})
	}
}

func TestWorkspace_PolicyEmptyFormsRemainUsable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"sql_null", nil},
		{"empty_text", ""},
		{"whitespace", " \n\t "},
		{"empty_array", `[]`},
		{"json_null", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{}
			m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
			ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
			setWorkspacePolicyText(t, st, tenant, ws.WorkspaceRef, tc.value)
			if _, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, "ok.txt", []byte("allowed"), actorU, actorKindU); err != nil {
				t.Fatalf("valid empty policy refused write: %v", err)
			}
			got, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "ok.txt", actorU, actorKindU)
			if err != nil || got.Content != "allowed" {
				t.Fatalf("valid empty policy refused read: %v", err)
			}
			dto, err := m.getWorkspace(ctx, tenant, ws.WorkspaceRef)
			if err != nil || len(dto.AllowSubpaths) != 0 {
				t.Fatalf("empty policy registry projection changed: %v", err)
			}
			page, err := m.listWorkspaces(ctx, tenant, model.Query{})
			if err != nil || len(page.Items) != 1 || page.Items[0].WorkspaceRef != ws.WorkspaceRef {
				t.Fatalf("valid policy disappeared from the registry: %v", err)
			}
			if _, err := m.createRun(ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: ws.WorkspaceRef,
				Actor: actorU, ActorKind: actorKindU,
			}); err != nil {
				t.Fatalf("valid empty policy refused launch: %v", err)
			}
			if fr.lastSpec().Dir != ws.RootPath {
				t.Fatal("launch lost the registered root")
			}
		})
	}
}

func TestWorkspace_StoredPolicyPreservesLegacyRootEntries(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{"sole_empty", `[""]`, []string{""}},
		{"mixed_empty_subtree", `["", "src"]`, []string{"", "src"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, st, tenant, _ := newRuntimeHarness(t)
			ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{AllowSubpaths: []string{"src"}})
			setWorkspacePolicyText(t, st, tenant, ws.WorkspaceRef, tc.raw)
			if _, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, "root.txt", []byte("legacy root"), actorU, actorKindU); err != nil {
				t.Fatalf("legacy empty entry must retain root access: %v", err)
			}
			read, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "root.txt", actorU, actorKindU)
			if err != nil || read.Content != "legacy root" {
				t.Fatalf("legacy root read changed: %v", err)
			}
			dto, err := m.getWorkspace(ctx, tenant, ws.WorkspaceRef)
			if err != nil || !reflect.DeepEqual(dto.AllowSubpaths, tc.want) {
				t.Fatalf("stored entries must survive the detail DTO unchanged: %v", err)
			}
			page, err := m.listWorkspaces(ctx, tenant, model.Query{})
			if err != nil || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0].AllowSubpaths, tc.want) {
				t.Fatalf("stored entries must survive the list DTO unchanged: %v", err)
			}
		})
	}
}

func TestWorkspace_StoredPolicyUsesRegistrationPathRules(t *testing.T) {
	ctx := context.Background()
	m, st, tenant, _ := newRuntimeHarness(t)
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{AllowSubpaths: []string{" src ", ""}})
	if !reflect.DeepEqual(ws.AllowSubpaths, []string{"src"}) {
		t.Fatalf("registration normalization changed: %v", ws.AllowSubpaths)
	}
	if err := os.Mkdir(filepath.Join(ws.RootPath, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.RootPath, "private.txt"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stored authority is validated as written, never normalized during reading.
	setWorkspacePolicyText(t, st, tenant, ws.WorkspaceRef, `["src"]`)
	stored, err := m.getWorkspace(ctx, tenant, ws.WorkspaceRef)
	if err != nil || !reflect.DeepEqual(stored.AllowSubpaths, []string{"src"}) {
		t.Fatalf("canonical stored policy changed in the DTO: %v", err)
	}
	if _, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, "src/ok.txt", []byte("allowed"), actorU, actorKindU); err != nil {
		t.Fatalf("restricted policy refused an allowed file: %v", err)
	}
	if got, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "src/ok.txt", actorU, actorKindU); err != nil || got.Content != "allowed" {
		t.Fatalf("restricted policy refused an allowed read: %v", err)
	}
	if _, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "private.txt", actorU, actorKindU); !isStatus(err, 403) {
		t.Fatalf("restricted policy admitted a sibling read: %v", err)
	}
	if _, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, "private.txt", []byte("changed"), actorU, actorKindU); !isStatus(err, 403) {
		t.Fatalf("restricted policy admitted a sibling write: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(ws.RootPath, "private.txt")); err != nil || string(got) != "private" {
		t.Fatalf("sibling bytes changed: %v", err)
	}
	for _, path := range []string{"../private", filepath.Join(ws.RootPath, "src"), "src\x00private"} {
		if _, err := m.createWorkspace(ctx, tenant, CreateWorkspaceParams{
			RootPath: ws.RootPath, AllowSubpaths: []string{path}, Actor: actorU, ActorKind: actorKindU,
		}); !isStatus(err, 400) {
			t.Fatalf("registration accepted an invalid path: %v", err)
		}
	}
}

func TestWorkspace_CorruptPolicyBlocksResume(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{initSID: "workspace-policy-session"}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	run, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: ws.WorkspaceRef,
		Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "workspace resume session id", func() bool {
		got, err := m.getRun(ctx, tenant, run.RunRef)
		return err == nil && got.ClaudeSessionID == "workspace-policy-session"
	})
	if _, err := m.stopRun(ctx, tenant, run.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	setWorkspacePolicyText(t, st, tenant, ws.WorkspaceRef, `["src"`)
	_, err = m.resumeRun(ctx, tenant, run.RunRef, actorU, actorKindU, "")
	assertWorkspacePolicyUnavailable(t, err)
	fr.mu.Lock()
	launches := len(fr.specs)
	fr.mu.Unlock()
	if launches != 1 {
		t.Fatal("corrupt policy allowed a second process launch")
	}
	got, err := m.getRun(ctx, tenant, run.RunRef)
	if err != nil || got.State != stateStopped {
		t.Fatalf("rejected resume changed the stopped run: %v", err)
	}
	setWorkspacePolicyText(t, st, tenant, ws.WorkspaceRef, `[]`)
	if _, err := m.resumeRun(ctx, tenant, run.RunRef, actorU, actorKindU, ""); err != nil {
		t.Fatalf("restored valid policy must allow resume: %v", err)
	}
	fr.mu.Lock()
	launches = len(fr.specs)
	fr.mu.Unlock()
	if launches != 2 {
		t.Fatal("positive resume control did not launch")
	}
}

func TestWorkspace_CorruptPolicyPrecedesRootInspection(t *testing.T) {
	ctx := context.Background()
	m, st, tenant, _ := newRuntimeHarness(t)
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	setWorkspacePolicyText(t, st, tenant, ws.WorkspaceRef, `{`)
	if err := os.Remove(ws.RootPath); err != nil {
		t.Fatal(err)
	}
	_, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "missing", actorU, actorKindU)
	assertWorkspacePolicyUnavailable(t, err)
}

func TestWorkspace_CorruptPolicyRegistryHTTP(t *testing.T) {
	m, st, tenant, _ := newRuntimeHarness(t)
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	setWorkspacePolicyText(t, st, tenant, ws.WorkspaceRef, `{"private-policy-value":true}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	m.handleListWorkspaces(rec, req, api.ModuleContext{Tenant: tenant})
	if rec.Code != http.StatusFailedDependency || rec.Body.String() != "{\"error\":{\"message\":\"workspace allow_subpaths policy is unavailable\"}}\n" {
		t.Fatalf("corrupt policy HTTP response: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestWorkspace_PolicyRecordTypes(t *testing.T) {
	// A non-text value violates model.Record's normalized JSON-column contract;
	// exercise that boundary explicitly without claiming such a row was persisted.
	for _, value := range []any{false, int64(7), []byte(`[]`), []string{"src"}} {
		_, err := decodeSubpaths(model.Record{colWsAllowSubpaths: value})
		assertWorkspacePolicyUnavailable(t, err)
	}
	if paths, err := decodeSubpaths(model.Record{}); err != nil || len(paths) != 0 {
		t.Fatalf("absent nullable column must remain unrestricted: %v", err)
	}
}

func setWorkspacePolicyText(t *testing.T, st store.Store, tenant model.TenantID, ref string, value any) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		rec, err := findWorkspaceRec(ctx, repo, ref)
		if err != nil {
			return err
		}
		rec[colWsAllowSubpaths] = value
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("persist policy fixture: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		rec, err := findWorkspaceRec(ctx, repo, ref)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(rec[colWsAllowSubpaths], value) {
			t.Fatal("stored policy fixture did not round-trip unchanged")
		}
		return nil
	}); err != nil {
		t.Fatalf("read policy fixture: %v", err)
	}
}

func assertWorkspacePolicyUnavailable(t *testing.T, err error) {
	t.Helper()
	if !isStatus(err, 424) {
		t.Errorf("corrupt policy must refuse with 424, got %v", err)
		return
	}
	if err.Error() != "workspace allow_subpaths policy is unavailable" {
		t.Errorf("policy failure must have a fixed safe diagnostic, got %v", err)
	}
}
