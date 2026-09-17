// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Inventory topology — honest page states (2026-09-08).
//
// On the base, Topology painted a generic ErrorState for every failure (a 403
// included) and its kind badges counted the loaded page as if it were the
// estate. These causals pin the correction: loading, 403, other failure, empty
// and a successful loaded page are distinct; a failed refresh does not keep
// retired rows or counts as current; `has_more === true` discloses the page
// limit; a tenant switch owns a different query.
//
// Transport: `inventoryApi` is a mock; `inventoryKeys`, Topology, AsyncSection
// and the i18n store are the real modules.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'
import type { CatalogEntry } from './types'
import './i18n'

const auth = vi.hoisted(() => ({ activeTenant: 't1' as string | null }))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: auth.activeTenant, can: () => true }),
}))

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    inventoryApi: { summary: vi.fn(), entities: vi.fn(), detail: vi.fn() },
  }
})

import { inventoryApi, inventoryKeys } from './api'
import { Topology } from './topology'

const entry = (
  kind: string,
  id: string,
  name: string,
  ref: string,
  status = 'active',
): CatalogEntry => ({
  kind,
  entity_id: id,
  name,
  ref,
  status,
  signal_sources: ['otel'],
  first_seen: '2026-06-03T10:00:00Z',
  last_seen: '2026-06-04T07:00:00Z',
  occurrence_count: 1,
})

const T1: CatalogEntry[] = [
  entry('agent', 'a1', 'prod-orchestrator', 'sess-orch'),
  entry('agent', 'a2', 'idle-agent', 'sess-idle', 'stale'),
  entry('model', 'm1', 'opus', 'mdl-opus'),
]

const T2: CatalogEntry[] = [
  entry('agent', 'b1', 'billing-bot', 'sess-bill'),
  entry('resource', 'r1', 'docs-bucket', 'res-docs'),
]

const forbidden = () =>
  new ApiError(403, 'forbidden', 'workspace confined', 'req-403')
const unavailable = () =>
  new ApiError(500, 'internal', 'store unavailable', 'req-500')

function renderTopo() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const onSelect = vi.fn()
  const tree = (
    <QueryClientProvider client={qc}>
      <Topology onSelect={onSelect} />
    </QueryClientProvider>
  )
  const utils = render(tree)
  return {
    qc,
    onSelect,
    ...utils,
    rerender: () =>
      utils.rerender(
        <QueryClientProvider client={qc}>
          <Topology onSelect={onSelect} />
        </QueryClientProvider>,
      ),
  }
}

beforeEach(() => {
  auth.activeTenant = 't1'
  vi.mocked(inventoryApi.entities)
    .mockReset()
    .mockResolvedValue({ items: T1, has_more: false })
})

