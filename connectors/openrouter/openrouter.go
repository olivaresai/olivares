// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package openrouter is the Olivares AI governance connector for OpenRouter — the
// unified aggregation gateway that fronts 400+ models across many upstream
// providers behind one API key. It is a read-only, minimal-data governance source
// built on the shared connectors/modelprovider contract.
//
// WHAT IT READS (GET-only, never prompts or completions):
//   - the LIVE model catalog (GET /api/v1/models) with per-token list pricing,
//     converted to USD/MTok — the source of truth for which models an OpenRouter
//     key can reach and what they cost (VERIFIED-SHAPE);
//   - the account usage/limit posture (GET /api/v1/auth/key) — spend, cap and
//     remaining credit for the configured key (VERIFIED-SHAPE).
//
// WHAT IT GOVERNS:
//   - an APPROVED-MODEL policy (allow/deny) evaluated against the live catalog: a
//     denied model that is reachable is a posture finding; an approved model that
//     is missing or deprecated is flagged so the allowlist stays honest;
//   - COST + DRIFT at the point of use via the exported Meter helper — the caller
//     that drove an OpenRouter call prices it from the live catalog and learns
//     whether the model was policy-approved (the honest per-call drift signal).
//
// HONEST SCOPE. OpenRouter's grouped-analytics endpoint (/api/v1/analytics/
// activity) is BETA; its response shape could not be pinned to a stable schema
// offline, so per-model batch usage is NOT read here (it would be a fabricated
// shape). Per-model/per-user cost is produced by Meter instead — the same honest
// pattern as connectors/deepseek and connectors/mistral. It imports only the SDK
// and the Apache modelprovider contract, never the engine (/core).
package openrouter

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.openrouter"

const (
	defaultBaseURL = "https://openrouter.ai/api/v1"
	modelsPath     = "/models"
	keyPath        = "/auth/key"

	// costTypeOpenRouter tags every OpenRouter CostSample so FinOps attributes
	// OpenRouter-routed spend distinctly from the underlying upstream providers.
	costTypeOpenRouter = "openrouter"
)

// Finding subjects for the OpenRouter governance posture findings.
const (
	subjectAccount = "openrouter.account"
	subjectPolicy  = "openrouter.model_policy"
)

// Source is the OpenRouter governance source connector. It satisfies
// sdk.SourceConnector (account + model-policy posture) and
// modelprovider.CatalogProvider (the live model catalog).
type Source struct {
	client *modelprovider.Client

	credential string
	baseURL    string
	policy     modelPolicy

	doer modelprovider.Doer // injected transport (tests); nil => default
	now  func() time.Time   // injectable clock (tests); nil => time.Now
}

var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns an OpenRouter source with default configuration.
func New() *Source {
	return &Source{baseURL: defaultBaseURL}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "OpenRouter (catalog + account posture + model policy + cost metering)",
		Description: "Read-only OpenRouter governance: live model catalog (GET /api/v1/models, per-token list pricing → USD/MTok), account usage/limit posture (GET /api/v1/auth/key), an approved-model allow/deny policy evaluated against the live catalog, and cost + policy-drift metering around the inference path (Meter). " +
			"OpenRouter fronts 400+ models across many upstream providers behind one key; spend is tracked distinctly under the \"openrouter\" cost type. " +
			"The beta grouped-analytics endpoint is not read (unstable shape); per-model/per-user cost is produced by Meter. Reads model metadata and account posture only — never prompts, completions, or key values.",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "OpenRouter API key reference (read-only Bearer; never persisted). Empty = offline catalog only."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "OpenRouter API base URL."},
			{Key: "approved_models", Type: sdk.FieldString, Description: "Optional comma-separated allowlist of approved OpenRouter model ids (e.g. anthropic/claude-sonnet-4). A reachable model outside the list is flagged."},
			{Key: "denied_models", Type: sdk.FieldString, Description: "Optional comma-separated denylist of model ids that must not be reachable. A denied model present in the live catalog is a High posture finding."},
		},
	}
}

