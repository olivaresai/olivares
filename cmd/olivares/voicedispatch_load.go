// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"time"

	voiceconn "github.com/olivaresai/olivares/connectors/voice"
)

// voicedispatch_load.go loads the voice Dispatcher from the operator-provisioned
// config (OLIVARES_VOICE_DISPATCH_CONFIG), mirroring loadDeployExecutorConfig /
// loadOrchDispatchConfig: an absent path keeps the deny-closed unwiredDispatcher,
// while a supplied unreadable/invalid file fails startup. Provider master keys and
// the BYO Anthropic key live ONLY here, never in the module store or a returned session.

// voiceDispatchConfig is the operator's voice-actuation provisioning: the provider
// credentials/endpoints and the per-agent governed session policies.
type voiceDispatchConfig struct {
	Providers     []voiceProviderJSON `json:"providers"`
	Policies      []voicePolicyJSON   `json:"policies"`
	DefaultPolicy *voicePolicyJSON    `json:"default_policy,omitempty"`
}

// voiceProviderJSON configures one realtime provider. Kind selects the adapter
// (openai|elevenlabs|deepgram|gemini); Ref is the provider_ref the OpenRequest
// carries (defaults to Kind). APIKey is the provider MASTER key (server-only). Think
// is the Claude-as-think brain wiring for carriers that reason via Claude (Deepgram).
type voiceProviderJSON struct {
	Ref     string          `json:"ref"`
	Kind    string          `json:"kind"`
	APIKey  string          `json:"api_key"`
	BaseURL string          `json:"base_url"`
	AgentID string          `json:"agent_id"` // ElevenLabs
	Think   *voiceThinkJSON `json:"think,omitempty"`
}

// voiceThinkJSON is the server-side Claude-as-think wiring: the Messages API endpoint
// and the BYO Anthropic credential (held here by value, never returned to a client).
type voiceThinkJSON struct {
	EndpointURL      string `json:"endpoint_url"`
	AnthropicKey     string `json:"anthropic_key"`
	AnthropicVersion string `json:"anthropic_version"`
}

// voicePolicyJSON is one agent's governed session settings. These are fixed
// server-side; the client cannot escalate them.
type voicePolicyJSON struct {
	AgentRef           string            `json:"agent_ref"`
	ProviderRef        string            `json:"provider_ref"`
	Model              string            `json:"model"`
	ThinkModel         string            `json:"think_model"`
	Voice              string            `json:"voice"`
	Instructions       string            `json:"instructions"`
	Language           string            `json:"language"`
	MaxDurationSeconds int               `json:"max_duration_seconds"`
	TurnDetection      turnDetectionJSON `json:"turn_detection"`
	Tools              []toolJSON        `json:"tools"`
}

type turnDetectionJSON struct {
	Type              string  `json:"type"`
	EOTThreshold      float64 `json:"eot_threshold"`
	EagerEOTThreshold float64 `json:"eager_eot_threshold"`
	EOTTimeoutMS      int     `json:"eot_timeout_ms"`
	SilenceMS         int     `json:"silence_ms"`
}

type toolJSON struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// defaultAnthropicVersion is the stable Anthropic Messages API version header value
// used for the Claude-as-think brain when the operator does not pin one. It is the
// long-standing documented version, not a fabricated id.
const defaultAnthropicVersion = "2023-06-01"

// loadVoiceDispatchConfig reads OLIVARES_VOICE_DISPATCH_CONFIG. A missing path is an
// empty config (dispatcher not wired; opens stay declared-not-opened). A supplied path
// must be readable and contain valid JSON or startup fails closed.
func loadVoiceDispatchConfig(_ *slog.Logger) (voiceDispatchConfig, error) {
	path := os.Getenv("OLIVARES_VOICE_DISPATCH_CONFIG")
	if path == "" {
		return voiceDispatchConfig{}, nil
	}
	var cfg voiceDispatchConfig
	if err := loadOperatorJSONConfig("OLIVARES_VOICE_DISPATCH_CONFIG", path, &cfg); err != nil {
		return voiceDispatchConfig{}, err
	}
	return cfg, nil
}

