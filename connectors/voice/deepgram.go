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

// Deepgram Voice Agent is the canonical Claude-as-think carrier. It owns listening,
// speaking and turn-taking (Flux end-of-turn) and calls Claude's Messages API for the
// reasoning turn via its agent.think block. This path is SERVER-MEDIATED: the BYO
// Anthropic key lives in the governed Settings (agent.think.endpoint.headers) and is
// applied to the provider server-side, NEVER returned to the client. A short-lived
// Deepgram token is minted from the master key and IS the client credential.
const (
	deepgramDefaultBase = "https://api.deepgram.com"
	deepgramGrantPath   = "/v1/auth/grant"
	// deepgramAgentWS is the fixed agent WebSocket the client connects to with the
	// minted token.
	deepgramAgentWS = "wss://agent.deepgram.com/v1/agent/converse"
	// deepgramAuthScheme is Deepgram's master-key auth scheme ("Token <key>", not
	// Bearer). The master key value is set here and nowhere else.
	deepgramAuthScheme = "Token "
)

// DeepgramConfig configures the Deepgram Voice Agent adapter. APIKey is the master
// key, sent only in the Authorization header of the token grant. BaseURL overrides the
// API host (testing); Transport is the injectable HTTP doer.
type DeepgramConfig struct {
	APIKey    string
	BaseURL   string
	Transport Transport
}

// DeepgramProvider is the Deepgram Voice Agent implementation of Provider.
type DeepgramProvider struct {
	apiKey  string
	baseURL string
	tr      Transport
}

// Compile-time proof that DeepgramProvider satisfies the Provider contract.
var _ Provider = (*DeepgramProvider)(nil)

// NewDeepgram returns a Deepgram Voice Agent provider. A nil cfg.Transport falls back
// to a real *http.Client with a bounded timeout.
func NewDeepgram(cfg DeepgramConfig) *DeepgramProvider {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = deepgramDefaultBase
	}
	return &DeepgramProvider{
		apiKey:  cfg.APIKey,
		baseURL: base,
		tr:      defaultTransport(cfg.Transport),
	}
}

// Name returns the provider's stable identifier.
func (p *DeepgramProvider) Name() string { return "deepgram" }

// deepgramSettings is the governed Voice Agent configuration applied server-side over
// the agent WebSocket (the "Settings" message). It carries the Claude-as-think brain
// (agent.think.provider + endpoint + functions), the listen/speak models, and the
// Flux turn-detection — all fixed from policy. CRITICALLY, agent.think.endpoint.headers
// holds the BYO Anthropic key; this whole struct stays server-side and is NEVER part
// of a returned Session.
type deepgramSettings struct {
	Type  string        `json:"type"`
	Agent deepgramAgent `json:"agent"`
}

type deepgramAgent struct {
	Language string         `json:"language,omitempty"`
	Listen   deepgramListen `json:"listen"`
	Think    deepgramThink  `json:"think"`
	Speak    deepgramSpeak  `json:"speak"`
	Greeting string         `json:"greeting,omitempty"`
}

type deepgramListen struct {
	Provider deepgramListenProvider `json:"provider"`
}

type deepgramListenProvider struct {
	Type  string `json:"type"`
	Model string `json:"model,omitempty"`
}

type deepgramThink struct {
	Provider  deepgramThinkProvider `json:"provider"`
	Endpoint  deepgramThinkEndpoint `json:"endpoint"`
	Functions []deepgramFunction    `json:"functions,omitempty"`
	Prompt    string                `json:"prompt,omitempty"`
}

type deepgramThinkProvider struct {
	Type  string `json:"type"`
	Model string `json:"model"`
}

type deepgramThinkEndpoint struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type deepgramFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type deepgramSpeak struct {
	Provider deepgramSpeakProvider `json:"provider"`
}

type deepgramSpeakProvider struct {
	Type  string `json:"type"`
	Model string `json:"model,omitempty"`
}

