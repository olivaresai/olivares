// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// Protocol is the upstream request dialect a driver speaks.
type Protocol string

const (
	ProtocolOpenAICompat Protocol = "openai-compatible"
	ProtocolAnthropic    Protocol = "anthropic-messages"
	ProtocolOllama       Protocol = "ollama"
)

// Transport is how bytes move. HTTP is a blocking JSON round-trip. SSE is
// server-sent events. WebSocket is named because the matrix asks; no driver
// here uses it.
type Transport string

const (
	TransportHTTP Transport = "http"
	TransportSSE  Transport = "sse"
	TransportWS   Transport = "websocket"
)

// DriverName identifies a concrete adapter.
type DriverName string

const (
	DriverOpenAICompat DriverName = "openai-compat"
	DriverAnthropic    DriverName = "anthropic-messages"
	DriverOllama       DriverName = "ollama"
)

// CellStatus is the honesty vocabulary for one matrix cell.
type CellStatus string

const (
	StatusImplemented CellStatus = "implemented"
	StatusTested      CellStatus = "tested"
	StatusDesignOnly  CellStatus = "design-only"
)

// Cell is one driver × protocol × transport entry.
type Cell struct {
	Driver    DriverName
	Protocol  Protocol
	Transport Transport
	Status    CellStatus
	Notes     string
}

// Descriptor is what a driver reports about itself.
type Descriptor struct {
	Driver   DriverName
	Protocol Protocol
	// Transports the driver actually speaks in this package.
	Transports []Transport
}

// Message is one turn. Content is text only in this slice; tool use, vision
// and structured output are design-only.
type Message struct {
	Role    string
	Content string
}

// MessageRequest is the uniform CreateMessage / StreamMessage input.
type MessageRequest struct {
	Model     string
	Messages  []Message
	MaxTokens int
}

// Usage is observed token counts. Zero means the upstream did not report them,
// not that the call was free.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

// MessageResponse is the uniform non-stream result.
type MessageResponse struct {
	ID    string
	Model string
	Role  string
	Text  string
	Stop  string
	Usage Usage
}

// Event is one pull from a stream. Type is text_delta, usage, or stop.
type Event struct {
	Type  string
	Text  string
	Usage *Usage
}

const (
	EventTextDelta = "text_delta"
	EventUsage     = "usage"
	EventStop      = "stop"
)

// Stream is a pull-based event iterator. Recv reads the next upstream event
// and does not prefetch, so a slow consumer applies backpressure. The caller
// must Close. Recv returns io.EOF after a clean stop.
type Stream interface {
	Recv() (Event, error)
	Close() error
	Usage() Usage
}

// Driver is the invocation seam. CreateMessage never streams. StreamMessage
// always streams. A request with no model or no messages is a bad_request.
type Driver interface {
	Descriptor() Descriptor
	CreateMessage(ctx context.Context, req MessageRequest) (MessageResponse, error)
	StreamMessage(ctx context.Context, req MessageRequest) (Stream, error)
}

// Config pins one upstream. BaseURL is the API root (no path). Credential is
// held in memory for the request and never logged. A nil CostHook is NopCostHook.
// A nil Doer uses http.DefaultClient.
type Config struct {
	BaseURL    string
	Credential string
	Headers    map[string]string
	Doer       modelprovider.Doer
	CostHook   CostHook
}

// ValidateRequest rejects a request the contract will not send.
func ValidateRequest(req MessageRequest) error {
	if req.Model == "" {
		return &Error{Code: CodeBadRequest, HTTPStatus: 400, Message: "model is required"}
	}
	if len(req.Messages) == 0 {
		return &Error{Code: CodeBadRequest, HTTPStatus: 400, Message: "messages are required"}
	}
	if req.MaxTokens <= 0 {
		return &Error{Code: CodeBadRequest, HTTPStatus: 400, Message: "max_tokens must be positive"}
	}
	return nil
}
