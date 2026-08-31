// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.claude"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgGRPCAddr         = "grpc_addr"
	cfgHTTPAddr         = "http_addr"
	cfgEnableGRPC       = "enable_grpc"
	cfgEnableHTTP       = "enable_http"
	cfgHookPath         = "hook_path"
	cfgCorrelationWait  = "correlation_window"
	cfgSilenceThreshold = "silence_threshold"
	cfgAllowPublicBind  = "allow_public_bind"
	cfgContentCapture   = "content_capture"
	cfgEnforcement      = "enforcement"
	cfgGateway          = "gateway"
	cfgAgentSDKPolicy   = "agent_sdk_policy"
	cfgAgentSDKConfig   = "agent_sdk_config"
	// ANT2-12/13: paths to the live managed-mcp.json and the settings file carrying
	// the sandbox.* posture. The connector OBSERVES these files (read-only) and models
	// their effect; it never authors them (that is). Empty = not observed.
	cfgManagedMCPPath = "managed_mcp_path"
	cfgSandboxPath    = "sandbox_path"
	// cfgSemconvOptIn mirrors OTEL_SEMCONV_STABILITY_OPT_IN: when it contains
	// "gen_ai_latest_experimental" the vendor-neutral gen_ai.* ingest profile
	// (OBS-01) is enabled. Off by default because the conventions are Development.
	cfgSemconvOptIn = "semconv_opt_in"
	// cfgResourceLabels: the ALLOWLIST of OTEL_RESOURCE_ATTRIBUTES keys the
	// connector honors as attribution labels. Empty = off (minimal-data default).
	cfgResourceLabels = "resource_labels"
	// cfgClaudeCodeMetrics: persist the VALUES of the Claude Code productivity/
	// adoption metrics (lines_of_code/commit/pull_request/session/token/code_edit_tool.
	// decision/active_time) as MetricSamples, in addition to the liveness signal they
	// already feed. Default true (the values already arrive at the receiver; persisting
	// them is the adoption dashboard's substrate). cost.usage is NEVER persisted here
	// (the authoritative cost path is api_request — counting it twice would
	// double-count). Set false to keep the receiver liveness-only.
	cfgClaudeCodeMetrics = "claude_code_metrics"
)

// Configuration defaults. The OTLP ports mirror the OpenTelemetry conventions
// (4317 gRPC, 4318 HTTP); the connector binds them itself rather than sharing the
// agent's, so disabling a Claude Code OTEL_* variable cannot silence the
// collector (docs/SECURITY-HARDENING.md).
const (
	defaultGRPCAddr         = "127.0.0.1:4317"
	defaultHTTPAddr         = "127.0.0.1:4318"
	defaultHookPath         = "/hooks"
	defaultCorrelationWait  = 5 * time.Second
	defaultSilenceThreshold = 2 * time.Minute
)

