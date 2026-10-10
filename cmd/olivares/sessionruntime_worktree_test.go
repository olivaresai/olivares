// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
)

func TestSessionWorktreeRootDefaultsBesideTheSessionWorkspaces(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ dataDir, override, want string }{
		{"/var/lib/olivares", "", "/var/lib/olivares/session-worktrees"},
		{"/var/lib/olivares/", "", "/var/lib/olivares/session-worktrees"},
		{"/var/lib/olivares", "/srv/worktrees", "/srv/worktrees"},
		{"", "", ""},
		{"relative/data", "", ""},
	} {
		if got := sessionWorktreeRootFor(tc.dataDir, tc.override); got != tc.want {
			t.Errorf("sessionWorktreeRootFor(%q, %q) = %q, want %q", tc.dataDir, tc.override, got, tc.want)
		}
	}
}

// The directory is bound at boot through the door that reports a refusal, and a
// refusal is one ERROR line with its remedy; the node keeps serving every other launch.
func TestSessionWorktreesAreBoundAtBootAndARefusalIsReported(t *testing.T) {
	t.Parallel()
	none := func(string) string { return "" }

	t.Run("default", func(t *testing.T) {
		dataDir := t.TempDir()
		m := sessions.New(buildSessionRuntimeOptions(none, nil, dataDir, nil)...)
		if m.SessionWorktreesConfigured() {
			t.Fatal("worktrees are configured before boot binds them")
		}
		if err := useSessionWorktrees(m, dataDir, none, nil); err != nil || !m.SessionWorktreesConfigured() {
			t.Fatalf("default binding: err=%v configured=%v", err, m.SessionWorktreesConfigured())
		}
	})

	t.Run("override", func(t *testing.T) {
		dataDir, elsewhere := t.TempDir(), t.TempDir()
		getenv := func(name string) string {
			if name == envSessionWorktreeDir {
				return elsewhere
			}
			return ""
		}
		m := sessions.New(buildSessionRuntimeOptions(none, nil, dataDir, nil)...)
		if err := useSessionWorktrees(m, dataDir, getenv, nil); err != nil || !m.SessionWorktreesConfigured() {
			t.Fatalf("override binding: err=%v configured=%v", err, m.SessionWorktreesConfigured())
		}
	})

	t.Run("refused", func(t *testing.T) {
		dataDir := t.TempDir()
		target := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dataDir, sessionWorktreeDirName)); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		m := sessions.New(buildSessionRuntimeOptions(none, nil, dataDir, nil)...)

		err := useSessionWorktrees(m, dataDir, none, log)

		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("a symlinked worktree directory was accepted: %v", err)
		}
		if m.SessionWorktreesConfigured() {
			t.Fatal("a refused worktree directory left the option configured")
		}
		if n := countLogLevel(buf.String(), "ERROR"); n != 1 {
			t.Fatalf("a refusal is reported once at boot, got %d ERROR lines:\n%s", n, buf.String())
		}
	})
}
