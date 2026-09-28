// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"encoding/json"
	"testing"
)

func TestJudgeProtocolMatchesEffectiveRequest(t *testing.T) {
	d := &routeDoer{routes: map[string]string{"POST /v1/messages": `{"id":"fixture","model":"claude-opus-4-8","content":[{"type":"text","text":"{\"score\":1,\"passed\":true,\"reason\":\"ok\"}"}]}`}}
	cfg := InferenceConfig{APIKey: "fixture-key", DefaultModel: "claude-opus-4-8", Doer: d}
	inf := NewInference(cfg)
	contextPin := NewJudgeProtocolContext(cfg)
	protocol, ok := inf.DescribeJudge("", contextPin)
	if !ok {
		t.Fatal("known configured default became unknown")
	}
	_, resp, err := inf.JudgeWithResponse(context.Background(), JudgeInput{Output: "answer", Criterion: "correct"})
	if err != nil {
		t.Fatal(err)
	}
	var request MessageRequest
	if err := json.Unmarshal([]byte(d.lastBody), &request); err != nil {
		t.Fatal(err)
	}
	if protocol.Model != request.Model || protocol.Model != resp.Model || protocol.ConfigDigest == "" {
		t.Fatalf("descriptor did not describe effective request: %+v request=%s response=%s", protocol, request.Model, resp.Model)
	}
	explicit, ok := inf.DescribeJudge(cfg.DefaultModel, contextPin)
	if !ok || explicit != protocol {
		t.Fatal("explicit and default resolution disagree")
	}
	cfg.APIKey = "another-fixture-key"
	if rotated := NewJudgeProtocolContext(cfg); rotated != contextPin {
		t.Fatal("credential rotation changed scoring identity")
	}
	cfg.AnthropicVersion = "different-declared-protocol"
	changed, ok := inf.DescribeJudge("", NewJudgeProtocolContext(cfg))
	if !ok || changed.ConfigDigest == protocol.ConfigDigest {
		t.Fatal("API protocol change not captured")
	}
	if _, ok := inf.DescribeJudge("", JudgeProtocolContext{}); ok {
		t.Fatal("missing transport capture was fabricated")
	}
	unknown := NewInference(InferenceConfig{})
	if _, ok := unknown.DescribeJudge("", NewJudgeProtocolContext(InferenceConfig{})); ok {
		t.Fatal("missing actual model became known")
	}
	// An arbitrary declared alias is accepted; the descriptor does not assert that
	// the remote provider's weights are immutable or infer aliases from spelling.
	alias, ok := inf.DescribeJudge("deployment-alias", contextPin)
	if !ok || alias.Model != "deployment-alias" {
		t.Fatal("declared alias was reclassified by its name")
	}
}
