// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import "strings"

// MinPermissionProfilesCodexVersion is the documented minimum for managed permission
// profiles. "Codex 0.137.0 and earlier ignore allowed_permission_profiles and managed
// default_permissions". The connector cannot observe a host's Codex version from these
// TOML files, so preview/drift can only surface this as metadata/honesty.
const MinPermissionProfilesCodexVersion = "0.138.0"

// approval_policy values (config.toml / managed_config.toml; VERIFIED 2026-06-20
// developers.openai.com/codex/config-reference). "on-failure" is DEPRECATED (use
// on-request interactive / never non-interactive); the granular inline-table form
// (approval_policy = { granular = { ... } }) is a fourth shape captured as a non-scalar
// by the reader, never matched against a scalar expectation.
const (
	ApprovalUntrusted = "untrusted"
	ApprovalOnRequest = "on-request"
	ApprovalNever     = "never"
)

// sandbox_mode values (VERIFIED 2026-06-20). danger-full-access is the escape hatch a
// requirement constrains away (omit it from allowed_sandbox_modes to block --yolo).
const (
	SandboxReadOnly       = "read-only"
	SandboxWorkspaceWrite = "workspace-write"
	SandboxDangerFull     = "danger-full-access"
)

// web_search values (VERIFIED 2026-06-20; default "cached"). allowed_web_search_modes
// = [] permits ONLY "disabled" (which stays implicitly allowed); ["cached"] blocks live
// search even under danger-full-access.
const (
	WebSearchDisabled = "disabled"
	WebSearchCached   = "cached"
	WebSearchLive     = "live"
)

// allowed_approvals_reviewers values (VERIFIED 2026-06-20): require the automatic-review
// subagent (auto_review) and/or allow manual approval (user).
const (
	ReviewerAutoReview = "auto_review"
	ReviewerUser       = "user"
)

// [otel] exporter values. metrics_exporter additionally accepts "statsig" (its
// default); none means telemetry off for that signal.
//
// ⛔ RE-VERIFIED 2026-08-18 AGAINST codex-cli 0.147.0 ITSELF, and the shape had drifted
// since the 2026-06-20 reading in a way that BROKE EVERY GOVERNED SESSION. The exporter
// slots are an externally-tagged enum whose variants are NOT all the same kind:
//
//	none, statsig        UNIT variants   → render as a bare string
//	otlp-http, otlp-grpc STRUCT variants → render as a table, and `endpoint` is REQUIRED;
//	                                       otlp-http also REQUIRES `protocol`
//
// Measured by feeding real config.toml files to the binary with CODEX_HOME pointed at a
// scratch dir, one shape per run:
//
//	[otel.exporter.otlp-http] endpoint=…            → "missing field `protocol`"
//	exporter = "otlp-http"                          → "invalid type: unit variant, expected struct variant"
//	[otel.exporter.otlp-http] protocol="binary"     → "missing field `endpoint`"
//	[otel.exporter.otlp] …                          → "unknown variant `otlp`, expected one of
//	                                                   `none`, `statsig`, `otlp-http`, `otlp-grpc`"
//	[otel.exporter.otlp-http] protocol="http"       → "unknown variant `http`, expected `binary` or `json`"
//	[otel.exporter.otlp-grpc] endpoint=…            → ACCEPTED (protocol optional for grpc)
//
// **The failure is total, not partial.** Codex refuses to load config.toml AT ALL, so a
// managed pin that authored an OTLP endpoint stopped the agent from starting — the
// governed path failing closed on its own operator. That is why this is a re-verification
// and not an addition: a constant list can be right while the SHAPE around it is wrong.
const (
	OTELExporterNone     = "none"
	OTELExporterOTLPHTTP = "otlp-http"
	OTELExporterOTLPGRPC = "otlp-grpc"
	OTELExporterStatsig  = "statsig"
)

// [otel.exporter.<otlp-*>] protocol values (VERIFIED 2026-08-18 against codex-cli 0.147.0:
// anything else is rejected with "expected `binary` or `json`"). Binary is what the CLI
// actually posts — the probe received Content-Type: application/x-protobuf.
const (
	OTELProtocolBinary = "binary"
	OTELProtocolJSON   = "json"
)

// structVariantExporter reports whether an exporter id is a STRUCT variant, i.e. one that
// cannot be rendered as a bare string and requires an endpoint.
func structVariantExporter(e string) bool {
	return e == OTELExporterOTLPHTTP || e == OTELExporterOTLPGRPC
}

