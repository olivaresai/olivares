// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Collection coverage panel: the operator names ONE opened registration, nothing is
// read until the three selectors are valid, and the answer is shown as the contract
// states it — current evidence and the historical last qualified success apart,
// unknown never painted as zero or none, has_more never read as completeness.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, NetworkError } from '@/lib/api/errors'
import type {
  CollectionCoverage as CoverageValue,
  CollectionEvidence,
  CollectionPage,
  CollectionResult,
} from './types'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

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
      collections: vi.fn(),
    },
  }
})

import { inventoryApi } from './api'
import { CollectionCoverage } from './collection-coverage'
import { InventoryView } from './inventory-view'

const FP_A = 'a'.repeat(64)
const FP_B = 'b'.repeat(64)

const current = (extra: Partial<CollectionResult> = {}): CollectionResult => ({
  run_id: 'run-current-1',
  source_id: 'src-aaaa',
  source_revision: 7,
  environment_ref: 'xenv_1',
  run_order: 4,
  scope_contract: 'azure-resource-graph/2022-10-01/id-v1',
  family: 'azure.resource',
  requested_scope: FP_A,
  fulfilled_scope: FP_A,
  coverage: 'complete',
  reason: 'exhausted',
  projection: 'committed',
  admitted_count: 12,
  committed_count: 11,
  expected_count: 13,
  host_started_at: '2026-09-20T10:00:00Z',
  host_finished_at: '2026-09-20T10:05:00Z',
  producer_started_at: '2026-09-20T10:00:01Z',
  producer_finished_at: '2026-09-20T10:04:59Z',
  qualified_at: '2026-09-20T10:06:00Z',
  ...extra,
})

const lastQualified: NonNullable<CollectionEvidence['last_qualified_success']> =
  {
    run_id: 'run-qualified-0',
    source_id: 'src-aaaa',
    source_revision: 7,
    environment_ref: 'xenv_1',
    scope_contract: 'azure-resource-graph/2022-10-01/id-v1',
    family: 'azure.resource',
    requested_scope: FP_B,
    fulfilled_scope: FP_B,
    expected_count: 9,
    qualified_at: '2026-09-01T08:00:00Z',
    host_started_at: '2026-09-01T07:50:00Z',
    host_finished_at: '2026-09-01T07:55:00Z',
    producer_started_at: '2026-09-01T07:50:01Z',
    producer_finished_at: '2026-09-01T07:54:59Z',
  }

const page = (
  item: CollectionEvidence,
  extra: Partial<CollectionPage> = {},
): CollectionPage => ({ items: [item], has_more: false, ...extra })

/** The handler's answer for a selection with no stored head. */
const unmatched = (): CollectionPage =>
  page({
    current: {
      run_id: '',
      source_id: 'src-aaaa',
      source_revision: 7,
      environment_ref: 'xenv_1',
      run_order: 0,
      coverage: 'unknown',
      reason: 'missing_report',
      projection: 'pending',
      admitted_count: 0,
      committed_count: 0,
      expected_count: 0,
      host_started_at: '',
    },
  })

function mount(tenant: string | null = 't1') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const utils = render(
    <QueryClientProvider client={qc}>
      <CollectionCoverage tenant={tenant} />
    </QueryClientProvider>,
  )
  return { qc, ...utils }
}

async function select(
  user: ReturnType<typeof userEvent.setup>,
  values: { source?: string; revision?: string; environment?: string } = {},
) {
  const { source = 'src-aaaa', revision = '7', environment = 'xenv_1' } = values
  const sourceBox = screen.getByLabelText(/^Source ID/)
  const revisionBox = screen.getByLabelText(/^Source revision/)
  const environmentBox = screen.getByLabelText(/^Environment reference/)
  await user.clear(sourceBox)
  if (source !== '') await user.type(sourceBox, source)
  await user.clear(revisionBox)
  if (revision !== '') await user.type(revisionBox, revision)
  await user.clear(environmentBox)
  if (environment !== '') await user.type(environmentBox, environment)
  await user.click(screen.getByRole('button', { name: 'Read coverage' }))
}

