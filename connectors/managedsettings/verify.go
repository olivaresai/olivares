// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Connector vocabulary for the access graph + findings.
const (
	// originManagedPolicy is the OriginKind for a managed-settings policy edge: the
	// managed scope (a host / org-distributed policy) that GRANTS a capability to
	// the agents running under it. Reconciles it onto the PERMITTED side.
	originManagedPolicy = "managed_policy"
	// resPermission is the ResourceKind for a Claude Code permission grant.
	resPermission = "claude.permission"
	// findingKindDrift marks a PERMITTED-policy vs OBSERVED-config divergence.
	findingKindDrift = "policy_drift"
)

// inferRuleMode infers the R/RW mode of a Claude Code permission rule from its
// tool prefix ("Read(/x)" → read, "Write(/x)" → write). An unrecognized or
// argument-less rule is mode-unknown (honest: a grant we can't classify is still
// inventoried, never guessed as R or W).
func inferRuleMode(rule string) model.AccessMode {
	tool := rule
	if i := strings.IndexByte(rule, '('); i >= 0 {
		tool = rule[:i]
	}
	switch strings.TrimSpace(tool) {
	case "Read", "Glob", "Grep", "LS", "NotebookRead", "WebFetch", "WebSearch":
		return model.ModeRead
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		return model.ModeWrite
	default:
		return model.ModeUnknown
	}
}

// permittedEdges emits one PERMITTED policy-grant edge per allow rule in the
// source policy, so module III's PERMITTED side reflects what the managed policy
// grants the fleet. Deny/ask rules are NOT edges — a negative permission needs the
// DENIED edge dimension (a tracked EdgeObservation contract change); their
// enforcement is surfaced as drift findings instead.
func permittedEdges(scope string, allow []string, at time.Time) []model.EdgeObservation {
	out := make([]model.EdgeObservation, 0, len(allow))
	for _, rule := range allow {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		out = append(out, model.EdgeObservation{
			OriginKind:   originManagedPolicy,
			OriginRef:    scope,
			ResourceKind: resPermission,
			ResourceRef:  rule,
			Mode:         inferRuleMode(rule),
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   at,
		})
	}
	return out
}

// drift is one detected divergence between the authored policy and the live config.
type drift struct {
	severity model.Severity
	title    string
	key      string
}

