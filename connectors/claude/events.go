// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// Claude Code OTEL event names (carried in LogRecord.EventName, or in the
// legacy "event.name" attribute). Source: Claude Code monitoring docs —
// https://code.claude.com/docs/en/monitoring-usage (Events).
const (
	evtToolResult    = "claude_code.tool_result"
	evtToolDecision  = "claude_code.tool_decision"
	evtAPIRequest    = "claude_code.api_request"
	evtMCPConnection = "claude_code.mcp_server_connection"
	evtUserPrompt    = "claude_code.user_prompt"
	// Security/audit events Anthropic documents as "forward to a SIEM for a
	// per-user audit trail" (OBS-05). Previously unrouted and dropped.
	evtAuth           = "claude_code.auth"
	evtAPIError       = "claude_code.api_error"
	evtAPIRetriesGone = "claude_code.api_retries_exhausted"
	evtPermissionMode = "claude_code.permission_mode_changed"

	// ANT2-09 net-new events: the Claude Code OTLP plane is the audit/SIEM data plane
	// (distinct from the server-side REST aggregate). These extend the mapped set
	// to the supply-chain / agent-lifecycle / context events a governance plane needs.
	// A plugin install and a skill activation are supply-chain/governance signals
	// (what executable surface a session pulled in); a compaction is a forensic
	// continuity signal (context was summarized — the transcript is no longer whole).
	evtPluginInstalled = "claude_code.plugin_installed"
	evtSkillActivated  = "claude_code.skill_activated"
	evtCompaction      = "claude_code.compaction"
	// evtHookExecutionPrefix matches the hook_execution_* family (start/completed/…),
	// the runtime side of a configured hook firing. Handled by prefix so a new suffix
	// is not silently dropped.
	evtHookExecutionPrefix = "claude_code.hook_execution"
)

// Claude Code OTLP METRICS (ANT2-09). These are recognized for naming/recognition.
// Their VALUES are NOT turned into cost here — cost.usage/token.usage are reconciled
// against the authoritative cost path so a session is never DOUBLE-COUNTED ("one
// source of cost per session"). The metric BATCH feeds liveness + identity
// attribution via onSignal (otlp.go); and, opt-in, the NON-cost counts
// (lines/commits/PRs/sessions/tokens/edit-decisions/active-time) are persisted as
// adoption MetricSamples (metrics.go) — cost.usage is excluded there too. Source:
// code.claude.com/docs/en/monitoring-usage (Metrics).
const (
	metricSessionCount     = "claude_code.session.count"
	metricLinesOfCode      = "claude_code.lines_of_code.count"
	metricPullRequest      = "claude_code.pull_request.count"
	metricCommit           = "claude_code.commit.count"
	metricCostUsage        = "claude_code.cost.usage"
	metricTokenUsage       = "claude_code.token.usage"
	metricCodeEditDecision = "claude_code.code_edit_tool.decision"
	metricActiveTime       = "claude_code.active_time.total"
)

// claudeCodeMetrics is the recognized metric-name set (the 8 documented metrics).
// It is the recognition aid for naming/validation. The receiver consumes the NON-cost
// VALUES as adoption MetricSamples when enabled (isAdoptionMetric); cost.usage
// stays on the authoritative FinOps path and is never persisted here.
var claudeCodeMetrics = map[string]struct{}{
	metricSessionCount: {}, metricLinesOfCode: {}, metricPullRequest: {}, metricCommit: {},
	metricCostUsage: {}, metricTokenUsage: {}, metricCodeEditDecision: {}, metricActiveTime: {},
}

// IsClaudeCodeMetric reports whether a metric name is one of the 8 documented Claude
// Code OTLP metrics (ANT2-09). It is the recognition aid a SIEM/observability consumer
// uses to attribute a metric batch, and the gate for adoption-value
// persistence (isAdoptionMetric narrows it to the non-cost subset).
func IsClaudeCodeMetric(name string) bool {
	_, ok := claudeCodeMetrics[name]
	return ok
}

