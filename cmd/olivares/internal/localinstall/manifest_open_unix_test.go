// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package localinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeRecord writes m as the install record in dir and returns its path.
func writeRecord(t *testing.T, dir string, m *Manifest) string {
	t.Helper()
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "install-manifest.json")
	if err := os.WriteFile(name, body, 0o640); err != nil {
		t.Fatal(err)
	}
	return name
}

// swapBeforeOpen makes the next Load find something else at the record's name when
// it opens it, as the account that owns the record's directory can arrange between
// any earlier check and the open.
func swapBeforeOpen(t *testing.T, swap func(name string)) {
	t.Helper()
	prev := manifestOpenHook
	manifestOpenHook = func(name string) {
		manifestOpenHook = prev
		swap(name)
	}
	t.Cleanup(func() { manifestOpenHook = prev })
}

// loadReturns runs Load in the background and fails the test if Load is still
// blocked after three seconds; it then releases the reader by opening fifo for
// writing, so no goroutine outlives the test.
func loadReturns(t *testing.T, name, fifo string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := Load(name, false)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		t.Fatalf("Load blocked opening a FIFO that stood at the record's name when it opened it")
		return nil
	}
}

func TestLoadDoesNotBlockOnAFIFOSwappedInBeforeTheOpen(t *testing.T) {
	name := writeRecord(t, t.TempDir(), legacyManifest())
	swapBeforeOpen(t, func(name string) {
		_ = os.Remove(name)
		if err := syscall.Mkfifo(name, 0o600); err != nil {
			t.Error(err)
		}
	})
	if err := loadReturns(t, name, name); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("a FIFO at the record's name was not refused as a non-regular file: %v", err)
	}
}

func TestLoadDoesNotFollowALinkSwappedInBeforeTheOpen(t *testing.T) {
	dir := t.TempDir()
	name := writeRecord(t, dir, legacyManifest())
	fifo := filepath.Join(dir, "unrelated.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	swapBeforeOpen(t, func(name string) {
		_ = os.Remove(name)
		if err := os.Symlink(fifo, name); err != nil {
			t.Error(err)
		}
	})
	if err := loadReturns(t, name, fifo); err == nil || !strings.Contains(err.Error(), "not a link") {
		t.Fatalf("a link at the record's name was followed or not refused as a link: %v", err)
	}
}

// A record the service account can put in its own directory is refused before any
// operation sees it: it names paths outside the release-index layout, or it is not
// root's. plan, preserve and purge therefore never act on it, and an unrelated file
// it names stays as it was.
func TestPlantedRecordCannotDirectPlanPreserveOrPurge(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "etc", "unrelated")
	if err := os.MkdirAll(filepath.Dir(victim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte("unrelated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "var", "lib", "olivares")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	outOfLayout := map[string]func(m *Manifest){
		"config": func(m *Manifest) { m.Config, m.Files[1].Path = "/etc/unrelated", "/etc/unrelated" },
		"unit":   func(m *Manifest) { m.Files[2].Path = "/etc/unrelated" },
		"binary": func(m *Manifest) { m.Files[0].Path = "/etc/unrelated" },
		"data":   func(m *Manifest) { m.DataDir, m.ManifestPath = "/etc", "/etc/install-manifest.json" },
	}
	for field, plant := range outOfLayout {
		m := legacyManifest()
		plant(m)
		m.Files[0].Managed, m.Files[1].Managed, m.Files[2].Managed = true, true, true
		name := writeRecord(t, dataDir, m)
		if _, err := Load(name, false); err == nil || !strings.Contains(err.Error(), "install_layout") &&
			!strings.Contains(err.Error(), "unexpected manifest path") && !strings.Contains(err.Error(), "tuple") {
			t.Fatalf("a record naming a %s outside the layout was loaded: %v", field, err)
		}
		// Execute validates again before it plans, so the record cannot reach an
		// operation even when a caller skips Load.
		for _, op := range []Operation{Plan, Preserve, Purge} {
			var out strings.Builder
			if err := Execute(m, Options{Operation: op, Root: root, Out: &out}); err == nil {
				t.Fatalf("%s accepted a record naming a %s outside the layout", op, field)
			}
		}
	}

	// In-layout, but written by the service account rather than root.
	name := writeRecord(t, dataDir, legacyManifest())
	if os.Getuid() == 0 {
		if err := os.Chown(name, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load(name, true); err == nil || !strings.Contains(err.Error(), "owner uid") {
		t.Fatalf("a system record not owned by root was loaded: %v", err)
	}

	body, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "unrelated\n" || info.Mode().Perm() != 0o600 {
		t.Fatalf("the unrelated file changed: %q %v", body, info.Mode().Perm())
	}
}
