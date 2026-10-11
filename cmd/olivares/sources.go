// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
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
	"github.com/olivaresai/olivares/connectors/aws"
	"github.com/olivaresai/olivares/connectors/awskms"
	azureactivity "github.com/olivaresai/olivares/connectors/azure-activity"
	azureblobaudit "github.com/olivaresai/olivares/connectors/azure-blob-audit"
	azureopenai "github.com/olivaresai/olivares/connectors/azure-openai"
	"github.com/olivaresai/olivares/connectors/azureaisearch"
	"github.com/olivaresai/olivares/connectors/azurekeyvault"
	"github.com/olivaresai/olivares/connectors/bedrock"
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
	cfaigateway "github.com/olivaresai/olivares/connectors/cloudflare-ai-gateway"
	cfmcpportals "github.com/olivaresai/olivares/connectors/cloudflare-mcp-portals"
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
	gitbinding "github.com/olivaresai/olivares/connectors/gitbinding"
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
	"github.com/olivaresai/olivares/connectors/mysqlaudit"
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
	"github.com/olivaresai/olivares/connectors/paperclip"
	"github.com/olivaresai/olivares/connectors/pgaudit"
	"github.com/olivaresai/olivares/connectors/pgcontent"
	redshiftaudit "github.com/olivaresai/olivares/connectors/redshift-audit"
	runtimesource "github.com/olivaresai/olivares/connectors/runtime"
	"github.com/olivaresai/olivares/connectors/s3cloudtrail"
	"github.com/olivaresai/olivares/connectors/s3content"
	"github.com/olivaresai/olivares/connectors/salesforce"
	"github.com/olivaresai/olivares/connectors/sapodata"
	"github.com/olivaresai/olivares/connectors/servicenow"
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
// Transports (CB-1, decided = option C). The process mode is fixed per kind, and
// docs-site reference/connectors.md "Process mode per kind" names it:
//   - (B) out-of-process plugin, AutoMTLS — exactly the kinds in pluginBinaryForKind
//     (outputPluginForKind for destinations), whose dependency trees must never link
//     into the core, plus every external (operator-admitted) plugin. The binary is
//     embedded (firstparty) and launched as a confined subprocess; the runtime
//     restarts a source or destination plugin whose process dies.
//   - (A) in-process — every other first-party kind (inProcSourceFactories), linked
//     into the engine binary. The release does not build its connectors/<kind>/cmd
//     plugin program; that program exists to run it as an external plugin.
//   - (C) remote collector push — the same source wiring runs in `collector` mode
//     with a push Sink (collector.go); resolved through the SAME wireSources.

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
	// periodic re-scans wire a sources entry with poll_seconds instead.
	//
	// The source is registered under THIS ENTRY'S NAME, not under the connector's
	// descriptor, so two entries of one kind are two sources: okta and entra both
	// resolve to the one idp connector (Descriptor olivares.idp) and can now BOTH
	// be wired as sources, each with its own configuration and lifecycle. What is
	// still refused is a duplicate NAME — including a sources[] entry that already
	// claimed it — and the refusal leaves the first registration untouched.
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
	path := osGetenv("OLIVARES_SOURCES_CONFIG")
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

