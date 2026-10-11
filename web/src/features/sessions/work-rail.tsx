// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE WORK RAIL — every session, grouped by what it needs from the operator.
//
// ⛔ NO GESTURE WITHOUT A KEYBOARD PATH, AND NO KEY WITHOUT A NAMED ONE. A review
//    rejected drag-and-drop as the way to change state for this reason, and the rule is
//    general: `p` pins the focused row, and the SAME action is offered in words, with
//    this key beside it, in the narrative pane's menu — which also names the action
//    before it happens (Pin / Unpin), the other half of the same rule.
//
//    ⛔ THE MENU IS NOT ON THE ROW, and that is not a preference. `option` is
//       children-presentational in ARIA: a focusable control inside one is ignored by
//       assistive technology and reported by axe (`nested-interactive`). A first draft
//       put a `⋯` trigger in every row; it also broke the rail's single tab stop the
//       moment the roving index was not on that row — measured here, in this file's
//       own keyboard test, before the menu moved.
//
// ⛔ ARROWS MOVE, ENTER OPENS. Moving focus is not choosing: an operator walking the
//    rail with the keyboard must be able to read the labels without firing a
//    navigation per keystroke, which on this screen would be a request per keystroke.
//    That is the `aria-selected` / focus split a listbox is FOR, so the rail is one:
//    `role="listbox"` with roving tabindex, one tab stop for the whole rail.
//
// ⛔ AND A ROW SAYS WHAT IT IS DOING, NOT WHAT IT IS. `workLine` is the front door's ladder —
//    summary, then goal, then the action and resource the connector reported, then the
//    session's own reference — and it is imported rather than re-derived, so the rail
//    and the front door can never tell the same session two different ways.
import { Pin, SquareTerminal } from 'lucide-react'
import {
  useCallback,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { runFolder } from './folder'
import { EmptyState } from '@/components/ui/empty-state'
import { workLine } from '@/features/home/work-line'
import { RelTimeLabel, humanDurationSeconds } from '@/features/shared'
import { resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import { toolName } from '@/features/agentops/tool-names'
import { StateDot } from './session-state-dot'
import {
  primaryRun,
  sessionNaming,
  sharedNames,
  type UnifiedSession,
} from './provenance'
import { addressOf } from './session-address'
import { pinnedAddress } from './session-pins'
import { WorkClause } from './work-clause'
import { groupSessions, railOrder, WORK_GROUPS } from './session-groups'

export interface WorkRailProps {
  sessions: readonly UnifiedSession[]
  /** The address of the session on screen, or null. */
  selected: string | null
  onOpen: (session: UnifiedSession) => void
  pinned: ReadonlySet<string>
  /** null when this principal has nowhere to store a preference — then no pin is offered. */
  onTogglePin: ((address: string) => void) | null
  /** Rendered when the estate has no sessions at all. One action, already authorized. */
  emptyAction?: ReactNode
  loading?: boolean
}

/** The tool a session runs, by its product name; null when nothing declared one. */
function toolLabel(session: UnifiedSession): string | null {
  const run = primaryRun(session.runs)
  const driver = run?.provider_driver || session.live?.provider
  return driver ? toolName(driver) : null
}

/**
 * ONE ROW: what it is called, what state it is in, how long it has been at it, and
 * what it is doing now — in one clause.
 */
function RailRow({
  session,
  selected,
  focused,
  pinned,
  onOpen,
  registerRef,
  shared,
}: {
  session: UnifiedSession
  selected: boolean
  focused: boolean
  pinned: boolean
  onOpen: (session: UnifiedSession) => void
  registerRef: (address: string, el: HTMLDivElement | null) => void
  /** Names another row of this rail also carries: those rows show their tail. */
  shared: ReadonlySet<string>
}) {
  const { t } = useTranslation('sessions')
  const address = addressOf(session)
  const run = primaryRun(session.runs)
  const folder = runFolder(run)
  // The clause comes from the OBSERVED half, through that same ladder. With no observed
  // half there is no sentence to tell — a launched run whose telemetry has not arrived
  // is a real state, and saying so beats printing its reference twice.
  const line = session.live ? workLine(session.live).text : null
  const seconds = session.live?.duration_seconds ?? 0
  // ⛔ THE ROW IS CALLED WHAT IT IS DOING, AND IT USED TO BE CALLED BY ITS REFERENCE.
  //    `sessionLabel`'s second rung is `s.sessionRef`, so a rail of six discovered
  //    sessions read `sess-coder-7a3f`, `sess-batch-1` … — six machine ids where the
  //    operator has to choose one. `sessionNaming` is the front door's ladder; when it
  //    has no sentence the row says so and keeps the distinguishing tail beside it.
  const naming = sessionNaming(session, t('untitled'), shared)
  // Name, tail and WHOLE reference on one hover, the way the front door's rows do it:
  // a truncated name stays readable and the identifier stays reachable from the row
  // that no longer paints it. Duplicates are dropped — an untitled row whose tail IS
  // its reference would otherwise say it twice.
  const nameTitle = [naming.name, naming.shortId, naming.reference]
    .filter((part, i, all) => part && all.indexOf(part) === i)
    .join(' · ')
  // The hover text: the elapsed time and the observed clause, i.e. exactly the two facts
  // the row used to spend two extra lines on. `undefined` and not an empty string when
  // there is neither — a tooltip that says nothing is worse than no tooltip.
  const rowTitle =
    [seconds > 0 ? humanDurationSeconds(seconds) : null, line]
      .filter(Boolean)
      .join(' · ') || undefined

  const tool = toolLabel(session)
  const folderPart = folder ? (
    <span title={run?.workspace_path} data-testid="rail-row-folder">
      {folder}
    </span>
  ) : null

  return (
    <div
      ref={(el) => registerRef(address, el)}
      role="option"
      aria-selected={selected}
      tabIndex={focused ? 0 : -1}
      data-testid="rail-row"
      data-address={address}
      onClick={() => onOpen(session)}
      /* ⛔ A ROW IS 52 px: tool icon, a one-line title, a second line, and on the right the
         state as a dot and the time as a relative figure. The title is cut with an
         ellipsis and its whole text (name, tail, reference) is on the name's hover; the
         elapsed duration and the observed clause are on the row's own hover and are
         painted in full by the thread this row opens. What stays on the row is what a
         person picks a row BY. The dot has no width to lose in German, which is what the
         old 146 px state badge did to the name.

         ⛔ SELECTED IS NEUTRAL. The orange marks the one action, the focus ring and the
         brand; "this is open" is a tonal fill, and "working" is the dot. */
      title={rowTitle}
      className={cn(
        'group relative flex h-14 cursor-pointer min-[761px]:h-[52px] items-center gap-2.5 px-3 outline-none transition-colors',
        selected ? 'bg-active' : 'hover:bg-hover',
        'focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
      )}
    >
      <SquareTerminal
        aria-hidden="true"
        className="size-4 shrink-0 text-text-3"
      />
      <span className="flex min-w-0 flex-1 flex-col justify-center">
        <span
          data-testid="rail-row-name"
          title={nameTitle}
          className="min-w-0 truncate text-body font-medium text-text"
        >
          {pinned ? (
            <>
              {/* An `aria-label` on a bare <svg> is not reliably announced: the icon is
                  hidden and the WORD joins the row's accessible name, which for a
                  `role="option"` is composed from its contents. A pin is a state the
                  operator put the row in, so it is painted in the muted register: the
                  accent is not a second meaning in a 300 px list. */}
              <Pin
                className="mr-1 inline size-3 align-[-1px] text-muted-foreground"
                aria-hidden="true"
              />
              <span className="sr-only">{t('rail.pinnedMark')}</span>{' '}
            </>
          ) : null}
          <WorkClause text={naming.name} live={session.live} />
          {naming.shortId ? (
            <span className="font-mono text-caption text-text-3">
              {' '}
              {naming.shortId}
            </span>
          ) : null}
        </span>
        {tool || folderPart ? (
          <span
            data-testid="rail-row-meta"
            className="min-w-0 truncate text-overline font-normal text-text-3"
          >
            {tool}
            {tool && folderPart ? ' · ' : null}
            {folderPart}
          </span>
        ) : null}
      </span>
      <span className="flex shrink-0 items-center gap-1.5">
        <StateDot session={session} />
        {session.lastActivityMs > 0 ? (
          <RelTimeLabel
            ts={new Date(session.lastActivityMs).toISOString()}
            className="shrink-0 whitespace-nowrap text-overline font-normal tabular-nums text-text-3"
          />
        ) : null}
      </span>
    </div>
  )
}

export function WorkRail({
  sessions,
  selected,
  onOpen,
  pinned,
  onTogglePin,
  emptyAction,
  loading,
}: WorkRailProps) {
  const { t } = useTranslation('sessions')
  const groups = groupSessions(sessions, pinned)
  const order = railOrder(groups)
  const shared = sharedNames(sessions, t('untitled'))
  const rowRefs = useRef(new Map<string, HTMLDivElement>())

  // WHICH ROW THE KEYBOARD IS ON. It is NOT the selection: moving focus down a rail of
  // sessions must not open one per keystroke. It starts on the open session so a
  // deep-linked arrival can walk away from where it landed.
  const [focusedAddress, setFocusedAddress] = useState<string | null>(null)
  const focused =
    (focusedAddress && order.some((s) => addressOf(s) === focusedAddress)
      ? focusedAddress
      : null) ??
    (selected && order.some((s) => addressOf(s) === selected)
      ? selected
      : (order[0] && addressOf(order[0])) || null)

  const registerRef = useCallback(
    (address: string, el: HTMLDivElement | null) => {
      if (el) rowRefs.current.set(address, el)
      else rowRefs.current.delete(address)
    },
    [],
  )

  const moveTo = useCallback((address: string) => {
    setFocusedAddress(address)
    rowRefs.current.get(address)?.focus()
  }, [])

  /**
   * EVERY KEY HERE COMES FROM THE DECLARED TABLE, not from this switch. The table is
   * what the documentation page prints and what `model.test.ts` enumerates; a key
   * matched by hand here would be a binding nobody could find and nothing could check.
   * The context is `railFocused`, which is true because this handler only runs when the
   * rail has focus — and an unknown context key is false, so these rows cannot fire
   * anywhere else.
   */
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (order.length === 0) return
    const command = resolveBinding(KEYBINDINGS, e, { railFocused: true })
    if (!command) return
    const index = order.findIndex((s) => addressOf(s) === focused)
    const at = index === -1 ? 0 : index

    if (command === 'rail.next' || command === 'rail.previous') {
      e.preventDefault()
      const next = command === 'rail.next' ? at + 1 : at - 1
      // No wrap: a rail that jumps from the last row to the first hides the fact that
      // the operator reached the end of the estate.
      if (next < 0 || next >= order.length) return
      moveTo(addressOf(order[next]))
      return
    }
    if (command === 'rail.first' || command === 'rail.last') {
      e.preventDefault()
      moveTo(addressOf(order[command === 'rail.first' ? 0 : order.length - 1]))
      return
    }
    if (command === 'rail.open') {
      e.preventDefault()
      const row = order[at]
      if (row) onOpen(row)
      return
    }
    if (command === 'rail.pin' && onTogglePin) {
      e.preventDefault()
      const row = order[at]
      if (row) onTogglePin(pinnedAddress(pinned, row) ?? addressOf(row))
    }
  }

  if (!loading && sessions.length === 0) {
    return (
      <EmptyState
        icon={<SquareTerminal />}
        title={t('empty.title')}
        description={t('empty.description')}
        action={emptyAction}
      />
    )
  }

  return (
    // Roles, not tags: the ARIA tree a listbox must have is `listbox > group >
    // option`, and a group HEADING is a child of its group, never of the listbox.
    // Wrapping each section in a presentational <li> put that heading directly under
    // the listbox, where it is not an allowed child — a structure axe reads as broken
    // and a screen reader reads as a stray line of text.
    <div
      role="listbox"
      aria-label={t('rail.label')}
      // A listbox with no option yet is only a valid tree while it says it is busy.
      aria-busy={loading ? true : undefined}
      data-testid="work-rail"
      onKeyDown={onKeyDown}
      className="flex flex-col"
    >
      {groups
        .filter((group) => group.sessions.length > 0)
        .map((group) => (
          <div
            key={group.id}
            role="group"
            aria-labelledby={`rail-group-${group.id}`}
          >
            <p
              className="sticky top-0 z-10 bg-canvas px-3 pt-4 pb-1 text-overline font-medium text-text-3"
              id={`rail-group-${group.id}`}
            >
              {/* THE SPACE IS NOT DECORATION. This paragraph is the group's accessible
                  name (`aria-labelledby`), and without it the name is the two run
                  together: "Working1". A group with no rows is not drawn, and no
                  sentence stands in for it. */}
              {t(`list.group.${group.id}`)}{' '}
              <span className="tabular-nums">{group.sessions.length}</span>
            </p>
            {group.sessions.map((s) => (
              <RailRow
                key={s.key}
                session={s}
                selected={addressOf(s) === selected}
                focused={addressOf(s) === focused}
                pinned={pinnedAddress(pinned, s) !== undefined}
                onOpen={onOpen}
                registerRef={registerRef}
                shared={shared}
              />
            ))}
          </div>
        ))}
    </div>
  )
}

export { WORK_GROUPS }
