// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE STRIP AT THE TOP OF THE LIST: "2 need you · 5 running · $14 today", one line.
//
// Each part is a link. Need you and running narrow the list to their section (`?show=`,
// a second press shows the whole list again); the cost opens Usage. A part with nothing to
// say is left out, and with nothing at all there is no strip. An unknown cost is left out
// too: never "$0" for a cost nobody reported, never "at least" for part of one.
// Overview shows the same strip with what failed today, its filters opening Sessions.
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { formatMicroUsd } from '@/lib/format'
import { cn } from '@/lib/utils'
import { PANE_PARAM, SHOW_PARAM, type StripFilter } from './session-address'
import './i18n'

const PART =
  'rounded-sm outline-none hover:text-text hover:underline focus-visible:ring-2 focus-visible:ring-focus aria-[current=true]:font-semibold aria-[current=true]:text-text'

export function SessionStrip({
  needYou,
  running,
  failed = null,
  costMicroUsd,
  usagePath,
  listPath,
  show,
  toList = false,
  className,
}: {
  /** Sessions in the Needs you section. */
  needYou: number
  /** Sessions in the Working section; null when the run half was not read. */
  running: number | null
  /** Sessions that failed today (Overview); null when not said. */
  failed?: number | null
  /** What today's sessions cost, only when every one of them reported it; 0 is left out. */
  costMicroUsd: number | null
  /** The Usage page when the person may open it; else the cost is plain text. */
  usagePath: string | null
  /** Another page's strip (Overview): need you and running open this list narrowed to
   *  their section; null, the person may not open it and they are plain text. */
  listPath?: string | null
  /** The section the list is narrowed to now (`?show=`, decoded by the view). */
  show?: StripFilter
  /** The list is not on screen (a phone in a conversation): a filter also brings it. */
  toList?: boolean
  className?: string
}) {
  const { t } = useTranslation('sessions')

  const filter = (section: StripFilter, text: string) => {
    const tone = section === 'attention' && 'text-warning'
    if (listPath === null) return <span className={cn(tone)}>{text}</span>
    if (listPath)
      return (
        <Link
          to={listPath as never}
          search={{ [SHOW_PARAM]: section } as never}
          className={cn(PART, tone)}
        >
          {text}
        </Link>
      )
    const on = show === section
    return (
      <Link
        to="."
        search={
          ((prev: Record<string, unknown>) => ({
            ...prev,
            [SHOW_PARAM]: on ? undefined : section,
            ...(toList ? { [PANE_PARAM]: 'rail' } : {}),
          })) as never
        }
        // The router calls a link active when the URL holds every key it sets, and the
        // link that clears `show` would read as the page; `show` must match, unset or not.
        activeOptions={{ explicitUndefined: true }}
        aria-current={on ? 'true' : undefined}
        className={cn(PART, tone)}
      >
        {text}
      </Link>
    )
  }

  const cost = costMicroUsd
    ? t('strip.costToday', { amount: formatMicroUsd(costMicroUsd) })
    : null
  const parts = [
    needYou > 0
      ? filter('attention', t('strip.needYou', { count: needYou }))
      : null,
    running ? filter('active', t('strip.running', { count: running })) : null,
    failed ? (
      <span className="text-bad">
        {t('strip.failedToday', { count: failed })}
      </span>
    ) : null,
    cost && usagePath ? (
      <Link to={usagePath as never} className={PART}>
        {cost}
      </Link>
    ) : (
      cost
    ),
  ].filter(Boolean)
  if (parts.length === 0) return null

  return (
    <nav
      aria-label={t('strip.label')}
      aria-live="polite"
      data-testid="sessions-strip"
      className={cn(
        'min-w-0 truncate px-3 pb-2 text-caption tabular-nums text-text-2',
        className,
      )}
    >
      {parts.map((part, i) => (
        <span key={i}>
          {i > 0 ? <span aria-hidden="true"> · </span> : null}
          {part}
        </span>
      ))}
    </nav>
  )
}