// newVoiceDispatcher builds the real voice.Dispatcher from config, or nil when no
// provider OR no usable policy is provisioned (the module then keeps its deny-closed
// unwiredDispatcher).
func newVoiceDispatcher(cfg voiceDispatchConfig, log *slog.Logger) *voiceDispatcher {
	providers := make(map[string]voiceconn.Provider)
	think := make(map[string]thinkDefaults)
	for _, p := range cfg.Providers {
		ref := orDefaultStr(p.Ref, p.Kind)
		prov := buildVoiceProvider(p)
		if prov == nil {
			log.Warn("voice-dispatch: skipping provider with unknown kind", "kind", p.Kind, "ref", ref)
			continue
		}
		providers[ref] = prov
		if p.Think != nil {
			think[ref] = thinkDefaults{
				endpointURL: orDefaultStr(p.Think.EndpointURL, "https://api.anthropic.com/v1/messages"),
				headers:     anthropicHeaders(p.Think),
			}
		}
	}

	policies := make(map[string]voiceSessionPolicy)
	for _, p := range cfg.Policies {
		if strings.TrimSpace(p.AgentRef) == "" {
			log.Warn("voice-dispatch: skipping policy with empty agent_ref")
			continue
		}
		policies[p.AgentRef] = toSessionPolicy(p)
	}
	var fallback *voiceSessionPolicy
	if cfg.DefaultPolicy != nil {
		fp := toSessionPolicy(*cfg.DefaultPolicy)
		fallback = &fp
	}

	if len(providers) == 0 || (len(policies) == 0 && fallback == nil) {
		return nil
	}
	log.Info("voice-dispatch: voice dispatcher wired (module XVI now ACTS)", "providers", len(providers), "policies", len(policies), "default_policy", fallback != nil)
	return &voiceDispatcher{providers: providers, think: think, policies: policies, fallback: fallback, log: log}
}

// buildVoiceProvider constructs the connector adapter for a provider kind, or nil for
// an unknown kind.
func buildVoiceProvider(p voiceProviderJSON) voiceconn.Provider {
	switch strings.ToLower(strings.TrimSpace(p.Kind)) {
	case "openai":
		return voiceconn.NewOpenAI(voiceconn.OpenAIConfig{APIKey: p.APIKey, BaseURL: p.BaseURL})
	case "elevenlabs":
		return voiceconn.NewElevenLabs(voiceconn.ElevenLabsConfig{APIKey: p.APIKey, BaseURL: p.BaseURL, AgentID: p.AgentID})
	case "deepgram":
		return voiceconn.NewDeepgram(voiceconn.DeepgramConfig{APIKey: p.APIKey, BaseURL: p.BaseURL})
	case "gemini":
		return voiceconn.NewGemini(voiceconn.GeminiConfig{APIKey: p.APIKey, BaseURL: p.BaseURL})
	default:
		return nil
	}
}

// anthropicHeaders builds the server-held BYO Messages API auth headers (x-api-key +
// anthropic-version). These never leave the server (Deepgram applies them server-side).
func anthropicHeaders(t *voiceThinkJSON) map[string]string {
	h := map[string]string{"anthropic-version": orDefaultStr(t.AnthropicVersion, defaultAnthropicVersion)}
	if strings.TrimSpace(t.AnthropicKey) != "" {
		h["x-api-key"] = t.AnthropicKey
	}
	return h
}

// toSessionPolicy maps the operator JSON to the resolved governed policy.
func toSessionPolicy(p voicePolicyJSON) voiceSessionPolicy {
	return voiceSessionPolicy{
		providerRef:  p.ProviderRef,
		model:        p.Model,
		thinkModel:   p.ThinkModel,
		voice:        p.Voice,
		instructions: p.Instructions,
		language:     p.Language,
		maxDuration:  time.Duration(p.MaxDurationSeconds) * time.Second,
		turn: voiceconn.TurnDetection{
			Type:              p.TurnDetection.Type,
			EOTThreshold:      p.TurnDetection.EOTThreshold,
			EagerEOTThreshold: p.TurnDetection.EagerEOTThreshold,
			EOTTimeoutMS:      p.TurnDetection.EOTTimeoutMS,
			SilenceMS:         p.TurnDetection.SilenceMS,
		},
		tools: toVoiceTools(p.Tools),
	}
}

func toVoiceTools(in []toolJSON) []voiceconn.Tool {
	if len(in) == 0 {
		return nil
	}
	out := make([]voiceconn.Tool, 0, len(in))
	for _, t := range in {
		out = append(out, voiceconn.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return out
}
