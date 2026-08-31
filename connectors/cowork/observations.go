// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// originSession is the OriginKind for every access edge this connector emits: the
// Cowork session, the operational unit and the finest non-shared attribution
// available from the telemetry (ARCHITECTURE.md, README.md module II).
const originSession = "session"

// providerAnthropic is the CostSample provider for Cowork's cost telemetry. The
// model attribute distinguishes Opus/Sonnet/Haiku; the gateway SURFACE is resolved
// by gatewayForModel from the model id (Cowork runs on the Anthropic-operated
// surface; a Bedrock-served deployment is detected from the inference-profile id).
const providerAnthropic = "anthropic"

// Finding kinds this connector emits. They are open strings the ledger/SIEM/
// dashboards group on; they intentionally reuse the claude connector's vocabulary
// where the meaning is identical (health/policy_decision) and add the Cowork-
// specific governance signal (auto_approved_action).
const (
	// findingKindAutoApproved marks an AI-initiated, HIGH-RISK action that ran
	// AUTOMATICALLY — approved by a config rule or a hook (decision_source ∈
	// {config, hook}), with no human in the loop. This is the central Cowork
	// governance signal (A1): for a knowledge-work agent acting on a
	// shared workspace, an auto-approved write/shell is exactly what an operator that
	// GOVERNS Cowork must see, the inverse of a denied tool decision.
	findingKindAutoApproved = "auto_approved_action"
	// findingKindPolicyDecision marks an attempted-but-refused tool call (a denied
	// tool_decision) — visibility into an agent pushing beyond its grants.
	findingKindPolicyDecision = "policy_decision"
	// findingKindHealth marks a Cowork API failure (api_error).
	findingKindHealth = "health"
	// findingKindSelfAudit is the once-per-Gather content-capture posture record.
	findingKindSelfAudit = "self_audit"
)

// edgeFromTool builds the access edge for a Cowork tool invocation: a session
// touched a resource, with the R/RW mode inferred from the tool and the resource
// reference redacted. Confidence is attributed because the origin is a concrete
// session. It returns false when there is no session or tool to attribute to.
func edgeFromTool(sessionID, toolName string, input map[string]any, at time.Time) (model.EdgeObservation, bool) {
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
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      toolName,
		ObservedAt:   at,
	}, true
}

// edgeFromMCPServer builds the topology edge for a Cowork MCP connector call: a
// session used an MCP server, named by the tool_result's mcp_server_scope. It is
// distinct from the mcp.tool access edge (edgeFromTool): this is the connector/
// server the session reached, the inventory/governance surface the per-tool
// connector controls govern. Mode is Unknown (using a connector is not itself an
// R/RW access). It returns false unless the event named an MCP server scope.
func edgeFromMCPServer(ev coworkEvent) (model.EdgeObservation, bool) {
	if ev.sessionID == "" || ev.mcpServerScope == "" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   originSession,
		OriginRef:    ev.sessionID,
		ResourceKind: resMCPServer,
		ResourceRef:  redact.Clean(ev.mcpServerScope),
		Mode:         model.ModeUnknown,
		Source:       model.SignalOTEL,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   ev.at,
	}, true
}

// costFromAPIRequest builds a CostSample from a Cowork api_request event. It
// returns false unless the event carried a cost figure, so a request without cost
// does not emit a zero-cost sample. The Actor is the shared account ref so FinOps
// can attribute Cowork seat spend per user (the subscription billing source: an
// estimated, direct-surface sample with an Actor and no API dimension is what
// claude-api's BillingSourceOf classifies as subscription). Provenance is estimated
// (Cowork's cost_usd is the agent's own per-request figure, not a billed invoice).
func costFromAPIRequest(ev coworkEvent, configuredGateway model.Gateway) (model.CostSample, bool) {
	if ev.name != evtAPIRequest || !ev.hasCost {
		return model.CostSample{}, false
	}
	return model.CostSample{
		ProviderRef:           providerAnthropic,
		ModelRef:              ev.model,
		SessionRef:            ev.sessionID,
		Actor:                 AccountRef(ev.accountID, ev.accountUUID, ev.userID),
		InputTokens:           ev.inputTokens + ev.cacheReadTokens + ev.cacheCreationTokens,
		OutputTokens:          ev.outputTokens,
		CostMicroUSD:          microUSD(ev.costUSD),
		OccurredAt:            ev.at,
		CacheReadTokens:       ev.cacheReadTokens,
		CacheCreation5mTokens: ev.cacheCreationTokens,
		Gateway:               gatewayForModel(configuredGateway, ev.model),
		Provenance:            model.ProvenanceEstimated,
	}, true
}

// microUSD converts a USD float to integer micro-units (millionths of a dollar),
// rounding to nearest and clamping non-positive/NaN/Inf to zero.
func microUSD(usd float64) int64 {
	if usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0
	}
	return int64(math.Round(usd * 1_000_000))
}

// gatewayForModel resolves the deployment surface that served a Cowork call. A
// Bedrock cross-region inference-profile id is hard evidence (it appears only on
// Bedrock) so it wins over the configured value; otherwise the operator's declared
// surface applies, defaulting to direct.
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
// inference-profile id (a geographic prefix + "anthropic.").
func hasBedrockGeoPrefix(modelID string) bool {
	for _, p := range []string{"us.anthropic.", "eu.anthropic.", "apac.anthropic.", "global.anthropic."} {
		if strings.HasPrefix(modelID, p) {
			return true
		}
	}
	return false
}

