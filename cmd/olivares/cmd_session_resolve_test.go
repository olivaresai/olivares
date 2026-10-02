// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestSessionStartUsesTheEnginesDefaultProfile is HU 030: without --profile, `session
// start` used the tool's own login while the console chose a working profile (one bound
// to a Providers key). It now asks the engine (POST provider-profiles/resolve, FH 026/028),
// launches under the profile it answers, and says which one in one line of three forms.
func TestSessionStartUsesTheEnginesDefaultProfile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolved map[string]any
		tool     string
		want     string
	}{
		{"own login", map[string]any{"reason": "own_login", "created": false,
			"profile": map[string]any{"profile_ref": "ppf-own", "driver": "claude", "display_name": "Claude Code"}},
			"claude", "Using Claude Code (own login)"},
		{"api key", map[string]any{"reason": "api_key", "created": true,
			"profile":  map[string]any{"profile_ref": "ppf-key", "driver": "claude", "display_name": "Claude Code (Anthropic prod)"},
			"provider": map[string]any{"provider_ref": "prv-1", "kind": "anthropic", "display_name": "Anthropic prod"}},
			// The engine names the profile after the provider; refresh 09 printed it twice:
			// "Using Claude Code (Anthropic test) (API key Anthropic test)".
			"claude", "Using Claude Code (API key Anthropic prod)"},
		{"api key, a profile the admin named", map[string]any{"reason": "api_key", "created": false,
			"profile":  map[string]any{"profile_ref": "ppf-prod", "driver": "claude", "display_name": "Claude (prod)"},
			"provider": map[string]any{"provider_ref": "prv-1", "kind": "anthropic", "display_name": "Anthropic prod"}},
			"claude", "Using Claude (prod) (API key Anthropic prod)"},
		{"local model", map[string]any{"reason": "api_key", "created": false,
			"profile":  map[string]any{"profile_ref": "ppf-local", "driver": "opencode", "display_name": "OpenCode (Local Ollama)"},
			"provider": map[string]any{"provider_ref": "prv-2", "kind": "ollama", "display_name": "Local Ollama"}},
			"opencode", "Using OpenCode (local model Local Ollama)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			f.resolved = tc.resolved
			_, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--tool", tc.tool}, sessionCreds(f.URL)...)...)
			if err != nil {
				t.Fatalf("start: %v\n%s", err, errb)
			}
			if !strings.Contains(errb, tc.want+"\n") || strings.Count(errb, "Using ") != 1 {
				t.Fatalf("stderr = %q, want the one line %q", errb, tc.want)
			}
			res := f.postsTo(profilesPath + "/resolve")
			runs := f.postsTo(sessionRunsPath)
			profile, _ := tc.resolved["profile"].(map[string]any)
			if len(res) != 1 || res[0]["driver"] != tc.tool || len(runs) != 1 || runs[0]["provider_profile_ref"] != profile["profile_ref"] {
				t.Fatalf("resolve = %v, launch = %v", res, runs)
			}
		})
	}

	// --profile keeps working unchanged: no resolve call, no line.
	f := newFakeSessionEngine(t)
	_, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "--profile", "ppf-mine"}, sessionCreds(f.URL)...)...)
	if err != nil || len(f.postsTo(profilesPath+"/resolve")) != 0 || strings.Contains(errb, "Using ") ||
		f.postsTo(sessionRunsPath)[0]["provider_profile_ref"] != "ppf-mine" {
		t.Fatalf("--profile: err=%v stderr=%q resolve=%v", err, errb, f.postsTo(profilesPath+"/resolve"))
	}
}

// TestSessionStartPrintsTheEnginesRefusalAsItIs: when nothing can run the tool, the
// engine answers 409 with one sentence; the CLI prints it as it is (exit 5) and does not
// register the folder or launch anything.
func TestSessionStartPrintsTheEnginesRefusalAsItIs(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.resolveRefusal = "Claude Code is not signed in and Providers has nothing it can use. Sign it in under AI tools, or add an Anthropic key in Providers."
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir()}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Conflict || err == nil || err.Error() != f.resolveRefusal {
		t.Fatalf("err = %v (exit %d), want the engine's sentence", err, exitcode.From(err))
	}
	if len(f.postsTo(sessionWorkspacesPath)) != 0 || len(f.postsTo(sessionRunsPath)) != 0 {
		t.Fatal("a refused start registered a folder or launched a session")
	}
}

// TestSessionStartPrintsTheEnginesUnavailableSentence: FH 032 answers 503 with one
// sentence when the node cannot read the tool's sign-in status. It is printed as it is
// (exit 6, the engine's side), like the 409.
func TestSessionStartPrintsTheEnginesUnavailableSentence(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.resolveStatus = 503
	f.resolveRefusal = "This server cannot read Claude Code's sign-in right now. Try again in a moment."
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir()}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Server || err == nil || err.Error() != f.resolveRefusal {
		t.Fatalf("err = %v (exit %d), want the engine's sentence, exit 6", err, exitcode.From(err))
	}
	if len(f.postsTo(sessionWorkspacesPath)) != 0 || len(f.postsTo(sessionRunsPath)) != 0 {
		t.Fatal("a refused start registered a folder or launched a session")
	}
}

// TestSessionStartRefusalInJSONCarriesItsStatus is J7 on refresh 09: with -o json the
// resolve refusal was {"error":{"message":...}} with no status, unlike every other engine
// refusal (EU-08). It now carries the status, as text it is the same sentence and exit.
func TestSessionStartRefusalInJSONCarriesItsStatus(t *testing.T) {
	for _, status := range []int{409, 503} {
		f := newFakeSessionEngine(t)
		f.resolveStatus = status
		f.resolveRefusal = "Claude Code is not signed in and Providers has nothing it can use. Sign it in under AI tools, or add an Anthropic key in Providers."
		_, _, err := execSessionCLI(t, nil, append([]string{"session", "start", t.TempDir(), "-o", "json"}, sessionCreds(f.URL)...)...)
		if err == nil || err.Error() != f.resolveRefusal {
			t.Fatalf("%d: err = %v", status, err)
		}
		var out bytes.Buffer
		if werr := printCLIErrorAs(&out, err, true); werr != nil {
			t.Fatal(werr)
		}
		var got struct {
			Error struct {
				Message string `json:"message"`
				Status  int    `json:"status"`
			} `json:"error"`
		}
		if jerr := json.Unmarshal(out.Bytes(), &got); jerr != nil || got.Error.Status != status || got.Error.Message != f.resolveRefusal {
			t.Fatalf("%d: -o json = %q (%v)", status, out.String(), jerr)
		}
	}
}