// config is the resolved, validated connector configuration.
type config struct {
	grpcAddr          string
	httpAddr          string
	enableGRPC        bool
	enableHTTP        bool
	hookPath          string
	correlationWait   time.Duration
	silenceThreshold  time.Duration
	allowPublicBind   bool
	redaction         redactionPolicy
	enforcement       hookEnforcementPolicy
	gateway           model.Gateway
	agentSDKPolicy    AgentSDKPolicy // CLA-10: declared max Agent SDK posture (drift check)
	agentSDKConfig    AgentSDKConfig // declared Agent SDK program config (dangerous-knob posture)
	agentSDKConfigSet bool           // whether an agent_sdk_config was declared
	genAIProfile      bool           // OBS-01: vendor-neutral gen_ai.* ingest profile (opt-in)
	managedMCPPath    string         // ANT2-12: path to the observed managed-mcp.json (empty = none)
	sandboxPath       string         // ANT2-13: path to the observed sandbox settings (empty = none)
	resourceLabels    []string       // allowlisted OTEL_RESOURCE_ATTRIBUTES keys (empty = off)
	claudeCodeMetrics bool           // persist claude_code.* metric VALUES (default on)
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude Code (OTEL + hooks)",
		Description: "Ingests Claude Code OTLP telemetry and hooks AND, opt-in, vendor-neutral OpenTelemetry GenAI (gen_ai.*) telemetry from any OTel-instrumented agent; emits attributed R/RW access edges, cost samples and anti-evasion findings.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgEnableGRPC, Type: sdk.FieldBool, Default: "true", Description: "serve the OTLP/gRPC receiver"},
			{Key: cfgGRPCAddr, Type: sdk.FieldString, Default: defaultGRPCAddr, Description: "OTLP/gRPC listen address"},
			{Key: cfgEnableHTTP, Type: sdk.FieldBool, Default: "true", Description: "serve the OTLP/HTTP receiver and the hook endpoint"},
			{Key: cfgHTTPAddr, Type: sdk.FieldString, Default: defaultHTTPAddr, Description: "OTLP/HTTP and hook listen address"},
			{Key: cfgHookPath, Type: sdk.FieldString, Default: defaultHookPath, Description: "HTTP path that accepts Claude Code hook payloads"},
			{Key: cfgCorrelationWait, Type: sdk.FieldDuration, Default: defaultCorrelationWait.String(), Description: "how long to hold a tool event waiting for its hook/OTEL counterpart before emitting"},
			{Key: cfgSilenceThreshold, Type: sdk.FieldDuration, Default: defaultSilenceThreshold.String(), Description: "OTEL silence beyond which a still-active (hooking) session is flagged as a telemetry gap"},
			{Key: cfgAllowPublicBind, Type: sdk.FieldBool, Default: "false", Description: "DANGEROUS: allow binding the OTLP/hook receivers to a non-loopback address. The cooperative ingest is UNAUTHENTICATED, so a non-loopback bind lets anyone reachable forge telemetry/edges; keep on loopback (the eBPF backstop captures off-host activity)"},
			{Key: cfgContentCapture, Type: sdk.FieldString, Default: "", Description: "OBS-10: comma-separated allowlist of content categories the connector may RETAIN, mirroring Claude Code's OTEL_LOG_* flags (user_prompts, tool_details, tool_content, raw_api_bodies). Empty = structural-only (the default, safe posture): no prompt text, tool bodies or API bodies are retained even if Claude Code emits them. Extended-thinking is always redacted. Opting a category in only affects the tracing-beta span-event path"},
			{Key: cfgEnforcement, Type: sdk.FieldString, Default: "", Description: "CLA-01: OPT-IN governed enforcement policy as JSON {\"rules\":[{\"tool\":\"Bash\",\"decision\":\"ask\",\"reason\":\"…\"},{\"resource_kind\":\"file\",\"mode\":\"write\",\"decision\":\"deny\"}]}. Empty = cooperative-only (hooks observed, never gated — the default, read-first posture). When set, the connector returns a Claude Code permissionDecision (deny/ask) on matching PreToolUse/PermissionRequest hooks and records each gate as a finding. An invalid policy fails at Open (never silently ungoverned)"},
			{Key: cfgGateway, Type: sdk.FieldString, Default: "", Description: "CLA-11: the deployment SURFACE this Claude Code fleet runs on (direct|bedrock-mantle|bedrock-legacy|vertex|foundry|claude-platform-aws). Empty/direct = first-party API. Bedrock-served calls are also auto-detected from the model id (us./eu./apac./global.anthropic.* = bedrock-legacy CRIS, bare anthropic.* = bedrock-mantle); set this for Vertex/Foundry, which the model id cannot distinguish. The resolved surface tags every cost sample so FinOps/governance is never blind to the deployment"},
			{Key: cfgAgentSDKPolicy, Type: sdk.FieldString, Default: "", Description: "CLA-10: OPT-IN Agent SDK governance as JSON {\"max_permission_mode\":\"acceptEdits\"}. Declares the most permissive Agent SDK permissionMode (default|plan|acceptEdits|auto|dontAsk|bypassPermissions) the fleet may run in; an observed escalation beyond it (cross-referenced against managed-settings when wired) emits a drift finding. Per-knob authorizations (allow_session_store|allow_skip_permissions|allow_plugins|allow_max_budget|permission_prompt_tool) degrade a declared dangerous knob from HIGH to an Info observation. Empty = no policy (mode transitions are still observed, just not policy-checked). An invalid policy fails at Open (never silently un-governed)"},
			{Key: cfgAgentSDKConfig, Type: sdk.FieldString, Default: "", Description: "OPT-IN DECLARED Agent SDK program config the connector models for governance, as JSON (e.g. {\"permission_mode\":\"bypassPermissions\",\"session_store\":true,\"agents\":[\"researcher\"]}). When set, the connector emits a posture finding per dangerous knob (sessionStore/allowDangerouslySkipPermissions/plugins/maxBudgetUsd/permissionPromptToolName — HIGH unless authorized in agent_sdk_policy) plus the dominant subagent bypassPermissions-inheritance finding and the effective-cap drift. Structural metadata only (never a prompt/secret). Empty = not modeled. An invalid config fails at Open"},
			{Key: cfgSemconvOptIn, Type: sdk.FieldString, Default: "", Description: "OBS-01/AIP-06: mirrors OTEL_SEMCONV_STABILITY_OPT_IN. Set to a comma-separated list containing \"gen_ai_latest_experimental\" to enable the vendor-neutral OpenTelemetry GenAI (gen_ai.*) ingest profile pinned to semconv v1.41.1, so ANY OTel-instrumented agent (not just Claude Code) feeds the access-map/cost pipeline. The profile normalizes the THREE GenAI dialects that coexist in 2026 fleets (legacy OpenLLMetry gen_ai.prompt.{i}.*, the deprecated v1.36-or-prior per-message events, the v1.37+ messages generation), stamping each normalized event with the dialect's semconv pin, and maps the mcp.* conventions (v1.39) plus the invoke_agent client/internal split and invoke_workflow (v1.41). Empty/off by default because the gen_ai conventions are Development status (pre-stable); off, a gen_ai.* record still feeds the liveness watchdog but is not mapped"},
			{Key: cfgManagedMCPPath, Type: sdk.FieldString, Default: "", Description: "ANT2-12: path to the live managed-mcp.json. When set, the connector OBSERVES it (read-only) and models its eval-order (deny wins → allow by exact URL/command match) — emitting a drift finding for any allow entry that relies on serverName only (NOT a security control). It does not author the file (that is). Empty = not observed"},
			{Key: cfgSandboxPath, Type: sdk.FieldString, Default: "", Description: "ANT2-13: path to the settings file carrying the sandbox.* lockdown posture. When set, the connector OBSERVES it (read-only) and emits posture findings (unsandboxed-commands allowed, fail-open-if-unavailable, the egress allowlist, and the domain-fronting caveat that the proxy does NOT TLS-inspect). Empty = not observed"},
			{Key: cfgResourceLabels, Type: sdk.FieldString, Default: "", Description: "comma-separated ALLOWLIST of OTEL_RESOURCE_ATTRIBUTES keys to honor as attribution labels (e.g. \"team,project,cost_center\"). Since Claude Code 2.1.161 the operator's resource attributes ride every metric datapoint and event record; allowlisted keys are scrubbed and attached as Labels on the once-per-session identity edges and on every cost sample (FinOps slices spend by them — team/project become first-class dimensions). Keys colliding with the standard attributes (session.id, organization.id, …) are ignored. Empty = off (minimal-data default: arbitrary operator labels are NOT ingested)"},
			{Key: cfgClaudeCodeMetrics, Type: sdk.FieldBool, Default: "true", Description: "persist the VALUES of the Claude Code productivity/adoption metrics (lines_of_code/commit/pull_request/session.count, token.usage by model, code_edit_tool.decision accept/reject by tool, active_time) as MetricSamples — the substrate of the Adoption dashboard, attributed to the SESSION (the developer email Claude Code exports on OAuth is NEVER read here; per-developer ROI rides the admin Analytics feed instead). The values already arrive at the receiver; this only stops discarding them. cost.usage is NEVER persisted (the authoritative cost path is api_request — counting it twice would double-count). Set false to keep the receiver liveness-only. BOUNDARY: the OTLP plane sees only what the fleet's OTEL exporter sends — a 3P-provider (Bedrock/Vertex/Foundry) Claude Code estate that does not export OTLP is invisible here"},
		},
	}
}

