// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { preferenceStorage } from '@/lib/preference-storage'
import { ArrowLeft, PanelRight, X } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Link, useRouter, type AnyRouter } from '@tanstack/react-router'
import { WorkComposer } from '@/components/layout/work-composer'
import { COMPOSER_INPUT_ID } from '@/components/layout/work-composer-id'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { PaneResizeHandle } from '@/components/ui/pane-resize'
import { usePaneVars } from '@/components/ui/pane-vars'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { useMinWidth } from '@/lib/hooks/use-min-width'
import {
  isTypingTarget,
  modalIsOpen,
  resolveBinding,
} from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { StateBlock } from '@/components/ui/state-block'
import { usePreferencesStore } from '@/stores/preferences'
import type { ConversationItem } from './conversation-frames'
import {
  hostIsolationUnavailable,
  operatorName,
  runTitle,
  primaryRun,
  sessionPlace,
  sessionShortId,
  sessionFolder,
  type UnifiedSession,
} from './provenance'
import {
  addressOf,
  SESSION_PARAM,
  SHOW_PARAM,
  WORK_PANES,
  type EvidenceBlock,
  type SessionAddress,
  type StripFilter,
  type WorkPane,
} from './session-address'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { SessionContextPane } from './session-context-pane'
import { useSessionMenu } from './session-menu'
import { isPinnable, isPinned, preferenceStorageKey } from './session-pins'
import { useAuth } from '@/lib/auth/context'
import { IconTip } from './icon-tip'
import { filterSessions, sessionFolders } from './session-filter'
import { groupOf, groupSessions, inSection, railOrder } from './session-groups'
import { SessionStrip } from './session-strip'
import {
  TerminalDrawer,
  TerminalHost,
  useTerminalDrawer,
} from './terminal-drawer'
import { SessionNarrative } from './session-narrative'
import { BESIDE_PANE, CONTEXT_PANE, LIST_PANE } from './surface-resize'
import { useAutoTitle } from './use-auto-title'
import { useFarSessionHits } from './far-session-hits'
import type { SessionResolution } from './use-session-resolution'
import { WorkRail } from './work-rail'
import './i18n'

export interface WorkSurfaceProps {
  sessions: readonly UnifiedSession[]
  /** The workspaces' names by ref, for the rail's workspace rows. */
  workspaceNames?: ReadonlyMap<string, string>
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

  /** The work region of an empty estate, gated by the caller: the composer that starts the
   * first session. */
  emptyAction?: React.ReactNode
  /** New session was asked for: `emptyAction` is in front of the sessions listed. */
  starting?: boolean
  /** Why the list could not be read, and the way to ask again; see WorkRail. */
  error?: unknown
  onRetry?: () => void
  /** The side pane's tab was chosen (`?panel=`). */
  onPanel?: (panel: string) => void
  /** The page's heading and verbs (name, count, New session, the views menu). Always the
   * root's first child: it reaches the shell's header from every state of this surface and
   * stays mounted across them, so the focus it restores is not taken again. */
  listHeader?: React.ReactNode
  /** The foot of the list: what it does not show, and Load more. */
  listFooter?: React.ReactNode
  /** The strip's facts: who waits, what runs, today's cost (session-strip.tsx). */
  strip?: Omit<
    React.ComponentProps<typeof SessionStrip>,
    'show' | 'toList' | 'className'
  >
  /** The section the strip narrowed the list to (`?show=`). */
  show?: StripFilter
}

const paneId = (pane: WorkPane) => `work-pane-${pane}`

function focusColumn(column: HTMLElement | null) {
  const field = column?.querySelector<HTMLTextAreaElement>('textarea')
  ;(field && !field.disabled ? field : column)?.focus()
}

/** The name under which this browser remembers, per person, whether the side pane is open. */
const PANEL_OPEN_KEY = 'olivares.sessions.sidePane'

/** Where Details opens by default: from here the conversation keeps the main column with
 * the navigation, the list and Details beside it. Narrower (a 1280 px laptop) it starts
 * closed, one click away, so the conversation keeps the room, unless the session
 * changed files. */
const DETAILS_OPEN_BY_DEFAULT_PX = 1440

/** Whether this person left Details open, or null with no choice yet: then it is open
 * where the window is at least DETAILS_OPEN_BY_DEFAULT_PX wide, or when the session
 * changed files. A principal with no partition (a token) is never remembered and sees
 * the default. */
