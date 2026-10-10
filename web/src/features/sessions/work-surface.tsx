// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ArrowLeft, PanelRight, X } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { WorkComposer } from '@/components/layout/work-composer'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
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
  WORK_PANES,
  type EvidenceBlock,
  type SessionAddress,
  type WorkPane,
} from './session-address'
import { SessionContextPane } from './session-context-pane'
import { pinnedAddress } from './session-pins'
import { IconTip } from './icon-tip'
import { FILTER_FROM, filterSessions } from './session-filter'
import { groupOf } from './session-groups'
import { SessionNarrative } from './session-narrative'
import type { SessionResolution } from './use-session-resolution'
import { WorkRail } from './work-rail'
import './i18n'

export interface WorkSurfaceProps {
  sessions: readonly UnifiedSession[]
  loading: boolean
  address: SessionAddress
  resolution: SessionResolution
  pinned: ReadonlySet<string>
  onTogglePin: ((address: string) => void) | null
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
}

const paneId = (pane: WorkPane) => `work-pane-${pane}`

/** Where this browser remembers whether the side pane is open. */
const PANEL_OPEN_KEY = 'olivares.sessions.sidePane'
/** The widest screen on which the side pane starts closed. */
const PANEL_OPEN_MIN_WIDTH = 1441

function storedPanelOpen(): boolean {
  try {
    const stored = window.localStorage.getItem(PANEL_OPEN_KEY)
    if (stored === 'open') return true
    if (stored === 'closed') return false
  } catch {
    // A browser that refuses storage keeps the default.
  }
  return (
    typeof window !== 'undefined' && window.innerWidth >= PANEL_OPEN_MIN_WIDTH
  )
}

function rememberPanelOpen(open: boolean) {
  try {
    window.localStorage.setItem(PANEL_OPEN_KEY, open ? 'open' : 'closed')
  } catch {
    // Not remembered: the pane still opens and closes.
  }
}

export function WorkSurface({
  sessions,
  loading,
  address,
  resolution,
  pinned,
  onTogglePin,
  onOpen,
  onPane,
  onEvidence,
  onOpenDetail,
  emptyAction,
  listHeader,
  onPanel,
}: WorkSurfaceProps) {
  const { t } = useTranslation('sessions')
  const selected = address.address
  const run = primaryRun(resolution.session.runs)
  const pinAt = pinnedAddress(pinned, resolution.session)
  const [inspected, setInspected] = useState<ConversationItem | null>(null)
  const [frameCwd, setFrameCwd] = useState<string | null>(null)

  // Keep all panes mounted; the URL selects the visible pane on narrow screens.
  // THE SIDE PANE IS CLOSED BY DEFAULT up to 1440 px, where the thread needs the width, and
  // open from there; once a person has opened or closed it, that choice is remembered in
  // this browser. The address naming it (`?pane=context`) opens it either way.
  const [contextOpen, setContextOpenState] = useState(
    () => address.pane === 'context' || storedPanelOpen(),
  )
  const setContextOpen = (open: boolean | ((was: boolean) => boolean)) =>
    setContextOpenState((was) => {
      const next = typeof open === 'function' ? open(was) : open
      rememberPanelOpen(next)
      return next
    })
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
        className="hidden aria-pressed:bg-active xl:inline-flex"
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
        className="max-[760px]:hidden xl:hidden"
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
      if (isTypingTarget(e.target)) return
      const command = resolveBinding(KEYBINDINGS, e, { workSurface: true })
      if (command !== 'surface.nextPane' && command !== 'surface.previousPane')
        return
      e.preventDefault()
      const at = WORK_PANES.indexOf(address.pane)
      const step = command === 'surface.nextPane' ? 1 : -1
      const next =
        WORK_PANES[(at + step + WORK_PANES.length) % WORK_PANES.length]
      onPane(next)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [address.pane, onPane])

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
  const rail = (
    <WorkRail
      sessions={shown}
      selected={selected}
      onOpen={onOpen}
      pinned={pinned}
      onTogglePin={onTogglePin}
      emptyAction={emptyAction}
      loading={loading}
    />
  )

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
        </section>
      </div>
    )

  return (
    <div
      className="flex min-h-0 min-w-0 flex-1 flex-col"
      data-testid="work-surface"
    >
      <div
        data-context={contextOpen ? 'open' : 'closed'}
        className={cn(
          // The list is 300 px wide, 280 px at 1360 px and below; the thread takes the rest
          // and the context pane (20 rem) joins them only on a wide screen.
          'grid min-h-0 flex-1 [--list-w:300px] max-[1360px]:[--list-w:280px]',
          'min-[761px]:grid-cols-[var(--list-w)_minmax(0,1fr)]',
          contextOpen &&
            'xl:grid-cols-[var(--list-w)_minmax(0,1fr)_var(--console-inspector-width)]',
        )}
      >
        <section
          id={paneId('rail')}
          aria-label={t('surface.pane.rail')}
          className={cn(
            'flex min-h-0 min-w-0 flex-col border-line min-[761px]:border-r',
            address.pane === 'narrative' && 'hidden min-[761px]:flex',
            address.pane === 'context' && 'hidden xl:flex',
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
        </section>

        <section
          id={paneId('narrative')}
          aria-label={t('surface.pane.narrative')}
          className={cn(
            'flex min-h-0 min-w-0 flex-col overflow-hidden',
            address.pane === 'rail' && 'hidden min-[761px]:flex',
            address.pane === 'context' && 'hidden xl:flex',
          )}
        >
          <SessionNarrative
            resolution={resolution}
            pinned={pinAt !== undefined}
            onTogglePin={
              onTogglePin && ((address) => onTogglePin(pinAt ?? address))
            }
            onOpenDetail={onOpenDetail}
            evidence={address.evidence}
            onExpandEvidence={onEvidence}
            inspectedId={inspected?.id ?? null}
            onInspect={setInspected}
            contextToggle={contextToggle}
            contextPaneButton={contextPaneButton}
            onOpenContext={() => onPane('context')}
            onFolder={setFrameCwd}
            back={back}
            peerSessions={sessions}
          />
          {run && !hostIsolationUnavailable(run) ? (
            <WorkComposer
              frame="docked"
              attached={{
                run,
                group: groupOf(resolution.session),
                title:
                  operatorName(resolution.session) ??
                  sessionShortId(resolution.session),
              }}
            />
          ) : null}
        </section>

        <section
          id={paneId('context')}
          aria-label={t('surface.pane.context')}
          className={cn(
            'min-h-0 overflow-y-auto border-line p-4 max-xl:col-span-full xl:border-l',
            address.pane !== 'context' && 'hidden',
            contextOpen ? 'xl:block' : 'xl:hidden',
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
              className="xl:hidden"
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
              className="ml-auto max-xl:hidden"
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
