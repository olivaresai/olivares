// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
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

/** The keys OpenCode can be held to at the provider's own address (openCodeKeyProviders in
 * modules/sessions/driver_opencode.go). */
const OPENCODE_VENDOR_KEYS: readonly string[] = ['anthropic', 'openai', 'xai']

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
 * it does not start. It mirrors recordServesDriver in modules/sessions/provider_record.go
 * (FH d4af6c7a, 26.10.1), the one rule the engine reads at bind, resolve and launch; the
 * console only offers what the engine accepts, and kinds.test.ts pins the table. Claude
 * Code: an Anthropic key. Codex: an OpenAI key, an OpenAI-compatible endpoint or a local
 * model. Grok Build: an xAI key. OpenCode: an Anthropic, OpenAI or xAI key at the
 * provider's own address (no base URL), a local model, or an OpenAI-compatible server at
 * a local http address. Any other driver: nothing.
 */
export function recordServesDriver(
  kind: string,
  baseURL: string | undefined,
  driver: string,
): boolean {
  switch (driver.trim().toLowerCase()) {
    case 'claude':
      return kind === 'anthropic'
    case 'codex':
      return (
        kind === 'openai' || kind === 'openai_compatible' || kind === 'ollama'
      )
    case 'gemini-cli':
      return kind === 'gemini' && !baseURL?.trim()
    case 'grok':
      return kind === 'xai'
    case 'opencode':
      return (
        kind === 'ollama' ||
        (kind === 'openai_compatible' && isLocalHttpEndpoint(baseURL)) ||
        (OPENCODE_VENDOR_KEYS.includes(kind) && !baseURL?.trim())
      )
    default:
      return false
  }
}
