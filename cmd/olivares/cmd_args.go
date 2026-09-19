// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// exactRef is cobra.ExactArgs(1) for a command whose one positional is an
// identifier the CLI then puts in a URL path.
//
// MEASURED 2026-09-18 walking the first hour: `olivares provider test ""` was
// accepted. An empty string IS one argument, so ExactArgs(1) is satisfied, and
// the command built "/v1/.../providers//test" and sent it — a network round trip,
// the operator's credential on the wire, and a server-side 404 or 405 to explain
// a mistake that was visible in the argument list before anything left the host.
//
// The same shape is on every `<…-ref>` positional in this binary; it is fixed
// here, once, rather than in each RunE. A blank reference is a usage error, which
// is the exit code a caller distinguishes from "the server said no".
//
// Whitespace is rejected, not trimmed. " prv_1 " is a typo, and silently
// repairing it would mean the reference the operator typed and the reference the
// engine acted on are different strings — the class of quiet repair that was
// removed from doctor's console-state reader on the same day.
func exactRef(name string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			return err
		}
		if strings.TrimSpace(args[0]) == "" {
			return exitcode.New(exitcode.Usage, fmt.Errorf(
				"%s is empty: name the %s to act on, then run%s", name, name, exampleHint(cmd)))
		}
		if args[0] != strings.TrimSpace(args[0]) {
			return exitcode.New(exitcode.Usage, fmt.Errorf(
				"%s %q has leading or trailing whitespace: pass the reference exactly as it was issued", name, args[0]))
		}
		return nil
	}
}

// exampleHint turns the command's own first Example line into the next command an
// operator runs, or says nothing when the command has none. The renderer's Next
// primitive is for a command that SUCCEEDED; this is an argument validator, whose
// error cobra prints with the usage block, so the hint travels in the sentence.
func exampleHint(cmd *cobra.Command) string {
	for _, line := range strings.Split(cmd.Example, "\n") {
		if line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#")); line != "" &&
			strings.HasPrefix(line, "olivares ") {
			return ": " + line
		}
	}
	return " it with the reference the engine issued"
}
