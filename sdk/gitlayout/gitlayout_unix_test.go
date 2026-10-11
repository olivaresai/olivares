// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gitlayout

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestReadNeverBlocksOnAFIFO: the session can write its .git and its worktree
// entry, so a FIFO at any pointer file, with or without a writer holding it
// open, is refused at once instead of holding the engine.
func TestReadNeverBlocksOnAFIFO(t *testing.T) {
	for _, name := range []string{".git", "commondir", "gitdir"} {
		for _, held := range []bool{false, true} {
			label := name
			if held {
				label += " held open"
			}
			t.Run(label, func(t *testing.T) {
				f := newFixture(t)
				path := filepath.Join(f.entry, name)
				if name == ".git" {
					path = filepath.Join(f.wt, ".git")
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
				if held {
					w, err := os.OpenFile(path, os.O_RDWR, 0)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { w.Close() })
				}
				done := make(chan bool, 1)
				go func() { _, ok := Read(f.wt); done <- ok }()
				select {
				case ok := <-done:
					if ok {
						t.Fatal("Read admitted a FIFO pointer")
					}
				case <-time.After(20 * time.Second):
					// End a blocked open or read.
					if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
						w.Close()
					}
					t.Fatal("Read blocked on a FIFO pointer")
				}
			})
		}
	}
}
