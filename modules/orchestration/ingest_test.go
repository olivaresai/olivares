// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"testing"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestClassifyLinkDelegation covers the cooperative-delegation signals the
// extension classifies: the CURRENT Agent tool and legacy Task tool (both agent.task
// edges), gen_ai nested-agent delegation, MCP topology and A2A — and the negative
// cases that must stay unclassified (a generic file access, a delegation edge with no
// worker).
func TestClassifyLinkDelegation(t *testing.T) {
	cases := []struct {
		name     string
		edge     sdkmodel.EdgeObservation
		wantKind string
		wantWork string
		wantTool string
		wantOK   bool
	}{
		{
			name:     "legacy Task tool",
			edge:     sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: "s1", ResourceKind: "agent.task", ResourceRef: "code-reviewer", ToolRef: "Task"},
			wantKind: linkDelegation, wantWork: "code-reviewer", wantTool: "Task", wantOK: true,
		},
		{
			name:     "current Agent tool",
			edge:     sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: "s1", ResourceKind: "agent.task", ResourceRef: "researcher", ToolRef: "Agent"},
			wantKind: linkDelegation, wantWork: "researcher", wantTool: "Agent", wantOK: true,
		},
		{
			name:     "gen_ai nested agent delegation",
			edge:     sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: "s1", ResourceKind: resGenAIAgent, ResourceRef: "planner", ToolRef: ""},
			wantKind: linkDelegation, wantWork: "planner", wantTool: "", wantOK: true,
		},
		{
			name:     "mcp server",
			edge:     sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: "s1", ResourceKind: "mcp.server", ResourceRef: "github"},
			wantKind: linkMCPServer, wantWork: "github", wantOK: true,
		},
		{
			name:     "a2a peer",
			edge:     sdkmodel.EdgeObservation{OriginKind: "agent", OriginRef: "planner", ResourceKind: resA2AAgent, ResourceRef: "researcher", ToolRef: "summarize"},
			wantKind: linkA2A, wantWork: "researcher", wantTool: "summarize", wantOK: true,
		},
		{
			name: "non-delegation file access is ignored",
			edge: sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: "s1", ResourceKind: "file", ResourceRef: "/etc/hosts", ToolRef: "Read"},
		},
		{
			name: "delegation with no worker is not ok",
			edge: sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: "s1", ResourceKind: "agent.task", ResourceRef: "", ToolRef: "Agent"},
		},
		{
			name: "an unknown tool on an agent.task edge is not delegation",
			edge: sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: "s1", ResourceKind: "agent.task", ResourceRef: "x", ToolRef: "SomethingElse"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyLink(tc.edge)
			if got.ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (%+v)", got.ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got.kind != tc.wantKind || got.worker != tc.wantWork || got.toolRef != tc.wantTool {
				t.Fatalf("classifyLink = {kind:%q worker:%q tool:%q}, want {kind:%q worker:%q tool:%q}",
					got.kind, got.worker, got.toolRef, tc.wantKind, tc.wantWork, tc.wantTool)
			}
		})
	}
}
