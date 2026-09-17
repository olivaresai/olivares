// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C3 observation history: DTO projection, 25+6 paging, error over retained
// rows, first-page recovery, malformed continuation, times, keyboard.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from 'i18next'
import { ApiError } from '@/lib/api/errors'
import type { ObservationItem, ObservationPage } from './api'
import './i18n'

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    inventoryApi: {
      ...actual.inventoryApi,
      observations: vi.fn(),
    },
  }
})

import { inventoryApi, inventoryKeys } from './api'
import { ObservationHistory } from './observation-history'

const rid = (n: number) =>
  `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`

const item = (
  n: number,
  extra: Partial<ObservationItem> = {},
): ObservationItem => ({
  receipt_id: rid(n),
  event_type: n % 2 === 0 ? 'edge.observed' : 'cost.sampled',
  registration:
    n === 0
      ? {
          registration_state: 'registered_snapshot',
          source_id: 'src-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee',
          source_revision: 1_000_000_000_042,
          environment_ref:
            'exec-env/' + 'very-long-execution-environment-name/'.repeat(4) + n,
        }
      : n === 1
        ? { registration_state: 'unattributed' }
        : { registration_state: 'invalid' },
  source_occurred_at: n === 1 ? undefined : extra.source_occurred_at,
  first_received_at: '2026-01-15T12:00:00Z',
  last_received_at: '2026-03-01T08:00:00Z',
  deliveries: n === 0 ? 4 : 1,
  conflicting_redelivery: n === 2,
  ...extra,
})

const page = (start: number, count: number, more: boolean): ObservationPage => {
  const items = Array.from({ length: count }, (_, i) => item(start + i))
  return more
    ? { items, has_more: true, cursor: items.at(-1)!.receipt_id }
    : { items, has_more: false }
}

const calls: Array<{ kind: string; id: string; opts: unknown }> = []

function mount(kind = 'agent', id = 'a1', tenant: string | null = 't1') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const utils = render(
    <QueryClientProvider client={qc}>
      <ObservationHistory tenant={tenant} kind={kind} id={id} />
    </QueryClientProvider>,
  )
  return { qc, ...utils }
}

beforeEach(async () => {
  calls.length = 0
  await act(async () => {
    await i18n.changeLanguage('en')
  })
  vi.mocked(inventoryApi.observations)
    .mockReset()
    .mockImplementation(async (kind, id, opts) => {
      calls.push({ kind, id, opts })
      return page(0, 3, false)
    })
})

afterEach(async () => {
  await act(async () => {
    await i18n.changeLanguage('en')
  })
})

