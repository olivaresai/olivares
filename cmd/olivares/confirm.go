// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// addYesFlag declares the escape hatch every destructive verb must offer, with
// one wording so an operator learns it once.
func addYesFlag(cmd *cobra.Command, yes *bool) {
	cmd.Flags().BoolVarP(yes, "yes", "y", false,
		"proceed without the confirmation prompt (required in a non-interactive session)")
}

// interactiveStdin reports whether r is a terminal a human can answer at. It is
// a variable so a test can exercise the interactive branch without a pty.
//
// It asks isatty, not os.ModeCharDevice. The mode bit was the first attempt and
// it is wrong in the direction that matters: /dev/null is a character device, so
// `olivares … rm </dev/null` looked interactive, and so does a pty-less pipe on
// some platforms. The failure mode of getting this wrong is not a cosmetic
// message — `yes | olivares agent workspace rm … --recursive` would have counted
// as consent, which is precisely what the confirmation exists to prevent.
var interactiveStdin = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// confirmDestructive gates an irreversible action behind an explicit yes.
//
// WHAT THIS IS AND IS NOT. It is a guard against an UNATTENDED invocation — a
// cron job, a CI step, a `yes |` pipeline — reaching a destructive verb by
// accident. It is NOT proof that a human is present: a program can allocate a
// pseudo-terminal and answer, and isatty cannot tell that apart from a person.
// The sol-max contrast demonstrated exactly that with `script`. Anything needing
// real human authorization belongs in the control plane's approval path, not in
// a terminal probe.
//
// The three states are deliberately different, because conflating them is how a
// destructive command ends up running unattended:
//
//   - --yes           the operator already decided; proceed.
//   - a terminal      ask, and proceed only on an explicit y/yes.
//   - NOT a terminal  REFUSE, and say that --yes is how to mean it. A prompt
//     written to a pipe is answered by EOF, which is not consent — it is the
//     absence of a human. Before this, `agent workspace rm --recursive` deleted a
//     subtree with no confirmation at all, in any session.
//
// what should name the specific thing being destroyed ("delete 12 paths under
// reports/ in workspace ws-123"), never the verb alone: the operator is being
// asked to check a target, not to acknowledge a category.
func confirmDestructive(cmd *cobra.Command, assumeYes bool, what string) error {
	if assumeYes {
		return nil
	}
	in := cmd.InOrStdin()
	if !interactiveStdin(in) {
		return exitcode.New(exitcode.Usage, fmt.Errorf(
			"refusing to %s without confirmation: this session is not interactive, "+
				"so there is nobody to ask — pass --yes to state the intent explicitly", what))
	}
	// A prompt that could not be written is a question nobody was asked. Refuse
	// rather than proceed on an answer to an invisible prompt.
	if _, werr := fmt.Fprintf(cmd.ErrOrStderr(),
		"About to %s.\nThis cannot be undone. Continue? [y/N]: ", what); werr != nil {
		return exitcode.New(exitcode.Err, fmt.Errorf(
			"refusing to %s: the confirmation prompt could not be shown: %w", what, werr))
	}
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return exitcode.New(exitcode.Usage, fmt.Errorf("aborted: %s was not confirmed", what))
}
