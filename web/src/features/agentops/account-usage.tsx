// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The usage an account's tool reported, said the same way wherever the console names the
// account: its share of the week and of five hours, a window at 100 % marked with when it
// resets, and nothing (never 0 %) when the tool reported none. Every window by its own name,
// with credits and reset times, is the account's detail on Usage, and only there.
import { Fragment } from 'react'
import { useTranslation } from 'react-i18next'
import type { ProviderSnapshot } from '@/features/agent-tools/api'
import { useProfileList } from '@/features/agent-tools/use-profiles'
import { formatDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import './i18n'
import type { ProviderAccountUsage } from './types'

/** The two windows with local names, shown first in the detail; other rows use the tool's label. */
const NAMED = [
  { window: 'five_hour', key: 'fiveHour' },
  { window: 'seven_day', key: 'week' },
] as const

/** The windows an account is judged by, the week first: its share of each is what every
 * place that names the account says. */
const SHARES = [
  { window: 'seven_day', key: 'shareOfWeek' },
  { window: 'five_hour', key: 'shareOfFiveHours' },
] as const

/** The engine's own labels for its two plan windows, for a reading that carries only the
 * label (the provider snapshot), and its label for a per-model weekly cap
 * ("Weekly (<model>)", agenttoolsapi/providers_probe.go). */
const ENGINE_LABELS: Record<string, string> = {
  '5-hour': 'five_hour',
  Weekly: 'seven_day',
}
const MODEL_WEEK = /^Weekly \((.+)\)$/

/** A provider snapshot's limits in an account's shape: the snapshot names a window by the
 * engine's label alone. */
export function snapshotUsage(s: ProviderSnapshot): ProviderAccountUsage[] {
  return s.limits.map((l) => ({
    window: ENGINE_LABELS[l.label] ?? l.label,
    label: l.label,
    utilization: l.percent,
    resets_at: l.resets_at,
    observed_at: s.checked_at,
  }))
}

/** A usage window in the console's language: "Semanal", or "Solo Fable" for a weekly cap
 * of one model. A window the console has no word for keeps the tool's own label. */
export function useWindowName(): (u: {
  window?: string
  label?: string
}) => string {
  const { t } = useTranslation('agentops')
  return ({ window, label = '' }) => {
    const id = window ?? ENGINE_LABELS[label]
    const key = NAMED.find((n) => n.window === id)?.key
    if (key) return t(`usage.${key}`)
    const model = MODEL_WEEK.exec(label)?.[1]
    if (model) return t('usage.weekModel', { model })
    return label || window || ''
  }
}

/** A percent that says 100 only when the window is at its limit. */
const shownPercent = (utilization: number) =>
  utilization >= 100 ? 100 : Math.min(99, Math.max(0, Math.round(utilization)))

/** The windows still in force. One whose reset time has passed is the tool's reading from
 * before the reset, so it is unknown now, not a figure and not a limit. */
const inForce = (usage: readonly ProviderAccountUsage[] | undefined) =>
  usage?.filter((u) => !u.resets_at || !(Date.parse(u.resets_at) <= Date.now()))

/** A window in force with the figure the tool gave: a percent or a credit balance. */
type Figure = {
  usage: ProviderAccountUsage
  percent?: number
  balance?: string
  warning: boolean
}

/** The windows in force the tool gave a figure for; one without is unknown, never 0. */
function figures(usage: readonly ProviderAccountUsage[] | undefined): Figure[] {
  return (inForce(usage) ?? []).flatMap((u): Figure[] => {
    if (u.credit) {
      const balance = u.credit.unlimited ? '∞' : u.credit.balance
      return balance === undefined
        ? []
        : [{ usage: u, balance, warning: false }]
    }
    if (u.utilization === undefined || !Number.isFinite(u.utilization))
      return []
    return [
      {
        usage: u,
        percent: shownPercent(u.utilization),
        warning: u.utilization >= 80,
      },
    ]
  })
}

/** The account's share of the week and of five hours, the week first; a window the tool
 * did not report is left out. */
export function sharesOf(usage: readonly ProviderAccountUsage[] | undefined) {
  const all = figures(usage)
  return SHARES.flatMap(({ window, key }) => {
    const f = all.find((f) => f.usage.window === window)
    return f?.percent === undefined
      ? []
      : [{ key, percent: f.percent, warning: f.warning }]
  })
}

/** The first window at 100 % or more (a per-model window counts), else undefined. */
export function limitReached(
  usage: readonly ProviderAccountUsage[] | undefined,
): ProviderAccountUsage | undefined {
  return inForce(usage)?.find(
    (u) => u.utilization !== undefined && u.utilization >= 100,
  )
}

/** "29% of the week · 17% of 5 hours". With `showUnknown` an account that reported nothing
 * says so; otherwise it says nothing. */
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
  const shares = sharesOf(usage)
  if (shares.length === 0)
    return showUnknown && figures(usage).length === 0 ? (
      <span className={cn('text-caption text-text-2', className)}>
        {t('usage.unknown')}
      </span>
    ) : null
  return (
    <span
      data-testid="account-usage"
      className={cn('text-caption tabular-nums text-text-2', className)}
    >
      {shares.map((s, i) => (
        <Fragment key={s.key}>
          {i > 0 ? ' · ' : null}
          <span className={s.warning ? 'text-warning' : undefined}>
            {t(`usage.${s.key}`, { percent: s.percent })}
          </span>
        </Fragment>
      ))}
    </span>
  )
}

