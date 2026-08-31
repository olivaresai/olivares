// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package local is the Olivares AI connector for local model inference: Ollama and
// vLLM. It exposes a modelprovider.Catalog of the models loaded on each local
// server (with a coarse endpoint latency for the router's latency policy) and, for
// vLLM, emits model.CostSample usage from the Prometheus token counters. Local
// inference has no $/token list price — cost is compute, not API billing — so the
// derived monetary amount is zero unless the operator declares a $/MTok compute
// rate (cost_per_mtok_usd); the value here is the token usage and latency.
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md-3): it performs only GETs against
// the operator's own inference servers, reads token counts and model metadata, and
// carries no prompts or outputs. It imports only the SDK and the Apache
// modelprovider contract, never the engine.
package local

import (
	"context"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.local"

// subjectResidency is the subject kind for "this model is loaded right now", which
// is a different assertion from the catalog's "this model is installed".
const subjectResidency = "local.residency"

const defaultOllamaURL = "http://localhost:11434"

// Source is the local-inference source connector. It satisfies sdk.SourceConnector
// (vLLM token usage as observations) and modelprovider.CatalogProvider (the Ollama
// + vLLM model catalog).
type Source struct {
	ollamaURL    string
	vllmURL      string
	vllmKey      string
	costPerMTok  float64
	doer         modelprovider.Doer
	now          func() time.Time
	ollamaClient *modelprovider.Client
	vllmClient   *modelprovider.Client
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns a local connector with Ollama enabled on its default URL and vLLM
// disabled until configured.
func New() *Source {
	return &Source{ollamaURL: defaultOllamaURL}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Local inference (Ollama + vLLM)",
		Description: "Reads local model catalogs (Ollama, vLLM) and vLLM token usage (read-only).",
		ConfigFields: []sdk.ConfigField{
			{Key: "ollama_url", Type: sdk.FieldString, Default: defaultOllamaURL, Description: "Ollama base URL (empty disables Ollama)."},
			{Key: "vllm_url", Type: sdk.FieldString, Description: "vLLM OpenAI-compatible base URL (empty disables vLLM)."},
			{Key: "vllm_api_key", Type: sdk.FieldString, Secret: true, Description: "Optional bearer token for a protected vLLM server."},
			{Key: "cost_per_mtok_usd", Type: sdk.FieldString, Description: "Optional operator-declared compute cost in USD per million tokens (default 0: local is compute, not $/token)."},
		},
	}
}

// Open reads configuration and builds the read-only clients for whichever local
// servers are enabled. A missing setting simply disables that source; Open never
// fails for a disabled server.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v, ok := cfg.Lookup("ollama_url"); ok {
		s.ollamaURL = v // an explicit empty value disables Ollama
	}
	s.vllmURL = cfg.Get("vllm_url")
	s.vllmKey = cfg.Get("vllm_api_key")
	if v := cfg.Get("cost_per_mtok_usd"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			s.costPerMTok = f
		}
	}
	if s.ollamaURL != "" {
		s.ollamaClient = modelprovider.NewClient(s.ollamaURL, s.doer, modelprovider.AuthNone, "", nil)
	}
	if s.vllmURL != "" {
		scheme, cred := modelprovider.AuthNone, ""
		if s.vllmKey != "" {
			scheme, cred = modelprovider.AuthBearer, s.vllmKey
		}
		s.vllmClient = modelprovider.NewClient(s.vllmURL, s.doer, scheme, cred, nil)
	}
	return nil
}

// Gather scrapes vLLM's Prometheus token counters and emits one CostSample per
// model. The counters are cumulative, so each gather is a running-total snapshot
// (the engine/FinOps diff successive samples). Ollama exposes no aggregate token
// metrics, so it contributes no METERING — but it does contribute RESIDENCY posture
// from /api/ps, which is what it is holding in memory right now rather than what it
// could serve. A batch source: returns nil when the scrape completes.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	// RESIDENCY FIRST, and it is not metering: /api/ps answers which models are LOADED
	// right now, which /api/tags cannot. Without it this connector reports what is
	// INSTALLED and calls it local inference posture — the gap the connectors reference
	// used to have to declare in prose.
	if err := s.gatherOllamaResidency(ctx, sink); err != nil {
		return err
	}
	if s.vllmClient == nil {
		return nil // no vLLM configured: nothing to meter
	}
	body, err := s.vllmClient.GetText(ctx, "/metrics", nil)
	if err != nil {
		return err
	}
	occurred := s.clock().UTC()
	for modelRef, tok := range parseVLLMTokens(body) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		u := modelprovider.Usage{
			ProviderRef:  modelprovider.ProviderVLLM,
			ModelRef:     modelRef,
			InputTokens:  tok.prompt,
			OutputTokens: tok.generation,
			OccurredAt:   occurred,
		}
		if err := sink.Emit(ctx, s.usageSample(u)); err != nil {
			return err
		}
	}
	return nil
}

// gatherOllamaResidency emits one posture observation per RESIDENT model, from
// GET /api/ps. It is deliberately separate from Snapshot: the catalog is what the
// server COULD serve, and this is what it is holding in memory right now, with the
// GPU/CPU split and the unload deadline that only this endpoint carries.
//
// Offline (no Ollama configured) it emits nothing rather than a finding: an absent
// server is not a posture, and inventing one would make "not configured" and
// "configured and empty" indistinguishable.
func (s *Source) gatherOllamaResidency(ctx context.Context, sink sdk.Sink) error {
	if s.ollamaClient == nil {
		return nil
	}
	var resp ollamaPSResponse
	if _, err := s.timedGetJSON(ctx, s.ollamaClient, "/api/ps", &resp); err != nil {
		return err
	}
	occurred := s.clock().UTC()
	for _, m := range resp.Models {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := m.Name
		if name == "" {
			name = m.Model
		}
		if err := sink.Emit(ctx, s.residencyPosture(name, m, occurred)); err != nil {
			return err
		}
	}
	return nil
}