function storedPanelChoice(key: string | null): boolean | null {
  if (!key) return null
  try {
    // A person with no choice yet keeps the one this browser saved for everyone before.
    const stored =
      preferenceStorage.getItem(key) ??
      preferenceStorage.getItem(PANEL_OPEN_KEY)
    return stored === null ? null : stored !== 'closed'
  } catch {
    // A browser that refuses storage keeps the default.
    return null
  }
}

function rememberPanelOpen(key: string | null, open: boolean) {
  if (!key) return
  try {
    preferenceStorage.setItem(key, open ? 'open' : 'closed')
  } catch {
    // Not remembered: the pane still opens and closes.
  }
}

/**
 * WHERE DETAILS SITS BESIDE THE CONVERSATION (the regression design): from 1120 px, where the
 * 264 px list and the 280 px pane leave the conversation 576 px; with two conversations,
 * from 2201 px, where each keeps 760 px. Narrower, Details opens as a dialog sheet over
 * the list and the conversation, which stay where they are.
 */
const DETAILS_BESIDE_PX = { one: 1120, two: 2201 } as const

/** Where focus goes when the Details sheet closes: what opened it, else the Details
 * button, else (on a phone, where that button is hidden) the thread's menu. */
function returnFocus(...candidates: (Element | null | undefined)[]) {
  const target = candidates.find(
    (el): el is HTMLElement =>
      el instanceof HTMLElement &&
      el.isConnected &&
      // A hidden element cannot take focus; where the check is missing, try it.
      (typeof el.checkVisibility !== 'function' || el.checkVisibility()),
  )
  target?.focus()
}

