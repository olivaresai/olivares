// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Truth-criticals: rotate-auth carries the NEW credential (the endpoint
// 400s on an empty body), edit PRESERVES the SIEM sink profile (omitting
// sink_* would make the backend delete it), "Load more" actually paginates,
// and the delivery filters are real controls fed from the live rosters.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    eventTypes: vi.fn(),
    subscriptions: vi.fn(),
    subscription: vi.fn(),
    createSubscription: vi.fn(),
    updateSubscription: vi.fn(),
    deleteSubscription: vi.fn(),
    rotateSecret: vi.fn(),
    rotateAuth: vi.fn(),
    testSubscription: vi.fn(),
    replayEvents: vi.fn(),
    subscriptionRevisions: vi.fn(),
    restoreSubscription: vi.fn(),
    events: vi.fn(),
    deliveries: vi.fn(),
    deadLetters: vi.fn(),
    redeliver: vi.fn(),
    egressPolicyStatus: vi.fn(),
    egressCompatReport: vi.fn(),
  },
  authState: {
    can: (_p: string): boolean => true,
    activeTenant: 't1' as string | null,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  eventingApi: api,
}))

import { EventingView } from './eventing-view'

const sub = {
  id: 's1',
  name: 'siem-hook',
  enabled: true,
  event_types: ['audit.event'],
  match_sources: [],
  endpoint: 'https://siem.example.com/ingest',
  secret_hint: 'abc123def456',
  role: 'viewer',
  description: '',
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  auth_type: 'bearer' as const,
  auth_value_hint: 'ff00aa11bb22',
  auth_header_name: '',
  max_attempts: 0,
  initial_interval_seconds: 0,
  sink_kind: 'splunk_hec',
  sink_format: 'ocsf',
  sink_opts: { index: 'olv-audit' },
  sink_cred_hint: 'aa11bb22cc33',
}

const eventTypeCatalog = {
  event_types: [
    {
      type: 'audit.event',
      stability: 'stable',
      permission: 'audit:event:read',
      description: 'Audit ledger event',
    },
  ],
}

function makeEvent(seq: number) {
  return {
    id: `e${seq}`,
    seq,
    type: 'audit.event',
    occurred_at: '2026-07-01T00:00:00Z',
    source: `source-${seq}`,
    payload: null,
  }
}

function makeDelivery(id: string) {
  return {
    id,
    subscription: 's1',
    event_id: `ev-${id}`,
    event_seq: 1,
    event_type: 'audit.event',
    status: 'delivered' as const,
    origin: 'live' as const,
    attempts: 1,
    next_attempt_at: '',
    last_attempt_at: '2026-07-01T00:00:00Z',
    last_status: '200',
  }
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return {
    ...render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>),
    qc,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.subscriptions.mockResolvedValue({ items: [sub], has_more: false })
  api.subscription.mockResolvedValue(sub)
  api.eventTypes.mockResolvedValue(eventTypeCatalog)
  api.events.mockResolvedValue({
    items: [makeEvent(1)],
    next_seq: 2,
    has_more: false,
  })
  api.deliveries.mockResolvedValue({
    items: [makeDelivery('d1')],
    has_more: false,
  })
  api.deadLetters.mockResolvedValue({ items: [], has_more: false })
  api.egressPolicyStatus.mockResolvedValue({
    in_force: true,
    mode: 'enforced',
    enforcement_committed: true,
    writer_fence: { armed: true, required_capability: 1, binary_capability: 1 },
  })
  api.egressCompatReport.mockResolvedValue({
    seeded: true,
    intact: true,
    subscriptions: 0,
    unparsed: 0,
    authorities: [],
    still_needed: 0,
  })
  api.rotateAuth.mockResolvedValue(sub)
  api.updateSubscription.mockResolvedValue(sub)
  api.replayEvents.mockResolvedValue({
    replayed: 2,
    next_seq: 3,
    has_more: false,
  })
  api.subscriptionRevisions.mockResolvedValue({ items: [], has_more: false })
  api.restoreSubscription.mockResolvedValue(sub)
})

describe('EventingView revision history', () => {
  it('keeps History available with read permission alone', async () => {
    authState.can = (permission) => permission === 'eventing:subscription:read'
    const user = userEvent.setup()
    wrap(<EventingView />)

    await user.click(
      await screen.findByRole('button', {
        name: /view revision history for siem-hook/i,
      }),
    )

    await waitFor(() =>
      expect(api.subscriptionRevisions).toHaveBeenCalledWith('s1', {
        cursor: undefined,
        limit: 50,
      }),
    )
    expect(
      await screen.findByRole('dialog', {
        name: /revision history — siem-hook/i,
      }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /restore this revision/i }),
    ).not.toBeInTheDocument()
  })
})

