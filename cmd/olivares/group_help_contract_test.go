// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// This file pins the half of the subcommand contract that no error-shaped
// witness can see. cobra answers `--help` and `--version` at command.go:934 and
// :939, BEFORE ValidateArgs at :968, and ExecuteC (:1152-1154) reports the first
// of those as a NIL error — so `olivares <group> <typo> --help` printed the
// group's help on stdout and exited 0 while the identical typo without `--help`
// exited 2. Every test below therefore drives classifyOutcome(root.ExecuteC()),
// which is what the process itself does, instead of asserting on the error.
//
// Scope is the GROUPS makeGroupStub marked, the root included. A leaf keeps its
// existing help behavior even when its operands are missing or partial, which
// TestValidCommandHelpStillSucceeds proves over the whole tree; a leaf-help
// contract is separate work.

// runOutcome executes argv against a fresh tree and reports exactly what the
// process would: the exit code, what would have been printed to stderr, and the
// bytes that reached stdout.
func runOutcome(t *testing.T, argv ...string) (code int, printed string, stdout string) {
	t.Helper()
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(argv)
	code, printable := classifyOutcome(root.ExecuteC())
	if printable != nil {
		printed = printable.Error()
	}
	return code, printed, out.String()
}

// TestGroupHelpDoesNotRescueAnUnknownSubcommand is the blanket guarantee for the
// three forms a mistyped subcommand takes once help is on the line. It walks the
// real tree for the same reason TestUnknownSubcommandIsAUsageError does: a list
// of command names goes stale the moment somebody adds a command, and the defect
// would come back in silence for the new one only.
func TestGroupHelpDoesNotRescueAnUnknownSubcommand(t *testing.T) {
	groups, _ := contractCommandPaths(t)
	if len(groups) < 30 {
		t.Fatalf("walked only %d groups; the tree walk is not seeing the real tree", len(groups))
	}
	forms := [][]string{{unknownToken, "--help"}, {unknownToken, "-h"}, {"--help", unknownToken}}

	covered, skipped := 0, []string(nil)
	for _, path := range groups {
		name := strings.Join(path, " ")
		target, _, err := newRootCmd().Find(path)
		if err != nil {
			t.Fatalf("cannot resolve %q: %v", name, err)
		}
		// A group that is ALSO runnable in its own right (`quickstart`) keeps
		// its own validator and is not a group stub, so it is outside this
		// increment: re-asking an arbitrary command's validator after help is
		// the separate leaf-help contract. Pin what it does today instead of
		// skipping it in silence — if that changes, this test says so.
		if !isGroupStub(target) {
			skipped = append(skipped, name)
			if code, _, _ := runOutcome(t, append(append([]string{}, path...), unknownToken)...); code != exitcode.Usage {
				t.Errorf("`olivares %s %s` exit = %d, want %d: the runnable parent's own "+
					"validator must keep refusing a typo", name, unknownToken, code, exitcode.Usage)
			}
			continue
		}
		covered++
		for _, form := range forms {
			argv := append(append([]string{}, path...), form...)
			line := "olivares " + strings.Join(argv, " ")
			code, printed, stdout := runOutcome(t, argv...)
			if code != exitcode.Usage {
				t.Errorf("`%s` exit = %d, want %d (usage): asking for help must not rescue a "+
					"command that does not exist", line, code, exitcode.Usage)
			}
			if stdout != "" {
				t.Errorf("`%s` wrote %d bytes to stdout; a rejected invocation must not put "+
					"help on the good channel:\n%s", line, len(stdout), stdout)
			}
			if !strings.Contains(printed, unknownToken) {
				t.Errorf("`%s` does not name the offending token %q: %q", line, unknownToken, printed)
			}
		}
	}
	if covered < 100 {
		t.Fatalf("only %d group stubs covered; the contract must reach the whole tree", covered)
	}
	t.Logf("%d group stubs x %d forms = %d rows; %d runnable parent(s) outside this increment: %s",
		covered, len(forms), covered*len(forms), len(skipped), strings.Join(skipped, ", "))
}

