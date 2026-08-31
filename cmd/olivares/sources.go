// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/firstparty"
	"github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/connectors/aaa"
	"github.com/olivaresai/olivares/connectors/agent365"
	"github.com/olivaresai/olivares/connectors/agentcore"
	"github.com/olivaresai/olivares/connectors/agentsmd"
	aigateway "github.com/olivaresai/olivares/connectors/ai-gateway"
	"github.com/olivaresai/olivares/connectors/aicontroltower"
	"github.com/olivaresai/olivares/connectors/argocd"
	"github.com/olivaresai/olivares/connectors/awskms"
	azureactivity "github.com/olivaresai/olivares/connectors/azure-activity"
	azureblobaudit "github.com/olivaresai/olivares/connectors/azure-blob-audit"
	azureopenai "github.com/olivaresai/olivares/connectors/azure-openai"
	"github.com/olivaresai/olivares/connectors/azureaisearch"
	"github.com/olivaresai/olivares/connectors/azurekeyvault"
	bedrockkb "github.com/olivaresai/olivares/connectors/bedrock-kb"
	bigqueryaudit "github.com/olivaresai/olivares/connectors/bigquery-audit"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	claudeappsgateway "github.com/olivaresai/olivares/connectors/claude-apps-gateway"
	claudebatch "github.com/olivaresai/olivares/connectors/claude-batch"
	claudecompliance "github.com/olivaresai/olivares/connectors/claude-compliance"
	claudeconfig "github.com/olivaresai/olivares/connectors/claude-config"
	claudeconsole "github.com/olivaresai/olivares/connectors/claude-console"
	claudemanagedagents "github.com/olivaresai/olivares/connectors/claude-managed-agents"
	claudeprojects "github.com/olivaresai/olivares/connectors/claude-projects"
	clauderoutines "github.com/olivaresai/olivares/connectors/claude-routines"
	claudewif "github.com/olivaresai/olivares/connectors/claude-wif"
	"github.com/olivaresai/olivares/connectors/cline"
	"github.com/olivaresai/olivares/connectors/cloudflare"
	"github.com/olivaresai/olivares/connectors/codex"
	codexmanagedconfig "github.com/olivaresai/olivares/connectors/codex-managed-config"
	"github.com/olivaresai/olivares/connectors/cohere"
	"github.com/olivaresai/olivares/connectors/confluence"
	"github.com/olivaresai/olivares/connectors/contentsource"
	coworkanalytics "github.com/olivaresai/olivares/connectors/cowork-analytics"
	"github.com/olivaresai/olivares/connectors/crossplane"
	"github.com/olivaresai/olivares/connectors/cursor"
	databricksuc "github.com/olivaresai/olivares/connectors/databricks-uc"
	"github.com/olivaresai/olivares/connectors/deepseek"
	deltasharing "github.com/olivaresai/olivares/connectors/delta-sharing"
	"github.com/olivaresai/olivares/connectors/ebpf"
	"github.com/olivaresai/olivares/connectors/edugain"
	egressproxy "github.com/olivaresai/olivares/connectors/egress-proxy"
	entraagent "github.com/olivaresai/olivares/connectors/entra-agent"
	envoyaigw "github.com/olivaresai/olivares/connectors/envoy-ai-gateway"
	"github.com/olivaresai/olivares/connectors/externalsecrets"
	"github.com/olivaresai/olivares/connectors/fal"
	"github.com/olivaresai/olivares/connectors/flux"
	foundryagents "github.com/olivaresai/olivares/connectors/foundry-agents"
	"github.com/olivaresai/olivares/connectors/fscontent"
	gcpaudit "github.com/olivaresai/olivares/connectors/gcp-audit"
	"github.com/olivaresai/olivares/connectors/gcpkms"
	gcsaudit "github.com/olivaresai/olivares/connectors/gcs-audit"
	"github.com/olivaresai/olivares/connectors/gdrive"
	"github.com/olivaresai/olivares/connectors/gemini"
	geminicli "github.com/olivaresai/olivares/connectors/gemini-cli"
	githubsrc "github.com/olivaresai/olivares/connectors/github"
	gitlabsrc "github.com/olivaresai/olivares/connectors/gitlab"
	"github.com/olivaresai/olivares/connectors/glm"
	googleadk "github.com/olivaresai/olivares/connectors/google-adk"
	googleagent "github.com/olivaresai/olivares/connectors/google-agent"
	"github.com/olivaresai/olivares/connectors/goose"
	"github.com/olivaresai/olivares/connectors/grok"
	"github.com/olivaresai/olivares/connectors/hermes"
	icebergcatalog "github.com/olivaresai/olivares/connectors/iceberg-catalog"
	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/idp"
	inferencegateway "github.com/olivaresai/olivares/connectors/inference-gateway"
	"github.com/olivaresai/olivares/connectors/infisical"
	istiotelemetry "github.com/olivaresai/olivares/connectors/istio-telemetry"
	"github.com/olivaresai/olivares/connectors/kerberos"
	"github.com/olivaresai/olivares/connectors/keycloak"
	"github.com/olivaresai/olivares/connectors/kmip"
	kongagw "github.com/olivaresai/olivares/connectors/kong-agent-gateway"
	kongaudit "github.com/olivaresai/olivares/connectors/kong-audit"
	"github.com/olivaresai/olivares/connectors/ldap"
	"github.com/olivaresai/olivares/connectors/litellm"
	"github.com/olivaresai/olivares/connectors/local"
	"github.com/olivaresai/olivares/connectors/managedsettings"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/connectors/mcpb"
	"github.com/olivaresai/olivares/connectors/mistral"
	mongoaudit "github.com/olivaresai/olivares/connectors/mongo-audit"
	mssqlaudit "github.com/olivaresai/olivares/connectors/mssql-audit"
	"github.com/olivaresai/olivares/connectors/notion"
	"github.com/olivaresai/olivares/connectors/oasf"
	"github.com/olivaresai/olivares/connectors/onepassword"
	"github.com/olivaresai/olivares/connectors/openai"
	"github.com/olivaresai/olivares/connectors/openclaw"
	"github.com/olivaresai/olivares/connectors/opencode"
	"github.com/olivaresai/olivares/connectors/openhands"
	"github.com/olivaresai/olivares/connectors/openidfed"
	"github.com/olivaresai/olivares/connectors/openlineage"
	"github.com/olivaresai/olivares/connectors/openrouter"
	oracleaudit "github.com/olivaresai/olivares/connectors/oracle-audit"
	"github.com/olivaresai/olivares/connectors/pgaudit"
	"github.com/olivaresai/olivares/connectors/pgcontent"
	redshiftaudit "github.com/olivaresai/olivares/connectors/redshift-audit"
	runtimesource "github.com/olivaresai/olivares/connectors/runtime"
	"github.com/olivaresai/olivares/connectors/s3cloudtrail"
	"github.com/olivaresai/olivares/connectors/s3content"
	"github.com/olivaresai/olivares/connectors/salesforce"
	"github.com/olivaresai/olivares/connectors/sapodata"
	"github.com/olivaresai/olivares/connectors/sharepoint"
	snowflakecontent "github.com/olivaresai/olivares/connectors/snowflake"
	snowflakeaudit "github.com/olivaresai/olivares/connectors/snowflake-audit"
	"github.com/olivaresai/olivares/connectors/sops"
	"github.com/olivaresai/olivares/connectors/spiffe"
	"github.com/olivaresai/olivares/connectors/ssf"
	"github.com/olivaresai/olivares/connectors/tak"
	"github.com/olivaresai/olivares/connectors/vault"
	"github.com/olivaresai/olivares/connectors/vertex"
	"github.com/olivaresai/olivares/connectors/xai"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk"
)

// This file is the CB-1 ingestion composition: the single place that wires real
// connectors into the runtime as a PRODUCTION caller, for observation sources AND
// identity roster providers, resolved from ONE operator config so the
// connector-wiring decision is made once (12 §7.3 IDN-06). It mirrors the notify
// composition (notifydispatch.go): secrets live in the operator's config file,
// referenced by value, never persisted by the engine (docs/SECURITY-HARDENING.md).
//
// It is also the kind→connector registry for the R/RW access-map DIFFERENTIAL
// connectors: pgaudit/s3cloudtrail/ebpf/runtime/mcp are wired
// here as in-process observation sources (buildInProcSource), and the knowledge
// document sources (gdrive/confluence/notion/sharepoint/s3content/sap_odata/
// salesforce/snowflake/azure_ai_search) — which are contentsource.Source, not
// sdk.SourceConnector — are resolved by knowledgeContentOptions for the knowledge
// module (VIII) to drive, from the SAME operator config.
//
// Transports (CB-1, decided = option C):
//   - (B) out-of-process plugin, AutoMTLS — the substrate. A first-party connector
//     with a heavy dependency tree (claude/OTLP) is embedded (firstparty) and
//     launched as an isolated subprocess, so its deps never link into the core.
//   - (C) remote collector push — the same source wiring runs in `collector` mode
//     with a push Sink (collector.go); resolved through the SAME wireSources.
//   - (A) in-process — the fast path, only for first-party connectors already
//     linked for another reason (Vault, which is also a roster provider).

// defaultRosterSyncInterval is how often the engine re-runs the roster SyncRoster
// when the operator does not override it.
const defaultRosterSyncInterval = 15 * time.Minute

// sourcesConfig is the operator's ingestion provisioning, read from the file named
// by OLIVARES_SOURCES_CONFIG. It is the single CB-1 wiring input for sources AND
// identity roster providers.
type sourcesConfig struct {
	// Sources are observation source connectors registered with the runtime.
	Sources []sourceSpec `json:"sources"`
	// Identity are identity connectors whose roster snapshot feeds governance
	// (the NHI roster), and which may additionally stream permitted-access edges.
	Identity []identitySpec `json:"identity"`
	// Documents are knowledge document-source connectors (gdrive/confluence/notion/
	// sharepoint/s3content/sap_odata/salesforce/snowflake/azure_ai_search). They
	// are NOT observation sources: they emit no bus
	// observation and produce no R/RW edge — the knowledge module (VIII) PULLS them
	// (List/Fetch) on an ingest request — so they are wired into that module at
	// construction (knowledgeContentOptions), not registered with the runtime here.
	Documents []documentSpec `json:"documents,omitempty"`
	// RosterSyncSeconds overrides the periodic roster-sync interval (default 900s).
	RosterSyncSeconds int `json:"roster_sync_seconds,omitempty"`
	// ConnectorTrust is the operator trust root for EXTERNAL (third-party)
	// connector plugin binaries (S142): the anchors (keys/roots, optionally
	// keyless identity pins) and predicate allow-list admitExternalPlugin
	// verifies a source's Plugin attestation against. nil ⇒ deny-closed: every
	// external plugin source is refused — there is no observe mode and no
	// allow-unsigned escape hatch. It lives HERE, in the same operator-owned
	// config file that carries source secrets, because trusting a third-party
	// binary is a CB-1 wiring decision made once by the operator
	// (externalplugins.go documents the full trust model).
	ConnectorTrust *connectorTrustSpec `json:"connector_trust,omitempty"`
}

