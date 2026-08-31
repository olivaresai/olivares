// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Connector vocabulary for the access graph + findings.
const (
	// originManagedConfig is the OriginKind for a Codex managed-config policy edge: the
	// managed scope (a host / org-distributed Codex policy) that GRANTS a capability to
	// the agents running under it. It is the Codex sibling of managedsettings'
	// "managed_policy" origin; module III reconciles it onto the PERMITTED side.
	originManagedConfig = "codex_managed_config"
	// resMCPServer is the ResourceKind for an allowed Codex MCP server.
	resMCPServer = "codex.mcp_server"
	// resNetworkDomain is the ResourceKind for an allowed Codex egress domain.
	resNetworkDomain = "codex.network_domain"
	// findingKindDrift marks a PERMITTED-policy vs OBSERVED-config divergence (shared
	// vocabulary with managedsettings so the access-map treats Codex drift uniformly).
	findingKindDrift = "policy_drift"
)

// drift is one detected divergence between the authored policy and a live file.
type drift struct {
	severity model.Severity
	key      string
	title    string
}

// permittedEdges emits one PERMITTED edge per allowed MCP server and per allowed egress
// domain, so module III's PERMITTED side reflects what the Codex managed policy grants
// the fleet. The edges come from the AUTHORED intent when configured (what the org MEANS
// to permit), else from the LIVE files (inventory what is in force). Mode is Unknown —
// honest: an MCP server's tools and a domain's traffic are not classifiable R/W from the
// allowlist alone (the connector never guesses).
func permittedEdges(scope string, mcp []MCPServer, domains []string, at time.Time) []model.EdgeObservation {
	out := make([]model.EdgeObservation, 0, len(mcp)+len(domains))
	for _, s := range mcp {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		out = append(out, model.EdgeObservation{
			OriginKind:   originManagedConfig,
			OriginRef:    scope,
			ResourceKind: resMCPServer,
			ResourceRef:  name,
			Mode:         model.ModeUnknown,
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   at,
		})
	}
	for _, d := range domains {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		out = append(out, model.EdgeObservation{
			OriginKind:   originManagedConfig,
			OriginRef:    scope,
			ResourceKind: resNetworkDomain,
			ResourceRef:  d,
			Mode:         model.ModeUnknown,
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   at,
		})
	}
	return out
}

