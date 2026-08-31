// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// managed-settings.json — verified set (ANT2-11). Verbatim against the live docs
// on 2026-06-06: code.claude.com/docs/en/settings · /server-managed-settings ·
// /permissions. The spec listed ~16 managed-only keys but several
// (policyHelper, forceLoginMethod, parentSettingsBehavior, skillOverrides,
// disableRemoteControl) are NOT in the live managed-only table — we do NOT encode
// them (no inventes claves). The live managed-only table has 14 keys.
import { type KeyDescriptor, type SchemaIssue, validateWith, z } from './types'

const SETTINGS = 'https://code.claude.com/docs/en/settings'
const SERVER_MANAGED = 'https://code.claude.com/docs/en/server-managed-settings'
const PERMISSIONS = 'https://code.claude.com/docs/en/permissions'

/** OS paths where Claude Code reads managed-settings.json (verified). The legacy
 *  Windows C:\\ProgramData\\ClaudeCode path is DEAD since v2.1.75 (do
 *  not re-add it). */
export const MANAGED_SETTINGS_PATHS = {
  macos: '/Library/Application Support/ClaudeCode/managed-settings.json',
  linux: '/etc/claude-code/managed-settings.json',
  windows: 'C:\\Program Files\\ClaudeCode\\managed-settings.json',
  windowsLegacyRemoved: 'C:\\ProgramData\\ClaudeCode\\managed-settings.json',
  windowsLegacyRemovedSince: 'v2.1.75',
  source: SETTINGS,
} as const

/** The drop-in directory: managed-settings.d/ merges (systemd convention). */
export const DROP_IN_MERGE = {
  dir: 'managed-settings.d/',
  rules: [
    'managed-settings.json is merged first as the base.',
    'Then all *.json files in the drop-in directory, sorted alphabetically.',
    'Later files override scalars; arrays are concatenated and de-duplicated; objects deep-merge.',
  ],
  source: SETTINGS,
} as const

/** Settings precedence, highest first. Permission rules MERGE across scopes (the
 *  one documented exception to scalar override). */
export const SETTINGS_PRECEDENCE = [
  { scope: 'Managed', note: 'Highest — cannot be overridden by anything.' },
  { scope: 'Command line', note: 'Temporary session overrides.' },
  { scope: 'Local', note: 'Overrides project and user settings.' },
  { scope: 'Project', note: 'Overrides user settings.' },
  {
    scope: 'User',
    note: 'Lowest — applies when nothing else specifies the setting.',
  },
] as const
export const PRECEDENCE_PERMISSION_RULES_MERGE =
  'Exception: permission rules MERGE across scopes rather than override.'

/** The server-managed tier (Claude.ai admin console). Distinct semantics from a
 *  managed file on the host: fail-closed when forceRemoteSettingsRefresh is set,
 *  NO-MERGE with endpoint-managed settings (first non-empty source wins), hourly
 *  poll. Default is fail-OPEN unless forceRemoteSettingsRefresh is true. */
export const SERVER_MANAGED_TIER = {
  failClosedKey: 'forceRemoteSettingsRefresh',
  failClosed:
    'When forceRemoteSettingsRefresh is true, the CLI blocks at startup until remote settings are freshly fetched, and EXITS if the fetch fails (fail-closed). Default is fail-open.',
  noMerge:
    'Server-managed and endpoint-managed settings do NOT merge: if the server delivers any keys, endpoint-managed settings are ignored entirely. Server is checked first.',
  poll: 'Fetched at startup, then polled hourly during active sessions.',
  source: SERVER_MANAGED,
} as const

/** The 14 managed-only keys (take effect ONLY in managed settings), verified. */
export const MANAGED_ONLY_KEYS: readonly KeyDescriptor[] = [
  {
    key: 'allowAllClaudeAiMcps',
    type: 'boolean',
    scope: 'managed-only',
    since: 'v2.1.149',
    summary:
      'Load claude.ai connectors alongside the servers in managed-mcp.json.',
    source: PERMISSIONS,
  },
  {
    key: 'allowedChannelPlugins',
    type: 'string[]',
    scope: 'managed-only',
    summary: 'Restrict which channel plugins may be used.',
    source: PERMISSIONS,
  },
  {
    key: 'allowManagedHooksOnly',
    type: 'boolean',
    scope: 'managed-only',
    summary:
      'Only hooks defined in managed settings run; user/project hooks are ignored.',
    source: PERMISSIONS,
  },
  {
    key: 'allowManagedMcpServersOnly',
    type: 'boolean',
    scope: 'managed-only',
    summary:
      'Lock the MCP allowlist to managed sources — user/project/local allowlists are ignored (the denylist still merges from all sources).',
    source: PERMISSIONS,
  },
  {
    key: 'allowManagedPermissionRulesOnly',
    type: 'boolean',
    scope: 'managed-only',
    summary: 'Only permission rules from managed settings apply.',
    source: PERMISSIONS,
  },
  {
    key: 'blockedMarketplaces',
    type: 'string[]',
    scope: 'managed-only',
    toConfirm: true,
    summary:
      'Denylist of plugin marketplaces. Appears in the managed-only table; the enforced allowlist mechanism is strictKnownMarketplaces (verify intended semantics).',
    source: PERMISSIONS,
  },
  {
    key: 'channelsEnabled',
    type: 'boolean',
    scope: 'managed-only',
    summary: 'Enable/disable channels org-wide.',
    source: PERMISSIONS,
  },
  {
    key: 'forceRemoteSettingsRefresh',
    type: 'boolean',
    scope: 'managed-only',
    summary:
      'Fail-closed server-managed settings: block startup until a fresh remote fetch, exit on failure.',
    source: SERVER_MANAGED,
  },
  {
    key: 'pluginTrustMessage',
    type: 'string',
    scope: 'managed-only',
    summary: 'Custom message shown when a user is about to trust a plugin.',
    source: PERMISSIONS,
  },
  {
    key: 'sandbox.filesystem.allowManagedReadPathsOnly',
    type: 'boolean',
    scope: 'managed-only',
    summary: 'Only allowRead entries from managed settings are honored.',
    source: SETTINGS,
  },
  {
    key: 'sandbox.network.allowManagedDomainsOnly',
    type: 'boolean',
    scope: 'managed-only',
    summary:
      'Only allowedDomains from managed settings are honored; non-allowed domains are blocked (not prompted).',
    source: SETTINGS,
  },
  {
    key: 'strictKnownMarketplaces',
    type: 'string[]',
    scope: 'managed-only',
    summary:
      'Plugin-marketplace allowlist. Undefined = no restriction; [] = complete lockdown; a list = allowlist.',
    source: PERMISSIONS,
  },
  {
    key: 'strictPluginOnlyCustomization',
    type: 'string[]',
    scope: 'managed-only',
    summary:
      'Restrict customization to plugins only (e.g. "mcp" → servers can only come from plugins).',
    source: PERMISSIONS,
  },
  {
    key: 'wslInheritsWindowsSettings',
    type: 'boolean',
    scope: 'managed-only',
    summary: 'WSL inherits the Windows managed settings.',
    source: PERMISSIONS,
  },
]

