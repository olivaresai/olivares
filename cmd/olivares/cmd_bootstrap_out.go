// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"io"

	"github.com/spf13/cobra"
)

// The two rendering primitives the LOCAL-FACT bootstrap and DR commands need on
// top of render.go's renderOut (VER-06 lot L3: auth login/logout/use-context,
// config validate, connector init, db init, dr backup/push/pull).
//
// WHY THESE COMMANDS ARE DIFFERENT FROM THE REST OF THE TREE. The majority form
// in this CLI is "the engine's bytes verbatim, as json.RawMessage" — the reason is
// written at cmd_observeplane.go:364-386 and cmd_modelstack.go:388-392: a typed
// CLI struct exists to build the TEXT table, and re-marshaling it would silently
// drop every field the CLI does not model, including fields the engine adds later.
// None of these nine leaves talks to an engine. They report facts this process
// just produced on local disk (a written bundle, a generated repository, a
// provisioned role, a selected context), so there are no engine bytes to preserve
// and the honest value is the CLI's own typed DTO — the MINORITY form, which is
// exactly what the other engine-less leaves already use (auth status,
// compliance, version, config effective: 40 of the 122 renderer call sites).
//
// WHAT THESE TWO FUNCTIONS ADD. Both exist because a mutating command emits more
// than one kind of line, and only one kind belongs in a machine-readable
// document:
//
//   - the FACT (what happened) → the JSON document on stdout;
//   - COMMENTARY (advice, warnings, progress notes about work already done) →
//     stdout in text mode exactly as before, stderr under -o json.
//
// Commentary must not be dropped, and it must not be printed on stdout under
// -o json. Dropping it would lose real information: `dr backup` reports which
// stale bundles retention deleted and warns when a prune could not be done, and
// an operator who never sees that warning believes a retention policy that did
// not run. Printing it on stdout would make the document unparseable, which is
// the whole defect VER-06 exists to close.
//
// The text pane is UNCHANGED by construction: in text mode commentaryOut returns
// the very writer the command used before (cmd.OutOrStdout()), at the same call
// sites, in the same order.

// commentaryOut returns the writer a command's HUMAN commentary must go to:
// cmd.OutOrStdout() in text mode — byte-for-byte what these commands did before
// VER-06 — and cmd.ErrOrStderr() under -o json, where stdout is reserved for the
// JSON document.
//
// It reports the flag-parse error rather than guessing a format: -o is validated
// at parse time (outputFlagValue.Set), so an error here means the flag could not
// be READ, and picking a default would decide the stream contract on a value
// nobody established.
func commentaryOut(cmd *cobra.Command) (io.Writer, error) {
	format, err := selectedOutput(cmd)
	if err != nil {
		return nil, err
	}
	if format == "json" {
		return cmd.ErrOrStderr(), nil
	}
	return cmd.OutOrStdout(), nil
}

// renderJSONOnly is renderOut for a command that has ALREADY written its human
// form, line by line, as the work happened — `dr backup`, whose text output
// interleaves the bundle line with the offsite and retention lines it can only
// produce after that point.
//
// Buffering those lines to hand them to renderOut as one textFn was rejected: on
// the offsite-failure path the command returns an error, and today's stdout
// already carries the "DR bundle written" line by then. A buffer that is dropped
// on the error return would delete an operator's only record that the local
// bundle IS on disk — a text change, on the one path where the text matters most.
//
// So the text branch here writes nothing (it already did, in place, through
// commentaryOut) and the json branch emits the document. It goes through
// renderOut on purpose: one marshaller, one indentation, one trailing newline for
// the whole CLI.
func renderJSONOnly(cmd *cobra.Command, value any) error {
	return renderOut(cmd, func(io.Writer) error { return nil }, value)
}
