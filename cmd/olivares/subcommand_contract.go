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

// groupStubAnnotation marks a command GROUP whose RunE this file installed. The
// RunE is not a verb an operator invokes — it exists only because cobra refuses
// to validate the arguments of a command it considers non-runnable (see
// enforceSubcommandContract). The annotation lets the help renderer keep the
// group's usage block exactly as it was before that RunE existed.
const groupStubAnnotation = "olivares.ai/group-stub"

// enforceSubcommandContract makes the exit-code contract printed in the root
// help (`2  usage error (unknown flag or bad arguments)`) true for the WHOLE
// command tree, in one traversal, instead of command by command.
//
// WHY A TRAVERSAL AND NOT A FLAG ON EACH GROUP. Two independent cobra
// behaviors conspire to turn `olivares <group> <typo>` into a silent success,
// and neither is reachable by configuring the group:
//
//  1. cobra command.go:955 — `if !c.Runnable() { return flag.ErrHelp }` runs
//     BEFORE `ValidateArgs` at :968. A group has no RunE, so it is not
//     runnable, so its `Args` validator is never consulted. Setting
//     `cobra.NoArgs` on a group is therefore INERT — this tree proves it: `mcp`
//     and `mcp pins` both carried an `Args` validator and both still exited 0.
//  2. cobra command.go:1151 — `ExecuteC` treats that `flag.ErrHelp` as "the
//     user asked for help": it prints the help text to STDOUT and returns a
//     NIL error. The process exits 0.
//
// The only cobra-supported way to reach argument validation on a group is to
// make the group runnable. So the traversal installs a RunE that prints the
// group's help (preserving what `olivares agent` alone has always done) and an
// `Args` validator that rejects a leftover positional argument as a usage
// error. cobra's `Find` (command.go:775) only applies its built-in `legacyArgs`
// check when `Args` is nil, and `legacyArgs` (args.go:35) only rejects unknown
// commands at the ROOT — which is why, before this, the root was the one place
// that noticed a typo at all.
//
// The traversal also classifies every OTHER command's argument-validation
// failure as exit 2. A leaf like `olivares version zzqq` already produced the
// right message ("unknown command ... for ..."), but `exitcode.From` found no
// code attached and defaulted to 1, so a script could not tell a typo from a
// real failure.
//
// It must walk the tree rather than consult a list of command names: a list
// goes stale the moment someone adds a command, and the defect would come back
// silently. TestSubcommandContract walks the same tree and fails if any node
// escapes.
func enforceSubcommandContract(root *cobra.Command) {
	// cobra adds `help` inside ExecuteC, i.e. after this constructor returns.
	// Materialize it here so it receives the same contract as everything else
	// AND so the tests walk the same tree the binary actually runs. Both cobra
	// initialisers are no-ops when the command already exists (command.go:1268,
	// completions.go:750), and this tree registers its own `completion`.
	root.InitDefaultHelpCmd()

	// Capture cobra's stock help renderer BEFORE overriding it; the value it
	// returns takes the command as a parameter, so it works for every node.
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(c *cobra.Command, args []string) {
		// cobra prints the `<path> [flags]` usage line only for a runnable
		// command (command.go:2058). A group stub is runnable only as an
		// implementation detail of argument validation, so hide that line —
		// otherwise every group's help would grow a line advertising an
		// invocation that does nothing but print this same help.
		if isGroupStub(c) {
			saved := c.RunE
			c.RunE = nil
			defer func() { c.RunE = saved }()
		}
		defaultHelp(c, args)
	})

	walkCommands(root, func(cmd *cobra.Command) {
		switch {
		case cmd.Name() == "help" && cmd.HasParent() && cmd.Parent() == root:
			adoptHelpCommand(cmd)
		case cmd.HasSubCommands() && !cmd.Runnable():
			makeGroupStub(cmd)
		default:
			// Leaves, and the genuinely runnable parents (`quickstart`), keep
			// their own validator; only its exit classification changes.
			cmd.Args = usageCodedArgs(cmd.Args)
		}
	})
}