describe('ObservationHistory projection and paging', () => {
  it('renders the three registration forms, both event types, times, deliveries and conflict as text', async () => {
    vi.mocked(inventoryApi.observations).mockImplementation(async () => ({
      items: [
        item(0, { source_occurred_at: '2025-06-01T08:30:00Z' }),
        item(1),
        item(2, { source_occurred_at: '2027-12-01T00:00:00Z' }),
      ],
      has_more: false,
    }))
    mount()
    expect(
      await screen.findByText('Historical registration snapshot'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: 'Observation history' }),
    ).toBeInTheDocument()
    expect(screen.getByText(/Source id at reception/)).toBeInTheDocument()
    expect(
      screen.getByText(/Applied registration revision/),
    ).toBeInTheDocument()
    expect(screen.getByText(/Execution environment/)).toBeInTheDocument()
    expect(screen.getByText('Unattributed')).toBeInTheDocument()
    expect(
      screen.getByText('Incomplete historical snapshot'),
    ).toBeInTheDocument()
    expect(screen.getAllByText('Edge observed').length).toBeGreaterThan(0)
    expect(screen.getByText('Cost sampled')).toBeInTheDocument()
    expect(screen.getByText('4')).toBeInTheDocument()
    expect(
      screen.getByText(
        'A redelivery with different facts is retained. This does not count or date those variants.',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getAllByText('No conflicting redelivery is retained.').length,
    ).toBeGreaterThan(0)
    expect(screen.getByText('2025-06-01T08:30:00Z')).toBeInTheDocument()
    expect(screen.getByText('2027-12-01T00:00:00Z')).toBeInTheDocument()
    expect(
      screen.getByText('The source did not declare an occurrence instant.'),
    ).toBeInTheDocument()
    expect(screen.queryByText('registered source')).toBeNull()
    expect(screen.queryByText('currently registered')).toBeNull()
    expect(
      screen.getByText(
        'This history does not establish source coverage, completeness of discovery, or current health.',
      ),
    ).toBeInTheDocument()
    expect(screen.getByText('3 loaded')).toBeInTheDocument()
    expect(screen.queryByText(/3 of |total 3/i)).toBeNull()
    const refresh = screen.getByRole('button', { name: 'Refresh history' })
    expect(refresh).toBeInTheDocument()
    refresh.focus()
    expect(refresh).toHaveFocus()
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
  })

  it('a malformed page is an error, not an empty history or a repeated first page', async () => {
    vi.mocked(inventoryApi.observations).mockRejectedValue(
      new ApiError(
        200,
        'invalid_response',
        'The observation page has more receipts but no usable cursor.',
        'req-malformed',
      ),
    )
    mount()
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('req-malformed')).toBeInTheDocument()
    expect(screen.queryByText(/No stored receipts are available/)).toBeNull()
    expect(screen.queryByText(rid(0))).toBeNull()
  })

  it('pages 25+6, keeps ASC order, one in-flight load-more, and shows loaded not total', async () => {
    let resolveMore!: (p: ObservationPage) => void
    vi.mocked(inventoryApi.observations).mockImplementation(
      async (_kind, _id, opts) => {
        calls.push({ kind: _kind, id: _id, opts })
        const cursor =
          opts && typeof opts === 'object' && 'cursor' in opts
            ? (opts as { cursor?: string }).cursor
            : undefined
        if (!cursor) return page(0, 25, true)
        return await new Promise<ObservationPage>((r) => {
          resolveMore = r
        })
      },
    )
    const user = userEvent.setup()
    mount()
    expect(await screen.findByText('25 loaded')).toBeInTheDocument()
    const first = screen.getAllByText(/00000000-0000-4000-8000-/)[0]
    const last = screen.getAllByText(/00000000-0000-4000-8000-/)[24]
    expect(first).toHaveTextContent(rid(0))
    expect(last).toHaveTextContent(rid(24))
    const more = screen.getByRole('button', { name: 'Load more' })
    await user.click(more)
    await user.click(more)
    expect(
      calls.filter((c) => (c.opts as { cursor?: string }).cursor).length,
    ).toBe(1)
    expect(calls[1]!.opts).toMatchObject({
      tenant: 't1',
      cursor: rid(24),
    })
    await act(async () => {
      resolveMore(page(25, 6, false))
    })
    expect(await screen.findByText('31 loaded')).toBeInTheDocument()
    expect(screen.getByText(rid(25))).toBeInTheDocument()
    expect(screen.getByText(rid(30))).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
    expect(screen.queryByText(/31 of |total 31/i)).toBeNull()
  })

  it('empty 200 is available-not-never, not a coverage claim', async () => {
    vi.mocked(inventoryApi.observations).mockResolvedValue({
      items: [],
      has_more: false,
    })
    mount()
    expect(
      await screen.findByText(
        'No stored receipts are available. That does not prove this entity was never observed.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText('never observed')).toBeNull()
  })

  it('403 is calm; 404 is catalog-gone; 500 shows retry and request id', async () => {
    vi.mocked(inventoryApi.observations).mockRejectedValue(
      new ApiError(403, 'forbidden', 'no', 'req-403'),
    )
    const { unmount } = mount()
    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    unmount()

    vi.mocked(inventoryApi.observations).mockRejectedValue(
      new ApiError(404, 'not_found', 'missing', 'req-404'),
    )
    const second = mount()
    expect(
      await screen.findByText('This entry is no longer in the catalog.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    second.unmount()

    vi.mocked(inventoryApi.observations).mockRejectedValue(
      new ApiError(500, 'internal', 'boom', 'req-500'),
    )
    mount()
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('req-500')).toBeInTheDocument()
    expect(
      within(alert).getByRole('button', { name: 'Retry' }),
    ).toBeInTheDocument()
  })

  it('a 500 on load-more hides retained rows; retry restarts at page 1', async () => {
    vi.mocked(inventoryApi.observations)
      .mockResolvedValueOnce(page(0, 25, true))
      .mockRejectedValueOnce(new ApiError(500, 'internal', 'page2', 'req-p2'))
      .mockResolvedValueOnce(page(0, 3, false))
    const user = userEvent.setup()
    const { qc } = mount()
    expect(await screen.findByText(rid(0))).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('req-p2')).toBeInTheDocument()
    expect(screen.queryByText(rid(0))).toBeNull()
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText(rid(0))).toBeInTheDocument()
    expect(screen.getByText('3 loaded')).toBeInTheDocument()
    expect(screen.queryByText(rid(24))).toBeNull()
    expect(
      qc.getQueryData(inventoryKeys.observations('t1', 'agent', 'a1')),
    ).toEqual({ pages: [page(0, 3, false)], pageParams: [undefined] })
  })

  it('refresh discards prior pages and cursor and fetches page 1', async () => {
    const pages = [page(0, 25, true), page(25, 6, false), page(0, 2, false)]
    let n = 0
    vi.mocked(inventoryApi.observations).mockImplementation(
      async (kind, id, opts) => {
        calls.push({ kind, id, opts })
        return pages[Math.min(n++, pages.length - 1)]!
      },
    )
    const user = userEvent.setup()
    mount()
    expect(await screen.findByText('25 loaded')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByText('31 loaded')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Refresh history' }))
    expect(await screen.findByText('2 loaded')).toBeInTheDocument()
    expect(screen.queryByText(rid(24))).toBeNull()
    const last = calls.at(-1)!.opts as { cursor?: string; tenant: string }
    expect(last.cursor).toBeUndefined()
    expect(last.tenant).toBe('t1')
  })

  it('unmount with gcTime 0 drops the cache; a late ignored abort does not repaint', async () => {
    let resolveLate!: (p: ObservationPage) => void
    const late = new Promise<ObservationPage>((r) => {
      resolveLate = r
    })
    vi.mocked(inventoryApi.observations).mockImplementation(async () => late)
    const { qc, unmount } = mount()
    expect(
      await screen.findByText('Loading observation history'),
    ).toBeInTheDocument()
    unmount()
    await act(async () => {
      resolveLate(page(0, 3, false))
      await late
    })
    await waitFor(() =>
      expect(
        qc.getQueryData(inventoryKeys.observations('t1', 'agent', 'a1')),
      ).toBeUndefined(),
    )
    expect(screen.queryByText(rid(0))).toBeNull()
  })

  it('close then immediate reopen of the same identity ignores a prior in-flight page that skipped abort', async () => {
    const pending: Array<(p: ObservationPage) => void> = []
    vi.mocked(inventoryApi.observations).mockImplementation(async () => {
      return new Promise<ObservationPage>((resolve) => {
        pending.push(resolve)
      })
    })
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const tree = (open: boolean) => (
      <QueryClientProvider client={qc}>
        {open ? (
          <ObservationHistory tenant="t1" kind="agent" id="a1" />
        ) : (
          <p>history closed</p>
        )}
      </QueryClientProvider>
    )
    const { rerender } = render(tree(true))
    expect(
      await screen.findByText('Loading observation history'),
    ).toBeInTheDocument()
    await waitFor(() => expect(pending.length).toBeGreaterThan(0))
    const life1 = pending.length
    rerender(tree(false))
    expect(screen.getByText('history closed')).toBeInTheDocument()
    rerender(tree(true))
    expect(
      await screen.findByText('Loading observation history'),
    ).toBeInTheDocument()
    await waitFor(() => expect(pending.length).toBeGreaterThan(life1))
    await act(async () => {
      pending[0]!(page(0, 3, false))
    })
    expect(screen.queryByText(rid(0))).toBeNull()
    expect(
      qc.getQueryData(inventoryKeys.observations('t1', 'agent', 'a1')),
    ).toBeUndefined()
    expect(screen.getByText('Loading observation history')).toBeInTheDocument()
    await act(async () => {
      pending[life1]!({ items: [item(99)], has_more: false })
    })
    expect(await screen.findByText(rid(99))).toBeInTheDocument()
    expect(screen.queryByText(rid(0))).toBeNull()
    expect(
      qc.getQueryData(inventoryKeys.observations('t1', 'agent', 'a1')),
    ).toEqual({
      pages: [{ items: [item(99)], has_more: false }],
      pageParams: [undefined],
    })
  })

  it('refresh of the same history that then 403s hides retained receipts', async () => {
    vi.mocked(inventoryApi.observations)
      .mockResolvedValueOnce(page(0, 3, false))
      .mockRejectedValueOnce(new ApiError(403, 'forbidden', 'no', 'req-403'))
    const user = userEvent.setup()
    mount()
    expect(await screen.findByText(rid(0))).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Refresh history' }))
    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(screen.queryByText(rid(0))).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('forwards the query signal and aborts it when the observer unmounts in flight', async () => {
    const seen: AbortSignal[] = []
    vi.mocked(inventoryApi.observations).mockImplementation(
      async (_k, _id, opts) => {
        seen.push((opts as { signal: AbortSignal }).signal)
        return new Promise(() => {})
      },
    )
    const { unmount } = mount()
    await waitFor(() => expect(seen.length).toBe(1))
    expect(seen[0]!.aborted).toBe(false)
    unmount()
    expect(seen[0]!.aborted).toBe(true)
  })
})

describe('ObservationHistory locales', () => {
  it.each(['en', 'es', 'de', 'fr', 'ja', 'ru', 'zh'] as const)(
    'renders translated history copy in %s without falling back to English keys',
    async (lang) => {
      vi.mocked(inventoryApi.observations).mockResolvedValue({
        items: [],
        has_more: false,
      })
      await act(async () => {
        await i18n.changeLanguage(lang)
      })
      mount()
      const title = await screen.findByRole('heading', {
        name: i18n.t('inventory:history.title'),
      })
      expect(title).toBeInTheDocument()
      expect(title.textContent).not.toMatch(/^history\./)
      expect(
        await screen.findByText(String(i18n.t('inventory:history.empty'))),
      ).toBeInTheDocument()
      if (lang !== 'en') {
        expect(screen.queryByText('Observation history')).toBeNull()
      }
    },
  )
})