func knownOTELProtocol(p string) bool {
	return p == OTELProtocolBinary || p == OTELProtocolJSON
}

// knownApprovalPolicy reports whether p is a documented scalar approval policy.
func knownApprovalPolicy(p string) bool {
	switch p {
	case ApprovalUntrusted, ApprovalOnRequest, ApprovalNever:
		return true
	default:
		return false
	}
}

// knownSandboxMode reports whether m is a documented sandbox mode.
func knownSandboxMode(m string) bool {
	switch m {
	case SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFull:
		return true
	default:
		return false
	}
}

// knownWebSearchMode reports whether w is a documented web-search mode.
func knownWebSearchMode(w string) bool {
	switch w {
	case WebSearchDisabled, WebSearchCached, WebSearchLive:
		return true
	default:
		return false
	}
}

// knownReviewer reports whether r is a documented approvals reviewer.
func knownReviewer(r string) bool {
	return r == ReviewerAutoReview || r == ReviewerUser
}

// knownExporter reports whether e is a documented otel logs/trace exporter (metrics
// additionally accepts statsig — checked separately where relevant).
func knownExporter(e string) bool {
	switch e {
	case OTELExporterNone, OTELExporterOTLPHTTP, OTELExporterOTLPGRPC:
		return true
	default:
		return false
	}
}

func knownWindowsSandboxImplementation(s string) bool {
	return s == "elevated" || s == "unelevated"
}

func knownMarketplaceSource(s string) bool {
	switch s {
	case "git", "host_pattern", "local":
		return true
	default:
		return false
	}
}

func knownPrefixDecision(s string) bool {
	return s == "prompt" || s == "forbidden"
}

// MCPServer is one entry of the requirements.toml [mcp_servers.<name>] allowlist.
// Codex enables an MCP server ONLY when BOTH its name AND its identity match an
// approved entry; an empty allowlist disables ALL MCP servers (VERIFIED 2026-06-20).
// Exactly one of Command (a stdio server: identity = { command = ... }) or URL (a
// streamable-HTTP server: identity = { url = ... }) is the identity selector.
type MCPServer struct {
	// Name is the [mcp_servers.<Name>] table key (the user-facing server label).
	Name string `json:"name"`
	// Command is the stdio identity (identity = { command = ... }).
	Command string `json:"command,omitempty"`
	// URL is the streamable-HTTP identity (identity = { url = ... }).
	URL string `json:"url,omitempty"`
	// MatcherForm is true only on observed TOML that uses Codex's matcher-form MCP
	// identity. Authoring/rendering remains exact-strings-only; drift compares these
	// observed entries by server name and emits an Info manual-review finding.
	MatcherForm bool `json:"matcher_form,omitempty"`
}

// identityKind returns the non-sensitive identity selector ("command" or "url") and
// its value, for drift comparison + non-sensitive finding labels.
func (m MCPServer) identityKind() (kind, value string) {
	if m.MatcherForm {
		return "matcher", ""
	}
	if c := strings.TrimSpace(m.Command); c != "" {
		return "command", c
	}
	return "url", strings.TrimSpace(m.URL)
}

// NetworkConfig is the managed_config.toml experimental_network egress posture
// (VERIFIED 2026-06-20). managed_allowed_domains_only locks egress to the admin
// allowlist (the exfiltration-surface lockdown, the Codex analog of Claude's
// sandbox.network.allowManagedDomainsOnly).
type NetworkConfig struct {
	// Enabled pins experimental_network.enabled. nil = not authored.
	Enabled *bool `json:"enabled,omitempty"`
	// AllowedDomains is experimental_network.allowed_domains (egress allowlist; glob ok).
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	// DeniedDomains is experimental_network.denied_domains (egress blocklist; deny>allow).
	DeniedDomains []string `json:"denied_domains,omitempty"`
	// ManagedAllowedDomainsOnly pins experimental_network.managed_allowed_domains_only.
	ManagedAllowedDomainsOnly bool `json:"managed_allowed_domains_only,omitempty"`
	// HTTPPort pins experimental_network.http_port.
	HTTPPort *int `json:"http_port,omitempty"`
	// SocksPort pins experimental_network.socks_port.
	SocksPort *int `json:"socks_port,omitempty"`
	// UnixSockets pins experimental_network.unix_sockets.
	UnixSockets []string `json:"unix_sockets,omitempty"`
	// AllowLocalBinding pins experimental_network.allow_local_binding. nil = not authored.
	AllowLocalBinding *bool `json:"allow_local_binding,omitempty"`
}

