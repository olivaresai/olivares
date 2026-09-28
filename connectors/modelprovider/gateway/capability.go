// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// Route is the invocation path Effective judges a catalog against.
type Route string

// RouteGateway is this seam. Text is the only message shape. Streaming is
// the only capability flag Effective keeps, and only when the catalog
// already lists it. The raw-body path does not stream.
const RouteGateway Route = "gateway"

// Effective returns the capabilities route can invoke from catalog.
// RouteGateway keeps streaming and drops every other flag, including
// tool_use, vision, structured_outputs and extended_thinking. A catalog
// bit is not invocation. Any other route is outside this seam and yields
// no flags here.
func Effective(catalog []modelprovider.Capability, route Route) []modelprovider.Capability {
	out := []modelprovider.Capability{}
	if route != RouteGateway {
		return out
	}
	var saw bool
	for _, c := range catalog {
		if c == modelprovider.CapStreaming && !saw {
			out = append(out, c)
			saw = true
		}
	}
	return out
}

// Refuse accepts one text chat body and rejects every other shape with
// CodeNotImplemented. The accepted body has only model, messages, and
// max_tokens. Each message has only role and string content. role is
// system, user, or assistant. Null or empty tool fields, a null
// response_format, stream, and text-only content blocks are refused:
// they are not treated as absent. A tool, an image part, a JSON schema,
// a Responses item, or a vendor turn this contract cannot carry is
// rejected. Refuse does not encode and does not call a doer.
func Refuse(raw []byte) error {
	_, err := admit(raw)
	return err
}

// OfferText sends raw through driver only when admit accepts it as text.
// It is non-stream only. A stream uses Driver.StreamMessage with a typed
// MessageRequest, not a raw body. The driver then encodes that
// MessageRequest as it does today. An unsupported body returns before
// CreateMessage, so the doer sees nothing.
func OfferText(ctx context.Context, driver Driver, raw []byte) (MessageResponse, error) {
	req, err := admit(raw)
	if err != nil {
		return MessageResponse{}, err
	}
	return driver.CreateMessage(ctx, req)
}

func admit(raw []byte) (MessageRequest, error) {
	if err := refuseUnsupported(raw); err != nil {
		return MessageRequest{}, err
	}
	return parseText(raw)
}

// refuseUnsupported is the pre-send guard. It must stay ahead of parseText:
// parseText ignores JSON keys the text struct does not name, which would
// otherwise drop tools, thinking, or a reasoning item and send the rest.
// Duplicate keys are refused the same way chat-text objects are: a later
// value must not hide an earlier turn.
func refuseUnsupported(raw []byte) error {
	top, kind := scanObject(raw)
	switch kind {
	case scanSyntax:
		return &Error{Code: CodeBadRequest, HTTPStatus: http.StatusBadRequest, Message: "invalid text request"}
	case scanNotObject, scanDuplicate:
		return textOnly()
	}
	for key := range top {
		switch key {
		case "model", "messages", "max_tokens":
		default:
			return textOnly()
		}
	}
	msgRaw, ok := top["messages"]
	if !ok {
		return nil
	}
	msgs, kind := scanArray(msgRaw)
	if kind != scanOK {
		return textOnly()
	}
	for _, msg := range msgs {
		obj, kind := scanObject(msg)
		if kind != scanOK {
			return textOnly()
		}
		for key, val := range obj {
			switch key {
			case "role":
				var role string
				if json.Unmarshal(val, &role) != nil || !textRole(role) {
					return textOnly()
				}
			case "content":
				var text string
				if err := json.Unmarshal(val, &text); err != nil {
					return textOnly()
				}
			default:
				return textOnly()
			}
		}
	}
	return nil
}

type scanKind int

const (
	scanOK scanKind = iota
	scanSyntax
	scanNotObject
	scanDuplicate
)

// scanObject walks one JSON object and keeps each raw value. A repeated
// key is scanDuplicate so the caller can refuse it instead of keeping
// the last value, matching chatTextObject.
func scanObject(raw []byte) (map[string]json.RawMessage, scanKind) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		if json.Valid(raw) {
			return nil, scanNotObject
		}
		return nil, scanSyntax
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		if json.Valid(raw) {
			return nil, scanNotObject
		}
		return nil, scanSyntax
	}
	obj := make(map[string]json.RawMessage)
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, scanSyntax
		}
		name, ok := key.(string)
		if !ok {
			return nil, scanSyntax
		}
		if _, dup := obj[name]; dup {
			return nil, scanDuplicate
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, scanSyntax
		}
		obj[name] = val
	}
	tok, err = dec.Token()
	if err != nil || tok != json.Delim('}') {
		return nil, scanSyntax
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, scanSyntax
	}
	return obj, scanOK
}

func scanArray(raw []byte) ([]json.RawMessage, scanKind) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		if json.Valid(raw) {
			return nil, scanNotObject
		}
		return nil, scanSyntax
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('[') {
		if json.Valid(raw) {
			return nil, scanNotObject
		}
		return nil, scanSyntax
	}
	var items []json.RawMessage
	for dec.More() {
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, scanSyntax
		}
		items = append(items, val)
	}
	tok, err = dec.Token()
	if err != nil || tok != json.Delim(']') {
		return nil, scanSyntax
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, scanSyntax
	}
	return items, scanOK
}

func parseText(raw []byte) (MessageRequest, error) {
	var body struct {
		Model     string    `json:"model"`
		Messages  []Message `json:"messages"`
		MaxTokens int       `json:"max_tokens"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return MessageRequest{}, &Error{Code: CodeBadRequest, HTTPStatus: http.StatusBadRequest, Message: "invalid text request"}
	}
	req := MessageRequest{Model: body.Model, Messages: body.Messages, MaxTokens: body.MaxTokens}
	if err := ValidateRequest(req); err != nil {
		return MessageRequest{}, err
	}
	return req, nil
}

func textRole(role string) bool {
	return role == "system" || role == "user" || role == "assistant"
}

func textOnly() error {
	return &Error{
		Code:       CodeNotImplemented,
		HTTPStatus: http.StatusNotImplemented,
		Message:    "gateway sends text only",
	}
}
