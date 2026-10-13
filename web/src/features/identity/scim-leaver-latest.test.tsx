// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The SCIM leaver card shows the NEWEST deprovision events.
//
// The audit ledger pages FORWARDS from `from` in ascending sequence order, so one
// `limit: 10` read returned the ten OLDEST leavers: past ten events a tenant never saw
// its latest ones. The card is mounted against a ledger fake that answers the way the
// engine does (core/api/handlers_audit.go): an ascending filtered walk from `from`, a
// `limit` outside 1..1000 falls back to 100, and `has_more` plus `next_from` while a
// page is full. A card that reads one short page, or asks for more than the engine's
// page maximum, fails here and not in a customer's tenant.
//
// This file also holds the cut-short notice tests for the leaver list. They were written
// against `{ items, has_more }` replies with no `next_from`, which a ledger never sends
// while it has more pages; they now run against the ledger fake.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    ssoStatus: vi.fn(),
    scimServiceProviderConfig: vi.fn(),
    scimUsers: vi.fn(),
    audit: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    principal: null,
    can: (_permission: string) => true,
  },
}))

vi.mock('@tanstack/react-router', () => ({ useNavigate: () => vi.fn() }))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, identityApi: api }
})

// Import AFTER the mocks are declared.
import { identityKeys } from './api'
import { FederationTab } from './federation'

const LEAVER_ACTION = 'scim.user.deprovision'
/** core/api/handlers_audit.go: a limit above 1000 (or below 1) falls back to 100. */
const ENGINE_PAGE_MAX = 1000

interface AuditParams {
  action?: string
  from?: number
  limit?: number
}

/** A chain of `leavers` deprovision events, each followed by one unrelated event, so a
 *  sequence is not the same number as the leaver's position in the list. `usr_N` is the
 *  Nth leaver and the newest one is `usr_<leavers>`. Events carry the engine's own field
 *  names (core/api/dto.go AuditEventDTO: `seq`, `occurred_at`). */
function ledgerOf(leavers: number) {
  const chain = Array.from({ length: leavers * 2 }, (_, i) =>
    i % 2 === 0
      ? {
          id: `ev_${i + 1}`,
          seq: i + 1,
          action: LEAVER_ACTION,
          target_id: `usr_${i / 2 + 1}`,
          occurred_at: '2026-06-07T00:00:00Z',
        }
      : { id: `ev_${i + 1}`, seq: i + 1, action: 'session.login' },
  )
  return async ({ action = '', from = 1, limit = 100 }: AuditParams) => {
    const size = limit >= 1 && limit <= ENGINE_PAGE_MAX ? limit : 100
    const matches = chain.filter(
      (e) => e.seq >= from && e.action.startsWith(action),
    )
    const items = matches.slice(0, size)
    const hasMore = matches.length > size
    return {
      items,
      has_more: hasMore,
      next_from: hasMore ? items[items.length - 1].seq + 1 : chain.length + 1,
      head_seq: chain.length,
    }
  }
}

function mount(ui: React.ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
  return client
}

/** The target ids the card lists, top to bottom. */
function listedLeavers() {
  return screen.getAllByText(/^usr_\d+$/).map((el) => el.textContent)
}

beforeEach(() => {
  api.ssoStatus.mockRejectedValue(new Error('not under test'))
  api.scimServiceProviderConfig.mockRejectedValue(new Error('not under test'))
  api.scimUsers.mockRejectedValue(new Error('not under test'))
  api.audit.mockImplementation(ledgerOf(0))
})

afterEach(() => {
  vi.clearAllMocks()
})

