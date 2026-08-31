// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"strconv"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// MCP semantic conventions (v1.39.0) + the invoke_agent client/internal
// split (v1.41.0), both Development status: experimental, mapped with CLEAN
// degradation (an unrecognized shape feeds liveness and is skipped explicitly,
// never mis-mapped). The mcp.* conventions moved WITH gen_ai.* out of the main
// semantic-conventions repo in semconv v1.42.0 (2026-06-12, #3696) to the
// dedicated open-telemetry/semantic-conventions-genai repo; v1.41.1 remains the
// last VERSIONED vocabulary label, while main@c321d7e is the upstream shape ref
// re-verified 2026-07-05.
//
// MCP (re-verified 2026-07-05 against semantic-conventions-genai main@c321d7e
// docs/gen-ai/mcp.md — set unchanged from the v1.39.0/v1.41.1 tags): EXACTLY four
// mcp.* attributes exist — mcp.method.name (the only Required one),
// mcp.protocol.version, mcp.resource.uri, mcp.session.id. There is NO
// mcp.tool.name: the tool rides gen_ai.tool.name, the prompt rides
// gen_ai.prompt.name (new in v1.39), the JSON-RPC id rides jsonrpc.request.id.
// MCP tool-call spans are spec-compatible with gen_ai execute_tool spans
// (instrumentations SHOULD merge them and add mcp.* to the execute_tool span).
// Client spans are SpanKind CLIENT, server spans SERVER.
//
// These conventions are what lets the product JOIN its own MCP governance
// facts — the managed-mcp.json posture (ANT2-12, subjectManagedMCP), the
// claude_code.mcp_server_connection edges (resMCPServer) and the mcp__*
// tool-invocation edges (resMCP) — with the traces an OTel-instrumented MCP
// fleet emits: the edges below reuse the SAME resource kinds, so module III
// diffs declared vs observed MCP surface across both signal planes.
const (
	attrMCPMethod          = "mcp.method.name"
	attrMCPSession         = "mcp.session.id"
	attrMCPResourceURI     = "mcp.resource.uri"
	attrMCPProtocolVersion = "mcp.protocol.version"
	// attrGenAIPromptName is the v1.39 MCP prompt reference (gen_ai.*-namespaced
	// by the spec; NOT the legacy indexed gen_ai.prompt.{i}.* content shape).
	attrGenAIPromptName = "gen_ai.prompt.name"
	// attrServerAddress/attrServerPort locate the remote side of a CLIENT span
	// (MCP server, or a remote agent service on invoke_agent client spans).
	attrServerAddress = "server.address"
	attrServerPort    = "server.port"
)

// mcp.method.name values this profile maps (of the 25 well-known v1.39 values;
// the rest feed liveness only).
const (
	mcpMethodToolsCall     = "tools/call"
	mcpMethodResourcesRead = "resources/read"
	mcpMethodResourcesSub  = "resources/subscribe"
	mcpMethodPromptsGet    = "prompts/get"
)

// gen_ai.operation.name values added to the mapped set by (v1.41.1 enum;
// invoke_workflow is new in v1.41.0).
const (
	opInvokeAgent    = "invoke_agent"
	opInvokeWorkflow = "invoke_workflow"
)

// attrGenAIWorkflowName is the v1.41.0 workflow identity (CrewAI-style crews
// SHOULD report invoke_workflow spans carrying it).
const attrGenAIWorkflowName = "gen_ai.workflow.name"

// Resource kinds added by (open strings the engine resolves to Resource
// entities, like the rest of resource.go).
const (
	// resMCPResource is an MCP resource a client read/subscribed to
	// (mcp.resource.uri, sanitized). resMCPPrompt is an MCP prompt pulled into
	// context (prompts/get — executable-adjacent surface, like artifacts).
	resMCPResource = "mcp.resource"
	resMCPPrompt   = "mcp.prompt"
	// resGenAIAgentRemote is an agent invoked OVER A REMOTE SERVICE (the v1.41
	// invoke_agent CLIENT variant with a server address) — a cross-boundary
	// delegation, governance-distinct from an in-process sub-agent
	// (resGenAIAgent). resGenAIWorkflow is a v1.41 invoke_workflow target
	// (a CrewAI-style crew/workflow).
	resGenAIAgentRemote = "genai.agent.remote"
	resGenAIWorkflow    = "genai.workflow"
)

