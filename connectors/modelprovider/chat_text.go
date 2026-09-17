// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChatTextErrorKind classifies a codec failure without carrying provider content.
type ChatTextErrorKind string

const (
	ChatTextInvalidRequest      ChatTextErrorKind = "invalid_request"
	ChatTextInvalidLimit        ChatTextErrorKind = "invalid_limit"
	ChatTextLimitExceeded       ChatTextErrorKind = "limit_exceeded"
	ChatTextInvalidJSON         ChatTextErrorKind = "invalid_json"
	ChatTextInvalidResponse     ChatTextErrorKind = "invalid_response"
	ChatTextUnsupportedResponse ChatTextErrorKind = "unsupported_response"
)

// ChatTextCodecError contains only a kind and a fixed schema field name. It never
// wraps a JSON error or includes a request/response value. It is not an HTTP error.
type ChatTextCodecError struct {
	Kind  ChatTextErrorKind
	Field string
}

func (e *ChatTextCodecError) Error() string {
	return "modelprovider: chat text: " + string(e.Kind) + " (" + e.Field + ")"
}

func chatTextError(kind ChatTextErrorKind, field string) error {
	return &ChatTextCodecError{Kind: kind, Field: field}
}

// ChatTextRequest is the deliberately narrow text.v1 operation: one user turn.
// MaxCompletionTokens bounds all generated tokens, including reasoning, rather
// than promising a visible-text length. Model support is the caller's concern.
// This codec performs no authorization, transport, token estimation or pricing.
type ChatTextRequest struct {
	Model               string
	Input               string
	MaxCompletionTokens int64
}

// PreparedChatTextRequest holds the exact serialized bytes and their SHA-256.
// Its zero value is unprepared. Copying the value is safe; Bytes returns a copy.
// No credentials, headers, endpoint or mutable request object are retained.
type PreparedChatTextRequest struct {
	body   string
	digest [sha256.Size]byte
}

// IsZero reports whether this value was not prepared successfully.
func (p PreparedChatTextRequest) IsZero() bool { return p.body == "" }

// Bytes returns a caller-owned copy of the exact prepared JSON, or nil if zero.
func (p PreparedChatTextRequest) Bytes() []byte {
	if p.IsZero() {
		return nil
	}
	return []byte(p.body)
}

// Digest returns the SHA-256 of Bytes, or the zero digest for an unprepared value.
func (p PreparedChatTextRequest) Digest() [sha256.Size]byte { return p.digest }

// PrepareChatTextRequest validates and serializes text.v1 exactly once. It does
// not trim or normalize the input. Invalid UTF-8 is rejected instead of silently
// being replaced by encoding/json. Size and policy limits belong to the caller.
func PrepareChatTextRequest(in ChatTextRequest) (PreparedChatTextRequest, error) {
	if !chatTextModelValid(in.Model) {
		return PreparedChatTextRequest{}, chatTextError(ChatTextInvalidRequest, "model")
	}
	if !utf8.ValidString(in.Input) || strings.TrimSpace(in.Input) == "" {
		return PreparedChatTextRequest{}, chatTextError(ChatTextInvalidRequest, "input")
	}
	if in.MaxCompletionTokens <= 0 {
		return PreparedChatTextRequest{}, chatTextError(ChatTextInvalidRequest, "max_completion_tokens")
	}
	body, err := json.Marshal(struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		MaxCompletionTokens int64 `json:"max_completion_tokens"`
		Stream              bool  `json:"stream"`
		N                   int   `json:"n"`
		Store               bool  `json:"store"`
	}{
		Model: in.Model,
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{{Role: "user", Content: in.Input}},
		MaxCompletionTokens: in.MaxCompletionTokens,
		N:                   1,
	})
	if err != nil {
		return PreparedChatTextRequest{}, chatTextError(ChatTextInvalidRequest, "request")
	}
	return PreparedChatTextRequest{body: string(body), digest: sha256.Sum256(body)}, nil
}

func chatTextModelValid(s string) bool {
	return s != "" && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
}

// ChatTextState distinguishes a normal stop, a length-limited result, content
// filtering and a refusal. FinishReason remains available even on a refusal.
type ChatTextState string

const (
	ChatTextStopped         ChatTextState = "stop"
	ChatTextLengthLimited   ChatTextState = "length"
	ChatTextContentFiltered ChatTextState = "content_filter"
	ChatTextRefused         ChatTextState = "refusal"
)

// ChatTextUsage preserves reported counters; nil means absent or null, never zero.
// Totals and detail counters are observations, not prices or inferred quantities.
type ChatTextUsage struct {
	PromptTokens            *int64
	CompletionTokens        *int64
	TotalTokens             *int64
	PromptTokensDetails     *ChatTextPromptTokensDetails
	CompletionTokensDetails *ChatTextCompletionTokensDetails
}