func (n *NetworkConfig) hasAny() bool {
	return n != nil && (n.Enabled != nil || len(n.AllowedDomains) > 0 ||
		len(n.DeniedDomains) > 0 || n.ManagedAllowedDomainsOnly ||
		n.HTTPPort != nil || n.SocksPort != nil || len(n.UnixSockets) > 0 ||
		n.AllowLocalBinding != nil)
}

// OTELConfig is the managed_config.toml [otel] telemetry pin (VERIFIED 2026-06-20).
// The sanctioned OBSERVE path: point Codex's OTEL export at the control-plane collector
// with raw prompts OFF. Endpoint renders NESTED under the chosen exporter id
// (exporter.<id>.endpoint) — there is NO flat otel.endpoint key.
type OTELConfig struct {
	Environment string `json:"environment,omitempty"`
	// Exporter is the LOGS exporter (none|otlp-http|otlp-grpc).
	Exporter string `json:"exporter,omitempty"`
	// TraceExporter / MetricsExporter pin the other signals (optional).
	TraceExporter   string `json:"trace_exporter,omitempty"`
	MetricsExporter string `json:"metrics_exporter,omitempty"`
	// LogUserPrompt pins otel.log_user_prompt. Pin it FALSE to forbid exporting raw
	// user prompts (the minimal-data posture). nil = not authored.
	LogUserPrompt *bool `json:"log_user_prompt,omitempty"`
	// Endpoint is the OTLP endpoint the chosen exporter points at (the org collector).
	//
	// ⚠ It is the FULL URL Codex posts to, not an OTLP base: the probe recorded POSTs to
	// "/" when the endpoint had no path, NOT to the spec's /v1/logs. A collector that only
	// serves /v1/logs never sees a single record from Codex.
	Endpoint string `json:"endpoint,omitempty"`
	// Protocol pins otel.exporter.<id>.protocol (binary|json). REQUIRED by otlp-http,
	// optional for otlp-grpc; defaults to binary, which is what the CLI posts.
	Protocol string `json:"protocol,omitempty"`
}

func (o *OTELConfig) hasAny() bool {
	return o != nil && (o.Environment != "" || o.Exporter != "" || o.TraceExporter != "" ||
		o.MetricsExporter != "" || o.LogUserPrompt != nil || o.Endpoint != "" ||
		o.Protocol != "")
}

// telemetryOn reports whether the authored OTEL pin turns telemetry ON: a logs/trace
// exporter set to something other than "none", OR an endpoint authored (which render
// defaults to an otlp-http exporter, so the pin does emit telemetry).
func (o *OTELConfig) telemetryOn() bool {
	if o == nil {
		return false
	}
	return (o.Exporter != "" && o.Exporter != OTELExporterNone) ||
		(o.TraceExporter != "" && o.TraceExporter != OTELExporterNone) ||
		strings.TrimSpace(o.Endpoint) != ""
}

// RemoteSandboxConfig is one [[remote_sandbox_config]] requirements entry.
type RemoteSandboxConfig struct {
	HostnamePatterns    []string `json:"hostname_patterns,omitempty"`
	AllowedSandboxModes []string `json:"allowed_sandbox_modes,omitempty"`
}

// MarketplacesRequirement is the requirements.toml [marketplaces] supply-chain gate.
type MarketplacesRequirement struct {
	RestrictToAllowedSources bool                         `json:"restrict_to_allowed_sources,omitempty"`
	AllowedSources           map[string]MarketplaceSource `json:"allowed_sources,omitempty"`
}

// MarketplaceSource is one [marketplaces.allowed_sources.<name>] entry.
type MarketplaceSource struct {
	Source      string `json:"source,omitempty"`
	URL         string `json:"url,omitempty"`
	Ref         string `json:"ref,omitempty"`
	HostPattern string `json:"host_pattern,omitempty"`
	Path        string `json:"path,omitempty"`
}

// PrefixRule is one requirements [rules].prefix_rules entry. Requirements rules can
// only prompt or forbid (not allow).
type PrefixRule struct {
	Pattern       []PatternToken `json:"pattern,omitempty"`
	Decision      string         `json:"decision,omitempty"`
	Justification string         `json:"justification,omitempty"`
}

