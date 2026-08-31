// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectorScratchDefaultsToPrivateDataDirTemp(t *testing.T) {
	t.Setenv("TMPDIR", "")
	dataDir := t.TempDir()

	dir, err := newConnectorScratchDir(dataDir)
	if err != nil {
		t.Fatalf("newConnectorScratchDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	wantRoot := filepath.Join(dataDir, "tmp")
	if filepath.Dir(dir) != wantRoot || !strings.HasPrefix(filepath.Base(dir), "connectors-") {
		t.Fatalf("scratch dir = %q, want %s/connectors-<random>", dir, wantRoot)
	}
	for _, path := range []string{wantRoot, dir} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %#o, want 0700", path, got)
		}
	}
}

func TestConnectorScratchHonorsExplicitTMPDIR(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	dataDir := filepath.Join(t.TempDir(), "data")

	dir, err := newConnectorScratchDir(dataDir)
	if err != nil {
		t.Fatalf("newConnectorScratchDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if filepath.Dir(dir) != tmpDir || !strings.HasPrefix(filepath.Base(dir), "olivares-connectors-") {
		t.Fatalf("scratch dir = %q, want explicit TMPDIR %s/olivares-connectors-<random>", dir, tmpDir)
	}
	if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
		t.Fatalf("explicit TMPDIR must not touch data-dir %q; stat error = %v", dataDir, statErr)
	}
}

func TestConnectorScratchDoesNotOverrideBrokenExplicitTMPDIR(t *testing.T) {
	base := t.TempDir()
	tmpDir := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(tmpDir, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmpDir)
	dataDir := t.TempDir()

	if dir, err := newConnectorScratchDir(dataDir); err == nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("broken explicit TMPDIR unexpectedly fell back to %q", dir)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "tmp")); !os.IsNotExist(statErr) {
		t.Fatalf("broken explicit TMPDIR must not be overridden with data-dir; stat error = %v", statErr)
	}
}

func TestConnectorScratchFallsBackOnlyAfterDataDirWriteFailure(t *testing.T) {
	t.Setenv("TMPDIR", "")
	// A regular file cannot contain <data-dir>/tmp. This is deterministic even
	// when the test process is root, unlike chmod-based unwritable fixtures.
	dataDir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dataDir, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir, err := newConnectorScratchDir(dataDir)
	if err != nil {
		t.Fatalf("newConnectorScratchDir fallback: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if strings.HasPrefix(dir, dataDir+string(filepath.Separator)) {
		t.Fatalf("fallback scratch %q remained below unwritable data-dir %q", dir, dataDir)
	}
	if !strings.HasPrefix(filepath.Base(dir), "olivares-connectors-") {
		t.Fatalf("fallback scratch = %q, want olivares-connectors-<random>", dir)
	}
	info, statErr := os.Stat(dir)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("fallback scratch mode = %#o, want 0700", got)
	}
}
