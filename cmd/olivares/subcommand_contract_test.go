// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// unknownToken is deliberately not a prefix of, and far from, every command name
// in the tree, so cobra's suggestion machinery cannot rescue it.
const unknownToken = "zzqq-not-a-subcommand"

// commandPaths walks the real tree and returns the argv of every GROUP (a
// command with children) and of every command in the tree.
//
// It walks rather than consulting a list on purpose: a hard-coded list of
// command names goes stale the moment somebody adds a command, and the defect
// this file exists to prevent — a mistyped subcommand exiting 0 — would come
// back in silence for the new command only.
func contractCommandPaths(t *testing.T) (groups [][]string, all [][]string) {
	t.Helper()
	var walk func(*cobra.Command, []string)
	walk = func(cmd *cobra.Command, prefix []string) {
		if cmd.HasParent() {
			all = append(all, prefix)
			if cmd.HasSubCommands() {
				groups = append(groups, prefix)
			}
		}
		for _, child := range cmd.Commands() {
			walk(child, append(append([]string{}, prefix...), child.Name()))
		}
	}
	walk(newRootCmd(), nil)
	return groups, all
}

// TestUnknownSubcommandIsAUsageError is the blanket guarantee: for EVERY group
// in the tree, a mistyped subcommand must be a usage error (exit 2) reported on
// stderr — never help on stdout with exit 0, which `set -e` reads as success.
func TestUnknownSubcommandIsAUsageError(t *testing.T) {
	groups, _ := contractCommandPaths(t)
	if len(groups) < 30 {
		t.Fatalf("walked only %d groups; the tree walk is not seeing the real tree", len(groups))
	}
	for _, path := range groups {
		name := strings.Join(path, " ")
		t.Run(name, func(t *testing.T) {
			root := newRootCmd()
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetArgs(append(append([]string{}, path...), unknownToken))
			_, err := root.ExecuteC()
			if err == nil {
				t.Fatalf("`olivares %s %s` succeeded; a mistyped subcommand must fail", name, unknownToken)
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("`olivares %s %s` exit = %d, want %d (usage): %v",
					name, unknownToken, got, exitcode.Usage, err)
			}
			if out.Len() != 0 {
				t.Fatalf("`olivares %s %s` wrote %d bytes to stdout; a rejected invocation "+
					"must not put help on the good channel:\n%s", name, unknownToken, out.Len(), out.String())
			}
			if !strings.Contains(err.Error(), unknownToken) {
				t.Fatalf("error does not name the offending token %q: %v", unknownToken, err)
			}
		})
	}
}

// TestUnknownTopLevelCommandIsAUsageError pins the root, which cobra alone got
// half right: it reported the unknown command but exited 1, so a script could
// not tell a typo from a real failure.
func TestUnknownTopLevelCommandIsAUsageError(t *testing.T) {
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{unknownToken})
	_, err := root.ExecuteC()
	if err == nil {
		t.Fatal("an unknown top-level command must fail")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit = %d, want %d (usage): %v", got, exitcode.Usage, err)
	}
	if out.Len() != 0 {
		t.Fatalf("wrote %d bytes to stdout: %s", out.Len(), out.String())
	}
}

// TestUnknownHelpTopicIsAUsageError covers `olivares help <typo>`, which cobra
// implements as a Run (no error return) that prints "Unknown help topic" and
// exits 0.
func TestUnknownHelpTopicIsAUsageError(t *testing.T) {
	for _, args := range [][]string{
		{"help", unknownToken},
		{"help", "agent", unknownToken},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := newRootCmd()
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			root.SetArgs(args)
			_, err := root.ExecuteC()
			if err == nil {
				t.Fatalf("`olivares %s` succeeded; an unknown help topic must fail", strings.Join(args, " "))
			}
			if got := exitcode.From(err); got != exitcode.Usage {
				t.Fatalf("exit = %d, want %d (usage): %v", got, exitcode.Usage, err)
			}
		})
	}
}

// TestGroupWithNoArgumentsStillPrintsHelp guards the behavior the fix must NOT
// break: `olivares agent` on its own is a legitimate way to discover the group,
// and must keep printing help on stdout and succeeding.
func TestGroupWithNoArgumentsStillPrintsHelp(t *testing.T) {
	groups, _ := contractCommandPaths(t)
	for _, path := range groups {
		name := strings.Join(path, " ")
		root := newRootCmd()
		target, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("cannot resolve %q: %v", name, err)
		}
		// `quickstart` has children AND a real RunE of its own — invoking it
		// bare starts an engine, which is not this test's business.
		if !isGroupStub(target) {
			continue
		}
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetArgs(path)
		if _, err := root.ExecuteC(); err != nil {
			t.Fatalf("`olivares %s` must succeed and print help, got: %v", name, err)
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Fatalf("`olivares %s` printed no usage block on stdout:\n%s", name, out.String())
		}
	}
}

// TestGroupHelpDoesNotAdvertiseAFlagsInvocation pins the cosmetic contract: the
// RunE installed on a group exists only so cobra will validate its arguments,
// so the group's help must not grow a `olivares <group> [flags]` usage line
// advertising an invocation whose entire effect is to print that same help.
func TestGroupHelpDoesNotAdvertiseAFlagsInvocation(t *testing.T) {
	groups, _ := contractCommandPaths(t)
	for _, path := range groups {
		name := strings.Join(path, " ")
		root := newRootCmd()
		target, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("cannot resolve %q: %v", name, err)
		}
		if !isGroupStub(target) {
			continue
		}
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetArgs(append(append([]string{}, path...), "--help"))
		if _, err := root.ExecuteC(); err != nil {
			t.Fatalf("`olivares %s --help` failed: %v", name, err)
		}
		if strings.Contains(out.String(), "olivares "+name+" [flags]") {
			t.Fatalf("`olivares %s --help` advertises a [flags] invocation that only prints help:\n%s",
				name, out.String())
		}
	}
}

// TestEveryCommandValidatesItsArguments is the anti-escape net: the traversal
// must reach every node. A command left with a nil Args validator is one cobra
// would silently hand arbitrary arguments to.
func TestEveryCommandValidatesItsArguments(t *testing.T) {
	root := newRootCmd()
	var missing []string
	walkCommands(root, func(cmd *cobra.Command) {
		if cmd.Args == nil {
			missing = append(missing, cmd.CommandPath())
		}
	})
	if len(missing) > 0 {
		t.Fatalf("%d command(s) escaped the subcommand contract: %s",
			len(missing), strings.Join(missing, ", "))
	}
}

// TestMissingRequiredFlagIsAUsageError pins the classification cobra raises
// past every hook a command tree can install: `connector init x` without its
// required flags is a wrong invocation, not a generic failure.
func TestMissingRequiredFlagIsAUsageError(t *testing.T) {
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"connector", "init", "example"})
	cmd, err := root.ExecuteC()
	if err == nil {
		t.Fatal("`connector init` without its required flags must fail")
	}
	// runMain classifies through cobra's own validators; mirror that here.
	code := exitcode.From(err)
	if code == exitcode.Err && cmd != nil &&
		(cmd.ValidateRequiredFlags() != nil || cmd.ValidateFlagGroups() != nil) {
		code = exitcode.Usage
	}
	if code != exitcode.Usage {
		t.Fatalf("exit = %d, want %d (usage): %v", code, exitcode.Usage, err)
	}
}
