// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// The real launch path, with only the agent replaced by a shell: a session in a
// write-without-delete workspace creates and changes files and is refused every
// removal, rename and move, while the same shell in a read-write workspace is not.
func TestWriteNoDeleteSessionChangesFilesAndCannotRemoveOrMoveThem(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + state.Reason)
	}
	const script = `
r=report.txt
step() { n=$1; shift; if "$@" 2>/dev/null; then echo "$n=ok" >> $r; else echo "$n=denied" >> $r; fi; }
step create  sh -c 'printf new > new.txt'
step modify  sh -c 'printf changed > keep.txt'
step rename  /bin/mv keep.txt renamed.txt
step unlink  /bin/rm new.txt
step rmdir   /bin/rmdir sub
`
	for _, tc := range []struct {
		mode string
		want string
	}{
		{mountNoDelete, "create=ok\nmodify=ok\nrename=denied\nunlink=denied\nrmdir=denied\n"},
		{mountRW, "create=ok\nmodify=ok\nrename=ok\nunlink=ok\nrmdir=ok\n"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			folder, data := t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(folder, "sub"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(folder, "keep.txt"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			m, _, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{data}, true))
			ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: folder, MountMode: tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := m.resolveLaunchWorkspace(t.Context(), tenant, ws.WorkspaceRef)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(resolved.closeReadOnlyHandles)
			spec := m.childSpec(CreateRunParams{Isolation: IsolationNative, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: resolved})
			spec.Program, spec.Args = "/bin/sh", []string{"-c", script}
			proc, err := NewProcRunner().Launch(t.Context(), spec)
			if err != nil {
				t.Fatalf("launch refused: %v", err)
			}
			for range proc.Output() {
			}
			if code, err := proc.Wait(); err != nil || code != 0 {
				t.Fatalf("exit=%d err=%v", code, err)
			}
			report, _ := os.ReadFile(filepath.Join(folder, "report.txt"))
			if string(report) != tc.want {
				t.Fatalf("report:\n%s\nwant:\n%s", report, tc.want)
			}
			if tc.mode == mountNoDelete {
				for _, name := range []string{"keep.txt", "new.txt", "sub"} {
					if _, err := os.Stat(filepath.Join(folder, name)); err != nil {
						t.Errorf("%s is gone after refused removals: %v", name, err)
					}
				}
				if b, _ := os.ReadFile(filepath.Join(folder, "keep.txt")); !strings.HasPrefix(string(b), "changed") {
					t.Errorf("keep.txt = %q, want the child's change", b)
				}
			}
		})
	}
}

// The session console's Git is the engine's own, run for the person with hooks off.
// It stages and commits in a write-without-delete folder: it renames lock files,
// which the agent may not.
func TestWriteNoDeleteWorkspaceCommitsThroughTheSessionGitPanel(t *testing.T) {
	g := gitPanelFixture(t)
	g.workspace = &resolvedWorkspace{mountMode: mountNoDelete, rootReal: g.dir}
	if !g.writable() {
		t.Fatal("the session Git panel treats a write-without-delete folder as read-only")
	}
	if err := os.WriteFile(filepath.Join(g.dir, "panel.txt"), []byte("panel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := g.stage(t.Context(), []string{"panel.txt"}, false); err != nil {
		t.Fatalf("stage: %v", err)
	}
	principal := auth.Principal{Kind: auth.KindUser, DisplayName: "Session Author", Email: "session@example.invalid"}
	if err := g.commit(t.Context(), "Add the panel file", principal); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := worktreeGitOut(t, g.dir, "log", "-1", "--format=%s"); got != "Add the panel file" {
		t.Fatalf("last commit = %q", got)
	}
}

// The agent's own git works in a no-delete folder that is a Git repository: Git
// rewrites its index by renaming index.lock over it, so the repository's own metadata
// directory keeps full write rights while the working files do not. A routine
// git status (which refreshes the index after a file's timestamp changed), git add
// and git commit finish and leave no index.lock behind; the person's Git panel then
// stages and commits; and the working files still cannot be removed.
func TestWriteNoDeleteAgentGitWorksAndThePanelStillCommits(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	g := gitPanelFixture(t) // skips where Landlock is unavailable
	linked := filepath.Join(t.TempDir(), "linked")
	worktreeGitOut(t, g.dir, "worktree", "add", "-q", "-b", "linked", linked)
	linked, err = filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	const script = `
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 LC_ALL=C
touch -d 2001-01-01 README
step() { n=$1; shift; "$@" >/dev/null 2>$n.err; echo "$n=$?" >> report.txt; }
step status git status
step diff git diff
echo agent > agent.txt
step add git add agent.txt
step commit git -c user.name=Agent -c user.email=agent@example.invalid -c commit.gpgsign=false commit -qm agent
step remove rm README
`
	for _, tc := range []struct {
		name string
		dir  string
	}{{"clone", g.dir}, {"linked worktree", linked}} {
		t.Run(tc.name, func(t *testing.T) {
			data := t.TempDir()
			m, _, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{data}, true))
			ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: tc.dir, MountMode: mountNoDelete})
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := m.resolveLaunchWorkspace(t.Context(), tenant, ws.WorkspaceRef)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(resolved.closeReadOnlyHandles)
			spec := m.childSpec(CreateRunParams{Isolation: IsolationNative, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: resolved})
			spec.Program, spec.Args = "/bin/sh", []string{"-c", "PATH=" + filepath.Dir(git) + ":$PATH\n" + script}
			proc, err := NewProcRunner().Launch(t.Context(), spec)
			if err != nil {
				t.Fatalf("launch refused: %v", err)
			}
			for range proc.Output() {
			}
			if code, err := proc.Wait(); err != nil || code != 0 {
				t.Fatalf("exit=%d err=%v", code, err)
			}
			report, _ := os.ReadFile(filepath.Join(tc.dir, "report.txt"))
			for _, line := range []string{"status=0", "diff=0", "add=0", "commit=0"} {
				if !strings.Contains(string(report), line+"\n") {
					errs, _ := filepath.Glob(filepath.Join(tc.dir, "*.err"))
					var detail []byte
					for _, f := range errs {
						b, _ := os.ReadFile(f)
						detail = append(detail, filepath.Base(f)+": "+string(b)...)
					}
					t.Fatalf("agent git: want %s in\n%s%s", line, report, detail)
				}
			}
			// The working files are still protected: the removal was refused.
			if !strings.Contains(string(report), "remove=1\n") {
				t.Errorf("the agent removed a working file:\n%s", report)
			}
			if _, err := os.Stat(filepath.Join(tc.dir, "README")); err != nil {
				t.Errorf("README is gone: %v", err)
			}
			lock := filepath.Join(worktreeGitOut(t, tc.dir, "rev-parse", "--absolute-git-dir"), "index.lock")
			if _, err := os.Stat(lock); !os.IsNotExist(err) {
				t.Errorf("the agent's git left %s behind (stat err=%v)", lock, err)
			}
			if got := worktreeGitOut(t, tc.dir, "log", "-1", "--format=%s"); got != "agent" {
				t.Errorf("last commit = %q, want the agent's", got)
			}
			// The person's Git panel stages and commits after the agent's git ran.
			panel := sessionGit{module: m, dir: tc.dir, preset: PresetEditsAndCommands, workspace: resolved}
			if err := os.WriteFile(filepath.Join(tc.dir, "panel.txt"), []byte("panel\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := panel.stage(t.Context(), []string{"panel.txt"}, false); err != nil {
				t.Fatalf("panel stage after the agent's git: %v", err)
			}
			principal := auth.Principal{Kind: auth.KindUser, DisplayName: "Session Author", Email: "session@example.invalid"}
			if err := panel.commit(t.Context(), "Add the panel file", principal); err != nil {
				t.Fatalf("panel commit after the agent's git: %v", err)
			}
			if got := worktreeGitOut(t, tc.dir, "log", "-1", "--format=%s"); got != "Add the panel file" {
				t.Errorf("last commit = %q", got)
			}
		})
	}
}

