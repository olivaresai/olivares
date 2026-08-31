// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package gemini is the Olivares AI connector for the Google Gemini API
// (generativelanguage.googleapis.com). It exposes a modelprovider.Catalog of the
// Gemini models — display names, context/output limits, the declared capability
// set (streaming, tool/function calling, vision, PDF, structured outputs, prompt
// caching, batch) and family list pricing — to module X, and it emits one
// model.CostSample per model/usage bucket (the "cost.sampled" event that feeds
// module XI / FinOps) from an operator-wired usage export.
//
// Honest scope (a documented v1 limitation, not an omission): the public
// generativelanguage API has NO first-party usage/cost report — unlike the
// Anthropic and OpenAI admin APIs — and exposes NO organization key/workspace
// inventory (API keys and projects are managed in the GCP Cloud Console /
// apikeys.googleapis.com). So Snapshot leaves Keys and Workspaces empty, and
// Gather consumes the operator's own Vertex/Cloud-Billing token-usage export
// (usage_url); with no usage_url configured Gather emits nothing.
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md-3): it performs only GETs, never
// persists or logs the API key, and carries token counts, cost, capabilities and
// inventory METADATA — never prompts, completions, or key values. It imports only
// the SDK and the Apache modelprovider contract, never the engine.
package gemini

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.gemini"

// Default configuration values.
const (
	defaultBaseURL  = "https://generativelanguage.googleapis.com"
	defaultMaxPages = 20
)

// Source is the Gemini source connector. It satisfies sdk.SourceConnector
// (usage/cost as observations) and modelprovider.CatalogProvider (the model
// catalog). A single instance serves both: Gather streams CostSamples from the
// operator usage export; Snapshot returns the model catalog.
type Source struct {
	client   *modelprovider.Client
	apiKey   string
	baseURL  string
	usageURL string
	maxPages int
	doer     modelprovider.Doer // optional injected transport (tests); nil => default
	now      func() time.Time   // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns a Gemini source with default configuration.
func New() *Source {
	return &Source{
		baseURL:  defaultBaseURL,
		maxPages: defaultMaxPages,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Gemini API",
		Description: "Reads the Gemini (Google) model catalog and an operator-wired usage export (read-only).",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "Gemini API key reference (read-only; never persisted). Empty = offline catalog only."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Gemini API base URL."},
			{Key: "usage_url", Type: sdk.FieldString, Description: "Optional path of the operator's Vertex/Cloud-Billing token-usage export (Gemini has no native usage report). Empty = Gather emits nothing."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per snapshot/gather."},
		},
	}
}

// Open reads configuration and builds the read-only Gemini API client. It never
// fails for a missing credential: with no api_key the connector runs in offline
// catalog mode (Snapshot returns the declared catalog; Gather emits nothing).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := cfg.Get("base_url"); v != "" {
		s.baseURL = v
	}
	s.usageURL = cfg.Get("usage_url")
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	s.apiKey = cfg.Get("api_key")

	s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthGoogleKey, s.apiKey, nil)
	return nil
}

// Gather pulls the operator's usage export and emits one CostSample per model/
// usage row, deriving cost from the declared family pricing. Gemini has no
// first-party usage report, so this reads the export the operator wires at
// usage_url; with no usage_url (or no credential) it returns nil immediately. It
// is a batch source: it returns nil when the export is drained.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.usageURL == "" || s.client == nil {
		return nil // no usage export configured: nothing to meter
	}
	var resp usageResponse
	if err := s.client.GetJSON(ctx, s.usageURL, url.Values{}, &resp); err != nil {
		return err
	}
	for _, bucket := range resp.Data {
		occurred := parseTime(bucket.OccurredAt)
		for _, r := range bucket.Results {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if r.Model == "" {
				continue // an aggregate row with no model dimension; skip
			}
			if err := sink.Emit(ctx, s.costSample(r, occurred)); err != nil {
				return err
			}
		}
	}
	return nil
}

// costSample turns one usage result row into a derived CostSample.
func (s *Source) costSample(r usageResult, occurred time.Time) model.CostSample {
	u := modelprovider.Usage{
		ProviderRef:     modelprovider.ProviderGoogle,
		ModelRef:        r.Model,
		InputTokens:     r.InputTokens,
		OutputTokens:    r.OutputTokens,
		CacheReadTokens: r.CachedInputTokens,
		OccurredAt:      occurred,
	}
	p, _, _, ok := pricingFor(r.Model)
	if !ok {
		// Unknown price: record usage with an underived (0) cost rather than guess.
		return modelprovider.ToCostSampleWithCost(u, 0)
	}
	return modelprovider.ToCostSample(u, p)
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// Snapshot returns the Gemini catalog. With a credential it lists models live
// (read-only) and enriches each with declared family pricing and the declared
// capability set; with no credential it returns the declared offline catalog
// (models only). Keys and Workspaces are always empty: the generativelanguage API
// exposes no organization key/workspace inventory (a documented v1 limitation —
// keys/projects live in the GCP Cloud Console / apikeys.googleapis.com).
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: modelprovider.ProviderGoogle, Kind: modelprovider.KindHostedAPI,
			Title: "Google (Gemini)", BaseURL: s.baseURL,
		},
		CapturedAt: s.clock().UTC(),
	}
	if s.apiKey == "" || s.client == nil {
		cat.Models = declaredCatalogModels()
		return cat, nil
	}

	models, err := s.fetchModels(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	cat.Models = models
	// Keys/Workspaces intentionally left nil: the generativelanguage API has no
	// org key/workspace inventory (managed in GCP Cloud Console). See package doc.
	return cat, nil
}

// declaredCatalogModels builds the offline model list from the declared ids.
func declaredCatalogModels() []modelprovider.Model {
	out := make([]modelprovider.Model, 0, len(declaredModelIDs))
	for _, d := range declaredModelIDs {
		out = append(out, buildModel(d.id, d.displayName, 0, 0, nil))
	}
	return out
}

// fetchModels lists /v1beta/models and enriches each with declared pricing +
// capabilities, following nextPageToken pagination up to the safety bound.
func (s *Source) fetchModels(ctx context.Context) ([]modelprovider.Model, error) {
	var out []modelprovider.Model
	token := ""
	for i := 0; i < s.maxPages; i++ {
		var resp modelsResponse
		q := url.Values{"pageSize": {"100"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		if err := s.client.GetJSON(ctx, "/v1beta/models", q, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Models {
			ref := trimModelPrefix(m.Name)
			out = append(out, buildModel(ref, m.DisplayName, m.InputTokenLimit, m.OutputTokenLimit, m.SupportedGenerationMethods))
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	return out, nil
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// parseTime parses an RFC3339 timestamp, returning the zero time on any error so a
// missing/odd export timestamp never aborts a gather.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
