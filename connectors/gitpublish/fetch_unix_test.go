// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gitpublish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestFetchRefusesANonPointerWorktreeBacklink: the session can write its
// worktree entry, so the entry's gitdir file is read as git's short pointer
// file and nothing else. A FIFO there, with or without a writer holding it
// open, must not hold the engine, and an oversized file is refused, not read
// whole.
func TestFetchRefusesANonPointerWorktreeBacklink(t *testing.T) {
	fifo := func(t *testing.T, backlink string) {
		if err := syscall.Mkfifo(backlink, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, plant := range map[string]func(t *testing.T, backlink, dotgit string){
		"a FIFO": func(t *testing.T, backlink, _ string) { fifo(t, backlink) },
		"a FIFO a writer holds open": func(t *testing.T, backlink, _ string) {
			fifo(t, backlink)
			w, err := os.OpenFile(backlink, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { w.Close() })
		},
		"an oversized file": func(t *testing.T, backlink, dotgit string) {
			// Read whole, this still names the worktree (leading slashes
			// resolve away): only the size cap refuses it.
			writeFile(t, backlink, strings.Repeat("/", 8192)+dotgit+"\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSessionFixture(t)
			entry := run(t, s.session, "rev-parse", "--absolute-git-dir")
			backlink := filepath.Join(entry, "gitdir")
			if err := os.Remove(backlink); err != nil {
				t.Fatal(err)
			}
			plant(t, backlink, filepath.Join(s.session, ".git"))
			done := make(chan error, 1)
			go func() { done <- s.x.Fetch(context.Background(), s.managed, s.session, s.commit) }()
			select {
			case err := <-done:
				if !errors.Is(err, ErrSource) {
					t.Fatalf("err = %v, want ErrSource", err)
				}
			case <-time.After(20 * time.Second):
				// End the blocked open or read: a writer comes and goes, and
				// Cleanup closes a held one.
				if w, err := os.OpenFile(backlink, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					w.Close()
				}
				t.Fatal("Fetch blocked reading the worktree's gitdir file")
			}
			if _, err := s.x.CommitTree(context.Background(), s.managed, s.commit); !errors.Is(err, ErrContent) {
				t.Fatalf("a refused session fed its commit: %v", err)
			}
		})
	}
}

// TestFetchDoesNotBlockOnAFIFOCommondir: the session can write its worktree
// entry's commondir too. A FIFO there, held open by a writer, must not hold
// the engine: the layout read refuses it instead of waiting on it.
func TestFetchDoesNotBlockOnAFIFOCommondir(t *testing.T) {
	s := newSessionFixture(t)
	entry := run(t, s.session, "rev-parse", "--absolute-git-dir")
	commondir := filepath.Join(entry, "commondir")
	if err := os.Remove(commondir); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(commondir, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := os.OpenFile(commondir, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.x.Fetch(ctx, s.managed, s.session, s.commit); !errors.Is(err, ErrSource) {
		t.Fatalf("err = %v, want ErrSource at once (a FIFO commondir held the fetch)", err)
	}
}
