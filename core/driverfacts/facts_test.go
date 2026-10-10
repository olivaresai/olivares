// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package driverfacts

import (
	"slices"
	"testing"
)

func TestBindingKeepsEndpointRestrictions(t *testing.T) {
	for _, tc := range []struct {
		driver, kind, endpoint string
		want                   bool
	}{
		{"claude", "anthropic", "https://gateway.example", true},
		{"claude", "openai", "", false},
		{"codex", "openai_compatible", "https://gateway.example", true},
		{"codex", "ollama", "http://127.0.0.1:11434", true},
		{"grok", "xai", "", true},
		{"opencode", "anthropic", "", true},
		{"opencode", "openai", "https://api.openai.com/v1", false},
		{"opencode", "xai", "https://gateway.example", false},
		{"opencode", "openai_compatible", "", false},
		{"opencode", "ollama", "http://127.0.0.1:11434", true},
		{"gemini-cli", "openai", "", false},
		{"ollama", "ollama", "", false},
		{"unknown", "anthropic", "", false},
	} {
		t.Run(tc.driver+"/"+tc.kind+"/"+tc.endpoint, func(t *testing.T) {
			facts, _ := Lookup(tc.driver)
			if got := facts.CanBind(tc.kind, tc.endpoint); got != tc.want {
				t.Fatalf("CanBind(%q, %q) = %v, want %v", tc.kind, tc.endpoint, got, tc.want)
			}
		})
	}
}

func TestReadersCannotChangeDriverFacts(t *testing.T) {
	facts, ok := Lookup("opencode")
	if !ok {
		t.Fatal("OpenCode is missing")
	}
	facts.LoginArgs[0] = "mutated"
	facts.StatusArgs[0] = "mutated"
	facts.AuthFailures[0] = "mutated"
	facts.Bindings[0].Kind = "mutated"
	facts.EnvPrefixes[0] = "MUTATED_"
	rows := All()
	for i := range rows {
		if rows[i].Key == "opencode" {
			rows[i].LoginArgs[0] = "mutated"
		}
	}
	facts, _ = Lookup("opencode")
	if facts.LoginArgs[0] != "auth" || facts.StatusArgs[0] != "auth" || facts.AuthFailures[0] != "no provider" || !facts.CanBind("anthropic", "") ||
		facts.EnvPrefixes[0] != "OPENCODE_" {
		t.Fatalf("a caller mutated the shared facts: %+v", facts)
	}
	if _, ok := Lookup("unknown"); ok {
		t.Fatal("unknown driver accepted")
	}
}

func TestInventoryDoesNotAdvertiseUnavailableSessions(t *testing.T) {
	facts, ok := Lookup("ollama")
	if !ok || facts.Session || facts.SignIn != "" || len(facts.Bindings) != 0 {
		t.Fatalf("ollama must be explicit without claiming a session adapter: %+v", facts)
	}
}

// Gemini CLI uses the official bundle, native Google sign-in and its own API key.
func TestGeminiCLIHasASessionDriverAndVendorBinding(t *testing.T) {
	facts, ok := Lookup("gemini-cli")
	if !ok || !facts.Session || facts.Program != "gemini" || facts.ConfigDir != ".gemini" ||
		facts.ConfigHomeEnv != GeminiConfigHomeEnv || facts.RuntimeBinEnv != GeminiRuntimeBinEnv {
		t.Fatalf("gemini-cli must declare its session driver: %+v", facts)
	}
	if facts.Installer != InstallRelease || facts.SignIn != SignInPaste || !facts.CanBind("gemini", "") || facts.CanBind("gemini", "https://gateway.example") {
		t.Fatalf("gemini-cli must declare its installer, native sign-in and vendor key: %+v", facts)
	}
	for _, kind := range []string{"anthropic", "openai", "xai", "ollama", "openai_compatible"} {
		if facts.CanBind(kind, "") || facts.CanBind(kind, "https://gateway.example") {
			t.Fatalf("gemini-cli must not bind a %s record", kind)
		}
	}
}

// Only a tool whose hosts were measured behind the session proxy is network
// confined; measuring another tool is the change that adds it here.
func TestOnlyMeasuredToolsAreEgressEnforced(t *testing.T) {
	var measured []string
	for _, f := range All() {
		if f.EgressMeasured {
			measured = append(measured, f.Key)
		}
	}
	if !slices.Equal(measured, []string{"claude", "codex", "gemini-cli"}) {
		t.Fatalf("egress-measured tools = %v, want [claude codex gemini-cli]", measured)
	}
}

// Every tool a session can run declares its own credential families and the
// variable of its configuration home: the session runtime refuses both from every
// source but that tool's own launch, and a row without them would refuse nothing.
func TestSessionToolsDeclareTheirEnvironment(t *testing.T) {
	for _, facts := range All() {
		if !facts.Session {
			if len(facts.EnvPrefixes) != 0 {
				t.Errorf("%s runs no session and declares families %v", facts.Key, facts.EnvPrefixes)
			}
			continue
		}
		if len(facts.EnvPrefixes) == 0 || facts.ConfigHomeEnv == "" {
			t.Errorf("%s runs sessions and declares families %v, configuration home %q", facts.Key, facts.EnvPrefixes, facts.ConfigHomeEnv)
		}
		for _, prefix := range facts.EnvPrefixes {
			if len(prefix) < 2 || prefix[len(prefix)-1] != '_' {
				t.Errorf("%s family %q is not a NAME_ prefix", facts.Key, prefix)
			}
		}
	}
}
