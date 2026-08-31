// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import "strings"

// precedence.go CODIFIES the Claude Code settings PRECEDENCE semantics (B2) — the
// rules the authoring console must resolve correctly so a published managed policy means
// what the operator thinks it means. delivery.go models the TWO managed DELIVERY tiers
// (server vs endpoint); this models the FULL scope hierarchy below them and the
// merge-vs-override + enforce-before-exec rules. These are pure, testable facts: the
// connector resolves and EXPLAINS precedence, it never enforces it (Claude Code does).
//
// VERIFIED 2026-06-09 (code.claude.com/docs/en/settings), recorded in
//
//
//	Hierarchy (high→low): "Managed (cannot be overridden) > CLI args > Local > Project >
//	User." Permission RULES MERGE across scopes (they do NOT override). Managed-only
//	lockdown keys make the ALLOWLIST managed-only while the DENYLIST always merges from all
//	scopes. Marketplace/permission enforcement happens BEFORE the network/filesystem op.

// SettingsScope is one level of the Claude Code settings hierarchy.
type SettingsScope string

const (
	// ScopeManaged is the highest tier and CANNOT be overridden — not even by CLI args.
	// It is where both managed DELIVERY tiers (server/endpoint, delivery.go) sit.
	ScopeManaged SettingsScope = "managed"
	// ScopeCLIArgs are command-line flags (a temporary session override of the lower tiers).
	ScopeCLIArgs SettingsScope = "cli-args"
	// ScopeLocal overrides project and user (a developer's per-checkout settings.local.json).
	ScopeLocal SettingsScope = "local"
	// ScopeProject overrides user (the repo's .claude/settings.json).
	ScopeProject SettingsScope = "project"
	// ScopeUser is the lowest tier (~/.claude/settings.json).
	ScopeUser SettingsScope = "user"
)

// settingsPrecedence is the verified high→low order. Index 0 is the highest precedence.
var settingsPrecedence = []SettingsScope{ScopeManaged, ScopeCLIArgs, ScopeLocal, ScopeProject, ScopeUser}

// EnvSafeMode is the safe-mode env var (VERIFIED 2026-06-10, docs.claude.com/en/
// docs/claude-code/cli-reference; changelog 2.1.169). There is NO `safeMode`
// settings key — safe mode is the --safe-mode CLI flag (which sets this env var).
// Its governance posture cuts BOTH ways and the console must explain it honestly:
//
//   - it is NOT a policy bypass: "Managed settings policy still applies, including
//     policy-configured hooks, status line, and file-suggestion commands" — the
//     managed PEP hook keeps gating under safe mode;
//   - but managed PLUGINS, managed SKILLS, managed CLAUDE.md and policy-configured
//     MCP SERVERS do NOT load under it — a fleet whose controls ride those
//     surfaces loses them when a developer starts with --safe-mode.
const EnvSafeMode = "CLAUDE_CODE_SAFE_MODE"

// precedenceRank returns a scope's rank (0 = highest precedence). An unknown scope ranks
// below every known one (it can never win an override).
func precedenceRank(s SettingsScope) int {
	for i, sc := range settingsPrecedence {
		if sc == s {
			return i
		}
	}
	return len(settingsPrecedence)
}

// ScopeOutranks reports whether scope a takes precedence over scope b (a is higher in the
// hierarchy). It is the primitive the override resolution uses; ScopeManaged outranks
// every other scope (the non-overridable invariant).
func ScopeOutranks(a, b SettingsScope) bool {
	return precedenceRank(a) < precedenceRank(b)
}

// EffectiveScope returns the scope that WINS for an OVERRIDE-semantics setting given the
// scopes that set it — the highest-precedence present scope. ok is false when no listed
// scope is present (the setting is unset everywhere). Order of the input does not matter.
// (Permission RULES do not use this — they merge; see RulesMerge.)
func EffectiveScope(present []SettingsScope) (SettingsScope, bool) {
	best, ok := SettingsScope(""), false
	for _, s := range present {
		if !ok || ScopeOutranks(s, best) {
			best, ok = s, true
		}
	}
	return best, ok
}

// RulesMerge is the verified fact that PERMISSION RULES (allow/deny/ask) MERGE across
// every scope rather than override. A managed deny is therefore ALWAYS in force (a lower
// scope can never remove it), and a managed allow adds to — never replaces — lower-scope
// allows (unless allowManagedPermissionRulesOnly locks rule authorship to managed). It is
// a const-true documented invariant used by the preview and asserted by tests.
const RulesMerge = true

// AllowlistSources returns the scopes that contribute to a MANAGED-ONLY-capable ALLOWLIST
// (allowedMcpServers, sandbox allowRead, sandbox allowedDomains, the marketplace
// allowlist) given whether its managed-only lockdown flag is set. When locked, ONLY the
// managed scope's allowlist is honored; otherwise the allowlist merges from all scopes.
func AllowlistSources(managedOnlyLockdown bool) []SettingsScope {
	if managedOnlyLockdown {
		return []SettingsScope{ScopeManaged}
	}
	return append([]SettingsScope(nil), settingsPrecedence...)
}

// DenylistAlwaysMerges is the verified fact that a DENYLIST (deniedMcpServers, denyRead,
// deniedDomains, permissions.deny) ALWAYS merges from ALL scopes — even under a managed-
// only allowlist lockdown. Deny is never weakened by a higher-tier allowlist lockdown.
const DenylistAlwaysMerges = true