// inProcSourceFactories constructs fresh observation sources and supplies the
// canonical kinds offered by the connector catalog. Constructors do no I/O.
var inProcSourceFactories = map[string]func() sdk.SourceConnector{
	"cloudflare-mcp-portals": func() sdk.SourceConnector { return cfmcpportals.New() },

	"a2a":                   func() sdk.SourceConnector { return a2a.New() },
	"aaa":                   func() sdk.SourceConnector { return aaa.New() },
	"agent365":              func() sdk.SourceConnector { return agent365.New() },
	"agentcore":             func() sdk.SourceConnector { return agentcore.New() },
	"agents-md":             func() sdk.SourceConnector { return agentsmd.New() },
	"ai-gateway":            func() sdk.SourceConnector { return aigateway.New() },
	"argocd":                func() sdk.SourceConnector { return argocd.New() },
	"aws":                   func() sdk.SourceConnector { return aws.New() },
	"aws-kms":               func() sdk.SourceConnector { return awskms.New() },
	"azure-activity":        func() sdk.SourceConnector { return azureactivity.New() },
	"azure-blob-audit":      func() sdk.SourceConnector { return azureblobaudit.New() },
	"azure-key-vault":       func() sdk.SourceConnector { return azurekeyvault.New() },
	"azure-openai":          func() sdk.SourceConnector { return azureopenai.New() },
	"bedrock":               func() sdk.SourceConnector { return bedrock.New() },
	"bedrock-kb":            func() sdk.SourceConnector { return bedrockkb.New() },
	"bigquery-audit":        func() sdk.SourceConnector { return bigqueryaudit.New() },
	"claude-api":            func() sdk.SourceConnector { return claudeapi.New() },
	"claude-apps-gateway":   func() sdk.SourceConnector { return claudeappsgateway.New() },
	"claude-batch":          func() sdk.SourceConnector { return claudebatch.New() },
	"claude-compliance":     func() sdk.SourceConnector { return claudecompliance.New() },
	"claude-config":         func() sdk.SourceConnector { return claudeconfig.New() },
	"claude-managed-agents": func() sdk.SourceConnector { return claudemanagedagents.New() },
	"claude-projects":       func() sdk.SourceConnector { return claudeprojects.New() },
	"claude-routines":       func() sdk.SourceConnector { return clauderoutines.New() },
	"cline":                 func() sdk.SourceConnector { return cline.New() },
	"cloudflare":            func() sdk.SourceConnector { return cloudflare.New() },
	"cloudflare-ai-gateway": func() sdk.SourceConnector { return cfaigateway.New() },
	"codex":                 func() sdk.SourceConnector { return codex.New() },
	"codex-managed-config":  func() sdk.SourceConnector { return codexmanagedconfig.New() },
	"cohere":                func() sdk.SourceConnector { return cohere.New() },
	"cowork-analytics":      func() sdk.SourceConnector { return coworkanalytics.New() },
	"crossplane":            func() sdk.SourceConnector { return crossplane.New() },
	"cursor":                func() sdk.SourceConnector { return cursor.New() },
	"databricks-uc":         func() sdk.SourceConnector { return databricksuc.New() },
	"deepseek":              func() sdk.SourceConnector { return deepseek.New() },
	"delta-sharing":         func() sdk.SourceConnector { return deltasharing.New() },
	"ebpf":                  func() sdk.SourceConnector { return ebpf.New() },
	"edugain":               func() sdk.SourceConnector { return edugain.New() },
	"egress-proxy":          func() sdk.SourceConnector { return egressproxy.New() },
	"entra-agent":           func() sdk.SourceConnector { return entraagent.New() },
	"envoy-ai-gateway":      func() sdk.SourceConnector { return envoyaigw.New() },
	"external-secrets":      func() sdk.SourceConnector { return externalsecrets.New() },
	"fal":                   func() sdk.SourceConnector { return fal.New() },
	"flux":                  func() sdk.SourceConnector { return flux.New() },
	"foundry-agents":        func() sdk.SourceConnector { return foundryagents.New() },
	"gcp-audit":             func() sdk.SourceConnector { return gcpaudit.New() },
	"gcp-kms":               func() sdk.SourceConnector { return gcpkms.New() },
	"gcs-audit":             func() sdk.SourceConnector { return gcsaudit.New() },
	"gemini":                func() sdk.SourceConnector { return gemini.New() },
	"gemini-cli":            func() sdk.SourceConnector { return geminicli.New() },
	"git":                   func() sdk.SourceConnector { return gitbinding.New() },
	"github":                func() sdk.SourceConnector { return githubsrc.New() },
	"gitlab":                func() sdk.SourceConnector { return gitlabsrc.New() },
	"glm":                   func() sdk.SourceConnector { return glm.New() },
	"google-adk":            func() sdk.SourceConnector { return googleadk.New() },
	"google-agent":          func() sdk.SourceConnector { return googleagent.New() },
	"goose":                 func() sdk.SourceConnector { return goose.New() },
	"grok":                  func() sdk.SourceConnector { return grok.New() },
	"hermes":                func() sdk.SourceConnector { return hermes.New() },
	"iceberg-catalog":       func() sdk.SourceConnector { return icebergcatalog.New() },
	"idp":                   func() sdk.SourceConnector { return idp.New() },
	"inference-gateway":     func() sdk.SourceConnector { return inferencegateway.New() },
	"infisical":             func() sdk.SourceConnector { return infisical.New() },
	"istio-telemetry":       func() sdk.SourceConnector { return istiotelemetry.New() },
	"kerberos":              func() sdk.SourceConnector { return kerberos.New() },
	"kmip":                  func() sdk.SourceConnector { return kmip.New() },
	"kong-agent-gateway":    func() sdk.SourceConnector { return kongagw.New() },
	"kong-audit":            func() sdk.SourceConnector { return kongaudit.New() },
	"ldap":                  func() sdk.SourceConnector { return ldap.New() },
	"litellm":               func() sdk.SourceConnector { return litellm.New() },
	"local":                 func() sdk.SourceConnector { return local.New() },
	"managed-settings":      func() sdk.SourceConnector { return managedsettings.New() },
	"mcp":                   func() sdk.SourceConnector { return mcpc.New() },
	"mcpb":                  func() sdk.SourceConnector { return mcpb.New() },
	"mistral":               func() sdk.SourceConnector { return mistral.New() },
	"mongo-audit":           func() sdk.SourceConnector { return mongoaudit.New() },
	"mssql-audit":           func() sdk.SourceConnector { return mssqlaudit.New() },
	"mysql-audit":           func() sdk.SourceConnector { return mysqlaudit.New() },
	"oasf":                  func() sdk.SourceConnector { return oasf.New() },
	"onepassword":           func() sdk.SourceConnector { return onepassword.New() },
	"openai":                func() sdk.SourceConnector { return openai.New() },
	"openclaw":              func() sdk.SourceConnector { return openclaw.New() },
	"opencode":              func() sdk.SourceConnector { return opencode.New() },
	"openhands":             func() sdk.SourceConnector { return openhands.New() },
	"openidfed":             func() sdk.SourceConnector { return openidfed.New() },
	"openlineage":           func() sdk.SourceConnector { return openlineage.New() },
	"openrouter":            func() sdk.SourceConnector { return openrouter.New() },
	"oracle-audit":          func() sdk.SourceConnector { return oracleaudit.New() },
	"paperclip":             func() sdk.SourceConnector { return paperclip.New() },
	"pgaudit":               func() sdk.SourceConnector { return pgaudit.New() },
	"redshift-audit":        func() sdk.SourceConnector { return redshiftaudit.New() },
	"runtime":               func() sdk.SourceConnector { return runtimesource.New() },
	"s3cloudtrail":          func() sdk.SourceConnector { return s3cloudtrail.New() },
	"servicenow-cmdb":       func() sdk.SourceConnector { return servicenow.NewCMDBSource() },
	"snowflake-audit":       func() sdk.SourceConnector { return snowflakeaudit.New() },
	"sops":                  func() sdk.SourceConnector { return sops.New() },
	"ssf":                   func() sdk.SourceConnector { return ssf.New() },
	"tak":                   func() sdk.SourceConnector { return tak.New() },
	"vault":                 func() sdk.SourceConnector { return vault.New() },
	"vault-audit":           func() sdk.SourceConnector { return vault.NewAudit() },
	"vertex":                func() sdk.SourceConnector { return vertex.New() },
	"xai":                   func() sdk.SourceConnector { return xai.New() },
}

