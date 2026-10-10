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

// With OpenCode ready on a local model, `tool ls` and `olivares` still
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

// fakeReadiness is the engine's readiness answer: every session tool, ready or not.
// refusedKeySentence is the engine's readiness sentence for a tool whose key its
// provider refused (toolReadiness in modules/sessions).
func refusedKeySentence(driver string) string {
	return toolName(driver) + " would run on the API key Old OpenAI, which its provider refused at the last test. Replace it in Providers, or add another key."
}

func fakeReadiness(ready map[string]bool) map[string]any {
	var tools []map[string]any
	for _, d := range sessionToolOrder {
		if ready[d] {
			tools = append(tools, map[string]any{"driver": d, "ready": true, "reason": "api_key"})
		} else {
			tools = append(tools, map[string]any{"driver": d, "ready": false, "code": "nothing_to_run_on", "message": "not ready"})
		}
	}
	return map[string]any{"tools": tools}
}

// The CLI and the console read one answer: a tool whose only key its provider refused
// at the last test is not ready, so the next step is not a session that cannot start.
func TestToolListDoesNotNameTheSessionOnARefusedKey(t *testing.T) {
	f := newFakeToolEngine(t)
	f.refused = true
	out, errb, err := execSessionCLI(t, nil, append([]string{"tool", "ls"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("tool ls: %v\n%s", err, errb)
	}
	if strings.Contains(out, "next: olivares session start <folder>") || !strings.Contains(out, refusedKeySentence("opencode")) {
		t.Fatalf("tool ls with a refused key =\n%s", out)
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
		case profilesPath + "/readiness":
			_ = json.NewEncoder(w).Encode(fakeReadiness(map[string]bool{"opencode": true}))
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

// With no tool ready and a key its provider refused, `olivares` names the refused key:
// the next step from the tool rows alone does not say why nothing can start.
func TestBareOlivaresNamesARefusedKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/livez":
			w.WriteHeader(http.StatusOK)
		case "/v1/auth/whoami":
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "user", "display_name": "Ana", "actor": "user:1"})
		case agentToolsPath + "/inventory":
			_ = json.NewEncoder(w).Encode(map[string]any{"drivers": []string{"claude"}, "inventory": map[string]any{}})
		case profilesPath + "/readiness":
			answer := fakeReadiness(map[string]bool{})
			for _, tool := range answer["tools"].([]map[string]any) {
				if tool["driver"] == "codex" {
					tool["code"], tool["message"] = "key_refused", refusedKeySentence("codex")
				}
			}
			_ = json.NewEncoder(w).Encode(answer)
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
	if !strings.Contains(out, refusedKeySentence("codex")) {
		t.Fatalf("olivares with only a refused key =\n%s", out)
	}
	out, _, err = execRoot(t, "-o", "json")
	var report rootStatusReport
	if err != nil || json.Unmarshal([]byte(out), &report) != nil || report.Refusal != refusedKeySentence("codex") {
		t.Fatalf("olivares -o json with only a refused key = %v\n%s", err, out)
	}
}

// The CLI could not start the product's Ollama. `tool start ollama`
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
