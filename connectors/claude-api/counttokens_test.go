// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestCountTokens_ParsesAndOmitsRuntimeParams(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"POST /v1/messages/count_tokens": `{"input_tokens":1551}`,
	}}
	inf := newInf(d, model.GatewayDirect)
	tc, err := inf.CountTokens(context.Background(), MessageRequest{
		MaxTokens:     64000, // a runtime param that must NOT reach count_tokens
		ServiceTier:   ServiceTierAuto,
		StopSequences: []string{"STOP"},
		System:        []ContentBlock{CachedTextBlock("rubric", "")},
		Messages:      []Message{{Role: "user", Content: []ContentBlock{TextBlock("hello")}}},
	})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if tc.InputTokens != 1551 {
		t.Errorf("input_tokens = %d, want 1551", tc.InputTokens)
	}
	if !strings.Contains(d.lastURL, "/v1/messages/count_tokens") {
		t.Errorf("did not hit count_tokens: %s", d.lastURL)
	}
	// Prompt-shaping fields are forwarded; runtime params are not.
	if !strings.Contains(d.lastBody, `"claude-opus-4-8"`) || !strings.Contains(d.lastBody, "hello") {
		t.Errorf("prompt not forwarded: %s", d.lastBody)
	}
	for _, banned := range []string{`"max_tokens"`, `"stream"`, `"service_tier"`, `"stop_sequences"`} {
		if strings.Contains(d.lastBody, banned) {
			t.Errorf("count_tokens body carried runtime param %s: %s", banned, d.lastBody)
		}
	}
}

func TestCountTokens_NormalizesThinkingForModel(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"POST /v1/messages/count_tokens": `{"input_tokens":88}`,
	}}
	inf := newInf(d, model.GatewayDirect) // default claude-opus-4-8 rejects the legacy budget
	_, err := inf.CountTokens(context.Background(), MessageRequest{
		Thinking: EnabledThinking(16000), // legacy fixed budget
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("q")}}},
	})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	// The count must mirror what CreateMessage will send: budget rewritten to adaptive.
	if !strings.Contains(d.lastBody, `"thinking":{"type":"adaptive"}`) {
		t.Errorf("thinking not normalized for count_tokens: %s", d.lastBody)
	}
	if strings.Contains(d.lastBody, `"budget_tokens"`) {
		t.Errorf("legacy budget leaked into count_tokens body: %s", d.lastBody)
	}
}

func TestCountTokens_NotConfigured(t *testing.T) {
	inf := &Inference{}
	if _, err := inf.CountTokens(context.Background(), MessageRequest{Model: "m"}); err != ErrNotConfigured {
		t.Errorf("want ErrNotConfigured, got %v", err)
	}
}