// sourceSpec provisions one observation source.
type sourceSpec struct {
	Name string `json:"name"`
	// Kind selects the connector (e.g. "claude", "vault").
	Kind string `json:"kind"`
	// Tenant is the business tenant reference its observations belong to.
	Tenant string `json:"tenant"`
	// PollSeconds re-runs a BATCH source every interval (0 = run once / streaming).
	// It applies to in-process sources; a streaming plugin source (claude) ignores it.
	PollSeconds int `json:"poll_seconds,omitempty"`
	// Config is the connector's settings (carries the source's secrets by value).
	Config map[string]string `json:"config,omitempty"`
	// Plugin provisions this source as an EXTERNAL (third-party) connector plugin
	// binary (S142) instead of a first-party kind. When set, Kind is NOT
	// consulted — the binary self-describes via its Descriptor (the kind maps are
	// first-party routing only) — and PollSeconds is ignored exactly as it is for
	// first-party plugin sources (one-shot/streaming; the engine owns
	// scheduling). The binary runs ONLY after admitExternalPlugin verifies its
	// signature and digest against cfg.ConnectorTrust (deny-closed).
	Plugin *externalPluginSpec `json:"plugin,omitempty"`
}

// identitySpec provisions one identity connector as a roster provider (and,
// optionally, as a permitted-access source).
type identitySpec struct {
	Name string `json:"name"`
	// Kind selects the identity connector ("ldap", "idp", "vault", "infisical", "spiffe").
	Kind string `json:"kind"`
	// Tenant is the business tenant the roster belongs to.
	Tenant string `json:"tenant"`
	// AsSource also wires the connector as an in-process source so its Gather
	// runs. Since every identity connector with a grant surface emits its
	// permitted-grant edges there (vault ACL paths; ldap privileged-directory
	// grants; idp app/scope assignments; infisical project grants), so
	// as_source=true gives a ONE-SHOT permitted-grant pass per boot. For
	// periodic re-scans wire a sources entry with poll_seconds instead — NEVER
	// both for one kind (Descriptor names are unique; the second registration
	// fails as a duplicate). Note okta+entra share Descriptor olivares.idp, so
	// only one idp-family instance can register as a source per process (the
	// One-instance-per-kind limit).
	AsSource bool `json:"as_source,omitempty"`
	// Config is the connector's settings (carries directory/credential references).
	Config map[string]string `json:"config,omitempty"`
}

// documentSpec provisions one knowledge document source. Name is the source name an
// operator references in POST /v1/m/knowledge/kbs/{id}/ingest {"source":"<name>"};
// Kind selects a first-party connector (gdrive/confluence/notion/sharepoint/
// s3content/sap_odata/salesforce/snowflake/azure_ai_search). Plugin provisions an
// EXTERNAL (third-party) content-source plugin binary instead, using the same
// trust/admission shape as sourceSpec.Plugin. Config carries connector settings —
// an export path and/or a secret-store credential REFERENCE
// (e.g. vault:secret/...#token), never an inline secret (first-party connectors'
// Descriptor declares their fields; plugin descriptors are fetched out-of-process).
type documentSpec struct {
	Name   string              `json:"name"`
	Kind   string              `json:"kind"`
	Config map[string]string   `json:"config,omitempty"`
	Plugin *externalPluginSpec `json:"plugin,omitempty"`
}

// rosterSyncInterval resolves the configured or default roster-sync cadence.
func (c sourcesConfig) rosterSyncInterval() time.Duration {
	if c.RosterSyncSeconds > 0 {
		return time.Duration(c.RosterSyncSeconds) * time.Second
	}
	return defaultRosterSyncInterval
}

// loadSourcesConfig reads OLIVARES_SOURCES_CONFIG (a JSON sourcesConfig). It is the
// operator's secret-bearing config, kept out of the store. A missing path yields an
// empty config (and the boot warns that nothing real is wired); a supplied path must be
// readable and contain valid JSON or startup fails closed.
func loadSourcesConfig(_ *slog.Logger) (sourcesConfig, error) {
	path := os.Getenv("OLIVARES_SOURCES_CONFIG")
	if path == "" {
		return sourcesConfig{}, nil
	}
	var cfg sourcesConfig
	if err := loadOperatorJSONConfig("OLIVARES_SOURCES_CONFIG", path, &cfg); err != nil {
		return sourcesConfig{}, err
	}
	return cfg, nil
}

// pluginBinaryForKind maps a source kind to the embedded plugin binary that serves
// it OUT-OF-PROCESS (CB-1 transport B). A kind listed here is launched as an
// isolated subprocess so its dependency tree never links into the engine; a kind
// not listed is built in-process (transport A) when buildInProcSource knows it.
var pluginBinaryForKind = map[string]string{
	"claude": "claude-source",
	// the Claude Cowork OTLP/HTTP logs receiver. Runs OUT-OF-PROCESS exactly
	// like the claude source so its OpenTelemetry-proto dependency tree never links
	// into the core (the same deps/SBOM isolation, ARCHITECTURE.md). Its sibling
	// engagement source (cowork-analytics) is modelprovider-only and runs in-process.
	"cowork": "cowork-source",
	// Messaging/eventing broker observers (INT-MSG-*). Every one runs
	// OUT-OF-PROCESS (transport B, AutoMTLS) so its wire-protocol dependency tree —
	// franz-go for kafka/debezium, go-amqp for amqp, the hand-rolled stdlib clients
	// for nats/mqtt, SigV4/REST for cloudqueue — NEVER links into the core. That is
	// the deps/SBOM isolation the distributed ingest plane is built on (ARCHITECTURE.md,
	// S02 §1): the composition root references a connector by KIND only and imports
	// none of them. The embedded binary is produced by `task build:connectors`; a
	// plain dev build omits it and the boot WARNS honestly (12 §5), never a silent
	// no-op. Kafka reaches Event Hubs/Redpanda/MSK and AMQP reaches RabbitMQ/Service
	// Bus through the same one connector each (one wire, many targets).
	"kafka":      "kafka-source",
	"amqp":       "amqp-source",
	"nats":       "nats-source",
	"mqtt":       "mqtt-source",
	"cloudqueue": "cloudqueue-source",
	"debezium":   "debezium-source",
	// Network/mesh L7 observers that carry the Envoy/Cilium gRPC dependency tree
	// run OUT-OF-PROCESS (transport B, AutoMTLS) so go-control-plane and the Cilium
	// API never link into the pure-Go core (ARCHITECTURE.md, §4). One connector each:
	// `envoy` hosts the ALS/ext_authz/ext_proc observation services Envoy streams to
	// (read-first, always allow/continue); `hubble` is the Hubble Relay flow client.
	// The composition root references them by KIND only and imports neither.
	"envoy":  "envoy-source",
	"hubble": "hubble-source",
}