// requirementsDrift compares the authored requirements against the live requirements.toml
// and returns a finding per divergence. The dangerous escape hatches — an unconstrained
// sandbox/approval policy, remote control not disabled, the MCP lockdown not in force —
// are HIGH: they are exactly the constraints that, when absent, leave a developer free to
// do what the org forbade (Codex CLAMPS a conflicting user value, but only if the
// constraint is actually deployed — an absent constraint clamps nothing). Only fields the
// expected policy ASSERTS are checked (an unset expectation is not drift).
func requirementsDrift(scope string, expected Requirements, live requirementsWire, md toml.MetaData, at time.Time) []model.FindingReport {
	var drifts []drift
	add := func(sev model.Severity, key, title string) { drifts = append(drifts, drift{sev, key, title}) }

	// allowed_approval_policies — absent leaves the user free to pick "never" (no approvals);
	// a host that ALLOWS extra policies the org excluded is the escape (HIGH).
	if len(expected.AllowedApprovalPolicies) > 0 {
		allowlistDrift(add, "allowed_approval_policies", "approval-policy",
			isDefined(md, "allowed_approval_policies"), expected.AllowedApprovalPolicies, live.AllowedApprovalPolicies,
			"a user may select any approval policy, including 'never'", model.SeverityHigh, model.SeverityHigh)
	}
	// allowed_sandbox_modes — absent leaves danger-full-access available (--yolo); a host
	// that ALLOWS danger-full-access the org excluded is the escape (HIGH).
	if len(expected.AllowedSandboxModes) > 0 {
		allowlistDrift(add, "allowed_sandbox_modes", "sandbox-mode",
			isDefined(md, "allowed_sandbox_modes"), expected.AllowedSandboxModes, live.AllowedSandboxModes,
			"a user may select danger-full-access / --yolo", model.SeverityHigh, model.SeverityHigh)
	}
	// allowed_web_search_modes — three-state (the [] lockdown allows only "disabled").
	// "disabled" is ALWAYS implicitly allowed (policy.go), so an authored [] lockdown and a
	// live ["disabled"] are SEMANTICALLY IDENTICAL: normalize "disabled" into BOTH sets
	// before comparing so the equivalence does not surface as spurious drift. This implicit
	// member is unique to web-search — do NOT apply it to the other allowlists.
	if expected.AllowedWebSearchModes != nil {
		allowlistDrift(add, "allowed_web_search_modes", "web-search",
			isDefined(md, "allowed_web_search_modes"),
			withDisabled(*expected.AllowedWebSearchModes), withDisabled(live.AllowedWebSearchModes),
			"a user may run live web search", model.SeverityMedium, model.SeverityMedium)
	}
	// allowed_approvals_reviewers — absent (or a host that additionally allows "user")
	// lets the developer skip the automatic-review subagent.
	if len(expected.AllowedApprovalsReviewers) > 0 {
		allowlistDrift(add, "allowed_approvals_reviewers", "approvals-reviewer",
			isDefined(md, "allowed_approvals_reviewers"), expected.AllowedApprovalsReviewers, live.AllowedApprovalsReviewers,
			"automatic review may be skipped", model.SeverityMedium, model.SeverityMedium)
	}
	// allowed_permission_profiles — three-state table: omitted/false denies a profile,
	// including future Codex profiles. Missing deployment is MEDIUM; broadening to
	// :danger-full-access is HIGH.
	if expected.AllowedPermissionProfiles != nil {
		permissionProfilesDrift(add, expected.AllowedPermissionProfiles, live.AllowedPermissionProfiles, isDefined(md, "allowed_permission_profiles"))
	}
	if want := strings.TrimSpace(expected.EnforceResidency); want != "" {
		if !isDefined(md, "enforce_residency") || strings.TrimSpace(live.EnforceResidency) != want {
			add(model.SeverityMedium, "enforce_residency", "residency requirement drift (expected "+want+")")
		}
	}
	if len(expected.WindowsAllowedSandboxImplementations) > 0 {
		var liveImpls []string
		if live.Windows != nil {
			liveImpls = live.Windows.AllowedSandboxImplementations
		}
		allowlistDrift(add, "windows.allowed_sandbox_implementations", "Windows sandbox-implementation",
			isDefined(md, "windows", "allowed_sandbox_implementations"), expected.WindowsAllowedSandboxImplementations, liveImpls,
			"Windows sandbox implementation constraint is not deployed", model.SeverityMedium, model.SeverityMedium)
	}
	if len(expected.RemoteSandboxConfigs) > 0 {
		remoteSandboxDrift(add, expected.RemoteSandboxConfigs, remoteSandboxConfigsFromWire(live.RemoteSandboxConfigs), isDefined(md, "remote_sandbox_config"))
	}
	// allow_remote_control = false — only false is meaningful (disables device remote
	// control). HIGH like Claude's disableRemoteControl: a session driven from another
	// device relays I/O outside the governed transport.
	if expected.AllowRemoteControl != nil && !*expected.AllowRemoteControl {
		if live.AllowRemoteControl == nil || *live.AllowRemoteControl {
			add(model.SeverityHigh, "allow_remote_control", "device remote control is NOT disabled on host (a session may be driven from another device, relaying I/O outside the governed transport — org policy requires allow_remote_control=false)")
		}
	}
	// allow_appshots = false.
	if expected.AllowAppshots != nil && !*expected.AllowAppshots {
		if live.AllowAppshots == nil || *live.AllowAppshots {
			add(model.SeverityMedium, "allow_appshots", "Appshots are NOT disabled on host (org policy requires allow_appshots=false)")
		}
	}
	if expected.AllowLockedComputerUse != nil && !*expected.AllowLockedComputerUse {
		if live.ComputerUse == nil || live.ComputerUse.AllowLockedComputerUse == nil || *live.ComputerUse.AllowLockedComputerUse {
			add(model.SeverityMedium, "computer_use.allow_locked_computer_use", "locked computer use is NOT disabled on host (org policy requires computer_use.allow_locked_computer_use=false)")
		}
	}
	// allow_managed_hooks_only = true — hook supply-chain lockdown (requirements-only).
	//
	// HIGH, like allow_remote_control and the marketplace gate above, and for a reason
	// measured rather than assumed (codex-cli 0.145.0): on Codex a HOOK IS THE
	// POLICY ENFORCEMENT POINT — a PreToolUse hook command is what vetoes a tool call. A
	// host that has not locked the hook supply chain lets user/project/plugin hooks load
	// alongside the managed one, so the enforcing hook can be shadowed or displaced. That
	// is an escape hatch left open, which is exactly what this package's own doctrine
	// grades HIGH; MEDIUM also kept it OFF the record, because sub-HIGH findings are not
	// persisted (modules/security/anomaly.go), so the operator was never told the PEP
	// could be bypassed.
	if expected.AllowManagedHooksOnly && !live.AllowManagedHooksOnly {
		add(model.SeverityHigh, "allow_managed_hooks_only", "managed-hooks-only is NOT enforced on host (user/project/plugin hooks load — the hook supply-chain is unlocked, and on Codex a hook IS the enforcement point)")
	}
	// [permissions.filesystem] deny_read — every authored secret-read deny rule must be present.
	if len(expected.DenyRead) > 0 {
		for _, p := range missingStrings(expected.DenyRead, live.denyRead()) {
			add(model.SeverityMedium, "permissions.filesystem.deny_read", "secret-read deny rule missing on host: "+p)
		}
	}
	// [features] pins — each authored pin must be enforced with the same value.
	for _, name := range sortedFeatureKeys(expected.Features) {
		want := expected.Features[name]
		got, ok := live.Features[name]
		if !ok || got != want {
			add(model.SeverityMedium, "features."+name, "feature pin not enforced on host: "+name+" (requirement pins "+boolWord(want)+")")
		}
	}
	// [mcp_servers] allowlist — three-state; absent lockdown is HIGH (any MCP allowed).
	if expected.AllowedMCPServers != nil {
		liveServers := liveMCPServers(live.MCPServers)
		switch {
		case !isDefined(md, "mcp_servers"):
			sev, detail := model.SeverityMedium, "MCP server allowlist is NOT enforced on host (a user may enable unapproved MCP servers)"
			if len(*expected.AllowedMCPServers) == 0 {
				sev, detail = model.SeverityHigh, "MCP server LOCKDOWN (empty [mcp_servers]) is NOT enforced on host — any MCP server may be enabled (org policy requires complete lockdown)"
			}
			add(sev, "mcp_servers", detail)
		default:
			mcpAllowlistDrift(add, *expected.AllowedMCPServers, liveServers)
		}
	}
	// default_permissions.
	if dp := strings.TrimSpace(expected.DefaultPermissions); dp != "" && strings.TrimSpace(live.DefaultPermissions) != dp {
		add(model.SeverityMedium, "default_permissions", "default_permissions drift (expected "+dp+")")
	}
	// guardian_policy_config — presence (the automatic-review policy text is opaque).
	if strings.TrimSpace(expected.GuardianPolicyConfig) != "" && strings.TrimSpace(live.GuardianPolicyConfig) == "" {
		add(model.SeverityMedium, "guardian_policy_config", "automatic-review (guardian) policy is NOT configured on host")
	}
	if expected.Marketplaces != nil {
		marketplacesDrift(add, expected.Marketplaces, marketplacesFromWire(live.Marketplaces), isDefined(md, "marketplaces"))
	}
	if len(expected.PrefixRules) > 0 {
		prefixRulesDrift(add, expected.PrefixRules, prefixRulesFromWire(live.Rules), isDefined(md, "rules", "prefix_rules"))
	}
	if expected.Network.hasAny() {
		drifts = append(drifts, networkDrift(expected.Network, live.ExperimentalNetwork)...)
	}

	return toFindings(scope, drifts, at)
}