// buildSettings turns a governed policy into the Deepgram Voice Agent Settings. It is
// unexported and pure so tests can assert the governed config — especially that
// agent.think.provider.type == "anthropic" with policy.Think.Model and that the
// Anthropic key in policy.Think.Headers is confined to the (server-side) endpoint
// headers — WITHOUT a network call. It must only be called when policy.Think != nil
// (MintSession enforces this).
func (p *DeepgramProvider) buildSettings(policy SessionPolicy) deepgramSettings {
	providerType := "anthropic"
	if policy.Think != nil && policy.Think.ProviderType != "" {
		providerType = policy.Think.ProviderType
	}
	var model, endpoint string
	var headers map[string]string
	if policy.Think != nil {
		model = policy.Think.Model
		endpoint = policy.Think.EndpointURL
		headers = policy.Think.Headers
	}

	var fns []deepgramFunction
	for _, t := range policy.Tools {
		// Tool and deepgramFunction are field-identical; a direct conversion avoids a
		// field-by-field copy (and satisfies staticcheck).
		fns = append(fns, deepgramFunction(t))
	}

	return deepgramSettings{
		Type: "Settings",
		Agent: deepgramAgent{
			Language: policy.Language,
			Listen: deepgramListen{
				Provider: deepgramListenProvider{
					Type:  "deepgram",
					Model: deepgramFluxModel(policy.TurnDetection),
				},
			},
			Think: deepgramThink{
				Provider:  deepgramThinkProvider{Type: providerType, Model: model},
				Endpoint:  deepgramThinkEndpoint{URL: endpoint, Headers: headers},
				Functions: fns,
				Prompt:    policy.Instructions,
			},
			Speak: deepgramSpeak{
				Provider: deepgramSpeakProvider{Type: "deepgram", Model: policy.Voice},
			},
		},
	}
}

// deepgramFluxModel selects the listen model. Deepgram's Flux model carries native
// end-of-turn detection (eot_threshold / eager_eot_threshold / eot_timeout_ms), so
// when the policy requests "flux" turn-detection the Flux model is chosen; otherwise
// the default streaming model is left for the provider to pick.
func deepgramFluxModel(td TurnDetection) string {
	if td.Type == "flux" {
		return "flux-general-en"
	}
	return ""
}

// fluxTurnParams returns the governed Flux end-of-turn parameters from the policy.
// These are applied server-side as part of the listen configuration; they are surfaced
// as their own helper so tests can assert the turn-detection governance without
// reaching into the full Settings tree.
func (p *DeepgramProvider) fluxTurnParams(td TurnDetection) map[string]any {
	if td.Type != "flux" {
		return nil
	}
	return map[string]any{
		"eot_threshold":       td.EOTThreshold,
		"eager_eot_threshold": td.EagerEOTThreshold,
		"eot_timeout_ms":      td.EOTTimeoutMS,
	}
}

