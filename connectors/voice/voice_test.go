// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubTransport is a deterministic Transport. It captures the last request (method,
// URL, headers, body) and returns a canned status + JSON body. No network is touched.
type stubTransport struct {
	status   int
	respBody string

	// captured request
	gotMethod  string
	gotURL     string
	gotHeaders http.Header
	gotBody    []byte
}

func (s *stubTransport) Do(req *http.Request) (*http.Response, error) {
	s.gotMethod = req.Method
	s.gotURL = req.URL.String()
	s.gotHeaders = req.Header.Clone()
	if req.Body != nil {
		s.gotBody, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(s.respBody)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// masterKey is the secret that must NEVER appear in any returned Session.
const masterKey = "MASTER-SECRET-KEY-do-not-leak"

// anthropicKey is the BYO Anthropic key that must NEVER appear in a returned Session.
const anthropicKey = "sk-ant-DO-NOT-LEAK"

const principal = "sha256-pseudonymous-principal"

// governedPolicy is a fully-specified governed policy used to assert that the request
// body reflects policy (not a client-supplied value).
func governedPolicy() SessionPolicy {
	return SessionPolicy{
		Model:        "governed-model-x",
		Voice:        "governed-voice-x",
		Instructions: "You are a governed agent. Stay on policy.",
		Tools: []Tool{{
			Name:        "lookup_order",
			Description: "Look up an order by id.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`),
		}},
		TurnDetection: TurnDetection{
			Type:              "server_vad",
			EOTThreshold:      0.7,
			EagerEOTThreshold: 0.3,
			EOTTimeoutMS:      4000,
			SilenceMS:         500,
		},
		MaxDuration: 20 * time.Minute,
		Language:    "en-US",
	}
}

// assertNoMasterKeyInSession proves the minimal-data invariant: none of the Session's
// string fields contains the master key (or any other forbidden secret passed in).
func assertNoMasterKeyInSession(t *testing.T, sess Session, secrets ...string) {
	t.Helper()
	fields := map[string]string{
		"Provider":            sess.Provider,
		"Model":               sess.Model,
		"Transport":           sess.Transport,
		"SessionID":           sess.SessionID,
		"EphemeralCredential": sess.EphemeralCredential,
		"ConnectCoords":       sess.ConnectCoords,
	}
	for name, v := range fields {
		for _, secret := range secrets {
			assert.NotContainsf(t, v, secret, "Session.%s must not contain secret %q", name, secret)
		}
	}
}

// --- OpenAI ----------------------------------------------------------------------

func TestOpenAIMintSession(t *testing.T) {
	tr := &stubTransport{
		status:   http.StatusOK,
		respBody: `{"value":"ek_ephemeral_abc","expires_at":1700000600,"session":{"id":"sess_123"}}`,
	}
	p := NewOpenAI(OpenAIConfig{APIKey: masterKey, BaseURL: "https://api.example.test", Transport: tr})

	policy := governedPolicy()
	sess, err := p.MintSession(context.Background(), policy, principal)
	require.NoError(t, err)

	// (1) ephemeral credential + connect coords returned; master key absent everywhere.
	assert.Equal(t, "openai", sess.Provider)
	assert.Equal(t, "webrtc", sess.Transport)
	assert.Equal(t, "ek_ephemeral_abc", sess.EphemeralCredential)
	assert.Equal(t, "sess_123", sess.SessionID)
	assert.Equal(t, "https://api.example.test/v1/realtime/calls", sess.ConnectCoords)
	assertNoMasterKeyInSession(t, sess, masterKey)

	// Request hit the documented endpoint with the master key in Authorization only.
	assert.Equal(t, http.MethodPost, tr.gotMethod)
	assert.Equal(t, "https://api.example.test/v1/realtime/client_secrets", tr.gotURL)
	assert.Equal(t, "Bearer "+masterKey, tr.gotHeaders.Get("Authorization"))

	// (4) OpenAI-Safety-Identifier == principal.
	assert.Equal(t, principal, tr.gotHeaders.Get("OpenAI-Safety-Identifier"))

	// (2) the body fixes model/voice/tools/turn-detection FROM policy.
	var body openaiSettings
	require.NoError(t, json.Unmarshal(tr.gotBody, &body))
	assert.Equal(t, "realtime", body.Session.Type)
	assert.Equal(t, policy.Model, body.Session.Model)
	assert.Equal(t, policy.Instructions, body.Session.Instructions)
	require.NotNil(t, body.Session.Audio)
	assert.Equal(t, policy.Voice, body.Session.Audio.Output.Voice)
	require.Len(t, body.Session.Tools, 1)
	assert.Equal(t, "lookup_order", body.Session.Tools[0].Name)
	require.NotNil(t, body.Session.TurnDetection)
	assert.Equal(t, "server_vad", body.Session.TurnDetection.Type)
	assert.Equal(t, 500, body.Session.TurnDetection.SilenceDuration)

	// (4) creation TTL is NOT trusted: ExpiresAt is derived from policy.MaxDuration.
	gotTTL := time.Until(sess.ExpiresAt)
	assert.InDelta(t, policy.MaxDuration.Seconds(), gotTTL.Seconds(), 60,
		"OpenAI ExpiresAt must be derived from policy.MaxDuration, not the creation-only client-secret TTL")
}

func TestOpenAIDefaultModel(t *testing.T) {
	tr := &stubTransport{respBody: `{"value":"ek","session":{"id":"s"}}`}
	p := NewOpenAI(OpenAIConfig{APIKey: masterKey, Transport: tr})
	policy := governedPolicy()
	policy.Model = "" // no model => default
	sess, err := p.MintSession(context.Background(), policy, principal)
	require.NoError(t, err)
	assert.Equal(t, openaiDefaultModel, sess.Model)
	assert.True(t, strings.HasPrefix(sess.ConnectCoords, openaiDefaultBase), "default base host used")
}

func TestOpenAIMintHTTPError(t *testing.T) {
	tr := &stubTransport{status: http.StatusUnauthorized, respBody: `{"error":"bad key"}`}
	p := NewOpenAI(OpenAIConfig{APIKey: masterKey, Transport: tr})
	_, err := p.MintSession(context.Background(), governedPolicy(), principal)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), masterKey, "error must not leak the master key")
}

