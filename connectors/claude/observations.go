// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// originSession is the OriginKind for every edge this connector emits: the
// Claude Code session, the operational unit and the finest non-shared
// attribution available from the telemetry (ARCHITECTURE.md, README.md module II).
const originSession = "session"

// originAgent is the OriginKind of the subagent-hierarchy edge: the
// PARENT agent instance that spawned a subagent. It is the SDK's documented
// "agent" origin, so inventory materializes the parent as an Agent entity.
const originAgent = "agent"

// resMCPServer is the ResourceKind for an MCP server an agent connected to.
const resMCPServer = "mcp.server"

// providerAnthropic is the CostSample provider for Claude Code's own cooperative
// cost telemetry. The model attribute distinguishes Opus/Sonnet/Haiku; the gateway
// SURFACE (direct/Bedrock/Vertex/Foundry) is resolved by gatewayForModel (CLA-11):
// the operator's declared `gateway` config, or auto-detected from the model id for
// Bedrock inference-profile ids.
const providerAnthropic = "anthropic"

// gatewayForModel resolves the deployment surface that served a cooperative Claude
// Code call (CLA-11). Bedrock inference-profile ids are hard evidence — they appear
// ONLY on Bedrock — so they win over the configured value: a geo-prefixed CRIS id
// (us./eu./apac./global.anthropic.*) is bedrock-legacy, a bare anthropic.* id is
// bedrock-mantle. With no Bedrock evidence in the id, the operator's declared surface
// applies (Vertex/Foundry cannot be told apart from a bare claude-* id), defaulting
// to direct.
func gatewayForModel(configured model.Gateway, modelID string) model.Gateway {
	switch {
	case hasBedrockGeoPrefix(modelID):
		return model.GatewayBedrockLegacy
	case strings.HasPrefix(modelID, "anthropic."):
		return model.GatewayBedrockMantle
	}
	if configured != "" {
		return configured
	}
	return model.GatewayDirect
}

// hasBedrockGeoPrefix reports whether modelID is a Bedrock cross-region
// inference-profile id (a geographic prefix + "anthropic."), e.g.
// "us.anthropic.claude-opus-4-8" (verified vs AWS geographic-cross-region-inference
// docs, jun-2026).
func hasBedrockGeoPrefix(modelID string) bool {
	for _, p := range []string{"us.anthropic.", "eu.anthropic.", "apac.anthropic.", "global.anthropic."} {
		if strings.HasPrefix(modelID, p) {
			return true
		}
	}
	return false
}

// edgeFromTool builds the access edge for a resolved tool invocation: a session
// touched a resource, with the R/RW mode inferred from the tool and the resource
// reference redacted. confidence is attributed because the origin is a concrete
// session (not a shared account); it is independent of whether the specific
// resource is known. It returns false when there is no session to attribute to.
func edgeFromTool(sessionID, toolName string, input map[string]any, at time.Time, confidence model.Confidence) (model.EdgeObservation, bool) {
	if sessionID == "" || toolName == "" {
		return model.EdgeObservation{}, false
	}
	kind, ref, mode := resourceFromTool(toolName, input)
	return model.EdgeObservation{
		OriginKind:   originSession,
		OriginRef:    sessionID,
		ResourceKind: kind,
		ResourceRef:  ref,
		Mode:         mode,
		Source:       model.SignalOTEL,
		Confidence:   confidence,
		ToolRef:      toolName,
		ObservedAt:   at,
	}, true
}

