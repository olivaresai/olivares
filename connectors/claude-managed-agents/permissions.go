// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"fmt"
	"sort"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// permissions.go models the CMA permission surface in both directions of the
// PERMITTED-vs-OBSERVED diff:
//
//   - PERMITTED: an agent's tools[] is a DECLARED, ENUMERABLE grant — which built-in,
//     MCP and custom tools the agent may use and under which permission_policy
//     (always_allow | always_ask; there is NO always_deny — a deny is expressed at
//     confirmation time). agentToolEdges expands it into model.SignalPolicy edges, the
//     permitted side modules/access-map diffs observed tool use against. The multiagent
//     roster (which sub-agents a coordinator may spawn) is likewise a declared grant —
//     rosterEdges.
//   - OBSERVED: a session paused on requires_action is the always_ask gate firing.
//     toolConfirmationFinding routes it as a pending-approval finding — the connector
//     OBSERVES + ROUTES, it never decides or posts the confirmation. The AGPL
//     composition root raises the governed approval through the HITL bridge
//     (gateOnce) and, only on approval, an AGPL-side actuator posts
//     user.tool_confirmation back; an Apache connector that cannot import /core must
//     not hold that privileged actuation (doctrine: the bridge proposes
//     decides).
//
// Correction to the model: the session resource carries no stop_reason, so
// the requires_action signal is recovered from the session event list
// (fetchAwaitingConfirmation), event-driven off the session.status_idled webhook.

// toolConfirmationFinding reports a session paused on an always_ask permission policy:
// a human must allow/deny each blocking tool call (user.tool_confirmation). blocking is
// the stop_reason.event_ids count recovered from the event list. It is the data source
// of the managed-agents HITL queue (ANT2-14: kind=governance +
// subject_kind=anthropic.managed_agent).
func toolConfirmationFinding(sessionID string, blocking int, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingGovernance,
		Severity:    model.SeverityLow,
		SubjectKind: kindManagedAgent,
		SubjectRef:  labelRef(sessionID, "session"),
		Title:       "CMA tool call awaiting human confirmation (always_ask / HITL)",
		DetailHash:  redact.Hash(fmt.Sprintf("session=%s stop_reason=requires_action blocking_events=%d; an always_ask permission policy paused the session pending user.tool_confirmation (route via the HITL bridge)", sessionID, blocking)),
		OWASPASI:    []string{asiIdentityAbuse},
		OccurredAt:  at,
	}
}

// permissionPolicyEdge records that an always_ask permission policy is actively gating
// a session (emitted alongside toolConfirmationFinding), so module III sees that the
// session's tool use is governed rather than free. It is an OBSERVATION of the gate
// firing — the declared policy itself travels as agentToolEdges.
func permissionPolicyEdge(sessionID string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originSession,
		OriginRef:    redact.Clean(sessionID),
		ResourceKind: kindPermPolicy,
		ResourceRef:  policyAlwaysAsk,
		Mode:         model.ModeRead,
		Source:       model.SignalCMA,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}
}

// effectiveToolConfig resolves a tool's enabled/policy from a toolset's default_config
// plus its configs[] override. A nil Enabled means "absent" ⇒ available (the toolset is
// declared on the agent); an empty policy type inherits the toolset default.
func effectiveToolConfig(def ToolConfig, override *NamedToolConfig) (enabled bool, policy string) {
	enabled = def.Enabled == nil || *def.Enabled
	policy = def.PermissionPolicy.Type
	if override != nil {
		if override.Enabled != nil {
			enabled = *override.Enabled
		}
		if override.PermissionPolicy.Type != "" {
			policy = override.PermissionPolicy.Type
		}
	}
	return enabled, policy
}