// Open reads configuration and builds the read-only Bearer client. It never fails
// for a missing credential: with no api_key the connector runs in offline catalog
// mode (Snapshot returns the declared catalog; Gather emits nothing).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimRight(cfg.Get("base_url"), "/"); v != "" {
		s.baseURL = v
	}
	s.credential = cfg.Get("api_key")
	s.policy = newModelPolicy(cfg.Get("approved_models"), cfg.Get("denied_models"))
	s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthBearer, s.credential, nil)
	return nil
}

// Gather emits the OpenRouter governance posture. It is a batch source (returns
// nil when done). With no credential it returns nil immediately (offline). When
// credentialed it emits the account usage/limit posture, then evaluates the
// model-approval policy against the live catalog.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.credential == "" || s.client == nil {
		return nil // offline mode: nothing to pull
	}
	if err := s.gatherAccount(ctx, sink); err != nil {
		return err
	}
	return s.gatherPolicy(ctx, sink)
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Source) gatherAccount(ctx context.Context, sink sdk.Sink) error {
	var resp keyResponse
	if err := s.client.GetJSON(ctx, keyPath, nil, &resp); err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.accountUnavailableFinding())
		}
		return err
	}
	d := resp.Data
	sev := model.SeverityInfo
	title := "OpenRouter account posture available (key usage read)"
	if d.Limit != nil && *d.Limit > 0 && d.Usage >= *d.Limit {
		sev = model.SeverityLow
		title = "OpenRouter account credit exhausted (usage at or over the configured limit)"
	} else if d.LimitRemaining != nil && *d.LimitRemaining > 0 && d.Limit != nil && *d.Limit > 0 && *d.LimitRemaining <= *d.Limit*0.1 {
		sev = model.SeverityLow
		title = "OpenRouter account credit low (≤10% of the limit remaining)"
	}
	return sink.Emit(ctx, model.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: subjectAccount,
		SubjectRef:  "account",
		Title:       title,
		DetailHash:  redact.Hash(accountDetail(d)),
		OccurredAt:  s.clock().UTC(),
	})
}

func (s *Source) accountUnavailableFinding() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectAccount,
		SubjectRef:  "account",
		Title:       "OpenRouter account posture unavailable (key not entitled / endpoint unavailable)",
		DetailHash:  redact.Hash("openrouter account path=" + keyPath + " base=" + s.baseURL + " returned 401/403/404"),
		OccurredAt:  s.clock().UTC(),
	}
}

func accountDetail(d keyData) string {
	parts := []string{
		"is_free_tier=" + strconv.FormatBool(d.IsFreeTier),
		"usage_usd=" + strconv.FormatFloat(d.Usage, 'f', -1, 64),
		"limit_usd=" + floatPtr(d.Limit),
		"limit_remaining_usd=" + floatPtr(d.LimitRemaining),
	}
	if d.RateLimit != nil {
		parts = append(parts, "rate_limit="+strconv.FormatInt(d.RateLimit.Requests, 10)+"/"+redact.Clean(d.RateLimit.Interval))
	}
	return strings.Join(parts, "|")
}

func floatPtr(p *float64) string {
	if p == nil {
		return "unset"
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

func isUnavailable(err error) bool {
	var apiErr *modelprovider.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == 401 || apiErr.Status == 403 || apiErr.Status == 404
	}
	msg := err.Error()
	return strings.Contains(msg, "status 401") || strings.Contains(msg, "status 403") || strings.Contains(msg, "status 404")
}

// Snapshot returns the OpenRouter catalog. With a credential it reads GET
// /api/v1/models live (models + per-token pricing); with no credential it returns
// a small declared catalog so the connector is useful air-gapped. The Models API
// carries no key/secret material.
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: modelprovider.ProviderOpenRouter, Kind: modelprovider.KindGateway,
			Title: "OpenRouter", BaseURL: s.baseURL,
		},
		CapturedAt: s.clock().UTC(),
	}
	if s.credential == "" || s.client == nil {
		cat.Models = declaredCatalogModels()
		return cat, nil
	}
	models, err := s.fetchModels(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	cat.Models = models
	return cat, nil
}

