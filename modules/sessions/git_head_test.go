// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The HEAD reader is pure Go: these fixtures use the git CLI to BUILD a repository (as a
// developer would), and the reader then has to agree with what git committed.

func gitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func fixtureWrite(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openFixtureRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// numbered is a text with enough lines that successive versions delta well in a pack.
func numbered(version int) string {
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "line %03d of the file, version %d\n", i, version/(1+i%7))
	}
	return b.String()
}

// TestHeadBlobReadsWhatWasCommitted: the committed text comes back, not the working tree's,
// from loose objects, from packs with delta chains, and with packed or detached HEAD.
func TestHeadBlobReadsWhatWasCommitted(t *testing.T) {
	dir := t.TempDir()
	gitFixture(t, dir, "init", "-q", "-b", "main")
	fixtureWrite(t, dir, "README.md", "# Title\n")
	fixtureWrite(t, dir, "docs/deep/notes.txt", numbered(0))
	gitFixture(t, dir, "add", ".")
	gitFixture(t, dir, "commit", "-q", "-m", "first")
	root := openFixtureRoot(t, dir)

	fixtureWrite(t, dir, "README.md", "# Title\nedited in the working tree\n")
	got, err := headBlob(root, "README.md")
	if err != nil || string(got) != "# Title\n" {
		t.Fatalf("loose headBlob = %q, %v; want the committed text", got, err)
	}

	// Four more versions of the notes, then everything into one pack with deltas.
	var last string
	for v := 1; v <= 4; v++ {
		last = numbered(v * 11)
		fixtureWrite(t, dir, "docs/deep/notes.txt", last)
		gitFixture(t, dir, "commit", "-q", "-am", fmt.Sprintf("v%d", v))
	}
	fixtureWrite(t, dir, "docs/deep/notes.txt", "scratch\n")
	gitFixture(t, dir, "repack", "-adq", "--depth=20", "--window=50")
	gitFixture(t, dir, "pack-refs", "--all")
	if out := gitFixture(t, dir, "count-objects", "-v"); !strings.Contains(out, "count: 0") {
		t.Fatalf("fixture still has loose objects:\n%s", out)
	}
	if out := gitFixture(t, dir, "verify-pack", "-v", firstPackIdx(t, dir)); !strings.Contains(out, "chain length") {
		t.Fatalf("fixture pack has no delta chain, the delta path is untested:\n%s", out)
	}
	for _, rel := range []string{"docs/deep/notes.txt", "README.md"} {
		want := gitFixture(t, dir, "show", "HEAD:"+rel)
		if got, err := headBlob(root, rel); err != nil || string(got) != want {
			t.Fatalf("packed headBlob(%s) = %d bytes, %v; want %d bytes", rel, len(got), err, len(want))
		}
	}
	if got, _ := headBlob(root, "docs/deep/notes.txt"); string(got) != last {
		t.Fatalf("packed notes are not the last committed version")
	}

	gitFixture(t, dir, "checkout", "-q", "-f", "--detach", "HEAD~2") // -f: the working tree still holds the scratch edit
	want := gitFixture(t, dir, "show", "HEAD:docs/deep/notes.txt")
	if got, err := headBlob(root, "docs/deep/notes.txt"); err != nil || string(got) != want {
		t.Fatalf("detached headBlob = %d bytes, %v; want HEAD~2's %d bytes", len(got), err, len(want))
	}
}

func firstPackIdx(t *testing.T, dir string) string {
	t.Helper()
	idx, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.idx"))
	if err != nil || len(idx) != 1 {
		t.Fatalf("pack idx = %v, %v", idx, err)
	}
	return idx[0]
}

