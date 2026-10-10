// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// bareTokenFlag matches `--token` but not `--token-file`: the deprecated spelling
// that puts the bearer in argv and shell history (cliTokenArgvWarning).
var bareTokenFlag = regexp.MustCompile(`--token([^-\w]|$)`)

// TestNoHelpRecommendsDeprecatedTokenFlag: `tokens issue --help`
// told users to save the secret with `olivares auth login --token`, the spelling
// no help lists and that warns it is deprecated when used. A command may name a bare
// --token only when it lists a --token flag of its own (upgrade, release
// export-mirror); everywhere else the instruction is --token-file.
func TestNoHelpRecommendsDeprecatedTokenFlag(t *testing.T) {
	checked := 0
	walkCommandTree(newRootCmd(), func(c *cobra.Command) {
		if f := c.Flags().Lookup("token"); f != nil && !f.Hidden {
			return
		}
		checked++
		for _, text := range []string{c.Short, c.Long, c.Example} {
			if bareTokenFlag.MatchString(text) {
				t.Errorf("%s help recommends the deprecated --token flag:\n%s", c.CommandPath(), text)
			}
		}
	})
	if checked < 400 {
		t.Fatalf("walked %d commands; the tree has hundreds, so the walk is not looking", checked)
	}
}

// TestNoErrorRecommendsDeprecatedTokenFlag: the errors that tell a user which
// credential to pass name --token-file, as missingCLIValueError already does.
func TestNoErrorRecommendsDeprecatedTokenFlag(t *testing.T) {
	prepareModelstackCLITest(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	// Each message keyed by the sentence it must start with, so an unrelated error
	// that happens to mention --token-file cannot stand in for it.
	msgs := map[string]string{
		"this sign-in has ended": describeAPIRefusal(http.StatusUnauthorized,
			[]byte(`{"error":{"code":"unauthenticated","message":"auth: unauthenticated"}}`)),
	}
	for prefix, args := range map[string][]string{
		"no credential": {"auth", "login", "--server", "https://127.0.0.1:1"},
		"no token":      {"evals", "gate", "--server", "https://127.0.0.1:1", "--tenant", "t", "--check-id", "g"},
	} {
		// Empty stdin, not a terminal: login must refuse rather than prompt.
		_, _, err := execRootStdin(t, "", args...)
		if err == nil {
			t.Fatalf("olivares %s with no credential succeeded", strings.Join(args, " "))
		}
		msgs[prefix] = err.Error()
	}
	for prefix, msg := range msgs {
		if !strings.HasPrefix(msg, prefix) || bareTokenFlag.MatchString(msg) || !strings.Contains(msg, "--token-file") {
			t.Errorf("%q\nwant it to start %q and name --token-file, never the deprecated --token", msg, prefix)
		}
	}
}