describe('EventingView bulk subscription state', () => {
  it('selects subscriptions, updates each current item and invalidates the list', async () => {
    const first = { ...sub, enabled: false }
    const second = {
      ...sub,
      id: 's2',
      name: 'billing-hook',
      enabled: false,
      endpoint: 'https://billing.example.com/ingest',
      sink_opts: { index: 'olv-billing' },
    }
    api.subscriptions.mockResolvedValue({
      items: [first, second],
      has_more: false,
    })
    api.subscription.mockImplementation(async (id: string) =>
      id === 's1' ? first : second,
    )
    api.updateSubscription.mockImplementation(
      async (id: string, body: typeof sub) => ({ ...body, id }),
    )
    const user = userEvent.setup()
    const { qc } = wrap(<EventingView />)
    const invalidate = vi.spyOn(qc, 'invalidateQueries')

    await user.click(
      await screen.findByRole('checkbox', {
        name: /select all visible rows/i,
      }),
    )
    await user.click(screen.getByRole('button', { name: /enable selected/i }))

    await waitFor(() => expect(api.updateSubscription).toHaveBeenCalledTimes(2))
    expect(api.subscription.mock.calls.map(([id]) => id)).toEqual(['s1', 's2'])
    expect(api.updateSubscription).toHaveBeenNthCalledWith(
      1,
      's1',
      expect.objectContaining({
        name: 'siem-hook',
        enabled: true,
        event_types: ['audit.event'],
        endpoint: 'https://siem.example.com/ingest',
        sink_kind: 'splunk_hec',
        sink_opts: { index: 'olv-audit' },
      }),
    )
    expect(api.updateSubscription).toHaveBeenNthCalledWith(
      2,
      's2',
      expect.objectContaining({
        name: 'billing-hook',
        enabled: true,
        sink_opts: { index: 'olv-billing' },
      }),
    )
    expect(invalidate).toHaveBeenCalledTimes(2)
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ['eventing', 't1', 'subscriptions'],
    })
  })
})

describe('EventingView replay', () => {
  it('replays a range and opens replay-filtered deliveries', async () => {
    const user = userEvent.setup()
    wrap(<EventingView />)
    await user.click(
      await screen.findByRole('button', { name: /actions for siem-hook/i }),
    )
    await user.click(await screen.findByRole('menuitem', { name: /^replay$/i }))
    const dialog = await screen.findByRole('dialog')
    await user.clear(within(dialog).getByLabelText(/from sequence/i))
    await user.type(within(dialog).getByLabelText(/from sequence/i), '10')
    await user.click(within(dialog).getByRole('button', { name: /^replay$/i }))
    await waitFor(() =>
      expect(api.replayEvents).toHaveBeenCalledWith('s1', { from_seq: 10 }),
    )
    await user.click(
      within(dialog).getByRole('button', { name: /view replayed deliveries/i }),
    )
    await waitFor(() =>
      expect(api.deliveries).toHaveBeenCalledWith(
        expect.objectContaining({ origin: 'replay' }),
      ),
    )
  })
})

describe('EventingView rotate-auth (E3a)', () => {
  it('collects the new credential in a dialog and sends it in the body', async () => {
    const user = userEvent.setup()
    wrap(<EventingView />)

    await user.click(
      await screen.findByRole('button', { name: /actions for siem-hook/i }),
    )
    await user.click(
      await screen.findByRole('menuitem', { name: /rotate auth/i }),
    )

    const dialog = await screen.findByRole('dialog')
    // Nothing was posted before the operator supplies the credential.
    expect(api.rotateAuth).not.toHaveBeenCalled()

    const input = within(dialog).getByLabelText(/new credential/i)
    expect(input).toHaveAttribute('type', 'password')
    await user.type(input, 'tok-rotated-1')
    await user.click(
      within(dialog).getByRole('button', { name: /rotate credential/i }),
    )

    await waitFor(() =>
      expect(api.rotateAuth).toHaveBeenCalledWith('s1', {
        auth_value: 'tok-rotated-1',
      }),
    )
  })
})

