// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ProviderSnapshot } from '@/features/agent-tools/api'
import { ENGINE_LABELS } from '@/features/agentops/account-usage'
import { toolName } from '@/features/agentops/engine-tools'
import type { ProviderAccountDTO } from '@/features/agentops/types'

/** One row of the page: an account, or the server user's own login of a tool. */
export type UsageRow = Pick<
  ProviderAccountDTO,
  'account_ref' | 'name' | 'display_name' | 'driver' | 'identity' | 'usage'
>

/** The fullest window's percent: how near the row is to its limit (a credit has none). */
const peak = (a: UsageRow) =>
  (a.usage ?? []).reduce((m, u) => Math.max(m, u.utilization ?? -1), -1)

/**
 * The rows whose tool reported a usage window, the one nearest its limit first. A row that
 * reported none is left out: an unknown window is never 0%. Ties go by the reference, so
 * the order does not change between two reads of the same numbers.
 */
export function accountsNearestLimit(rows: readonly UsageRow[]): UsageRow[] {
  return rows
    .filter((a) => a.usage?.length)
    .sort(
      (a, b) =>
        peak(b) - peak(a) || a.account_ref.localeCompare(b.account_ref, 'en'),
    )
}

/**
 * The server user's own login of a tool is no account, so the account list never has it.
 * A system administrator reads it from the providers snapshot; it becomes one more row, in
 * the account's own shape. A login that is not ready or reported no window is left out.
 */
export function defaultLoginRows(
  snaps: readonly ProviderSnapshot[],
): UsageRow[] {
  return snaps
    .filter((s) => s.default && s.state === 'ready' && s.limits.length > 0)
    .map((s) => ({
      account_ref: s.instance,
      name: [toolName(s.driver), s.plan || s.auth_method]
        .filter(Boolean)
        .join(' · '),
      driver: s.driver,
      identity: s.email ?? '',
      usage: s.limits.map((l) => ({
        window: ENGINE_LABELS[l.label] ?? l.label,
        label: l.label,
        utilization: l.percent,
        resets_at: l.resets_at,
        observed_at: s.checked_at,
      })),
    }))
}
