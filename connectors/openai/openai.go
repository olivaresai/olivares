// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package openai is the Olivares AI connector for the OpenAI platform (and Azure
// OpenAI). It reads Chat Completions and Responses-era token usage through the org
// Usage/Costs APIs and emits one model.CostSample per project/model/usage bucket
// (the "cost.sampled" event that feeds module XI / FinOps, attributed to the
// Olivares workspace by OpenAI project), and it exposes a modelprovider.Catalog of
// the OpenAI stack — models, declared capabilities (streaming, tool/function
// calling, vision, structured outputs, prompt caching, batch, files), declared
// per-family list pricing, and API-key/project inventory metadata — to module X.
//
// The Assistants API surface this connector inventories is deprecated upstream
// (sunset 2026-08-26). Until removal, that inventory doubles as a deprecation-risk
// detector; after removal, the connector degrades to an informational posture
// finding. Conversations and responses expose no org-wide list endpoints, so
// governance of that surface is usage/cost/retention-based by design, with no
// content collection.
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md-3): it performs only GETs, never
// persists or logs the admin/org credential, and carries token counts, cost,
// capabilities and inventory METADATA — never prompts, completions, or key values
// (the admin-keys API returns only a masked redacted value, never the secret). It
// imports only the SDK and the Apache modelprovider contract, never the engine.
package openai

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.openai"

// Default configuration values.
const (
	defaultBaseURL     = "https://api.openai.com"
	defaultProvider    = "openai"
	defaultLookback    = 24 * time.Hour
	defaultBucketWidth = "1d"
	defaultMaxPages    = 20
)

// Source is the OpenAI source connector. It satisfies sdk.SourceConnector
// (usage/cost as observations) and modelprovider.CatalogProvider (the model/key
// catalog). A single instance serves both: Gather streams CostSamples; Snapshot
// returns the catalog.
type Source struct {
	client           *modelprovider.Client
	assistantsClient *modelprovider.Client // second client with OpenAI-Beta: assistants=v2
	apiKey           string
	baseURL          string
	providerRef      string
	lookback         time.Duration
	bucketWidth      string
	maxPages         int
	costs            bool               // opt-in billed-cost ingestion (default false)
	assistants       bool               // opt-in assistants/files/vector-stores inventory
	admin            bool               // opt-in extended admin depth (invites, project users/keys)
	asstPolicy       *assistantsPolicy  // operator-declared assistants policy (nil = no enforcement)
	doer             modelprovider.Doer // optional injected transport (tests); nil => default
	now              func() time.Time   // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns an OpenAI source with default configuration.
func New() *Source {
	return &Source{
		baseURL:     defaultBaseURL,
		providerRef: modelprovider.ProviderOpenAI,
		lookback:    defaultLookback,
		bucketWidth: defaultBucketWidth,
		maxPages:    defaultMaxPages,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "OpenAI",
		Description: "Reads OpenAI (and Azure OpenAI) usage/cost and the model/key catalog via the org API (read-only).",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "OpenAI admin/org API key reference (read-only; never persisted). Empty = offline catalog only."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "OpenAI API base URL (the Azure endpoint when provider=azure-openai)."},
			{Key: "provider", Type: sdk.FieldString, Default: defaultProvider, Description: "Provider mode: openai or azure-openai."},
			{Key: "lookback", Type: sdk.FieldDuration, Default: "24h", Description: "How far back to pull usage on each Gather."},
			{Key: "bucket_width", Type: sdk.FieldString, Default: defaultBucketWidth, Description: "Usage bucket granularity: 1d, 1h or 1m."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per gather."},
			{Key: "costs", Type: sdk.FieldBool, Default: "false", Description: "Enable billed-cost ingestion (org-wide unless scoped by project). Off by default to avoid silently counting all org spend."},
			{Key: "assistants", Type: sdk.FieldBool, Default: "false", Description: "Enable Assistants API inventory (deprecated upstream, sunset 2026-08-26; emits deprecation-risk findings until removal), plus files/vector stores inventory (these survive the sunset) and policy enforcement."},
			{Key: "admin", Type: sdk.FieldBool, Default: "false", Description: "Enable extended admin depth (invites, project users, service accounts, project API keys)."},
			{Key: "assistants_allowed_models", Type: sdk.FieldString, Default: "", Description: "Comma-separated model prefixes allowed for assistants (empty = all). Policy violations are emitted as findings."},
			{Key: "assistants_allowed_tools", Type: sdk.FieldString, Default: "", Description: "Comma-separated tool types allowed for assistants (empty = all). e.g. code_interpreter,file_search."},
		},
	}
}

