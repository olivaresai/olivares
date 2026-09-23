// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The account page through its ACTUAL mount path: the registered view's element inside
// `RequirePermission`, under the real `AuthProvider`, exactly as the route tree composes
// it. The read tier is withdrawn and restored by a fresh whoami answer for the same
// principal, tenant and credential. No mocked `can()`, tenant switch or logout drives
// it. The guard unmounts the page before any account component can observe the loss.
//
// The client keeps inactive entries forever and never treats one as stale, so a
// leftover account byte would repaint on regrant without a read.
import { afterEach, expect, it, vi } from 'vitest'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  createTestQueryClient,
  renderIntel,
  screen,
  userEvent,
  waitFor,
  within,
} from '@/test/intel'
import { RequirePermission } from '@/components/layout/require-permission'
import { FEATURE_VIEWS } from '@/features/registry'
import { AuthProvider } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type { QueryClient } from '@tanstack/react-query'
import type { ProviderAccountDTO } from './types'

const api = vi.hoisted(() => ({
  whoami: vi.fn(),
  listAccounts: vi.fn(),
  getAccount: vi.fn(),
  adoptAccount: vi.fn(),
}))
const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))
vi.mock('@/lib/api/endpoints', async (original) => ({
  ...(await original<typeof import('@/lib/api/endpoints')>()),
  authApi: { whoami: api.whoami },
}))
vi.mock('./api', async (original) => {
  const real = await original<typeof import('./api')>()
  return {
    ...real,
    agentOpsApi: {
      ...real.agentOpsApi,
      listAccounts: api.listAccounts,
      getAccount: api.getAccount,
      adoptAccount: api.adoptAccount,
    },
  }
})

const TENANT = 't-accounts'
const view = FEATURE_VIEWS.find((v) => v.id === 'providerAccounts')!

const principal = (permissions: string[]): Whoami => ({
  kind: 'user',
  user_id: 'account-member',
  actor: 'account-member',
  display_name: 'Account member',
  superadmin: false,
  grants: [{ tenant: TENANT, role: 'viewer', permissions }],
})
const READ = 'sessions:account:read'
const WRITE = 'sessions:account:write'

const account = (ref: string, name: string): ProviderAccountDTO => ({
  account_ref: ref,
  name,
  driver: 'claude',
  environment_ref: 'xenv_1',
  state: 'active',
  home_mode: 'adopted',
  home_generation: 0,
  home_relative: '',
  isolation_level: 'shared',
  auth_source: 'provider_account_home',
  identity: '',
  identity_source: 'none',
  created_at: '2026-09-01T10:00:00Z',
  updated_at: '2026-09-01T10:00:00Z',
})
const workClaude = account('ppf_a', 'work-claude')
const adopted = account('ppf_b', 'claude-b')
type Page = { items: ProviderAccountDTO[]; has_more: boolean }

function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

/** Cache entries whose DATA carries an account name: judged by content, not by key. */
function accountBytes(qc: QueryClient) {
  return qc
    .getQueryCache()
    .findAll()
    .filter((q) =>
      /work-claude|claude-b/.test(JSON.stringify(q.state.data ?? null)),
    )
}

async function setWhoami(qc: QueryClient, permissions: string[]) {
  api.whoami.mockResolvedValue(principal(permissions))
  await act(async () => {
    await qc.refetchQueries({ queryKey: queryKeys.whoami })
  })
}

function mountAccountsRoute(permissions: string[]) {
  api.whoami.mockResolvedValue(principal(permissions))
  // Synthetic session only; no refresh timer or HTTP login in this fixture.
  useSessionStore.setState({
    token: 'fixture-only-account-session',
    sessionId: 'fixture-only-account-session-id',
    expiresAt: null,
  })
  useTenantStore.getState().setActiveTenant(TENANT)
  const qc = createTestQueryClient()
  qc.setDefaultOptions({
    queries: { retry: false, gcTime: Infinity, staleTime: Infinity },
  })
  const root = createRootRoute({
    component: () => (
      <AuthProvider>
        <Outlet />
      </AuthProvider>
    ),
  })
  // The composition of app/routes.tsx for one feature view, verbatim.
  const route = createRoute({
    getParentRoute: () => root,
    path: view.path,
    component: () => (
      <RequirePermission view={view}>{view.element()}</RequirePermission>
    ),
  })
  const router = createRouter({
    routeTree: root.addChildren([route]),
    history: createMemoryHistory({ initialEntries: [view.path] }),
  })
  const ui = renderIntel(<RouterProvider router={router} />, {
    queryClient: qc,
  })
  return { qc, ui }
}

