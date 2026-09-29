// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The account page under a moved authority boundary, with the same tenant and the same
// tiers. There are three moves, the engine's three ways of changing who is calling:
// another principal; a new session; and a same-session credential rotation, where
// `POST /v1/auth/refresh` returns the SAME session id with a new bearer. The tenant
// move is pinned in provider-accounts-panel.test.tsx. For each move, nothing read,
// pending or submitted under the previous boundary is painted, announced or applied
// under the next, and no key carries the bearer or the session id.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PageActionsProvider } from '@/components/ui/page-actions'
import { useSessionStore } from '@/stores/session'

const auth = vi.hoisted(() => ({
  perms: new Set<string>(),
  principal: 'u1',
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: false,
    principal: { user_id: auth.principal, aal: 1 },
  }),
}))
const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))
const api = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getAccount: vi.fn(),
  adoptAccount: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})

import { ProviderAdminView } from './provider-admin-view'
import type { ProviderAccountDTO } from './types'

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
const ofA = account('ppf_a', 'account-read-by-a')
const ofB = account('ppf_z', 'account-read-by-b')
const adoptedByA = account('ppf_b', 'adopted-under-a')
type Page = { items: ProviderAccountDTO[]; has_more: boolean }

function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}
const signalOf = (call: unknown[]) =>
  (call[1] as { signal?: AbortSignal } | undefined)?.signal

const EXP = '2030-01-01T00:00:00Z'
const SID = 'sid-fixed'
let rotations = 0
const moves: Array<[string, () => void]> = [
  ['principal', () => (auth.principal = 'u2')],
  [
    'new-session credential',
    () =>
      useSessionStore.getState().setSession({
        token: 'olvs_next',
        sessionId: `sid-${Date.now()}`,
        expiresAt: EXP,
      }),
  ],
  [
    'same-session credential rotation',
    () => {
      const before = useSessionStore.getState().sessionId
      useSessionStore.getState().setSession({
        token: `olvs_rotated_${++rotations}`,
        sessionId: SID,
        expiresAt: EXP,
      })
      expect(useSessionStore.getState().sessionId).toBe(before)
    },
  ],
]

/** Every key of the provider room is partitioned by an OPAQUE number, and no key in the
 *  cache carries the bearer or the session id. */
function expectOpaqueKeys(qc: QueryClient) {
  const { token, sessionId } = useSessionStore.getState()
  const keys = qc
    .getQueryCache()
    .findAll()
    .map((q) => q.queryKey as readonly unknown[])
  const room = keys.filter((k) => k[0] === 'agentops' && k[2] === 'b')
  expect(room.length).toBeGreaterThan(0)
  for (const k of room) expect(typeof k[3]).toBe('number')
  const flat = JSON.stringify(keys)
  expect(flat).not.toContain('olvs_')
  if (token) expect(flat).not.toContain(token)
  if (sessionId) expect(flat).not.toContain(sessionId)
}

/** Cache entries whose DATA carries something of boundary A. */
function bytesOfA(qc: QueryClient) {
  return qc
    .getQueryCache()
    .findAll()
    .filter((q) =>
      /account-read-by-a|adopted-under-a|ppf_b/.test(
        JSON.stringify(q.state.data ?? null),
      ),
    )
}

function wrap() {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: Infinity, staleTime: Infinity },
    },
  })
  const tree = () => (
    <QueryClientProvider client={qc}>
      <PageActionsProvider>
        <ProviderAdminView entrance="accounts" />
      </PageActionsProvider>
    </QueryClientProvider>
  )
  const ui = render(tree())
  return { qc, rerender: () => ui.rerender(tree()) }
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.principal = 'u1'
  auth.perms = new Set(['sessions:account:read', 'sessions:account:write'])
  useSessionStore.setState({
    token: 'olvs_first',
    sessionId: SID,
    expiresAt: EXP,
  })
  api.getAccount.mockImplementation(async (ref: string) =>
    ref === 'ppf_z' ? ofB : ofA,
  )
})