// managedConfigDrift compares the authored managed defaults against the live
// managed_config.toml. A missing DEFAULT is a WEAKER posture than a missing requirement
// (the user can change a default in-session), so the scalar defaults drift LOW; the
// security-relevant network/telemetry defaults drift MEDIUM, and a managed default that
// EXPORTS RAW PROMPTS is HIGH (a minimal-data violation the org explicitly pinned off).
func managedConfigDrift(scope string, expected ManagedConfig, live managedConfigWire, md toml.MetaData, at time.Time) []model.FindingReport {
	var drifts []drift
	add := func(sev model.Severity, key, title string) { drifts = append(drifts, drift{sev, key, title}) }

	if want := strings.TrimSpace(expected.ApprovalPolicy); want != "" {
		got, granular := approvalPolicyScalar(live.ApprovalPolicy)
		if granular || got != want {
			add(model.SeverityLow, "approval_policy", "managed default approval_policy drift (expected "+want+")")
		}
	}
	if want := strings.TrimSpace(expected.SandboxMode); want != "" && strings.TrimSpace(live.SandboxMode) != want {
		add(model.SeverityLow, "sandbox_mode", "managed default sandbox_mode drift (expected "+want+")")
	}
	if want := strings.TrimSpace(expected.WebSearch); want != "" {
		got, table := webSearchScalar(live.WebSearch)
		if table || got != want {
			add(model.SeverityLow, "web_search", "managed default web_search drift (expected "+want+")")
		}
	}
	// [sandbox_workspace_write] network_access — only a live TRUE diverges from a pinned
	// FALSE (an absent value defaults to no-network, which already meets the intent).
	if expected.NetworkAccess != nil && !*expected.NetworkAccess {
		if na := live.networkAccess(); na != nil && *na {
			add(model.SeverityMedium, "sandbox_workspace_write.network_access", "managed default ENABLES network egress (sandbox_workspace_write.network_access=true) — org policy pins it off")
		}
	}
	// experimental_network egress posture.
	if expected.Network.hasAny() {
		drifts = append(drifts, networkDrift(expected.Network, live.ExperimentalNetwork)...)
	}
	// [otel] telemetry pins.
	if expected.OTEL.hasAny() {
		drifts = append(drifts, otelDrift(expected.OTEL, live.OTEL)...)
	}

	return toFindings(scope, drifts, at)
}