type ChatTextPromptTokensDetails struct {
	CachedTokens *int64
	AudioTokens  *int64
}

type ChatTextCompletionTokensDetails struct {
	ReasoningTokens          *int64
	AudioTokens              *int64
	AcceptedPredictionTokens *int64
	RejectedPredictionTokens *int64
}

// ChatTextResponse is one observed assistant choice. Model is the returned model,
// not a verified identity, route or permission. Text and Refusal distinguish null
// or absent from a present empty string. A nil Usage means absent or null usage.
type ChatTextResponse struct {
	Model        string
	Text         *string
	Refusal      *string
	FinishReason string
	State        ChatTextState
	Usage        *ChatTextUsage
}

// DecodeChatTextResponse decodes one complete JSON body, including any surrounding
// whitespace, within the caller's positive byte limit. The caller must separately
// bound transport reads; this pure codec never reads a stream or performs I/O.
// Unknown fields are accepted. Known tool/function/audio payloads, multimodal
// content, duplicate keys in inspected objects and unknown finish reasons fail.
// Every error returns a zero response, never a partially decoded success.
func DecodeChatTextResponse(body []byte, maxBytes int) (ChatTextResponse, error) {
	if maxBytes <= 0 {
		return ChatTextResponse{}, chatTextError(ChatTextInvalidLimit, "response")
	}
	if len(body) > maxBytes {
		return ChatTextResponse{}, chatTextError(ChatTextLimitExceeded, "response")
	}
	if !utf8.Valid(body) || !json.Valid(body) || !chatTextUnicodeValid(body) {
		return ChatTextResponse{}, chatTextError(ChatTextInvalidJSON, "response")
	}
	result, err := decodeChatTextResponse(body)
	if err != nil {
		return ChatTextResponse{}, err
	}
	return result, nil
}

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD. Reject those
// escapes instead of changing the provider's text. The JSON syntax is already
// validated, so each escape here has its required bytes and hexadecimal digits.
func chatTextUnicodeValid(body []byte) bool {
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			continue
		}
		i++
		if body[i] != 'u' {
			continue
		}
		n, _ := strconv.ParseUint(string(body[i+1:i+5]), 16, 16)
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
				return false
			}
			low, _ := strconv.ParseUint(string(body[i+3:i+7]), 16, 16)
			if low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func decodeChatTextResponse(body []byte) (ChatTextResponse, error) {
	root, err := chatTextObject(body, "response")
	if err != nil {
		return ChatTextResponse{}, err
	}
	if chatTextPresent(root["error"]) {
		return ChatTextResponse{}, chatTextError(ChatTextInvalidResponse, "error")
	}
	if err := chatTextUnsupported(root); err != nil {
		return ChatTextResponse{}, err
	}
	if raw, ok := root["object"]; ok {
		var object string
		if json.Unmarshal(raw, &object) != nil || object != "chat.completion" {
			return ChatTextResponse{}, chatTextError(ChatTextInvalidResponse, "object")
		}
	}
	var result ChatTextResponse
	if json.Unmarshal(root["model"], &result.Model) != nil || !chatTextModelValid(result.Model) {
		return result, chatTextError(ChatTextInvalidResponse, "model")
	}
	var choices []json.RawMessage
	if json.Unmarshal(root["choices"], &choices) != nil || len(choices) != 1 {
		return result, chatTextError(ChatTextInvalidResponse, "choices")
	}
	choice, err := chatTextObject(choices[0], "choice")
	if err != nil {
		return result, err
	}
	index, err := chatTextCounter(choice["index"], "choice.index")
	if err != nil || index == nil || *index != 0 {
		return result, chatTextError(ChatTextInvalidResponse, "choice.index")
	}
	if err := chatTextUnsupported(choice); err != nil {
		return result, err
	}
	if json.Unmarshal(choice["finish_reason"], &result.FinishReason) != nil {
		return result, chatTextError(ChatTextInvalidResponse, "finish_reason")
	}
	switch result.FinishReason {
	case "stop":
		result.State = ChatTextStopped
	case "length":
		result.State = ChatTextLengthLimited
	case "content_filter":
		result.State = ChatTextContentFiltered
	case "tool_calls", "function_call":
		return result, chatTextError(ChatTextUnsupportedResponse, "finish_reason")
	default:
		return result, chatTextError(ChatTextInvalidResponse, "finish_reason")
	}
	message, err := chatTextObject(choice["message"], "message")
	if err != nil {
		return result, err
	}
	var role string
	if json.Unmarshal(message["role"], &role) != nil || role != "assistant" {
		return result, chatTextError(ChatTextInvalidResponse, "message.role")
	}
	if err := chatTextUnsupported(message); err != nil {
		return result, err
	}
	if result.Text, err = chatTextString(message["content"], "message.content"); err != nil {
		return result, err
	}
	if result.Refusal, err = chatTextString(message["refusal"], "message.refusal"); err != nil {
		return result, err
	}
	if result.Refusal != nil {
		result.State = ChatTextRefused
	} else if result.State == ChatTextStopped && result.Text == nil {
		return result, chatTextError(ChatTextInvalidResponse, "message.content")
	}
	result.Usage, err = chatTextUsage(root["usage"])
	return result, err
}

