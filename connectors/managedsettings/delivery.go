// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import "strings"

// This file models the TWO delivery tiers of Claude Code managed settings and the
// NO-MERGE precedence between them, plus the third-party-provider bypass — the
// posture a SOC/CTO governs (CLA-05/A). It is MODELING only: the connector
// never delivers settings (server-managed is delivered by api.anthropic.com; the
// endpoint file is distributed by deploy/VII) — it captures the rule so the control
// plane can resolve and EXPLAIN the effective managed source without faking it.
//
// VERIFIED 2026-06-08 against the live docs (fetch recorded in
//):
//   - https://code.claude.com/docs/en/server-managed-settings (tiers, precedence, bypass, min versions)
//   - https://code.claude.com/docs/en/settings (hierarchy + endpoint file paths)
// Do not pin these from memory — re-verify on the next build; mark anything the live
// docs no longer state as to-confirm rather than asserting it.

// DeliveryTier identifies HOW a managed-settings configuration reaches a Claude Code
// client. Both tiers occupy the SAME (highest) precedence band — above CLI args — but
// they do not merge with each other (see ResolveManagedSource).
type DeliveryTier string

const (
	// TierServerManaged is the NET-NEW tier: configuration delivered from
	// api.anthropic.com at authentication time and re-fetched on an hourly poll,
	// configured in the claude.ai admin console (Claude Code > Managed settings).
	TierServerManaged DeliveryTier = "server-managed"
	// TierEndpointManaged is the managed-settings.json file delivered to the device
	// by MDM / OS policy (the tier this connector already reads and verifies).
	TierEndpointManaged DeliveryTier = "endpoint-managed"
)

// requiredMinimumVersion captures the Claude Code versions that gate the
// server-managed tier (VERIFIED 2026-06-08; Teams vs Enterprise differ). These are
// posture facts the console surfaces — never an enforcement the connector performs.
const (
	// ServerManagedMinVersionTeams is the minimum Claude Code version for
	// server-managed settings on Claude for Teams.
	ServerManagedMinVersionTeams = "2.1.38"
	// ServerManagedMinVersionEnterprise is the minimum for Claude for Enterprise.
	ServerManagedMinVersionEnterprise = "2.1.30"
	// ForceRefreshAuthExemptVersion is the version from which `claude auth`
	// subcommands are exempt from forceRemoteSettingsRefresh's fail-closed startup
	// gate (so an operator can re-auth when expired creds caused the fetch failure).
	ForceRefreshAuthExemptVersion = "2.1.139"
)

// ManagedDeliveryNote summarizes which managed source governs a host, for the
// authoring console's dry-run/posture (B). It carries no settings VALUES — only
// the resolved tier, what is ignored, and the honest reason.
type ManagedDeliveryNote struct {
	// Governed reports whether ANY managed source is in force.
	Governed bool `json:"governed"`
	// Effective is the tier that wins (empty when ungoverned or bypassed).
	Effective DeliveryTier `json:"effective_tier,omitempty"`
	// Ignored lists the managed sources that are present but NOT in force because a
	// higher-precedence source won (the no-merge rule) or a provider bypass dropped them.
	Ignored []DeliveryTier `json:"ignored_tiers,omitempty"`
	// Bypassed reports that the SERVER-managed tier does not apply because a
	// third-party model provider is configured.
	Bypassed bool `json:"server_tier_bypassed,omitempty"`
	// Reason is a short, non-sensitive explanation of the resolution.
	Reason string `json:"reason"`
}