export function WorkSurface({
  sessions,
  workspaceNames,
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
  starting = false,
  error,
  onRetry,
  listHeader,
  listFooter,
  onPanel,
  strip,
  show,
}: WorkSurfaceProps) {
  const { t } = useTranslation('sessions')
  // The conversation in front and the one beside it each get a title from their first
  // message when they have neither a name nor a title.
  useAutoTitle(primaryRun(resolution.runs))
  useAutoTitle(primaryRun(besideResolution?.runs ?? []))
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
  const [unknownFrames, setUnknownFrames] = useState<readonly string[]>([])
  const closeBeside = useCallback(() => {
    onCloseBeside?.()
    requestAnimationFrame(() => focusColumn(primaryColumn.current))
  }, [onCloseBeside])

  // Keep the list and the conversation mounted; the URL selects the one in front below 761 px.
  // DETAILS IS OPEN BY DEFAULT beside the conversation; once a person has opened or closed
  // it, that choice is remembered for that person in this browser. The address naming it
  // (`?pane=context`) opens it either way: beside the work, or as the sheet below 1120 px.
  const { principal, activeTenant, can } = useAuth()
  const panelKey =
    typeof window === 'undefined'
      ? null
      : preferenceStorageKey(
          PANEL_OPEN_KEY,
          window.location.origin,
          principal ?? null,
          activeTenant ?? null,
        )
  const roomy = useMinWidth(DETAILS_OPEN_BY_DEFAULT_PX)
  const [contextOpen, setContextOpenState] = useState(
    () => address.pane === 'context' || (storedPanelChoice(panelKey) ?? roomy),
  )
  // Another person or organization: read that person's choice, never carry the last one.
  const [seenPanelKey, setSeenPanelKey] = useState(panelKey)
  if (seenPanelKey !== panelKey) {
    setSeenPanelKey(panelKey)
    setContextOpenState(
      address.pane === 'context' || (storedPanelChoice(panelKey) ?? roomy),
    )
  }
  // A session that changed files opens Details, on Changes, wherever it fits beside the
  // work, unless this person chose: the same read the Changes tab makes, from its cache.
  const run = can('sessions:run:read')
    ? primaryRun(resolution.session.runs)
    : undefined
  const changes = useQuery({
    queryKey: agentOpsKeys.runChanges(activeTenant, run?.run_ref ?? ''),
    queryFn: ({ signal }) => agentOpsApi.changes(run!.run_ref, { signal }),
    enabled: !!run,
  })
  // An address that names the terminal (`?panel=terminal`, a link kept from the side tab it
  // used to be) opens the drawer.
  const wantsTerminal = address.panel === 'terminal' && !!run?.workspace_ref
  const terminalRun = run?.run_ref
  useEffect(() => {
    if (wantsTerminal && terminalRun)
      useTerminalDrawer.getState().setOpen(terminalRun, true)
  }, [wantsTerminal, terminalRun])
  const changedRun = changes.data?.files.length ? run?.run_ref : undefined
  const [seenChangedRun, setSeenChangedRun] = useState<string>()
  if (seenChangedRun !== changedRun) {
    setSeenChangedRun(changedRun)
    if (changedRun && storedPanelChoice(panelKey) === null)
      setContextOpenState(true)
  }
  const setContextOpen = (open: boolean | ((was: boolean) => boolean)) =>
    setContextOpenState((was) => {
      const next = typeof open === 'function' ? open(was) : open
      rememberPanelOpen(panelKey, next)
      return next
    })
  const besideFits = useMinWidth(DETAILS_BESIDE_PX[split ? 'two' : 'one'])
  const detailsBeside = besideFits && contextOpen
  const sheetOpen = !besideFits && address.pane === 'context'
  const [seenPane, setSeenPane] = useState(address.pane)
  if (seenPane !== address.pane) {
    setSeenPane(address.pane)
    if (address.pane === 'context') setContextOpenState(true)
  }
  useEffect(() => {
    if (!detailsBeside) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented) return
      if (isTypingTarget(e.target)) return
      // An open dialog or menu owns this Escape: it closes that, not the pane.
      if (
        modalIsOpen() ||
        document.querySelector('[role="menu"][data-state="open"]')
      )
        return
      // A pane closed by Escape is not a choice to remember: the stored state stays.
      setContextOpenState(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [detailsBeside])
  // DETAILS: one icon in the thread's header. Where Details fits beside the conversation it
  // shows and hides the pane; narrower, it opens the sheet (`?pane=context`). On a phone the
  // thread's menu holds the same entry, so the header keeps its room for the title.
  const detailsButton = useRef<HTMLButtonElement>(null)
  const sheetOpener = useRef<Element | null>(null)
  const openSheet = (from: Element | null | undefined) => {
    sheetOpener.current = from ?? null
    onPane('context')
  }
  const closeSheet = () => {
    if (address.pane === 'context') onPane('narrative')
  }
  const contextToggle = (
    <IconTip label={t('surface.details')}>
      <Button
        ref={detailsButton}
        variant="ghost"
        size="icon"
        aria-controls={paneId('context')}
        aria-label={t('surface.details')}
        data-testid="context-toggle"
        {...(besideFits
          ? {
              'aria-pressed': contextOpen,
              onClick: () => setContextOpen((open) => !open),
            }
          : {
              'aria-haspopup': 'dialog' as const,
              'aria-expanded': sheetOpen,
              onClick: (e: React.MouseEvent<HTMLButtonElement>) =>
                openSheet(e.currentTarget),
            })}
        className={cn(
          'aria-pressed:bg-active',
          !besideFits && 'max-[760px]:hidden',
        )}
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
        modalIsOpen() ||
        document.querySelector('[role="menu"][data-state="open"]')
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

  // Capture session chords, leaving terminal input to the shell except its toggle.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.isComposing || !resolution.target) return
      if (
        modalIsOpen() ||
        document.querySelector('[role="menu"][data-state="open"]')
      )
        return
      const command = resolveBinding(KEYBINDINGS, e, { workSurface: true })
      if (
        e.target instanceof Element &&
        e.target.closest('.xterm') &&
        command !== 'surface.terminal'
      )
        return
      if (command === 'shell.panel') {
        e.preventDefault()
        e.stopPropagation()
        if (e.repeat) return
        const next = !(detailsBeside || sheetOpen)
        setContextOpen(next)
        if (!besideFits) {
          if (next) openSheet(document.activeElement)
          else closeSheet()
        }
        return
      }
      // The terminal is the drawer under the conversation, not a tab of the side pane.
      if (
        command === 'surface.terminal' &&
        run?.workspace_ref &&
        can('sessions:workspace:write')
      ) {
        e.preventDefault()
        e.stopPropagation()
        if (!e.repeat) useTerminalDrawer.getState().toggle(run.run_ref)
        return
      }
      const panel =
        command === 'surface.changes'
          ? 'changes'
          : command === 'surface.preview'
            ? 'preview'
            : null
      if (
        panel &&
        onPanel &&
        run &&
        (panel === 'preview' || run.workspace_ref)
      ) {
        e.preventDefault()
        e.stopPropagation()
        if (e.repeat) return
        if ((detailsBeside || sheetOpen) && address.panel === panel) {
          setContextOpen(false)
          closeSheet()
        } else {
          onPanel(panel)
          setContextOpen(true)
          if (!besideFits) openSheet(document.activeElement)
        }
        return
      }
      const picker =
        command === 'surface.model' || command === 'surface.effort'
          ? 'model'
          : command === 'surface.access'
            ? 'access'
            : null
      if (!picker) return
      const column =
        split &&
        (besideColumn.current?.contains(e.target as Node) ||
          (!primaryColumn.current?.contains(e.target as Node) &&
            address.pane === 'beside'))
          ? besideColumn.current
          : primaryColumn.current
      const button = column?.querySelector<HTMLButtonElement>(
        `[data-session-picker="${picker}"]`,
      )
      if (!button || button.disabled) return
      e.preventDefault()
      e.stopPropagation()
      if (e.repeat) return
      button.focus()
      button.click()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  })

  // THE FILTER FIELD narrows what the rows show: by name, tool or folder, in this browser,
  // with no request.
  const [query, setQuery] = useState('')
  // THE FOLDER FILTER is the person's choice and the console remembers it. A stored folder
  // with no session left in it filters nothing: the list is never empty for a stale choice.
  const storedFolder = usePreferencesStore((s) => s.sessionsFolder)
  const setStoredFolder = usePreferencesStore((s) => s.setSessionsFolder)
  const folders = useMemo(() => sessionFolders(sessions), [sessions])
  const folder =
    storedFolder && folders.includes(storedFolder) ? storedFolder : null
  // Clear filters empties all three, the strip's section in the address too, and the
  // field takes the keyboard back. Outside a router (a view on its own) there is none.
  const router = useRouter({ warn: false }) as AnyRouter | undefined
  const filterField = useRef<HTMLInputElement>(null)
  const clearFilters = () => {
    setQuery('')
    setStoredFolder(null)
    if (show)
      void router?.navigate({
        to: '.',
        search: ((prev: Record<string, unknown>) => ({
          ...prev,
          [SHOW_PARAM]: undefined,
        })) as never,
        hash: true,
      })
    filterField.current?.focus()
  }
  // The strip's need you and running links narrow the list to their section first. A
  // section that empties has no strip part to clear it: the whole list is back.
  const shown = useMemo(() => {
    const inFolder = folder
      ? sessions.filter((s) => sessionFolder(s) === folder)
      : sessions
    const section = show ? inFolder.filter((s) => inSection(s, show)) : []
    return filterSessions(
      section.length > 0 ? section : inFolder,
      query,
      t('untitled'),
    )
  }, [show, folder, sessions, query, t])
  const noMatch = query.trim() !== '' && shown.length === 0
  // THE NEXT AND PREVIOUS SESSION, in the order the list draws them. Never while typing:
  // on a Mac, Option+Shift+Up and Down extend a selection to the paragraph's edge in a
  // field. An open dialog owns the keyboard. It stops at the ends, as the arrows in the
  // list do, and needs a session open: in the list itself the arrows and `j`/`k` walk the
  // rows.
  const workingFolded = usePreferencesStore((s) => s.workingFolded)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (
        e.defaultPrevented ||
        e.isComposing ||
        isTypingTarget(e.target) ||
        modalIsOpen() ||
        document.querySelector('[role="menu"][data-state="open"]') ||
        !resolution.target
      )
        return
      const command = resolveBinding(KEYBINDINGS, e, { workSurface: true })
      if (
        command !== 'surface.nextSession' &&
        command !== 'surface.previousSession'
      )
        return
      e.preventDefault()
      if (e.repeat) return
      const order = railOrder(
        groupSessions(shown, pinned),
        false,
        false,
        !workingFolded,
      )
      const at = order.findIndex((s) => addressOf(s) === selected)
      if (at === -1) return
      const next = order[command === 'surface.nextSession' ? at + 1 : at - 1]
      if (next) onOpen(next)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [shown, pinned, workingFolded, selected, onOpen, resolution.target])
  const farHits = useFarSessionHits(query, noMatch)
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
      workspaceNames={workspaceNames}
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
      error={error}
      onRetry={onRetry}
      loading={loading}
    />
  )

  const column = (current: SessionResolution, beside: boolean) => {
    const currentRun = primaryRun(current.session.runs)
    const pinnedHere = isPinned(pinned, current.session)
    const composerId =
      currentRun && !hostIsolationUnavailable(currentRun)
        ? beside
          ? `${COMPOSER_INPUT_ID}-beside`
          : COMPOSER_INPUT_ID
        : undefined
    return (
      <TerminalHost run={beside ? undefined : currentRun}>
        <SessionNarrative
          resolution={current}
          composerId={composerId}
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
          onOpenContext={
            beside || besideFits
              ? undefined
              : () =>
                  openSheet(
                    primaryColumn.current?.querySelector(
                      '[data-testid="narrative-menu"]',
                    ),
                  )
          }
          onFolder={beside ? undefined : setFrameCwd}
          onUnknownFrames={beside ? undefined : setUnknownFrames}
          back={beside ? undefined : back}
          peerSessions={sessions}
        />
        {currentRun && composerId ? (
          <WorkComposer
            key={currentRun.run_ref}
            inputId={composerId}
            frame="docked"
            attached={{
              run: currentRun,
              group: groupOf(current.session),
              title:
                operatorName(current.session) ??
                runTitle(current.session) ??
                sessionPlace(current.session) ??
                sessionShortId(current.session),
            }}
          />
        ) : null}
      </TerminalHost>
    )
  }

  // ONE DETAILS, mounted where it is shown: beside the work or in the sheet, never both.
  const details = (
    <SessionContextPane
      resolution={resolution}
      inspected={inspected}
      evidence={address.evidence}
      onExpandEvidence={onEvidence}
      peerSessions={sessions}
      frameCwd={frameCwd}
      unknownFrames={unknownFrames}
      panel={address.panel}
      onPanel={onPanel}
    />
  )

  // A new session uses the work region; the sidebar already holds the work list.
  if (
    !loading &&
    emptyAction &&
    (starting || (sessions.length === 0 && !resolution.target))
  )
    return (
      <div
        className="flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto"
        data-testid="work-surface"
      >
        {listHeader}
        {emptyAction}
      </div>
    )

  // The list is a destination of its own, never a second column beside the thread.
  if (!resolution.target || address.pane === 'rail')
    return (
      <div
        className="flex min-h-0 min-w-0 flex-1 flex-col"
        data-testid="work-surface"
      >
        {listHeader}
        <section
          ref={railSection}
          id={paneId('rail')}
          aria-label={t('surface.pane.rail')}
          className="flex min-h-0 min-w-0 flex-1 flex-col"
        >
          {strip ? <SessionStrip {...strip} show={show} /> : null}
          {sessions.length > 0 ? (
            <div className="flex shrink-0 items-center gap-2 px-3 pb-2">
              <Input
                ref={filterField}
                type="search"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={t('list.filter')}
                aria-label={t('list.filter')}
                data-testid="rail-filter"
                className="min-w-0 flex-1"
              />
              {folders.length > 1 ? (
                <Select
                  value={folder ? `folder:${folder}` : 'all'}
                  onValueChange={(value) =>
                    setStoredFolder(
                      value === 'all' ? null : value.slice('folder:'.length),
                    )
                  }
                >
                  <SelectTrigger
                    aria-label={t('list.folderFilter')}
                    className="w-auto max-w-[40%] shrink-0"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">{t('list.allFolders')}</SelectItem>
                    {folders.map((name) => (
                      <SelectItem key={name} value={`folder:${name}`}>
                        {name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : null}
            </div>
          ) : null}
          <div className="min-h-0 flex-1 overflow-y-auto">
            {noMatch && farHits.length > 0 ? (
              // Sessions older than the loaded page, found by the console's search.
              <ul data-testid="rail-far-hits" className="flex flex-col">
                {farHits.map((hit) => (
                  <li key={hit.id}>
                    <Link
                      to={'/sessions' as never}
                      search={{ [SESSION_PARAM]: hit.id } as never}
                      className="line-clamp-2 px-3 py-2 text-body font-medium [overflow-wrap:anywhere] text-text outline-none hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset"
                    >
                      {hit.name}
                    </Link>
                  </li>
                ))}
              </ul>
            ) : noMatch ? (
              <StateBlock
                state="filtered"
                size="panel"
                data-testid="rail-no-match"
                title={t('list.noMatch')}
                filters={[
                  query.trim(),
                  folder,
                  show ? t(`list.group.${show}`) : null,
                ]
                  .filter(Boolean)
                  .join(' · ')}
                hiddenCount={sessions.length}
                onClear={clearFilters}
              />
            ) : (
              rail
            )}
          </div>
          {listFooter}
          {menu.dialogs}
        </section>
      </div>
    )

  return (
    <div
      className="flex min-h-0 min-w-0 flex-1 flex-col"
      data-testid="work-surface"
    >
      {listHeader}
      <div
        ref={surfaceGrid}
        data-context={detailsBeside ? 'open' : 'closed'}
        style={surfaceVars}
        className={cn(
          // The list is 292 px wide and Details 304 px, 264 and 280 px at 1350 px and below
          // (the regression design); the conversation takes the rest. A row's title is cut with an
          // ellipsis and its whole text is on the row's hover.
          // A person can drag either edge (--list-w, --ctx-w are then set inline); each
          // pane stops at 35% of the surface, so a width stored on a wide screen never
          // squeezes the thread on a narrow one.
          'relative grid min-h-0 flex-1 [--ctx-w:304px] [--list-w:292px] max-[1350px]:[--ctx-w:280px] max-[1350px]:[--list-w:264px]',
          // ONE column template: a media-prefixed one beside another would outrank it.
          detailsBeside
            ? 'grid-cols-[minmax(0,1fr)_min(var(--ctx-w),35%)]'
            : 'grid-cols-1',
        )}
      >
        <div className={cn('flex min-h-0 min-w-0 flex-col overflow-clip')}>
          {/* A phone shows one pane: the strip is above the conversation too, and its
              filters bring the list in front. */}
          {strip ? (
            <SessionStrip
              {...strip}
              show={show}
              toList
              className="pt-2 min-[761px]:hidden"
            />
          ) : null}
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
              data-focused={!split || focusedColumn === 'narrative'}
              className={cn(
                'flex min-h-0 min-w-0 flex-col overflow-clip',
                // The ring says which of two columns takes the keys; one narrative has no
                // such question.
                split &&
                  'outline-none focus-within:ring-1 focus-within:ring-focus focus-within:ring-inset',
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

        {detailsBeside ? (
          <>
            <PaneResizeHandle
              pane={CONTEXT_PANE}
              container={surfaceGrid}
              target={contextColumn}
              grow={-1}
              label={t('surface.resize.context')}
              style={{ right: 'min(var(--ctx-w), 35%)' }}
              className="translate-x-1/2"
            />
            <section
              ref={contextColumn}
              id={paneId('context')}
              aria-label={t('surface.pane.context')}
              // The column does not scroll: the pane's tab bar stays put and each tab
              // fills the height below it (session-context-pane.tsx).
              className="flex min-h-0 min-w-0 flex-col border-l border-line p-4"
            >
              <div className="mb-2 flex shrink-0 items-center justify-end">
                <Button
                  variant="ghost"
                  size="icon"
                  onClick={() => setContextOpen(false)}
                  aria-label={t('surface.closeContext')}
                  title={t('surface.closeContext')}
                  data-testid="context-close"
                >
                  <X />
                </Button>
              </div>
              {details}
            </section>
          </>
        ) : null}
      </div>
      {/* Under the whole surface, Details included: the terminal keeps the width.
          It is the current session's, so it hides with that session's column. */}
      <div
        className={cn(
          'contents',
          split && address.pane === 'beside' && 'max-[1279px]:hidden',
        )}
      >
        <TerminalDrawer run={primaryRun(resolution.session.runs)} />
      </div>
      <Sheet
        open={sheetOpen}
        onOpenChange={(open) => {
          if (!open) closeSheet()
        }}
      >
        <SheetContent
          id={paneId('context')}
          aria-describedby={undefined}
          className="max-w-md overflow-y-auto p-4"
          onCloseAutoFocus={(e) => {
            e.preventDefault()
            returnFocus(
              sheetOpener.current,
              detailsButton.current,
              primaryColumn.current?.querySelector(
                '[data-testid="narrative-menu"]',
              ),
            )
            sheetOpener.current = null
          }}
        >
          <SheetHeader>
            <SheetTitle>{t('surface.details')}</SheetTitle>
          </SheetHeader>
          {sheetOpen ? details : null}
        </SheetContent>
      </Sheet>
    </div>
  )
}
