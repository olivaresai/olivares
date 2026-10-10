// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// PrepareDeepSeekTextRequest is the version-1 DeepSeek text adapter: one user
// turn, non-streaming and explicitly non-thinking. It emits only documented
// fields. The generic text.v1 codec remains unchanged.
// Contract: https://api-docs.deepseek.com/api/create-chat-completion/ (2026-10-02).
func PrepareDeepSeekTextRequest(in ChatTextRequest) (PreparedChatTextRequest, error) {
	if !chatTextModelValid(in.Model) {
		return PreparedChatTextRequest{}, chatTextError(ChatTextInvalidRequest, "model")
	}
	if !utf8.ValidString(in.Input) || strings.TrimSpace(in.Input) == "" {
		return PreparedChatTextRequest{}, chatTextError(ChatTextInvalidRequest, "input")
	}
	if in.MaxCompletionTokens <= 0 || in.MaxCompletionTokens > 393216 {
		return PreparedChatTextRequest{}, chatTextError(ChatTextInvalidRequest, "max_tokens")
	}
	body, err := json.Marshal(struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		MaxTokens int64 `json:"max_tokens"`
		Stream    bool  `json:"stream"`
		Thinking  struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}{
		Model: in.Model,
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{{Role: "user", Content: in.Input}},
		MaxTokens: in.MaxCompletionTokens,
		Thinking: struct {
			Type string `json:"type"`
		}{Type: "disabled"},
	})
	if err != nil {
		return PreparedChatTextRequest{}, chatTextError(ChatTextInvalidRequest, "request")
	}
	return PreparedChatTextRequest{body: string(body), digest: sha256.Sum256(body)}, nil
}
