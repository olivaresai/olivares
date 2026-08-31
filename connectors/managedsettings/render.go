// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"encoding/json"
	"strings"
)

// toWire converts the governance-authored Policy to the exact managed-settings.json
// wire shape, translating the booleans Claude Code expresses non-boolean-ly (the
// disable* markers) and dropping empty sections so the emitted file is minimal.
func (p Policy) toWire() managedSettings {
	ms := managedSettings{
		AllowManagedMcpServersOnly:      p.AllowManagedMCPServersOnly,
		AllowManagedPermissionRulesOnly: p.AllowManagedPermissionRulesOnly,
		ForceLoginMethod:                p.ForceLoginMethod,
		MinimumVersion:                  p.MinimumVersion,
		ForceRemoteSettingsRefresh:      p.ForceRemoteSettingsRefresh,
		AllowManagedHooksOnly:           p.AllowManagedHooksOnly,
		// NET-NEW 2.1.17x scalar keys (semantics on the Policy fields).
		RequiredMinimumVersion:       p.RequiredMinimumVersion,
		RequiredMaximumVersion:       p.RequiredMaximumVersion,
		PluginSuggestionMarketplaces: p.PluginSuggestionMarketplaces,
		ChannelsEnabled:              p.ChannelsEnabled,
		ParentSettingsBehavior:       p.ParentSettingsBehavior,
		DisableBundledSkills:         p.DisableBundledSkills,
		// NET-NEW 2026 currency keys. disableRemoteControl is a plain bool;
		// skillOverrides and policyHelper render below only when authored.
		DisableRemoteControl: p.DisableRemoteControl,
		// NET-NEW 2026-07 currency keys (VERIFIED 2026-07-03).
		DisableSideloadFlags:       p.DisableSideloadFlags,
		PluginTrustMessage:         p.PluginTrustMessage,
		DisableSkillShellExecution: p.DisableSkillShellExecution,
	}
	// forceLoginOrgUUID renders in whichever verified wire form was authored: the
	// single STRING (pre-selects the org at login) or the ARRAY (any listed org,
	// no pre-selection). Both authored at once is a ValidateJSON issue; the single
	// form wins here so a malformed double-authoring is still observable.
	if u := strings.TrimSpace(p.ForceLoginOrgUUID); u != "" {
		b, _ := json.Marshal(u)
		ms.ForceLoginOrgUUID = b
	} else if len(p.ForceLoginOrgUUIDs) > 0 {
		b, _ := json.Marshal(p.ForceLoginOrgUUIDs)
		ms.ForceLoginOrgUUID = b
	}
	// fallbackModel renders CANONICALLY as an array (the documented example shape;
	// the wire also accepts a bare string, normalized away on the round trip).
	if len(p.FallbackModels) > 0 {
		ms.FallbackModel = fallbackModelsToRaw(p.FallbackModels)
	}
	// strictKnownMarketplaces renders as an ARRAY: a nil pointer omits the key (no
	// restriction); a non-nil pointer renders the entries — a non-nil EMPTY slice renders
	// as `[]`, the verified complete-lockdown posture (never JSON null, which would read as
	// "unset"). blockedMarketplaces renders only when non-empty (an empty blocklist blocks
	// nothing — same as absent).
	if p.StrictKnownMarketplaces != nil {
		ms.StrictKnownMarketplaces = marketplacesToRaw(*p.StrictKnownMarketplaces)
	}
	if len(p.BlockedMarketplaces) > 0 {
		ms.BlockedMarketplaces = marketplacesToRaw(p.BlockedMarketplaces)
	}
	if p.StrictPluginOnlyCustomization {
		ms.StrictPluginOnlyCustomization = json.RawMessage("true")
	}
	// Sandbox lockdown flags and credential rules render UNDER their verified
	// sandbox.* wire locations, only when asserted, so the emitted file stays
	// minimal and a non-asserted flag never appears as `false`.
	if p.AllowManagedDomainsOnly || p.AllowManagedReadPathsOnly || p.SandboxCredentials.hasAny() {
		sb := &msSandbox{}
		if p.AllowManagedDomainsOnly {
			sb.Network = &msSandboxNetwork{AllowManagedDomainsOnly: true}
		}
		if p.AllowManagedReadPathsOnly {
			sb.Filesystem = &msSandboxFilesystem{AllowManagedReadPathsOnly: true}
		}
		if p.SandboxCredentials.hasAny() {
			sb.Credentials = p.SandboxCredentials
		}
		ms.Sandbox = sb
	}
	if p.AutoMode.hasAny() {
		ms.AutoMode = &msAutoMode{
			Environment: p.AutoMode.Environment,
			Allow:       p.AutoMode.Allow,
			SoftDeny:    p.AutoMode.SoftDeny,
			HardDeny:    p.AutoMode.HardDeny,
		}
	}
	// allowedMcpServers renders like the marketplace allowlist: a nil pointer
	// omits the key (no restriction); a non-nil pointer renders the predicates — a
	// non-nil EMPTY slice renders as `[]`, the verified complete-lockdown posture.
	if p.AllowedMCPServers != nil {
		ms.AllowedMcpServers = mcpRulesToRaw(*p.AllowedMCPServers)
	}
	for _, r := range p.DeniedMCPServers {
		ms.DeniedMcpServers = append(ms.DeniedMcpServers, mcpRuleToRaw(r))
	}
	// NET-NEW: managed hooks + telemetry env render at their top-level wire keys,
	// only when authored (so a policy that asserts neither emits neither — minimal file).
	if len(p.Hooks) > 0 {
		ms.Hooks = p.Hooks
	}
	if len(p.Env) > 0 {
		ms.Env = p.Env
	}
	// NET-NEW 2026 currency keys. skillOverrides renders the authored name→state
	// map verbatim; policyHelper renders the corroborated {"path": ...} object (only when
	// a non-empty path is authored — a path-less helper computes nothing).
	if len(p.SkillOverrides) > 0 {
		ms.SkillOverrides = p.SkillOverrides
	}
	if p.PolicyHelper != nil {
		if path := strings.TrimSpace(p.PolicyHelper.Path); path != "" {
			ms.PolicyHelper = &msPolicyHelper{Path: path}
		}
	}
	// NET-NEW model-governance keys (VERIFIED 2026-06-27). availableModels renders
	// only when non-empty (an empty/nil array = no restriction, same as absent).
	// enforceAvailableModels renders only when true (managed-only, no effect without
	// availableModels). disableClaudeAiConnectors renders only when true (any-true-wins).
	if len(p.AvailableModels) > 0 {
		ms.AvailableModels = p.AvailableModels
	}
	if p.EnforceAvailableModels {
		ms.EnforceAvailableModels = true
	}
	if p.DisableClaudeAiConnectors {
		ms.DisableClaudeAiConnectors = true
	}

	perm := p.Permissions
	if perm.hasAny() {
		mp := &msPermissions{
			Allow:                 perm.Allow,
			Deny:                  perm.Deny,
			Ask:                   perm.Ask,
			DefaultMode:           perm.DefaultMode,
			AdditionalDirectories: perm.AdditionalDirectories,
		}
		if perm.DisableBypassPermissionsMode {
			mp.DisableBypassPermissionsMode = disableMarker
		}
		if perm.DisableAutoMode {
			mp.DisableAutoMode = disableMarker
		}
		ms.Permissions = mp
	}
	return ms
}

// hasAny reports whether the permission section carries any authored content.
func (p Permissions) hasAny() bool {
	return len(p.Allow) > 0 || len(p.Deny) > 0 || len(p.Ask) > 0 ||
		p.DefaultMode != "" || len(p.AdditionalDirectories) > 0 ||
		p.DisableBypassPermissionsMode || p.DisableAutoMode
}

// Render produces the managed-settings.json bytes for a governance-authored Policy,
// indented for the operator to read and commit. This is the authoring half of
// CLA-05: a control plane that can EMIT the only non-overridable Claude Code policy
// layer from its own governance state, ready to distribute to the OS policy paths.
func Render(p Policy) ([]byte, error) {
	return json.MarshalIndent(p.toWire(), "", "  ")
}
