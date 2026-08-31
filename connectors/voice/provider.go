// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Transport is the injectable HTTP doer. Production passes net/http's *http.Client;
// tests pass a deterministic stub. Keeping it an interface keeps the package offline-testable.
type Transport interface {
	Do(req *http.Request) (*http.Response, error)
}

// Tool is one governed tool exposed to the realtime agent (JSON-Schema params, never a secret).
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// TurnDetection is the barge-in / end-of-turn tuning, fixed from policy.
type TurnDetection struct {
	Type              string // "server_vad" | "semantic_vad" | "flux" | ""
	EOTThreshold      float64
	EagerEOTThreshold float64
	EOTTimeoutMS      int
	SilenceMS         int
}

// ThinkConfig is the Claude-as-think brain. Anthropic exposes NO native realtime/voice API;
// the canonical pattern is STT->LLM->TTS where the provider does listen/speak/turn-taking and
// calls Claude's Messages API for reasoning. Headers hold the server-only BYO Anthropic auth and
// are NEVER returned to the client.
type ThinkConfig struct {
	ProviderType string            // "anthropic"
	Model        string            // e.g. "claude-fable-5", "claude-opus-4-8", "claude-sonnet-5", "claude-sonnet-4-6", "claude-haiku-4-5"
	EndpointURL  string            // "https://api.anthropic.com/v1/messages"
	Headers      map[string]string // x-api-key, anthropic-version — server-held, never returned
}

// SessionPolicy is the GOVERNED, server-fixed session configuration. Every field is set from the
// operator's approved policy, NEVER from the client (anti-escalation: a client cannot upgrade the
// model, change instructions, widen tools, or relax turn-detection).
type SessionPolicy struct {
	Model         string
	Voice         string
	Instructions  string
	Tools         []Tool
	TurnDetection TurnDetection
	MaxDuration   time.Duration // independent server-side max-duration; NOT the provider credential TTL
	Language      string        // BCP-47, optional
	Think         *ThinkConfig  // non-nil => Claude-as-think (Deepgram path)
}

// Session is the minted, governed, EPHEMERAL handle returned to the caller. It carries ONLY the
// short-lived client credential + connect coordinates — NEVER the provider master key, audio, or
// transcript text (docs/SECURITY-HARDENING.md).
type Session struct {
	Provider            string
	Model               string
	Transport           string // "webrtc" | "websocket" | "sip"
	SessionID           string
	EphemeralCredential string
	ConnectCoords       string // SDP/answer endpoint, WSS URL, or connect endpoint
	ExpiresAt           time.Time
	ServerMediated      bool // true when the control plane mediates so a BYO key stays server-side (Claude-as-think)
}

// EventKind is the normalized cross-provider event taxonomy (the whole point of the connector:
// one barge-in event regardless of provider).
type EventKind string

// Normalized event kinds emitted by every adapter's normalize function.
const (
	EventSessionStarted      EventKind = "session_started"
	EventUserStartedSpeaking EventKind = "user_started_speaking" // == barge-in / interrupt, unified
	EventAgentSpeaking       EventKind = "agent_speaking"
	EventAgentDone           EventKind = "agent_done"
	EventToolCall            EventKind = "tool_call"
	EventTranscriptDelta     EventKind = "transcript_delta" // metadata delta only — never raw audio/text
	EventError               EventKind = "error"
	EventSessionEnded        EventKind = "session_ended"
)

// Event is a normalized realtime event. Detail is a short, non-sensitive label (tool name, error
// class) — NEVER transcript text or audio bytes.
type Event struct {
	Kind      EventKind
	SessionID string
	Detail    string
}

// Provider is the provider-agnostic governed realtime backend.
type Provider interface {
	Name() string
	// MintSession mints, server-side, an ephemeral policy-fixed session credential. principal is a
	// pseudonymous principal id (a hash) used as the provider audit/safety identifier — never PII.
	MintSession(ctx context.Context, policy SessionPolicy, principal string) (Session, error)
	// Resume re-establishes a session from a resumption token (provider-specific).
	Resume(ctx context.Context, token string) (Session, error)
	// Terminate ends a session server-side. Idempotent.
	Terminate(ctx context.Context, sessionID, reason string) error
	// StreamEvents returns a normalized event channel for a session (barge-in unified across providers).
	StreamEvents(ctx context.Context, sessionID string) (<-chan Event, error)
}

// defaultTimeout is the request timeout used when an adapter falls back to a real
// *http.Client (Transport left nil at construction).
const defaultTimeout = 30 * time.Second

// maxBody caps every provider response body so a hostile or runaway control-plane
// endpoint cannot exhaust memory. Mint/grant responses are tiny; 1 MiB is generous.
const maxBody = 1 << 20

// defaultTransport returns the supplied Transport, or a real *http.Client with a
// bounded timeout when none was injected. Each adapter constructor calls this so a
// nil Transport never panics and never leaks an unbounded request.
func defaultTransport(t Transport) Transport {
	if t != nil {
		return t
	}
	return &http.Client{Timeout: defaultTimeout}
}

// httpDo performs one HTTP request through the injectable Transport and returns the
// status code and a body-capped response. It is shared by every adapter so the body
// cap, context wiring, and error wrapping are identical across providers. headers
// are applied verbatim (this is where the provider master key / BYO auth travels —
// in the request, never in the returned Session). A nil body sends no payload.
func httpDo(ctx context.Context, t Transport, method, url string, headers map[string]string, body []byte) (status int, respBody []byte, err error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, nil, fmt.Errorf("voice: build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := t.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("voice: do request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("voice: read response: %w", err)
	}
	return resp.StatusCode, data, nil
}

// emptyEventStream returns an immediately-closed Event channel. The adapters use it
// for StreamEvents in v1: the live WebSocket/WebRTC read loop that decodes provider
// frames belongs to the data plane (it owns the media connection), so this connector
// does not fake events from a connection it does not hold. Each adapter's normalize
// function — which IS complete and unit-tested — is what the data-plane read loop
// calls to map provider frames onto the normalized taxonomy. Returning a closed
// channel keeps StreamEvents honest: a consuming range loop terminates at once
// rather than receiving fabricated events.
func emptyEventStream() <-chan Event {
	ch := make(chan Event)
	close(ch)
	return ch
}
