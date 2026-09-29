// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ProviderAccentName } from './types'
export const providerAccentNames = [
  'orange',
  'green',
  'amber',
  'red',
  'blue',
] as const
export const colors: Record<ProviderAccentName, string> = {
  orange: 'var(--accent-text)',
  green: 'var(--ok)',
  amber: 'var(--warn)',
  red: 'var(--bad)',
  blue: 'var(--info)',
}
export function isProviderAccent(value: string): value is ProviderAccentName {
  return providerAccentNames.some((name) => name === value)
}