// serverAddr joins server.address[:server.port] into one endpoint reference.
func serverAddr(a attrs) string {
	host := a.str(attrServerAddress)
	if host == "" {
		return ""
	}
	if port, ok := a.intVal(attrServerPort); ok && port > 0 {
		return host + ":" + strconv.FormatInt(port, 10)
	}
	return host
}

// callerRef is the CALLER-side origin for a delegation/MCP edge: the
// conversation, else the MCP session. It deliberately ignores the agent
// identity — on invoke_agent spans gen_ai.agent.* names the CALLEE.
func (e genAIEvent) callerRef() (kind, ref string) {
	switch {
	case e.conversationID != "":
		return originSession, e.conversationID
	case e.mcpSession != "":
		return originSession, e.mcpSession
	default:
		return "", ""
	}
}

// mcpEdgesFromGenAI builds the access-map facts an MCP-convention signal
// carries (v1.39): the session/agent used an MCP server (joining the
// claude_code.mcp_server_connection edges by resource kind), read/subscribed
// an MCP resource (a REAL read-mode access), or pulled an MCP prompt. A
// SERVER-kind span is the server's own view — its origin is the remote
// client, not an identity this connector can attribute — so it feeds liveness
// only (clean degradation, never a guessed edge).
func mcpEdgesFromGenAI(e genAIEvent) []model.EdgeObservation {
	if e.mcpMethod == "" || e.spanKind == tracepb.Span_SPAN_KIND_SERVER {
		return nil
	}
	kind, ref := e.originRef()
	if ref == "" {
		return nil
	}
	base := model.EdgeObservation{
		OriginKind: kind,
		OriginRef:  ref,
		Mode:       model.ModeUnknown,
		Source:     model.SignalOTEL,
		Confidence: model.ConfidenceAttributed,
		ObservedAt: e.at,
	}
	var out []model.EdgeObservation
	// The MCP server endpoint itself (topology/inventory, resMCPServer — the
	// same kind the claude_code.mcp_server_connection edge uses).
	if e.serverAddr != "" {
		srv := base
		srv.ResourceKind = resMCPServer
		srv.ResourceRef = e.serverAddr
		out = append(out, srv)
	}
	switch e.mcpMethod {
	case mcpMethodResourcesRead, mcpMethodResourcesSub:
		// A read-mode access: MCP resource URIs carry real R-semantics. The URI
		// is sanitized (credentials/query stripped) before it becomes a ref.
		if e.mcpResourceURI != "" {
			res := base
			res.ResourceKind = resMCPResource
			res.ResourceRef = redact.SanitizeURL(e.mcpResourceURI)
			res.Mode = model.ModeRead
			out = append(out, res)
		}
	case mcpMethodPromptsGet:
		if e.mcpPromptName != "" {
			p := base
			p.ResourceKind = resMCPPrompt
			p.ResourceRef = e.mcpPromptName
			p.Mode = model.ModeRead
			out = append(out, p)
		}
	}
	return out
}

