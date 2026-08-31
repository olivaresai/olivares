// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codexmanagedconfig

// precedence.go CODIFIES the Codex managed-config precedence semantics — the rules the
// authoring console must resolve correctly so a published policy means what the operator
// thinks it means. These are pure, testable facts: the connector resolves and EXPLAINS
// precedence, it never enforces it (Codex does).
//
// VERIFIED 2026-06-20 (developers.openai.com/codex/enterprise/managed-configuration):
//
//	REQUIREMENTS (constraints) — checked in order, FIRST value wins:
//	  1. Cloud-managed requirements (ChatGPT Business/Enterprise, fetched from the service)
//	  2. macOS managed preferences (MDM) via com.openai.codex:requirements_toml_base64
//	  3. System requirements.toml (/etc/codex on Unix, %ProgramData%\OpenAI\Codex on Windows)
//	  Scalar requirements use that first-value rule. Tables ACCUMULATE across requirement
//	  layers one entry at a time; a later source can add profile/source names, but for the
//	  SAME profile/source name the earlier source wins. Legacy managed_config.toml
//	  approval_policy / sandbox_mode are interpreted by Codex as single-value requirements.
//
//	MANAGED DEFAULTS — effective config assembled TOP overrides BOTTOM:
//	  1. Managed preferences (macOS MDM via com.openai.codex:config_toml_base64) — highest
//	  2. managed_config.toml (system/managed file)
//	  3. config.toml (user's base configuration)
//	  (CLI --config key=value applies to the base, but the managed layers override it —
//	   each run starts from the managed defaults even with local flags.)

// MDM (macOS managed preferences) facts: the preference domain + the two base64-TOML keys.
const (
	// MDMDomain is the macOS managed-preferences application id Codex reads.
	MDMDomain = "com.openai.codex"
	// MDMConfigKey holds base64-encoded managed DEFAULTS (config.toml / managed_config.toml schema).
	MDMConfigKey = "config_toml_base64"
	// MDMRequirementsKey holds base64-encoded REQUIREMENTS (requirements.toml schema).
	MDMRequirementsKey = "requirements_toml_base64"
)

// System-tier file paths (the layer this connector reads).
const (
	SystemRequirementsPathUnix     = "/etc/codex/requirements.toml"
	SystemRequirementsPathWindows  = `%ProgramData%\OpenAI\Codex\requirements.toml`
	SystemManagedConfigPathUnix    = "/etc/codex/managed_config.toml"
	SystemManagedConfigPathWindows = `%ProgramData%\OpenAI\Codex\managed_config.toml`
)

// RequirementsTier is one source of the requirements (constraints) precedence chain.
type RequirementsTier string

const (
	// TierCloudManaged is the highest requirements tier (ChatGPT Business/Enterprise).
	TierCloudManaged RequirementsTier = "cloud-managed"
	// TierMDMRequirements is the macOS MDM requirements preference (com.openai.codex).
	TierMDMRequirements RequirementsTier = "mdm"
	// TierSystemRequirements is the system requirements.toml file (the tier this connector reads).
	TierSystemRequirements RequirementsTier = "system-file"
)

// requirementsPrecedence is the verified first-wins order (index 0 = highest).
var requirementsPrecedence = []RequirementsTier{TierCloudManaged, TierMDMRequirements, TierSystemRequirements}

// RequirementsPrecedence returns the verified requirements source order (high->low).
func RequirementsPrecedence() []RequirementsTier {
	return append([]RequirementsTier(nil), requirementsPrecedence...)
}

// DryRunLine is one precedence/effect line of a managed dry-run (the shape the authoring
// layer maps onto a console resolved view). It carries no policy VALUES — only the scope
// label and a non-sensitive explanation.
type DryRunLine struct {
	Scope string `json:"scope"`
	Note  string `json:"note"`
}

// PrecedencePreview returns the human-readable precedence/effect lines for the authoring
// console's dry-run. It carries the verified ordering + the honesty caveat that the
// control plane reads only the SYSTEM tier — the cloud-managed and MDM tiers sit above
// the file and cannot be observed from here.
func PrecedencePreview() []DryRunLine {
	return []DryRunLine{
		{
			Scope: "requirements-precedence",
			Note:  "requirements (constraints) resolve cloud-managed > MDM > system-file / FIRST-WINS: (1) cloud-managed requirements (ChatGPT Business/Enterprise), (2) macOS MDM com.openai.codex:" + MDMRequirementsKey + ", (3) system requirements.toml (" + SystemRequirementsPathUnix + " on Unix). Scalar settings use the first source that sets a value; tables accumulate entry-by-entry, and for the same profile/source name the earlier source wins",
		},
		{
			Scope: "managed-defaults-precedence",
			Note:  "managed defaults resolve MDM > managed_config.toml > config.toml / TOP-OVERRIDES-BOTTOM: macOS MDM com.openai.codex:" + MDMConfigKey + " > managed_config.toml (" + SystemManagedConfigPathUnix + ") > the user's config.toml. CLI --config applies to the base but the managed layers override it, so each run starts from the managed defaults",
		},
		{
			Scope: "enforcement",
			Note:  "requirements are constraints users cannot override: when a user config value conflicts with a requirement, Codex FALLS BACK to a compatible value and notifies the user (a clamp, not a session reject). A managed_config.toml default that violates a requirement, or a [features] pin conflict, is REJECTED",
		},
		{
			Scope: "mdm-payload",
			Note:  "the macOS MDM tiers are base64-encoded TOML under the " + MDMDomain + " preference domain: " + MDMRequirementsKey + " (requirements.toml) and " + MDMConfigKey + " (managed_config.toml) — distribute the rendered files there for Macs",
		},
		{
			Scope: "verification-scope (per-client / honesty)",
			Note:  "this connector reads the SYSTEM-tier files only. The cloud-managed and MDM tiers have HIGHER precedence and cannot be observed from the control plane's environment — so an absent system file means the host is unverifiable-from-here, NOT necessarily ungoverned (a ChatGPT-Business host may be governed entirely by cloud-managed requirements). Conversely, an air-gapped or non-cloud-signed host relies ENTIRELY on the system file",
		},
	}
}
