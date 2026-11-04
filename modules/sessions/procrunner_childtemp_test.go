// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// A confined child's TMPDIR is a private s-* directory under the engine's child temporary root (<data>/tool-tmp in the
// composition), never the host's /tmp: on the Server image the tool's temporary type covers that folder only, and the
// engine's own scratch in /tmp (the egress proxy, gpg, DR) stays out of the tool's reach.
func TestProcRunnerConfinedChildTempDirIsUnderTheChildTempRoot(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + s.Reason)
	}
	root := filepath.Join(t.TempDir(), "tool-tmp")
	confine.UseChildTempRoot(root)
	t.Cleanup(func() { confine.UseChildTempRoot("") })
	folder := t.TempDir()
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{
		Program: "/bin/sh", Args: []string{"-c", `echo "tmpdir=$TMPDIR"`}, Dir: folder, WaitDelay: 2 * time.Second,
		Confinement: &confine.Policy{ReadWrite: []string{folder}},
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
	tmp := ""
	for _, line := range strings.Split(out.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "tmpdir="); ok {
			tmp = v
		}
	}
	if filepath.Dir(tmp) != root || !strings.HasPrefix(filepath.Base(tmp), "s-") {
		t.Fatalf("child TMPDIR = %q, want an s-* directory under %s", tmp, root)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("the child's TMPDIR %s survives the child: %v", tmp, err)
	}
}

// A child that leaves a read-only folder in its TMPDIR (Go's module cache, a test that ended before restoring a mode)
// still leaves nothing under the child temporary root once it has exited: a long-running engine starts no sweep.
func TestProcRunnerConfinedChildTempDirIsRemovedWhenTheChildLeftAReadOnlyFolder(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + s.Reason)
	}
	root := filepath.Join(t.TempDir(), "tool-tmp")
	confine.UseChildTempRoot(root)
	t.Cleanup(func() { confine.UseChildTempRoot(""); _ = exec.Command("chmod", "-R", "u+w", root).Run() })
	folder := t.TempDir()
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{
		Program: "/bin/sh", Args: []string{"-c", `mkdir -p "$TMPDIR/mod/x@v1" && : > "$TMPDIR/mod/x@v1/go.mod" && chmod -R a-w "$TMPDIR/mod"`},
		Dir: folder, WaitDelay: 2 * time.Second, Confinement: &confine.Policy{ReadWrite: []string{folder}},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	for range proc.Output() {
	}
	if exit, _ := proc.Wait(); exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("the child's TMPDIR survives the child: %s holds %d entries", root, len(left))
	}
}

// Go drops a build cache entry only after five days unused, so an account home's cache grows without bound between
// those trims (15 GB measured in one home). Once a child that ran under that home exits, the cache is cut back to the
// limit, least recently used entries first.
func TestProcRunnerBoundsTheHomeGoBuildCacheWhenTheChildExits(t *testing.T) {
	limit := goBuildCacheLimit
	goBuildCacheLimit = 8
	t.Cleanup(func() { goBuildCacheLimit = limit })
	home := t.TempDir()
	cache := filepath.Join(home, ".cache", "go-build", "ab")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	old, recent := filepath.Join(cache, "old-d"), filepath.Join(cache, "recent-d")
	for _, f := range []string{old, recent} {
		if err := os.WriteFile(f, []byte("12345678"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{
		Program: "/bin/sh", Args: []string{"-c", "true"}, Dir: home, WaitDelay: 2 * time.Second,
		Env: []EnvVar{{Name: envUserHome, Value: home}},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	for range proc.Output() {
	}
	if exit, _ := proc.Wait(); exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the least recently used entry %s survives a cache over the limit: %v", old, err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("the recent entry %s was removed although the cache then fits: %v", recent, err)
	}
}

// Go keeps a cached executable as a -d directory that holds the binary; the bound counts and removes those entries too.
func TestProcRunnerBoundsTheHomeGoBuildCacheCountingExecutableEntries(t *testing.T) {
	limit := goBuildCacheLimit
	goBuildCacheLimit = 8
	t.Cleanup(func() { goBuildCacheLimit = limit })
	home := t.TempDir()
	cache := filepath.Join(home, ".cache", "go-build", "ab")
	exe := filepath.Join(cache, "oldexe-d")
	if err := os.MkdirAll(exe, 0o700); err != nil {
		t.Fatal(err)
	}
	recent := filepath.Join(cache, "recent-d")
	for _, f := range []string{filepath.Join(exe, "prog"), recent} {
		if err := os.WriteFile(f, []byte("12345678"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(exe, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{
		Program: "/bin/sh", Args: []string{"-c", "true"}, Dir: home, WaitDelay: 2 * time.Second,
		Env: []EnvVar{{Name: envUserHome, Value: home}},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	for range proc.Output() {
	}
	if exit, _ := proc.Wait(); exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if _, err := os.Stat(exe); !os.IsNotExist(err) {
		t.Errorf("the least recently used executable entry %s survives a cache over the limit: %v", exe, err)
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("the recent entry %s was removed although the cache then fits: %v", recent, err)
	}
}
