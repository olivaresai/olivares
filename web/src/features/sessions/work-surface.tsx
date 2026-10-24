// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ArrowLeft, PanelRight, X } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { WorkComposer } from '@/components/layout/work-composer'
import { COMPOSER_INPUT_ID } from '@/components/layout/work-composer-id'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { PaneResizeHandle } from '@/components/ui/pane-resize'
import { usePaneVars } from '@/components/ui/pane-vars'
import { isTypingTarget, resolveBinding } from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import type { ConversationItem } from './conversation-frames'
import {
  hostIsolationUnavailable,
  operatorName,
  primaryRun,
  sessionShortId,
  type UnifiedSession,
} from './provenance'
import {
  addressOf,
  WORK_PANES,
  type EvidenceBlock,
  type SessionAddress,
  type WorkPane,
} from './session-address'
import { SessionContextPane } from './session-context-pane'
import { useSessionMenu } from './session-menu'
import { isPinnable, isPinned, preferenceStorageKey } from './session-pins'
import { useAuth } from '@/lib/auth/context'
import { IconTip } from './icon-tip'
import { FILTER_FROM, filterSessions } from './session-filter'
import { groupOf } from './session-groups'
import { SessionNarrative } from './session-narrative'
import { BESIDE_PANE, CONTEXT_PANE, LIST_PANE } from './surface-resize'
import type { SessionResolution } from './use-session-resolution'
import { WorkRail } from './work-rail'
import './i18n'

export interface WorkSurfaceProps {
  sessions: readonly UnifiedSession[]
  loading: boolean
  address: SessionAddress
  resolution: SessionResolution
  besideResolution?: SessionResolution
  onOpenBeside?: (session: UnifiedSession) => void
  onCloseBeside?: () => void
  onOpenBesideDetail?: () => void
  pinned: ReadonlySet<string>
  onTogglePin: ((address: string, session?: UnifiedSession) => void) | null
  onOpen: (session: UnifiedSession) => void
  onPane: (pane: WorkPane) => void
  onEvidence: (block: EvidenceBlock) => void
  onOpenDetail: () => void

  /** The one action of an empty estate, already gated by the caller. */
  emptyAction?: React.ReactNode
  /** The side pane's tab was chosen (`?panel=`). */
  onPanel?: (panel: string) => void
  /** The 48 px head of the list column: name, count, New session and the views menu. */
  listHeader?: React.ReactNode
  /** The foot of the list: what it does not show, and Load more (dev#3120). */
  listFooter?: React.ReactNode
}

const paneId = (pane: WorkPane) => `work-pane-${pane}`

function focusColumn(column: HTMLElement | null) {
  const field = column?.querySelector<HTMLTextAreaElement>('textarea')
  ;(field && !field.disabled ? field : column)?.focus()
}

/** The name under which this browser remembers, per person, whether the side pane is open. */
const PANEL_OPEN_KEY = 'olivares.sessions.sidePane'

/** Whether this person left the side pane open. CLOSED BY DEFAULT at every width
 * (dev#3162): the conversation is the page, and the pane opens on demand. A
 * principal with no partition (a token) is never remembered. */
function storedPanelOpen(key: string | null): boolean {
  if (!key) return false
  try {
    // A person with no choice yet keeps the one this browser saved for everyone before.
    const stored =
      window.localStorage.getItem(key) ??
      window.localStorage.getItem(PANEL_OPEN_KEY)
    return stored === 'open'
  } catch {
    // A browser that refuses storage keeps the default.
    return false
  }
}

function rememberPanelOpen(key: string | null, open: boolean) {
  if (!key) return
  try {
    window.localStorage.setItem(key, open ? 'open' : 'closed')
  } catch {
    // Not remembered: the pane still opens and closes.
  }
}

/**
 * WHERE THE SIDE PANE SITS BESIDE THE CONVERSATION: only where the conversation keeps its
 * 760 px (dev#3162). One column needs the 360 px list, 760 px and the 320 px pane: 1441 px;
 * two columns need 2 × 760 px: 2201 px. Narrower, the open pane takes the conversation's
 * place instead of squeezing it, and the list stays beside it; on a phone (below 761 px) it
 * is the one pane in front.
 */
