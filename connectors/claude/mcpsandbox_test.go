// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// TestManagedMCPEvalOrder pins the ANT2-12 evaluation order: deny wins, then allow by
// exact url/command, then serverName-only (allowed-but-weak), else default deny.
func TestManagedMCPEvalOrder(t *testing.T) {
	m := ManagedMCP{
		Allow: MCPMatchSet{
			URLs:        []string{"https://good.example/mcp"},
			Commands:    []string{"/usr/bin/safe-mcp"},
			ServerNames: []string{"weakly-named"},
		},
		Deny: MCPMatchSet{
			URLs: []string{"https://good.example/mcp"}, // also in allow → deny must win
		},
	}

	// Denylist wins even when the same url is allow-listed.
	if d := m.Evaluate(MCPCandidate{URL: "https://good.example/mcp"}); d.Allowed {
		t.Errorf("deny must win over allow for the same url; got %+v", d)
	}
	// Allow by exact command.
	if d := m.Evaluate(MCPCandidate{Command: "/usr/bin/safe-mcp"}); !d.Allowed || d.WeakServerNameOnly {
		t.Errorf("exact command allow = %+v, want allowed/strong", d)
	}
	// serverName-only allow → allowed but flagged weak (not a security control).
	if d := m.Evaluate(MCPCandidate{Name: "weakly-named"}); !d.Allowed || !d.WeakServerNameOnly {
		t.Errorf("serverName-only allow = %+v, want allowed+weak", d)
	}
	// Not in allowlist → denied (allowlist is the gate).
	if d := m.Evaluate(MCPCandidate{URL: "https://unknown.example/mcp"}); d.Allowed {
		t.Errorf("unknown server must be denied; got %+v", d)
	}
	// A command in the denylist beats nothing in the allowlist.
	m2 := ManagedMCP{Deny: MCPMatchSet{Commands: []string{"rm-rf-mcp"}}}
	if d := m2.Evaluate(MCPCandidate{Command: "rm-rf-mcp"}); d.Allowed {
		t.Errorf("explicitly denied command must be denied; got %+v", d)
	}
}

// TestManagedMCPDriftFinding proves gatherManagedMCP emits a drift finding for a
// serverName-only allow entry (ANT2-12).
func TestManagedMCPDriftFinding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed-mcp.json")
	if err := os.WriteFile(path, []byte(`{"allow":{"urls":["https://a/mcp"],"serverNames":["spoofable"]},"deny":{}}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	s := New()
	s.cfg.managedMCPPath = path
	var obs []model.Observation
	s.gatherManagedMCP(d2Time, func(o model.Observation) { obs = append(obs, o) })

	var sawDrift, sawSummary bool
	for _, o := range obs {
		f, ok := o.(model.FindingReport)
		if !ok {
			continue
		}
		if f.Kind == "drift" && f.SubjectRef == "spoofable" {
			sawDrift = true
		}
		if f.Kind == "posture" && f.SubjectKind == subjectManagedMCP {
			sawSummary = true
		}
	}
	if !sawDrift {
		t.Error("expected a serverName-only drift finding for 'spoofable'")
	}
	if !sawSummary {
		t.Error("expected a managed-mcp posture summary finding")
	}
}

// TestSandboxPosture proves ANT2-13: an explicitly weakened sandbox is flagged and the
// egress allowlist carries the domain-fronting caveat.
func TestSandboxPosture(t *testing.T) {
	yes := true
	no := false
	sb := &SandboxConfig{
		AllowUnsandboxedCommands: &yes,
		FailIfUnavailable:        &no,
		AllowedDomains:           []string{"api.anthropic.com"},
		DeniedDomains:            []string{"evil.example"},
	}
	findings := SandboxPostureFindings(sb, d2Time)
	var sawUnsandboxed, sawFailOpen, sawFronting bool
	for _, f := range findings {
		switch {
		case f.SubjectRef == "allowUnsandboxedCommands" && f.Severity == model.SeverityHigh:
			sawUnsandboxed = true
		case f.SubjectRef == "failIfUnavailable" && f.Severity == model.SeverityMedium:
			sawFailOpen = true
		case f.SubjectKind == subjectEgress:
			sawFronting = true
		}
	}
	if !sawUnsandboxed || !sawFailOpen || !sawFronting {
		t.Errorf("sandbox posture findings missing one of {unsandboxed:%v failOpen:%v egress/fronting:%v}", sawUnsandboxed, sawFailOpen, sawFronting)
	}
	// A nil sandbox block yields no findings.
	if len(SandboxPostureFindings(nil, d2Time)) != 0 {
		t.Error("nil sandbox config must yield no findings")
	}
	// The documented default egress allowlist is non-empty reference data.
	if len(DefaultEgressAllowlist()) == 0 {
		t.Error("default egress allowlist must be modeled")
	}
}
