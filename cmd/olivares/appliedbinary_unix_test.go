// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A FIFO planted where the evidence or the artifact belongs must not hang a start: a start blocked
// on a pipe nobody writes would keep the engine, and every health probe, from ever answering.
func TestHandOverDoesNotBlockOnAFifo(t *testing.T) {
	priv := newSigningKey(t)
	rel := signRelease(t, priv, "26.800", []byte("applied-binary-26.800"))
	for _, name := range []string{"release.json", "artifact"} {
		t.Run("a FIFO as "+name, func(t *testing.T) {
			isolateTemp(t)
			dataDir := t.TempDir()
			image := filepath.Join(t.TempDir(), "olivares")
			if err := os.WriteFile(image, []byte("image-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := recordAppliedRelease(dataDir, rel.manifest, rel.sig, rel.archive, rel.bin); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(dataDir, "ota", "release.json")
			if name == "artifact" {
				names, _ := filepath.Glob(filepath.Join(dataDir, "ota", "artifact-*"))
				if len(names) != 1 {
					t.Fatalf("artifacts = %v", names)
				}
				victim = names[0]
			}
			if err := os.Remove(victim); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(victim, 0o600); err != nil {
				t.Fatal(err)
			}
			done := make(chan handOverRun, 1)
			go func() { done <- runHandOver(t, dataDir, image, "26.700", rel.pub, nil) }()
			select {
			case r := <-done:
				if r.execed != "" || r.err != nil || !strings.Contains(r.warned, "not a regular file") {
					t.Fatalf("exec = %q err = %v warned = %q, want the image to run with the FIFO named", r.execed, r.err, r.warned)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the hand-over blocked on a FIFO")
			}
		})
	}
}
