// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestNotSignedInIsOneSentenceAndOneExitCode: the CLI audit of 09b found three answers
// to one condition on a computer with no sign-in. session, tool, mcp and provider said
// "Not signed in: no server is set and no context is active (config: <path>). Sign in
// first ..." (exit 2); status and auth status said "no server: set --server,
// OLIVARES_SERVER_URL, or an active client context" (exit 1); logout said "no client
// context selected; pass --context" (exit 1). Every one now says the same sentence, without
// the config path, and exits 2.
func TestNotSignedInIsOneSentenceAndOneExitCode(t *testing.T) {
	prepareModelstackCLITest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("OLIVARES_DATA_DIR", t.TempDir())
	const want = "Not signed in. Sign in first: olivares login (scripts: --server or OLIVARES_SERVER_URL)"
	for _, args := range [][]string{
		{"session", "ls"}, {"tool", "ls"}, {"mcp", "ls"}, {"provider", "ls"}, {"status"}, {"auth", "status"},
	} {
		_, _, err := execRoot(t, args...)
		if exitcode.From(err) != exitcode.Usage || err == nil || err.Error() != want {
			t.Errorf("olivares %s: err = %v (exit %d)\nwant %q", strings.Join(args, " "), err, exitcode.From(err), want)
		}
	}
	_, _, err := execRoot(t, "logout")
	if exitcode.From(err) != exitcode.Usage || err == nil || err.Error() != "Not signed in: there is no saved sign-in to remove." {
		t.Errorf("olivares logout: err = %v (exit %d)", err, exitcode.From(err))
	}
}
