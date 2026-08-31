// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.cowork"

// version is the connector's own semantic version. 0.2.0 added the
// connector-controls config surface (connector_controls + org_ref).
const version = "0.2.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgEnableHTTP        = "enable_http"
	cfgHTTPAddr          = "http_addr"
	cfgLogsPath          = "logs_path"
	cfgAuthHeader        = "auth_header"
	cfgAuthToken         = "auth_token"
	cfgAllowPublicBind   = "allow_public_bind"
	cfgRequireService    = "require_service"
	cfgGateway           = "gateway"
	cfgContentCapture    = "content_capture"
	cfgConnectorControls = "connector_controls"
	cfgOrgRef            = "org_ref"
)

// Configuration defaults. The HTTP port (4319) is one past Claude Code's OTLP/HTTP
// port (4318) so the two receivers can run side by side on one host. The logs path
// mirrors the OTLP/HTTP convention. The default require_service ("cowork") gates the
// receiver to Cowork records.
const (
	defaultHTTPAddr       = "127.0.0.1:4319"
	defaultLogsPath       = "/v1/logs"
	defaultAuthHeader     = "Authorization"
	defaultRequireService = serviceNameCowork
	// defaultOrgRef mirrors cowork-analytics: the stable reference for the governed
	// org, the identity the PERMITTED connector-control edges hang off.
	defaultOrgRef = "anthropic-org"
)

// config is the resolved, validated connector configuration.
type config struct {
	enableHTTP      bool
	httpAddr        string
	logsPath        string
	authHeader      string
	authToken       string
	allowPublicBind bool
	requireService  string
	gateway         model.Gateway
	// contentCapture, when true, would let the connector retain prompt/tool content
	// Cowork always sends. It defaults FALSE (structural-only) and there is no code
	// path that retains content even when true today — the flag exists so the
	// self-audit posture finding states the operator's intent honestly; flipping it
	// is a deliberate, audited choice, not a silent default.
	contentCapture bool
	// orgRef is the governed-org identity the PERMITTED connector-control edges hang
	// off (the org-effective control floor is not session-scoped).
	orgRef string
	// rawControls is the UNPARSED connector_controls JSON as configured. loadConfig
	// stays non-failing (a read-only collector degrades, never crashes on degradable
	// settings), but an authored control policy is NOT degradable — Open parses and
	// validates rawControls fail-closed (ParseConnectorControls), so a typo surfaces
	// before Gather rather than silently un-governing the org.
	rawControls string
	// controls is the parsed org-effective per-tool connector-control policy
	// (controls.go), set by Open. The zero value means not configured.
	controls ConnectorControlPolicy
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude Cowork (OTEL)",
		Description: "Ingests Claude Cowork OTLP/HTTP telemetry (the five log events: user_prompt, tool_result, api_request, api_error, tool_decision) and emits attributed R/RW access edges, per-account cost samples, and findings — including AUTO-APPROVED-HIGH-RISK actions (config/hook decision_source on a write/shell tool), denied tool decisions, and CONNECTOR-CONTROL DRIFT against the org-effective per-tool connector controls (connector_controls), whose non-blocked entries also emit PERMITTED policy edges. Materializes the shared account identity so Cowork OTEL correlates with the Compliance API. Observe-only; never proxies the subscription.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgEnableHTTP, Type: sdk.FieldBool, Default: "true", Description: "serve the OTLP/HTTP logs receiver (Cowork emits HTTP-only, logs-only telemetry)"},
			{Key: cfgHTTPAddr, Type: sdk.FieldString, Default: defaultHTTPAddr, Description: "OTLP/HTTP listen address. Cowork's admin console points Anthropic's cloud at this endpoint; bind loopback for a local OTEL-collector sidecar that forwards, or a reachable address WITH auth_token set"},
			{Key: cfgLogsPath, Type: sdk.FieldString, Default: defaultLogsPath, Description: "HTTP path that accepts OTLP logs export requests"},
			{Key: cfgAuthHeader, Type: sdk.FieldString, Default: defaultAuthHeader, Description: "name of the HTTP header carrying the shared secret Cowork is configured to send (e.g. Authorization or x-api-key)"},
			{Key: cfgAuthToken, Type: sdk.FieldString, Secret: true, Default: "", Description: "expected value of auth_header. When set, every OTLP request MUST carry the matching header or it is rejected (401). Required when binding a non-loopback address (Anthropic's cloud PUSHES to this endpoint, so an unauthenticated public receiver would let anyone forge Cowork telemetry). Never persisted"},
			{Key: cfgAllowPublicBind, Type: sdk.FieldBool, Default: "false", Description: "DANGEROUS: allow a non-loopback bind WITHOUT an auth_token (accept-the-risk escape hatch). Default false: a non-loopback bind requires auth_token, else Open fails closed"},
			{Key: cfgRequireService, Type: sdk.FieldString, Default: defaultRequireService, Description: "require resource attribute service.name to equal this value (default \"cowork\") so a Claude Code record pointed at this receiver is not mis-ingested as Cowork. Set empty to accept any service.name"},
			{Key: cfgGateway, Type: sdk.FieldString, Default: "", Description: "the deployment SURFACE Cowork's models are served through (direct|bedrock-mantle|bedrock-legacy|vertex|foundry|claude-platform-aws). Empty/direct = first-party; Bedrock-served calls are auto-detected from the model id. Tags every cost sample so FinOps is never blind to the surface"},
			{Key: cfgContentCapture, Type: sdk.FieldBool, Default: "false", Description: "OBS-10: Cowork ALWAYS includes prompt/tool content in its events. Default false = structural-only (the connector discards content, retaining only redacted resource refs). The self-audit posture finding records the resolved value on the ledger"},
			{Key: cfgOrgRef, Type: sdk.FieldString, Default: defaultOrgRef, Description: "stable reference for the governed org (mirrors cowork-analytics) — the identity the PERMITTED connector-control edges attach to"},
			{Key: cfgConnectorControls, Type: sdk.FieldString, Default: "", Description: "org-EFFECTIVE projection of the GA per-tool connector controls, as a ConnectorControlPolicy JSON ({\"default\":...,\"connectors\":{<server>:{\"level\":...,\"tools\":{<tool>:...}}}} with levels always_allow|needs_approval|blocked). Upstream these are configured per ROLE in the admin console role editor, Connectors tab (Enterprise plans; a connector set to Custom enables per-tool levels) and are CONSOLE-ONLY — no Admin API or managed-settings key — so the operator authors the effective org-wide floor here. It becomes PERMITTED mcp.server/mcp.tool edges plus a live drift finding when a blocked or needs-approval connector/tool executes un-gated. Empty = not configured; malformed JSON fails Open (deny-closed)"},
		},
	}
}

