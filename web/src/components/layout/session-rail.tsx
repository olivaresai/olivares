// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION RAIL (redesign §3.2): the live sessions grouped by what they need from the
// operator — Needs you, Working, Earlier — with the handoffs offered to the operator in
// Needs you (§3.7.14). The grouping is `session-rail-model.ts`; this file reads the two
// sources and draws the rows.
//
// The reads are `use-session-rail.ts`: only what the principal's journeys may read.
//
// ONE TAB STOP, arrows inside, as the area directory and the work rail do: the rail can
// hold two dozen rows, and Tab must reach the top bar and the work without walking them.
// The keys are the table's `rail.*` rules (lib/keybindings/table.ts), resolved with
// `railFocused`, so the rail answers the same keys everywhere it appears.
import { Link } from '@tanstack/react-router'
import { ChevronRight, CircleCheck, Pause } from 'lucide-react'
import {
  useEffect,
  useId,
  useRef,
  type KeyboardEvent,
  type RefObject,
} from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import {
  type RailGroup,
  type RailRow,
  type RailState,
} from './session-rail-model'

export type RailStatus = 'loading' | 'ready' | 'error'

/** The glyph of a state, with its word as the accessible name. Colour never carries the
 * state alone: the word is always there for a screen reader, and the shape differs. */
export function RailGlyph({ state }: { state: RailState }) {
  const { t } = useTranslation('nav')
  const word = t(`shell.rail.state.${state}`)
  if (state === 'need')
    return (
      <span
        role="img"
        aria-label={word}
        className="m-0.5 inline-block size-2.5 shrink-0 rotate-45 rounded-[2px] bg-warn"
      />
    )
  if (state === 'live')
    return (
      <span
        role="img"
        aria-label={word}
        className="inline-block size-3.5 shrink-0 rounded-full border-2 border-accent-soft border-t-accent border-r-accent motion-safe:animate-spin motion-safe:[animation-duration:1.2s]"
      />
    )
  const Icon = state === 'ended' ? CircleCheck : Pause
  return (
    <Icon
      role="img"
      aria-label={word}
      className="size-3.5 shrink-0 text-text-3"
    />
  )
}

/** "2m", "3h", "4d": the compact elapsed time of a row, in the operator's language. */
function elapsed(t: TFunction, minutes: number): string {
  if (minutes < 60) return t('nav:shell.rail.minutes', { count: minutes })
  if (minutes < 60 * 24)
    return t('nav:shell.rail.hours', { count: Math.floor(minutes / 60) })
  return t('nav:shell.rail.days', { count: Math.floor(minutes / (60 * 24)) })
}

function RailRowLink({ row }: { row: RailRow }) {
  const { t } = useTranslation('nav')
  // A row the engine gave no title is named by its identifier: the localized word, then the
  // identifier in mono, so an id never reads as prose (a work item id; a session id cut to
  // eight characters, the full one in the title).
  const named =
    row.kind === 'handoff'
      ? { label: t('shell.rail.workItem'), id: row.reference }
      : row.title
        ? null
        : { label: t('shell.rail.session'), id: row.reference.slice(0, 8) }
  const title = named
    ? `${named.label} ${row.kind === 'handoff' ? named.id : row.reference}`
    : (row.title ?? '')
  const meta =
    row.kind === 'handoff'
      ? t('shell.rail.handoffFrom', { from: row.from ?? '' })
      : row.meta
  return (
    <Link
      to={row.to as never}
      search={row.search as never}
      // The rail is one tab stop: every row starts untabbable and `useRailRoving`
      // promotes exactly one.
      tabIndex={-1}
      data-rail-row={row.kind}
      className={cn(
        'relative grid grid-cols-[18px_minmax(0,1fr)_auto] gap-x-2 gap-y-0.5 rounded-ctl px-2.5 py-[7px] outline-none',
        'hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus',
      )}
    >
      <span className="row-span-2 flex h-5 items-center justify-center">
        <RailGlyph state={row.state} />
      </span>
      <span className="truncate text-body font-medium text-text" title={title}>
        {named ? (
          <>
            {named.label}{' '}
            <span data-rail-id="" className="font-mono text-mono-s">
              {named.id}
            </span>
          </>
        ) : (
          title
        )}
      </span>
      <span
        className={cn(
          'text-overline tabular-nums',
          row.state === 'live' ? 'text-accent-text' : 'text-text-3',
        )}
      >
        {elapsed(t, row.minutes)}
      </span>
      {meta ? (
        <span
          className="col-span-2 col-start-2 truncate text-caption text-text-3"
          title={meta}
        >
          {meta}
        </span>
      ) : null}
    </Link>
  )
}

