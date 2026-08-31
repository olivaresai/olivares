// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// okMsg is a minimal successful Messages response body for the routeDoer.
const okMsg = `{"id":"msg_p","type":"message","role":"assistant","model":"claude-opus-4-8",` +
	`"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`

// TestCreateMessage_AssemblesBetaHeaders proves CreateMessage collects the union of
// anthropic-beta headers the request's 2026 primitives require (D1/D3/D4/D5) into one
// comma-separated header, and serializes the new request shapes.
func TestCreateMessage_AssemblesBetaHeaders(t *testing.T) {
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": okMsg}}
	inf := newInf(d, model.GatewayDirect)
	_, err := inf.CreateMessage(context.Background(), MessageRequest{
		MaxTokens: 64,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{TextBlock("hi")}},
			SystemMessage("policy: only read"), // D3 mid-conversation system message
		},
		Tools:             []any{AdvisorTool("claude-sonnet-4-6")},           // D1 advisor
		OutputConfig:      &OutputConfig{TaskBudget: TokenTaskBudget(50000)}, // D4
		ContextManagement: CompactionContextManagement(),                     // D5
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	for _, want := range []string{BetaMidConversationSystem, BetaTaskBudgets, BetaCompaction, BetaAdvisorTool} {
		if !strings.Contains(d.lastBeta, want) {
			t.Errorf("anthropic-beta %q missing from %q", want, d.lastBeta)
		}
	}
	// The new request shapes serialize on the wire.
	if !strings.Contains(d.lastBody, `"task_budget":{"type":"tokens","total":50000}`) {
		t.Errorf("task_budget not serialized: %s", d.lastBody)
	}
	if !strings.Contains(d.lastBody, `"context_management":{"edits":[{"type":"compact_20260112"}]}`) {
		t.Errorf("compaction edit not serialized: %s", d.lastBody)
	}
	if !strings.Contains(d.lastBody, `"role":"system"`) {
		t.Errorf("system-role message not serialized: %s", d.lastBody)
	}
}

// TestCreateMessage_NoPrimitivesNoBetaHeader proves a plain request sends no beta
// header (byte-compatible with the pre-2026 shape).
func TestCreateMessage_NoPrimitivesNoBetaHeader(t *testing.T) {
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": okMsg}}
	inf := newInf(d, model.GatewayDirect)
	if _, err := inf.CreateMessage(context.Background(), MessageRequest{
		MaxTokens: 8,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if d.lastBeta != "" {
		t.Errorf("unexpected anthropic-beta header on a plain request: %q", d.lastBeta)
	}
	if strings.Contains(d.lastBody, "output_config") || strings.Contains(d.lastBody, "context_management") {
		t.Errorf("plain request must omit the 2026 fields: %s", d.lastBody)
	}
}

// TestTaskBudget_MinimumEnforced proves D4's 20k floor fails honestly client-side and
// a valid budget passes.
func TestTaskBudget_MinimumEnforced(t *testing.T) {
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": okMsg}}
	inf := newInf(d, model.GatewayDirect)
	base := MessageRequest{MaxTokens: 8, Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}}}

	below := base
	below.OutputConfig = &OutputConfig{TaskBudget: TokenTaskBudget(MinTaskBudgetTokens - 1)}
	if _, err := inf.CreateMessage(context.Background(), below); err == nil {
		t.Fatalf("a task_budget below %d must error before the call", MinTaskBudgetTokens)
	} else if !strings.Contains(err.Error(), "minimum") {
		t.Errorf("error should name the minimum: %v", err)
	}

	atMin := base
	atMin.OutputConfig = &OutputConfig{TaskBudget: TokenTaskBudget(MinTaskBudgetTokens)}
	if _, err := inf.CreateMessage(context.Background(), atMin); err != nil {
		t.Errorf("a task_budget at the minimum must be accepted: %v", err)
	}
}

