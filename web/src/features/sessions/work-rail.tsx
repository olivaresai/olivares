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
//    ⛔ EVERY ROW HAS ONE ACTIONS MENU (#2795), opened three ways: a right-click (the
//       browser's own menu said Back and Reload), the `⋯` beside the row, and Shift+F10
//       or the menu key on the focused row. The `⋯` is NOT a control inside the row, and
//       that is not a preference: `option` is children-presentational in ARIA, so a
//       focusable control inside one is ignored by assistive technology and reported by
//       axe (`nested-interactive`), and a first draft that put one in every row broke the
//       rail's single tab stop. It is a pointer shortcut beside the option, the way Open
//       beside is; the keyboard's path is the declared key.
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
import {
  ChevronDown,
  Columns2,
  MoreHorizontal,
  Pin,
  SquareTerminal,
} from 'lucide-react'
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
import { humanDurationSeconds } from '@/features/shared'
import { resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { usePreferencesStore, type Density } from '@/stores/preferences'
import { IconTip } from './icon-tip'
import {
  StateWord,
  useForgetTurnsOnLeave,
  useSeenTurns,
} from './session-state-word'
import { ToolMark } from './tool-mark'
import {
  primaryRun,
  sessionFolder,
  sessionNaming,
  sessionPlace,
  sessionTool,
  sharedNames,
  type UnifiedSession,
} from './provenance'
import { addressOf } from './session-address'
import { isPinnable } from './session-pins'
import {
  groupSessions,
  railOrder,
  WORK_GROUPS,
  type WorkGroup,
} from './session-groups'

export interface WorkRailProps {
  sessions: readonly UnifiedSession[]
  /** The address of the session on screen, or null. */
  selected: string | null
  onOpen: (session: UnifiedSession) => void
  onOpenBeside?: (session: UnifiedSession) => void
  pinned: ReadonlySet<string>
  /** Pin or unpin the session at this address; null offers no pin at all. */
  onTogglePin: ((address: string) => void) | null
  /** The items of a row's actions menu; absent, a row has no menu and a right-click is
   * the browser's. */
  rowMenu?: (session: UnifiedSession) => ReactNode
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
  compact: 'h-14 min-[761px]:h-11',
  comfortable: 'h-14 min-[761px]:h-12',
}

/**
 * A ROW: the tool's mark; the folder small above what
 * the session is called; at the right what it is doing in a word with its time. The full
 * path and what it is doing now are on the row's hover and in the thread it opens.
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
  menu,
  menuOpen,
  onMenuOpenChange,
  onMenuClosed,
  onFocused,
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
  /** The row's actions menu items; absent, the row has no menu. */
  menu?: (session: UnifiedSession) => ReactNode
  menuOpen: boolean
  onMenuOpenChange: (open: boolean) => void
  /** Where focus goes when the menu closes: back to this row, not to the `⋯`. */
  onMenuClosed: () => void
  /** The row took DOM focus, by a click or by the keyboard. */
  onFocused: () => void
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
  const folder = sessionFolder(session)
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
        onFocus={onFocused}
        onContextMenu={
          menu
            ? (e) => {
                e.preventDefault()
                onMenuOpenChange(true)
              }
            : undefined
        }
        /* ⛔ TWO SHORT LINES, BOTH WITH WORDS: the folder and the state above,
         the name below. The old second line was blank for every observed session
         (#2266); this one carries the place and what the session is doing, which is what
         a person picks a row BY. The name is cut with an ellipsis and its whole text
         (name, tail, reference) is on the name's hover.

         ⛔ SELECTED IS NEUTRAL. The orange marks the one action, the focus ring and the
         brand; "this is open" is a tonal fill, and the state is a word. */
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
          onOpenBeside && menu
            ? 'max-[760px]:pr-22 pointer-coarse:pr-22'
            : (onOpenBeside || menu) &&
                'max-[760px]:pr-12 pointer-coarse:pr-12',
        )}
      >
        <ToolMark tool={sessionTool(session)} />
        <span className="flex min-w-0 flex-1 flex-col justify-center gap-0.5">
          {/* The place above the name, small; the state and its time at
            its right. */}
          <span className="flex min-w-0 items-center gap-2">
            <span
              data-testid="rail-row-place"
              className="min-w-0 flex-1 truncate text-overline font-normal text-text-3"
            >
              {folder}
            </span>
            <span
              className={cn(
                'flex shrink-0 items-center gap-1.5',
                // Faded, not hidden: it keeps its place and the row's accessible name.
                // Hover only: a focused row is not asking for the button.
                (onOpenBeside || menu) &&
                  'min-[761px]:group-hover/row:opacity-0',
              )}
            >
              <StateWord session={session} />
            </span>
          </span>
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
            className={cn(
              'absolute top-1/2 -translate-y-1/2 opacity-0 group-hover/row:opacity-100 max-[760px]:opacity-100 pointer-coarse:opacity-100',
              menu ? 'right-11' : 'right-1',
            )}
          >
            <Columns2 aria-hidden="true" />
          </Button>
        </IconTip>
      ) : null}
      {menu ? (
        <DropdownMenu open={menuOpen} onOpenChange={onMenuOpenChange}>
          <IconTip label={t('rail.rowMenu', { name: naming.name })}>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t('rail.rowMenu', { name: naming.name })}
                // THE POINTER'S WAY TO THE MENU, beside the option and not in it (see the
                // head of this file): out of the tab order and the accessibility tree,
                // like Open beside. Painted on hover, while its menu is open, and always
                // on a phone or with a coarse pointer.
                aria-hidden="true"
                tabIndex={-1}
                data-testid="rail-row-menu"
                className="absolute top-1/2 right-1 -translate-y-1/2 opacity-0 group-hover/row:opacity-100 data-[state=open]:opacity-100 max-[760px]:opacity-100 pointer-coarse:opacity-100"
              >
                <MoreHorizontal aria-hidden="true" />
              </Button>
            </DropdownMenuTrigger>
          </IconTip>
          <DropdownMenuContent
            align="end"
            onCloseAutoFocus={(e) => {
              // The `⋯` is out of the tab order: focus returns to the row it acted on.
              e.preventDefault()
              onMenuClosed()
            }}
          >
            {menu(session)}
          </DropdownMenuContent>
        </DropdownMenu>
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
  rowMenu,
}: WorkRailProps) {
  const { t } = useTranslation('sessions')
  useForgetTurnsOnLeave()
  useSeenTurns(sessions)
  const groups = groupSessions(sessions, pinned)
  // THE SNOOZED SECTION IS FOLDED (#2480): a snooze takes the session out of the list
  // until its moment, and one toggle under the list keeps it reachable, so a snooze is
  // never a way to lose a session. Folded, its rows are not drawn and not walked.
  const [showSnoozed, setShowSnoozed] = useState(false)
  const snoozedCount =
    groups.find((group) => group.id === 'snoozed')?.sessions.length ?? 0
  // THE ARCHIVED SECTION FOLDS THE SAME WAY (#2795), under its own toggle: one rule for
  // every session that is out of the list, whoever put it there.
  const [showArchived, setShowArchived] = useState(false)
  const archivedCount =
    groups.find((group) => group.id === 'archived')?.sessions.length ?? 0
  // THE WORKING SECTION IS FOLDED TOO: a running session never moves a row
  // under the pointer, so the rows stay shut until asked. It arrives open when the session
  // on screen is one of them, so the open session is never hidden by its own list.
  const workingRows =
    groups.find((group) => group.id === 'active')?.sessions ?? []
  const [showWorking, setShowWorking] = useState(() =>
    workingRows.some((s) => addressOf(s) === selected),
  )
  const order = railOrder(groups, showSnoozed, showArchived, showWorking)
  const shared = sharedNames(sessions, t('untitled'))
  const rowRefs = useRef(new Map<string, HTMLDivElement>())

  // WHICH ROW THE KEYBOARD IS ON. It is NOT the selection: moving focus down a rail of
  // sessions must not open one per keystroke. It starts on the open session so a
  // deep-linked arrival can walk away from where it landed.
  const [focusedAddress, setFocusedAddress] = useState<string | null>(null)
  // The one row whose actions menu is open, if any.
  const [menuAddress, setMenuAddress] = useState<string | null>(null)
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
    // A row's open menu is portalled out of the rail's DOM but not out of its React
    // tree: its keys bubble here, and Enter on an item must not also open the session,
    // nor `p` typed to find an item pin it. Only keys pressed in the rail itself count.
    if (!e.currentTarget.contains(e.target as Node)) return
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
    if (command === 'rail.menu' && rowMenu) {
      e.preventDefault()
      const row = order[at]
      if (row) setMenuAddress(addressOf(row))
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

  // The sections drawn: a folded one only while it is open (Working has its own control).
  const visible = groups.filter(
    (group) =>
      group.sessions.length > 0 &&
      (group.id !== 'snoozed' || showSnoozed) &&
      (group.id !== 'archived' || showArchived),
  )
  const fold = visible.find((group) => group.id === 'active')
  const above = visible.filter(
    (group) => group.id === 'pinned' || group.id === 'attention',
  )
  const below = visible.filter(
    (group) => group !== fold && !above.includes(group),
  )
  const rowsOf = (list: readonly UnifiedSession[]) =>
    list.map((s) => (
      <RailRow
        key={s.key}
        session={s}
        selected={addressOf(s) === selected}
        focused={addressOf(s) === focused}
        pinned={pinned.has(s.key)}
        onOpen={onOpen}
        onOpenBeside={addressOf(s) !== selected ? onOpenBeside : undefined}
        registerRef={registerRef}
        shared={shared}
        menu={rowMenu}
        menuOpen={menuAddress === addressOf(s)}
        onMenuOpenChange={(open) => {
          if (open) setFocusedAddress(addressOf(s))
          setMenuAddress(open ? addressOf(s) : null)
        }}
        onMenuClosed={() => moveTo(addressOf(s))}
        // THE KEYBOARD'S ROW IS THE ONE THAT HAS FOCUS, however it got there:
        // a click on a row moves it too, so `p` and Shift+F10 act on that row.
        onFocused={() => setFocusedAddress(addressOf(s))}
      />
    ))
  const sections = (list: WorkGroup[]) =>
    list.map((group) => (
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
        {rowsOf(group.sessions)}
      </div>
    ))

  return (
    <>
      {/* Roles, not tags: the ARIA tree a listbox must have is `listbox > group >
        option`, and a group HEADING is a child of its group, never of the listbox.
        Wrapping each section in a presentational <li> put that heading directly under
        the listbox, where it is not an allowed child — a structure axe reads as broken
        and a screen reader reads as a stray line of text.

        ⛔ THE WORKING FOLD'S BUTTON IS NOT A CHILD OF A LISTBOX EITHER (axe,
        aria-required-children: a button under a listbox is unallowed, group or no
        group). It sits where the Working section belongs, between the listbox that holds
        Pinned and Needs you and the one that holds the rest, so the order a person reads
        is kept and each listbox owns options only. One wrapper owns the keys, so the
        arrow keys walk across all of them. With every session folded there is no option
        to own, and no listbox is drawn. */}
      {loading || visible.length > 0 ? (
        <div
          data-testid="work-rail"
          onKeyDown={onKeyDown}
          className="flex flex-col"
        >
          {above.length > 0 ? (
            <div
              role="listbox"
              aria-label={t('rail.label')}
              aria-busy={loading ? true : undefined}
            >
              {sections(above)}
            </div>
          ) : null}
          {fold ? (
            <div>
              <button
                type="button"
                id="rail-group-active"
                aria-expanded={showWorking}
                onClick={() => setShowWorking((shown) => !shown)}
                className="sticky top-0 z-10 flex w-full items-center gap-1 bg-canvas px-3 pt-4 pb-1 text-left text-overline font-medium text-text-3 outline-none hover:text-text-2 focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset"
              >
                <ChevronDown
                  aria-hidden="true"
                  className={cn(
                    'size-3 transition-transform',
                    !showWorking && '-rotate-90',
                  )}
                />
                {t('list.group.active')}{' '}
                <span className="tabular-nums">{fold.sessions.length}</span>
              </button>
              {showWorking ? (
                <div
                  role="listbox"
                  aria-labelledby="rail-group-active"
                  aria-busy={loading ? true : undefined}
                >
                  <div role="group" aria-labelledby="rail-group-active">
                    {rowsOf(fold.sessions)}
                  </div>
                </div>
              ) : null}
            </div>
          ) : null}
          {below.length > 0 || (loading && above.length === 0 && !fold) ? (
            // A listbox with no option yet is only a valid tree while it says it is busy.
            <div
              role="listbox"
              aria-label={t('rail.label')}
              aria-busy={loading ? true : undefined}
            >
              {sections(below)}
            </div>
          ) : null}
        </div>
      ) : null}
      {/* OUTSIDE the listbox: a listbox owns options, and a button in it would be a
        second kind of child and a second tab stop inside the rail's one. */}
      <FoldToggle
        label={t('list.group.snoozed')}
        count={snoozedCount}
        shown={showSnoozed}
        onToggle={() => setShowSnoozed((shown) => !shown)}
      />
      <FoldToggle
        label={t('list.group.archived')}
        count={archivedCount}
        shown={showArchived}
        onToggle={() => setShowArchived((shown) => !shown)}
      />
    </>
  )
}

/** The toggle under the rail that unfolds one section kept out of the list. */
function FoldToggle({
  label,
  count,
  shown,
  onToggle,
}: {
  label: string
  count: number
  shown: boolean
  onToggle: () => void
}) {
  if (count === 0) return null
  return (
    <button
      type="button"
      aria-expanded={shown}
      onClick={onToggle}
      className="flex items-center gap-1 px-3 pt-4 pb-2 text-left text-overline font-medium text-text-3 outline-none hover:text-text-2 focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset"
    >
      <ChevronDown
        aria-hidden="true"
        className={cn('size-3 transition-transform', !shown && '-rotate-90')}
      />
      {label} <span className="tabular-nums">{count}</span>
    </button>
  )
}

export { WORK_GROUPS }
