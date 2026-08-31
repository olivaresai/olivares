// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package claudeapi is the Olivares AI connector for the Claude (Anthropic) API
// and Console. It reads the organization's usage and cost through the Admin API
// and emits one model.CostSample per model/usage bucket (the "cost.sampled"
// event that feeds module XI / FinOps), and it exposes a modelprovider.Catalog of
// the Claude stack — models, the full capability set (prompt caching, batch,
// files, extended thinking, computer use, memory tool, context management,
// vision/PDF, structured outputs, citations), and API-key/workspace inventory
// metadata — to module X.
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md-3): it performs only GETs, never
// persists or logs the admin credential, and carries token counts, cost,
// capabilities and inventory METADATA — never prompts, completions, or key values
// (the Admin API returns only a masked key hint, never the secret). It imports
// only the SDK and the Apache modelprovider contract, never the engine.
package claudeapi

import (
	"context"
	"math"
	"net/url"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.claude-api"

// Default configuration values.
const (
	defaultBaseURL          = "https://api.anthropic.com"
	defaultAnthropicVersion = "2023-06-01"
	defaultLookback         = 24 * time.Hour
	defaultCostLookback     = 48 * time.Hour // cost_report is daily; a 2-day window absorbs late settlement
	defaultBucketWidth      = "1d"
	defaultMaxPages         = 20

	usageReportPath      = "/v1/organizations/usage_report/messages"
	costReportPath       = "/v1/organizations/cost_report"
	claudeCodePath       = "/v1/organizations/usage_report/claude_code"
	claudeCodeDateLayout = "2006-01-02"

	// fastModeBeta is the anthropic-beta header value gating the fast-mode speed
	// dimension on the Messages Usage Report (verified 2026-07-04; also in
	// knownBetaHeaders). The report documents group_by[]=speed, but the result schema
	// still does NOT echo a speed field, so the robust attribution path is FILTERING
	// the pull per speed (speeds[]) and tagging each sample with the requested band.
	// speedStandard/speedFast are the two accepted bands.
	betaHeaderName = "anthropic-beta"
	fastModeBeta   = "fast-mode-2026-02-01"
	speedStandard  = "standard"
	speedFast      = "fast"
)

// usageGroupBy is the full attribution dimension set the Messages Usage Report
// supports without a beta header (speed is beta-gated and omitted by default). Each
// dimension makes the per-model cost attributable for chargeback/SLA/residency.
var usageGroupBy = []string{
	"model", "workspace_id", "api_key_id",
	"service_tier", "context_window", "inference_geo",
	"account_id", "service_account_id",
}

// Source is the Claude API source connector. It satisfies sdk.SourceConnector
// (usage/cost as observations) and modelprovider.CatalogProvider (the model/key
// catalog). A single instance serves both: Gather streams CostSamples; Snapshot
// returns the catalog.
type Source struct {
	client       *modelprovider.Client
	adminKey     string
	baseURL      string
	version      string
	lookback     time.Duration
	bucketWidth  string
	workspaceID  string
	maxPages     int
	costReport   bool // pull billed cost_report in addition to derived usage
	costLookback time.Duration
	claudeCode   bool               // pull the free Claude Code Analytics per-developer cost feed
	shadowAuth   bool               // flag customer_type=api developers (shadow auth) on the feed
	fastMode     bool               // split the usage pull by speed (fast-mode attribution, beta-gated)
	analyticsKey string             // DISTINCT read:analytics credential for Enterprise Analytics (family #3)
	doer         modelprovider.Doer // optional injected transport (tests); nil => default
	now          func() time.Time   // injectable clock (tests); nil => time.Now
	toolsets     []ToolsetGrant     // CLA-09: operator-declared mcp_toolset allow-sets (PERMITTED MCP edges)
	serverTools  []ServerToolGrant  // D7: operator-declared server-tool-TYPE allow-sets (PERMITTED server-tool edges)
	gateway      model.Gateway      // ANT2-01: the deployment surface this connector reads (default direct)
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns a Claude API source with default configuration.
func New() *Source {
	return &Source{
		baseURL:      defaultBaseURL,
		version:      defaultAnthropicVersion,
		lookback:     defaultLookback,
		bucketWidth:  defaultBucketWidth,
		maxPages:     defaultMaxPages,
		costReport:   true,
		costLookback: defaultCostLookback,
		shadowAuth:   true,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude API",
		Description: "Reads Claude (Anthropic) usage/cost and the model/key catalog via the Admin API (read-only).",
		ConfigFields: []sdk.ConfigField{
			{Key: "admin_key", Type: sdk.FieldString, Secret: true, Description: "Anthropic Admin API key reference (read-only; never persisted). Empty = offline catalog only."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Anthropic API base URL."},
			{Key: "anthropic_version", Type: sdk.FieldString, Default: defaultAnthropicVersion, Description: "anthropic-version header value."},
			{Key: "lookback", Type: sdk.FieldDuration, Default: "24h", Description: "How far back to pull usage on each Gather."},
			{Key: "bucket_width", Type: sdk.FieldString, Default: defaultBucketWidth, Description: "Usage bucket granularity: 1d, 1h or 1m."},
			{Key: "workspace_id", Type: sdk.FieldString, Description: "Optional workspace filter for usage and inventory."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per gather."},
			{Key: "cost_report", Type: sdk.FieldBool, Default: "true", Description: "Also pull the billed cost_report (authoritative cost, daily) in addition to the derived usage estimate."},
			{Key: "cost_lookback", Type: sdk.FieldDuration, Default: "48h", Description: "How far back to pull billed cost on each Gather (cost_report is daily)."},
			{Key: "claude_code", Type: sdk.FieldBool, Default: "false", Description: "Also pull the free Claude Code Analytics feed (per-developer estimated cost by model) for chargeback."},
			{Key: "claude_code_shadow_auth", Type: sdk.FieldBool, Default: "true", Description: "with the claude_code feed on, flag each developer whose Claude Code usage bills as customer_type=api (a personal/API key OUTSIDE the org subscription) as a Medium governance finding — identity/cost drift: their seat attribution and spend governance ride an ungoverned key. Set false for orgs that intentionally run Claude Code on API billing. BOUNDARY (verified 2026-06-10): the Analytics feed only tracks usage on the Claude API — Claude Platform on AWS, Microsoft Foundry, Amazon Bedrock and Vertex AI usage is NOT included, so absence of findings is not evidence of absence for 3P-provider fleets (the OTel plane covers those)."},
			{Key: "fast_mode", Type: sdk.FieldBool, Default: "false", Description: "Split the Messages Usage Report by speed (standard|fast) for fast-mode cost attribution. Beta-gated (anthropic-beta: fast-mode-2026-02-01) and opt-in: the report does not echo a speed field, so the connector issues one filtered pull per speed (doubling usage calls) and tags each CostSample. Off = single untagged pull (no speed dimension)."},
			{Key: "analytics_key", Type: sdk.FieldString, Secret: true, Description: "Enterprise Analytics credential reference (DISTINCT from admin_key): an x-api-key carrying the read:analytics scope (Enterprise plan). Empty = Enterprise Analytics ingest off (deny-closed, honest absence). Used ONLY for the org-level engagement roll-up (DAU/WAU/MAU + seats across Chat/Code/Cowork); never for cost (that would double-count the Usage & Cost API). Anthropic-operated surfaces only."},
			{Key: "gateway", Type: sdk.FieldString, Default: string(model.GatewayDirect), Description: "ANT2-01 deployment surface this connector reads: direct|claude-platform-aws|bedrock-mantle|bedrock-legacy|vertex|foundry. It gates which Anthropic APIs apply: on a surface without the Admin API (Bedrock/Vertex/Foundry) the governance/catalog ingest degrades honestly (a posture finding), never a fabricated empty inventory."},
			{Key: "mcp_toolsets", Type: sdk.FieldString, Default: "", Description: "CLA-09/AIP-08: JSON array of operator-declared Messages-API mcp_toolset allow-sets governing which MCP tools an API-driven Claude agent MAY invoke, e.g. [{\"agent_ref\":\"agent:billing\",\"server_name\":\"github\",\"default_enabled\":false,\"allowed_tools\":[\"list_issues\"],\"denied_tools\":[\"delete_repo\"]}]. agent_ref MUST be the agent's EXTERNAL ID as discovered by the runtime/governance source (the same id the observed session resolves to) — otherwise module III cannot reconcile the PERMITTED edge against the observed access and the grant is an honest no-op, never a false grant. default_enabled=true is rejected at Open (its closure cannot be enumerated API-side; declare explicit allowed_tools). Each allow-listed tool is emitted as a PERMITTED (policy) access edge to module III for the R/RW diff vs the observed/introspected MCP tools. Empty = no API-side MCP governance declared. Models the mcp-client-2025-11-20 surface (tools-only, non-ZDR; unavailable on Bedrock/Vertex)."},
			{Key: "server_tool_grants", Type: sdk.FieldString, Default: "", Description: "D7: JSON array of operator-declared allow-sets governing which Anthropic SERVER-tool TYPES an API-driven Claude agent MAY use, e.g. [{\"agent_ref\":\"agent:research\",\"allowed_types\":[\"web_search_20260209\",\"web_fetch_20260209\"],\"denied_types\":[\"code_execution_20260120\"]}]. agent_ref MUST be the agent's EXTERNAL id (same reconciliation rule as mcp_toolsets). Each allowed dated TYPE becomes a PERMITTED (policy) access edge (kind anthropic.server_tool) module III crosses against observed tool use; a denied type grants nothing; an UNRECOGNIZED allowed type emits a posture finding (verify the dated identifier) rather than a silent pass. Empty = no API-side server-tool governance declared."},
		},
	}
}

// Open reads configuration and builds the read-only Admin API client. It never
// fails for a missing credential: with no admin_key the connector runs in offline
// catalog mode (Snapshot returns the declared catalog; Gather emits nothing).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := cfg.Get("base_url"); v != "" {
		s.baseURL = v
	}
	if v := cfg.Get("anthropic_version"); v != "" {
		s.version = v
	}
	s.lookback = cfg.GetDuration("lookback", s.lookback)
	if v := cfg.Get("bucket_width"); v != "" {
		s.bucketWidth = v
	}
	s.workspaceID = cfg.Get("workspace_id")
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	s.costReport = cfg.GetBool("cost_report", s.costReport)
	s.costLookback = cfg.GetDuration("cost_lookback", s.costLookback)
	s.claudeCode = cfg.GetBool("claude_code", s.claudeCode)
	s.shadowAuth = cfg.GetBool("claude_code_shadow_auth", s.shadowAuth)
	s.fastMode = cfg.GetBool("fast_mode", s.fastMode)
	s.analyticsKey = cfg.Get("analytics_key")
	s.adminKey = cfg.Get("admin_key")
	if v := cfg.Get("gateway"); v != "" {
		s.gateway = model.Gateway(v)
	} else {
		s.gateway = model.GatewayDirect
	}

	// CLA-09: parse the operator-declared mcp_toolset allow-sets. A malformed policy
	// fails Open (never silently ungoverned), even though it would also stop cost
	// ingestion — governance integrity wins.
	toolsets, err := parseToolsets(cfg.Get("mcp_toolsets"))
	if err != nil {
		return err
	}
	s.toolsets = toolsets

	// D7: parse the operator-declared server-tool-type allow-sets. Same posture as
	// mcp_toolsets — a malformed policy fails Open (never silently ungoverned).
	serverTools, err := parseServerToolGrants(cfg.Get("server_tool_grants"))
	if err != nil {
		return err
	}
	s.serverTools = serverTools

	s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, s.adminKey,
		map[string]string{"anthropic-version": s.version})
	return nil
}

// Gather pulls Claude cost for the lookback window in TWO streams and emits a
// CostSample for each row: (1) the derived USAGE estimate — one sample per
// model+attribution bucket, cost derived from declared list pricing
// (provenance=estimated); and (2) when enabled, the BILLED cost_report — the
// authoritative figure to reconcile against the Anthropic invoice
// (provenance=billed). Both are tagged so module XI can prefer billed truth and
// surface drift, and keep the estimate as the fallback for Priority Tier (which the
// cost endpoint never bills). It is a batch source: it returns nil when the window
// is drained. With no admin credential it returns nil immediately.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	// CLA-09: emit the PERMITTED mcp_toolset edges first — operator-declared policy,
	// independent of the Admin credential, so API-side MCP governance flows even when
	// the connector runs in offline (no admin_key) mode.
	if err := s.gatherToolsetEdges(ctx, sink); err != nil {
		return err
	}
	// D7: emit the PERMITTED server-tool-TYPE edges (+ unrecognized-type posture
	// findings). Operator-declared policy, credential-independent — so API-side
	// server-tool governance flows even in offline (no admin_key) mode, like CLA-09.
	if err := s.gatherServerToolEdges(ctx, sink); err != nil {
		return err
	}
	// Enterprise Analytics (family #3) authenticates with its OWN read:analytics
	// credential, so it runs independently of the Admin key — deny-closed (no
	// analytics_key ⇒ skipped). It carries engagement evidence, never cost.
	if err := s.gatherEnterpriseAnalytics(ctx, sink); err != nil {
		return err
	}
	// ANT2-01 per-surface capability divergence: warn that the configured surface
	// caps a current model's context window below its standard window (e.g. Foundry caps
	// Opus 4.8 at 200K vs 1M). It is DECLARED knowledge, CREDENTIAL-INDEPENDENT, so it is
	// emitted BEFORE the offline short-circuit below — like the CLA-09/D7 edges above. A
	// Microsoft Foundry estate exposes NO Admin API, so its natural config carries no
	// admin_key; gating this behind the Admin key would silence it for the exact estate it
	// exists to warn. Caps nothing on a non-capping surface ⇒ no finding.
	if f, ok := s.surfaceCapabilityDivergenceFinding(s.clock().UTC()); ok {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	if s.adminKey == "" {
		return nil // offline mode: only operator-declared policy + analytics, nothing else pulled
	}
	// ANT2-03 param-deprecation pre-advice: warn that Opus 4.7+ rejects non-default
	// temperature/top_p/top_k BEFORE the 400. It is declared knowledge, but emitted only
	// on an active (credentialed) run so an unconfigured connector stays silent.
	if f, ok := samplingDeprecationFinding(s.clock().UTC()); ok {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	// ANT2-04/05/06: read-only governance posture (External Keys/CMEK, Rate Limits,
	// Workspace residency). Emitted first because, on a surface without the Admin API,
	// it is the ONE honest "ingest degraded" signal and the usage/cost pulls below
	// would 404 — gatherGovernance short-circuits with that finding for those surfaces.
	if !s.surface().Supports("admin") {
		return s.gatherGovernance(ctx, sink)
	}
	if err := s.gatherGovernance(ctx, sink); err != nil {
		return err
	}
	if err := s.gatherKeyLifecycle(ctx, sink); err != nil {
		return err
	}
	if f, ok := s.betaHeaderInventoryFinding(s.clock().UTC()); ok {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	// Lifecycle: the recorder wraps the sink and collects the distinct model ids
	// the usage/cost/Claude-Code pulls observe IN USE, so deprecated/retired models
	// surface as deprecated_model_in_use findings AFTER the pulls (one per model,
	// sorted — deterministic).
	rec := newModelRecorder(sink)
	if err := s.gatherUsage(ctx, rec); err != nil {
		return err
	}
	if s.costReport {
		if err := s.gatherCost(ctx, rec); err != nil {
			return err
		}
	}
	if s.claudeCode {
		if err := s.gatherClaudeCode(ctx, rec); err != nil {
			return err
		}
	}
	return s.emitLifecycleFindings(ctx, sink, rec.models())
}

// gatherUsage pulls the Messages Usage Report across the full attribution group_by
// set and emits one derived (estimated) CostSample per result row. With fast-mode
// attribution off (default) it is a single untagged pull; with it on, the pull is
// split per speed (the report does not echo speed, so each filtered pull's samples
// are tagged with the speed it requested).
func (s *Source) gatherUsage(ctx context.Context, sink sdk.Sink) error {
	for _, speed := range s.usageSpeeds() {
		if err := s.gatherUsageSpeed(ctx, sink, speed); err != nil {
			return err
		}
	}
	return nil
}

// usageSpeeds is the set of speed bands to split the usage pull across: a single
// untagged pull ([""]) by default, or one filtered pull per speed when fast-mode
// attribution is enabled. {standard, fast} partition all rows with no double-count
// (a model that does not support fast returns only under standard).
func (s *Source) usageSpeeds() []string {
	if s.fastMode {
		return []string{speedStandard, speedFast}
	}
	return []string{""}
}

// gatherUsageSpeed pulls one speed slice of the usage report. speed=="" is the
// untagged default pull on the base client; a non-empty speed uses a beta-gated
// client (anthropic-beta: fast-mode-2026-02-01), adds the speeds[] filter, and tags
// each emitted sample's Speed — the report carries no speed field to read it back.
func (s *Source) gatherUsageSpeed(ctx context.Context, sink sdk.Sink, speed string) error {
	client := s.client
	if speed != "" {
		client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, s.adminKey,
			map[string]string{"anthropic-version": s.version, betaHeaderName: fastModeBeta})
	}
	start := s.clock().Add(-s.lookback).UTC()
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var resp usageResponse
		q := url.Values{}
		q.Set("starting_at", start.Format(time.RFC3339))
		q.Set("bucket_width", s.bucketWidth)
		for _, g := range usageGroupBy {
			q.Add("group_by[]", g)
		}
		if speed != "" {
			q.Add("speeds[]", speed)
		}
		if s.workspaceID != "" {
			q.Set("workspace_id", s.workspaceID)
		}
		if page != "" {
			q.Set("page", page)
		}
		if err := client.GetJSON(ctx, usageReportPath, q, &resp); err != nil {
			return err
		}
		for _, bucket := range resp.Data {
			occurred := parseTime(bucket.StartingAt)
			for _, r := range bucket.Results {
				if r.Model == "" {
					continue // an aggregate row with no model dimension; skip
				}
				cs := s.costSample(r, occurred)
				cs.Speed = speed // empty for the default pull; the requested band otherwise
				if err := sink.Emit(ctx, cs); err != nil {
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

// gatherCost pulls the billed cost_report (daily granularity, grouped by workspace +
// description so model/service_tier/cost_type/context_window/inference_geo are
// populated) and emits one authoritative (billed) CostSample per result row.
func (s *Source) gatherCost(ctx context.Context, sink sdk.Sink) error {
	start := s.clock().Add(-s.costLookback).UTC()
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var resp costReportResponse
		q := url.Values{}
		q.Set("starting_at", start.Format(time.RFC3339))
		q.Set("bucket_width", "1d") // cost_report supports daily granularity only
		q.Add("group_by[]", "workspace_id")
		q.Add("group_by[]", "description")
		if page != "" {
			q.Set("page", page)
		}
		if err := s.client.GetJSON(ctx, costReportPath, q, &resp); err != nil {
			return err
		}
		for _, bucket := range resp.Data {
			occurred := parseTime(bucket.StartingAt)
			for _, r := range bucket.Results {
				if err := sink.Emit(ctx, billedSample(r, occurred, s.surface().Gateway)); err != nil {
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

// gatherClaudeCode pulls the free Claude Code Analytics feed for the current UTC day
// and emits, per (actor, model), one CostSample — the per-developer estimated cost for
// chargeback (subscription rows only). The feed is daily and keyed by a single
// starting_at date; the productivity metrics (an internal design note (not shipped) accept-
// reject/per-model tokens) are mapped separately as adoption MetricSamples (claudeCodeMetricSamples) and the audit FindingReport — never as cost, so the cost side
// and the adoption side never double-count.
//
// BOUNDARY (VERIFIED 2026-06-10, docs.claude.com/en/api/claude-code-analytics-api,
// verbatim): "This API only tracks Claude Code usage on the Claude API. Usage through
// Claude Platform on AWS, Claude in Microsoft Foundry, Claude in Amazon Bedrock, or
// Claude on Vertex AI is not included." So the surface is direct, the shadow-auth
// detector below only SEES Claude-API-served usage, and a 3P-provider fleet is
// invisible here — the OTel plane (connectors/claude) is its observation path.
func (s *Source) gatherClaudeCode(ctx context.Context, sink sdk.Sink) error {
	day := s.clock().UTC().Format(claudeCodeDateLayout)
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var resp claudeCodeResponse
		q := url.Values{}
		q.Set("starting_at", day)
		q.Set("limit", "1000") // Prevent the default 20/page from truncating at 400 devs/day.
		if page != "" {
			q.Set("page", page)
		}
		if err := s.client.GetJSON(ctx, claudeCodePath, q, &resp); err != nil {
			return err
		}
		occurred := parseTime(resp.dateOf(day))
		for _, rec := range resp.Data {
			at := parseTime(rec.Date)
			if at.IsZero() {
				at = occurred
			}
			// Productivity evidence (family #2): tool accept/reject + LOC/commits,
			// emitted for EVERY developer regardless of billing source — it is ROI/
			// governance data, not cost, so there is no double-count concern. The finding
			// is the sealed AUDIT record (hashed detail); the MetricSamples below are the
			// queryable read-model the Adoption dashboard aggregates. Both coexist.
			if f, ok := claudeCodeProductivityFinding(rec, at); ok {
				if err := sink.Emit(ctx, f); err != nil {
					return err
				}
			}
			// persist the per-developer/day productivity VALUES as adoption metrics
			// (an internal design note (not shipped) accept-reject/per-model tokens). Daily
			// snapshots — never cost (cost stays subscription-only below).
			for _, ms := range claudeCodeMetricSamples(rec, at) {
				if err := sink.Emit(ctx, ms); err != nil {
					return err
				}
			}
			// Shadow-auth detector: a developer whose Claude Code bills as
			// customer_type=api runs on a personal/API key OUTSIDE the org
			// subscription — identity/cost drift (seat attribution and spend
			// governance ride an ungoverned key). The detector SIGNALS drift, never
			// recon: the finding names the actor and day, no usage figures.
			if s.shadowAuth && rec.CustomerType == "api" {
				if f, ok := shadowAuthFinding(rec, at); ok {
					if err := sink.Emit(ctx, f); err != nil {
						return err
					}
				}
			}
			// Only subscription Claude Code spend is exclusive to this feed; API spend
			// is already in usage_report (counting it here too would double-count the
			// estimated stream). Emit COST only for customer_type="subscription".
			if rec.CustomerType != "subscription" {
				continue
			}
			for _, mb := range rec.ModelBreakdown {
				if err := sink.Emit(ctx, claudeCodeSample(rec.Actor.ref(), mb, at)); err != nil {
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

// dateOf returns a fallback occurred time for a claude_code page (the requested day),
// used when a record omits its own date.
func (claudeCodeResponse) dateOf(day string) string { return day + "T00:00:00Z" }

// claudeCodeSample builds an estimated CostSample for one actor's per-model Claude
// Code spend. estimated_cost.amount is integer minor units (cents), so micro-USD is
// amount × 10_000. Tokens fold to the input total with the cache split
// carried; the untiered cache-creation maps to the 5m (default) tier.
func claudeCodeSample(actor string, mb claudeCodeModelSpan, at time.Time) model.CostSample {
	t := mb.Tokens
	return model.CostSample{
		ProviderRef:           modelprovider.ProviderAnthropic,
		ModelRef:              mb.Model,
		InputTokens:           t.Input + t.CacheRead + t.CacheCreation,
		OutputTokens:          t.Output,
		CostMicroUSD:          centsIntToMicroUSD(mb.EstimatedCost.Amount),
		OccurredAt:            at,
		CacheReadTokens:       t.CacheRead,
		CacheCreation5mTokens: t.CacheCreation,
		Actor:                 actor,
		Gateway:               model.GatewayDirect,
		Provenance:            model.ProvenanceEstimated,
	}
}

// centsIntToMicroUSD converts an integer minor-unit amount (cents) to micro-USD
// (1 cent = 10_000 µUSD). A negative amount clamps to 0.
func centsIntToMicroUSD(cents int64) int64 {
	if cents < 0 {
		return 0
	}
	return cents * 10_000
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// costSample turns one usage result row into a derived CostSample. The two
// cache-write TTLs are carried DISTINCTLY (not summed) so the 1h tier is priced at
// its own ~2.0× rate instead of the 5m ~1.25× rate, and the full attribution set
// (workspace/api-key/service-tier/context-window/inference-geo) flows onto the
// sample. The cost is DERIVED from list pricing, so provenance is "estimated"; the
// surface is the connector's configured deployment surface (ANT2-01) — the Admin API
// only serves the Anthropic-operated surfaces (direct, claude-platform-aws), so this
// must NOT hardcode direct or FinOps mis-attributes a Claude-Platform-on-AWS estate.
func (s *Source) costSample(r usageResult, occurred time.Time) model.CostSample {
	u := modelprovider.Usage{
		ProviderRef:           modelprovider.ProviderAnthropic,
		ModelRef:              r.Model,
		InputTokens:           r.UncachedInputTokens,
		OutputTokens:          r.OutputTokens,
		CacheCreation1hTokens: r.CacheCreation.Ephemeral1hInputTokens,
		CacheCreation5mTokens: r.CacheCreation.Ephemeral5mInputTokens,
		CacheReadTokens:       r.CacheReadInputTokens,
		OccurredAt:            occurred,
		WorkspaceRef:          r.WorkspaceID,
		APIKeyRef:             r.APIKeyID,
		Actor:                 firstNonEmpty(r.AccountID, r.ServiceAccountID),
		ServiceTier:           r.ServiceTier,
		ContextWindow:         r.ContextWindow,
		InferenceGeo:          r.InferenceGeo,
		Gateway:               s.surface().Gateway,
		Provenance:            model.ProvenanceEstimated,
	}
	p, _, _, ok := pricingFor(r.Model)
	if !ok {
		// Unknown price: record usage with an underived (0) cost rather than guess.
		return modelprovider.ToCostSampleWithCost(u, 0)
	}
	return modelprovider.ToCostSample(u, p)
}

// billedSample turns one cost_report result row into an authoritative (billed)
// CostSample. cost_report returns no token counts (it is money, not usage), so the
// token fields are zero; the value is the billed amount itself. The description
// grouping populates model/service_tier/cost_type/context_window/inference_geo. The
// surface is the connector's configured deployment surface (ANT2-01), not hardcoded
// direct, so billed FinOps is attributed to the right surface.
func billedSample(r costReportResult, occurred time.Time, gateway model.Gateway) model.CostSample {
	return model.CostSample{
		ProviderRef:   modelprovider.ProviderAnthropic,
		ModelRef:      r.Model,
		CostMicroUSD:  centsToMicroUSD(r.Amount),
		OccurredAt:    occurred,
		WorkspaceRef:  r.WorkspaceID,
		ServiceTier:   r.ServiceTier,
		ContextWindow: r.ContextWindow,
		InferenceGeo:  r.InferenceGeo,
		CostType:      r.CostType,
		Gateway:       gateway,
		Provenance:    model.ProvenanceBilled,
	}
}

// centsToMicroUSD converts a cost_report amount — a decimal string in the currency's
// lowest units (cents for USD, e.g. "123.45" = $1.2345) — to integer micro-USD.
// One cent is 10,000 micro-USD. A malformed or negative amount yields 0 (unknown),
// never a guessed cost (ARCHITECTURE.md). Money stays integer on the wire; the float is a
// parse intermediate, rounded once.
func centsToMicroUSD(amount string) int64 {
	if amount == "" {
		return 0
	}
	cents, err := strconv.ParseFloat(amount, 64)
	if err != nil || cents < 0 {
		return 0
	}
	return int64(math.Round(cents * 10_000))
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// Snapshot returns the Claude catalog. With a credential it lists models, API keys
// and workspaces live (read-only) and enriches each model with declared pricing
// and the Claude stack; with no credential it returns the declared offline
// catalog (models only). It never returns key values — only the masked hint.
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: modelprovider.ProviderAnthropic, Kind: modelprovider.KindHostedAPI,
			Title: "Anthropic (Claude)", BaseURL: s.baseURL,
		},
		CapturedAt: s.clock().UTC(),
	}
	sf := s.surface()
	// The Models API is the capability source-of-truth (ANT2-16), but only the
	// Anthropic-operated surfaces expose it. Without a credential OR on a surface with
	// no Models API (Bedrock/Vertex/Foundry), return the declared offline catalog
	// (AsOf-stamped, CapabilitySource=declared) — honest degradation, never a 404.
	if sf.Supports("models") {
		cat.BetaHeaders = KnownBetaHeaders()
	}
	if s.adminKey == "" || s.client == nil || !sf.Supports("models") {
		cat.Models = declaredCatalogModels()
		return cat, nil
	}

	models, err := s.fetchModels(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	cat.Models = models

	// The Admin API (keys/workspaces/external_keys/rate_limits) is a strict subset of
	// surfaces (ANT2-01). Only read it where it applies.
	if sf.Supports("admin") {
		keys, err := s.fetchKeys(ctx)
		if err != nil {
			return modelprovider.Catalog{}, err
		}
		cat.Keys = keys

		ws, err := s.fetchWorkspaces(ctx)
		if err != nil {
			return modelprovider.Catalog{}, err
		}
		cat.Workspaces = ws

		ek, err := s.fetchExternalKeys(ctx) // ANT2-04: CMEK inventory (refs only)
		if err != nil {
			return modelprovider.Catalog{}, err
		}
		cat.ExternalKeys = ek

		rl, err := s.fetchRateLimits(ctx, ws) // ANT2-05: read-only rate-limit inventory
		if err != nil {
			return modelprovider.Catalog{}, err
		}
		cat.RateLimits = rl
	}
	return cat, nil
}

// declaredCatalogModels builds the offline model list from the declared ids.
func declaredCatalogModels() []modelprovider.Model {
	out := make([]modelprovider.Model, 0, len(declaredModelIDs))
	for _, d := range declaredModelIDs {
		out = append(out, buildModel(d.id, d.displayName))
	}
	return out
}

// fetchModels lists /v1/models and enriches each with declared pricing + stack.
func (s *Source) fetchModels(ctx context.Context) ([]modelprovider.Model, error) {
	var out []modelprovider.Model
	after := ""
	for i := 0; i < s.maxPages; i++ {
		var resp modelsResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, "/v1/models", q, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Data {
			mdl := buildModel(m.ID, m.DisplayName)
			mdl.CreatedAt = parseTime(m.CreatedAt)
			// ANT2-16: prefer the live Models-API capabilities/limits over the declared
			// catalog. max_tokens/max_input_tokens override the declared family defaults;
			// the capabilities object becomes the per-model knob set; the source flips to
			// "live" so a consumer knows it is authoritative, not estimated.
			if m.MaxTokens > 0 {
				mdl.MaxOutputTokens = m.MaxTokens
			}
			mdl.MaxInputTokens = m.MaxInputTokens
			if m.Capabilities != nil {
				mdl.APICapabilities = &modelprovider.ModelCapabilities{
					Batch:             m.Capabilities.Batch,
					StructuredOutputs: m.Capabilities.StructuredOutputs,
					EffortLevels:      m.Capabilities.Effort,
					ThinkingModes:     m.Capabilities.Thinking,
					ContextManagement: m.Capabilities.ContextManagement,
					AsOf:              s.clock().UTC().Format(claudeCodeDateLayout),
				}
				mdl.CapabilitySource = "live"
			}
			out = append(out, mdl)
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

// fetchKeys lists /v1/organizations/api_keys as inventory metadata (no secrets).
func (s *Source) fetchKeys(ctx context.Context) ([]modelprovider.KeyRef, error) {
	var out []modelprovider.KeyRef
	after := ""
	for i := 0; i < s.maxPages; i++ {
		var resp apiKeysResponse
		q := url.Values{"limit": {"100"}}
		if s.workspaceID != "" {
			q.Set("workspace_id", s.workspaceID)
		}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, "/v1/organizations/api_keys", q, &resp); err != nil {
			return nil, err
		}
		for _, k := range resp.Data {
			out = append(out, modelprovider.KeyRef{
				ID: k.ID, Name: k.Name, WorkspaceRef: k.WorkspaceID,
				Status: k.Status, Hint: k.PartialKeyHint, CreatedAt: parseTime(k.CreatedAt),
				ExpiresAt: parseTime(k.ExpiresAt), CreatedBy: k.CreatedBy.ID,
				PrincipalType: k.Principal.Type,
			})
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

// fetchWorkspaces lists /v1/organizations/workspaces as inventory metadata.
func (s *Source) fetchWorkspaces(ctx context.Context) ([]modelprovider.WorkspaceRef, error) {
	var out []modelprovider.WorkspaceRef
	after := ""
	for i := 0; i < s.maxPages; i++ {
		var resp workspacesResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, "/v1/organizations/workspaces", q, &resp); err != nil {
			return nil, err
		}
		for _, w := range resp.Data {
			ref := modelprovider.WorkspaceRef{
				ID: w.ID, Name: w.Name,
				Archived: w.ArchivedAt != "", CreatedAt: parseTime(w.CreatedAt),
				// ANT2-06 governance object (refs only, never key material).
				ExternalKeyID: w.ExternalKeyID,
				CompartmentID: w.CompartmentID,
				Geo:           w.WorkspaceGeo,
				Tags:          w.Tags,
			}
			if w.DataResidency != nil {
				ref.Residency = &modelprovider.DataResidency{
					AllowedInferenceGeos: w.DataResidency.AllowedInferenceGeos,
					DefaultInferenceGeo:  w.DataResidency.DefaultInferenceGeo,
				}
			}
			out = append(out, ref)
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

// parseTime parses an RFC3339 timestamp, returning the zero time on any error so a
// missing/odd provider timestamp never aborts a gather.
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
