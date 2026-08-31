// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { TimelineDTO } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
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

function renderTimeline() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <SessionTimeline sessionRef="sess-alpha" />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
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
})
