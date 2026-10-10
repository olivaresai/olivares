// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"archive/zip"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// Use the real HTTP engine, SQLite, runner, Go compiler and test executable. Only
// the provider's stream-json protocol is a fixture; it invokes ordinary Go tools.
func TestWorkspaceReadOnlyFoldersAllowGoWithoutOpeningExternalWrites(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock || state.ABI < 3 {
		t.Skip("host cannot enforce read-only truncation: " + state.String())
	}
	folder, cache, data := t.TempDir(), t.TempDir(), t.TempDir()
	dep := filepath.Join(cache, "example.invalid", "dep@v1.0.0")
	goRoot, err := filepath.EvalSymlinks(runtime.GOROOT())
	if err != nil {
		t.Fatal(err)
	}
	const depMod = "module example.invalid/dep\ngo 1.26.8\n"
	const depCode = "package dep\nconst Value = 42\n"
	files := map[string]string{
		filepath.Join(folder, "go.mod"):       "module example.invalid/session\ngo 1.26.8\nrequire example.invalid/dep v1.0.0\n",
		filepath.Join(folder, "main_test.go"): "package session\nimport (\"testing\"; \"example.invalid/dep\")\nfunc TestCachedDependency(t *testing.T) { if dep.Value != 42 { t.Fatal(dep.Value) } }\n",
		filepath.Join(data, "engine-key"):     "synthetic-engine-key",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Populate a normal GOMODCACHE through Go's file-proxy protocol, offline. The
	// confined session then resolves the require via that cache, with no replace.
	proxy := t.TempDir()
	version := filepath.Join(proxy, "example.invalid", "dep", "@v")
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"v1.0.0.mod": depMod, "v1.0.0.info": `{"Version":"v1.0.0","Time":"2026-10-02T00:00:00Z"}`} {
		if err := os.WriteFile(filepath.Join(version, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	zipFile, err := os.Create(filepath.Join(version, "v1.0.0.zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zipFile)
	for name, content := range map[string]string{"go.mod": depMod, "dep.go": depCode} {
		w, err := zw.Create("example.invalid/dep@v1.0.0/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipFile.Close(); err != nil {
		t.Fatal(err)
	}
	download := exec.CommandContext(t.Context(), filepath.Join(goRoot, "bin", "go"), "mod", "download", "example.invalid/dep@v1.0.0")
	download.Dir = folder
	download.Env = []string{"PATH=" + os.Getenv("PATH"), "GOENV=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOSUMDB=off", "GOPROXY=file://" + filepath.ToSlash(proxy), "GOMODCACHE=" + cache, "GOPATH=" + t.TempDir(), "GOCACHE=" + t.TempDir()}
	if out, err := download.CombinedOutput(); err != nil {
		t.Fatalf("offline module-cache fixture: %v\n%s", err, out)
	}
	if err := os.Chmod(dep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dep, "dep.go"), 0o600); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	agent := filepath.Join(folder, "agent.sh")
	script := `#!/bin/sh
printf '{"type":"system","subtype":"init","session_id":"go-fixture"}\n'
export GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off GOMAXPROCS=2 GOFLAGS=-p=1
export GOCACHE="$TMPDIR/go-build" GOPATH="$TMPDIR/go-path"
export GOMODCACHE=` + quote(cache) + `
` + quote(filepath.Join(goRoot, "bin", "go")) + ` test -vet=off -parallel=1 -count=1 . > go-proof.log 2>&1 || exit 11
if cat ` + quote(filepath.Join(data, "engine-key")) + ` 2>/dev/null; then exit 12; fi
if printf forbidden > ` + quote(filepath.Join(dep, "dep.go")) + ` 2>/dev/null; then exit 13; fi
echo read-only-controls-pass >> go-proof.log
printf '{"type":"result","subtype":"success","is_error":false,"result":"Go test and confinement controls passed"}\n'
`
	if err := os.WriteFile(agent, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(NewProcRunner()), WithProgram(agent), WithCredentialSource(staticCred()), WithConfinement([]string{data}, true))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "go-folders")
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": folder}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	launch := func(want string) {
		t.Helper()
		r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, h.m, tenant), "workspace_ref": ref, "transport": "stream-json", "isolation": "native"}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("launch=%d %s", r.code, r.raw)
		}
		runRef := r.body["run_ref"].(string)
		var state string
		waitAttach(t, "Go fixture completion", 150*time.Second, func() bool {
			run, err := m.getRun(t.Context(), model.TenantID(tenant), runRef)
			if err != nil {
				return false
			}
			state = run.State
			return state == stateStopped || state == stateFailed
		})
		proof, _ := os.ReadFile(filepath.Join(folder, "go-proof.log"))
		if state != want {
			t.Fatalf("run state=%s want=%s\n%s", state, want, proof)
		}
		t.Logf("run %s: %s\n%s", runRef, state, proof)
	}
	launch(stateFailed)
	patched := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+ref, admin, map[string]any{"read_only_folders": []string{goRoot, cache}}, tenantHdr(tenant))
	if patched.code != http.StatusOK {
		t.Fatalf("grant Go folders=%d %s", patched.code, patched.raw)
	}
	launch(stateStopped)
	proof, _ := os.ReadFile(filepath.Join(folder, "go-proof.log"))
	if !strings.Contains(string(proof), "ok ") || !strings.Contains(string(proof), "read-only-controls-pass") {
		t.Fatalf("Go did not run with read-only controls: %s", proof)
	}
	if content, _ := os.ReadFile(filepath.Join(dep, "dep.go")); string(content) != depCode {
		t.Fatalf("read-only dependency changed: %s", content)
	}
}

