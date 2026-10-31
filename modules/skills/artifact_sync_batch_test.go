// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

func TestStagedRevisionSyncsOnlyItsFilesInBatches(t *testing.T) {
	tr := newTree()
	for i := 0; i < 201; i++ {
		name, body := fmt.Sprintf("research/%03d.md", i), "retained support"
		if i == 0 {
			name, body = "research/SKILL.md", "---\nname: research\ndescription: Review primary sources.\nlicense: MIT\n---\nRead references.\n"
		}
		if err := tr.entry(name, false); err != nil {
			t.Fatal(err)
		}
		if err := tr.file(name, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	pack, err := tr.validate(digest([]byte("fixture")))
	if err != nil {
		t.Fatal(err)
	}
	m := &Module{opts: Options{ArtifactRoot: t.TempDir()}}
	tenant, id := model.TenantID("t1"), model.NewID()
	revision := filepath.Join(m.opts.ArtifactRoot, string(tenant), id.String())
	original := syncStagedFile
	t.Cleanup(func() { syncStagedFile = original })
	var calls, active atomic.Int32
	started := make(chan struct{})
	syncStagedFile = func(f *os.File) error {
		if active.Add(1) > 8 {
			active.Add(-1)
			return errors.New("more than eight concurrent file syncs")
		}
		defer active.Add(-1)
		// Write the entire tree before waiting for durability, and never serialize
		// every file behind the first slow flush.
		for _, entry := range pack.Manifest {
			if _, err := os.Stat(filepath.Join(revision, entry.Path)); err != nil {
				return err
			}
		}
		switch calls.Add(1) {
		case 1:
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				return errors.New("file syncs ran serially")
			}
		case 2:
			close(started)
		}
		return f.Sync()
	}
	remove, err := m.stageArtifact(tenant, id, pack)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remove() })
	if got := calls.Load(); int(got) != len(pack.Manifest) {
		t.Fatalf("synced %d files, want %d", got, len(pack.Manifest))
	}
}

func TestStagedRevisionSyncFailureRemovesUncommittedFiles(t *testing.T) {
	tr := newTree()
	name := "research/SKILL.md"
	if err := tr.entry(name, false); err != nil {
		t.Fatal(err)
	}
	if err := tr.file(name, []byte("---\nname: research\ndescription: Review primary sources.\n---\nRead references.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pack, err := tr.validate(digest([]byte("fixture")))
	if err != nil {
		t.Fatal(err)
	}
	m := &Module{opts: Options{ArtifactRoot: t.TempDir()}}
	tenant, id := model.TenantID("t1"), model.NewID()
	original := syncStagedFile
	t.Cleanup(func() { syncStagedFile = original })
	syncStagedFile = func(*os.File) error { return errors.New("writeback failed") }
	if _, err := m.stageArtifact(tenant, id, pack); err == nil {
		t.Fatal("publication ignored the durability error")
	}
	if _, err := os.Stat(filepath.Join(m.opts.ArtifactRoot, string(tenant), id.String())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncommitted revision remains: %v", err)
	}
}
