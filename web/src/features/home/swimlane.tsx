// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// LAST HOUR (#2547): every live session over the hour that ends now, one row each. A bar
// is a stretch of activity and its shade is how many tool calls it made, minute by minute
// (no tick per call); a warn-tinted span waits on an approval, a dotted line waits for the
// person's next prompt, a red tick is a failure, and the approval Needs you shows first is
// the one orange mark. Each row opens its session in the Estate sheet. Dragging across
// the rows, or choosing a ten-minute mark, selects a range and offers the Ledger for it.
// The numbers are in each row's name and in the table under the rows; nothing moves but
// the clock, so reduced motion changes nothing.
import { useQueries } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { CalendarClock } from 'lucide-react'
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent,
} from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Button } from '@/components/ui/button'
import { MonoMark } from '@/components/ui/mono-mark'
import { StatusChip } from '@/components/ui/status-chip'
import { RunStateBadge } from '@/features/agentops/run-state-badge'
import type { ApprovalDTO } from '@/features/governance/types'
import { sessionsApi } from '@/features/sessions/api'
import { CcStateBadge } from '@/features/sessions/cc-state-badge'
import { runFolder } from '@/features/sessions/folder'
import { opensInSheet } from '@/features/sessions/opens-in-sheet'
import {
  primaryRun,
  sessionNaming,
  sessionTool,
  sharedNames,
  type UnifiedSession,
} from '@/features/sessions/provenance'
import { addressOf, SESSION_PARAM } from '@/features/sessions/session-address'
import type { TimelineDTO } from '@/features/sessions/types'
import { useAuth } from '@/lib/auth/context'
import { formatInt, formatTimeOfDay } from '@/lib/format'
import { cn } from '@/lib/utils'
import {
  buildLanes,
  densityStep,
  ledgerRange,
  onSwimlane,
  TICK_MS,
  WINDOW_MS,
  windowTicks,
  type Lane,
} from './swimlane-model'
import './i18n'

const MINUTE = 60_000
/** One page of a row's hour; a row whose hour needs more pages than this is not drawn. */
const PAGE = 1000
const MAX_PAGES = 10
/** A drag shorter than this is a click, not a range. */
const DRAG_PX = 4

/** The four density tones, weakest first: the share of the text colour a minute of a
 * run is drawn in. One hue, an ordinal ramp the dataviz validator passed in both themes;
 * the weakest still clears 3:1 against the surface (WCAG 1.4.11). */
const TONE = ['52%', '64%', '76%', '90%'] as const
const tone = (calls: number) =>
  `color-mix(in oklab, var(--text) ${TONE[densityStep(calls)]}, transparent)`

/** The tool's own colour, beside its name (never alone). Literal classes, so the build
 * keeps them. */
const TOOL_MARK: Record<string, string> = {
  claude: 'bg-tool-claude',
  codex: 'bg-tool-codex',
  opencode: 'bg-tool-opencode',
  'gemini-cli': 'bg-tool-gemini',
}

/** Every event of one session from `since` on, page by page. */
async function hourOf(s: UnifiedSession, since: string, signal: AbortSignal) {
  const out: TimelineDTO[] = []
  let cursor: string | undefined
  for (let page = 0; page < MAX_PAGES; page++) {
    const params = { since, limit: PAGE, cursor }
    const res = s.liveRef
      ? await sessionsApi.timelineById(s.liveRef, params, { signal })
      : await sessionsApi.timeline(s.sessionRef!, params, { signal })
    out.push(...res.items)
    if (!res.has_more) return out
    // More pages and no way to them: a part of the hour is not an hour.
    if (!res.cursor)
      throw new Error('the timeline said it has more and gave no cursor')
    cursor = res.cursor
  }
  throw new Error(`more than ${PAGE * MAX_PAGES} events in the last hour`)
}

/** Where a time sits on the plot, as a share of its width. */
const pct = (ms: number, now: number) =>
  `${Math.min(100, Math.max(0, ((ms - (now - WINDOW_MS)) / WINDOW_MS) * 100))}%`

