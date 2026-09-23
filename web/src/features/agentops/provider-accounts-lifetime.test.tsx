// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The account plane's lifetime inside the provider room, which is where the page is
// mounted: its Accounts tab exists only while the account read tier does. The boundary
// (principal, tenant, credential) never moves in this file; only a tier leaves and comes
// back, which is the case the room's gating unmounts before anything inside it could
// observe the loss.
//
// The client below keeps every inactive entry forever and never considers one stale. A
// leftover byte would therefore stay in the cache and repaint on regrant without a read.
// Removal and a fresh read must come from the account plane itself, not from a client
// default.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PageActionsProvider } from '@/components/ui/page-actions'

const auth = vi.hoisted(() => ({ perms: new Set<string>() }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: false,
    principal: { user_id: 'u1', aal: 1 },
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
  listProfiles: vi.fn(),
  listBindings: vi.fn(),
  listRuns: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})
vi.mock('@/features/console/api', () => ({
  consoleApi: { listSources: vi.fn() },
  consoleKeys: { sources: () => ['console', 'sources'] },
}))

import { ApiError, NetworkError } from '@/lib/api/errors'
import { ProviderAdminView } from './provider-admin-view'
import type { ProviderAccountDTO } from './types'

const PR = 'sessions:profile:read'
const AR = 'sessions:account:read'
const AW = 'sessions:account:write'

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
const page = (items: ProviderAccountDTO[]): Page => ({ items, has_more: false })

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}
const signalOf = (call: unknown[]) =>
  (call[1] as { signal?: AbortSignal } | undefined)?.signal

/** Cache entries whose DATA carries an account name: judged by content, not by key. */
function accountBytes(qc: QueryClient) {
  return qc
    .getQueryCache()
    .findAll()
    .filter((q) =>
      /work-claude|claude-b/.test(JSON.stringify(q.state.data ?? null)),
    )
}

function wrap(entrance: 'profiles' | 'accounts') {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: Infinity, staleTime: Infinity },
    },
  })
  const tree = () => (
    <QueryClientProvider client={qc}>
      <PageActionsProvider>
        <ProviderAdminView entrance={entrance} />
      </PageActionsProvider>
    </QueryClientProvider>
  )
  const ui = render(tree())
  return { qc, rerender: () => ui.rerender(tree()) }
}

async function submitAdoption(
  user: ReturnType<typeof userEvent.setup>,
  ref: string,
) {
  await user.click(
    await screen.findByRole('button', { name: 'Adopt a profile' }),
  )
  const dialog = await screen.findByRole('dialog', {
    name: 'Adopt a provider profile',
  })
  await user.type(
    within(dialog).getByRole('textbox', { name: 'Profile reference' }),
    ref,
  )
  await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
  await waitFor(() => expect(api.adoptAccount).toHaveBeenCalledOnce())
}

beforeEach(() => {
  vi.clearAllMocks()
  api.listAccounts.mockResolvedValue(page([workClaude]))
  api.getAccount.mockImplementation(async (ref: string) => {
    const hit = [workClaude, adopted].find((a) => a.account_ref === ref)
    if (!hit) throw new ApiError(404, 'not_found', 'provider account not found')
    return hit
  })
  api.listProfiles.mockResolvedValue({ items: [], has_more: false })
  api.listBindings.mockResolvedValue({ items: [], has_more: false })
  api.listRuns.mockResolvedValue({ items: [], has_more: false })
})

describe('account read leaves the room, same boundary', () => {
  it('a pending point read is aborted, loaded account bytes leave the cache, and a regrant reads before it paints', async () => {
    auth.perms = new Set([PR, AR])
    const detail = deferred<ProviderAccountDTO>()
    api.getAccount.mockReturnValueOnce(detail.promise)
    const user = userEvent.setup()
    const { qc, rerender } = wrap('profiles')
    await user.click(await screen.findByRole('tab', { name: 'Accounts' }))
    await user.click(await screen.findByRole('button', { name: 'work-claude' }))
    await waitFor(() => expect(api.getAccount).toHaveBeenCalledOnce())
    const pointRead = signalOf(api.getAccount.mock.calls[0])
    expect(accountBytes(qc)).not.toEqual([])

    auth.perms = new Set([PR])
    rerender()
    await waitFor(() =>
      expect(screen.queryByRole('tab', { name: 'Accounts' })).toBeNull(),
    )
    expect(pointRead?.aborted).toBe(true)
    await waitFor(() => expect(accountBytes(qc)).toEqual([]))
    expect(screen.queryByText('work-claude')).toBeNull()
    // The aborted read answers anyway: it lands nowhere.
    await act(async () => {
      detail.resolve(workClaude)
    })
    expect(accountBytes(qc)).toEqual([])

    // The tier returns. Nothing old is painted while the fresh read is pending.
    const fresh = deferred<Page>()
    api.listAccounts.mockReturnValueOnce(fresh.promise)
    const before = api.listAccounts.mock.calls.length
    auth.perms = new Set([PR, AR])
    rerender()
    await waitFor(() =>
      expect(api.listAccounts).toHaveBeenCalledTimes(before + 1),
    )
    expect(screen.queryByText('work-claude')).toBeNull()
    await act(async () => {
      fresh.resolve(page([workClaude]))
    })
    expect(
      await screen.findByRole('button', { name: 'work-claude' }),
    ).toBeInTheDocument()
  })

  it('an answered list and an answered detail leave the cache with the tier', async () => {
    auth.perms = new Set([PR, AR])
    const user = userEvent.setup()
    const { qc, rerender } = wrap('profiles')
    await user.click(await screen.findByRole('tab', { name: 'Accounts' }))
    await user.click(await screen.findByRole('button', { name: 'work-claude' }))
    const sheet = await screen.findByRole('dialog')
    expect(
      await within(sheet).findByText('Adopted (an existing home)'),
    ).toBeInTheDocument()

    auth.perms = new Set([PR])
    rerender()
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(accountBytes(qc)).toEqual([]))
  })
})