beforeEach(async () => {
  await act(async () => {
    await i18n.changeLanguage('en')
  })
  vi.mocked(inventoryApi.collections).mockReset()
  vi.mocked(inventoryApi.collections).mockResolvedValue(
    page({ current: current(), last_qualified_success: lastQualified }),
  )
})

describe('CollectionCoverage selection', () => {
  it('starts idle and reads nothing until the operator submits a selection', async () => {
    mount()
    expect(
      screen.getByText(/Nothing is read until all three are valid/),
    ).toBeInTheDocument()
    expect(screen.getByLabelText(/^Source ID/)).toBeInTheDocument()
    expect(screen.getByLabelText(/^Source revision/)).toBeInTheDocument()
    expect(screen.getByLabelText(/^Environment reference/)).toBeInTheDocument()
    expect(inventoryApi.collections).not.toHaveBeenCalled()
  })

  it('reads nothing while a selector is missing, and says which one', async () => {
    const user = userEvent.setup()
    mount()
    await select(user, { revision: '' })
    const alerts = await screen.findAllByRole('alert')
    expect(alerts.map((a) => a.textContent)).toEqual(['Enter a value.'])
    expect(screen.getByLabelText(/^Source revision/)).toHaveAttribute(
      'aria-invalid',
      'true',
    )
    expect(screen.getByLabelText(/^Source revision/)).toHaveFocus()
    await select(user, { source: '   ', revision: '3', environment: '' })
    expect(screen.getAllByRole('alert')).toHaveLength(2)
    expect(inventoryApi.collections).not.toHaveBeenCalled()
  })

  it.each(['0', '-3', '1.5', '1e3', '12a', '99999999999999999999'])(
    'refuses revision %s as not a positive whole number',
    async (revision) => {
      const user = userEvent.setup()
      mount()
      await select(user, { revision })
      expect(await screen.findByRole('alert')).toHaveTextContent(
        'Enter a positive whole number.',
      )
      expect(inventoryApi.collections).not.toHaveBeenCalled()
    },
  )

  it('measures the 128 limit in bytes, not characters', async () => {
    const user = userEvent.setup()
    mount()
    // 43 three-byte characters: 43 characters, 129 bytes.
    await select(user, { source: '€'.repeat(43) })
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Use at most 128 bytes.',
    )
    expect(inventoryApi.collections).not.toHaveBeenCalled()
  })

  it('reads once with the trimmed selectors, a numeric revision and the tenant', async () => {
    const user = userEvent.setup()
    mount('t-view')
    await select(user, {
      source: '  src-aaaa ',
      revision: '7',
      environment: ' xenv_1',
    })
    expect(await screen.findByText('Complete')).toBeInTheDocument()
    expect(inventoryApi.collections).toHaveBeenCalledTimes(1)
    const [selection, opts] = vi.mocked(inventoryApi.collections).mock.calls[0]!
    expect(selection).toEqual({
      source_id: 'src-aaaa',
      source_revision: 7,
      environment_ref: 'xenv_1',
    })
    expect(opts).toMatchObject({ tenant: 't-view' })
  })

  it('shows a loading status while the read is in flight', async () => {
    vi.mocked(inventoryApi.collections).mockReturnValue(new Promise(() => {}))
    const user = userEvent.setup()
    mount()
    await select(user)
    expect(
      await screen.findByText('Reading collection coverage'),
    ).toBeInTheDocument()
    expect(
      screen
        .getByText('Reading collection coverage')
        .closest('[role="status"]'),
    ).toHaveAttribute('aria-busy', 'true')
  })
})

