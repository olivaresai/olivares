// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/modules/sessions"
)

// writeManagedGrokInstall lays down a MANAGED install of the official Grok CLI
// under root, exactly as `olivares agent tool install --driver grok` leaves it:
// the release directory, the executable, and a receipt/v2 whose inventory and
// fetched-object digest both name the bytes on disk. Nothing here is a mock of the
// installer — it is the artifact the installer writes, so the observer that reads
// it is under test rather than simulated.
func writeManagedGrokInstall(t *testing.T, root string) string {
	t.Helper()
	const version, vendorPlatform = "9.9.9", "linux-x86_64"
	releaseDir := filepath.Join(root, "grok", version+"-"+vendorPlatform)
	binDir := filepath.Join(releaseDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir release: %v", err)
	}
	exe := filepath.Join(binDir, "grok")
	body := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(exe, body, 0o700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	receipt := toolinstall.ReceiptV2{
		Schema: toolinstall.ReceiptSchemaV2, Driver: "grok", Version: version,
		VendorPlatform: vendorPlatform,
		Platform:       toolinstall.PlatformV2{OS: "linux", Arch: "amd64"},
		Destination: toolinstall.Destination{
			Root: root, ReleaseDir: releaseDir, Executable: exe,
		},
		PlanDigest:       hex.EncodeToString(sha256.New().Sum(nil)),
		PackagePolicyID:  "origin-only-v1",
		VerificationKind: toolinstall.VerificationNoneOriginOnly,
		FetchedObject: toolinstall.FetchedObjectObserved{
			SHA256: digest, Size: int64(len(body)),
		},
		Payload: toolinstall.ObservedPayloadInventory{Members: []toolinstall.ObservedMember{
			{Path: "bin", Kind: "directory", Mode: 0o700},
			{Path: "bin/grok", Kind: "regular", SHA256: digest, Size: int64(len(body)), Mode: 0o700},
		}},
		InstalledAt: time.Now().UTC(), InstallerVersion: "test",
	}
	blob, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, toolinstall.ReceiptFile), blob, 0o600); err != nil {
		t.Fatalf("write receipt: %v", err)
	}
	return exe
}

// TestManagedInstallRegistersTheDriverFromTheEnginesOwnDataDirectory pins what
// the boot hint promises. Measured 2026-09-18: the hint offered `olivares agent tool
// install --driver grok`, and installing into the engine's own `<data-dir>/tools`
// left the driver unregistered — only OLIVARES_SESSION_RUNTIME_GROK_BIN worked.
//
// The cause was that the host-tool observer RE-DERIVED the data directory from
// the environment instead of being given the one the engine runs on, so this test
// sets the environment to point SOMEWHERE ELSE on purpose. That is the whole
// measurement: with the variable unset and XDG pointing at an empty tree, the only
// way to find the install is the directory the engine was started with.
func TestManagedInstallRegistersTheDriverFromTheEnginesOwnDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	exe := writeManagedGrokInstall(t, filepath.Join(dataDir, "tools"))

	// The environment default must NOT lead to the install.
	t.Setenv("OLIVARES_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	noPins := func(k string) string {
		switch k {
		case envSessionGrokBin, envSessionCodexBin, envSessionOpenCodeBin, envSessionClaudeBin:
			return ""
		default:
			return os.Getenv(k)
		}
	}

	m := sessions.New(buildSessionRuntimeOptions(noPins, nil, dataDir, nil)...)
	if got := m.OperableProviderDrivers(); !slices.Contains(got, "grok") {
		t.Fatalf("a managed install in the engine's own data directory did not register the driver; operable=%v (install at %s)", got, exe)
	}

	// The control: WITHOUT the engine's data directory, the same environment finds
	// nothing — which is what the engine did on every boot before this parameter
	// existed, and what makes the assertion above about the parameter rather than
	// about the fixture.
	blind := sessions.New(buildSessionRuntimeOptions(noPins, nil, "", nil)...)
	if got := blind.OperableProviderDrivers(); slices.Contains(got, "grok") {
		t.Fatalf("the environment default found the install after all; the test proves nothing: operable=%v", got)
	}
}

// TestManagedInstallObserverPrefersTheDeclaredDataDirectory is the unit half: the
// observer's root is the declared directory's `tools`, not the environment's.
func TestManagedInstallObserverPrefersTheDeclaredDataDirectory(t *testing.T) {
	declared := t.TempDir()
	t.Setenv("OLIVARES_DATA_DIR", t.TempDir())

	obs := newHostToolObserverForDataDir(declared, func(string) string { return "" })
	if obs.root != filepath.Join(declared, "tools") {
		t.Fatalf("observer root = %q, want %q", obs.root, filepath.Join(declared, "tools"))
	}
	if obs.rootErr != nil {
		t.Fatalf("declaring the data directory must not leave the root in error: %v", obs.rootErr)
	}
}
