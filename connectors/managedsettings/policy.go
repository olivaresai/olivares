// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"encoding/json"
	"strings"
)

// Permission modes a managed policy may pin (permissions.defaultMode). The
// friction-reducing modes are exactly what the non-overridable flags exist to
// constrain. Source: https://code.claude.com/docs/en/permissions
const (
	ModeDefault           = "default"
	ModePlan              = "plan"
	ModeAcceptEdits       = "acceptEdits"
	ModeAuto              = "auto"
	ModeDontAsk           = "dontAsk"
	ModeBypassPermissions = "bypassPermissions"
)

// disableMarker is the literal value Claude Code expects for the disable* keys
// (permissions.disableBypassPermissionsMode / disableAutoMode): the string
// "disable", not a boolean.
const disableMarker = "disable"

// parentSettingsBehavior values (VERIFIED 2026-06-10, docs.claude.com/en/docs/
// claude-code/settings; changelog 2.1.133). The managed-only key controls whether
// managed settings supplied PROGRAMMATICALLY by an embedding host process (the
// Agent SDK, an IDE extension) apply when an admin-deployed managed tier is also
// present: "first-wins" (the default) DROPS the parent-supplied settings entirely;
// "merge" applies them UNDER the admin tier, filtered so they can TIGHTEN policy
// but never loosen it. No effect when no admin tier is deployed.
const (
	ParentFirstWins = "first-wins"
	ParentMerge     = "merge"
)

// fallbackModelMax is the documented cap on the fallbackModel chain (VERIFIED
// 2026-06-10; changelog 2.1.166): "Chains are capped at three models; extra
// entries are ignored." Authoring more than three is dead policy — ValidateJSON
// flags it rather than publishing entries the client will silently ignore.
const fallbackModelMax = 3

// Skill-visibility states for skillOverrides (VERIFIED 2026-06-16 against
// code.claude.com/docs/en/skills). A per-skill override maps a skill NAME to
// exactly one of these four states; a skill ABSENT from the map is treated
// as SkillOn. The two right-hand columns are (listed-to-the-model, shown-in-/-menu).
const (
	SkillOn                = "on"                  // name + description listed to the model; in the / menu
	SkillNameOnly          = "name-only"           // name only listed to the model; in the / menu
	SkillUserInvocableOnly = "user-invocable-only" // HIDDEN from the model; still user-invocable in the / menu
	SkillOff               = "off"                 // hidden from the model AND the / menu
)

// skillOverrideDefault is the EFFECTIVE state of a skill absent from skillOverrides.
const skillOverrideDefault = SkillOn

// PolicyHelper is the governance-authored policyHelper intent: the path to the admin-
// deployed executable that computes managed settings dynamically at startup. Only the
// `path` field is part of the CORROBORATED wire shape (VERIFIED 2026-06-16, two
// independent reads of code.claude.com/docs/en/settings; example value
// {"path": "/usr/local/bin/claude-policy"}). The richer runtime contract (timeoutMs/
// refreshIntervalMs, the stdout `managedSettings` envelope, fail-closed exit codes) was
// single-read / to-confirm and is deliberately NOT modeled — see Policy.PolicyHelper.
type PolicyHelper struct {
	Path string `json:"path"`
}

// MCPServerRule is one allowedMcpServers/deniedMcpServers PREDICATE entry
// (VERIFIED 2026-06-10, docs.claude.com/en/docs/claude-code/{settings,managed-mcp};
// glob deny support changelog 2.1.166). Exactly one selector is set:
//
//   - Name (wire: serverName) matches the user-assigned server label EXACTLY —
//     "wildcards are not expanded", so a '*' in a name is a footgun that matches
//     nothing (ValidateJSON rejects it).
//   - URL (wire: serverUrl) matches a remote server URL, "exact or with `*`
//     wildcards anywhere in the pattern, including the scheme".
//
// The wire predicates compare as AUTHORED PATTERNS in drift — the connector never
// expands a glob (it verifies the host carries the org's rule, not its closure).
type MCPServerRule struct {
	Name string `json:"server_name,omitempty"`
	URL  string `json:"server_url,omitempty"`
}