describe('a submitted adoption outlives the loss of a tier, never its boundary', () => {
  it('write leaves while the POST is parked: the panel says so, and a committed late answer is reported and read back', async () => {
    auth.perms = new Set([AR, AW])
    const answer = deferred<ProviderAccountDTO>()
    api.adoptAccount.mockReturnValue(answer.promise)
    const user = userEvent.setup()
    const { rerender } = wrap('accounts')
    await submitAdoption(user, 'ppf_b')

    auth.perms = new Set([AR])
    rerender()
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Adopt a provider profile' }),
      ).toBeNull(),
    )
    expect(
      await screen.findByText(
        'Adopting ppf_b. The server has not answered yet.',
      ),
    ).toHaveAttribute('role', 'status')

    const reads = api.listAccounts.mock.calls.length
    await act(async () => {
      answer.resolve(adopted)
    })
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'Adopted as claude-b',
        undefined,
      ),
    )
    expect(
      await screen.findByText('ppf_b was adopted as claude-b.'),
    ).toBeInTheDocument()
    expect(api.listAccounts.mock.calls.length).toBeGreaterThan(reads)
    expect(api.adoptAccount).toHaveBeenCalledOnce()
    // Following up is a read: the account opens from a fresh point read.
    await user.click(screen.getByRole('button', { name: 'Open account' }))
    await waitFor(() =>
      expect(api.getAccount).toHaveBeenCalledWith('ppf_b', expect.anything()),
    )
    expect(
      await screen.findByRole('heading', { name: /claude-b/ }),
    ).toBeInTheDocument()
  })

  it('write leaves while the POST is parked and the answer is lost: reconciliation reads, never re-sends', async () => {
    auth.perms = new Set([AR, AW])
    const answer = deferred<ProviderAccountDTO>()
    api.adoptAccount.mockReturnValue(answer.promise)
    api.getAccount
      .mockRejectedValueOnce(
        new ApiError(404, 'not_found', 'provider account not found'),
      )
      .mockResolvedValueOnce(adopted)
    const user = userEvent.setup()
    const { rerender } = wrap('accounts')
    await submitAdoption(user, 'ppf_b')
    auth.perms = new Set([AR])
    rerender()
    await act(async () => {
      answer.reject(new NetworkError('down'))
    })
    expect(
      await screen.findByText(
        'The outcome of adopting ppf_b is not known here.',
      ),
    ).toBeInTheDocument()
    expect(
      await screen.findByText('At this read, ppf_b is not an account.'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Check again' }))
    expect(
      await screen.findByText('At this read, ppf_b is the account claude-b.'),
    ).toBeInTheDocument()
    expect(api.adoptAccount).toHaveBeenCalledOnce()
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('write leaves while the POST is parked and the engine refuses: the refusal is reported and nothing changed', async () => {
    auth.perms = new Set([AR, AW])
    const reason =
      'account name "claude-1" is already taken in this environment; choose another name'
    const answer = deferred<ProviderAccountDTO>()
    api.adoptAccount.mockReturnValue(answer.promise)
    const user = userEvent.setup()
    const { rerender } = wrap('accounts')
    await submitAdoption(user, 'ppf_b')
    auth.perms = new Set([AR])
    rerender()
    await act(async () => {
      answer.reject(new ApiError(409, 'conflict', reason))
    })
    expect(
      await screen.findByText(`ppf_b was not adopted: ${reason}`),
    ).toBeInTheDocument()
    expect(toast.success).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText(/ppf_b was not adopted/)).toBeNull()
  })

  it('read leaves while the POST is parked: the committed late answer paints nothing; the regrant reads fresh and reconciles', async () => {
    auth.perms = new Set([PR, AR, AW])
    const answer = deferred<ProviderAccountDTO>()
    api.adoptAccount.mockReturnValue(answer.promise)
    const user = userEvent.setup()
    const { qc, rerender } = wrap('profiles')
    await user.click(await screen.findByRole('tab', { name: 'Accounts' }))
    await submitAdoption(user, 'ppf_b')

    auth.perms = new Set([PR, AW])
    rerender()
    await waitFor(() =>
      expect(screen.queryByRole('tab', { name: 'Accounts' })).toBeNull(),
    )
    await act(async () => {
      answer.resolve(adopted)
    })
    // No account detail without the read tier: no toast, no name, no bytes.
    expect(toast.success).not.toHaveBeenCalled()
    expect(screen.queryByText(/claude-b/)).toBeNull()
    await waitFor(() => expect(accountBytes(qc)).toEqual([]))

    const reads = api.listAccounts.mock.calls.length
    auth.perms = new Set([PR, AR, AW])
    rerender()
    await waitFor(() =>
      expect(api.listAccounts.mock.calls.length).toBeGreaterThan(reads),
    )
    expect(
      await screen.findByText(
        'The outcome of adopting ppf_b is not known here.',
      ),
    ).toBeInTheDocument()
    expect(
      await screen.findByText('At this read, ppf_b is the account claude-b.'),
    ).toBeInTheDocument()
    expect(api.adoptAccount).toHaveBeenCalledOnce()
  })
})