// driftFindings compares the governance-authored expected policy against the live
// managed-settings.json and returns a finding per divergence. The most dangerous
// gaps — bypass-permissions not disabled, org pinning not enforced, managed-MCP
// not locked — are high severity: they are exactly the constraints that, when
// absent, leave a developer free to do what the org forbade. Only fields the
// expected policy actually ASSERTS are checked (an unset expectation is not drift).
func driftFindings(scope string, expected Policy, live managedSettings, at time.Time) []model.FindingReport {
	var drifts []drift
	add := func(sev model.Severity, key, title string) { drifts = append(drifts, drift{sev, title, key}) }

	livePerm := msPermissions{}
	if live.Permissions != nil {
		livePerm = *live.Permissions
	}

	if expected.Permissions.DisableBypassPermissionsMode && !livePerm.bypassDisabled() {
		add(model.SeverityHigh, "disableBypassPermissionsMode", "bypassPermissions mode is NOT disabled on host (org policy requires it)")
	}
	if expected.Permissions.DisableAutoMode && !livePerm.autoDisabled() {
		add(model.SeverityMedium, "disableAutoMode", "auto mode is NOT disabled on host (org policy requires it)")
	}
	// forceLoginOrgUUID: the wire accepts a single string (pre-selects the org) or
	// an array (any listed org, VERIFIED 2026-06-10). The two forms are
	// DISTINCT postures, so drift compares form-aware: a host carrying the same
	// UUIDs in the other form has still drifted (the pre-selection/any-of semantics
	// the org authored are not in force).
	liveSingle, liveList, _ := forceLoginOrgFromRaw(live.ForceLoginOrgUUID)
	if expected.ForceLoginOrgUUID != "" && liveSingle != expected.ForceLoginOrgUUID {
		add(model.SeverityHigh, "forceLoginOrgUUID", "forceLoginOrgUUID is not pinned to the org (login federation not enforced)")
	}
	if len(expected.ForceLoginOrgUUIDs) > 0 && !sameStringSet(expected.ForceLoginOrgUUIDs, liveList) {
		add(model.SeverityHigh, "forceLoginOrgUUID", "forceLoginOrgUUID allowed-org set drifts from the authored list (login federation not enforced)")
	}
	if expected.ForceLoginMethod != "" && live.ForceLoginMethod != expected.ForceLoginMethod {
		add(model.SeverityMedium, "forceLoginMethod", "forceLoginMethod drift (expected "+expected.ForceLoginMethod+")")
	}
	if expected.AllowManagedMCPServersOnly && !live.AllowManagedMcpServersOnly {
		add(model.SeverityHigh, "allowManagedMcpServersOnly", "managed-MCP-only is NOT enforced (host may add unapproved MCP servers)")
	}
	// MCP server predicates (VERIFIED 2026-06-10). The allowlist mirrors the
	// marketplace allowlist: a host with no valid array is worst when the org
	// authored the `[]` complete lockdown (the host then allows ANY MCP server);
	// a present-but-different predicate set is Medium drift. Patterns compare as
	// AUTHORED (a glob is never expanded). Denylist entries missing on the host
	// mean a server the org blocked is still configurable there.
	if expected.AllowedMCPServers != nil {
		liveRules, livePresent := liveMCPRules(live.AllowedMcpServers)
		switch {
		case !livePresent:
			sev := model.SeverityMedium
			detail := "MCP server allowlist (allowedMcpServers) is NOT enforced on host (host may configure unapproved MCP servers)"
			if len(*expected.AllowedMCPServers) == 0 {
				sev = model.SeverityHigh
				detail = "MCP server LOCKDOWN ([]) is NOT enforced on host — any MCP server may be configured (org policy requires complete lockdown)"
			}
			add(sev, "allowedMcpServers", detail)
		case !sameMCPRuleSet(*expected.AllowedMCPServers, liveRules):
			add(model.SeverityMedium, "allowedMcpServers", "MCP server allowlist on host drifts from the authored predicate set")
		}
	}
	if len(expected.DeniedMCPServers) > 0 {
		liveDenied := liveMCPRuleList(live.DeniedMcpServers)
		for _, r := range missingMCPRules(expected.DeniedMCPServers, liveDenied) {
			add(model.SeverityMedium, "deniedMcpServers", "managed MCP denylist entry missing on host: "+describeMCPRule(r))
		}
	}
	if expected.AllowManagedPermissionRulesOnly && !live.AllowManagedPermissionRulesOnly {
		add(model.SeverityMedium, "allowManagedPermissionRulesOnly", "user/project permission rules are NOT locked to managed")
	}
	// Marketplace allowlist (B1). The authored allowlist is the ARRAY form; the
	// host must carry the SAME set. A host with no valid allowlist at all is the worst
	// case — and HIGH when the org authored the `[]` complete-lockdown (the host then
	// allows ANY marketplace the org forbade); a present-but-different set is Medium drift.
	if expected.StrictKnownMarketplaces != nil {
		liveEntries, livePresent := liveMarketplaces(live.StrictKnownMarketplaces)
		switch {
		case !livePresent:
			sev := model.SeverityMedium
			detail := "plugin-marketplace allowlist is NOT enforced on host (host may add unapproved marketplaces)"
			if len(*expected.StrictKnownMarketplaces) == 0 {
				sev = model.SeverityHigh
				detail = "marketplace LOCKDOWN ([]) is NOT enforced on host — any plugin marketplace may be added (org policy requires complete lockdown)"
			}
			add(sev, "strictKnownMarketplaces", detail)
		case !sameMarketplaceSet(*expected.StrictKnownMarketplaces, liveEntries):
			add(model.SeverityMedium, "strictKnownMarketplaces", "plugin-marketplace allowlist on host drifts from the authored set")
		}
	}
	// Marketplace blocklist: every authored blocked source must be present on the host
	// (a missing blocklist entry means the host can still fetch from a source the org
	// blocked) — mirrors the permissions.deny "missing on host" check.
	if len(expected.BlockedMarketplaces) > 0 {
		liveBlocked, _ := liveMarketplaces(live.BlockedMarketplaces)
		for _, m := range missingMarketplaceEntries(expected.BlockedMarketplaces, liveBlocked) {
			add(model.SeverityMedium, "blockedMarketplaces", "managed marketplace blocklist entry missing on host: "+describeMarketplace(m))
		}
	}
	// NET-NEW 2026-07 currency keys (VERIFIED 2026-07-03). disableSideloadFlags
	// only protects a policy that asserts marketplace/MCP lockdown; without that live
	// lockdown, there is no sideload bypass of a governed marketplace posture to report.
	if live.sideloadFlagRelevant() && !live.DisableSideloadFlags {
		add(model.SeverityHigh, "disableSideloadFlags", "plugin sideload flags are NOT rejected — strictKnownMarketplaces is bypassable per-run via --plugin-dir/--plugin-url/--agents/--mcp-config (documented bypass, v2.1.193+)")
	}
	// NET-NEW managed-only keys (A). The two sandbox lockdowns are HIGH — when
	// absent they leave the agent free to exfiltrate to any domain or read any secret
	// path; forceRemoteSettingsRefresh is HIGH (the host can start UNGOVERNED in the
	// brief unenforced window). The honest verified wire locations are checked.
	if expected.ForceRemoteSettingsRefresh && !live.ForceRemoteSettingsRefresh {
		add(model.SeverityHigh, "forceRemoteSettingsRefresh", "fail-closed startup is NOT enforced (host may start in the brief unenforced window without managed settings)")
	}
	if expected.AllowManagedDomainsOnly && !live.domainsLockdown() {
		add(model.SeverityHigh, "sandbox.network.allowManagedDomainsOnly", "egress is NOT locked to managed domains (sandbox may reach unapproved domains — exfiltration surface)")
	}
	if expected.AllowManagedReadPathsOnly && !live.readPathsLockdown() {
		add(model.SeverityHigh, "sandbox.filesystem.allowManagedReadPathsOnly", "filesystem reads are NOT locked to managed paths (sandbox may read unapproved secret paths)")
	}
	if expected.AllowManagedHooksOnly && !live.AllowManagedHooksOnly {
		add(model.SeverityMedium, "allowManagedHooksOnly", "user/project/plugin hooks are NOT restricted to managed (hook supply-chain not locked)")
	}
	if (live.domainsLockdown() || live.readPathsLockdown()) && !live.credentialsProtectionSet() {
		add(model.SeverityMedium, "sandbox.credentials", "sandbox.credentials is empty while sandbox lockdown is asserted — default read policy still allows ~/.aws/credentials and ~/.ssh (no built-in credential deny list)")
	}
	// NET-NEW: the PreToolUse PEP hook is the enforcement point. If the
	// authored policy distributes one but the host does not carry it, the host is
	// OBSERVED but NOT GOVERNED — the most consequential gap this surface can find, so
	// it drifts HIGH (the enforcement the org published is simply not in force).
	if hasPreToolUseHook(expected.Hooks) && !hasPreToolUseHook(live.Hooks) {
		add(model.SeverityHigh, "hooks.PreToolUse", "managed PreToolUse PEP hook is NOT distributed to host — agent actions are unenforced (observed, not governed)")
	}
	// The sanctioned OBSERVE path. When the authored policy turns
	// telemetry on, assert the live MANAGED env (1) also turns it on, and (2) points OTEL at
	// the SAME control-plane collector. Managed-settings env "cannot be overridden by users"
	// (VERIFIED 2026-06-20), so presence in the live managed env IS the non-overridable
	// assertion. Absence = the fleet may emit no sanctioned telemetry (a detection gap, not an
	// enforcement one) — Medium; an endpoint that DIVERGES from the authored Olivares
	// collector means the signal flows off-Olivares (or nowhere) — Medium. Only checked when
	// the operator asserts telemetry (an unset expectation is not drift).
	if envHasTelemetry(expected.Env) {
		if !envHasTelemetry(live.Env) {
			add(model.SeverityMedium, "env.CLAUDE_CODE_ENABLE_TELEMETRY", "managed telemetry env is NOT distributed to host — Claude Code is unobserved (no sanctioned OTEL signal)")
		} else if wantEP := strings.TrimSpace(expected.Env[EnvOTLPEndpoint]); wantEP != "" {
			if gotEP := strings.TrimSpace(live.Env[EnvOTLPEndpoint]); gotEP != wantEP {
				add(model.SeverityMedium, "env.OTEL_EXPORTER_OTLP_ENDPOINT", "managed telemetry endpoint on host diverges from the authored control-plane collector — the sanctioned OTEL signal may flow to a non-Olivares endpoint (or be dropped)")
			}
		}
	}
	// A non-default ANTHROPIC_BASE_URL routes inference to a custom endpoint and BYPASSES
	// server-managed-settings entirely (VERIFIED 2026-06-20). When the operator declares the
	// authorized gateway, a live managed env pinning a DIFFERENT base-URL is a posture finding
	// (inference left the authorized path, skipping both server-managed-settings and the
	// Olivares gateway) — High. An ABSENT live base-URL is NOT drift (direct api.anthropic.com
	// is the sanctioned path). The base-URL may embed a token, so only its presence+divergence
	// is reported and the detail is hashed — the URL itself is never emitted.
	if want := strings.TrimSpace(expected.AuthorizedGatewayBaseURL); want != "" {
		if got := strings.TrimSpace(live.Env[EnvBaseURL]); got != "" && got != want {
			add(model.SeverityHigh, "env.ANTHROPIC_BASE_URL", "host pins an ANTHROPIC_BASE_URL that diverges from the authorized gateway — inference is routed to an unauthorized endpoint, bypassing server-managed-settings and the Olivares gateway")
		}
	}
	if expected.AutoMode.hasAny() && !live.autoModeSet() {
		add(model.SeverityMedium, "autoMode", "auto-mode classifier trust configuration is NOT present on host (autoMode drift)")
	}
	if expected.StrictPluginOnlyCustomization && !live.strictPluginCustomizationSet() {
		add(model.SeverityMedium, "strictPluginOnlyCustomization", "user/project plugin customization is NOT restricted")
	}
	if dm := expected.Permissions.DefaultMode; dm != "" && livePerm.DefaultMode != dm {
		add(model.SeverityMedium, "permissions.defaultMode", "permission defaultMode drift (expected "+dm+")")
	}
	if expected.MinimumVersion != "" && live.MinimumVersion != expected.MinimumVersion {
		add(model.SeverityLow, "minimumVersion", "minimumVersion drift (expected "+expected.MinimumVersion+")")
	}
	// NET-NEW 2.1.17x keys (VERIFIED 2026-06-10). The required*Version hard
	// gates guarantee every other policy key is UNDERSTOOD by the client (an
	// out-of-range client exits at startup), so their absence re-opens the
	// old-client hole — Medium. fallbackModel compares as the ORDERED chain
	// (position carries meaning; it never merges across scopes). channelsEnabled
	// drifts Low: a host missing it is MORE restrictive (channels blocked), a
	// functional divergence rather than a security gap.
	if expected.RequiredMinimumVersion != "" && live.RequiredMinimumVersion != expected.RequiredMinimumVersion {
		add(model.SeverityMedium, "requiredMinimumVersion", "requiredMinimumVersion hard startup gate is NOT enforced (clients below the org floor may run)")
	}
	if expected.RequiredMaximumVersion != "" && live.RequiredMaximumVersion != expected.RequiredMaximumVersion {
		add(model.SeverityMedium, "requiredMaximumVersion", "requiredMaximumVersion hard startup gate is NOT enforced (clients above the org ceiling may run)")
	}
	if len(expected.FallbackModels) > 0 {
		liveModels, _ := fallbackModelsFromRaw(live.FallbackModel)
		if !sameStringChain(expected.FallbackModels, liveModels) {
			add(model.SeverityMedium, "fallbackModel", "fallbackModel chain drifts from the authored order (host may fall back to unapproved models)")
		}
	}
	if len(expected.PluginSuggestionMarketplaces) > 0 && !sameStringSet(expected.PluginSuggestionMarketplaces, live.PluginSuggestionMarketplaces) {
		add(model.SeverityMedium, "pluginSuggestionMarketplaces", "plugin-suggestion marketplace allowlist drifts from the authored set")
	}
	if expected.ChannelsEnabled && !live.ChannelsEnabled {
		add(model.SeverityLow, "channelsEnabled", "channelsEnabled is NOT set on host (channels the org enabled are blocked there — functional drift)")
	}
	if expected.ParentSettingsBehavior != "" && live.ParentSettingsBehavior != expected.ParentSettingsBehavior {
		add(model.SeverityMedium, "parentSettingsBehavior", "parentSettingsBehavior drift (expected "+expected.ParentSettingsBehavior+") — SDK/IDE parent-supplied settings may resolve differently than the org intends")
	}
	if expected.DisableBundledSkills && !live.DisableBundledSkills {
		add(model.SeverityMedium, "disableBundledSkills", "bundled skills are NOT disabled on host (host loads bundled executable surface the org disabled)")
	}
	// NET-NEW 2026 currency keys (VERIFIED 2026-06-16). disableRemoteControl drifts
	// HIGH: when the org disables Remote Control but the host does not, the fleet can drive
	// sessions from claude.ai/code or the Claude app, relaying I/O to the Anthropic cloud
	// OUTSIDE the governed transport — the sessions run unobserved and unenforced,
	// the same ungoverned class as a missing PEP hook or the brief forceRemoteSettingsRefresh
	// window. This is the GOVERN side of the FASE V asymmetry.
	if expected.DisableRemoteControl && !live.DisableRemoteControl {
		add(model.SeverityHigh, "disableRemoteControl", "Remote Control is NOT disabled on host — sessions may be driven from claude.ai/code or the Claude app, relaying I/O to the Anthropic cloud outside the governed transport (org policy requires it disabled)")
	}
	// skillOverrides: each skill the org authored an override for must resolve to the SAME
	// EFFECTIVE state on the host (a skill absent on the host is "on"). A skill the org hid
	// (off / user-invocable-only) that stays visible lets the model see or auto-invoke a
	// skill the org meant to suppress — a visibility control (the skillscan posture sibling),
	// Medium. Only authored skills are checked (an unset expectation is not drift).
	for _, skill := range sortedKeys(expected.SkillOverrides) {
		want := expected.SkillOverrides[skill]
		if got := skillOverrideState(live.SkillOverrides, skill); got != want {
			add(model.SeverityMedium, "skillOverrides", "skill visibility override not enforced on host: "+skill+" (org wants "+want+", host has "+got+")")
		}
	}
	// policyHelper: the org's DYNAMIC managed-settings source. When authored but absent or
	// pointed at a different executable on the host, the startup-computed managed policy is
	// not in force — Medium (the host may still carry a static managed-settings.json). Only
	// the corroborated `path` is compared.
	if expected.PolicyHelper != nil {
		if want := strings.TrimSpace(expected.PolicyHelper.Path); want != "" {
			if livePath, livePresent := live.policyHelperPath(); !livePresent || livePath != want {
				add(model.SeverityMedium, "policyHelper", "policyHelper is NOT configured to the authored helper on host (the org's dynamic managed-settings computation is not in force)")
			}
		}
	}
	// NET-NEW model-governance keys (VERIFIED 2026-06-27). availableModels drift is
	// Medium: the picker restriction is not deny-closed enforcement (the PEP is), but its
	// absence means the fleet can select models the org restricted. enforceAvailableModels
	// drift is Medium: without it the Default model ignores the allowlist (a weaker posture).
	// disableClaudeAiConnectors drift is Medium: cloud connectors remain active, expanding
	// the MCP surface the org chose to close.
	if len(expected.AvailableModels) > 0 && !sameStringSet(expected.AvailableModels, live.AvailableModels) {
		add(model.SeverityMedium, "availableModels", "model allowlist (availableModels) on host drifts from the authored set — users may select models the org restricted")
	}
	if expected.EnforceAvailableModels && !live.EnforceAvailableModels {
		add(model.SeverityMedium, "enforceAvailableModels", "enforceAvailableModels is NOT set on host — the Default model ignores the availableModels allowlist (org policy requires enforcement)")
	}
	if expected.DisableClaudeAiConnectors && !live.DisableClaudeAiConnectors {
		add(model.SeverityMedium, "disableClaudeAiConnectors", "claude.ai MCP connectors are NOT disabled on host — cloud connectors remain active (org policy requires them disabled)")
	}
	// NET-NEW 2026-07 currency keys (VERIFIED 2026-07-03). pluginTrustMessage is
	// intentionally not drift-checked: it is informational UX, not lockdown.
	if expected.DisableSkillShellExecution && !live.DisableSkillShellExecution {
		add(model.SeverityMedium, "disableSkillShellExecution", "inline shell execution in skills/custom commands is NOT disabled — user/project/plugin skill sources remain an ungoverned execution surface")
	}
	var liveCredentials *msSandboxCredentials
	if live.Sandbox != nil {
		liveCredentials = live.Sandbox.Credentials
	}
	if expected.SandboxCredentials.hasAny() && !sameSandboxCredentials(expected.SandboxCredentials, liveCredentials) {
		add(model.SeverityMedium, "sandbox.credentials", "sandbox.credentials on host drifts from the authored credential restrictions")
	}
	for _, rule := range expected.Permissions.Deny {
		if !containsRule(livePerm.Deny, rule) {
			add(model.SeverityMedium, "permissions.deny", "managed deny rule missing on host: "+rule)
		}
	}

	out := make([]model.FindingReport, 0, len(drifts))
	for _, d := range drifts {
		out = append(out, model.FindingReport{
			Kind:        findingKindDrift,
			Severity:    d.severity,
			SubjectKind: originManagedPolicy,
			SubjectRef:  scope,
			Title:       d.title,
			DetailHash:  redact.Hash(scope + "|" + d.key + "|" + d.title),
			OccurredAt:  at,
		})
	}
	return out
}

// absenceFinding reports that the host's managed-settings.json is missing or
// unreadable/invalid — the host is NOT governed by the only non-overridable layer,
// the most serious posture this connector can find.
func absenceFinding(scope, reason string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingKindDrift,
		Severity:    model.SeverityHigh,
		SubjectKind: originManagedPolicy,
		SubjectRef:  scope,
		Title:       "managed-settings.json " + reason + " — host is ungoverned",
		DetailHash:  redact.Hash(scope + "|absent|" + reason),
		OccurredAt:  at,
	}
}

// containsRule reports whether rule is present in the rule set (exact match).
func containsRule(set []string, rule string) bool {
	for _, r := range set {
		if r == rule {
			return true
		}
	}
	return false
}

// liveAllowRules returns the allow rules from the live config, used to emit
// PERMITTED edges from disk when no authored intent is configured (observe-only:
// inventory the managed policy actually in force).
func (m managedSettings) liveAllowRules() []string {
	if m.Permissions == nil {
		return nil
	}
	return m.Permissions.Allow
}
