// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// What the module hands the container runner: the mounts that stand in for the native
// child's Landlock policy, and the launches it refuses because a mount cannot carry
// them. The runner is tested apart, against a stand-in CLI (container_runner_test.go).

func openWorkspace(t *testing.T, m *Module, tenant model.TenantID, params CreateWorkspaceParams) *resolvedWorkspace {
	t.Helper()
	ws, err := m.createWorkspace(t.Context(), tenant, params)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := m.resolveLaunchWorkspace(t.Context(), tenant, ws.WorkspaceRef)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolved.closeReadOnlyHandles)
	return resolved
}

func hasBind(binds []ContainerBind, host, target string, readOnly bool) bool {
	return slices.ContainsFunc(binds, func(b ContainerBind) bool {
		return b.HostPath == host && b.Target == target && b.ReadOnly == readOnly
	})
}

func TestChildSpecGivesAContainerItsMountsAndNoLandlockPolicy(t *testing.T) {
	folder, vendor, config, user := t.TempDir(), "", t.TempDir(), t.TempDir()
	gitFixture(t, folder, "init", "-q")
	if err := os.MkdirAll(filepath.Join(folder, ".git", "hooks"), 0o755); err != nil { // a git without templates makes none
		t.Fatal(err)
	}
	vendor = filepath.Join(folder, "vendor")
	if err := os.Mkdir(vendor, 0o755); err != nil {
		t.Fatal(err)
	}
	managed := t.TempDir()
	useClaudeManagedSettingsDir(t, managed)
	m, _, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{t.TempDir()}, false))
	ws := openWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: folder, MountMode: mountRW, ReadOnlyFolders: []string{vendor}})
	p := CreateRunParams{Isolation: IsolationContainer, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude, ConfigHome: config, UserHome: user}}

	spec := m.childSpec(p, childDecision{ws: ws})

	if spec.Confinement != nil || spec.ConfinementMounts.ReadOnly != nil || spec.ConfinementFiles != nil {
		t.Errorf("a container got a Landlock policy computed from its in-container path: %+v", spec.Confinement)
	}
	if spec.Dir != "/workspace" || spec.Workspace == nil || spec.Workspace.HostPath != ws.rootReal || spec.Workspace.ContainerTarget != "/workspace" || spec.Workspace.ReadOnly {
		t.Errorf("workspace = %+v dir %q, want the folder mounted read-write at /workspace", spec.Workspace, spec.Dir)
	}
	hooks, gitConfig := filepath.Join(realPath(t, folder), ".git", "hooks"), filepath.Join(realPath(t, folder), ".git", "config")
	for _, want := range []ContainerBind{
		{HostPath: config, Target: config},
		{HostPath: user, Target: user},
		{HostPath: realPath(t, vendor), Target: "/workspace/vendor", ReadOnly: true},
		{HostPath: hooks, Target: "/workspace/.git/hooks", ReadOnly: true},
		{HostPath: gitConfig, Target: "/workspace/.git/config", ReadOnly: true},
	} {
		if !slices.Contains(spec.Binds, want) {
			t.Errorf("binds lack %+v:\n%+v", want, spec.Binds)
		}
	}
	if !slices.Contains(spec.Binds, ContainerBind{HostPath: managed, Target: "/etc/claude-code", ReadOnly: true, NoRelabel: true}) {
		t.Errorf("the host's Claude Code managed settings are not what the container reads:\n%+v", spec.Binds)
	}
	for _, bind := range spec.Binds {
		if _, err := os.Lstat(bind.HostPath); err != nil {
			t.Errorf("bind %+v names a host path that does not exist (podman refuses it, Docker would create a directory in the folder)", bind)
		}
	}
}

