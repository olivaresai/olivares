// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import "testing"

// TestMCPTop10CatalogComplete asserts the catalog covers MCP01..MCP10 exactly once,
// with the verified verbatim titles (the load-bearing positioning claim — a wrong
// id/title reads as amateur to a security reviewer).
func TestMCPTop10CatalogComplete(t *testing.T) {
	want := map[string]string{
		"MCP01:2025": "Token Mismanagement & Secret Exposure",
		"MCP02:2025": "Privilege Escalation via Scope Creep",
		"MCP03:2025": "Tool Poisoning",
		"MCP04:2025": "Software Supply Chain Attacks & Dependency Tampering",
		"MCP05:2025": "Command Injection & Execution",
		"MCP06:2025": "Intent Flow Subversion",
		"MCP07:2025": "Insufficient Authentication & Authorization",
		"MCP08:2025": "Lack of Audit and Telemetry",
		"MCP09:2025": "Shadow MCP Servers",
		"MCP10:2025": "Context Injection & Over-Sharing",
	}
	got := MCPTop10()
	if len(got) != 10 {
		t.Fatalf("expected 10 MCP controls, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, c := range got {
		wantTitle, ok := want[c.ID]
		if !ok {
			t.Errorf("unexpected control id %q", c.ID)
			continue
		}
		if c.Title != wantTitle {
			t.Errorf("%s title = %q, want %q", c.ID, c.Title, wantTitle)
		}
		if c.ProductControl == "" || len(c.Evidence) == 0 {
			t.Errorf("%s must map to a product control with evidence: %+v", c.ID, c)
		}
		seen[c.ID] = true
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("missing control %q", id)
		}
	}
}

func TestMCPTop10ForFindingKind(t *testing.T) {
	cases := map[string]string{
		"mcp_shadow":     "MCP09:2025",
		"mcp_auth":       "MCP07:2025",
		"mcp_surface":    "MCP10:2025",
		"mcp_provenance": "MCP04:2025",
		"mcp_revision":   "", // no unambiguous mapping
		"mcp_posture":    "", // multi-id by design — the precise MCP id rides in the title
		"health":         "",
		"":               "",
	}
	for kind, want := range cases {
		if got := MCPTop10ForFindingKind(kind); got != want {
			t.Errorf("MCPTop10ForFindingKind(%q) = %q, want %q", kind, got, want)
		}
	}
}

// TestMCPTop10Immutable confirms the accessor returns a copy (callers can't mutate
// the canonical catalog).
func TestMCPTop10Immutable(t *testing.T) {
	a := MCPTop10()
	a[0].Title = "MUTATED"
	if MCPTop10()[0].Title == "MUTATED" {
		t.Error("MCPTop10() must return a defensive copy")
	}
}
