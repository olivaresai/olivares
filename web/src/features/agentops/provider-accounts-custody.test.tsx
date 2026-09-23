// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Custody of a submitted adoption's profile reference by the APP SHELL. The real
// `Providers` (query client, `AuthProvider`, toaster, step-up host) render the real account
// page. An adoption is submitted and its POST stays parked, so the page writes the intent it
// keeps for an answer it could not show. Then the page is UNMOUNTED, the way the room tab
// and the route guard remove it, and the authority boundary moves while no provider-room
// component is mounted. Only the shell remains to end what the old boundary left in memory.
//
// Four moves the engine and the console really make: a terminal 401 followed by another
// principal's sign-in, a same-session credential rotation, and a tenant switch with its
// round trip. The fourth case moves no boundary: read leaves and returns for the same
// principal, tenant and credential, and the unresolved adoption must still be reconciled
// then.
//
// The check reads both caches by CONTENT. After the boundary moved, no query anywhere may
// still hold the submitted profile reference, and no mutation in the MutationCache may still
// carry the submitted adoption's variables (its profile reference or its draft name). The
// adoption is parked, so its mutation is still pending: TanStack never collects a pending
// mutation on its own, and only its removal at the move ends it.
import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  whoami: vi.fn(),
  listAccounts: vi.fn(),
  getAccount: vi.fn(),
  adoptAccount: vi.fn(),
}))
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

import { Providers } from '@/app/providers'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { ProviderAccountsPanel } from './provider-accounts-panel'
import type { ProviderAccountDTO } from './types'

const PARKED = 'ppf_parked_custody'
const DRAFT_NAME = 'parked-draft-name'
const T1 = 't-custody-1'
const T2 = 't-custody-2'
const SID = 'sid-custody'
const EXP = '2030-01-01T00:00:00Z'
const TIERS = ['sessions:account:read', 'sessions:account:write']

const principal = (
  id: string,
  permissions: string[] = TIERS,
  tenants: string[] = [T1, T2],
): Whoami => ({
  kind: 'user',
  user_id: id,
  actor: id,
  display_name: id,
  superadmin: false,
  grants: tenants.map((tenant) => ({ tenant, role: 'editor', permissions })),
})

const adopted: ProviderAccountDTO = {
  account_ref: PARKED,
  name: 'claude-parked',
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
}

let client: QueryClient | null = null
/** Hands the shell's own query client to the test; calls no authority hook. */
function ClientProbe() {
  client = useQueryClient()
  return null
}
function Shell({ room }: { room: boolean }) {
  return (
    <Providers>
      <ClientProbe />
      {room ? <ProviderAccountsPanel /> : null}
    </Providers>
  )
}

/** Does any query in the shell's cache still hold the submitted profile reference? */
function cacheHolds(ref: string) {
  const qc = client
  if (!qc) throw new Error('the shell query client was never observed')
  return qc
    .getQueryCache()
    .findAll()
    .some((q) => JSON.stringify(q.state.data ?? null).includes(ref))
}

/** Does any value reachable from `value` through own enumerable fields contain `needle`? */
function reaches(
  value: unknown,
  needle: string,
  seen = new Set<object>(),
): boolean {
  if (typeof value === 'string') return value.includes(needle)
  if (value === null || typeof value !== 'object' || seen.has(value))
    return false
  seen.add(value)
  return Object.values(value).some((v) => reaches(v, needle, seen))
}

/** Does any mutation in the shell's MutationCache still carry `needle` in its variables? */
function mutationCacheHolds(needle: string) {
  const qc = client
  if (!qc) throw new Error('the shell query client was never observed')
  return qc
    .getMutationCache()
    .getAll()
    .some((m) => reaches(m.state.variables, needle))
}

/** Nothing of the parked adoption is left in either cache. */
async function retiredAdoptionGone() {
  await waitFor(() => expect(cacheHolds(PARKED)).toBe(false))
  await waitFor(() => expect(mutationCacheHolds(PARKED)).toBe(false))
  expect(mutationCacheHolds(DRAFT_NAME)).toBe(false)
}

async function settleWhoami(userId: string) {
  await waitFor(() =>
    expect(client?.getQueryData<Whoami>(queryKeys.whoami)?.user_id).toBe(
      userId,
    ),
  )
}

/** Mounts the shell with the page, submits an adoption whose POST never answers, and
 *  unmounts the page: the room is absent from here on. */