func TestChildSpecKeepsANativeLaunchExactlyAsItWas(t *testing.T) {
	folder := t.TempDir()
	m, _, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{t.TempDir()}, false))
	ws := openWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: folder, MountMode: mountRW})
	spec := m.childSpec(CreateRunParams{Isolation: IsolationNative, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: ws})
	if spec.Binds != nil {
		t.Errorf("a native launch carries container mounts: %+v", spec.Binds)
	}
	if spec.Confinement == nil || !slices.Contains(spec.Confinement.ReadWrite, ws.rootReal) {
		t.Errorf("a native launch lost its Landlock policy: %+v", spec.Confinement)
	}
}

func TestChildSpecMountsAReadOnlySessionsFolderReadOnly(t *testing.T) {
	folder := t.TempDir()
	gitFixture(t, folder, "init", "-q")
	m, _, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{t.TempDir()}, false))
	ws := openWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: folder, MountMode: mountRW})
	spec := m.childSpec(CreateRunParams{Isolation: IsolationContainer, PermissionMode: "plan", ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: ws})
	if spec.Workspace == nil || !spec.Workspace.ReadOnly {
		t.Fatalf("a read-only session's folder is mounted writable: %+v", spec.Workspace)
	}
	for _, bind := range spec.Binds {
		if !bind.ReadOnly && pathsOverlap(bind.HostPath, ws.rootReal) {
			t.Errorf("a writable mount reopens the read-only folder: %+v", bind)
		}
	}
}

func TestChildSpecMountsTheRunsOwnDirectoryWhenThereIsNoWorkspace(t *testing.T) {
	m, _, _, _ := newRuntimeHarness(t)
	dir := t.TempDir()
	spec := m.childSpec(CreateRunParams{Isolation: IsolationContainer, WorkspaceDir: dir, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{})
	if spec.Dir != dir || !hasBind(spec.Binds, dir, dir, false) {
		t.Errorf("dir %q binds %+v, want the run's own directory mounted at its own path", spec.Dir, spec.Binds)
	}
}

func TestContainerRefusal(t *testing.T) {
	data := t.TempDir()
	sibling := data + "2" // shares data's name as a prefix and is not inside it
	inside := filepath.Join(data, "accounts", "home")
	for _, dir := range []string{inside, sibling} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })
	m, _, _, _ := newRuntimeHarness(t, WithConfinement([]string{data}, false))
	ws := func(root string) *resolvedWorkspace {
		return &resolvedWorkspace{rootReal: root, containerTgt: "/workspace"}
	}

	type launch struct {
		p CreateRunParams
		d childDecision
	}
	refused := map[string]struct {
		launch
		want *runErr
	}{
		"a folder that contains the engine's data": {launch{CreateRunParams{}, childDecision{ws: ws(filepath.Dir(data))}}, containerMountRefusal},
		"the engine's data itself":                 {launch{CreateRunParams{}, childDecision{ws: ws(data)}}, containerMountRefusal},
		"a home that contains the engine's data":   {launch{CreateRunParams{ProviderHome: &ProviderHomeSnapshot{ConfigHome: filepath.Dir(data), UserHome: t.TempDir()}}, childDecision{ws: ws(t.TempDir())}}, containerMountRefusal},
		"the host's root":                          {launch{CreateRunParams{}, childDecision{ws: ws("/")}}, containerMountRefusal},
		"/run":                                     {launch{CreateRunParams{}, childDecision{ws: ws("/run")}}, containerMountRefusal},
		"/proc":                                    {launch{CreateRunParams{}, childDecision{ws: ws("/proc")}}, containerMountRefusal},
		"/sys":                                     {launch{CreateRunParams{}, childDecision{ws: ws("/sys")}}, containerMountRefusal},
		"/dev":                                     {launch{CreateRunParams{}, childDecision{ws: ws("/dev")}}, containerMountRefusal},
		"a folder inside /run, where the runtime's socket lives": {launch{CreateRunParams{}, childDecision{ws: ws("/run/user/1000/podman")}}, containerMountRefusal},
		"a home inside /run":                      {launch{CreateRunParams{ProviderHome: &ProviderHomeSnapshot{ConfigHome: "/run/user/1000/x", UserHome: t.TempDir()}}, childDecision{ws: ws(t.TempDir())}}, containerMountRefusal},
		"a run directory that is the host's root": {launch{CreateRunParams{WorkspaceDir: "/"}, childDecision{}}, containerMountRefusal},
	}
	for name, c := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			err := m.containerRefusal(c.p, c.d)
			var re *runErr
			if !errors.As(err, &re) || re.status != http.StatusUnprocessableEntity || re != c.want {
				t.Fatalf("err = %v, want the 422 %q", err, c.want.msg)
			}
		})
	}
	allowed := map[string]launch{
		"an ordinary folder": {CreateRunParams{}, childDecision{ws: ws(t.TempDir())}},
		"a folder whose name only starts like the engine's data": {CreateRunParams{}, childDecision{ws: ws(sibling)}},
		"an account home inside the engine's data":               {CreateRunParams{ProviderHome: &ProviderHomeSnapshot{ConfigHome: inside, UserHome: inside}}, childDecision{ws: ws(t.TempDir())}},
		"the run's own directory inside the data":                {CreateRunParams{WorkspaceDir: inside}, childDecision{}},
	}
	for name, c := range allowed {
		t.Run("allows "+name, func(t *testing.T) {
			if err := m.containerRefusal(c.p, c.d); err != nil {
				t.Fatalf("err = %v; a native session may have this, so a container may", err)
			}
		})
	}
	t.Run("allows the engine user's own home, which is never mounted whole", func(t *testing.T) {
		home := engineUserHome()
		if home == "" {
			t.Skip("the engine user has no home directory here")
		}
		mh, _, _, _ := newRuntimeHarness(t, WithConfinement([]string{filepath.Join(home, ".olivares-test-data")}, false))
		p := CreateRunParams{ProviderHome: &ProviderHomeSnapshot{ConfigHome: t.TempDir(), UserHome: home}}
		if err := mh.containerRefusal(p, childDecision{ws: ws(t.TempDir())}); err != nil {
			t.Fatalf("err = %v; only the configuration home of such a profile is granted, as natively", err)
		}
	})
}