// EnforcePoint is one verified BEFORE-EXECUTION enforcement fact: the governance check
// `Gate` runs at `When`, BEFORE the side-effecting operation `BeforeOp`. These encode the
// enforce-before-exec/network/fs guarantees the brief requires the authoring to surface.
type EnforcePoint struct {
	Gate     string `json:"gate"`
	When     string `json:"when"`
	BeforeOp string `json:"before_op"`
}

// EnforceBeforeExec returns the verified ordered enforcement points (B2). They are facts
// the console surfaces so an operator knows a managed restriction takes effect BEFORE the
// dangerous operation — not after the agent has already acted/fetched.
func EnforceBeforeExec() []EnforcePoint {
	return []EnforcePoint{
		{
			Gate:     "marketplace allowlist/blocklist (strictKnownMarketplaces / blockedMarketplaces)",
			When:     "on marketplace add and on plugin install, update, refresh, and auto-update",
			BeforeOp: "any network fetch or filesystem write — blocked sources are checked BEFORE downloading, so they never touch the filesystem",
		},
		{
			Gate:     "managed permissions.deny rules",
			When:     "before the permission allow rules and before the auto-mode classifier",
			BeforeOp: "the tool/Bash/file action — a managed deny blocks first and cannot be overridden",
		},
		{
			Gate:     "forceRemoteSettingsRefresh fail-closed startup",
			When:     "at process startup, before the session is usable",
			BeforeOp: "any agent work — the CLI blocks until a fresh managed fetch and EXITS if it fails",
		},
		{
			Gate:     "sandbox network/filesystem managed lockdown (allowManagedDomainsOnly / allowManagedReadPathsOnly)",
			When:     "before egress / before a filesystem read",
			BeforeOp: "the network connection / the file read — a non-allowed domain is blocked WITHOUT prompting; a non-managed read path is ignored",
		},
	}
}

// PrecedencePreview returns the human-readable precedence/merge/enforce lines for the
// authoring console's dry-run (B2). It carries NO settings values — only the resolved
// hierarchy and the non-sensitive rules — so the console can EXPLAIN how a managed policy
// will resolve against the lower tiers without faking enforcement.
func PrecedencePreview() []DryRunLine {
	lines := []DryRunLine{
		{
			Scope: "hierarchy",
			Note:  "settings precedence (high→low): managed (CANNOT be overridden) > CLI args > local > project > user; a managed value wins over everything, including command-line arguments",
		},
		{
			Scope: "permission-rules-merge",
			Note:  "permission rules (allow/deny/ask) MERGE across all scopes — they do not override; a managed deny is therefore always in force and a lower scope can never remove it",
		},
		{
			Scope: "managed-only-allowlist",
			Note:  "when a managed-only lockdown is set (allowManagedMcpServersOnly / sandbox.network.allowManagedDomainsOnly / sandbox.filesystem.allowManagedReadPathsOnly / strictKnownMarketplaces), ONLY the managed allowlist is honored — but the matching DENYLIST still merges from every scope",
		},
		{
			Scope: "sandbox-credentials-merge",
			Note:  "sandbox.credentials deny entries merge from all scopes and only tighten policy; env mask and allowPlaintextInject are honored only from user/managed/CLI settings (project/.local ignored), and deny takes precedence over mask for the same variable",
		},
		// NET-NEW 2.1.17x precedence facts (VERIFIED 2026-06-10).
		{
			Scope: "fallback-model-no-merge",
			Note:  "fallbackModel is the ONE array setting that does NOT merge across scopes: position carries meaning in the chain, so the highest-precedence file that defines it supplies the ENTIRE value (chains cap at three models; --fallback-model overrides for one session)",
		},
		{
			Scope: "parent-settings-tier",
			Note:  "managed settings supplied programmatically by an embedding host process (Agent SDK / IDE extension) form a PARENT tier governed by parentSettingsBehavior: \"first-wins\" (default) drops them entirely when an admin-deployed managed tier is present; \"merge\" applies them UNDER the admin tier, filtered to only TIGHTEN policy, never loosen it",
		},
		{
			Scope: "safe-mode",
			Note:  "safe mode (--safe-mode / CLAUDE_CODE_SAFE_MODE, no settings key) is NOT a governance bypass — managed settings policy still applies, including policy-configured hooks — but managed plugins, managed skills, managed CLAUDE.md and policy-configured MCP servers do NOT load under it, so controls riding those surfaces drop for that session",
		},
	}
	for _, ep := range EnforceBeforeExec() {
		lines = append(lines, DryRunLine{
			Scope: "enforce-before-exec",
			Note:  ep.Gate + " is enforced " + ep.When + ", BEFORE " + ep.BeforeOp,
		})
	}
	return lines
}

// normalizeScope maps a free-form scope token to a known SettingsScope (tolerant of
// common spellings), or "" if unrecognized. Useful when a caller passes scope strings
// from an external source.
func normalizeScope(s string) SettingsScope {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "managed", "managed-settings", "managed_settings":
		return ScopeManaged
	case "cli", "cli-args", "cli_args", "command-line", "args":
		return ScopeCLIArgs
	case "local":
		return ScopeLocal
	case "project":
		return ScopeProject
	case "user":
		return ScopeUser
	default:
		return ""
	}
}
