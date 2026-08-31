// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// controls.go models the GA per-tool connector controls of Claude Cowork — the
// PERMITTED side of the connector/tool access-map diff, plus the live drift signal
// when an executed tool contradicts that policy.
//
// What the platform actually ships (verified AsOf 2026-06-10, primary sources:
// claude.com/blog/cowork-for-enterprise, GA 2026-04-09, and
// support.claude.com/en/articles/13930452-manage-custom-roles-on-enterprise-plans):
// per-tool connector controls are configured in the admin console ROLE EDITOR,
// "Connectors" tab (Enterprise plans), per connector or per individual tool, with
// three levels VERBATIM: "Always allow" / "Needs approval" / "Blocked" ("Blocked:
// The connector or tool is hidden"); setting a connector to "Custom" enables the
// per-tool configuration. The controls apply to Claude Cowork cloud and desktop;
// they do NOT govern Cowork deployed on a third-party platform. There is NO Admin
// API and NO managed-settings key for these controls — they are console-only.
//
// Why the model here is ORG-EFFECTIVE rather than per-role (honesty): upstream the
// controls are scoped to a ROLE, but Cowork's OTel telemetry does not carry the
// acting user's role, so this connector cannot resolve which role's matrix governed
// a given tool_result. The operator therefore authors the org-EFFECTIVE projection
// — the floor every role is expected to satisfy — and the connector turns it into:
//   - PERMITTED edges (Source=model.SignalPolicy; modules/access-map derives
//     permitted=true from that source) for every non-blocked connector/tool, in the
//     same mcp.server / "server/tool" shapes the OBSERVED OTel edges use, so the
//     diff reconciles byte-for-byte; and
//   - a LIVE DRIFT finding when a tool_result proves a blocked connector/tool
//     executed, or a needs-approval one ran with AUTOMATIC approval (config/hook
//     decision_source) — the human gate the policy demands did not happen.
// Because the upstream mechanism is console-only, drift here means the authored
// projection and the console reality diverged (or a role is broader than the
// declared floor) — exactly what an operator governing Cowork must see.

package cowork

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// findingKindControlDrift marks a tool_result that contradicts the org-effective
// connector-control policy: a blocked connector/tool EXECUTED, or a needs-approval
// one ran auto-approved. It is the OBSERVED-vs-PERMITTED live signal companion to
// the PermittedEdges policy side.
const findingKindControlDrift = "connector_control_drift"

// originIdentity is the OriginKind of the PERMITTED connector-control edges: the
// policy hangs off the governed org identity (cfgOrgRef), not a session — at
// authoring time there is no session, and the org-effective floor applies to all.
const originIdentity = "identity"

// ControlLevel is one of the three GA control levels of the role editor's
// "Connectors" tab, in the connector's stable snake_case spelling ("Always allow" /
// "Needs approval" / "Blocked" in the console UI).
type ControlLevel string

const (
	// ControlAlwaysAllow lets the connector/tool run without a per-use approval.
	ControlAlwaysAllow ControlLevel = "always_allow"
	// ControlNeedsApproval requires a HUMAN approval before each use; an execution
	// whose decision_source is config/hook means that gate did not happen.
	ControlNeedsApproval ControlLevel = "needs_approval"
	// ControlBlocked hides the connector or tool entirely ("Blocked: The connector
	// or tool is hidden") — an execution under it is hard drift.
	ControlBlocked ControlLevel = "blocked"
)

// Valid reports whether l is one of the three documented control levels.
func (l ControlLevel) Valid() bool {
	switch l {
	case ControlAlwaysAllow, ControlNeedsApproval, ControlBlocked:
		return true
	default:
		return false
	}
}

// ConnectorControl is the control for one connector (MCP server): a connector-wide
// Level and optional per-tool overrides — the latter mirror the console's "Custom"
// mode, where a connector's individual tools are configured separately. An empty
// Level with Tools set is exactly that Custom posture (tools resolve individually,
// the rest of the connector falls through to the policy Default).
type ConnectorControl struct {
	Level ControlLevel            `json:"level"`
	Tools map[string]ControlLevel `json:"tools"`
}

// ConnectorControlPolicy is the operator-authored, org-EFFECTIVE projection of the
// GA per-tool connector controls (see the file header for why it is org-effective
// rather than per-role). Connectors is keyed by MCP server name — the same name
// the OBSERVED edges carry (mcp_server_scope / the mcp__<server>__<tool> prefix).
type ConnectorControlPolicy struct {
	Default    ControlLevel                `json:"default"`
	Connectors map[string]ConnectorControl `json:"connectors"`
}

