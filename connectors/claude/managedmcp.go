// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file OBSERVES the live managed-mcp.json (ANT2-12) — the client-side allow/deny
// control over which MCP servers a Claude Code client may connect to — and models its
// EVALUATION ORDER so the resolved effect (which servers are permitted/denied) is
// visible to governance. The order is verified: the allow and deny sets MERGE, the
// DENYLIST WINS, then the allowlist matches BY TYPE with EXACT url/command match. A
// serverName-only match is NOT a security control (a hostile server can claim any
// name), so an allow entry that relies on serverName alone is surfaced as a DRIFT
// finding. The connector only OBSERVES the file and models its effect; authoring/
// editing it is.
//
// Authority (verbatim, jun-2026): code.claude.com/docs/en/managed-mcp (ANT2-12).
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// subjectManagedMCP is the FindingReport subject for managed-mcp posture/drift.
const subjectManagedMCP = "claude.managed_mcp"

// MCPMatchSet is one side (allow or deny) of managed-mcp.json, matched BY TYPE. URLs
// and Commands are EXACT-match security controls; ServerNames is NOT a security
// control (it is spoofable) — a match on it alone is surfaced as drift.
type MCPMatchSet struct {
	URLs        []string `json:"urls"`
	Commands    []string `json:"commands"`
	ServerNames []string `json:"serverNames"`
}

// ManagedMCP is the observed managed-mcp.json: the allow/deny control over MCP servers.
type ManagedMCP struct {
	Allow MCPMatchSet `json:"allow"`
	Deny  MCPMatchSet `json:"deny"`
}

// MCPCandidate is a concrete MCP server being evaluated against the policy.
type MCPCandidate struct {
	Name    string
	URL     string
	Command string
}

// MCPDecision is the resolved effect of the policy on a candidate.
type MCPDecision struct {
	// Allowed is the final allow/deny verdict.
	Allowed bool
	// Reason is a short, non-sensitive explanation of which rule decided.
	Reason string
	// WeakServerNameOnly is true when the candidate was ALLOWED purely on a serverName
	// match (no url/command allow matched) — a permit that is not a real security
	// control (spoofable). It is allowed (the policy says so) but flagged as drift.
	WeakServerNameOnly bool
}

// Evaluate resolves the managed-mcp.json eval-order for a candidate (ANT2-12): deny
// wins (by exact url/command OR by serverName), then allow by EXACT url/command, then
// allow by serverName-only (allowed-but-weak), else default DENY (the allowlist is the
// gate). It is a pure function — the testable core of the modeled effect.
func (m ManagedMCP) Evaluate(c MCPCandidate) MCPDecision {
	// 1) Denylist wins. A deny by ANY type (incl. serverName) blocks — denying by a
	//    spoofable name is conservative (it can only over-deny, never over-allow).
	if c.URL != "" && contains(m.Deny.URLs, c.URL) {
		return MCPDecision{Allowed: false, Reason: "denied by url"}
	}
	if c.Command != "" && contains(m.Deny.Commands, c.Command) {
		return MCPDecision{Allowed: false, Reason: "denied by command"}
	}
	if c.Name != "" && contains(m.Deny.ServerNames, c.Name) {
		return MCPDecision{Allowed: false, Reason: "denied by serverName"}
	}
	// 2) Allow by EXACT url/command (the real security controls).
	if c.URL != "" && contains(m.Allow.URLs, c.URL) {
		return MCPDecision{Allowed: true, Reason: "allowed by url"}
	}
	if c.Command != "" && contains(m.Allow.Commands, c.Command) {
		return MCPDecision{Allowed: true, Reason: "allowed by command"}
	}
	// 3) Allow by serverName ONLY — permitted by the policy, but NOT a security control.
	if c.Name != "" && contains(m.Allow.ServerNames, c.Name) {
		return MCPDecision{Allowed: true, Reason: "allowed by serverName (NOT a security control)", WeakServerNameOnly: true}
	}
	// 4) Not in the allowlist → denied (the allowlist is the gate).
	return MCPDecision{Allowed: false, Reason: "not in allowlist"}
}

// contains reports whether s is in xs (exact match).
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// parseManagedMCP decodes a managed-mcp.json body. A malformed file is an error so a
// broken control file is surfaced (a posture finding), never silently treated as "no
// control" (which would fail-open).
func parseManagedMCP(body []byte) (ManagedMCP, error) {
	var m ManagedMCP
	if err := json.Unmarshal(body, &m); err != nil {
		return ManagedMCP{}, fmt.Errorf("claude: parse managed-mcp.json: %w", err)
	}
	return m, nil
}

// gatherManagedMCP reads the observed managed-mcp.json (if configured) and emits its
// posture: a summary finding of the control, plus a DRIFT finding for every allow
// entry that relies on serverName only (not a security control). A missing file is a
// no-op (the operator may not use managed-mcp); an unreadable/malformed file is a
// medium posture finding (a broken control is a real risk). It never authors the file.
func (s *Source) gatherManagedMCP(at time.Time, dispatch func(model.Observation)) {
	if s.cfg.managedMCPPath == "" {
		return
	}
	body, err := os.ReadFile(s.cfg.managedMCPPath)
	if err != nil {
		dispatch(model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectManagedMCP,
			SubjectRef:  s.cfg.managedMCPPath,
			Title:       "managed-mcp.json not readable",
			DetailHash:  redact.Hash("managed-mcp read error: " + err.Error()),
			OccurredAt:  at,
		})
		return
	}
	m, err := parseManagedMCP(body)
	if err != nil {
		dispatch(model.FindingReport{
			Kind:        "posture",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectManagedMCP,
			SubjectRef:  s.cfg.managedMCPPath,
			Title:       "managed-mcp.json malformed (MCP control may be ineffective)",
			DetailHash:  redact.Hash("managed-mcp parse error"),
			OccurredAt:  at,
		})
		return
	}

	// Summary posture: how many real (url/command) vs weak (serverName) allow controls.
	realAllow := len(m.Allow.URLs) + len(m.Allow.Commands)
	dispatch(model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectManagedMCP,
		SubjectRef:  s.cfg.managedMCPPath,
		Title:       fmt.Sprintf("managed-mcp.json: %d exact allow rule(s), %d deny rule(s), %d serverName-only allow(s)", realAllow, len(m.Deny.URLs)+len(m.Deny.Commands)+len(m.Deny.ServerNames), len(m.Allow.ServerNames)),
		DetailHash:  redact.Hash(fmt.Sprintf("managed-mcp allow_url=%d allow_cmd=%d allow_name=%d deny=%d", len(m.Allow.URLs), len(m.Allow.Commands), len(m.Allow.ServerNames), len(m.Deny.URLs)+len(m.Deny.Commands)+len(m.Deny.ServerNames))),
		OccurredAt:  at,
	})

	// Drift: each serverName-only allow is a spoofable, non-security control.
	for _, name := range m.Allow.ServerNames {
		dispatch(model.FindingReport{
			Kind:        "drift",
			Severity:    model.SeverityMedium,
			SubjectKind: subjectManagedMCP,
			SubjectRef:  name,
			Title:       "MCP server allowed by serverName only — not a security control (spoofable)",
			DetailHash:  redact.Hash("managed-mcp allow.serverNames entry " + name + " is not a security control; use exact url/command (ANT2-12)"),
			OccurredAt:  at,
		})
	}
}
