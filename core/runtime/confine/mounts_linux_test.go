// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// gitFolder is a session folder with the layout git init writes.
func gitFolder(t *testing.T) string {
	t.Helper()
	folder := t.TempDir()
	for path, content := range map[string]string{
		".git/hooks/pre-commit.sample": "#!/bin/sh\n",
		".git/config":                  "[core]\n\trepositoryformatversion = 0\n",
		".git/HEAD":                    "ref: refs/heads/main\n",
	} {
		full := filepath.Join(folder, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return folder
}

// mountsProbeScript prints one `name=writable|blocked` line for each thing a
// session may try on a Git folder: the two Git writes the guard exists to stop,
// the ones Git itself needs, and moving .git away to put a copy in its place.
const mountsProbeScript = `
w() { if sh -c "$2" 2>/dev/null; then echo "$1=writable"; else echo "$1=blocked"; fi; }
w hook 'echo x > .git/hooks/pre-commit'
w config 'echo x >> .git/config'
w newfile 'echo y > newfile'
w lock 'echo z > .git/index.lock && mv .git/index.lock .git/index'
w move 'mv .git .git.moved'
echo "caps=$(awk '/^Cap(Eff|Prm|Amb|Bnd):/ {print $2}' /proc/self/status | sort -u | tr '\n' ' ')"
`

func runGuarded(t *testing.T, folder string, m *Mounts) map[string]string {
	t.Helper()
	return runScript(t, folder, m, mountsProbeScript)
}

func runScript(t *testing.T, folder string, m *Mounts, script string) map[string]string {
	t.Helper()
	return startScript(t, folder, m, script)()
}

// startScript starts the session and returns what waits for it and reads its lines,
// so a test can act on the folder from outside while the session is alive.
func startScript(t *testing.T, folder string, m *Mounts, script string) func() map[string]string {
	t.Helper()
	requireLandlock(t)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = folder
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	p := Policy{ReadWrite: []string{folder}, ReadOnly: systemReadOnly, Devices: []Device{{Path: "/dev/null", Write: true}}}
	if _, err := Wrap(cmd, p); err != nil {
		t.Fatal(err)
	}
	if m != nil {
		if err := WrapMounts(cmd, *m); errors.Is(err, ErrMountsUnavailable) {
			t.Skip("this host does not allow user and mount namespaces: " + err.Error())
		} else if err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return func() map[string]string {
		t.Helper()
		if err := cmd.Wait(); err != nil {
			t.Fatalf("session: %v\n%s", err, out.String())
		}
		got := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			name, state, _ := strings.Cut(line, "=")
			got[name] = strings.TrimSpace(state)
		}
		return got
	}
}

func TestGitHooksAndConfigAreReadOnlyInAWritableFolder(t *testing.T) {
	folder := gitFolder(t)
	got := runGuarded(t, folder, &Mounts{
		ReadOnly: []string{filepath.Join(folder, ".git/hooks"), filepath.Join(folder, ".git/config")},
		Pin:      []string{filepath.Join(folder, ".git")},
	})
	// The session holds no capability: it cannot remount what it was given read-only.
	want := map[string]string{"hook": "blocked", "config": "blocked", "newfile": "writable", "lock": "writable", "move": "blocked", "caps": "0000000000000000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("guarded folder: got %v, want %v", got, want)
	}
}

// Without the mounts the same session writes a hook and moves .git: the test
// above is red for exactly that variant and for no other reason.
func TestUnguardedGitFolderIsWritable(t *testing.T) {
	got := runGuarded(t, gitFolder(t), nil)
	delete(got, "caps")
	want := map[string]string{"hook": "writable", "config": "writable", "newfile": "writable", "lock": "writable", "move": "writable"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unguarded folder: got %v, want %v", got, want)
	}
}

// The guard on a file is a mount on that file's own entry in its directory, in the
// session's namespace. When something outside the session renames a new file over
// it (git writes .git/config that way: config.lock onto config), the kernel
// detaches the mount there, because the entry the mount sat on is gone. From then
// on the session writes the new file. The documented limit is this: the
// protection lasts as long as the file does. A directory that is not empty (hooks)
// cannot be replaced that way. Should a host show "after=blocked", the limit is
// smaller than documented and the changelog and sessions reference can say so.
func TestGitConfigGuardEndsWhenSomethingOutsideRenamesOverTheFile(t *testing.T) {
	folder := gitFolder(t)
	config := filepath.Join(folder, ".git/config")
	replacement := filepath.Join(folder, ".git/config.lock")
	if err := os.WriteFile(replacement, []byte("[core]\n\trepositoryformatversion = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wait := startScript(t, folder, &Mounts{
		ReadOnly: []string{filepath.Join(folder, ".git/hooks"), config},
		Pin:      []string{filepath.Join(folder, ".git")},
	}, `
w() { if sh -c "$2" 2>/dev/null; then echo "$1=writable"; else echo "$1=blocked"; fi; }
w before 'echo x >> .git/config'
: > ready
while [ ! -e go ]; do sleep 0.1; done
w after 'echo x >> .git/config'
w hook-after 'echo x > .git/hooks/pre-commit'
`)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Lstat(filepath.Join(folder, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session did not reach its first check")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// From the engine's namespace, where config is no mount point, as git does it.
	if err := os.Rename(replacement, config); err != nil {
		t.Fatalf("rename over the guarded file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := wait()
	want := map[string]string{"before": "blocked", "after": "writable", "hook-after": "blocked"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestWrapMountsRefusesAnythingButTheHelperCommand(t *testing.T) {
	cmd := exec.Command("/bin/true")
	cmd.Args = []string{"/bin/true", "not-the-helper", "--", "/bin/true"}
	if err := WrapMounts(cmd, Mounts{ReadOnly: []string{"/tmp"}}); err == nil || errors.Is(err, ErrMountsUnavailable) {
		t.Fatalf("a command that is not the helper's was wrapped: %v", err)
	}
	cmd.Args = []string{"/bin/true", HelperArg, "--", "/bin/true"}
	if err := WrapMounts(cmd, Mounts{ReadOnly: []string{"relative"}}); err == nil || errors.Is(err, ErrMountsUnavailable) {
		t.Fatalf("a relative path was accepted: %v", err)
	}
}

// A name git reads and the session could create is made before it is bound, so
// the session cannot create it with its own content.
func TestWrapMountsMakesAMissingNodeThenGuardsIt(t *testing.T) {
	folder := gitFolder(t)
	commondir := filepath.Join(folder, ".git/commondir")
	got := runScript(t, folder, &Mounts{
		ReadOnly: []string{commondir},
		Create:   []Node{{Path: commondir, Content: ".\n"}},
		Pin:      []string{filepath.Join(folder, ".git")},
	}, `
w() { if sh -c "$2" 2>/dev/null; then echo "$1=writable"; else echo "$1=blocked"; fi; }
w commondir 'echo ../x > .git/commondir'
w sibling 'echo y > .git/not-guarded'
echo "content=$(cat .git/commondir)"
`)
	want := map[string]string{"commondir": "blocked", "sibling": "writable", "content": "."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Nothing is made on a host that cannot apply the mounts, or whose node cannot
// be made: the caller starts the session without them and says so.
func TestWrapMountsMakesNothingWhereItCannotGuard(t *testing.T) {
	folder := gitFolder(t)
	helper := func() *exec.Cmd {
		cmd := exec.Command("/bin/true")
		cmd.Args = []string{"/bin/true", HelperArg, "--", "/bin/true"}
		return cmd
	}
	made := filepath.Join(folder, ".git/commondir")
	m := Mounts{ReadOnly: []string{made}, Create: []Node{{Path: made, Content: ".\n"}}}
	saved := probeMounts
	defer func() { probeMounts = saved }()

	probeMounts = func() error { return errors.New("denied") }
	if err := WrapMounts(helper(), m); !errors.Is(err, ErrMountsUnavailable) {
		t.Fatalf("WrapMounts = %v, want ErrMountsUnavailable", err)
	}
	if _, err := os.Lstat(made); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a node was made on a host that cannot guard it: %v", err)
	}

	probeMounts = func() error { return nil }
	m.Create = []Node{{Path: filepath.Join(folder, "no-such-directory", "node")}}
	if err := WrapMounts(helper(), m); !errors.Is(err, ErrMountsUnavailable) || !strings.Contains(err.Error(), "cannot create") {
		t.Fatalf("WrapMounts = %v, want ErrMountsUnavailable naming the node", err)
	}
}

func TestCreateWritesTheWholeNodeOrNothingAndNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "commondir")
	if err := create(Node{Path: path, Content: ".\n"}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != ".\n" {
		t.Fatalf("content = %q, %v", raw, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, %v", info, err)
	}
	// What is there stays as it is.
	if err := create(Node{Path: path, Content: "../elsewhere\n"}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != ".\n" {
		t.Fatalf("an existing node was replaced: %q", raw)
	}
	if err := create(Node{Path: filepath.Join(dir, "hooks"), Dir: true}); err != nil {
		t.Fatal(err)
	}
	// A failed one leaves no partial file behind.
	if err := create(Node{Path: filepath.Join(dir, "missing", "node")}); err == nil {
		t.Fatal("a node in a missing directory was created")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".olivares-node-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}