// buildInProcSource constructs a first-party source connector cheap enough to run
// IN-PROCESS (CB-1 transport A, the fast path). Only low-dependency connectors
// already linked for another reason qualify — Vault, which is also a roster
// provider, additionally streams permitted-access edges. Everything heavier goes
// out-of-process (pluginBinaryForKind).
func buildInProcSource(kind string) (sdk.SourceConnector, bool) {
	switch kind {
	case "vault":
		return vault.New(), true
	case "claude-api":
		// Claude Admin-API cost + governance source (Apache). Read-only,
		// minimal-dependency (only the modelprovider HTTP client), so it runs in-process
		// (transport A). Its Gather streams CostSamples and emits the ANT2-03/04/05/06
		// governance posture findings; offline (no admin_key) it is a no-op. NOTE: this
		// Gather instance is owned by the runtime scheduler and is NOT what serves module
		// routes — module-reachable reads construct their own dedicated instance in the
		// composition root (the rate-limit inventory in modelsactuate.go, the
		// platforms reference in platformsref.go); the CatalogProvider.Snapshot live
		// catalog itself still has no module consumer.
		return claudeapi.New(), true
	case "claude-config":
		// CLA-14: the static-config discovery feeder. Reads a Claude config tree
		// (subagents/Skills/plugins/output-styles) and emits DECLARED-capability edges
		// (Source=config) so the capability graph distinguishes declared from observed.
		// Read-only, metadata-only (never a prompt body/skill content/secret), and a
		// dependency-light pure-stdlib+yaml parser — so it runs IN-PROCESS (transport A)
		// like claude-api, NOT out-of-process with the OTLP-heavy runtime claude source.
		// A re-pollable batch source: set poll_seconds to re-scan; the reactor's upsert
		// by (origin, capability) makes re-emission idempotent.
		return claudeconfig.New(), true
	case "claude-managed-agents":
		// (C1-C5): the Claude Managed Agents control-plane source (Apache). Inventories +
		// governs the CMA resources orthogonal to A2A/MCP — Vaults + MCP credentials, Memory
		// Stores + immutable memory-version audit/redaction, permission-policies + outcome
		// graders, the self-hosted work queue, and Skills — and terminates the signed CMA
		// webhooks FAIL-CLOSED (HMAC-verified; unsigned/stale/replayed deliveries are rejected).
		// Read-only (every API call is a GET) and pure-stdlib+httpx, so it runs IN-PROCESS
		// (transport A). It is a STREAMING source: register it with poll_seconds=0 — it owns its
		// own poll cadence (refresh_interval) and blocks in Gather running the webhook receiver
		// and the GET-pollers; the engine never re-polls a streaming source. Offline (no
		// api_key/webhook_secret) it is a no-op (never a fabricated inventory).
		return claudemanagedagents.New(), true
	case "codex":
		// G4: OpenAI Codex governance source (Apache). Read-only and minimal-
		// dependency (only the modelprovider HTTP client), so it runs in-process
		// (transport A) exactly like claude-api. Its Gather streams Codex-attributed
		// CostSamples (Analytics estimated + opt-in billed Costs API, CostType="codex"),
		// the Codex Usage/Auth/Admin-Audit compliance logs and the org audit logs as
		// external_activity evidence, and adoption findings; the catalog half (Snapshot:
		// Codex models + workspace/access-token + admin-key inventory) is declared through
		// the CatalogProvider seam (no module consumer reads it today). Auth = OpenAI API
		// key OR a Codex workspace
		// access token (never a consumer subscription, ToS). Offline (no api_key) it is a
		// no-op; the sales-gated/UNVERIFIED Analytics/Compliance surfaces degrade to a
		// posture finding rather than failing. cmd/codex-source exists for collector mode.
		return codex.New(), true
	case "grok":
		// AGT-04: fuente de gobierno de Grok Build (Apache). Local y de sólo lectura — no
		// habla con ninguna API, así que corre en proceso sin credencial. Su Gather emite el
		// perfil de sandbox OBSERVADO y, siempre, el hallazgo de que ese perfil **no está
		// impuesto**: se elige por tres vías (--sandbox, config.toml, GROK_SANDBOX) y x.ai no
		// documenta ninguna imposición administrativa. La mitad de observación va por el
		// ingest OTLP que ya existe (Grok emite OTLP por gRPC y HTTP, verificado en su
		// Cargo.toml). El cable de hooks vive en connectors/grok/session.
		return grok.New(), true
	case "cursor":
		// Cursor governance source (Apache). Read-only and minimal-
		// dependency (a small Basic-auth HTTP client over the Cursor Admin API, plus the
		// modelprovider cost helper), so it runs in-process (transport A) exactly like
		// codex. Its Gather streams Cursor-attributed billed CostSamples (chargedCents,
		// CostType="cursor"), the team audit logs as external_activity evidence, member
		// inventory and per-user budget-posture findings. Auth = a Cursor Admin API key
		// presented as the HTTP Basic username (never a consumer credential). Offline (no
		// api_key) it is a no-op; a plan-gated 403/404 degrades to a posture finding
		// rather than failing. cmd/cursor-source exists for collector mode.
		return cursor.New(), true
	case "gemini-cli":
		// Gemini CLI governance source (Apache). Reads Google's Gemini CLI
		// agent's LOCAL settings.json layers (system/user/workspace) read-only — pure
		// stdlib, no network — so it runs in-process (transport A) like managed-settings.
		// Its Gather emits PERMITTED config edges (configured MCP servers + allowed tools),
		// posture findings on enforcement gaps (no admin settings, YOLO not disabled,
		// telemetry off, prompt logging on, wide tool/MCP surface, auth not pinned), an
		// effective-config inventory, an observe-coverage finding (live usage rides the
		// Gen_ai.* OTel ingest, since the CLI emits gen_ai.*), and a Policy-Engine
		// presence signal. It is NOT the Gemini API (that is the "gemini" kind). A
		// re-pollable batch source: set poll_seconds to re-scan. cmd/gemini-cli-source
		// exists for collector mode.
		return geminicli.New(), true
	case "openhands":
		// OpenHands governance source (Apache). Reads the local config.toml
		// + env vars (read-only, no network), so it runs in-process (transport A).
		// Emits posture findings on enforcement gaps (sandbox type, model pinning,
		// credential exposure, telemetry, iteration limits), PERMITTED MCP/action
		// edges, an inventory summary, and a coverage finding. OpenHands has the
		// best OSS OTEL gen_ai.* story — live usage arrives via the ingest.
		// CostType="openhands". A re-pollable batch source: set poll_seconds.
		return openhands.New(), true
	case "goose":
		// Goose (by Block) governance source (Apache). Reads profiles.yaml
		// + env vars (read-only, no network), in-process (transport A). Posture
		// findings on admin settings, model pinning, extension governance,
		// telemetry, tool approval mode, code isolation. PERMITTED extension/tool
		// edges. CostType="goose". A re-pollable batch source: set poll_seconds.
		return goose.New(), true
	case "cline":
		// Cline / Kilo Code governance source (Apache). Reads VSCode
		// settings.json (cline.*/kilocode.* namespace via variant config),
		// in-process (transport A). Posture findings on auto-approve, MCP
		// allowlist, credential exposure, model pinning, custom instructions,
		// no native OTEL. CostType="cline". A re-pollable batch source.
		return cline.New(), true
	case "opencode":
		// opencode (SST) agent-surface governance (Apache). Reads local opencode.json(c)
		// (JSONC), in-process. Posture on permission model, admin/managed override layer, MCP
		// allowlist, credential-in-config, share egress, OTEL coverage; permitted MCP/tool/agent
		// edges; authoring fragment. CostType="opencode". Re-pollable.
		return opencode.New(), true
	case "openclaw":
		// OpenClaw governance source (Apache). Reads the real
		// ~/.openclaw/openclaw.json JSON5 surface (env/default/legacy/profile
		// discovery, confined $include, ${VAR} validation), in-process
		// (transport A). Evaluates gateway/channel/tool/sandbox/skill/plugin/model
		// posture per agent, emits config-declared channel/skill/model edges,
		// inventory and config-only diagnostics coverage. No inline PEP hook is
		// verified upstream. Exports Meter with provider/model split. CostType=
		// "openclaw". Re-pollable.
		return openclaw.New(), true
	case "hermes":
		// Hermes Agent governance source (Apache). Reads the real
		// $HERMES_HOME/config.yaml layout (default ~/.hermes/config.yaml),
		// ~/.hermes/profiles/* profile trees, state-dir .env key names, and the
		// /etc/hermes managed scope ($HERMES_MANAGED_DIR) with managed leaf
		// overrides, in-process (transport A). Evaluates terminal/channel/skill/
		// security/model/MCP posture, emits config-declared channel/skill/model/
		// MCP edges, inventory and Langfuse-plugin coverage. No inline PEP hook
		// or native OTEL exporter is verified upstream. Exports Meter with
		// provider awareness. CostType="hermes". Re-pollable.
		return hermes.New(), true
	case "fal":
		// G5: fal.ai governance + metering source (Apache). Read-only and minimal-
		// dependency (only the modelprovider HTTP client, with the fal "Key" auth scheme),
		// so it runs in-process (transport A). fal is API-key-only, pay-per-output, with no
		// public usage/audit API — so Gather governs by KEY LIFECYCLE (inventory + rotation
		// posture, the control point) and METERS cost around the queue API (the exported
		// Meter helper prices a completed result; configured request ids meter from queue
		// status) → CostSamples on the canonical path. Deep governance (SOC2/SSO/private
		// endpoints) is sales-gated and surfaced as an honest UNVERIFIED caveat. Offline
		// (no api_key) it is a no-op. cmd/fal-source exists for the collector mode.
		return fal.New(), true
	case "vertex":
		// Google Vertex AI catalog + usage + cost + Model Armor source (Apache). The
		// enterprise Google surface the gemini (AI Studio) connector does NOT cover. Read-only
		// and minimal-dependency (stdlib JWT-bearer mint + the modelprovider contract), so it
		// runs in-process (transport A) like the other model-provider connectors. Its Gather
		// emits per-model token usage from Cloud Monitoring (CostSample, cost derived from list
		// pricing, Gateway=vertex), opt-in billed cost from an operator-wired billing-export
		// result (GCP has no real-time cost API), and opt-in Model Armor safety_posture findings
		// (templates + floor settings); Snapshot exposes the Gemini + Claude-on-Vertex catalog.
		// IAM/audit-log activity is deferred to the gcp-audit connector. Offline (no credential)
		// Snapshot returns the declared catalog and Gather is a no-op. cmd/vertex-source exists
		// for the collector mode.
		return vertex.New(), true
	case "azure-openai":
		// Azure OpenAI / AI Foundry catalog + usage + cost source (Apache). The REAL
		// Azure surfaces (ARM Cognitive Services deployments/models, Azure Monitor token
		// metrics, Cost Management) — distinct from the openai connector's azure-openai mode,
		// which calls OpenAI-org paths absent on Azure. Read-only and minimal-dependency
		// (stdlib client-credentials mint + the modelprovider contract), so it runs in-process
		// (transport A). Its Gather emits per-deployment token usage (CostSample, derived cost,
		// Gateway=foundry) and opt-in billed cost (Cost Management, billed/estimated by calendar
		// finalization); Snapshot exposes the deployment+model catalog incl. Claude-on-Foundry.
		// Responsible-AI/content-filter posture is deferred to the azure-activity connector
		// (enable_rai). Offline (no credential) it is a no-op. cmd/azure-openai-source exists for
		// the collector mode.
		return azureopenai.New(), true
	case "openai":
		// Composition gap closed 2026-08-09 (release contrast F.1 order 4): OpenAI (platform) usage/cost + model/key catalog source (Apache). The package
		// existed and was complete since but was NEVER in this switch, so the product
		// could not select it: a Tier-2 provider present in the tree and absent from the
		// binary. Read-only and minimal-dependency (the modelprovider HTTP client), so it runs
		// in-process (transport A) like the other model-provider connectors. Distinct from the
		// azure-openai case above, which speaks the REAL Azure surfaces; this one speaks
		// OpenAI-org paths. Offline (no api_key) it is a catalog-only no-op.
		return openai.New(), true
	case "gemini":
		// Composition gap closed 2026-08-09 (release contrast F.1 order 4): Gemini (Google) API catalog + operator-wired usage export source (Apache).
		// Wired for the same reason as openai above: the package was complete and unreachable.
		// Distinct from the gemini-cli case, which observes LOCAL CLI settings/posture, and
		// from vertex, which speaks the Vertex AI surfaces. Offline (no api_key) it is a
		// catalog-only no-op.
		return gemini.New(), true
	case "local":
		// Composition gap closed 2026-08-09 (release contrast F.1 order 4): local/self-hosted inference (Ollama + vLLM) catalog and vLLM token usage
		// (Apache). The canon puts Ollama and self-hosted as ALWAYS PRESENT, and this was the
		// third package built and never composed. Read-only, no credential by default (Ollama
		// needs none on localhost), so it runs in-process. Offline (both URLs empty) it is a
		// no-op.
		return local.New(), true
	case "deepseek":
		// DeepSeek catalog + balance + sovereignty source (Apache). Read-only and
		// minimal-dependency (only the modelprovider HTTP client), so it runs in-process
		// (transport A) like the other model-provider connectors. Its Gather emits the hosted
		// PRC sovereignty posture plus GET /user/balance account availability; Snapshot exposes
		// the live/declarative DeepSeek catalog with v4 pricing and legacy retirement metadata.
		// Cost remains metered around the inference path via the exported Meter helper because
		// DeepSeek exposes no aggregate usage API. Offline (no api_key) it is a no-op.
		// cmd/deepseek-source exists for the collector mode.
		return deepseek.New(), true
	case "glm":
		// Zhipu GLM catalog + cost-metering + sovereignty source (Apache). PRC-nexus
		// (parent Entity-Listed); catalog-only + Meter like deepseek/mistral (no usage API),
		// sovereignty caveat on both z.ai and bigmodel.cn surfaces. Offline (no api_key) no-op.
		return glm.New(), true
	case "openrouter":
		// OpenRouter aggregation-gateway governance (Apache). Read-only,
		// minimal-dependency (only the modelprovider HTTP client), so it runs in-process
		// like the other model-provider connectors. Snapshot exposes the live catalog
		// (GET /api/v1/models) with per-token pricing converted to USD/MTok; Gather emits
		// account usage/limit posture (GET /api/v1/auth/key) and the approved-model policy
		// drift (denied-reachable / approved-missing) against the live catalog. Cost +
		// per-call policy verdict are produced by the exported MeterCall helper (OpenRouter
		// reports the billed cost). Offline (no api_key) it is a no-op.
		return openrouter.New(), true
	case "mistral":
		// Mistral AI / la Plateforme catalog + cost-metering source (Apache). Read-only
		// and minimal-dependency (only the modelprovider HTTP client), so it runs in-process
		// (transport A) like the other model-provider connectors. Mistral exposes NO public
		// usage/billing/spending-cap API (dashboard-only, primary-source verified) — so its
		// REAL surface is the live model catalog (GET /v1/models + declared list pricing,
		// Snapshot), Gather emits an honest "no public usage/billing API" coverage caveat plus
		// an OPT-IN, UNVERIFIED-OFFLINE org/workspace/key inventory + rotation posture (default
		// off, path-overridable, degrades 403/404), and cost is metered around the inference
		// path via the exported Meter helper (estimated from list pricing, like fal — there is
		// no usage API to pull). Offline (no api_key) it is a no-op. cmd/mistral-source exists
		// for the collector mode.
		return mistral.New(), true
	case "xai":
		// xAI / Grok governance + billing source (Apache). Read-only and
		// minimal-dependency (the modelprovider HTTP client over two planes), so it runs
		// in-process (transport A). Its Gather streams billed xAI CostSamples from the GET
		// Management billing endpoints (finalized invoices + current-cycle preview,
		// CostType="xai"), the API-key + ACL inventory's rotation + broad-ACL posture, and
		// credit-balance / spending-limit FinOps posture; Snapshot exposes the live Grok
		// catalog (GET /v1/language-models, prices derived from the API) or the declared set.
		// Management key (keys+billing) and inference key (catalog) are distinct credentials;
		// offline (no management key) Gather is a no-op. The POST usage-analytics endpoint is
		// intentionally NOT used (it would break the GET-only read-first guarantee; billed
		// invoices are the authoritative cost). cmd/xai-source exists for the collector mode.
		return xai.New(), true
	case "cohere":
		// E3: Cohere catalog + cost-metering source (Apache). Read-only and
		// minimal-dependency (only the modelprovider HTTP client), so it runs in-process
		// (transport A) like the other model-provider connectors. Cohere exposes NO public
		// usage/billing/org API (dashboard-only) — so Snapshot exposes the live model
		// catalog (GET /v1/models, cursor-paginated), Gather emits an honest coverage
		// caveat, and cost is metered around the inference path via the exported Meter
		// helper (estimated from list pricing, like mistral/fal). Offline (no api_key)
		// it is a no-op. A re-pollable batch source: set poll_seconds.
		return cohere.New(), true
	case "claude-projects":
		// Claude Projects governance source (Apache). Read-only inventory of
		// Organization Projects (name/membership/API keys) via the Admin API, with
		// operator-configurable policy (forbidden name patterns, archive-after-days,
		// member/key limits). Artifact lifecycle tracking derived from Compliance API
		// activity events. Minimal-dependency (modelprovider HTTP client), so it runs
		// in-process (transport A). Offline (no api_key) it is a no-op.
		return claudeprojects.New(), true
	case "claude-compliance":
		// CLA-06/FIN-05: Claude Compliance API Activity Feed evidence source
		// (Apache). Read-only and GET-only BY CONSTRUCTION (the shared GET-only
		// modelprovider client cannot perform the destructive content-DELETE the
		// Compliance API also exposes — that is HITL-gated, out of scope), and it
		// depends only on the modelprovider HTTP client, so it runs in-process
		// (transport A) exactly like claude-api. Its Gather paginates the feed and
		// emits one minimal-data FindingReport per activity record (actor PII folded
		// into a one-way hash, never surfaced — docs/SECURITY-HARDENING.md), which the engine appends
		// to the tamper-evident ledger and the SIEM export forwards. No secret in the
		// binary: the Activity-Feed Admin key (sk-ant-admin01- with
		// read:compliance_activities) travels by operator Config, never persisted.
		// Offline (no api_key) it is an honest no-op (no fabricated evidence).
		return claudecompliance.New(), true
	case "claude-apps-gateway":
		// E1: Claude apps gateway posture, inventory, and audit-event ingest source
		// (Apache). Reads an existing gateway.yaml, optional JSONL audit export, and
		// optional unauthenticated live probe endpoints. Emits minimal-data topology edges,
		// declared model grants, posture findings, audit findings, and event counters.
		return claudeappsgateway.New(), true
	case "claude-batch":
		// E3: Anthropic Message Batches + Files API governance source (Apache).
		// Read-only inventory of batches and file uploads (identifiers, status, counts,
		// timestamps — never payloads or file content, docs/SECURITY-HARDENING.md), operator-declared
		// batch policy enforcement (allowed models, line limits, allowed creators) and
		// upload retention-expiry signals. Minimal-dependency (modelprovider HTTP
		// client), so it runs in-process (transport A) exactly like claude-api. Offline
		// (no admin_key) it emits an honest offline finding. A re-pollable batch
		// source: set poll_seconds.
		return claudebatch.New(), true
	case "claude-routines":
		// E3: Claude Code Routines (scheduled triggers / cron agents) inventory
		// source (Apache). Read-only GETs against the Claude Code Remote API emitting
		// inventory edges + governance findings (excessive cadence, unreviewed
		// routines, anonymous triggers); prompt content is never stored, only hashed.
		// Minimal-dependency (the shared httpx client), in-process (transport A). It is
		// a STREAMING source: register it with poll_seconds=0 — it owns its own poll
		// cadence (refresh) and blocks in Gather running the refresh loop; the engine
		// never re-polls a streaming source. Offline (no api_key) Open fails closed.
		return clauderoutines.New(), true
	case "tak":
		// TAK Server posture + governed Cursor-on-Target (CoT) ingest source
		// (Apache). Stdlib-only, NO heavyweight dependency tree — posture reads a TAK
		// Server CoreConfig.xml offline (plus an optional mTLS version probe) and the
		// CoT ingest owns its own UDP/TCP listener sockets — so it runs IN-PROCESS
		// (transport A) like the other minimal-dependency observers. CoT is minimal-data:
		// positions and the free-form <detail> are digested and the emitting uid is
		// hashed before it leaves the connector; the feed is scopeable as source_type=
		// data (feed_ref). Offline (no server_url/config path and no listener) it is an
		// honest no-op. The CoT wire format is a clean-room implementation from the
		// public-release MITRE spec — it links no GPL TAK/ATAK code (connectors/tak/doc.go).
		return tak.New(), true
	case "a2a":
		// the A2A (Agent2Agent v1.0) OBSERVATION source (Apache). Read-only —
		// it discovers each configured agent's Card over HTTP, verifies the JWS/JCS
		// signature, and turns observed task/message interactions into agent↔agent
		// edges with a confidence reflecting the peer's verified trust. It never ACTS
		// on a peer (the connector invariant): a task is observed, never dispatched.
		// Minimal-dependency (net/http + stdlib crypto), so it runs in-process
		// (transport A) like the other observers. It requires at least one configured
		// agent or interaction — an empty config is honestly rejected at Open
		// ("nothing to observe"), a config-error contract, not a silent no-op boot.
		// Emitting SIGNED Agent Cards is a separate concern (connectors/a2a/issue.go E5), not this observe leg.
		return a2a.New(), true
	case "managed-settings":
		// Reads a small JSON file on disk and emits PERMITTED policy edges + drift
		// findings; cheap and low-dependency, so it runs in-process (CLA-05).
		return managedsettings.New(), true
	case "codex-managed-config":
		// G4 (C2): the Codex enforcement-posture sibling of managed-settings —
		// reads the host's system-tier requirements.toml + managed_config.toml (TOML),
		// emits the allowed MCP servers / egress domains as PERMITTED edges and reports
		// drift against the governance-authored Codex policy (constraints vs managed
		// defaults). Read-only, pure-stdlib+TOML, so it runs in-process (transport A)
		// exactly like managed-settings. The codex read-only governance source (kind
		// "codex": analytics/costs/compliance/audit) is a SEPARATE connector; this one is
		// the authoring+drift leg it lacked. cmd/codex-managed-config-source exists for
		// the out-of-process collector mode.
		return codexmanagedconfig.New(), true
	case "agents-md":
		// CUR-7: AGENTS.md/CLAUDE.md instruction-file integrity + injection
		// scanner. Walks a governed repo read-only, verifies the live tree against
		// the authored SHA-256 baseline (drift: altered/unbaselined/missing) and
		// scans content for instruction-injection/hidden-Unicode/secret threats —
		// minimal-data findings, never content. Pure stdlib filesystem batch
		// source, so it runs in-process (the managed-settings sibling).
		return agentsmd.New(), true
	case "mcpb":
		// CUR-7: Claude Desktop .mcpb extension governance. Inventories
		// unpacked installs / bundle shares, posture-scans manifests and reports
		// PERMITTED-vs-OBSERVED drift against the authored Enterprise allowlist
		// (incl. signature-presence under require_signed). Pure stdlib (archive/
		// zip) filesystem batch source, in-process like managed-settings.
		return mcpb.New(), true
	case "cowork-analytics":
		// the Claude Cowork engagement source (Apache). Read-only, GET-only, and
		// modelprovider-only (the Enterprise Analytics API), so it runs in-process
		// (transport A) exactly like claude-api/claude-compliance. Its Gather emits the
		// Cowork DAU/WAU/MAU + per-user activity engagement finding and a coverage
		// posture finding; Cowork COST flows via the OTEL cowork source, not here.
		// Offline (no api_key) it is an honest no-op. The OTLP-heavy runtime cowork
		// source runs OUT-OF-PROCESS (pluginBinaryForKind["cowork"]).
		return coworkanalytics.New(), true
	case "kerberos":
		//: tails KDC auth telemetry (Windows 4768/4769 or krb5kdc) and
		// emits the Kerberoasting finding. A streaming tail (poll_seconds=0).
		return kerberos.New(), true
	case "aaa":
		//: RADIUS/TACACS+ AAA observation — a log tail or the hardened
		// loopback RADIUS receiver (a streaming source; poll_seconds=0).
		return aaa.New(), true
	case "ssf":
		//: the SSF/CAEP receiver (agent kill-switch). A streaming inbound
		// receiver, loopback-default (poll_seconds=0).
		return ssf.New(), true
	case "edugain":
		//: verifies an eduGAIN/InCommon aggregate and emits the federation
		// posture finding. A re-pollable batch source (set poll_seconds, e.g. daily).
		return edugain.New(), true
	case "openidfed":
		//: resolves OpenID Federation 1.0 trust chains for configured
		// entities. A re-pollable batch source (set poll_seconds).
		return openidfed.New(), true

	// Data-platform R/RW observers: each parses a platform's
	// NATIVE audit/lineage EXPORT (a file the operator ships), classifies R/RW
	// VERBATIM and emits EdgeObservation on the contract. They are pure-stdlib
	// parsers (NO warehouse/cloud SDK, NO new dependency, never a DB connection), so
	// they run IN-PROCESS (transport A) exactly like the observers above — there
	// is no heavy dependency tree to isolate out-of-process. SAMPLING sources
	// (snowflake/oracle/redshift/bigquery/gcs/databricks-uc/iceberg-catalog: batch,
	// return nil at EOF) are re-run by the engine's re-poll scheduler — set
	// poll_seconds; TAIL sources (mssql/azure-blob/mongo/openlineage/delta-sharing:
	// follow=true) block in Gather until ctx is canceled (poll_seconds=0). Iceberg
	// emits the PERMITTED side (policy grants + vended-credential NHIs), not observed.
	case "snowflake-audit":
		return snowflakeaudit.New(), true
	case "databricks-uc":
		return databricksuc.New(), true
	case "bigquery-audit":
		return bigqueryaudit.New(), true
	case "mssql-audit":
		return mssqlaudit.New(), true
	case "oracle-audit":
		return oracleaudit.New(), true
	case "mongo-audit":
		return mongoaudit.New(), true
	case "redshift-audit":
		return redshiftaudit.New(), true
	case "gcs-audit":
		return gcsaudit.New(), true
	case "azure-blob-audit":
		return azureblobaudit.New(), true
	case "iceberg-catalog":
		return icebergcatalog.New(), true
	case "openlineage":
		return openlineage.New(), true
	case "delta-sharing":
		return deltasharing.New(), true

	// S165 cloud management-plane observers. Each is a live, read-only API
	// client of an org/tenant management plane — Resource Manager/IAM + Cloud Audit
	// Logs for GCP, Resource Graph + Azure Monitor Activity Log for Azure — with
	// hand-rolled stdlib OAuth2 (SA jwt-bearer / AAD client-credentials), NO cloud
	// SDK and no new dependency, so they run IN-PROCESS (transport A) like the live
	// entra-agent identity source. They complete the tri-cloud management-plane
	// parity with connectors/aws: org/folder/project + tenant/subscription topology
	// (signal "gcp"/"azure") and identity→{gcp,azure}.api control-plane activity
	// (signal "gcp_audit"/"azure_activity"). With no credential each is offline
	// (Gather emits nothing); they NEVER read a payload, secret or key (docs/SECURITY-HARDENING.md).
	case "gcp-audit":
		return gcpaudit.New(), true
	case "azure-activity":
		return azureactivity.New(), true
	case "cloudflare":
		// E3: Cloudflare edge-estate inventory source (Apache). Read-only
		// discovery of Workers, R2 buckets and Logpush jobs via the REST API v4
		// (Bearer, scoped read-only token) emitting topology edges — the edge sibling
		// of the S165 management-plane observers above. Pure stdlib HTTP, no cloud
		// SDK, so it runs IN-PROCESS (transport A). Distinct from the
		// cloudflare-ai-gateway and cloudflare-mcp-portals AI-surface connectors. A
		// re-pollable batch source: set poll_seconds.
		return cloudflare.New(), true
	case "bedrock-kb":
		// E3: Amazon Bedrock Knowledge Bases governance source (Apache).
		// Read-only observation of Bedrock KB retrieval via the Agent Runtime
		// Retrieve API (a health-check query — never RetrieveAndGenerate, which would
		// trigger billable inference): connectivity/retrieval posture findings per KB
		// plus KB→data-source topology edges. It does NOT store, index or embed
		// documents (Bedrock manages its own vector store) and never reads full
		// document content. Minimal-dependency (stdlib + the shared awssig SigV4
		// signer), in-process (transport A). A re-pollable batch source: set
		// poll_seconds.
		return bedrockkb.New(), true

	// Secrets/PKI/KMS observers. Each is a
	// pure-stdlib parser (cloud audit export, ESO/SOPS manifests) or a pure-Go
	// TTLV/TLS client (KMIP) — NO cloud SDK, no new dependency — so it runs
	// IN-PROCESS (transport A) like the data-platform observers. They emit OBSERVED
	// key/secret-access edges (cloud audit), provisioning edges (ESO/SOPS) or custody
	// edges (KMIP); they NEVER read a secret value or key material (docs/SECURITY-HARDENING.md-3). All
	// six are ALSO roster providers (buildRosterProvider) for the secret-store
	// inventory, so an operator may wire them as an identity entry with
	// as_source=true to get both the inventory and the edges.
	case "aws-kms":
		return awskms.New(), true
	case "gcp-kms":
		return gcpkms.New(), true
	case "azure-key-vault":
		return azurekeyvault.New(), true
	case "external-secrets":
		return externalsecrets.New(), true
	case "sops":
		return sops.New(), true
	case "kmip":
		return kmip.New(), true

	// Network/mesh/gateway L7 observers. Each is a
	// pure-stdlib/yaml parser of an exported artifact (Istio Telemetry CRDs, the K8s
	// Gateway API Inference Extension CRDs, the egress-proxy verdict log, the Envoy AI
	// Gateway usage records, Kong audit logs) — NO heavy dependency tree, NO live API,
	// NO listener — so they run IN-PROCESS (transport A) like the observers.
	// They emit L7 edges/findings (meshobs), permitted inference-routing edges
	// (SignalPolicy), CostSamples (AI Gateway → module XXI) and config-change findings.
	// The two proto-heavy connectors (envoy, hubble) run OUT-OF-PROCESS instead — see
	// pluginBinaryForKind. SAMPLING parsers are re-run by the engine's re-poll
	// scheduler (set poll_seconds); a tailing one blocks in Gather (poll_seconds=0).
	case "istio-telemetry":
		return istiotelemetry.New(), true
	case "inference-gateway":
		return inferencegateway.New(), true
	case "egress-proxy":
		return egressproxy.New(), true
	case "ai-gateway":
		return aigateway.New(), true
	case "kong-audit":
		return kongaudit.New(), true
	// AI-gateway config-posture family: read the customer gateway's DECLARED
	// config (not traffic) and emit posture + gateway-vs-Olivares policy drift. Each
	// is a pure-stdlib/yaml parser of an EXPORTED artifact, IN-PROCESS (transport A),
	// complementing the usage/audit siblings (ai-gateway, kong-audit).
	case "envoy-ai-gateway":
		return envoyaigw.New(), true
	case "kong-agent-gateway":
		return kongagw.New(), true
	case "litellm":
		return litellm.New(), true

	// IaC/GitOps read-first observers. Each parses an EXPORTED
	// CRD manifest (Argo CD Application, Flux Kustomization/HelmRelease/GitRepository,
	// Crossplane XRD) — a pure-stdlib/yaml parser with NO Kubernetes/cloud SDK and no
	// live API, so it runs IN-PROCESS (transport A) like the observers. They
	// OBSERVE the GitOps/IDP estate (sync/health/drift, reconciliation status, composite
	// API surface) and emit FindingReport posture; they never mutate the estate (acting
	// on a deployment is module VII/HITL-gated). SAMPLING sources (batch, return nil
	// at EOF) are re-run by the engine's re-poll scheduler — set poll_seconds.
	case "argocd":
		return argocd.New(), true
	case "flux":
		return flux.New(), true
	case "crossplane":
		return crossplane.New(), true

	// (FASE P / B1) — the R/RW access-map DIFFERENTIAL connectors (README.md module III): the moat made configurable in a stock `serve`. Each is a
	// pure-Go reader of a LOCAL artifact/stream — a PostgreSQL pgAudit log, an AWS
	// CloudTrail export, a Tetragon kernel-event stream, the host runtime, or an MCP
	// server's introspection — with NO heavy dependency tree (pgaudit/s3cloudtrail/
	// ebpf/runtime are pure-stdlib; mcp adds only go-jose, already linked by the MCP
	// gateway), so all run IN-PROCESS (transport A) exactly like the
	// observers above. The cmd/{pg-audit,s3-cloudtrail,ebpf-source} go-plugin mains stay
	// for the out-of-process collector mode (transport C). They emit EdgeObservation/
	// FindingReport the access-map module (the SOLE writer of AccessEdge) fuses into the
	// PERMITTED-vs-OBSERVED graph; honest coverage is per resource-kind tier (fusion.go)
	// and per attribution confidence — NEVER claimed firm where it is not: eBPF is
	// agent-anonymous (always `approximate`), and a shared pgAudit application_name / IAM
	// role collapses to `approximate` too (hardens this). Deny-closed: each is
	// opt-in (wired only when named in config), and pgAudit/CloudTrail/MCP hard-fail at
	// Open without their required log_path/path/servers — never a silent no-op; eBPF
	// defaults to reading stdin and runtime to procfs/k8s discovery, inert until pointed
	// at a real Tetragon export / host. DEPLOYMENT (docs-site reference/connectors): eBPF
	// consumes a Tetragon export written by a SEPARATE privileged DaemonSet (the connector
	// needs no kernel capability — it reads a 0600 file/FIFO), and runtime needs host
	// access (a GET-only docker.sock, the k8s ServiceAccount, procfs). Re-poll cadence: s3cloudtrail/runtime/
	// mcp are batch (set poll_seconds); a pgAudit csvlog is a batch and a jsonlog tails
	// (follow=true); eBPF is a streaming backstop that blocks in Gather (poll_seconds=0).
	// (FED-1): the Vault audit-log ingest — the OBSERVED counterpart of the
	// vault roster connector's SignalPolicy PERMITTED grants (same "entity:<name>"
	// ref space, same "vault.path" resource kind, so module III diffs them). A
	// pure-stdlib logtail JSON-lines parser of the file audit device (the pgaudit
	// jsonlog sibling), in-process. Batch by default (set poll_seconds); follow=true
	// tails and blocks in Gather (poll_seconds=0). It is a SOURCE, not a roster
	// provider — the roster half stays "vault" (buildRosterProvider).
	case "vault-audit":
		return vault.NewAudit(), true
	// (FED-1): the federation connectors whose Gather is a re-pollable BATCH
	// scan (1Password item-usage edges; entra-agent/agentcore long-lived-credential
	// drift findings; oasf badge findings) register here so a cfg.Sources entry
	// with poll_seconds re-runs them — the identity entry's as_source=true path
	// (rt.AddSource) runs a Gather ONCE per boot, which is wrong for an event feed
	// and stale for a drift scan. Wire the ROSTER half as an identity entry
	// WITHOUT as_source and the edges/findings half as a sources entry with
	// poll_seconds; never both for the same kind (Descriptor names are unique —
	// the second registration would fail as a duplicate).
	case "onepassword":
		return onepassword.New(), true
	case "entra-agent":
		return entraagent.New(), true
	case "agent365":
		return agent365.New(), true
	case "google-agent":
		return googleagent.New(), true
	case "google-adk":
		// governance of agents BUILT WITH Google's ADK 2.0 framework (Apache).
		// Read-only: reads exported ADK Session JSON (agent/app inventory, sub-agents,
		// users, tool function-calls, transfers, state/error counts — never message
		// content), an approved-tool policy (drift), execution tracking, and Vertex
		// reasoningEngine correlation. Distinct from google-agent (the Agent Platform
		// surface). Offline (no session_dir) it is a no-op.
		return googleadk.New(), true
	case "foundry-agents":
		return foundryagents.New(), true
	case "agentcore":
		return agentcore.New(), true
	case "oasf":
		return oasf.New(), true
	// the identity sources whose Gather is now a re-pollable BATCH
	// permitted-grant scan (ldap privileged-directory grants; idp Okta/Entra
	// app/scope assignment grants; infisical project grants — vault, above, was
	// always live) register here for the same reason as the kinds: a
	// cfg.Sources entry with poll_seconds re-runs the scan, while the identity
	// entry's as_source=true runs it ONCE per boot. okta/entra resolve to the
	// SAME idp connector (Descriptor olivares.idp), so only one idp-family
	// instance can register as a source per process (the
	// one-instance-per-kind limit).
	case "ldap":
		return ldap.New(), true
	case "idp", "okta", "entra":
		return idp.New(), true
	case "infisical":
		return infisical.New(), true
	// GitHub/GitLab source connectors — observe code repositories as data
	// sources for coding agents, emitting R/RW access edges (observed) and ACL
	// edges (permitted) to the access map. Webhook-first with API polling for
	// reconciliation. Streaming sources (poll_seconds=0): Gather blocks running
	// the webhook HTTP receiver and the API poller until ctx is canceled.
	case "github":
		return githubsrc.New(), true
	case "gitlab":
		return gitlabsrc.New(), true

	case "pgaudit", "pg-audit":
		return pgaudit.New(), true
	case "s3cloudtrail", "s3-cloudtrail":
		return s3cloudtrail.New(), true
	case "ebpf":
		return ebpf.New(), true
	case "runtime":
		return runtimesource.New(), true
	case "mcp":
		return mcpc.New(), true
	default:
		// Build-tag-gated commercial connectors (e.g. CyberArk Conjur). The
		// default (AGPL) build resolves none (enterpriseInProcSource returns
		// (nil,false) in wire_noenterprise.go); `-tags enterprise` wires them.
		return enterpriseInProcSource(kind)
	}
}

