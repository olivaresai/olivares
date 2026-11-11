// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TERMINALS OF A FOLDER, kept outside the screen.
//
// A shell lives on the server (modules/sessions/terminal.go); here lives what a person sees
// of it: the xterm.js screen with its scrollback, and the stream that feeds it. Both belong
// to a controller in a module-level registry, not to a React component, so leaving a
// session and coming back finds the same screens, scrollback and shells. A view only
// borrows a controller's screen (`mount`) and gives it back (`unmount`).
//
// Output arrives as server-sent events with the offset each chunk starts at, so a dropped
// connection resumes where it stopped. Input goes out as plain requests, one at a time and
// in order, so what is typed arrives as it was typed.
//
// A shell has no time limit on the server: it runs until the person closes it, so the ids
// are remembered in this browser (not this tab) and a terminal left for an hour, or in a
// tab that was closed, is found again with its process and its output.
//
// ⛔ A RECONNECT NEVER STARTS A SHELL. Only `start` does, and only because a person asked
//    (a button, or a command they chose to run). A remembered id that the server no longer
//    holds is forgotten quietly.
import type { FitAddon } from '@xterm/addon-fit'
import type { SearchAddon } from '@xterm/addon-search'
import type { Terminal as XTerm } from '@xterm/xterm'
import { agentOpsApi } from '@/features/agentops/api'
import { subscribeStream } from '@/features/shared/sse'
import { ApiError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { terminalTheme } from './terminal-theme'
import { unsafeCommand } from './run-in-terminal'
import { isCopyChord, terminalShortcut } from './terminal-shortcuts'

export type TerminalState =
  'pending' | 'starting' | 'live' | 'reconnecting' | 'ended'

export interface TerminalProblem {
  /** `step_up` needs the person to confirm their identity first; the rest is shown as is. */
  kind: 'step_up' | 'refused' | 'busy' | 'failed' | 'unsafe'
}

export interface TerminalView {
  title: string
  state: TerminalState
  exitCode: number | null
  hasSelection: boolean
}

const FIRST_RETRY_MS = 500
const RESIZE_DEBOUNCE_MS = 120
const MAX_RETRY_MS = 5000
const SCROLLBACK_LINES = 10_000
/** How long after the person last acted in a terminal a program may set the clipboard. */
const CLIPBOARD_GESTURE_MS = 5000

/** A program may put text on the clipboard (OSC 52) only while the person is at this terminal. */
export function clipboardWriteAllowed(
  now: number,
  lastGesture: number | null,
  focused: boolean,
): boolean {
  return (
    focused && lastGesture !== null && now - lastGesture <= CLIPBOARD_GESTURE_MS
  )
}

export function problemOf(err: unknown): TerminalProblem {
  if (err instanceof ApiError) {
    if (err.code === 'step_up_required') return { kind: 'step_up' }
    if (err.status === 429) return { kind: 'busy' }
    if (err.status === 403 || err.status === 404) return { kind: 'refused' }
  }
  return { kind: 'failed' }
}

function decode(b64: string): Uint8Array {
  const raw = atob(b64)
  const bytes = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i)
  return bytes
}

/** What a controller needs from its group. */
interface Owner {
  readonly workspaceRef: string
  readonly tenant: string | null
  changed: () => void
  /** A start the server refused (dropped), or an input it did not take (kept). */
  refused: (
    c: TerminalController,
    problem: TerminalProblem,
    drop?: boolean,
  ) => void
  /** A remembered id the server no longer holds. */
  forget: (c: TerminalController) => void
}

let seq = 0

export class TerminalController {
  readonly key = `t${++seq}`
  private view: TerminalView
  private readonly listeners = new Set<() => void>()
  private serverId: string | null = null
  private offset = 0
  private resuming = false
  private stream: AbortController | null = null
  private unsent = ''
  private sending = false
  private resizeTimer: ReturnType<typeof setTimeout> | undefined
  private disposed = false
  /** Commands chosen before the shell was live; they run once it is. */
  private queued: string[] = []
  /** When the person last typed, clicked or pasted in this terminal, or chose a command. */
  private gestureAt: number | null = null