describe('CollectionCoverage evidence', () => {
  const labels: Record<CoverageValue, string> = {
    complete: 'Complete',
    partial: 'Partial',
    unavailable: 'Unavailable',
    unsupported: 'Unsupported',
    unknown: 'Unknown',
  }

  it.each(Object.entries(labels))(
    'renders coverage %s as the text label %s',
    async (value, label) => {
      vi.mocked(inventoryApi.collections).mockResolvedValue(
        page({ current: current({ coverage: value as CoverageValue }) }),
      )
      const user = userEvent.setup()
      mount()
      await select(user)
      const region = await screen.findByRole('region', {
        name: 'Current collection evidence',
      })
      expect(within(region).getByText(label)).toBeInTheDocument()
    },
  )

  it('states the scope of the answer: this registration and query, not a global snapshot', async () => {
    const user = userEvent.setup()
    mount()
    await select(user)
    expect(
      await screen.findByText(
        /It is not a global estate snapshot and not a provider authorization/,
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/Empty completion does not delete resources/),
    ).toBeInTheDocument()
  })

  it('renders the current run identity, counts and fingerprints', async () => {
    const user = userEvent.setup()
    mount()
    await select(user)
    const region = await screen.findByRole('region', {
      name: 'Current collection evidence',
    })
    expect(within(region).getByText('run-current-1')).toBeInTheDocument()
    expect(within(region).getByText('12')).toBeInTheDocument()
    expect(within(region).getByText('11')).toBeInTheDocument()
    expect(within(region).getByText('13')).toBeInTheDocument()
    expect(within(region).getAllByText(FP_A)).toHaveLength(2)
    expect(within(region).getByText('exhausted')).toBeInTheDocument()
    expect(within(region).getByText('Committed')).toBeInTheDocument()
    expect(within(region).getByText('2026-09-20T10:06:00Z')).toBeInTheDocument()
  })

  it('shows an unmatched selection as unknown, never as zero or none', async () => {
    vi.mocked(inventoryApi.collections).mockResolvedValue(unmatched())
    const user = userEvent.setup()
    mount()
    await select(user)
    const region = await screen.findByRole('region', {
      name: 'Current collection evidence',
    })
    expect(within(region).getByText('Unknown')).toBeInTheDocument()
    expect(
      within(region).getByText(/No collection run is recorded/),
    ).toBeInTheDocument()
    expect(
      within(region).getByText(/never as none or as inherited completeness/),
    ).toBeInTheDocument()
    expect(within(region).queryByText('0')).toBeNull()
    expect(within(region).queryByText(/^none$/i)).toBeNull()
    expect(within(region).queryByText('Admitted members')).toBeNull()
    expect(within(region).queryByText('Expected members')).toBeNull()
    expect(within(region).queryByText('Complete')).toBeNull()
  })

  it('does not paint the expected count of a run the host has not closed as zero', async () => {
    vi.mocked(inventoryApi.collections).mockResolvedValue(
      page({
        current: current({
          coverage: 'unknown',
          reason: 'missing_report',
          projection: 'pending',
          admitted_count: 3,
          committed_count: 0,
          expected_count: 0,
          host_finished_at: undefined,
          producer_finished_at: undefined,
          qualified_at: undefined,
        }),
      }),
    )
    const user = userEvent.setup()
    mount()
    await select(user)
    const region = await screen.findByRole('region', {
      name: 'Current collection evidence',
    })
    expect(
      within(region).getByText('Not recorded until the host closes the run.'),
    ).toBeInTheDocument()
    expect(within(region).getByText('3')).toBeInTheDocument()
  })

  it('shows the last qualified success in its own section, apart from the current run', async () => {
    const user = userEvent.setup()
    mount()
    await select(user)
    const currentRegion = await screen.findByRole('region', {
      name: 'Current collection evidence',
    })
    const historic = screen.getByRole('region', {
      name: 'Last qualified success',
    })
    expect(within(historic).getByText('run-qualified-0')).toBeInTheDocument()
    expect(within(historic).getByText('9')).toBeInTheDocument()
    expect(within(historic).getAllByText(FP_B)).toHaveLength(2)
    expect(
      within(historic).getByText(/separate from the current enumeration/),
    ).toBeInTheDocument()
    expect(within(currentRegion).queryByText('run-qualified-0')).toBeNull()
    expect(within(historic).queryByText('run-current-1')).toBeNull()
  })

  it('states the absence of a last qualified success instead of hiding the section', async () => {
    vi.mocked(inventoryApi.collections).mockResolvedValue(
      page({ current: current({ coverage: 'partial', reason: 'page_limit' }) }),
    )
    const user = userEvent.setup()
    mount()
    await select(user)
    const historic = await screen.findByRole('region', {
      name: 'Last qualified success',
    })
    expect(
      within(historic).getByText(
        /No qualified success is reported for this registration and scope/,
      ),
    ).toBeInTheDocument()
  })

  it('says has_more is a list flag, not a completeness signal', async () => {
    vi.mocked(inventoryApi.collections).mockResolvedValue(
      page({ current: current() }, { has_more: true, cursor: 'next' }),
    )
    const user = userEvent.setup()
    mount()
    await select(user)
    expect(
      await screen.findByText(/not evidence of collection completeness/),
    ).toBeInTheDocument()
  })

  it('says an empty completion does not delete resources', async () => {
    vi.mocked(inventoryApi.collections).mockResolvedValue(
      page({
        current: current({
          admitted_count: 0,
          committed_count: 0,
          expected_count: 0,
        }),
      }),
    )
    const user = userEvent.setup()
    mount()
    await select(user)
    const region = await screen.findByRole('region', {
      name: 'Current collection evidence',
    })
    expect(
      within(region).getByText(
        'This run completed with no members. Empty completion does not delete resources from the catalog.',
      ),
    ).toBeInTheDocument()
  })
})

