// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Root on FH 108: with OpenCode ready on a local model, `tool ls` and `olivares` still
// said "next: olivares tool login claude". A tool the engine can run a session on now
// makes the session the next step, as `session start` picks it.
func TestToolListNamesTheSessionWhenAToolIsReady(t *testing.T) {
	f := newFakeToolEngine(t)
	f.ready = map[string]bool{"opencode": true}
	out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "ls"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("tool ls: %v\n%s", err, errb)
	}
	if !strings.Contains(out, "next: olivares session start <folder>") || strings.Contains(out, "tool login claude") {
		t.Fatalf("tool ls with OpenCode ready =\n%s", out)
	}
}

func TestBareOlivaresNamesTheSessionWhenAToolIsReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/livez":
			w.WriteHeader(http.StatusOK)
		case "/v1/auth/whoami":
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "user", "display_name": "Ana", "actor": "user:1"})
		case agentToolsPath + "/inventory":
			_ = json.NewEncoder(w).Encode(map[string]any{"drivers": []string{"claude"}, "inventory": map[string]any{}})
		case profilesPath + "/resolve":
			if r.URL.Query().Get("driver") == "opencode" {
				_ = json.NewEncoder(w).Encode(map[string]any{"reason": "api_key"})
				return
			}
			w.WriteHeader(http.StatusConflict)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	config := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(cliConfigOverrideEnv, config)
	if err := writeCLIConfig(config, cliConfig{CurrentContext: "lab", Contexts: []cliContext{{
		Name: "lab", Server: srv.URL, Token: "t", Tenant: "x",
	}}}); err != nil {
		t.Fatal(err)
	}
	out, _, err := execRoot(t, []string{}...)
	if err != nil {
		t.Fatalf("olivares: %v", err)
	}
	if !strings.Contains(out, "olivares session start <folder>") || strings.Contains(out, "olivares tool install claude") {
		t.Fatalf("olivares with OpenCode ready =\n%s", out)
	}
}

// Root on FH 108: the CLI could not start the product's Ollama. `tool start ollama`
// starts it for the person's organization, waits until it answers, and names what is
// next; another tool is told it does not run as a service.
func TestToolStartOllamaStartsTheService(t *testing.T) {
	f := newFakeToolEngine(t)
	out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "start", "ollama"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("tool start ollama: %v\n%s", err, errb)
	}
	f.mu.Lock()
	tenant := f.ollamaTenant
	f.mu.Unlock()
	if tenant != "tenant-a" {
		t.Fatalf("started for %v, want the person's organization", tenant)
	}
	for _, want := range []string{"Ollama is running at http://127.0.0.1:11434.", "download one in AI tools", "next: olivares session start <folder>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool start ollama lacks %q:\n%s", want, out)
		}
	}
	if _, _, err := execSessionCLI(t, nil, append([]string{"tool", "start", "codex"}, sessionCreds(f.URL)...)...); exitcode.From(err) != exitcode.Usage {
		t.Fatalf("tool start codex = %v, want a usage error", err)
	}
}
