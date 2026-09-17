// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Inventory entity sheet — destination links follow the registered navigation
// authority (2026-09-08).
//
// On the base, EntityDetailSheet always rendered Access-map / Sessions links for
// certain kinds, independently of whether those destinations were navigable.
// These causals pin the correction: the real `useViewAccess` over the real
// FEATURE_VIEWS decides, `can()` is the real membership test over a whoami
// fixture, and a generic href never invents an entity edge. The guard under
// test is NOT mocked.
//
// Doubles, named here and nowhere else:
//   · AUTH DOUBLE — `authApi.whoami` answers with per-tenant permission sets
//     taken from the registry entries themselves.
//   · API DOUBLE — `inventoryApi.detail` only; the sheet's point read is not
//     the subject of this battery.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FEATURE_VIEWS } from '@/features/registry'
import { queryKeys } from '@/lib/api/query'
import type { Grant, Whoami } from '@/lib/api/types'
import { AuthProvider } from '@/lib/auth/context'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { useWorkspaceStore } from '@/stores/workspace'
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
      summary: vi.fn(),
      entities: vi.fn(),
      detail: vi.fn(),
      observations: vi.fn(),
    },
  }
})

import { inventoryApi } from './api'
import { EntityDetailSheet } from './entity-detail'

const ACCESS_MAP = FEATURE_VIEWS.find((v) => v.id === 'accessMap')!
const SESSIONS = FEATURE_VIEWS.find((v) => v.id === 'sessions')!

const grant = (tenant: string, permissions: string[]): Grant => ({
  tenant,
  role: 'viewer',
  permissions,
})

const principal = (grants: Grant[]): Whoami => ({
  kind: 'user',
  user_id: 'u1',
  actor: 'u1',
  display_name: 'Operator',
  superadmin: false,
  grants,
})

const entry = (
  kind: string,
  id: string,
  name: string,
  ref: string,
): CatalogEntry => ({
  kind,
  entity_id: id,
  name,
  ref,
  status: 'active',
  signal_sources: ['otel'],
  hosts: ['host-1'],
  first_seen: '2026-06-03T10:00:00Z',
  last_seen: '2026-06-04T07:00:00Z',
  occurrence_count: 1,
})

const AGENT = entry('agent', 'a1', 'prod-orchestrator', 'sess-orch')
const SESSION = entry('session', 's1', 'live-session', 'sess-live')
const IDENTITY = entry('identity', 'i1', 'svc-bot', 'id-bot')
const RESOURCE = entry('resource', 'r1', 'docs-bucket', 'res-docs')
const MODEL = entry('model', 'm1', 'opus', 'mdl-opus')

const detailOf = (e: CatalogEntry): EntityDetail => ({
  entry: e,
  detail: { identity_id: 'identity-7' },
})

const BOTH = [ACCESS_MAP.permission!, SESSIONS.permission!]
const ACCESS_ONLY = [ACCESS_MAP.permission!]
const SESSIONS_ONLY = [SESSIONS.permission!]
const NONE: string[] = []

function mount(row: CatalogEntry, qc?: QueryClient) {
  const client =
    qc ?? new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const tree = (
    <QueryClientProvider client={client}>
      <AuthProvider>
        <EntityDetailSheet entry={row} onClose={() => {}} />
      </AuthProvider>
    </QueryClientProvider>
  )
  const utils = render(tree)
  return {
    qc: client,
    ...utils,
    rerender: (next: CatalogEntry) =>
      utils.rerender(
        <QueryClientProvider client={client}>
          <AuthProvider>
            <EntityDetailSheet entry={next} onClose={() => {}} />
          </AuthProvider>
        </QueryClientProvider>,
      ),
  }
}

const sheet = () => screen.findByRole('dialog')

const linksIn = (node: HTMLElement) => ({
  access: within(node).queryByRole('link', { name: 'Access map' }),
  sessions: within(node).queryByRole('link', { name: 'Sessions' }),
})

const settleWhoami = async (qc: QueryClient) => {
  const dialog = await sheet()
  expect(await within(dialog).findByText(/identity-7/)).toBeInTheDocument()
  await waitFor(() => expect(qc.getQueryData(queryKeys.whoami)).toBeTruthy())
  return dialog
}