// ParseConnectorControls parses the connector_controls JSON. An empty/whitespace
// raw means NOT CONFIGURED (zero policy, nil error) — the controls surface is
// opt-in. Malformed JSON or any invalid level string is a hard error: an
// operator-authored policy must never silently disappear (deny-closed authoring —
// the same posture as claude-api's parseToolsets). A per-tool override must name a
// valid level: the override's presence IS the intent, so an empty value is an
// authoring error, while an absent connector Level / Default legitimately means
// "fall through".
func ParseConnectorControls(raw string) (ConnectorControlPolicy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ConnectorControlPolicy{}, nil
	}
	var p ConnectorControlPolicy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return ConnectorControlPolicy{}, fmt.Errorf("not a valid JSON policy: %w", err)
	}
	if p.Default != "" && !p.Default.Valid() {
		return ConnectorControlPolicy{}, fmt.Errorf("invalid default level %q", p.Default)
	}
	for server, c := range p.Connectors {
		if c.Level != "" && !c.Level.Valid() {
			return ConnectorControlPolicy{}, fmt.Errorf("connector %q: invalid level %q", server, c.Level)
		}
		for tool, lv := range c.Tools {
			if !lv.Valid() {
				return ConnectorControlPolicy{}, fmt.Errorf("connector %q tool %q: invalid level %q", server, tool, lv)
			}
		}
	}
	return p, nil
}

// Configured reports whether the operator authored any control at all — a listed
// connector or an org Default. The connector evaluates controls only when true, so
// an org that never opted in sees no permitted edges and no drift findings.
func (p ConnectorControlPolicy) Configured() bool {
	return p.Default != "" || len(p.Connectors) > 0
}

// EffectiveLevel resolves the control level governing one (server, tool), with the
// console's precedence: a per-tool override wins over the connector's Level, which
// wins over the policy Default; an empty value at each step falls through. A
// CONFIGURED policy that still resolves to nothing returns ControlBlocked —
// DENY-CLOSED: once an operator authors a control floor, an unlisted connector is
// treated as blocked, because "I forgot to list it" must read as "not permitted",
// never as a silent allow (the same reason an unlisted connector emits no
// permitted edge). Callers gate on Configured() first, so a never-authored policy
// does not block anything.
func (p ConnectorControlPolicy) EffectiveLevel(server, tool string) ControlLevel {
	if c, ok := p.Connectors[server]; ok {
		if tool != "" {
			if lv, ok := c.Tools[tool]; ok && lv != "" {
				return lv
			}
		}
		if c.Level != "" {
			return c.Level
		}
	}
	if p.Default != "" {
		return p.Default
	}
	return ControlBlocked
}

// PermittedEdges turns the policy into PERMITTED access edges — the side of the
// access-map diff modules/access-map derives permitted=true from (Source ==
// model.SignalPolicy). One mcp.server edge per listed connector whose effective
// level is not blocked, and one mcp.tool edge per non-blocked per-tool override,
// with ResourceRef "<server>/<tool>" — byte-matching mcpResourceRef's form so the
// diff reconciles against the OBSERVED OTel edges. Blocked entries emit NOTHING (a
// block is the absence of permission, not a grant). The edges hang off the governed
// org identity (orgRef): the org-effective floor is not session-scoped. Mode is
// Unknown — a control grant is not itself a data read or write. ToolRef stays
// empty on server edges and names the tool on tool edges (mirrors claude-api's
// ToolsetGrant.PermittedEdges). Order is deterministic (servers sorted, then each
// server's tools sorted) so re-emission per Gather is byte-stable.
func (p ConnectorControlPolicy) PermittedEdges(orgRef string, at time.Time) []model.EdgeObservation {
	if orgRef == "" || !p.Configured() {
		return nil
	}
	servers := make([]string, 0, len(p.Connectors))
	for s := range p.Connectors {
		servers = append(servers, s)
	}
	sort.Strings(servers)
	var out []model.EdgeObservation
	for _, server := range servers {
		c := p.Connectors[server]
		if p.EffectiveLevel(server, "") != ControlBlocked {
			out = append(out, model.EdgeObservation{
				OriginKind:   originIdentity,
				OriginRef:    orgRef,
				ResourceKind: resMCPServer,
				ResourceRef:  server,
				Mode:         model.ModeUnknown,
				Source:       model.SignalPolicy,
				Confidence:   model.ConfidenceAttributed,
				ObservedAt:   at,
			})
		}
		tools := make([]string, 0, len(c.Tools))
		for t := range c.Tools {
			tools = append(tools, t)
		}
		sort.Strings(tools)
		for _, tool := range tools {
			if c.Tools[tool] == ControlBlocked {
				continue
			}
			out = append(out, model.EdgeObservation{
				OriginKind:   originIdentity,
				OriginRef:    orgRef,
				ResourceKind: resMCP,
				ResourceRef:  server + "/" + tool,
				Mode:         model.ModeUnknown,
				Source:       model.SignalPolicy,
				Confidence:   model.ConfidenceAttributed,
				ToolRef:      tool,
				ObservedAt:   at,
			})
		}
	}
	return out
}

