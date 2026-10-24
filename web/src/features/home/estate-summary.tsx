// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ESTATE IN ONE SENTENCE (#2546, C2-IMPROVE E3): what runs, for how many people, what
// it cost today and what needs a person, in large type, where the figure tiles were. One
// quiet line under it puts today against yesterday and, when a monthly budget is read,
// against the month. Every clause comes from a read `home-view` already made; a clause
// whose source has no answer is left out, and nothing is said as a zero it did not read.
import { useTranslation } from 'react-i18next'
import { Skeleton } from '@/components/ui/skeleton'
import { formatMicroUsd } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { PendingReason, TileState } from './view-state'
import type { MonthBudget } from './month-budget'
import type { TodaysCost } from './now-sessions'
import './i18n'

export interface EstateSummaryProps {
  /** The sessions reads' state: no figure while loading, a sentence of failure on error. */
  state: TileState
  /** Why a read with no answer is not fetching (paused offline, or not started). */
  pending?: PendingReason
  /** Live sessions; `null` while unknown. */
  running: number | null
  /** The live page was cut: `running` is a floor, and whom they act for is not known. */
  runningMore?: boolean
  /** People the live sessions act for; `null` when one of them does not say. */
  people: number | null
  today: TodaysCost | null
  /** The page was cut while it still held today's sessions: today is at least the sum. */
  todayFloor: boolean
  yesterday: TodaysCost | null
  /** What needs a person: pending requests plus sessions and handoffs waiting. */
  needs: { count: number; more: boolean } | null
  /** The tenant's monthly budget, when one is read and its amount is proven. */
  month: MonthBudget | null
}

/** Where the month meter turns to a warning: most of the budget is spent. */
const MONTH_WARNING = 0.8

export function EstateSummary({
  state,
  pending = null,
  running,
  runningMore = false,
  people,
  today,
  todayFloor,
  yesterday,
  needs,
  month,
}: EstateSummaryProps) {
  const { t } = useTranslation(['home', 'nav'])
  const usd = (micro: number) => formatMicroUsd(micro)

  const clauses: string[] = []
  if (running === 0) {
    // A cut page with no live session on it cannot say that nothing runs.
    if (!runningMore) clauses.push(t('summary.nothingRunning'))
  } else if (running !== null)
    clauses.push(
      runningMore
        ? t('summary.runningMore', { n: running })
        : people
          ? t('summary.runningFor', {
              count: running,
              people: t('summary.people', { count: people }),
            })
          : t('summary.running', { count: running }),
    )
  if (today?.costMicroUsd != null)
    clauses.push(
      t(todayFloor ? 'summary.todayFloor' : 'summary.today', {
        amount: usd(today.costMicroUsd),
      }),
    )
  if (needs && needs.count > 0)
    clauses.push(
      needs.more
        ? t('summary.needsMore', { n: needs.count })
        : t('summary.needs', { count: needs.count }),
    )

  const spend: string[] = []
  if (yesterday?.costMicroUsd != null)
    spend.push(t('summary.yesterday', { amount: usd(yesterday.costMicroUsd) }))
  if (today && today.unpriced > 0)
    spend.push(t('summary.unpriced', { count: today.unpriced }))

  return (
    <section
      aria-labelledby="estate-summary-heading"
      data-testid="estate-summary"
      data-estate-region="summary"
      className="flex min-w-0 flex-col gap-2"
    >
      <h2 id="estate-summary-heading" className="sr-only">
        {t('summary.heading')}
      </h2>
      {state === 'loading' && pending ? (
        <p className="text-body text-text-2">{t(`state.${pending}`)}</p>
      ) : state === 'loading' ? (
        <Skeleton className="h-10 w-[min(36rem,100%)]" />
      ) : state === 'unavailable' ? (
        <p className="text-body text-text-2">{t('nav:shell.rail.error')}</p>
      ) : (
        <>
          {clauses.length > 0 ? (
            <p
              data-testid="estate-summary-sentence"
              className="text-balance font-display text-display text-text tabular-nums xl:text-display-lg"
            >
              {clauses.join(' · ')}
            </p>
          ) : null}
          {spend.length > 0 || month ? (
            <p
              data-testid="estate-summary-spend"
              className="flex flex-wrap items-center gap-x-3 gap-y-1 text-body text-text-2 tabular-nums"
            >
              {spend.length > 0 ? <span>{spend.join(' · ')}</span> : null}
              {month ? <MonthMeter {...month} /> : null}
            </p>
          ) : null}
        </>
      )}
    </section>
  )
}

/** Spend this month against the monthly budget: a meter in neutral ink that turns to a
 * warning past most of the budget and to danger over it, with the figures in words. */
function MonthMeter({ spentMicroUsd, limitMicroUsd, atLeast }: MonthBudget) {
  const { t } = useTranslation('home')
  const text = t(atLeast ? 'summary.monthFloor' : 'summary.month', {
    spent: formatMicroUsd(spentMicroUsd),
    limit: formatMicroUsd(limitMicroUsd),
  })
  const ratio = spentMicroUsd / limitMicroUsd
  return (
    <span className="inline-flex items-center gap-2">
      <span
        role="meter"
        aria-label={t('summary.monthLabel')}
        aria-valuemin={0}
        aria-valuemax={limitMicroUsd / 1_000_000}
        aria-valuenow={Math.min(spentMicroUsd, limitMicroUsd) / 1_000_000}
        aria-valuetext={text}
        className="relative h-1.5 w-24 overflow-hidden rounded-full bg-muted"
      >
        <span
          className={cn(
            'absolute inset-y-0 left-0 rounded-full',
            ratio >= 1
              ? 'bg-danger'
              : ratio >= MONTH_WARNING
                ? 'bg-warning'
                : 'bg-text-2',
          )}
          style={{ width: `${Math.min(ratio, 1) * 100}%` }}
        />
      </span>
      {/* The meter already says this to assistive technology (aria-valuetext). */}
      <span aria-hidden>{text}</span>
    </span>
  )
}
