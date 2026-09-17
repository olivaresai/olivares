// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// runArgvForCode drives one command tree with argv and returns the code the
// process would exit with, plus the message the operator would read.
//
// It reads the code the way main.go does — exitcode.From on the error ExecuteC
// returned — rather than asserting on the error's text, because the text is
// exactly what is NOT changing here and the code is what is.
func runArgvForCode(t *testing.T, newCmd func() *cobra.Command, argv ...string) (int, string) {
	t.Helper()
	// resolveTenant falls back to the environment, so a tenant leaking in from
	// the developer's shell would turn every refusal row below into a different
	// failure further down the command. Clear both names it reads.
	t.Setenv("OLIVARES_TENANT", "")
	t.Setenv("OLIVARES_HOOK_PEP_TENANT", "")
	cmd := newCmd()
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(argv)
	err := cmd.Execute()
	if err == nil {
		return exitcode.OK, ""
	}
	return exitcode.From(err), err.Error()
}

// TestArgumentRefusalsAreUsageErrors pins QA-06: a RunE that refuses because of
// what the CALLER typed must exit Usage (2), the same code cobra's own machinery
// already produces for a missing required flag or an unknown flag.
//
// Measured on the source this test was written against, every row below exited 1
// — "generic failure with no more specific classification" — so a CI gate could
// not tell a typo from a database that was really broken, while the identical
// mistake on a sibling command (`dr verify` without --in, `members grant` without
// --user) already exited 2.
//
// The message text is deliberately NOT changed by the repair, so this test
// asserts both: the code moves, the sentence does not.
func TestArgumentRefusalsAreUsageErrors(t *testing.T) {
	dir := t.TempDir()

	for _, tc := range []struct {
		name    string
		newCmd  func() *cobra.Command
		argv    []string
		wantMsg string
	}{
		// The shared helper, through four of its nineteen call sites in three
		// different files and two different command groups: classifying one call
		// site instead of resolveTenant itself leaves the others red.
		{"audit verify without a tenant", newAuditCmd,
			[]string{"verify", "--data-dir", dir, "--strict"},
			"tenant required: pass --tenant or set $OLIVARES_TENANT"},
		{"audit export without a tenant", newAuditCmd,
			[]string{"export", "--data-dir", dir},
			"tenant required: pass --tenant or set $OLIVARES_TENANT"},
		{"eventing subscriptions ls without a tenant", newEventingCmd,
			[]string{"subscriptions", "ls", "--data-dir", dir},
			"tenant required: pass --tenant or set $OLIVARES_TENANT"},
		{"audit observe-report without a tenant", newAuditCmd,
			[]string{"observe-report", "--data-dir", dir},
			"tenant required: pass --tenant or set $OLIVARES_TENANT"},

		// The per-command refusals the QA row measured, plus the two siblings
		// that live in the same RunE and were found with them.
		{"db check with no DSN at all", newDBCmd,
			[]string{"check"},
			"nothing to check: pass --dsn (and optionally --owner-dsn / --admin-dsn)"},
		{"db check with an unknown engine", newDBCmd,
			[]string{"check", "--engine", "zzqq", "--dsn", "x"},
			`--engine "zzqq" must be sqlite or postgres`},
		{"db check with a DSN reference that does not resolve", newDBCmd,
			[]string{"check", "--engine", "sqlite", "--dsn", "env:NO_SUCH_VAR_ZZQQ"},
			`--dsn: environment variable "NO_SUCH_VAR_ZZQQ" is not set`},
		{"security check with no feed", newSecurityCmd,
			[]string{"check"},
			"olivares security check: --feed <advisories.json> is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := runArgvForCode(t, tc.newCmd, tc.argv...)
			if code != exitcode.Usage {
				t.Errorf("exit = %d, want %d (Usage): %s", code, exitcode.Usage, msg)
			}
			if msg != tc.wantMsg {
				t.Errorf("message changed:\n got %q\nwant %q", msg, tc.wantMsg)
			}
		})
	}
}

// TestNonArgumentFailuresStayGeneric is the control the rule above has to earn.
//
// "Classify the refusals about the caller's own arguments" is only a contract if
// something is left unclassified: a repair that wrapped every error out of these
// RunEs in Usage would pass the table above and would tell an operator that a
// feed file the caller named correctly, but which cannot be read, was a typo.
func TestNonArgumentFailuresStayGeneric(t *testing.T) {
	dir := t.TempDir()

	t.Run("an unreadable advisories feed is not a usage error", func(t *testing.T) {
		missing := filepath.Join(dir, "no-such-feed.json")
		code, msg := runArgvForCode(t, newSecurityCmd, "check", "--feed", missing)
		if code != exitcode.Err {
			t.Errorf("exit = %d, want %d (Err): %s", code, exitcode.Err, msg)
		}
		if !strings.Contains(msg, "read advisories feed") {
			t.Errorf("expected the read failure to be named, got %q", msg)
		}
	})

	t.Run("a tenant that is present but malformed is not the missing-tenant refusal", func(t *testing.T) {
		code, msg := runArgvForCode(t, newAuditCmd, "export", "--data-dir", dir, "--tenant", "not-a-uuid")
		if code != exitcode.Err {
			t.Errorf("exit = %d, want %d (Err): %s", code, exitcode.Err, msg)
		}
		if !strings.HasPrefix(msg, "--tenant:") {
			t.Errorf("expected the parse failure to be named, got %q", msg)
		}
	})
}
