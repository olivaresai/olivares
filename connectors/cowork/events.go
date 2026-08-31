// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// Cowork OTEL event names. Source: claude.com/docs/cowork/monitoring (jun-2026),
// cross-verified against the Claude Help Center article and independent ingest
// vendors. The official doc labels the FIVE events with BARE names; some vendors
// observe the same events on the wire carrying Claude Code's "claude_code."
// prefix (Cowork rides the same telemetry pipeline). The connector normalizes the
// name (canonicalEventName) so BOTH forms map — a real Cowork event is never
// missed. No source has ever shown a "cowork." prefix; it is stripped defensively.
const (
	evtUserPrompt   = "user_prompt"
	evtToolResult   = "tool_result"
	evtAPIRequest   = "api_request"
	evtAPIError     = "api_error"
	evtToolDecision = "tool_decision"
)

// eventNamePrefixes are the producer prefixes stripped to reach the canonical bare
// event name. "claude_code." is what ingest vendors observe on the Cowork wire;
// "cowork." is stripped defensively though no source emits it.
var eventNamePrefixes = []string{"claude_code.", "cowork."}

// serviceNameCowork is the resource attribute value that identifies Cowork (vs
// Claude Code's "claude-code"). The connector gates on it so a Claude Code record
// accidentally pointed at this receiver is not mis-attributed as Cowork.
const serviceNameCowork = "cowork"

// Standard / resource attribute keys this connector reads from the Cowork monitoring
// telemetry. ONLY keys actually consumed by parseLogRecord are declared (so the set
// can never over-claim a provenance for a key it ignores). user.email is deliberately
// NOT read into any observation — it is PII the minimal-data posture never retains.
const (
	attrServiceName    = "service.name"
	attrEventName      = "event.name"
	attrSessionID      = "session.id"
	attrOrgID          = "organization.id"
	attrUserID         = "user.id"
	attrAccountUUID    = "user.account_uuid"
	attrAccountID      = "user.account_id"
	attrPromptID       = "prompt.id"
	attrWorkspacePaths = "workspace.host_paths"
)

// Per-event attribute keys this connector reads (names from the Cowork monitoring doc).
const (
	// tool_result + tool_decision
	attrToolName       = "tool_name"
	attrSuccess        = "success"
	attrDecision       = "decision"        // tool_decision: "accept" | "reject"
	attrSource         = "source"          // tool_decision: config|hook|user_*
	attrDecisionSource = "decision_source" // tool_result: who approved (config|hook|user_*)
	attrToolInput      = "tool_input"
	attrToolParameters = "tool_parameters"
	attrMCPServerScope = "mcp_server_scope"
	// api_request
	attrModel       = "model"
	attrCostUSD     = "cost_usd"
	attrInputTokens = "input_tokens"
	attrOutTokens   = "output_tokens"
	attrCacheRead   = "cache_read_tokens"
	attrCacheCreate = "cache_creation_tokens"
	// api_error
	attrErrorMessage = "error"
	attrStatusCode   = "status_code"
	attrAttempt      = "attempt"
	// user_prompt — only the LENGTH is read; the prompt text ("prompt") is content
	// Cowork ALWAYS sends but the connector NEVER reads or stores (docs/SECURITY-HARDENING.md).
	attrPromptLength = "prompt_length"
)

// tool_decision outcomes and decision sources (verbatim enum from the Cowork doc).
const (
	decisionAccept = "accept"
	decisionReject = "reject"

	// srcConfig/srcHook are the AUTOMATIC (no human in the loop) approval sources: a
	// pre-configured permission rule or a programmatic hook decided. A "user_"-prefixed
	// source (user_permanent|user_temporary|user_abort|user_reject) is a MANUAL human
	// decision. isAutoApproved encodes this split — the manual-vs-automatic dimension
	// the Cowork governance signal hinges on.
	srcConfig     = "config"
	srcHook       = "hook"
	srcUserPrefix = "user_"
)

// isAutoApproved reports whether a decision source means the action was approved
// AUTOMATICALLY (config/hook), with no human in the loop. A "user_*" source is a
// manual human decision; an empty/unknown source is treated as NOT auto-approved
// (fail-safe: never claim an action was automated when the provenance is unknown).
func isAutoApproved(source string) bool {
	return source == srcConfig || source == srcHook
}

// isManualDecision reports whether a decision source is an explicit human action.
func isManualDecision(source string) bool {
	return strings.HasPrefix(source, srcUserPrefix)
}

// canonicalEventName strips any known producer prefix so a bare ("tool_result")
// and a prefixed ("claude_code.tool_result") name both reduce to the canonical
// bare event name this connector switches on.
func canonicalEventName(name string) string {
	for _, p := range eventNamePrefixes {
		if strings.HasPrefix(name, p) {
			return name[len(p):]
		}
	}
	return name
}