// loadConfig reads the resolved settings, applying defaults. Degradable
// misconfigurations (an out-of-range duration) fall back to their default rather
// than failing the connector (a read-only collector should degrade, not crash).
// The ONE setting that fails hard is the enforcement policy: a malformed policy
// must not silently leave the fleet ungoverned, so its parse error is returned.
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		grpcAddr:         firstNonEmpty(cfg.Get(cfgGRPCAddr), defaultGRPCAddr),
		httpAddr:         firstNonEmpty(cfg.Get(cfgHTTPAddr), defaultHTTPAddr),
		enableGRPC:       cfg.GetBool(cfgEnableGRPC, true),
		enableHTTP:       cfg.GetBool(cfgEnableHTTP, true),
		hookPath:         firstNonEmpty(cfg.Get(cfgHookPath), defaultHookPath),
		correlationWait:  cfg.GetDuration(cfgCorrelationWait, defaultCorrelationWait),
		silenceThreshold: cfg.GetDuration(cfgSilenceThreshold, defaultSilenceThreshold),
		allowPublicBind:  cfg.GetBool(cfgAllowPublicBind, false),
	}
	if c.correlationWait <= 0 {
		c.correlationWait = defaultCorrelationWait
	}
	if c.silenceThreshold <= 0 {
		c.silenceThreshold = defaultSilenceThreshold
	}
	if c.hookPath == "" || c.hookPath[0] != '/' {
		c.hookPath = defaultHookPath
	}
	c.redaction, _ = parseRedaction(cfg.Get(cfgContentCapture))
	enf, err := parseEnforcement(cfg.Get(cfgEnforcement))
	if err != nil {
		return c, err
	}
	c.enforcement = enf
	// Gateway is degradable: an unrecognized surface falls back to direct (the safe,
	// most-common default) rather than failing the collector — a typo must not stop
	// telemetry from flowing. An empty value also means direct.
	if g := model.Gateway(cfg.Get(cfgGateway)); g != "" && g.Valid() {
		c.gateway = g
	}
	// Agent SDK policy is governance: a malformed policy fails hard (never silently
	// un-governed), like the enforcement policy above.
	pol, _, err := parseAgentSDKPolicy(cfg.Get(cfgAgentSDKPolicy))
	if err != nil {
		return c, err
	}
	c.agentSDKPolicy = pol
	// the DECLARED Agent SDK program config (dangerous-knob posture). A malformed
	// config fails hard for the same reason as the policy — a control plane that cannot
	// parse the declared fleet config must not silently skip its findings.
	sdkCfg, sdkSet, err := parseAgentSDKConfig(cfg.Get(cfgAgentSDKConfig))
	if err != nil {
		return c, err
	}
	c.agentSDKConfig = sdkCfg
	c.agentSDKConfigSet = sdkSet
	// OBS-01: the gen_ai.* profile is opt-in (the conventions are Development), gated
	// on the spec's own OTEL_SEMCONV_STABILITY_OPT_IN token.
	c.genAIProfile = genAIOptIn(cfg.Get(cfgSemconvOptIn))
	c.managedMCPPath = cfg.Get(cfgManagedMCPPath)
	c.sandboxPath = cfg.Get(cfgSandboxPath)
	c.resourceLabels = splitCSV(cfg.Get(cfgResourceLabels))
	c.claudeCodeMetrics = cfg.GetBool(cfgClaudeCodeMetrics, true)
	return c, nil
}

// splitCSV splits a comma-separated config value into trimmed, non-empty tokens.
func splitCSV(s string) []string {
	var out []string
	for _, tok := range strings.Split(s, ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// firstNonEmpty returns a if non-empty, else b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
