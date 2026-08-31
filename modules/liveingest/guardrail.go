// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package liveingest

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// surfaceToolArgs mirrors the security module's Surface vocabulary
// (modules/security/guardrail.go: input|output|tool_args|tool_result). The text
// liveingest forwards on this surface is the connector's resolved tool-argument
// REFERENCE, so it is the tool_args surface. It is a string so liveingest need not
// import the AGPL security module's types.
const surfaceToolArgs = "tool_args"

// maxObservedLen bounds the redacted excerpt the producer puts on the bus. The
// connector already reduces a tool argument to a short redacted reference (a path,
// a sanitized URL, an MCP tool ref), so this is a defensive cap, not the primary
// redaction — the producer never carries an unbounded value (docs/SECURITY-HARDENING.md). The
// security consumer clamps again before inspecting.
const maxObservedLen = 2048

// EdgeObservation ResourceKind wire strings the connector emits (the contract;
// connectors/claude/resource.go). They are literals here, NOT a connector import: a
// module never imports a connector's internal package, and the wire vocabulary is the
// stable contract. Only RESOURCE-bearing tool surfaces carry an argument reference
// worth inspecting; the topology/attribution kinds (identity.*, mcp.server,
// a2a.agent, genai.agent) and the bare tool-usage fallback carry no resolved
// reference and are skipped. genai.tool is deliberately absent: its edge sets
// ResourceRef == ToolRef == the tool name (connectors/claude/genai.go), so it carries
// no distinct argument and would always be skipped anyway.
const (
	resKindFile      = "file"       // a filesystem path (Read/Write/Edit/…)
	resKindHTTP      = "http.url"   // a sanitized web endpoint (WebFetch)
	resKindShell     = "shell"      // a shell invocation (Bash; program name only)
	resKindMCPTool   = "mcp.tool"   // an observed MCP tool invocation (server/tool ref)
	resKindAgentTask = "agent.task" // a delegated subagent task (Agent/Task tool, subagent_type)
)

// observedToolArgs returns the redacted tool-argument REFERENCE to inspect for an
// edge, and ok=false for the edges that carry no reference: a non-session origin, a
// topology/attribution edge (identity.*, mcp.server, a2a.agent, genai.agent), the
// bare tool-usage fallback (claude.tool, whose ref is just the tool name), or any
// edge whose ref is empty or equal to the tool name (no resolved detail).
//
// The ref is ALREADY redacted by the connector at the true origin (resource.go,
// internal/redact) and is a resource IDENTIFIER, not the raw argument: a sanitized
// file path, a URL host+path (no query/credentials), a Bash PROGRAM name (args
// dropped), a subagent_type, or a server/tool ref. The argument CONTENT is stripped
// at the connector and never reaches the bus. Realistic detections on this surface
// are therefore PII or a secret embedded in a reference (e.g. an email in a path) and
// anomalous/sensitive-resource patterns — NOT prompt-injection or jailbreak, which
// need the stripped argument text (the same minimal-data limit that keeps the
// input/output/tool_result content surfaces unavailable in-process; docs/SECURITY-HARDENING.md).
func observedToolArgs(edge model.EdgeObservation) (string, bool) {
	if edge.OriginKind != "session" {
		return "", false
	}
	switch edge.ResourceKind {
	case resKindFile, resKindHTTP, resKindShell, resKindMCPTool, resKindAgentTask:
	default:
		return "", false
	}
	ref := strings.TrimSpace(edge.ResourceRef)
	if ref == "" || ref == edge.ToolRef {
		// No resolved resource detail (a usage edge whose ref is the tool name).
		return "", false
	}
	return ref, true
}

// onObservedEdge is the producer (opt-in only). It forwards the redacted
// tool-argument reference of a resource-bearing edge as an event.ObservedText on the
// tool_args surface and publishes it as guardrail.observed for the security detector
// chain (modules/security/observed.go), so the detectors run on real estate traffic
// automatically — without a caller POSTing to /guardrails/inspect. It is DETECTIVE
// and minimal-data: it forwards only the already-redacted, bounded REFERENCE (see
// observedToolArgs for what realistically trips on it), never raw prompt/output/tool
// content, and moves no raw payload onto the bus. A clean ref produces no event from
// security (no detector trips); the security finding carries a one-way DetailHash,
// never the excerpt (proven in the tests).
func (m *Module) onObservedEdge(ctx context.Context, tenant string, edge model.EdgeObservation) error {
	if m.host == nil {
		return nil
	}
	ref, ok := observedToolArgs(edge)
	if !ok {
		return nil
	}
	bounded := clamp(ref, maxObservedLen)
	ot := event.ObservedText{
		Surface:     surfaceToolArgs,
		Text:        bounded,
		SessionRef:  edge.OriginRef,
		ResourceRef: bounded,
		// AgentRef is left empty: the session is the finding's subject and the agent
		// attribution rides a separate identity.agent edge (sessions writes agent_ref).
	}
	return m.host.Publish(ctx, event.GuardrailObserved(tenant, "module:"+Namespace, ot))
}

// clamp truncates s to at most n bytes on a rune boundary. It trims trailing bytes
// until the prefix is valid UTF-8, which drops any rune the byte cut left incomplete
// (its lead byte too, not only continuation bytes), so the bus never carries an
// invalid rune. A rune is at most 4 bytes, so this trims ≤3 extra bytes.
func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	b := s[:n]
	for len(b) > 0 && !utf8.ValidString(b) {
		b = b[:len(b)-1]
	}
	return b
}
