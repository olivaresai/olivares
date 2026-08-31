// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// This file is the AUTHORING entry-point of the connector (B): the exported,
// pure functions the AGPL governance module calls to validate, preview the
// delivery precedence of, and drift-verify a managed-settings.json document authored
// in the console — WITHOUT this Apache connector importing /core or /modules (the
// legal arrow only runs module→connector). It reuses the SAME verified Render/drift
// logic the read-only Source uses, so the authoring and verification halves can never
// disagree.

// ValidateJSON validates a managed-settings.json document SERVER-SIDE (defense in
// depth — the UI is never the security boundary). It returns a list of issue strings
// (empty = valid). It is forward-compatible: unknown top-level keys are NOT rejected
// (Claude Code adds keys frequently), but a known key with the wrong shape, or a
// permissions.defaultMode that is not a recognized mode, is reported. A document that
// is not a JSON object at all is the first, fatal issue.
func ValidateJSON(content []byte) []string {
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return []string{"managed-settings document is empty"}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return []string{"managed-settings must be a JSON object: " + err.Error()}
	}
	var issues []string
	// Known keys must type-check. Decoding into the typed wire shape (which ignores
	// unknown keys) surfaces a type mismatch on a known key.
	var ms managedSettings
	if err := json.Unmarshal([]byte(trimmed), &ms); err != nil {
		issues = append(issues, "a known key has the wrong type: "+err.Error())
	}
	if ms.Permissions != nil {
		if dm := ms.Permissions.DefaultMode; dm != "" && !knownMode(dm) {
			issues = append(issues, fmt.Sprintf("permissions.defaultMode %q is not one of default|plan|acceptEdits|auto|dontAsk|bypassPermissions", dm))
		}
		if dbp := ms.Permissions.DisableBypassPermissionsMode; dbp != "" && dbp != disableMarker {
			issues = append(issues, fmt.Sprintf("permissions.disableBypassPermissionsMode must be the string %q (not a boolean)", disableMarker))
		}
		if da := ms.Permissions.DisableAutoMode; da != "" && da != disableMarker {
			issues = append(issues, fmt.Sprintf("permissions.disableAutoMode must be the string %q (not a boolean)", disableMarker))
		}
	}
	// NET-NEW surfaces: managed hooks (shape) + telemetry env (deny-closed on
	// inline credentials). Both are validated SERVER-SIDE so a hollow PEP hook or a
	// secret inlined in a plaintext managed file never publishes looking valid.
	issues = append(issues, validateHooks(ms.Hooks)...)
	issues = append(issues, validateEnv(ms.Env)...)
	// NET-NEW (B1): the marketplace allowlist/blocklist MUST be the verified ARRAY
	// shape (a legacy bool would not enforce), with valid per-source fields + compilable
	// regexes — so a malformed allowlist never publishes looking governed.
	issues = append(issues, validateMarketplaceArray(ms.StrictKnownMarketplaces, "strictKnownMarketplaces")...)
	issues = append(issues, validateMarketplaceArray(ms.BlockedMarketplaces, "blockedMarketplaces")...)
	// NET-NEW 2.1.17x keys (VERIFIED 2026-06-10 — see the Policy field docs).
	issues = append(issues, validate2117x(ms)...)
	// NET-NEW 2026 currency keys (VERIFIED 2026-06-16).
	issues = append(issues, validate2026(ms)...)
	// NET-NEW model-governance keys (VERIFIED 2026-06-27).
	issues = append(issues, validateS268(ms)...)
	// NET-NEW 2026-07 currency keys (VERIFIED 2026-07-03).
	issues = append(issues, validateS327(ms)...)
	return issues
}

