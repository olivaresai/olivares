// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// render.go is the AUTHORING half: it turns a governance-authored Policy into the exact
// requirements.toml / managed_config.toml bytes an operator distributes. It builds a
// map[string]any (not a tagged struct) on purpose — TOML's struct `omitempty` cannot
// express the THREE-STATE keys this connector needs (a present "[]" / empty "[mcp_servers]"
// LOCKDOWN must render, never be dropped as "empty"), and a map gives full control while
// BurntSushi keeps the output deterministic (it sorts map keys and emits scalars/arrays
// before [tables], so the result is always valid, ordered TOML).

// RenderRequirements produces the requirements.toml bytes for the authored constraint
// layer. An all-zero Requirements renders empty (nothing to constrain).
func RenderRequirements(p Policy) ([]byte, error) {
	if issues := validateRequirementsPolicy(p.Requirements); len(issues) > 0 {
		return nil, fmt.Errorf("invalid requirements policy: %s", strings.Join(issues, "; "))
	}
	return toml.Marshal(requirementsToMap(p.Requirements))
}

// RenderManagedConfig produces the managed_config.toml bytes for the authored defaults
// layer. An all-zero ManagedConfig renders empty (no managed defaults).
func RenderManagedConfig(p Policy) ([]byte, error) {
	return toml.Marshal(managedConfigToMap(p.Defaults))
}

// requirementsToMap projects Requirements onto the wire map, inserting only authored
// keys. The three-state pointers (AllowedWebSearchModes, AllowedMCPServers) render their
// EMPTY-present LOCKDOWN form (`[]` / empty `[mcp_servers]`) when set to a non-nil empty
// value, and are omitted entirely when nil.
func requirementsToMap(r Requirements) map[string]any {
	m := map[string]any{}
	if len(r.AllowedApprovalPolicies) > 0 {
		m["allowed_approval_policies"] = toAnySlice(r.AllowedApprovalPolicies)
	}
	if len(r.AllowedSandboxModes) > 0 {
		m["allowed_sandbox_modes"] = toAnySlice(r.AllowedSandboxModes)
	}
	if r.AllowedWebSearchModes != nil { // three-state: empty -> "[]" (only "disabled")
		m["allowed_web_search_modes"] = toAnySlice(*r.AllowedWebSearchModes)
	}
	if len(r.AllowedApprovalsReviewers) > 0 {
		m["allowed_approvals_reviewers"] = toAnySlice(r.AllowedApprovalsReviewers)
	}
	if r.AllowedPermissionProfiles != nil {
		m["allowed_permission_profiles"] = boolMap(*r.AllowedPermissionProfiles)
	}
	if s := strings.TrimSpace(r.EnforceResidency); s != "" {
		m["enforce_residency"] = s
	}
	if len(r.WindowsAllowedSandboxImplementations) > 0 {
		m["windows"] = map[string]any{
			"allowed_sandbox_implementations": toAnySlice(sortedStrings(r.WindowsAllowedSandboxImplementations)),
		}
	}
	if len(r.RemoteSandboxConfigs) > 0 {
		m["remote_sandbox_config"] = remoteSandboxConfigsToMaps(r.RemoteSandboxConfigs)
	}
	if r.AllowRemoteControl != nil {
		m["allow_remote_control"] = *r.AllowRemoteControl
	}
	if r.AllowAppshots != nil {
		m["allow_appshots"] = *r.AllowAppshots
	}
	if r.AllowLockedComputerUse != nil {
		m["computer_use"] = map[string]any{"allow_locked_computer_use": *r.AllowLockedComputerUse}
	}
	if r.AllowManagedHooksOnly {
		m["allow_managed_hooks_only"] = true
	}
	if s := strings.TrimSpace(r.DefaultPermissions); s != "" {
		m["default_permissions"] = s
	}
	if s := strings.TrimSpace(r.GuardianPolicyConfig); s != "" {
		m["guardian_policy_config"] = s
	}
	if len(r.Features) > 0 {
		m["features"] = r.Features
	}
	if len(r.DenyRead) > 0 {
		m["permissions"] = map[string]any{
			"filesystem": map[string]any{"deny_read": toAnySlice(r.DenyRead)},
		}
	}
	if r.AllowedMCPServers != nil { // three-state: empty -> "[mcp_servers]" (all MCP off)
		m["mcp_servers"] = mcpServersToMap(*r.AllowedMCPServers)
	}
	if r.Marketplaces != nil {
		m["marketplaces"] = marketplacesToMap(*r.Marketplaces)
	}
	if len(r.PrefixRules) > 0 {
		m["rules"] = map[string]any{"prefix_rules": prefixRulesToMaps(r.PrefixRules)}
	}
	if r.Network.hasAny() {
		m["experimental_network"] = networkToMap(*r.Network)
	}
	return m
}

