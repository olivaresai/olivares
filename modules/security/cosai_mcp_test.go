// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"fmt"
	"testing"
)

// TestCoSAIMCPCatalogComplete asserts the catalog covers MCP-T1..MCP-T12 exactly
// once, IN ORDER, with the verified verbatim titles (the load-bearing positioning
// claim — a wrong id/title reads as amateur to a security reviewer).
func TestCoSAIMCPCatalogComplete(t *testing.T) {
	wantTitles := []string{
		"Improper Authentication and Identity Management",
		"Missing or Improper Access Control",
		"Input Validation/Sanitization Failures",
		"Input/Instruction Boundary Distinction Failure",
		"Inadequate Data Protection and Confidentiality Controls",
		"Missing Integrity/Verification Controls",
		"Session and Transport Security Failures",
		"Network Binding/Isolation Failures",
		"Trust Boundary and Privilege Design Failures",
		"Resource Management/Rate Limiting Absence",
		"Supply Chain and Lifecycle Security Failures",
		"Insufficient Logging, Monitoring, and Auditability",
	}
	got := CoSAIMCPSecurity()
	if len(got) != 12 {
		t.Fatalf("expected 12 CoSAI MCP controls, got %d", len(got))
	}
	validCoverage := map[mcpCoverage]bool{mcpAddressed: true, mcpPartial: true, mcpReferenced: true}
	for i, c := range got {
		wantID := fmt.Sprintf("MCP-T%d", i+1)
		if c.ID != wantID {
			t.Errorf("entry %d: id = %q, want %q (catalog must be in order)", i, c.ID, wantID)
			continue
		}
		if c.Title != wantTitles[i] {
			t.Errorf("%s title = %q, want %q", c.ID, c.Title, wantTitles[i])
		}
		if c.ProductControl == "" || len(c.Evidence) == 0 {
			t.Errorf("%s must map to a product control with evidence: %+v", c.ID, c)
		}
		if !validCoverage[c.Coverage] {
			t.Errorf("%s coverage = %q, not a valid grade", c.ID, c.Coverage)
		}
	}
}

// TestCoSAIMCPOWASPRefsValid asserts every OWASPRefs entry — Olivares' OWN
// cross-map, no upstream CoSAI↔OWASP mapping exists — points at a real OWASP MCP
// Top 10 id from the sibling catalog (owasp_mcp.go, same package).
func TestCoSAIMCPOWASPRefsValid(t *testing.T) {
	owaspIDs := map[string]bool{}
	for _, c := range MCPTop10() {
		owaspIDs[c.ID] = true
	}
	for _, c := range CoSAIMCPSecurity() {
		for _, ref := range c.OWASPRefs {
			if !owaspIDs[ref] {
				t.Errorf("%s OWASPRefs entry %q is not an OWASP MCP Top 10 id", c.ID, ref)
			}
		}
	}
}

func TestCoSAIMCPForFindingKind(t *testing.T) {
	cases := map[string]string{
		"mcp_shadow":     "MCP-T11",
		"mcp_provenance": "MCP-T11",
		"mcp_auth":       "MCP-T1",
		"mcp_surface":    "MCP-T4",
		// mcp_posture is DELIBERATELY absent: a posture finding's category depends
		// on the specific issue (over-broad scope = MCP-T2, injected description =
		// MCP-T4, …) so a single kind→id mapping would be wrong — the precise id
		// rides in the finding's title, mirroring mcpTop10ByFindingKind.
		"mcp_posture":  "",
		"mcp_revision": "", // no unambiguous mapping
		"health":       "",
		"":             "",
	}
	for kind, want := range cases {
		if got := CoSAIMCPForFindingKind(kind); got != want {
			t.Errorf("CoSAIMCPForFindingKind(%q) = %q, want %q", kind, got, want)
		}
	}
}

// TestCoSAIMCPImmutable confirms the accessor returns a copy (callers can't mutate
// the canonical catalog).
func TestCoSAIMCPImmutable(t *testing.T) {
	a := CoSAIMCPSecurity()
	a[0].Title = "MUTATED"
	if CoSAIMCPSecurity()[0].Title == "MUTATED" {
		t.Error("CoSAIMCPSecurity() must return a defensive copy")
	}
}
