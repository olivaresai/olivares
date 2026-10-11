// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { TileState } from './components'
export function tileState(
  ...queries: { isLoading: boolean; isError: boolean }[]
): TileState {
  if (queries.some((q) => q.isError)) return 'unavailable'
  if (queries.some((q) => q.isLoading)) return 'loading'
  return 'ready'
}

export type SourceQuery = {
  isPending: boolean
  isLoading: boolean
  isError: boolean
  fetchStatus: 'fetching' | 'paused' | 'idle'
}

export type PendingReason = 'pendingIdle' | 'pendingPaused' | null

export function pendingReason(query: SourceQuery): PendingReason {
  if (!query.isPending || query.isError) return null
  if (query.fetchStatus === 'paused') return 'pendingPaused'
  if (query.fetchStatus === 'idle') return 'pendingIdle'
  return null
}