// managedConfigToMap projects ManagedConfig onto the wire map.
func managedConfigToMap(c ManagedConfig) map[string]any {
	m := map[string]any{}
	if s := strings.TrimSpace(c.ApprovalPolicy); s != "" {
		m["approval_policy"] = s
	}
	if s := strings.TrimSpace(c.SandboxMode); s != "" {
		m["sandbox_mode"] = s
	}
	if s := strings.TrimSpace(c.WebSearch); s != "" {
		m["web_search"] = s
	}
	if c.NetworkAccess != nil || len(c.WritableRoots) > 0 {
		sb := map[string]any{}
		if c.NetworkAccess != nil {
			sb["network_access"] = *c.NetworkAccess
		}
		if len(c.WritableRoots) > 0 {
			sb["writable_roots"] = toAnySlice(c.WritableRoots)
		}
		m["sandbox_workspace_write"] = sb
	}
	if c.Network.hasAny() {
		m["experimental_network"] = networkToMap(*c.Network)
	}
	if c.OTEL.hasAny() {
		m["otel"] = otelToMap(*c.OTEL)
	}
	return m
}

// networkToMap projects the experimental_network egress posture.
func networkToMap(n NetworkConfig) map[string]any {
	m := map[string]any{}
	if n.Enabled != nil {
		m["enabled"] = *n.Enabled
	}
	if len(n.AllowedDomains) > 0 {
		m["allowed_domains"] = toAnySlice(n.AllowedDomains)
	}
	if len(n.DeniedDomains) > 0 {
		m["denied_domains"] = toAnySlice(n.DeniedDomains)
	}
	if n.ManagedAllowedDomainsOnly {
		m["managed_allowed_domains_only"] = true
	}
	if n.HTTPPort != nil {
		m["http_port"] = *n.HTTPPort
	}
	if n.SocksPort != nil {
		m["socks_port"] = *n.SocksPort
	}
	if len(n.UnixSockets) > 0 {
		m["unix_sockets"] = toAnySlice(sortedStrings(n.UnixSockets))
	}
	if n.AllowLocalBinding != nil {
		m["allow_local_binding"] = *n.AllowLocalBinding
	}
	return m
}

// otelToMap projects the [otel] telemetry pin. The endpoint renders NESTED under the
// chosen exporter id (exporter.<id>.endpoint) — the verified location; there is no flat
// otel.endpoint key.
//
// ⛔ «When no endpoint is authored the exporter renders as the bare id» — that sentence
// was here, and for otlp-http/otlp-grpc it produced a config codex-cli 0.147.0 REFUSES TO
// LOAD ("invalid type: unit variant, expected struct variant"). Those two are struct
// variants; only none and statsig are unit variants. See the measured table over the
// exporter constants in policy.go. The bare-id path is now reserved for the unit variants,
// and an authorable combination that cannot render into something Codex accepts is
// refused at authoring time instead of shipped as a config that stops the agent.
func otelToMap(o OTELConfig) map[string]any {
	m := map[string]any{}
	if s := strings.TrimSpace(o.Environment); s != "" {
		m["environment"] = s
	}
	if o.LogUserPrompt != nil {
		m["log_user_prompt"] = *o.LogUserPrompt
	}
	endpoint := strings.TrimSpace(o.Endpoint)
	// metrics_exporter is the SAME enum with the same two kinds of variant, and it rendered
	// as a bare string unconditionally — so `metrics_exporter = "otlp-http"` produced the
	// very config the binary rejects with "expected struct variant". Measured 2026-08-18
	// against codex-cli 0.147.0: bare statsig LOADS, bare otlp-http does NOT, and the table
	// form with protocol + endpoint LOADS.
	if s := strings.TrimSpace(o.MetricsExporter); s != "" {
		if v := exporterValue(s, endpoint, strings.TrimSpace(o.Protocol)); v != nil {
			m["metrics_exporter"] = v
		}
	}
	// An authored endpoint with no exporter id has nowhere to nest (there is no flat
	// otel.endpoint key) — default the LOGS exporter to otlp-http so the operator's
	// collector pin actually renders, instead of being silently dropped. An operator who
	// wants otlp-grpc sets the exporter explicitly.
	protocol := strings.TrimSpace(o.Protocol)
	logsExporter := strings.TrimSpace(o.Exporter)
	if logsExporter == "" && endpoint != "" {
		logsExporter = OTELExporterOTLPHTTP
	}
	m["exporter"] = exporterValue(logsExporter, endpoint, protocol)
	if te := strings.TrimSpace(o.TraceExporter); te != "" {
		m["trace_exporter"] = exporterValue(te, endpoint, protocol)
	}
	// Drop an empty exporter slot (exporterValue returns nil for an unset/none exporter
	// with no endpoint, so the bare id is not emitted as an empty value).
	if m["exporter"] == nil {
		delete(m, "exporter")
	}
	return m
}

