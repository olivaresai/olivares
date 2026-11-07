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
	"time"

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

// A start without --account runs on an account that can answer now. The engine's own
// choice for the tool made a profile on the host's login, which can be at its weekly
// limit, and left it registered with no name. So the account list (with the usage each
// tool reported) is read first: the first account with no exhausted window is the
// profile, and the engine is not asked to choose. Every account at its limit refuses
// before a folder or a session is made. With no usage reported nothing is known about
// any account, and the engine chooses as it always did.
func TestSessionStartWithoutAccountSkipsAccountsAtTheirLimit(t *testing.T) {
	next := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	account := func(ref, name string, windows ...map[string]any) map[string]any {
		a := map[string]any{"account_ref": ref, "name": name, "driver": "claude", "state": "active"}
		if len(windows) > 0 {
			a["usage"] = windows
		}
		return a
	}
	window := func(name string, used float64, resets string) map[string]any {
		return map[string]any{"window": name, "utilization": used, "resets_at": resets}
	}
	for _, tc := range []struct {
		name        string
		accounts    []map[string]any
		wantProfile string
		wantResolve int
		wantErr     string
	}{
		{"the first account with capacity", []map[string]any{
			account("ppf_b", "claude-b", window("five_hour", 12, next), window("seven_day", 100, next)),
			account("ppf_c", "claude-c", window("seven_day", 74, next)),
			account("ppf_a", "claude-a", window("seven_day", 34, next)),
		}, "ppf_c", 0, ""},
		{"a window of one model leaves the account usable", []map[string]any{
			account("ppf_b", "claude-b", window("seven_day:opus", 100, next), window("seven_day", 20, next)),
		}, "ppf_b", 0, ""},
		{"a window that has reset is not a limit", []map[string]any{
			account("ppf_b", "claude-b", window("seven_day", 100, past)),
		}, "ppf_b", 0, ""},
		{"every account at its limit refuses", []map[string]any{
			account("ppf_b", "claude-b", window("seven_day", 100, next)),
			account("ppf_t", "team-b", window("five_hour", 100, next)),
		}, "", 0, "at its limit"},
		{"no usage reported leaves the choice to the engine", []map[string]any{
			account("ppf_b", "claude-b"),
		}, "ppf-new-claude", 1, ""},
		{"an account of unknown usage is not skipped for the engine's choice", []map[string]any{
			account("ppf_b", "claude-b", window("seven_day", 100, next)),
			account("ppf_n", "claude-n"),
		}, "ppf-new-claude", 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSessionEngine(t, func(f *fakeSessionEngine) { f.accounts = tc.accounts })
			_, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir()}, sessionCreds(f.URL)...)...)
			if (tc.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("start: %v\n%s, want error %q", err, errb, tc.wantErr)
			}
			if got := len(f.postsTo(profilesPath + "/resolve")); got != tc.wantResolve {
				t.Fatalf("the start asked the engine to choose %d times, want %d", got, tc.wantResolve)
			}
			runs := f.postsTo(sessionRunsPath)
			if tc.wantProfile == "" {
				if exitcode.From(err) != exitcode.Conflict || len(runs) != 0 || len(f.postsTo(sessionWorkspacesPath)) != 0 {
					t.Fatalf("exit %d, runs %v: a refused start must be a conflict that made no folder and no session", exitcode.From(err), runs)
				}
				return
			}
			if len(runs) != 1 || runs[0]["provider_profile_ref"] != tc.wantProfile {
				t.Fatalf("launch = %v, want the profile %s", runs, tc.wantProfile)
			}
		})
	}
}
