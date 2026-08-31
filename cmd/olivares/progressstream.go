// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"io"

	"github.com/spf13/cobra"
)

// progressstream.go answers the one question that renderOut cannot: where does a
// leaf put the lines it prints BEFORE it knows the answer?
//
// renderOut (render.go) serves a leaf that computes a result and then renders it
// once. Two leaves in the release/PSIRT ceremony are not shaped like that: they
// narrate. `security drill` prints `ok <step> <duration>` as each step passes,
// and `upgrade` prints its OTA key, its source, its CRL warnings and its whole
// plan before it downloads anything — a sequence that can take minutes and can
// still fail after the narration.
//
// Those lines cannot go into renderOut's text closure. Deferring them to the end
// would delete them from exactly the run that needs them (the one that fails
// halfway), and hiding a multi-minute download behind a silent terminal is a
// regression no JSON pane is worth. Nor can they stay on stdout under -o json:
// prose interleaved with a document leaves stdout unparseable, which is the
// entire defect VER-06 exists to remove.
//
// So the narration MOVES STREAM instead of moving in time: stdout when the
// operator is reading it, stderr when a script is parsing stdout. That is the
// ordinary convention for progress on a machine-readable stdout, it preserves
// every byte on both panes, and it changes nothing without -o json — the text
// pane keeps writing to stdout in the same order it always did.
//
// WHAT IT IS NOT FOR: bytes that are the command's PRODUCT rather than its
// commentary. `upgrade --install-timer` prints two systemd unit files; those are
// an artifact, like the OTA manifest cmd_release_manifest.go refuses to reformat,
// and they stay on stdout.
func progressStream(cmd *cobra.Command) io.Writer {
	// An unreadable --output is not this function's error to report: renderOut
	// asks the same question later and fails the command properly. Falling back to
	// stdout here keeps the pre-existing behavior in a case that cannot survive
	// to the render anyway.
	if format, err := selectedOutput(cmd); err == nil && format == "json" {
		return cmd.ErrOrStderr()
	}
	return cmd.OutOrStdout()
}