func TestWorkspaceReadOnlyFoldersRequireConfinementBeforeSpawn(t *testing.T) {
	folder, tools := t.TempDir(), t.TempDir()
	m, _, tenant, _ := newRuntimeHarness(t)
	ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: folder, ReadOnlyFolders: []string{tools}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := m.resolveLaunchWorkspace(t.Context(), tenant, ws.WorkspaceRef)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolved.closeReadOnlyHandles)
	spec := m.childSpec(CreateRunParams{Isolation: IsolationNative, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: resolved})
	spec.Program, spec.Args = "/bin/sh", []string{"-c", "echo escaped > \"$1\"", "sh", filepath.Join(tools, "escape")}
	proc, err := NewProcRunner().Launch(t.Context(), spec)
	if err == nil {
		for range proc.Output() {
		}
		code, _ := proc.Wait()
		t.Fatalf("configured read-only folders launched without confinement: exit=%d", code)
	}
	if _, err := os.Stat(filepath.Join(tools, "escape")); !os.IsNotExist(err) {
		t.Fatalf("unconfined session changed a read-only folder: %v", err)
	}
	resolved.readOnlyFolders = nil
	if fallback := m.childSpec(CreateRunParams{Isolation: IsolationNative, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: resolved}); fallback.ConfinementRequired {
		t.Fatal("default-empty workspace changed the existing optional-confinement behavior")
	}
}

func TestWorkspaceReadOnlyFolderRefusesAnOverlappingWritableGrant(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + state.Reason)
	}
	folder, data := t.TempDir(), t.TempDir()
	cache := filepath.Join(folder, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	m, _, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{data}, true))
	ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: folder, ReadOnlyFolders: []string{cache}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := m.resolveLaunchWorkspace(t.Context(), tenant, ws.WorkspaceRef)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolved.closeReadOnlyHandles)
	spec := m.childSpec(CreateRunParams{Isolation: IsolationNative, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: resolved})
	spec.Program, spec.Args = "/bin/sh", []string{"-c", "echo escaped > cache/escape"}
	proc, err := NewProcRunner().Launch(t.Context(), spec)
	if err == nil {
		for range proc.Output() {
		}
		code, _ := proc.Wait()
		t.Fatalf("overlapping grant started a child: exit=%d", code)
	}
	if !strings.Contains(err.Error(), "would be writable") {
		t.Fatalf("conflict was not explained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "escape")); !os.IsNotExist(err) {
		t.Fatalf("conflicting session changed its read-only folder: %v", err)
	}
}
