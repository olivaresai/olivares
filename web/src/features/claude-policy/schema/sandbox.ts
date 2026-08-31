// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Sandbox lockdown + egress allowlist — verified set (ANT2-13, differentiator).
// Verbatim against code.claude.com/docs/en/sandboxing · /network-config on
// 2026-06-06.
import { type KeyDescriptor, type SchemaIssue, validateWith, z } from './types'

const SANDBOXING = 'https://code.claude.com/docs/en/sandboxing'
const NETWORK = 'https://code.claude.com/docs/en/network-config'

/** OS sandbox primitives (verified). */
export const SANDBOX_PRIMITIVES = {
  macos: 'Seatbelt (built-in; nothing to install).',
  linux: 'bubblewrap (filesystem isolation) + seccomp.',
  source: SANDBOXING,
} as const

/** sandbox.* keys (verified). */
export const SANDBOX_KEYS: readonly KeyDescriptor[] = [
  {
    key: 'sandbox.failIfUnavailable',
    type: 'boolean',
    scope: 'any',
    summary:
      'Hard-fail (instead of degrade) when sandboxing is unavailable. For managed deployments that require the sandbox as a security gate.',
    source: SANDBOXING,
  },
  {
    key: 'sandbox.allowUnsandboxedCommands',
    type: 'boolean',
    scope: 'any',
    summary:
      'Set to false for Strict sandbox mode — disables the dangerouslyDisableSandbox escape hatch. (false is the locked-down value.)',
    source: SANDBOXING,
  },
  {
    key: 'sandbox.filesystem.allowRead',
    type: 'string[]',
    scope: 'any',
    summary:
      'Read-allowed paths. Arrays MERGE across scopes (paths combine, not replace).',
    source: SANDBOXING,
  },
  {
    key: 'sandbox.filesystem.allowWrite',
    type: 'string[]',
    scope: 'any',
    summary: 'Write-allowed paths (merged across scopes).',
    source: SANDBOXING,
  },
  {
    key: 'sandbox.filesystem.denyRead',
    type: 'string[]',
    scope: 'any',
    summary: 'Read-denied paths.',
    source: SANDBOXING,
  },
  {
    key: 'sandbox.filesystem.denyWrite',
    type: 'string[]',
    scope: 'any',
    summary: 'Write-denied paths.',
    source: SANDBOXING,
  },
  {
    key: 'sandbox.filesystem.allowManagedReadPathsOnly',
    type: 'boolean',
    scope: 'managed-only',
    summary: 'Only allowRead entries from managed settings are honored.',
    source: SANDBOXING,
  },
  {
    key: 'sandbox.network.allowedDomains',
    type: 'string[]',
    scope: 'any',
    summary: 'Domains Bash commands may reach (pre-allow to avoid the prompt).',
    source: SANDBOXING,
  },
  {
    key: 'sandbox.network.deniedDomains',
    type: 'string[]',
    scope: 'any',
    summary:
      'Domains blocked even when a broader allowedDomains wildcard would permit them.',
    source: SANDBOXING,
  },
  {
    key: 'sandbox.network.allowManagedDomainsOnly',
    type: 'boolean',
    scope: 'managed-only',
    summary:
      'Only allowedDomains from managed settings are honored; non-allowed domains are blocked (not prompted).',
    source: SANDBOXING,
  },
]

/** The default egress allowlist Claude Code requires (network-config, verified).
 *  This is what the engine MUST be able to reach — render it as a viewer. */
export const EGRESS_ALLOWLIST: readonly { host: string; purpose: string }[] = [
  { host: 'api.anthropic.com', purpose: 'Claude API requests.' },
  { host: 'claude.ai', purpose: 'claude.ai account authentication.' },
  {
    host: 'platform.claude.com',
    purpose: 'Anthropic Console account authentication.',
  },
  { host: 'downloads.claude.ai', purpose: 'Plugin / executable downloads.' },
  { host: 'storage.googleapis.com', purpose: 'Asset/storage downloads.' },
  { host: 'bridge.claudeusercontent.com', purpose: 'Bridge service.' },
  {
    host: 'raw.githubusercontent.com',
    purpose: 'Raw GitHub content (plugins/marketplaces).',
  },
]
export const EGRESS_SOURCE = NETWORK

/** Proxy / CA / mTLS env vars (verified). SOCKS is NOT supported. */
export const EGRESS_ENV_VARS = [
  { name: 'HTTPS_PROXY', summary: 'HTTPS egress proxy (recommended).' },
  { name: 'HTTP_PROXY', summary: 'HTTP egress proxy (if HTTPS unavailable).' },
  {
    name: 'NODE_EXTRA_CA_CERTS',
    summary: 'Path to a custom CA bundle to trust.',
  },
  { name: 'CLAUDE_CODE_CLIENT_CERT', summary: 'Client certificate for mTLS.' },
  { name: 'CLAUDE_CODE_CLIENT_KEY', summary: 'Client private key for mTLS.' },
] as const
export const SOCKS_UNSUPPORTED = 'SOCKS proxies are NOT supported.'

/**
 * THE honesty caveat: the proxy matches the allowlist on the
 * CLIENT-SUPPLIED hostname (SNI) and does NOT terminate or inspect TLS. So the
 * allowlist is a capability grant, not hermetic containment — broad domains (e.g.
 * github.com) open exfiltration / domain-fronting paths. The UI must show this and
 * NEVER sell the allowlist as content-inspecting or front-proof.
 */
export const DOMAIN_FRONTING_CAVEAT =
  'The built-in proxy enforces the allowlist by the requested hostname and does NOT terminate or inspect TLS. Allowing broad domains (e.g. github.com) can create data-exfiltration / domain-fronting paths — the allowlist is a capability grant, not hermetic containment.'

export const SANDBOX_KEY_SET: ReadonlySet<string> = new Set([
  'failIfUnavailable',
  'allowUnsandboxedCommands',
  'filesystem',
  'network',
  'autoAllowBashToolReadOnly',
  'enabled',
  'excludedTools',
])

export const sandboxSchema = z
  .object({
    failIfUnavailable: z.boolean().optional(),
    allowUnsandboxedCommands: z.boolean().optional(),
    filesystem: z
      .object({
        allowRead: z.array(z.string()).optional(),
        allowWrite: z.array(z.string()).optional(),
        denyRead: z.array(z.string()).optional(),
        denyWrite: z.array(z.string()).optional(),
        allowManagedReadPathsOnly: z.boolean().optional(),
      })
      .passthrough()
      .optional(),
    network: z
      .object({
        allowedDomains: z.array(z.string()).optional(),
        deniedDomains: z.array(z.string()).optional(),
        allowManagedDomainsOnly: z.boolean().optional(),
      })
      .passthrough()
      .optional(),
  })
  .passthrough()

/** Validate a `sandbox` object (the value of the top-level `sandbox` key). */
export function validateSandbox(value: unknown): SchemaIssue[] {
  const issues = validateWith(sandboxSchema, value)
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    for (const key of Object.keys(value as Record<string, unknown>)) {
      if (!SANDBOX_KEY_SET.has(key)) {
        issues.push({
          path: `sandbox.${key}`,
          message: `Unknown sandbox key "${key}" — verify against the docs.`,
          severity: 'warning',
        })
      }
    }
  }
  return issues
}