// TestHeadBlobSaysWhyThereIsNothingToCompare: no repository, an unborn HEAD, an untracked
// path and a directory are different answers, and none is a made-up empty file.
func TestHeadBlobSaysWhyThereIsNothingToCompare(t *testing.T) {
	plain := t.TempDir()
	fixtureWrite(t, plain, "a.txt", "x")
	if _, err := headBlob(openFixtureRoot(t, plain), "a.txt"); !errors.Is(err, errNoRepository) {
		t.Fatalf("no repository: err = %v, want errNoRepository", err)
	}

	dir := t.TempDir()
	gitFixture(t, dir, "init", "-q", "-b", "main")
	fixtureWrite(t, dir, "new.txt", "untracked")
	root := openFixtureRoot(t, dir)
	if _, err := headBlob(root, "new.txt"); !errors.Is(err, errNotInHead) {
		t.Fatalf("unborn HEAD: err = %v, want errNotInHead", err)
	}
	fixtureWrite(t, dir, "sub/kept.txt", "kept")
	gitFixture(t, dir, "add", "sub/kept.txt")
	gitFixture(t, dir, "commit", "-q", "-m", "one")
	for _, rel := range []string{"new.txt", "sub", "sub/kept.txt/below", "nope/deeper.txt"} {
		if _, err := headBlob(root, rel); !errors.Is(err, errNotInHead) {
			t.Errorf("headBlob(%q) err = %v, want errNotInHead", rel, err)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "..", "sub/../../x"} {
		if _, err := headBlob(root, bad); err == nil || errors.Is(err, errNotInHead) {
			t.Errorf("headBlob(%q) err = %v, want a refusal of the path", bad, err)
		}
	}
}

// TestHeadBlobNeverLeavesTheFolder: the folder is written by the agent, so a .git that is
// a link, an alternates file or a ref link must not reach another repository.
func TestHeadBlobNeverLeavesTheFolder(t *testing.T) {
	other := t.TempDir() // another tenant's repository, same file name
	gitFixture(t, other, "init", "-q", "-b", "main")
	fixtureWrite(t, other, "README.md", "ANOTHER TENANT'S SECRET\n")
	gitFixture(t, other, "add", ".")
	gitFixture(t, other, "commit", "-q", "-m", "secret")

	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(other, ".git"), filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, dir, "README.md", "mine")
	if got, err := headBlob(openFixtureRoot(t, dir), "README.md"); err == nil {
		t.Fatalf(".git symlink reached another repository: %q", got)
	}

	// A real .git whose objects are only in an alternate outside the folder: unreadable here,
	// and said so, never "not in HEAD" (that would claim the file is new).
	dir2 := t.TempDir()
	gitFixture(t, dir2, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir2, ".git", "objects", "info", "alternates"), []byte(filepath.Join(other, ".git", "objects")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitFixture(t, other, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir2, ".git", "refs", "heads", "main"), []byte(strings.TrimSpace(head)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, dir2, "README.md", "mine")
	got, err := headBlob(openFixtureRoot(t, dir2), "README.md")
	if err == nil || errors.Is(err, errNotInHead) || bytes.Contains(got, []byte("SECRET")) {
		t.Fatalf("alternates object store: %q, %v; want errNoRepository", got, err)
	}

	// A linked worktree has a .git FILE naming a directory outside the folder: not followed.
	dir3 := t.TempDir()
	fixtureWrite(t, dir3, ".git", "gitdir: "+filepath.Join(other, ".git")+"\n")
	fixtureWrite(t, dir3, "README.md", "mine")
	if got, err := headBlob(openFixtureRoot(t, dir3), "README.md"); !errors.Is(err, errNoRepository) {
		t.Fatalf(".git file: %q, %v; want errNoRepository", got, err)
	}
}

// TestHeadBlobBoundsWhatItInflates: a committed blob that inflates past the ceiling is
// refused, not read into the engine's memory.
func TestHeadBlobBoundsWhatItInflates(t *testing.T) {
	dir := t.TempDir()
	gitFixture(t, dir, "init", "-q", "-b", "main")
	fixtureWrite(t, dir, "big.bin", strings.Repeat("\x00", headMaxObject+1))
	fixtureWrite(t, dir, "ok.txt", "fine\n")
	gitFixture(t, dir, "add", ".")
	gitFixture(t, dir, "commit", "-q", "-m", "big")
	root := openFixtureRoot(t, dir)
	if _, err := headBlob(root, "big.bin"); !errors.Is(err, errHeadTooLarge) {
		t.Fatalf("big blob err = %v, want errHeadTooLarge", err)
	}
	if got, err := headBlob(root, "ok.txt"); err != nil || string(got) != "fine\n" {
		t.Fatalf("small blob next to it = %q, %v", got, err)
	}
}