// --- ElevenLabs ------------------------------------------------------------------

func TestElevenLabsMintSession(t *testing.T) {
	const signed = "wss://api.elevenlabs.io/v1/convai/conversation?conversation_signature=signed-token-xyz"
	tr := &stubTransport{respBody: `{"signed_url":"` + signed + `"}`}
	p := NewElevenLabs(ElevenLabsConfig{
		APIKey:    masterKey,
		BaseURL:   "https://api.example.test",
		AgentID:   "agent_42",
		Transport: tr,
	})

	sess, err := p.MintSession(context.Background(), governedPolicy(), principal)
	require.NoError(t, err)

	// (1) signed URL is both credential and connect coords; master key absent.
	assert.Equal(t, "elevenlabs", sess.Provider)
	assert.Equal(t, "websocket", sess.Transport)
	assert.Equal(t, signed, sess.EphemeralCredential)
	assert.Equal(t, signed, sess.ConnectCoords)
	assertNoMasterKeyInSession(t, sess, masterKey)

	// (2) governed agent_id fixes the request; master key only in xi-api-key header.
	assert.Equal(t, http.MethodGet, tr.gotMethod)
	assert.Contains(t, tr.gotURL, "/v1/convai/conversation/get-signed-url")
	assert.Contains(t, tr.gotURL, "agent_id=agent_42")
	assert.Equal(t, masterKey, tr.gotHeaders.Get("xi-api-key"))
	assert.Empty(t, tr.gotHeaders.Get("Authorization"), "ElevenLabs uses xi-api-key, never Authorization")

	// ~15 min start window.
	assert.InDelta(t, elevenSignedTTL.Seconds(), time.Until(sess.ExpiresAt).Seconds(), 60)
}

func TestElevenLabsRequiresAgentID(t *testing.T) {
	p := NewElevenLabs(ElevenLabsConfig{APIKey: masterKey, Transport: &stubTransport{}})
	_, err := p.MintSession(context.Background(), governedPolicy(), principal)
	require.Error(t, err)
}

// --- Deepgram (Claude-as-think) --------------------------------------------------

func thinkPolicy() SessionPolicy {
	p := governedPolicy()
	p.TurnDetection = TurnDetection{
		Type:              "flux",
		EOTThreshold:      0.8,
		EagerEOTThreshold: 0.4,
		EOTTimeoutMS:      3000,
	}
	p.Think = &ThinkConfig{
		ProviderType: "anthropic",
		Model:        "claude-opus-4-8",
		EndpointURL:  "https://api.anthropic.com/v1/messages",
		Headers: map[string]string{
			"x-api-key":         anthropicKey,
			"anthropic-version": "2023-06-01",
		},
	}
	return p
}