// buildRosterProvider constructs an identity connector by kind, returning it both
// as the roster GraphProvider (the snapshot half governance reconciles) and as the
// SourceConnector (so it can be Opened, and wired as a source). Since every
// Identity source with a grant surface has a live Gather emitting its
// identity→resource permitted grants as SignalPolicy edges — vault ACL paths,
// ldap privileged-directory grants, idp app/scope assignments, infisical project
// grants — so AsSource produces edges for all of them (one-shot per boot; for
// periodic re-scans wire a sources entry with poll_seconds, buildInProcSource).
// spiffe and keycloak remain roster-only (their Gather is a no-op).
// rosterProviderForKind maps a directory roster kind to the keycloak connector's
// `provider` setting. The connector is one multi-provider directory reader behind a
// provider switch; an operator may register it under the intuitive kind (pingone /
// forgerock) and we seed the matching provider so the kind and the backend can never
// diverge. "" for kinds whose connector reads no provider field.
func rosterProviderForKind(kind string) string {
	switch kind {
	case "pingone", "ping":
		return "pingone"
	case "forgerock":
		return "forgerock"
	case "keycloak":
		return "keycloak"
	default:
		return ""
	}
}

// rosterSettings returns the spec's settings, defaulting `provider` from the kind
// alias when the operator omitted it — so kind=pingone/forgerock can never silently
// run the default keycloak backend. It never overrides an explicit provider and
// returns the original map untouched when no defaulting applies.
func rosterSettings(kind string, settings map[string]string) map[string]string {
	p := rosterProviderForKind(kind)
	if p == "" || strings.TrimSpace(settings["provider"]) != "" {
		return settings
	}
	cp := make(map[string]string, len(settings)+1)
	for k, v := range settings {
		cp[k] = v
	}
	cp["provider"] = p
	return cp
}

