// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func TestSessionMCPLaunchConfigurationKeepsBearerInEnvironment(t *testing.T) {
	for _, driver := range []string{"claude", "codex"} {
		t.Run(driver, func(t *testing.T) {
			const bearer = "fixture-session-token-never-in-file-or-args"
			spec := LaunchSpec{Args: []string{"app-server"}, Env: []EnvVar{{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: bearer}}, Confinement: &confine.Policy{ReadWrite: []string{t.TempDir()}}}
			cleanup, err := ConfigureSessionMCP(&spec, driver, t.TempDir(), "run-one", "https://localhost:8443/session/mcp", "OLIVARES_HOOK_PEP_TOKEN")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if len(spec.Confinement.ReadOnly) != 1 {
				t.Fatal("agent cannot read its MCP configuration")
			}
			path := spec.Confinement.ReadOnly[0]
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), bearer) || strings.Contains(strings.Join(spec.Args, " "), bearer) {
				t.Fatal("bearer escaped its environment")
			}
			if !strings.Contains(string(raw), "OLIVARES_HOOK_PEP_TOKEN") || !strings.Contains(strings.Join(spec.Args, " "), "mcp") {
				t.Fatal("MCP endpoint was not configured")
			}
			cleanup()
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("configuration was not cleaned up")
			}
		})
	}
}

// ACP carries the server; the driver's provider and permission settings stay intact.
func TestSessionMCPLaunchKeepsOpenCodesInlineConfiguration(t *testing.T) {
	for _, existing := range []string{`{"share":"disabled","enabled_providers":["anthropic"]}`, ""} {
		const bearer = "fixture-session-token-never-in-file-or-args"
		spec := LaunchSpec{Args: []string{"acp"}, Env: []EnvVar{{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: bearer}}}
		if existing != "" {
			spec.Env = append(spec.Env, EnvVar{Name: envOpenCodeConfigContent, Value: existing})
		}
		cleanup, err := ConfigureSessionMCP(&spec, providerDriverOpenCode, t.TempDir(), "run-one", "http://127.0.0.1:8443/session/mcp", "OLIVARES_HOOK_PEP_TOKEN")
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		var content string
		for _, item := range spec.Env {
			if item.Name == envOpenCodeConfigContent {
				content = item.Value
			}
		}
		if content != existing || strings.Join(spec.Args, " ") != "acp" || strings.Contains(content, bearer) {
			t.Fatal("MCP changed provider configuration or exposed its credential")
		}
		if spec.SessionMCPURL != "http://127.0.0.1:8443/session/mcp" || spec.SessionMCPTokenEnv != "OLIVARES_HOOK_PEP_TOKEN" {
			t.Fatal("ACP did not receive the session connection")
		}
	}
}

// Claude Code and Codex are handed the one `olivares` server and nothing else:
// a registered server is never a second entry of the driver's own config, so
// two servers cannot collide on a tool name inside the driver. The aggregate
// behind the one endpoint qualifies every tool by its server (ACP drivers carry
// the same single server: driver_session_mcp_test.go).
func TestSessionMCPLaunchHandsClaudeAndCodexExactlyTheOneOlivaresServer(t *testing.T) {
	for _, driver := range []string{providerDriverClaude, providerDriverCodex} {
		t.Run(driver, func(t *testing.T) {
			spec := LaunchSpec{Args: []string{"app-server"}, Env: []EnvVar{{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "fixture-token"}}, Confinement: &confine.Policy{ReadWrite: []string{t.TempDir()}}}
			cleanup, err := ConfigureSessionMCP(&spec, driver, t.TempDir(), "run-one", "https://localhost:8443/session/mcp", "OLIVARES_HOOK_PEP_TOKEN")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			raw, err := os.ReadFile(spec.Confinement.ReadOnly[0])
			if err != nil {
				t.Fatal(err)
			}
			if driver == providerDriverClaude {
				var cfg struct {
					Servers map[string]any `json:"mcpServers"`
				}
				if err := json.Unmarshal(raw, &cfg); err != nil || len(cfg.Servers) != 1 || cfg.Servers[sessionMCPServerName] == nil {
					t.Fatalf("claude config has servers %v (%v), want only %q", cfg.Servers, err, sessionMCPServerName)
				}
				return
			}
			if n := strings.Count(string(raw), "[mcp_servers."); n != 1 || !strings.Contains(string(raw), "[mcp_servers."+sessionMCPServerName+"]") {
				t.Fatalf("codex config has %d server tables, want only %q:\n%s", n, sessionMCPServerName, raw)
			}
			for i := 0; i+1 < len(spec.Args) && spec.Args[i] == "-c"; i += 2 {
				if !strings.HasPrefix(spec.Args[i+1], "mcp_servers."+sessionMCPServerName+".") {
					t.Fatalf("codex override %q is not for the %s server", spec.Args[i+1], sessionMCPServerName)
				}
			}
		})
	}
}

func TestSessionMCPRemoteClaudeCarriesConfiguration(t *testing.T) {
	const bearer = "fixture-bearer-must-stay-in-env"
	spec := LaunchSpec{RunOn: "node-remote", Env: []EnvVar{{Name: envHookPEPToken, Value: bearer}}}
	cleanup, err := ConfigureSessionMCP(&spec, "claude", t.TempDir(), "run-remote", "https://central.example/session/mcp", envHookPEPToken)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(spec.LaunchFiles) != 1 {
		t.Fatalf("remote --mcp-config %v has no LaunchFile: %v", spec.Args, spec.LaunchFiles)
	}
	file := spec.LaunchFiles[0]
	if file.Path != spec.Args[1] || file.Mode != 0o600 {
		t.Fatalf("wrong MCP LaunchFile: %v", file)
	}
	raw, err := os.ReadFile(file.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(file.Data) || strings.Contains(string(file.Data), bearer) {
		t.Fatal("MCP file missing data or contains credential")
	}
}