// costFromEvent builds a CostSample from a claude_code.api_request event. It
// returns false unless the event carried a cost figure, so a request event
// without cost (e.g. an error) does not emit a zero-cost sample. configuredGateway is
// the operator's declared fleet surface (empty = auto/direct); the per-call surface
// is resolved by gatewayForModel so Bedrock-served calls are tagged even when the
// fleet config is left at the default.
func costFromEvent(ev claudeEvent, configuredGateway model.Gateway) (model.CostSample, bool) {
	if ev.name != evtAPIRequest || !ev.hasCost {
		return model.CostSample{}, false
	}
	// Claude Code's OTEL api_request reports input_tokens as the UNCACHED input, with
	// cache reads/creations separately (the standard gen_ai convention), so InputTokens
	// (the contract's TOTAL input volume) folds all three. The creation count is
	// untiered on this feed; it is carried as the 5-minute (default) TTL tier — the
	// precise 1h/5m split is only available from the Admin usage report. Cost is the
	// provider's own per-request figure, so provenance is estimated.
	return model.CostSample{
		ProviderRef:           providerAnthropic,
		ModelRef:              ev.model,
		SessionRef:            ev.sessionID,
		InputTokens:           ev.inputTokens + ev.cacheReadTokens + ev.cacheCreationTokens,
		OutputTokens:          ev.outputTokens,
		CostMicroUSD:          microUSD(ev.costUSD),
		OccurredAt:            ev.at,
		CacheReadTokens:       ev.cacheReadTokens,
		CacheCreation5mTokens: ev.cacheCreationTokens,
		Gateway:               gatewayForModel(configuredGateway, ev.model),
		Provenance:            model.ProvenanceEstimated,
		// the operator's allowlisted OTEL_RESOURCE_ATTRIBUTES ride each cost
		// sample so FinOps can slice spend by the org's own dimensions (team,
		// project, cost_center) — the documented enterprise use of resource
		// attributes (2.1.161). nil when the resource_labels allowlist is off.
		Labels: ev.labels,
	}, true
}

// subagentEdges builds the access-graph facts for one subagent identity carried
// by a claude_code.llm_request/tool trace span (agent_id/parent_agent_id
// are span-only — 2.1.139/2.1.145):
//
//   - session → identity.subagent (membership: the subagent INSTANCE acted under
//     the session; agent_id absent = the main session = no edge — the caller
//     guards that);
//   - parent agent → identity.subagent (hierarchy: WHO SPAWNED IT), only when
//     parent_agent_id is present. When absent, the spawner is the main session
//     per the verified semantics, which the membership edge already captures.
//
// Both edges are ModeUnknown topology links (a hierarchy is not an R/RW access),
// SignalOTEL, ConfidenceAttributed — the same posture as identity edges.
func subagentEdges(sessionID, agentID, parentAgentID string, at time.Time) []model.EdgeObservation {
	if sessionID == "" || agentID == "" {
		return nil
	}
	out := []model.EdgeObservation{{
		OriginKind:   originSession,
		OriginRef:    sessionID,
		ResourceKind: resIdentitySubagent,
		ResourceRef:  agentID,
		Mode:         model.ModeUnknown,
		Source:       model.SignalOTEL,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}}
	if parentAgentID != "" {
		out = append(out, model.EdgeObservation{
			OriginKind:   originAgent,
			OriginRef:    parentAgentID,
			ResourceKind: resIdentitySubagent,
			ResourceRef:  agentID,
			Mode:         model.ModeUnknown,
			Source:       model.SignalOTEL,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   at,
		})
	}
	return out
}

// microUSD converts a USD float to integer micro-units (millionths of a dollar),
// rounding to nearest and clamping negatives to zero (cost is never negative).
func microUSD(usd float64) int64 {
	if usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0
	}
	return int64(math.Round(usd * 1_000_000))
}

// edgeFromMCPConnection builds the edge for an observed MCP server connection: a
// session uses an MCP server (topology/inventory; README.md modules I, V). The mode
// is unknown — a connection is not itself an R/RW access. It returns false unless
// the server can be named (Claude Code redacts the server name unless
// OTEL_LOG_TOOL_DETAILS=1) and the connection actually succeeded.
func edgeFromMCPConnection(ev claudeEvent) (model.EdgeObservation, bool) {
	if ev.name != evtMCPConnection || ev.sessionID == "" || ev.mcpServer == "" {
		return model.EdgeObservation{}, false
	}
	if ev.mcpStatus != "" && ev.mcpStatus != "connected" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   originSession,
		OriginRef:    ev.sessionID,
		ResourceKind: resMCPServer,
		ResourceRef:  ev.mcpServer,
		Mode:         model.ModeUnknown,
		Source:       model.SignalOTEL,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   ev.at,
	}, true
}

