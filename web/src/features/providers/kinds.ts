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

/** Which driver each kind serves, for the copy that explains a binding refusal
 * before the operator earns it. It mirrors `recordServesDriver` in
 * modules/sessions/provider_record.go; the ENGINE decides, this only explains. */
export const KIND_DRIVERS: Record<ProviderKind, string[]> = {
  ollama: ['codex'],
  anthropic: ['claude', 'opencode'],
  openai: ['codex', 'opencode'],
  xai: ['grok', 'opencode'],
  openai_compatible: ['claude', 'codex', 'grok', 'opencode'],
}