// Open reads configuration and builds the read-only API client. It never fails for
// a missing credential: with no api_key the connector runs in offline catalog mode
// (Snapshot returns the declared catalog; Gather emits nothing). When provider is
// "azure-openai" the catalog provider ref switches to ProviderAzureOpenAI and the
// configured base_url is treated as the Azure endpoint (no other behavior change).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := cfg.Get("base_url"); v != "" {
		s.baseURL = v
	}
	if v := cfg.Get("provider"); v != "" {
		if v == modelprovider.ProviderAzureOpenAI {
			s.providerRef = modelprovider.ProviderAzureOpenAI
		} else {
			s.providerRef = modelprovider.ProviderOpenAI
		}
	}
	s.lookback = cfg.GetDuration("lookback", s.lookback)
	if v := cfg.Get("bucket_width"); v != "" {
		s.bucketWidth = v
	}
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		// A zero/negative bound would make every pull loop run zero times — which for
		// the moderation posture would emit a FALSE "no usage observed" finding without
		// a single request (fabricated posture). Floor it, mirroring the aws/azure
		// connectors' clamps, so a misconfiguration never silently degrades a read.
		s.maxPages = defaultMaxPages
	}
	s.costs = cfg.GetBool("costs", false)
	s.assistants = cfg.GetBool("assistants", false)
	s.admin = cfg.GetBool("admin", false)
	s.apiKey = cfg.Get("api_key")

	s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthBearer, s.apiKey, nil)
	s.assistantsClient = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthBearer, s.apiKey,
		map[string]string{"OpenAI-Beta": "assistants=v2"})

	s.asstPolicy = parseAssistantsPolicy(cfg)
	return nil
}

// usageLimitFor returns the maximum number of buckets the Usage API accepts per page
// for a bucket width. The Usage API caps the page limit by width (1d→31, 1h→168,
// 1m→1440); sending a larger limit (e.g. the list-endpoint-style 100 against a 1d
// width) is rejected with HTTP 400. Pagination (next_page) still drains the window, so
// clamping to the per-width max never under-reads.
func usageLimitFor(bucketWidth string) int {
	switch bucketWidth {
	case "1h":
		return 168
	case "1m":
		return 1440
	default: // "1d" and any unexpected value: the conservative daily cap
		return 31
	}
}