// TestCompactionRoundTrip proves a server-side compaction block (D5) survives the
// AppendAssistantTurn round-trip byte-identically and that Text() ignores it.
func TestCompactionRoundTrip(t *testing.T) {
	body := `{"id":"msg_c","type":"message","role":"assistant","model":"claude-opus-4-8",` +
		`"stop_reason":"end_turn","content":[` +
		`{"type":"compaction","compacted_summary":"earlier turns summarized","extra":{"k":1}},` +
		`{"type":"text","text":"answer"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": body}}
	inf := newInf(d, model.GatewayDirect)
	resp, err := inf.CreateMessage(context.Background(), MessageRequest{
		MaxTokens:         8,
		Messages:          []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
		ContextManagement: CompactionContextManagement(),
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if !resp.HasCompaction() {
		t.Error("HasCompaction should be true (a compaction block was returned)")
	}
	if resp.Text() != "answer" {
		t.Errorf("Text() = %q, want %q (compaction block must not leak into text)", resp.Text(), "answer")
	}
	// The round-trip preserves the compaction block verbatim, including fields the
	// connector does not model (compacted_summary, extra) — appending only the text
	// would drop the compaction state the next request needs.
	msgs := AppendAssistantTurn(nil, resp)
	wire, err := json.Marshal(msgs[0].Content)
	if err != nil {
		t.Fatalf("marshal round-tripped content: %v", err)
	}
	if !strings.Contains(string(wire), `"compacted_summary":"earlier turns summarized"`) ||
		!strings.Contains(string(wire), `"extra":{"k":1}`) {
		t.Errorf("compaction block not preserved on round-trip: %s", wire)
	}
}

// TestStructuredOutputs_SupportGateAndShape proves the D6 support gate and the GA
// output_config.format shape (no beta header).
func TestStructuredOutputs_SupportGateAndShape(t *testing.T) {
	for _, id := range []string{"claude-opus-4-8", "claude-sonnet-4-6", "claude-haiku-4-5", "claude-opus-4-1", "claude-opus-4-5"} {
		if !SupportsStructuredOutputs(id) {
			t.Errorf("%s should support structured outputs", id)
		}
	}
	for _, id := range []string{"claude-opus-4-0", "claude-3-5-sonnet", "claude-3-opus", ""} {
		if SupportsStructuredOutputs(id) {
			t.Errorf("%s should NOT support structured outputs", id)
		}
	}
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": okMsg}}
	inf := newInf(d, model.GatewayDirect)
	if _, err := inf.CreateMessage(context.Background(), MessageRequest{
		MaxTokens:    8,
		Messages:     []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
		OutputConfig: &OutputConfig{Format: JSONSchemaFormat(json.RawMessage(`{"type":"object","additionalProperties":false}`))},
	}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if !strings.Contains(d.lastBody, `"format":{"type":"json_schema","schema":{"type":"object","additionalProperties":false}}`) {
		t.Errorf("structured-outputs format not serialized as output_config.format: %s", d.lastBody)
	}
	// GA: structured outputs add NO beta header.
	if d.lastBeta != "" {
		t.Errorf("structured outputs must send no beta header (GA), got %q", d.lastBeta)
	}
	if strings.Contains(d.lastBody, "output_format") {
		t.Errorf("the deprecated top-level output_format must never be sent: %s", d.lastBody)
	}
}

// TestServiceTier_RequestValidation proves D8: only auto|standard_only are legal on a
// request; an assigned-tier value is rejected before the call.
func TestServiceTier_RequestValidation(t *testing.T) {
	for _, ok := range []string{"", ServiceTierAuto, ServiceTierStandardOnly} {
		if !ValidRequestServiceTier(ok) {
			t.Errorf("%q should be a valid request service_tier", ok)
		}
	}
	for _, bad := range []string{ServiceTierPriority, ServiceTierStandard, ServiceTierBatch, "flex"} {
		if ValidRequestServiceTier(bad) {
			t.Errorf("%q must NOT be a valid request service_tier", bad)
		}
	}
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": okMsg}}
	inf := newInf(d, model.GatewayDirect)
	if _, err := inf.CreateMessage(context.Background(), MessageRequest{
		MaxTokens:   8,
		ServiceTier: ServiceTierPriority,
		Messages:    []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	}); err == nil {
		t.Error("service_tier=priority on a request must be rejected client-side (it 400s upstream)")
	}
}

// TestSearchResultBlock_Shape proves D2's verified wire shape, with and without
// citations, and that it round-trips losslessly through the content marshaller.
func TestSearchResultBlock_Shape(t *testing.T) {
	b := SearchResultBlock("https://example.com/a", "Article", []string{"the content"}, true)
	if b.Type != blockSearchResult {
		t.Errorf("type = %q, want search_result", b.Type)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"type":"search_result"`,
		`"source":"https://example.com/a"`,
		`"title":"Article"`,
		`"content":[{"type":"text","text":"the content"}]`,
		`"citations":{"enabled":true}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("search_result block missing %q in %s", want, got)
		}
	}
	// citations omitted when disabled.
	noCite, _ := json.Marshal(SearchResultBlock("s", "t", []string{"x"}, false))
	if strings.Contains(string(noCite), "citations") {
		t.Errorf("citations must be omitted when disabled: %s", noCite)
	}
}

// TestContentBlock_RoundTripPreservesText proves a decoded text block re-serializes
// without losing its cache_control (lossless raw capture does not corrupt known fields).
func TestContentBlock_RoundTripPreservesText(t *testing.T) {
	in := `{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"1h"}}`
	var cb ContentBlock
	if err := json.Unmarshal([]byte(in), &cb); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cb.Type != "text" || cb.Text != "hello" {
		t.Errorf("typed fields not populated: %+v", cb)
	}
	out, _ := json.Marshal(cb)
	if string(out) != in {
		t.Errorf("round-trip changed bytes:\n got %s\nwant %s", out, in)
	}
}

// TestBetaHeaders_Stable proves BetaHeaders de-duplicates and reports only what the
// request actually uses.
func TestBetaHeaders_Stable(t *testing.T) {
	req := MessageRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{TextBlock("a")}},
			SystemMessage("p"),
			SystemMessage("q"), // two system messages → mid-conv header once
		},
	}
	got := req.BetaHeaders()
	if len(got) != 1 || got[0] != BetaMidConversationSystem {
		t.Errorf("BetaHeaders = %v, want [%s] (deduped)", got, BetaMidConversationSystem)
	}
	if betaHeaderMap(nil) != nil {
		t.Error("betaHeaderMap(nil) must be nil (no header sent)")
	}
}
