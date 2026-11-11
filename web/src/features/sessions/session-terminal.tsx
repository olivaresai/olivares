// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SHELLS IN A FOLDER, side by side or one at a time.
//
// The shells run on the server under the folder's confinement (modules/sessions/terminal.go)
// and their screens live in the terminal store (terminal-store.ts), so this view only lends
// a screen its place: leaving and coming back finds the same terminals. xterm.js loads when
// the first terminal opens, so the console's first paint carries none of it.
//
// ⛔ OPENING THE PANEL STARTS NOTHING. A terminal is an act (it starts a process and is
//    written to the audit ledger), so the empty panel offers a button, and a reload takes
//    back only the shells this tab already had. Reading changes nothing.
import { useQuery } from '@tanstack/react-query'
import {
  ArrowDownToLine,
  ChevronDown,
  ChevronUp,
  ClipboardPaste,
  Columns2,
  Copy,
  Link2,
  MoreHorizontal,
  Play,
  Plus,
  Search,
  SendHorizontal,
  Square,
  Terminal as TerminalIcon,
  TextCursor,
  X,
} from 'lucide-react'
import {
  useEffect,
  useId,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import {
  CommandDialog,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { agentOpsKeys } from '@/features/agentops/api'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import './i18n'
import {
  projectScripts,
  topLevelFiles,
  useAddedScripts,
  type ProjectScript,
} from './project-scripts'
import { openTerminalLink } from './terminal-links'
import { terminalShortcut } from './terminal-shortcuts'
import {
  terminalGroup,
  type TerminalController,
  type TerminalGroup,
  type TerminalView,
} from './terminal-store'

/** The keys a touch keyboard lacks. */
const TOUCH_KEYS: readonly { id: string; seq: string }[] = [
  { id: 'esc', seq: '\x1b' },
  { id: 'tab', seq: '\t' },
  { id: 'ctrlC', seq: '\x03' },
  { id: 'up', seq: '\x1b[A' },
  { id: 'down', seq: '\x1b[B' },
]

export interface SessionTerminalProps {
  workspaceRef: string
  /** The folder's path, so the files the output names can be opened. */
  root?: string | null
  /** Offered when the host can carry selected output to the agent's message. */
  onSendToAgent?: (text: string, title: string) => void
}

/** The terminals of one folder for the signed-in person. Another folder is another group. */
export function useTerminalGroup(
  workspaceRef: string,
  root?: string | null,
): TerminalGroup {
  const tenant = useTenantStore((s) => s.activeTenant)
  const generation = useSessionStore((s) => s.credentialGeneration)
  const { principal } = useAuth()
  const person = principal?.user_id
    ? JSON.stringify([principal.kind, principal.user_id])
    : null
  const group = terminalGroup(tenant, generation, person, workspaceRef)
  useEffect(() => {
    if (root) group.setRoot(root)
  }, [group, root])
  return group
}

export function SessionTerminal(props: SessionTerminalProps) {
  const group = useTerminalGroup(props.workspaceRef, props.root)
  return <TerminalPanel group={group} onSendToAgent={props.onSendToAgent} />
}

const noSubscribe = () => () => {}
const NO_VIEW: TerminalView = {
  title: '',
  state: 'pending',
  exitCode: null,
  hasSelection: false,
}

function useControllerView(c: TerminalController | undefined): TerminalView {
  return useSyncExternalStore(
    c?.subscribe ?? noSubscribe,
    c?.getView ?? (() => NO_VIEW),
  )
}

export function TerminalPanel({
  group,
  onSendToAgent,
  place,
  trail,
  projectRef,
}: {
  group: TerminalGroup
  onSendToAgent?: (text: string, title: string) => void
  /** Where the terminals run, named quietly after the tabs, before the shell. */
  place?: ReactNode
  /** The host's own controls, at the end of the title row. */
  trail?: ReactNode
  /** The project folder the person's added scripts are kept by; else the group's folder. */
  projectRef?: string
}) {
  const { t } = useTranslation('sessions')
  const view = useSyncExternalStore(group.subscribe, group.getView)
  const { tenant, workspaceRef } = group
  const added = useAddedScripts(projectRef ?? workspaceRef)
  // The entry Details reads: with both open, the folder is listed and read once. Only
  // the declarations the folder holds are read, as a missing one would answer 404.
  const declared = useQuery({
    queryKey: [...agentOpsKeys.files(tenant, workspaceRef, ''), 'scripts'],
    queryFn: async ({ signal }) =>
      projectScripts(
        workspaceRef,
        tenant,
        signal,
        await topLevelFiles(workspaceRef, tenant, signal),
      ),
    retry: false,
    refetchOnWindowFocus: false,
  })
  const scripts = declared.data ?? []
  const scriptsFailed = declared.isError
  const [renaming, setRenaming] = useState<string | null>(null)
  const [closing, setClosing] = useState<TerminalController | null>(null)
  const [searching, setSearching] = useState(false)
  const [links, setLinks] = useState<TerminalController | null>(null)
  const screensId = useId()
  const [menu, setMenu] = useState<{
    x: number
    y: number
    ctrl: TerminalController
  } | null>(null)

  const active = view.items.find((c) => c.key === view.active)
  const beside = view.items.find((c) => c.key === view.split)
  const activeView = useControllerView(active)

  // Closing a shell that is running asks first; one that ended closes at once, and the
  // server forgets the history a restart left of it.
  const requestClose = (c: TerminalController) => {
    if (c.getView().state === 'ended') void group.close(c)
    else setClosing(c)
  }

  // The drawer's keys, while the focus is in this panel.
  const onKeyDown = (e: ReactKeyboardEvent) => {
    const key = terminalShortcut(e.nativeEvent)
    if (!key) return
    e.preventDefault()
    if (key === 'terminal.new') group.create()
    else if (key === 'terminal.previous') group.step(-1)
    else if (key === 'terminal.next') group.step(1)
    else if (key === 'terminal.close' && active) requestClose(active)
    else if (key === 'terminal.search' && active) setSearching(true)
    else if (key === 'terminal.links' && active) setLinks(active)
  }

  // What the folder declares, then what the person added, each run as typed on a click.
  const runnable = [...scripts, ...added.map((s) => ({ ...s, source: '' }))]
  const primaryScript = runnable.find((s) => s.name === 'dev') ?? runnable[0]
  const runScript = (script: ProjectScript) => group.run(script.command)
  const scriptActions = primaryScript ? (
    <div className="flex min-w-0 max-w-full items-center gap-1">
      <Button
        size="sm"
        className="min-w-0 max-w-full shrink"
        title={primaryScript.command}
        onClick={() => runScript(primaryScript)}
      >
        <Play aria-hidden />
        <span className="truncate">
          {t('terminal.runScript', { name: primaryScript.name })}
        </span>
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            size="icon"
            variant="ghost"
            aria-label={t('terminal.scripts')}
          >
            <ChevronDown aria-hidden />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          {runnable.map((script) => (
            <DropdownMenuItem
              key={`${script.source}:${script.name}`}
              onSelect={() => runScript(script)}
            >
              {t('terminal.runScript', { name: script.name })}{' '}
              <span className="ml-2 text-text-3">{script.source}</span>
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  ) : scriptsFailed ? (
    <p role="alert" className="text-caption text-warning">
      {t('terminal.scriptsFailed')}
    </p>
  ) : null

  const problem = view.problem ? (
    <p role="alert" className="px-1 text-caption text-warning">
      {t(`terminal.problem.${view.problem.kind}`)}
    </p>
  ) : null

  if (view.items.length === 0)
    return (
      <section
        data-testid="session-terminal"
        className="flex min-w-0 flex-col gap-2"
      >
        {/* The host's bar is the pane the title row is once a terminal opens. */}
        {place || trail ? (
          <div className="flex h-9 min-w-0 shrink-0 items-center gap-1 border-b border-line bg-[var(--pane,var(--frame))] px-2">
            <span className="flex-1" />
            {place}
            {trail}
          </div>
        ) : null}
        <div className="flex min-w-0 flex-col items-start gap-2 px-1">
          {scriptActions}
          {/* AN EMPTY PANEL IS ITS ONE ACTION. What the shell is (its limits, that each
              line is recorded) is the action's hover, not a paragraph in front of it. */}
          {problem}
          <Button title={t('terminal.hint')} onClick={() => group.create()}>
            <TerminalIcon aria-hidden />
            {t('terminal.open')}
          </Button>
        </div>
      </section>
    )

  const ended = activeView.state === 'ended'
  let status = ''
  if (activeView.state === 'reconnecting') status = t('terminal.reconnecting')
  else if (activeView.state === 'starting') status = t('terminal.starting')
  else if (ended)
    status = activeView.restarted
      ? t('terminal.restarted')
      : activeView.exitCode === null
        ? t('terminal.ended')
        : t('terminal.endedWith', { code: activeView.exitCode })
  // Closing a terminal a restart ended deletes its history: its Close says so.
  const closeLabel = t(
    activeView.restarted ? 'terminal.closeWithHistory' : 'terminal.closeNamed',
    { name: activeView.title },
  )

  // One list of actions, in the title row's menu and on a right-click of a screen. A
  // right-click acts on the screen it was made on.
  const actions = (target: TerminalController | undefined) => {
    const v = target?.getView() ?? NO_VIEW
    return (
      <>
        <DropdownMenuItem onSelect={() => setSearching(true)}>
          <Search aria-hidden />
          {t('terminal.search')}
        </DropdownMenuItem>
        {runnable.map((script) => (
          <DropdownMenuItem
            key={`${script.source}:${script.name}`}
            onSelect={() => runScript(script)}
          >
            <Play aria-hidden />
            {t('terminal.runScript', { name: script.name })}
            <span className="ml-auto text-text-3">{script.source}</span>
          </DropdownMenuItem>
        ))}
        <DropdownMenuItem
          disabled={v.state !== 'live'}
          onSelect={() => target?.send('\x03')}
        >
          <Square aria-hidden />
          {t('terminal.stopScript')}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          disabled={!v.hasSelection}
          onSelect={() => {
            const text = target?.getSelection()
            if (text) void navigator.clipboard.writeText(text).catch(() => {})
          }}
        >
          <Copy aria-hidden />
          {t('terminal.copy')}
        </DropdownMenuItem>
        <DropdownMenuItem
          disabled={v.state === 'ended'}
          onSelect={() =>
            void navigator.clipboard
              .readText()
              .then((text) => target?.paste(text))
              .catch(() => {})
          }
        >
          <ClipboardPaste aria-hidden />
          {t('terminal.paste')}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => target?.selectAll()}>
          <TextCursor aria-hidden />
          {t('terminal.selectAll')}
        </DropdownMenuItem>
        {onSendToAgent ? (
          <DropdownMenuItem
            disabled={!v.hasSelection}
            title={t('terminal.sendToAgentHint')}
            onSelect={() => {
              const text = target?.getSelection()
              if (target && text) onSendToAgent(text, v.title)
            }}
          >
            <SendHorizontal aria-hidden />
            {t('terminal.sendToAgent')}
          </DropdownMenuItem>
        ) : null}
        <DropdownMenuItem onSelect={() => setLinks(target ?? null)}>
          <Link2 aria-hidden />
          {t('terminal.links')}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => target?.scrollToBottom()}>
          <ArrowDownToLine aria-hidden />
          {t('terminal.jumpToLatest')}
        </DropdownMenuItem>
      </>
    )
  }
  // A closed menu gives the keys back to the terminal it acted on.
  const refocus = (target: TerminalController | undefined) => (e: Event) => {
    e.preventDefault()
    target?.focus()
  }
  const openMenuAt =
    (ctrl: TerminalController) => (e: ReactMouseEvent<HTMLDivElement>) => {
      e.preventDefault()
      setMenu({ x: e.clientX, y: e.clientY, ctrl })
    }

  return (
    <section
      data-testid="session-terminal"
      className="flex min-h-0 min-w-0 flex-1 flex-col gap-1"
      onKeyDown={onKeyDown}
    >
      {/* ONE TITLE ROW, a pane above the screen: the terminals first, then where
          they run, quiet, then the controls. Everything else is in its menu and
          on keys. */}
      <div className="flex h-9 min-w-0 shrink-0 items-center gap-1 border-b border-line bg-[var(--pane,var(--frame))] px-2">
        <Tabs
          value={view.active ?? ''}
          onValueChange={(key) => group.select(key)}
          className="min-w-0"
        >
          <TabsList aria-label={t('terminal.list')}>
            {view.items.map((c) => (
              <TerminalTab
                key={c.key}
                ctrl={c}
                controls={screensId}
                selected={c.key === view.split}
                renaming={renaming === c.key}
                onRename={() => setRenaming(c.key)}
                onRenamed={(title) => {
                  if (title) c.rename(title)
                  setRenaming(null)
                }}
              />
            ))}
          </TabsList>
        </Tabs>
        <Button
          variant="ghost"
          size="icon"
          aria-label={t('terminal.new')}
          title={t('terminal.new')}
          onClick={() => group.create()}
        >
          <Plus aria-hidden />
        </Button>
        <span className="flex-1" />
        {place}
        {activeView.shell ? (
          <span className="shrink-0 font-mono text-caption text-text-3">
            {activeView.shell}
          </span>
        ) : null}
        <Button
          variant="ghost"
          size="icon"
          aria-label={beside ? t('terminal.unsplit') : t('terminal.split')}
          title={beside ? t('terminal.unsplit') : t('terminal.split')}
          aria-pressed={!!beside}
          onClick={() => group.toggleSplit()}
        >
          <Columns2 aria-hidden />
        </Button>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t('terminal.actions')}
              title={t('terminal.actions')}
            >
              <MoreHorizontal aria-hidden />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" onCloseAutoFocus={refocus(active)}>
            {actions(active)}
          </DropdownMenuContent>
        </DropdownMenu>
        {/* One close, for the terminal in front: a tab list holds tabs only. */}
        {active ? (
          <Button
            variant="ghost"
            size="icon"
            aria-label={closeLabel}
            title={closeLabel}
            onClick={() => requestClose(active)}
          >
            <X aria-hidden />
          </Button>
        ) : null}
        {trail}
      </div>
      {searching && active ? (
        <SearchBar ctrl={active} onDone={() => setSearching(false)} />
      ) : null}
      <p
        role="status"
        className="min-w-0 shrink-0 px-1 text-caption text-muted-foreground empty:hidden"
      >
        {status}
      </p>
      {problem}
      {ended ? (
        <div className="shrink-0 px-1">
          <Button size="sm" onClick={() => group.create()}>
            {t('terminal.again')}
          </Button>
        </div>
      ) : null}
      <div
        id={screensId}
        className={cn(
          'grid min-h-0 min-w-0 flex-1 gap-2',
          beside && 'grid-cols-2',
        )}
      >
        {active ? (
          <TerminalScreen
            key={active.key}
            ctrl={active}
            onContextMenu={openMenuAt(active)}
          />
        ) : null}
        {beside ? (
          <TerminalScreen
            key={beside.key}
            ctrl={beside}
            onContextMenu={openMenuAt(beside)}
          />
        ) : null}
      </div>
      <DropdownMenu
        open={menu !== null}
        onOpenChange={(open) => {
          if (!open) setMenu(null)
        }}
      >
        <DropdownMenuTrigger asChild>
          <span
            aria-hidden
            className="pointer-events-none fixed size-0"
            style={{ left: menu?.x ?? 0, top: menu?.y ?? 0 }}
          />
        </DropdownMenuTrigger>
        <DropdownMenuContent
          align="start"
          onCloseAutoFocus={refocus(menu?.ctrl)}
        >
          {menu ? actions(menu.ctrl) : null}
        </DropdownMenuContent>
      </DropdownMenu>
      <div
        data-testid="terminal-keys"
        className="hidden gap-1 [@media(pointer:coarse)]:flex"
      >
        {TOUCH_KEYS.map((key) => (
          <Button
            key={key.id}
            size="sm"
            disabled={ended}
            onClick={() => {
              active?.send(key.seq)
              active?.focus()
            }}
          >
            {t(`terminal.keys.${key.id}`)}
          </Button>
        ))}
      </div>
      <TerminalLinks
        ctrl={links}
        onDone={() => {
          links?.focus()
          setLinks(null)
        }}
      />
      <ConfirmDialog
        open={closing !== null}
        onOpenChange={(open) => {
          if (!open) setClosing(null)
        }}
        title={t('terminal.closeTitle', { name: closing?.getView().title })}
        description={t('terminal.closeBody')}
        confirmLabel={t('terminal.closeConfirm')}
        tone="danger"
        onConfirm={() => {
          const c = closing
          setClosing(null)
          if (c) void group.close(c)
        }}
      />
    </section>
  )
}

function TerminalTab({
  ctrl,
  controls,
  selected,
  renaming,
  onRename,
  onRenamed,
}: {
  ctrl: TerminalController
  /** The id of the screens the tab shows. */
  controls: string
  /** Shown beside the active terminal. */
  selected: boolean
  renaming: boolean
  onRename: () => void
  onRenamed: (title: string | null) => void
}) {
  const { t } = useTranslation('sessions')
  const view = useControllerView(ctrl)
  return renaming ? (
    <Input
      autoFocus
      aria-label={t('terminal.rename')}
      defaultValue={view.title}
      className="h-7 w-32"
      onBlur={(e) => onRenamed(e.currentTarget.value)}
      onKeyDown={(e) => {
        if (e.key === 'Enter') onRenamed(e.currentTarget.value)
        else if (e.key === 'Escape') onRenamed(null)
      }}
    />
  ) : (
    <TabsTrigger
      value={ctrl.key}
      aria-controls={controls}
      title={t('terminal.renameHint')}
      className={cn('max-w-40 truncate px-2', selected && 'bg-active')}
      onDoubleClick={onRename}
      onKeyDown={(e) => {
        if (e.key === 'F2') onRename()
      }}
    >
      {/* An ended terminal says so in words, which stay when a long name is cut. */}
      <span className="truncate">{view.title}</span>{' '}
      {view.state === 'ended' ? (
        <span className="shrink-0 text-caption">{t('terminal.endedMark')}</span>
      ) : null}
    </TabsTrigger>
  )
}

/** The links on screen, for a person on the keyboard. */
function TerminalLinks({
  ctrl,
  onDone,
}: {
  ctrl: TerminalController | null
  onDone: () => void
}) {
  const { t } = useTranslation('sessions')
  const links = ctrl?.visibleLinks() ?? []
  return (
    <CommandDialog
      open={ctrl !== null}
      onOpenChange={(open) => {
        if (!open) onDone()
      }}
      title={t('terminal.links')}
      description={t('terminal.linksHint')}
    >
      <CommandInput placeholder={t('terminal.links')} />
      <CommandList>
        <CommandEmpty>{t('terminal.noLinks')}</CommandEmpty>
        {links.map((link, i) => (
          <CommandItem
            key={`${i}:${link.href}`}
            value={`${i} ${link.text}`}
            onSelect={() => {
              openTerminalLink(link)
              onDone()
            }}
          >
            {link.text}
          </CommandItem>
        ))}
      </CommandList>
    </CommandDialog>
  )
}

/** One screen: it lends the controller's xterm a place and follows the place's size. */
function TerminalScreen({
  ctrl,
  onContextMenu,
}: {
  ctrl: TerminalController
  onContextMenu: (e: ReactMouseEvent<HTMLDivElement>) => void
}) {
  const { t } = useTranslation('sessions')
  const host = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = host.current
    if (!el) return
    void ctrl.mount(el).then(() => ctrl.focus())
    const resized = new ResizeObserver(() => ctrl.fit())
    resized.observe(el)
    return () => {
      resized.disconnect()
      ctrl.unmount(el)
    }
  }, [ctrl])
  // The input's name follows the language without restarting the terminal.
  const label = t('terminal.input')
  useEffect(() => {
    ctrl.setInputLabel(label)
  }, [ctrl, label])
  return (
    <div
      ref={host}
      role="group"
      aria-label={t('terminal.label')}
      data-testid="terminal-screen"
      onContextMenu={onContextMenu}
      className="min-h-0 min-w-0 overflow-hidden bg-[var(--well,var(--frame))] px-4.5 py-3"
    />
  )
}

function SearchBar({
  ctrl,
  onDone,
}: {
  ctrl: TerminalController
  onDone: () => void
}) {
  const { t } = useTranslation('sessions')
  const [query, setQuery] = useState('')
  return (
    <div className="flex shrink-0 items-center gap-1 px-1" role="search">
      <Input
        autoFocus
        value={query}
        aria-label={t('terminal.searchLabel')}
        className="max-w-64"
        onChange={(e) => {
          setQuery(e.target.value)
          if (e.target.value) ctrl.findNext(e.target.value)
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            if (e.shiftKey) ctrl.findPrevious(query)
            else ctrl.findNext(query)
          } else if (e.key === 'Escape') {
            ctrl.clearSearch()
            onDone()
            ctrl.focus()
          }
        }}
      />
      <Button
        variant="ghost"
        size="icon"
        aria-label={t('terminal.searchPrevious')}
        onClick={() => ctrl.findPrevious(query)}
      >
        <ChevronUp aria-hidden />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        aria-label={t('terminal.searchNext')}
        onClick={() => ctrl.findNext(query)}
      >
        <ChevronDown aria-hidden />
      </Button>
    </div>
  )
}