// UnmarshalJSON accepts the canonical object form AND a bare JSON string (treated
// as a serverName), so operator-authored expected_policy documents written against
// the pre []string surface keep decoding. The wire predicate keys
// (serverName/serverUrl) are also accepted so one type serves both halves.
func (r *MCPServerRule) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*r = MCPServerRule{Name: s}
		return nil
	}
	var obj struct {
		// Authored (snake_case) and wire (camelCase) spellings of the predicates.
		Name     string `json:"server_name"`
		URL      string `json:"server_url"`
		WireName string `json:"serverName"`
		WireURL  string `json:"serverUrl"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	r.Name = firstNonEmpty(obj.Name, obj.WireName)
	r.URL = firstNonEmpty(obj.URL, obj.WireURL)
	return nil
}

// MCPServersByName builds name-predicate rules from server labels — the helper the
// authoring surfaces (and the cowork bridge) use for the common named-server form.
func MCPServersByName(names ...string) []MCPServerRule {
	out := make([]MCPServerRule, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, MCPServerRule{Name: n})
		}
	}
	return out
}

// Policy is the GOVERNANCE-AUTHORED intent: the org's desired managed-settings
// posture in clean, boolean-where-possible form. It is the input to Render (to
// emit the JSON) and the expected reference in verification (to detect drift).
// Fields map 1:1 to managed-settings.json keys; the booleans are rendered to
// Claude Code's wire form (e.g. DisableBypassPermissionsMode → "disable").
type Policy struct {
	Permissions Permissions `json:"permissions"`

	// MCP governance (predicates upgraded for 2.1.17x — VERIFIED 2026-06-10).
	// AllowedMCPServers is THREE-STATE like StrictKnownMarketplaces: nil = not
	// authored (no restriction); a non-nil EMPTY slice = the `[]` complete-lockdown
	// posture (no MCP server may be configured); a non-empty slice = the allowlist.
	// The denylist always takes precedence over the allowlist and applies to ALL
	// scopes including managed servers. Invalid-value semantics on the client are
	// ASYMMETRIC (changelog 2.1.154/2.1.169): an invalid allowedMcpServers value is
	// enforced as an EMPTY allowlist (fail-closed); a wholly-invalid
	// deniedMcpServers is dropped with a warning (fail-open).
	AllowedMCPServers          *[]MCPServerRule `json:"allowed_mcp_servers,omitempty"`
	DeniedMCPServers           []MCPServerRule  `json:"denied_mcp_servers,omitempty"`
	AllowManagedMCPServersOnly bool             `json:"allow_managed_mcp_servers_only,omitempty"`

	// Permission-rule lockdown.
	AllowManagedPermissionRulesOnly bool `json:"allow_managed_permission_rules_only,omitempty"`

	// Plugin/marketplace governance. StrictKnownMarketplaces is the managed-only plugin-
	// marketplace ALLOWLIST (VERIFIED 2026-06-09: an array of source objects, NOT a bool —
	// see marketplace.go). Its THREE states are distinguished by the pointer: nil = not
	// authored (no restriction); a non-nil EMPTY slice = the `[]` complete-lockdown posture
	// (no marketplace may be added); a non-empty slice = the exact-match allowlist.
	// BlockedMarketplaces is the managed-only blocklist (same entry shape).
	StrictKnownMarketplaces       *[]Marketplace `json:"strict_known_marketplaces,omitempty"`
	BlockedMarketplaces           []Marketplace  `json:"blocked_marketplaces,omitempty"`
	StrictPluginOnlyCustomization bool           `json:"strict_plugin_only_customization,omitempty"`

	// Login federation. The wire forceLoginOrgUUID accepts a SINGLE UUID string
	// (which also PRE-SELECTS that org during login) or an ARRAY of UUIDs (any
	// listed org accepted, no pre-selection) — VERIFIED 2026-06-10 (changelog
	// 2.1.147 enforcement; 2.1.161 third-party fix). Author exactly ONE of the two
	// fields below (both set is a ValidateJSON issue). When set in managed scope,
	// API-key/ANTHROPIC_AUTH_TOKEN/apiKeyHelper sessions are BLOCKED at startup;
	// third-party provider sessions (Bedrock/Vertex/Foundry/Mantle) are NOT blocked
	// (cloud IAM governs those). An EMPTY array fails closed and blocks ALL login.
	ForceLoginMethod   string   `json:"force_login_method,omitempty"` // "claudeai" | "console" | "gateway" (VERIFIED 2026-07-20; v2.1.212+ enforces it across VS Code/SDK/setup-token/install-github-app, not just the terminal)
	ForceLoginOrgUUID  string   `json:"force_login_org_uuid,omitempty"`
	ForceLoginOrgUUIDs []string `json:"force_login_org_uuids,omitempty"`

	// Minimum enforced Claude Code version (the SOFT update floor: prevents
	// downgrades but never blocks startup — distinct from the hard gates below).
	MinimumVersion string `json:"minimum_version,omitempty"`

	// RequiredMinimumVersion / RequiredMaximumVersion are the HARD startup gates
	// (managed-only; VERIFIED 2026-06-10; changelog 2.1.163): outside the range
	// Claude Code EXITS at startup and directs the user to an approved version
	// (claude update/install/doctor keep working for recovery; auto-updates skip
	// versions above the ceiling). They FAIL OPEN by design — an invalid value is
	// STRIPPED rather than enforced — so ValidateJSON flags a non-version-shaped
	// value loudly: publishing it would silently enforce NOTHING. Clients older
	// than 2.1.163 ignore both keys.
	RequiredMinimumVersion string `json:"required_minimum_version,omitempty"`
	RequiredMaximumVersion string `json:"required_maximum_version,omitempty"`

	// FallbackModels is the ordered fallbackModel chain (VERIFIED 2026-06-10;
	// changelog 2.1.166): tried in order when the primary model is overloaded or
	// unavailable; "default" expands to the default model; capped at THREE (extras
	// ignored by the client). UNIQUE precedence semantics: unlike every other array
	// setting it does NOT merge across scopes — the highest-precedence file that
	// defines it supplies the ENTIRE chain (see precedence.go).
	FallbackModels []string `json:"fallback_models,omitempty"`

	// PluginSuggestionMarketplaces (managed-only; VERIFIED 2026-06-10; changelog
	// 2.1.152): marketplace NAMES whose plugins may surface as contextual install
	// suggestions. No marketplace-declared suggestion surfaces without this
	// allowlist. A name only takes effect when the marketplace is registered on the
	// machine AND its registered source is also declared in managed settings
	// (extraKnownMarketplaces or strictKnownMarketplaces); the official marketplace
	// is exempt from the source requirement.
	PluginSuggestionMarketplaces []string `json:"plugin_suggestion_marketplaces,omitempty"`

	// ChannelsEnabled (managed-only; VERIFIED 2026-06-10; changelog 2.1.128): on
	// claude.ai Team/Enterprise, channels are BLOCKED when unset or false; console
	// (API-key) orgs that deploy managed settings must set true to allow channels.
	ChannelsEnabled bool `json:"channels_enabled,omitempty"`

	// ParentSettingsBehavior (managed-only; "first-wins" | "merge"; VERIFIED
	// 2026-06-10; changelog 2.1.133) — see the ParentFirstWins/ParentMerge consts.
	ParentSettingsBehavior string `json:"parent_settings_behavior,omitempty"`

	// DisableBundledSkills (any scope; VERIFIED 2026-06-10; changelog 2.1.169;
	// env equivalent CLAUDE_CODE_DISABLE_BUNDLED_SKILLS=1): removes the bundled
	// skills/workflows entirely (built-in slash commands stay typable but are
	// hidden from the model); plugin and .claude skills are unaffected.
	DisableBundledSkills bool `json:"disable_bundled_skills,omitempty"`

	// --- NET-NEW managed-only keys (VERIFIED 2026-06-08 against
	// code.claude.com/docs/en/{permissions,sandboxing,server-managed-settings}).
	// These take effect ONLY in managed scope (no effect in user/project settings).

	// ForceRemoteSettingsRefresh fail-closes startup: the CLI blocks until a fresh
	// server-managed fetch and EXITS if it fails (self-perpetuating once delivered;
	// `claude auth` is exempt as of v2.1.139). The strongest startup posture.
	ForceRemoteSettingsRefresh bool `json:"force_remote_settings_refresh,omitempty"`

	// AllowManagedHooksOnly loads only managed/SDK/force-enabled-plugin hooks; user,
	// project and other plugin hooks are blocked (hook-supply-chain lockdown).
	AllowManagedHooksOnly bool `json:"allow_managed_hooks_only,omitempty"`

	// AllowManagedDomainsOnly (wire: sandbox.network.allowManagedDomainsOnly) restricts
	// the egress allowlist to managed allowedDomains, blocking non-allowed domains
	// WITHOUT prompting. Denied domains still merge from all sources. The exfiltration-
	// surface lockdown for the sandbox.
	AllowManagedDomainsOnly bool `json:"allow_managed_domains_only,omitempty"`

	// AllowManagedReadPathsOnly (wire: sandbox.filesystem.allowManagedReadPathsOnly)
	// restricts filesystem read paths to managed allowRead entries; user/project/local
	// allowRead are ignored. denyRead still merges from all sources. The secret-read
	// lockdown for the sandbox.
	AllowManagedReadPathsOnly bool `json:"allow_managed_read_paths_only,omitempty"`

	// AutoMode configures the trust of the auto-mode classifier (the second gate that
	// runs AFTER the permissions system). Prose, not regex (the classifier reads the
	// entries as natural-language rules). nil = not authored.
	AutoMode *AutoModePolicy `json:"auto_mode,omitempty"`

	// --- NET-NEW surfaces: managed hooks + telemetry env -------------------

	// Hooks is the managed Claude Code hooks block, keyed by event name (PreToolUse,
	// PostToolUse, …). Its headline use is DISTRIBUTING the PreToolUse PEP hook as
	// a managed (non-overridable) hook — pair it with AllowManagedHooksOnly for anti-
	// tamper. Build the PEP entry with PEPHook. nil/empty = no managed hooks authored.
	Hooks map[string][]HookMatcher `json:"hooks,omitempty"`

	// Env is the managed telemetry/environment block (the sanctioned OBSERVE path): CLAUDE_CODE_ENABLE_TELEMETRY + OTEL_* pointed at the control-plane
	// collector. Build it with TelemetryEnv. It is validated deny-closed against inline
	// credentials (a managed file is plaintext on disk). nil/empty = not authored.
	//
	// Env DOUBLES as the telemetry VERIFICATION expectation: when it sets
	// CLAUDE_CODE_ENABLE_TELEMETRY, driftFindings asserts the live MANAGED env also enables
	// telemetry; when it also sets OTEL_EXPORTER_OTLP_ENDPOINT, drift asserts the live
	// endpoint MATCHES it — the sanctioned OTEL signal must flow to the authored Olivares
	// collector. Because managed-settings env "cannot be overridden by users" (VERIFIED
	// 2026-06-20, code.claude.com/docs/en/monitoring-usage), presence in the live managed env
	// IS the non-overridable assertion; its absence/divergence is drift (the fleet may emit
	// no sanctioned telemetry, or emit it off-Olivares).
	Env map[string]string `json:"env,omitempty"`

	// AuthorizedGatewayBaseURL is the inference gateway the org sanctions (its
	// ANTHROPIC_BASE_URL) — a VERIFICATION-only expectation, NOT a managed-settings.json wire
	// key (Render never emits it; it is not counted in HasAnyKeys). When set, driftFindings
	// flags a live managed env that pins a DIFFERENT ANTHROPIC_BASE_URL: a non-default
	// base-URL routes inference to a custom endpoint and BYPASSES server-managed-settings
	// entirely (VERIFIED 2026-06-20, code.claude.com/docs/en/server-managed-settings —
	// server-managed settings "are not available when using ... a non-default
	// ANTHROPIC_BASE_URL"), so a divergent value means inference left the authorized path. An
	// ABSENT live base-URL is NOT drift (direct api.anthropic.com is the sanctioned path).
	// Empty = not asserted. In the VerifyDriftJSON publish path it is DERIVED from the
	// authored managed env's ANTHROPIC_BASE_URL (fromWire): an org that pins the gateway there
	// is verified against it automatically. Consumers (console, compliance) read this field
	// as the declared authorized gateway.
	AuthorizedGatewayBaseURL string `json:"authorized_gateway_base_url,omitempty"`

	// --- NET-NEW 2026 currency keys (VERIFIED 2026-06-16 via two independent reads
	// of code.claude.com/docs/en/{settings,skills,permissions,server-managed-settings}).
	// These close the last managed-settings.json currency gap and the asymmetry
	// with remote control: Olivares OPERATES it but could not yet GOVERN it.

	// DisableRemoteControl disables Claude Code's Remote Control feature (TOP-LEVEL bool,
	// any-scope, min v2.1.128): it "blocks `claude remote-control`, the `--remote-control`
	// flag, auto-start, and the in-session toggle". Remote Control drives a LOCAL session
	// from claude.ai/code or the Claude app, relaying its I/O to the Anthropic cloud
	// OUTSIDE Olivares' governed transport (verified) — so a fleet that leaves it
	// enabled on unmanaged devices can run sessions entirely past the PEP/budget/recording
	// boundary. Placed in a managed scope it enforces PER-DEVICE, independent of the
	// org-wide admin toggle. This is the GOVERN half of the surface FASE V OPERATES.
	DisableRemoteControl bool `json:"disable_remote_control,omitempty"`

	// SkillOverrides is the per-skill VISIBILITY map (TOP-LEVEL, any-scope, min v2.1.129):
	// it maps a skill NAME to one of exactly four states — SkillOn (name+description listed
	// to the model, in the / menu), SkillNameOnly (name only), SkillUserInvocableOnly
	// (HIDDEN from the model, still user-invocable), SkillOff (hidden from both) — WITHOUT
	// editing the skill's SKILL.md. A skill absent from the map is treated as SkillOn. It
	// does NOT apply to plugin skills (managed via /plugin). It COMPLEMENTS
	// connectors/claude-config/skillscan.go: skillscan GRADES a skill's posture; this
	// GOVERNS whether the model may see/auto-invoke it. NOTE: it is a TOP-LEVEL key — the
	// "policySettings.skillOverrides" nesting some sources show is an internal config-layer
	// name from a reverse-engineered GitHub issue, refuted on the vendor docs.
	SkillOverrides map[string]string `json:"skill_overrides,omitempty"`

	// PolicyHelper is the admin-deployed executable that COMPUTES managed settings
	// dynamically at startup (TOP-LEVEL, managed-only / OS-policy-only, min v2.1.136):
	// "Only honored from MDM or a system managed-settings.json file" — it CANNOT ship via
	// the server-managed tier. Its corroborated wire shape is an OBJECT with a single
	// `path` field (NOT a bare string like apiKeyHelper). The richer runtime contract is
	// to-confirm and deliberately NOT modeled — only `path` is asserted and drift-checked.
	// nil = not authored.
	PolicyHelper *PolicyHelper `json:"policy_helper,omitempty"`

	// --- NET-NEW model-governance keys (VERIFIED 2026-06-27 against
	// code.claude.com/docs/en/{settings,model-config,server-managed-settings,mcp}).

	// AvailableModels is the model ALLOWLIST (TOP-LEVEL array of model aliases/IDs,
	// any-scope, no min-version annotation): "Restrict which models users can select for
	// the main session, subagents, skills, and the advisor. Does not affect the Default
	// option unless enforceAvailableModels is also set." An EMPTY or nil array = no
	// restriction (all models selectable). Entries are model aliases ("sonnet", "haiku")
	// or full model IDs ("claude-sonnet-4-6"). This is a PICKER restriction on the client,
	// NOT deny-closed enforcement — the user can still switch models via --model or
	// ANTHROPIC_MODEL for one session. The Olivares model-access PEP (scopegate) is
	// the deny-closed enforcement layer; this is a posture/drift signal, not a substitute.
	AvailableModels []string `json:"available_models,omitempty"`

	// EnforceAvailableModels extends the availableModels allowlist to the Default model
	// (TOP-LEVEL bool, MANAGED-ONLY, min v2.1.175): "When true in managed settings and
	// availableModels is a non-empty array, the Default option falls back to the first
	// allowlisted entry that is available. Has no effect when availableModels is unset or
	// empty." This is the stronger posture: without it, Default ignores the allowlist.
	EnforceAvailableModels bool `json:"enforce_available_models,omitempty"`

	// DisableClaudeAiConnectors disables claude.ai MCP connectors (TOP-LEVEL bool,
	// any-scope, min v2.1.182): "Disable claude.ai MCP connectors so they are not
	// auto-fetched or connected. true in any source takes precedence, so a checked-in
	// project .claude/settings.json can opt a repo out of cloud connectors, but a
	// project-level false cannot override a user- or policy-level true. Servers passed
	// explicitly via --mcp-config are unaffected." The any-true-wins precedence is
	// ASYMMETRIC: a true cannot be overridden by a lower-precedence false. To deny
	// individual connectors instead of all, use deniedMcpServers.
	DisableClaudeAiConnectors bool `json:"disable_claude_ai_connectors,omitempty"`

	// --- NET-NEW 2026-07 currency keys (VERIFIED 2026-07-03 against
	// code.claude.com/docs/en/{settings,sandboxing}).

	// DisableSideloadFlags (TOP-LEVEL bool, managed-only, min v2.1.193): "Reject
	// the --plugin-dir, --plugin-url, --agents, and --mcp-config CLI flags at
	// startup" so strictKnownMarketplaces cannot be bypassed for one run.
	DisableSideloadFlags bool `json:"disable_sideload_flags,omitempty"`

	// PluginTrustMessage (TOP-LEVEL string, managed-only): "Custom message appended
	// to the plugin trust warning shown before installation." It is informational
	// UX, so verification observes/renders it but does not treat it as lockdown.
	PluginTrustMessage string `json:"plugin_trust_message,omitempty"`

	// DisableSkillShellExecution (TOP-LEVEL bool, managed-only): "Disable inline
	// shell execution" for skills and custom commands from user, project, plugin,
	// or additional-directory sources. Bundled and managed skills are not affected.
	DisableSkillShellExecution bool `json:"disable_skill_shell_execution,omitempty"`

	// SandboxCredentials (wire: sandbox.credentials, min v2.1.187; mask min
	// v2.1.199) restricts listed credential files and env vars in sandboxed
	// commands. "There is no built-in credential deny list"; the default read policy
	// still allows files such as ~/.aws/credentials and ~/.ssh unless listed here.
	// allowPlaintextInject is preserved as observable presence without asserting its
	// finer shape.
	//
	// CURRENCY (VERIFIED 2026-07-20 against code.claude.com/docs/en/sandboxing): this
	// key is CURRENT — it landed at v2.1.187 (NOT 2.1.208, as an premise
	// assumed) and is UNCHANGED at/after 2.1.208; there is NO "settings picker"
	// feature (0 hits in the changelog/settings docs — do not model one). Enforcement
	// point: sandbox.credentials is an OS-sandbox layer (Seatbelt/bubblewrap for file
	// denials; the network proxy for env `mask`) BENEATH the hook/permission PEP — the
	// hook-PEP never sees it. The hook↔sandbox seam that DID move (also VERIFIED): as
	// of v2.1.211 a PreToolUse hook's `ask` now FLOORS the decision at a prompt (auto
	// mode no longer overrides it for unsandboxed Bash) — favorable to the hook-PEP, no
	// change required here.
	SandboxCredentials *msSandboxCredentials `json:"sandbox_credentials,omitempty"`
}

// AutoModePolicy is the governance-authored trust configuration for the auto-mode
// classifier (VERIFIED 2026-06-08 code.claude.com/docs/en/auto-mode-config). Entries
// are PROSE (natural-language rules), never regex/tool patterns. Precedence inside
// the classifier: hard_deny (unconditional) > soft_deny (user intent / allow can
// override) > allow (exceptions to soft_deny) > explicit user intent. Managed entries
// can be EXTENDED but not REMOVED by a developer.
type AutoModePolicy struct {
	// Environment tells the classifier which repos/buckets/domains are trusted (what
	// "external" means); anything not listed is a potential exfiltration target.
	Environment []string `json:"environment,omitempty"`
	// Allow are prose exceptions that override matching soft_deny rules.
	Allow []string `json:"allow,omitempty"`
	// SoftDeny are destructive actions that explicit user intent / an allow can clear.
	SoftDeny []string `json:"soft_deny,omitempty"`
	// HardDeny are unconditional security boundaries (user intent does not override).
	HardDeny []string `json:"hard_deny,omitempty"`
}

// hasAny reports whether the auto-mode policy carries any authored content.
func (a *AutoModePolicy) hasAny() bool {
	return a != nil && (len(a.Environment) > 0 || len(a.Allow) > 0 || len(a.SoftDeny) > 0 || len(a.HardDeny) > 0)
}

// Permissions is the governance-authored permission posture.
type Permissions struct {
	Allow                 []string `json:"allow,omitempty"`
	Deny                  []string `json:"deny,omitempty"`
	Ask                   []string `json:"ask,omitempty"`
	DefaultMode           string   `json:"default_mode,omitempty"`
	AdditionalDirectories []string `json:"additional_directories,omitempty"`

	// DisableBypassPermissionsMode forbids the most dangerous mode (the agent runs
	// without any permission prompts). DisableAutoMode forbids auto mode.
	DisableBypassPermissionsMode bool `json:"disable_bypass_permissions_mode,omitempty"`
	DisableAutoMode              bool `json:"disable_auto_mode,omitempty"`
}

// --- on-disk schema (the exact managed-settings.json wire shape) -------------

// managedSettings is the subset of the live managed-settings.json this connector
// reads/writes. Unknown keys are ignored on read (forward-compatible) and omitted
// on write. The disable* keys and strictPluginOnlyCustomization use Claude Code's
// non-boolean wire forms, captured as RawMessage so a present-but-other shape is
// still observable rather than a decode failure.
type managedSettings struct {
	Permissions *msPermissions `json:"permissions,omitempty"`
	// allowedMcpServers is RawMessage (not a slice) so the `[]` complete-lockdown
	// posture is REPRESENTABLE and round-trips: a []RawMessage+omitempty would drop
	// an empty-present array on render, silently un-authoring the lockdown (same precedent as strictKnownMarketplaces). deniedMcpServers keeps the list
	// form (an empty denylist blocks nothing — same as absent).
	AllowedMcpServers               json.RawMessage   `json:"allowedMcpServers,omitempty"`
	DeniedMcpServers                []json.RawMessage `json:"deniedMcpServers,omitempty"`
	AllowManagedMcpServersOnly      bool              `json:"allowManagedMcpServersOnly,omitempty"`
	AllowManagedPermissionRulesOnly bool              `json:"allowManagedPermissionRulesOnly,omitempty"`
	// strictKnownMarketplaces / blockedMarketplaces are ARRAYS of marketplace-source
	// objects on the wire (VERIFIED 2026-06-09). Captured as RawMessage — like
	// strictPluginOnlyCustomization — so a present-but-other shape (a legacy/hostile bool)
	// is still OBSERVABLE (and drift-able) rather than failing the whole parse.
	StrictKnownMarketplaces       json.RawMessage `json:"strictKnownMarketplaces,omitempty"`
	BlockedMarketplaces           json.RawMessage `json:"blockedMarketplaces,omitempty"`
	StrictPluginOnlyCustomization json.RawMessage `json:"strictPluginOnlyCustomization,omitempty"`
	ForceLoginMethod              string          `json:"forceLoginMethod,omitempty"`
	// forceLoginOrgUUID is RawMessage: the wire accepts a single UUID STRING (also
	// pre-selects the org) OR an ARRAY of UUIDs (VERIFIED 2026-06-10). A typed
	// string field would fail the WHOLE parse on the array form, mis-reporting the
	// host as "present but invalid JSON" (ungoverned) — the marketplace lesson.
	ForceLoginOrgUUID json.RawMessage `json:"forceLoginOrgUUID,omitempty"`
	MinimumVersion    string          `json:"minimumVersion,omitempty"`

	// --- NET-NEW 2.1.17x keys (VERIFIED 2026-06-10 docs.claude.com settings
	// page + raw changelog; per-key semantics on the Policy fields). fallbackModel
	// is RawMessage because the wire accepts a string OR an array.
	RequiredMinimumVersion       string          `json:"requiredMinimumVersion,omitempty"`
	RequiredMaximumVersion       string          `json:"requiredMaximumVersion,omitempty"`
	FallbackModel                json.RawMessage `json:"fallbackModel,omitempty"`
	PluginSuggestionMarketplaces []string        `json:"pluginSuggestionMarketplaces,omitempty"`
	ChannelsEnabled              bool            `json:"channelsEnabled,omitempty"`
	ParentSettingsBehavior       string          `json:"parentSettingsBehavior,omitempty"`
	DisableBundledSkills         bool            `json:"disableBundledSkills,omitempty"`

	// NET-NEW managed-only keys (A). The sandbox lockdown keys live UNDER the
	// `sandbox` object (sandbox.network / sandbox.filesystem) on the wire; the rest
	// are top-level. Unknown keys are ignored on read (forward-compatible).
	ForceRemoteSettingsRefresh bool        `json:"forceRemoteSettingsRefresh,omitempty"`
	AllowManagedHooksOnly      bool        `json:"allowManagedHooksOnly,omitempty"`
	AutoMode                   *msAutoMode `json:"autoMode,omitempty"`
	Sandbox                    *msSandbox  `json:"sandbox,omitempty"`

	// NET-NEW: managed hooks + telemetry env. Their wire JSON is identical to the
	// authored form, so the same HookMatcher / string-map types serve both halves; the
	// round-trip (Render→fromWire) preserves them for drift.
	Hooks map[string][]HookMatcher `json:"hooks,omitempty"`
	Env   map[string]string        `json:"env,omitempty"`

	// NET-NEW 2026 currency keys (VERIFIED 2026-06-16). disableRemoteControl is a
	// top-level bool; skillOverrides is a top-level skill-name→state map; policyHelper is a
	// top-level object whose only ASSERTED field is `path` — unknown sibling keys
	// (timeoutMs/refreshIntervalMs, to-confirm) are ignored on read (forward-compatible),
	// and a non-object policyHelper value is caught by the ValidateJSON typed decode.
	DisableRemoteControl bool              `json:"disableRemoteControl,omitempty"`
	SkillOverrides       map[string]string `json:"skillOverrides,omitempty"`
	PolicyHelper         *msPolicyHelper   `json:"policyHelper,omitempty"`

	// NET-NEW model-governance keys (VERIFIED 2026-06-27).
	AvailableModels           []string `json:"availableModels,omitempty"`
	EnforceAvailableModels    bool     `json:"enforceAvailableModels,omitempty"`
	DisableClaudeAiConnectors bool     `json:"disableClaudeAiConnectors,omitempty"`

	// NET-NEW 2026-07 currency keys (VERIFIED 2026-07-03).
	DisableSideloadFlags       bool   `json:"disableSideloadFlags,omitempty"`
	PluginTrustMessage         string `json:"pluginTrustMessage,omitempty"`
	DisableSkillShellExecution bool   `json:"disableSkillShellExecution,omitempty"`
}

// msPolicyHelper is the corroborated policyHelper wire shape: an object with a string
// `path`. Unknown sibling keys are ignored on read so a richer host object is still
// parsed (its `path` extracted) rather than failing the whole parse.
type msPolicyHelper struct {
	Path string `json:"path"`
}

// policyHelperPath returns the corroborated `path` of the live policyHelper and whether
// the object is present at all (a present object with an empty path is present=true).
func (m managedSettings) policyHelperPath() (path string, present bool) {
	if m.PolicyHelper == nil {
		return "", false
	}
	return strings.TrimSpace(m.PolicyHelper.Path), true
}

// msAutoMode is the exact autoMode wire shape (prose arrays).
type msAutoMode struct {
	Environment []string `json:"environment,omitempty"`
	Allow       []string `json:"allow,omitempty"`
	SoftDeny    []string `json:"soft_deny,omitempty"`
	HardDeny    []string `json:"hard_deny,omitempty"`
}

// msSandbox is the subset of the `sandbox` object carrying the managed-only lockdown
// flags. Unknown sandbox sub-keys (allowRead/denyRead/proxy ports, …) are ignored on
// read so a present-but-richer sandbox config is still observable.
type msSandbox struct {
	Network     *msSandboxNetwork     `json:"network,omitempty"`
	Filesystem  *msSandboxFilesystem  `json:"filesystem,omitempty"`
	Credentials *msSandboxCredentials `json:"credentials,omitempty"`
}

type msSandboxNetwork struct {
	AllowManagedDomainsOnly bool `json:"allowManagedDomainsOnly,omitempty"`
}

type msSandboxFilesystem struct {
	AllowManagedReadPathsOnly bool `json:"allowManagedReadPathsOnly,omitempty"`
}

type msSandboxCredentials struct {
	Files                []msCredentialFileRule `json:"files,omitempty"`
	EnvVars              []msCredentialEnvRule  `json:"envVars,omitempty"`
	AllowPlaintextInject json.RawMessage        `json:"allowPlaintextInject,omitempty"` // observable presence; shape not asserted
}

// SandboxCredentials is the exported authoring alias for sandbox.credentials.
type SandboxCredentials = msSandboxCredentials

type msCredentialFileRule struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// CredentialFileRule is the exported authoring alias for sandbox.credentials.files.
type CredentialFileRule = msCredentialFileRule

type msCredentialEnvRule struct {
	Name        string   `json:"name"`
	Mode        string   `json:"mode"`
	InjectHosts []string `json:"injectHosts,omitempty"`
}

// CredentialEnvRule is the exported authoring alias for sandbox.credentials.envVars.
type CredentialEnvRule = msCredentialEnvRule

// domainsLockdown reports whether the live config locks egress to managed domains
// (sandbox.network.allowManagedDomainsOnly).
func (m managedSettings) domainsLockdown() bool {
	return m.Sandbox != nil && m.Sandbox.Network != nil && m.Sandbox.Network.AllowManagedDomainsOnly
}

// readPathsLockdown reports whether the live config locks read paths to managed
// allowRead (sandbox.filesystem.allowManagedReadPathsOnly).
func (m managedSettings) readPathsLockdown() bool {
	return m.Sandbox != nil && m.Sandbox.Filesystem != nil && m.Sandbox.Filesystem.AllowManagedReadPathsOnly
}

// credentialsProtectionSet reports whether sandbox.credentials carries at least one
// file/env credential restriction. allowPlaintextInject alone is observable but does
// not restrict any credential by itself.
func (m managedSettings) credentialsProtectionSet() bool {
	return m.Sandbox != nil && m.Sandbox.Credentials != nil && m.Sandbox.Credentials.protectionSet()
}

// credentialMaskUsed reports whether sandbox.credentials.envVars uses the v2.1.199+
// mask mode for any environment variable.
func (m managedSettings) credentialMaskUsed() bool {
	if m.Sandbox == nil || m.Sandbox.Credentials == nil {
		return false
	}
	for _, env := range m.Sandbox.Credentials.EnvVars {
		if strings.TrimSpace(env.Mode) == "mask" {
			return true
		}
	}
	return false
}

func (c *msSandboxCredentials) protectionSet() bool {
	return c != nil && (len(c.Files) > 0 || len(c.EnvVars) > 0)
}

func (c *msSandboxCredentials) hasAny() bool {
	return c.protectionSet() || (c != nil && rawPresent(c.AllowPlaintextInject))
}

// sideloadFlagRelevant reports whether the live policy asserts a marketplace/MCP
// lockdown that documented sideload CLI flags can bypass for one run. A PRESENT
// strictKnownMarketplaces counts even when the array is empty — `[]` is the
// complete-lockdown posture, the configuration MOST exposed to the bypass.
func (m managedSettings) sideloadFlagRelevant() bool {
	if m.AllowManagedMcpServersOnly {
		return true
	}
	if _, present := liveMarketplaces(m.StrictKnownMarketplaces); present {
		return true
	}
	return rawPresent(m.BlockedMarketplaces)
}

// sameSandboxCredentials compares the typed sandbox.credentials block by canonical
// JSON. The block has no maps, so this is stable for equal authored/rendered values.
func sameSandboxCredentials(a, b *msSandboxCredentials) bool {
	if !a.hasAny() && !b.hasAny() {
		return true
	}
	aj, aerr := json.Marshal(a)
	bj, berr := json.Marshal(b)
	return aerr == nil && berr == nil && string(aj) == string(bj)
}

// autoModeSet reports whether the live config declares any autoMode trust entries.
func (m managedSettings) autoModeSet() bool {
	a := m.AutoMode
	return a != nil && (len(a.Environment) > 0 || len(a.Allow) > 0 || len(a.SoftDeny) > 0 || len(a.HardDeny) > 0)
}

type msPermissions struct {
	Allow                        []string `json:"allow,omitempty"`
	Deny                         []string `json:"deny,omitempty"`
	Ask                          []string `json:"ask,omitempty"`
	DefaultMode                  string   `json:"defaultMode,omitempty"`
	AdditionalDirectories        []string `json:"additionalDirectories,omitempty"`
	DisableBypassPermissionsMode string   `json:"disableBypassPermissionsMode,omitempty"`
	DisableAutoMode              string   `json:"disableAutoMode,omitempty"`
}

// bypassDisabled reports whether the live config has the bypass-permissions mode
// disabled (the literal "disable" marker).
func (m msPermissions) bypassDisabled() bool { return m.DisableBypassPermissionsMode == disableMarker }

// autoDisabled reports whether the live config has auto mode disabled.
func (m msPermissions) autoDisabled() bool { return m.DisableAutoMode == disableMarker }

// strictPluginCustomizationSet reports whether strictPluginOnlyCustomization is
// truthy on disk. Claude Code accepts either a bool or an array of surfaces
// (["skills","hooks",…]); either non-empty form counts as "set".
func (m managedSettings) strictPluginCustomizationSet() bool {
	raw := m.StrictPluginOnlyCustomization
	if len(raw) == 0 {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return len(arr) > 0
	}
	return false
}

// knownSkillState reports whether v is one of the four documented skillOverrides states.
func knownSkillState(v string) bool {
	switch v {
	case SkillOn, SkillNameOnly, SkillUserInvocableOnly, SkillOff:
		return true
	default:
		return false
	}
}

// skillOverrideState returns the EFFECTIVE state of a named skill under a skillOverrides
// map: the authored value, or the SkillOn default when the skill is absent.
func skillOverrideState(overrides map[string]string, skill string) string {
	if v, ok := overrides[skill]; ok {
		return v
	}
	return skillOverrideDefault
}

// parseLive decodes the live managed-settings.json. A nil/empty body decodes to a
// zero managedSettings; a malformed body returns the decode error.
func parseLive(data []byte) (managedSettings, error) {
	var m managedSettings
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return managedSettings{}, err
	}
	return m, nil
}