func TestDeepgramBuildSettings(t *testing.T) {
	p := NewDeepgram(DeepgramConfig{APIKey: masterKey, Transport: &stubTransport{}})
	policy := thinkPolicy()
	s := p.buildSettings(policy)

	// (3) think.provider.type == "anthropic" with policy.Think.Model.
	assert.Equal(t, "anthropic", s.Agent.Think.Provider.Type)
	assert.Equal(t, "claude-opus-4-8", s.Agent.Think.Provider.Model)
	assert.Equal(t, "https://api.anthropic.com/v1/messages", s.Agent.Think.Endpoint.URL)

	// The BYO Anthropic key lives ONLY in the (server-side) endpoint headers.
	assert.Equal(t, anthropicKey, s.Agent.Think.Endpoint.Headers["x-api-key"])

	// Tools are projected as think functions from policy.
	require.Len(t, s.Agent.Think.Functions, 1)
	assert.Equal(t, "lookup_order", s.Agent.Think.Functions[0].Name)

	// Voice + Flux listen model fixed from policy.
	assert.Equal(t, policy.Voice, s.Agent.Speak.Provider.Model)
	assert.Equal(t, "flux-general-en", s.Agent.Listen.Provider.Model)

	// Flux end-of-turn params are governed from policy.
	flux := p.fluxTurnParams(policy.TurnDetection)
	require.NotNil(t, flux)
	assert.Equal(t, 0.8, flux["eot_threshold"])
	assert.Equal(t, 0.4, flux["eager_eot_threshold"])
	assert.Equal(t, 3000, flux["eot_timeout_ms"])
}

func TestDeepgramMintSession(t *testing.T) {
	tr := &stubTransport{respBody: `{"access_token":"dg_token_abc","expires_in":30}`}
	p := NewDeepgram(DeepgramConfig{APIKey: masterKey, BaseURL: "https://api.example.test", Transport: tr})

	sess, err := p.MintSession(context.Background(), thinkPolicy(), principal)
	require.NoError(t, err)

	// (1) ephemeral token + fixed agent WS; server-mediated; master + Anthropic key absent.
	assert.Equal(t, "deepgram", sess.Provider)
	assert.Equal(t, "websocket", sess.Transport)
	assert.True(t, sess.ServerMediated)
	assert.Equal(t, "dg_token_abc", sess.EphemeralCredential)
	assert.Equal(t, "wss://agent.deepgram.com/v1/agent/converse", sess.ConnectCoords)
	assert.Equal(t, "claude-opus-4-8", sess.Model)
	assertNoMasterKeyInSession(t, sess, masterKey, anthropicKey)

	// The grant request used "Token <key>" auth at the documented endpoint.
	assert.Equal(t, http.MethodPost, tr.gotMethod)
	assert.Equal(t, "https://api.example.test/v1/auth/grant", tr.gotURL)
	assert.Equal(t, "Token "+masterKey, tr.gotHeaders.Get("Authorization"))

	// The grant request body NEVER carries the Anthropic key (Settings are applied
	// over the agent WS server-side, not in the token grant).
	assert.NotContains(t, string(tr.gotBody), anthropicKey)

	// expires_in honored.
	assert.InDelta(t, 30, time.Until(sess.ExpiresAt).Seconds(), 5)
}

func TestDeepgramRequiresThink(t *testing.T) {
	p := NewDeepgram(DeepgramConfig{APIKey: masterKey, Transport: &stubTransport{}})
	policy := governedPolicy()
	policy.Think = nil // (3) MintSession must error if Think == nil.
	_, err := p.MintSession(context.Background(), policy, principal)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Think")
}

// --- Gemini ----------------------------------------------------------------------