// exporterValue renders an exporter slot in the shape the variant requires: a table for
// the struct variants (otlp-http, otlp-grpc) and a bare string for the unit ones (none,
// statsig). Returns nil when nothing is meaningfully set.
//
// protocol defaults to binary on a struct variant: otlp-http REQUIRES the field, and
// binary is what the CLI actually posts (measured Content-Type: application/x-protobuf).
// Rendering it for otlp-grpc too is harmless — grpc accepts it — and keeps one shape.
func exporterValue(exporter, endpoint, protocol string) any {
	id := strings.TrimSpace(exporter)
	if id == "" {
		return nil
	}
	if !structVariantExporter(id) {
		return id
	}
	// A struct variant with no endpoint has no renderable form. Authoring refuses this
	// combination (see authoring.go); rendering nil here means a caller that bypassed
	// validation emits NO exporter key rather than a config Codex cannot load.
	if endpoint == "" {
		return nil
	}
	p := strings.TrimSpace(protocol)
	if p == "" {
		p = OTELProtocolBinary
	}
	return map[string]any{id: map[string]any{"endpoint": endpoint, "protocol": p}}
}

// mcpServersToMap projects the MCP allowlist to the [mcp_servers.<name>] table set. An
// EMPTY input renders an empty map (BurntSushi emits "[mcp_servers]" — the present-but-
// empty LOCKDOWN that disables all MCP servers). Each entry renders identity = { command }
// or identity = { url } (the verified requirements form).
func mcpServersToMap(servers []MCPServer) map[string]any {
	out := make(map[string]any, len(servers))
	for _, s := range servers {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		if s.MatcherForm {
			continue // observed matcher-form identities are not authorable/renderable.
		}
		identity := map[string]any{}
		if c := strings.TrimSpace(s.Command); c != "" {
			identity["command"] = c
		} else if u := strings.TrimSpace(s.URL); u != "" {
			identity["url"] = u
		}
		out[name] = map[string]any{"identity": identity}
	}
	return out
}

func remoteSandboxConfigsToMaps(configs []RemoteSandboxConfig) []map[string]any {
	out := make([]map[string]any, 0, len(configs))
	for _, c := range configs {
		m := map[string]any{}
		if len(c.HostnamePatterns) > 0 {
			m["hostname_patterns"] = toAnySlice(sortedStrings(c.HostnamePatterns))
		}
		if len(c.AllowedSandboxModes) > 0 {
			m["allowed_sandbox_modes"] = toAnySlice(sortedStrings(c.AllowedSandboxModes))
		}
		out = append(out, m)
	}
	return out
}

func marketplacesToMap(req MarketplacesRequirement) map[string]any {
	m := map[string]any{"restrict_to_allowed_sources": req.RestrictToAllowedSources}
	if len(req.AllowedSources) > 0 {
		srcs := map[string]any{}
		for _, name := range sortedMapKeys(req.AllowedSources) {
			src := req.AllowedSources[name]
			sm := map[string]any{}
			if s := strings.TrimSpace(src.Source); s != "" {
				sm["source"] = s
			}
			if s := strings.TrimSpace(src.URL); s != "" {
				sm["url"] = s
			}
			if s := strings.TrimSpace(src.Ref); s != "" {
				sm["ref"] = s
			}
			if s := strings.TrimSpace(src.HostPattern); s != "" {
				sm["host_pattern"] = s
			}
			if s := strings.TrimSpace(src.Path); s != "" {
				sm["path"] = s
			}
			srcs[name] = sm
		}
		m["allowed_sources"] = srcs
	}
	return m
}

func prefixRulesToMaps(rules []PrefixRule) []map[string]any {
	out := make([]map[string]any, 0, len(rules))
	for _, r := range rules {
		pm := make([]map[string]any, 0, len(r.Pattern))
		for _, tok := range r.Pattern {
			tm := map[string]any{}
			if s := strings.TrimSpace(tok.Token); s != "" {
				tm["token"] = s
			}
			if len(tok.AnyOf) > 0 {
				tm["any_of"] = toAnySlice(sortedStrings(tok.AnyOf))
			}
			pm = append(pm, tm)
		}
		rm := map[string]any{"pattern": pm}
		if s := strings.TrimSpace(r.Decision); s != "" {
			rm["decision"] = s
		}
		if s := strings.TrimSpace(r.Justification); s != "" {
			rm["justification"] = s
		}
		out = append(out, rm)
	}
	return out
}

// toAnySlice converts a []string to a []any so a TOML array (incl. the empty "[]"
// lockdown form) renders. A nil input yields a non-nil empty []any so the caller's
// three-state pointer still renders "[]".
func toAnySlice(ss []string) []any {
	out := make([]any, 0, len(ss))
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

func boolMap(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[strings.TrimSpace(k)] = v
	}
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sortedMapKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
