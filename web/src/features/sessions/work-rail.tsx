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
//    The pointer's Open beside button is not in that tab order or that tree.
//
// ⛔ AND A ROW IS CALLED BY WHAT IT IS, NOT BY ITS LAST FILE. `sessionNaming` is the list's
//    one policy over the front door's ladder — the operator's name, then the summary or
//    goal the engine sent, then the session's tool and folder, then "untitled" with the
//    distinguishing tail — and it is imported rather than re-derived, so the rail, the
//    table and the sidebar can never tell the same session two different ways. The action
//    and resource a connector reported are the secondary fact: on the row's hover and in
//    the thread, never the title (#2266).
import { Columns2, Pin, SquareTerminal } from 'lucide-react'
import {
  useCallback,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/ui/empty-state'
import { runFolder } from './folder'
import { workLine } from '@/features/home/work-line'
import { RelTimeLabel, humanDurationSeconds } from '@/features/shared'
import { resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { usePreferencesStore, type Density } from '@/stores/preferences'
import { IconTip } from './icon-tip'
import { StateDot } from './session-state-dot'
import {
  primaryRun,
  sessionNaming,
  sessionPlace,
  sharedNames,
  type UnifiedSession,
} from './provenance'
import { addressOf } from './session-address'
import { isPinnable } from './session-pins'
import { groupSessions, railOrder, WORK_GROUPS } from './session-groups'

export interface WorkRailProps {
  sessions: readonly UnifiedSession[]
  /** The address of the session on screen, or null. */
  selected: string | null
  onOpen: (session: UnifiedSession) => void
  onOpenBeside?: (session: UnifiedSession) => void
  pinned: ReadonlySet<string>
  /** Pin or unpin the session at this address; null offers no pin at all. */
  onTogglePin: ((address: string) => void) | null
  /** Rendered when the estate has no sessions at all. One action, already authorized. */
  emptyAction?: ReactNode
  loading?: boolean
}

/**
 * THE LIST'S ONE ROW HEIGHT: the `h-*` token of `DENSITY_ROW` for the operator's density
 * (stores/preferences.ts; work-rail.test.tsx pins the two together), from the shell's
 * breakpoint up. Below it the row keeps a 44 px touch target. A literal per density so
 * the class is in the stylesheet.
 */
const ROW_HEIGHT: Record<Density, string> = {
  compact: 'h-11 min-[761px]:h-8',
  comfortable: 'h-11 min-[761px]:h-9',
}

/**
 * ONE ROW, ONE LINE: tool icon, what the session is called, and on the right its state
 * as a dot and the time as a relative figure. The tool, the folder, how long it has been
 * at it and what it is doing now are on the row's hover and in the thread it opens.
 */
function RailRow({
  session,
  selected,
  focused,
  pinned,
  onOpen,
  registerRef,
  shared,
  onOpenBeside,
}: {
  session: UnifiedSession
  selected: boolean
  focused: boolean
  pinned: boolean
  onOpen: (session: UnifiedSession) => void
  registerRef: (address: string, el: HTMLDivElement | null) => void
  /** Names another row of this rail also carries: those rows show their tail. */
  shared: ReadonlySet<string>
  onOpenBeside?: (session: UnifiedSession) => void
}) {
  const { t } = useTranslation('sessions')
  const density = usePreferencesStore((s) => s.density)
  const address = addressOf(session)
  const run = primaryRun(session.runs)
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
  // The hover text: the tool and folder when the name is not already them, the folder's
  // full path, the elapsed time and the observed clause — the facts the row used to spend
  // a second line on, which was blank for every observed session. `undefined` and not an
  // empty string when there is none: a tooltip that says nothing is worse than no tooltip.
  const place = sessionPlace(session)
  const rowTitle =
    [
      place && place !== naming.name ? place : null,
      // The folder Olivares made for the run is named by the run's id and is no place
      // an operator chose (`runFolder`): its path is not painted either.
      runFolder(run) ? run?.workspace_path : null,
      seconds > 0 ? humanDurationSeconds(seconds) : null,
      line,
    ]
      .filter(Boolean)
      .join(' · ') || undefined

  return (
    <div className="group/row relative">
      <div
        ref={(el) => registerRef(address, el)}
        role="option"
        aria-selected={selected}
        tabIndex={focused ? 0 : -1}
        data-testid="rail-row"
        data-address={address}
        onClick={() => onOpen(session)}
        /* ⛔ A ROW IS ONE LINE AT THE LIST'S STANDARD ROW HEIGHT: tool icon, the name, and
         on the right the state as a dot and the time as a relative figure. It was a
         52 px row with a second line that was blank for every observed session (#2266:
         half the screen empty, fifty rows of the same shape). The name is cut with an
         ellipsis and its whole text (name, tail, reference) is on the name's hover; the
         tool, folder, elapsed duration and observed clause are on the row's own hover and
         are painted in full by the thread this row opens. What stays on the row is what a
         person picks a row BY. The dot has no width to lose in German, which is what the
         old 146 px state badge did to the name.

         ⛔ SELECTED IS NEUTRAL. The orange marks the one action, the focus ring and the
         brand; "this is open" is a tonal fill, and "working" is the dot. */
        title={rowTitle}
        className={cn(
          'group relative flex cursor-pointer items-center gap-2.5 px-3 outline-none transition-colors',
          ROW_HEIGHT[density],
          selected ? 'bg-active' : 'hover:bg-hover',
          'focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
          // The pointer's Open beside button takes the time's place on hover (below), so the
          // name keeps the room: reserving 48 px for it on every row left a title 103 px.
          // On a phone, or with a coarse pointer (a tablet has no hover to reveal it), the
          // button is always painted and keeps its own column.
          onOpenBeside && 'max-[760px]:pr-12 pointer-coarse:pr-12',
        )}
      >
        <SquareTerminal
          aria-hidden="true"
          className="size-4 shrink-0 text-text-3"
        />
        <span className="flex min-w-0 flex-1 items-center">
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
                  accent is not a second meaning in a 360 px list. */}
                <Pin
                  className="mr-1 inline size-3 align-[-1px] text-muted-foreground"
                  aria-hidden="true"
                />
                <span className="sr-only">{t('rail.pinnedMark')}</span>{' '}
              </>
            ) : null}
            {naming.name}
            {naming.shortId ? (
              <span className="font-mono text-caption text-text-3">
                {' '}
                {naming.shortId}
              </span>
            ) : null}
          </span>
        </span>
        <span className="flex shrink-0 items-center gap-1.5">
          <StateDot session={session} />
          {session.lastActivityMs > 0 ? (
            <RelTimeLabel
              ts={new Date(session.lastActivityMs).toISOString()}
              className={cn(
                'shrink-0 whitespace-nowrap text-overline font-normal tabular-nums text-text-3',
                // Faded, not hidden: it keeps its place in the layout and in the row's
                // accessible name. Hover only: a focused row is not asking for the button.
                onOpenBeside && 'min-[761px]:group-hover/row:opacity-0',
              )}
            />
          ) : null}
        </span>
      </div>
      {onOpenBeside ? (
        <IconTip label={t('surface.openBeside')}>
          <Button
            variant="ghost"
            size="icon"
            aria-label={t('surface.openBeside')}
            // A POINTER SHORTCUT, not a second control in the listbox: a listbox owns
            // options, and a button per row would add a tab stop per row. Keyboard and
            // screen-reader users have the declared command (Alt+Shift+O on the row)
            // and the palette; this stays for the mouse and the touch screen.
            // Painted on hover (always on a phone or with a coarse pointer, which has no
            // hover) and never on focus: a clicked or arrowed-to row is focus-within with
            // the pointer elsewhere, and the button sits over the time's end, which only a
            // hover fades out.
            aria-hidden="true"
            tabIndex={-1}
            onClick={() => onOpenBeside(session)}
            className="absolute top-1/2 right-1 -translate-y-1/2 opacity-0 group-hover/row:opacity-100 max-[760px]:opacity-100 pointer-coarse:opacity-100"
          >
            <Columns2 aria-hidden="true" />
          </Button>
        </IconTip>
      ) : null}
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
  onOpenBeside,
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
    // Row action buttons keep their native Enter/Space activation.
    if (e.target instanceof Element && e.target.closest('button')) return
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
      // A session with no run has nothing to keep a pin on (`isPinnable`): not offered.
      if (row && isPinnable(row)) onTogglePin(addressOf(row))
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
                pinned={pinned.has(s.key)}
                onOpen={onOpen}
                onOpenBeside={
                  addressOf(s) !== selected ? onOpenBeside : undefined
                }
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
