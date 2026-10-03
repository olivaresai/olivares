// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRunChangesStayInsideTheFolder: the session's changes are the files modified since
// the run started; reads never leave the folder, by path or by symlink.
func TestRunChangesStayInsideTheFolder(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write := func(p, body string, at time.Time) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(full, at, at); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now().Add(-time.Hour)
	write("old.txt", "before the run", start.Add(-time.Hour))
	write("notes.md", "- first\n", start.Add(time.Minute))
	write("src/main.go", "package main\n", start.Add(2*time.Minute))
	write(".git/index", "vcs", start.Add(3*time.Minute))
	write("blob.bin", "a\x00b", start.Add(4*time.Minute))
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("engine secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	files, truncated, err := listChanges(root, start)
	if err != nil || truncated {
		t.Fatalf("listChanges = %v, %v", truncated, err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path)
	}
	want := []string{"blob.bin", "src/main.go", "notes.md"} // newest first; no .git, no old file, no link
	if len(got) != len(want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("changes = %v, want %v", got, want)
		}
	}

	if f, err := readChangedFile(root, "notes.md"); err != nil || f.Text != "- first\n" || f.Binary {
		t.Fatalf("notes.md = %+v, %v", f, err)
	}
	if f, err := readChangedFile(root, "blob.bin"); err != nil || !f.Binary || f.Text != "" {
		t.Fatalf("blob.bin = %+v, %v", f, err)
	}
	for _, bad := range []string{"", "/etc/passwd", "../" + filepath.Base(outside) + "/secret", "..", "link"} {
		if _, err := readChangedFile(root, bad); err == nil {
			t.Errorf("read %q left the folder or followed a link", bad)
		} else if bad == "link" && !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("symlink read error = %v, want invalid", err)
		}
	}
}
