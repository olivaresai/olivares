// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// GenAI agent-operation spans (issue #429), the semconv siblings of the model
// spans in genai.go. Attribute keys and span-name shapes verified against
// open-telemetry/semantic-conventions-genai (status: Development):
// docs/gen-ai/gen-ai-agent-spans.md — invoke_agent: "Span name SHOULD be
// `invoke_agent {gen_ai.agent.name}`", INTERNAL kind for a same-process agent;
// docs/gen-ai/gen-ai-spans.md — execute_tool: "Span name SHOULD be
// `execute_tool {gen_ai.tool.name}`", INTERNAL kind, gen_ai.tool.name Required,
// gen_ai.conversation.id Conditionally Required "if available".
// load_skill is emitted with the pending operation name the issue adopts
// (#429's evidence lists load_skill among gen_ai.operation.name values; current
// main models it as an execute_tool refinement — same attributes either way).
// Content (gen_ai.input.messages, gen_ai.output.messages, tool.call.arguments,
// tool.call.result) is Opt-In per the conventions and Olivares emits none.
const (
	attrGenAIConversationID = "gen_ai.conversation.id"
	attrGenAIAgentName      = "gen_ai.agent.name"
	attrGenAIToolName       = "gen_ai.tool.name"
	attrGenAISkillName      = "gen_ai.skill.name"

	opInvokeAgent = "invoke_agent"
	opExecuteTool = "execute_tool"
	opLoadSkill   = "load_skill"

	// attrOlivaresToolDecision records the governed PEP outcome on an
	// execute_tool span. It names no secret: allow/deny/ask is the decision the
	// ledger already anchors. Product keys stay in the reserved ai.olivares.*
	// namespace (freeze).
	attrOlivaresToolDecision = "ai.olivares.tool.decision"
	// errorTypeHookPEP is the low-cardinality error.type of a tool-decision span
	// that failed to decide (a plane fault), as distinct from a governed deny.
	errorTypeHookPEP = "olivares.hook_pep"

	// maxAgentNameBytes bounds the caller-chosen names (tool, skill) that become
	// span names and attributes, so a hostile or buggy agent cannot inflate
	// exporter payloads or collector cardinality.
	maxAgentNameBytes = 128
)

// boundName cuts a caller-chosen name to maxAgentNameBytes on a valid UTF-8
// boundary.
func boundName(name string) string {
	if len(name) > maxAgentNameBytes {
		name = name[:maxAgentNameBytes]
	}
	return strings.ToValidUTF8(name, "")
}

// AgentSpan is the live handle of one GenAI agent-operation span started by
// this provider. A nil *AgentSpan is a valid no-op holder, and every method is
// nil-safe, so a caller wires tracing without an enabled check — a tracing
// fault must never break the engine (docs/SECURITY-HARDENING.md).
type AgentSpan struct {
	span oteltrace.Span
}

// End finishes the span (idempotent by OTel contract).
func (s *AgentSpan) End() {
	if s == nil || s.span == nil {
		return
	}
	s.span.End()
}

// SetDecision records the governed PEP verdict of the tool call this span
// covers. A deny is the operation's outcome, not an error: the span status is
// untouched.
func (s *AgentSpan) SetDecision(decision string) {
	if s == nil || s.span == nil {
		return
	}
	s.span.SetAttributes(attribute.String(attrOlivaresToolDecision, decision))
}

// RecordFailure marks the span as one that could not complete its operation:
// the status is set per the Recording Errors convention, with the
// instrumentation's own low-cardinality error.type. The error text is never
// exported: it may carry paths or identifiers, and the connector already logs it.
func (s *AgentSpan) RecordFailure() {
	if s == nil || s.span == nil {
		return
	}
	s.span.SetStatus(codes.Error, errorTypeHookPEP)
	s.span.SetAttributes(attribute.String("error.type", errorTypeHookPEP))
}

// SpanContext reports the span's context for parenting later spans (tool,
// model) under the same session trace. With tracing disabled it is not valid,
// which is exactly the signal callers use to skip registering a link.
func (s *AgentSpan) SpanContext() oteltrace.SpanContext {
	if s == nil || s.span == nil {
		return oteltrace.SpanContext{}
	}
	return s.span.SpanContext()
}

