// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestExamplesInvokeRealCommandsAndFlags checks every `Examples:` block in the
// tree against the tree itself: the command path must resolve, and each long
// flag it passes must exist on the command it is passed to (or be inherited).
//
// It exists because help text is the one part of a CLI nothing else validates.
// A renamed flag leaves the old name in an example forever, and an example
// written from memory can name a flag that never existed — which is exactly how
// this file came to be: `olivares evals gate --config … --candidates …` was
// written into the `evals` group help during and the real flags are
// --suite/--subject/--outputs.
func TestExamplesInvokeRealCommandsAndFlags(t *testing.T) {
	root := newRootCmd()
	checked := 0
	walkCommands(root, func(cmd *cobra.Command) {
		for _, line := range strings.Split(cmd.Example, "\n") {
			invocation, ok := invocationFrom(line)
			if !ok {
				continue
			}
			checked++
			verifyInvocation(t, root, cmd.CommandPath(), invocation)
		}
	})
	// A guard on the guard: if the extraction silently stops matching, the test
	// would pass by checking nothing.
	if checked < 100 {
		t.Fatalf("only %d example invocations extracted; the extractor is not seeing the help text", checked)
	}
	t.Logf("verified %d example invocations", checked)
}

// invocationFrom pulls the `olivares …` words out of one example line, or
// reports that the line carries no invocation to check (a comment, a shell
// continuation, prose, or a pipeline whose olivares part is not leading).
func invocationFrom(line string) ([]string, bool) {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "sudo ")
	if !strings.HasPrefix(s, "olivares ") {
		return nil, false
	}
	// Stop at the first shell metacharacter: everything after it belongs to the
	// shell, not to this command's flag set.
	for _, stop := range []string{"|", ">", "<", ";", "&&", "\\"} {
		if i := strings.Index(s, stop); i >= 0 {
			s = s[:i]
		}
	}
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return nil, false
	}
	return fields[1:], true
}

func verifyInvocation(t *testing.T, root *cobra.Command, owner string, words []string) {
	t.Helper()
	// Split the leading subcommand path from the arguments/flags.
	var path []string
	for _, w := range words {
		if strings.HasPrefix(w, "-") {
			break
		}
		if _, _, err := root.Find(append(append([]string{}, path...), w)); err != nil {
			break
		}
		candidate := append(append([]string{}, path...), w)
		found, _, _ := root.Find(candidate)
		if found == nil || found.CommandPath() != "olivares "+strings.Join(candidate, " ") {
			break
		}
		path = candidate
	}
	target, _, err := root.Find(path)
	if err != nil || target == nil {
		t.Errorf("%s: example names a command that does not resolve: olivares %s",
			owner, strings.Join(words, " "))
		return
	}
	for _, w := range words {
		if !strings.HasPrefix(w, "--") || w == "--" {
			continue
		}
		name := strings.TrimPrefix(w, "--")
		if i := strings.Index(name, "="); i >= 0 {
			name = name[:i]
		}
		if name == "" {
			continue
		}
		if target.Flags().Lookup(name) == nil && target.InheritedFlags().Lookup(name) == nil &&
			root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("%s: example passes --%s to %q, which has no such flag: olivares %s",
				owner, name, target.CommandPath(), strings.Join(words, " "))
		}
	}
}
