// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { formatDateTime, formatRelativeTime } from '@/lib/format'
import { parseTs } from './format'

/** A ticking clock so relative-time labels refresh themselves (every `intervalMs`). */
function useNow(intervalMs = 30_000): number {
  const [, setTick] = useState(0)
  useEffect(() => {
    const id = setInterval(() => setTick((n) => n + 1), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return 0
}

/**
 * RelTime renders a localized, auto-refreshing relative timestamp ("2m ago" /
 * "hace 2 min") via the shared `formatRelativeTime` (Intl-localized to the active
 * language), with the absolute time as a hover title. Used for first/last-seen
 * across the inventory, sessions and health views so staleness reads consistently.
 */
export function RelTime({
  ts,
  className,
}: {
  ts: string | null | undefined
  className?: string
}) {
  const { i18n } = useTranslation()
  useNow() // re-render on a cadence so the relative label stays current
  if (!parseTs(ts)) return <span className={className}>—</span>
  return (
    <time
      className={className}
      title={formatDateTime(ts, i18n.language)}
      dateTime={ts ?? undefined}
    >
      {formatRelativeTime(ts, i18n.language)}
    </time>
  )
}
