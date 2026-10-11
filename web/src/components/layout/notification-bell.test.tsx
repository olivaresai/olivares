// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { identity } = vi.hoisted(() => ({
  identity: {
    activeTenant: 't1',
    principal: { kind: 'user', user_id: 'u-self' },
  },
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...identity, isSuperadmin: true, can: () => true }),
}))

vi.mock('@/features/console/api', () => ({
  consoleKeys: { members: (t: string | null) => ['console', t, 'members'] },
  consoleApi: {
    listMembers: async () => ({
      items: [{ user_id: 'u-ada', email: 'ada@acme.io', display_name: 'Ada' }],
    }),
  },
}))

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({
    to,
    search,
    children,
    onClick,
  }: {
    to: string
    search?: Record<string, string>
    children: ReactNode
    onClick?: () => void
  }) => (
    <a
      href={search ? `${to}?${new URLSearchParams(search)}` : to}
      onClick={onClick}
    >
      {children}
    </a>
  ),
}))

// The engine's GET /v1/audit/recent, reproduced: newest first, no audit.read rows, and
// the read itself appends nothing. The ledger is mutable so an event can arrive later.
const { ledger, recentMock, listMock, appendEvent, resetLedger } = vi.hoisted(
  () => {
    const size = 30
    const at = (seq: number) =>
      new Date(Date.UTC(2026, 5, 26, 10, 0, 0) + seq * 60_000).toISOString()
    const make = (seq: number, action = `event.${seq}`) => ({
      id: `ev${seq}`,
      seq,
      occurred_at: at(seq),
      actor: 'admin@test.com',
      actor_kind: 'user',
      action,
      target_kind: 'core.api_token',
      target_id: `tok-${seq}`,
      prev_hash: '',
      hash: `h${seq}`,
    })
    // Every third event is a person's ledger read: the bell must never show one.
    const seed = () =>
      Array.from({ length: size }, (_, i) =>
        make(i + 1, (i + 1) % 3 === 0 ? 'audit.read' : `event.${i + 1}`),
      )
    const events = seed()
    const recent = vi.fn(async (params?: { limit?: number }) => ({
      items: events
        .filter((e) => e.action !== 'audit.read')
        .reverse()
        .slice(0, params?.limit ?? 10),
      head_seq: events.length,
    }))
    return {
      ledger: events,
      recentMock: recent,
      listMock: vi.fn(),
      appendEvent: () => {
        const event = make(events.length + 1)
        events.push(event)
        return event
      },
      resetLedger: () => events.splice(0, events.length, ...seed()),
    }
  },
)

vi.mock('@/lib/api/endpoints', () => ({
  auditApi: { recent: recentMock, list: listMock },
}))

import { NotificationBell } from './notification-bell'

const LAST_SEEN_KEY = 'olivares.notifications.lastSeen'
const shown = () => ledger.filter((e) => e.action !== 'audit.read')
const labelOf = (event: { action: string }) =>
  event.action.replace(/[._]+/g, ' ').replace(/^./, (c) => c.toUpperCase())

function Wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

const mount = () =>
  render(
    <Wrapper>
      <NotificationBell />
    </Wrapper>,
  )

beforeEach(() => {
  localStorage.clear()
  identity.activeTenant = 't1'
  identity.principal = { kind: 'user', user_id: 'u-self' }
  recentMock.mockClear()
  listMock.mockClear()
  resetLedger()
})

