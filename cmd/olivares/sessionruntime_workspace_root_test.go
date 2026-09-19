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

// countLogLevel counts the records a text handler wrote at one level.
func countLogLevel(out string, level string) int {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "level="+level) {
			n++
		}
	}
	return n
}

// TestARefusedSessionWorkspaceRootIsRefusedAtBoot is the composition root's half
// of the retention guard. The module validates the root; until this test the
// engine wired it through `WithSessionWorkspaceRoot`, an Option, which cannot
// return anything — so a refused root left the node unable to launch any session
// without a registered workspace, and the only place that said so was the 503
// returned to whoever made the first such launch.
//
// A refusal is a configuration fact: it belongs in the boot log, ONCE, with its
// remedy, and the node stays deny-closed for exactly those launches.
func TestARefusedSessionWorkspaceRootIsRefusedAtBoot(t *testing.T) {
	t.Parallel()

	// A data directory whose `session-workspaces` is a symbolic link: a root the
	// module refuses by name, for a reason that carries its own remedy.
	dataDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("mkdir the link target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(dataDir, sessionWorkspaceDirName)); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := sessions.New(buildSessionRuntimeOptions(func(string) string { return "" }, nil, dataDir, nil)...)

	err := useSessionWorkspaceRoot(m, dataDir, log)
	if err == nil {
		t.Fatal("a refused session-workspace root was accepted at boot")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("the refusal does not name what is wrong: %v", err)
	}
	// Deny-closed: the node did not take the root, so a launch that needs a
	// directory of its own is refused rather than started in the engine's cwd.
	if m.SessionWorkspaceRootConfigured() {
		t.Fatal("the module took a root its own validation refused")
	}
	out := buf.String()
	if n := countLogLevel(out, "ERROR"); n != 1 {
		t.Fatalf("a refused root must say so ONCE at ERROR, got %d lines:\n%s", n, out)
	}
	// The line has to tell an operator what to do with it, not only that
	// something is wrong: the path it derived, and the remedy the module named.
	for _, want := range []string{
		filepath.Join(dataDir, sessionWorkspaceDirName),
		"name the directory it resolves to",
		"deny-closed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the boot line does not carry %q:\n%s", want, out)
		}
	}
}

// TestAUsableSessionWorkspaceRootIsWiredAtBootWithoutAnError is the other side,
// so the test above cannot pass by refusing everything: the ordinary data
// directory is taken, the node can give a session a directory of its own, and
// nothing is logged at ERROR.
func TestAUsableSessionWorkspaceRootIsWiredAtBootWithoutAnError(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := sessions.New(buildSessionRuntimeOptions(func(string) string { return "" }, nil, dataDir, nil)...)

	if err := useSessionWorkspaceRoot(m, dataDir, log); err != nil {
		t.Fatalf("an ordinary data directory was refused: %v", err)
	}
	if !m.SessionWorkspaceRootConfigured() {
		t.Fatal("a usable root left the node unable to give a session a directory of its own")
	}
	out := buf.String()
	if n := countLogLevel(out, "ERROR"); n != 0 {
		t.Fatalf("a usable root logged %d ERROR lines:\n%s", n, out)
	}
	if !strings.Contains(out, filepath.Join(dataDir, sessionWorkspaceDirName)) {
		t.Fatalf("the boot line does not name the root it wired:\n%s", out)
	}
}

// TestNoDataDirectoryLeavesTheNodeDenyClosedWithoutAnError pins the third state:
// nothing to derive a root from is a WARNING, not a refusal — there is no
// operator value to correct, and a guessed absolute path would be worse than the
// deny-closed answer.
func TestNoDataDirectoryLeavesTheNodeDenyClosedWithoutAnError(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := sessions.New(buildSessionRuntimeOptions(func(string) string { return "" }, nil, "", nil)...)

	if err := useSessionWorkspaceRoot(m, "", log); err != nil {
		t.Fatalf("an absent data directory reported a refusal: %v", err)
	}
	if m.SessionWorkspaceRootConfigured() {
		t.Fatal("a node with no data directory claims it can give a session a directory of its own")
	}
	out := buf.String()
	if n := countLogLevel(out, "ERROR"); n != 0 {
		t.Fatalf("an absent data directory logged %d ERROR lines:\n%s", n, out)
	}
	if n := countLogLevel(out, "WARN"); n != 1 {
		t.Fatalf("an absent data directory must say so once at WARN, got %d:\n%s", n, out)
	}
}