// A container run goes through launchChild, which is where the refusal is applied: a
// refusal that holds when called directly proves nothing if the launch never calls it.
func TestCreateRefusesAContainerRunThatAMountCannotCarryAndStartsNothing(t *testing.T) {
	data := t.TempDir()
	cases := map[string]struct {
		rootOf    func() string
		wantReady bool
	}{
		"a folder with the engine's data in it": {rootOf: func() string { return filepath.Dir(data) }},
		"an ordinary folder":                    {rootOf: func() string { return t.TempDir() }, wantReady: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{initSID: "s"}
			m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()), WithConfinement([]string{data}, false))
			ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: c.rootOf(), Actor: actorU, ActorKind: actorKindU})
			if err != nil {
				t.Skipf("registration already refuses this folder: %v", err)
			}
			_, err = createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{
				Transport: TransportStreamJSON, Isolation: IsolationContainer, WorkspaceRef: ws.WorkspaceRef,
				Actor: actorU, ActorKind: actorKindU,
			})
			if c.wantReady {
				if err != nil || runner.lastSpec().Isolation != IsolationContainer {
					t.Fatalf("an ordinary container run: err %v spec %+v", err, runner.lastSpec())
				}
				return
			}
			if !isStatus(err, http.StatusUnprocessableEntity) {
				t.Fatalf("err = %v, want a 422", err)
			}
			if len(runner.specs) != 0 {
				t.Fatalf("a refused container run still reached the runner: %+v", runner.specs)
			}
		})
	}
}