/** Exactly one row of the rail is tabbable; the table's rail keys move it. The rows are
 * read from the DOM at keystroke time, so the shape the operator sees is the one walked. */
function useRailRoving(ref: RefObject<HTMLDivElement | null>) {
  const rowsOf = (): HTMLElement[] =>
    Array.from(
      ref.current?.querySelectorAll<HTMLElement>('[data-rail-row]') ?? [],
    )
  useEffect(() => {
    const rows = rowsOf()
    if (rows.length === 0) return
    const at = Math.max(
      0,
      rows.findIndex((r) => r.tabIndex === 0),
    )
    rows.forEach((r, i) => {
      r.tabIndex = i === at ? 0 : -1
    })
  })
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const rows = rowsOf()
    const from = (event.target as HTMLElement).closest<HTMLElement>(
      '[data-rail-row]',
    )
    const at = from ? rows.indexOf(from) : -1
    if (at < 0) return
    const command = resolveBinding(KEYBINDINGS, event, { railFocused: true })
    const to =
      command === 'rail.next'
        ? Math.min(at + 1, rows.length - 1)
        : command === 'rail.previous'
          ? Math.max(at - 1, 0)
          : command === 'rail.first'
            ? 0
            : command === 'rail.last'
              ? rows.length - 1
              : null
    if (to === null) return
    event.preventDefault()
    rows.forEach((r, i) => {
      r.tabIndex = i === to ? 0 : -1
    })
    rows[to]?.focus()
  }
  return onKeyDown
}

/** The rail, drawn from its groups. Pure: `useSessionRail` feeds it. */
export function SessionRailView({
  groups,
  status,
}: {
  groups: readonly RailGroup[]
  status: RailStatus
}) {
  const { t } = useTranslation('nav')
  const headingId = useId()
  const railRef = useRef<HTMLDivElement>(null)
  const onRailKeyDown = useRailRoving(railRef)
  return (
    <section
      aria-labelledby={headingId}
      aria-busy={status === 'loading' || undefined}
      className="flex min-h-0 flex-1 flex-col"
    >
      <div className="flex items-center justify-between px-2.5 pt-3 pb-1">
        <h2 id={headingId} className="text-overline font-semibold text-text-3">
          {t('shell.rail.title')}
        </h2>
        <Link
          to={'/sessions' as never}
          className="inline-flex min-h-6 items-center gap-0.5 rounded-ctl px-1 text-caption text-text-3 outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
        >
          {t('shell.rail.all')}
          <ChevronRight aria-hidden className="size-3.5" />
        </Link>
      </div>
      <div
        ref={railRef}
        onKeyDown={onRailKeyDown}
        data-slot="session-rail"
        className="flex min-h-0 flex-1 flex-col gap-px overflow-y-auto [mask-image:linear-gradient(to_bottom,#000_calc(100%-24px),transparent)] pb-6"
      >
        {status === 'error' ? (
          <p className="px-2.5 py-2 text-caption text-text-3">
            {t('shell.rail.error')}
          </p>
        ) : status === 'loading' ? (
          <p className="px-2.5 py-2 text-caption text-text-3">
            {t('shell.rail.loading')}
          </p>
        ) : (
          groups.map((group) => (
            <div
              key={group.id}
              role="group"
              aria-label={t(`shell.rail.groups.${group.id}`)}
              data-rail-group={group.id}
              className="flex flex-col gap-px"
            >
              <p
                aria-hidden
                className="px-2.5 pt-2 pb-0.5 text-overline text-text-3"
              >
                {t(`shell.rail.groups.${group.id}`)}
              </p>
              {group.rows.length === 0 ? (
                <p className="px-2.5 pb-1 text-caption text-text-3">
                  {t(`shell.rail.empty.${group.id}`)}
                </p>
              ) : (
                group.rows.map((row) => <RailRowLink key={row.key} row={row} />)
              )}
            </div>
          ))
        )}
      </div>
    </section>
  )
}