/** Keys that exist but are explicitly NOT managed-only (common confusion). */
export const NOT_MANAGED_ONLY_NOTES: readonly KeyDescriptor[] = [
  {
    key: 'disableBypassPermissionsMode',
    type: 'enum',
    enum: ['disable'],
    scope: 'any',
    summary:
      'Works from ANY scope (a user can set it on themselves). Typically placed in managed settings but not managed-only.',
    source: PERMISSIONS,
  },
  {
    key: 'policyHelper',
    type: 'string',
    scope: 'any',
    summary:
      'OS-level policy source only; not honored in server-managed settings and not in the managed-only table.',
    source: SERVER_MANAGED,
  },
  {
    key: 'requiredMinimumVersion',
    type: 'string',
    scope: 'any',
    summary:
      'Minimum allowed Claude Code version (the correct key name; not "minimumVersion").',
    source: SETTINGS,
  },
]

const MANAGED_ONLY_KEY_SET = new Set(
  MANAGED_ONLY_KEYS.map((k) => k.key.split('.')[0]),
)

/** Well-known top-level managed-settings keys (superset) so we can WARN — never
 *  hard-error — on a likely typo. managed-settings is open-ended, so unknown keys
 *  are a warning, not a rejection. */
export const KNOWN_TOP_LEVEL_KEYS: ReadonlySet<string> = new Set([
  // governance / managed-only
  ...MANAGED_ONLY_KEY_SET,
  'disableBypassPermissionsMode',
  'policyHelper',
  'requiredMinimumVersion',
  'requiredMaximumVersion',
  'forceLoginOrgUUID',
  // common settings
  'permissions',
  'hooks',
  'env',
  'model',
  'sandbox',
  'mcpServers',
  'enableAllProjectMcpServers',
  'apiKeyHelper',
  'awsAuthRefresh',
  'awsCredentialExport',
  'includeCoAuthoredBy',
  'cleanupPeriodDays',
  'statusLine',
  'outputStyle',
  'spinnerTips',
  'autoCompactThreshold',
  'disableAllHooks',
  'disabledMcpjsonServers',
  'enabledMcpjsonServers',
  'telemetry',
  '$schema',
])

/** Permissions sub-object: lenient (rules are free-form match strings). */
const permissionsSchema = z
  .object({
    allow: z.array(z.string()).optional(),
    deny: z.array(z.string()).optional(),
    ask: z.array(z.string()).optional(),
    defaultMode: z
      .enum(['default', 'acceptEdits', 'plan', 'bypassPermissions'])
      .optional(),
    additionalDirectories: z.array(z.string()).optional(),
    disableBypassPermissionsMode: z.literal('disable').optional(),
  })
  .passthrough()

/** Top-level managed-settings shape — every field optional, passthrough unknowns. */
export const managedSettingsSchema = z
  .object({
    allowAllClaudeAiMcps: z.boolean().optional(),
    allowedChannelPlugins: z.array(z.string()).optional(),
    allowManagedHooksOnly: z.boolean().optional(),
    allowManagedMcpServersOnly: z.boolean().optional(),
    allowManagedPermissionRulesOnly: z.boolean().optional(),
    blockedMarketplaces: z.array(z.string()).optional(),
    channelsEnabled: z.boolean().optional(),
    forceRemoteSettingsRefresh: z.boolean().optional(),
    pluginTrustMessage: z.string().optional(),
    strictKnownMarketplaces: z.array(z.string()).optional(),
    strictPluginOnlyCustomization: z.array(z.string()).optional(),
    wslInheritsWindowsSettings: z.boolean().optional(),
    requiredMinimumVersion: z.string().optional(),
    permissions: permissionsSchema.optional(),
  })
  .passthrough()

/**
 * Validate a managed-settings object: type-check known keys (error), warn on
 * unknown top-level keys (likely typo), and inform when a managed-only key is
 * present (it only takes effect here).
 */
export function validateManagedSettings(value: unknown): SchemaIssue[] {
  const issues = validateWith(managedSettingsSchema, value)
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    for (const key of Object.keys(value as Record<string, unknown>)) {
      if (!KNOWN_TOP_LEVEL_KEYS.has(key)) {
        issues.push({
          path: key,
          message: `Unknown managed-settings key "${key}" — verify against the docs (could be a typo).`,
          severity: 'warning',
        })
      }
    }
  }
  return issues
}