// isCostMetric reports whether a metric name is one whose VALUE would double-count the
// authoritative cost path if summed as cost (cost.usage / token.usage). The receiver
// uses it to PROVE it never turns these into a CostSample (reconciliation, not
// summation —).
func isCostMetric(name string) bool {
	return name == metricCostUsage || name == metricTokenUsage
}

// Standard and per-event attribute keys (Claude Code monitoring docs). Only the
// keys this connector reads are named; the rest are ignored.
const (
	attrEventName   = "event.name"
	attrSessionID   = "session.id"
	attrAccountUUID = "user.account_uuid"
	attrOrgID       = "organization.id"
	attrAppVersion  = "app.version"
	attrAgentName   = "agent.name"
	// attrAppEntrypoint (`app.entrypoint`) is the 2.1.17x standard attribute shared
	// by ALL metrics and events (VERIFIED 2026-06-10, monitoring-usage doc;
	// changelog 2.1.152): HOW the session was launched (cli, sdk-cli, sdk-ts,
	// sdk-py, claude-vscode, …). It is OPT-IN on the producer
	// (OTEL_METRICS_INCLUDE_ENTRYPOINT, default false) — ingest never assumes it.
	attrAppEntrypoint = "app.entrypoint"
	// attrAgentID / attrParentAgentID are TRACE-SPAN-ONLY attributes (VERIFIED 2026-06-10 — they appear on NO metric and NO log event): the
	// per-instance subagent id on claude_code.llm_request spans (changelog
	// 2.1.139) and claude_code.tool spans (2.1.145). agent_id is "absent on the
	// main session"; parent_agent_id is "absent for the main session and for
	// agents spawned directly from it". They require the tracing beta
	// (CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1 + OTEL_TRACES_EXPORTER).
	attrAgentID       = "agent_id"
	attrParentAgentID = "parent_agent_id"
	attrToolName      = "tool_name"
	attrToolUseID     = "tool_use_id"
	// attrPromptID (`prompt.id`, dotted — NOT `prompt_id`) is the UUID v4 that groups
	// every event produced while processing ONE user prompt/turn (user_prompt, api_request,
	// tool_result). VERIFIED 2026-06-09 (code.claude.com/docs/en/monitoring-usage):
	// event-only, intentionally excluded from metrics (cardinality). It is the forensic
	// GROUPING key for a turn; tool_use_id remains the per-CALL correlation join.
	attrPromptID      = "prompt.id"
	attrSuccess       = "success"
	attrDecision      = "decision"
	attrSource        = "source"
	attrToolInput     = "tool_input"
	attrToolParams    = "tool_parameters"
	attrModel         = "model"
	attrCostUSD       = "cost_usd"
	attrInputTokens   = "input_tokens"
	attrOutputTokens  = "output_tokens"
	attrCacheRead     = "cache_read_tokens"
	attrCacheCreate   = "cache_creation_tokens"
	attrStatus        = "status"
	attrTransportType = "transport_type"
	attrServerScope   = "server_scope"
	attrServerName    = "server_name"
	attrErrorCode     = "error_code"

	// user_prompt — only the LENGTH is read; the prompt text (attribute "prompt")
	// is redacted by Claude Code unless OTEL_LOG_USER_PROMPTS=1 and is never read
	// or stored here regardless (redaction-by-default, OBS-10, docs/SECURITY-HARDENING.md).
	attrPromptLength = "prompt_length"
	// api_error / api_retries_exhausted
	attrErrorMessage  = "error"
	attrStatusCode    = "status_code"
	attrAttempt       = "attempt"
	attrTotalAttempts = "total_attempts"
	// permission_mode_changed — from/to ∈ default|plan|acceptEdits|auto|
	// bypassPermissions; trigger ∈ shift_tab|exit_plan_mode|auto_gate_denied|
	// auto_opt_in (or absent).
	attrFromMode = "from_mode"
	attrToMode   = "to_mode"
	attrTrigger  = "trigger"
	// auth — action ∈ login|logout; auth_method e.g. "oauth".
	attrAuthAction = "action"
	attrAuthMethod = "auth_method"

	// ANT2-09 attribution dimensions: the OTLP audit plane tags records with the
	// query source (user|agent_sdk|…), the effort tier, and the named agent/skill/
	// plugin/mcp-server that acted. They make a finding attributable to WHAT pulled in
	// or ran a surface, not just which session.
	attrEffort        = "effort"
	attrQuerySource   = "query_source"
	attrSkillName     = "skill.name"
	attrPluginName    = "plugin.name"
	attrMCPServerName = "mcp_server.name"
	attrHookEventName = "hook_event_name"

	// attrType is the adoption-metric datapoint axis (VERIFIED 2026-06-20,
	// code.claude.com/docs/en/monitoring-usage Metrics): it distinguishes lines
	// added/removed, token input/output/cacheRead/cacheCreation, and active_time
	// user/cli. (The edit-decision `language` and session `start_type` axes are
	// recognized but deliberately not persisted — see adoptionMetricDimensions.)
	attrType = "type"
)