// Gather pulls the usage report for the lookback window and emits one CostSample
// per project/model/usage bucket, deriving cost from the declared family pricing, then
// emits the read-only safety-posture finding (OpenAI Moderation). It is a batch
// source: it returns nil when the window is drained (the runtime decides when to
// call it again). With no credential it returns nil immediately.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.apiKey == "" {
		return nil // offline mode: nothing to pull
	}
	if err := s.gatherUsage(ctx, sink); err != nil {
		return err
	}
	if err := s.gatherSafetyPosture(ctx, sink); err != nil {
		return err
	}
	if err := s.gatherOrgGraph(ctx, sink); err != nil {
		return err
	}
	if err := s.gatherAuditLogs(ctx, sink); err != nil {
		return err
	}
	projects, projectErr := s.fetchNonArchivedProjects(ctx)
	if err := s.gatherDataRetentionForProjects(ctx, sink, projects, projectErr); err != nil {
		return err
	}
	if s.costs {
		if err := s.gatherSpendAlertsForProjects(ctx, sink, projects, projectErr); err != nil {
			return err
		}
		if err := s.gatherCosts(ctx, sink); err != nil {
			return err
		}
	}
	if s.assistants {
		if err := s.gatherAssistants(ctx, sink); err != nil {
			return err
		}
		if err := s.gatherFiles(ctx, sink); err != nil {
			return err
		}
		if err := s.gatherVectorStores(ctx, sink); err != nil {
			return err
		}
	}
	if s.admin {
		if err := s.gatherInvites(ctx, sink); err != nil {
			return err
		}
		if err := s.gatherModelPermissionsForProjects(ctx, sink, projects, projectErr); err != nil {
			return err
		}
		if err := s.gatherHostedToolPermissionsForProjects(ctx, sink, projects, projectErr); err != nil {
			return err
		}
		if err := s.gatherAgentKitPosture(ctx, sink); err != nil {
			return err
		}
		if err := s.gatherGroups(ctx, sink); err != nil {
			return err
		}
		if err := s.gatherRoles(ctx, sink); err != nil {
			return err
		}
		if err := s.gatherProjectAdmin(ctx, sink); err != nil {
			return err
		}
	}
	return nil
}

// gatherUsage emits one CostSample per project/model/usage bucket for the lookback
// window (grouping by project_id makes each sample attributable to an Olivares
// workspace — the Responses-era FinOps correlation).
func (s *Source) gatherUsage(ctx context.Context, sink sdk.Sink) error {
	start := s.clock().Add(-s.lookback).UTC()
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var resp usageResponse
		q := url.Values{}
		q.Set("start_time", strconv.FormatInt(start.Unix(), 10))
		q.Set("bucket_width", s.bucketWidth)
		q.Add("group_by[]", "model")
		q.Add("group_by[]", "project_id")
		q.Add("group_by[]", "service_tier")
		q.Set("limit", strconv.Itoa(usageLimitFor(s.bucketWidth)))
		if page != "" {
			q.Set("page", page)
		}
		if err := s.client.GetJSON(ctx, "/v1/organization/usage/completions", q, &resp); err != nil {
			return err
		}
		for _, bucket := range resp.Data {
			occurred := time.Unix(bucket.StartTime, 0).UTC()
			for _, r := range bucket.Results {
				if r.Model == "" {
					continue // an aggregate row with no model dimension; skip
				}
				if err := sink.Emit(ctx, s.costSample(r, occurred)); err != nil {
					return err
				}
			}
		}
		if !resp.HasMore || resp.NextPage == "" {
			return nil
		}
		page = resp.NextPage
	}
	return nil
}

