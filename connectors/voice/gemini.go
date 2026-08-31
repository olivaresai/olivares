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

// Google Gemini Live (v1alpha) mints an ephemeral auth token server-side: a POST to
// /v1alpha/auth_tokens with the master key in the x-goog-api-key header returns a
// token (~30 min expiry) the client uses to open the BidiGenerateContent WebSocket.
// Session resumption is MANDATORY for sessions longer than ~10 minutes within the
// token's window, so the mint request enables SessionResumptionConfig.
const (
	geminiDefaultBase  = "https://generativelanguage.googleapis.com"
	geminiTokensPath   = "/v1alpha/auth_tokens"
	geminiBidiBase     = "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1alpha.GenerativeService.BidiGenerateContent"
	geminiDefaultModel = "gemini-2.5-flash-native-audio-preview"
	// geminiAPIKeyHeader is the EXACT header Gemini authenticates the mint with. The
	// master key value is set here and nowhere else.
	geminiAPIKeyHeader = "x-goog-api-key"
	// geminiTokenTTL is the ephemeral token's approximate lifetime.
	geminiTokenTTL = 30 * time.Minute
)

// GeminiConfig configures the Gemini Live adapter. APIKey is the master key, sent only
// in the x-goog-api-key header. BaseURL overrides the API host (testing); Transport is
// the injectable HTTP doer.
type GeminiConfig struct {
	APIKey    string
	BaseURL   string
	Transport Transport
}

// GeminiProvider is the Gemini Live implementation of Provider.
type GeminiProvider struct {
	apiKey  string
	baseURL string
	tr      Transport
}

// Compile-time proof that GeminiProvider satisfies the Provider contract.
var _ Provider = (*GeminiProvider)(nil)

// NewGemini returns a Gemini Live provider. A nil cfg.Transport falls back to a real
// *http.Client with a bounded timeout.
func NewGemini(cfg GeminiConfig) *GeminiProvider {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = geminiDefaultBase
	}
	return &GeminiProvider{
		apiKey:  cfg.APIKey,
		baseURL: base,
		tr:      defaultTransport(cfg.Transport),
	}
}

// Name returns the provider's stable identifier.
func (p *GeminiProvider) Name() string { return "gemini" }

// geminiSettings is the governed ephemeral-token request body. It pins the live
// connection's model and system instruction and enables SessionResumptionConfig
// (mandatory for >10-minute sessions). Every field is fixed from policy.
type geminiSettings struct {
	Uses                   int                          `json:"uses,omitempty"`
	LiveConnectConstraints *geminiLiveConnectConstraint `json:"liveConnectConstraints,omitempty"`
}

type geminiLiveConnectConstraint struct {
	Model  string       `json:"model"`
	Config geminiConfig `json:"config"`
}

type geminiConfig struct {
	SessionResumption *geminiSessionResumption `json:"sessionResumption"`
	SystemInstruction *geminiContent           `json:"systemInstruction,omitempty"`
	Tools             []geminiTool             `json:"tools,omitempty"`
	SpeechConfig      *geminiSpeechConfig      `json:"speechConfig,omitempty"`
}

// geminiSessionResumption enables resumption; an empty object opts the session in
// (the provider returns resumption handles over the live channel).
type geminiSessionResumption struct{}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiSpeechConfig struct {
	VoiceConfig  *geminiVoiceConfig `json:"voiceConfig,omitempty"`
	LanguageCode string             `json:"languageCode,omitempty"`
}

type geminiVoiceConfig struct {
	PrebuiltVoiceConfig *geminiPrebuiltVoice `json:"prebuiltVoiceConfig,omitempty"`
}

type geminiPrebuiltVoice struct {
	VoiceName string `json:"voiceName"`
}

// buildSettings turns a governed policy into the Gemini ephemeral-token request body,
// pinning the model/instruction/tools/voice and enabling mandatory session resumption.
// It is unexported and pure so tests can assert the governed config (including that
// SessionResumptionConfig is enabled) WITHOUT a network call.
func (p *GeminiProvider) buildSettings(policy SessionPolicy) geminiSettings {
	model := policy.Model
	if model == "" {
		model = geminiDefaultModel
	}
	cfg := geminiConfig{
		SessionResumption: &geminiSessionResumption{}, // mandatory for >10 min sessions
	}
	if policy.Instructions != "" {
		cfg.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: policy.Instructions}}}
	}
	for _, t := range policy.Tools {
		cfg.Tools = append(cfg.Tools, geminiTool{
			FunctionDeclarations: []geminiFunctionDecl{{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			}},
		})
	}
	if policy.Voice != "" || policy.Language != "" {
		sc := &geminiSpeechConfig{LanguageCode: policy.Language}
		if policy.Voice != "" {
			sc.VoiceConfig = &geminiVoiceConfig{PrebuiltVoiceConfig: &geminiPrebuiltVoice{VoiceName: policy.Voice}}
		}
		cfg.SpeechConfig = sc
	}
	return geminiSettings{
		Uses: 1,
		LiveConnectConstraints: &geminiLiveConnectConstraint{
			Model:  model,
			Config: cfg,
		},
	}
}