// claudeEvent is the connector's normalized view of one Claude Code OTEL log
// record: the event name, the time it occurred, the identity/session attribution,
// and the union of per-event fields this connector maps. Absent fields are zero.
type claudeEvent struct {
	name       string
	at         time.Time
	sessionID  string
	accountID  string
	orgID      string
	appVersion string
	agentName  string
	// entrypoint is the opt-in app.entrypoint launch surface (empty when the
	// producer did not opt in via OTEL_METRICS_INCLUDE_ENTRYPOINT).
	entrypoint string
	// labels are the operator's allowlisted OTEL_RESOURCE_ATTRIBUTES,
	// collected at the RECEIVER layer (the allowlist is config, which the pure
	// parser does not see) and scrubbed there. nil = feature off / none present.
	labels map[string]string

	// prompt.id — groups every event of one user prompt/turn (forensic grouping key).
	// Present on user_prompt/api_request/tool_result; empty when the producer omits it.
	promptID string

	// tool_result / tool_decision
	toolName       string
	toolUseID      string
	success        *bool
	decision       string         // tool_decision: "accept"/"reject"
	decisionSource string         // tool_decision: who decided (config/hook/user_*)
	toolInput      map[string]any // present only with OTEL_LOG_TOOL_DETAILS=1

	// api_request
	model               string
	costUSD             float64
	hasCost             bool
	inputTokens         int64
	outputTokens        int64
	cacheReadTokens     int64
	cacheCreationTokens int64

	// mcp_server_connection
	mcpStatus    string
	mcpTransport string
	mcpScope     string
	mcpServer    string
	mcpErrorCode string

	// user_prompt (length only; content is never read — OBS-10)
	promptLength int64

	// api_error / api_retries_exhausted
	errMessage string
	statusCode int64
	attempt    int64

	// permission_mode_changed
	fromMode    string
	toMode      string
	modeTrigger string

	// auth (login/logout); success reuses the shared success pointer
	authAction string
	authMethod string

	// ANT2-09 attribution + net-new events.
	effort      string // effort tier (low..xhigh)
	querySource string // who issued the query (user|agent_sdk|…)
	skillName   string // skill_activated: the activated skill (skill.name)
	pluginName  string // plugin_installed: the installed plugin (plugin.name)
	hookName    string // hook_execution_*: the hook event that fired (hook_event_name)
}

// originRef returns the most precise stable attribution for the event: the
// session id (the operational unit, always present on a real Claude Code event).
// Identity resolution (session→agent→NHI) is the inventory module's job; the
// connector attributes to the session so the edge is never a shared account
// (ARCHITECTURE.md).
func (e claudeEvent) originRef() string { return e.sessionID }