// deepgramGrantResponse is the subset of the token-grant response the connector reads.
type deepgramGrantResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// MintSession (server-mediated) builds the governed Settings (with the BYO Anthropic
// key kept server-side), mints a short-lived Deepgram token from the master key, and
// returns a Session carrying ONLY that token plus the agent WebSocket. Neither the
// master key nor the Settings (and therefore not the Anthropic key) are part of the
// returned Session. The Deepgram path REQUIRES the Claude-as-think config: a nil
// policy.Think is an error.
func (p *DeepgramProvider) MintSession(ctx context.Context, policy SessionPolicy, principal string) (Session, error) {
	if policy.Think == nil {
		return Session{}, fmt.Errorf("deepgram: policy.Think is required (Voice Agent uses Claude-as-think)")
	}

	// buildSettings is invoked for governance/validation; the result is applied to the
	// agent WebSocket server-side (data plane) and is intentionally NOT returned.
	settings := p.buildSettings(policy)
	if settings.Agent.Think.Provider.Type != "anthropic" {
		return Session{}, fmt.Errorf("deepgram: think provider must be anthropic, got %q", settings.Agent.Think.Provider.Type)
	}

	headers := map[string]string{
		"Authorization": deepgramAuthScheme + p.apiKey,
		"Content-Type":  "application/json",
	}
	status, data, err := httpDo(ctx, p.tr, http.MethodPost, p.baseURL+deepgramGrantPath, headers, []byte("{}"))
	if err != nil {
		return Session{}, fmt.Errorf("deepgram: grant token: %w", err)
	}
	if status < 200 || status >= 300 {
		return Session{}, fmt.Errorf("deepgram: auth/grant http %d", status)
	}
	var out deepgramGrantResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return Session{}, fmt.Errorf("deepgram: decode grant: %w", err)
	}
	if out.AccessToken == "" {
		return Session{}, fmt.Errorf("deepgram: grant response had no access_token")
	}

	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = defaultTimeout // honest floor when the grant omits expires_in
	}
	return Session{
		Provider:            p.Name(),
		Model:               policy.Think.Model,
		Transport:           "websocket",
		SessionID:           principal,
		EphemeralCredential: out.AccessToken,
		ConnectCoords:       deepgramAgentWS,
		ServerMediated:      true,
		ExpiresAt:           time.Now().UTC().Add(ttl),
	}, nil
}

// Resume is not supported by Deepgram Voice Agent: mint a fresh token + Settings.
func (p *DeepgramProvider) Resume(_ context.Context, _ string) (Session, error) {
	return Session{}, fmt.Errorf("deepgram: resume not supported; mint a new session")
}

// Terminate ends a session server-side. A Voice Agent session ends when the WebSocket
// closes; there is no separate teardown call, so this is an idempotent no-op.
func (p *DeepgramProvider) Terminate(_ context.Context, _, _ string) error { return nil }

// StreamEvents returns a closed channel: the live agent WebSocket read loop is the
// data plane's concern. normalize is the complete, unit-tested per-frame mapping.
func (p *DeepgramProvider) StreamEvents(_ context.Context, _ string) (<-chan Event, error) {
	return emptyEventStream(), nil
}

// deepgramFrame is the minimal envelope of a Deepgram Voice Agent server message
// needed to classify it. For ConversationText only the role is read as a label — the
// transcript content field is deliberately never decoded.
type deepgramFrame struct {
	Type string `json:"type"`
	Role string `json:"role,omitempty"`
}

// normalize maps one Deepgram Voice Agent server message onto the normalized taxonomy.
// The barge-in signal "UserStartedSpeaking" maps to EventUserStartedSpeaking.
// ConversationText becomes a TranscriptDelta carrying ONLY the role label, never the
// "content" text. It returns (Event{}, false) for messages outside the taxonomy.
func (p *DeepgramProvider) normalize(raw []byte) (Event, bool) {
	var f deepgramFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return Event{}, false
	}
	switch f.Type {
	case "Welcome", "SettingsApplied":
		return Event{Kind: EventSessionStarted, Detail: f.Type}, true
	case "UserStartedSpeaking":
		return Event{Kind: EventUserStartedSpeaking, Detail: f.Type}, true
	case "AgentStartedSpeaking":
		return Event{Kind: EventAgentSpeaking, Detail: f.Type}, true
	case "AgentAudioDone":
		return Event{Kind: EventAgentDone, Detail: f.Type}, true
	case "FunctionCallRequest":
		return Event{Kind: EventToolCall, Detail: f.Type}, true
	case "ConversationText":
		// Label is the role only (e.g. "user"/"assistant"); the transcript "content"
		// field is never decoded into the Event — minimal-data invariant.
		return Event{Kind: EventTranscriptDelta, Detail: f.Role}, true
	case "Error":
		return Event{Kind: EventError, Detail: f.Type}, true
	default:
		return Event{}, false
	}
}