// costSample turns one usage result row into a derived CostSample. OpenAI reports
// input_tokens INCLUDING cached input, so the uncached input is the difference and
// the cached portion is carried as the cache-read tier.
func (s *Source) costSample(r usageResult, occurred time.Time) model.CostSample {
	uncached := r.InputTokens - r.InputCachedTokens
	if uncached < 0 {
		uncached = 0
	}
	u := modelprovider.Usage{
		ProviderRef:     s.providerRef,
		ModelRef:        r.Model,
		WorkspaceRef:    r.ProjectID,
		InputTokens:     uncached,
		OutputTokens:    r.OutputTokens,
		CacheReadTokens: r.InputCachedTokens,
		OccurredAt:      occurred,
		ServiceTier:     r.ServiceTier,
	}
	p, _, _, _, ok := pricingFor(r.Model)
	if !ok {
		// Unknown price: record usage with an underived (0) cost rather than guess.
		return modelprovider.ToCostSampleWithCost(u, 0)
	}
	return modelprovider.ToCostSample(u, p)
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// parseAssistantsPolicy reads the operator-declared assistants policy from config.
// Returns nil if no policy fields are set (inventory-only, no enforcement).
func parseAssistantsPolicy(cfg sdk.Config) *assistantsPolicy {
	models := splitCSV(cfg.Get("assistants_allowed_models"))
	tools := splitCSV(cfg.Get("assistants_allowed_tools"))
	if len(models) == 0 && len(tools) == 0 {
		return nil
	}
	return &assistantsPolicy{
		AllowedModels: models,
		AllowedTools:  tools,
	}
}

// splitCSV splits a comma-separated string into trimmed, non-empty parts.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Snapshot returns the OpenAI catalog. With a credential it lists models, admin API
// keys and projects live (read-only) and enriches each model with declared pricing
// and capabilities; with no credential it returns the declared offline catalog
// (models only). It never returns key values — only the masked redacted value.
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: s.providerRef, Kind: modelprovider.KindHostedAPI,
			Title: s.providerTitle(), BaseURL: s.baseURL,
		},
		CapturedAt: s.clock().UTC(),
	}
	if s.apiKey == "" || s.client == nil {
		cat.Models = declaredCatalogModels(s.providerRef)
		return cat, nil
	}

	models, err := s.fetchModels(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	cat.Models = models

	keys, err := s.fetchKeys(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	cat.Keys = keys

	ws, err := s.fetchProjects(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	cat.Workspaces = ws
	return cat, nil
}

// providerTitle is the human display label for the configured provider mode.
func (s *Source) providerTitle() string {
	if s.providerRef == modelprovider.ProviderAzureOpenAI {
		return "Azure OpenAI"
	}
	return "OpenAI"
}

// declaredCatalogModels builds the offline model list from the declared ids.
func declaredCatalogModels(providerRef string) []modelprovider.Model {
	out := make([]modelprovider.Model, 0, len(declaredModelIDs))
	for _, d := range declaredModelIDs {
		out = append(out, buildModel(providerRef, d.id, d.displayName))
	}
	return out
}

// fetchModels lists /v1/models (no pagination) and enriches each with declared
// pricing + capabilities.
func (s *Source) fetchModels(ctx context.Context) ([]modelprovider.Model, error) {
	var resp modelsResponse
	if err := s.client.GetJSON(ctx, "/v1/models", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]modelprovider.Model, 0, len(resp.Data))
	for _, m := range resp.Data {
		mdl := buildModel(s.providerRef, m.ID, m.ID)
		mdl.CreatedAt = unixTime(m.Created)
		out = append(out, mdl)
	}
	return out, nil
}

// fetchKeys lists /v1/organization/admin_api_keys as inventory metadata (no
// secrets — only the masked redacted value). Cursor pagination via after_id/last_id.
func (s *Source) fetchKeys(ctx context.Context) ([]modelprovider.KeyRef, error) {
	var out []modelprovider.KeyRef
	after := ""
	for i := 0; i < s.maxPages; i++ {
		var resp adminKeysResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, "/v1/organization/admin_api_keys", q, &resp); err != nil {
			return nil, err
		}
		for _, k := range resp.Data {
			out = append(out, modelprovider.KeyRef{
				ID: k.ID, Name: k.Name,
				Status: k.Status, Hint: k.RedactedValue, CreatedAt: unixTime(k.CreatedAt),
			})
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

// fetchProjects lists /v1/organization/projects as workspace inventory metadata.
// Cursor pagination via after_id/last_id.
func (s *Source) fetchProjects(ctx context.Context) ([]modelprovider.WorkspaceRef, error) {
	var out []modelprovider.WorkspaceRef
	after := ""
	for i := 0; i < s.maxPages; i++ {
		var resp projectsResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, "/v1/organization/projects", q, &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Data {
			out = append(out, modelprovider.WorkspaceRef{
				ID: p.ID, Name: p.Name,
				Archived: p.Status == "archived", CreatedAt: unixTime(p.CreatedAt),
			})
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
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

// unixTime converts a Unix-seconds timestamp to UTC, returning the zero time for a
// zero/absent value so a missing provider timestamp never aborts a snapshot.
func unixTime(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}
