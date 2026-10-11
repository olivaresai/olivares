// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
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
