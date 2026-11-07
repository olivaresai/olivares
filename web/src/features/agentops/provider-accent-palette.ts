// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ProviderAccentName } from './types'
/** The accents an account may carry: the server's closed list, in its order
 * (modules/sessions/provider_account.go; the first five are published). provider-accent
 * .test.tsx holds this list to the OpenAPI snapshot. */
export const providerAccentNames = [
  'orange',
  'green',
  'amber',
  'red',
  'blue',
  'purple',
  'pink',
  'teal',
  'slate',
] as const satisfies readonly ProviderAccentName[]
/** The dot paints its own theme token (tokens/console, 3:1 on every surface of both
 * themes), never a status color: an accent says which account, not how it is. */
export const accentColor = (name: ProviderAccentName) =>
  `var(--account-${name})`
export function isProviderAccent(value: string): value is ProviderAccentName {
  return providerAccentNames.some((name) => name === value)
}