// TestUnknownGroupSubcommandWithHelpIsByteIdenticalToWithoutIt is the reason the
// suppression lives in the help renderer and not only in the classifier: the two
// spellings of one mistake must be one answer, not two that happen to share an
// exit code.
func TestUnknownGroupSubcommandWithHelpIsByteIdenticalToWithoutIt(t *testing.T) {
	groups, _ := contractCommandPaths(t)
	for _, path := range groups {
		name := strings.Join(path, " ")
		target, _, err := newRootCmd().Find(path)
		if err != nil {
			t.Fatalf("cannot resolve %q: %v", name, err)
		}
		if !isGroupStub(target) {
			continue
		}
		bare := append(append([]string{}, path...), unknownToken)
		wantCode, wantPrinted, wantStdout := runOutcome(t, bare...)
		gotCode, gotPrinted, gotStdout := runOutcome(t, append(bare, "--help")...)
		if gotCode != wantCode || gotPrinted != wantPrinted || gotStdout != wantStdout {
			t.Errorf("`olivares %s %s --help` differs from the same line without --help:\n"+
				"  code   %d vs %d\n  stderr %q vs %q\n  stdout %d vs %d bytes",
				name, unknownToken, gotCode, wantCode, gotPrinted, wantPrinted,
				len(gotStdout), len(wantStdout))
		}
	}
}

// TestRootUnknownCommandWithHelpOrVersionIsAUsageError covers the root, which
// contractCommandPaths does not walk, and the version short-circuit, which is
// the OTHER pre-validation return and exists only here: cobra declares
// `--version` where Version is set (command.go:1238-1241), and this tree sets it
// on the root alone.
func TestRootUnknownCommandWithHelpOrVersionIsAUsageError(t *testing.T) {
	for _, argv := range [][]string{
		{unknownToken, "--help"},
		{unknownToken, "-h"},
		{"--help", unknownToken},
		{"-h", unknownToken},
		{unknownToken, "--version"},
	} {
		line := "olivares " + strings.Join(argv, " ")
		code, printed, stdout := runOutcome(t, argv...)
		if code != exitcode.Usage {
			t.Errorf("`%s` exit = %d, want %d (usage)", line, code, exitcode.Usage)
		}
		if !strings.Contains(printed, unknownToken) {
			t.Errorf("`%s` does not name the offending token: %q", line, printed)
		}
		if argv[len(argv)-1] == "--version" {
			// RECORDED LIMITATION, pinned rather than hidden. cobra prints the
			// version template inline at command.go:939-953 and exports no
			// version callback to intercept (getVersionTemplateFunc is
			// unexported), so this one line survives on stdout while the exit
			// code and the refusal are correct. One command, one line: the
			// assertion fails if that residue ever grows.
			if lines := strings.Count(stdout, "\n"); lines != 1 || !strings.HasPrefix(stdout, "olivares version ") {
				t.Errorf("`%s` stdout residue is not the single version line: %q", line, stdout)
			}
			continue
		}
		if stdout != "" {
			t.Errorf("`%s` wrote %d bytes to stdout:\n%s", line, len(stdout), stdout)
		}
	}
}

// TestValidCommandHelpStillSucceeds is the preservation half, swept over the
// WHOLE tree rather than sampled: every command that exists must still answer
// `--help` with its help text on stdout and exit 0.
//
// The rows that make this more than a formality are the leaves whose own Args
// validator refuses an empty or partial operand list — `connector init --help`
// is `cobra.ExactArgs(1)` asked with none. Help for operands you have not
// supplied yet is how an operator learns what to supply; a contract that
// re-validated every leaf after help would turn that into a usage error, which
// is exactly why this increment is scoped to groups.
func TestValidCommandHelpStillSucceeds(t *testing.T) {
	_, all := contractCommandPaths(t)
	if len(all) < 300 {
		t.Fatalf("walked only %d commands; the tree walk is not seeing the real tree", len(all))
	}
	paths := append([][]string{nil}, all...) // nil = the root itself
	for _, path := range paths {
		name := "olivares " + strings.Join(path, " ")
		code, printed, stdout := runOutcome(t, append(append([]string{}, path...), "--help")...)
		if code != exitcode.OK {
			t.Errorf("`%s --help` exit = %d, want 0: %s", name, code, printed)
		}
		if stdout == "" {
			t.Errorf("`%s --help` printed nothing on stdout", name)
		}
	}
	t.Logf("%d command paths swept with --help", len(paths))
}