// mcpServerTool splits an MCP tool name (mcp__<server>__<tool>) into its server and
// tool, reporting ok=false when the name is not MCP-shaped or names no server. It
// parallels mcpResourceRef (resource.go) cut-for-cut, so a (server, tool) resolved
// here always reassembles to the exact "server/tool" ResourceRef the observed and
// permitted edges carry. A prefix-only or serverless name ("mcp__", "mcp____x") has
// nothing to govern and is not ok.
func mcpServerTool(toolName string) (server, tool string, ok bool) {
	if !strings.HasPrefix(toolName, mcpToolPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(toolName, mcpToolPrefix)
	if s, t, found := strings.Cut(rest, "__"); found {
		if s == "" {
			return "", "", false
		}
		return s, t, true
	}
	return rest, "", rest != ""
}

// controlTarget derives the (server, tool) a tool_result targets for control
// evaluation: an mcp__<server>__<tool> name resolves both; otherwise a tool_result
// carrying mcp_server_scope (a connector tool whose name is not MCP-prefixed)
// resolves the server from the scope and the tool from the BARE tool name — the
// same namespace the policy's per-tool keys use, so a per-tool override (an
// always_allow exception under a needs-approval connector, or a blocked tool under
// an allowed one) still resolves on scope-only events instead of falling back to
// the connector level (which would be fail-open for the blocked-tool case and a
// false HIGH for the excepted one). The scope is scrubbed (redact.Clean) exactly as
// edgeFromMCPServer does, so the lookup key matches the observed-edge ref. Anything
// else (a built-in tool, a skill) is not governed by connector controls and is not ok.
func controlTarget(ev coworkEvent) (server, tool string, ok bool) {
	if strings.HasPrefix(ev.toolName, mcpToolPrefix) {
		return mcpServerTool(ev.toolName)
	}
	if ev.mcpServerScope != "" {
		return redact.Clean(ev.mcpServerScope), ev.toolName, true
	}
	return "", "", false
}

// controlDriftFinding reports OBSERVED-vs-PERMITTED drift for an EXECUTED
// connector tool (a tool_result — the action provably ran) against its resolved
// control level:
//   - blocked: the console promises "the connector or tool is hidden", yet it
//     executed — hard drift between the authored floor and the live console (or a
//     role broader than the declared floor). HIGH.
//   - needs_approval executed with an AUTOMATIC decision_source (config/hook): the
//     human gate the policy demands did not happen. HIGH.
//   - needs_approval with a manual user_* decision, or always_allow: the control
//     worked — no finding (ok=false).
//
// The severity is High in both drift arms because each is an un-gated tool
// execution the org's authored policy forbids: OWASP Agentic ASI02 (tool misuse)
// and OWASP LLM06:2025 (excessive agency). Detail (session|server|tool|level|
// decision_source|prompt.id) rides as a hash only (docs/SECURITY-HARDENING.md). ok=false when
// there is no session to attribute.
func controlDriftFinding(ev coworkEvent, level ControlLevel, server, tool string) (model.FindingReport, bool) {
	if ev.sessionID == "" {
		return model.FindingReport{}, false
	}
	target := server
	if tool != "" {
		target = server + "/" + tool
	}
	var title string
	switch {
	case level == ControlBlocked:
		title = "Cowork connector control drift: blocked connector/tool executed: " + target
	case level == ControlNeedsApproval && isAutoApproved(ev.decisionSource):
		title = "Cowork connector control drift: needs-approval connector/tool ran auto-approved (" +
			ev.decisionSource + "): " + target
	default:
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingKindControlDrift,
		Severity:    model.SeverityHigh,
		SubjectKind: originSession,
		SubjectRef:  ev.sessionID,
		Title:       title,
		DetailHash: redact.Hash(ev.sessionID + "|" + server + "|" + tool + "|" + string(level) +
			"|" + ev.decisionSource + "|" + ev.promptID),
		OccurredAt: ev.at,
		OWASPLLM:   []string{"LLM06:2025"},
		OWASPASI:   []string{"ASI02"},
	}, true
}
