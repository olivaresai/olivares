// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// limitCommands is every command with an int --limit, by path below the root.
func limitCommands(t *testing.T) [][]string {
	t.Helper()
	var paths [][]string
	walkCommands(newRootCmd(), func(c *cobra.Command) {
		if f := c.Flags().Lookup("limit"); f != nil && f.Value.Type() == "int" {
			paths = append(paths, strings.Fields(c.CommandPath())[1:])
		}
	})
	if len(paths) < 10 {
		t.Fatalf("found %d commands with an int --limit; the census of 09b had 10 sites", len(paths))
	}
	return paths
}

// TestEveryLimitFlagRefusesANegativeValue is ID's sweep of 09b: `olivares users ls
// --limit -1` exited 0 with the whole list. Every command with an int --limit refuses a
// negative value while its flags are parsed, exit 2, before any request; the engine is
// never asked (no server is configured here, so a request would fail differently).
func TestEveryLimitFlagRefusesANegativeValue(t *testing.T) {
	prepareModelstackCLITest(t)
	for _, p := range limitCommands(t) {
		_, _, err := execRoot(t, append(append([]string{}, p...), "--limit", "-1")...)
		if exitcode.From(err) != exitcode.Usage || err == nil || !strings.Contains(err.Error(), "must be 1 or more") {
			t.Errorf("olivares %s --limit -1: err = %v (exit %d)", strings.Join(p, " "), err, exitcode.From(err))
		}
	}
}

// TestEveryLimitFlagKeepsZeroAsIn2610: 26.10 documented "--limit 0 = the engine's
// default", and a patch does not make a documented input fail (Root, 26.10.1). Where 0 is
// the flag's own default, an explicit 0 parses and prints the one deprecation line; where
// the default is a number (audit ls, work, messages), 0 parses silently and the command
// checks its range as 26.10 did.
func TestEveryLimitFlagKeepsZeroAsIn2610(t *testing.T) {
	root := newRootCmd()
	zeroDefaults := 0
	walkCommands(root, func(c *cobra.Command) {
		f := c.Flags().Lookup("limit")
		if f == nil || f.Value.Type() != "int" {
			return
		}
		var errb bytes.Buffer
		c.SetErr(&errb)
		if err := c.Flags().Set("limit", "0"); err != nil {
			t.Errorf("%s --limit 0: %v", c.CommandPath(), err)
			return
		}
		want := ""
		if f.DefValue == "0" {
			zeroDefaults++
			want = limitZeroWarning + "\n"
		}
		if errb.String() != want {
			t.Errorf("%s --limit 0: stderr = %q, want %q", c.CommandPath(), errb.String(), want)
		}
	})
	if zeroDefaults < 5 {
		t.Fatalf("found %d --limit flags whose default is 0; 26.10 documented five", zeroDefaults)
	}
}

// TestUsersLsLimitZeroIsTheEnginesDefaultWithOneWarning drives the documented 26.10
// spelling end to end: exit 0, the listing printed, no limit sent (the engine's own
// default), and stderr exactly the deprecation line.
func TestUsersLsLimitZeroIsTheEnginesDefaultWithOneWarning(t *testing.T) {
	prepareBootstrapCLITest(t)
	var query atomic.Value
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.RawQuery)
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	})
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("olvk_caller\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := execRoot(t, "users", "ls", "--server", srv.URL, "--token-file", tokenFile, "--limit", "0")
	if err != nil {
		t.Fatalf("users ls --limit 0 must keep working in 26.10.x: %v (stderr %q)", err, stderr)
	}
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("server hits = %d, want 1", n)
	}
	if q, _ := query.Load().(string); strings.Contains(q, "limit=") {
		t.Fatalf("query = %q: --limit 0 must leave the page size to the engine", q)
	}
	if stderr != limitZeroWarning+"\n" {
		t.Fatalf("stderr = %q, want exactly the deprecation line", stderr)
	}

	// -1 on the same command: usage, exit 2, and no request.
	_, _, err = execRoot(t, "users", "ls", "--server", srv.URL, "--token-file", tokenFile, "--limit", "-1")
	if exitcode.From(err) != exitcode.Usage {
		t.Fatalf("users ls --limit -1: err = %v (exit %d), want usage", err, exitcode.From(err))
	}
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("server hits = %d after --limit -1, want still 1", n)
	}
}

// TestTheLimitGuardReadsANumberAsTheFlagDoes is SR4's finding on 2e20b4d660: pflag reads an
// int flag with base 0, so --limit -0x1, -0b1 and -0o1 are -1, and a decimal-only guard let
// them reach the engine. The guard reads the value the same way: every negative spelling is
// refused while the flags are parsed, every spelling of 0 prints the deprecation line, and a
// positive one is the number the flag holds.
func TestTheLimitGuardReadsANumberAsTheFlagDoes(t *testing.T) {
	for _, neg := range []string{"-1", "-0x1", "-0b1", "-0o1", "-01", "-1_0"} {
		var limit int
		fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
		fs.IntVar(&limit, "limit", 0, "")
		guard := positiveLimit{Value: fs.Lookup("limit").Value, cmd: &cobra.Command{}, zeroIsDefault: true}
		if err := guard.Set(neg); err == nil || !strings.Contains(err.Error(), "must be 1 or more") {
			t.Errorf("--limit %s: err = %v, want the refusal", neg, err)
		}
	}
	for _, tc := range []struct {
		in   string
		want int
		warn bool
	}{{"0", 0, true}, {"0x0", 0, true}, {"-0", 0, true}, {"0x10", 16, false}, {"0b11", 3, false}, {"1_000", 1000, false}} {
		var limit int
		fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
		fs.IntVar(&limit, "limit", 0, "")
		var errb bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetErr(&errb)
		guard := positiveLimit{Value: fs.Lookup("limit").Value, cmd: cmd, zeroIsDefault: true}
		if err := guard.Set(tc.in); err != nil || limit != tc.want || (errb.Len() > 0) != tc.warn {
			t.Errorf("--limit %s: err = %v, limit = %d, warned = %v; want %d, warned %v", tc.in, err, limit, errb.Len() > 0, tc.want, tc.warn)
		}
	}
}