// findingFromToolDecision reports a DENIED Claude Code tool decision as a
// security-relevant signal (docs/SECURITY-HARDENING.md, §8): an agent ATTEMPTED a tool and was
// blocked (by config/hook/user). Unlike an accepted call (an observed access), a
// denial is an attempted-but-refused access — visibility into an agent pushing
// beyond its grants, the kind of signal a "permitted vs observed" product must not
// drop. It is emitted as a low-severity policy finding with the deciding source;
// modeling the denial as a distinct DENIED dimension on the access graph is a
// tracked enhancement (it needs an EdgeObservation contract change). It returns
// false for an accepted decision (the guardrail working is not a finding) or when
// there is no session to attribute to.
func findingFromToolDecision(ev claudeEvent) (model.FindingReport, bool) {
	if ev.name != evtToolDecision || ev.sessionID == "" || ev.decision != "reject" {
		return model.FindingReport{}, false
	}
	tool := ev.toolName
	if tool == "" {
		tool = "unknown"
	}
	src := ev.decisionSource
	if src == "" {
		src = "unknown"
	}
	title := "agent tool call denied (" + src + "): " + tool
	// since 2.1.157 the tool_decision event carries tool_parameters (under
	// OTEL_LOG_TOOL_DETAILS=1) precisely so a consumer can SEE WHICH COMMAND was
	// rejected. The same redaction pipeline as the access edges applies
	// (resourceFromTool sanitizes paths/URLs and reduces a shell command to its
	// program token), so the title carries a sanitized ref, never raw arguments —
	// and the hash distinguishes distinct denials of the same tool. Without
	// detail the finding is byte-identical to the pre-2.1.157 shape.
	kind, ref, _ := resourceFromTool(ev.toolName, ev.toolInput)
	detail := ev.sessionID + "|" + tool + "|" + ev.decisionSource
	if kind != resTool && ref != "" {
		title += " -> " + kind + ":" + ref
		detail += "|" + kind + "|" + ref
	}
	return model.FindingReport{
		Kind:        "policy_decision",
		Severity:    model.SeverityLow,
		SubjectKind: originSession,
		SubjectRef:  ev.sessionID,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  ev.at,
	}, true
}

// findingKindPolicyChange marks a governance-relevant Claude Code configuration
// change (a permission-mode transition). findingKindAuth marks an authentication
// state change. Both are events Anthropic documents as SIEM-forwarded for a
// per-user audit trail (OBS-05); routing them through a FindingReport puts them
// on the tamper-evident ledger and the SIEM export path (docs/SECURITY-HARDENING.md, §6).
const (
	findingKindPolicyChange = "policy_change"
	findingKindAuth         = "auth"
	findingKindHealth       = "health"
	// findingKindSupplyChain marks a session pulling in executable surface (a plugin
	// install or a skill activation): a supply-chain/governance signal (ANT2-09).
	findingKindSupplyChain = "supply_chain"
	// findingKindForensic marks a forensic-continuity signal (e.g. a context
	// compaction, after which the transcript is no longer whole) (ANT2-09).
	findingKindForensic = "forensic"
)

