// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"os"
	"path/filepath"
	"testing"
)

// A directory that merely carries a bundle's name must not hide the real file
// of that name in a later directory.
func TestListBundleFilesSkipsDirectoriesBeforeDeduplicating(t *testing.T) {
	selected, legacy := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(selected, "x.drbundle"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "x.drbundle"), []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "notes.txt"), []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := listBundleFiles([]string{selected, legacy})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].dir != legacy || files[0].entry.Name() != "x.drbundle" {
		t.Fatalf("listed %+v", files)
	}
}

// The first directory wins when both hold a bundle of the same name.
func TestListBundleFilesPrefersTheFirstDirectory(t *testing.T) {
	selected, legacy := t.TempDir(), t.TempDir()
	for _, dir := range []string{selected, legacy} {
		if err := os.WriteFile(filepath.Join(dir, "x.drbundle"), []byte("bundle"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := listBundleFiles([]string{selected, legacy})
	if err != nil || len(files) != 1 || files[0].dir != selected {
		t.Fatalf("listed %+v, %v", files, err)
	}
}