// Aliases remain valid in stored and file-based source definitions without adding
// duplicate catalog entries.
var inProcSourceAliases = map[string]string{
	"entra":         "idp",
	"okta":          "idp",
	"pg-audit":      "pgaudit",
	"s3-cloudtrail": "s3cloudtrail",
}

// buildInProcSource resolves first-party kinds and preserves the edition seam.
func buildInProcSource(kind string) (sdk.SourceConnector, bool) {
	if canonical, ok := inProcSourceAliases[kind]; ok {
		kind = canonical
	}
	if constructor, ok := inProcSourceFactories[kind]; ok {
		return constructor(), true
	}
	if thisEdition.inProcSource == nil {
		return nil, false
	}
	return thisEdition.inProcSource(kind)
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
	// (buildInProcSource above) rather than via as_source=true, which runs Gather
	// once per boot. (Registering the same KIND twice is no longer the obstacle it
	// was: each registration carries its own name. The reason is the cadence — a
	// one-shot pass per boot is not a re-pollable scan.)
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
	case "paperclip":
		// Observe-only (I2.P5): companies as groups and agents as NHIs on the
		// roster; runs and cost reach the ledger when the identity entry also
		// sets as_source=true, or through a sources entry with poll_seconds.
		c := paperclip.New()
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
		// resolves none (it has no rosterProvider edition port); `-tags enterprise`
		// wires them.
		if thisEdition.rosterProvider == nil {
			return nil, nil, false
		}
		return thisEdition.rosterProvider(kind)
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
// an in-process-kind source is added directly with its re-poll interval. Each of
// the three is registered under the ENTRY'S NAME (not the connector's descriptor),
// so an operator can run several sources of one kind side by side. Every
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
		// The entry's own name IS the registration identity, on all three transports
		// below, so two entries of one kind are two sources. A nameless entry is NOT
		// wired: falling back to the connector's descriptor would silently make it
		// "the one instance of that kind" and collide with its named siblings.
		name := strings.TrimSpace(s.Name)
		if name == "" {
			log.Warn("ingest: source has no name; not wired (a configured source is never registered under its connector's descriptor by fallback)", "kind", s.Kind)
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
			if err := rt.LoadSourcePluginVerifiedNamed(name, s.Plugin.Path, scfg, s.Tenant, digest); err != nil {
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
			if err := rt.LoadSourcePluginNamed(name, path, scfg, s.Tenant); err != nil {
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
		if err := rt.AddPollSourceNamed(name, conn, scfg, s.Tenant, time.Duration(s.PollSeconds)*time.Second); err != nil {
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
// SEPARATE instance as a source — registered under the entry's own name, with its
// own settings map — so the connector's permitted-access edges flow —
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
				sourceName := strings.TrimSpace(spec.Name)
				if sourceName == "" {
					log.Warn("roster: identity provider has no name, so it cannot also be wired as a source; roster still active", "kind", spec.Kind)
					continue
				}
				// The same RESOLVED VALUES, in this instance's own map: the second
				// instance is the same connector kind, so its declared secret fields
				// and references match — but the two components must not share a
				// mutable settings map. (The runtime copies again at registration;
				// this keeps the provider's map out of reach either way.)
				sourceCfg := sdk.Config{Settings: make(map[string]string, len(resolved.Settings))}
				for k, v := range resolved.Settings {
					sourceCfg.Settings[k] = v
				}
				// Registered under the ENTRY'S NAME: okta and entra share the one idp
				// descriptor and are still two distinct sources.
				if err := rt.AddSourceNamed(sourceName, srcConn, sourceCfg, spec.Tenant); err != nil {
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
