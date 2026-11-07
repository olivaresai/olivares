// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
/** A region's three honest states: its answer, loading, or a read that failed. */
export type TileState = 'ready' | 'loading' | 'unavailable'

export function tileState(
  ...queries: { isLoading: boolean; isError: boolean }[]
): TileState {
  if (queries.some((q) => q.isError)) return 'unavailable'
  if (queries.some((q) => q.isLoading)) return 'loading'
  return 'ready'
}