/** Every window the account's tool reported, by name, with its figure and when it resets:
 * the account's detail on Usage, the one place a window's own name is shown. */
export function UsageWindows({
  usage,
  className,
}: {
  usage: readonly ProviderAccountUsage[] | undefined
  className?: string
}) {
  const { t, i18n } = useTranslation('agentops')
  const windowName = useWindowName()
  const all = figures(usage)
  if (all.length === 0) return null
  const ordered = [
    ...NAMED.flatMap(({ window }) =>
      all.filter((f) => f.usage.window === window),
    ),
    ...all.filter((f) => !NAMED.some((n) => n.window === f.usage.window)),
  ]
  return (
    <ul
      className={cn(
        'flex flex-col gap-1 text-caption tabular-nums text-text-2',
        className,
      )}
    >
      {ordered.map(({ usage: u, percent, balance, warning }, i) => {
        const name = windowName(u)
        const resets =
          u.resets_at && !Number.isNaN(Date.parse(u.resets_at))
            ? u.resets_at
            : undefined
        return (
          <li
            key={`${u.window}:${i}`}
            className="flex flex-wrap justify-between gap-x-3"
          >
            <span className={warning ? 'text-warning' : undefined}>
              {balance === undefined
                ? t('usage.percent', { window: name, percent })
                : `${name} ${balance}`}
            </span>
            {resets ? (
              <time dateTime={resets} className="text-text-3">
                {t('usage.resets', {
                  when: formatDateTime(resets, i18n.language),
                })}
              </time>
            ) : null}
          </li>
        )
      })}
    </ul>
  )
}

/** One short bar per account: the larger of its two shares, 0-100. */
export function UsageMeter({
  usage,
}: {
  usage: readonly ProviderAccountUsage[] | undefined
}) {
  const percents = sharesOf(usage).map((s) => s.percent)
  if (percents.length === 0) return null
  const peak = Math.max(...percents)
  // The figures beside it say the same in words; the bar is for the eye.
  return (
    <meter
      aria-hidden
      min={0}
      max={100}
      low={80}
      high={99}
      optimum={0}
      value={peak}
      className="ms-2 me-1.5 h-1.5 w-10 align-middle"
    />
  )
}

/** The mark before a launch: the limit reached and when it resets. A window other than the
 * week or five hours is not named here; its name is on Usage. `quiet` drops the live
 * status, for a row in a list that says it among others. */
export function AtLimit({
  usage,
  quiet = false,
  className,
}: {
  usage: readonly ProviderAccountUsage[] | undefined
  quiet?: boolean
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
      role={quiet ? undefined : 'status'}
      className={cn('text-caption font-medium text-warning', className)}
    >
      {t(`usage.limit.${key}${time ? 'Resets' : ''}`, { time })}
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