// validateS327 validates the 2026-07 currency keys SERVER-SIDE (VERIFIED
// 2026-07-03). The scalar bool/string keys are type-checked by the managedSettings
// decode above; sandbox.credentials needs semantic checks because its modes are strings.
func validateS327(ms managedSettings) []string {
	var issues []string
	if ms.Sandbox == nil || ms.Sandbox.Credentials == nil {
		return issues
	}
	for i, file := range ms.Sandbox.Credentials.Files {
		if strings.TrimSpace(file.Path) == "" {
			issues = append(issues, fmt.Sprintf("sandbox.credentials.files[%d].path is empty", i))
		}
		if strings.TrimSpace(file.Mode) != "deny" {
			issues = append(issues, fmt.Sprintf("sandbox.credentials.files[%d].mode = %q is not deny (files only support deny)", i, file.Mode))
		}
	}
	for i, env := range ms.Sandbox.Credentials.EnvVars {
		if strings.TrimSpace(env.Name) == "" {
			issues = append(issues, fmt.Sprintf("sandbox.credentials.envVars[%d].name is empty", i))
		}
		switch strings.TrimSpace(env.Mode) {
		case "deny", "mask":
		default:
			issues = append(issues, fmt.Sprintf("sandbox.credentials.envVars[%d].mode = %q is not deny|mask", i, env.Mode))
		}
	}
	return issues
}

// validate2026 validates the currency keys SERVER-SIDE (VERIFIED 2026-06-16). Each
// check exists because the CLIENT-side failure mode would otherwise surprise the operator:
//   - an unknown skillOverrides STATE is silently ignored by the client (the skill stays
//     visible), so a typo'd "off" would publish looking like it hides the skill;
//   - an empty skill NAME key is meaningless;
//   - a policyHelper with no `path` is a hollow helper — it computes no managed settings
//     yet the file looks governed (the same deny-closed posture as a commandless PEP hook).
//
// The `disableRemoteControl` bool needs no extra check: the typed decode in ValidateJSON
// already rejects a non-boolean value as "a known key has the wrong type", and a
// non-object policyHelper is rejected the same way (it cannot decode into msPolicyHelper).
func validate2026(ms managedSettings) []string {
	var issues []string
	for _, skill := range sortedKeys(ms.SkillOverrides) {
		if strings.TrimSpace(skill) == "" {
			issues = append(issues, "skillOverrides has an empty skill-name key")
			continue
		}
		if v := ms.SkillOverrides[skill]; !knownSkillState(v) {
			issues = append(issues, fmt.Sprintf("skillOverrides[%s] = %q is not one of %s|%s|%s|%s (the client ignores an unknown state and leaves the skill visible)",
				strconv.Quote(skill), v, SkillOn, SkillNameOnly, SkillUserInvocableOnly, SkillOff))
		}
	}
	if ms.PolicyHelper != nil && strings.TrimSpace(ms.PolicyHelper.Path) == "" {
		issues = append(issues, "policyHelper must carry a non-empty `path` to the helper executable (a path-less helper computes no managed settings)")
	}
	return issues
}

// validateS268 validates the model-governance keys SERVER-SIDE (VERIFIED 2026-06-27).
// availableModels entries must be non-empty strings (an empty alias matches nothing and is
// dead policy); enforceAvailableModels without availableModels is hollow (no effect).
// disableClaudeAiConnectors needs no extra check: the typed decode rejects a non-boolean.
func validateS268(ms managedSettings) []string {
	var issues []string
	for i, m := range ms.AvailableModels {
		if strings.TrimSpace(m) == "" {
			issues = append(issues, fmt.Sprintf("availableModels[%d] is empty (entries are model aliases or IDs)", i))
		}
	}
	if ms.EnforceAvailableModels && len(ms.AvailableModels) == 0 {
		issues = append(issues, "enforceAvailableModels is true but availableModels is empty — the enforcement has no effect without a non-empty allowlist")
	}
	return issues
}

