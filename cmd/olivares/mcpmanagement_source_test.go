// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPGatewaySourceSelectionRejectsMixedOwnership(t *testing.T) {
	for name, raw := range map[string]string{
		"explicit empty":                 `{"mcp_source":""}`,
		"explicit null":                  `{"mcp_source":null}`,
		"duplicate source":               `{"mcp_source":"file","mcp_source":"store"}`,
		"case duplicate":                 `{"mcp_source":"file","MCP_SOURCE":"store"}`,
		"masked MCP conflict":            `{"mcp_source":"store","mcp":{"resource":"https://plane.test/mcp"},"mcp":null}`,
		"masked session conflict":        `{"mcp_source":"store","session_tools":true,"session_tools":false}`,
		"unknown source":                 `{"mcp_source":"database-ish"}`,
		"store with file MCP":            `{"mcp_source":"store","mcp":{"resource":"https://plane.test/mcp"}}`,
		"store with file session switch": `{"mcp_source":"store","session_tools":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gateway.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OLIVARES_AGENT_GATEWAY_CONFIG", path)
			if _, err := loadAgentGatewayConfig(discardLogger()); err == nil {
				t.Fatal("conflicting or unknown configuration source accepted")
			}
		})
	}
}
