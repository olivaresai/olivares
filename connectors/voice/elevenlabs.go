// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ElevenLabs Conversational mints a per-conversation signed WSS URL server-side: a
// GET to /v1/convai/conversation/get-signed-url with the master key in the xi-api-key
// header returns a signed URL valid ~15 minutes to START a session (the live session
// itself may run longer). The signed token IS the client credential; the connector
// never returns the master key.
const (
	elevenDefaultBase = "https://api.elevenlabs.io"
	elevenSignedPath  = "/v1/convai/conversation/get-signed-url"
	// elevenAPIKeyHeader is the EXACT header ElevenLabs authenticates with (not
	// Authorization). The master key value is set here and nowhere else.
	elevenAPIKeyHeader = "xi-api-key"
	// elevenSignedTTL is how long a freshly signed URL is valid to START a session.
	elevenSignedTTL = 15 * time.Minute
)

// ElevenLabsConfig configures the ElevenLabs Conversational adapter. AgentID is the
// governed agent whose policy (voice, prompt, tools) lives in the ElevenLabs agent
// itself; APIKey is the master key, sent only in the xi-api-key header.
type ElevenLabsConfig struct {
	APIKey    string
	BaseURL   string
	AgentID   string
	Transport Transport
}

// ElevenLabsProvider is the ElevenLabs Conversational implementation of Provider.
type ElevenLabsProvider struct {
	apiKey  string
	baseURL string
	agentID string
	tr      Transport
}

// Compile-time proof that ElevenLabsProvider satisfies the Provider contract.
var _ Provider = (*ElevenLabsProvider)(nil)

// NewElevenLabs returns an ElevenLabs Conversational provider. A nil cfg.Transport
// falls back to a real *http.Client with a bounded timeout.
func NewElevenLabs(cfg ElevenLabsConfig) *ElevenLabsProvider {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = elevenDefaultBase
	}
	return &ElevenLabsProvider{
		apiKey:  cfg.APIKey,
		baseURL: base,
		agentID: cfg.AgentID,
		tr:      defaultTransport(cfg.Transport),
	}
}

// Name returns the provider's stable identifier.
func (p *ElevenLabsProvider) Name() string { return "elevenlabs" }

// buildSignedURL returns the governed signed-URL request target. The agent_id is the
// only governed parameter at mint time (ElevenLabs holds the rest of the agent policy
// server-side). It is unexported and pure so tests can assert it without a network
// call. agentID falls back to policy is not applicable: ElevenLabs governs by agent.
func (p *ElevenLabsProvider) buildSignedURL() string {
	q := url.Values{}
	q.Set("agent_id", p.agentID)
	return p.baseURL + elevenSignedPath + "?" + q.Encode()
}

// elevenSignedResponse is the subset of the signed-URL response the connector reads.
type elevenSignedResponse struct {
	SignedURL string `json:"signed_url"`
}

// MintSession requests a signed WSS URL server-side and returns a Session whose
// credential and connect coordinates are both that signed URL (the signed token is
// the credential). The master key travels in the xi-api-key header and never appears
// in the returned Session. ExpiresAt is now + the ~15-minute start window.
func (p *ElevenLabsProvider) MintSession(ctx context.Context, policy SessionPolicy, principal string) (Session, error) {
	if p.agentID == "" {
		return Session{}, fmt.Errorf("elevenlabs: agent_id is required")
	}
	headers := map[string]string{elevenAPIKeyHeader: p.apiKey}
	status, data, err := httpDo(ctx, p.tr, http.MethodGet, p.buildSignedURL(), headers, nil)
	if err != nil {
		return Session{}, fmt.Errorf("elevenlabs: get signed url: %w", err)
	}
	if status < 200 || status >= 300 {
		return Session{}, fmt.Errorf("elevenlabs: get-signed-url http %d", status)
	}
	var out elevenSignedResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return Session{}, fmt.Errorf("elevenlabs: decode signed url: %w", err)
	}
	if out.SignedURL == "" {
		return Session{}, fmt.Errorf("elevenlabs: signed url response was empty")
	}
	return Session{
		Provider:            p.Name(),
		Model:               policy.Model,
		Transport:           "websocket",
		SessionID:           p.agentID,
		EphemeralCredential: out.SignedURL,
		ConnectCoords:       out.SignedURL,
		ExpiresAt:           time.Now().UTC().Add(elevenSignedTTL),
	}, nil
}

// Resume is not supported by ElevenLabs Conversational: mint a fresh signed URL.
func (p *ElevenLabsProvider) Resume(_ context.Context, _ string) (Session, error) {
	return Session{}, fmt.Errorf("elevenlabs: resume not supported; mint a new signed url")
}

// Terminate ends a session server-side. ElevenLabs sessions end when the WebSocket
// closes; there is no separate server-side teardown call, so this is an idempotent
// no-op satisfying the contract.
func (p *ElevenLabsProvider) Terminate(_ context.Context, _, _ string) error { return nil }

// StreamEvents returns a closed channel: the live WebSocket read loop is the data
// plane's concern. normalize is the complete, unit-tested per-frame mapping.
func (p *ElevenLabsProvider) StreamEvents(_ context.Context, _ string) (<-chan Event, error) {
	return emptyEventStream(), nil
}

// elevenFrame is the minimal envelope of an ElevenLabs Conversational WebSocket event
// needed to classify it. Only the type and a VAD score are read — never audio or the
// transcript/agent text payloads.
type elevenFrame struct {
	Type          string `json:"type"`
	VADScoreEvent *struct {
		VADScore float64 `json:"vad_score"`
	} `json:"vad_score_event,omitempty"`
}

// elevenVADBargeIn is the VAD score above which a vad_score frame is treated as the
// user starting to speak (barge-in). ElevenLabs emits a continuous score; a high
// value is the interrupt signal alongside the explicit interruption frame.
const elevenVADBargeIn = 0.9

// normalize maps one ElevenLabs Conversational WebSocket event onto the normalized
// taxonomy. Both an explicit "interruption" frame and a high "vad_score" map to
// EventUserStartedSpeaking (barge-in). It returns (Event{}, false) for frames outside
// the taxonomy. No transcript or agent text is copied into the returned Event.
func (p *ElevenLabsProvider) normalize(raw []byte) (Event, bool) {
	var f elevenFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return Event{}, false
	}
	switch f.Type {
	case "conversation_initiation_metadata":
		return Event{Kind: EventSessionStarted, Detail: f.Type}, true
	case "interruption":
		return Event{Kind: EventUserStartedSpeaking, Detail: f.Type}, true
	case "vad_score":
		if f.VADScoreEvent != nil && f.VADScoreEvent.VADScore >= elevenVADBargeIn {
			return Event{Kind: EventUserStartedSpeaking, Detail: f.Type}, true
		}
		return Event{}, false
	case "agent_response", "audio", "internal_tentative_agent_response":
		// Agent audio / (tentative) agent turns are agent-speaking signals. The label
		// is the frame type only; the spoken/agent text is never copied.
		return Event{Kind: EventAgentSpeaking, Detail: f.Type}, true
	case "agent_response_correction":
		// A correction supersedes an in-flight agent turn — still an agent-speaking
		// state, surfaced with its own label.
		return Event{Kind: EventAgentSpeaking, Detail: f.Type}, true
	case "user_transcript":
		// A transcript metadata delta — label only, never the recognized text.
		return Event{Kind: EventTranscriptDelta, Detail: f.Type}, true
	case "client_tool_call":
		return Event{Kind: EventToolCall, Detail: f.Type}, true
	default:
		return Event{}, false
	}
}