// coworkEvent is the connector's normalized view of one Cowork OTEL log record:
// the canonical event name, the time it occurred, the identity/account attribution
// (the spine of the OTEL↔Compliance correlation), and the union of per-event
// fields this connector maps. Absent fields are zero. The prompt/tool CONTENT is
// never carried here — only the redacted resource derived from it (resource.go).
type coworkEvent struct {
	name        string
	at          time.Time
	serviceName string

	// Identity / account attribution. accountUUID + accountID are the shared account
	// identifier Anthropic exposes for correlating Cowork OTEL with the Compliance
	// API (blog: "Shared User Account Identifier"). userEmail is intentionally absent.
	sessionID      string
	orgID          string
	userID         string
	accountUUID    string
	accountID      string
	promptID       string
	workspacePaths string

	// tool_result / tool_decision
	toolName       string
	success        *bool
	decision       string         // tool_decision: accept|reject
	decisionSource string         // who decided (config|hook|user_*); carried on BOTH events
	mcpServerScope string         // tool_result: the MCP server scope backing the tool
	toolInput      map[string]any // structured tool input (reduced to a redacted ref, never stored)

	// api_request
	model               string
	costUSD             float64
	hasCost             bool
	inputTokens         int64
	outputTokens        int64
	cacheReadTokens     int64
	cacheCreationTokens int64

	// api_error
	errMessage string
	statusCode int64
	attempt    int64

	// user_prompt (length only; content is never read)
	promptLength int64
}

// identity returns the session-scoped account attribution for an event.
func (e coworkEvent) identity() coworkIdentity {
	return coworkIdentity{
		sessionID:   e.sessionID,
		orgID:       e.orgID,
		accountUUID: e.accountUUID,
		accountID:   e.accountID,
	}
}

// parseLogRecord turns an OTLP LogRecord (with its resource-level attributes) into
// a coworkEvent, or reports false if the record carries no recognizable Cowork
// event name OR is not a Cowork record. The event name is read from the first-class
// EventName field (OTLP proto ≥ v1.5.0) with a fallback to the legacy "event.name"
// attribute, then normalized (canonicalEventName) so a bare or claude_code.-prefixed
// name both map. requireService, when non-empty, gates on service.name so a Claude
// Code record is not mis-ingested as Cowork; an empty service.name is tolerated
// (some collectors strip resource attrs) so a correctly-named event is never dropped.
func parseLogRecord(rec *logspb.LogRecord, resource []*commonpb.KeyValue, requireService string) (coworkEvent, bool) {
	if rec == nil {
		return coworkEvent{}, false
	}
	a := newAttrs(resource, rec.GetAttributes())

	svc := a.str(attrServiceName)
	if requireService != "" && svc != "" && svc != requireService {
		return coworkEvent{}, false
	}

	name := rec.GetEventName()
	if name == "" {
		name = a.str(attrEventName)
	}
	name = canonicalEventName(name)
	switch name {
	case evtUserPrompt, evtToolResult, evtAPIRequest, evtAPIError, evtToolDecision:
	default:
		return coworkEvent{}, false
	}

	ev := coworkEvent{
		name:           name,
		at:             recordTime(rec),
		serviceName:    svc,
		sessionID:      a.str(attrSessionID),
		orgID:          a.str(attrOrgID),
		userID:         a.str(attrUserID),
		accountUUID:    a.str(attrAccountUUID),
		accountID:      a.str(attrAccountID),
		promptID:       a.str(attrPromptID),
		workspacePaths: a.str(attrWorkspacePaths),
	}

	switch name {
	case evtToolResult:
		ev.toolName = a.str(attrToolName)
		ev.decisionSource = a.str(attrDecisionSource)
		ev.mcpServerScope = a.str(attrMCPServerScope)
		if b, ok := a.boolVal(attrSuccess); ok {
			ev.success = &b
		}
		// Reduce the tool input to a redacted resource at ingest (resource.go); the raw
		// map is read here only to derive that ref and is never stored on an observation.
		if in := a.objectVal(attrToolInput); in != nil {
			ev.toolInput = in
		} else if in := a.objectVal(attrToolParameters); in != nil {
			ev.toolInput = in
		}
	case evtToolDecision:
		ev.toolName = a.str(attrToolName)
		ev.decision = a.str(attrDecision)
		ev.decisionSource = a.str(attrSource)
	case evtAPIRequest:
		ev.model = a.str(attrModel)
		if c, ok := a.floatVal(attrCostUSD); ok {
			ev.costUSD, ev.hasCost = c, true
		}
		ev.inputTokens, _ = a.intVal(attrInputTokens)
		ev.outputTokens, _ = a.intVal(attrOutTokens)
		ev.cacheReadTokens, _ = a.intVal(attrCacheRead)
		ev.cacheCreationTokens, _ = a.intVal(attrCacheCreate)
	case evtAPIError:
		ev.model = a.str(attrModel)
		ev.errMessage = a.str(attrErrorMessage)
		ev.statusCode, _ = a.intVal(attrStatusCode)
		ev.attempt, _ = a.intVal(attrAttempt)
	case evtUserPrompt:
		// Liveness + prompt-volume only; the prompt text is never read (docs/SECURITY-HARDENING.md).
		ev.promptLength, _ = a.intVal(attrPromptLength)
	}
	return ev, true
}

// recordTime returns the record's event time as a UTC time.Time, preferring
// TimeUnixNano and falling back to ObservedTimeUnixNano; if both are zero it
// returns the zero time, which the caller stamps to receive time.
func recordTime(rec *logspb.LogRecord) time.Time {
	if t := rec.GetTimeUnixNano(); t != 0 {
		return time.Unix(0, int64(t)).UTC()
	}
	if t := rec.GetObservedTimeUnixNano(); t != 0 {
		return time.Unix(0, int64(t)).UTC()
	}
	return time.Time{}
}
