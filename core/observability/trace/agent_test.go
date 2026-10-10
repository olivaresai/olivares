// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Issue #429's exit criterion, proven with the in-memory span recorder: one
// session's trace carries the agent span, the tool spans and the model span
// under the GenAI semconv names, all sharing gen_ai.conversation.id.
func TestSessionTraceCompositeInOneTrace(t *testing.T) {
	p, sr, _ := enabledTestProvider()

	agent := p.InvokeAgent("claude", "run-42")
	sc := agent.SpanContext()
	if !sc.IsValid() {
		t.Fatal("the invoke_agent span context must be valid so later spans can parent on it")
	}
	tool := p.ExecuteTool(sc, "claude", "Bash", "run-42")
	tool.SetDecision("allow")
	tool.End()

	// The model call carries the session trace context (what the inference-proxy
	// link middleware installs) so its chat span joins the same trace.
	ctx := ContextWithSessionTrace(context.Background(), sc, "run-42")
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(cannedMessagesResponse)),
		}, nil
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages",
		strings.NewReader(`{"model":"claude-opus-4-8","max_tokens":16}`))
	if _, err := p.AnthropicHTTPClient(base).Do(req); err != nil {
		t.Fatalf("Do: %v", err)
	}
	agent.End()

	ended := sr.Ended()
	if len(ended) != 3 {
		t.Fatalf("expected 3 spans (invoke_agent, execute_tool, chat), got %d", len(ended))
	}
	byTrace := map[string]int{}
	for _, span := range ended {
		byTrace[span.SpanContext().TraceID().String()]++
	}
	if len(byTrace) != 1 {
		t.Fatalf("the session's spans must share ONE trace id, got %d traces", len(byTrace))
	}

	names := map[string]bool{}
	for _, span := range ended {
		names[span.Name()] = true
	}
	for _, want := range []string{"invoke_agent claude", "execute_tool Bash", "chat claude-opus-4-8"} {
		if !names[want] {
			t.Errorf("span %q missing from the session trace; got %v", want, names)
		}
	}

	for _, span := range ended {
		wantOp := map[string]string{
			"invoke_agent claude": opInvokeAgent,
			"execute_tool Bash":   opExecuteTool,
		}[span.Name()]
		if wantOp != "" {
			if v, ok := attrOf(span, attrGenAIOperation); !ok || v.AsString() != wantOp {
				t.Errorf("%s: gen_ai.operation.name = %v ok=%v, want %q", span.Name(), v.AsString(), ok, wantOp)
			}
			if v, ok := attrOf(span, attrGenAIConversationID); !ok || v.AsString() != "run-42" {
				t.Errorf("%s: gen_ai.conversation.id = %v ok=%v, want run-42", span.Name(), v.AsString(), ok)
			}
			if span.SpanKind().String() != "internal" {
				t.Errorf("%s: span kind = %s, want internal", span.Name(), span.SpanKind())
			}
		}
	}

	var toolSpan, chatSpan sdkSpan
	for _, span := range ended {
		switch span.Name() {
		case "execute_tool Bash":
			toolSpan = span
		case "chat claude-opus-4-8":
			chatSpan = span
		}
	}
	if parent := toolSpan.Parent(); parent.SpanID() != sc.SpanID() {
		t.Errorf("execute_tool parent = %s, want the invoke_agent span %s", parent.SpanID(), sc.SpanID())
	}
	if parent := chatSpan.Parent(); parent.SpanID() != sc.SpanID() {
		t.Errorf("chat parent = %s, want the invoke_agent span %s", parent.SpanID(), sc.SpanID())
	}
	if v, ok := attrOf(chatSpan, attrGenAIConversationID); !ok || v.AsString() != "run-42" {
		t.Errorf("chat span: gen_ai.conversation.id = %v ok=%v, want run-42", v.AsString(), ok)
	}
	if v, ok := attrOf(toolSpan, attrOlivaresToolDecision); !ok || v.AsString() != "allow" {
		t.Errorf("execute_tool: %s = %v ok=%v, want allow", attrOlivaresToolDecision, v.AsString(), ok)
	}
	if v, ok := attrOf(toolSpan, attrGenAIToolName); !ok || v.AsString() != "Bash" {
		t.Errorf("execute_tool: gen_ai.tool.name = %v ok=%v, want Bash", v.AsString(), ok)
	}
	if v, ok := attrOf(toolSpan, attrGenAIAgentName); !ok || v.AsString() != "claude" {
		t.Errorf("execute_tool: gen_ai.agent.name = %v ok=%v, want claude", v.AsString(), ok)
	}
}

// The chat span of a call with NO session link keeps today's shape: no
// conversation.id attribute appears out of thin air.
func TestChatSpanHasNoConversationIDWithoutSessionLink(t *testing.T) {
	p, sr, _ := enabledTestProvider()
	doCannedMessagesCall(t, p)
	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("expected 1 span, got %d", len(ended))
	}
	assertNoAttr(t, ended[0], attrGenAIConversationID)
}