// networkDrift checks the experimental_network egress posture.
func networkDrift(expected *NetworkConfig, live *expNetworkWire) []drift {
	var drifts []drift
	add := func(sev model.Severity, key, title string) { drifts = append(drifts, drift{sev, key, title}) }
	if expected.Enabled != nil {
		liveEnabled := false
		liveEnabledSet := false
		if live != nil && live.Enabled != nil {
			liveEnabled, liveEnabledSet = *live.Enabled, true
		}
		switch {
		case !*expected.Enabled && liveEnabledSet && liveEnabled:
			add(model.SeverityHigh, "experimental_network.enabled", "experimental_network.enabled=true opens egress on host while org policy pins enabled=false")
		case *expected.Enabled && (!liveEnabledSet || !liveEnabled):
			add(model.SeverityLow, "experimental_network.enabled", "experimental_network.enabled is not enabled on host (more restrictive than authored)")
		}
	}
	if expected.ManagedAllowedDomainsOnly {
		if live == nil || live.ManagedAllowedDomainsOnly == nil || !*live.ManagedAllowedDomainsOnly {
			add(model.SeverityMedium, "experimental_network.managed_allowed_domains_only", "egress is NOT locked to the managed allowlist on host (experimental_network.managed_allowed_domains_only not in force — exfiltration surface)")
		}
	}
	var liveAllowed, liveDenied []string
	if live != nil {
		liveAllowed, liveDenied = live.AllowedDomains, live.DeniedDomains
	}
	if len(expected.AllowedDomains) > 0 && !sameStringSet(expected.AllowedDomains, liveAllowed) {
		add(model.SeverityMedium, "experimental_network.allowed_domains", "egress allowlist on host drifts from the authored allowed_domains")
	}
	for _, d := range missingStrings(expected.DeniedDomains, liveDenied) {
		add(model.SeverityMedium, "experimental_network.denied_domains", "egress denylist entry missing on host: "+d)
	}
	if expected.HTTPPort != nil {
		if live == nil || live.HTTPPort == nil || *live.HTTPPort != *expected.HTTPPort {
			add(model.SeverityMedium, "experimental_network.http_port", "experimental_network.http_port drift (expected "+strconv.Itoa(*expected.HTTPPort)+")")
		}
	}
	if expected.SocksPort != nil {
		if live == nil || live.SocksPort == nil || *live.SocksPort != *expected.SocksPort {
			add(model.SeverityMedium, "experimental_network.socks_port", "experimental_network.socks_port drift (expected "+strconv.Itoa(*expected.SocksPort)+")")
		}
	}
	if len(expected.UnixSockets) > 0 {
		var liveSockets []string
		if live != nil {
			liveSockets = live.UnixSockets
		}
		if extras := setExtras(liveSockets, expected.UnixSockets); len(extras) > 0 {
			add(model.SeverityMedium, "experimental_network.unix_sockets", "host experimental_network.unix_sockets additionally allows "+strings.Join(extras, ", "))
		} else if !sameStringSet(expected.UnixSockets, liveSockets) {
			add(model.SeverityLow, "experimental_network.unix_sockets", "host experimental_network.unix_sockets is narrower than the authored set")
		}
	}
	if expected.AllowLocalBinding != nil {
		if live == nil || live.AllowLocalBinding == nil || *live.AllowLocalBinding != *expected.AllowLocalBinding {
			add(model.SeverityMedium, "experimental_network.allow_local_binding", "experimental_network.allow_local_binding drift (expected "+boolWord(*expected.AllowLocalBinding)+")")
		}
	}
	return drifts
}

