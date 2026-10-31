// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// asInstalledPackage makes this process look like a deb/rpm install running from
// pkgPath: `upgrade` resolves its default target to it (runningBinary) and the
// package predicate names it (installedPackageBinary). A package host is not a
// container, so the answer is pinned rather than detected.
func asInstalledPackage(t *testing.T, pkgPath string) {
	t.Helper()
	oldBin, oldPkg, oldIn := runningBinary, installedPackageBinary, inContainer
	runningBinary = func() (string, error) { return pkgPath, nil }
	installedPackageBinary = pkgPath
	inContainer = func() bool { return false }
	t.Cleanup(func() { runningBinary, installedPackageBinary, inContainer = oldBin, oldPkg, oldIn })
}

// TestUpgradeRefusesToReplaceThePackageOwnedBinary is the regression reproducer:
// on a deb/rpm install, run as root, `/usr/bin/olivares` is writable, so
// `olivares upgrade` (without --target) swapped the package-owned binary in
// place — the file no longer matched the package manager's database (`rpm -V`
// fails) and the next package upgrade silently overwrote it. The swap must be
// refused, before any download, with a usage exit code and one line naming the
// package-manager route; `--check` and an explicit `--target` keep working.
func TestUpgradeRefusesToReplaceThePackageOwnedBinary(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1 := buildStub(t, "26.700")
	v2 := buildStub(t, "26.800")
	pkg := writeTarget(t, v1) // the fake /usr/bin/olivares: a real, runnable binary
	asInstalledPackage(t, pkg)

	t.Run("refuses the in-place swap before any download", func(t *testing.T) {
		f := newUpdFixture(t, "26.800", "26.600", v2)
		downloaded := false
		f.onArtifact = func() { downloaded = true }
		out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		// The premise first, so a red run shows the defect itself: on unfixed main this
		// prints the NEW version and reproduces the in-place swap the issue names.
		if got := runsVersion(t, pkg); !strings.Contains(got, "26.700") {
			t.Fatalf("the package-owned binary was replaced in place (now %q): the regression", got)
		}
		if err == nil {
			t.Fatal("upgrading the package-owned binary in place must be refused")
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Fatalf("exit code = %d, want Usage (%d): %v", got, exitcode.Usage, err)
		}
		if !strings.Contains(err.Error(), "systemctl start olivares-appliance-update.service") {
			t.Fatalf("the refusal must name the package-manager route (appliance service): %v", err)
		}
		// Not even the update source is contacted: the OTA key line is the last one
		// before the refusal, so no manifest was fetched either (the guard sits ahead
		// of buildUpdateSource and fetchManifest, not merely ahead of the artifact).
		if strings.Contains(out, "source:") {
			t.Fatalf("the refusal must precede the manifest fetch, got output:\n%s", out)
		}
		if downloaded {
			t.Fatal("the refusal must happen before the artifact download")
		}
		if b, _ := filepath.Glob(pkg + ".bak-*"); len(b) != 0 {
			t.Fatalf("a refused upgrade must leave no backup: %v", b)
		}
	})

	t.Run("--check still shows the plan", func(t *testing.T) {
		f := newUpdFixture(t, "26.800", "26.600", v2)
		out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--os", "linux", "--arch", "amd64", "--check", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("--check on the package-owned binary: %v", err)
		}
		if !strings.Contains(out, "upgrade available") {
			t.Fatalf("--check should show the plan, got:\n%s", out)
		}
		if got := runsVersion(t, pkg); !strings.Contains(got, "26.700") {
			t.Fatalf("--check must not swap: %q", got)
		}
	})

	t.Run("an explicit --target still replaces the path it names", func(t *testing.T) {
		f := newUpdFixture(t, "26.800", "26.600", v2)
		other := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", other, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("an explicit --target is the staging path and keeps working: %v", err)
		}
		if got := runsVersion(t, other); !strings.Contains(got, "26.800") {
			t.Fatalf("an explicit --target should still upgrade: %q", got)
		}
		if got := runsVersion(t, pkg); !strings.Contains(got, "26.700") {
			t.Fatalf("the package-owned binary must stay untouched: %q", got)
		}
	})

	// The refusal is keyed on "no --target was given" (targetIsSelf), not on the path
	// alone: an operator who NAMES the package path has taken over the choice, and
	// that published path must keep its exit-0 swap. A guard re-keyed on
	// target == installedPackageBinary alone would turn this subtest red.
	t.Run("an explicit --target naming the package path still replaces it", func(t *testing.T) {
		f := newUpdFixture(t, "26.800", "26.600", v2)
		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--target", pkg, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("an explicit --target naming the package path is the operator's choice: %v", err)
		}
		if got := runsVersion(t, pkg); !strings.Contains(got, "26.800") {
			t.Fatalf("an explicit --target naming the package path should still upgrade: %q", got)
		}
		// Restore the 26.700 package binary the sibling subtests share.
		if err := os.WriteFile(pkg, v1, 0o755); err != nil {
			t.Fatal(err)
		}
	})

	// The carve-out, pinned: in a container the swap target is the data volume, never
	// the image's file, so the same running binary must NOT meet the package refusal.
	// Dropping the !inContainer() arm of the guard would turn this subtest red.
	t.Run("a container running the package path is not refused", func(t *testing.T) {
		dataDir := execCapableDir(t)
		isolateTemp(t) // the container's proofs of the applied binary live in the temp dir
		oldIn := inContainer
		inContainer = func() bool { return true }
		t.Cleanup(func() { inContainer = oldIn })
		f := newUpdFixture(t, "26.800", "26.600", v2)
		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
			"--os", "linux", "--arch", "amd64", "--yes", "--data-dir", dataDir)
		if err != nil {
			t.Fatalf("a container upgrades into its data volume: %v", err)
		}
		if got := runsVersion(t, pkg); !strings.Contains(got, "26.700") {
			t.Fatalf("the image's package path was replaced in a container (%q)", got)
		}
		if got := runsVersion(t, appliedBinaryPath(dataDir)); !strings.Contains(got, "26.800") {
			t.Fatalf("the data volume holds %q, want the applied 26.800", got)
		}
	})
}