// A governed deny is the tool operation's OUTCOME, not an error; a decider fault
// is recorded as one. The two must stay distinguishable on the span.
func TestExecuteToolDecisionVersusError(t *testing.T) {
	p, sr, _ := enabledTestProvider()

	denied := p.ExecuteTool(oteltrace.SpanContext{}, "", "WebFetch", "run-7")
	denied.SetDecision("deny")
	denied.End()

	failed := p.ExecuteTool(oteltrace.SpanContext{}, "", "WebFetch", "run-7")
	failed.RecordFailure()
	failed.End()

	ended := sr.Ended()
	if len(ended) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(ended))
	}
	for _, span := range ended {
		if v, ok := attrOf(span, attrGenAIToolName); !ok || v.AsString() != "WebFetch" {
			t.Errorf("%s: gen_ai.tool.name = %v ok=%v, want WebFetch", span.Name(), v.AsString(), ok)
		}
	}
	if code := ended[0].Status().Code; code != codes.Unset {
		t.Errorf("a denied tool decision must not be an error span; status = %v", code)
	}
	if v, ok := attrOf(ended[0], attrOlivaresToolDecision); !ok || v.AsString() != "deny" {
		t.Errorf("deny decision attribute = %v ok=%v", v.AsString(), ok)
	}
	if code := ended[1].Status().Code; code != codes.Error {
		t.Errorf("a failed decision must be an error span; status = %v", code)
	}
	if v, ok := attrOf(ended[1], "error.type"); !ok || v.AsString() == "" {
		t.Errorf("error.type missing on the failed decision span")
	}
}

// A caller-chosen name is bounded before it names a span, and a failed decision
// exports a status only: the error text (paths, identifiers) never leaves.
func TestAgentSpanNamesAreBoundedAndFailuresCarryNoText(t *testing.T) {
	p, sr, _ := enabledTestProvider()

	huge := strings.Repeat("t", 1<<20)
	p.ExecuteTool(oteltrace.SpanContext{}, "", huge, "").End()
	p.LoadSkill("", huge).End()
	failed := p.ExecuteTool(oteltrace.SpanContext{}, "", "Bash", "")
	failed.RecordFailure()
	failed.End()

	ended := sr.Ended()
	if len(ended) != 3 {
		t.Fatalf("expected 3 spans, got %d", len(ended))
	}
	for _, span := range ended[:2] {
		if len(span.Name()) > len("execute_tool ")+maxAgentNameBytes {
			t.Errorf("span name is %d bytes; caller-chosen names must be bounded", len(span.Name()))
		}
	}
	if n := len(ended[2].Events()); n != 0 {
		t.Errorf("a failed decision exported %d span events; the error text must not leave the process", n)
	}
}

// A nil provider (a composition that wires no tracer) yields no-op spans.
func TestNilProviderStartsNoOpSpans(t *testing.T) {
	var p *Provider
	p.InvokeAgent("claude", "run").End()
	p.ExecuteTool(oteltrace.SpanContext{}, "", "Bash", "run").End()
	p.LoadSkill("run", "pdf").End()
}

// load_skill: one span per skill, named per the convention, carrying the skill
// name and the conversation when the assignment names a session.
func TestLoadSkillSpan(t *testing.T) {
	p, sr, _ := enabledTestProvider()

	p.LoadSkill("run-9", "pdf-report").End()
	p.LoadSkill("", "excel-formulas").End()

	ended := sr.Ended()
	if len(ended) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(ended))
	}
	first := ended[0]
	if first.Name() != "load_skill pdf-report" {
		t.Errorf("span name = %q, want load_skill pdf-report", first.Name())
	}
	if v, ok := attrOf(first, attrGenAIOperation); !ok || v.AsString() != opLoadSkill {
		t.Errorf("gen_ai.operation.name = %v ok=%v, want %q", v.AsString(), ok, opLoadSkill)
	}
	if v, ok := attrOf(first, attrGenAISkillName); !ok || v.AsString() != "pdf-report" {
		t.Errorf("gen_ai.skill.name = %v ok=%v, want pdf-report", v.AsString(), ok)
	}
	if v, ok := attrOf(first, attrGenAIConversationID); !ok || v.AsString() != "run-9" {
		t.Errorf("gen_ai.conversation.id = %v ok=%v, want run-9", v.AsString(), ok)
	}
	assertNoAttr(t, ended[1], attrGenAIConversationID)
}

// Content attributes are Opt-In per the conventions and Olivares keeps them off:
// no agent-operation span ever carries message or argument content.
func TestAgentSpansCarryNoContent(t *testing.T) {
	p, sr, _ := enabledTestProvider()

	agent := p.ExecuteTool(oteltrace.SpanContext{}, "claude", "Bash", "run-42")
	agent.SetDecision("allow")
	agent.End()
	p.LoadSkill("run-42", "pdf-report").End()
	p.InvokeAgent("claude", "run-42").End()

	for _, span := range sr.Ended() {
		for _, key := range []string{"gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.system_instructions", "gen_ai.tool.call.arguments", "gen_ai.tool.call.result"} {
			assertNoAttr(t, span, key)
		}
	}
}

// With tracing disabled the agent-span API must be a harmless no-op whose span
// context is NOT valid (so callers skip registering links).
func TestDisabledProviderAgentSpansAreNoops(t *testing.T) {
	p, err := New(context.Background(), Config{Enabled: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	agent := p.InvokeAgent("claude", "run-42")
	if sc := agent.SpanContext(); sc.IsValid() {
		t.Errorf("disabled provider must not yield a linkable span context: %s", sc.TraceID())
	}
	agent.SetDecision("allow")
	agent.RecordFailure()
	agent.End() // must not panic
}

// sdkSpan is the recorder-span view the assertions above need.
type sdkSpan interface {
	Attributes() []attribute.KeyValue
	Parent() oteltrace.SpanContext
}
