// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// execRoot runs the CLI in-memory and returns stdout, stderr and the error.
func execRoot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	// Pin the client config to a per-test directory, so none of the 72
	// execRoot call sites reads the developer's real
	// ~/.config/olivares/config.yaml. Tests that pin OLIVARES_CLI_CONFIG
	// themselves keep it — that override wins in cliConfigPath.
	//
	// ⚠ THIS IS UNPROVEN HARDENING, and saying so is the point. Measured: the
	// whole package was run against a planted config naming
	// https://servidor-que-no-debe-usarse.invalid with a bearer token, with
	// and without this pin, and the results were IDENTICAL — every current
	// test passes --server explicitly, which outranks the stored context. So
	// nothing today depends on this line. It is here because the next test
	// that omits --server would silently acquire whatever context the machine
	// running it happens to hold, and that failure would be invisible on the
	// author's machine. If a mutation sweep reports this line as dead, that
	// report is correct and this comment is the answer to it.
	if os.Getenv(cliConfigOverrideEnv) == "" {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	}
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errb.String(), err
}

func TestExitCodeUsageOnBadFlag(t *testing.T) {
	_, _, err := execRoot(t, "status", "--definitely-not-a-flag")
	if err == nil {
		t.Fatal("bad flag must error")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Fatalf("exit code = %d, want %d (usage)", got, exitcode.Usage)
	}
}

func TestStatusExitCodeReflectsEngineHealth(t *testing.T) {
	for _, tc := range []struct {
		status  string
		wantErr bool
	}{
		{"operational", false},
		{"degraded", true},
		{"outage", true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/status" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": tc.status, "timestamp": "t"})
		}))
		out, _, err := execRoot(t, "status", "--server", srv.URL)
		srv.Close()
		if !tc.wantErr {
			if err != nil {
				t.Fatalf("%s: unexpected error %v", tc.status, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s: want a degraded exit, got success", tc.status)
		}
		if got := exitcode.From(err); got != exitcode.Degraded {
			t.Fatalf("%s: exit code = %d, want %d", tc.status, got, exitcode.Degraded)
		}
		// The report already printed — the error must be silent (no double print).
		if !exitcode.Silent(err) {
			t.Fatalf("%s: degraded error must be silent, got %q", tc.status, err)
		}
		if !strings.Contains(out, tc.status) {
			t.Fatalf("%s: report missing from stdout: %q", tc.status, out)
		}
	}
}

func TestCompletionHelpShowsInstructionsNotScript(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		out, _, err := execRoot(t, "completion", shell, "--help")
		if err != nil {
			t.Fatalf("%s --help: %v", shell, err)
		}
		if !strings.Contains(out, "To load completions") {
			t.Fatalf("%s --help: installation instructions missing: %q", shell, out[:min(len(out), 200)])
		}
	}
}

func TestCompleteSessionsQueriesRunsEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
			{"run_ref": "run-abc123", "name": "nightly-triage", "state": "running"},
		}})
	}))
	defer srv.Close()
	t.Setenv("OLIVARES_SERVER_URL", srv.URL)
	t.Setenv("OLIVARES_TOKEN", "tok")

	comps, _ := completeSessions(nil, nil, "")
	if gotPath != "/v1/m/sessions/runs" {
		t.Fatalf("queried %q, want the sessions runs endpoint (agent args are run refs)", gotPath)
	}
	joined := strings.Join(comps, " ")
	if !strings.Contains(joined, "run-abc123") || !strings.Contains(joined, "nightly-triage") {
		t.Fatalf("completions missing run ref/name: %v", comps)
	}
}

func TestEveryVisibleCommandIsGrouped(t *testing.T) {
	root := newRootCmd()
	root.InitDefaultHelpCmd()
	for _, c := range root.Commands() {
		if c.Hidden {
			continue
		}
		if c.GroupID == "" {
			t.Errorf("visible command %q has no help group — add it to commandGroups", c.Name())
		}
	}
}

func TestCanonicalShortVerbAliases(t *testing.T) {
	root := newRootCmd()
	find := func(path ...string) *cobra.Command {
		cur := root
		for _, name := range path {
			var next *cobra.Command
			for _, c := range cur.Commands() {
				if c.Name() == name {
					next = c
					break
				}
			}
			if next == nil {
				t.Fatalf("command %v not found at %q", path, name)
			}
			cur = next
		}
		return cur
	}
	rm := find("agent", "session", "rm")
	if !hasAlias(rm, "delete") {
		t.Fatalf("agent session rm must keep the delete alias, has %v", rm.Aliases)
	}
	ls := find("dr", "ls")
	if !hasAlias(ls, "list") {
		t.Fatalf("dr ls must keep the list alias, has %v", ls.Aliases)
	}
}

func hasAlias(c *cobra.Command, alias string) bool {
	for _, a := range c.Aliases {
		if a == alias {
			return true
		}
	}
	return false
}

func TestVersionFlagReportsBuildMetadata(t *testing.T) {
	out, _, err := execRoot(t, "--version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "commit") || !strings.Contains(out, "built") {
		t.Fatalf("--version output missing build provenance: %q", out)
	}
}