  private ready: Promise<XTerm> | null = null
  private term: XTerm | null = null
  private fitAddon: FitAddon | null = null
  private searchAddon: SearchAddon | null = null
  private opened = false
  private inputLabel = ''
  private readonly container = document.createElement('div')

  private readonly owner: Owner

  constructor(owner: Owner, title: string, remembered?: string) {
    this.owner = owner
    this.container.className = 'h-full w-full'
    // Only the person acts through the page's events; what a program prints cannot.
    const gesture = () => (this.gestureAt = Date.now())
    for (const type of ['keydown', 'pointerdown', 'paste'])
      this.container.addEventListener(type, gesture, true)
    this.view = {
      title,
      state: remembered ? 'reconnecting' : 'pending',
      exitCode: null,
      hasSelection: false,
    }
    if (remembered) {
      this.serverId = remembered
      this.resuming = true
      void this.ensureTerm().then(() => this.attach(remembered))
    }
  }

  // --- what a view reads -------------------------------------------------------------

  subscribe = (listener: () => void) => {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }
  getView = () => this.view

  /** The server's id, to remember the terminal across a reload. */
  get id(): string | null {
    return this.serverId
  }

  getSelection(): string {
    return this.term?.getSelection() ?? ''
  }

  private set(patch: Partial<TerminalView>) {
    this.view = { ...this.view, ...patch }
    this.listeners.forEach((l) => l())
    this.owner.changed()
  }

  rename(title: string) {
    if (title.trim()) this.set({ title: title.trim() })
  }

  // --- the screen --------------------------------------------------------------------