// Without confinement a no-delete promise would be a label. The launch is refused,
// not run unconfined, when the node has no confinement policy.
func TestWriteNoDeleteSessionDoesNotStartWithoutConfinement(t *testing.T) {
	folder := t.TempDir()
	m, _, tenant, _ := newRuntimeHarness(t)
	ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: folder, MountMode: mountNoDelete})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := m.resolveLaunchWorkspace(t.Context(), tenant, ws.WorkspaceRef)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolved.closeReadOnlyHandles)
	spec := m.childSpec(CreateRunParams{Isolation: IsolationNative, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: resolved})
	spec.Program, spec.Args = "/bin/sh", []string{"-c", "echo escaped > escape.txt"}
	proc, err := NewProcRunner().Launch(t.Context(), spec)
	if err == nil {
		for range proc.Output() {
		}
		_, _ = proc.Wait()
		t.Fatal("a write-without-delete session started without confinement")
	}
	if _, err := os.Stat(filepath.Join(folder, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("the refused session still ran: %v", err)
	}
}

// A read-only folder under a write-without-delete workspace would be changed through
// the workspace's own rights; the launch is refused, as under a read-write workspace.
func TestWriteNoDeleteWorkspaceRefusesAReadOnlyFolderInsideIt(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock || state.ABI < 3 {
		t.Skip("host cannot enforce read-only folders: " + state.String())
	}
	folder, data := t.TempDir(), t.TempDir()
	vendor := filepath.Join(folder, "vendor")
	if err := os.Mkdir(vendor, 0o700); err != nil {
		t.Fatal(err)
	}
	m, _, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{data}, true))
	ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{RootPath: folder, MountMode: mountNoDelete, ReadOnlyFolders: []string{vendor}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := m.resolveLaunchWorkspace(t.Context(), tenant, ws.WorkspaceRef)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolved.closeReadOnlyHandles)
	spec := m.childSpec(CreateRunParams{Isolation: IsolationNative, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}, childDecision{ws: resolved})
	spec.Program, spec.Args = "/bin/sh", []string{"-c", "echo escaped > vendor/escape"}
	proc, err := NewProcRunner().Launch(t.Context(), spec)
	if err == nil {
		for range proc.Output() {
		}
		_, _ = proc.Wait()
		t.Fatal("a read-only folder under a no-delete workspace started a session")
	}
	if !strings.Contains(err.Error(), "would be writable") {
		t.Fatalf("the conflict was not explained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vendor, "escape")); !os.IsNotExist(err) {
		t.Fatalf("the refused session changed the read-only folder: %v", err)
	}
}