// loadConfig reads the resolved settings, applying defaults. Degradable
// misconfigurations fall back to a default rather than failing (a read-only
// collector should degrade, not crash); the deny-closed BIND check (a non-loopback
// address without an auth token) is enforced in Open, where the resolved address is
// known. The require_service default ("cowork") is preserved when the key is absent
// but honored as empty when explicitly set to "" (accept-any), so the distinction
// between "unset" and "deliberately disabled" is not lost.
func loadConfig(cfg sdk.Config) config {
	c := config{
		enableHTTP:      cfg.GetBool(cfgEnableHTTP, true),
		httpAddr:        firstNonEmpty(cfg.Get(cfgHTTPAddr), defaultHTTPAddr),
		logsPath:        firstNonEmpty(cfg.Get(cfgLogsPath), defaultLogsPath),
		authHeader:      firstNonEmpty(cfg.Get(cfgAuthHeader), defaultAuthHeader),
		authToken:       cfg.Get(cfgAuthToken),
		allowPublicBind: cfg.GetBool(cfgAllowPublicBind, false),
		contentCapture:  cfg.GetBool(cfgContentCapture, false),
		orgRef:          firstNonEmpty(cfg.Get(cfgOrgRef), defaultOrgRef),
		rawControls:     cfg.Get(cfgConnectorControls),
	}
	if c.logsPath == "" || c.logsPath[0] != '/' {
		c.logsPath = defaultLogsPath
	}
	// require_service: honor an explicit empty (accept-any); default only when unset.
	if v, ok := cfg.Lookup(cfgRequireService); ok {
		c.requireService = v
	} else {
		c.requireService = defaultRequireService
	}
	// Gateway is degradable: an unrecognized surface falls back to direct rather than
	// failing the collector. An empty value also means direct.
	if g := model.Gateway(cfg.Get(cfgGateway)); g != "" && g.Valid() {
		c.gateway = g
	}
	return c
}

// firstNonEmpty returns a if non-empty, else b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