func buildRosterProvider(kind string) (identitysource.GraphProvider, sdk.SourceConnector, bool) {
	switch kind {
	case "ldap":
		c := ldap.New()
		return c, c, true
	case "idp", "okta", "entra":
		c := idp.New()
		return c, c, true
	case "keycloak", "pingone", "ping", "forgerock":
		//: the self-hosted & cloud directory connector behind one
		// provider switch (keycloak | pingone | forgerock), on the same
		// identitysource.GraphProvider contract. The kind is an alias for resolution;
		// the connector reads config.provider (default keycloak) to choose the backend,
		// so a pingone/forgerock entry MUST set provider accordingly.
		c := keycloak.New()
		return c, c, true
	case "vault":
		c := vault.New()
		return c, c, true
	case "infisical":
		c := infisical.New()
		return c, c, true
	case "spiffe":
		c := spiffe.New()
		return c, c, true
	case "claude-console":
		// CLA-13/IDN-02: governs Claude's OWN org IAM. Unlike the other roster
		// providers its Gather is NOT a no-op — it emits the SSO/SCIM blind-spot
		// finding — so set the identity entry's as_source=true to also wire it as a
		// source and route that finding to the ledger.
		c := claudeconsole.New()
		return c, c, true
	case "claude-wif":
		// CLA-12/IDN-01: Claude identity (NHI & WIF) roster provider (Apache).
		// Snapshot models the Anthropic NHI roster — api keys, workspaces, service
		// accounts (svac_), federation issuers/rules (fdis_/fdrl_) — converging by
		// external_id (the raw Anthropic id) so module III can diff PERMITTED-vs-OBSERVED.
		// Like claude-console its Gather is NOT a no-op: it emits the PERMITTED scope
		// edges (svac_/apikey_ → workspace, Source=policy) AND the WIF static-key footgun
		// finding (a static ANTHROPIC_API_KEY — even =="" — silently shadows federation;
		// High). So set the identity entry's as_source=true to also wire it as a source
		// and route those edges/findings to the ledger. Low-dependency (only the
		// modelprovider HTTP client), so it runs in-process here; cmd/claude-wif-source
		// exists for the out-of-process collector mode. With an org:admin OAuth token
		// (org_admin_oauth_token) it LISTS the org's live federation (service accounts/
		// issuers/rules) and reconciles it against the declared rules — drift edges/
		// findings to the ledger, the live graph to the console; without it, it models
		// only the operator-declared federation, an honest absence, never an invented
		// roster. The credential-emitting WIF Exchanger is a SEPARATE primitive the host
		// wires explicitly (claudewif.NewExchanger), never part of the Gather plane.
		c := claudewif.New()
		return c, c, true

	// (FED-1) — the hyperscaler agent-identity registries federated against
	// the plane's SPIFFE/WIF roster, plus the control-tower/descriptor/secret
	// sources. All read-only (federation never writes to a registry; export to the
	// towers is). The three hyperscaler connectors stamp the dedicated
	// per-agent kinds (agent_identity/workload_identity) the access-map attribution
	// axis treats as FIRM; google-agent rows use the full SPIFFE ID as Ref so
	// they converge with the spiffe roster by external_id. entra-agent's and
	// agentcore's Gather is NOT a no-op (the nhi_longlived_credential drift-class
	// findings — Five Eyes 2026-05), agent365's emits registry-hygiene findings,
	// foundry-agents emits ARM-derived application posture findings, google-agent
	// emits registry/gateway posture findings, oasf's emits the badge findings,
	// and onepassword's streams the item-usage secret-access edges — those seven
	// are re-pollable BATCH scans, so wire their
	// edges/findings half as a cfg.Sources entry with poll_seconds
	// (buildInProcSource above), NOT via as_source=true
	// (which runs Gather once per boot, and a second registration of the same
	// kind would collide on the unique Descriptor name).
	// ai-control-tower Gather is a no-op (roster only).
	case "entra-agent":
		c := entraagent.New()
		return c, c, true
	case "agentcore":
		c := agentcore.New()
		return c, c, true
	case "google-agent":
		c := googleagent.New()
		return c, c, true
	case "oasf":
		c := oasf.New()
		return c, c, true
	case "agent365":
		c := agent365.New()
		return c, c, true
	case "foundry-agents":
		c := foundryagents.New()
		return c, c, true
	case "ai-control-tower":
		c := aicontroltower.New()
		return c, c, true
	case "onepassword":
		c := onepassword.New()
		return c, c, true

	// Secret-store inventory providers: each exposes the
	// secret-manager custodians it sees as secret_store NHIs that converge by
	// external_id into the unified roster (GET /governance/identities?kind=secret_store).
	// Their Gather is NOT a no-op — it emits the key/secret-access, provisioning or
	// custody edges — so set as_source=true on the identity entry to also wire the
	// edges. Honest limit (existence vs use): a connector reading only a management
	// view (KMIP Locate, ESO/SOPS manifests) yields the store's EXISTENCE; only a
	// connected audit trail (cloud audit) yields who USES it.
	case "aws-kms":
		c := awskms.New()
		return c, c, true
	case "gcp-kms":
		c := gcpkms.New()
		return c, c, true
	case "azure-key-vault":
		c := azurekeyvault.New()
		return c, c, true
	case "external-secrets":
		c := externalsecrets.New()
		return c, c, true
	case "sops":
		c := sops.New()
		return c, c, true
	case "kmip":
		c := kmip.New()
		return c, c, true
	default:
		// Build-tag-gated commercial roster providers (e.g. CyberArk Conjur):
		// hosts as NHI + the host→variable permitted grants. The default (AGPL) build
		// resolves none (enterpriseRosterProvider returns (nil,nil,false) in
		// wire_noenterprise.go); `-tags enterprise` wires them.
		return enterpriseRosterProvider(kind)
	}
}