// PatternToken is one command-prefix matcher token. Exactly one of Token or AnyOf is
// authored/read for each element.
type PatternToken struct {
	Token string   `json:"token,omitempty"`
	AnyOf []string `json:"any_of,omitempty"`
}

// Requirements is the governance-authored requirements.toml intent — the non-
// overridable CONSTRAINT layer. Fields map to the verified requirements keys; the
// allow*-arrays are exact-match sets, the *bool pins carry the only meaningful value
// Codex honors (false for the allow_* toggles), and the three-state pointers
// distinguish "unconstrained" (nil) from the "[]"/empty-table LOCKDOWN.
type Requirements struct {
	// AllowedApprovalPolicies constrains which approval policies a user may select
	// (members of {untrusted,on-request,never}; omitting "never" blocks --ask-for-approval never).
	AllowedApprovalPolicies []string `json:"allowed_approval_policies,omitempty"`
	// AllowedSandboxModes constrains selectable sandbox modes; omit danger-full-access to block --yolo.
	AllowedSandboxModes []string `json:"allowed_sandbox_modes,omitempty"`
	// AllowedWebSearchModes is THREE-STATE: nil = unconstrained; a non-nil EMPTY slice
	// renders "[]" (only "disabled" permitted); a list is the allowlist.
	AllowedWebSearchModes *[]string `json:"allowed_web_search_modes,omitempty"`
	// AllowedApprovalsReviewers constrains the reviewer ("auto_review" and/or "user").
	AllowedApprovalsReviewers []string `json:"allowed_approvals_reviewers,omitempty"`
	// AllowedPermissionProfiles is THREE-STATE: nil = not authored; present-empty =
	// deny ALL profiles (lockdown). Profiles that are omitted or set to false are
	// denied, including profiles added in future versions.
	AllowedPermissionProfiles *map[string]bool `json:"allowed_permission_profiles,omitempty"`
	// EnforceResidency pins enforce_residency. The live docs currently accept "us".
	EnforceResidency string `json:"enforce_residency,omitempty"`
	// WindowsAllowedSandboxImplementations constrains Windows sandbox implementations.
	WindowsAllowedSandboxImplementations []string `json:"windows_allowed_sandbox_implementations,omitempty"`
	// RemoteSandboxConfigs constrains remote-host sandbox modes by hostname pattern.
	RemoteSandboxConfigs []RemoteSandboxConfig `json:"remote_sandbox_config,omitempty"`

	// AllowRemoteControl pins allow_remote_control. Codex treats ONLY false as
	// disabling device remote control (Codex driven from another device, relaying I/O
	// OUTSIDE a governed transport — the Codex analog of Claude's disableRemoteControl).
	// A nil pointer = unconstrained. (requirements.toml-only.)
	AllowRemoteControl *bool `json:"allow_remote_control,omitempty"`
	// AllowAppshots pins allow_appshots; only false is meaningful. (requirements.toml-only.)
	AllowAppshots *bool `json:"allow_appshots,omitempty"`
	// AllowLockedComputerUse pins [computer_use].allow_locked_computer_use; only false
	// is meaningful as a lockdown pin.
	AllowLockedComputerUse *bool `json:"allow_locked_computer_use,omitempty"`
	// AllowManagedHooksOnly = true skips user/project/session/plugin hooks while still
	// loading managed hooks — the hook supply-chain lockdown. requirements.toml-ONLY
	// (verbatim: "putting it in config.toml does not enable managed-hooks-only mode").
	AllowManagedHooksOnly bool `json:"allow_managed_hooks_only,omitempty"`

	// DenyRead is [permissions.filesystem] deny_read — absolute or ~-relative paths the
	// agent may never read (the secret-read lockdown; full-access is rejected when set).
	DenyRead []string `json:"deny_read,omitempty"`
	// Features pins [features] flags (name->bool) — the dangerous-capability lockdown
	// (computer_use/browser_use/in_app_browser/unified_exec/...). Codex normalizes the
	// feature set to these pins and rejects conflicting config.toml writes.
	Features map[string]bool `json:"features,omitempty"`

	// AllowedMCPServers is THREE-STATE: nil = unconstrained; a non-nil EMPTY slice
	// renders "[mcp_servers]" present-but-empty (ALL MCP servers disabled); a list is
	// the name+identity allowlist.
	AllowedMCPServers *[]MCPServer `json:"allowed_mcp_servers,omitempty"`

	// DefaultPermissions is the default_permissions profile (":read-only"/":workspace"/
	// ":danger-full-access" or a custom profile name).
	DefaultPermissions string `json:"default_permissions,omitempty"`
	// Marketplaces constrains Codex marketplace sources.
	Marketplaces *MarketplacesRequirement `json:"marketplaces,omitempty"`
	// PrefixRules constrains command prefixes (prompt/forbid only).
	PrefixRules []PrefixRule `json:"prefix_rules,omitempty"`
	// Network pins requirements.toml [experimental_network]. The live 2026-07-04
	// reference places this block in requirements; ManagedConfig.Network remains for
	// backward compatibility with older authored policies.
	Network *NetworkConfig `json:"network,omitempty"`
	// GuardianPolicyConfig is the guardian_policy_config automatic-review policy text
	// (it replaces the tenant section of the auto-review policy). Presence is verified;
	// the body is treated as opaque governance text.
	GuardianPolicyConfig string `json:"guardian_policy_config,omitempty"`
}

