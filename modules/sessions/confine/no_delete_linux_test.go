// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noDeleteScript tries every way a child can remove or move a name out of the
// folder, then every way it may still change the folder. Each step reports one
// line, so a regression names the right being.
const noDeleteScript = `
export LC_ALL=C
cd "$1"
try() { name=$1; shift; if "$@" 2>>errors.txt; then echo "$name=ok"; else echo "$name=denied"; fi; }
try create       sh -c 'printf created > created.txt'
try append       sh -c 'printf " more" >> keep.txt'
try overwrite    sh -c 'printf rewritten > keep.txt'
try mkdir        mkdir made
try nested       sh -c 'printf deep > sub/deep.txt'
try unlink       rm keep.txt
try unlink-nested rm sub/inner.txt
try rmdir        rmdir sub
try rename       mv keep.txt renamed.txt
try rename-out   mv keep.txt "$2/moved.txt"
try replace      mv created.txt keep.txt
try link-out     ln keep.txt "$2/linked.txt"
`

func TestNoDeleteFolderCreatesAndChangesButNeverRemovesOrMoves(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	outside := filepath.Join(f.root, "outside")
	for path, content := range map[string]string{
		filepath.Join(f.folder, "keep.txt"):         "keep",
		filepath.Join(f.folder, "sub", "inner.txt"): "inner",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	p := Policy{ReadWrite: []string{outside}, Protect: []string{f.data}}
	cmd, state, err := CommandWithNoDelete(t.Context(), p, false, nil, []string{f.folder}, "sh", "-c", noDeleteScript, "sh", f.folder, outside)
	if err != nil || state.Mode != ModeLandlock {
		t.Fatalf("no-delete command: state=%v err=%v", state, err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	got := string(out)
	for step, want := range map[string]string{
		"create": "ok", "append": "ok", "overwrite": "ok", "mkdir": "ok", "nested": "ok",
		"unlink": "denied", "unlink-nested": "denied", "rmdir": "denied",
		"rename": "denied", "rename-out": "denied", "replace": "denied", "link-out": "denied",
	} {
		if !strings.Contains(got, step+"="+want+"\n") {
			t.Errorf("%s: want %s in\n%s", step, want, got)
		}
	}
	// The kernel refuses, not a silent no-op: removals and moves with EACCES, and
	// a link out of the folder with EXDEV, which is how Landlock answers a missing
	// REFER.
	errs, _ := os.ReadFile(filepath.Join(f.folder, "errors.txt"))
	if n := strings.Count(string(errs), "Permission denied"); n != 6 {
		t.Errorf("%d permission refusals, want 6 (unlink, unlink-nested, rmdir, rename, rename-out, replace):\n%s", n, errs)
	}
	if n := strings.Count(string(errs), "Invalid cross-device link"); n != 1 {
		t.Errorf("%d cross-device refusals, want 1 (link-out):\n%s", n, errs)
	}
	// What the child changed is there; nothing it was refused went away or moved.
	for path, want := range map[string]string{
		filepath.Join(f.folder, "keep.txt"):         "rewritten",
		filepath.Join(f.folder, "created.txt"):      "created",
		filepath.Join(f.folder, "sub", "inner.txt"): "inner",
		filepath.Join(f.folder, "sub", "deep.txt"):  "deep",
	} {
		if b, err := os.ReadFile(path); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", path, b, err, want)
		}
	}
	for _, gone := range []string{filepath.Join(f.folder, "renamed.txt"), filepath.Join(outside, "moved.txt"), filepath.Join(outside, "linked.txt")} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("%s exists after a refused move or link: %v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.folder, "made")); err != nil {
		t.Errorf("the created folder is missing: %v", err)
	}
}

// A protected path inside the folder carves the grant into its entries; each entry
// keeps the no-delete rights, and the protected path stays unreachable.
func TestNoDeleteFolderStillCarvesAProtectedPathInsideIt(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	secret := filepath.Join(f.folder, "secret")
	for path, content := range map[string]string{
		filepath.Join(f.folder, "a.txt"):    "a",
		filepath.Join(f.folder, "b", "x"):   "x",
		filepath.Join(secret, "engine.key"): "sealing-key-fixture",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(f.root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	const script = `
export LC_ALL=C
cd "$1"
try() { name=$1; shift; if "$@" 2>/dev/null; then echo "$name=ok"; else echo "$name=denied"; fi; }
try modify       sh -c 'printf more >> a.txt'
try create-in-b  sh -c 'printf new > b/new.txt'
try unlink       rm a.txt
try unlink-in-b  rm b/x
try read-secret  cat secret/engine.key
try write-secret sh -c 'printf x > secret/new'
`
	p := Policy{ReadWrite: []string{outside}, Protect: []string{secret}}
	cmd, _, err := CommandWithNoDelete(t.Context(), p, false, nil, []string{f.folder}, "sh", "-c", script, "sh", f.folder)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	for step, want := range map[string]string{
		"modify": "ok", "create-in-b": "ok", "unlink": "denied", "unlink-in-b": "denied",
		"read-secret": "denied", "write-secret": "denied",
	} {
		if !strings.Contains(string(out), step+"="+want+"\n") {
			t.Errorf("%s: want %s in\n%s", step, want, out)
		}
	}
}

// Landlock grants add up: a writable grant over the folder, or inside it, would
// give the delete right back. The launch is refused before anything starts.
func TestNoDeleteRefusesAWritableGrantThatReopensRemoval(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	inside := filepath.Join(f.folder, "cache")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, writable := range map[string]string{"same": f.folder, "above": f.root, "inside": inside} {
		t.Run(name, func(t *testing.T) {
			p := Policy{ReadWrite: []string{writable}, Protect: []string{f.data}}
			cmd, _, err := CommandWithNoDelete(t.Context(), p, false, nil, []string{f.folder}, "sh", "-c", "exit 0")
			if err == nil || cmd != nil || !strings.Contains(err.Error(), "could delete through") {
				t.Fatalf("writable %s admitted: command=%v error=%v", name, cmd != nil, err)
			}
		})
	}
}

// A read-only path under the folder would be changed through the folder's own
// create and modify rights, so it refuses the launch, as it does under a read-write
// folder.
func TestNoDeleteRefusesAReadOnlyPathInsideIt(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	vendor := filepath.Join(f.folder, "vendor")
	scratch := filepath.Join(f.root, "scratch")
	for _, dir := range []string{vendor, scratch} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	held, err := OpenDirectory(vendor)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	for name, tc := range map[string]struct {
		p     Policy
		files []*os.File
	}{
		"sealed path": {Policy{ReadWrite: []string{scratch}, Sealed: []string{vendor}}, nil},
		"held handle": {Policy{ReadWrite: []string{scratch}}, []*os.File{held}},
	} {
		t.Run(name, func(t *testing.T) {
			cmd, _, err := CommandWithNoDelete(t.Context(), tc.p, false, tc.files, []string{f.folder}, "sh", "-c", "exit 0")
			if err == nil || cmd != nil || !strings.Contains(err.Error(), "would be writable") {
				t.Fatalf("a read-only path under a no-delete folder was admitted: command=%v error=%v", cmd != nil, err)
			}
		})
	}
}

// Git rewrites its index by renaming index.lock over it, so a repository's .git
// directory keeps the full rights; the working files around it, and a directory that
// only looks like it (.github), do not. A .git that is a link grants nothing past it.
func TestNoDeleteFolderKeepsOnlyItsGitDirectoryWritable(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	outside := filepath.Join(f.root, "outside")
	linkedFolder := filepath.Join(f.root, "linked-folder")
	for path, content := range map[string]string{
		filepath.Join(f.folder, ".git", "HEAD"):         "ref: refs/heads/main\n",
		filepath.Join(f.folder, ".git", "objects", "x"): "x",
		filepath.Join(f.folder, ".github", "ci.yml"):    "ci",
		filepath.Join(f.folder, "keep.txt"):             "keep",
		filepath.Join(outside, "victim.txt"):            "victim",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(linkedFolder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(linkedFolder, ".git")); err != nil {
		t.Fatal(err)
	}
	const script = `
export LC_ALL=C
cd "$1"
try() { name=$1; shift; if "$@" 2>/dev/null; then echo "$name=ok"; else echo "$name=denied"; fi; }
try git-lock-rename  sh -c 'printf new > .git/index.lock && mv .git/index.lock .git/index'
try git-unlink       rm .git/index
try git-nested       sh -c 'printf y > .git/objects/y && rm .git/objects/y'
try git-dir-rename   mv .git .git.bak
try git-dir-rmdir    sh -c 'rm -rf .git/objects .git/HEAD .git/index; rmdir .git'
try work-unlink      rm keep.txt
try work-rename      mv keep.txt moved.txt
try other-dot-unlink rm .github/ci.yml
`
	// The session's own scratch directory, as the launcher adds it: the one writable path
	// a confined child needs. It is not the victim's directory.
	scratch := filepath.Join(f.root, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	p := Policy{ReadWrite: []string{scratch}, Protect: []string{f.data}}
	cmd, _, err := CommandWithNoDelete(t.Context(), p, false, nil, []string{f.folder}, "sh", "-c", script, "sh", f.folder)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	for step, want := range map[string]string{
		"git-lock-rename": "ok", "git-unlink": "ok", "git-nested": "ok",
		"git-dir-rename": "denied", "git-dir-rmdir": "denied",
		"work-unlink": "denied", "work-rename": "denied", "other-dot-unlink": "denied",
	} {
		if !strings.Contains(string(out), step+"="+want+"\n") {
			t.Errorf("%s: want %s in\n%s", step, want, out)
		}
	}
	// A .git link is not a Git directory: nothing past it is granted.
	cmd, _, err = CommandWithNoDelete(t.Context(), p, false, nil, []string{linkedFolder}, "sh", "-c", `rm "$1/victim.txt"`, "sh", outside)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("a .git link opened %s for removal:\n%s", outside, out)
	}
	if _, err := os.Stat(filepath.Join(outside, "victim.txt")); err != nil {
		t.Errorf("the file behind a .git link is gone: %v", err)
	}
}

// A .git link to a directory inside the folder is no Git directory either: the files
// behind it stay protected. The first command proves the file is there and readable.
func TestNoDeleteFolderDoesNotTakeAGitLinkIntoItselfForAGitDirectory(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	scratch := filepath.Join(f.root, "scratch")
	for _, dir := range []string{scratch, filepath.Join(f.folder, "src")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.folder, "src", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src", filepath.Join(f.folder, ".git")); err != nil {
		t.Fatal(err)
	}
	const probe = `cd "$1" && test -f src/x && echo present; rm src/x 2>/dev/null && echo removed || echo denied`
	p := Policy{ReadWrite: []string{scratch}, Protect: []string{f.data}}
	cmd, _, err := CommandWithNoDelete(t.Context(), p, false, nil, []string{f.folder}, "sh", "-c", probe, "sh", f.folder)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "present\n") || !strings.Contains(string(out), "denied\n") || strings.Contains(string(out), "removed") {
		t.Errorf("a .git link into the folder opened src for removal:\n%s", out)
	}
}
