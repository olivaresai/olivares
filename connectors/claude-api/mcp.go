// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file models the Anthropic Messages-API MCP connector (CLA-09 ≡ AIP-08): the
// API-SIDE governance of which MCP tools a Claude API-driven agent may invoke, via
// mcp_servers + the mcp_toolset allow/denylist. This is DISTINCT from introspecting
// an MCP server over the protocol (the `mcp` connector, protocolVersion 2025-06-18) — that is the OBSERVED/INTROSPECTED surface; this is the PERMITTED side
// (policy): the explicit allow-set a deployment grants. The connector turns each
// allow-listed tool into a PERMITTED access edge (Source=policy) that module III
// crosses against the observed/introspected MCP edges to compute the R/RW diff for
// API-driven agents.
//
// Field names + constraints are verified against the Messages-API MCP-connector docs
// (jun-2026): the current beta header is mcp-client-2025-11-20 (the inline
// tool_configuration of mcp-client-2025-04-04 is deprecated); only tool calls are
// supported (no MCP resources/prompts); the feature is NOT eligible for Zero Data
// Retention; and it is available on the Claude API, Claude Platform on AWS and
// Microsoft Foundry — NOT on Amazon Bedrock or Google Vertex.
package claudeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// gatherToolsetEdges emits the PERMITTED MCP-tool edges for every declared toolset
// grant (CLA-09). It is idempotent — module III upserts by natural key — so emitting
// the same allow-set on each gather is safe. Edges carry Source=policy, so III
// derives permitted=true and crosses them against the observed/introspected MCP edges.
func (s *Source) gatherToolsetEdges(ctx context.Context, sink sdk.Sink) error {
	if len(s.toolsets) == 0 {
		return nil
	}
	now := s.clock().UTC()
	for _, g := range s.toolsets {
		for _, e := range g.PermittedEdges(now) {
			if err := sink.Emit(ctx, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// parseToolsets parses the connector's mcp_toolsets config (a JSON array of
// ToolsetGrant). An empty string is no governance (nil, no error). A malformed policy
// is a hard error — a typo must not silently leave an API-driven agent ungoverned
// (same posture as the cooperative connector's enforcement policy).
func parseToolsets(s string) ([]ToolsetGrant, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var grants []ToolsetGrant
	if err := json.Unmarshal([]byte(s), &grants); err != nil {
		return nil, fmt.Errorf("claudeapi: mcp_toolsets is not a valid JSON array of grants: %w", err)
	}
	for _, g := range grants {
		// default_enabled=true is an "all server tools minus denylist" posture whose
		// PERMITTED set cannot be enumerated API-side without introspecting the server's
		// tool inventory (that is the protocol `mcp` connector's job). Emitting only the
		// explicit allow-set would understate the real grant — a misleading partial
		// permitted set. Reject it (never silently ungoverned): the operator must
		// declare explicit allowed_tools here, and the broad posture is reconciled via
		// the introspected OBSERVED side in module III.
		if g.DefaultEnabled {
			return nil, fmt.Errorf("claudeapi: mcp_toolset for agent %q/server %q sets default_enabled=true, which the API-side connector cannot enumerate; declare explicit allowed_tools instead", g.AgentRef, g.ServerName)
		}
	}
	return grants, nil
}

// MCP-connector beta headers and constraints (verified jun-2026).
const (
	// MCPBetaHeader is the current Messages-API MCP-connector beta header value.
	MCPBetaHeader = "mcp-client-2025-11-20"
	// MCPBetaHeaderDeprecated is the prior version (inline tool_configuration); it is
	// deprecated — callers MUST migrate to MCPBetaHeader.
	MCPBetaHeaderDeprecated = "mcp-client-2025-04-04"

	// resMCPTool is the ResourceKind of an MCP tool — it MUST match the observed otel
	// mcp.tool edges so a permitted edge reconciles with the observed one.
	resMCPTool = "mcp.tool"
)

// MCPConnectorAvailability records, per deployment surface, whether the Messages-API
// MCP connector is available (verified jun-2026). Bedrock and Vertex do NOT support
// it; the API, Claude Platform on AWS and Foundry do.
var MCPConnectorAvailability = map[model.Gateway]bool{
	model.GatewayDirect:            true,
	model.GatewayClaudePlatformAWS: true,
	model.GatewayFoundry:           true,
	model.GatewayBedrockMantle:     false,
	model.GatewayBedrockLegacy:     false,
	model.GatewayVertex:            false,
}

// MCPConnectorAvailableOn reports whether the MCP connector is available on a
// gateway. An unseeded gateway returns false (fail-closed: do not assume support).
func MCPConnectorAvailableOn(g model.Gateway) bool { return MCPConnectorAvailability[g] }

// ---- Messages-API request shape (what the inference client sends) ----------------

// MCPServer is one entry of the request's mcp_servers[]: a remote MCP server a Claude
// deployment is wired to. Only the URL transport is supported (the server must be
// publicly reachable over HTTPS); local STDIO servers cannot be connected.
type MCPServer struct {
	Type               string `json:"type"` // always "url"
	URL                string `json:"url"`
	Name               string `json:"name"`
	AuthorizationToken string `json:"authorization_token,omitempty"`
}

// MCPToolConfig is a per-tool override inside an mcp_toolset: enabled is the
// allow/deny switch (the denylist sets it false), defer_loading defers the tool's
// schema until first use.
type MCPToolConfig struct {
	Enabled      bool `json:"enabled"`
	DeferLoading bool `json:"defer_loading,omitempty"`
}

// MCPToolset is one entry of the request's tools[] of type "mcp_toolset" (the current
// shape under MCPBetaHeader). default_config.enabled is the server-wide default
// (default-disabled is the safe posture); configs holds per-tool overrides.
type MCPToolset struct {
	Type          string                   `json:"type"` // always "mcp_toolset"
	MCPServerName string                   `json:"mcp_server_name"`
	DefaultConfig MCPToolConfig            `json:"default_config"`
	Configs       map[string]MCPToolConfig `json:"configs,omitempty"`
}

// ---- Governance grant (operator-declared, the PERMITTED side) ---------------------

// ToolsetGrant is the operator-declared governance of one API-driven agent's MCP
// access: which server, the default-disabled posture, and the explicit allow/deny
// tool sets. It is the PERMITTED-side policy the connector turns into access edges.
type ToolsetGrant struct {
	// AgentRef is the API-driven agent/deployment this grant governs (the edge origin).
	AgentRef string `json:"agent_ref"`
	// ServerName is the MCP server (matches an mcp_servers[].name).
	ServerName string `json:"server_name"`
	// ServerURL is the server's HTTPS URL (informational; the edge keys on name/tool).
	ServerURL string `json:"server_url,omitempty"`
	// DefaultEnabled mirrors default_config.enabled (default false = default-disabled).
	DefaultEnabled bool `json:"default_enabled,omitempty"`
	// AllowedTools are the explicitly-enabled tools (the allow-set). These become
	// PERMITTED edges.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// DeniedTools are explicitly-disabled tools (the denylist) — modeled for
	// governance/visibility; they are NOT permitted, so they emit no permitted edge.
	DeniedTools []string `json:"denied_tools,omitempty"`
}

// Valid reports whether the grant is well-formed enough to emit edges (it names an
// agent, a server, and at least one allowed tool). default-disabled with no allowed
// tools grants nothing, so it is intentionally a no-op rather than an error.
func (g ToolsetGrant) Valid() bool {
	return strings.TrimSpace(g.AgentRef) != "" && strings.TrimSpace(g.ServerName) != "" && len(g.allowedSet()) > 0
}

// allowedSet returns the de-duplicated, denylist-subtracted set of permitted tool
// names: a tool present in both allow and deny is treated as DENIED (deny wins).
func (g ToolsetGrant) allowedSet() []string {
	denied := make(map[string]struct{}, len(g.DeniedTools))
	for _, d := range g.DeniedTools {
		if d = strings.TrimSpace(d); d != "" {
			denied[d] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(g.AllowedTools))
	out := make([]string, 0, len(g.AllowedTools))
	for _, t := range g.AllowedTools {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, isDenied := denied[t]; isDenied {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// PermittedEdges turns the grant into PERMITTED access edges — one per allow-listed
// tool. Source is SignalPolicy (module III derives permitted=true from it; never
// observed); OriginKind is "agent" (the only routable kind besides session/identity);
// ResourceKind/ResourceRef are mcp.tool / "<server>/<tool>" — EXACTLY the shape of
// the observed otel mcp.tool edges so the diff reconciles. Mode is unknown: an
// allow-grant is not itself a data read or write. at stamps the observation.
func (g ToolsetGrant) PermittedEdges(at time.Time) []model.EdgeObservation {
	if !g.Valid() {
		return nil
	}
	var out []model.EdgeObservation
	for _, tool := range g.allowedSet() {
		out = append(out, model.EdgeObservation{
			OriginKind:   "agent",
			OriginRef:    g.AgentRef,
			ResourceKind: resMCPTool,
			ResourceRef:  g.ServerName + "/" + tool,
			Mode:         model.ModeUnknown,
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ToolRef:      tool,
			ObservedAt:   at,
		})
	}
	return out
}