describe('CollectionCoverage failures', () => {
  it('gives a 400 its own invalid-selection copy', async () => {
    vi.mocked(inventoryApi.collections).mockRejectedValue(
      new ApiError(400, 'bad_request', 'required', 'req-400'),
    )
    const user = userEvent.setup()
    mount()
    await select(user)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Selection refused')
    expect(alert).toHaveTextContent(
      'Source ID and environment reference must be nonempty and at most 128 bytes',
    )
    expect(alert).not.toHaveTextContent('Something went wrong')
  })

  it('gives a 423 the suspended-tenant copy', async () => {
    vi.mocked(inventoryApi.collections).mockRejectedValue(
      new ApiError(423, 'locked', 'suspended', 'req-423'),
    )
    const user = userEvent.setup()
    mount()
    await select(user)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Tenant service suspended')
    expect(alert).toHaveTextContent(
      "This tenant's service is suspended or not in service",
    )
  })

  it('shows a generic failure with its request id and a retry, not an empty answer', async () => {
    vi.mocked(inventoryApi.collections).mockRejectedValue(
      new ApiError(500, 'internal', 'store failed', 'req-500'),
    )
    const user = userEvent.setup()
    mount()
    await select(user)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Something went wrong')
    expect(alert).toHaveTextContent('req-500')
    expect(screen.queryByText('Unknown')).toBeNull()
    vi.mocked(inventoryApi.collections).mockResolvedValue(unmatched())
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Unknown')).toBeInTheDocument()
    expect(inventoryApi.collections).toHaveBeenCalledTimes(2)
  })

  it('shows a network failure as unreachable', async () => {
    vi.mocked(inventoryApi.collections).mockRejectedValue(
      new NetworkError('offline'),
    )
    const user = userEvent.setup()
    mount()
    await select(user)
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Control plane unreachable',
    )
  })

  it('shows a 403 as the calm forbidden state', async () => {
    vi.mocked(inventoryApi.collections).mockRejectedValue(
      new ApiError(403, 'forbidden', 'no', 'req-403'),
    )
    const user = userEvent.setup()
    mount()
    await select(user)
    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
  })
})

describe('InventoryView collection coverage tab', () => {
  it('reaches the coverage panel from the inventory view without reading it', async () => {
    vi.mocked(inventoryApi.summary).mockResolvedValue({
      by_kind: {},
      by_source: {},
      total: 0,
    })
    vi.mocked(inventoryApi.entities).mockResolvedValue({
      items: [],
      has_more: false,
    })
    const user = userEvent.setup()
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={qc}>
        <InventoryView />
      </QueryClientProvider>,
    )
    await user.click(
      await screen.findByRole('tab', { name: 'Collection coverage' }),
    )
    expect(screen.getByLabelText(/^Source ID/)).toBeInTheDocument()
    expect(inventoryApi.collections).not.toHaveBeenCalled()
    await select(user)
    expect(await screen.findByText('Complete')).toBeInTheDocument()
    expect(vi.mocked(inventoryApi.collections).mock.calls[0]![1]).toMatchObject(
      {
        tenant: 't1',
      },
    )
  })
})
