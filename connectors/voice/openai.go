// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OpenAI Realtime is GA. A session credential is minted server-side by POSTing the
// governed session config to /v1/realtime/client_secrets with the master key in the
// Authorization header; the response carries a short-lived client secret the browser
// uses for the WebRTC SDP exchange at /v1/realtime/calls. The client secret is
// CREATION-only (default TTL 600s, range 10-7200s) — it is not a duration budget, so
// the connector derives its own deadline from policy.MaxDuration.
const (
	openaiDefaultBase  = "https://api.openai.com"
	openaiSecretsPath  = "/v1/realtime/client_secrets"
	openaiCallsPath    = "/v1/realtime/calls"
	openaiDefaultModel = "gpt-realtime-2"
	// openaiSafetyHeader carries the pseudonymous principal as OpenAI's audit/safety
	// identifier (the documented anti-abuse primitive). It is a hash, never PII.
	openaiSafetyHeader = "OpenAI-Safety-Identifier"
	// openaiCreationTTL is the client-secret default lifetime OpenAI applies when none
	// is requested. It bounds CREATION only; live session length is independent.
	openaiCreationTTL = 600 * time.Second
)

// OpenAIConfig configures the OpenAI Realtime adapter. APIKey is the master key,
// held in memory only and sent solely in the Authorization header. BaseURL overrides
// the API host (testing); Transport is the injectable HTTP doer.
type OpenAIConfig struct {
	APIKey    string
	BaseURL   string
	Transport Transport
}

// OpenAIProvider is the OpenAI Realtime implementation of Provider.
type OpenAIProvider struct {
	apiKey  string
	baseURL string
	tr      Transport
}

// Compile-time proof that OpenAIProvider satisfies the Provider contract.
var _ Provider = (*OpenAIProvider)(nil)

// NewOpenAI returns an OpenAI Realtime provider. A nil cfg.Transport falls back to a
// real *http.Client with a bounded timeout.
func NewOpenAI(cfg OpenAIConfig) *OpenAIProvider {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = openaiDefaultBase
	}
	return &OpenAIProvider{
		apiKey:  cfg.APIKey,
		baseURL: base,
		tr:      defaultTransport(cfg.Transport),
	}
}

// Name returns the provider's stable identifier.
func (p *OpenAIProvider) Name() string { return "openai" }

// openaiSettings is the governed session-creation body POSTed to
// /v1/realtime/client_secrets. Every field is fixed from policy; the client supplies
// none of it. The shape follows OpenAI's session object (session.type="realtime",
// model, instructions, audio.output.voice, tools, turn_detection).
type openaiSettings struct {
	Session openaiSession `json:"session"`
}

type openaiSession struct {
	Type          string            `json:"type"`
	Model         string            `json:"model"`
	Instructions  string            `json:"instructions,omitempty"`
	Audio         *openaiAudio      `json:"audio,omitempty"`
	Tools         []openaiTool      `json:"tools,omitempty"`
	TurnDetection *openaiTurnDetect `json:"turn_detection,omitempty"`
}

type openaiAudio struct {
	Output openaiAudioOutput `json:"output"`
}

type openaiAudioOutput struct {
	Voice string `json:"voice,omitempty"`
}

type openaiTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openaiTurnDetect struct {
	Type            string `json:"type,omitempty"`
	SilenceDuration int    `json:"silence_duration_ms,omitempty"`
}