// buildContentSource constructs a knowledge DOCUMENT source by kind: the
// gdrive/confluence/notion/sharepoint/s3content/sap_odata/salesforce/snowflake/
// azure_ai_search connectors. Unlike buildInProcSource these implement
// contentsource.Source, NOT sdk.SourceConnector — they emit no bus observation and
// produce no R/RW edge. The knowledge module (VIII) drives their List/Fetch on an
// ingest request (knowledge.WithSource), so they are NEVER registered with the
// runtime/scheduler; this registry only maps kind→connector for the composition root.
// Each is read-only and minimal-data (it carries ACL/provenance, and the module —
// not the connector — redacts the body before persisting). ok=false for an unknown
// kind so the caller WARNS rather than silently dropping a configured source.
func buildContentSource(kind string, richDoc contentsource.RichDocExtractor) (contentsource.Source, bool) {
	switch kind {
	case "gdrive":
		return gdrive.New(), true
	case "confluence":
		return confluence.New(), true
	case "notion":
		return notion.New(), true
	case "sharepoint":
		return sharepoint.New(), true
	case "s3content":
		return s3content.New(), true
	case "sap_odata":
		return sapodata.New(), true
	case "salesforce":
		return salesforce.New(), true
	case "snowflake":
		return snowflakecontent.New(), true
	case "azure_ai_search":
		return azureaisearch.New(), true
	case "postgres", "pgcontent":
		// the operational-database content source. Materializes PostgreSQL rows
		// as governed knowledge documents (read-only by construction, declared per-row
		// ACL, per-column classification) — distinct from the pgaudit R/RW access
		// observer, and NOT NL-to-SQL. Like the other content sources it emits no bus
		// observation; the knowledge module drives its List/Fetch.
		return pgcontent.New(), true
	case "filesystem", "fscontent":
		// the file-server content source. Ingests a directory tree (local or an
		// NFS/SMB mount) as governed documents — read confined to the root by
		// construction (symlink-escape/traversal refused via os.Root), POSIX owner/
		// group/ACL mapped to Document ACLs, xattr classification. Distinct from the
		// filelog log SINK; the knowledge module drives its List/Fetch.
		//
		// inject the sandboxed rich-document extractor so DOCX/PPTX/XLSX files
		// are ingested as text (extracted out-of-process under plugin confinement).
		// A nil extractor (tests / a build without it) simply falls back to the
		// text-only walk — rich documents are then a counted skip, never a failure.
		return fscontent.New(fscontent.WithExtractor(richDoc)), true
	default:
		return nil, false
	}
}

