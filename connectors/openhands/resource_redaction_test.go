// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openhands

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestGatherSanitizesMCPResourceURLs(t *testing.T) {
	for _, tc := range []struct {
		name, url, command, want string
	}{
		{"userinfo-query-fragment", "https://operator:synthetic-pass@mcp.example:8443/sse?token=synthetic-token#synthetic-fragment", "", "https://mcp.example:8443/sse"},
		{"encoded-userinfo", "https://synthetic%40user:synthetic%2Fpass@mcp.example/mcp", "", "https://mcp.example/mcp"},
		{"username-only", "https://synthetic-user@mcp.example/mcp", "", "https://mcp.example/mcp"},
		{"opaque-query", "https://mcp.example/a%20b?opaque=synthetic-signature", "", "https://mcp.example/a%20b"},
		{"relative-authority", "//operator:synthetic-pass@mcp.example/mcp?sig=synthetic-signature", "", "//mcp.example/mcp"},
		{"invalid-escape", "https://operator:synthetic%zzpass@mcp.example/mcp?token=synthetic-token", "", "main"},
		{"invalid-userinfo", "https://operator:synthetic pass@mcp.example/mcp", "", "main"},
		{"hostless-https", "https:/operator:plain-pass@mcp.example/mcp?sig=synthetic#x", "", "main"},
		{"hostless-extra-slash-https", "https:////operator:synthetic-pass@mcp.example/mcp", "", "main"},
		{"relative-empty-authority", "///operator:plain-pass@mcp.example/mcp?sig=synthetic#x", "", "main"},
		{"hostless-other-scheme", "ftp:////operator:synthetic-pass@mcp.example/mcp", "", "main"},
		{"hostless-http", "http:/operator:synthetic-pass@mcp.example/mcp", "", "main"},
		{"opaque-https", "https:operator:synthetic-pass@mcp.example/mcp", "", "main"},
		{"ordinary-url", "https://mcp.example:8443/a%20b", "", "https://mcp.example:8443/a%20b"},
		{"ordinary-http", "http://mcp.example/mcp", "", "http://mcp.example/mcp"},
		{"command-punctuation", "", "node server.js --filter=a?b#c", "node server.js --filter=a?b#c"},
		{"command-url-argument", "", "node server.js --origin=https://mcp.example/mcp?mode=read#tools", "node server.js --origin=https://mcp.example/mcp?mode=read#tools"},
		{"server-name", "", "", "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			config := fmt.Sprintf("[core]\nplugins = [\"Browse?mode=read#tools\"]\n[mcp.servers.main]\nurl = %q\ncommand = %q\n", tc.url, tc.command)
			if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			sink := gatherWith(t, map[string]string{"config_path": path, "agent_ref": "test-agent"})
			edges := sink.edges()
			if len(edges) != 2 {
				t.Fatalf("published %d edges, want MCP and action edges", len(edges))
			}
			for _, edge := range edges {
				if edge.OriginRef != "test-agent" {
					t.Errorf("origin = %q, want test-agent", edge.OriginRef)
				}
				switch edge.ResourceKind {
				case resourceMCPServer:
					if edge.ToolRef != "main" || edge.ResourceRef != tc.want {
						t.Errorf("MCP identity = (%q, %q), want (main, %q)", edge.ToolRef, edge.ResourceRef, tc.want)
					}
				case resourceAction:
					if edge.ResourceRef != "Browse?mode=read#tools" {
						t.Errorf("action identity = %q, want unchanged", edge.ResourceRef)
					}
				default:
					t.Errorf("unexpected resource kind %q", edge.ResourceKind)
				}
			}
		})
	}
}