// otelDrift checks the [otel] telemetry pins. log_user_prompt=true on the host is HIGH:
// the managed default would export RAW USER PROMPTS to telemetry, the minimal-data
// violation the org pinned off.
func otelDrift(expected *OTELConfig, live map[string]any) []drift {
	var drifts []drift
	add := func(sev model.Severity, key, title string) { drifts = append(drifts, drift{sev, key, title}) }
	liveOn := otelExporterName(live) != "" && otelExporterName(live) != OTELExporterNone ||
		otelTraceExporterName(live) != "" && otelTraceExporterName(live) != OTELExporterNone
	// Telemetry-not-configured and endpoint-mismatch are mutually exclusive: if the host
	// emits no telemetry at all, report THAT (the endpoint check below would double-report
	// the same root cause); only when telemetry IS on (or the org did not pin it on) do we
	// assert the endpoint points at the authored collector.
	switch {
	case expected.telemetryOn() && !liveOn:
		add(model.SeverityMedium, "otel.exporter", "managed telemetry default is NOT configured on host — Codex may emit no sanctioned OTEL signal")
	default:
		if ep := strings.TrimSpace(expected.Endpoint); ep != "" && !otelEndpointPresent(live, ep) {
			add(model.SeverityMedium, "otel.exporter.endpoint", "managed telemetry endpoint on host does not point at the authored collector — the sanctioned OTEL signal may flow elsewhere (or nowhere)")
		}
	}
	// log_user_prompt: only a live TRUE diverges from a pinned FALSE (absent defaults to false).
	if expected.LogUserPrompt != nil && !*expected.LogUserPrompt {
		if v, present := otelLogUserPrompt(live); present && v {
			add(model.SeverityHigh, "otel.log_user_prompt", "managed telemetry EXPORTS RAW USER PROMPTS on host (otel.log_user_prompt=true) — org policy pins it off (minimal-data)")
		}
	}
	return drifts
}

// requirementsAbsence reports that the host's system-tier requirements.toml is missing or
// unreadable/invalid. It is HONEST about the higher precedence tiers (precedence.go): the
// cloud-managed (ChatGPT Business/Enterprise) and macOS MDM requirements sit ABOVE the
// file and cannot be observed from here, so a host governed only by those is NOT
// "ungoverned" — it is unverifiable-from-here. Severity is HIGH when the operator AUTHORED
// requirements that the system tier does not carry (clear drift: the authored constraints
// are not deployed), else MEDIUM (the system tier is empty; cloud/MDM may still govern).
func requirementsAbsence(scope, reason string, authored bool, at time.Time) model.FindingReport {
	sev := model.SeverityMedium
	if authored {
		sev = model.SeverityHigh
	}
	return model.FindingReport{
		Kind:        findingKindDrift,
		Severity:    sev,
		SubjectKind: originManagedConfig,
		SubjectRef:  scope,
		Title:       "Codex requirements.toml (system tier) " + reason + " — host is not constrained by the verifiable requirements layer (cloud-managed/MDM requirements, higher precedence, are not observable from here)",
		DetailHash:  redact.Hash(scope + "|requirements-absent|" + reason),
		OccurredAt:  at,
	}
}

// managedConfigAbsence reports that the host's system-tier managed_config.toml is missing
// or invalid. It is emitted only when the operator authored managed defaults (an empty
// managed-defaults state is normal, not a finding), and is MEDIUM — a managed DEFAULT is a
// weaker posture than a missing requirement (the user can set values themselves anyway).
func managedConfigAbsence(scope, reason string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingKindDrift,
		Severity:    model.SeverityMedium,
		SubjectKind: originManagedConfig,
		SubjectRef:  scope,
		Title:       "Codex managed_config.toml (system tier) " + reason + " — the authored managed defaults are not deployed to the host",
		DetailHash:  redact.Hash(scope + "|managed-config-absent|" + reason),
		OccurredAt:  at,
	}
}

// toFindings maps drifts to minimal-data FindingReports (the detail is hashed, never the
// raw key/title — the same minimal-data posture as managedsettings).
func toFindings(scope string, drifts []drift, at time.Time) []model.FindingReport {
	out := make([]model.FindingReport, 0, len(drifts))
	for _, d := range drifts {
		out = append(out, model.FindingReport{
			Kind:        findingKindDrift,
			Severity:    d.severity,
			SubjectKind: originManagedConfig,
			SubjectRef:  scope,
			Title:       d.title,
			DetailHash:  redact.Hash(scope + "|" + d.key + "|" + d.title),
			OccurredAt:  at,
		})
	}
	return out
}

// --- set / comparison helpers ----------------------------------------------------

// allowlistDrift evaluates an authored allowlist CONSTRAINT against the live set. A
// constraint is weakened in two ways, graded differently:
//   - NOT enforced at all (absent) -> absentSev, with the absent-case explanation.
//   - enforced but BROADER (the host additionally allows values the org excluded — the
//     escape) -> broaderSev, naming the extra values.
//
// A host that is merely NARROWER than authored (more restrictive — not a security gap) is
// surfaced LOW. label is the human noun (e.g. "sandbox-mode"); absentWhy completes the
// sentence "... constraint is NOT enforced on host (<absentWhy>)".
func allowlistDrift(add func(model.Severity, string, string), key, label string, defined bool, authored, live []string, absentWhy string, absentSev, broaderSev model.Severity) {
	if !defined {
		add(absentSev, key, label+" constraint is NOT enforced on host ("+absentWhy+")")
		return
	}
	if extras := setExtras(live, authored); len(extras) > 0 {
		add(broaderSev, key, "host "+label+" allowlist additionally allows "+strings.Join(extras, ", ")+" — value(s) the org excluded")
		return
	}
	if !sameStringSet(authored, live) {
		add(model.SeverityLow, key, "host "+label+" allowlist is narrower than the authored set (more restrictive than intended)")
	}
}

