// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// `upgrade --install-timer` on the package-owned binary emitted a unit whose ExecStart
// upgrades /usr/bin/olivares in place. The package manager owns that file, so the nightly
// run either replaces it behind dpkg/rpm/apk or fails every night. The generator must
// refuse with the package-manager route instead of writing the unit.
func TestUpgradeInstallTimerRefusesPackageOwnedBinary(t *testing.T) {
	pinPackageHost(t)
	const pkgBin = "/usr/bin/olivares"
	dataDir := t.TempDir()

	t.Run("--timer-dir refuses and writes no unit", func(t *testing.T) {
		dir := t.TempDir()
		out, _, err := runLeafCLI(t, "upgrade", "--install-timer",
			"--timer-dir", dir, "--data-dir", dataDir, "--target", pkgBin)
		if err == nil {
			t.Fatalf("upgrade --install-timer --target %s succeeded; want a refusal", pkgBin)
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("exit code = %d, want %d (usage)", got, exitcode.Usage)
		}
		if out != "" || strings.Contains(err.Error(), "[Service]") {
			t.Errorf("unit text reached stdout %q or the refusal %q", out, err)
		}
		if !strings.Contains(err.Error(), "package manager") {
			t.Errorf("refusal %q does not name the package-manager route", err)
		}
		// The appliance installs this same path from a package, and its update unit renews the
		// entitled-channel credential first: a bare `dnf upgrade` there skips that.
		if !strings.Contains(err.Error(), applianceUpdateRoute) {
			t.Errorf("refusal %q does not name the appliance route %q", err, applianceUpdateRoute)
		}
		for _, name := range []string{"olivares-upgrade.service", "olivares-upgrade.timer"} {
			if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
				t.Errorf("%s was written for a package-owned binary", name)
			}
		}
	})

	t.Run("print path refuses and prints no unit", func(t *testing.T) {
		out, _, err := runLeafCLI(t, "upgrade", "--install-timer",
			"--data-dir", dataDir, "--target", pkgBin)
		if err == nil {
			t.Fatalf("upgrade --install-timer --target %s printed units; want a refusal", pkgBin)
		}
		if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("exit code = %d, want %d (usage)", got, exitcode.Usage)
		}
		if strings.Contains(out, "[Service]") || strings.Contains(out, "[Timer]") {
			t.Errorf("a unit was printed for a package-owned binary:\n%s", out)
		}
	})
}

// isInstalledPackageBinary follows a symlink before it compares, so a link named elsewhere is
// refused; a path that is not the package binary, or does not resolve to it, is not.
func TestIsInstalledPackageBinary(t *testing.T) {
	dangling := filepath.Join(t.TempDir(), "olivares")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), dangling); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	tests := []struct {
		name string
		bin  string
		want bool
	}{
		{"the package binary", "/usr/bin/olivares", true},
		{"a non-package install", "/usr/local/bin/olivares", false},
		{"a dangling link does not resolve to the package binary", dangling, false},
		{"a trailing slash on the package binary", "/usr/bin/olivares/", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInstalledPackageBinary(tt.bin); got != tt.want {
				t.Errorf("isInstalledPackageBinary(%q) = %v, want %v", tt.bin, got, tt.want)
			}
		})
	}

	t.Run("a live link to the package binary", func(t *testing.T) {
		pkg := filepath.Join(t.TempDir(), "olivares")
		if err := os.WriteFile(pkg, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write: %v", err)
		}
		link := filepath.Join(t.TempDir(), "olivares")
		if err := os.Symlink(pkg, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		resolved, err := filepath.EvalSymlinks(pkg)
		if err != nil {
			t.Fatalf("EvalSymlinks: %v", err)
		}
		old := installedPackageBinary
		installedPackageBinary = resolved
		t.Cleanup(func() { installedPackageBinary = old })
		if !isInstalledPackageBinary(link) {
			t.Errorf("isInstalledPackageBinary(%q) = false, want true: the link resolves to the package binary", link)
		}
	})
}

// applianceUpdateRoute is the route dev#2426 (PR #2819) names for the Olivares appliance.
const applianceUpdateRoute = "systemctl start olivares-appliance-update.service"