  private ensureTerm(): Promise<XTerm> {
    this.ready ??= (async () => {
      const [
        { Terminal },
        { FitAddon },
        { SearchAddon },
        { WebLinksAddon },
        { Unicode11Addon },
        { ClipboardAddon },
      ] = await Promise.all([
        import('@xterm/xterm'),
        import('@xterm/addon-fit'),
        import('@xterm/addon-search'),
        import('@xterm/addon-web-links'),
        import('@xterm/addon-unicode11'),
        import('@xterm/addon-clipboard'),
        import('@xterm/xterm/css/xterm.css'),
      ])
      const term = new Terminal({
        cursorBlink: true,
        fontFamily:
          'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
        fontSize: 13,
        scrollback: SCROLLBACK_LINES,
        allowProposedApi: true,
        // The WebGL renderer paints on a canvas: this keeps the rows as text a screen
        // reader (and the page) can read.
        screenReaderMode: true,
        theme: terminalTheme(),
      })
      this.fitAddon = new FitAddon()
      this.searchAddon = new SearchAddon()
      term.loadAddon(this.fitAddon)
      term.loadAddon(this.searchAddon)
      term.loadAddon(new Unicode11Addon())
      term.unicode.activeVersion = '11'
      // Only http(s) links open, in a new tab that cannot reach this page.
      term.loadAddon(
        new WebLinksAddon((_event, uri) => {
          if (/^https?:\/\//i.test(uri))
            window.open(uri, '_blank', 'noopener,noreferrer')
        }),
      )
      // A program may put text on the clipboard (OSC 52) only while the person is at this
      // terminal, shortly after they acted in it; it may never read it.
      term.loadAddon(
        new ClipboardAddon(undefined, {
          readText: async () => '',
          writeText: async (_selection, text) => {
            const focused =
              document.hasFocus() &&
              this.container.contains(document.activeElement)
            if (!clipboardWriteAllowed(Date.now(), this.gestureAt, focused))
              return
            await navigator.clipboard.writeText(text)
          },
        }),
      )
      term.attachCustomKeyEventHandler((e) => {
        if (e.type !== 'keydown') return true
        if (isCopyChord(e)) {
          const text = term.getSelection()
          if (text) void navigator.clipboard.writeText(text).catch(() => {})
          return false
        }
        // Reading the scrollback keeps the shell's own Ctrl+A, Home and End.
        if (e.ctrlKey && e.shiftKey && !e.altKey) {
          if (e.code === 'KeyA') {
            term.selectAll()
            return false
          }
          if (e.code === 'Home' || e.code === 'End') {
            if (e.code === 'Home') term.scrollToTop()
            else term.scrollToBottom()
            return false
          }
        }
        // The drawer's own keys are not typed into the shell.
        return terminalShortcut(e) === null
      })
      term.onData((data) => this.send(data))
      term.onResize(({ rows, cols }) => this.resize(rows, cols))
      term.onSelectionChange(() =>
        this.set({ hasSelection: term.hasSelection() }),
      )
      this.term = term
      return term
    })()
    return this.ready
  }

  /** Shows the screen in `host`, and starts the shell if this terminal waits for one. */
  async mount(host: HTMLElement): Promise<void> {
    const term = await this.ensureTerm()
    if (this.disposed) return
    host.appendChild(this.container)
    if (!this.opened) {
      this.opened = true
      term.open(this.container)
      if (this.inputLabel)
        term.textarea?.setAttribute('aria-label', this.inputLabel)
      void this.enableWebgl(term)
    }
    this.fit()
    if (this.view.state === 'pending') void this.start(term.rows, term.cols)
  }

  /** Gives the screen back; the terminal and its scrollback stay. */
  unmount(host: HTMLElement) {
    if (this.container.parentElement === host) host.removeChild(this.container)
  }

  private async enableWebgl(term: XTerm) {
    try {
      const { WebglAddon } = await import('@xterm/addon-webgl')
      if (this.disposed) return
      const webgl = new WebglAddon()
      webgl.onContextLoss(() => webgl.dispose())
      term.loadAddon(webgl)
    } catch {
      // No WebGL here: xterm's own renderer draws it.
    }
  }

  fit() {
    if (!this.container.isConnected) return
    try {
      this.fitAddon?.fit()
    } catch {
      // The host has no size yet; the next resize fits it.
    }
  }

  focus() {
    this.term?.focus()
  }

  recolor() {
    if (this.term) this.term.options.theme = terminalTheme()
  }

  setInputLabel(label: string) {
    this.inputLabel = label
    this.term?.textarea?.setAttribute('aria-label', label)
  }

  paste(text: string) {
    this.term?.paste(text)
  }

  selectAll() {
    this.term?.selectAll()
  }

  scrollToBottom() {
    this.term?.scrollToBottom()
  }

  findNext(query: string): boolean {
    return this.searchAddon?.findNext(query) ?? false
  }
  findPrevious(query: string): boolean {
    return this.searchAddon?.findPrevious(query) ?? false
  }
  clearSearch() {
    this.searchAddon?.clearDecorations()
  }

  // --- the connection ----------------------------------------------------------------

  private end(code: number | null) {
    this.stream?.abort()
    this.stream = null
    this.serverId = null
    this.unsent = ''
    this.queued = []
    this.set({ state: 'ended', exitCode: code })
  }

  // Reads the output until the shell ends. A dropped connection is retried, from the offset
  // already shown, for as long as the server still holds the shell (it answers 404 or 410
  // once it does not): the server has no time limit, so neither has the view.
  private attach(id: string) {
    this.stream?.abort()
    const ctl = new AbortController()
    this.stream = ctl
    this.serverId = id
    const run = async () => {
      let wait = FIRST_RETRY_MS
      while (!ctl.signal.aborted) {
        try {
          await subscribeStream({
            path: `/v1/m/sessions/workspaces/${encodeURIComponent(this.owner.workspaceRef)}/terminals/${encodeURIComponent(id)}/stream`,
            query: { from: String(this.offset) },
            token: useSessionStore.getState().csrfToken,
            tenant: this.owner.tenant,
            signal: ctl.signal,
            onOpen: () => {
              wait = FIRST_RETRY_MS
              this.resuming = false
              this.set({ state: 'live' })
              this.runQueued()
            },
            onMessage: (msg) => {
              if (ctl.signal.aborted) return
              if (msg.event === 'exit') {
                const body = JSON.parse(msg.data) as { code?: number }
                this.end(body.code ?? null)
                return
              }
              if (msg.event !== 'out') return
              const frame = JSON.parse(msg.data) as { o: number; d: string }
              const bytes = decode(frame.d)
              // Bytes at or before the offset were shown already.
              const skip = Math.max(0, this.offset - frame.o)
              if (skip >= bytes.length) return
              this.offset = frame.o + bytes.length
              this.term?.write(skip ? bytes.subarray(skip) : bytes)
            },
          })
          if (this.serverId !== id) return // ended by an exit frame
        } catch (err) {
          if (ctl.signal.aborted) return
          const status = (err as { status?: number }).status
          if (status === 404 || status === 410) {
            // A shell the server no longer holds: a reload that remembered it forgets it.
            if (this.resuming) this.owner.forget(this)
            else this.end(null)
            return
          }
        }
        if (ctl.signal.aborted) return
        this.set({ state: 'reconnecting' })
        await new Promise((r) => setTimeout(r, wait))
        wait = Math.min(wait * 2, MAX_RETRY_MS)
      }
    }
    void run()
  }

  /** Starts the shell in the folder; the size is the window the screen already has. */
  async start(rows: number, cols: number): Promise<void> {
    this.set({ state: 'starting' })
    this.offset = 0
    try {
      const started = await agentOpsApi.startTerminal(this.owner.workspaceRef, {
        rows,
        cols,
      })
      if (this.disposed) {
        void agentOpsApi
          .closeTerminal(this.owner.workspaceRef, started.terminal)
          .catch(() => {})
        return
      }
      this.resuming = false
      this.attach(started.terminal)
    } catch (err) {
      // A refused start leaves nothing behind: the group says why.
      this.owner.refused(this, problemOf(err))
    }
  }

  private async flush() {
    if (this.sending) return
    this.sending = true
    try {
      while (this.unsent && this.serverId) {
        // UTF-8 needs at most three bytes per UTF-16 code unit. Keep each
        // request below the server's 64 KiB limit, including non-ASCII paste.
        let chunkEnd = Math.min(this.unsent.length, Math.floor(65536 / 3))
        const last = this.unsent.charCodeAt(chunkEnd - 1)
        if (chunkEnd < this.unsent.length && last >= 0xd800 && last <= 0xdbff)
          chunkEnd--
        const data = this.unsent.slice(0, chunkEnd)
        this.unsent = this.unsent.slice(chunkEnd)
        try {
          await agentOpsApi.terminalInput(
            this.owner.workspaceRef,
            this.serverId,
            data,
          )
        } catch (err) {
          if (
            err instanceof ApiError &&
            (err.status === 404 || err.status === 410)
          )
            this.end(null)
          else this.owner.refused(this, problemOf(err), false)
          return
        }
      }
    } finally {
      this.sending = false
    }
  }

  send(data: string) {
    if (!this.serverId) return
    this.unsent += data
    void this.flush()
  }

  /** Types a command into the shell and runs it; before the shell is live, once it is. */
  run(command: string) {
    if (this.view.state === 'ended') return
    this.gestureAt = Date.now() // the person chose this command
    if (this.view.state !== 'live') {
      this.queued.push(command)
      return
    }
    this.runNow(command)
  }

  private runNow(command: string) {
    // ^U clears what was typed; paste keeps a multi-line command in one piece (bracketed
    // paste where the shell asks for it); Enter runs it.
    this.send('\x15')
    this.term?.paste(command)
    this.send('\r')
    this.term?.focus()
  }

  private runQueued() {
    const all = this.queued
    this.queued = []
    all.forEach((c) => this.runNow(c))
  }

  private resize(rows: number, cols: number) {
    clearTimeout(this.resizeTimer)
    this.resizeTimer = setTimeout(() => {
      if (this.serverId)
        void agentOpsApi
          .terminalSize(this.owner.workspaceRef, this.serverId, { rows, cols })
          .catch(() => {})
    }, RESIZE_DEBOUNCE_MS)
  }

  /** Ends the shell on the server and everything it started. */
  async close(): Promise<void> {
    const id = this.serverId
    if (this.view.state !== 'ended') this.end(null)
    if (id) {
      try {
        await agentOpsApi.closeTerminal(this.owner.workspaceRef, id)
      } catch {
        // The server ends an unattended shell on its own within the reconnect window.
      }
    }
  }

  /** Lets go of the screen and the connection; the shell is left to the server's window. */
  dispose() {
    this.disposed = true
    this.stream?.abort()
    this.stream = null
    clearTimeout(this.resizeTimer)
    this.container.remove()
    this.term?.dispose()
    this.term = null
  }
}

// --- the group of one folder ---------------------------------------------------------

export interface GroupView {
  items: readonly TerminalController[]
  active: string | null
  /** The terminal shown beside the active one, if any. */
  split: string | null
  problem: TerminalProblem | null
}

interface Remembered {
  items: { id: string; title: string }[]
  active: number
}

/** The list belongs to one person in one tenant and folder; without a person nothing is kept. */
const storageKey = (
  person: string | null,
  tenant: string | null,
  ref: string,
) =>
  person
    ? `olivares.terminals.v2.${JSON.stringify([person, tenant ?? '', ref])}`
    : null

function readRemembered(key: string | null): Remembered | null {
  if (!key) return null
  try {
    const raw = window.localStorage.getItem(key)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Remembered
    return Array.isArray(parsed.items) ? parsed : null
  } catch {
    return null
  }
}

export class TerminalGroup implements Owner {
  private view: GroupView = {
    items: [],
    active: null,
    split: null,
    problem: null,
  }
  private readonly listeners = new Set<() => void>()
  private count = 0
  private readonly storage: string | null
  private disposed = false

