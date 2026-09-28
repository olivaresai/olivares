// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// JudgeProtocolContext retains only a digest of non-secret transport configuration.
// Build it beside NewInference from the same config; APIKey and Doer are excluded.
// The zero value is deliberately unknown, never a fabricated direct-provider pin.
type JudgeProtocolContext struct{ digest string }

func NewJudgeProtocolContext(cfg InferenceConfig) JudgeProtocolContext {
	endpoint, version := cfg.BaseURL, cfg.AnthropicVersion
	if endpoint == "" {
		endpoint = defaultBaseURL
	}
	if version == "" {
		version = defaultAnthropicVersion
	}
	b, _ := json.Marshal(struct{ Endpoint, APIVersion string }{endpoint, version})
	sum := sha256.Sum256(b)
	return JudgeProtocolContext{digest: hex.EncodeToString(sum[:])}
}

// JudgeProtocol is a declared effective scoring protocol, not proof of immutable
// remote weights. Model is exactly the identifier sent after default resolution.
type JudgeProtocol struct {
	Implementation string
	Version        string
	Provider       string
	Model          string
	ConfigDigest   string
}

// DescribeJudge uses the same defaults, structured-output decision and request
// normalization as JudgeWithResponse. Fixture bodies and credentials are excluded.
func (inf *Inference) DescribeJudge(modelRef string, context JudgeProtocolContext) (JudgeProtocol, bool) {
	if inf == nil || inf.client == nil || context.digest == "" {
		return JudgeProtocol{}, false
	}
	modelID := modelRef
	if modelID == "" {
		modelID = inf.defaultModel
	}
	req := MessageRequest{Model: modelID, MaxTokens: judgeMaxTokens, System: []ContentBlock{CachedTextBlock(judgeSystemPrompt, "")}}
	if SupportsStructuredOutputs(modelID) {
		req.OutputConfig = &OutputConfig{Format: JSONSchemaFormat(judgeOutputSchema)}
	}
	effective, err := NormalizeMessageRequest(req, inf.defaultModel)
	if err != nil {
		return JudgeProtocol{}, false
	}
	b, err := json.Marshal(struct {
		Request                     MessageRequest
		Transport, Parser, Assembly string
	}{effective, context.digest, "parseJudgeVerdict-v1", "criterion-input-expected-output-v1"})
	if err != nil {
		return JudgeProtocol{}, false
	}
	sum := sha256.Sum256(b)
	return JudgeProtocol{Implementation: "claude-api/judge", Version: "1", Provider: "anthropic/" + string(inf.gateway), Model: effective.Model, ConfigDigest: hex.EncodeToString(sum[:])}, true
}