function span(start: number, end: number, now: number): CSSProperties {
  return {
    left: pct(start, now),
    width: `max(3px, calc(${pct(end, now)} - ${pct(start, now)}))`,
  }
}

/** A run's time in words: one time when it fits in one minute, else from and to. */
function runWords(
  r: { start: number; end: number },
  clock: (ms: number) => string,
  t: TFunction<'home'>,
): string {
  const from = clock(r.start)
  const to = clock(r.end)
  return from === to ? from : t('swimlane.range', { from, to })
}

/** What a row is waiting on, or how it ended, in words: the real moments, never the
 * window edge a mark is cut to. */
function stateWords(
  lane: Lane,
  clock: (ms: number) => string,
  t: TFunction<'home'>,
): string[] {
  const words = lane.waits.map((w) =>
    w.asked === undefined
      ? t(w.flagged ? 'swimlane.needsYou' : 'swimlane.waiting')
      : t(w.flagged ? 'swimlane.needsYouSince' : 'swimlane.waitingSince', {
          at: clock(w.asked),
        }),
  )
  if (lane.state === 'idle')
    words.push(
      lane.idleSince === undefined
        ? t('swimlane.idle')
        : t('swimlane.idleSince', { at: clock(lane.idleSince) }),
    )
  if (lane.failedAt !== undefined)
    words.push(t('swimlane.failedAt', { at: clock(lane.failedAt) }))
  return words
}

/** What one row says in words: its name and every number it draws. A row whose events
 * are still on their way says so, and one whose read failed says that. */
function laneSentence(
  lane: Lane,
  name: string,
  read: 'pending' | 'error' | 'ok',
  clock: (ms: number) => string,
  t: TFunction<'home'>,
): string {
  const parts = [name]
  if (!lane.read)
    parts.push(t(read === 'pending' ? 'swimlane.reading' : 'swimlane.unread'))
  else if (lane.runs.length === 0) parts.push(t('swimlane.noActivity'))
  else
    parts.push(
      t('swimlane.active', {
        spans: lane.runs.map((r) => runWords(r, clock, t)).join(', '),
      }),
    )
  if (lane.read)
    parts.push(
      t('swimlane.toolCalls', {
        count: lane.toolCalls,
        n: formatInt(lane.toolCalls),
      }),
    )
  return [...parts, ...stateWords(lane, clock, t)].join('. ') + '.'
}

function LanePlot({
  lane,
  now,
  clock,
}: {
  lane: Lane
  now: number
  clock: (ms: number) => string
}) {
  const { t } = useTranslation('home')
  return (
    <>
      {lane.runs.map((r) => (
        <span
          key={r.start}
          data-mark="run"
          title={t('swimlane.runTip', {
            from: clock(r.start),
            to: clock(r.end),
            count: r.density.reduce((a, b) => a + b, 0),
          })}
          className="absolute top-1/2 h-3.5 -translate-y-1/2 rounded-sm"
          style={{
            ...span(r.start, r.end, now),
            // A stepped gradient: each minute of the run in its own tone.
            background: `linear-gradient(to right, ${r.density
              .map((c, i, all) => {
                const a = (i / all.length) * 100
                const b = ((i + 1) / all.length) * 100
                return `${tone(c)} ${a}% ${b}%`
              })
              .join(', ')})`,
          }}
        />
      ))}
      {lane.idleFrom !== undefined ? (
        <span
          data-mark="idle"
          className="absolute top-1/2 border-t-2 border-dotted border-text-3"
          style={span(lane.idleFrom, now, now)}
        />
      ) : null}
      {lane.waits.map((w) =>
        w.asked === undefined ? null : (
          <span
            key={w.id}
            data-mark="wait"
            title={t(
              w.flagged ? 'swimlane.needsYouSince' : 'swimlane.waitingSince',
              { at: clock(w.asked) },
            )}
            className={cn(
              'absolute rounded-[3px] bg-warn',
              w.strip
                ? 'top-[calc(50%+11px)] h-1'
                : 'top-1/2 h-3.5 -translate-y-1/2 border border-warn/50 bg-warn/40',
            )}
            style={span(w.start, w.end, now)}
          />
        ),
      )}
      {lane.asks.map((a) => (
        <span
          key={a.id}
          data-mark={a.flagged ? 'ask-flagged' : 'ask'}
          className={cn(
            'absolute top-1/2 h-7 w-[3px] -translate-x-1/2 -translate-y-1/2 rounded-[2px]',
            a.flagged ? 'bg-accent' : 'bg-text',
          )}
          style={{ left: pct(a.at, now) }}
        />
      ))}
      {lane.failedAt !== undefined ? (
        <>
          <span
            data-mark="failure"
            className="absolute top-1/2 h-7 w-[3px] -translate-x-1/2 -translate-y-1/2 rounded-[2px] bg-bad"
            style={{ left: pct(lane.failedAt, now) }}
          />
          <span
            className="absolute top-0.5 whitespace-nowrap text-overline font-medium leading-tight text-bad"
            style={
              // The word sits on the side of the tick with room for it.
              (lane.failedAt - (now - WINDOW_MS)) / WINDOW_MS > 0.8
                ? { right: `calc(100% - ${pct(lane.failedAt, now)} + 6px)` }
                : { left: `calc(${pct(lane.failedAt, now)} + 6px)` }
            }
          >
            {t('swimlane.failed')}
          </span>
        </>
      ) : null}
    </>
  )
}

