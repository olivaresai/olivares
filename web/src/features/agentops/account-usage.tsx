// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The usage an account's tool reported, said the same way wherever the console names the
// account: the reported windows and credits, a window at 100 % marked with when it
// resets, and nothing (never 0 %) when the tool reported none.
import { Fragment } from 'react'
import { useTranslation } from 'react-i18next'
import { useProfileList } from '@/features/agent-tools/use-profiles'
import { formatDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import './i18n'
import type { ProviderAccountUsage } from './types'

/** The two windows with local names, shown first; other rows use the tool's label. */
const NAMED = [
  { window: 'five_hour', key: 'fiveHour' },
  { window: 'seven_day', key: 'week' },
] as const

/** A percent that says 100 only when the window is at its limit. */
const shownPercent = (utilization: number) =>
  utilization >= 100 ? 100 : Math.min(99, Math.max(0, Math.round(utilization)))

/** The windows still in force. One whose reset time has passed is the tool's reading from
 * before the reset, so it is unknown now, not a figure and not a limit. */
const inForce = (usage: readonly ProviderAccountUsage[] | undefined) =>
  usage?.filter((u) => !u.resets_at || !(Date.parse(u.resets_at) <= Date.now()))

/** The first window at 100 % or more (a per-model window counts), else undefined. */
export function limitReached(
  usage: readonly ProviderAccountUsage[] | undefined,
): ProviderAccountUsage | undefined {
  return inForce(usage)?.find(
    (u) => u.utilization !== undefined && u.utilization >= 100,
  )
}

/** "5-hour 42% · Weekly 17%"; a window the tool did not report is left out. With
 * `showUnknown` an account that reported none says so; otherwise it says nothing. */
export function AccountUsage({
  usage,
  showUnknown = false,
  className,
}: {
  usage: readonly ProviderAccountUsage[] | undefined
  showUnknown?: boolean
  className?: string
}) {
  const { t } = useTranslation('agentops')
  const current = inForce(usage)
  const ordered = [
    ...NAMED.flatMap(
      ({ window }) => current?.filter((u) => u.window === window) ?? [],
    ),
    ...(current?.filter((u) => !NAMED.some((n) => n.window === u.window)) ??
      []),
  ]
  const windows = ordered.flatMap((u) => {
    const key = NAMED.find((n) => n.window === u.window)?.key
    const label = key ? t(`usage.${key}`) : u.label || u.window
    if (u.credit) {
      const balance = u.credit.unlimited ? '∞' : u.credit.balance
      return balance === undefined
        ? []
        : [{ key: u.window, text: `${label} ${balance}`, warning: false }]
    }
    if (u.utilization === undefined || !Number.isFinite(u.utilization))
      return []
    return [
      {
        key: u.window,
        text: t('usage.percent', {
          window: label,
          percent: shownPercent(u.utilization),
        }),
        warning: u.utilization >= 80,
      },
    ]
  })
  if (windows.length === 0)
    return showUnknown ? (
      <span className={cn('text-caption text-text-2', className)}>
        {t('usage.unknown')}
      </span>
    ) : null
  return (
    <span
      data-testid="account-usage"
      className={cn('text-caption tabular-nums text-text-2', className)}
    >
      {windows.map((w, i) => (
        <Fragment key={w.key}>
          {i > 0 ? ' · ' : null}
          <span className={w.warning ? 'text-warning' : undefined}>
            {w.text}
          </span>
        </Fragment>
      ))}
    </span>
  )
}

/** The mark before a launch: the window at its limit and when it resets. */
export function AtLimit({
  usage,
  className,
}: {
  usage: readonly ProviderAccountUsage[] | undefined
  className?: string
}) {
  const { t, i18n } = useTranslation('agentops')
  const hit = limitReached(usage)
  if (!hit) return null
  const key =
    NAMED.find((n) => n.window === hit.window)?.key ?? ('other' as const)
  const time =
    hit.resets_at && !Number.isNaN(Date.parse(hit.resets_at))
      ? formatDateTime(hit.resets_at, i18n.language)
      : ''
  return (
    <span
      role="status"
      className={cn('text-caption font-medium text-warning', className)}
    >
      {t(`usage.limit.${key}${time ? 'Resets' : ''}`, {
        label: hit.label || hit.window,
        time,
      })}
    </span>
  )
}

/** The usage of the account a session or a pick names, read from the account list every
 * surface shares; nothing while the list is not allowed, not read yet, or lacks it. */
export function useAccountUsageOf(
  accountRef: string | undefined,
): readonly ProviderAccountUsage[] | undefined {
  const list = useProfileList(!!accountRef)
  return list.data?.accounts.find((a) => a.account_ref === accountRef)?.usage
}
