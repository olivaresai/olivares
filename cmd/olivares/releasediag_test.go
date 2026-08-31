// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"sort"
	"strings"
	"testing"
)

// runRootForTest executes the root command with args and returns stdout.
func runRootForTest(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// TestRootVersionWired proves the cobra root carries the build metadata, so
// `olivares --version` reports the SAME stamped version/commit/date the
// `version` subcommand does — the E2 traceability contract. A root with
// Version == "" silently drops the --version flag entirely.
func TestRootVersionWired(t *testing.T) {
	root := newRootCmd()
	if root.Version == "" {
		t.Fatal("root.Version is empty: cobra --version is not wired")
	}
	for _, want := range []string{version, commit, date} {
		if !strings.Contains(root.Version, want) {
			t.Errorf("root.Version %q does not carry build metadatum %q", root.Version, want)
		}
	}
}

// TestCommandsCmdPrintsSortedTree proves the hidden `commands` diagnostic prints
// the FULL cobra command tree, sorted and one path per line — the stable snapshot
// scripts/release-smoke.sh diffs between a packaged artifact and a binary built
// from the same source to catch a stale/divergent release binary (E2).
func TestCommandsCmdPrintsSortedTree(t *testing.T) {
	out, err := runRootForTest(t, "commands")
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 20 {
		t.Fatalf("suspiciously small command tree (%d lines):\n%s", len(lines), out)
	}
	for _, want := range []string{"olivares serve", "olivares version", "olivares dr drill", "olivares commands"} {
		if !strings.Contains(out, want+"\n") && !strings.HasSuffix(out, want) {
			t.Errorf("command tree is missing %q", want)
		}
	}
	if !sort.StringsAreSorted(lines) {
		t.Error("command tree is not sorted; the smoke diff would be order-flaky")
	}
}

// TestFirstPartyBinsCmdHonest proves the hidden `firstparty-bins` diagnostic
// never lies in either build state: it always prints an honest count (zero on a
// plain dev build, the extract-verified list when bins/ is populated), while
// --require of an absent plugin fails — that failure is exactly what the
// release smoke asserts against the artifact.
func TestFirstPartyBinsCmdHonest(t *testing.T) {
	out, err := runRootForTest(t, "firstparty-bins")
	if err != nil {
		t.Fatalf("firstparty-bins: %v", err)
	}
	if !strings.Contains(out, "embedded connector plugin(s)") {
		t.Errorf("missing count trailer in output:\n%s", out)
	}

	// A name that can never exist must fail closed under --require.
	if _, err := runRootForTest(t, "firstparty-bins", "--require", "no-such-plugin"); err == nil {
		t.Fatal("--require no-such-plugin succeeded; the release smoke would be vacuous")
	}
}
