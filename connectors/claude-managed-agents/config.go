// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.claude-managed-agents"

// version is the connector's own semantic version. 0.2.0 added active-session/thread
// observation (observe_sessions), the Dreams governance surface (observe_dreams +
// admitted_dream_outputs, deny-closed output-store admission) and the PERMITTED policy
// edges (vault credentials, agent tools[]/skills/roster). 0.3.0 added the
// ThreadEventReader — the dedicated request-time read surface the composition root
// constructs for the claude-agents console (structural thread events, never content).
const version = "0.3.0"

// Default configuration values (verified against the CMA docs, jun-2026).
const (
	defaultBaseURL          = "https://api.anthropic.com"
	defaultAnthropicVersion = "2023-06-01"
	// defaultBetaHeader gates every Managed Agents endpoint; the SDK sets it automatically.
	defaultBetaHeader = "managed-agents-2026-04-01"

	defaultRefresh        = 5 * time.Minute
	defaultMaxPages       = 20
	defaultWebhookAddr    = "127.0.0.1:8842"
	defaultWebhookPath    = "/cma/webhooks"
	defaultWebhookMaxSkew = 5 * time.Minute // the documented SDK unwrap() freshness window

	// backlogThreshold is the work-queue depth at or above which the connector raises a
	// backlog posture finding (a self-hosted queue with no workers silently starves
	// sessions). It is operator-tunable via work_backlog_threshold.
	defaultBacklogThreshold = 1
)

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgAPIKey           = "api_key"
	cfgBaseURL          = "base_url"
	cfgVersion          = "anthropic_version"
	cfgBeta             = "beta_header"
	cfgWorkspaceID      = "workspace_id"
	cfgMaxPages         = "max_pages"
	cfgRefresh          = "refresh_interval"
	cfgObserveVaults    = "observe_vaults"
	cfgObserveMemory    = "observe_memory"
	cfgObserveWorkQueue = "observe_work_queue"
	cfgObserveSkills    = "observe_skills"
	cfgObserveSessions  = "observe_sessions"
	cfgObserveDreams    = "observe_dreams"
	cfgAdmittedOutputs  = "admitted_dream_outputs"
	cfgEnvironmentIDs   = "environment_ids"
	cfgBacklog          = "work_backlog_threshold"

	cfgWebhookSecret = "webhook_secret"
	cfgWebhookAddr   = "webhook_listen_addr"
	cfgWebhookPath   = "webhook_path"
	cfgAllowPublic   = "allow_public_bind"
	cfgWebhookSkew   = "webhook_max_skew"
)

// config is the resolved connector configuration.
type config struct {
	apiKey      string
	baseURL     string
	version     string
	beta        string
	workspaceID string
	maxPages    int
	refresh     time.Duration

	observeVaults    bool
	observeMemory    bool
	observeWorkQueue bool
	observeSkills    bool
	observeSessions  bool
	observeDreams    bool
	admittedOutputs  map[string]bool // operator-recorded HITL admissions of dream output stores
	environmentIDs   []string
	backlog          int

	webhookSecret string
	webhookAddr   string
	webhookPath   string
	allowPublic   bool
	webhookSkew   time.Duration
}

// pollEnabled reports whether any GET-poller surface is configured (an API key plus at
// least one observe toggle). With no API key the connector runs webhook-only (or, with no
// webhook secret either, observes nothing — an honest no-op, never a fabricated inventory).
func (c config) pollEnabled() bool {
	return c.apiKey != "" && (c.observeVaults || c.observeMemory || c.observeWorkQueue ||
		c.observeSkills || c.observeSessions || c.observeDreams)
}