// validate2117x validates the 2.1.17x keys SERVER-SIDE. Each check exists
// because the CLIENT-side failure mode would otherwise surprise the operator:
// the required*Version gates FAIL OPEN (an invalid value is stripped — published
// garbage enforces nothing); an invalid allowedMcpServers is enforced as an EMPTY
// allowlist (fail-closed lockout); an empty forceLoginOrgUUID array blocks ALL
// login; fallbackModel entries beyond three are silently ignored; a non-MCP glob
// in an allow rule is rejected by the client at startup.
func validate2117x(ms managedSettings) []string {
	var issues []string
	// forceLoginMethod values: claudeai|console|gateway (VERIFIED 2026-07-20 against
	// code.claude.com/docs/en/settings — "gateway" locks login to a cloud gateway and
	// pairs with forceLoginGatewayUrl; the pre allowlist wrongly rejected it).
	if fm := ms.ForceLoginMethod; fm != "" && fm != "claudeai" && fm != "console" && fm != "gateway" {
		issues = append(issues, fmt.Sprintf("forceLoginMethod %q is not one of claudeai|console|gateway", fm))
	}
	if rawPresent(ms.ForceLoginOrgUUID) {
		single, list, emptyList := forceLoginOrgFromRaw(ms.ForceLoginOrgUUID)
		switch {
		case emptyList:
			issues = append(issues, "forceLoginOrgUUID is an EMPTY array — this fails closed and blocks ALL login (the client treats it as a misconfiguration); author the org UUID(s) or remove the key")
		case single == "" && list == nil:
			issues = append(issues, "forceLoginOrgUUID must be a UUID string or an array of UUID strings")
		}
	}
	if psb := ms.ParentSettingsBehavior; psb != "" && psb != ParentFirstWins && psb != ParentMerge {
		issues = append(issues, fmt.Sprintf("parentSettingsBehavior %q is not one of %s|%s", psb, ParentFirstWins, ParentMerge))
	}
	if rawPresent(ms.FallbackModel) {
		models, present := fallbackModelsFromRaw(ms.FallbackModel)
		if !present {
			issues = append(issues, "fallbackModel must be a model string or an array of model strings")
		} else {
			if len(models) > fallbackModelMax {
				issues = append(issues, fmt.Sprintf("fallbackModel has %d entries — the chain is capped at %d and the client IGNORES the extras (dead policy)", len(models), fallbackModelMax))
			}
			for i, m := range models {
				if strings.TrimSpace(m) == "" {
					issues = append(issues, fmt.Sprintf("fallbackModel[%d] is empty", i))
				}
			}
		}
	}
	issues = append(issues, validateMCPAllowRaw(ms.AllowedMcpServers, "allowedMcpServers")...)
	issues = append(issues, validateMCPDenyList(ms.DeniedMcpServers, "deniedMcpServers")...)
	issues = append(issues, validateRequiredVersions(ms.RequiredMinimumVersion, ms.RequiredMaximumVersion)...)
	for i, name := range ms.PluginSuggestionMarketplaces {
		if strings.TrimSpace(name) == "" {
			issues = append(issues, fmt.Sprintf("pluginSuggestionMarketplaces[%d] is empty (entries are marketplace names)", i))
		}
	}
	if ms.Permissions != nil {
		for _, rule := range ms.Permissions.Allow {
			if bad, why := invalidAllowGlob(rule); bad {
				issues = append(issues, "permissions.allow rule "+strconv.Quote(rule)+": "+why)
			}
		}
	}
	return issues
}

// versionShape matches a plausible dotted version string. The required*Version
// gates FAIL OPEN on the client (an invalid value is stripped, enforcing
// nothing), so a non-version-shaped value must be flagged loudly at publish time.
var versionShape = regexp.MustCompile(`^\d+(\.\d+)*$`)

// validateRequiredVersions validates the hard startup gates: each value must be
// version-shaped, and an authored min ABOVE the authored max is an empty range
// that would exit EVERY client at startup — a catastrophic publish.
func validateRequiredVersions(minV, maxV string) []string {
	var issues []string
	for _, kv := range []struct{ key, val string }{
		{"requiredMinimumVersion", minV}, {"requiredMaximumVersion", maxV},
	} {
		if kv.val != "" && !versionShape.MatchString(kv.val) {
			issues = append(issues, fmt.Sprintf("%s %q is not a version string — the client FAILS OPEN on an invalid value (it is stripped and enforces nothing)", kv.key, kv.val))
		}
	}
	if minV != "" && maxV != "" && versionShape.MatchString(minV) && versionShape.MatchString(maxV) &&
		compareVersions(minV, maxV) > 0 {
		issues = append(issues, fmt.Sprintf("requiredMinimumVersion %q is ABOVE requiredMaximumVersion %q — the allowed range is empty, every client would exit at startup", minV, maxV))
	}
	return issues
}