// walkCommands applies fn to cmd and every command beneath it.
func walkCommands(cmd *cobra.Command, fn func(*cobra.Command)) {
	fn(cmd)
	for _, child := range cmd.Commands() {
		walkCommands(child, fn)
	}
}

// isGroupStub reports whether cmd's RunE was installed by makeGroupStub.
func isGroupStub(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Annotations[groupStubAnnotation] == "true"
}

// makeGroupStub turns a command group into something cobra will validate.
func makeGroupStub(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[groupStubAnnotation] = "true"
	cmd.Args = usageCodedArgs(rejectUnknownSubcommand)
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		// Reached only with zero positional arguments — `olivares agent` on its
		// own. Help on stdout, exit 0: unchanged from before this contract.
		return c.Help()
	}
}

// adoptHelpCommand replaces the Run of cobra's built-in `help` command with a
// RunE, so that `olivares help <not-a-command>` reports a usage error instead
// of printing "Unknown help topic" and exiting 0. cobra's version cannot do
// this: it is a `Run`, which has no way to return an error (command.go:1269).
//
// It also cannot detect the unknown topic the way cobra's version did. cobra
// relied on Find returning an error, and Find only produces one through
// legacyArgs, which it skips now that the root carries an Args validator
// (command.go:775). So resolve the topic and check what Find could NOT consume:
// a leftover positional means the topic named a command that does not exist, at
// whatever depth. Leftover flags are ignored, as cobra's help always did.
func adoptHelpCommand(cmd *cobra.Command) {
	cmd.Run = nil
	cmd.Args = usageCodedArgs(nil)
	if strings.TrimSpace(cmd.Example) == "" {
		cmd.Example = "  olivares help\n  olivares help agent session create"
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		target, rest, err := c.Root().Find(args)
		unresolved := ""
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				unresolved = a
				break
			}
		}
		if target == nil || err != nil || unresolved != "" {
			at := c.Root()
			if target != nil {
				at = target
			}
			return exitcode.New(exitcode.Usage, fmt.Errorf(
				"unknown help topic %q for %q%s",
				strings.Join(args, " "), at.CommandPath(),
				suggestionBlock(at, []string{unresolved})))
		}
		target.InitDefaultHelpFlag()
		target.InitDefaultVersionFlag()
		return target.Help()
	}
}

// rejectUnknownSubcommand refuses any positional argument reaching a group. The
// wording matches cobra's own so the message is identical to the one the root
// has always produced for an unknown top-level command.
func rejectUnknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return fmt.Errorf("unknown command %q for %q%s",
		args[0], cmd.CommandPath(), suggestionBlock(cmd, args))
}

// usageCodedArgs wraps a positional-argument validator so that whatever it
// rejects exits 2 (usage) rather than 1 (generic). A validator that already
// classified its error keeps that classification.
//
// A nil inner validator becomes cobra.ArbitraryArgs, which is exactly what
// cobra's ValidateArgs substitutes for a nil Args (command.go:1172) — so
// installing this wrapper never tightens what a command accepts. It does move
// the check: with Args non-nil, cobra's Find stops applying legacyArgs, whose
// only effect for a non-root command was to return nil anyway (args.go:30-38).
func usageCodedArgs(inner cobra.PositionalArgs) cobra.PositionalArgs {
	if inner == nil {
		inner = cobra.ArbitraryArgs
	}
	return func(cmd *cobra.Command, args []string) error {
		err := inner(cmd, args)
		if err == nil {
			return nil
		}
		if exitcode.From(err) != exitcode.Err {
			return err
		}
		return exitcode.New(exitcode.Usage, err)
	}
}

// suggestionBlock renders cobra's "Did you mean this?" hint. cobra keeps its
// own renderer unexported (findSuggestions, command.go:781); this reproduces it
// from the exported SuggestionsFor, honoring the same two knobs.
func suggestionBlock(cmd *cobra.Command, args []string) string {
	if cmd.DisableSuggestions || len(args) == 0 {
		return ""
	}
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	suggestions := cmd.SuggestionsFor(args[0])
	if len(suggestions) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nDid you mean this?\n")
	for _, s := range suggestions {
		fmt.Fprintf(&sb, "\t%v\n", s)
	}
	return sb.String()
}
