// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package confine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The engine sweeps what a previous run left in the child temporary root (a child killed with the engine), and makes
// each child's directory private there.
func TestChildTempDirIsPrivateAndTheSweepEmptiesTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tool-tmp")
	UseChildTempRoot(root)
	t.Cleanup(func() { UseChildTempRoot("") })
	left := filepath.Join(root, "s-1234")
	if err := os.MkdirAll(filepath.Join(left, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "nested", "file"), []byte("leftover"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SweepChildTemp(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("after the sweep the root holds %v (%v)", entries, err)
	}
	dir, err := ChildTempDir()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || filepath.Dir(dir) != root || info.Mode().Perm() != 0o700 {
		t.Fatalf("child dir %s (%v, %v), want a 0700 directory under %s", dir, info, err, root)
	}
}

// On a fresh install the root does not exist yet: the sweep has nothing to do, and the first child's directory creates
// the root itself, private to the engine.
func TestChildTempDirCreatesAMissingRootPrivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data", "tool-tmp")
	UseChildTempRoot(root)
	t.Cleanup(func() { UseChildTempRoot("") })
	if err := SweepChildTemp(); err != nil {
		t.Fatalf("sweep of a missing root = %v, want nil", err)
	}
	dir, err := ChildTempDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != root || !strings.HasPrefix(filepath.Base(dir), "s-") {
		t.Fatalf("child dir %s, want an s-* directory under %s", dir, root)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("root %s (%v, %v), want a 0700 directory", root, info, err)
	}
}

// The sweep removes only what ChildTempDir makes there, direct s-<decimal> folders; anything else under the root (a
// folder someone put there, a link) is left in place.
func TestTheSweepRemovesOnlyTheChildTempDirsItMade(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tool-tmp")
	UseChildTempRoot(root)
	t.Cleanup(func() { UseChildTempRoot("") })
	outside := t.TempDir()
	for _, d := range []string{"s-1234", "keep-me", "s-abc"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "s-999")); err != nil {
		t.Fatal(err)
	}
	_ = SweepChildTemp()
	if _, err := os.Lstat(filepath.Join(root, "s-1234")); !os.IsNotExist(err) {
		t.Errorf("the child temporary directory s-1234 survives the sweep: %v", err)
	}
	for _, kept := range []string{"keep-me", "s-abc", "s-999"} {
		if _, err := os.Lstat(filepath.Join(root, kept)); err != nil {
			t.Errorf("the sweep removed %s, which ChildTempDir never makes: %v", kept, err)
		}
	}
}