// parseLogRecord turns an OTLP LogRecord (with its resource-level attributes)
// into a claudeEvent, or reports false if the record carries no recognizable
// event name. The event name is read from the first-class EventName field
// (OTLP proto ≥ v1.5.0) with a fallback to the legacy "event.name" attribute.
func parseLogRecord(rec *logspb.LogRecord, resource []*commonpb.KeyValue) (claudeEvent, bool) {
	if rec == nil {
		return claudeEvent{}, false
	}
	a := newAttrs(resource, rec.GetAttributes())

	name := rec.GetEventName()
	if name == "" {
		name = a.str(attrEventName)
	}
	if name == "" {
		return claudeEvent{}, false
	}

	ev := claudeEvent{
		name:        name,
		at:          recordTime(rec),
		sessionID:   a.str(attrSessionID),
		accountID:   a.str(attrAccountUUID),
		orgID:       a.str(attrOrgID),
		appVersion:  a.str(attrAppVersion),
		agentName:   a.str(attrAgentName),
		entrypoint:  a.str(attrAppEntrypoint),
		promptID:    a.str(attrPromptID),
		effort:      a.str(attrEffort),
		querySource: a.str(attrQuerySource),
	}

	// hook_execution_* is a family; read its hook name so the runtime-side hook firing
	// is attributable. Handled by prefix so a new suffix is not dropped (ANT2-09).
	if strings.HasPrefix(name, evtHookExecutionPrefix) {
		ev.hookName = a.str(attrHookEventName)
	}

	switch name {
	case evtToolResult, evtToolDecision:
		ev.toolName = a.str(attrToolName)
		ev.toolUseID = a.str(attrToolUseID)
		// tool_decision carries the outcome in "decision" (accept/reject) and the
		// deciding authority in "source" (config/hook/user_*). Keep them separate so
		// a denial can be surfaced with its provenance (was the dead "decision"
		// field, never read — now routed, see routeOTEL/findingFromToolDecision).
		ev.decision = a.str(attrDecision)
		ev.decisionSource = a.str(attrSource)
		if b, ok := a.boolVal(attrSuccess); ok {
			ev.success = &b
		}
		if in := a.objectVal(attrToolInput); in != nil {
			ev.toolInput = in
		} else if in := a.objectVal(attrToolParams); in != nil {
			ev.toolInput = in
		}
	case evtAPIRequest:
		ev.model = a.str(attrModel)
		if c, ok := a.floatVal(attrCostUSD); ok {
			ev.costUSD, ev.hasCost = c, true
		}
		ev.inputTokens, _ = a.intVal(attrInputTokens)
		ev.outputTokens, _ = a.intVal(attrOutputTokens)
		ev.cacheReadTokens, _ = a.intVal(attrCacheRead)
		ev.cacheCreationTokens, _ = a.intVal(attrCacheCreate)
	case evtMCPConnection:
		ev.mcpStatus = a.str(attrStatus)
		ev.mcpTransport = a.str(attrTransportType)
		ev.mcpScope = a.str(attrServerScope)
		ev.mcpServer = a.str(attrServerName)
		ev.mcpErrorCode = a.str(attrErrorCode)
	case evtUserPrompt:
		// Prompt-volume/liveness signal only. The prompt content is never read
		// (see attrPromptLength); routing it feeds the silence watchdog and the
		// per-session prompt count rather than leaving the event dead (OBS-05).
		ev.promptLength, _ = a.intVal(attrPromptLength)
	case evtAuth:
		ev.authAction = a.str(attrAuthAction)
		ev.authMethod = a.str(attrAuthMethod)
		if b, ok := a.boolVal(attrSuccess); ok {
			ev.success = &b
		}
	case evtAPIError:
		ev.model = a.str(attrModel)
		ev.errMessage = a.str(attrErrorMessage)
		ev.statusCode, _ = a.intVal(attrStatusCode)
		ev.attempt, _ = a.intVal(attrAttempt)
	case evtAPIRetriesGone:
		ev.model = a.str(attrModel)
		ev.errMessage = a.str(attrErrorMessage)
		ev.statusCode, _ = a.intVal(attrStatusCode)
		ev.attempt, _ = a.intVal(attrTotalAttempts)
	case evtPermissionMode:
		ev.fromMode = a.str(attrFromMode)
		ev.toMode = a.str(attrToMode)
		ev.modeTrigger = a.str(attrTrigger)
	case evtPluginInstalled:
		ev.pluginName = a.str(attrPluginName)
	case evtSkillActivated:
		ev.skillName = a.str(attrSkillName)
	case evtCompaction:
		// No extra fields; the event itself is the forensic continuity signal.
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