// TestHelpFormsThatMustNotChange names the individual rows the sweep above would
// not distinguish, so a regression reports WHICH property broke instead of one
// line inside 800.
func TestHelpFormsThatMustNotChange(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		why  string
	}{
		{[]string{"--help"}, "the root's own help"},
		{[]string{"--version"}, "the root's version, with no leftover to refuse"},
		{[]string{"audit", "--help"}, "a group with nothing left over"},
		{[]string{"audit", "verify", "--help"}, "a leaf"},
		{[]string{"connector", "init", "--help"}, "a leaf whose Args is ExactArgs(1), asked with NO operand"},
		{[]string{"connector", "init", "example", "--help"}, "the same leaf with its operand supplied"},
		{[]string{"completion", "bash", "--help"}, "DisableFlagParsing: cobra never sets the help flag"},
		{[]string{"agent"}, "a bare group still discovers itself"},
		{[]string{"access-map", "--help"}, "an alias resolved by findNext"},
		{[]string{"agent", "session", "list", "--help"}, "an alias on a leaf"},
		{[]string{"help"}, "the adopted help command"},
		{[]string{"help", "agent"}, "a valid help topic"},
	} {
		line := "olivares " + strings.Join(tc.argv, " ")
		code, printed, stdout := runOutcome(t, tc.argv...)
		if code != exitcode.OK {
			t.Errorf("`%s` exit = %d, want 0 (%s): %s", line, code, tc.why, printed)
		}
		if stdout == "" {
			t.Errorf("`%s` printed nothing on stdout (%s)", line, tc.why)
		}
	}
}

// TestRejectedInvocationsThatAlreadyFailedStillFail is the independent control
// set: rows whose exit 2 does NOT come from this increment. If a change to the
// predicate ever started answering them, these would keep passing for the wrong
// reason — so each asserts the message that names its real cause.
func TestRejectedInvocationsThatAlreadyFailedStillFail(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{unknownToken}, "unknown command"},
		{[]string{"audit", unknownToken}, "unknown command"},
		{[]string{unknownToken, "--", "--help"}, "unknown command"},
		{[]string{"--", unknownToken}, "unknown command"},
		{[]string{"audit", "--", unknownToken}, "unknown command"},
		{[]string{"serve", "--not-a-flag-zzqq"}, "unknown flag"},
		{[]string{"completion", "bash", unknownToken, "--help"}, "this command takes none"},
		{[]string{"help", unknownToken}, "unknown help topic"},
		{[]string{"audit", unknownToken, "--version"}, "unknown flag"},
	} {
		line := "olivares " + strings.Join(tc.argv, " ")
		code, printed, stdout := runOutcome(t, tc.argv...)
		if code != exitcode.Usage {
			t.Errorf("`%s` exit = %d, want %d (usage)", line, code, exitcode.Usage)
		}
		if !strings.Contains(printed, tc.want) {
			t.Errorf("`%s` must still fail for its own reason %q, got %q", line, tc.want, printed)
		}
		if stdout != "" {
			t.Errorf("`%s` wrote %d bytes to stdout", line, len(stdout))
		}
	}
}

// TestClassifyOutcomePreservesExistingClassifications guards the extraction
// itself. Moving runMain's tail into a named function must not move any verdict
// with it: the silent outcomes, the errAffected sentinel and the precedence of
// an already-typed error are the same decisions they were inline.
func TestClassifyOutcomePreservesExistingClassifications(t *testing.T) {
	plain := errors.New("something broke")
	for _, tc := range []struct {
		name        string
		cmd         *cobra.Command
		err         error
		wantCode    int
		wantPrinted string
	}{
		{"success", nil, nil, exitcode.OK, ""},
		{"unclassified failure is generic and explained", nil, plain, exitcode.Err, "something broke"},
		{"an already-typed error keeps its code", nil,
			exitcode.New(exitcode.Auth, plain), exitcode.Auth, "something broke"},
		{"wrapped errAffected exits degraded and quietly", nil,
			fmt.Errorf("context: %w", errAffected), exitcode.Degraded, ""},
		{"a silent coded error prints nothing; its report is on stdout", nil,
			exitcode.New(exitcode.Indeterminate, nil), exitcode.Indeterminate, ""},
		{"a nil command cannot be re-asked about its flags", nil, plain, exitcode.Err, "something broke"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, printable := classifyOutcome(tc.cmd, tc.err)
			printed := ""
			if printable != nil {
				printed = printable.Error()
			}
			if code != tc.wantCode || printed != tc.wantPrinted {
				t.Fatalf("classifyOutcome = (%d, %q), want (%d, %q)",
					code, printed, tc.wantCode, tc.wantPrinted)
			}
		})
	}

	// The re-ask of cobra's own validators needs a real resolved command:
	// `connector init example` reaches RunE's guard with a required flag
	// missing, which cobra raises past every hook as a plain error.
	t.Run("a missing required flag is still re-asked and becomes usage", func(t *testing.T) {
		code, printed, _ := runOutcome(t, "connector", "init", "example")
		if code != exitcode.Usage {
			t.Fatalf("exit = %d, want %d (usage): %s", code, exitcode.Usage, printed)
		}
	})
}