// withDisabled returns the set with "disabled" always present. "disabled" is the one
// web-search mode that stays implicitly allowed (policy.go), so an authored [] lockdown and
// a live ["disabled"] denote the same set; normalizing both before comparison keeps that
// equivalence from surfacing as spurious drift. ONLY web-search has an implicit member.
func withDisabled(ss []string) []string {
	for _, s := range ss {
		if strings.TrimSpace(s) == WebSearchDisabled {
			return ss
		}
	}
	return append(append([]string(nil), ss...), WebSearchDisabled)
}

// setExtras returns the live entries that are NOT in the authored set (deduped, in live
// order) — the values a host's allowlist permits beyond what the org authored.
func setExtras(live, authored []string) []string {
	a := stringSet(authored)
	seen := map[string]struct{}{}
	var extra []string
	for _, v := range live {
		t := strings.TrimSpace(v)
		if t == "" {
			continue
		}
		if _, ok := a[t]; ok {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		extra = append(extra, t)
	}
	return extra
}

// sameStringSet reports whether two string slices denote the same set (order- and
// duplicate-insensitive, trimmed).
func sameStringSet(a, b []string) bool {
	sa, sb := stringSet(a), stringSet(b)
	if len(sa) != len(sb) {
		return false
	}
	for k := range sa {
		if _, ok := sb[k]; !ok {
			return false
		}
	}
	return true
}

func stringSet(ss []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		if t := strings.TrimSpace(s); t != "" {
			out[t] = struct{}{}
		}
	}
	return out
}

// missingStrings returns the authored entries ABSENT from the live list, in authored
// order (the deny-rule/denylist drift signal).
func missingStrings(authored, live []string) []string {
	liveSet := stringSet(live)
	var missing []string
	for _, a := range authored {
		if t := strings.TrimSpace(a); t != "" {
			if _, ok := liveSet[t]; !ok {
				missing = append(missing, t)
			}
		}
	}
	return missing
}

// mcpKey is one MCP server's stable identity for order-independent set comparison
// (name + identity-kind + identity-value).
func mcpKey(s MCPServer) string {
	kind, val := s.identityKind()
	return strings.TrimSpace(s.Name) + "\x00" + kind + "\x00" + val
}

// sameMCPSet reports whether two MCP allowlists denote the same name+identity set.
func sameMCPSet(a, b []MCPServer) bool {
	sa, sb := mcpKeySet(a), mcpKeySet(b)
	if len(sa) != len(sb) {
		return false
	}
	for k := range sa {
		if _, ok := sb[k]; !ok {
			return false
		}
	}
	return true
}

func mcpKeySet(servers []MCPServer) map[string]struct{} {
	out := make(map[string]struct{}, len(servers))
	for _, s := range servers {
		out[mcpKey(s)] = struct{}{}
	}
	return out
}

// liveMCPServers projects the live [mcp_servers] map into []MCPServer (deterministic
// order by name), reading the identity = { command|url } form (the config.toml flat
// command/url is tolerated for observability).
func liveMCPServers(m map[string]mcpServerWire) []MCPServer {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]MCPServer, 0, len(names))
	for _, name := range names {
		w := m[name]
		out = append(out, MCPServer{Name: name, Command: w.command(), URL: w.url(), MatcherForm: w.matcherForm()})
	}
	return out
}

func permissionProfilesDrift(add func(model.Severity, string, string), authored *map[string]bool, live map[string]bool, defined bool) {
	if !defined {
		add(model.SeverityMedium, "allowed_permission_profiles", "permission-profile allowlist is NOT enforced on host (constraint not deployed)")
		return
	}
	authAllowed, liveAllowed := allowedBoolKeys(*authored), allowedBoolKeys(live)
	// Report EVERY profile the host allows and the org did not. Reporting only the
	// first hid the rest, and since the extras are walked in sorted order it could
	// hide the HIGH one: a profile sorting before ":danger-full-access" took the
	// single slot and the escape hatch went unmentioned at MEDIUM.
	extras := setExtras(liveAllowed, authAllowed)
	for _, extra := range extras {
		sev := model.SeverityMedium
		if extra == ":danger-full-access" {
			sev = model.SeverityHigh
		}
		add(sev, "allowed_permission_profiles", "host permission-profile allowlist additionally allows "+extra+" — profile the org excluded")
	}
	if len(extras) > 0 {
		// The allowlist is WIDER than authored; "narrower" below would misdescribe it.
		return
	}
	if len(authAllowed) > 0 && !sameStringSet(authAllowed, liveAllowed) {
		add(model.SeverityLow, "allowed_permission_profiles", "host permission-profile allowlist is narrower than the authored set")
	}
}