func TestGeminiMintSession(t *testing.T) {
	tr := &stubTransport{respBody: `{"name":"authTokens/abc","token":"gm_token_xyz"}`}
	p := NewGemini(GeminiConfig{APIKey: masterKey, BaseURL: "https://api.example.test", Transport: tr})

	policy := governedPolicy()
	sess, err := p.MintSession(context.Background(), policy, principal)
	require.NoError(t, err)

	// (1) ephemeral token in credential + connect coords; master key absent.
	assert.Equal(t, "gemini", sess.Provider)
	assert.Equal(t, "websocket", sess.Transport)
	assert.Equal(t, "gm_token_xyz", sess.EphemeralCredential)
	assert.Contains(t, sess.ConnectCoords, "BidiGenerateContent")
	assert.Contains(t, sess.ConnectCoords, "access_token=gm_token_xyz")
	assertNoMasterKeyInSession(t, sess, masterKey)

	// (2) governed config + mandatory session resumption; master key only in header.
	assert.Equal(t, http.MethodPost, tr.gotMethod)
	assert.Equal(t, "https://api.example.test/v1alpha/auth_tokens", tr.gotURL)
	assert.Equal(t, masterKey, tr.gotHeaders.Get("x-goog-api-key"))

	var body geminiSettings
	require.NoError(t, json.Unmarshal(tr.gotBody, &body))
	require.NotNil(t, body.LiveConnectConstraints)
	assert.Equal(t, policy.Model, body.LiveConnectConstraints.Model)
	require.NotNil(t, body.LiveConnectConstraints.Config.SessionResumption,
		"Gemini session resumption is mandatory for >10 min sessions and must be enabled at mint")
	require.NotNil(t, body.LiveConnectConstraints.Config.SystemInstruction)
	assert.Equal(t, policy.Instructions, body.LiveConnectConstraints.Config.SystemInstruction.Parts[0].Text)
	require.Len(t, body.LiveConnectConstraints.Config.Tools, 1)
}

func TestGeminiResumeRequiresHandle(t *testing.T) {
	p := NewGemini(GeminiConfig{APIKey: masterKey, Transport: &stubTransport{}})
	_, err := p.Resume(context.Background(), "")
	require.Error(t, err)

	sess, err := p.Resume(context.Background(), "resume-handle-1")
	require.NoError(t, err)
	assert.Equal(t, "resume-handle-1", sess.SessionID)
}

// --- normalize: unified barge-in across all four providers ------------------------

