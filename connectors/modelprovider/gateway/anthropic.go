// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"encoding/json"
	"strings"
)

const defaultAnthropicVersion = "2023-06-01"

// AnthropicDriver speaks POST /v1/messages (HTTP JSON and SSE).
type AnthropicDriver struct{ *httpDriver }

var _ Driver = (*AnthropicDriver)(nil)

func NewAnthropic(cfg Config) (*AnthropicDriver, error) {
	extra := copyHeaders(cfg.Headers)
	if extra == nil {
		extra = map[string]string{}
	}
	if extra["anthropic-version"] == "" {
		extra["anthropic-version"] = defaultAnthropicVersion
	}
	d, err := newHTTPDriver(cfg, DriverAnthropic, ProtocolAnthropic, "/v1/messages", extra)
	if err != nil {
		return nil, err
	}
	d.encode = encodeAnthropic
	d.decode = decodeAnthropic
	d.parse = parseAnthropicSSE
	return &AnthropicDriver{d}, nil
}

type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Stream    bool               `json:"stream,omitempty"`
}

type anthropicResponse struct {
	ID         string           `json:"id"`
	Model      string           `json:"model"`
	Role       string           `json:"role"`
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func encodeAnthropic(req MessageRequest, stream bool) (any, error) {
	out := anthropicRequest{Model: req.Model, MaxTokens: req.MaxTokens, Stream: stream}
	for _, m := range req.Messages {
		role := m.Role
		if role == "system" {
			if out.System != "" {
				out.System += "\n"
			}
			out.System += m.Content
			continue
		}
		if role == "" {
			role = "user"
		}
		out.Messages = append(out.Messages, anthropicMessage{
			Role:    role,
			Content: []anthropicBlock{{Type: "text", Text: m.Content}},
		})
	}
	if len(out.Messages) == 0 {
		return nil, &Error{Code: CodeBadRequest, HTTPStatus: 400, Message: "messages are required"}
	}
	return out, nil
}

func decodeAnthropic(raw json.RawMessage) (MessageResponse, error) {
	var in anthropicResponse
	if err := decodeJSON(raw, &in); err != nil {
		return MessageResponse{}, err
	}
	var b strings.Builder
	for _, c := range in.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	role := in.Role
	if role == "" {
		role = "assistant"
	}
	return MessageResponse{
		ID:    in.ID,
		Model: in.Model,
		Role:  role,
		Text:  b.String(),
		Stop:  in.StopReason,
		Usage: Usage{InputTokens: in.Usage.InputTokens, OutputTokens: in.Usage.OutputTokens},
	}, nil
}
