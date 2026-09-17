// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Caller-visible behavior (CR1 §3): a held CRL lease makes the REAL upgrade command warn that
// the observation was not recorded and install exactly as before. The caller, bindVerifiedRelease,
// is unchanged; this proves the busy error reaches its existing warning route and nothing else.
func TestUpgradeWarnsAndStillInstallsWhileTheCRLLeaseIsHeld(t *testing.T) {
	oldBin := buildStub(t, "26.7.0")
	newBin := buildStub(t, "26.8.0")
	f := newUpdFixture(t, "26.8.0", "", newBin)
	target := writeTarget(t, oldBin)
	dataDir := t.TempDir()

	ops := defaultCRLPersistOps()
	lease, err := acquireCRLStoreLock(dataDir, ops)
	if err != nil {
		t.Fatalf("could not take the CRL lease to simulate another recorder: %v", err)
	}
	defer func() { _ = releaseCRLStoreLock(lease, ops, nil, false) }()

	out, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64,
		"--target", target, "--yes", "--data-dir", dataDir)
	if err != nil {
		t.Fatalf("a busy CRL lease must not block the upgrade: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "WARNING: could not record the channel's license CRL") {
		t.Errorf("the caller must warn that the observation was not recorded; output:\n%s", out)
	}
	if strings.Contains(out, "recorded the channel license CRL in") {
		t.Errorf("a refused observation must not be reported as recorded; output:\n%s", out)
	}
	if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
		t.Errorf("the upgrade must install as before; the target reports %q\noutput:\n%s", got, out)
	}
	if _, err := os.Stat(crlFilePath(dataDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no CRL store may be published under another recorder's lease: %v", err)
	}
}