describe('Topology availability', () => {
  it('POSITIVE CONTROL: while pending, no row and no count is shown', async () => {
    vi.mocked(inventoryApi.entities).mockReturnValue(new Promise(() => {}))
    renderTopo()
    expect(await screen.findByRole('status')).toHaveAttribute(
      'aria-busy',
      'true',
    )
    expect(screen.queryByText('prod-orchestrator')).toBeNull()
    expect(screen.queryByText('Who acts')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByText('Not authorized')).toBeNull()
    expect(screen.queryByText('Nothing discovered yet')).toBeNull()
  })

  it('REPRODUCES A DEFECT: an initial 403 is a calm refusal, never a generic error, never a zero estate, never empty', async () => {
    vi.mocked(inventoryApi.entities).mockRejectedValue(forbidden())
    renderTopo()
    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByText('Something went wrong')).toBeNull()
    expect(screen.queryByText('prod-orchestrator')).toBeNull()
    expect(screen.queryByText('Nothing discovered yet')).toBeNull()
    expect(screen.queryByText('Who acts')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('REPRODUCES A DEFECT: an initial 500 is a failure with its request id and a retry, not a refusal', async () => {
    vi.mocked(inventoryApi.entities).mockRejectedValue(unavailable())
    const user = userEvent.setup()
    renderTopo()
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('Something went wrong')).toBeInTheDocument()
    expect(within(alert).getByText('req-500')).toBeInTheDocument()
    expect(screen.queryByText('Not authorized')).toBeNull()
    expect(screen.queryByText('prod-orchestrator')).toBeNull()

    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: T1,
      has_more: false,
    })
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('prod-orchestrator')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('REPRODUCES A DEFECT: a network failure is named as such, with a retry', async () => {
    vi.mocked(inventoryApi.entities).mockRejectedValue(
      new NetworkError('fetch failed'),
    )
    renderTopo()
    const alert = await screen.findByRole('alert')
    expect(
      within(alert).getByText('Control plane unreachable'),
    ).toBeInTheDocument()
    expect(
      within(alert).getByRole('button', { name: 'Retry' }),
    ).toBeInTheDocument()
    expect(screen.queryByText('prod-orchestrator')).toBeNull()
  })

  it('POSITIVE CONTROL: an admitted empty page is empty, not an error and not a refusal', async () => {
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: [],
      has_more: false,
    })
    renderTopo()
    expect(
      await screen.findByText('Nothing discovered yet'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByText('Not authorized')).toBeNull()
    expect(screen.queryByText('Who acts')).toBeNull()
  })

  it('POSITIVE CONTROL: a successful loaded page shows the bands, the rows and the kind counts of that page', async () => {
    renderTopo()
    expect(await screen.findByText('prod-orchestrator')).toBeInTheDocument()
    expect(screen.getByText('Who acts')).toBeInTheDocument()
    expect(screen.getByText('What they can use')).toBeInTheDocument()
    expect(screen.getByText('idle-agent')).toBeInTheDocument()
    expect(screen.getByText('opus')).toBeInTheDocument()
    expect(screen.getByLabelText('2 loaded')).toBeInTheDocument()
    expect(screen.getByLabelText('1 loaded')).toBeInTheDocument()
    expect(screen.queryByText('Showing first 200 entities')).toBeNull()
    expect(screen.queryByText(/not of the whole estate/)).toBeNull()
  })

  it('REPRODUCES A DEFECT: success followed by a 403 refresh shows the refusal, not the previous rows or counts as current', async () => {
    const { qc } = renderTopo()
    expect(await screen.findByText('prod-orchestrator')).toBeInTheDocument()
    vi.mocked(inventoryApi.entities).mockRejectedValue(forbidden())
    await act(async () => {
      await qc.refetchQueries({
        queryKey: inventoryKeys.entities('t1', { limit: 200 }),
      })
    })
    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(screen.queryByText('prod-orchestrator')).toBeNull()
    expect(screen.queryByText('idle-agent')).toBeNull()
    expect(screen.queryByLabelText('2 loaded')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('REPRODUCES A DEFECT: success followed by a failed refresh shows the failure, not cached rows', async () => {
    const { qc } = renderTopo()
    expect(await screen.findByText('prod-orchestrator')).toBeInTheDocument()
    vi.mocked(inventoryApi.entities).mockRejectedValue(unavailable())
    await act(async () => {
      await qc.refetchQueries({
        queryKey: inventoryKeys.entities('t1', { limit: 200 }),
      })
    })
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('req-500')).toBeInTheDocument()
    expect(screen.queryByText('prod-orchestrator')).toBeNull()
    expect(screen.queryByText('Who acts')).toBeNull()
  })

  it('POSITIVE CONTROL: retry after a failed refresh recovers the page', async () => {
    vi.mocked(inventoryApi.entities).mockRejectedValueOnce(unavailable())
    const user = userEvent.setup()
    renderTopo()
    const alert = await screen.findByRole('alert')
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('prod-orchestrator')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('REPRODUCES A DEFECT: has_more === true discloses the page limit and labels counts as this page, not the estate', async () => {
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: T1,
      has_more: true,
      cursor: 'c1',
    })
    renderTopo()
    expect(await screen.findByText('prod-orchestrator')).toBeInTheDocument()
    expect(screen.getByText('Showing first 200 entities')).toBeInTheDocument()
    expect(
      screen.getByText(
        'Counts are of loaded items on this page, not of the whole estate.',
      ),
    ).toBeInTheDocument()
    expect(screen.getByLabelText('2 loaded on this page')).toBeInTheDocument()
    expect(screen.getByLabelText('1 loaded on this page')).toBeInTheDocument()
    expect(screen.queryByLabelText('200 loaded on this page')).toBeNull()
  })

  it('POSITIVE CONTROL: a non-boolean has_more does not disclose a page limit', async () => {
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: T1,
      has_more: 'true' as unknown as boolean,
    })
    renderTopo()
    expect(await screen.findByText('prod-orchestrator')).toBeInTheDocument()
    expect(screen.queryByText('Showing first 200 entities')).toBeNull()
    expect(screen.queryByText(/not of the whole estate/)).toBeNull()
    expect(screen.getByLabelText('2 loaded')).toBeInTheDocument()
  })

  it('POSITIVE CONTROL: an arbitrary kind on the loaded page is shown, not dropped', async () => {
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: [...T1, entry('custom_widget', 'w1', 'widget-one', 'wid-1')],
      has_more: false,
    })
    renderTopo()
    expect(await screen.findByText('widget-one')).toBeInTheDocument()
    expect(screen.getByText('Other kinds')).toBeInTheDocument()
    expect(screen.getByText('custom_widget')).toBeInTheDocument()
  })

  it('REPRODUCES A DEFECT: switching tenant retires the previous page and reads under the new tenant', async () => {
    vi.mocked(inventoryApi.entities).mockImplementation(async () => {
      const tenant = auth.activeTenant
      if (tenant === 't2') return { items: T2, has_more: false }
      return { items: T1, has_more: false }
    })
    const { qc, rerender } = renderTopo()
    expect(await screen.findByText('prod-orchestrator')).toBeInTheDocument()
    expect(
      qc.getQueryData(inventoryKeys.entities('t1', { limit: 200 })),
    ).toEqual({ items: T1, has_more: false })

    auth.activeTenant = 't2'
    rerender()

    expect(await screen.findByText('billing-bot')).toBeInTheDocument()
    expect(screen.queryByText('prod-orchestrator')).toBeNull()
    expect(screen.queryByText('idle-agent')).toBeNull()
    expect(screen.getByText('docs-bucket')).toBeInTheDocument()
    await waitFor(() =>
      expect(
        qc.getQueryData(inventoryKeys.entities('t2', { limit: 200 })),
      ).toEqual({ items: T2, has_more: false }),
    )
  })

  it('POSITIVE CONTROL: keyboard activation of an entity chip selects it by its full identity', async () => {
    const user = userEvent.setup()
    const { onSelect } = renderTopo()
    const chip = await screen.findByRole('button', {
      name: 'prod-orchestrator',
    })
    chip.focus()
    expect(chip).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect.mock.calls[0]![0]).toMatchObject({
      entity_id: 'a1',
      name: 'prod-orchestrator',
      ref: 'sess-orch',
    })
  })
})