// A server that governs tool calls with hooks (the stock engine does) must not lose them
// in a container: the hook program and the run's settings are mounted read-only, and the
// endpoint and credential travel in the environment as they do natively.
func TestCreateCarriesTheToolCallHooksIntoAContainer(t *testing.T) {
	dataDir := t.TempDir()
	useClaudeManagedSettingsDir(t, t.TempDir())
	engine := filepath.Join(t.TempDir(), "olivares")
	if err := os.WriteFile(engine, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	gate := &spyGate{inner: LaunchDecision{Allowed: true, InjectEnv: []EnvVar{
		{Name: envHookPEPURL, Value: "https://127.0.0.1:7/v1/hooks"},
		{Name: envHookPEPToken, Value: "pep-token"},
	}}}
	runner := &fakeRunner{initSID: "s"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate),
		WithClaudeHookPEP(dataDir, engine), WithConfinement([]string{dataDir}, false))
	dto, err := createProfiledTestRun(t, m, t.Context(), tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationContainer, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatalf("a hook-governed container run was refused: %v", err)
	}
	spec := runner.lastSpec()
	runDir := filepath.Join(dataDir, "run", dto.RunRef)
	if !hasBind(spec.Binds, runDir, runDir, true) {
		t.Errorf("the run's hook settings are not mounted read-only: %+v", spec.Binds)
	}
	if !hasBind(spec.Binds, engine, engine, true) {
		t.Errorf("the hook program is not mounted: %+v", spec.Binds)
	}
	if i := slices.Index(spec.Args, claudeSettingsFlag); i < 0 || spec.Args[i+1] != filepath.Join(runDir, "pep-settings.json") {
		t.Errorf("the launch does not carry the hook settings: %q", spec.Args)
	}
	if !slices.ContainsFunc(spec.Env, func(e EnvVar) bool { return e.Name == envHookPEPURL }) {
		t.Errorf("the hook endpoint did not reach the environment")
	}
	// A hook that cannot run (another architecture, no shell) must block the tool call, not
	// let it through: Claude Code proceeds on every hook failure except exit 2.
	settings := mustRead(t, filepath.Join(runDir, "pep-settings.json"))
	if !strings.Contains(settings, "|| exit 2") {
		t.Errorf("the container's hook command does not fail closed: %s", settings)
	}
	if !hasBind(spec.Binds, engine, engine, true) || !slices.Contains(spec.Binds, ContainerBind{HostPath: engine, Target: engine, ReadOnly: true, NoRelabel: true}) {
		t.Errorf("the engine's program must be mounted without relabelling: %+v", spec.Binds)
	}

	// The same launch natively is unchanged: no appended exit status, no container mounts.
	native, _, nativeTenant, _ := newRuntimeHarness(t, WithRunner(runner), WithCredentialSource(staticCred()), WithLaunchGate(gate),
		WithClaudeHookPEP(dataDir, engine), WithConfinement([]string{dataDir}, false))
	ndto, err := createProfiledTestRun(t, native, t.Context(), nativeTenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatalf("native run: %v", err)
	}
	if body := mustRead(t, filepath.Join(dataDir, "run", ndto.RunRef, "pep-settings.json")); strings.Contains(body, "exit 2") {
		t.Errorf("a native hook command was changed: %s", body)
	}
}

func TestAllowReadMountsForAContainerAndSkipsWhatTheImageProvides(t *testing.T) {
	dir := t.TempDir()
	container := LaunchSpec{Isolation: IsolationContainer}
	container.AllowRead(dir)
	container.AllowRead("/usr/lib/node_modules")
	container.AllowRead("")
	if len(container.Binds) != 1 || !hasBind(container.Binds, dir, dir, true) {
		t.Errorf("container binds = %+v, want only %s, read-only (a mount over /usr would replace the image's own programs)", container.Binds, dir)
	}
	native := LaunchSpec{Isolation: IsolationNative}
	native.AllowRead(dir)
	if native.Binds != nil {
		t.Errorf("a native launch got container mounts: %+v", native.Binds)
	}
}
