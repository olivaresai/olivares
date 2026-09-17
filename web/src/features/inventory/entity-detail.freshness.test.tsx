// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C3 freshness comes from the current point read, not the selected catalog row.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { AuthProvider } from '@/lib/auth/context'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type { CatalogEntry, EntityDetail } from './types'
import './i18n'

const whoamiMock = vi.hoisted(() => vi.fn())

vi.mock('@/lib/api/endpoints', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/endpoints')>()
  return {
    ...actual,
    authApi: {
      ...actual.authApi,
      whoami: (...a: unknown[]) => whoamiMock(...a),
      logout: () => Promise.resolve(),
    },
  }
})

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useRouter: () => undefined,
}))

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    inventoryApi: {
      ...actual.inventoryApi,
      summary: vi.fn(),
      entities: vi.fn(),
      detail: vi.fn(),
      observations: vi.fn(),
    },
  }
})

import { inventoryApi } from './api'
import { EntityDetailSheet } from './entity-detail'

const selected: CatalogEntry = {
  kind: 'agent',
  entity_id: 'a1',
  name: 'prod-orchestrator',
  ref: 'sess-orch',
  status: 'active',
  signal_sources: ['otel'],
  hosts: ['host-1'],
  first_seen: '2026-06-03T10:00:00Z',
  last_seen: '2026-06-04T07:00:00Z',
  occurrence_count: 42,
}

const fresh: EntityDetail = {
  entry: {
    ...selected,
    last_seen: '2026-09-01T00:00:00Z',
    first_seen: '2026-05-01T00:00:00Z',
    occurred_at: '2025-12-31T23:59:59Z',
    occurrence_count: 7,
    status: 'stale',
  },
  detail: { identity_id: 'identity-7' },
}

function mount(entry: CatalogEntry | null = selected) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AuthProvider>
        <EntityDetailSheet entry={entry} onClose={() => {}} />
      </AuthProvider>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  whoamiMock.mockReset().mockResolvedValue({
    kind: 'user',
    user_id: 'u1',
    actor: 'u1',
    display_name: 'Operator',
    superadmin: false,
    grants: [
      {
        tenant: 't1',
        role: 'viewer',
        permissions: ['inventory:catalog:read'],
      },
    ],
  })
  useSessionStore.setState({
    token: 'olvs_test',
    sessionId: 's1',
    expiresAt: null,
  })
  useTenantStore.setState({ activeTenant: 't1' })
  vi.mocked(inventoryApi.observations)
    .mockReset()
    .mockResolvedValue({ items: [], has_more: false })
  vi.mocked(inventoryApi.detail).mockReset().mockResolvedValue(fresh)
})

describe('EntityDetailSheet C3 freshness', () => {
  it('does not treat the selected row as current freshness while the point read is pending', async () => {
    vi.mocked(inventoryApi.detail).mockReturnValue(new Promise(() => {}))
    mount()
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('prod-orchestrator')).toBeInTheDocument()
    expect(within(dialog).queryByText('Catalog freshness')).toBeNull()
    expect(within(dialog).queryByText('2026-06-04T07:00:00Z')).toBeNull()
    expect(within(dialog).queryByText('2026-09-01T00:00:00Z')).toBeNull()
  })

  it('shows point-read freshness on success, including a distinct occurrence instant', async () => {
    mount()
    const dialog = await screen.findByRole('dialog')
    expect(
      await within(dialog).findByText('Catalog freshness'),
    ).toBeInTheDocument()
    expect(within(dialog).getByText('2026-09-01T00:00:00Z')).toBeInTheDocument()
    expect(within(dialog).getByText('2025-12-31T23:59:59Z')).toBeInTheDocument()
    expect(within(dialog).queryByText('2026-06-04T07:00:00Z')).toBeNull()
    expect(within(dialog).getByText('7')).toBeInTheDocument()
    expect(
      within(dialog).getByText(
        'Catalog freshness and observation history are independent reads, not one atomic snapshot.',
      ),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByText(
        'Active and stale are stored catalog states. They are not health, availability, or source coverage.',
      ),
    ).toBeInTheDocument()
  })

  it('does not fall back to the selected row after 403 or 404', async () => {
    vi.mocked(inventoryApi.detail).mockRejectedValue(
      new ApiError(403, 'forbidden', 'no', 'req-403'),
    )
    const first = mount()
    const dialog = await screen.findByRole('dialog')
    expect(
      await within(dialog).findByText('Not authorized'),
    ).toBeInTheDocument()
    expect(within(dialog).queryByText('Catalog freshness')).toBeNull()
    expect(within(dialog).queryByText('2026-06-04T07:00:00Z')).toBeNull()
    first.unmount()

    vi.mocked(inventoryApi.detail).mockRejectedValue(
      new ApiError(404, 'not_found', 'gone', 'req-404'),
    )
    mount()
    const gone = await screen.findByRole('dialog')
    expect(
      await within(gone).findByText('This entry is no longer in the catalog.'),
    ).toBeInTheDocument()
    expect(within(gone).queryByText('2026-06-04T07:00:00Z')).toBeNull()
  })

  it('omits source occurrence without substituting reception or now', async () => {
    vi.mocked(inventoryApi.detail).mockResolvedValue({
      entry: { ...fresh.entry, occurred_at: undefined },
      detail: fresh.detail,
    })
    mount()
    const dialog = await screen.findByRole('dialog')
    expect(
      await within(dialog).findByText(
        'The source did not declare an occurrence instant.',
      ),
    ).toBeInTheDocument()
    expect(within(dialog).queryByText('2025-12-31T23:59:59Z')).toBeNull()
    expect(within(dialog).getByText('2026-09-01T00:00:00Z')).toBeInTheDocument()
  })

  it('unmounts the body when the sheet has no selection', async () => {
    mount(null)
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(inventoryApi.detail).not.toHaveBeenCalled()
    expect(inventoryApi.observations).not.toHaveBeenCalled()
  })
})