describe('SCIM leaver evidence shows the newest events', () => {
  it('lists the ten newest leavers, newest first, when the ledger has more', async () => {
    api.audit.mockImplementation(ledgerOf(25))
    mount(<FederationTab />)
    await screen.findByText('usr_25')
    expect(listedLeavers()).toEqual([
      'usr_25',
      'usr_24',
      'usr_23',
      'usr_22',
      'usr_21',
      'usr_20',
      'usr_19',
      'usr_18',
      'usr_17',
      'usr_16',
    ])
  })

  it('reaches the newest leaver when the ledger spans several engine pages', async () => {
    api.audit.mockImplementation(ledgerOf(2500))
    mount(<FederationTab />)
    await screen.findByText('usr_2500')
    expect(listedLeavers()[0]).toBe('usr_2500')
    expect(listedLeavers()).toHaveLength(10)
    // 2,500 matches at the engine's 1,000-row page maximum: three reads, each
    // asking for the leaver action and never above the maximum.
    expect(api.audit).toHaveBeenCalledTimes(3)
    for (const [params, opts] of api.audit.mock.calls as [
      AuditParams,
      { signal?: AbortSignal },
    ][]) {
      expect(params.action).toBe(LEAVER_ACTION)
      expect(params.limit).toBeLessThanOrEqual(ENGINE_PAGE_MAX)
      expect(opts.signal).toBeInstanceOf(AbortSignal)
    }
  })

  it('takes the newest ten from two pages when the last page holds only one', async () => {
    // 1,001 leavers: the first page is usr_1..usr_1000 and the second only usr_1001, so
    // the ten newest are nine rows of page one and the single row of page two.
    api.audit.mockImplementation(ledgerOf(1001))
    mount(<FederationTab />)
    await screen.findByText('usr_1001')
    expect(listedLeavers()).toEqual(
      Array.from({ length: 10 }, (_, i) => `usr_${1001 - i}`),
    )
    expect(api.audit).toHaveBeenCalledTimes(2)
  })

  it('reads one page when the ledger holds exactly one full page of leavers', async () => {
    api.audit.mockImplementation(ledgerOf(ENGINE_PAGE_MAX))
    mount(<FederationTab />)
    await screen.findByText(`usr_${ENGINE_PAGE_MAX}`)
    expect(listedLeavers()[0]).toBe(`usr_${ENGINE_PAGE_MAX}`)
    expect(api.audit).toHaveBeenCalledTimes(1)
  })

  it('sends no further page once the query is cancelled mid-walk', async () => {
    // A tenant switch cancels the old tenant's queries. The page already in flight is
    // discarded by the query; the next page must not be sent at all, because the client
    // addresses each request with the tenant that is active when it is sent.
    const ledger = ledgerOf(2500)
    let open!: () => void
    const gate = new Promise<void>((resolve) => (open = resolve))
    api.audit.mockImplementationOnce(async (params: AuditParams) => {
      await gate
      return ledger(params)
    })
    const client = mount(<FederationTab />)
    await waitFor(() => expect(api.audit).toHaveBeenCalledTimes(1))
    await act(() =>
      client.cancelQueries({
        queryKey: identityKeys.audit('t1', LEAVER_ACTION),
      }),
    )
    open()
    await act(async () => {
      await gate
    })
    expect(api.audit).toHaveBeenCalledTimes(1)
  })

  it('lists every leaver, with no cut-short notice, when there are ten or fewer', async () => {
    api.audit.mockImplementation(ledgerOf(10))
    mount(<FederationTab />)
    await screen.findByText('usr_10')
    expect(listedLeavers()).toHaveLength(10)
    expect(listedLeavers()[9]).toBe('usr_1')
    expect(screen.queryByText(/older ones/i)).not.toBeInTheDocument()
  })

  it('shows the empty state for a ledger with no leaver', async () => {
    mount(<FederationTab />)
    await screen.findByText('No deprovision events')
    expect(screen.queryByText(/older ones/i)).not.toBeInTheDocument()
  })

  it.each([
    ['gives no next_from', undefined],
    ['gives a next_from that does not move forward', 1],
  ])(
    'fails visibly when a page says there is more and %s',
    async (_name, nextFrom) => {
      api.audit.mockResolvedValue({
        items: [{ id: 'ev_1', action: LEAVER_ACTION, target_id: 'usr_1' }],
        has_more: true,
        next_from: nextFrom,
      })
      mount(<FederationTab />)
      // The rows on that page are the OLDEST: showing them as the latest is the defect.
      // The assertions are scoped to the leaver card: the SCIM config section above
      // it fails too in this file and paints the same error copy.
      const heading = await screen.findByRole('heading', {
        name: 'Leaver evidence',
      })
      const card = heading.parentElement as HTMLElement
      await within(card).findByText(/something went wrong/i)
      expect(within(card).queryByText('usr_1')).not.toBeInTheDocument()
    },
  )
})

describe('SCIM leaver evidence declares a cut-short list', () => {
  it('names the rows shown and that older events exist', async () => {
    api.audit.mockImplementation(ledgerOf(11))
    mount(<FederationTab />)
    expect(
      await screen.findByText('Newest 10 leaver events; there are older ones'),
    ).toBeVisible()
  })

  it('does not warn when the list is complete', async () => {
    api.audit.mockImplementation(ledgerOf(1))
    mount(<FederationTab />)
    await screen.findByText('usr_1')
    expect(screen.queryByText(/older ones/i)).not.toBeInTheDocument()
  })

  it('does not contradict the empty state when pages come back empty but the scan goes on', async () => {
    // A bounded scan can end a page with no match and `has_more`: the walk follows
    // `next_from` to the end, and a ledger with no leaver must not carry a notice.
    api.audit
      .mockResolvedValueOnce({ items: [], has_more: true, next_from: 20001 })
      .mockResolvedValueOnce({ items: [], has_more: false, next_from: 20001 })
    mount(<FederationTab />)
    await screen.findByText('No deprovision events')
    expect(api.audit).toHaveBeenCalledTimes(2)
    expect(screen.queryByText(/older ones/i)).not.toBeInTheDocument()
  })

  it('drops the notice when a refetch fails over the stale page', async () => {
    api.audit.mockImplementation(ledgerOf(11))
    const client = mount(<FederationTab />)
    await screen.findByText('Newest 10 leaver events; there are older ones')
    api.audit.mockRejectedValue(new Error('engine unavailable'))
    await act(() =>
      client.refetchQueries({
        queryKey: identityKeys.audit('t1', LEAVER_ACTION),
      }),
    )
    await waitFor(() =>
      expect(screen.queryByText(/older ones/i)).not.toBeInTheDocument(),
    )
  })
})
