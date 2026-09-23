// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// mcp_egress_destinations_test.go closes the multi-destination gap of the MCP egress oracle
// (Spec review MAJOR-1) and the mixed batch envelope (Standards review E-5): every declared
// server must be described with its own origin and its own toolset position, the destination
// limit is inclusive, and a batch envelope is either governed on every entry or on none.
package claudeapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// threeServerDecl declares three servers whose toolsets appear in a different order, with a
// non-MCP tool between them, so origin and tool-index pairing by name is observable.
func threeServerDecl() (tools, servers []any) {
	servers = []any{
		map[string]any{"type": "url", "name": "srv-a", "url": "https://a.example.com/a"},
		map[string]any{"type": "url", "name": "srv-b", "url": "https://b.example.com:8443/b?x=1", "authorization_token": "tok-b"},
		map[string]any{"type": "url", "name": "srv-c", "url": "https://[2001:db8::2]/c"},
	}
	tools = []any{
		map[string]any{"type": "mcp_toolset", "mcp_server_name": "srv-c"},
		webSearchTool(),
		map[string]any{"type": "mcp_toolset", "mcp_server_name": "srv-a", "default_config": map[string]any{"enabled": false}},
		map[string]any{"type": "mcp_toolset", "mcp_server_name": "srv-b"},
	}
	return tools, servers
}

// jsonRoundTrip is the test's own expectation of what a fixture serializes to.
func jsonRoundTrip(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("fixture marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("fixture unmarshal: %v", err)
	}
	return out
}

// TestMCPWireSnapshotDescribesEveryDestination proves each server is described with its OWN
// canonical origin and the index of the toolset that names it, in mcp_servers[] order, and
// that the forwarded body keeps every tools[] and mcp_servers[] position. Mutants: every
// destination given the first origin, only the first destination described, or tool indexes
// paired by toolset position instead of by name.
func TestMCPWireSnapshotDescribesEveryDestination(t *testing.T) {
	tools, servers := threeServerDecl()
	gov, _, in := mcpGovern(t, mcpDecl(tools, servers))
	if in.MCP == nil {
		t.Fatal("a request declaring three servers produced no MCP gate input")
	}
	want := []MCPDestination{
		{ServerIndex: 0, ToolIndex: 2, Origin: "https://a.example.com:443"},
		{ServerIndex: 1, ToolIndex: 3, Origin: "https://b.example.com:8443"},
		{ServerIndex: 2, ToolIndex: 0, Origin: "https://[2001:db8::2]:443"},
	}
	if !reflect.DeepEqual(in.MCP.Destinations, want) {
		t.Fatalf("destinations = %#v, want %#v", in.MCP.Destinations, want)
	}
	if !reflect.DeepEqual(in.Tools, []any{mcpMarkerView(), webSearchTool(), mcpMarkerView(), mcpMarkerView()}) {
		t.Errorf("gate Tools = %#v, want markers at 0, 2, 3 and the web tool at 1", in.Tools)
	}
	if digest, err := MCPCoverageDigest(in); err != nil || digest != in.MCP.Coverage.Digest {
		t.Errorf("issued coverage over three destinations does not verify: %v", err)
	}
	sentTools, sentServers := sentMCP(t, frozenBody(t, gov))
	fixtureTools, fixtureServers := threeServerDecl()
	if !reflect.DeepEqual(sentTools, jsonRoundTrip(t, fixtureTools)) {
		t.Errorf("forwarded tools = %#v, want the declared order", sentTools)
	}
	if !reflect.DeepEqual(sentServers, jsonRoundTrip(t, fixtureServers)) {
		t.Errorf("forwarded servers = %#v, want the declared order", sentServers)
	}
}

// TestMCPWireSnapshotAcceptsTheDestinationLimit proves the limit is inclusive: exactly
// MCPMaxDestinations servers are captured and described in order. Mutant: refusing at the
// limit instead of above it.
func TestMCPWireSnapshotAcceptsTheDestinationLimit(t *testing.T) {
	var tools, servers []any
	for i := 0; i < MCPMaxDestinations; i++ {
		name := "srv-" + string(rune('a'+i))
		servers = append(servers, map[string]any{"type": "url", "name": name, "url": "https://mcp.example.com/" + name})
		tools = append(tools, map[string]any{"type": "mcp_toolset", "mcp_server_name": name})
	}
	_, in := mcpCapture(t, mcpDecl(tools, servers))
	if in.MCP == nil || len(in.MCP.Destinations) != MCPMaxDestinations {
		t.Fatalf("captured %#v, want %d destinations", in.MCP, MCPMaxDestinations)
	}
	for j, d := range in.MCP.Destinations {
		if d != (MCPDestination{ServerIndex: uint32(j), ToolIndex: uint32(j), Origin: "https://mcp.example.com:443"}) {
			t.Errorf("destination %d = %#v", j, d)
		}
	}
}

// TestBatches_MCPBindingRefusesMixedEnvelope proves a governed batch is governed on every
// entry: an envelope mixing bound and unbound entries is refused (mcp_binding_changed), with
// no conditional beta gap for an unbound entry that declares MCP; an entirely unbound legacy
// batch keeps its transport. Expected at bf54a94bc0: both mixed rows accepted (red).
func TestBatches_MCPBindingRefusesMixedEnvelope(t *testing.T) {
	bound, _, _ := mcpGovern(t, mcpMapDecl())
	absent, _, _ := mcpGovern(t, mcpPrompt())
	rows := map[string][]BatchRequest{
		"bound declaration with an unbound entry": {{CustomID: "c0", Params: bound}, {CustomID: "c1", Params: mcpPrompt()}},
		"bound absence with an unbound MCP entry": {{CustomID: "c0", Params: absent}, {CustomID: "c1", Params: mcpMapDecl()}},
	}
	for name, entries := range rows {
		t.Run(name, func(t *testing.T) {
			_, err := MarshalPreparedBatch(entries)
			requireMCPCode(t, err, MCPDenyBindingChanged)
		})
	}
	p, err := MarshalPreparedBatch([]BatchRequest{{CustomID: "c0", Params: mcpMapDecl()}, {CustomID: "c1", Params: mcpPrompt()}})
	if err != nil {
		t.Fatalf("an entirely unbound legacy batch must still freeze: %v", err)
	}
	if !strings.Contains(string(p.Body()), mcpTestServerURL) {
		t.Errorf("unbound legacy batch lost its declaration: %s", p.Body())
	}
	if !mcpBetaConsistent(p.beta, true) {
		t.Errorf("unbound legacy batch beta = %q, want the current MCP beta once", p.beta)
	}
}
