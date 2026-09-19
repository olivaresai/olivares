// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import "encoding/json"

// OpenAIDriver speaks OpenAI-compatible POST /v1/chat/completions (HTTP JSON
// and SSE). LiteLLM and Bifrost OpenAI surfaces use this driver.
type OpenAIDriver struct{ *httpDriver }

var _ Driver = (*OpenAIDriver)(nil)

func NewOpenAICompat(cfg Config) (*OpenAIDriver, error) {
	d, err := newHTTPDriver(cfg, DriverOpenAICompat, ProtocolOpenAICompat, "/v1/chat/completions", copyHeaders(cfg.Headers))
	if err != nil {
		return nil, err
	}
	d.encode = encodeOpenAI
	d.decode = decodeOpenAI
	d.parse = parseOpenAISSE
	return &OpenAIDriver{d}, nil
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type openaiChatRequest struct {
	Model      string            `json:"model"`
	Messages   []openaiMessage   `json:"messages"`
	MaxTokens  int               `json:"max_tokens,omitempty"`
	Stream     bool              `json:"stream,omitempty"`
	StreamOpts *openaiStreamOpts `json:"stream_options,omitempty"`
	N          int               `json:"n,omitempty"`
}

type openaiChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

func encodeOpenAI(req MessageRequest, stream bool) (any, error) {
	out := openaiChatRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		N:         1,
	}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, openaiMessage{Role: m.Role, Content: m.Content})
	}
	if stream {
		out.Stream = true
		out.StreamOpts = &openaiStreamOpts{IncludeUsage: true}
	}
	return out, nil
}

func decodeOpenAI(raw json.RawMessage) (MessageResponse, error) {
	var in openaiChatResponse
	if err := decodeJSON(raw, &in); err != nil {
		return MessageResponse{}, err
	}
	out := MessageResponse{ID: in.ID, Model: in.Model, Role: "assistant"}
	if len(in.Choices) > 0 {
		out.Text = in.Choices[0].Message.Content
		out.Stop = in.Choices[0].FinishReason
		if in.Choices[0].Message.Role != "" {
			out.Role = in.Choices[0].Message.Role
		}
	}
	if in.Usage != nil {
		out.Usage = Usage{InputTokens: in.Usage.PromptTokens, OutputTokens: in.Usage.CompletionTokens}
	}
	return out, nil
}

func copyHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
