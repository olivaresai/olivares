// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// managed-mcp.json + plugin-marketplace governance — verified set (ANT2-12).
// Verbatim against code.claude.com/docs/en/managed-mcp · /plugin-marketplaces
// on 2026-06-06.
import { type SchemaIssue, validateWith, z } from './types'

const MANAGED_MCP = 'https://code.claude.com/docs/en/managed-mcp'
const PLUGIN_MARKETPLACES =
  'https://code.claude.com/docs/en/plugin-marketplaces'

/** The 3 system paths for managed-mcp.json (verified). */
export const MANAGED_MCP_PATHS = {
  macos: '/Library/Application Support/ClaudeCode/managed-mcp.json',
  linux: '/etc/claude-code/managed-mcp.json',
  windows: 'C:\\Program Files\\ClaudeCode\\managed-mcp.json',
  source: MANAGED_MCP,
} as const

export const MANAGED_MCP_EXCLUSIVE =
  'If managed-mcp.json is deployed, Claude Code loads ONLY the servers it defines. Users cannot add, modify, or use any other MCP servers — including plugin-provided servers.'

/** The evaluation order, verbatim semantics: merge → denylist wins → typed allowlist. */
export const MCP_EVALUATION_ORDER = [
  { step: 1, rule: 'Merge the allow/deny lists from all sources.' },
  {
    step: 2,
    rule: 'Check the denylist: a server matching ANY denylist entry — by URL, command, or name — is blocked. Nothing overrides a denylist match.',
  },
  {
    step: 3,
    rule: 'Check the allowlist by server TYPE with EXACT URL/command matching.',
  },
] as const

/** The critical honesty caveat: serverName matching is NOT a security control. */
export const SERVER_NAME_NOT_SECURITY =
  'An allowlist that uses only serverName entries is NOT a security control: the name is a user-assigned label, not the underlying server — a user can label any server. Enforce by exact URL/command match.'

/** The two distinct managed-settings keys that gate MCP (commonly confused). */
export const MCP_GATING_KEYS = [
  {
    key: 'allowManagedMcpServersOnly',
    summary:
      'Locks the allowlist to managed sources: user/project/local allowlists are ignored. The denylist still merges from all sources.',
    source: MANAGED_MCP,
  },
  {
    key: 'allowAllClaudeAiMcps',
    since: 'v2.1.149',
    summary:
      'Loads claude.ai connectors alongside the servers in managed-mcp.json.',
    source: MANAGED_MCP,
  },
] as const

/** Plugin-marketplace governance (verified). The enforcer is strictKnownMarketplaces. */
export const PLUGIN_MARKETPLACE_GOVERNANCE = {
  enforcer: 'strictKnownMarketplaces',
  behavior: [
    'Undefined (default): no restriction — users may add any marketplace.',
    'Empty array []: complete lockdown — users cannot add any new marketplace.',
    'A list: allowlist of permitted marketplaces.',
  ],
  matching:
    'hostPattern (regex on the marketplace host) and pathPattern (regex on the filesystem path).',
  pluginOnly:
    'strictPluginOnlyCustomization restricts customization to plugins only.',
  airGap: {
    seedDir:
      'CLAUDE_CODE_PLUGIN_SEED_DIR — pre-populated plugin cache for containers/CI.',
    cacheDir:
      'CLAUDE_CODE_PLUGIN_CACHE_DIR — controls the plugin install directory at build time.',
  },
  privateNpm:
    'A marketplace plugin source may set a `registry` field for a private/internal npm registry.',
  checkTiming:
    'Restrictions are checked before any network/filesystem op, on add and on plugin install/update/refresh/auto-update.',
  source: PLUGIN_MARKETPLACES,
} as const

const mcpServerSchema = z
  .object({
    type: z.enum(['stdio', 'sse', 'http', 'ws']).optional(),
    command: z.string().optional(),
    args: z.array(z.string()).optional(),
    url: z.string().optional(),
  })
  .passthrough()

/** managed-mcp.json shape: mcpServers map + allow/deny lists. Lenient/passthrough. */
export const managedMcpSchema = z
  .object({
    mcpServers: z.record(z.string(), mcpServerSchema).optional(),
    allowlist: z.array(z.unknown()).optional(),
    denylist: z.array(z.unknown()).optional(),
  })
  .passthrough()

export function validateManagedMcp(value: unknown): SchemaIssue[] {
  const issues = validateWith(managedMcpSchema, value)
  // Honest nudge: a serverName-only allowlist is not a security control.
  if (value && typeof value === 'object') {
    const v = value as Record<string, unknown>
    const allow = Array.isArray(v.allowlist) ? v.allowlist : []
    const nameOnly = allow.some(
      (e) =>
        e &&
        typeof e === 'object' &&
        'serverName' in (e as object) &&
        !('url' in (e as object)) &&
        !('command' in (e as object)),
    )
    if (nameOnly) {
      issues.push({
        path: 'allowlist',
        message: SERVER_NAME_NOT_SECURITY,
        severity: 'warning',
      })
    }
  }
  return issues
}
