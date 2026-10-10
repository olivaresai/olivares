// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// `session start <dir> "prompt"` without --tool always ran Claude Code, even when
// only OpenCode and a local model were set up. It now runs the first tool, in the
// console's order, that the engine's readiness answer says can start, the tool
// `olivares` and `tool ls` name; none ready is Claude Code, whose refusal says what to
// set up; an explicit tool is kept. Only the chosen tool is resolved, and no tool is
// previewed (B4.10b). A tool whose only key its provider refused is not ready, even
// though its resolve would answer (ARCH.C3).
func TestSessionStartRunsTheFirstReadyTool(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ready       map[string]bool
		refused     map[string]bool
		fails       map[string]int
		args        []string
		wantPosts   []string
		wantErr     string
		wantProfile string
	}{
		{"claude is ready", map[string]bool{"claude": true, "codex": true}, nil, nil, nil, []string{"claude"}, "", "ppf-new-claude"},
		{"only Gemini is ready", map[string]bool{"gemini-cli": true}, nil, nil, nil, []string{"gemini-cli"}, "", "ppf-new-gemini-cli"},
		{"only the local model", map[string]bool{"opencode": true}, nil, nil, nil, []string{"opencode"}, "", "ppf-new-opencode"},
		{"the console's order", map[string]bool{"opencode": true, "codex": true}, nil, nil, nil, []string{"codex"}, "", "ppf-new-codex"},
		{"none ready shows Claude Code's refusal", map[string]bool{}, nil, nil, nil, []string{"claude"}, "not ready: claude", ""},
		{"a refused key is skipped", map[string]bool{"claude": true, "codex": true}, map[string]bool{"claude": true}, nil, nil, []string{"codex"}, "", "ppf-new-codex"},
		// With none ready, the refused key is the cause: its sentence, not Claude Code's
		// "add an Anthropic key", and no tool is resolved.
		{"only a refused key names it", map[string]bool{"codex": true}, map[string]bool{"codex": true}, nil, nil, nil, refusedKeySentence("codex"), ""},
		{"the first refused key in the console's order", map[string]bool{"codex": true, "opencode": true}, map[string]bool{"codex": true, "opencode": true}, nil, nil, nil, refusedKeySentence("codex"), ""},
		// Readiness never reaches the write, so a ready tool whose profile cannot be made
		// is chosen and its refusal shown: the next tool does not run in its place.
		{"a ready tool's refusal ends the search", map[string]bool{"claude": true, "codex": true}, nil, map[string]int{"claude": 409}, nil, []string{"claude"}, "already owns this home", ""},
		{"an explicit tool is kept", map[string]bool{"opencode": true, "codex": true}, nil, nil, []string{"--tool", "opencode"}, []string{"opencode"}, "", "ppf-new-opencode"},
		{"an explicit profile resolves nothing", nil, nil, nil, []string{"--profile", "ppf-named"}, nil, "", "ppf-named"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			f.ready, f.refused, f.resolveFails = tc.ready, tc.refused, tc.fails
			args := append(append([]string{"session", "start", t.TempDir()}, tc.args...), sessionCreds(f.URL)...)
			_, errb, err := execSessionCLI(t, nil, args...)
			if (tc.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("start: %v\n%s, want error %q", err, errb, tc.wantErr)
			}
			var refusal *apiRefusal
			if strings.Contains(tc.wantErr, "which its provider refused") && (exitcode.From(err) != exitcode.Conflict || !errors.As(err, &refusal) || refusal.code != "key_refused") {
				t.Fatalf("start on a refused key: exit %d, %v, want the launch's conflict and key_refused", exitcode.From(err), err)
			}
			var got []string
			for _, p := range f.postsTo(profilesPath + "/resolve") {
				got = append(got, str(p, "driver"))
			}
			if !slices.Equal(got, tc.wantPosts) {
				t.Fatalf("the start resolved %v, want %v", got, tc.wantPosts)
			}
			for _, hit := range f.hits {
				if hit == "GET "+profilesPath+"/resolve" {
					t.Fatalf("the start previewed a tool before resolving it: %v", f.hits)
				}
			}
			runs := f.postsTo(sessionRunsPath)
			if tc.wantProfile == "" {
				if len(runs) != 0 {
					t.Fatalf("a refused start launched %v", runs)
				}
				return
			}
			if len(runs) != 1 || str(runs[0], "provider_profile_ref") != tc.wantProfile {
				t.Fatalf("the launch = %v, want the profile %s", runs, tc.wantProfile)
			}
		})
	}
}

// A ready preview does not promise that creating a profile can write. An operational
// refusal must end the launch even when the next tool already has a usable profile.
func TestSessionStartOperationalResolveRefusals(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
	}{
		{http.StatusServiceUnavailable, "not_leader"},
		{http.StatusServiceUnavailable, "audit_spool_full"},
		{http.StatusServiceUnavailable, "unavailable"},
		{http.StatusNotFound, "not_found"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			f.ready = map[string]bool{"claude": true, "codex": true}
			f.profiles = []map[string]any{{"profile_ref": "ppf-existing-codex", "driver": "codex", "auth_source": "provider_account_home"}}
			f.resolveFails = map[string]int{"claude": tc.status}
			f.resolveCodes = map[string]string{"claude": tc.code}
			// Establish the old preview's choice; POST alone must preserve its refusal.
			preview, err := f.Client().Get(f.URL + profilesPath + "/resolve?driver=claude")
			if err != nil {
				t.Fatal(err)
			}
			var answer map[string]any
			decodeErr := json.NewDecoder(preview.Body).Decode(&answer)
			preview.Body.Close()
			if preview.StatusCode != http.StatusOK || decodeErr != nil || answer["reason"] != "api_key" {
				t.Fatalf("Claude preview = %d %v %v, want ready", preview.StatusCode, answer, decodeErr)
			}
			args := append([]string{"session", "start", t.TempDir()}, sessionCreds(f.URL)...)
			_, errb, err := execSessionCLI(t, nil, args...)
			var refusal *apiRefusal
			if !errors.As(err, &refusal) || refusal.status != tc.status || refusal.code != tc.code {
				t.Fatalf("start: %v\n%s, want Claude's %d %s", err, errb, tc.status, tc.code)
			}
			posts := f.postsTo(profilesPath + "/resolve")
			if len(posts) != 1 || str(posts[0], "driver") != "claude" {
				t.Fatalf("resolved %v, want only Claude", posts)
			}
			if runs := f.postsTo(sessionRunsPath); len(runs) != 0 {
				t.Fatalf("a refused resolve launched %v", runs)
			}
		})
	}
}
