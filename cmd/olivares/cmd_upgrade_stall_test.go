// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// --timeout bounds a transfer that makes no progress, not the whole run: a release that
// keeps arriving on a slow link installs however long it takes, and one that stops
// delivering bytes is refused after --timeout of silence with the binary untouched.
func TestUpgradeTimeoutIsAStallBound(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build stub binaries")
	}
	v1 := buildStub(t, "26.700")
	v2 := buildStub(t, "26.800")

	t.Run("a download that keeps arriving outlasts --timeout", func(t *testing.T) {
		f := newUpdFixture(t, "26.800", "26.600", v2)
		f.artifactGap = 150 * time.Millisecond // 8 parts: about 1 s in all, 150 ms apart
		target := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64, "--timeout", "500ms",
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err != nil {
			t.Fatalf("a progressing download must install: %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.800") {
			t.Fatalf("target not upgraded: %q", got)
		}
	})

	t.Run("a download that stops is refused after --timeout of silence", func(t *testing.T) {
		f := newUpdFixture(t, "26.800", "26.600", v2)
		f.onArtifact = func() { time.Sleep(1500 * time.Millisecond) }
		target := writeTarget(t, v1)
		_, err := runUpgradeCmd(t, "--endpoint", f.server.URL, "--pubkey", f.pubB64, "--timeout", "300ms",
			"--target", target, "--os", "linux", "--arch", "amd64", "--yes", "--data-dir", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "no data received for 300ms") {
			t.Fatalf("a stalled download must be refused as a stall, got %v", err)
		}
		if got := runsVersion(t, target); !strings.Contains(got, "26.700") {
			t.Fatalf("a refused download must leave the target untouched: %q", got)
		}
	})
}
