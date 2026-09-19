// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import "encoding/json"

// OllamaDriver speaks POST /api/chat. Streaming is HTTP chunked NDJSON, not SSE.
type OllamaDriver struct{ *httpDriver }

var _ Driver = (*OllamaDriver)(nil)

func NewOllama(cfg Config) (*OllamaDriver, error) {
	d, err := newHTTPDriver(cfg, DriverOllama, ProtocolOllama, "/api/chat", copyHeaders(cfg.Headers))
	if err != nil {
		return nil, err
	}
	d.encode = encodeOllama
	d.decode = decodeOllama
	d.parse = parseOllamaNDJSON
	return &OllamaDriver{d}, nil
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatRequest struct {
	Model     string          `json:"model"`
	Messages  []ollamaMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	KeepAlive string          `json:"keep_alive,omitempty"`
	Options   *struct {
		NumPredict int `json:"num_predict,omitempty"`
	} `json:"options,omitempty"`
}

type ollamaChatResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done            bool  `json:"done"`
	PromptEvalCount int64 `json:"prompt_eval_count"`
	EvalCount       int64 `json:"eval_count"`
}

func encodeOllama(req MessageRequest, stream bool) (any, error) {
	out := ollamaChatRequest{Model: req.Model, Stream: stream}
	for _, m := range req.Messages {
		role := m.Role
		if role == "" {
			role = "user"
		}
		out.Messages = append(out.Messages, ollamaMessage{Role: role, Content: m.Content})
	}
	if req.MaxTokens > 0 {
		out.Options = &struct {
			NumPredict int `json:"num_predict,omitempty"`
		}{NumPredict: req.MaxTokens}
	}
	return out, nil
}

func decodeOllama(raw json.RawMessage) (MessageResponse, error) {
	var in ollamaChatResponse
	if err := decodeJSON(raw, &in); err != nil {
		return MessageResponse{}, err
	}
	role := in.Message.Role
	if role == "" {
		role = "assistant"
	}
	return MessageResponse{
		Model: in.Model,
		Role:  role,
		Text:  in.Message.Content,
		Usage: Usage{InputTokens: in.PromptEvalCount, OutputTokens: in.EvalCount},
	}, nil
}
