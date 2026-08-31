// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	voiceconn "github.com/olivaresai/olivares/connectors/voice"
	"github.com/olivaresai/olivares/modules/security"
	voicemod "github.com/olivaresai/olivares/modules/voice"
)

const (
	envVoiceCallConfig     = "OLIVARES_VOICE_CALL_CONFIG"
	defaultVoiceCallListen = "127.0.0.1:8456"
)

// voiceCallConfig is the optional inbound OpenAI Realtime SIP call-plane
// provisioning. It mirrors the HITL receiver posture: a secret-bearing operator
// config file, out of the store; absent or invalid means the webhook is not mounted.
type voiceCallConfig struct {
	Listen           string `json:"listen"`
	WebhookSecret    string `json:"webhook_secret"`
	Tenant           string `json:"tenant"`
	ProjectRef       string `json:"project_ref"`
	ProviderRef      string `json:"provider_ref"`
	MaxObservers     int    `json:"max_observers"`
	StopSweepSeconds int    `json:"stop_sweep_seconds"`
}

func loadVoiceCallConfig(_ *slog.Logger) (voiceCallConfig, error) {
	path := os.Getenv(envVoiceCallConfig)
	if path == "" {
		return voiceCallConfig{}, nil
	}
	var cfg voiceCallConfig
	if err := loadOperatorJSONConfig(envVoiceCallConfig, path, &cfg); err != nil {
		return voiceCallConfig{}, err
	}
	return cfg, nil
}

func voiceCallModuleConfig(cfg voiceCallConfig, log *slog.Logger) (voicemod.CallConfig, bool) {
	// shared deny-closed policy. The two cases are logged differently on purpose —
	// a PRESENT but invalid tenant is an operator typo worth an Error naming the value,
	// whereas an ABSENT tenant is only worth a Warn, and only when a webhook secret says
	// somebody meant to provision this receiver.
	tenant, present, terr := parseBusinessTenant("voice-call config: tenant", cfg.Tenant)
	if terr != nil {
		log.Error("voice-call: configured tenant is invalid; receiver NOT mounted", "err", terr)
		return voicemod.CallConfig{}, false
	}
	if !present {
		if strings.TrimSpace(cfg.WebhookSecret) != "" {
			log.Warn("voice-call: webhook secret configured but no tenant; receiver not mounted")
		}
		return voicemod.CallConfig{}, false
	}
	return voicemod.CallConfig{
		Tenant:            tenant,
		WorkspaceRef:      strings.TrimSpace(cfg.ProjectRef),
		MaxObservers:      cfg.MaxObservers,
		StopSweepInterval: time.Duration(cfg.StopSweepSeconds) * time.Second,
	}, true
}

type openAICallCredentials struct {
	apiKey  string
	baseURL string
}

func openAICallCredentialsFromVoiceDispatch(cfg voiceDispatchConfig, ref string) (openAICallCredentials, bool) {
	want := strings.TrimSpace(ref)
	if want == "" {
		want = "openai"
	}
	for _, p := range cfg.Providers {
		provRef := strings.TrimSpace(orDefaultStr(p.Ref, p.Kind))
		if provRef != want || strings.ToLower(strings.TrimSpace(p.Kind)) != "openai" {
			continue
		}
		if strings.TrimSpace(p.APIKey) == "" {
			return openAICallCredentials{}, false
		}
		return openAICallCredentials{apiKey: p.APIKey, baseURL: p.BaseURL}, true
	}
	return openAICallCredentials{}, false
}

type voiceCallController struct {
	client voiceconn.CallClient
}

var _ voicemod.CallController = voiceCallController{}

