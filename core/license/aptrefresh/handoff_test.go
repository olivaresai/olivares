// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package aptrefresh

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// handoffDir is a stand-in for HandoffDir as tmpfiles creates it: a directory of the running uid, 0700.
func handoffDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "olivares-apt-handoff")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func issuedDocument() Document {
	inv := "4f1c0a2b9e8d7c6b5a4f3e2d1c0b9a88"
	return Issued(validCycle, &inv, testAnswer(), testNow)
}

// TestPublishWritesOnlyTheCycleFile: the handoff lands as <cycle>.json, a regular 0600 file of the running
// uid with one link, holding exactly the document's bytes; no temporary file stays behind (r3 §3.1.5.5).
func TestPublishWritesOnlyTheCycleFile(t *testing.T) {
	dir := handoffDir(t)
	doc := issuedDocument()
	if err := Publish(dir, doc); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := listDir(t, dir); strings.Join(got, ",") != validCycle+".json" {
		t.Fatalf("the handoff directory holds %v, want only %s.json", got, validCycle)
	}
	path := filepath.Join(dir, validCycle+".json")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || st.Nlink != 1 || int(st.Uid) != os.Getuid() {
		t.Fatalf("handoff %s mode %04o links %d uid %d, want a regular 0600 file, one link, uid %d",
			info.Mode().Type(), info.Mode().Perm(), st.Nlink, st.Uid, os.Getuid())
	}
	want, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the handoff holds %q, want %q", got, want)
	}
	decodeHandoff(t, got)
}

// TestPublishReplacesThisCycleFileOnly: a second publication for the same cycle replaces its file and
// leaves another cycle's file alone.
func TestPublishReplacesThisCycleFileOnly(t *testing.T) {
	dir := handoffDir(t)
	other := filepath.Join(dir, "ffffffffffffffffffffffffffffffff.json")
	if err := os.WriteFile(other, []byte("other cycle"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Publish(dir, NotIssued(validCycle, nil, OutcomeUnknown, "", testNow)); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	if err := Publish(dir, issuedDocument()); err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, validCycle+".json"))
	if m := decodeHandoff(t, got); m["outcome"] != "issued" {
		t.Fatalf("the cycle file holds outcome %v, want issued", m["outcome"])
	}
	if b, _ := os.ReadFile(other); string(b) != "other cycle" {
		t.Fatalf("another cycle's file changed: %q", b)
	}
}

// TestPublishRefusesAnUnsafeDirectory: the fixed directory is opened with O_DIRECTORY|O_NOFOLLOW and
// checked by fstat — owner equal to the running uid, mode 0700. A symlinked directory, a wrong owner or a
// wrong mode refuses with no write.
func TestPublishRefusesAnUnsafeDirectory(t *testing.T) {
	t.Run("a symlinked directory", func(t *testing.T) {
		real := handoffDir(t)
		link := filepath.Join(t.TempDir(), "olivares-apt-handoff")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		assertCustodyRefused(t, Publish(link, issuedDocument()))
		if got := listDir(t, real); len(got) != 0 {
			t.Fatalf("the symlink's target received %v", got)
		}
	})
	for _, mode := range []os.FileMode{0o750, 0o755, 0o770, 0o777, 0o500, os.ModeSticky | 0o700, os.ModeSetgid | 0o700} {
		t.Run("mode "+mode.String(), func(t *testing.T) {
			dir := handoffDir(t)
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			assertCustodyRefused(t, Publish(dir, issuedDocument()))
			_ = os.Chmod(dir, 0o700)
			if got := listDir(t, dir); len(got) != 0 {
				t.Fatalf("a %s directory received %v", mode, got)
			}
		})
	}
	t.Run("a foreign owner", func(t *testing.T) {
		dir := handoffDir(t)
		saved := getuid
		getuid = func() int { return os.Getuid() + 1 }
		t.Cleanup(func() { getuid = saved })
		assertCustodyRefused(t, Publish(dir, issuedDocument()))
		if got := listDir(t, dir); len(got) != 0 {
			t.Fatalf("a directory of another owner received %v", got)
		}
	})
	t.Run("a regular file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "olivares-apt-handoff")
		if err := os.WriteFile(file, []byte("not a directory"), 0o700); err != nil {
			t.Fatal(err)
		}
		assertCustodyRefused(t, Publish(file, issuedDocument()))
	})
	t.Run("absent", func(t *testing.T) {
		assertCustodyRefused(t, Publish(filepath.Join(t.TempDir(), "absent"), issuedDocument()))
	})
}

// TestPublishRefusesAPlantedTemporaryFile: .<cycle>.tmp is created with O_CREAT|O_EXCL|O_NOFOLLOW, so a
// file or a symlink already under that name refuses; the planted entry and its target stay as they were
// and no <cycle>.json appears.
func TestPublishRefusesAPlantedTemporaryFile(t *testing.T) {
	t.Run("a regular file", func(t *testing.T) {
		dir := handoffDir(t)
		tmp := filepath.Join(dir, "."+validCycle+".tmp")
		if err := os.WriteFile(tmp, []byte("planted"), 0o600); err != nil {
			t.Fatal(err)
		}
		before, _ := os.Lstat(tmp)
		assertCustodyRefused(t, Publish(dir, issuedDocument()))
		after, err := os.Lstat(tmp)
		if err != nil || !os.SameFile(before, after) {
			t.Fatalf("the planted file was replaced or removed: %v", err)
		}
		if b, _ := os.ReadFile(tmp); string(b) != "planted" {
			t.Fatalf("the planted file was written: %q", b)
		}
		if got := listDir(t, dir); strings.Join(got, ",") != "."+validCycle+".tmp" {
			t.Fatalf("the directory holds %v", got)
		}
	})
	t.Run("a symlink", func(t *testing.T) {
		dir := handoffDir(t)
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "."+validCycle+".tmp")); err != nil {
			t.Fatal(err)
		}
		assertCustodyRefused(t, Publish(dir, issuedDocument()))
		if b, _ := os.ReadFile(target); string(b) != "target" {
			t.Fatalf("the symlink's target was written: %q", b)
		}
		if _, err := os.Lstat(filepath.Join(dir, validCycle+".json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a handoff was published past the planted symlink: %v", err)
		}
	})
}

// TestPublishRefusesAnInvalidDocumentBeforeOpening: a document outside the schema writes nothing.
func TestPublishRefusesAnInvalidDocumentBeforeOpening(t *testing.T) {
	dir := handoffDir(t)
	if err := Publish(dir, NotIssued("../../etc/x", nil, OutcomeUnknown, "", testNow)); err == nil {
		t.Fatal("Publish accepted an invalid cycle")
	}
	if got := listDir(t, dir); len(got) != 0 {
		t.Fatalf("an invalid document wrote %v", got)
	}
}

func assertCustodyRefused(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrHandoffCustody) {
		t.Fatalf("Publish = %v, want ErrHandoffCustody", err)
	}
}
