// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE HISTOGRAM STRIP under the Ledger query: 48 px, one neutral bar per hour (per day or
// week over a longer window), every bar in the same quiet tone and at most 12 px wide: the
// bucket holding now is named in its label and by the axis's "Now", and one busy hour reads
// as a bar, never as a solid block. A bar is a button the width of its bucket: it
// filters the ledger to its bucket. One tab stop for the strip; the arrow keys move along
// it. Every bar names its bucket and count, so no value is pointer-only.
//
// ⛔ IT COUNTS THE ENTRIES LOADED. The ledger API has no count per bucket; the strip counts
//    what the list holds, and the list ends with its Load more row while more exist.
import { useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import type { AuditEventDTO } from '@/lib/api/types'
import { bucketsOf, type Bucket } from './ledger'

export function LedgerHistogram({
  entries,
  from,
  to,
  now,
  onPick,
}: {
  entries: readonly AuditEventDTO[]
  from: Date
  to: Date
  /** The moment the bucket drawn in full ink holds. */
  now: Date
  onPick: (bucket: Bucket) => void
}) {
  const { t, i18n } = useTranslation('audit')
  const { unit, buckets } = bucketsOf(entries, from, to)
  const max = Math.max(1, ...buckets.map((b) => b.count))
  const nowAt = now.getTime()
  const [focus, setFocus] = useState<number | null>(null)
  const active = Math.min(focus ?? buckets.length - 1, buckets.length - 1)
  const fmt = new Intl.DateTimeFormat(
    i18n.language,
    unit === 'hour'
      ? {
          month: 'short',
          day: 'numeric',
          hour: '2-digit',
          minute: '2-digit',
          hourCycle: 'h23',
        }
      : { weekday: 'short', month: 'short', day: 'numeric' },
  )
  const title = t(`ledger.histogram.${unit}`)

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    const step = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : null
    if (step === null) return
    e.preventDefault()
    const next = Math.min(buckets.length - 1, Math.max(0, active + step))
    setFocus(next)
    e.currentTarget.querySelectorAll<HTMLButtonElement>('button')[next]?.focus()
  }

  return (
    <figure className="flex flex-col gap-1">
      <figcaption className="text-caption font-medium text-text">
        {title}
      </figcaption>
      <div className="flex h-12 flex-col">
        <div
          role="group"
          aria-label={title}
          onKeyDown={onKeyDown}
          className="flex h-9 items-end gap-px border-b border-line"
        >
          {buckets.map((b, i) => {
            const isCurrent =
              nowAt >= b.start.getTime() && nowAt < b.end.getTime()
            const label =
              t('ledger.histogram.bar', {
                at: fmt.format(b.start),
                count: b.count,
              }) + (isCurrent ? `, ${t('ledger.histogram.now')}` : '')
            return (
              <button
                key={b.start.getTime()}
                type="button"
                tabIndex={i === active ? 0 : -1}
                onClick={() => {
                  setFocus(null)
                  onPick(b)
                }}
                onFocus={() => setFocus(i)}
                aria-label={label}
                title={label}
                className="flex h-full min-w-0 flex-1 items-end justify-center outline-none hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus"
              >
                {b.count > 0 ? (
                  <span
                    aria-hidden
                    style={{ height: `${Math.max(8, (b.count / max) * 100)}%` }}
                    className="block w-full max-w-3 rounded-t-[2px] bg-ctl-border"
                  />
                ) : null}
              </button>
            )
          })}
        </div>
        <div
          aria-hidden
          className="flex h-3 justify-between text-overline leading-3 text-text-2"
        >
          <span>{fmt.format(buckets[0]?.start ?? from)}</span>
          <span>
            {nowAt <= to.getTime() ? t('ledger.histogram.now') : fmt.format(to)}
          </span>
        </div>
      </div>
    </figure>
  )
}
