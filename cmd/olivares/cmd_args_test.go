// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestExactRefRefusesABlankReferenceLocally pins that a blank reference is refused
// before any request is built.
//
// MEASURED 2026-09-18: `olivares provider test ""` built
// "/v1/m/sessions/providers//test" and SENT it — a round trip with the operator's
// credential on the wire to explain a mistake visible in the argument list. An
// empty string is one argument, so ExactArgs(1) was satisfied.
func TestExactRefRefusesABlankReferenceLocally(t *testing.T) {
	cmd := &cobra.Command{Use: "test <provider-ref>", Example: "  olivares provider test prv_01J8ABCDEF"}
	for _, blank := range []string{"", " ", "\t", "\n", "   \t  "} {
		err := exactRef("provider-ref")(cmd, []string{blank})
		if err == nil {
			t.Fatalf("a blank reference %q must not reach the network", blank)
		}
		if code := exitcode.From(err); code != exitcode.Usage {
			t.Fatalf("blank %q: exit code %v, want Usage — a caller distinguishes this from \"the server said no\"", blank, code)
		}
		// The error names the next command, from the command's own example.
		if !strings.Contains(err.Error(), "olivares provider test prv_01J8ABCDEF") {
			t.Fatalf("the refusal must name what to run instead: %v", err)
		}
	}
}

// TestExactRefRejectsWhitespaceRatherThanTrimmingIt: " prv_1 " is a typo, and
// trimming it would mean the reference the operator typed and the one the engine
// acted on are different strings.
func TestExactRefRejectsWhitespaceRatherThanTrimmingIt(t *testing.T) {
	cmd := &cobra.Command{Use: "get <provider-ref>"}
	err := exactRef("provider-ref")(cmd, []string{" prv_1 "})
	if err == nil {
		t.Fatal("a padded reference must be refused, not repaired")
	}
	if exitcode.From(err) != exitcode.Usage {
		t.Fatalf("want a usage exit code, got %v", exitcode.From(err))
	}
	if !strings.Contains(err.Error(), "exactly as it was issued") {
		t.Fatalf("the refusal must say why: %v", err)
	}
}

// TestExactRefPassesARealReference is the control: the guard must not be the
// reason a working command stops working.
func TestExactRefPassesARealReference(t *testing.T) {
	cmd := &cobra.Command{Use: "get <provider-ref>"}
	if err := exactRef("provider-ref")(cmd, []string{"prv_01J8ABCDEF"}); err != nil {
		t.Fatalf("a real reference must pass: %v", err)
	}
	// And the arity check it wraps is still the arity check.
	if err := exactRef("provider-ref")(cmd, nil); err == nil {
		t.Fatal("no argument at all must still be refused")
	}
	if err := exactRef("provider-ref")(cmd, []string{"a", "b"}); err == nil {
		t.Fatal("two arguments must still be refused")
	}
}

// TestExampleHintFallsBackWhenACommandHasNoExample keeps the refusal a sentence
// even for a command that documents no example.
func TestExampleHintFallsBackWhenACommandHasNoExample(t *testing.T) {
	err := exactRef("run-ref")(&cobra.Command{Use: "events <run-ref>"}, []string{""})
	if err == nil || !strings.Contains(err.Error(), "the reference the engine issued") {
		t.Fatalf("want the fallback hint, got %v", err)
	}
	// A commented example line is a comment, not a command to run.
	hint := exampleHint(&cobra.Command{Example: "  # list them first\n  olivares provider ls"})
	if hint != ": olivares provider ls" {
		t.Fatalf("exampleHint picked %q; a comment is not the next command", hint)
	}
}