export interface SwimlaneProps {
  /** The sessions Estate already read (live and settled, merged). */
  sessions: readonly UnifiedSession[]
  /** The pending approvals in the queue's order. */
  approvals: readonly ApprovalDTO[]
  /** False when the approvals could not be read (refused or failed): no wait is drawn,
   * and the swimlane says why instead of implying that nothing waits. */
  approvalsRead?: boolean
  now: number
  onOpen?: (address: string) => void
}

export function Swimlane({
  sessions,
  approvals,
  approvalsRead = true,
  now,
  onOpen,
}: SwimlaneProps) {
  const { t, i18n } = useTranslation(['home', 'sessions'])
  const { activeTenant } = useAuth()
  const clock = (ms: number) => formatTimeOfDay(ms, i18n.language)
  // The hour starts on a whole minute. One key per session, read again every minute,
  // so a row keeps its bars while the next minute is on its way; the read takes the
  // hour that ends at the latest render.
  const since = new Date(
    Math.floor((now - WINDOW_MS) / MINUTE) * MINUTE,
  ).toISOString()
  const shown = sessions.filter((s) => onSwimlane(s, now - WINDOW_MS))
  const reads = useQueries({
    queries: shown.map((s) => ({
      // Its own root: the reads under `sessions` are invalidated by a pin or a move,
      // which must not read every row's hour again.
      queryKey: ['swimlane', activeTenant, s.liveRef ?? s.sessionRef ?? s.key],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        hourOf(s, since, signal),
      // A session the plane has not observed yet has no events to read.
      enabled: !!(s.liveRef || s.sessionRef),
      refetchInterval: MINUTE,
      // A failed read says so at once and is read again in a minute; repeating a read
      // of up to ten pages would only add load.
      retry: false,
    })),
  })
  const observed = (s: UnifiedSession) => !!(s.liveRef || s.sessionRef)
  // A read that failed is not read, even when an older minute is still at hand: the
  // lane says so rather than draw a past minute as now.
  const readOf = (i: number) =>
    !observed(shown[i]!)
      ? ('ok' as const)
      : reads[i]?.isError
        ? ('error' as const)
        : reads[i]?.data
          ? ('ok' as const)
          : ('pending' as const)
  // Derived once per answer and per minute, not on every pointer move of a drag.
  const answers = reads.map((r) => `${r.dataUpdatedAt}:${r.isError}`).join()
  const lanes = useMemo(
    () =>
      buildLanes({
        sessions: shown,
        events: new Map(
          shown.map((s, i) => [
            s.key,
            !observed(s)
              ? ([] as TimelineDTO[])
              : readOf(i) === 'ok'
                ? reads[i]?.data
                : undefined,
          ]),
        ),
        approvals,
        now,
      }),
    // `shown` and `reads` follow `sessions`, `answers` and `now`.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [sessions, answers, approvals, now],
  )
  const readByKey = new Map(shown.map((s, i) => [s.key, readOf(i)]))
  const untitled = t('recent.untitled')
  const shared = sharedNames(shown, untitled)
  const ticks = windowTicks(now)

  // Range selection: a drag across the plot, or a ten-minute mark.
  const plotRef = useRef<HTMLDivElement>(null)
  const drag = useRef<{ x: number; t: number } | null>(null)
  const [range, setRange] = useState<{ a: number; b: number } | null>(null)
  const sectionRef = useRef<HTMLElement>(null)
  // A range chosen from the keyboard (a ten-minute mark) moves focus to its one next
  // step, the Ledger link, which carries the range as its description: the focus says it,
  // so the swimlane adds no live region to Estate.
  const ledgerRef = useRef<HTMLAnchorElement>(null)
  const focusLedger = useRef(false)
  useEffect(() => {
    if (!focusLedger.current || !range) return
    focusLedger.current = false
    ledgerRef.current?.focus()
  }, [range])
  const timeAt = (clientX: number) => {
    const box = plotRef.current?.getBoundingClientRect()
    if (!box || box.width === 0) return now
    const share = Math.min(1, Math.max(0, (clientX - box.left) / box.width))
    return now - WINDOW_MS + share * WINDOW_MS
  }
  const onPointerDown = (e: PointerEvent<HTMLElement>) => {
    if (e.button !== 0 || (e.target as HTMLElement).closest('a,button')) return
    const box = plotRef.current?.getBoundingClientRect()
    if (!box || e.clientX < box.left || e.clientX > box.right) return
    drag.current = { x: e.clientX, t: timeAt(e.clientX) }
    e.currentTarget.setPointerCapture?.(e.pointerId)
  }
  const onPointerMove = (e: PointerEvent<HTMLElement>) => {
    if (!drag.current || Math.abs(e.clientX - drag.current.x) < DRAG_PX) return
    setRange({ a: drag.current.t, b: timeAt(e.clientX) })
  }
  const onPointerUp = () => {
    drag.current = null
  }
  const clear = () => {
    setRange(null)
    // The button that had focus is gone: focus stays in the swimlane.
    sectionRef.current?.focus()
  }
  const selected = range ? ledgerRange(range.a, range.b) : null

  if (lanes.length === 0) return null
  return (
    <section
      ref={sectionRef}
      tabIndex={-1}
      aria-labelledby="swimlane-heading"
      data-testid="estate-swimlane"
      className="mb-6 min-w-0 outline-none rounded-card border border-line bg-surface p-4 [--label:15rem] [--side:8.5rem] xl:[--label:18rem]"
    >
      <div className="mb-2 flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2
          id="swimlane-heading"
          className="text-heading font-semibold text-text"
        >
          {t('swimlane.title')}
        </h2>
        <p className="text-caption text-text-3">
          {t('swimlane.subtitle', {
            from: clock(now - WINDOW_MS),
            to: clock(now),
          })}
        </p>
        {approvalsRead ? null : (
          <p className="w-full text-caption text-text-3">
            {t('swimlane.approvalsUnread')}
          </p>
        )}
      </div>

      <div
        data-testid="swimlane-area"
        className="relative touch-pan-y select-none"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
      >
        {/* The axis: a ten-minute mark selects its ten minutes (the one-click way to a range). */}
        <div className="grid grid-cols-[var(--label)_minmax(0,1fr)_var(--side)] items-center">
          <span />
          <div
            ref={plotRef}
            data-testid="swimlane-plot"
            className="relative h-7"
          >
            {ticks.map((tick) => (
              <button
                key={tick}
                type="button"
                aria-label={t('swimlane.selectSlot', {
                  from: clock(tick),
                  to: clock(tick + TICK_MS),
                })}
                onClick={() => {
                  focusLedger.current = true
                  setRange({ a: tick, b: Math.min(tick + TICK_MS, now) })
                }}
                className="absolute top-0 h-7 -translate-x-1/2 rounded-full px-1.5 text-caption tabular-nums text-text-3 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
                style={{ left: pct(tick, now) }}
              >
                {clock(tick)}
              </button>
            ))}
          </div>
          <span className="justify-self-start pl-2 text-caption font-medium tabular-nums text-text">
            {t('swimlane.now', { at: clock(now) })}
          </span>
        </div>

        {/* Grid lines, the selected range and the now line, under and over the lanes. */}
        <div
          aria-hidden
          className="pointer-events-none absolute top-7 bottom-0 left-[var(--label)] right-[var(--side)]"
        >
          {ticks.map((tick) => (
            <span
              key={tick}
              className="absolute top-0 bottom-0 w-px bg-line"
              style={{ left: pct(tick, now) }}
            />
          ))}
          {range ? (
            <span
              data-testid="swimlane-range"
              className="absolute top-0 bottom-0 bg-text/10"
              style={span(
                Math.min(range.a, range.b),
                Math.max(range.a, range.b),
                now,
              )}
            />
          ) : null}
          <span className="absolute top-0 right-0 bottom-0 border-r border-dashed border-text" />
        </div>

        <ol className="relative grid gap-0.5" aria-label={t('swimlane.lanes')}>
          {lanes.map((lane) => {
            const s = lane.session
            const run = primaryRun(s.runs)
            const naming = sessionNaming(s, untitled, shared)
            const tool = sessionTool(s)
            const driver =
              run?.provider_driver || s.live?.provider || s.live?.engine
            const actsFor = run?.acts_for
            const person =
              actsFor?.kind === 'automation'
                ? t('swimlane.automation')
                : actsFor?.name
            const folder = runFolder(run)
            const meta = [tool, person, folder].filter(Boolean).join(' · ')
            return (
              <li
                key={s.key}
                data-testid="swimlane-lane"
                className="grid h-[60px] grid-cols-[var(--label)_minmax(0,1fr)_var(--side)] items-center rounded-ctl hover:bg-hover"
              >
                <Link
                  to={'/sessions' as never}
                  search={{ [SESSION_PARAM]: addressOf(s) } as never}
                  onClick={(e) => {
                    if (!onOpen || !opensInSheet(e)) return
                    e.preventDefault()
                    onOpen(addressOf(s))
                  }}
                  className="flex min-w-0 items-center gap-2.5 rounded-ctl py-1 pr-3 pl-1 outline-none focus-visible:ring-2 focus-visible:ring-focus"
                >
                  {actsFor?.kind === 'automation' ? (
                    <span
                      aria-hidden
                      className="grid size-7 shrink-0 place-items-center rounded-[8px] bg-hover text-text-2"
                    >
                      <CalendarClock className="size-3.5" />
                    </span>
                  ) : actsFor?.name ? (
                    <MonoMark name={actsFor.name} className="shrink-0" />
                  ) : (
                    <span aria-hidden className="size-7 shrink-0" />
                  )}
                  <span className="grid min-w-0">
                    <span
                      title={naming.name}
                      className="truncate font-medium text-text"
                    >
                      {naming.name}
                    </span>
                    <span className="flex min-w-0 items-center gap-1.5 text-caption text-text-3">
                      {driver && TOOL_MARK[driver] ? (
                        <span
                          aria-hidden
                          className={cn(
                            'size-2 shrink-0 rounded-[2px]',
                            TOOL_MARK[driver],
                          )}
                        />
                      ) : null}
                      <span className="truncate">{meta}</span>
                    </span>
                  </span>
                </Link>
                <div
                  role="img"
                  aria-label={laneSentence(
                    lane,
                    [naming.name, meta].filter(Boolean).join(', '),
                    readByKey.get(s.key) ?? 'pending',
                    clock,
                    t,
                  )}
                  className="relative h-full overflow-hidden"
                >
                  <LanePlot lane={lane} now={now} clock={clock} />
                </div>
                <span className="justify-self-end pl-2">
                  {lane.state === 'waiting' ? (
                    // A run stays `running` while a tool call waits on a person (HU-R12):
                    // the row says what it waits for, not the process state.
                    <StatusChip
                      tone="warn"
                      className="whitespace-normal text-right"
                    >
                      {t(
                        lane.waits.some((w) => w.flagged)
                          ? 'swimlane.needsYou'
                          : 'swimlane.waiting',
                      )}
                    </StatusChip>
                  ) : run ? (
                    <RunStateBadge run={run} className="whitespace-nowrap" />
                  ) : s.live ? (
                    <CcStateBadge
                      state={s.live.cc_state}
                      className="whitespace-nowrap"
                    />
                  ) : null}
                </span>
              </li>
            )
          })}
        </ol>
      </div>

      <div className="mt-3 flex min-h-8 flex-wrap items-center gap-x-5 gap-y-2 text-caption text-text-3">
        {selected && range ? (
          <span className="flex flex-wrap items-center gap-2 text-text">
            <span id="swimlane-range-text" className="tabular-nums">
              {t('swimlane.range', {
                from: clock(Date.parse(selected.since)),
                to: clock(Date.parse(selected.until)),
              })}
            </span>
            <Button asChild size="sm" variant="outline">
              <Link
                ref={ledgerRef}
                to={'/audit' as never}
                search={selected as never}
                aria-describedby="swimlane-range-text"
              >
                {t('swimlane.openLedger')}
              </Link>
            </Button>
            <Button size="sm" variant="ghost" onClick={clear}>
              {t('swimlane.clear')}
            </Button>
          </span>
        ) : (
          <span>{t('swimlane.hint')}</span>
        )}
      </div>

      <ul
        className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-2 text-caption text-text-3"
        aria-label={t('swimlane.legend')}
      >
        <li className="inline-flex items-center gap-2">
          <span
            aria-hidden
            className="h-2.5 w-[22px] rounded-[3px]"
            style={{
              background: `linear-gradient(to right, ${tone(1)} 0 50%, ${tone(6)} 50% 100%)`,
            }}
          />
          {t('swimlane.legendRun')}
        </li>
        <li className="inline-flex items-center gap-2">
          <span
            aria-hidden
            className="h-2.5 w-[22px] rounded-[3px] border border-warn/50 bg-warn/40"
          />
          {t('swimlane.legendWait')}
        </li>
        <li className="inline-flex items-center gap-2">
          <span
            aria-hidden
            className="w-[22px] border-t-2 border-dotted border-text-3"
          />
          {t('swimlane.legendIdle')}
        </li>
        <li className="inline-flex items-center gap-2">
          <span aria-hidden className="h-3.5 w-[3px] rounded-[2px] bg-bad" />
          {t('swimlane.legendFailure')}
        </li>
      </ul>

      <details className="mt-3">
        <summary className="cursor-pointer text-caption font-medium text-text-3 hover:text-text">
          {t('swimlane.table')}
        </summary>
        <table className="mt-2 w-full text-caption">
          <thead className="text-left text-text-3">
            <tr>
              <th scope="col" className="py-1 pr-3 font-medium">
                {t('swimlane.colSession')}
              </th>
              <th scope="col" className="py-1 pr-3 font-medium">
                {t('swimlane.colActive')}
              </th>
              <th scope="col" className="py-1 pr-3 text-right font-medium">
                {t('swimlane.colCalls')}
              </th>
              <th scope="col" className="py-1 font-medium">
                {t('swimlane.colNow')}
              </th>
            </tr>
          </thead>
          <tbody>
            {lanes.map((lane) => {
              const naming = sessionNaming(lane.session, untitled, shared)
              const now_ = stateWords(lane, clock, t)
              return (
                <tr key={lane.session.key} className="border-t border-line">
                  <th
                    scope="row"
                    className="py-1 pr-3 text-left font-normal text-text"
                  >
                    {naming.name}
                  </th>
                  <td className="py-1 pr-3 tabular-nums">
                    {!lane.read
                      ? t(
                          readByKey.get(lane.session.key) === 'error'
                            ? 'swimlane.unread'
                            : 'swimlane.reading',
                        )
                      : lane.runs
                          .map((r) => runWords(r, clock, t))
                          .join(', ') || '—'}
                  </td>
                  <td className="py-1 pr-3 text-right tabular-nums">
                    {lane.read ? formatInt(lane.toolCalls) : '—'}
                  </td>
                  <td className="py-1">
                    {now_.join('; ') || t('swimlane.working')}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </details>
    </section>
  )
}
