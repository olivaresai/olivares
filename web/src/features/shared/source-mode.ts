// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

export type SourceMode = 'export' | 'live' | 'direct' | (string & {})
export type NormalizedSourceMode = 'export' | 'live' | 'direct'

export function normalizeSourceMode(
  value?: SourceMode | string | null,
): NormalizedSourceMode {
  const v = String(value ?? '')
    .trim()
    .toLowerCase()
  if (v === 'live') return 'live'
  if (v === 'direct') return 'direct'
  return 'export'
}