async function parkAnAdoptionAndLeaveTheRoom() {
  api.adoptAccount.mockReturnValue(new Promise(() => {}))
  const user = userEvent.setup()
  const ui = render(<Shell room />)
  await settleWhoami('u-a')
  await user.click(
    await screen.findByRole('button', { name: 'Adopt a profile' }),
  )
  const dialog = await screen.findByRole('dialog', {
    name: 'Adopt a provider profile',
  })
  await user.type(
    within(dialog).getByRole('textbox', { name: 'Profile reference' }),
    PARKED,
  )
  await user.type(
    within(dialog).getByRole('textbox', { name: 'Account name (optional)' }),
    DRAFT_NAME,
  )
  await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
  await waitFor(() => expect(api.adoptAccount).toHaveBeenCalledOnce())
  expect(cacheHolds(PARKED)).toBe(true)
  // The probes see the submitted adoption where it lives while its POST is pending.
  expect(mutationCacheHolds(PARKED)).toBe(true)
  expect(mutationCacheHolds(DRAFT_NAME)).toBe(true)
  ui.rerender(<Shell room={false} />)
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
  )
  // Same boundary: the unresolved adoption is kept for its reconciliation.
  expect(cacheHolds(PARKED)).toBe(true)
  expect(mutationCacheHolds(PARKED)).toBe(true)
  return ui
}

beforeEach(() => {
  vi.clearAllMocks()
  client = null
  api.whoami.mockResolvedValue(principal('u-a'))
  api.listAccounts.mockResolvedValue({ items: [], has_more: false })
  api.getAccount.mockImplementation(async (ref: string) => {
    if (ref === PARKED) return adopted
    throw new ApiError(404, 'not_found', 'provider account not found')
  })
  useSessionStore.setState({
    token: 'olvs_custody_first',
    sessionId: SID,
    expiresAt: EXP,
  })
  useTenantStore.getState().setActiveTenant(T1)
})

afterEach(() => {
  useSessionStore.getState().clear()
  useTenantStore.getState().clear()
})

describe('the shell ends a retired boundary’s adoption intent while the room is absent', () => {
  it('a terminal 401 and another principal’s sign-in leave no trace of the previous principal’s submission', async () => {
    const ui = await parkAnAdoptionAndLeaveTheRoom()
    // What the client's onUnauthorized does on a terminal 401.
    act(() => {
      useSessionStore.getState().clear()
    })
    // Another principal signs in: a new session, and whoami read for it (what login() does).
    api.whoami.mockResolvedValue(principal('u-b'))
    await act(async () => {
      useSessionStore.getState().setSession({
        token: 'olvs_custody_other',
        sessionId: 'sid-other',
        expiresAt: EXP,
      })
      await client?.refetchQueries({ queryKey: queryKeys.whoami })
    })
    await settleWhoami('u-b')
    await retiredAdoptionGone()
    // The page of the new principal shows nothing of it either.
    ui.rerender(<Shell room />)
    expect(await screen.findByText('No provider accounts')).toBeInTheDocument()
    expect(screen.queryByText(new RegExp(PARKED))).toBeNull()
    expect(api.getAccount).not.toHaveBeenCalled()
  })

  it('a same-session credential rotation retires the boundary and its intent', async () => {
    await parkAnAdoptionAndLeaveTheRoom()
    act(() => {
      useSessionStore.getState().setSession({
        token: 'olvs_custody_rotated',
        sessionId: SID,
        expiresAt: EXP,
      })
    })
    expect(useSessionStore.getState().sessionId).toBe(SID)
    await retiredAdoptionGone()
  })

  it('a tenant switch retires the intent, and the round trip does not bring it back', async () => {
    const ui = await parkAnAdoptionAndLeaveTheRoom()
    act(() => {
      useTenantStore.getState().setActiveTenant(T2)
    })
    await retiredAdoptionGone()
    act(() => {
      useTenantStore.getState().setActiveTenant(T1)
    })
    expect(cacheHolds(PARKED)).toBe(false)
    expect(mutationCacheHolds(PARKED)).toBe(false)
    ui.rerender(<Shell room />)
    expect(await screen.findByText('No provider accounts')).toBeInTheDocument()
    expect(screen.queryByText(new RegExp(PARKED))).toBeNull()
  })
})

describe('the same boundary keeps what it may still need', () => {
  it('read leaves and returns for the same principal, tenant and credential: the intent stays and the regranted page reconciles it by reading', async () => {
    const ui = await parkAnAdoptionAndLeaveTheRoom()
    api.whoami.mockResolvedValue(principal('u-a', []))
    await act(async () => {
      await client?.refetchQueries({ queryKey: queryKeys.whoami })
    })
    expect(cacheHolds(PARKED)).toBe(true)
    api.whoami.mockResolvedValue(principal('u-a'))
    await act(async () => {
      await client?.refetchQueries({ queryKey: queryKeys.whoami })
    })
    expect(cacheHolds(PARKED)).toBe(true)
    ui.rerender(<Shell room />)
    expect(
      await screen.findByText(
        `The outcome of adopting ${PARKED} is not known here.`,
      ),
    ).toBeInTheDocument()
    expect(
      await screen.findByText(
        `At this read, ${PARKED} is the account claude-parked.`,
      ),
    ).toBeInTheDocument()
    expect(api.adoptAccount).toHaveBeenCalledOnce()
  })
})
