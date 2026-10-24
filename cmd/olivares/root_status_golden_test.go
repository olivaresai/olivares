// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBareOlivaresJSONOutputIsUnchanged is exit criterion (b) of #2569: the
// JSON pane, and the `next` value scripts read from it, are byte-identical to
// the output captured from origin/main before this change.
func TestBareOlivaresJSONOutputIsUnchanged(t *testing.T) {
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("OLIVARES_DATA_DIR", t.TempDir())
	t.Setenv("OLIVARES_SERVER_URL", "")
	out, _, err := execRoot(t, []string{"-o", "json"}...) // empty, not nil: nil makes cobra read os.Args
	if err != nil {
		t.Fatalf("olivares -o json: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "root_status.golden.json"))
	if err != nil {
		t.Fatalf("read the golden file: %v", err)
	}
	if out != string(want) {
		t.Fatalf("bare olivares JSON changed:\ngot:\n%q\nwant:\n%q", out, want)
	}
}

// TestBareOlivaresTextOnAPipeIsUnchanged is the other half of the same
// contract: a pipe gets the same bytes it got before, captured from origin/main
// f0418750b in testdata/root_status.golden.txt, with neither the terminal hint
// line nor a second mention of the ui command.
func TestBareOlivaresTextOnAPipeIsUnchanged(t *testing.T) {
	t.Setenv(cliConfigOverrideEnv, filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("OLIVARES_DATA_DIR", t.TempDir())
	t.Setenv("OLIVARES_SERVER_URL", "")
	out, _, err := execRoot(t, []string{}...) // empty, not nil: nil makes cobra read os.Args
	if err != nil {
		t.Fatalf("olivares: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "root_status.golden.txt"))
	if err != nil {
		t.Fatalf("read the golden file: %v", err)
	}
	if out != string(want) {
		t.Fatalf("bare olivares text on a pipe changed:\ngot:\n%q\nwant:\n%q", out, want)
	}
	if strings.Contains(out, "Interactive:") {
		t.Fatalf("a pipe got the terminal hint:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  ui ") {
			t.Fatalf("the bare command repeats the ui row:\n%s", out)
		}
	}
	for _, want := range []string{"Start here:", "quickstart", "All commands: olivares --help"} {
		if !strings.Contains(out, want) {
			t.Fatalf("bare olivares text lost %q:\n%s", want, out)
		}
	}
}