describe('NotificationBell', () => {
  it('keeps unread activity independent when the organization or user changes', async () => {
    const user = userEvent.setup()
    const view = mount()
    const dot = () => view.container.querySelector('.bg-primary')
    const bell = () => screen.getByRole('button', { name: 'Recent activity' })
    const redraw = () =>
      view.rerender(
        <Wrapper>
          <NotificationBell />
        </Wrapper>,
      )
    await waitFor(() => expect(dot()).not.toBeNull())
    await user.click(bell())
    expect(dot()).toBeNull()
    await user.keyboard('{Escape}')

    expect(localStorage.getItem(LAST_SEEN_KEY)).toBeNull()
    identity.activeTenant = 't2'
    redraw()
    await waitFor(() => expect(dot()).not.toBeNull())
    await user.click(bell())
    expect(dot()).toBeNull()
    await user.keyboard('{Escape}')

    identity.activeTenant = 't1'
    redraw()
    await waitFor(() => expect(dot()).toBeNull())
    identity.principal = { kind: 'user', user_id: 'u-other' }
    redraw()
    await waitFor(() => expect(dot()).not.toBeNull())
  })

  it('migrates the legacy marker only to a user partition, never a token or second tenant', async () => {
    localStorage.setItem(LAST_SEEN_KEY, shown().at(-1)!.occurred_at)
    identity.principal = { kind: 'token', user_id: 'u-self' }
    const view = mount()
    const dot = () => view.container.querySelector('.bg-primary')
    await waitFor(() => expect(dot()).not.toBeNull())
    expect(localStorage.getItem(LAST_SEEN_KEY)).not.toBeNull()
    identity.principal = { kind: 'user', user_id: 'u-self' }
    view.rerender(
      <Wrapper>
        <NotificationBell />
      </Wrapper>,
    )
    await waitFor(() => expect(dot()).toBeNull())
    expect(localStorage.getItem(LAST_SEEN_KEY)).toBeNull()
    identity.activeTenant = 't2'
    view.rerender(
      <Wrapper>
        <NotificationBell />
      </Wrapper>,
    )
    await waitFor(() => expect(dot()).not.toBeNull())
    view.unmount()
    identity.activeTenant = 't1'
    const restored = mount()
    await waitFor(() => expect(recentMock).toHaveBeenCalled())
    expect(restored.container.querySelector('.bg-primary')).toBeNull()
  })

  it('resets a token’s volatile read marker and closes the popover on tenant switch without storage writes', async () => {
    identity.principal = { kind: 'token', user_id: 'u-self' }
    const writes = vi.spyOn(Storage.prototype, 'setItem')
    try {
      const user = userEvent.setup()
      const view = mount()
      const dot = () => view.container.querySelector('.bg-primary')
      await waitFor(() => expect(dot()).not.toBeNull())
      await user.click(screen.getByRole('button', { name: 'Recent activity' }))
      expect(dot()).toBeNull()
      expect(screen.getByRole('link', { name: /view all/i })).toBeVisible()
      ledger.splice(-10)
      identity.activeTenant = 't2'
      view.rerender(
        <Wrapper>
          <NotificationBell />
        </Wrapper>,
      )
      await waitFor(() => expect(dot()).not.toBeNull())
      expect(screen.queryByRole('link', { name: /view all/i })).toBeNull()
      expect(writes).not.toHaveBeenCalled()
    } finally {
      writes.mockRestore()
    }
  })

  it('reaches a work action behind ten internal records within the recent API bound', async () => {
    const items = [
      ...Array.from({ length: 10 }, (_, i) => ({
        ...ledger[i]!,
        id: `internal-${i}`,
        action: 'authorization_decision.record',
      })),
      {
        ...ledger[10]!,
        actor: 'user:u-self',
        action: 'sessions.work.item.create',
      },
    ]
    // The published API accepts 1..50 and falls back to ten for an invalid limit.
    // Real use returned only ten rows for the former request of 100.
    recentMock.mockImplementationOnce(async (params?: { limit?: number }) => ({
      head_seq: 11,
      items: items.slice(
        0,
        params?.limit && params.limit >= 1 && params.limit <= 50
          ? params.limit
          : 10,
      ),
    }))
    mount()
    await userEvent.click(
      screen.getByRole('button', { name: 'Recent activity' }),
    )
    expect(await screen.findByText('You created a work item')).toBeVisible()
  })

  it("shows people's actions by default and exposes internal records on request", async () => {
    recentMock.mockResolvedValueOnce({
      head_seq: 2,
      items: [
        {
          ...ledger[0]!,
          id: 'internal',
          action: 'authorization_decision.record',
        },
        {
          ...ledger[1]!,
          id: 'work',
          actor: 'user:u-self',
          action: 'sessions.work.item.ready',
        },
      ],
    })
    const user = userEvent.setup()
    mount()
    await user.click(screen.getByRole('button', { name: 'Recent activity' }))
    expect(
      await screen.findByText('You marked a work item ready'),
    ).toBeVisible()
    expect(screen.queryByText('Authorization decision record')).toBeNull()
    await user.click(
      screen.getByRole('checkbox', { name: 'Include internal records' }),
    )
    expect(
      await screen.findByText('Authorization decision record'),
    ).toBeVisible()
  })

  it('shows the newest events first, never a ledger read, and never reads the ledger list', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(screen.getByRole('button'))
    const newest = shown().at(-1)!
    await screen.findByText(labelOf(newest))
    const rendered = screen
      .getAllByText(/^Event \d+$/)
      .map((node) => node.textContent)
    expect(rendered).toEqual(shown().reverse().slice(0, 10).map(labelOf))
    expect(screen.queryByText('audit read')).toBeNull()
    // The audited list appends an audit.read per call: the bell must not poll it.
    expect(listMock).not.toHaveBeenCalled()
  })

  it('lights the dot for a new event, then clears and keeps the newest timestamp on open', async () => {
    const [previous, newest] = shown().slice(-2)
    localStorage.setItem(LAST_SEEN_KEY, previous!.occurred_at)
    const user = userEvent.setup()
    const { container } = mount()
    await waitFor(() =>
      expect(container.querySelector('.bg-primary')).not.toBeNull(),
    )
    await user.click(screen.getByRole('button'))
    expect(container.querySelector('.bg-primary')).toBeNull()
    expect(localStorage.getItem(LAST_SEEN_KEY)).toBeNull()
    expect(
      localStorage.getItem(
        `${LAST_SEEN_KEY}:${JSON.stringify([window.location.origin, 'user', 'u-self', 't1'])}`,
      ),
    ).toBe(newest!.occurred_at)
  })

  it('does not light the dot when the newest event was already seen', async () => {
    localStorage.setItem(LAST_SEEN_KEY, shown().at(-1)!.occurred_at)
    const { container } = mount()
    await waitFor(() => expect(recentMock).toHaveBeenCalled())
    await waitFor(() =>
      expect(container.querySelector('.bg-primary')).toBeNull(),
    )
  })

  it('keeps polling: an event that arrives later lights the dot with no reload', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      localStorage.setItem(LAST_SEEN_KEY, shown().at(-1)!.occurred_at)
      const { container } = mount()
      await waitFor(() => expect(recentMock).toHaveBeenCalled())
      expect(container.querySelector('.bg-primary')).toBeNull()
      appendEvent()
      // Far past the interval: a longer interval fails, a bell that stopped polling never fires.
      await vi.advanceTimersByTimeAsync(5 * 60_000)
      expect(container.querySelector('.bg-primary')).not.toBeNull()
      expect(recentMock.mock.calls.length).toBeGreaterThan(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('shows the empty state for an empty ledger', async () => {
    recentMock.mockResolvedValueOnce({ items: [], head_seq: 0 })
    const user = userEvent.setup()
    mount()
    await user.click(screen.getByRole('button'))
    await screen.findByText(/no recent activity/i)
    expect(screen.queryAllByText(/^Event \d+$/)).toHaveLength(0)
  })

  it('tells events as sentences, hides routine ones and links an approval to its request', async () => {
    const at = (m: number) =>
      new Date(Date.UTC(2026, 9, 1, 12, m, 0)).toISOString()
    recentMock.mockResolvedValueOnce({
      head_seq: 5,
      items: [
        {
          id: 'e5',
          seq: 5,
          occurred_at: at(5),
          actor: 'user:u-ada',
          actor_kind: 'user',
          action: 'governance.approval.create',
          target_kind: 'governance.approval',
          target_id: 'apr-1',
          prev_hash: '',
          hash: '',
        },
        {
          id: 'e4',
          seq: 4,
          occurred_at: at(4),
          actor: 'user:u-self',
          actor_kind: 'user',
          action: 'auth.login',
          target_kind: '',
          target_id: '',
          prev_hash: '',
          hash: '',
        },
        {
          id: 'e3',
          seq: 3,
          occurred_at: at(3),
          actor: 'user:u-self',
          actor_kind: 'user',
          action: 'sessions.run.launched',
          target_kind: '',
          target_id: '',
          prev_hash: '',
          hash: '',
        },
        {
          id: 'e2',
          seq: 2,
          occurred_at: at(2),
          actor: 'user:u-ada',
          actor_kind: 'user',
          action: 'mcp_gateway.read',
          target_kind: '',
          target_id: '',
          prev_hash: '',
          hash: '',
        },
        {
          id: 'e1',
          seq: 1,
          occurred_at: at(1),
          actor: 'system',
          actor_kind: 'system',
          action: 'audit.checkpoint',
          target_kind: '',
          target_id: '',
          prev_hash: '',
          hash: '',
        },
      ],
    })
    const user = userEvent.setup()
    mount()
    await user.click(screen.getByRole('button'))
    const approval = await screen.findByText('Ada requested approval')
    expect(approval.closest('a')).toHaveAttribute(
      'href',
      '/permissions?approval=apr-1',
    )
    expect(screen.getByText('You started a session')).toBeInTheDocument()
    expect(screen.getByText('You signed in')).toBeInTheDocument()
    expect(screen.getByText('Audit checkpoint created')).toBeInTheDocument()
    expect(screen.queryByText(/auth login|mcp gateway read/i)).toBeNull()
    expect(screen.queryByText(/system: system|user:u-/)).toBeNull()
  })

  it('links "view all" to the audit ledger', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(screen.getByRole('button'))
    const link = await screen.findByRole('link', { name: /view all/i })
    expect(link).toHaveAttribute('href', '/audit')
  })
})
