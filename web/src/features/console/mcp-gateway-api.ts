// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http, type RequestOptions } from '@/lib/api/client'

export interface MCPToolPolicy {
  name: string
  required_scope: string
  destructive: boolean
}
export interface MCPServerInput {
  name: string
  /** streamable_http: a remote server at `url`; stdio: `command` started on this server. */
  transport: 'streamable_http' | 'stdio'
  url: string
  command?: string
  args?: string[]
  env?: Record<string, string>
  /** NAME -> store:mcp/<secret>: secret values reach the command as environment. */
  env_secret_refs?: Record<string, string>
  credential_ref?: string
  egress_cidrs: string[]
  /** A command server's network: only these HTTPS hosts (host or host:port). Omitted,
   * the command keeps the network of the launch it runs next to. */
  egress_hosts?: string[]
  /** External OAuth trust: none at all (`{}`) or complete; never generated. */
  trust: {
    resource?: string
    issuer?: string
    jwks_url?: string
    jwks?: unknown
  }
  /** Omitted on an enable: every tested tool asks first (Root 2026-10-02). An explicit
   * list, empty included, is kept as sent: destructive false runs without approval,
   * destructive true asks first, a tool left out is not allowed. */
  allowed_tools?: MCPToolPolicy[]
  enabled: boolean
}
export interface MCPServer extends MCPServerInput {
  id: string
  /** The tested tools the server's own hints call read-only (MC, Root 2026-10-02): a
   * proposal to pre-select, never a grant. Run without approval is the administrator's
   * explicit choice. */
  proposed_allow?: string[]
  probe: {
    state: string
    tested_at?: string
    tools: { name: string; fingerprint: string; read_only?: boolean }[]
    /** Why an unreachable test failed, when the engine can tell. */
    reason?: MCPProbeReason
    /** A local server's failure in one redacted line (stderr or the OS error). */
    detail?: string
    http_status?: number
  }
}
export type MCPProbeReason =
  | 'dns'
  | 'tls'
  | 'timeout'
  | 'connection_refused'
  | 'http_status'
  | 'process_start'
  | 'process_exit'
export interface MCPGatewaySnapshot {
  version: number
  source: 'file' | 'store'
  read_only: boolean
  session_tools: boolean
  session_endpoint: string
  servers: MCPServer[]
  governance: Record<string, string>
}
const base = '/v1/console/mcp-gateway'
type Options = Omit<RequestOptions, 'method' | 'body'>
export const mcpGatewayApi = {
  get: (opts?: Options) => http.get<MCPGatewaySnapshot>(base, opts),
  put: (
    version: number,
    server: MCPServerInput,
    id?: string,
    opts?: Options,
  ) =>
    id
      ? http.put<MCPGatewaySnapshot>(
          `${base}/servers/${encodeURIComponent(id)}`,
          { version, server },
          opts,
        )
      : http.post<MCPGatewaySnapshot>(
          `${base}/servers`,
          { version, server },
          opts,
        ),
  test: (version: number, id: string, opts?: Options) =>
    http.post<MCPGatewaySnapshot>(
      `${base}/servers/${encodeURIComponent(id)}/test`,
      { version },
      opts,
    ),
  remove: (version: number, id: string, opts?: Options) =>
    http.delete<MCPGatewaySnapshot>(
      `${base}/servers/${encodeURIComponent(id)}`,
      { version },
      opts,
    ),
  session: (version: number, enabled: boolean, opts?: Options) =>
    http.put<MCPGatewaySnapshot>(
      `${base}/session-tools`,
      { version, enabled },
      opts,
    ),
}
export const mcpGatewayKeys = {
  scope: (tenant: string | null, epoch: number) =>
    ['console', 'mcp-gateway', tenant, epoch] as const,
}

export function mcpHTTPSURL(value: string): boolean {
  try {
    const url = new URL(value)
    return (
      url.protocol === 'https:' &&
      !!url.hostname &&
      !url.username &&
      !url.password &&
      !url.search &&
      !url.hash &&
      value.trim() === value &&
      value.length <= 2048
    )
  } catch {
    return false
  }
}
/** The engine's egress host form (core/auth mcpEgressHost): a lowercase public host,
 * with a port only when it is not 443. The engine also refuses reserved IP literals. */
export function mcpEgressHost(value: string): boolean {
  try {
    const url = new URL(`https://${value}`)
    return (
      url.host === value &&
      url.pathname === '/' &&
      !url.username &&
      !url.password &&
      !value.includes('*') &&
      !value.includes('%') &&
      !url.hostname.endsWith('.') &&
      url.hostname !== 'localhost' &&
      !url.hostname.endsWith('.localhost')
    )
  } catch {
    return false
  }
}
export function mcpPublicJWKS(value: string): unknown | null {
  try {
    const obj = JSON.parse(value) as { keys?: Record<string, unknown>[] }
    const allowed = new Set([
      'kty',
      'crv',
      'x',
      'y',
      'n',
      'e',
      'kid',
      'use',
      'alg',
      'key_ops',
      'x5c',
      'x5t',
      'x5t#S256',
    ])
    if (
      Object.keys(obj).length !== 1 ||
      !Array.isArray(obj.keys) ||
      obj.keys.length === 0 ||
      obj.keys.length > 16 ||
      value.length > 32768
    )
      return null
    if (
      obj.keys.some(
        (k) =>
          !k ||
          typeof k !== 'object' ||
          !['OKP', 'EC', 'RSA'].includes(String(k.kty)) ||
          Object.keys(k).some((name) => !allowed.has(name)),
      )
    )
      return null
    return obj
  } catch {
    return null
  }
}
