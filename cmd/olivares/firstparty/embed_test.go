// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firstparty

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// TestExtractWritesExecutable proves the embed→extract mechanism (CB-1 transport
// B): a present binary is written to a private 0700 file with its exact bytes.
func TestExtractWritesExecutable(t *testing.T) {
	want := []byte("\x7fELF-fake-connector-binary")
	fsys := fstest.MapFS{
		"bins/" + placeholderName: {Data: []byte("placeholder")},
		"bins/claude-source":      {Data: want},
	}
	dir := t.TempDir()
	path, err := extractFrom(fsys, filepath.Join(dir, "run"), "claude-source")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("extracted bytes differ from embedded binary")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("extracted file is not executable: mode %v", info.Mode().Perm())
	}
}

// TestExtractSamePluginUsesUniquePaths prevents an already-running plugin from
// being truncated when the operator configures a second instance of the same
// connector kind. On Linux, overwriting that executable fails with ETXTBSY.
func TestExtractSamePluginUsesUniquePaths(t *testing.T) {
	want := []byte("\x7fELF-fake-connector-binary")
	fsys := fstest.MapFS{
		"bins/" + placeholderName: {Data: []byte("placeholder")},
		"bins/claude-source":      {Data: want},
	}
	dir := t.TempDir()
	first, err := extractFrom(fsys, dir, "claude-source")
	if err != nil {
		t.Fatalf("first extract: %v", err)
	}
	second, err := extractFrom(fsys, dir, "claude-source")
	if err != nil {
		t.Fatalf("second extract: %v", err)
	}
	if first == second {
		t.Fatalf("repeated extraction reused %q; running instances need unique executables", first)
	}
	for _, path := range []string{first, second} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s bytes differ from embedded binary", path)
		}
	}
}

// TestExtractNotEmbeddedIsHonest proves the honest-skip contract: an absent binary
// (or the placeholder) yields ErrNotEmbedded, never a silent success — the boot
// warns and skips rather than pretending a source is wired.
func TestExtractNotEmbeddedIsHonest(t *testing.T) {
	fsys := fstest.MapFS{"bins/" + placeholderName: {Data: []byte("placeholder")}}
	dir := t.TempDir()

	if _, err := extractFrom(fsys, dir, "claude-source"); !errors.Is(err, ErrNotEmbedded) {
		t.Fatalf("missing binary err = %v, want ErrNotEmbedded", err)
	}
	if _, err := extractFrom(fsys, dir, placeholderName); !errors.Is(err, ErrNotEmbedded) {
		t.Fatalf("placeholder extract err = %v, want ErrNotEmbedded", err)
	}
	if got := availableIn(fsys); len(got) != 0 {
		t.Fatalf("availableIn with only a placeholder = %v, want empty", got)
	}
}

// TestEmbeddedSetIsValid proves the real embed pattern compiles and is readable on
// a plain build (only the placeholder present, so Available is empty — the honest
// dev/CI default).
func TestEmbeddedSetIsValid(t *testing.T) {
	// Available() reads the real embedded FS; on a connector-less build it is empty.
	if got := Available(); len(got) != 0 {
		t.Logf("note: %d first-party connector(s) embedded in this build: %v", len(got), got)
	}
}
