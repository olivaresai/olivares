// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestToolsetGrant_PermittedEdges(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	g := ToolsetGrant{
		AgentRef:     "agent:billing",
		ServerName:   "github",
		AllowedTools: []string{"list_issues", "get_pr", "list_issues"}, // dup
		DeniedTools:  []string{"delete_repo", "get_pr"},                // deny wins over allow for get_pr
	}
	edges := g.PermittedEdges(at)
	if len(edges) != 1 {
		t.Fatalf("permitted edges = %d, want 1 (list_issues; get_pr denied, dup removed)", len(edges))
	}
	e := edges[0]
	if e.Source != model.SignalPolicy {
		t.Errorf("source = %q, want policy (so III derives permitted=true)", e.Source)
	}
	if e.OriginKind != "agent" || e.OriginRef != "agent:billing" {
		t.Errorf("origin = %s/%s, want agent/agent:billing (routable kind)", e.OriginKind, e.OriginRef)
	}
	if e.ResourceKind != "mcp.tool" || e.ResourceRef != "github/list_issues" {
		t.Errorf("resource = %s/%s, want mcp.tool/github/list_issues (matches observed otel edge)", e.ResourceKind, e.ResourceRef)
	}
	if e.Mode != model.ModeUnknown || e.ToolRef != "list_issues" || e.Confidence != model.ConfidenceAttributed {
		t.Errorf("edge = %+v", e)
	}
	if !e.ObservedAt.Equal(at) {
		t.Errorf("observedAt = %v, want %v", e.ObservedAt, at)
	}
}

func TestToolsetGrant_InvalidGrantsNothing(t *testing.T) {
	// Default-disabled with no allowed tools is a no-op (grants nothing), not an error.
	if g := (ToolsetGrant{AgentRef: "a", ServerName: "s"}); g.Valid() || len(g.PermittedEdges(time.Now())) != 0 {
		t.Error("empty allow-set must grant nothing")
	}
	// Missing agent or server is invalid.
	if (ToolsetGrant{ServerName: "s", AllowedTools: []string{"t"}}).Valid() {
		t.Error("grant without agent_ref must be invalid")
	}
}

func TestParseToolsets(t *testing.T) {
	if g, err := parseToolsets(""); g != nil || err != nil {
		t.Errorf("empty => nil,nil; got %+v %v", g, err)
	}
	good := `[{"agent_ref":"a","server_name":"github","default_enabled":false,"allowed_tools":["list_issues"]}]`
	g, err := parseToolsets(good)
	if err != nil || len(g) != 1 || g[0].ServerName != "github" {
		t.Fatalf("parse good: %+v err=%v", g, err)
	}
	// Malformed fails hard (never silently ungoverned).
	if _, err := parseToolsets(`{not json`); err == nil {
		t.Error("malformed mcp_toolsets must fail Open")
	}
	// default_enabled=true is rejected (its closure can't be enumerated API-side;
	// emitting only the explicit allow-set would understate the real grant).
	if _, err := parseToolsets(`[{"agent_ref":"a","server_name":"s","default_enabled":true,"allowed_tools":["t"]}]`); err == nil {
		t.Error("default_enabled=true must fail Open (never a misleading partial permitted set)")
	}
}

func TestGather_EmitsPermittedMCPEdges(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	s.toolsets = []ToolsetGrant{{AgentRef: "agent:billing", ServerName: "github", AllowedTools: []string{"list_issues", "get_pr"}}}
	// No admin key: only the toolset (policy) edges should emit — governance flows even
	// in offline mode.
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.obs) != 2 {
		t.Fatalf("emitted %d, want 2 permitted mcp.tool edges", len(sink.obs))
	}
	for _, o := range sink.obs {
		e, ok := o.(model.EdgeObservation)
		if !ok {
			t.Fatalf("observation is not an edge: %T", o)
		}
		if e.Source != model.SignalPolicy || e.ResourceKind != "mcp.tool" || e.OriginRef != "agent:billing" {
			t.Errorf("edge = %+v", e)
		}
	}
}

func TestMCPConnectorAvailability(t *testing.T) {
	if !MCPConnectorAvailableOn(model.GatewayDirect) || !MCPConnectorAvailableOn(model.GatewayClaudePlatformAWS) || !MCPConnectorAvailableOn(model.GatewayFoundry) {
		t.Error("MCP connector must be available on API / Claude-Platform-AWS / Foundry")
	}
	if MCPConnectorAvailableOn(model.GatewayBedrockMantle) || MCPConnectorAvailableOn(model.GatewayBedrockLegacy) || MCPConnectorAvailableOn(model.GatewayVertex) {
		t.Error("MCP connector is NOT available on Bedrock/Vertex")
	}
}