const BESIDE = {
  one: {
    grid: 'min-[1441px]:grid-cols-[min(var(--list-w),35%)_minmax(0,1fr)_min(var(--ctx-w),35%)]',
    toggle: 'min-[1441px]:inline-flex',
    narrowButton: 'min-[1441px]:hidden',
    column: 'min-[1441px]:flex',
    pane: 'max-[760px]:col-span-full min-[1441px]:border-l',
    paneOpen: 'min-[1441px]:block',
    paneClosed: 'min-[1441px]:hidden',
    back: 'min-[1441px]:hidden',
    close: 'max-[1440px]:hidden',
  },
  two: {
    grid: 'min-[2201px]:grid-cols-[min(var(--list-w),35%)_minmax(0,1fr)_min(var(--ctx-w),35%)]',
    toggle: 'min-[2201px]:inline-flex',
    narrowButton: 'min-[2201px]:hidden',
    column: 'min-[2201px]:flex',
    pane: 'max-[760px]:col-span-full min-[2201px]:border-l',
    paneOpen: 'min-[2201px]:block',
    paneClosed: 'min-[2201px]:hidden',
    back: 'min-[2201px]:hidden',
    close: 'max-[2200px]:hidden',
  },
} as const

export function WorkSurface({
  sessions,
  loading,
  address,
  resolution,
  besideResolution,
  onOpenBeside,
  onCloseBeside,
  onOpenBesideDetail,
  pinned,
  onTogglePin,
  onOpen,
  onPane,
  onEvidence,
  onOpenDetail,
  emptyAction,
  listHeader,
  listFooter,
  onPanel,
}: WorkSurfaceProps) {
  const { t } = useTranslation('sessions')
  const selected = address.address
  const split = !!address.beside && !!besideResolution?.target
  const primaryColumn = useRef<HTMLElement>(null)
  const besideColumn = useRef<HTMLElement>(null)
  // The grids and panes the resize handles measure; each pane's stored width reaches its
  // grid as a custom property (the stylesheet default applies until one is stored).
  const surfaceGrid = useRef<HTMLDivElement>(null)
  const threadsGrid = useRef<HTMLDivElement>(null)
  const contextColumn = useRef<HTMLElement>(null)
  const surfaceVars = usePaneVars(LIST_PANE, CONTEXT_PANE)
  const threadsVars = usePaneVars(BESIDE_PANE)
  const focusedColumn = address.pane === 'beside' ? 'beside' : 'narrative'
  const [inspected, setInspected] = useState<ConversationItem | null>(null)
  const [frameCwd, setFrameCwd] = useState<string | null>(null)
  const closeBeside = useCallback(() => {
    onCloseBeside?.()
    requestAnimationFrame(() => focusColumn(primaryColumn.current))
  }, [onCloseBeside])

  // Keep all panes mounted; the URL selects the visible pane on narrow screens.
  // THE SIDE PANE IS CLOSED BY DEFAULT (dev#3162); once a person has opened or closed it,
  // that choice is remembered for that person in this browser. The address naming it
  // (`?pane=context`) opens it either way.
  const { principal, activeTenant } = useAuth()
  const panelKey =
    typeof window === 'undefined'
      ? null
      : preferenceStorageKey(
          PANEL_OPEN_KEY,
          window.location.origin,
          principal ?? null,
          activeTenant ?? null,
        )
  const [contextOpen, setContextOpenState] = useState(
    () => address.pane === 'context' || storedPanelOpen(panelKey),
  )
  // Another person or organization: read that person's choice, never carry the last one.
  const [seenPanelKey, setSeenPanelKey] = useState(panelKey)
  if (seenPanelKey !== panelKey) {
    setSeenPanelKey(panelKey)
    setContextOpenState(address.pane === 'context' || storedPanelOpen(panelKey))
  }
  const setContextOpen = (open: boolean | ((was: boolean) => boolean)) =>
    setContextOpenState((was) => {
      const next = typeof open === 'function' ? open(was) : open
      rememberPanelOpen(panelKey, next)
      return next
    })
  const beside = split ? BESIDE.two : BESIDE.one
  const [seenPane, setSeenPane] = useState(address.pane)
  if (seenPane !== address.pane) {
    setSeenPane(address.pane)
    if (address.pane === 'context') setContextOpenState(true)
  }
  useEffect(() => {
    if (!contextOpen) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented) return
      if (isTypingTarget(e.target)) return
      // An open dialog or menu owns this Escape: it closes that, not the pane.
      if (
        document.querySelector(
          '[role="dialog"][data-state="open"], [role="menu"][data-state="open"]',
        )
      )
        return
      // A pane closed by Escape is not a choice to remember: the stored state stays.
      setContextOpenState(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [contextOpen])
  // CONTEXT: an icon in the thread's header. From `xl` up it toggles the pane beside the
  // thread; between 761 px and `xl` there is no room beside it, so the same icon brings the
  // pane in front (`?pane=context`), and the pane's own back arrow returns to the thread.
  const contextToggle = (
    <IconTip label={t('surface.pane.context')}>
      <Button
        variant="ghost"
        size="icon"
        aria-pressed={contextOpen}
        aria-controls={paneId('context')}
        aria-label={t('surface.pane.context')}
        data-testid="context-toggle"
        onClick={() => setContextOpen((open) => !open)}
        className={cn('hidden aria-pressed:bg-active', beside.toggle)}
      >
        <PanelRight />
      </Button>
    </IconTip>
  )
  const contextPaneButton = (
    <IconTip label={t('surface.pane.context')}>
      <Button
        variant="ghost"
        size="icon"
        aria-controls={paneId('context')}
        aria-label={t('surface.pane.context')}
        data-testid="pane-button-context"
        onClick={() => onPane('context')}
        className={cn('max-[760px]:hidden', beside.narrowButton)}
      >
        <PanelRight />
      </Button>
    </IconTip>
  )
  // BACK TO THE LIST, below 761 px, where the list and the thread are one screen each.
  const back = (
    <IconTip label={t('surface.backTo', { pane: t('surface.pane.rail') })}>
      <Button
        variant="ghost"
        size="icon"
        aria-controls={paneId('rail')}
        aria-label={t('surface.backTo', { pane: t('surface.pane.rail') })}
        data-testid="pane-button-rail"
        onClick={() => onPane('rail')}
        className="-ml-1 min-[761px]:hidden"
      >
        <ArrowLeft />
      </Button>
    </IconTip>
  )

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (
        e.defaultPrevented ||
        document.querySelector(
          '[role="dialog"][data-state="open"], [role="menu"][data-state="open"]',
        )
      )
        return
      const command = resolveBinding(KEYBINDINGS, e, { workSurface: true })
      if (command === 'surface.focusBeside' && split) {
        e.preventDefault()
        const next = primaryColumn.current?.contains(document.activeElement)
          ? 'beside'
          : 'narrative'
        onPane(next)
        const column =
          next === 'beside' ? besideColumn.current : primaryColumn.current
        // The narrow-screen column becomes visible after the URL navigation.
        requestAnimationFrame(() => focusColumn(column))
        // A visible desktop column can take focus immediately.
        focusColumn(column)
        return
      }
      if (command === 'surface.closeBeside' && split && onCloseBeside) {
        e.preventDefault()
        closeBeside()
        return
      }
      if (isTypingTarget(e.target)) return
      if (command === 'surface.openBeside' && onOpenBeside && selected) {
        const focusedAddress = (document.activeElement as HTMLElement | null)
          ?.closest('[data-address]')
          ?.getAttribute('data-address')
        // The command names the row the person is on. Anywhere else it does nothing:
        // it never picks a session they did not point at.
        const row = sessions.find(
          (s) => s.key === focusedAddress && s.key !== selected,
        )
        if (row) {
          e.preventDefault()
          onOpenBeside(row)
        }
        return
      }
      if (command !== 'surface.nextPane' && command !== 'surface.previousPane')
        return
      e.preventDefault()
      const panes: readonly WorkPane[] = split
        ? ['rail', 'narrative', 'beside', 'context']
        : WORK_PANES
      const at = panes.indexOf(address.pane)
      const step = command === 'surface.nextPane' ? 1 : -1
      const next = panes[(at + step + panes.length) % panes.length]
      onPane(next)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [
    address.pane,
    onPane,
    split,
    onCloseBeside,
    closeBeside,
    onOpenBeside,
    selected,
    sessions,
  ])

  // THE FILTER FIELD appears when the list is long enough to need it, and narrows what the
  // rows show: by name, tool or folder, in this browser, with no request.
  const [query, setQuery] = useState('')
  const showFilter = sessions.length > FILTER_FROM
  const shown = useMemo(
    () =>
      showFilter ? filterSessions(sessions, query, t('untitled')) : sessions,
    [showFilter, sessions, query, t],
  )
  const noMatch = showFilter && query.trim() !== '' && shown.length === 0
  const railSection = useRef<HTMLElement>(null)
  const menu = useSessionMenu(resolution.grants, (address) => {
    // Back to the row the dialog acted on; a row that left the list (archived, deleted)
    // hands focus to the rail's own tab stop instead of the page.
    const rail = railSection.current
    const row =
      rail?.querySelector<HTMLElement>(
        `[role="option"][data-address="${CSS.escape(address)}"]`,
      ) ?? rail?.querySelector<HTMLElement>('[role="option"][tabindex="0"]')
    row?.focus()
  })
  const rail = (
    <WorkRail
      sessions={shown}
      selected={selected}
      onOpen={onOpen}
      onOpenBeside={selected ? onOpenBeside : undefined}
      pinned={pinned}
      onTogglePin={onTogglePin}
      rowMenu={(s) =>
        menu.items(s, {
          pinned: isPinned(pinned, s),
          onTogglePin:
            onTogglePin && isPinnable(s)
              ? () => onTogglePin(addressOf(s), s)
              : null,
        })
      }
      emptyAction={emptyAction}
      loading={loading}
    />
  )

  const column = (current: SessionResolution, beside: boolean) => {
    const currentRun = primaryRun(current.session.runs)
    const pinnedHere = isPinned(pinned, current.session)
    return (
      <>
        <SessionNarrative
          resolution={current}
          pinned={pinnedHere}
          onTogglePin={
            onTogglePin && isPinnable(current.session)
              ? (address) => onTogglePin(address, current.session)
              : null
          }
          onOpenDetail={
            beside ? (onOpenBesideDetail ?? (() => {})) : onOpenDetail
          }
          evidence={address.evidence}
          onExpandEvidence={onEvidence}
          inspectedId={beside ? null : (inspected?.id ?? null)}
          onInspect={beside ? undefined : setInspected}
          contextToggle={
            beside ? (
              <IconTip label={t('surface.closeBeside')}>
                <Button
                  variant="ghost"
                  size="icon"
                  className="hidden min-[1280px]:inline-flex"
                  aria-label={t('surface.closeBeside')}
                  onClick={closeBeside}
                >
                  <X aria-hidden="true" />
                </Button>
              </IconTip>
            ) : (
              contextToggle
            )
          }
          contextPaneButton={beside ? undefined : contextPaneButton}
          onOpenContext={beside ? undefined : () => onPane('context')}
          onFolder={beside ? undefined : setFrameCwd}
          back={beside ? undefined : back}
          peerSessions={sessions}
        />
        {currentRun && !hostIsolationUnavailable(currentRun) ? (
          <WorkComposer
            key={currentRun.run_ref}
            inputId={beside ? `${COMPOSER_INPUT_ID}-beside` : COMPOSER_INPUT_ID}
            frame="docked"
            attached={{
              run: currentRun,
              group: groupOf(current.session),
              title:
                operatorName(current.session) ??
                sessionShortId(current.session),
            }}
          />
        ) : null}
      </>
    )
  }

  // NOTHING TO LIST IS ONE FACT. With no session listed and none open, the rail's empty
  // state is the whole surface. A work pane saying "Select a session" beside it said the
  // same fact again, with a New session of its own (26.10.1 review).
  if (!loading && sessions.length === 0 && !resolution.target)
    return (
      <div
        className="flex min-h-0 min-w-0 flex-1 flex-col"
        data-testid="work-surface"
      >
        <section
          id={paneId('rail')}
          aria-label={t('surface.pane.rail')}
          className="flex min-h-0 flex-1 flex-col"
        >
          {listHeader}
          <div className="grid min-h-0 flex-1 place-items-center overflow-y-auto">
            {rail}
          </div>
          {listFooter}
        </section>
      </div>
    )

  return (
    <div
      className="flex min-h-0 min-w-0 flex-1 flex-col"
      data-testid="work-surface"
    >
      <div
        ref={surfaceGrid}
        data-context={contextOpen ? 'open' : 'closed'}
        style={surfaceVars}
        className={cn(
          // The list is 360 px wide, 280 px at 1360 px and below; the thread takes the rest
          // and the context pane (20 rem) joins them only on a wide screen. A row spends
          // 161 px on the icon, padding, state and time, and a title like "Claude Code ·
          // billing-api" or "Claude Code" with a session tail needs 163 to 181 px of text:
          // measured at 1440 px, the title had 103 px at 300 and 179 at 340.
          // A person can drag either edge (--list-w, --ctx-w are then set inline); each
          // pane stops at 35% of the surface, so a width stored on a wide screen never
          // squeezes the thread on a narrow one.
          'relative grid min-h-0 flex-1 [--ctx-w:var(--console-inspector-width)] [--list-w:360px] max-[1360px]:[--list-w:280px]',
          'min-[761px]:grid-cols-[min(var(--list-w),35%)_minmax(0,1fr)]',
          contextOpen && beside.grid,
        )}
      >
        <section
          ref={railSection}
          id={paneId('rail')}
          aria-label={t('surface.pane.rail')}
          className={cn(
            'flex min-h-0 min-w-0 flex-col border-line min-[761px]:border-r',
            // The list stays beside whatever is in front from 761 px: the thread, the
            // thread beside it, or the side pane that took the thread's place.
            address.pane !== 'rail' && 'hidden min-[761px]:flex',
          )}
        >
          {listHeader}
          {showFilter ? (
            <div className="shrink-0 px-3 pb-2">
              <Input
                type="search"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={t('list.filter')}
                aria-label={t('list.filter')}
                data-testid="rail-filter"
                className="h-8"
              />
            </div>
          ) : null}
          <div className="min-h-0 flex-1 overflow-y-auto">
            {noMatch ? (
              <p
                role="status"
                data-testid="rail-no-match"
                className="px-3 py-2 text-caption text-text-3"
              >
                {t('list.noMatch')}
              </p>
            ) : (
              rail
            )}
          </div>
          {listFooter}
          {menu.dialogs}
        </section>
        {/* Where the list ends: below 761 px the list is a screen of its own; from there it
            stays beside whatever is in front, so its edge does too. */}
        <PaneResizeHandle
          pane={LIST_PANE}
          container={surfaceGrid}
          target={railSection}
          grow={1}
          label={t('surface.resize.rail')}
          style={{ left: 'min(var(--list-w), 35%)' }}
          className="-translate-x-1/2 max-[760px]:hidden"
        />

        <div
          className={cn(
            'flex min-h-0 min-w-0 flex-col overflow-hidden',
            address.pane === 'rail' && 'hidden min-[761px]:flex',
            address.pane === 'context' && ['hidden', beside.column],
          )}
        >
          {split ? (
            <div
              role="group"
              className="flex h-12 shrink-0 items-center gap-1 border-b border-line px-2 min-[1280px]:hidden"
              aria-label={t('surface.switchSession')}
            >
              <div className="flex min-w-0 flex-1 gap-1">
                {(['narrative', 'beside'] as const).map((value) => (
                  <Button
                    key={value}
                    variant="ghost"
                    size="sm"
                    aria-pressed={
                      (address.pane === 'beside' ? 'beside' : 'narrative') ===
                      value
                    }
                    aria-controls={paneId(value)}
                    onClick={() => onPane(value)}
                    className="min-w-0 truncate aria-pressed:bg-active"
                  >
                    {t(
                      value === 'beside'
                        ? 'surface.besideSession'
                        : 'surface.currentSession',
                    )}
                  </Button>
                ))}
              </div>
              <Button
                variant="ghost"
                size="icon"
                className="ml-auto"
                aria-label={t('surface.closeBeside')}
                onClick={closeBeside}
              >
                <X aria-hidden="true" />
              </Button>
            </div>
          ) : null}
          <div
            ref={threadsGrid}
            style={threadsVars}
            className={cn(
              'relative grid min-h-0 min-w-0 flex-1',
              // Two threads split the width evenly until a person drags the divider
              // (--beside-w, the second thread's width), within 30% to 70%.
              split &&
                '[--beside-w:50%] min-[1280px]:grid-cols-[minmax(0,1fr)_clamp(30%,var(--beside-w),70%)]',
            )}
          >
            <section
              id={paneId('narrative')}
              ref={primaryColumn}
              tabIndex={split ? -1 : undefined}
              aria-label={t(
                split ? 'surface.currentSession' : 'surface.pane.narrative',
              )}
              onFocusCapture={() => {
                if (split && address.pane !== 'narrative') onPane('narrative')
              }}
              data-focused={focusedColumn === 'narrative'}
              className={cn(
                'flex min-h-0 min-w-0 flex-col overflow-hidden',
                // The ring says which of two columns takes the keys; one narrative has no
                // such question.
                split &&
                  'outline-none focus-within:ring-1 focus-within:ring-focus focus-within:ring-inset',
                address.pane === 'rail' && 'hidden min-[761px]:flex',
                address.pane === 'context' && ['hidden', beside.column],
                split &&
                  address.pane === 'beside' &&
                  'hidden min-[1280px]:flex',
              )}
            >
              {column(resolution, false)}
            </section>
            {split && besideResolution ? (
              <section
                id={paneId('beside')}
                ref={besideColumn}
                tabIndex={-1}
                aria-label={t('surface.besideSession')}
                onFocusCapture={() => {
                  if (address.pane !== 'beside') onPane('beside')
                }}
                data-focused={focusedColumn === 'beside'}
                className={cn(
                  'flex min-h-0 min-w-0 flex-col overflow-hidden border-line outline-none focus-within:ring-1 focus-within:ring-focus focus-within:ring-inset min-[1280px]:border-l',
                  address.pane !== 'beside' && 'hidden min-[1280px]:flex',
                )}
              >
                {column(besideResolution, true)}
              </section>
            ) : null}
            {split && besideResolution ? (
              <PaneResizeHandle
                pane={BESIDE_PANE}
                container={threadsGrid}
                target={besideColumn}
                grow={-1}
                label={t('surface.resize.beside')}
                style={{ right: 'clamp(30%, var(--beside-w), 70%)' }}
                className="translate-x-1/2 max-[1279px]:hidden"
              />
            ) : null}
          </div>
        </div>

        {/* Only beside the thread (where it keeps 760 px, pane open): below that the pane
            replaces it. */}
        {contextOpen ? (
          <PaneResizeHandle
            pane={CONTEXT_PANE}
            container={surfaceGrid}
            target={contextColumn}
            grow={-1}
            label={t('surface.resize.context')}
            style={{ right: 'min(var(--ctx-w), 35%)' }}
            className={cn('translate-x-1/2', beside.close)}
          />
        ) : null}
        <section
          ref={contextColumn}
          id={paneId('context')}
          aria-label={t('surface.pane.context')}
          className={cn(
            'min-h-0 min-w-0 overflow-y-auto border-line p-4',
            beside.pane,
            address.pane !== 'context' && 'hidden',
            contextOpen ? beside.paneOpen : beside.paneClosed,
          )}
        >
          <div className="mb-2 flex items-center justify-between">
            <Button
              variant="ghost"
              size="icon"
              aria-controls={paneId('narrative')}
              aria-label={t('surface.backTo', {
                pane: t('surface.pane.narrative'),
              })}
              title={t('surface.backTo', { pane: t('surface.pane.narrative') })}
              data-testid="pane-button-narrative"
              onClick={() => onPane('narrative')}
              className={beside.back}
            >
              <ArrowLeft />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              onClick={() => setContextOpen(false)}
              aria-label={t('surface.closeContext')}
              title={t('surface.closeContext')}
              data-testid="context-close"
              className={cn('ml-auto', beside.close)}
            >
              <X />
            </Button>
          </div>
          <SessionContextPane
            resolution={resolution}
            inspected={inspected}
            evidence={address.evidence}
            onExpandEvidence={onEvidence}
            peerSessions={sessions}
            frameCwd={frameCwd}
            panel={address.panel}
            onPanel={onPanel}
          />
        </section>
      </div>
    </div>
  )
}
