// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The message navigator (#2129) against a small fake of the engine's messages read, which
// keeps the engine's rules: ids are positions, `role` and `q` and `at` filter, `order` and
// `cursor` page, `id` reads one message whole with `later_same`.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axe from 'axe-core'
import { useRef } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { messages: vi.fn() },
}))

import { agentOpsApi } from '@/features/agentops/api'
import { ApiError } from '@/lib/api/errors'
import type {
  RunMessageDTO,
  RunMessagesDTO,
  RunMessagesParams,
} from '@/features/agentops/types'
import type { ConversationItem } from './conversation-frames'
import { MessageBar, MessageRail } from './message-navigator'
import { useMessageNavigator } from './use-message-navigator'

interface Stored extends RunMessageDTO {
  text: string
}

/** Message 1..N: the person's are odd, the answers even; the 7th and 9th repeat words. */
function transcript(n: number): Stored[] {
  return Array.from({ length: n }, (_, i) => {
    const id = i + 1
    const user = id % 2 === 1
    const words =
      id === 7 || id === 9 ? 'continue' : `${user ? 'request' : 'answer'} ${id}`
    const minutes = String(id).padStart(2, '0')
    return {
      id,
      role: user ? 'user' : 'assistant',
      at: `2026-10-14T10:${minutes}:00.000Z`,
      first_line: words,
      text: `${words}\nand the rest of message ${id}`,
    } as Stored
  })
}

function engine(all: Stored[]) {
  return async (
    _ref: string,
    p: RunMessagesParams = {},
  ): Promise<RunMessagesDTO> => {
    if (p.id !== undefined) {
      const m = all.find((x) => x.id === p.id)
      if (!m) throw new Error('404')
      const later = all.filter(
        (x) =>
          x.id > m.id && x.role === m.role && x.first_line === m.first_line,
      ).length
      return {
        driver: 'claude',
        transcript: 'read',
        total: all.length,
        items: [{ ...m, ...(later > 0 ? { later_same: later } : {}) }],
      }
    }
    const matching = all.filter(
      (m) =>
        (p.role === 'all' || m.role === 'user') &&
        (!p.q || m.text.toLowerCase().includes(p.q.toLowerCase())) &&
        (!p.at || m.at! >= p.at.replace('.000', '')),
    )
    const limit = Math.min(p.limit ?? 50, 200)
    const cursor = p.cursor === undefined ? undefined : Number(p.cursor)
    let page: Stored[]
    let more: boolean
    if (p.order === 'desc') {
      const below = matching.filter(
        (m) => cursor === undefined || m.id < cursor,
      )
      page = below.slice(-limit)
      more = below.length > page.length
    } else {
      const above = matching.filter((m) => m.id > (cursor ?? 0))
      page = above.slice(0, limit)
      more = above.length > page.length
    }
    const next = more
      ? String(p.order === 'desc' ? page[0]!.id : page[page.length - 1]!.id)
      : undefined
    return {
      driver: 'claude',
      transcript: 'read',
      total: matching.length,
      items: page.map(({ text: _text, ...m }) => m),
      ...(next ? { next } : {}),
    }
  }
}

/** The value a `datetime-local` field holds for the instant `utc`: its reading in
 * this host's own zone, which is how the panel reads the field (`instantOf`). A
 * fixed string would be a different instant per zone, and the transcript's times
 * are UTC, so a test that means one instant derives what it types. */