func chatTextPresent(raw json.RawMessage) bool {
	return len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// These fields cannot be treated as ignorable extensions on a text-only result.
// Null means no payload. An empty tool_calls array also contains no calls.
func chatTextUnsupported(object map[string]json.RawMessage) error {
	for _, field := range []string{"tools", "function_call", "audio", "tool_calls"} {
		raw := object[field]
		if !chatTextPresent(raw) {
			continue
		}
		if field == "tool_calls" {
			var calls []json.RawMessage
			if json.Unmarshal(raw, &calls) == nil && len(calls) == 0 {
				continue
			}
		}
		return chatTextError(ChatTextUnsupportedResponse, field)
	}
	return nil
}

// Inspect one object with exact JSON key spelling, refusing duplicate keys so an
// earlier tool call or usage value cannot disappear behind a later null/zero.
func chatTextObject(raw json.RawMessage, field string) (map[string]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, chatTextError(ChatTextInvalidResponse, field)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if _, err := d.Token(); err != nil {
		return nil, chatTextError(ChatTextInvalidResponse, field)
	}
	object := make(map[string]json.RawMessage)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, chatTextError(ChatTextInvalidResponse, field)
		}
		name, ok := key.(string)
		if !ok {
			return nil, chatTextError(ChatTextInvalidResponse, field)
		}
		if _, duplicate := object[name]; duplicate {
			return nil, chatTextError(ChatTextInvalidResponse, field)
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, chatTextError(ChatTextInvalidResponse, field)
		}
		object[name] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, chatTextError(ChatTextInvalidResponse, field)
	}
	return object, nil
}

func chatTextString(raw json.RawMessage, field string) (*string, error) {
	if !chatTextPresent(raw) {
		return nil, nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil, chatTextError(ChatTextUnsupportedResponse, field)
	}
	return &value, nil
}

func chatTextCounter(raw json.RawMessage, field string) (*int64, error) {
	if !chatTextPresent(raw) {
		return nil, nil
	}
	var value int64
	if json.Unmarshal(raw, &value) != nil || value < 0 {
		return nil, chatTextError(ChatTextInvalidResponse, field)
	}
	return &value, nil
}

func chatTextUsage(raw json.RawMessage) (*ChatTextUsage, error) {
	if !chatTextPresent(raw) {
		return nil, nil
	}
	object, err := chatTextObject(raw, "usage")
	if err != nil {
		return nil, err
	}
	u := &ChatTextUsage{}
	for _, counter := range []struct {
		name string
		dst  **int64
	}{{"prompt_tokens", &u.PromptTokens}, {"completion_tokens", &u.CompletionTokens}, {"total_tokens", &u.TotalTokens}} {
		if *counter.dst, err = chatTextCounter(object[counter.name], "usage."+counter.name); err != nil {
			return nil, err
		}
	}
	if raw := object["prompt_tokens_details"]; chatTextPresent(raw) {
		details, err := chatTextObject(raw, "usage.prompt_tokens_details")
		if err != nil {
			return nil, err
		}
		d := &ChatTextPromptTokensDetails{}
		if d.CachedTokens, err = chatTextCounter(details["cached_tokens"], "usage.cached_tokens"); err != nil {
			return nil, err
		}
		if d.AudioTokens, err = chatTextCounter(details["audio_tokens"], "usage.prompt_audio_tokens"); err != nil {
			return nil, err
		}
		u.PromptTokensDetails = d
	}
	if raw := object["completion_tokens_details"]; chatTextPresent(raw) {
		details, err := chatTextObject(raw, "usage.completion_tokens_details")
		if err != nil {
			return nil, err
		}
		d := &ChatTextCompletionTokensDetails{}
		for _, counter := range []struct {
			name string
			dst  **int64
		}{{"reasoning_tokens", &d.ReasoningTokens}, {"audio_tokens", &d.AudioTokens}, {"accepted_prediction_tokens", &d.AcceptedPredictionTokens}, {"rejected_prediction_tokens", &d.RejectedPredictionTokens}} {
			if *counter.dst, err = chatTextCounter(details[counter.name], "usage."+counter.name); err != nil {
				return nil, err
			}
		}
		u.CompletionTokensDetails = d
	}
	return u, nil
}