// findingFromPluginInstalled reports a Claude Code plugin install as a supply-chain
// governance signal (ANT2-09): a session pulled in executable surface. The plugin
// name and the query source ride the detail (hashed); the named agent/effort attribute
// the WHO. It returns false when there is no session or plugin to attribute.
func findingFromPluginInstalled(ev claudeEvent) (model.FindingReport, bool) {
	if ev.name != evtPluginInstalled || ev.sessionID == "" || ev.pluginName == "" {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingKindSupplyChain,
		Severity:    model.SeverityMedium,
		SubjectKind: originSession,
		SubjectRef:  ev.sessionID,
		Title:       "Claude Code plugin installed: " + ev.pluginName,
		DetailHash:  redact.Hash(ev.sessionID + "|plugin|" + ev.pluginName + "|" + ev.querySource + "|" + ev.agentName),
		OccurredAt:  ev.at,
	}, true
}

// findingFromSkillActivated reports a Claude Code skill activation as a supply-chain
// governance signal (ANT2-09): a skill (workspace-wide, executable scripts) ran in a
// session. It returns false when there is no session or skill to attribute.
func findingFromSkillActivated(ev claudeEvent) (model.FindingReport, bool) {
	if ev.name != evtSkillActivated || ev.sessionID == "" || ev.skillName == "" {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingKindSupplyChain,
		Severity:    model.SeverityInfo,
		SubjectKind: originSession,
		SubjectRef:  ev.sessionID,
		Title:       "Claude Code skill activated: " + ev.skillName,
		DetailHash:  redact.Hash(ev.sessionID + "|skill|" + ev.skillName + "|" + ev.querySource),
		OccurredAt:  ev.at,
	}, true
}

// findingFromCompaction reports a context compaction as a forensic-continuity signal
// (ANT2-09): after a compaction the transcript is summarized, so a later forensic read
// of the session must account for the lost detail. Info severity. It returns false
// when there is no session.
func findingFromCompaction(ev claudeEvent) (model.FindingReport, bool) {
	if ev.name != evtCompaction || ev.sessionID == "" {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingKindForensic,
		Severity:    model.SeverityInfo,
		SubjectKind: originSession,
		SubjectRef:  ev.sessionID,
		Title:       "Claude Code context compaction (transcript summarized)",
		DetailHash:  redact.Hash(ev.sessionID + "|compaction|" + ev.agentName),
		OccurredAt:  ev.at,
	}, true
}

// escalatedModes are the permission modes whose adoption weakens the guardrails a
// managed-settings policy may forbid (CLA-05: disableBypassPermissionsMode /
// disableAutoMode). A transition INTO one is the governance signal module VI/III
// care about — a developer reducing the friction the org may have mandated.
var escalatedModes = map[string]model.Severity{
	"bypassPermissions": model.SeverityHigh,
	"dontAsk":           model.SeverityMedium,
	"auto":              model.SeverityMedium,
	"acceptEdits":       model.SeverityMedium,
}

// findingFromPermissionMode reports a Claude Code permission-mode change as a
// governance finding. A move INTO a friction-reducing mode (bypassPermissions,
// auto, acceptEdits, dontAsk) is the escalation a control plane that GOVERNS
// Claude must see (it is exactly what managed-settings disableBypassPermissionsMode
// exists to prevent); a move back to plan/default is recorded at low severity. It
// returns false when there is no session to attribute to.
func findingFromPermissionMode(ev claudeEvent) (model.FindingReport, bool) {
	if ev.name != evtPermissionMode || ev.sessionID == "" || ev.toMode == "" {
		return model.FindingReport{}, false
	}
	sev := model.SeverityLow
	if s, ok := escalatedModes[ev.toMode]; ok {
		sev = s
	}
	trigger := ev.modeTrigger
	if trigger == "" {
		trigger = "unspecified"
	}
	return model.FindingReport{
		Kind:        findingKindPolicyChange,
		Severity:    sev,
		SubjectKind: originSession,
		SubjectRef:  ev.sessionID,
		Title:       "permission mode " + firstNonEmpty(ev.fromMode, "?") + " → " + ev.toMode + " (" + trigger + ")",
		DetailHash:  redact.Hash(ev.sessionID + "|" + ev.fromMode + "|" + ev.toMode + "|" + ev.modeTrigger),
		OccurredAt:  ev.at,
	}, true
}