// The three starters are nil-receiver safe: a nil provider (a composition that
// wires no tracer) yields a nil, no-op *AgentSpan.
//
// InvokeAgent starts the per-session agent span — the trace root of one agent
// run. agentName is gen_ai.agent.name (the driver Olivares launches);
// conversationID is gen_ai.conversation.id (the run reference; never a
// synthesized UUID — the conventions forbid fallback values). The returned
// handle owns the span's lifetime: the sessions runtime ends it when the run
// finalizes.
func (p *Provider) InvokeAgent(agentName, conversationID string) *AgentSpan {
	if p == nil {
		return nil
	}
	p = p.active()
	name := opInvokeAgent
	attrs := []attribute.KeyValue{attribute.String(attrGenAIOperation, opInvokeAgent)}
	if agentName != "" {
		name = opInvokeAgent + " " + agentName
		attrs = append(attrs, attribute.String(attrGenAIAgentName, agentName))
	}
	if conversationID != "" {
		attrs = append(attrs, attribute.String(attrGenAIConversationID, conversationID))
	}
	_, span := p.tracer.Start(context.Background(), name,
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
		oteltrace.WithAttributes(attrs...))
	return &AgentSpan{span: span}
}

// ExecuteTool starts one execute_tool span for a governed tool-call decision.
// parent is the session's invoke_agent span context (zero SpanContext starts a
// standalone root — a decision whose run is not live); agentName and
// conversationID are set when known.
func (p *Provider) ExecuteTool(parent oteltrace.SpanContext, agentName, toolName, conversationID string) *AgentSpan {
	if p == nil {
		return nil
	}
	p = p.active()
	toolName = boundName(toolName)
	if toolName == "" {
		toolName = "unknown" // gen_ai.tool.name is Required on execute_tool
	}
	ctx := context.Background()
	if parent.IsValid() {
		ctx = oteltrace.ContextWithRemoteSpanContext(ctx, parent)
	}
	attrs := []attribute.KeyValue{
		attribute.String(attrGenAIOperation, opExecuteTool),
		attribute.String(attrGenAIToolName, toolName),
	}
	if agentName != "" {
		attrs = append(attrs, attribute.String(attrGenAIAgentName, agentName))
	}
	if conversationID != "" {
		attrs = append(attrs, attribute.String(attrGenAIConversationID, conversationID))
	}
	_, span := p.tracer.Start(ctx, opExecuteTool+" "+toolName,
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
		oteltrace.WithAttributes(attrs...))
	return &AgentSpan{span: span}
}

// LoadSkill starts one load_skill span for a skill becoming available to an
// agent (a skills assignment pinning a revision's member to a target). The
// conversationID is set when the assignment names a session; an assignment to
// a workspace or template serves future conversations and carries none.
func (p *Provider) LoadSkill(conversationID, skillName string) *AgentSpan {
	if p == nil {
		return nil
	}
	p = p.active()
	skillName = boundName(skillName)
	if skillName == "" {
		skillName = "unknown" // gen_ai.skill.name drives the span name
	}
	attrs := []attribute.KeyValue{
		attribute.String(attrGenAIOperation, opLoadSkill),
		attribute.String(attrGenAISkillName, skillName),
	}
	if conversationID != "" {
		attrs = append(attrs, attribute.String(attrGenAIConversationID, conversationID))
	}
	_, span := p.tracer.Start(context.Background(), opLoadSkill+" "+skillName,
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
		oteltrace.WithAttributes(attrs...))
	return &AgentSpan{span: span}
}

// sessionTraceContext carries the session run reference beside the ambient
// remote span context, so spans started from the context (the model spans in
// genai.go) can attribute themselves to the session conversation without a
// second lookup.
type sessionTraceContext struct {
	conversationID string
}

type sessionTraceContextKey struct{}

// ContextWithSessionTrace installs a session run's agent-span context as the
// ambient remote parent of ctx and carries the run reference as the
// gen_ai.conversation.id of spans started from ctx. An invalid span context
// (tracing disabled, run not live) leaves ctx unchanged: telemetry never
// alters the request path.
func ContextWithSessionTrace(ctx context.Context, sc oteltrace.SpanContext, conversationID string) context.Context {
	if !sc.IsValid() {
		return ctx
	}
	ctx = oteltrace.ContextWithRemoteSpanContext(ctx, sc)
	return context.WithValue(ctx, sessionTraceContextKey{}, sessionTraceContext{conversationID: conversationID})
}

// conversationIDFrom reports the session run reference ContextWithSessionTrace
// carried, when it did.
func conversationIDFrom(ctx context.Context) (string, bool) {
	v, _ := ctx.Value(sessionTraceContextKey{}).(sessionTraceContext)
	return v.conversationID, v.conversationID != ""
}