// fetchModels reads GET /api/v1/models (full list in one data array) and builds
// the live catalog: live model ids, per-token pricing converted to USD/MTok, and
// capabilities derived from supported_parameters. CapabilitySource is "live".
func (s *Source) fetchModels(ctx context.Context) ([]modelprovider.Model, error) {
	var resp modelsResponse
	if err := s.client.GetJSON(ctx, modelsPath, nil, &resp); err != nil {
		return nil, err
	}
	asOf := s.clock().UTC().Format("2006-01-02")
	out := make([]modelprovider.Model, 0, len(resp.Data))
	for _, e := range resp.Data {
		if e.ID == "" {
			continue
		}
		m := modelprovider.Model{
			ProviderRef:      modelprovider.ProviderOpenRouter,
			Ref:              e.ID,
			DisplayName:      firstNonEmpty(e.Name, e.ID),
			CapabilitySource: "live",
			CreatedAt:        unixTime(e.Created),
			ContextWindow:    e.ContextLength,
			Capabilities:     capabilitiesFor(e.SupportedParameters),
		}
		if e.TopProvider != nil {
			m.MaxOutputTokens = e.TopProvider.MaxCompletionTokens
			if m.ContextWindow == 0 {
				m.ContextWindow = e.TopProvider.ContextLength
			}
		}
		if pc, ok := pricingFrom(e.Pricing, asOf); ok {
			m.Pricing = &pc
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

// pricingFrom converts OpenRouter's per-TOKEN USD string pricing to a USD/MTok
// ModelPricing (×1e6). ok is false when neither prompt nor completion price parses
// to a positive value (a free/unknown model keeps nil pricing — never a guess).
func pricingFrom(p *pricing, asOf string) (modelprovider.ModelPricing, bool) {
	if p == nil {
		return modelprovider.ModelPricing{}, false
	}
	in := perMTok(p.Prompt)
	out := perMTok(p.Completion)
	if in <= 0 && out <= 0 {
		return modelprovider.ModelPricing{}, false
	}
	return modelprovider.ModelPricing{
		InputPerMTokUSD:  in,
		OutputPerMTokUSD: out,
		Currency:         "USD",
		AsOf:             asOf,
		Source:           modelprovider.PricingList,
	}, true
}

// perMTok parses a per-token USD price string and returns USD per million tokens.
// A blank/"0"/unparseable value yields 0 (not an error — a missing price is not a
// fatal read). It is deliberately defensive against a hostile/misbehaving upstream:
// strconv.ParseFloat accepts "NaN"/"Inf"/"Infinity" without error and NaN defeats
// the v<=0 guard (every comparison with NaN is false), so those are rejected
// explicitly; a finite-but-huge value whose ×1e6 overflows to +Inf is rejected too.
// A poisoned price must never reach ModelPricing (it would corrupt downstream cost
// math), so any non-finite result falls back to 0 (no price) rather than propagating.
func perMTok(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	out := v * 1e6
	if math.IsInf(out, 0) || math.IsNaN(out) {
		return 0
	}
	return out
}

// capabilitiesFor maps OpenRouter supported_parameters to the coarse cross-vendor
// capability flags. Streaming is always available on OpenRouter.
func capabilitiesFor(params []string) []modelprovider.Capability {
	caps := []modelprovider.Capability{modelprovider.CapStreaming}
	seen := map[string]struct{}{}
	for _, p := range params {
		seen[strings.ToLower(strings.TrimSpace(p))] = struct{}{}
	}
	if _, ok := seen["tools"]; ok {
		caps = append(caps, modelprovider.CapToolUse)
	}
	if _, ok := seen["structured_outputs"]; ok {
		caps = append(caps, modelprovider.CapStructuredOutputs)
	} else if _, ok := seen["response_format"]; ok {
		caps = append(caps, modelprovider.CapStructuredOutputs)
	}
	if _, ok := seen["reasoning"]; ok {
		caps = append(caps, modelprovider.CapExtendedThinking)
	} else if _, ok := seen["include_reasoning"]; ok {
		caps = append(caps, modelprovider.CapExtendedThinking)
	}
	return caps
}

func unixTime(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
