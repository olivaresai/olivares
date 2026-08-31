// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestJailRejectsTraversal is the headline security test: every escape attempt
// (lexical `..`, absolute path, NUL byte, and SYMLINK escape) must be REFUSED — never
// clamped to the root.
func TestJailRejectsTraversal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// A real file inside the root (a legitimate target).
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A secret OUTSIDE the root, and a symlink inside the root pointing at it.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	escapeLink := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escapeLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	escapes := []string{
		"../" + filepath.Base(filepath.Dir(outside)) + "/secret.txt",
		"../../etc/passwd",
		"/etc/passwd",
		"sub/../../escapeme",
		"a\x00b",
		"escape",   // a symlink whose target is outside the root
		"escape/x", // through the escaping symlink
	}
	for _, rel := range escapes {
		if _, err := resolveWithin(root, rel, true); err == nil {
			t.Errorf("resolveWithin(%q) must be refused, but it resolved", rel)
		}
	}
	// A WRITE (mustExist=false) through the escaping symlink's parent must also fail.
	if _, err := resolveWithin(root, "escape/newfile", false); err == nil {
		t.Errorf("write through an escaping symlink must be refused")
	}
}

// TestJailAcceptsInRoot confirms legitimate in-root paths resolve (including a `..`
// that stays inside, and a create path that does not yet exist).
func TestJailAcceptsInRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok := []struct {
		rel       string
		mustExist bool
	}{
		{"a/f.txt", true},
		{"a/b", true},
		{"a/b/../f.txt", true}, // stays inside
		{".", true},
		{"a/b/new.txt", false}, // create path, parent exists
	}
	for _, c := range ok {
		abs, err := resolveWithin(root, c.rel, c.mustExist)
		if err != nil {
			t.Errorf("resolveWithin(%q, mustExist=%v) = %v, want ok", c.rel, c.mustExist, err)
			continue
		}
		if !within(mustEval(t, root), abs) {
			t.Errorf("resolved %q -> %q escaped the root", c.rel, abs)
		}
	}
}

// TestJailSymlinkWithinRootAllowed confirms a symlink that stays inside the root is
// followed and accepted (the jail blocks ESCAPE, not all symlinks).
func TestJailSymlinkWithinRootAllowed(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	abs, err := resolveWithin(root, "link.txt", true)
	if err != nil {
		t.Fatalf("an in-root symlink must resolve, got %v", err)
	}
	if filepath.Base(abs) != "real.txt" {
		t.Fatalf("symlink should resolve to its in-root target, got %q", abs)
	}
}

// TestUnderAllowedSubpath confirms the subpath allowlist confines the file API to the
// declared subtrees (empty allowlist = the whole root).
func TestUnderAllowedSubpath(t *testing.T) {
	t.Parallel()

	root := mustEval(t, t.TempDir())
	if underAllowedSubpath(root, nil, filepath.Join(root, "anything")) != true {
		t.Error("empty allowlist must expose the whole root")
	}
	allow := []string{"src", "docs"}
	if !underAllowedSubpath(root, allow, filepath.Join(root, "src", "main.go")) {
		t.Error("a path under an allowed subpath must pass")
	}
	if underAllowedSubpath(root, allow, filepath.Join(root, "secrets", "k")) {
		t.Error("a path outside every allowed subpath must be refused")
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("eval %q: %v", p, err)
	}
	return r
}

// TestResolveWithinRootGone fails closed when the workspace root is unavailable.
func TestResolveWithinRootGone(t *testing.T) {
	t.Parallel()

	if _, err := resolveWithin(filepath.Join(t.TempDir(), "does-not-exist"), "x", true); err == nil {
		t.Fatal("a missing root must fail closed, not resolve")
	}
}

// sanity: the traversal sentinel is what the jail returns for an escape.
func TestTraversalSentinel(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	_, err := resolveWithin(root, "../escape", true)
	if !errors.Is(err, errTraversal) {
		t.Fatalf("escape error = %v, want errTraversal", err)
	}
}