func (r Requirements) hasAny() bool {
	return len(r.AllowedApprovalPolicies) > 0 || len(r.AllowedSandboxModes) > 0 ||
		r.AllowedWebSearchModes != nil || len(r.AllowedApprovalsReviewers) > 0 ||
		r.AllowedPermissionProfiles != nil || r.EnforceResidency != "" ||
		len(r.WindowsAllowedSandboxImplementations) > 0 || len(r.RemoteSandboxConfigs) > 0 ||
		r.AllowRemoteControl != nil || r.AllowAppshots != nil ||
		r.AllowLockedComputerUse != nil || r.AllowManagedHooksOnly ||
		len(r.DenyRead) > 0 || len(r.Features) > 0 || r.AllowedMCPServers != nil ||
		r.DefaultPermissions != "" || r.Marketplaces != nil || len(r.PrefixRules) > 0 ||
		r.Network.hasAny() || r.GuardianPolicyConfig != ""
}

// ManagedConfig is the governance-authored managed_config.toml intent — the managed
// DEFAULTS layer (same schema as the user config.toml). These are starting values a
// user MAY change in-session, so a missing default is a WEAKER posture than a missing
// requirement; the network/telemetry defaults are nonetheless security-relevant.
type ManagedConfig struct {
	ApprovalPolicy string `json:"approval_policy,omitempty"` // untrusted|on-request|never
	SandboxMode    string `json:"sandbox_mode,omitempty"`    // read-only|workspace-write|danger-full-access
	WebSearch      string `json:"web_search,omitempty"`      // disabled|cached|live

	// NetworkAccess pins [sandbox_workspace_write] network_access — the egress toggle in
	// the workspace-write sandbox. Pin it FALSE to default the fleet to no outbound
	// network. nil = not authored.
	NetworkAccess *bool    `json:"network_access,omitempty"`
	WritableRoots []string `json:"writable_roots,omitempty"`

	// Network pins the experimental_network egress allowlist/lockdown.
	Network *NetworkConfig `json:"network,omitempty"`
	// OTEL pins the [otel] telemetry block (the sanctioned OBSERVE path).
	OTEL *OTELConfig `json:"otel,omitempty"`
}

func (m ManagedConfig) hasAny() bool {
	return m.ApprovalPolicy != "" || m.SandboxMode != "" || m.WebSearch != "" ||
		m.NetworkAccess != nil || len(m.WritableRoots) > 0 || m.Network.hasAny() || m.OTEL.hasAny()
}

// Policy is the GOVERNANCE-AUTHORED Codex managed-config intent: the org's desired
// posture across BOTH managed files. It is the input to RenderRequirements /
// RenderManagedConfig (to emit the TOML) and the expected reference in verification
// (to detect drift). It is carried as JSON on the connector's expected_policy config
// and the AGPL console's authoring surface; the rendered artifacts are TOML.
type Policy struct {
	// Requirements is the non-overridable constraint layer (requirements.toml).
	Requirements Requirements `json:"requirements"`
	// Defaults is the managed defaults layer (managed_config.toml).
	Defaults ManagedConfig `json:"managed_config"`
}

// firstNonEmpty returns the first non-empty trimmed string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