afterEach(() => {
  cleanup()
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
  vi.clearAllMocks()
})

it('the route guard unmounts the page on read withdrawal: a pending read is aborted, account bytes leave, and the regrant reads before painting', async () => {
  api.listAccounts.mockResolvedValue({ items: [workClaude], has_more: false })
  const detail = deferred<ProviderAccountDTO>()
  api.getAccount.mockReturnValueOnce(detail.promise)
  const { qc, ui } = mountAccountsRoute([READ])
  await userEvent.click(
    await screen.findByRole('button', { name: 'work-claude' }),
  )
  await waitFor(() => expect(api.getAccount).toHaveBeenCalledOnce())
  const pointRead = (
    api.getAccount.mock.calls[0][1] as { signal?: AbortSignal } | undefined
  )?.signal
  expect(accountBytes(qc)).not.toEqual([])

  await setWhoami(qc, [])
  expect(await screen.findByText('Not authorized')).toBeInTheDocument()
  expect(pointRead?.aborted).toBe(true)
  await waitFor(() => expect(accountBytes(qc)).toEqual([]))
  expect(screen.queryByText('work-claude')).toBeNull()

  const fresh = deferred<Page>()
  api.listAccounts.mockReturnValueOnce(fresh.promise)
  const reads = api.listAccounts.mock.calls.length
  await setWhoami(qc, [READ])
  await waitFor(() =>
    expect(api.listAccounts.mock.calls.length).toBeGreaterThan(reads),
  )
  expect(screen.queryByText('work-claude')).toBeNull()
  await act(async () => {
    fresh.resolve({ items: [workClaude], has_more: false })
  })
  expect(
    await screen.findByRole('button', { name: 'work-claude' }),
  ).toBeInTheDocument()
  ui.unmount()
  qc.clear()
})

it('read is withdrawn while an adopt POST is parked: the committed late answer paints and announces nothing; the regrant reads fresh and reconciles', async () => {
  api.listAccounts.mockResolvedValue({ items: [workClaude], has_more: false })
  api.getAccount.mockImplementation(async (ref: string) => {
    if (ref === 'ppf_b') return adopted
    if (ref === 'ppf_a') return workClaude
    throw new ApiError(404, 'not_found', 'provider account not found')
  })
  const answer = deferred<ProviderAccountDTO>()
  api.adoptAccount.mockReturnValue(answer.promise)
  const { qc, ui } = mountAccountsRoute([READ, WRITE])
  await userEvent.click(
    await screen.findByRole('button', { name: 'Adopt a profile' }),
  )
  const dialog = await screen.findByRole('dialog', {
    name: 'Adopt a provider profile',
  })
  await userEvent.type(
    within(dialog).getByRole('textbox', { name: 'Profile reference' }),
    'ppf_b',
  )
  await userEvent.click(within(dialog).getByRole('button', { name: 'Adopt' }))
  await waitFor(() => expect(api.adoptAccount).toHaveBeenCalledOnce())

  // Write stays; read leaves. The guard refuses the whole page.
  await setWhoami(qc, [WRITE])
  expect(await screen.findByText('Not authorized')).toBeInTheDocument()
  await act(async () => {
    answer.resolve(adopted)
  })
  expect(screen.queryByText(/claude-b/)).toBeNull()
  expect(toast.success).not.toHaveBeenCalled()
  await waitFor(() => expect(accountBytes(qc)).toEqual([]))

  const reads = api.listAccounts.mock.calls.length
  await setWhoami(qc, [READ, WRITE])
  await waitFor(() =>
    expect(api.listAccounts.mock.calls.length).toBeGreaterThan(reads),
  )
  expect(
    await screen.findByText('The outcome of adopting ppf_b is not known here.'),
  ).toBeInTheDocument()
  expect(
    await screen.findByText('At this read, ppf_b is the account claude-b.'),
  ).toBeInTheDocument()
  expect(api.adoptAccount).toHaveBeenCalledOnce()
  ui.unmount()
  qc.clear()
})
