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
import {
  ArrowDownToLine,
  ChevronDown,
  ChevronUp,
  ClipboardPaste,
  Columns2,
  Copy,
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
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import './i18n'
import { projectScripts, type ProjectScript } from './project-scripts'
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
  /** Offered when the host can carry selected output to the agent's message. */
  onSendToAgent?: (text: string, title: string) => void
}

/** The terminals of one folder for the signed-in person. Another folder is another group. */
export function useTerminalGroup(workspaceRef: string): TerminalGroup {
  const tenant = useTenantStore((s) => s.activeTenant)
  const generation = useSessionStore((s) => s.credentialGeneration)
  const { principal } = useAuth()
  const person = principal?.user_id
    ? JSON.stringify([principal.kind, principal.user_id])
    : null
  return terminalGroup(tenant, generation, person, workspaceRef)
}

export function SessionTerminal(props: SessionTerminalProps) {
  const group = useTerminalGroup(props.workspaceRef)
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
  lead,
  trail,
}: {
  group: TerminalGroup
  onSendToAgent?: (text: string, title: string) => void
  /** The host's own controls, at the start and the end of the title row. */
  lead?: ReactNode
  trail?: ReactNode
}) {
  const { t } = useTranslation('sessions')
  const view = useSyncExternalStore(group.subscribe, group.getView)
  const { tenant, workspaceRef } = group
  const [scripts, setScripts] = useState<ProjectScript[]>([])
  const [scriptsFailed, setScriptsFailed] = useState(false)
  const [renaming, setRenaming] = useState<string | null>(null)
  const [closing, setClosing] = useState<TerminalController | null>(null)
  const [searching, setSearching] = useState(false)
  const [menu, setMenu] = useState<{
    x: number
    y: number
    ctrl: TerminalController
  } | null>(null)

  useEffect(() => {
    const ctl = new AbortController()
    void projectScripts(workspaceRef, tenant, ctl.signal).then(
      (items) => {
        if (!ctl.signal.aborted) setScripts(items)
      },
      () => {
        if (!ctl.signal.aborted) setScriptsFailed(true)
      },
    )
    return () => ctl.abort()
  }, [workspaceRef, tenant])

  const active = view.items.find((c) => c.key === view.active)
  const beside = view.items.find((c) => c.key === view.split)
  const activeView = useControllerView(active)

  // Closing a shell that is running asks first; one that ended closes at once.
  const requestClose = (c: TerminalController) => {
    if (c.getView().state === 'ended') group.remove(c)
    else setClosing(c)
  }

  // The drawer's keys, while the focus is in this panel.
  const onKeyDown = (e: ReactKeyboardEvent) => {
    const key = terminalShortcut(e.nativeEvent)
    if (!key) return
    e.preventDefault()
    if (key === 'new') group.create()
    else if (key === 'previous') group.step(-1)
    else if (key === 'next') group.step(1)
    else if (key === 'close' && active) requestClose(active)
    else if (key === 'search' && active) setSearching(true)
  }

  const primaryScript = scripts.find((s) => s.name === 'dev') ?? scripts[0]
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
          {scripts.map((script) => (
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
        className="flex min-w-0 flex-col items-start gap-2 px-1"
      >
        {lead || trail ? (
          <div className="flex h-9 w-full min-w-0 shrink-0 items-center gap-1">
            {lead}
            <span className="flex-1" />
            {trail}
          </div>
        ) : null}
        {scriptActions}
        {/* AN EMPTY PANEL IS ITS ONE ACTION. What the shell is (its limits, that each line
            is recorded) is the action's hover, not a paragraph in front of it. */}
        {problem}
        <Button title={t('terminal.hint')} onClick={() => group.create()}>
          <TerminalIcon aria-hidden />
          {t('terminal.open')}
        </Button>
      </section>
    )

  const ended = activeView.state === 'ended'
  let status = ''
  if (activeView.state === 'reconnecting') status = t('terminal.reconnecting')
  else if (activeView.state === 'starting') status = t('terminal.starting')
  else if (ended)
    status =
      activeView.exitCode === null
        ? t('terminal.ended')
        : t('terminal.endedWith', { code: activeView.exitCode })

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
        {scripts.map((script) => (
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
      {/* ONE TITLE ROW. Everything else is in its menu and on keys. */}
      <div className="flex h-9 min-w-0 shrink-0 items-center gap-1 px-1">
        {lead}
        <div
          role="tablist"
          aria-label={t('terminal.list')}
          className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto"
        >
          {view.items.map((c) => (
            <TerminalTab
              key={c.key}
              ctrl={c}
              selected={c.key === view.active || c.key === view.split}
              current={c.key === view.active}
              renaming={renaming === c.key}
              onSelect={() => group.select(c.key)}
              onRename={() => setRenaming(c.key)}
              onRenamed={(title) => {
                if (title) c.rename(title)
                setRenaming(null)
              }}
              onClose={() => requestClose(c)}
            />
          ))}
        </div>
        <Button
          variant="ghost"
          size="icon"
          aria-label={t('terminal.new')}
          title={t('terminal.new')}
          onClick={() => group.create()}
        >
          <Plus aria-hidden />
        </Button>
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
        {trail}
      </div>
      {searching && active ? (
        <SearchBar ctrl={active} onDone={() => setSearching(false)} />
      ) : null}
      <p
        role="status"
        className="min-w-0 shrink-0 truncate px-1 text-caption text-muted-foreground empty:hidden"
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
          if (c) {
            void c.close()
            group.remove(c)
          }
        }}
      />
    </section>
  )
}

function TerminalTab({
  ctrl,
  selected,
  current,
  renaming,
  onSelect,
  onRename,
  onRenamed,
  onClose,
}: {
  ctrl: TerminalController
  selected: boolean
  current: boolean
  renaming: boolean
  onSelect: () => void
  onRename: () => void
  onRenamed: (title: string | null) => void
  onClose: () => void
}) {
  const { t } = useTranslation('sessions')
  const view = useControllerView(ctrl)
  return (
    <span
      className={cn(
        'flex shrink-0 items-center gap-1 rounded-ctl border border-transparent px-1',
        selected && 'border-line bg-active',
      )}
    >
      {renaming ? (
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
        <button
          type="button"
          role="tab"
          aria-selected={current}
          title={t('terminal.renameHint')}
          className="max-w-40 truncate py-1 text-caption focus-visible:ring-2 focus-visible:ring-focus"
          onClick={onSelect}
          onDoubleClick={onRename}
          onKeyDown={(e) => {
            if (e.key === 'F2') onRename()
          }}
        >
          {view.title}
        </button>
      )}
      <Button
        variant="ghost"
        size="icon"
        className="size-6"
        aria-label={t('terminal.closeNamed', { name: view.title })}
        onClick={onClose}
      >
        <X aria-hidden />
      </Button>
    </span>
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
      className="min-h-0 min-w-0 overflow-hidden bg-background pl-2"
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
