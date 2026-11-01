// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { engineTool } from '@/features/agentops/engine-tools'
import type { ProviderKind } from './types'

/**
 * The supported kinds, in the order a picker offers them.
 *
 * This closed list matches the engine's provider environment contracts.
 */
export const PROVIDER_KINDS: ProviderKind[] = [
  'anthropic',
  'openai',
  'xai',
  'gemini',
  'openai_compatible',
  'ollama',
]

/**
 * The kinds whose endpoint the engine also takes as plain http at a loopback or
 * private-network address, because a local model server is plain http: the kinds
 * validProviderBaseURL in modules/sessions/provider_record.go sends to
 * validLocalHTTPBaseURL. openai is https only. ollama has its own validator, and the
 * form fills its local address in. kinds.test.ts reads the Go rule and fails when the
 * two lists differ.
 */
export const LOCAL_HTTP_KINDS: readonly string[] = [
  'anthropic',
  'xai',
  'openai_compatible',
]

/**
 * Plain http at localhost, a loopback address or a private-network address, without
 * credentials, query or fragment: driverfacts.LocalHTTPEndpoint, the rule that lets
 * OpenCode be held to a local OpenAI-compatible server (vLLM, llama.cpp). It reads the
 * address as typed, like the engine (`localhost` is exact, an IPv4 octet has no leading
 * zero), and is deliberately a little stricter: an IPv4-mapped IPv6 form such as
 * [::ffff:127.0.0.1] is accepted by the engine and not offered here.
 */
export function isLocalHttpEndpoint(baseURL: string | undefined): boolean {
  const m =
    /^http:\/\/(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9.-]+)(?::\d{1,5})?(?:\/[^?#\s]*)?$/i.exec(
      (baseURL ?? '').trim(),
    )
  if (!m) return false
  const host = m[1]
  if (host === 'localhost') return true
  const octet = '(25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)'
  const v4 = new RegExp(`^${octet}\\.${octet}\\.${octet}\\.${octet}$`).exec(
    host,
  )
  if (v4) {
    const [a, b] = [Number(v4[1]), Number(v4[2])]
    return (
      a === 127 ||
      a === 10 ||
      (a === 172 && b >= 16 && b <= 31) ||
      (a === 192 && b === 168)
    )
  }
  // IPv6 literals: ::1 and the unique-local range fc00::/7.
  return /^\[(::1|f[cd][0-9a-f]{2}:[0-9a-f:]*)\]$/i.test(host)
}

/**
 * Whether a session of this driver can run on a record of this kind AND be held to the
 * record's endpoint: a session bound to a provider record reaches only that endpoint, or
 * it does not start. The kinds a tool runs on are the engine's (driverfacts Bindings, read
 * from GET /v1/m/sessions/tools); this applies each one's endpoint rule as CanBind does:
 * any endpoint, the provider's own address only (no base URL), or a local http address
 * only. A driver or rule the engine does not list serves nothing.
 */
export function recordServesDriver(
  kind: string,
  baseURL: string | undefined,
  driver: string,
): boolean {
  const binding = engineTool(driver.trim().toLowerCase())?.bindings.find(
    (b) => b.kind === kind,
  )
  switch (binding?.egress) {
    case 'configured-endpoint':
      return true
    case 'vendor-default-only':
      return !baseURL?.trim()
    case 'local-endpoint-only':
      return isLocalHttpEndpoint(baseURL)
    default:
      return false
  }
}