// compareVersions compares two dotted numeric versions segment-by-segment
// (missing segments count as 0). Returns -1/0/1.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var ai, bi int
		if i < len(as) {
			ai, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(bs[i])
		}
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return 1
		}
	}
	return 0
}

// invalidAllowGlob reports whether a permissions.allow rule carries a tool-name
// glob the CLIENT rejects (VERIFIED 2026-06-10; changelog 2.1.166): allow-rule
// globs are supported ONLY in the tool position after a literal mcp__<server>__
// prefix (e.g. mcp__github__get_*); the server segment must be glob-free. Deny
// rules are unrestricted ("*" denies every tool) and are not checked here.
func invalidAllowGlob(rule string) (bad bool, why string) {
	tool := rule
	if i := strings.IndexByte(rule, '('); i >= 0 {
		tool = rule[:i]
	}
	tool = strings.TrimSpace(tool)
	if !strings.Contains(tool, "*") {
		return false, ""
	}
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return true, "tool-name globs in ALLOW rules are only supported after a literal mcp__<server>__ prefix; the client rejects non-MCP globs"
	}
	server, _, ok := strings.Cut(rest, "__")
	if !ok || server == "" {
		return true, "an MCP allow glob needs the full mcp__<server>__<tool-glob> form"
	}
	if strings.Contains(server, "*") {
		return true, "the server segment of an MCP allow glob must be glob-free (only the tool position may carry '*')"
	}
	return false, ""
}

// knownMode reports whether m is one of the documented permission defaultModes.
func knownMode(m string) bool {
	switch m {
	case ModeDefault, ModePlan, ModeAcceptEdits, ModeAuto, ModeDontAsk, ModeBypassPermissions:
		return true
	default:
		return false
	}
}

// ParsePolicyFromWire parses a managed-settings.json document (the wire shape the
// console authors) into the connector's governance-authored Policy form, so the
// caller can re-Render the canonical bytes or feed drift verification. Unknown keys
// are ignored (forward-compatible); malformed JSON is an error.
func ParsePolicyFromWire(content []byte) (Policy, error) {
	ms, err := parseLive(content)
	if err != nil {
		return Policy{}, err
	}
	return fromWire(ms), nil
}

// CanonicalJSON re-renders a managed-settings.json document through the verified
// wire shape, producing the canonical, minimal bytes an operator distributes. It is
// the round-trip the console shows on publish (and the artifact deploy/VII ships).
func CanonicalJSON(content []byte) ([]byte, error) {
	p, err := ParsePolicyFromWire(content)
	if err != nil {
		return nil, err
	}
	return Render(p)
}

// VerifyDriftJSON runs the PERMITTED-policy-vs-OBSERVED-config drift check at publish
// time (B): authoredJSON is the just-published managed policy (PERMITTED), and
// observedJSON is the host's live managed-settings.json (OBSERVED). It reuses the
// connector's verified driftFindings / absenceFinding so authoring and verification
// share one source of truth. An absent (empty) or invalid observed config is itself a
// high-severity finding (the host is ungoverned). A malformed AUTHORED document is an
// error (it must never have been published — the caller is deny-closed on it).
func VerifyDriftJSON(scope string, authoredJSON, observedJSON []byte, at time.Time) ([]model.FindingReport, error) {
	expected, err := ParsePolicyFromWire(authoredJSON)
	if err != nil {
		return nil, fmt.Errorf("managed-settings: authored document is invalid: %w", err)
	}
	if len(strings.TrimSpace(string(observedJSON))) == 0 {
		return []model.FindingReport{absenceFinding(scope, "is absent", at)}, nil
	}
	live, perr := parseLive(observedJSON)
	if perr != nil {
		return []model.FindingReport{absenceFinding(scope, "is present but invalid JSON", at)}, nil
	}
	return driftFindings(scope, expected, live, at), nil
}