  readonly workspaceRef: string
  readonly tenant: string | null

  constructor(
    workspaceRef: string,
    tenant: string | null,
    person: string | null,
  ) {
    this.workspaceRef = workspaceRef
    this.tenant = tenant
    this.storage = storageKey(person, tenant, workspaceRef)
    const remembered = readRemembered(this.storage)
    if (remembered) {
      const items = remembered.items
        .filter((i) => typeof i.id === 'string' && typeof i.title === 'string')
        .map((i) => new TerminalController(this, i.title, i.id))
      this.count = items.length
      const active = items[remembered.active] ?? items[0] ?? null
      this.view = {
        items,
        active: active?.key ?? null,
        split: null,
        problem: null,
      }
    }
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }
  getView = () => this.view

  /** A controller's own state changed: remember the list. */
  changed = () => {
    if (!this.storage) return
    try {
      const active = this.view.items.findIndex(
        (c) => c.key === this.view.active,
      )
      const items = this.view.items.flatMap((c) =>
        c.id ? [{ id: c.id, title: c.getView().title }] : [],
      )
      if (items.length === 0) window.localStorage.removeItem(this.storage)
      else
        window.localStorage.setItem(
          this.storage,
          JSON.stringify({ items, active: Math.max(active, 0) }),
        )
    } catch {
      // A browser without storage loses the way back to a shell after a reload, nothing else.
    }
  }