// remoteAgentEdgeFromGenAI maps the v1.41 invoke_agent CLIENT variant: a
// REMOTE agent invocation (the spec's examples: OpenAI Assistants, Bedrock
// Agents — and any A2A-style delegation). The classification deliberately
// requires BOTH SpanKind CLIENT and a server.address: real frameworks violate
// the kind today (AutoGen and Microsoft Agent Framework hard-code CLIENT for
// IN-PROCESS agents, verified jun-2026), but an in-process invocation carries
// no server.address, so the address is the discriminating fact. Anything else
// — INTERNAL (Google ADK), UNSPECIFIED, CLIENT without an address — is an
// in-process invocation already covered by the conversation→agent attribution
// edge (agentEdgeFromGenAI): degraded cleanly, never a fabricated "remote".
func remoteAgentEdgeFromGenAI(e genAIEvent) (model.EdgeObservation, bool) {
	if e.operation != opInvokeAgent || e.spanKind != tracepb.Span_SPAN_KIND_CLIENT || e.serverAddr == "" {
		return model.EdgeObservation{}, false
	}
	kind, ref := e.callerRef()
	if ref == "" {
		return model.EdgeObservation{}, false
	}
	callee := firstNonEmptyStr(e.agentName, e.agentID, e.serverAddr)
	return model.EdgeObservation{
		OriginKind:   kind,
		OriginRef:    ref,
		ResourceKind: resGenAIAgentRemote,
		ResourceRef:  callee,
		Mode:         model.ModeUnknown,
		Source:       model.SignalOTEL,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   e.at,
	}, true
}

// workflowEdgeFromGenAI maps the v1.41 invoke_workflow span (CrewAI-style
// crews): the conversation ran a named workflow. Gated on the operation name
// so a workflow attribute inherited by child spans does not re-emit per child.
func workflowEdgeFromGenAI(e genAIEvent) (model.EdgeObservation, bool) {
	if e.operation != opInvokeWorkflow || e.workflowName == "" {
		return model.EdgeObservation{}, false
	}
	kind, ref := e.callerRef()
	if ref == "" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   kind,
		OriginRef:    ref,
		ResourceKind: resGenAIWorkflow,
		ResourceRef:  e.workflowName,
		Mode:         model.ModeUnknown,
		Source:       model.SignalOTEL,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   e.at,
	}, true
}

// dialectFindingTitles maps each non-current dialect to its drift title. The
// findings exist because dialect currency is OPERATOR-ACTIONABLE: a fleet
// still emitting a deprecated generation should upgrade its instrumentation
// before the emitters disappear from maintained SDKs.
var dialectFindingTitles = map[string]string{
	genAIDialectLegacy: "fleet emits pre-semconv OpenLLMetry GenAI telemetry (gen_ai.prompt.{i}.*; deprecated upstream)",
	genAIDialectV136:   "fleet emits the deprecated gen_ai v1.36-or-prior events generation (gen_ai.system; replaced in v1.37)",
}

// dialectDriftFinding reports ONE info finding per deprecated dialect per
// Gather run (bounded by construction: at most two), the first time a signal
// of that generation is seen. seen is the per-run dedup.
func dialectDriftFinding(e genAIEvent, seen *identitySeen) (model.FindingReport, bool) {
	title, deprecated := dialectFindingTitles[e.semconv]
	if !deprecated || !seen.first(e.semconv) {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        "drift",
		Severity:    model.SeverityInfo,
		SubjectKind: "genai.dialect",
		SubjectRef:  e.semconv,
		Title:       title,
		DetailHash:  redact.Hash("genai dialect " + e.semconv + " first seen on " + e.livenessKey()),
		OccurredAt:  e.at,
	}, true
}

// genAIPostureFinding is the once-per-run self-audit of the multi-dialect
// ingest (the OBS-10 selfAuditFinding pattern): it records WHICH semconv pin
// and dialect set this run normalizes, so an auditor can prove what the
// connector understood — and that agent/MCP conventions were consumed as
// Development-status (experimental), claiming no stability they do not have.
func genAIPostureFinding(at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: "genai.semconv",
		SubjectRef:  genAISemconvVersion,
		Title: "gen_ai multi-dialect ingest active: pin " + genAISemconvVersion +
			"; dialects openllmetry-legacy, " + genAIDialectV136 + "-events, 1.37+; mcp.* (1.39) and agent spans (1.41) are Development",
		DetailHash: redact.Hash("genai dialects=" + genAIDialectLegacy + "," + genAIDialectV136 + "," + genAIDialectCurrent),
		OccurredAt: at,
	}
}