// DeliveryPreview returns the human-readable precedence/effect lines for a managed
// document's dry-run (B): it explains, from the VERIFIED no-merge rule and the
// provider-bypass facts, how THIS authored content would resolve in the managed tier
// WITHOUT writing any host. authoredNonEmpty reports whether the content delivers any
// keys.
//
// HONESTY: the third-party-provider bypass is a PER-CLIENT condition (set on a
// developer's machine running Claude Code), which the control plane CANNOT observe
// from its own environment — so the preview states it as an unconditional caveat that
// MAY apply to any client, never asserting that no bypass exists (docs/SECURITY-HARDENING.md).
func DeliveryPreview(authoredNonEmpty bool) []DryRunLine {
	lines := []DryRunLine{{
		Scope: "managed-tier",
		Note:  "managed settings occupy the HIGHEST precedence band — above command-line arguments, local, project and user settings; nothing can override them",
	}}
	// As server-managed (absent any per-client bypass, which the caveat below covers).
	srv := ResolveManagedSource(authoredNonEmpty, false, false)
	lines = append(lines, DryRunLine{Scope: "as server-managed", Note: srv.Reason})
	// As endpoint-managed (the OS file), with a non-empty server source assumed absent.
	end := ResolveManagedSource(false, authoredNonEmpty, false)
	lines = append(lines, DryRunLine{Scope: "as endpoint-managed", Note: end.Reason})
	lines = append(lines, DryRunLine{
		Scope: "no-merge",
		Note:  "the two managed tiers do NOT merge: if server-managed delivers any keys, endpoint-managed is ignored entirely (server is checked first, then endpoint)",
	})
	// Endpoint-tier drop-in (VERIFIED 2026-06-16): the file-based managed tier reads
	// a managed-settings.d/ directory beside the base file. Surfaced so the console's
	// resolved view explains how fragments combine on a host.
	lines = append(lines, DryRunLine{
		Scope: "endpoint-drop-in",
		Note:  "the endpoint-managed (file) tier also reads a managed-settings.d/ drop-in directory beside managed-settings.json: the base file is merged FIRST, then *.json fragments in ALPHABETICAL order (numeric prefixes like 10-/20- control order; dotfiles ignored) — scalars are later-wins, arrays are concatenated and de-duplicated, objects are deep-merged. This is a file-tier feature only; it does not apply to the server-managed tier",
	})
	lines = append(lines, DryRunLine{
		Scope: "provider-bypass (per-client)",
		Note:  "on any client configured with a third-party model provider (CLAUDE_CODE_USE_BEDROCK/VERTEX/FOUNDRY/MANTLE or a custom ANTHROPIC_BASE_URL), SERVER-managed settings are bypassed entirely (they require a direct api.anthropic.com connection) — only the endpoint-managed file can govern that client. This is a per-client condition the control plane cannot observe from here.",
	})
	// The full scope precedence below the managed tier (B2): how a managed value
	// resolves against CLI/local/project/user, the merge-vs-override rules, and the
	// enforce-before-exec guarantees. Appended so the console's resolved view is complete.
	lines = append(lines, PrecedencePreview()...)
	return lines
}

// DryRunLine is one precedence/effect line of a managed dry-run (the shape the B
// authoring layer maps onto the DryRunResult.resolved array). It carries no settings
// VALUES — only the scope label and a non-sensitive explanation.
type DryRunLine struct {
	Scope string `json:"scope"`
	Note  string `json:"note"`
}

