// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// Each seed and command represents a separate process. Repeat the whole fixture
// in one test, seeding both estates before invoking either command, so both
// command and estate boundaries must release their sources.
func TestDRFixtureBacksUpAndVerifiesIndependentEstates(t *testing.T) {
	clearSealerKeyEnv(t)
	pf := l3Passphrase(t)
	dirs := []string{t.TempDir(), t.TempDir()}
	for _, dir := range dirs {
		seedDataDir(t, dir)
	}
	for _, dir := range dirs {
		bundle := filepath.Join(t.TempDir(), "estate.drbundle")
		if out, err := runDR(t, "backup", "--data-dir", dir, "--out", bundle, "--passphrase-file", pf); err != nil {
			t.Fatalf("backup: %v\n%s", err, out)
		}
		out, err := runDR(t, "verify", "--in", bundle, "--passphrase-file", pf)
		if err != nil || !strings.Contains(out, "DR drill PASSED") {
			t.Fatalf("verify independent estate: %v\n%s", err, out)
		}
	}
}

// TestExecRootOwnProcessResetsBothSides: execRootOwnProcess resets the
// composition on both sides of the command. An earlier engine of the fixture
// has installed the process-scoped license sources before it runs, and the
// fixture opens another after it; a missing reset on either side is the guard
// panic under the Business composition.
func TestExecRootOwnProcessResetsBothSides(t *testing.T) {
	clearSealerKeyEnv(t)
	pf := l3Passphrase(t)
	dir := t.TempDir()
	seedDataDir(t, dir)
	bootAndClose := func(step string) {
		t.Helper()
		eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", Version: "test"})
		if err != nil {
			t.Fatalf("%s: boot: %v", step, err)
		}
		if err := eng.Close(); err != nil {
			t.Fatalf("%s: close: %v", step, err)
		}
	}
	prepareCompositionTestBoot(t)
	bootAndClose("engine before the command")
	for i := 0; i < 2; i++ {
		bundle := filepath.Join(t.TempDir(), "estate.drbundle")
		if _, errOut, err := execRootOwnProcess(t, "dr", "backup", "--data-dir", dir, "--out", bundle, "--passphrase-file", pf); err != nil {
			t.Fatalf("backup %d: %v\n%s", i, err, errOut)
		}
	}
	bootAndClose("engine after the command")
}
