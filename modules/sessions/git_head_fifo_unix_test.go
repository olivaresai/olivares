// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHeadBlobDoesNotWaitOnAFifo: the agent can make a FIFO where git keeps a file, and a
// plain open of it blocks until someone writes. The read must refuse it at once.
func TestHeadBlobDoesNotWaitOnAFifo(t *testing.T) {
	dir := t.TempDir()
	gitFixture(t, dir, "init", "-q", "-b", "main")
	fixtureWrite(t, dir, "a.txt", "x\n")
	gitFixture(t, dir, "add", ".")
	gitFixture(t, dir, "commit", "-q", "-m", "one")
	head := strings.TrimSpace(gitFixture(t, dir, "rev-parse", "HEAD"))
	root := openFixtureRoot(t, dir)
	git := filepath.Join(dir, ".git")

	for name, victim := range map[string]string{
		"HEAD":           filepath.Join(git, "HEAD"),
		"the branch":     filepath.Join(git, "refs", "heads", "main"),
		"a loose object": filepath.Join(git, "objects", head[:2], head[2:]),
	} {
		saved, err := os.ReadFile(victim)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(victim); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(victim, 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := headBlob(root, "a.txt"); done <- err }()
		select {
		case err := <-done:
			if !errors.Is(err, errNoRepository) {
				t.Errorf("fifo as %s: err = %v, want errNoRepository", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("fifo as %s: the read is blocked", name)
		}
		if err := os.Remove(victim); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(victim, saved, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestHeadBlobDoesNotWaitOnAFifoPackDirectory: the same for the directory the packs are
// listed from, which only an object that is not loose ever reaches.
func TestHeadBlobDoesNotWaitOnAFifoPackDirectory(t *testing.T) {
	dir := t.TempDir()
	gitFixture(t, dir, "init", "-q", "-b", "main")
	fixtureWrite(t, dir, "a.txt", "x\n")
	gitFixture(t, dir, "add", ".")
	gitFixture(t, dir, "commit", "-q", "-m", "one")
	gitFixture(t, dir, "repack", "-adq") // every object is in a pack now, none is loose
	root := openFixtureRoot(t, dir)
	if got, err := headBlob(root, "a.txt"); err != nil || string(got) != "x\n" {
		t.Fatalf("packed fixture = %q, %v", got, err)
	}
	pack := filepath.Join(dir, ".git", "objects", "pack")
	if err := os.RemoveAll(pack); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(pack, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := headBlob(root, "a.txt"); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, errNoRepository) {
			t.Errorf("fifo as objects/pack: err = %v, want errNoRepository", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fifo as objects/pack: the read is blocked")
	}
}