// fromWire is the inverse of toWire: it projects a live/authored managed-settings.json
// wire document back into the governance-authored Policy form, so drift verification
// can diff PERMITTED (authored) vs OBSERVED with the SAME field set Render emits. Only
// asserted fields matter to drift (an unset expectation is not drift), so the disable*
// markers and the strictPluginOnlyCustomization bool/array forms collapse to booleans.
func fromWire(ms managedSettings) Policy {
	p := Policy{
		AllowManagedMCPServersOnly:      ms.AllowManagedMcpServersOnly,
		AllowManagedPermissionRulesOnly: ms.AllowManagedPermissionRulesOnly,
		StrictPluginOnlyCustomization:   ms.strictPluginCustomizationSet(),
		ForceLoginMethod:                ms.ForceLoginMethod,
		MinimumVersion:                  ms.MinimumVersion,
		ForceRemoteSettingsRefresh:      ms.ForceRemoteSettingsRefresh,
		AllowManagedHooksOnly:           ms.AllowManagedHooksOnly,
		AllowManagedDomainsOnly:         ms.domainsLockdown(),
		AllowManagedReadPathsOnly:       ms.readPathsLockdown(),
		// NET-NEW 2.1.17x scalar keys.
		RequiredMinimumVersion:       ms.RequiredMinimumVersion,
		RequiredMaximumVersion:       ms.RequiredMaximumVersion,
		PluginSuggestionMarketplaces: ms.PluginSuggestionMarketplaces,
		ChannelsEnabled:              ms.ChannelsEnabled,
		ParentSettingsBehavior:       ms.ParentSettingsBehavior,
		DisableBundledSkills:         ms.DisableBundledSkills,
		// NET-NEW 2026 currency keys. disableRemoteControl is a plain bool;
		// skillOverrides and policyHelper map below.
		DisableRemoteControl: ms.DisableRemoteControl,
		// NET-NEW 2026-07 currency keys.
		DisableSideloadFlags:       ms.DisableSideloadFlags,
		PluginTrustMessage:         ms.PluginTrustMessage,
		DisableSkillShellExecution: ms.DisableSkillShellExecution,
	}
	// forceLoginOrgUUID: string and array wire forms are distinct postures (the
	// string also pre-selects the org), so each maps to its own field. A present
	// EMPTY array (the fail-closed block-all-login misconfiguration) is NOT a
	// representable authored intent — ValidateJSON rejects it at publish time and
	// driftFindings reads the live wire value directly, so nothing is lost here.
	if single, list, _ := forceLoginOrgFromRaw(ms.ForceLoginOrgUUID); single != "" {
		p.ForceLoginOrgUUID = single
	} else if len(list) > 0 {
		p.ForceLoginOrgUUIDs = list
	}
	if models, present := fallbackModelsFromRaw(ms.FallbackModel); present {
		p.FallbackModels = models
	}
	// Marketplace allowlist/blocklist: capture the ARRAY form. A present array (even the
	// empty `[]` lockdown) sets the allowlist pointer; a present blocklist sets the slice.
	// A present-but-non-array value (legacy bool) is not representable as an allowlist, so
	// it is dropped from the canonical intent (drift still flags the host's non-conformance).
	if entries, present := liveMarketplaces(ms.StrictKnownMarketplaces); present {
		p.StrictKnownMarketplaces = &entries
	}
	if entries, present := liveMarketplaces(ms.BlockedMarketplaces); present && len(entries) > 0 {
		p.BlockedMarketplaces = entries
	}
	if ms.Permissions != nil {
		p.Permissions = Permissions{
			Allow:                        ms.Permissions.Allow,
			Deny:                         ms.Permissions.Deny,
			Ask:                          ms.Permissions.Ask,
			DefaultMode:                  ms.Permissions.DefaultMode,
			AdditionalDirectories:        ms.Permissions.AdditionalDirectories,
			DisableBypassPermissionsMode: ms.Permissions.bypassDisabled(),
			DisableAutoMode:              ms.Permissions.autoDisabled(),
		}
	}
	// MCP predicates: capture the three-state allowlist (a present array — even
	// the empty `[]` lockdown — sets the pointer; a present-but-non-array value is
	// not representable and is dropped from the canonical intent, drift still
	// flags the host's non-conformance) and the denylist entries. serverCommand/
	// unknown predicates are observable on the wire but not canonical (see
	// liveMCPRules).
	if rules, present := liveMCPRules(ms.AllowedMcpServers); present {
		p.AllowedMCPServers = &rules
	}
	if rules := liveMCPRuleList(ms.DeniedMcpServers); len(rules) > 0 {
		p.DeniedMCPServers = rules
	}
	if ms.autoModeSet() {
		p.AutoMode = &AutoModePolicy{
			Environment: ms.AutoMode.Environment,
			Allow:       ms.AutoMode.Allow,
			SoftDeny:    ms.AutoMode.SoftDeny,
			HardDeny:    ms.AutoMode.HardDeny,
		}
	}
	if len(ms.Hooks) > 0 {
		p.Hooks = ms.Hooks
	}
	if len(ms.Env) > 0 {
		p.Env = ms.Env
	}
	// when the org PINS an ANTHROPIC_BASE_URL in the managed env, that pinned value IS
	// the authorized gateway a host's live base-URL is verified against (a divergent live
	// value bypasses server-managed-settings — see Policy.AuthorizedGatewayBaseURL /
	// verify.go). The VerifyDriftJSON publish path flows through here, so this makes the
	// base-URL drift active automatically when the authored policy pins a gateway. (When the
	// expectation is configured directly as a Policy — the expected_policy connector config —
	// the field is set explicitly and this derivation is a no-op.)
	if bu := strings.TrimSpace(ms.Env[EnvBaseURL]); bu != "" {
		p.AuthorizedGatewayBaseURL = bu
	}
	// NET-NEW 2026 currency keys. skillOverrides maps straight across; policyHelper
	// captures only the corroborated `path` (unknown sibling keys on the wire are observed
	// by drift directly, not carried into the canonical authored intent).
	if len(ms.SkillOverrides) > 0 {
		p.SkillOverrides = ms.SkillOverrides
	}
	if path, present := ms.policyHelperPath(); present && path != "" {
		p.PolicyHelper = &PolicyHelper{Path: path}
	}
	// NET-NEW model-governance keys (VERIFIED 2026-06-27).
	if len(ms.AvailableModels) > 0 {
		p.AvailableModels = ms.AvailableModels
	}
	if ms.EnforceAvailableModels {
		p.EnforceAvailableModels = true
	}
	if ms.DisableClaudeAiConnectors {
		p.DisableClaudeAiConnectors = true
	}
	// NET-NEW 2026-07 currency keys. sandbox.credentials maps as a
	// typed block; allowPlaintextInject is preserved as raw observable presence.
	if ms.Sandbox != nil && ms.Sandbox.Credentials.hasAny() {
		p.SandboxCredentials = ms.Sandbox.Credentials
	}
	return p
}

