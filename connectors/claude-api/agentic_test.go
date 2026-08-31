// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestNormalizeThinkingForModel(t *testing.T) {
	// Legacy fixed budget → adaptive on a model that rejects it (display preserved).
	got := normalizeThinkingForModel("claude-opus-4-8", &Thinking{Type: ThinkingEnabled, BudgetTokens: 8000, Display: ThinkingDisplaySummarized})
	if got == nil || got.Type != ThinkingAdaptive || got.BudgetTokens != 0 || got.Display != ThinkingDisplaySummarized {
		t.Errorf("opus-4-8 enabled+budget should become adaptive (display kept): %+v", got)
	}
	// Fable 5 too (and Mythos 5).
	if g := normalizeThinkingForModel("claude-fable-5", EnabledThinking(8000)); g == nil || g.Type != ThinkingAdaptive {
		t.Errorf("fable-5 enabled+budget should become adaptive: %+v", g)
	}
	// Opus 4.6 still accepts the legacy budget (deprecated but functional) → unchanged.
	in := EnabledThinking(8000)
	if g := normalizeThinkingForModel("claude-opus-4-6", in); g != in {
		t.Errorf("opus-4-6 enabled+budget must pass through unchanged: %+v", g)
	}
	// Explicit disabled is dropped on the always-on Fable 5 / Mythos 5.
	if g := normalizeThinkingForModel("claude-fable-5", DisabledThinking()); g != nil {
		t.Errorf("fable-5 disabled must be dropped, got %+v", g)
	}
	if g := normalizeThinkingForModel("claude-mythos-5", DisabledThinking()); g != nil {
		t.Errorf("mythos-5 disabled must be dropped, got %+v", g)
	}
	// Disabled is fine on Opus 4.8 (accepted there).
	dis := DisabledThinking()
	if g := normalizeThinkingForModel("claude-opus-4-8", dis); g != dis {
		t.Errorf("opus-4-8 disabled must pass through: %+v", g)
	}
	// Adaptive and nil pass through.
	ad := AdaptiveThinking("")
	if g := normalizeThinkingForModel("claude-opus-4-8", ad); g != ad {
		t.Errorf("adaptive must pass through")
	}
	if g := normalizeThinkingForModel("claude-opus-4-8", nil); g != nil {
		t.Errorf("nil must pass through")
	}
}

func TestValidateToolChoice(t *testing.T) {
	if err := validateToolChoice(nil); err != nil {
		t.Errorf("nil tool_choice must be valid: %v", err)
	}
	for _, tc := range []*ToolChoice{AutoToolChoice(false), AnyToolChoice(true), NoToolChoice(), SpecificToolChoice("get_weather", false)} {
		if err := validateToolChoice(tc); err != nil {
			t.Errorf("valid tool_choice %+v rejected: %v", tc, err)
		}
	}
	if err := validateToolChoice(&ToolChoice{Type: ToolChoiceTypeTool}); err == nil {
		t.Errorf("tool_choice type tool with no name must error")
	}
	if err := validateToolChoice(&ToolChoice{Type: "bogus"}); err == nil {
		t.Errorf("unknown tool_choice type must error")
	}
}

func TestCreateMessage_WithholdsSamplingAndSendsAgenticParams(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"POST /v1/messages": `{"id":"m","type":"message","role":"assistant","model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":2}}`,
	}}
	inf := newInf(d, model.GatewayDirect) // default claude-opus-4-8 → rejects sampling params
	temp, topP := 0.7, 0.9
	topK := 40
	_, err := inf.CreateMessage(context.Background(), MessageRequest{
		MaxTokens:     64,
		Temperature:   &temp,
		TopP:          &topP,
		TopK:          &topK,
		StopSequences: []string{"END"},
		ToolChoice:    AnyToolChoice(true),
		Thinking:      EnabledThinking(8000), // legacy budget → normalized to adaptive
		Messages:      []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	// Sampling params withheld on the rejecting model.
	for _, banned := range []string{`"temperature"`, `"top_p"`, `"top_k"`, `"budget_tokens"`} {
		if strings.Contains(d.lastBody, banned) {
			t.Errorf("withheld/normalized param %s leaked: %s", banned, d.lastBody)
		}
	}
	// Agentic params that DO apply are sent.
	if !strings.Contains(d.lastBody, `"stop_sequences":["END"]`) {
		t.Errorf("stop_sequences not sent: %s", d.lastBody)
	}
	if !strings.Contains(d.lastBody, `"tool_choice":{"type":"any","disable_parallel_tool_use":true}`) {
		t.Errorf("tool_choice not sent: %s", d.lastBody)
	}
	if !strings.Contains(d.lastBody, `"thinking":{"type":"adaptive"}`) {
		t.Errorf("thinking not normalized to adaptive: %s", d.lastBody)
	}
}

func TestCreateMessage_PassesSamplingOnNonRejectingModel(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"POST /v1/messages": `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-5","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":5,"output_tokens":2}}`,
	}}
	inf := newInf(d, model.GatewayDirect)
	topP := 0.9
	topK := 40
	_, err := inf.CreateMessage(context.Background(), MessageRequest{
		Model:     "claude-sonnet-4-5", // does NOT reject sampling params
		MaxTokens: 64,
		TopP:      &topP,
		TopK:      &topK,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if !strings.Contains(d.lastBody, `"top_p":0.9`) || !strings.Contains(d.lastBody, `"top_k":40`) {
		t.Errorf("sampling params must pass through on a non-rejecting model: %s", d.lastBody)
	}
}

func TestCreateMessage_RejectsToolChoiceWithoutName(t *testing.T) {
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": `{}`}}
	inf := newInf(d, model.GatewayDirect)
	_, err := inf.CreateMessage(context.Background(), MessageRequest{
		MaxTokens:  8,
		ToolChoice: &ToolChoice{Type: ToolChoiceTypeTool}, // no name
		Messages:   []Message{{Role: "user", Content: []ContentBlock{TextBlock("x")}}},
	})
	if err == nil || !strings.Contains(err.Error(), "requires a tool name") {
		t.Fatalf("want tool_choice name error, got %v", err)
	}
}