// agentToolEdges expands an agent's declared tools[] into PERMITTED (SignalPolicy)
// edges — the enumerable grant set the access map diffs observed use against:
//
//   - agent_toolset_20260401: one edge per built-in tool name (the verbatim 8-name
//     enum), kind anthropic.agent_tool — the set IS enumerable, so the expansion is
//     complete, never partial.
//   - mcp_toolset: one mcp.tool edge per EXPLICIT configs[] entry ("<server>/<tool>",
//     byte-matching the observed mcp.tool shape so the diff reconciles) plus one
//     mcp.server edge when the toolset's default is enabled (the server as a whole is
//     reachable; its full tool list is NOT enumerable from the agent, so per-tool edges
//     are emitted only for explicitly configured tools — a partial set is labeled by
//     the server-level edge, never silently passed off as complete; mirrors the
//     claude-api CLA-09 stance).
//   - custom: one anthropic.agent_tool edge per declared custom tool.
//
// A disabled tool/toolset emits nothing. ToolRef carries the tool's leaf name; the
// resolved permission policy is part of the grant's identity and rides the edge's
// resource pairing (an always_ask grant is still a grant — the GATE observation is
// permissionPolicyEdge). Deterministic order for stable re-emission.
func agentToolEdges(a Agent, at time.Time) []model.EdgeObservation {
	agentRef := redact.Clean(a.ID)
	if agentRef == "" {
		return nil
	}
	edge := func(kind, ref, tool string) model.EdgeObservation {
		return model.EdgeObservation{
			OriginKind:   originAgent,
			OriginRef:    agentRef,
			ResourceKind: kind,
			ResourceRef:  ref,
			Mode:         model.ModeUnknown,
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ToolRef:      tool,
			ObservedAt:   at,
		}
	}
	var out []model.EdgeObservation
	for _, t := range a.Tools {
		switch t.Type {
		case toolsetBuiltin:
			overrides := overrideIndex(t.Configs)
			for _, name := range agentToolsetBuiltins {
				if enabled, _ := effectiveToolConfig(t.DefaultConfig, overrides[name]); enabled {
					out = append(out, edge(kindAgentTool, name, name))
				}
			}
		case toolsetMCP:
			server := redact.Clean(t.MCPServerName)
			if server == "" {
				continue
			}
			if enabled := t.DefaultConfig.Enabled == nil || *t.DefaultConfig.Enabled; enabled {
				out = append(out, edge(mcpServerResourceKind, server, ""))
			}
			names := make([]string, 0, len(t.Configs))
			overrides := overrideIndex(t.Configs)
			for name := range overrides {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if enabled, _ := effectiveToolConfig(t.DefaultConfig, overrides[name]); enabled {
					out = append(out, edge("mcp.tool", server+"/"+redact.Clean(name), name))
				}
			}
		case toolsetCustom:
			if name := redact.Clean(t.Name); name != "" {
				out = append(out, edge(kindAgentTool, name, name))
			}
		}
	}
	return out
}

// overrideIndex indexes a toolset's configs[] by tool name (last entry wins).
func overrideIndex(configs []NamedToolConfig) map[string]*NamedToolConfig {
	if len(configs) == 0 {
		return nil
	}
	idx := make(map[string]*NamedToolConfig, len(configs))
	for i := range configs {
		if configs[i].Name != "" {
			idx[configs[i].Name] = &configs[i]
		}
	}
	return idx
}

// rosterEdges expands a coordinator agent's multiagent roster into PERMITTED edges:
// which sub-agent definitions this agent may spawn as threads (max 20 unique agents,
// depth 1). The OBSERVED counterpart is the thread topology (threadEdge). A {type:
// "self"} entry is the agent itself — no edge (a self-loop adds nothing to the diff).
func rosterEdges(a Agent, at time.Time) []model.EdgeObservation {
	agentRef := redact.Clean(a.ID)
	if agentRef == "" || a.Multiagent == nil {
		return nil
	}
	var out []model.EdgeObservation
	for _, m := range a.Multiagent.Agents {
		if m.Type != "agent" || m.ID == "" {
			continue
		}
		out = append(out, model.EdgeObservation{
			OriginKind:   originAgent,
			OriginRef:    agentRef,
			ResourceKind: kindAgentDef,
			ResourceRef:  redact.Clean(m.ID),
			Mode:         model.ModeUnknown,
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   at,
		})
	}
	return out
}