func allowedBoolKeys(m map[string]bool) []string {
	var out []string
	for _, k := range sortedMapKeys(m) {
		if m[k] {
			out = append(out, strings.TrimSpace(k))
		}
	}
	return out
}

func remoteSandboxDrift(add func(model.Severity, string, string), authored, live []RemoteSandboxConfig, defined bool) {
	if !defined {
		add(model.SeverityMedium, "remote_sandbox_config", "remote sandbox constraints are NOT enforced on host")
		return
	}
	// Report per drifting entry in sorted-key order so output (and severity) never
	// depends on map iteration order; the entry count is operator-bounded.
	a, l := remoteSandboxSet(authored), remoteSandboxSet(live)
	for _, key := range sortedMapKeys(l) {
		liveModes := l[key]
		authModes, ok := a[key]
		if !ok {
			sev := model.SeverityMedium
			if _, danger := stringSet(liveModes)[SandboxDangerFull]; danger {
				sev = model.SeverityHigh
			}
			add(sev, "remote_sandbox_config", "host remote_sandbox_config has an unauthored hostname pattern set: "+displayPatternKey(key))
			continue
		}
		if extras := setExtras(liveModes, authModes); len(extras) > 0 {
			sev := model.SeverityMedium
			for _, extra := range extras {
				if extra == SandboxDangerFull {
					sev = model.SeverityHigh
				}
			}
			add(sev, "remote_sandbox_config", "host remote_sandbox_config broadens sandbox modes for "+displayPatternKey(key))
			continue
		}
		if !sameStringSet(authModes, liveModes) {
			add(model.SeverityMedium, "remote_sandbox_config", "host remote_sandbox_config differs for "+displayPatternKey(key))
		}
	}
	for _, key := range sortedMapKeys(a) {
		if _, ok := l[key]; !ok {
			add(model.SeverityMedium, "remote_sandbox_config", "authored remote_sandbox_config missing on host for "+displayPatternKey(key))
		}
	}
}

func remoteSandboxSet(configs []RemoteSandboxConfig) map[string][]string {
	out := make(map[string][]string, len(configs))
	for _, c := range configs {
		key := remoteSandboxKey(c.HostnamePatterns)
		if key == "" {
			continue
		}
		out[key] = sortedStrings(c.AllowedSandboxModes)
	}
	return out
}

func remoteSandboxKey(patterns []string) string {
	return strings.Join(sortedStrings(patterns), "\x00")
}

func displayPatternKey(key string) string {
	return strings.ReplaceAll(key, "\x00", ", ")
}

func mcpAllowlistDrift(add func(model.Severity, string, string), authored, live []MCPServer) {
	if names := matcherMCPNames(live); len(names) > 0 {
		add(model.SeverityInfo, "mcp_servers.matcher_identity", "MCP server identity uses matcher form; review manually — matcher semantics not modeled: "+strings.Join(names, ", "))
	}
	if !sameMCPSetWithMatchers(authored, live) {
		add(model.SeverityMedium, "mcp_servers", "MCP server allowlist on host drifts from the authored set")
	}
}

func matcherMCPNames(servers []MCPServer) []string {
	var names []string
	for _, s := range servers {
		if s.MatcherForm {
			names = append(names, strings.TrimSpace(s.Name))
		}
	}
	sort.Strings(names)
	return names
}

func sameMCPSetWithMatchers(authored, live []MCPServer) bool {
	authExact := mcpKeySet(authored)
	authNames := mcpNameSet(authored)
	liveComparable := make([]MCPServer, 0, len(live))
	for _, s := range live {
		if s.MatcherForm {
			if _, ok := authNames[strings.TrimSpace(s.Name)]; !ok {
				return false
			}
			continue
		}
		liveComparable = append(liveComparable, s)
	}
	liveExact := mcpKeySet(liveComparable)
	for key := range liveExact {
		if _, ok := authExact[key]; !ok {
			return false
		}
	}
	for _, s := range authored {
		if _, ok := liveExact[mcpKey(s)]; ok {
			continue
		}
		if hasMatcherName(live, strings.TrimSpace(s.Name)) {
			continue
		}
		return false
	}
	return true
}