type contentSourceMode interface {
	Mode() string
}

type modeContentSource struct {
	contentsource.Source
	mode string
}

func (s modeContentSource) Mode() string { return normalizeContentSourceMode(s.mode) }

// ListingComplete and ListPage forward the wrapped source's completeness / bounded-pagination
// capabilities (the wrapper embeds the Source INTERFACE, which erases the concrete type, so
// each capability must be re-exposed explicitly — as Mode is). WITHOUT ListPage re-exposed,
// the knowledge host's contentsource.PagedSource assertion fails for every mode-wrapped
// external plugin and the F5 bounded-wire + per-page completeness signal never engages —
// silently orphan-deleting a truncated tail. A wrapped source that does not implement the
// capability is reported complete (its listing is authoritative).
func (s modeContentSource) ListingComplete() bool { return wrappedListingComplete(s.Source) }
func (s modeContentSource) ListPage(ctx context.Context, cursor string, maxItems, maxBytes int) ([]contentsource.DocRef, string, bool, error) {
	return wrappedListPage(ctx, s.Source, cursor, maxItems, maxBytes)
}

type modeLiveContentSource struct {
	contentsource.LiveSource
	mode string
}

func (s modeLiveContentSource) Mode() string          { return normalizeContentSourceMode(s.mode) }
func (s modeLiveContentSource) ListingComplete() bool { return wrappedListingComplete(s.LiveSource) }
func (s modeLiveContentSource) ListPage(ctx context.Context, cursor string, maxItems, maxBytes int) ([]contentsource.DocRef, string, bool, error) {
	return wrappedListPage(ctx, s.LiveSource, cursor, maxItems, maxBytes)
}

var (
	_ contentsource.PagedSource = modeContentSource{}
	_ contentsource.PagedSource = modeLiveContentSource{}
)

// wrappedListingComplete reports the wrapped source's listing completeness, defaulting
// to complete when the source does not implement the capability.
func wrappedListingComplete(src contentsource.Source) bool {
	if r, ok := src.(contentsource.CompletenessReporter); ok {
		return r.ListingComplete()
	}
	return true
}

// wrappedListPage forwards to the wrapped source's bounded pagination when present, else the
// (already-bounded) List reported complete — so the mode wrapper never erases the F5 capability.
func wrappedListPage(ctx context.Context, src contentsource.Source, cursor string, maxItems, maxBytes int) ([]contentsource.DocRef, string, bool, error) {
	if paged, ok := src.(contentsource.PagedSource); ok {
		return paged.ListPage(ctx, cursor, maxItems, maxBytes)
	}
	refs, next, err := src.List(ctx, cursor)
	return refs, next, true, err
}

func wrapContentSourceMode(src contentsource.Source, mode string) contentsource.Source {
	mode = normalizeContentSourceMode(mode)
	if live, ok := src.(contentsource.LiveSource); ok {
		return modeLiveContentSource{LiveSource: live, mode: mode}
	}
	return modeContentSource{Source: src, mode: mode}
}

func normalizeContentSourceMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "live":
		return "live"
	default:
		return "export"
	}
}

func sourceModeFromConfig(cfg map[string]string) string {
	if cfg == nil {
		return "export"
	}
	return normalizeContentSourceMode(cfg["mode"])
}

// knowledgeContentOptions resolves the configured document sources (cfg.Documents)
// into the knowledge.WithSource options the composition root applies to module VIII.
// It is called from buildModules (wire.go) BEFORE the knowledge module is constructed
// — that module owns the document-source lifecycle, so this is where they are wired,
// the symmetric counterpart to wireSources for observation sources. Deny-closed and
// honest (12 §5): with no documents configured the module simply has no pull sources
// (and rejects an ingest naming an unknown one); an unknown kind or a nameless entry
// WARNS rather than silently no-op'ing. Secrets travel by Config reference, resolved at
// the connector's Open, never persisted by the engine (docs/SECURITY-HARDENING.md).
func knowledgeContentSources(cfg sourcesConfig, log *slog.Logger) []pendingContentSource {
	var pending []pendingContentSource
	// One sandboxed extractor is shared across all document sources (it is stateless —
	// each extraction spawns a fresh confined subprocess). Only the filesystem source
	// uses it today; other sources return already-extracted text from their API.
	richDoc := newSandboxedRichDocExtractor(log)
	for _, d := range cfg.Documents {
		name := strings.TrimSpace(d.Name)
		if name == "" {
			log.Warn("knowledge: document source has no name; not wired", "kind", d.Kind)
			continue
		}
		if d.Plugin != nil {
			digest, refusal := admitExternalPlugin(*d.Plugin, cfg.ConnectorTrust)
			if refusal != "" {
				log.Warn("knowledge: external content-source plugin refused (deny-closed); source NOT wired", "name", name, "reason", refusal)
				continue
			}
			pending = append(pending, pendingContentSource{name: name, kind: "plugin", plugin: d.Plugin, digest: digest, cfg: sdk.Config{Settings: d.Config}})
			continue
		}
		src, ok := buildContentSource(d.Kind, richDoc)
		if !ok {
			log.Warn("knowledge: unknown or unsupported document source kind; not wired", "name", name, "kind", d.Kind)
			continue
		}
		src = wrapContentSourceMode(src, d.Config["mode"])
		// Defer: boot resolves this source's secret references (a credential_ref) and
		// OPENS it once the store exists (deferredSecretWiring.openAll) — the
		// source must be opened before it can List/Fetch (ingest.go documents that
		// contract), but its `store:` references can only resolve post-store. Only a
		// source that opens successfully is then registered on the module (AddSource),
		// preserving the "only openable sources are wired" contract.
		pending = append(pending, pendingContentSource{name: name, kind: d.Kind, src: src, cfg: sdk.Config{Settings: d.Config}})
	}
	return pending
}

