// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// A session child launched with a confinement policy writes its folder and its
// own TMPDIR, cannot read the engine's sealing key, and its TMPDIR is removed
// when it exits.
func TestProcRunnerConfinedChildCannotReadTheEngineKey(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + s.Reason)
	}
	root := t.TempDir()
	data, folder, bin := filepath.Join(root, "data"), filepath.Join(root, "folder"), filepath.Join(root, "bin")
	for _, d := range []string{data, folder, bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(data, "secret-store.key")
	if err := os.WriteFile(key, []byte("sealing-key-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(bin, "agent")
	script := `#!/bin/sh
echo "tmpdir=$TMPDIR"
echo "home=$HOME"
cat "$1" && echo KEY-READ || echo key-denied
echo edit > notes.txt && echo folder-ok
echo scratch > "$TMPDIR/scratch" && echo tmp-ok
`
	if err := os.WriteFile(agent, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{
		Program: agent, Args: []string{key}, Dir: folder, WaitDelay: 2 * time.Second,
		Confinement: &confine.Policy{ReadWrite: []string{folder}, Protect: []string{data}},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if state, ok := confinementOf(proc); !ok || state.Mode != confine.ModeLandlock {
		t.Fatalf("confinement = %+v, %v", state, ok)
	}
	var out strings.Builder
	for f := range proc.Output() {
		out.Write(f.Data)
		out.WriteByte('\n')
	}
	if exit, _ := proc.Wait(); exit != 0 {
		t.Fatalf("exit %d:\n%s", exit, out.String())
	}
	got := out.String()
	for _, want := range []string{"key-denied", "folder-ok", "tmp-ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "KEY-READ") || strings.Contains(got, "sealing-key-fixture") {
		t.Fatalf("confined child read the engine key:\n%s", got)
	}
	tmp := ""
	for _, line := range strings.Split(got, "\n") {
		if v, ok := strings.CutPrefix(line, "tmpdir="); ok {
			tmp = v
		}
	}
	if tmp == "" || strings.HasPrefix(tmp, folder) {
		t.Fatalf("child TMPDIR = %q", tmp)
	}
	if !strings.Contains(got, "home="+tmp+"\n") {
		t.Errorf("a confined child with no HOME should get its TMPDIR as HOME:\n%s", got)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("session TMPDIR %s survives the child: %v", tmp, err)
	}
}

// Without a policy the runner behaves as before and says the child is not confined.
func TestProcRunnerWithoutPolicyRunsUnconfined(t *testing.T) {
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{Program: "true", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for range proc.Output() {
	}
	_, _ = proc.Wait()
	if state, ok := confinementOf(proc); !ok || state.Mode != confine.ModeNone {
		t.Fatalf("confinement = %+v, %v", state, ok)
	}
}

func TestSessionConfinementGrantsFolderAndHomes(t *testing.T) {
	m := &Module{Dependencies: &Dependencies{}, rt: newRuntimeState(nil)}
	if p := m.sessionConfinement("/w", nil, PresetNone); p != nil {
		t.Fatalf("unwired node confines: %+v", p)
	}
	WithConfinement([]string{"/data", "relative"}, true)(m)
	p := m.sessionConfinement("/w", &ProviderHomeSnapshot{ConfigHome: "/data/accounts/a/config", UserHome: "/data/accounts/a/user"}, PresetAsk)
	if p == nil || strings.Join(p.ReadWrite, ",") != "/w,/data/accounts/a/config,/data/accounts/a/user" ||
		strings.Join(p.Protect, ",") != "/data" || !m.rt.confineRequired {
		t.Fatalf("policy = %+v required=%v", p, m.rt.confineRequired)
	}
	spec := LaunchSpec{Confinement: p}
	spec.AllowRead("/data/run/r1")
	if strings.Join(spec.Confinement.ReadOnly, ",") != "/data/run/r1" {
		t.Fatalf("AllowRead = %v", spec.Confinement.ReadOnly)
	}
}

// The engine user's own home is never granted whole because a profile names it
// as HOME; the tool's configuration home is.
func TestSessionConfinementDoesNotOpenTheEngineUsersHome(t *testing.T) {
	m := &Module{Dependencies: &Dependencies{}, rt: newRuntimeState(nil)}
	WithConfinement([]string{"/data"}, false)(m)
	saved := engineUserHome
	engineUserHome = func() string { return "/home/engine" }
	defer func() { engineUserHome = saved }()
	p := m.sessionConfinement("/home/engine/project", &ProviderHomeSnapshot{ConfigHome: "/home/engine/.claude", UserHome: "/home/engine"}, PresetNone)
	if got := strings.Join(p.ReadWrite, ","); got != "/home/engine/project,/home/engine/.claude" {
		t.Fatalf("read-write = %s", got)
	}
}

// A read-only session (the "read_only" preset) reads its folder and writes only its
// account homes and its temporary directory: the operating system refuses every
// write to the folder for the whole process tree, whatever the tool's own settings
// say. The other presets keep the folder writable.
func TestReadOnlyPresetSessionCannotWriteItsFolder(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + s.Reason)
	}
	root := t.TempDir()
	data, folder, bin := filepath.Join(root, "data"), filepath.Join(root, "folder"), filepath.Join(root, "bin")
	for _, d := range []string{data, folder, bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(folder, "README"), []byte("project-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(bin, "agent")
	script := `#!/bin/sh
cat README && echo folder-read
(echo edit > notes.txt) 2>/dev/null && echo FOLDER-WRITTEN || echo folder-denied
(sh -c 'echo child > child.txt') 2>/dev/null && echo CHILD-WROTE || echo child-denied
echo scratch > "$TMPDIR/scratch" && echo tmp-ok
`
	if err := os.WriteFile(agent, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	m := &Module{Dependencies: &Dependencies{}, rt: newRuntimeState(nil)}
	WithConfinement([]string{data}, true)(m)
	for _, tc := range []struct {
		preset string
		want   []string
	}{
		{PresetReadOnly, []string{"project-fixture", "folder-read", "folder-denied", "child-denied", "tmp-ok"}},
		{PresetAsk, []string{"folder-read", "FOLDER-WRITTEN", "CHILD-WROTE", "tmp-ok"}},
	} {
		t.Run(tc.preset, func(t *testing.T) {
			_ = os.Remove(filepath.Join(folder, "notes.txt"))
			_ = os.Remove(filepath.Join(folder, "child.txt"))
			proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{
				Program: agent, Dir: folder, WaitDelay: 2 * time.Second,
				Confinement: m.sessionConfinement(folder, nil, tc.preset),
			})
			if err != nil {
				t.Fatalf("launch: %v", err)
			}
			var out strings.Builder
			for f := range proc.Output() {
				out.Write(f.Data)
				out.WriteByte('\n')
			}
			if exit, _ := proc.Wait(); exit != 0 {
				t.Fatalf("exit %d:\n%s", exit, out.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in:\n%s", want, out.String())
				}
			}
			if _, err := os.Stat(filepath.Join(folder, "notes.txt")); tc.preset == PresetReadOnly && !os.IsNotExist(err) {
				t.Fatalf("a read-only session wrote its folder: %v", err)
			}
		})
	}
}

// A read-only session's promise is the operating system's, so it never starts
// unconfined: the launch requires confinement whatever the node's own setting,
// and a node with no confinement wired refuses it before anything is spawned.
// Other presets keep the node's setting.
func TestReadOnlySessionRequiresConfinementBeforeSpawn(t *testing.T) {
	wired := New(WithConfinement([]string{"/fixture-data"}, false))
	for _, tc := range []struct {
		mode     string
		required bool
	}{{"plan", true}, {"default", false}, {"acceptEdits", false}} {
		p := CreateRunParams{PermissionMode: tc.mode, Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceDir: "/fixture-folder", ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}
		if spec := wired.childSpec(p, childDecision{}); spec.ConfinementRequired != tc.required {
			t.Errorf("permission mode %s: confinement required = %v, want %v", tc.mode, spec.ConfinementRequired, tc.required)
		}
	}

	folder := t.TempDir()
	unwired := New()
	p := CreateRunParams{PermissionMode: "plan", Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceDir: folder, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}
	spec := unwired.childSpec(p, childDecision{})
	spec.Program, spec.Args = "/bin/sh", []string{"-c", "echo written > spawned.txt"}
	if proc, err := NewProcRunner().Launch(context.Background(), spec); err == nil {
		for range proc.Output() {
		}
		_, _ = proc.Wait()
		t.Fatal("a read-only session started on a node with no confinement")
	}
	if _, err := os.Stat(filepath.Join(folder, "spawned.txt")); !os.IsNotExist(err) {
		t.Fatalf("the refused read-only launch spawned its program: %v", err)
	}
}

// A writable profile home that overlaps a read-only folder (above it, the same
// directory, or inside it) would reopen the folder, because Landlock grants add
// up. A read-only session does not get such a home writable, so neither the tool
// nor a child it starts can write the folder. A home that does not overlap keeps
// its writes: the tool still keeps its account state.
func TestReadOnlyFolderIsNotWidenedByAnOverlappingProfileHome(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + s.Reason)
	}
	for _, tc := range []struct {
		name  string
		homes func(root, folder string) ProviderHomeSnapshot
	}{
		{"user home above the folder", func(root, folder string) ProviderHomeSnapshot {
			return ProviderHomeSnapshot{UserHome: filepath.Dir(folder), ConfigHome: filepath.Join(root, "config")}
		}},
		{"configuration home is the folder", func(root, folder string) ProviderHomeSnapshot {
			return ProviderHomeSnapshot{UserHome: filepath.Join(root, "user"), ConfigHome: folder}
		}},
		{"configuration home inside the folder", func(root, folder string) ProviderHomeSnapshot {
			return ProviderHomeSnapshot{UserHome: filepath.Join(root, "user"), ConfigHome: filepath.Join(folder, ".tool")}
		}},
		{"homes beside the folder", func(root, folder string) ProviderHomeSnapshot {
			return ProviderHomeSnapshot{UserHome: filepath.Join(root, "user"), ConfigHome: filepath.Join(root, "config")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			data, folder, bin := filepath.Join(root, "data"), filepath.Join(root, "account", "project"), filepath.Join(root, "bin")
			home := tc.homes(root, folder)
			for _, d := range []string{data, folder, bin, home.UserHome, home.ConfigHome} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			agent := filepath.Join(bin, "agent")
			script := "#!/bin/sh\nprintf parent > direct.txt\nsh -c 'printf child > child.txt'\nprintf state > \"$1/state.txt\" && echo state-written\nexit 0\n"
			if err := os.WriteFile(agent, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			m := New(WithConfinement([]string{data}, true))
			p := CreateRunParams{PermissionMode: "plan", Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceDir: folder, ProviderHome: &home}
			spec := m.childSpec(p, childDecision{})
			spec.Program, spec.Args, spec.WaitDelay = agent, []string{home.ConfigHome}, 2*time.Second
			proc, err := NewProcRunner().Launch(context.Background(), spec)
			if err != nil {
				t.Fatalf("launch: %v", err)
			}
			var out strings.Builder
			for f := range proc.Output() {
				out.Write(f.Data)
			}
			_, _ = proc.Wait()
			for _, file := range []string{"direct.txt", "child.txt", "state.txt", filepath.Join(".tool", "state.txt")} {
				if _, err := os.Stat(filepath.Join(folder, file)); !os.IsNotExist(err) {
					t.Errorf("the read-only session wrote %s in its folder", file)
				}
			}
			if tc.name == "homes beside the folder" && !strings.Contains(out.String(), "state-written") {
				t.Errorf("a configuration home beside the folder lost its writes:\n%s", out.String())
			}
		})
	}
}

// The runner's private temporary directory and the directories the helper
// grants by default are writable for every child. Where one of them would lie
// in, or contain, a read-only session's folder, the launch is refused before
// anything starts: nothing is written in the folder. A read-only folder
// elsewhere still launches (TestReadOnlyPresetSessionCannotWriteItsFolder).
func TestReadOnlyFolderIsNotReopenedByRunnerOrDeviceGrants(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + s.Reason)
	}
	for _, tc := range []string{"runner temporary directory inside the folder", "folder under shared memory"} {
		t.Run(tc, func(t *testing.T) {
			root := t.TempDir()
			folder, data := filepath.Join(root, "folder"), filepath.Join(root, "data")
			if tc == "folder under shared memory" {
				var err error
				if folder, err = os.MkdirTemp("/dev/shm", "arch-readonly-"); err != nil {
					t.Skip("no /dev/shm here: " + err.Error())
				}
				t.Cleanup(func() { _ = os.RemoveAll(folder) })
			}
			for _, d := range []string{folder, data} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc == "runner temporary directory inside the folder" {
				t.Setenv("TMPDIR", folder)
			}
			m := New(WithConfinement([]string{data}, true))
			p := CreateRunParams{PermissionMode: "plan", Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceDir: folder, ProviderHome: &ProviderHomeSnapshot{Driver: providerDriverClaude}}
			spec := m.childSpec(p, childDecision{})
			spec.Program, spec.Args = "/bin/sh", []string{"-c", `printf x > direct.txt; printf x > "$TMPDIR/state"`}
			proc, err := NewProcRunner().Launch(context.Background(), spec)
			if err == nil {
				for range proc.Output() {
				}
				_, _ = proc.Wait()
				t.Fatal("a launch whose writable grants reopen the read-only folder was started")
			}
			if !strings.Contains(err.Error(), "read-only path") {
				t.Fatalf("refusal = %v, want it to name the read-only path", err)
			}
			entries, _ := os.ReadDir(folder)
			if len(entries) != 0 {
				t.Fatalf("the refused launch left %d entries in the read-only folder", len(entries))
			}
		})
	}
}
