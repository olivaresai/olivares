// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type {
  ProviderLimit,
  ProviderSnapshot,
} from '@/features/agent-tools/api'

/** One signed-in account and its official usage windows, as its tool reported them. */
export interface AccountUsage {
  instance: string
  driver: string
  /** The server user's own login of the tool, not one this organization made. */
  default: boolean
  plan?: string
  email?: string
  stale?: boolean
  checkedAt: string
  windows: ProviderLimit[]
  /** The fullest window's percent: how near the account is to its limit. */
  peak: number
  /** When the fullest window resets, when the tool says. */
  resetsAt?: string
}

/**
 * The accounts that reported a usage window, the one nearest its limit first. A login that
 * is not ready or reported no window is left out: an unknown window is never 0%. Ties go
 * by the instance, so the order does not change between two reads of the same numbers.
 */
export function accountsNearestLimit(
  snaps: readonly ProviderSnapshot[],
): AccountUsage[] {
  return snaps
    .filter((s) => s.state === 'ready' && s.limits.length > 0)
    .map((s) => {
      const fullest = s.limits.reduce((a, b) => (b.percent > a.percent ? b : a))
      return {
        instance: s.instance,
        driver: s.driver,
        default: s.default,
        plan: s.plan || s.auth_method,
        email: s.email,
        stale: s.stale,
        checkedAt: s.checked_at,
        windows: s.limits,
        peak: fullest.percent,
        resetsAt: fullest.resets_at,
      }
    })
    .sort(
      (a, b) => b.peak - a.peak || a.instance.localeCompare(b.instance, 'en'),
    )
}
