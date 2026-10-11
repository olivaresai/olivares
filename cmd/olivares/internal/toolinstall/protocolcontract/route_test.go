//go:build contract && linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package protocolcontract

import (
	"os"
	"path/filepath"
	"testing"
)

// Both requests use the same install root: separate roots cannot prove that the
// exact selection accepts the previously installed stable release's receipt.
func TestRealGrokStableToExactHandshake(t *testing.T) {
	if os.Getenv("OLIVARES_CONTRACT") != "1" {
		t.Skip("set OLIVARES_CONTRACT=1 to check the official Grok selection transition")
	}
	r, output := newToolResult(t, "grok", "credential-free-handshake-and-selection-transition", "grok-stable-exact.json")
	root := t.TempDir()
	stable, err := installToolAt("grok", "stable", root)
	if err != nil {
		r.check(t, "stable-install", false, err.Error())
		t.FailNow()
	}
	version := os.Getenv("OLIVARES_CONTRACT_VERSION_GROK")
	if version == "" || version == "stable" || version == "latest" {
		version = stable.Version
	}
	r.check(t, "stable-installed-version", stable.Version == version, stable.Version)
	before, err := os.Stat(stable.Executable)
	if err != nil {
		t.Fatal(err)
	}
	exact, err := installToolAt("grok", version, root)
	if err != nil {
		r.check(t, "stable-to-exact-install", false, err.Error())
		t.FailNow()
	}
	r.Binary = exact
	after, err := os.Stat(exact.Executable)
	if err != nil {
		t.Fatal(err)
	}
	r.check(t, "stable-to-exact-same-release", exact.Version == version && exact.Executable == stable.Executable && exact.SHA256 == stable.SHA256 && exact.PackageSHA256 == stable.PackageSHA256 && os.SameFile(before, after), map[string]any{"stable_version": stable.Version, "exact_version": exact.Version, "same_executable_file": os.SameFile(before, after), "executable_sha256": exact.SHA256})
	childOutput := filepath.Join(output, "grok-stable-exact")
	if err := os.MkdirAll(childOutput, 0700); err != nil {
		t.Fatal(err)
	}
	checkACP(t, r, childOutput)
}