// ResolveManagedSource applies the VERIFIED no-merge precedence (2026-06-08,
// code.claude.com/docs/en/server-managed-settings): the managed tier outranks CLI
// args; within it the FIRST source that delivers a non-empty configuration wins —
// server-managed is checked first, then endpoint-managed — and the sources DO NOT
// merge (if the winner delivers any keys, the other tier is ignored entirely).
//
// serverNonEmpty/endpointNonEmpty report whether each tier delivers any keys.
// serverBypassed reports that a third-party model provider is configured, which
// drops the SERVER tier (it requires a direct api.anthropic.com connection); the
// endpoint file is unaffected and may still govern. The result is honest about an
// ungoverned host rather than pretending governance that does not exist.
func ResolveManagedSource(serverNonEmpty, endpointNonEmpty, serverBypassed bool) ManagedDeliveryNote {
	note := ManagedDeliveryNote{}
	serverActive := serverNonEmpty && !serverBypassed
	switch {
	case serverActive:
		note.Governed = true
		note.Effective = TierServerManaged
		note.Reason = "server-managed settings deliver a non-empty configuration and win the managed tier; endpoint-managed settings (if any) are ignored — the tiers do not merge"
		if endpointNonEmpty {
			note.Ignored = append(note.Ignored, TierEndpointManaged)
		}
	case endpointNonEmpty:
		note.Governed = true
		note.Effective = TierEndpointManaged
		if serverBypassed && serverNonEmpty {
			note.Bypassed = true
			note.Ignored = append(note.Ignored, TierServerManaged)
			note.Reason = "server-managed settings are bypassed by a third-party model provider; the endpoint-managed managed-settings.json governs"
		} else {
			note.Reason = "server-managed settings deliver nothing; the endpoint-managed managed-settings.json governs"
		}
	default:
		note.Governed = false
		if serverBypassed && serverNonEmpty {
			note.Bypassed = true
			note.Ignored = append(note.Ignored, TierServerManaged)
			note.Reason = "host is UNGOVERNED: server-managed settings are bypassed by a third-party model provider and no endpoint-managed managed-settings.json is present"
		} else {
			note.Reason = "host is UNGOVERNED: neither managed tier delivers a configuration"
		}
	}
	return note
}

// Third-party model-provider environment variables that BYPASS the server-managed
// settings tier entirely (VERIFIED 2026-06-08; list VERIFIED 2026-07-03 against
// code.claude.com/docs/en/claude-platform-on-aws): server-managed settings require a
// direct connection to api.anthropic.com, so a non-default provider means the tier
// does not apply. Claude Platform on AWS routes to
// aws-external-anthropic.{region}.api.aws, so it bypasses server-managed settings
// like Mantle/Bedrock/Vertex/Foundry. A custom ANTHROPIC_BASE_URL (an LLM gateway)
// bypasses it the same way. The endpoint-managed FILE is unaffected by these.
var thirdPartyProviderVars = []string{
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_USE_MANTLE",
	"CLAUDE_CODE_USE_ANTHROPIC_AWS",
}

// envAnthropicBaseURL, when set to a non-default value, routes Claude Code through a
// custom endpoint / LLM gateway and bypasses the server-managed tier.
const envAnthropicBaseURL = "ANTHROPIC_BASE_URL"

// defaultAnthropicBaseURLs are the values that are NOT a custom endpoint (the public
// API). Anything else set on ANTHROPIC_BASE_URL is a bypass.
var defaultAnthropicBaseURLs = map[string]bool{
	"":                              true,
	"https://api.anthropic.com":     true,
	"https://api.anthropic.com/":    true,
	"https://api.anthropic.com/v1":  true,
	"https://api.anthropic.com/v1/": true,
}

// ServerTierBypassed reports whether the server-managed settings tier is bypassed by
// a configured third-party model provider, and the non-sensitive reason. lookup is
// the environment accessor (injectable for tests). A bypass var is a bypass when SET
// to a truthy value; ANTHROPIC_BASE_URL is a bypass when set to a non-default URL.
// The reason names WHICH signal triggered it — never a secret value.
func ServerTierBypassed(lookup func(string) (string, bool)) (bool, string) {
	for _, v := range thirdPartyProviderVars {
		if val, ok := lookup(v); ok && isTruthyEnv(val) {
			return true, v + " is set (third-party model provider) — server-managed settings require api.anthropic.com"
		}
	}
	if val, ok := lookup(envAnthropicBaseURL); ok {
		if trimmed := strings.TrimSpace(val); trimmed != "" && !defaultAnthropicBaseURLs[trimmed] {
			return true, envAnthropicBaseURL + " is a custom endpoint / LLM gateway — server-managed settings require a direct api.anthropic.com connection"
		}
	}
	return false, ""
}

// isTruthyEnv reports whether a CLAUDE_CODE_USE_* env var is set to an enabling
// value. Claude Code treats these as on for "1"/"true"/"yes" (case-insensitive); an
// explicit "0"/"false"/"" is off. A present-but-unrecognized value is treated as ON
// (fail-closed for posture: an unexpected value should not silently look ungoverned-
// proof when the provider may in fact be active).
func isTruthyEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