beforeEach(() => {
  whoamiMock
    .mockReset()
    .mockResolvedValue(
      principal([
        grant('t1', BOTH),
        grant('t2', ACCESS_ONLY),
        grant('t3', NONE),
      ]),
    )
  useSessionStore.setState({
    token: 'olvs_test',
    sessionId: 's1',
    expiresAt: null,
  })
  useTenantStore.setState({ activeTenant: 't1' })
  useWorkspaceStore.setState({
    activeWorkspace: null,
    activeWorkspaceName: null,
  })
  vi.mocked(inventoryApi.detail)
    .mockReset()
    .mockImplementation(async (_kind: string, _id: string) => detailOf(AGENT))
  vi.mocked(inventoryApi.observations)
    .mockReset()
    .mockResolvedValue({ items: [], has_more: false })
})

afterEach(() => {
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
  useWorkspaceStore.getState().clear()
})

describe('EntityDetailSheet destination links: registered authority', () => {
  it('REPRODUCES A DEFECT: an agent with both destinations navigable offers both generic registered paths and no query string', async () => {
    vi.mocked(inventoryApi.detail).mockResolvedValue(detailOf(AGENT))
    const { qc } = mount(AGENT)
    const dialog = await settleWhoami(qc)
    const { access, sessions } = linksIn(dialog)
    expect(access).not.toBeNull()
    expect(sessions).not.toBeNull()
    expect(access).toHaveAttribute('href', ACCESS_MAP.path)
    expect(sessions).toHaveAttribute('href', SESSIONS.path)
    expect(access!.getAttribute('href')).not.toMatch(/[?#]/)
    expect(sessions!.getAttribute('href')).not.toMatch(/[?#]/)
  })

  it('REPRODUCES A DEFECT: an agent with Access map denied and Sessions allowed offers only Sessions', async () => {
    whoamiMock.mockResolvedValue(principal([grant('t1', SESSIONS_ONLY)]))
    vi.mocked(inventoryApi.detail).mockResolvedValue(detailOf(AGENT))
    const { qc } = mount(AGENT)
    const dialog = await settleWhoami(qc)
    const { access, sessions } = linksIn(dialog)
    expect(access).toBeNull()
    expect(sessions).not.toBeNull()
    expect(sessions).toHaveAttribute('href', SESSIONS.path)
  })

  it('REPRODUCES A DEFECT: an agent with Sessions denied and Access map allowed offers only Access map', async () => {
    whoamiMock.mockResolvedValue(principal([grant('t1', ACCESS_ONLY)]))
    vi.mocked(inventoryApi.detail).mockResolvedValue(detailOf(AGENT))
    const { qc } = mount(AGENT)
    const dialog = await settleWhoami(qc)
    const { access, sessions } = linksIn(dialog)
    expect(access).not.toBeNull()
    expect(sessions).toBeNull()
    expect(access).toHaveAttribute('href', ACCESS_MAP.path)
  })

  it('REPRODUCES A DEFECT: an agent with both destinations refused offers neither link', async () => {
    whoamiMock.mockResolvedValue(principal([grant('t1', NONE)]))
    vi.mocked(inventoryApi.detail).mockResolvedValue(detailOf(AGENT))
    const { qc } = mount(AGENT)
    const dialog = await settleWhoami(qc)
    expect(linksIn(dialog).access).toBeNull()
    expect(linksIn(dialog).sessions).toBeNull()
  })

  it('POSITIVE CONTROL: a session kind with both destinations navigable offers both', async () => {
    vi.mocked(inventoryApi.detail).mockResolvedValue(detailOf(SESSION))
    const { qc } = mount(SESSION)
    const dialog = await settleWhoami(qc)
    expect(linksIn(dialog).access).not.toBeNull()
    expect(linksIn(dialog).sessions).not.toBeNull()
  })

  it('POSITIVE CONTROL: identity and resource offer Access map only, never Sessions, even when Sessions is navigable', async () => {
    for (const row of [IDENTITY, RESOURCE]) {
      vi.mocked(inventoryApi.detail).mockResolvedValue(detailOf(row))
      const { qc, unmount } = mount(row)
      const dialog = await settleWhoami(qc)
      expect(linksIn(dialog).access).not.toBeNull()
      expect(linksIn(dialog).sessions).toBeNull()
      unmount()
    }
  })

  it('POSITIVE CONTROL: a model is not a legitimate origin for either destination — no links even when both rooms are navigable', async () => {
    vi.mocked(inventoryApi.detail).mockResolvedValue(detailOf(MODEL))
    const { qc } = mount(MODEL)
    const dialog = await settleWhoami(qc)
    expect(linksIn(dialog).access).toBeNull()
    expect(linksIn(dialog).sessions).toBeNull()
  })

  it('REPRODUCES A DEFECT: withdrawing Sessions from the current tenant retires that link and keeps Access map', async () => {
    vi.mocked(inventoryApi.detail).mockResolvedValue(detailOf(AGENT))
    const { qc } = mount(AGENT)
    const dialog = await settleWhoami(qc)
    expect(linksIn(dialog).sessions).not.toBeNull()
    expect(linksIn(dialog).access).not.toBeNull()

    whoamiMock.mockResolvedValue(principal([grant('t1', ACCESS_ONLY)]))
    await act(async () => {
      await qc.refetchQueries({ queryKey: queryKeys.whoami })
    })

    await waitFor(() => expect(linksIn(dialog).sessions).toBeNull())
    expect(linksIn(dialog).access).not.toBeNull()
    expect(linksIn(dialog).access).toHaveAttribute('href', ACCESS_MAP.path)
  })

  it('POSITIVE CONTROL: regranting Sessions restores the Sessions link without inventing an edge', async () => {
    whoamiMock.mockResolvedValue(principal([grant('t1', ACCESS_ONLY)]))
    vi.mocked(inventoryApi.detail).mockResolvedValue(detailOf(AGENT))
    const { qc } = mount(AGENT)
    const dialog = await settleWhoami(qc)
    expect(linksIn(dialog).sessions).toBeNull()
    expect(linksIn(dialog).access).not.toBeNull()

    whoamiMock.mockResolvedValue(principal([grant('t1', BOTH)]))
    await act(async () => {
      await qc.refetchQueries({ queryKey: queryKeys.whoami })
    })

    await waitFor(() => expect(linksIn(dialog).sessions).not.toBeNull())
    expect(linksIn(dialog).sessions).toHaveAttribute('href', SESSIONS.path)
    expect(linksIn(dialog).sessions!.getAttribute('href')).not.toMatch(/[?#]/)
  })

  it('REPRODUCES A DEFECT: switching to a tenant whose grant lacks both destinations retires both links; the catalog fields stay', async () => {
    vi.mocked(inventoryApi.detail).mockResolvedValue(detailOf(AGENT))
    const { qc } = mount(AGENT)
    const dialog = await settleWhoami(qc)
    expect(linksIn(dialog).access).not.toBeNull()

    act(() => useTenantStore.getState().setActiveTenant('t3'))

    await waitFor(() => expect(linksIn(dialog).access).toBeNull())
    expect(linksIn(dialog).sessions).toBeNull()
    expect(within(dialog).getByText('sess-orch')).toBeInTheDocument()
    expect(within(dialog).getByText('prod-orchestrator')).toBeInTheDocument()
  })

  it('POSITIVE CONTROL: switching to a tenant that keeps Access map and not Sessions offers only Access map', async () => {
    vi.mocked(inventoryApi.detail).mockResolvedValue(detailOf(AGENT))
    const { qc } = mount(AGENT)
    const dialog = await settleWhoami(qc)
    expect(linksIn(dialog).sessions).not.toBeNull()

    act(() => useTenantStore.getState().setActiveTenant('t2'))

    await waitFor(() => expect(linksIn(dialog).sessions).toBeNull())
    expect(linksIn(dialog).access).not.toBeNull()
    expect(linksIn(dialog).access).toHaveAttribute('href', ACCESS_MAP.path)
  })
})