// residencyPosture describes one resident model. The severity is the placement, not
// the model: a model that is SPLIT between GPU and CPU, or fully on the CPU, is the
// one an operator is paying latency for without being told, so it is reported as a
// warning while a fully resident GPU model is information.
func (s *Source) residencyPosture(name string, m ollamaLoadedModel, at time.Time) model.FindingReport {
	placement := "gpu"
	sev := model.SeverityInfo
	switch {
	case m.SizeVRAM == 0:
		placement, sev = "cpu", model.SeverityMedium
	case m.SizeVRAM < m.Size:
		placement, sev = "split gpu/cpu", model.SeverityMedium
	}
	return model.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: subjectResidency,
		SubjectRef:  name,
		Title: "Ollama model resident on " + placement + ": " + name +
			" (" + strconv.FormatInt(m.SizeVRAM, 10) + " of " +
			strconv.FormatInt(m.Size, 10) + " bytes in VRAM)",
		DetailHash: redact.Hash("ollama resident model " + name +
			" placement=" + placement +
			" size=" + strconv.FormatInt(m.Size, 10) +
			" size_vram=" + strconv.FormatInt(m.SizeVRAM, 10) +
			" expires_at=" + m.ExpiresAt),
		OccurredAt: at,
	}
}

// usageSample turns local usage into a CostSample. With an operator compute rate it
// derives a cost; otherwise the monetary amount is zero (local is not $/token).
func (s *Source) usageSample(u modelprovider.Usage) model.CostSample {
	if s.costPerMTok > 0 {
		return modelprovider.ToCostSample(u, s.operatorPricing())
	}
	return modelprovider.ToCostSampleWithCost(u, 0)
}

// operatorPricing builds the declared operator compute rate as a flat $/MTok on
// input and output tokens.
func (s *Source) operatorPricing() modelprovider.ModelPricing {
	return modelprovider.ModelPricing{
		InputPerMTokUSD:  s.costPerMTok,
		OutputPerMTokUSD: s.costPerMTok,
		Currency:         "USD",
		AsOf:             s.clock().UTC().Format("2006-01-02"),
		Source:           modelprovider.PricingOperator,
	}
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// Snapshot returns the local catalog: the models on each enabled server. The
// catalog-level Provider is an umbrella ("local"); the authoritative per-model
// provider is each Model.ProviderRef (ollama or vllm). Pricing is the operator
// compute rate when declared, else nil (local has no list price). ObservedLatency
// is the endpoint round-trip, a coarse latency signal for the router.
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: "local", Kind: modelprovider.KindLocalInference,
			Title: "Local inference (Ollama + vLLM)", Local: true,
		},
		CapturedAt: s.clock().UTC(),
	}

	if s.ollamaClient != nil {
		models, err := s.ollamaModels(ctx)
		if err != nil {
			return modelprovider.Catalog{}, err
		}
		cat.Models = append(cat.Models, models...)
	}
	if s.vllmClient != nil {
		models, err := s.vllmModels(ctx)
		if err != nil {
			return modelprovider.Catalog{}, err
		}
		cat.Models = append(cat.Models, models...)
	}
	return cat, nil
}

// ollamaModels lists /api/tags and maps each tag to a local model.
func (s *Source) ollamaModels(ctx context.Context) ([]modelprovider.Model, error) {
	var resp ollamaTagsResponse
	latency, err := s.timedGetJSON(ctx, s.ollamaClient, "/api/tags", &resp)
	if err != nil {
		return nil, err
	}
	out := make([]modelprovider.Model, 0, len(resp.Models))
	for _, m := range resp.Models {
		out = append(out, s.localModel(modelprovider.ProviderOllama, m.Name, m.Name, latency))
	}
	return out, nil
}

// vllmModels lists the OpenAI-compatible /v1/models and maps each to a local model.
func (s *Source) vllmModels(ctx context.Context) ([]modelprovider.Model, error) {
	var resp vllmModelsResponse
	latency, err := s.timedGetJSON(ctx, s.vllmClient, "/v1/models", &resp)
	if err != nil {
		return nil, err
	}
	out := make([]modelprovider.Model, 0, len(resp.Data))
	for _, m := range resp.Data {
		out = append(out, s.localModel(modelprovider.ProviderVLLM, m.ID, m.ID, latency))
	}
	return out, nil
}

// localModel assembles a Model for a local server, attaching the operator compute
// price when one is declared and the measured endpoint latency.
func (s *Source) localModel(providerRef, ref, display string, latencyMillis int64) modelprovider.Model {
	m := modelprovider.Model{
		ProviderRef:           providerRef,
		Ref:                   ref,
		DisplayName:           display,
		ObservedLatencyMillis: latencyMillis,
		Capabilities:          []modelprovider.Capability{modelprovider.CapStreaming, modelprovider.CapToolUse},
	}
	if s.costPerMTok > 0 {
		p := s.operatorPricing()
		m.Pricing = &p
	}
	return m
}

// timedGetJSON issues a GetJSON and returns the round-trip latency in
// milliseconds, a coarse signal the router's latency policy can use.
func (s *Source) timedGetJSON(ctx context.Context, c *modelprovider.Client, path string, out any) (int64, error) {
	start := s.clock()
	err := c.GetJSON(ctx, path, nil, out)
	if err != nil {
		return 0, err
	}
	return s.clock().Sub(start).Milliseconds(), nil
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
