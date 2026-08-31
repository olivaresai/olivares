// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import (
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// wire.go holds the LIVE TOML shapes this connector READS from a host's system-tier
// requirements.toml / managed_config.toml, plus the accessors drift needs. Only the
// fields the connector maps are declared; unknown keys are ignored on read
// (forward-compatible — Codex adds keys frequently). The fields that Codex accepts in
// MORE THAN ONE shape (approval_policy: a string OR a granular inline-table; web_search:
// a string OR a table; the [otel] exporter: a string OR a sub-table) are decoded as
// `any` / a generic map so a present-but-richer host shape is still OBSERVABLE rather
// than failing the whole parse and mis-reporting the host as "present but invalid"
// (the managedsettings dual-shape lesson).
//
// THREE-STATE presence (absent vs "[]"/empty-table vs list) is resolved with
// toml.MetaData.IsDefined, NOT slice/map nil-ness: BurntSushi decodes a present "[]"
// to a non-nil empty slice and a present "[table]" to IsDefined=true, len 0, so the
// reader can tell a deliberate LOCKDOWN from an unconstrained absence.

// requirementsWire is the live requirements.toml subset.
type requirementsWire struct {
	AllowedApprovalPolicies   []string                  `toml:"allowed_approval_policies"`
	AllowedSandboxModes       []string                  `toml:"allowed_sandbox_modes"`
	AllowedWebSearchModes     []string                  `toml:"allowed_web_search_modes"`
	AllowedApprovalsReviewers []string                  `toml:"allowed_approvals_reviewers"`
	AllowedPermissionProfiles map[string]bool           `toml:"allowed_permission_profiles"`
	EnforceResidency          string                    `toml:"enforce_residency"`
	Windows                   *windowsWire              `toml:"windows"`
	RemoteSandboxConfigs      []remoteSandboxConfigWire `toml:"remote_sandbox_config"`
	AllowRemoteControl        *bool                     `toml:"allow_remote_control"`
	AllowAppshots             *bool                     `toml:"allow_appshots"`
	ComputerUse               *computerUseWire          `toml:"computer_use"`
	AllowManagedHooksOnly     bool                      `toml:"allow_managed_hooks_only"`
	DefaultPermissions        string                    `toml:"default_permissions"`
	GuardianPolicyConfig      string                    `toml:"guardian_policy_config"`
	Features                  map[string]bool           `toml:"features"`
	Permissions               *permissionsWire          `toml:"permissions"`
	MCPServers                map[string]mcpServerWire  `toml:"mcp_servers"`
	Marketplaces              *marketplacesWire         `toml:"marketplaces"`
	Rules                     *rulesWire                `toml:"rules"`
	ExperimentalNetwork       *expNetworkWire           `toml:"experimental_network"`
}

type windowsWire struct {
	AllowedSandboxImplementations []string `toml:"allowed_sandbox_implementations"`
}

type remoteSandboxConfigWire struct {
	HostnamePatterns    []string `toml:"hostname_patterns"`
	AllowedSandboxModes []string `toml:"allowed_sandbox_modes"`
}

type computerUseWire struct {
	AllowLockedComputerUse *bool `toml:"allow_locked_computer_use"`
}

type marketplacesWire struct {
	RestrictToAllowedSources bool                             `toml:"restrict_to_allowed_sources"`
	AllowedSources           map[string]marketplaceSourceWire `toml:"allowed_sources"`
}

type marketplaceSourceWire struct {
	Source      string `toml:"source"`
	URL         string `toml:"url"`
	Ref         string `toml:"ref"`
	HostPattern string `toml:"host_pattern"`
	Path        string `toml:"path"`
}

type rulesWire struct {
	PrefixRules []prefixRuleWire `toml:"prefix_rules"`
}

type prefixRuleWire struct {
	Pattern       []patternTokenWire `toml:"pattern"`
	Decision      string             `toml:"decision"`
	Justification string             `toml:"justification"`
}

type patternTokenWire struct {
	Token string   `toml:"token"`
	AnyOf []string `toml:"any_of"`
}

type permissionsWire struct {
	Filesystem *filesystemWire `toml:"filesystem"`
}

type filesystemWire struct {
	DenyRead []string `toml:"deny_read"`
}

// mcpServerWire is one live [mcp_servers.<name>] entry. The requirements form is
// identity = { command|url }; the config.toml form is a flat command/url. Both are
// read so either shape is observable (identity wins).
type mcpServerWire struct {
	Identity mcpIdentityWire `toml:"identity"`
	Command  string          `toml:"command"`
	URL      string          `toml:"url"`
}

type mcpIdentityWire struct {
	Command any `toml:"command"`
	URL     any `toml:"url"`
}

func (w mcpServerWire) command() string {
	return firstNonEmpty(exactString(w.Identity.Command), w.Command)
}
func (w mcpServerWire) url() string { return firstNonEmpty(exactString(w.Identity.URL), w.URL) }
func (w mcpServerWire) matcherForm() bool {
	return matcherValue(w.Identity.Command) || matcherValue(w.Identity.URL)
}

func exactString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func matcherValue(v any) bool {
	if v == nil {
		return false
	}
	_, exact := v.(string)
	return !exact
}

// denyRead returns the live [permissions.filesystem] deny_read entries.
func (r requirementsWire) denyRead() []string {
	if r.Permissions == nil || r.Permissions.Filesystem == nil {
		return nil
	}
	return r.Permissions.Filesystem.DenyRead
}

