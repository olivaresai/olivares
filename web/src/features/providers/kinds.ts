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
  'openai_compatible',
  'ollama',
]

/** The keys OpenCode can be held to at the provider's own address (openCodeKeyProviders in
 * modules/sessions/driver_opencode.go). */
const OPENCODE_VENDOR_KEYS: readonly string[] = ['anthropic', 'openai', 'xai']

/**
 * Whether a session of this driver can run on a record of this kind AND be held to the
 * record's endpoint: a session bound to a provider record reaches only that endpoint, or
 * it does not start. It mirrors recordServesDriver in modules/sessions/provider_record.go
 * (FH d4af6c7a, 26.10.1), the one rule the engine reads at bind, resolve and launch; the
 * console only offers what the engine accepts, and kinds.test.ts pins the table. Claude
 * Code: an Anthropic key. Codex: an OpenAI key, an OpenAI-compatible endpoint or a local
 * model. Grok Build: an xAI key. OpenCode: an Anthropic, OpenAI or xAI key at the
 * provider's own address (no base URL), or a local model. Any other driver: nothing.
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
    case 'grok':
      return kind === 'xai'
    case 'opencode':
      return (
        kind === 'ollama' ||
        (OPENCODE_VENDOR_KEYS.includes(kind) && !baseURL?.trim())
      )
    default:
      return false
  }
}