// buildSettings turns a governed policy into the OpenAI session-creation body. It is
// unexported and pure so tests can assert the governed config without a network call.
func (p *OpenAIProvider) buildSettings(policy SessionPolicy) openaiSettings {
	model := policy.Model
	if model == "" {
		model = openaiDefaultModel
	}
	s := openaiSession{
		Type:         "realtime",
		Model:        model,
		Instructions: policy.Instructions,
	}
	if policy.Voice != "" {
		s.Audio = &openaiAudio{Output: openaiAudioOutput{Voice: policy.Voice}}
	}
	for _, t := range policy.Tools {
		s.Tools = append(s.Tools, openaiTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}
	if policy.TurnDetection.Type != "" || policy.TurnDetection.SilenceMS > 0 {
		s.TurnDetection = &openaiTurnDetect{
			Type:            policy.TurnDetection.Type,
			SilenceDuration: policy.TurnDetection.SilenceMS,
		}
	}
	return openaiSettings{Session: s}
}

// openaiSecretResponse is the subset of the client-secret response the connector
// reads: the ephemeral secret value and its creation-only expiry.
type openaiSecretResponse struct {
	Value     string `json:"value"`
	ExpiresAt int64  `json:"expires_at"`
	Session   struct {
		ID string `json:"id"`
	} `json:"session"`
}

// MintSession mints a creation-only client secret server-side and returns a Session
// carrying ONLY that ephemeral secret plus the WebRTC SDP endpoint. The master key
// travels in the Authorization header and never appears in the returned Session. The
// returned ExpiresAt is the connector's OWN deadline derived from policy.MaxDuration
// (the client secret's TTL governs creation only, not session length).
func (p *OpenAIProvider) MintSession(ctx context.Context, policy SessionPolicy, principal string) (Session, error) {
	body, err := json.Marshal(p.buildSettings(policy))
	if err != nil {
		return Session{}, fmt.Errorf("openai: marshal settings: %w", err)
	}
	headers := map[string]string{
		"Authorization":    "Bearer " + p.apiKey,
		"Content-Type":     "application/json",
		openaiSafetyHeader: principal,
	}
	status, data, err := httpDo(ctx, p.tr, http.MethodPost, p.baseURL+openaiSecretsPath, headers, body)
	if err != nil {
		return Session{}, fmt.Errorf("openai: mint client secret: %w", err)
	}
	if status < 200 || status >= 300 {
		return Session{}, fmt.Errorf("openai: client_secrets http %d", status)
	}
	var out openaiSecretResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return Session{}, fmt.Errorf("openai: decode client secret: %w", err)
	}
	if out.Value == "" {
		return Session{}, fmt.Errorf("openai: client secret response had no value")
	}

	model := policy.Model
	if model == "" {
		model = openaiDefaultModel
	}
	return Session{
		Provider:            p.Name(),
		Model:               model,
		Transport:           "webrtc",
		SessionID:           out.Session.ID,
		EphemeralCredential: out.Value,
		ConnectCoords:       p.baseURL + openaiCallsPath,
		ExpiresAt:           p.deadline(policy),
	}, nil
}

// deadline derives the connector's own session deadline. It uses policy.MaxDuration
// when set (the governed session length), falling back to the client-secret creation
// TTL only as a floor — never trusting the provider credential's TTL as a budget.
func (p *OpenAIProvider) deadline(policy SessionPolicy) time.Time {
	d := policy.MaxDuration
	if d <= 0 {
		d = openaiCreationTTL
	}
	return time.Now().UTC().Add(d)
}

// Resume is not supported by OpenAI Realtime: a new client secret must be minted.
func (p *OpenAIProvider) Resume(_ context.Context, _ string) (Session, error) {
	return Session{}, fmt.Errorf("openai: resume not supported; mint a new session")
}

// Terminate is a no-op for minted WebRTC client-secret sessions: there is no
// server-side session to tear down here, and the client secret expires on its own.
// For live SIP calls, use CallClient.Hangup; that is the authoritative teardown
// path for the OpenAI Realtime call plane.
func (p *OpenAIProvider) Terminate(_ context.Context, _, _ string) error { return nil }

// StreamEvents returns a closed channel: the live WebRTC data-channel read loop is
// the data plane's concern (it holds the media connection). normalize is the complete,
// unit-tested mapping the read loop applies to each frame.
func (p *OpenAIProvider) StreamEvents(_ context.Context, _ string) (<-chan Event, error) {
	return emptyEventStream(), nil
}

// openaiFrame is the minimal envelope of an OpenAI Realtime server event needed to
// classify it. Only the type discriminator is read — never audio or transcript text.
type openaiFrame struct {
	Type string `json:"type"`
}

// normalize maps one OpenAI Realtime server event frame onto the normalized taxonomy.
// The barge-in signal input_audio_buffer.speech_started maps to EventUserStartedSpeaking.
// It returns (Event{}, false) for frames outside the taxonomy. No transcript text or
// audio is ever copied into the returned Event.
func (p *OpenAIProvider) normalize(raw []byte) (Event, bool) {
	var f openaiFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return Event{}, false
	}
	switch f.Type {
	case "session.created":
		return Event{Kind: EventSessionStarted, Detail: f.Type}, true
	case "input_audio_buffer.speech_started":
		return Event{Kind: EventUserStartedSpeaking, Detail: f.Type}, true
	case "response.audio.delta", "output_audio.delta", "response.output_audio.delta":
		return Event{Kind: EventAgentSpeaking, Detail: f.Type}, true
	case "response.done":
		return Event{Kind: EventAgentDone, Detail: f.Type}, true
	case "response.function_call_arguments.done":
		return Event{Kind: EventToolCall, Detail: f.Type}, true
	case "error":
		return Event{Kind: EventError, Detail: f.Type}, true
	default:
		return Event{}, false
	}
}