func mcpNameSet(servers []MCPServer) map[string]struct{} {
	out := make(map[string]struct{}, len(servers))
	for _, s := range servers {
		if name := strings.TrimSpace(s.Name); name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

func hasMatcherName(servers []MCPServer, name string) bool {
	for _, s := range servers {
		if s.MatcherForm && strings.TrimSpace(s.Name) == name {
			return true
		}
	}
	return false
}

func marketplacesDrift(add func(model.Severity, string, string), authored, live *MarketplacesRequirement, defined bool) {
	if authored == nil {
		return
	}
	if authored.RestrictToAllowedSources && (!defined || live == nil || !live.RestrictToAllowedSources) {
		add(model.SeverityHigh, "marketplaces.restrict_to_allowed_sources", "Codex marketplace supply-chain gate is off on host (restrict_to_allowed_sources not enforced)")
		return
	}
	if live == nil {
		if len(authored.AllowedSources) > 0 {
			add(model.SeverityLow, "marketplaces.allowed_sources", "host marketplace allowed_sources is narrower than the authored set")
		}
		return
	}
	authKeys, liveKeys := sortedMapKeys(authored.AllowedSources), sortedMapKeys(live.AllowedSources)
	if extras := setExtras(liveKeys, authKeys); len(extras) > 0 {
		add(model.SeverityMedium, "marketplaces.allowed_sources", "host marketplace allowed_sources includes unauthored source name "+strings.Join(extras, ", "))
		return
	}
	for _, name := range authKeys {
		got, ok := live.AllowedSources[name]
		if !ok {
			add(model.SeverityLow, "marketplaces.allowed_sources", "host marketplace allowed_sources is narrower than the authored set")
			return
		}
		if marketplaceSourceKey(authored.AllowedSources[name]) != marketplaceSourceKey(got) {
			add(model.SeverityMedium, "marketplaces.allowed_sources."+name, "host marketplace allowed source "+name+" differs from the authored source")
			return
		}
	}
}

func marketplaceSourceKey(s MarketplaceSource) string {
	return strings.Join([]string{
		strings.TrimSpace(s.Source),
		strings.TrimSpace(s.URL),
		strings.TrimSpace(s.Ref),
		strings.TrimSpace(s.HostPattern),
		strings.TrimSpace(s.Path),
	}, "\x00")
}

func prefixRulesDrift(add func(model.Severity, string, string), authored, live []PrefixRule, defined bool) {
	if !defined {
		if len(authored) > 0 {
			add(prefixRulesMaxSeverity(authored), "rules.prefix_rules",
				strconv.Itoa(len(authored))+" authored Codex prefix rule(s) are not enforced on host (requirements [rules] absent)")
		}
		return
	}
	// Aggregate by count so the reported severity never depends on map iteration
	// order: any missing "forbidden" rule is HIGH regardless of position.
	a, l := prefixRuleSet(authored), prefixRuleSet(live)
	missingForbidden, missingPrompt := 0, 0
	for key, decision := range a {
		if _, ok := l[key]; !ok {
			if strings.TrimSpace(decision) == "forbidden" {
				missingForbidden++
			} else {
				missingPrompt++
			}
		}
	}
	if missingForbidden > 0 {
		add(model.SeverityHigh, "rules.prefix_rules",
			strconv.Itoa(missingForbidden)+" authored forbidden Codex prefix rule(s) missing on host")
	}
	if missingPrompt > 0 {
		add(model.SeverityMedium, "rules.prefix_rules",
			strconv.Itoa(missingPrompt)+" authored prompt Codex prefix rule(s) missing on host")
	}
	extras := 0
	for key := range l {
		if _, ok := a[key]; !ok {
			extras++
		}
	}
	if extras > 0 {
		add(model.SeverityLow, "rules.prefix_rules",
			strconv.Itoa(extras)+" extra host Codex prefix rule(s) (stricter than authored)")
	}
}

// prefixRulesMaxSeverity is the severity of an entire authored rule set going
// unenforced: HIGH when any rule is "forbidden", MEDIUM otherwise.
func prefixRulesMaxSeverity(rules []PrefixRule) model.Severity {
	for _, r := range rules {
		if strings.TrimSpace(r.Decision) == "forbidden" {
			return model.SeverityHigh
		}
	}
	return model.SeverityMedium
}

func prefixRuleSet(rules []PrefixRule) map[string]string {
	out := make(map[string]string, len(rules))
	for _, r := range rules {
		key := prefixRuleKey(r)
		out[key] = strings.TrimSpace(r.Decision)
	}
	return out
}

func prefixRuleKey(r PrefixRule) string {
	var parts []string
	for _, tok := range r.Pattern {
		if s := strings.TrimSpace(tok.Token); s != "" {
			parts = append(parts, "token="+s)
			continue
		}
		parts = append(parts, "any_of="+strings.Join(sortedStrings(tok.AnyOf), ","))
	}
	return strings.Join(parts, "\x00") + "\x00decision=" + strings.TrimSpace(r.Decision)
}

// sortedFeatureKeys returns the feature names in deterministic order (stable findings).
func sortedFeatureKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// boolWord renders a bool as the word Codex uses in TOML.
func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