func newVoiceCallController(dispatchCfg voiceDispatchConfig, callCfg voiceCallConfig, log *slog.Logger) (voicemod.CallController, voicemod.SidebandAttacher) {
	creds, ok := openAICallCredentialsFromVoiceDispatch(dispatchCfg, callCfg.ProviderRef)
	if !ok {
		if strings.TrimSpace(callCfg.WebhookSecret) != "" {
			log.Warn("voice-call: OpenAI provider credentials not found in OLIVARES_VOICE_DISPATCH_CONFIG; call controller not wired")
		}
		return nil, nil
	}
	ctrl := voiceCallController{client: voiceconn.CallClient{APIKey: creds.apiKey, BaseURL: creds.baseURL}}
	dial := voiceconn.NewOpenAISidebandDialer(creds.apiKey, creds.baseURL)
	attach := func(ctx context.Context, callID string) (voicemod.CallSideband, error) {
		return dial(ctx, callID)
	}
	log.Info("voice-call: OpenAI Realtime SIP call controller wired", "provider_ref", orDefaultStr(callCfg.ProviderRef, "openai"))
	return ctrl, attach
}

func (c voiceCallController) Accept(ctx context.Context, callID string, cfg voicemod.CallAccept) error {
	return c.client.Accept(ctx, callID, voiceconn.AcceptConfig{Model: cfg.Model, Instructions: cfg.Instructions})
}

func (c voiceCallController) Reject(ctx context.Context, callID string, statusCode int) error {
	return c.client.Reject(ctx, callID, statusCode)
}

func (c voiceCallController) Hangup(ctx context.Context, callID string) error {
	return c.client.Hangup(ctx, callID)
}

type voiceTranscriptClassifier struct{}

var _ voicemod.TranscriptClassifier = voiceTranscriptClassifier{}

func (voiceTranscriptClassifier) Classify(text string) ([]voicemod.SensitivityHit, error) {
	hits := security.ClassifySensitivity(text)
	out := make([]voicemod.SensitivityHit, len(hits))
	for i, h := range hits {
		out[i] = voicemod.SensitivityHit{Class: h.Class, Rule: h.Rule, Count: h.Count, Severity: string(h.Severity)}
	}
	return out, nil
}

var voiceWebhookBinding struct {
	sync.RWMutex
	module *voicemod.Module
}

func bindVoiceWebhookModule(m *voicemod.Module) {
	voiceWebhookBinding.Lock()
	voiceWebhookBinding.module = m
	voiceWebhookBinding.Unlock()
}

func currentVoiceWebhookModule() *voicemod.Module {
	voiceWebhookBinding.RLock()
	defer voiceWebhookBinding.RUnlock()
	return voiceWebhookBinding.module
}

func buildVoiceWebhookServer(eng *engine, insecure bool, log *slog.Logger) (*http.Server, error) {
	cfg, err := loadVoiceCallConfig(log)
	if err != nil {
		return nil, fmt.Errorf("load voice call operator config: %w", err)
	}
	if strings.TrimSpace(cfg.WebhookSecret) == "" {
		log.Info("voice-call: OpenAI Realtime webhook receiver not mounted (OLIVARES_VOICE_CALL_CONFIG unset or webhook_secret empty)")
		return nil, nil
	}
	if _, ok := voiceCallModuleConfig(cfg, log); !ok {
		return nil, nil
	}
	mod := currentVoiceWebhookModule()
	if mod == nil {
		log.Warn("voice-call: voice module unavailable; OpenAI Realtime webhook receiver not mounted")
		return nil, nil
	}
	addr := cfg.Listen
	if addr == "" {
		addr = defaultVoiceCallListen
	}
	if !insecure && !hostIsLoopback(addr) {
		log.Warn("voice-call: webhook receiver bound to a NON-loopback address; front it with your ingress — its security is fail-closed signature verification, not network isolation", "addr", addr)
	}
	mux := http.NewServeMux()
	mux.Handle("/webhooks/openai-realtime", mod.RealtimeWebhookHandler(cfg.WebhookSecret))
	srv := eng.api.NewHTTPServer(addr)
	srv.Handler = mux
	log.Info("voice-call: OpenAI Realtime webhook receiver mounted", "addr", addr, "path", "/webhooks/openai-realtime")
	return srv, nil
}