// findingFromAuth reports a Claude Code authentication state change (/login,
// /logout). A FAILED auth is a security-relevant medium-severity signal; a
// successful one is a low-severity audit signal. The provider error text is never
// surfaced (it is folded into the hash only). It returns false when there is no
// action to attribute.
func findingFromAuth(ev claudeEvent) (model.FindingReport, bool) {
	if ev.name != evtAuth || ev.authAction == "" {
		return model.FindingReport{}, false
	}
	sev := model.SeverityLow
	title := "Claude auth " + ev.authAction
	if ev.success != nil && !*ev.success {
		sev = model.SeverityMedium
		title = "Claude auth " + ev.authAction + " failed"
	}
	if ev.authMethod != "" {
		title += " (" + ev.authMethod + ")"
	}
	subject := firstNonEmpty(ev.sessionID, "claude-auth")
	return model.FindingReport{
		Kind:        findingKindAuth,
		Severity:    sev,
		SubjectKind: originSession,
		SubjectRef:  subject,
		Title:       title,
		DetailHash:  redact.Hash(subject + "|" + ev.authAction + "|" + ev.authMethod),
		OccurredAt:  ev.at,
	}, true
}

// findingFromAPIError reports a Claude API failure as a health/forensic finding
// (README.md modules IX, XXII). A single api_error is low severity; retries-exhausted
// is a medium-severity reliability signal. The raw provider error message is hashed,
// never surfaced (minimal-data, docs/SECURITY-HARDENING.md). It returns false for other events or
// when there is no session.
func findingFromAPIError(ev claudeEvent) (model.FindingReport, bool) {
	if ev.sessionID == "" {
		return model.FindingReport{}, false
	}
	var sev model.Severity
	var title string
	switch ev.name {
	case evtAPIError:
		sev, title = model.SeverityLow, fmt.Sprintf("Claude API error (status %d)", ev.statusCode)
	case evtAPIRetriesGone:
		sev, title = model.SeverityMedium, fmt.Sprintf("Claude API retries exhausted after %d attempts (status %d)", ev.attempt, ev.statusCode)
	default:
		return model.FindingReport{}, false
	}
	if ev.model != "" {
		title += ": " + ev.model
	}
	return model.FindingReport{
		Kind:        findingKindHealth,
		Severity:    sev,
		SubjectKind: originSession,
		SubjectRef:  ev.sessionID,
		Title:       title,
		DetailHash:  redact.Hash(ev.sessionID + "|" + ev.name + "|" + ev.model + "|" + ev.errMessage),
		OccurredAt:  ev.at,
	}, true
}

// findingFromMCPConnection reports a failed or dropped MCP connection as a health
// finding (README.md module V, XXII). A connection failure is a low-severity signal
// on its own; the subject is the server when named, else the transport+scope.
func findingFromMCPConnection(ev claudeEvent) (model.FindingReport, bool) {
	if ev.name != evtMCPConnection {
		return model.FindingReport{}, false
	}
	switch ev.mcpStatus {
	case "failed", "disconnected":
	default:
		return model.FindingReport{}, false
	}
	subject := ev.mcpServer
	if subject == "" {
		subject = fmt.Sprintf("%s/%s", firstNonEmpty(ev.mcpTransport, "mcp"), firstNonEmpty(ev.mcpScope, "unknown"))
	}
	return model.FindingReport{
		Kind:        findingKindHealth,
		Severity:    model.SeverityLow,
		SubjectKind: resMCPServer,
		SubjectRef:  subject,
		Title:       "MCP server connection " + ev.mcpStatus,
		DetailHash:  redact.Hash(ev.sessionID + "|" + ev.mcpServer + "|" + ev.mcpErrorCode),
		OccurredAt:  ev.at,
	}, true
}