describe('account list and point read under a moved boundary', () => {
  it.each(moves)(
    'a %s change cancels A’s pending list, never paints it, and paints B from B’s own read',
    async (_what, move) => {
      const a = deferred<Page>()
      const b = deferred<Page>()
      api.listAccounts
        .mockReturnValueOnce(a.promise)
        .mockReturnValueOnce(b.promise)
      const { qc, rerender } = wrap()
      await waitFor(() => expect(api.listAccounts).toHaveBeenCalledOnce())
      act(() => {
        move()
      })
      rerender()
      await waitFor(() => expect(api.listAccounts).toHaveBeenCalledTimes(2))
      expect(signalOf(api.listAccounts.mock.calls[0])?.aborted).toBe(true)
      await act(async () => {
        a.resolve({ items: [ofA], has_more: false })
      })
      expect(screen.queryByText('account-read-by-a')).toBeNull()
      expect(bytesOfA(qc)).toEqual([])
      b.resolve({ items: [ofB], has_more: false })
      expect(
        await screen.findByRole('button', { name: 'account-read-by-b' }),
      ).toBeInTheDocument()
      expectOpaqueKeys(qc)
    },
  )

  it.each(moves)(
    'a %s change aborts A’s pending point read and closes the detail; nothing of it is cached or painted',
    async (_what, move) => {
      api.listAccounts
        .mockResolvedValueOnce({ items: [ofA], has_more: false })
        .mockResolvedValue({ items: [ofB], has_more: false })
      const detail = deferred<ProviderAccountDTO>()
      api.getAccount.mockReturnValueOnce(detail.promise)
      const user = userEvent.setup()
      const { qc, rerender } = wrap()
      await user.click(
        await screen.findByRole('button', { name: 'account-read-by-a' }),
      )
      await waitFor(() => expect(api.getAccount).toHaveBeenCalledOnce())
      const pointRead = signalOf(api.getAccount.mock.calls[0])
      act(() => {
        move()
      })
      rerender()
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
      expect(pointRead?.aborted).toBe(true)
      await act(async () => {
        detail.resolve(ofA)
      })
      expect(
        await screen.findByRole('button', { name: 'account-read-by-b' }),
      ).toBeInTheDocument()
      expect(screen.queryByText('account-read-by-a')).toBeNull()
      expect(bytesOfA(qc)).toEqual([])
      expectOpaqueKeys(qc)
    },
  )
})

describe('an adoption submitted under a moved boundary', () => {
  it.each(moves)(
    'a %s change drops A’s parked adoption: its late answer is not announced, applied or reconciled under B',
    async (_what, move) => {
      api.listAccounts
        .mockResolvedValueOnce({ items: [ofA], has_more: false })
        .mockResolvedValue({ items: [ofB], has_more: false })
      const answer = deferred<ProviderAccountDTO>()
      api.adoptAccount.mockReturnValue(answer.promise)
      const user = userEvent.setup()
      const { qc, rerender } = wrap()
      await user.click(
        await screen.findByRole('button', { name: 'Adopt a profile' }),
      )
      const dialog = await screen.findByRole('dialog', {
        name: 'Adopt a provider profile',
      })
      await user.type(
        within(dialog).getByRole('textbox', { name: 'Profile reference' }),
        'ppf_b',
      )
      await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
      await waitFor(() => expect(api.adoptAccount).toHaveBeenCalledOnce())

      act(() => {
        move()
      })
      rerender()
      expect(
        await screen.findByRole('button', { name: 'account-read-by-b' }),
      ).toBeInTheDocument()
      await act(async () => {
        answer.resolve(adoptedByA)
      })
      expect(toast.success).not.toHaveBeenCalled()
      expect(screen.queryByText(/ppf_b/)).toBeNull()
      expect(screen.queryByText(/adopted-under-a/)).toBeNull()
      expect(screen.queryByRole('dialog')).toBeNull()
      // No reconciliation read is made for A's submission under B.
      expect(api.getAccount).not.toHaveBeenCalledWith(
        'ppf_b',
        expect.anything(),
      )
      expect(bytesOfA(qc)).toEqual([])
      expectOpaqueKeys(qc)
    },
  )
})