// webhookEnabled reports whether the inbound signed-webhook receiver is configured. The
// signing secret is REQUIRED — a receiver that cannot verify is never mounted (docs/SECURITY-HARDENING.md).
func (c config) webhookEnabled() bool { return c.webhookSecret != "" }

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude Managed Agents (CMA) control plane",
		Description: "Observes/governs the CMA control-plane resources (Vaults + MCP credentials, Memory Stores + immutable memory-version audit/redaction, active sessions + multi-agent threads with memory-store mounts/vault use/outcome verdicts, agent tools[]/skills/roster as PERMITTED policy edges, self-hosted work queue, and Dreams memory-curation jobs with deny-closed HITL admission of the OUTPUT store) and terminates the signed CMA webhooks fail-closed with a GET-back enrichment. Read-only: every API call is a GET; the webhook receiver verifies the HMAC and rejects anything unsigned, stale or replayed.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgAPIKey, Type: sdk.FieldString, Secret: true, Description: "Anthropic organization API key reference for the read-only GET pollers (never persisted, never logged). Empty = webhook-only / offline (no inventory fabricated)."},
			{Key: cfgBaseURL, Type: sdk.FieldString, Default: defaultBaseURL, Description: "Anthropic API base URL."},
			{Key: cfgVersion, Type: sdk.FieldString, Default: defaultAnthropicVersion, Description: "anthropic-version header value."},
			{Key: cfgBeta, Type: sdk.FieldString, Default: defaultBetaHeader, Description: "anthropic-beta header value gating the Managed Agents API."},
			{Key: cfgWorkspaceID, Type: sdk.FieldString, Description: "Optional workspace filter for the inventory pollers."},
			{Key: cfgMaxPages, Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per poll pass."},
			{Key: cfgRefresh, Type: sdk.FieldDuration, Default: defaultRefresh.String(), Description: "GET-poller cadence (vaults/memory/work-queue/agents/sessions/dreams inventory)."},
			{Key: cfgObserveVaults, Type: sdk.FieldBool, Default: "true", Description: "Inventory + govern Vaults and their MCP credentials (lateral-movement posture; refresh state)."},
			{Key: cfgObserveMemory, Type: sdk.FieldBool, Default: "true", Description: "Inventory Memory Stores and their immutable memory-version audit (actor + redaction evidence)."},
			{Key: cfgObserveWorkQueue, Type: sdk.FieldBool, Default: "true", Description: "Observe the self-hosted work-queue depth/backlog/worker liveness (GET stats+list; never claims work)."},
			{Key: cfgObserveSkills, Type: sdk.FieldBool, Default: "true", Description: "Govern agent definitions: attached Skills (unpinned 'latest' supply-chain signals), declared tools[] permission policies as PERMITTED edges, and the multi-agent roster."},
			{Key: cfgObserveSessions, Type: sdk.FieldBool, Default: "true", Description: "Observe active (idle/running) sessions: memory-store mounts (read_write = poisoning write target), vault use, terminal outcome verdicts, multi-agent threads."},
			{Key: cfgObserveDreams, Type: sdk.FieldBool, Default: "true", Description: "Inventory + govern Dreams memory-curation jobs (research preview, GATED): provenance edges, OUTPUT-store HITL admission (deny-closed), unadmitted-attach drift. Degrades honestly (declared, never fabricated) when the org lacks preview access."},
			{Key: cfgAdmittedOutputs, Type: sdk.FieldString, Description: "JSON array or comma-separated list of dream OUTPUT memory-store ids (memstore_...) a human ADMITTED for productive attach via the governed approval. Deny-closed: every output store not listed is unadmitted; an observed attach of an unadmitted store raises a HIGH finding."},
			{Key: cfgEnvironmentIDs, Type: sdk.FieldString, Description: "Optional JSON array or comma-separated list of self-hosted environment ids (env_...) to poll for work-queue stats. Empty = discover via GET /v1/environments."},
			{Key: cfgBacklog, Type: sdk.FieldInt, Default: strconv.Itoa(defaultBacklogThreshold), Description: "Work-queue depth at/above which a backlog posture finding is raised."},

			{Key: cfgWebhookSecret, Type: sdk.FieldString, Secret: true, Description: "CMA webhook signing secret (whsec_... shown once at endpoint creation). Empty = the inbound receiver is not mounted. REQUIRED to terminate webhooks (an unverifiable receiver is never mounted)."},
			{Key: cfgWebhookAddr, Type: sdk.FieldString, Default: defaultWebhookAddr, Description: "Webhook receiver bind address. Loopback by default; a non-loopback bind is refused unless allow_public_bind=true. Front it with a TLS gateway: CMA requires HTTPS:443."},
			{Key: cfgWebhookPath, Type: sdk.FieldString, Default: defaultWebhookPath, Description: "Webhook receiver endpoint path."},
			{Key: cfgAllowPublic, Type: sdk.FieldBool, Default: "false", Description: "DANGEROUS: allow binding the receiver to a non-loopback address. Keep loopback (secure default) and terminate TLS in front."},
			{Key: cfgWebhookSkew, Type: sdk.FieldDuration, Default: defaultWebhookMaxSkew.String(), Description: "Freshness window for a webhook delivery (the documented SDK unwrap() rejects payloads older than 5 minutes)."},
		},
	}
}

