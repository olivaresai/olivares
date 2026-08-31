// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestQuickstartHeaderComesFirst pins the ordering E5 fixed. On a fresh
// install the welcome panel arrived at line 60 of the interleaved output,
// because runEngine boots before it can call announce — the panel carries the
// one-time setup token, which does not exist until the engine is up. A
// first-time operator's first impression of the product was sixty lines of WARN.
func TestQuickstartHeaderComesFirst(t *testing.T) {
	var out bytes.Buffer
	quickstartHeader(&out, t.TempDir(), false)

	got := out.String()
	if got == "" {
		t.Fatal("the header printed nothing")
	}
	first := ""
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) != "" {
			first = strings.TrimSpace(line)
			break
		}
	}
	if !strings.Contains(first, "OLIVARES AI") {
		t.Fatalf("the first non-empty line must name the product, got %q", first)
	}
	// It must set the expectation about what follows, or it is just a second
	// banner in front of the same wall.
	for _, want := range []string{"startup checks", "--quiet"} {
		if !strings.Contains(got, want) {
			t.Errorf("the header must mention %q so the log that follows is expected:\n%s", want, got)
		}
	}
}

// TestQuickstartHeaderNamesTheDataDirectory: a first run creates an
// installation, and the operator should be told where before it happens rather
// than from a WARN line naming a relative path.
func TestQuickstartHeaderNamesTheDataDirectory(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	quickstartHeader(&out, dir, true)
	if !strings.Contains(out.String(), dir) {
		t.Fatalf("the header must name the data directory %q:\n%s", dir, out.String())
	}
	if strings.Contains(out.String(), "--quiet to see only") {
		t.Error("the --quiet hint must not be offered to somebody who already passed it")
	}
}