  private update(patch: Partial<GroupView>) {
    this.view = { ...this.view, ...patch }
    this.listeners.forEach((l) => l())
    this.changed()
  }

  /** A new terminal waits for the person's act; its screen starts the shell. */
  create(): TerminalController {
    this.count++
    const next = new TerminalController(this, `Terminal ${this.count}`)
    this.update({
      items: [...this.view.items, next],
      active: next.key,
      problem: null,
    })
    return next
  }

  /** Shows `key`; the one that was active takes the other half of a split. */
  select(key: string) {
    if (key === this.view.active) return
    const split = this.view.split === key ? this.view.active : this.view.split
    this.update({ active: key, split })
  }

  step(by: 1 | -1) {
    const { items, active } = this.view
    if (items.length < 2) return
    const at = items.findIndex((c) => c.key === active)
    const next = items[(at + by + items.length) % items.length]
    if (next) this.select(next.key)
  }

  /** Splits: a new terminal beside the active one, or the split closed. */
  toggleSplit() {
    if (this.view.split) {
      this.update({ split: null })
      return
    }
    const former = this.view.active
    this.create()
    this.update({ split: former })
  }

  active(): TerminalController | undefined {
    return this.view.items.find((c) => c.key === this.view.active)
  }

  remove(c: TerminalController) {
    c.dispose()
    const items = this.view.items.filter((o) => o !== c)
    const at = this.view.items.indexOf(c)
    const active =
      this.view.active === c.key
        ? (items[Math.min(at, items.length - 1)]?.key ?? null)
        : this.view.active
    const split = this.view.split === c.key ? null : this.view.split
    this.update({ items, active, split: split === active ? null : split })
  }

