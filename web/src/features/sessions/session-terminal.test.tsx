// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The terminal tab: it starts nothing until a person asks, hands the terminal's window size
// to the server, carries what is typed and what is printed, and ends cleanly. xterm.js draws
// into a real browser's layout, which jsdom does not have, so a recording double stands in
// for it; the component, the connection hook and the stream parser are the production ones.
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
  }
  return { terms: [] as Fake[] }
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
    paste() {}
    hasSelection() {
      return false
    }
    getSelection() {
      return ''
    }
    onData(cb: (d: string) => void) {
      this.data.push(cb)
      return { dispose() {} }
    }
    onResize(cb: (s: { rows: number; cols: number }) => void) {
      this.resized.push(cb)
      return { dispose() {} }
    }
    onSelectionChange() {
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
vi.mock('@xterm/xterm/css/xterm.css', () => ({}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    startTerminal: vi.fn(),
    terminalInput: vi.fn(),
    terminalSize: vi.fn(),
    closeTerminal: vi.fn(),
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import i18n from '@/lib/i18n'
import { ApiError } from '@/lib/api/errors'
import { SessionTerminal } from './session-terminal'

let push: (text: string) => void = () => {}
let end: () => void = () => {}
let streams = 0
let nextStatus: number[] = []

beforeEach(() => {
  vi.clearAllMocks()
  window.sessionStorage.clear()
  xterm.terms.length = 0
  streams = 0
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
    return new Response(body, {
      status: 200,
      headers: { 'Content-Type': 'text/event-stream' },
    })
  }) as typeof fetch)
  vi.mocked(agentOpsApi.startTerminal).mockResolvedValue({
    terminal: 'term_1',
    confinement: 'landlock',
    idle_seconds: 900,
    reconnect_seconds: 60,
  })
  vi.mocked(agentOpsApi.terminalInput).mockResolvedValue({})
  vi.mocked(agentOpsApi.terminalSize).mockResolvedValue({})
  vi.mocked(agentOpsApi.closeTerminal).mockResolvedValue({ closed: true })
})

afterEach(() => {
  vi.restoreAllMocks()
})

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
    expect(
      screen.getByRole('button', { name: 'Open terminal' }),
    ).toBeInTheDocument()
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

  it('has one close button, which ends the shell and returns to the idle panel', async () => {
    const user = await openIt()
    expect(
      screen.getAllByRole('button', { name: 'Close terminal' }),
    ).toHaveLength(1)
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
    await user.click(
      screen.getByRole('button', { name: 'Open a new terminal' }),
    )
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
    window.sessionStorage.setItem('olivares.terminal..ws_1', 'term_gone')
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

  it('takes back the shell this tab had before a reload without a click', async () => {
    window.sessionStorage.setItem('olivares.terminal..ws_1', 'term_7')
    render(<SessionTerminal workspaceRef="ws_1" />)
    await waitFor(() => expect(streams).toBe(1))
    expect(agentOpsApi.startTerminal).not.toHaveBeenCalled()
    act(() => push(frame(0, 'still here')))
    await waitFor(() => expect(xterm.terms[0].written).toEqual(['still here']))
    act(() => end())
  })
})