// A connected Business timer is also the credential's upkeep. A host whose CLI is the package
// binary gets no unit, so the refusal must say how its credential is refreshed instead.
func TestUpgradeInstallTimerConnectRefusalNamesCredentialRefresh(t *testing.T) {
	pinPackageHost(t)
	dataDir := t.TempDir()
	_, _, err := runLeafCLI(t, "upgrade", "--install-timer", "--enterprise", "--connect",
		"--data-dir", dataDir, "--target", "/usr/bin/olivares")
	if err == nil {
		t.Fatal("upgrade --install-timer --enterprise --connect on the package binary succeeded; want a refusal")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (usage)", got, exitcode.Usage)
	}
	if !strings.Contains(err.Error(), "olivares license connect refresh") {
		t.Errorf("refusal %q does not name `olivares license connect refresh`", err)
	}

	// Without --connect the pointer would be noise: neither on Community nor on --enterprise alone
	// (a Business build reads a bare `upgrade` as `upgrade --enterprise`).
	for _, args := range [][]string{
		{"upgrade", "--install-timer", "--data-dir", dataDir, "--target", "/usr/bin/olivares"},
		{"upgrade", "--install-timer", "--enterprise", "--data-dir", dataDir, "--target", "/usr/bin/olivares"},
	} {
		_, _, err = runLeafCLI(t, args...)
		if err == nil || strings.Contains(err.Error(), "license connect refresh") {
			t.Errorf("%v: a refusal without --connect must not name the connected refresh, got %v", args, err)
		} else if got := exitcode.From(err); got != exitcode.Usage {
			t.Errorf("%v: exit code = %d, want %d (usage)", args, got, exitcode.Usage)
		}
	}
}

// A link elsewhere that resolves to the package binary is refused by the command, not only
// by the helper. installedPackageBinary stands a real file in for /usr/bin/olivares, which a
// test cannot create without root.
func TestUpgradeInstallTimerRefusesSymlinkToPackageBinary(t *testing.T) {
	pinPackageHost(t)
	dir := t.TempDir()
	pkg := filepath.Join(dir, "usr-bin-olivares")
	if err := os.WriteFile(pkg, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(dir, "olivares-link")
	if err := os.Symlink(pkg, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// The helper compares an EvalSymlinks-resolved path, so stand in the resolved one: a TempDir
	// that sits behind a symlink (macOS /var) would otherwise never match.
	resolved, err := filepath.EvalSymlinks(pkg)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	old := installedPackageBinary
	installedPackageBinary = resolved
	t.Cleanup(func() { installedPackageBinary = old })

	timerDir := t.TempDir()
	_, _, err = runLeafCLI(t, "upgrade", "--install-timer",
		"--timer-dir", timerDir, "--data-dir", t.TempDir(), "--target", link)
	if err == nil {
		t.Fatal("upgrade --install-timer --target <link to the package binary> succeeded; want a refusal")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (usage)", got, exitcode.Usage)
	}
	if ents, _ := os.ReadDir(timerDir); len(ents) != 0 {
		t.Errorf("a unit was written for a link to the package binary: %v", ents)
	}
}

// pinPackageHost stands a bare host in for inContainer: the refusal is for a package host, and
// the test run may itself be inside a container (the upgrade guard's tests pin it the same way).
func pinPackageHost(t *testing.T) {
	t.Helper()
	old := inContainer
	inContainer = func() bool { return false }
	t.Cleanup(func() { inContainer = old })
}

// In a container the image is immutable and an upgrade swaps the data volume, so the image's
// own binary path keeps its timer. The upgrade's guard (cmd_upgrade.go) makes the same exemption.
func TestUpgradeInstallTimerAllowsPackagePathInContainer(t *testing.T) {
	old := inContainer
	inContainer = func() bool { return true }
	t.Cleanup(func() { inContainer = old })

	dir := t.TempDir()
	if _, _, err := runLeafCLI(t, "upgrade", "--install-timer", "--timer-dir", dir,
		"--data-dir", t.TempDir(), "--target", "/usr/bin/olivares"); err != nil {
		t.Fatalf("upgrade --install-timer --target /usr/bin/olivares in a container was refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "olivares-upgrade.timer")); err != nil {
		t.Errorf("no timer written in a container: %v", err)
	}
}
