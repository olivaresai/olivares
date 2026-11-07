// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// The scope runs the program unchanged: its environment gains and loses nothing,
// and the ceilings reach systemd-run in its own grammar. A host that cannot make
// the scope keeps the command as it was and says why.
func TestSessionScopeKeepsTheProgramsEnvironment(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	// A stand-in for systemd-run: it records its arguments, adds what a scope adds to
	// the environment, and runs the command after "--" the way --scope does.
	stub := "#!/bin/sh\nd=$(dirname \"$0\")\necho \"$*\" >> \"$d/calls\"\nenv >> \"$d/seen\"\n[ ! -s \"$d/fail\" ] || { cat \"$d/fail\" >&2; exit 1; }\n" +
		"while [ \"$1\" != -- ]; do shift; done; shift\nINVOCATION_ID=scope exec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "systemd-run"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirWithBus(t))
	show := `printf '%s|%s|%s' "${XDG_RUNTIME_DIR-unset}" "${INVOCATION_ID-unset}" "${LD_LIBRARY_PATH-unset}"`
	run := func(env ...string) (string, *exec.Cmd, string) {
		cmd := exec.Command("/bin/sh", "-c", show)
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
		note := applySessionScope(cmd, "1G", "512", true)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the wrapped program: %v\nargv %q\noutput %s", err, cmd.Args, out)
		}
		return string(out), cmd, note
	}

	if got, _, note := run(); got != "unset|unset|unset" || note != "" {
		t.Fatalf("a program without XDG_RUNTIME_DIR saw %q (note %q), want unset|unset|unset", got, note)
	}
	if got, _, _ := run("XDG_RUNTIME_DIR=/run/private", "LD_LIBRARY_PATH=/opt/lib"); got != "/run/private|unset|/opt/lib" {
		t.Fatalf("a program with its own XDG_RUNTIME_DIR and LD_LIBRARY_PATH saw %q", got)
	}
	// The stages before the session's confinement run without what steers them.
	if seen, err := os.ReadFile(filepath.Join(dir, "seen")); err != nil || strings.Contains(string(seen), "LD_LIBRARY_PATH") {
		t.Fatalf("systemd-run ran with the session's LD_LIBRARY_PATH: %q (%v)", seen, err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--scope", "MemoryMax=1G", "MemorySwapMax=0", "TasksMax=512"} {
		if !strings.Contains(string(calls), want) {
			t.Fatalf("systemd-run was called with %q, want %q", calls, want)
		}
	}
	if user := os.Geteuid() != 0; user != strings.Contains(string(calls), "--user") {
		t.Fatalf("--user is for a person's engine, not root's: euid %d, calls %q", os.Geteuid(), calls)
	}

	if err := os.WriteFile(filepath.Join(dir, "fail"), []byte("Failed to connect to user scope bus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, cmd, note := run()
	if cmd.Path != "/bin/sh" || got != "unset|unset|unset" || !strings.Contains(note, "Failed to connect to user scope bus") {
		t.Fatalf("a host that cannot make the scope: path %q, output %q, note %q", cmd.Path, got, note)
	}
}

// A confined session must not reach the user manager that makes its scope: the
// manager's bus socket sits in the engine user's runtime directory, Landlock does
// not govern connect() to a pathname socket, and a unit started through it would
// run outside the session's confinement. The session sees an empty directory there.
func TestConfinedSessionInAScopeCannotSeeTheRuntimeDirectory(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + state.Reason)
	}
	if hostLacksMounts(t) {
		t.Skip("this host gives a session no user and mount namespace")
	}
	bin := t.TempDir()
	stub := "#!/bin/sh\nwhile [ \"$1\" != -- ]; do shift; done; shift\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runtimeDir := runtimeDirWithBus(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	folder, data := t.TempDir(), t.TempDir()
	m := New(WithConfinement([]string{data}, true))
	proc, err := NewProcRunner().Launch(t.Context(), LaunchSpec{
		Program: "/bin/sh", Args: []string{"-c", `if [ -e "$1/bus" ]; then echo bus=visible; else echo bus=hidden; fi`, "sh", runtimeDir},
		Dir:         folder,
		Confinement: m.sessionConfinement(folder, nil, PresetAsk), ConfinementRequired: true, WaitDelay: time.Second,
		MemoryMax: "1G", TasksMax: "512",
	})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for frame := range proc.Output() {
		out.Write(frame.Data)
		out.WriteByte('\n')
	}
	if code, err := proc.Wait(); code != 0 || err != nil {
		t.Fatalf("the session: exit=%d err=%v\n%s", code, err, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "bus=hidden" {
		t.Fatalf("the session saw %q: its scope's bus socket is reachable from the confined session", got)
	}
}

// runtimeDirWithBus is a stand-in for the engine user's runtime directory with a user
// manager: a directory that holds a bus.
func runtimeDirWithBus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bus"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The user bus is the account's, not the launch's: a child that gets no scope (a
// person's terminal here; a setup command, an MCP server, a tool read and the Git
// runs are built the same way) must not reach it either. Withholding the scope does not
// take the bus away.
func TestConfinedTerminalCannotSeeTheUserBus(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + state.Reason)
	}
	if hostLacksMounts(t) {
		t.Skip("this host gives a child no user and mount namespace")
	}
	runtimeDir := runtimeDirWithBus(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	folder, data := t.TempDir(), t.TempDir()
	m := New(WithConfinement([]string{data}, true))
	spec := m.terminalSpec(&resolvedWorkspace{rootReal: folder})
	spec.Program, spec.Args = "/bin/sh", []string{"-c", `if [ -e "$1/bus" ]; then echo bus=visible; else echo bus=hidden; fi`, "sh", runtimeDir}
	cmd, env, _, release, err := (&procRunner{}).command(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cmd.Dir, cmd.Env = folder, sanitizedEnv(spec.EnvAllow, env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the terminal's shell: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "bus=hidden" {
		t.Fatalf("the shell saw %q: the user bus is reachable from a confined terminal", got)
	}
}

// The D-Bus bus is not the user manager's only door: `systemd-run --user` without --wait
// and `systemctl --user` connect to <runtime dir>/systemd/private and need no bus. A host
// without the D-Bus user session (a minimal Debian image) has a user manager, after
// `loginctl enable-linger`, and no bus socket. The child must not see that socket either.
func TestConfinedTerminalCannotSeeTheUserManagersPrivateSocket(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + state.Reason)
	}
	if hostLacksMounts(t) {
		t.Skip("this host gives a child no user and mount namespace")
	}
	runtimeDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(runtimeDir, "systemd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "systemd", "private"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	folder, data := t.TempDir(), t.TempDir()
	m := New(WithConfinement([]string{data}, true))
	spec := m.terminalSpec(&resolvedWorkspace{rootReal: folder})
	spec.Program, spec.Args = "/bin/sh", []string{"-c", `if [ -e "$1/systemd/private" ]; then echo private=visible; else echo private=hidden; fi`, "sh", runtimeDir}
	cmd, env, _, release, err := (&procRunner{}).command(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cmd.Dir, cmd.Env = folder, sanitizedEnv(spec.EnvAllow, env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the terminal's shell: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "private=hidden" {
		t.Fatalf("the shell saw %q: the user manager's private socket is reachable from a confined terminal", got)
	}
}

// A session that is not kept away from the user manager gets no scope: the command
// stays as it was, systemd-run is never called, and the reason says why.
func TestSessionScopeIsRefusedWhenTheRuntimeDirectoryIsNotHidden(t *testing.T) {
	dir := t.TempDir()
	stub := "#!/bin/sh\necho called >> \"$(dirname \"$0\")/calls\"\nexec true\n"
	if err := os.WriteFile(filepath.Join(dir, "systemd-run"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirWithBus(t))
	cmd := exec.Command("/bin/true")
	before := append([]string{cmd.Path}, cmd.Args...)
	note := applySessionScope(cmd, "1G", "512", false)
	if !strings.Contains(note, "kept away from its user manager") || !reflect.DeepEqual(append([]string{cmd.Path}, cmd.Args...), before) {
		t.Fatalf("an unhidden runtime directory: note %q, command %q", note, cmd.Args)
	}
	if _, err := os.Stat(filepath.Join(dir, "calls")); err == nil {
		t.Fatal("systemd-run was called for a session that reaches the user manager")
	}
}
