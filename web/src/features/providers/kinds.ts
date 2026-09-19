// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ProviderKind } from './types'

/**
 * The four kinds, in the order a picker offers them.
 *
 * It is a CLOSED list and it matches the engine's own closed set: a kind decides
 * which environment variables a child process receives, so a console that offered a
 * fifth would be offering an injection the engine refuses. The order is the order of
 * the drivers the product installs (claude, codex, grok), with the escape hatch last.
 */
export const PROVIDER_KINDS: ProviderKind[] = [
  'anthropic',
  'openai',
  'xai',
  'openai_compatible',
]

/** Which driver each kind serves, for the copy that explains a binding refusal
 * before the operator earns it. It mirrors `recordServesDriver` in
 * modules/sessions/provider_record.go; the ENGINE decides, this only explains. */
export const KIND_DRIVERS: Record<ProviderKind, string[]> = {
  anthropic: ['claude', 'opencode'],
  openai: ['codex', 'opencode'],
  xai: ['grok', 'opencode'],
  openai_compatible: ['claude', 'codex', 'grok', 'opencode'],
}
