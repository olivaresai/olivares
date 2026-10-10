// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// accountEngine is the fake session engine plus the account list that
// `tool login --account` reads.
func accountEngine(t *testing.T) (*fakeSessionEngine, string) {
	t.Helper()
	f := newFakeSessionEngine(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == providerAccountsPath {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
				{"account_ref": "ppf_b", "name": "claude-b", "driver": "claude"},
			}})
			return
		}
		f.serve(w, r)
	}))
	t.Cleanup(server.Close)
	return f, server.URL
}

// `session start --account <name>` launches under that account's profile, found
// the way `tool login --account` finds it, and skips the engine's own choice.
func TestSessionStartByAccountName(t *testing.T) {
	f, url := accountEngine(t)
	_, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--account", "claude-b"}, sessionCreds(url)...)...)
	if err != nil {
		t.Fatalf("start: %v\n%s", err, errb)
	}
	runs := f.postsTo(sessionRunsPath)
	if len(runs) != 1 || runs[0]["provider_profile_ref"] != "ppf_b" {
		t.Fatalf("launch = %v, want the profile of claude-b", runs)
	}
	if len(f.postsTo(profilesPath+"/resolve")) != 0 {
		t.Fatal("a named account must not ask the engine to choose a profile")
	}
	if !strings.Contains(errb, "claude-b") {
		t.Fatalf("stderr = %q, want a line that names the account", errb)
	}
}

// The same exit codes as `tool login --account`, and nothing is registered or
// launched when the choice is wrong.
func TestSessionStartByAccountRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"unknown account", []string{"--account", "nobody"}, exitcode.NotFound},
		{"another tool", []string{"--account", "claude-b", "--tool", "codex"}, exitcode.Conflict},
		{"with a profile", []string{"--account", "claude-b", "--profile", "ppf_x"}, exitcode.Usage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, url := accountEngine(t)
			args := append([]string{"session", "start", t.TempDir()}, tc.args...)
			_, _, err := execSessionCLI(t, nil, append(args, sessionCreds(url)...)...)
			if tc.name == "with a profile" && (err == nil || !strings.Contains(err.Error(), "--profile")) {
				t.Fatalf("err = %v, want the sentence that says --account and --profile exclude each other", err)
			}
			if exitcode.From(err) != tc.want {
				t.Fatalf("exit = %d (%v), want %d", exitcode.From(err), err, tc.want)
			}
			if len(f.postsTo(sessionWorkspacesPath)) != 0 || len(f.postsTo(sessionRunsPath)) != 0 {
				t.Fatal("a refused start registered a folder or launched a session")
			}
		})
	}
}