describe('EventingView sink profile (E3b)', () => {
  it('shows the sink badge on the subscription card', async () => {
    wrap(<EventingView />)
    expect(await screen.findByText('Splunk HEC')).toBeInTheDocument()
  })

  it('re-sends the existing sink profile on an untouched edit (never deletes it)', async () => {
    const user = userEvent.setup()
    wrap(<EventingView />)

    await user.click(
      await screen.findByRole('button', { name: /actions for siem-hook/i }),
    )
    await user.click(await screen.findByRole('menuitem', { name: /^edit$/i }))

    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: /save changes/i }),
    )

    await waitFor(() =>
      expect(api.updateSubscription).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({
          sink_kind: 'splunk_hec',
          sink_format: 'ocsf',
          sink_opts: { index: 'olv-audit' },
        }),
      ),
    )
    // An untouched edit must NOT re-send a credential — empty means "keep the
    // sealed one" on the backend.
    const body = api.updateSubscription.mock.calls[0][1] as Record<
      string,
      unknown
    >
    expect(body.sink_cred).toBeUndefined()
  })

  it('preserves a newer sink profile when the list snapshot is stale', async () => {
    api.subscription.mockResolvedValue({
      ...sub,
      sink_kind: 'datadog',
      sink_format: 'json',
      sink_opts: { service: 'olivares-audit' },
      sink_cred_hint: 'newer-hint',
    })
    const user = userEvent.setup()
    wrap(<EventingView />)

    await user.click(
      await screen.findByRole('button', { name: /actions for siem-hook/i }),
    )
    await user.click(await screen.findByRole('menuitem', { name: /^edit$/i }))
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: /save changes/i,
      }),
    )

    await waitFor(() =>
      expect(api.updateSubscription).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({
          sink_kind: 'datadog',
          sink_format: 'json',
          sink_opts: { service: 'olivares-audit' },
        }),
      ),
    )
  })

  it('aborts the edit when the fresh profile cannot be loaded', async () => {
    api.subscription.mockRejectedValue(new Error('profile unavailable'))
    const user = userEvent.setup()
    wrap(<EventingView />)

    await user.click(
      await screen.findByRole('button', { name: /actions for siem-hook/i }),
    )
    await user.click(await screen.findByRole('menuitem', { name: /^edit$/i }))
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: /save changes/i,
      }),
    )

    await waitFor(() => expect(api.subscription).toHaveBeenCalledWith('s1'))
    expect(api.updateSubscription).not.toHaveBeenCalled()
  })
})

describe('EventingView pagination (E3c)', () => {
  it('walks the events seq cursor with a working Load more button', async () => {
    api.events
      .mockResolvedValueOnce({
        items: [makeEvent(1)],
        next_seq: 2,
        has_more: true,
      })
      .mockResolvedValueOnce({
        items: [makeEvent(2)],
        next_seq: 3,
        has_more: false,
      })

    const user = userEvent.setup()
    wrap(<EventingView />)
    await user.click(screen.getByRole('tab', { name: /events/i }))

    await screen.findByText('source-1')
    await user.click(await screen.findByRole('button', { name: /load more/i }))

    await waitFor(() =>
      expect(api.events).toHaveBeenCalledWith(
        expect.objectContaining({ since_seq: 2 }),
      ),
    )
    // Both pages stay rendered (accumulated, not replaced).
    expect(await screen.findByText('source-2')).toBeInTheDocument()
    expect(screen.getByText('source-1')).toBeInTheDocument()
    expect(screen.getByText(/no more events/i)).toBeInTheDocument()
  })

  it('walks the dead-letter cursor with Load more', async () => {
    api.deadLetters
      .mockResolvedValueOnce({
        items: [makeDelivery('dl1')],
        cursor: 'c1',
        has_more: true,
      })
      .mockResolvedValueOnce({
        items: [makeDelivery('dl2')],
        has_more: false,
      })

    const user = userEvent.setup()
    wrap(<EventingView />)
    await user.click(screen.getByRole('tab', { name: /dead letters/i }))

    await user.click(await screen.findByRole('button', { name: /load more/i }))
    await waitFor(() =>
      expect(api.deadLetters).toHaveBeenCalledWith(
        expect.objectContaining({ cursor: 'c1' }),
      ),
    )
  })
})

describe('EventingView delivery filters (E3d)', () => {
  it('filters by subscription from the real subscription roster', async () => {
    const user = userEvent.setup()
    wrap(<EventingView />)
    await user.click(screen.getByRole('tab', { name: /deliveries/i }))

    // First load: unfiltered.
    await waitFor(() =>
      expect(api.deliveries).toHaveBeenCalledWith(
        expect.objectContaining({ subscription: undefined }),
      ),
    )

    await user.click(screen.getByRole('combobox', { name: /subscription/i }))
    await user.click(await screen.findByRole('option', { name: 'siem-hook' }))

    await waitFor(() =>
      expect(api.deliveries).toHaveBeenCalledWith(
        expect.objectContaining({ subscription: 's1' }),
      ),
    )
  })

  it('filters by event type from the server catalog', async () => {
    const user = userEvent.setup()
    wrap(<EventingView />)
    await user.click(screen.getByRole('tab', { name: /deliveries/i }))
    await screen.findByRole('combobox', { name: /event type/i })

    await user.click(screen.getByRole('combobox', { name: /event type/i }))
    await user.click(await screen.findByRole('option', { name: 'audit.event' }))

    await waitFor(() =>
      expect(api.deliveries).toHaveBeenCalledWith(
        expect.objectContaining({ event_type: 'audit.event' }),
      ),
    )
  })
})