// managedConfigWire is the live managed_config.toml subset. approval_policy / web_search
// are `any` (dual-shape); the [otel] block is a generic map (the exporter is a
// string OR a sub-table carrying the nested endpoint).
type managedConfigWire struct {
	ApprovalPolicy        any             `toml:"approval_policy"`
	SandboxMode           string          `toml:"sandbox_mode"`
	WebSearch             any             `toml:"web_search"`
	SandboxWorkspaceWrite *sandboxWWWire  `toml:"sandbox_workspace_write"`
	ExperimentalNetwork   *expNetworkWire `toml:"experimental_network"`
	OTEL                  map[string]any  `toml:"otel"`
}

type sandboxWWWire struct {
	NetworkAccess *bool    `toml:"network_access"`
	WritableRoots []string `toml:"writable_roots"`
}

type expNetworkWire struct {
	Enabled                   *bool    `toml:"enabled"`
	AllowedDomains            []string `toml:"allowed_domains"`
	DeniedDomains             []string `toml:"denied_domains"`
	ManagedAllowedDomainsOnly *bool    `toml:"managed_allowed_domains_only"`
	HTTPPort                  *int     `toml:"http_port"`
	SocksPort                 *int     `toml:"socks_port"`
	UnixSockets               []string `toml:"unix_sockets"`
	AllowLocalBinding         *bool    `toml:"allow_local_binding"`
}

// approvalPolicyScalar returns the live approval_policy when it is the scalar string
// form, and whether the present value is a NON-scalar (the granular inline-table). A
// non-scalar present value never matches a scalar expectation (drift reports it).
func approvalPolicyScalar(v any) (s string, granular bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		return strings.TrimSpace(t), false
	default:
		return "", true // a table/other shape (e.g. { granular = { ... } })
	}
}

// webSearchScalar returns the live web_search scalar string form (a table form, e.g.
// [tools.web_search], yields ("", true)).
func webSearchScalar(v any) (s string, table bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		return strings.TrimSpace(t), false
	default:
		return "", true
	}
}

// networkAccess returns the live [sandbox_workspace_write] network_access pin.
func (m managedConfigWire) networkAccess() *bool {
	if m.SandboxWorkspaceWrite == nil {
		return nil
	}
	return m.SandboxWorkspaceWrite.NetworkAccess
}

// --- live [otel] accessors (the block is a generic map: the exporter may be a string
// or a sub-table, and the endpoint is nested under exporter.<id>.endpoint) ----------

// otelExporterName returns the live logs exporter id: when otel.exporter is a string it
// is that string; when it is a sub-table ({ "otlp-http" = { ... } }) it is the first
// key. "" when unset.
func otelExporterName(otel map[string]any) string {
	return exporterName(otel["exporter"])
}

// otelTraceExporterName returns the live trace exporter id (same dual-shape rule).
func otelTraceExporterName(otel map[string]any) string {
	return exporterName(otel["trace_exporter"])
}

func exporterName(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case map[string]any:
		// The conformant shape is a single-key {<id> = {...}} table. Sort so a malformed
		// multi-key table yields a DETERMINISTIC id rather than a random map-iteration order.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		if len(keys) == 0 {
			return ""
		}
		sort.Strings(keys)
		return strings.TrimSpace(keys[0])
	}
	return ""
}

// otelLogUserPrompt returns the live otel.log_user_prompt and whether it was present.
func otelLogUserPrompt(otel map[string]any) (val, present bool) {
	v, ok := otel["log_user_prompt"]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// otelEndpointPresent reports whether the literal endpoint string appears anywhere in
// the live [otel] subtree (it may be nested under exporter.<id>.endpoint or
// trace_exporter.<id>.endpoint, or carried inline). A best-effort, honest check: the
// dual exporter shapes make an exact structural match brittle, so the connector
// verifies the org's endpoint is PRESENT in the otel config rather than asserting a
// precise nesting it cannot guarantee across Codex versions.
func otelEndpointPresent(otel map[string]any, endpoint string) bool {
	want := strings.TrimSpace(endpoint)
	if want == "" {
		return false
	}
	return valueContainsString(otel, want)
}

// valueContainsString walks a decoded TOML value looking for an exact string match.
func valueContainsString(v any, want string) bool {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) == want
	case map[string]any:
		for _, sub := range t {
			if valueContainsString(sub, want) {
				return true
			}
		}
	case []any:
		for _, sub := range t {
			if valueContainsString(sub, want) {
				return true
			}
		}
	}
	return false
}

// parseRequirements decodes a live requirements.toml. A nil/empty body decodes to a
// zero wire (no requirements); a malformed body returns the decode error. The MetaData
// carries the three-state presence the drift checker consults (IsDefined).
func parseRequirements(data []byte) (requirementsWire, toml.MetaData, error) {
	var w requirementsWire
	if len(data) == 0 {
		return w, toml.MetaData{}, nil
	}
	md, err := toml.Decode(string(data), &w)
	if err != nil {
		return requirementsWire{}, toml.MetaData{}, err
	}
	return w, md, nil
}

// parseManagedConfig decodes a live managed_config.toml (same contract as parseRequirements).
func parseManagedConfig(data []byte) (managedConfigWire, toml.MetaData, error) {
	var w managedConfigWire
	if len(data) == 0 {
		return w, toml.MetaData{}, nil
	}
	md, err := toml.Decode(string(data), &w)
	if err != nil {
		return managedConfigWire{}, toml.MetaData{}, err
	}
	return w, md, nil
}

// isDefined is a nil-safe MetaData.IsDefined (a zero MetaData reports everything absent).
func isDefined(md toml.MetaData, key ...string) bool {
	return md.IsDefined(key...)
}
