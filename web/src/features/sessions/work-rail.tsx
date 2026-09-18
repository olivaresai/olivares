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
import { Pin } from 'lucide-react'
import {
  useCallback,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/ui/empty-state'
import { workLine } from '@/features/home/work-line'
import { RelTimeLabel, humanDurationSeconds } from '@/features/shared'
import { resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import { CcStateBadge } from './cc-state-badge'
import { RunStateBadge } from '@/features/agentops/run-state-badge'
import { primaryRun, sessionLabel, type UnifiedSession } from './provenance'
import { addressOf } from './session-address'
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
}: {
  session: UnifiedSession
  selected: boolean
  focused: boolean
  pinned: boolean
  onOpen: (session: UnifiedSession) => void
  registerRef: (address: string, el: HTMLDivElement | null) => void
}) {
  const { t } = useTranslation('sessions')
  const address = addressOf(session)
  const run = primaryRun(session.runs)
  // The clause comes from the OBSERVED half, through that same ladder. With no observed
  // half there is no sentence to tell — a launched run whose telemetry has not arrived
  // is a real state, and saying so beats printing its reference twice.
  const line = session.live ? workLine(session.live).text : null
  const seconds = session.live?.duration_seconds ?? 0

  return (
    <div
      ref={(el) => registerRef(address, el)}
      role="option"
      aria-selected={selected}
      tabIndex={focused ? 0 : -1}
      data-testid="rail-row"
      data-address={address}
      onClick={() => onOpen(session)}
      className={cn(
        'group relative flex cursor-pointer flex-col gap-1 border-l-2 px-3 py-2.5 outline-none transition-colors',
        selected
          ? 'border-l-accent bg-accent-soft'
          : 'border-l-transparent hover:bg-muted',
        'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset',
      )}
    >
      <div className="flex min-w-0 items-center gap-1.5">
        {pinned ? (
          <>
            {/* An `aria-label` on a bare <svg> is not reliably announced — the icon is
                hidden and the WORD is what joins the row's accessible name, which for a
                `role="option"` is composed from its contents. */}
            <Pin
              className="size-3 shrink-0 text-accent-text"
              aria-hidden="true"
            />
            <span className="sr-only">{t('rail.pinnedMark')}</span>{' '}
          </>
        ) : null}
        <span className="min-w-0 flex-1 truncate text-body font-medium text-foreground">
          {sessionLabel(session)}
        </span>
      </div>

      <div className="flex min-w-0 items-center gap-1.5">
        {session.live ? (
          <CcStateBadge state={session.live.cc_state} className="shrink-0" />
        ) : run ? (
          <RunStateBadge state={run.state} />
        ) : null}{' '}
        {/* ELAPSED, and only when the engine sent a duration. A zero printed as
            "0s" would read as "it did nothing", which is a different claim. */}
        {seconds > 0 ? (
          <span className="shrink-0 text-caption text-muted-foreground tabular-nums">
            {humanDurationSeconds(seconds)}
          </span>
        ) : null}{' '}
        {session.lastActivityMs > 0 ? (
          <RelTimeLabel
            ts={new Date(session.lastActivityMs).toISOString()}
            className="ml-auto shrink-0 text-caption text-muted-foreground"
          />
        ) : null}
      </div>

      <p
        className="truncate text-caption text-muted-foreground"
        title={line ?? undefined}
      >
        {line ?? t('rail.noObservation')}
      </p>
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
      if (row) onTogglePin(addressOf(row))
    }
  }

  if (!loading && sessions.length === 0) {
    return (
      <EmptyState
        icon={<Pin />}
        title={t('rail.emptyTitle')}
        description={t('rail.emptyDescription')}
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
      data-testid="work-rail"
      onKeyDown={onKeyDown}
      className="flex flex-col"
    >
      {groups.map((group) => (
        <div
          key={group.id}
          role="group"
          aria-labelledby={`rail-group-${group.id}`}
        >
          <p
            className="sticky top-0 z-10 bg-surface px-3 py-1.5 text-overline text-muted-foreground uppercase"
            id={`rail-group-${group.id}`}
          >
            {/* THE SPACE IS NOT DECORATION. This paragraph is the group's accessible
                name (`aria-labelledby`), and without it the name is the two run
                together — "Active0" — which is what the first live run's snapshot
                showed. */}
            {t(`rail.group.${group.id}`)}{' '}
            <span className="tabular-nums">{group.sessions.length}</span>
          </p>
          {group.sessions.length === 0 ? (
            // An empty section states its own sentence. A section that vanished when
            // it emptied would move the row under the operator's cursor.
            <p className="px-3 pb-2 text-caption text-muted-foreground">
              {t(`rail.groupEmpty.${group.id}`)}
            </p>
          ) : (
            group.sessions.map((s) => (
              <RailRow
                key={s.key}
                session={s}
                selected={addressOf(s) === selected}
                focused={addressOf(s) === focused}
                pinned={pinned.has(addressOf(s))}
                onOpen={onOpen}
                registerRef={registerRef}
              />
            ))
          )}
        </div>
      ))}
    </div>
  )
}

export { WORK_GROUPS }