// wireSources registers the configured observation sources with rt as a PRODUCTION
// caller (CB-1). An EXTERNAL plugin source (s.Plugin, S142) goes through the
// deny-closed admission gate first — signature verified against ConnectorTrust,
// digest pinned at exec — and is refused with a WARN otherwise; a plugin-kind
// source is extracted from the embedded set and loaded out-of-process (AutoMTLS);
// an in-process-kind source is added directly with its re-poll interval. Every
// un-wireable source WARNS (refused admission, unknown kind, not embedded, load
// error) — never a silent no-op (12 §5). It is shared by `serve` (sinks go to
// the local bus) and `collector` (sinks push to a remote core): the transport
// differs only in the runtime's SinkFactory. embedDir is a private scratch dir for
// extracted plugin binaries.
func wireSources(ctx context.Context, rt *runtime.Runtime, cfg sourcesConfig, embedDir string, resolver *secret.Resolver, log *slog.Logger) {
	if len(cfg.Sources) == 0 {
		// Honest posture (12 §5): a boot with no observation sources is a visible
		// state, not a silent no-op — the estate would run on no live traffic. The
		// symmetric roster half warns the same way (wireRoster).
		log.Warn("ingest: no observation sources configured (OLIVARES_SOURCES_CONFIG.sources is empty); no connector will ingest — the estate runs on no live traffic")
	}
	for _, s := range cfg.Sources {
		if s.Tenant == "" {
			log.Warn("ingest: source has no tenant; not wired", "name", s.Name, "kind", s.Kind)
			continue
		}
		rawCfg := sdk.Config{Settings: s.Config}
		if s.Plugin != nil {
			// S142: EXTERNAL (third-party) connector plugin. Admission is
			// deny-closed and PURE (externalplugins.go): operator-pinned digest +
			// verified Sigstore/DSSE attestation against ConnectorTrust, or the
			// source is NOT wired — there is no observe mode and no allow-unsigned
			// escape hatch. The refusal string is the single source of truth for
			// the WARN; the runtime then re-pins the digest at exec via go-plugin
			// SecureConfig (the verified bytes are the executed bytes).
			digest, refusal := admitExternalPlugin(*s.Plugin, cfg.ConnectorTrust)
			if refusal != "" {
				log.Warn("ingest: external connector plugin refused (deny-closed); source NOT wired", "name", s.Name, "reason", refusal)
				continue
			}
			// resolve secret references with no descriptor (the plugin
			// self-describes out-of-process, so the strict no-inline-secret check
			// cannot run here) — references still resolve to live values.
			scfg, rerr := resolveConfig(ctx, resolver, sdk.Descriptor{}, rawCfg)
			if rerr != nil {
				log.Warn("ingest: external connector plugin secret reference could not be resolved; source NOT wired", "name", s.Name)
				continue
			}
			if err := rt.LoadSourcePluginVerified(s.Plugin.Path, scfg, s.Tenant, digest); err != nil {
				log.Warn("ingest: failed to load external connector plugin; source not wired", "name", s.Name, "error", err)
				continue
			}
			log.Info("ingest: wired EXTERNAL source (signature verified, checksum-pinned, out-of-process AutoMTLS)", "name", s.Name, "tenant", s.Tenant, "digest", digest)
			continue
		}
		if bin, isPlugin := pluginBinaryForKind[s.Kind]; isPlugin {
			path, err := firstparty.Extract(embedDir, bin)
			if err != nil {
				log.Warn("ingest: first-party connector not embedded in this build; source NOT wired (build it with `task build:connectors`, or run it from a collector). It will not ingest.",
					"name", s.Name, "kind", s.Kind, "binary", bin)
				continue
			}
			// resolve references; an out-of-process plugin has no in-process
			// descriptor, so the strict check is skipped (references still resolve).
			scfg, rerr := resolveConfig(ctx, resolver, sdk.Descriptor{}, rawCfg)
			if rerr != nil {
				log.Warn("ingest: connector secret reference could not be resolved; source NOT wired", "name", s.Name, "kind", s.Kind)
				continue
			}
			if err := rt.LoadSourcePlugin(path, scfg, s.Tenant); err != nil {
				log.Warn("ingest: failed to load connector plugin; source not wired", "name", s.Name, "kind", s.Kind, "error", err)
				continue
			}
			log.Info("ingest: wired source (out-of-process plugin, AutoMTLS)", "name", s.Name, "kind", s.Kind, "tenant", s.Tenant)
			continue
		}
		conn, ok := buildInProcSource(s.Kind)
		if !ok {
			log.Warn("ingest: unknown or unsupported source kind; not wired", "name", s.Name, "kind", s.Kind)
			continue
		}
		// resolve references and enforce the strict no-inline-secret rule on
		// the connector's declared secret fields. An unresolvable secret fails the
		// source closed (it is not wired) rather than running it half-configured.
		scfg, rerr := resolveConfig(ctx, resolver, conn.Descriptor(), rawCfg)
		if rerr != nil {
			// Never log rerr: a backend resolver error can embed a response-body
			// excerpt that carries credential material (the wireRoster/notify rule).
			log.Warn("ingest: source secret reference could not be resolved; not wired", "name", s.Name, "kind", s.Kind)
			continue
		}
		if err := rt.AddPollSource(conn, scfg, s.Tenant, time.Duration(s.PollSeconds)*time.Second); err != nil {
			log.Warn("ingest: failed to register in-process source; not wired", "name", s.Name, "kind", s.Kind, "error", err)
			continue
		}
		log.Info("ingest: wired source (in-process fast-path)", "name", s.Name, "kind", s.Kind, "tenant", s.Tenant, "poll_seconds", s.PollSeconds)
	}
}

// wireRoster builds the configured identity GraphProviders, hands them to
// governance via UseRosterProviders, and schedules the periodic SyncRoster on the
// runtime's scheduler — closing IDN-06/CB-3 (the NHI roster stops being empty in
// the binary). It Opens each provider here (the GraphProvider seam has no Open;
// Snapshot needs the resolved config) and, when AsSource is set, also wires a
// SEPARATE instance as a source so the connector's permitted-access edges flow —
// since that is every identity connector with a grant surface
// (vault/ldap/idp/infisical, one-shot per boot; see identitySpec.AsSource), no
// longer Vault alone. Honest posture: with no providers configured, or all
// uncredentialed, it WARNS (the roster sync is then a visible no-op, not a silent
// one). It must be called before rt.Start. ctx bounds the Open calls.
func wireRoster(ctx context.Context, rt *runtime.Runtime, gov *governance.Module, wif *wifGraphAdapter, cfg sourcesConfig, resolver *secret.Resolver, log *slog.Logger) {
	var bindings []governance.RosterBinding
	// the idp connector's Entra path DEFERS agent identities
	// (servicePrincipalType "ServiceIdentity") to the dedicated entra-agent
	// connector, so the converged rows' Provider never flaps. An estate wiring
	// Entra via idp WITHOUT entra-agent therefore stops maintaining previously
	// rostered agent-identity rows — warn loudly (the per-sync audit also carries
	// the deferred count; docs/SECURITY-HARDENING.md never-silent-gap).
	var hasEntraIdp, hasEntraAgent bool
	for _, spec := range cfg.Identity {
		switch {
		case spec.Kind == "entra-agent":
			hasEntraAgent = true
		case spec.Kind == "entra", spec.Kind == "idp" && spec.Config["provider"] == "entra":
			hasEntraIdp = true
		}
	}
	if hasEntraIdp && !hasEntraAgent {
		log.Warn("roster: an Entra directory provider (idp) is wired without the entra-agent connector; Entra Agent ID agent identities are deferred by idp and will NOT be rostered/maintained — wire an entra-agent identity entry to govern them")
	}
	for _, spec := range cfg.Identity {
		if spec.Tenant == "" {
			log.Warn("roster: identity provider has no tenant; not wired", "name", spec.Name, "kind", spec.Kind)
			continue
		}
		provider, conn, ok := buildRosterProvider(spec.Kind)
		if !ok {
			log.Warn("roster: unknown identity connector kind; not wired", "name", spec.Name, "kind", spec.Kind)
			continue
		}
		// Open validates config (no network I/O); a configuration error here means
		// the provider could never snapshot, so skip it. Never log the error: a
		// connector's Open error can embed the configured endpoint/credential.
		// rosterSettings defaults the directory connector's `provider` from the kind
		// alias (kind=pingone/forgerock), so the registration kind and the connector
		// backend can never silently diverge.
		settings := rosterSettings(spec.Kind, spec.Config)
		// resolve the directory/credential references to live values and enforce
		// the strict no-inline-secret rule. An unresolvable secret fails the provider
		// closed (not wired) rather than snapshotting half-configured.
		resolved, rerr := resolveConfig(ctx, resolver, conn.Descriptor(), sdk.Config{Settings: settings})
		if rerr != nil {
			// Never log rerr: a backend resolver error can embed a response-body
			// excerpt that carries credential material (the conn.Open rule below).
			log.Warn("roster: identity provider secret reference could not be resolved; not wired", "name", spec.Name, "kind", spec.Kind)
			continue
		}
		if err := conn.Open(ctx, resolved); err != nil {
			log.Warn("roster: identity provider failed to open (configuration error); not wired", "name", spec.Name, "kind", spec.Kind)
			continue
		}
		bindings = append(bindings, governance.RosterBinding{Provider: provider, TenantRef: spec.Tenant})
		// E: a claude-wif source carries the operator-declared WIF object graph
		// (issuers/rules/service-accounts + footgun). Capture it so the identity console
		// (GET /v1/m/identity/wif) serves the DECLARED graph for this tenant.
		if wif != nil && spec.Kind == "claude-wif" {
			if src, ok := conn.(*claudewif.Source); ok {
				if t, present, terr := parseBusinessTenant("roster source: tenant", spec.Tenant); terr == nil && present {
					wif.add(t, src)
				}
			}
		}
		log.Info("roster: wired identity provider", "name", spec.Name, "kind", spec.Kind, "tenant", spec.Tenant)

		if spec.AsSource {
			if _, srcConn, _ := buildRosterProvider(spec.Kind); srcConn != nil {
				// Reuse the already-resolved config: the second instance is the same
				// connector kind, so its declared secret fields and references match.
				if err := rt.AddSource(srcConn, resolved, spec.Tenant); err != nil {
					log.Warn("roster: identity provider could not also be wired as a source; roster still active", "name", spec.Name, "kind", spec.Kind, "error", err)
				} else {
					log.Info("roster: also wired identity provider as a permitted-access source", "name", spec.Name, "kind", spec.Kind, "tenant", spec.Tenant)
				}
			}
		}
	}

	if len(bindings) == 0 {
		// /roster/sync is a no-op that answers; nothing refuses.
		log.Info("roster: no identity providers configured (OLIVARES_SOURCES_CONFIG.identity is empty); the NHI roster stays empty and /roster/sync is a no-op. Configure ldap/idp/vault/infisical/spiffe — or the agent registries entra-agent/agentcore/google-agent — to populate it.")
		return
	}
	gov.UseRosterProviders(bindings)
	interval := cfg.rosterSyncInterval()
	if err := rt.SchedulePeriodic("governance.roster.sync", interval, true, gov.Sync); err != nil {
		log.Warn("roster: failed to schedule periodic SyncRoster; roster will only sync on demand via POST /roster/sync", "error", err)
		return
	}
	log.Info("roster: scheduled periodic SyncRoster", "providers", len(bindings), "interval", interval.String())
}