// TestBargeInUnified is the central proof of the connector: each provider's distinct
// barge-in frame normalizes to the SAME EventUserStartedSpeaking kind.
func TestBargeInUnified(t *testing.T) {
	cases := []struct {
		name     string
		provider Provider
		raw      string
	}{
		{"openai", NewOpenAI(OpenAIConfig{}), `{"type":"input_audio_buffer.speech_started"}`},
		{"deepgram", NewDeepgram(DeepgramConfig{}), `{"type":"UserStartedSpeaking"}`},
		{"elevenlabs", NewElevenLabs(ElevenLabsConfig{}), `{"type":"interruption"}`},
		{"gemini", NewGemini(GeminiConfig{}), `{"serverContent":{"interrupted":true}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := normalizeOf(tc.provider, []byte(tc.raw))
			require.True(t, ok, "barge-in frame must be recognized")
			assert.Equal(t, EventUserStartedSpeaking, ev.Kind,
				"%s barge-in must normalize to the unified EventUserStartedSpeaking", tc.name)
		})
	}
}

// normalizeOf dispatches to the concrete provider's unexported normalize so the shared
// barge-in table can drive all four through one call site.
func normalizeOf(p Provider, raw []byte) (Event, bool) {
	switch v := p.(type) {
	case *OpenAIProvider:
		return v.normalize(raw)
	case *DeepgramProvider:
		return v.normalize(raw)
	case *ElevenLabsProvider:
		return v.normalize(raw)
	case *GeminiProvider:
		return v.normalize(raw)
	default:
		return Event{}, false
	}
}

// TestNormalizeTaxonomy spot-checks non-barge-in mappings per provider, including the
// minimal-data rule that a transcript delta carries only a label, never the text.
func TestNormalizeTaxonomy(t *testing.T) {
	oa := NewOpenAI(OpenAIConfig{})
	dg := NewDeepgram(DeepgramConfig{})
	el := NewElevenLabs(ElevenLabsConfig{})
	gm := NewGemini(GeminiConfig{})

	type tc struct {
		raw  string
		kind EventKind
		ev   func([]byte) (Event, bool)
	}
	cases := []tc{
		{`{"type":"session.created"}`, EventSessionStarted, oa.normalize},
		{`{"type":"response.done"}`, EventAgentDone, oa.normalize},
		{`{"type":"response.function_call_arguments.done"}`, EventToolCall, oa.normalize},
		{`{"type":"error"}`, EventError, oa.normalize},
		{`{"type":"Welcome"}`, EventSessionStarted, dg.normalize},
		{`{"type":"AgentStartedSpeaking"}`, EventAgentSpeaking, dg.normalize},
		{`{"type":"AgentAudioDone"}`, EventAgentDone, dg.normalize},
		{`{"type":"FunctionCallRequest"}`, EventToolCall, dg.normalize},
		{`{"type":"Error"}`, EventError, dg.normalize},
		{`{"type":"conversation_initiation_metadata"}`, EventSessionStarted, el.normalize},
		{`{"type":"agent_response"}`, EventAgentSpeaking, el.normalize},
		{`{"type":"client_tool_call"}`, EventToolCall, el.normalize},
		{`{"setupComplete":{}}`, EventSessionStarted, gm.normalize},
		{`{"toolCall":{}}`, EventToolCall, gm.normalize},
		{`{"serverContent":{"turnComplete":true}}`, EventAgentDone, gm.normalize},
	}
	for _, c := range cases {
		ev, ok := c.ev([]byte(c.raw))
		require.Truef(t, ok, "frame %s must be recognized", c.raw)
		assert.Equalf(t, c.kind, ev.Kind, "frame %s -> %s", c.raw, c.kind)
	}
}

// TestDeepgramTranscriptDeltaLabelOnly proves the minimal-data invariant in normalize:
// a ConversationText frame yields a TranscriptDelta whose Detail is the ROLE only —
// the spoken "content" is never decoded into the Event.
func TestDeepgramTranscriptDeltaLabelOnly(t *testing.T) {
	dg := NewDeepgram(DeepgramConfig{})
	const secretText = "my social security number is 123-45-6789"
	ev, ok := dg.normalize([]byte(`{"type":"ConversationText","role":"user","content":"` + secretText + `"}`))
	require.True(t, ok)
	assert.Equal(t, EventTranscriptDelta, ev.Kind)
	assert.Equal(t, "user", ev.Detail, "detail must be the role label only")
	assert.NotContains(t, ev.Detail, secretText, "transcript text must NEVER appear in the event")
}

// TestNormalizeUnknownFrame proves unknown/garbage frames are dropped honestly.
func TestNormalizeUnknownFrame(t *testing.T) {
	dg := NewDeepgram(DeepgramConfig{})
	_, ok := dg.normalize([]byte(`{"type":"SomeFutureFrame"}`))
	assert.False(t, ok)
	_, ok = dg.normalize([]byte(`not json`))
	assert.False(t, ok)
}

// --- constructors + StreamEvents -------------------------------------------------

// TestNilTransportFallsBackToHTTPClient proves (6): a nil Transport constructor does
// not panic and installs a real *http.Client (no network is touched — construct only).
func TestNilTransportFallsBackToHTTPClient(t *testing.T) {
	require.NotPanics(t, func() {
		_ = NewOpenAI(OpenAIConfig{APIKey: "k"})
		_ = NewElevenLabs(ElevenLabsConfig{APIKey: "k", AgentID: "a"})
		_ = NewDeepgram(DeepgramConfig{APIKey: "k"})
		_ = NewGemini(GeminiConfig{APIKey: "k"})
	})
	oa := NewOpenAI(OpenAIConfig{APIKey: "k"})
	_, isClient := oa.tr.(*http.Client)
	assert.True(t, isClient, "nil Transport must fall back to *http.Client")
}

// TestStreamEventsHonestlyEmpty proves StreamEvents returns an immediately-closed
// channel (the live read loop is the data plane's concern; no fabricated events).
func TestStreamEventsHonestlyEmpty(t *testing.T) {
	for _, p := range []Provider{
		NewOpenAI(OpenAIConfig{}),
		NewDeepgram(DeepgramConfig{}),
		NewElevenLabs(ElevenLabsConfig{}),
		NewGemini(GeminiConfig{}),
	} {
		ch, err := p.StreamEvents(context.Background(), "sess")
		require.NoError(t, err)
		count := 0
		for range ch { // closed channel => loop terminates immediately
			count++
		}
		assert.Equal(t, 0, count, "%s StreamEvents must not fabricate events", p.Name())
	}
}

// TestTerminateIdempotent proves Terminate is a safe idempotent no-op.
func TestTerminateIdempotent(t *testing.T) {
	for _, p := range []Provider{
		NewOpenAI(OpenAIConfig{}),
		NewDeepgram(DeepgramConfig{}),
		NewElevenLabs(ElevenLabsConfig{}),
		NewGemini(GeminiConfig{}),
	} {
		require.NoError(t, p.Terminate(context.Background(), "sess", "done"))
		require.NoError(t, p.Terminate(context.Background(), "sess", "done"))
	}
}