// geminiTokenResponse is the subset of the auth-token response the connector reads.
// Gemini returns the token under "name" (resource name) and may echo it as "token".
type geminiTokenResponse struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

// token returns the usable ephemeral token, preferring the explicit "token" field and
// falling back to the resource "name".
func (r geminiTokenResponse) token() string {
	if r.Token != "" {
		return r.Token
	}
	return r.Name
}

// MintSession mints an ephemeral token server-side and returns a Session carrying ONLY
// that token plus the BidiGenerateContent WebSocket (token in the query string, as the
// live API expects). The master key travels in the x-goog-api-key header and never
// appears in the returned Session. ExpiresAt is now + the ~30-minute token window.
func (p *GeminiProvider) MintSession(ctx context.Context, policy SessionPolicy, principal string) (Session, error) {
	body, err := json.Marshal(p.buildSettings(policy))
	if err != nil {
		return Session{}, fmt.Errorf("gemini: marshal settings: %w", err)
	}
	headers := map[string]string{
		geminiAPIKeyHeader: p.apiKey,
		"Content-Type":     "application/json",
	}
	status, data, err := httpDo(ctx, p.tr, http.MethodPost, p.baseURL+geminiTokensPath, headers, body)
	if err != nil {
		return Session{}, fmt.Errorf("gemini: mint auth token: %w", err)
	}
	if status < 200 || status >= 300 {
		return Session{}, fmt.Errorf("gemini: auth_tokens http %d", status)
	}
	var out geminiTokenResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return Session{}, fmt.Errorf("gemini: decode auth token: %w", err)
	}
	tok := out.token()
	if tok == "" {
		return Session{}, fmt.Errorf("gemini: auth token response was empty")
	}

	model := policy.Model
	if model == "" {
		model = geminiDefaultModel
	}
	return Session{
		Provider:            p.Name(),
		Model:               model,
		Transport:           "websocket",
		SessionID:           principal,
		EphemeralCredential: tok,
		ConnectCoords:       geminiBidiBase + "?access_token=" + tok,
		ExpiresAt:           time.Now().UTC().Add(geminiTokenTTL),
	}, nil
}

// Resume re-establishes a Gemini Live session from a resumption handle. Gemini
// resumption is performed over the live WebSocket using the handle the data plane
// captured; minting a fresh ephemeral token to carry it is the server-side step the
// control plane owns. The returned Session reuses the handle as the SessionID and a
// fresh token must accompany it — here we surface the handle so the data plane can
// reconnect; a token is minted by MintSession. For v1 the control plane mints a new
// token then reconnects with the handle, so Resume returns the handle bound to a new
// connect coordinate without a network call.
func (p *GeminiProvider) Resume(_ context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, fmt.Errorf("gemini: resume requires a resumption handle")
	}
	return Session{
		Provider:      p.Name(),
		Transport:     "websocket",
		SessionID:     token,
		ConnectCoords: geminiBidiBase,
		ExpiresAt:     time.Now().UTC().Add(geminiTokenTTL),
	}, nil
}

// Terminate ends a session server-side. A Gemini Live session ends when the WebSocket
// closes; there is no separate teardown call, so this is an idempotent no-op.
func (p *GeminiProvider) Terminate(_ context.Context, _, _ string) error { return nil }

// StreamEvents returns a closed channel: the live BidiGenerateContent read loop is the
// data plane's concern. normalize is the complete, unit-tested per-frame mapping.
func (p *GeminiProvider) StreamEvents(_ context.Context, _ string) (<-chan Event, error) {
	return emptyEventStream(), nil
}

// geminiFrame is the minimal envelope of a Gemini Live server message needed to
// classify it. Only structural flags are read — never the model/transcript text under
// modelTurn parts.
type geminiFrame struct {
	SetupComplete *json.RawMessage `json:"setupComplete,omitempty"`
	ToolCall      *json.RawMessage `json:"toolCall,omitempty"`
	ServerContent *struct {
		Interrupted  bool `json:"interrupted,omitempty"`
		TurnComplete bool `json:"turnComplete,omitempty"`
	} `json:"serverContent,omitempty"`
}

// normalize maps one Gemini Live server message onto the normalized taxonomy. The
// barge-in signal serverContent.interrupted == true maps to EventUserStartedSpeaking.
// It returns (Event{}, false) for messages outside the taxonomy. No model or
// transcript text is copied into the returned Event.
func (p *GeminiProvider) normalize(raw []byte) (Event, bool) {
	var f geminiFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return Event{}, false
	}
	switch {
	case f.SetupComplete != nil:
		return Event{Kind: EventSessionStarted, Detail: "setupComplete"}, true
	case f.ToolCall != nil:
		return Event{Kind: EventToolCall, Detail: "toolCall"}, true
	case f.ServerContent != nil && f.ServerContent.Interrupted:
		return Event{Kind: EventUserStartedSpeaking, Detail: "interrupted"}, true
	case f.ServerContent != nil && f.ServerContent.TurnComplete:
		return Event{Kind: EventAgentDone, Detail: "turnComplete"}, true
	default:
		return Event{}, false
	}
}