// loadConfig resolves and validates the connector configuration. It fails closed: a
// non-loopback webhook bind without allow_public_bind, or a nonsensical interval, is an
// error here, before Gather.
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		apiKey:           strings.TrimSpace(cfg.Get(cfgAPIKey)),
		baseURL:          firstNonEmpty(strings.TrimSpace(cfg.Get(cfgBaseURL)), defaultBaseURL),
		version:          firstNonEmpty(strings.TrimSpace(cfg.Get(cfgVersion)), defaultAnthropicVersion),
		beta:             firstNonEmpty(strings.TrimSpace(cfg.Get(cfgBeta)), defaultBetaHeader),
		workspaceID:      strings.TrimSpace(cfg.Get(cfgWorkspaceID)),
		maxPages:         cfg.GetInt(cfgMaxPages, defaultMaxPages),
		refresh:          cfg.GetDuration(cfgRefresh, defaultRefresh),
		observeVaults:    cfg.GetBool(cfgObserveVaults, true),
		observeMemory:    cfg.GetBool(cfgObserveMemory, true),
		observeWorkQueue: cfg.GetBool(cfgObserveWorkQueue, true),
		observeSkills:    cfg.GetBool(cfgObserveSkills, true),
		observeSessions:  cfg.GetBool(cfgObserveSessions, true),
		observeDreams:    cfg.GetBool(cfgObserveDreams, true),
		admittedOutputs:  admittedSet(cfg.Get(cfgAdmittedOutputs)),
		environmentIDs:   splitList(cfg.Get(cfgEnvironmentIDs)),
		backlog:          cfg.GetInt(cfgBacklog, defaultBacklogThreshold),

		webhookSecret: strings.TrimSpace(cfg.Get(cfgWebhookSecret)),
		webhookAddr:   firstNonEmpty(strings.TrimSpace(cfg.Get(cfgWebhookAddr)), defaultWebhookAddr),
		webhookPath:   firstNonEmpty(strings.TrimSpace(cfg.Get(cfgWebhookPath)), defaultWebhookPath),
		allowPublic:   cfg.GetBool(cfgAllowPublic, false),
		webhookSkew:   cfg.GetDuration(cfgWebhookSkew, defaultWebhookMaxSkew),
	}
	if c.maxPages <= 0 {
		c.maxPages = defaultMaxPages
	}
	if c.refresh <= 0 {
		c.refresh = defaultRefresh
	}
	if c.backlog < 0 {
		c.backlog = defaultBacklogThreshold
	}
	if c.webhookSkew <= 0 {
		c.webhookSkew = defaultWebhookMaxSkew
	}
	if c.webhookEnabled() {
		if !strings.HasPrefix(c.webhookSecret, webhookSecretPrefix) {
			return config{}, fmt.Errorf("claude-managed-agents: %s must be the whsec_-prefixed signing secret shown at webhook-endpoint creation", cfgWebhookSecret)
		}
		// Deny-closed at CONFIG time, through the product's single admission point
		//. This connector's own classifier used strings.EqualFold, which
		// folds UNICODE: it answered TRUE for "localhoſt" (U+017F), a host that is
		// not the name it checked for. netbind folds ASCII only.
		if err := netbind.Check(c.webhookAddr, c.bindPolicy()); err != nil {
			return config{}, fmt.Errorf("claude-managed-agents: %w", err)
		}
	}
	if !c.pollEnabled() && !c.webhookEnabled() {
		return config{}, fmt.Errorf("claude-managed-agents: nothing to observe (set %s for the inventory pollers and/or %s for the webhook receiver)", cfgAPIKey, cfgWebhookSecret)
	}
	return c, nil
}

// admittedSet parses the operator-recorded admitted dream-output list into a lookup set
// (same tolerant list syntax as environment_ids). An empty config yields an empty set:
// DENY-CLOSED — with nothing recorded, every dream output store is unadmitted.
func admittedSet(raw string) map[string]bool {
	ids := splitList(raw)
	if len(ids) == 0 {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// splitList parses an environment-id list given as a JSON array OR a comma/space-separated
// string. Blank entries are dropped.
func splitList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Tolerate a JSON array form ["env_a","env_b"] by stripping brackets/quotes; otherwise
	// split on comma/whitespace. Either way the result is a clean list of ids.
	raw = strings.Trim(raw, "[]")
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '"'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// bindPolicy describes the webhook receiver to the single admission point. The
// receiver has no TLS of its own — CMA requires HTTPS:443, so a TLS gateway is
// expected in front of it.
func (c config) bindPolicy() netbind.Policy {
	return netbind.Policy{
		Component:   "claude-managed-agents",
		Purpose:     "webhook receiver",
		AllowPublic: c.allowPublic,
		OptIn:       cfgAllowPublic,
	}
}