  refused = (c: TerminalController, problem: TerminalProblem, drop = true) => {
    if (drop) this.remove(c)
    this.update({ problem })
    // The limit is full of terminals this browser may not know: show them.
    if (problem.kind === 'busy') void this.adopt()
  }

  forget = (c: TerminalController) => this.remove(c)

  dismiss() {
    if (this.view.problem) this.update({ problem: null })
  }

  /**
   * Takes in the terminals the server holds for this person in this folder that this
   * browser lost the ids of (another browser, cleared site data), so they can be used or
   * closed. It only attaches; it never starts a shell.
   */
  async adopt(): Promise<void> {
    let listed
    try {
      listed = await agentOpsApi.listTerminals(this.workspaceRef, {
        tenant: this.tenant ?? undefined,
      })
    } catch {
      return // A list that cannot be read changes nothing; the person still has their own.
    }
    // A terminal being started may be in this list before its controller knows its id.
    const starting = this.view.items.some((c) =>
      ['pending', 'starting'].includes(c.getView().state),
    )
    if (this.disposed || starting) return
    const known = new Set(this.view.items.map((c) => c.id))
    const lost = listed.items.filter((t) => !known.has(t.terminal))
    if (lost.length === 0) return
    const adopted = lost.map(
      (t) =>
        new TerminalController(this, `Terminal ${++this.count}`, t.terminal),
    )
    this.update({
      items: [...this.view.items, ...adopted],
      active: this.view.active ?? adopted[0]!.key,
    })
  }

  /** Runs a command a person chose in the active terminal, or in a new one. */
  run(command: string) {
    // A command that acts as keys is refused, and the person is told.
    if (unsafeCommand(command)) {
      this.update({ problem: { kind: 'unsafe' } })
      return
    }
    const target = this.active()
    if (target && target.getView().state !== 'ended') target.run(command)
    else this.create().run(command)
  }

  dispose() {
    this.disposed = true
    this.view.items.forEach((c) => c.dispose())
    this.view = { items: [], active: null, split: null, problem: null }
    this.listeners.forEach((l) => l())
  }
}

// --- the registry --------------------------------------------------------------------

const groups = new Map<string, TerminalGroup>()
let recolorObserver: MutationObserver | null = null

/** The group of a folder for this sign-in; the same one every time it is asked. */
export function terminalGroup(
  tenant: string | null,
  generation: number,
  person: string | null,
  workspaceRef: string,
): TerminalGroup {
  const scope = `${tenant ?? ''}:${generation}:${person ?? ''}:`
  const key = `${scope}${workspaceRef}`
  let group = groups.get(key)
  if (!group) {
    // Another sign-in or tenant never sees these terminals: their screens go.
    for (const [k, g] of groups)
      if (!k.startsWith(scope)) {
        g.dispose()
        groups.delete(k)
      }
    group = new TerminalGroup(workspaceRef, tenant, person)
    groups.set(key, group)
    void group.adopt()
    recolorObserver ??= new MutationObserver(() =>
      groups.forEach((g) => g.getView().items.forEach((c) => c.recolor())),
    )
    recolorObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class', 'data-theme'],
    })
  }
  return group
}

/** For tests: forget every group without touching the server. */
export function resetTerminalGroups() {
  groups.forEach((g) => g.dispose())
  groups.clear()
}
