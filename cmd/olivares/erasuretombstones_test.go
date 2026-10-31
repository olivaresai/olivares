// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/modules/compliance"
)

// Tombstones copied to another host without their key cannot be opened by a new
// key: Key refuses, names the missing file and mints nothing.
func TestErasureTombstoneKeyRefusesToMintBesideTombstones(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, erasureTombstoneFile), []byte(`{"tenant":"t"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &erasureTombstoneLog{dir: dir}
	if _, err := l.Key(); err == nil || !strings.Contains(err.Error(), erasureTombstoneKeyFile) {
		t.Fatalf("Key beside tombstones without a key = %v, want an error naming %s", err, erasureTombstoneKeyFile)
	}
	if _, err := os.Stat(filepath.Join(dir, erasureTombstoneKeyFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("key file after the refusal: %v, want none minted", err)
	}

	// The first erasure of a data dir still mints the key.
	fresh := &erasureTombstoneLog{dir: t.TempDir()}
	if k, err := fresh.Key(); err != nil || len(k) != 32 {
		t.Fatalf("Key on a fresh data dir = %d bytes err=%v, want a new 32-byte key", len(k), err)
	}
}

// A restore with no tombstone file says so, so an operator who restored on
// another host without copying it sees why nothing was reapplied.
func TestErasureReplayNamesAnAbsentTombstoneFile(t *testing.T) {
	dir := t.TempDir()
	if err := dr.MarkErasureReplay(dir); err != nil {
		t.Fatal(err)
	}
	mod := compliance.New(compliance.WithTombstoneLog(&erasureTombstoneLog{dir: dir}))
	var said []string
	if err := replayErasures(context.Background(), mod, dir, func(l string) { said = append(said, l) }); err != nil {
		t.Fatal(err)
	}
	want := "erasure replay: no erasure tombstones in " + dir
	if !slices.Contains(said, want) {
		t.Fatalf("said = %q, want %q", said, want)
	}
}