// findingFromAutoApprovedAction reports a HIGH-RISK Cowork action that ran with
// AUTOMATIC approval (decision_source ∈ {config, hook}) — the central governance
// signal. It keys off the EXECUTED action (tool_result), so the resource
// the action touched is known and the finding proves the action actually ran (not
// merely that a decision was made). It returns false for: a non-tool_result event;
// a manually-approved action (a human decided — that is the guardrail working, not
// a finding); a low-risk action (a read auto-approval is not a governance gap); or
// when there is no session/tool to attribute. The resource ref is already redacted
// (resourceFromTool); the deciding source rides the (hashed) detail.
func findingFromAutoApprovedAction(ev coworkEvent) (model.FindingReport, bool) {
	if ev.name != evtToolResult || ev.sessionID == "" || ev.toolName == "" {
		return model.FindingReport{}, false
	}
	if !isAutoApproved(ev.decisionSource) {
		return model.FindingReport{}, false
	}
	kind, ref, mode := resourceFromTool(ev.toolName, ev.toolInput)
	if !isHighRiskTool(kind, mode) {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        findingKindAutoApproved,
		Severity:    model.SeverityHigh,
		SubjectKind: originSession,
		SubjectRef:  ev.sessionID,
		Title:       "Cowork auto-approved high-risk action (" + ev.decisionSource + "): " + ev.toolName + " → " + kind,
		DetailHash:  redact.Hash(ev.sessionID + "|" + ev.toolName + "|" + kind + "|" + ref + "|" + string(mode) + "|" + ev.decisionSource + "|" + ev.promptID),
		OccurredAt:  ev.at,
		// An auto-approved write/exec by an autonomous agent is the canonical "excessive
		// agency" failure mode (OWASP LLM06; OWASP Agentic ASI04 — excessive autonomy).
		OWASPLLM: []string{"LLM06:2025"},
		OWASPASI: []string{"ASI04"},
	}, true
}

// findingFromToolDecision reports a DENIED Cowork tool decision (decision=reject)
// as a security-relevant signal: an agent ATTEMPTED a tool and was blocked. Unlike
// an accepted call (an observed access), a denial is an attempted-but-refused
// access — visibility into an agent pushing beyond its grants. The deciding source
// rides the title (config/hook = an automatic policy block; user_* = a human
// reject). It returns false for an accepted decision or when there is no session.
func findingFromToolDecision(ev coworkEvent) (model.FindingReport, bool) {
	if ev.name != evtToolDecision || ev.sessionID == "" || ev.decision != decisionReject {
		return model.FindingReport{}, false
	}
	tool := firstNonEmpty(ev.toolName, "unknown")
	src := firstNonEmpty(ev.decisionSource, "unknown")
	return model.FindingReport{
		Kind:        findingKindPolicyDecision,
		Severity:    model.SeverityLow,
		SubjectKind: originSession,
		SubjectRef:  ev.sessionID,
		Title:       "Cowork tool call denied (" + src + "): " + tool,
		DetailHash:  redact.Hash(ev.sessionID + "|" + tool + "|" + ev.decisionSource + "|" + ev.promptID),
		OccurredAt:  ev.at,
	}, true
}

// findingFromAPIError reports a Cowork API failure as a health finding. A single
// api_error is low severity; the raw provider error message is hashed, never
// surfaced (minimal-data, docs/SECURITY-HARDENING.md). It returns false when there is no session.
func findingFromAPIError(ev coworkEvent) (model.FindingReport, bool) {
	if ev.name != evtAPIError || ev.sessionID == "" {
		return model.FindingReport{}, false
	}
	title := fmt.Sprintf("Cowork API error (status %d)", ev.statusCode)
	if ev.model != "" {
		title += ": " + ev.model
	}
	return model.FindingReport{
		Kind:        findingKindHealth,
		Severity:    model.SeverityLow,
		SubjectKind: originSession,
		SubjectRef:  ev.sessionID,
		Title:       title,
		DetailHash:  redact.Hash(ev.sessionID + "|api_error|" + ev.model + "|" + ev.errMessage),
		OccurredAt:  ev.at,
	}, true
}

// selfAuditFinding records the connector's content-capture posture once per Gather,
// before any telemetry is processed, so an auditor can prove what the connector was
// permitted to retain. Cowork ALWAYS sends prompt/tool content in its events; this
// finding states whether the connector discarded it (the default, structural-only
// posture) or — if an operator opted in — retained it. Info severity.
func selfAuditFinding(contentCapture bool, at time.Time) model.FindingReport {
	posture := "structural-only (prompt/tool content discarded)"
	if contentCapture {
		posture = "content-capture ENABLED (operator opt-in)"
	}
	return model.FindingReport{
		Kind:        findingKindSelfAudit,
		Severity:    model.SeverityInfo,
		SubjectKind: "cowork_connector",
		SubjectRef:  Name,
		Title:       "Cowork telemetry content-capture posture: " + posture,
		DetailHash:  redact.Hash(Name + "|content_capture=" + fmt.Sprintf("%t", contentCapture)),
		OccurredAt:  at,
	}
}