// HasAnyKeys reports whether a managed-settings.json document delivers ANY governed
// keys (used to decide non-emptiness for the no-merge precedence preview). A document
// that parses to an all-zero Policy (or fails to parse) delivers nothing.
func HasAnyKeys(content []byte) bool {
	p, err := ParsePolicyFromWire(content)
	if err != nil {
		return false
	}
	return p.Permissions.hasAny() ||
		p.AllowedMCPServers != nil || len(p.DeniedMCPServers) > 0 ||
		p.AllowManagedMCPServersOnly || p.AllowManagedPermissionRulesOnly ||
		p.StrictKnownMarketplaces != nil || len(p.BlockedMarketplaces) > 0 ||
		p.StrictPluginOnlyCustomization ||
		p.ForceLoginMethod != "" || p.ForceLoginOrgUUID != "" || len(p.ForceLoginOrgUUIDs) > 0 ||
		p.MinimumVersion != "" ||
		p.RequiredMinimumVersion != "" || p.RequiredMaximumVersion != "" ||
		len(p.FallbackModels) > 0 || len(p.PluginSuggestionMarketplaces) > 0 ||
		p.ChannelsEnabled || p.ParentSettingsBehavior != "" || p.DisableBundledSkills ||
		p.ForceRemoteSettingsRefresh || p.AllowManagedHooksOnly ||
		p.AllowManagedDomainsOnly || p.AllowManagedReadPathsOnly || p.AutoMode.hasAny() ||
		len(p.Hooks) > 0 || len(p.Env) > 0 ||
		// NET-NEW 2026 currency keys.
		p.DisableRemoteControl || len(p.SkillOverrides) > 0 ||
		(p.PolicyHelper != nil && strings.TrimSpace(p.PolicyHelper.Path) != "") ||
		// NET-NEW model-governance keys.
		len(p.AvailableModels) > 0 || p.EnforceAvailableModels || p.DisableClaudeAiConnectors ||
		// NET-NEW 2026-07 currency keys.
		p.DisableSideloadFlags || p.PluginTrustMessage != "" ||
		p.DisableSkillShellExecution || p.SandboxCredentials.hasAny()
}
