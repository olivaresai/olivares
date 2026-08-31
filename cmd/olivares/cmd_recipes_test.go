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

// recipesDoc is the outcome-recipe page this test gates.
const recipesDoc = "../../docs/CLI-RECIPES-BY-OUTCOME.md"

// TestRecipeInvocationsResolve checks every command the outcome recipes tell an
// operator to run against the tree that actually exists: the command path must
// resolve and every long flag must be real.
//
// WHY A DOC GETS A TEST. The recipes exist because per-command help cannot answer
// "how do I get X" — the answer crosses three or four groups. What a doc normally
// lacks in exchange is any way to notice that it went stale, and a runbook whose
// commands no longer exist is worse than none: it is read by somebody under
// pressure who then blames themselves. TestExamplesInvokeRealCommandsAndFlags
// already does this for the `Examples:` blocks compiled into the binary; this
// applies the SAME verifier (verifyInvocation) to the page, so the two surfaces
// rot at the same rate, which is none.
//
// ONLY FENCED CODE BLOCKS ARE CHECKED, and that is a decision rather than an
// implementation shortcut: a command named in prose may deliberately be one that
// does NOT exist — docs/CLI-VERB-PARITY.md names `olivares work ls` precisely to
// record that it answers "unknown command". Extracting prose would make this test
// fail on a page whose whole subject is verbs that are absent on purpose. Inside a
// ```sh fence the meaning is unambiguous: this is a line an operator runs.
func TestRecipeInvocationsResolve(t *testing.T) {
	root := newRootCmd()
	raw, err := os.ReadFile(filepath.Clean(recipesDoc))
	if err != nil {
		t.Fatalf("read %s: %v", recipesDoc, err)
	}
	checked := 0
	for _, line := range fencedLines(string(raw)) {
		invocation, ok := invocationFrom(line)
		if !ok {
			continue
		}
		checked++
		verifyInvocation(t, root, recipesDoc, invocation)
	}
	// A guard on the guard. Without it, a fence-marker change (or a rename of the
	// file) would leave this test extracting nothing and reporting green over an
	// empty set — the failure mode that makes a gate worse than no gate, because
	// it also silences the honest "I could not look".
	if checked < 20 {
		t.Fatalf("only %d recipe invocations extracted from %s; the extractor is not seeing the page",
			checked, recipesDoc)
	}
	t.Logf("verified %d recipe invocations", checked)
}

// fencedLines returns the lines inside ``` fenced blocks, in order. A fence opens
// on a line whose first non-space characters are ``` and closes on the next such
// line; anything outside is prose and is not returned.
func fencedLines(doc string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			out = append(out, line)
		}
	}
	return out
}

// TestRecipeDocIsReachable fails when the recipes page stops being referenced from
// the place an operator lands. A runbook nothing links to is a file, not a runbook;
// this is the cheapest possible check that the link survives a docs reshuffle.
func TestRecipeDocIsReachable(t *testing.T) {
	const from = "../../INSTALL.md"
	raw, err := os.ReadFile(filepath.Clean(from))
	if err != nil {
		t.Fatalf("read %s: %v", from, err)
	}
	if !strings.Contains(string(raw), "docs/CLI-RECIPES-BY-OUTCOME.md") {
		t.Errorf("%s no longer links docs/CLI-RECIPES-BY-OUTCOME.md — the recipes are unreachable from the install path", from)
	}
}