function localFieldOf(utc: string): string {
  const d = new Date(utc)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(
    d.getHours(),
  )}:${pad(d.getMinutes())}`
}

function conversation(): ConversationItem[] {
  const rows: ConversationItem[] = []
  for (const m of transcript(12)) {
    rows.push({
      kind: m.role === 'user' ? 'operator' : 'assistant',
      id: `row-${m.id}`,
      summary: m.first_line,
      text: m.text,
      raw: [],
    })
  }
  return rows
}

function Host({
  items,
  onJump,
  runRef = 'run_1',
}: {
  items: ConversationItem[]
  onJump: () => void
  runRef?: string
}) {
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const nav = useMessageNavigator({
    runRef,
    items,
    scrollRef,
    onJump,
  })
  return (
    <div>
      <MessageBar nav={nav} />
      <div className="relative">
        <MessageRail nav={nav} />
        <div ref={scrollRef} role="log" aria-label="Conversation">
          <ol>
            {items.map((item) => (
              <li key={item.id}>
                <button data-testid="conversation-item" data-item-id={item.id}>
                  {item.text}
                </button>
              </li>
            ))}
          </ol>
        </div>
      </div>
    </div>
  )
}

function show(
  all: Stored[] = transcript(12),
  items: ConversationItem[] = conversation(),
) {
  vi.mocked(agentOpsApi.messages).mockImplementation(engine(all))
  const onJump = vi.fn()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const view = render(
    <QueryClientProvider client={qc}>
      <Host items={items} onJump={onJump} />
    </QueryClientProvider>,
  )
  const idle = () => waitFor(() => expect(qc.isFetching()).toBe(0))
  return { ...view, onJump, qc, idle }
}

/** A session whose index read fails the way the engine says it does. */
function showFailing(error: unknown) {
  vi.mocked(agentOpsApi.messages).mockRejectedValue(error)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <Host items={conversation()} onJump={vi.fn()} />
    </QueryClientProvider>,
  )
  return () => waitFor(() => expect(qc.isFetching()).toBe(0))
}

const scrolled = vi.fn()

beforeEach(() => {
  scrolled.mockReset()
  Element.prototype.scrollIntoView = scrolled
})
afterEach(() => {
  cleanup()
  vi.mocked(agentOpsApi.messages).mockReset()
})

const lastAsk = () => {
  const calls = vi.mocked(agentOpsApi.messages).mock.calls
  return calls[calls.length - 1]![1]
}

describe('MessageNavigator rail', () => {
  it('has one mark per message the person sent, named by its time and first line', async () => {
    show()
    const marks = await screen.findAllByTestId('message-mark')
    expect(marks).toHaveLength(6)
    expect(marks[0]).toHaveAccessibleName(/Message 1, .*request 1/)
    expect(marks[5]).toHaveAccessibleName(/Message 11, .*request 11/)
    expect(
      screen.getByRole('navigation', { name: 'Messages you sent' }),
    ).toBeInTheDocument()
  })

  it('previews a message on hover and on focus', async () => {
    const user = userEvent.setup()
    show()
    const marks = await screen.findAllByTestId('message-mark')
    await user.hover(marks[2]!)
    expect(await screen.findByTestId('message-preview')).toHaveTextContent(
      'request 5',
    )
    await user.unhover(marks[2]!)
    expect(screen.queryByTestId('message-preview')).not.toBeInTheDocument()
    marks[4]!.focus()
    expect(await screen.findByTestId('message-preview')).toHaveTextContent(
      'continue',
    )
  })

  it('scrolls to the row and highlights it, and tells the conversation to stop following', async () => {
    const user = userEvent.setup()
    const { onJump } = show()
    const marks = await screen.findAllByTestId('message-mark')
    await user.click(marks[1]!)
    const row = (await screen.findAllByTestId('conversation-item'))[2]!
    await waitFor(() => expect(row.closest('li')).toHaveClass('ring-2'))
    expect(scrolled).toHaveBeenCalledTimes(1)
    expect(onJump).toHaveBeenCalledTimes(1)
    expect(row.closest('li')).toHaveFocus()
    expect(screen.getByRole('status')).toHaveTextContent('Jumped to message 3.')
    // Only that row is marked.
    expect(document.querySelectorAll('.ring-2')).toHaveLength(1)
  })

  it('is reachable and operable by keyboard alone', async () => {
    const user = userEvent.setup()
    show()
    await screen.findAllByTestId('message-mark')
    await user.tab() // the bar's toggle
    await user.tab() // the first mark of the rail
    expect(screen.getAllByTestId('message-mark')[0]).toHaveFocus()
    await user.keyboard('{Enter}')
    await waitFor(() =>
      expect(
        screen.getAllByTestId('conversation-item')[0]!.closest('li'),
      ).toHaveClass('ring-2'),
    )
  })

  it('stands for a long session with a few marks and keeps the newest', async () => {
    show(transcript(2000), conversation())
    const marks = await screen.findAllByTestId('message-mark')
    expect(marks).toHaveLength(24)
    expect(marks[23]).toHaveAccessibleName(/Message 1.?999,/)
    // The index is one page of the newest messages, not the transcript.
    expect(vi.mocked(agentOpsApi.messages).mock.calls[0]![1]).toMatchObject({
      order: 'desc',
      limit: 200,
    })
  })

  it('draws nothing for a session without messages, or one whose tool keeps no transcript to read', async () => {
    // A positive control first: the same wait sees the navigator of a session with messages.
    const control = show()
    await control.idle()
    expect(screen.getByTestId('message-bar')).toBeInTheDocument()
    control.unmount()

    const empty = show([])
    await empty.idle()
    expect(screen.queryByTestId('message-bar')).not.toBeInTheDocument()
    expect(screen.queryByTestId('message-rail')).not.toBeInTheDocument()
    expect(screen.queryByTestId('message-problem')).not.toBeInTheDocument()
    empty.unmount()

    const idle = showFailing(new ApiError(422, 'unprocessable', 'another tool'))
    await idle()
    expect(screen.queryByTestId('message-bar')).not.toBeInTheDocument()
    expect(screen.queryByTestId('message-problem')).not.toBeInTheDocument()
  })

  it('counts no messages while the conversation shows none (dev#3120)', async () => {
    // The 0.3 empty session read "Messages 2" beside "No turns yet": a count of rows
    // the person could not see. The control above shows the bar over shown rows.
    const empty = show(transcript(2), [])
    await empty.idle()
    expect(screen.queryByTestId('message-bar')).not.toBeInTheDocument()
    expect(screen.queryByText('Messages')).not.toBeInTheDocument()
    expect(screen.queryByTestId('message-rail')).not.toBeInTheDocument()
  })

  it('says nothing of a missing transcript while the conversation shows no message (dev#3120)', async () => {
    // A session stopped before its first turn has no transcript yet: over its empty
    // conversation "not in its account home" was a second, contradictory state.
    vi.mocked(agentOpsApi.messages).mockResolvedValue({
      driver: 'claude',
      transcript: 'missing',
      total: 0,
      items: [],
    })
    const qc = new QueryClient()
    render(
      <QueryClientProvider client={qc}>
        <Host items={[]} onJump={vi.fn()} />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(qc.isFetching()).toBe(0))
    expect(vi.mocked(agentOpsApi.messages)).toHaveBeenCalled()
    expect(screen.queryByTestId('message-problem')).not.toBeInTheDocument()
  })

  it.each([
    [500, 'internal', 'The messages could not be read.'],
    [
      409,
      'conflict',
      'This session was given secrets, so its messages cannot be listed. Its live output is still shown.',
    ],
  ])(
    'says why when the engine answers %s: a failure is not "no messages"',
    async (status, code, words) => {
      showFailing(new ApiError(status, code, 'refused'))
      expect(await screen.findByTestId('message-problem')).toHaveTextContent(
        words,
      )
      expect(screen.queryByTestId('message-rail')).not.toBeInTheDocument()
    },
  )

  it('says a transcript that should be there is not, and stays quiet while it is not written yet', async () => {
    vi.mocked(agentOpsApi.messages).mockResolvedValue({
      driver: 'claude',
      transcript: 'missing',
      total: 0,
      items: [],
    })
    const missing = new QueryClient()
    const first = render(
      <QueryClientProvider client={missing}>
        <Host items={conversation()} onJump={vi.fn()} />
      </QueryClientProvider>,
    )
    expect(await screen.findByTestId('message-problem')).toHaveTextContent(
      'not in its account home',
    )
    first.unmount()
    vi.mocked(agentOpsApi.messages).mockResolvedValue({
      driver: 'claude',
      transcript: 'pending',
      total: 0,
      items: [],
    })
    const pending = new QueryClient()
    render(
      <QueryClientProvider client={pending}>
        <Host items={conversation()} onJump={vi.fn()} />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(pending.isFetching()).toBe(0))
    expect(screen.queryByTestId('message-problem')).not.toBeInTheDocument()
  })

  it('shows a jump that could not be made and does not claim it', async () => {
    const user = userEvent.setup()
    show()
    const marks = await screen.findAllByTestId('message-mark')
    vi.mocked(agentOpsApi.messages).mockRejectedValue(
      new ApiError(500, 'internal', 'boom'),
    )
    await user.click(marks[0]!)
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'The messages could not be read.',
    )
    expect(document.querySelectorAll('.ring-2')).toHaveLength(0)
    // The engine answering without the message is a failed jump too, not "older".
    vi.mocked(agentOpsApi.messages).mockResolvedValue({
      driver: 'claude',
      transcript: 'pending',
      total: 0,
      items: [],
    })
    await user.click(marks[0]!)
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(screen.queryByTestId('message-far')).not.toBeInTheDocument()
  })

  it('announces a repeated jump again', async () => {
    const user = userEvent.setup()
    show()
    const marks = await screen.findAllByTestId('message-mark')
    await user.click(marks[1]!)
    const status = screen.getByRole('status')
    await waitFor(() =>
      expect(status).toHaveTextContent('Jumped to message 3.'),
    )
    const first = status.textContent
    await user.click(marks[1]!)
    await waitFor(() => expect(status.textContent).not.toBe(first))
    expect(status).toHaveTextContent('Jumped to message 3.')
  })

  it('shows nothing of the session that was on screen once the session changes', async () => {
    const user = userEvent.setup()
    let arrive: (value: RunMessagesDTO) => void = () => {}
    const second = new Promise<RunMessagesDTO>((resolve) => {
      arrive = resolve
    })
    vi.mocked(agentOpsApi.messages).mockImplementation(
      (ref: string, p?: RunMessagesParams) =>
        ref === 'run_1' ? engine(transcript(12))(ref, p) : second,
    )
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const view = render(
      <QueryClientProvider client={qc}>
        <Host items={conversation()} onJump={vi.fn()} runRef="run_1" />
      </QueryClientProvider>,
    )
    await user.click(await screen.findByRole('button', { name: /Messages/ }))
    expect(await screen.findAllByTestId('message-mark')).toHaveLength(6)
    expect(screen.getByTestId('message-panel')).toBeInTheDocument()
    view.rerender(
      <QueryClientProvider client={qc}>
        <Host items={conversation()} onJump={vi.fn()} runRef="run_2" />
      </QueryClientProvider>,
    )
    // While run_2's index is on its way nothing of run_1 is offered.
    expect(screen.queryByTestId('message-rail')).not.toBeInTheDocument()
    expect(screen.queryByTestId('message-bar')).not.toBeInTheDocument()
    arrive({
      driver: 'claude',
      transcript: 'read',
      total: 2,
      items: [
        {
          id: 1,
          role: 'user',
          first_line: 'only here',
          at: '2026-10-14T10:00:00Z',
        },
        {
          id: 3,
          role: 'user',
          first_line: 'and here',
          at: '2026-10-14T10:01:00Z',
        },
      ],
    })
    expect(await screen.findAllByTestId('message-mark')).toHaveLength(2)
    // The panel the person had open on run_1 is not open on run_2.
    expect(screen.getByRole('button', { name: /Messages/ })).toHaveAttribute(
      'aria-expanded',
      'false',
    )
    expect(screen.queryByTestId('message-panel')).not.toBeInTheDocument()
  })

  it('picks the right row among equal ones, as the engine counts them', async () => {
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByRole('button', { name: /Messages/ }))
    // Messages 7 and 9 are both "continue". The list reads newest first: 9, then 7.
    const equal = (await screen.findAllByTestId('message-result')).filter((r) =>
      r.textContent?.includes('continue'),
    )
    expect(equal).toHaveLength(2)
    await user.click(equal[1]!)
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(
        'Jumped to message 7.',
      ),
    )
    const rows = screen.getAllByTestId('conversation-item')
    expect(rows[6]!.closest('li')).toHaveClass('ring-2')
    expect(rows[8]!.closest('li')).not.toHaveClass('ring-2')
  })
})

describe('MessageNavigator panel', () => {
  const open = async (user: ReturnType<typeof userEvent.setup>) => {
    await user.click(await screen.findByRole('button', { name: /Messages/ }))
    return screen.findByTestId('message-panel')
  }

  it('lists what was sent, newest first, with its count', async () => {
    const user = userEvent.setup()
    show()
    const toggle = await screen.findByRole('button', { name: /Messages/ })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    const panel = await open(user)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(toggle).toHaveAttribute('aria-controls', panel.id)
    const list = await within(panel).findByTestId('message-list')
    const lines = within(list)
      .getAllByTestId('message-result')
      .map((b) => b.textContent)
    expect(lines[0]).toContain('request 11')
    expect(lines[lines.length - 1]).toContain('request 1')
    expect(within(panel).getByText('6 of 6 messages')).toBeInTheDocument()
  })

  it('searches the whole session on the engine and jumps to a result', async () => {
    const user = userEvent.setup()
    show()
    const panel = await open(user)
    await user.type(
      within(panel).getByLabelText("Search this session's messages"),
      'rest of message 5',
    )
    await waitFor(() =>
      expect(within(panel).getAllByTestId('message-result')).toHaveLength(1),
    )
    expect(lastAsk()).toMatchObject({ q: 'rest of message 5' })
    const results = within(panel).getAllByTestId('message-result')
    expect(results[0]).toHaveTextContent('request 5')
    await user.click(results[0]!)
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(
        'Jumped to message 5.',
      ),
    )
  })

  it('includes the answers on request', async () => {
    const user = userEvent.setup()
    show()
    const panel = await open(user)
    await user.click(within(panel).getByLabelText('Include answers'))
    await waitFor(() =>
      expect(within(panel).getAllByTestId('message-result')).toHaveLength(12),
    )
    expect(lastAsk()).toMatchObject({ role: 'all' })
    expect(within(panel).getAllByText('Answer')).toHaveLength(6)
  })

  it('pages further back with the engine’s cursor', async () => {
    const user = userEvent.setup()
    show(transcript(240), conversation())
    const panel = await open(user)
    expect(await within(panel).findAllByTestId('message-result')).toHaveLength(
      50,
    )
    await user.click(
      within(panel).getByRole('button', { name: 'Show earlier messages' }),
    )
    await waitFor(() =>
      expect(within(panel).getAllByTestId('message-result')).toHaveLength(100),
    )
    expect(lastAsk()).toMatchObject({ order: 'desc', cursor: '141' })
    expect(within(panel).getByText('100 of 120 messages')).toBeInTheDocument()
  })

  it('goes to a date and time: the first message at or after it', async () => {
    const user = userEvent.setup()
    show()
    const panel = await open(user)
    const go = within(panel).getByRole('button', { name: 'Go' })
    expect(go).toBeDisabled()
    const field = within(panel).getByLabelText('Go to date and time')
    // One instant, 10:04 UTC, whatever zone the suite runs in.
    await user.type(field, localFieldOf('2026-10-14T10:04:00Z'))
    expect(go).toBeEnabled()
    await user.click(go)
    const asked = vi
      .mocked(agentOpsApi.messages)
      .mock.calls.map((c) => c[1])
      .find((p) => p?.at !== undefined)
    expect(asked).toMatchObject({
      at: '2026-10-14T10:04:00.000Z',
      limit: 1,
    })
    // The first message at or after 10:04 (UTC) is message 5; its row is reached.
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(
        'Jumped to message 5.',
      ),
    )
    const rows = screen.getAllByTestId('conversation-item')
    expect(rows[4]!.closest('li')).toHaveClass('ring-2')
  })

  it('says so when there is no message at or after the time', async () => {
    const user = userEvent.setup()
    show([{ ...transcript(1)[0]!, at: '2020-01-01T00:00:00Z' }])
    const panel = await open(user)
    await user.type(
      within(panel).getByLabelText('Go to date and time'),
      '2026-10-14T10:04',
    )
    await user.click(within(panel).getByRole('button', { name: 'Go' }))
    expect(
      await within(panel).findByText('No message at or after that time.'),
    ).toBeInTheDocument()
  })

  it('shows a message older than the conversation on screen whole', async () => {
    const user = userEvent.setup()
    show(transcript(12), conversation().slice(6))
    const panel = await open(user)
    const results = await within(panel).findAllByTestId('message-result')
    await user.click(results[results.length - 1]!) // message 1: not on screen
    const far = await screen.findByTestId('message-far')
    expect(far).toHaveTextContent('request 1')
    expect(far).toHaveTextContent('and the rest of message 1')
    expect(screen.getByRole('status')).toHaveTextContent(
      'Message 1 is shown below',
    )
    await user.click(within(far).getByRole('button', { name: 'Close' }))
    expect(screen.queryByTestId('message-far')).not.toBeInTheDocument()
  })

  it('says how many records of the transcript could not be read', async () => {
    const user = userEvent.setup()
    const read = engine(transcript(12))
    vi.mocked(agentOpsApi.messages).mockImplementation(async (ref, p) => ({
      ...(await read(ref, p)),
      unreadable: 3,
    }))
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={qc}>
        <Host items={conversation()} onJump={vi.fn()} />
      </QueryClientProvider>,
    )
    const panel = await open(user)
    expect(
      await within(panel).findByText(
        '3 records of the transcript could not be read.',
      ),
    ).toBeInTheDocument()
  })

  it('passes axe with the panel open', async () => {
    const user = userEvent.setup()
    const { container } = show()
    await open(user)
    await screen.findAllByTestId('message-result')
    const violations = (
      await axe.run(container, {
        // jsdom has no layout, so contrast is the captures' to judge.
        rules: { 'color-contrast': { enabled: false } },
      })
    ).violations.map((v) => `${v.id}: ${v.help}`)
    expect(violations).toEqual([])
  })
})
