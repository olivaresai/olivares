// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localinstall

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const placedManifest = `{"manifest": "/var/lib/olivares/install-manifest.json"}` + "\n"

func stagedManifest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "install-manifest.json.new")
	if err := os.WriteFile(path, []byte(placedManifest), 0o640); err != nil {
		t.Fatal(err)
	}
	return path
}

// The appliance keeps /var/lib/olivares on its own volume, so root's staging cannot be renamed into it.
// Place writes the manifest inside the target's own directory and renames it there, and reports what it placed.
func TestPlace_WritesAndRenamesInsideTheTargetDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "install-manifest.json")
	placed, err := Place(stagedManifest(t), target, os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != placedManifest {
		t.Fatalf("target holds %q (%v), want the staged manifest", got, err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0o640 || placed.Inode != st.Ino || !placed.ModTime.Equal(info.ModTime()) {
		t.Fatalf("placed %+v, target mode %04o inode %d mtime %v", placed, info.Mode().Perm(), st.Ino, info.ModTime())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("the directory holds %d entries, want only the manifest: a temporary file was left", len(entries))
	}
}

// A link at the target's directory is never followed: root would otherwise write wherever it points.
func TestPlace_RefusesATargetDirectoryThatIsALink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "olivares")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Place(stagedManifest(t), filepath.Join(link, "install-manifest.json"), os.Getgid()); err == nil {
		t.Fatal("placed through a linked directory")
	}
	if entries, _ := os.ReadDir(real); len(entries) != 0 {
		t.Fatalf("the link's target got %d entries", len(entries))
	}
}

// A directory at the target name cannot be replaced; nothing is left behind.
func TestPlace_RefusesADirectoryAtTheTargetAndLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "install-manifest.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Place(stagedManifest(t), target, os.Getgid()); err == nil {
		t.Fatal("replaced a directory")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("a temporary file was left: %s", e.Name())
		}
	}
}

// The script records the placement as stat -c %i and %y with blanks as underscores print it.
func TestPlaced_PrintsInodeAndModTimeAsTheScriptRecordsThem(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "install-manifest.json")
	placed, err := Place(stagedManifest(t), target, os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(placed.String())
	if len(fields) != 2 || strings.Contains(fields[1], " ") || !strings.Contains(fields[1], "_") {
		t.Fatalf("printed %q, want 'INODE MTIME' with the mtime's blanks as underscores", placed.String())
	}
}
