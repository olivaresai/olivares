// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { TimelineDTO } from './types'

const auth = vi.hoisted(() => ({ usage: true }))
// Usage opens with its own read; every other permission is held.
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) =>
      permission === 'sessions:account:read' ? auth.usage : true,
  }),
}))

vi.mock('./api', () => ({
  sessionsApi: { live: vi.fn(), liveOne: vi.fn(), timeline: vi.fn() },
  sessionsKeys: {
    all: (t: string | null) => ['s', t],
    live: (t: string | null, p?: unknown) => ['s', t, 'live', p ?? null],
    liveOne: (t: string | null, ref: string) => ['s', t, 'one', ref],
    timeline: (t: string | null, ref: string, p?: unknown) => [
      's',
      t,
      'tl',
      ref,
      p ?? null,
    ],
  },
}))

import { sessionsApi } from './api'
import { SessionTimeline } from './timeline'

const events: TimelineDTO[] = [
  {
    at: '2026-06-04T10:00:00Z',
    kind: 'tool',
    tool_ref: 'fs.read',
    resource_ref: 'src/main.go',
    mode: 'read',
    source: 'otel',
  },
  {
    at: '2026-06-04T10:01:00Z',
    kind: 'mcp',
    tool_ref: 'memory.search',
    source: 'mcp-bridge',
  },
  {
    at: '2026-06-04T10:02:00Z',
    kind: 'finding',
    title: 'Unexpected write to secrets',
    resource_ref: 'appdb.public.secrets',
  },
]

/** A cost event as the engine writes it: its tokens, never an amount. */
const cost: TimelineDTO = {
  at: '2026-06-04T10:03:00Z',
  kind: 'cost',
  title: '1200 in / 800 out tokens',
  source: 'cost',
}

function renderTimeline() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <SessionTimeline sessionRef="sess-alpha" />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  auth.usage = true
  vi.mocked(sessionsApi.timeline).mockResolvedValue({
    items: events,
    has_more: false,
  })
})

describe('SessionTimeline', () => {
  it('renders timeline events with their kinds and refs', async () => {
    renderTimeline()
    expect(await screen.findByText('fs.read')).toBeInTheDocument()
    expect(screen.getByText('memory.search')).toBeInTheDocument()
    expect(screen.getByText('Unexpected write to secrets')).toBeInTheDocument()
    // It fetched the keyset page for this session.
    await waitFor(() =>
      expect(sessionsApi.timeline).toHaveBeenCalledWith(
        'sess-alpha',
        expect.objectContaining({ limit: expect.any(Number) }),
      ),
    )
  })

  it('filters the timeline by kind', async () => {
    const user = userEvent.setup()
    renderTimeline()
    await screen.findByText('fs.read')

    // Open the kind facet and choose "Finding".
    await user.click(screen.getByRole('combobox'))
    await user.click(await screen.findByRole('option', { name: 'Finding' }))

    // Only the finding remains; the tool/mcp entries are filtered out.
    await waitFor(() => {
      expect(
        screen.getByText('Unexpected write to secrets'),
      ).toBeInTheDocument()
      expect(screen.queryByText('fs.read')).not.toBeInTheDocument()
      expect(screen.queryByText('memory.search')).not.toBeInTheDocument()
    })
  })

  it('lists a cost event and offers its kind to a person Usage opens for', async () => {
    vi.mocked(sessionsApi.timeline).mockResolvedValue({
      items: [...events, cost],
      has_more: false,
    })
    const user = userEvent.setup()
    renderTimeline()
    const title = await screen.findByText('1200 in / 800 out tokens')
    const row = title.closest('li')!
    expect(within(row).getByText('Cost')).toBeInTheDocument()
    expect(within(row).getByText('Source: cost')).toBeInTheDocument()
    await user.click(screen.getByRole('combobox'))
    expect(
      await screen.findByRole('option', { name: 'Cost' }),
    ).toBeInTheDocument()
  })

  it('lists a token event under Tokens to a person Usage does not open for, with no cost word and no $', async () => {
    auth.usage = false
    vi.mocked(sessionsApi.timeline).mockResolvedValue({
      items: [...events, cost],
      has_more: false,
    })
    const user = userEvent.setup()
    renderTimeline()
    // The engine's cost event says tokens: a person's own tokens stay theirs to see.
    const title = await screen.findByText('1200 in / 800 out tokens')
    expect(within(title.closest('li')!).getByText('Tokens')).toBeInTheDocument()
    await user.click(screen.getByRole('combobox'))
    const options = await screen.findAllByRole('option')
    expect(options.map((o) => o.textContent)).toEqual([
      'All kinds',
      'Tool',
      'MCP',
      'Tokens',
      'Finding',
    ])
    expect(document.body).not.toHaveTextContent(/cost/i)
    expect(document.body).not.toHaveTextContent('$')
    // Tokens filters to the token event, as Cost does where Usage opens.
    await user.click(screen.getByRole('option', { name: 'Tokens' }))
    await waitFor(() => expect(screen.queryByText('fs.read')).toBeNull())
    expect(screen.getByText('1200 in / 800 out tokens')).toBeInTheDocument()
  })

  it('names no cost in the empty hint to a person Usage does not open for', async () => {
    auth.usage = false
    vi.mocked(sessionsApi.timeline).mockResolvedValue({
      items: [],
      has_more: false,
    })
    renderTimeline()
    expect(
      await screen.findByText(
        'As the session emits tool, MCP and finding events, they appear here.',
      ),
    ).toBeInTheDocument()
    expect(document.body).not.toHaveTextContent(/cost/i)
  })

  it('says no "No timeline events yet" while Load more can still bring the kind chosen', async () => {
    vi.mocked(sessionsApi.timeline).mockResolvedValue({
      items: events.slice(0, 2),
      has_more: true,
      cursor: 'c1',
    })
    const user = userEvent.setup()
    renderTimeline()
    await screen.findByText('fs.read')
    await user.click(screen.getByRole('combobox'))
    await user.click(await screen.findByRole('option', { name: 'Finding' }))
    await waitFor(() => expect(screen.queryByText('fs.read')).toBeNull())
    expect(screen.queryByText('No timeline events yet')).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Load more' }),
    ).toBeInTheDocument()
  })
})
