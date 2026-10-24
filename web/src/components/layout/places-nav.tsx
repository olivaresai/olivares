// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PLACES, IN THE TOP BAR: Now, Sessions, Tools, Rules and Ledger (Business adds
// Organization with its edition chip), each shown only when the principal may open it. The
// current place is the neutral tone step and full ink, never orange: orange marks the one
// thing that needs a person. The places fold on the room the bar gives them, measured
// against the width their labels need, not on the viewport and not on a threshold sized to
// one language: a ResizeObserver on the places box compares its available width with the
// unfolded row, measured once per label set and re-measured when the language, the place
// list, the counts or the edition chip changes. When the whole row does not fit, the other
// places fold to their icons; when not even the current place's label fits, it folds too.
// Each keeps its name for a screen reader and in a tooltip.
import { CountBadge } from '@/components/ui/count-badge'
import { Link, useRouterState } from '@tanstack/react-router'
import { type RefObject, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { EditionBadge } from '@/features/console/edition-badge'
import { useViewAccess } from '@/features/navigation/authorization'
import { resolveLocation } from '@/features/navigation/model'
import { cn } from '@/lib/utils'
import {
  currentDestination,
  journeyDestinations,
  type ShellDestination,
} from './shell-destinations'

const PLACE_CLASS = cn(
  'relative inline-flex h-9 min-w-9 shrink-0 items-center justify-center gap-2 rounded-full px-3 text-body font-medium whitespace-nowrap text-text-2 outline-none transition-colors duration-fast',
  'hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus',
  '[&_svg]:size-4 [&_svg]:shrink-0',
  'aria-[current=page]:bg-active aria-[current=page]:font-semibold aria-[current=page]:text-text',
)

/** The current place's look, for the measured copy (PLACE_CLASS applies it on aria-current). */
const CURRENT_LOOK = 'bg-active font-semibold text-text'

/** How much of the row fits: every label, the current one only, or none. */
type Fold = 'all' | 'current' | 'none'

/** One place's icon, label, count and edition chip, folded as `fold` says. */
function PlaceParts({
  place,
  label,
  count,
  urgent,
  here,
  fold,
  needsYou,
}: {
  place: ShellDestination
  label: string
  count?: number | string
  urgent: boolean
  here: boolean
  fold: Fold
  needsYou: string
}) {
  const Icon = place.icon
  const labelFolded = here ? fold === 'none' : fold !== 'all'
  return (
    <>
      <Icon aria-hidden />
      <span className={labelFolded ? 'sr-only' : undefined}>{label}</span>
      {count ? (
        <>
          <span className="sr-only">, </span>
          <CountBadge count={count} attention={urgent} />
        </>
      ) : null}
      {place.key === 'sessions' && urgent ? (
        <span data-slot="session-attention" className="flex items-center">
          <span
            aria-hidden
            className="block size-1.5 rounded-full bg-warning"
          />
          <span className="sr-only">, {needsYou}</span>
        </span>
      ) : null}
      {place.key === 'organization' ? (
        // The account menu names the edition to a screen reader.
        <span aria-hidden className={fold !== 'all' ? 'hidden' : undefined}>
          <EditionBadge compact />
        </span>
      ) : null}
    </>
  )
}

/** A hidden copy of the row, sized by its labels: `fold` fixes the state it measures. */
function MeasuredRow({
  rowRef,
  places,
  labels,
  counts,
  attention,
  current,
  fold,
  measure,
  needsYou,
}: {
  rowRef: RefObject<HTMLSpanElement | null>
  places: readonly ShellDestination[]
  labels: Readonly<Record<string, string>>
  counts?: Readonly<Record<string, number | string>>
  attention?: ReadonlySet<string>
  current: string | null
  fold: Fold
  measure: 'full' | 'tight'
  needsYou: string
}) {
  const folded = fold !== 'all'
  return (
    <span
      ref={rowRef}
      data-measure={measure}
      aria-hidden
      className="pointer-events-none invisible absolute top-0 -left-[10000px] flex w-max items-center gap-0.5 whitespace-nowrap"
    >
      {places.map((place) => {
        const here = current === place.key
        return (
          <span
            key={place.key}
            // No aria-current here: this copy is measured, not a place. It takes the current
            // place's look directly (the same classes PLACE_CLASS applies on aria-current),
            // because the semibold weight changes the width it measures.
            className={cn(
              PLACE_CLASS,
              here && CURRENT_LOOK,
              !here && folded && 'px-2.5',
            )}
          >
            <PlaceParts
              place={place}
              label={labels[place.key] ?? ''}
              count={counts?.[place.key]}
              urgent={attention?.has(place.key) ?? false}
              here={here}
              fold={fold}
              needsYou={needsYou}
            />
          </span>
        )
      })}
    </span>
  )
}

export function PlacesNav({
  counts,
  attention,
}: {
  /** A count shown after a place ("Rules 2"), by its label key. */
  counts?: Readonly<Record<string, number | string>>
  /** Keys whose count asks for a person (drawn in the warning role, not as plain text). */
  attention?: ReadonlySet<string>
}) {
  const { t } = useTranslation('nav')
  const { listed } = useViewAccess()
  const places = journeyDestinations(listed)
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const search = useRouterState({
    select: (s) => (s.location.search ?? {}) as Record<string, unknown>,
  })
  const current = currentDestination(resolveLocation(pathname), search, listed)
  const labels = Object.fromEntries(
    places.map((place) => [place.key, t(`shell.journeys.${place.key}`)]),
  )
  const needsYou = t('shell.rail.groups.needsYou')

  const boxRef = useRef<HTMLElement | null>(null)
  const fullRef = useRef<HTMLSpanElement | null>(null)
  const tightRef = useRef<HTMLSpanElement | null>(null)
  const [fold, setFold] = useState<Fold>('all')

  useLayoutEffect(() => {
    // The nav's parent is the places box the top bar gives it; its width is the room the
    // labels get. The hidden rows are measured too, so a language, a place, a count or
    // the edition chip that changes the labels re-measures them without a viewport query.
    const box = boxRef.current?.parentElement
    const full = fullRef.current
    const tight = tightRef.current
    if (!box || !full || !tight) return
    const fit = () => {
      const available = box.getBoundingClientRect().width
      const allLabels = full.getBoundingClientRect().width
      const currentOnly = tight.getBoundingClientRect().width
      setFold(
        available >= allLabels
          ? 'all'
          : available >= currentOnly
            ? 'current'
            : 'none',
      )
    }
    fit()
    const observer = new ResizeObserver(fit)
    observer.observe(box)
    observer.observe(full)
    observer.observe(tight)
    return () => observer.disconnect()
  }, [])

  return (
    <nav
      ref={boxRef}
      aria-label={t('shell.journeysLabel')}
      className="flex min-w-0 items-center gap-0.5"
    >
      {places.map((p) => {
        const count = counts?.[p.key]
        const urgent = attention?.has(p.key) ?? false
        const label = labels[p.key] ?? ''
        const here = current === p.key
        const folded = fold !== 'all'
        return (
          <Link
            key={p.key}
            to={p.path as never}
            search={p.search as never}
            // The model decides the current place. The router marks a link current on its
            // own (aria-current, after these props) wherever its path matches, so it is
            // held to the exact address, where it agrees with the model.
            activeOptions={{ exact: true }}
            aria-current={here ? 'page' : undefined}
            data-place={p.key}
            title={label}
            className={cn(PLACE_CLASS, !here && folded && 'px-2.5')}
          >
            <PlaceParts
              place={p}
              label={label}
              count={count}
              urgent={urgent}
              here={here}
              fold={fold}
              needsYou={needsYou}
            />
          </Link>
        )
      })}
      <MeasuredRow
        rowRef={fullRef}
        places={places}
        labels={labels}
        counts={counts}
        attention={attention}
        current={current}
        fold="all"
        measure="full"
        needsYou={needsYou}
      />
      <MeasuredRow
        rowRef={tightRef}
        places={places}
        labels={labels}
        counts={counts}
        attention={attention}
        current={current}
        fold="current"
        measure="tight"
        needsYou={needsYou}
      />
    </nav>
  )
}
