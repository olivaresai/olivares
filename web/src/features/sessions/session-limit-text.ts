// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// What the session header says about a limit (session-limit.tsx).
import type { TFunction } from 'i18next'
import type { RunLimit } from '@/features/agentops/types'

/** "Out of quota until 15:45", with the day when the reset is not today. */
export function limitSentence(
  limit: RunLimit,
  t: TFunction,
  now: Date = new Date(),
): string {
  if (limit.kind === 'rate') return t('sessions:limit.rate')
  const at = limit.resets_at ? new Date(limit.resets_at) : null
  if (!at || Number.isNaN(at.getTime())) return t('sessions:limit.usage')
  const sameDay = at.toDateString() === now.toDateString()
  const time = at.toLocaleString(
    undefined,
    sameDay
      ? { hour: '2-digit', minute: '2-digit' }
      : { dateStyle: 'short', timeStyle: 'short' },
  )
  return t('sessions:limit.usageUntil', { time })
}

/** A usage limit whose reset time has passed: the account works again, so nothing is said. */
export function limitLifted(limit: RunLimit, now: Date = new Date()): boolean {
  if (limit.kind !== 'usage' || !limit.resets_at) return false
  const at = new Date(limit.resets_at)
  return !Number.isNaN(at.getTime()) && at <= now
}
