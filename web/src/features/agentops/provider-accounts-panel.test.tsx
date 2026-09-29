// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The provider-account journey: list the tenant's named accounts, open one, and adopt
// an existing provider profile under an explicit name or a server-generated one. What
// these cases pin is the contract with the engine, not the paint: which control issues
// which request with which body; that a tier the principal lacks issues no request (the
// control is not there); that success is announced only after the engine answered; that
// a refusal changes nothing; that a list with more pages says so; and that nothing read
// or half-done under one tenant or credential is painted under the next.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  perms: new Set<string>(),
  principal: 'u1',
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: auth.activeTenant,
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
  createAccount: vi.fn(),
  patchAccountMetadata: vi.fn(),
  listProfiles: vi.fn(),
  listBindings: vi.fn(),
  listRuns: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})
vi.mock('@/features/identity/assurance', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  StepUpPanel: ({ action }: { action: string }) => (
    <span>{`step-up ceremony:${action}`}</span>
  ),
}))

import { ApiError, NetworkError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { findRawI18nKeys } from '@/test/i18n-keys'
import { ProviderAccountsPanel } from './provider-accounts-panel'
import type { ProviderAccountDTO, ProviderProfileDTO } from './types'

const AR = 'sessions:account:read'
const AW = 'sessions:account:write'
const PR = 'sessions:profile:read'

const account = (
  ref: string,
  name: string,
  over: Partial<ProviderAccountDTO> = {},
): ProviderAccountDTO => ({
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
  ...over,
})
const workClaude = account('ppf_a', 'work-claude')
const reviewCodex = account('ppf_c', 'review-codex', {
  driver: 'codex',
  // A relative home an older or newer engine might send: never painted.
  home_relative: 'tenant-1/xenv_1/ppf_c',
})
const adopted = account('ppf_b', 'claude-b')
const otherTenant = account('ppf_t2', 'other-tenant-account')

const profile = (
  ref: string,
  display_name: string,
  state = 'active',
): ProviderProfileDTO => ({
  profile_ref: ref,
  driver: 'claude',
  environment_ref: 'xenv_1',
  display_name,
  state,
  local_environment: true,
  operable: true,
})

const page = <T,>(items: T[], more?: string) =>
  more === undefined
    ? { items, has_more: false }
    : { items, has_more: true, cursor: more }

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function grant(...perms: string[]) {
  auth.perms = new Set(perms)
}

/** A FRESH element per render, so a boundary change reaches the tree like the real
 *  AuthContext delivers it. */
function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const tree = () => (
    <QueryClientProvider client={qc}>
      <ProviderAccountsPanel />
    </QueryClientProvider>
  )
  const ui = render(tree())
  return { qc, rerender: () => ui.rerender(tree()), container: ui.container }
}

async function openAdopt(user: ReturnType<typeof userEvent.setup>) {
  await user.click(
    await screen.findByRole('button', { name: 'Adopt a profile' }),
  )
  return screen.findByRole('dialog', { name: 'Adopt a provider profile' })
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.activeTenant = 't1'
  auth.principal = 'u1'
  grant(AR, AW)
  useSessionStore.setState({
    token: 'olvs_first',
    sessionId: 'sid-1',
    expiresAt: '2030-01-01T00:00:00Z',
  })
  api.listAccounts.mockResolvedValue(page([workClaude, reviewCodex]))
  api.getAccount.mockImplementation(async (ref: string) => {
    const hit = [workClaude, reviewCodex, adopted].find(
      (a) => a.account_ref === ref,
    )
    if (!hit) throw new ApiError(404, 'not_found', 'provider account not found')
    return hit
  })
  api.listProfiles.mockResolvedValue(page([]))
})

describe('ProviderAccountsPanel — the list', () => {
  it('lists the accounts the engine returned, states their isolation, and paints no home', async () => {
    wrap()
    expect(
      await screen.findByRole('button', { name: 'work-claude' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'review-codex' }),
    ).toBeInTheDocument()
    expect(screen.getAllByText('Shared')).toHaveLength(2)
    expect(screen.queryByText(/tenant-1\/xenv_1/)).not.toBeInTheDocument()
    // One bounded first page, under a signal the boundary can abort.
    expect(api.listAccounts).toHaveBeenCalledOnce()
    const [params, opts] = api.listAccounts.mock.calls[0]
    expect((params as { cursor?: string } | undefined)?.cursor).toBeUndefined()
    expect((opts as { signal?: AbortSignal }).signal).toBeInstanceOf(
      AbortSignal,
    )
    // Reading accounts reads nothing else.
    expect(api.listProfiles).not.toHaveBeenCalled()
    expect(api.listRuns).not.toHaveBeenCalled()
  })

  it('an empty answer is a complete empty list and says who may adopt', async () => {
    api.listAccounts.mockResolvedValue(page([]))
    grant(AR)
    wrap()
    expect(await screen.findByText('No provider accounts')).toBeInTheDocument()
    expect(screen.getByText(/needs sessions:account:write/)).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Adopt a profile' }),
    ).not.toBeInTheDocument()
  })

  it('an empty answer offers the adopt verb to a writer', async () => {
    api.listAccounts.mockResolvedValue(page([]))
    wrap()
    expect(await screen.findByText('No provider accounts')).toBeInTheDocument()
    expect(
      screen.getByText(/to name an existing provider profile as an account/),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Adopt a profile' }),
    ).toBeInTheDocument()
  })

  it('a paginated answer says more exist and loads the next page only on request', async () => {
    api.listAccounts
      .mockResolvedValueOnce(page([workClaude], 'c-2'))
      .mockResolvedValueOnce(page([reviewCodex]))
    const user = userEvent.setup()
    wrap()
    await screen.findByRole('button', { name: 'work-claude' })
    // Never a silent first page: the screen says the list goes on.
    expect(screen.getByText(/Shown so far: 1\./)).toBeInTheDocument()
    expect(api.listAccounts).toHaveBeenCalledOnce()
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    expect(
      await screen.findByRole('button', { name: 'review-codex' }),
    ).toBeInTheDocument()
    expect(api.listAccounts).toHaveBeenCalledTimes(2)
    expect(api.listAccounts.mock.calls[1][0]).toEqual({ cursor: 'c-2' })
    // The last page answered has_more=false: the notice and the control leave.
    expect(screen.queryByText(/Shown so far/)).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Load more' }),
    ).not.toBeInTheDocument()
  })

  it('a failed next page keeps the loaded rows, says the list is incomplete, and retries on request', async () => {
    api.listAccounts
      .mockResolvedValueOnce(page([workClaude], 'c-2'))
      .mockRejectedValueOnce(new ApiError(500, 'internal', 'boom'))
      .mockResolvedValueOnce(page([reviewCodex]))
    const user = userEvent.setup()
    wrap()
    await screen.findByRole('button', { name: 'work-claude' })
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    expect(
      await screen.findByText(/The next page of accounts could not be read\./),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'work-claude' }),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    expect(
      await screen.findByRole('button', { name: 'review-codex' }),
    ).toBeInTheDocument()
    expect(api.listAccounts.mock.calls[2][0]).toEqual({ cursor: 'c-2' })
    expect(screen.queryByText(/could not be read/)).toBeNull()
  })

  it('a refused list is calm and shows no account', async () => {
    api.listAccounts.mockRejectedValue(
      new ApiError(403, 'forbidden', 'workspace confined'),
    )
    wrap()
    // The calm refusal (a status, not an alert): never an error, never an empty list.
    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    expect(screen.queryByText('No provider accounts')).not.toBeInTheDocument()
  })

  it('a failed list is an error with a retry, never an empty list', async () => {
    api.listAccounts
      .mockRejectedValueOnce(new ApiError(500, 'internal', 'boom', 'req-9'))
      .mockResolvedValueOnce(page([workClaude]))
    const user = userEvent.setup()
    wrap()
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('req-9')).toBeInTheDocument()
    expect(screen.queryByText('No provider accounts')).not.toBeInTheDocument()
    await user.click(within(alert).getByRole('button', { name: 'Retry' }))
    expect(
      await screen.findByRole('button', { name: 'work-claude' }),
    ).toBeInTheDocument()
  })
})

describe('ProviderAccountsPanel — one account', () => {
  it('opens from the keyboard and shows the fresh point read, with nothing guessed', async () => {
    const user = userEvent.setup()
    wrap()
    const open = await screen.findByRole('button', { name: 'work-claude' })
    open.focus()
    await user.keyboard('{Enter}')
    const sheet = await screen.findByRole('dialog')
    await waitFor(() =>
      expect(api.getAccount).toHaveBeenCalledWith(
        'ppf_a',
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      ),
    )
    expect(
      within(sheet).getByRole('heading', { name: /work-claude/ }),
    ).toBeInTheDocument()
    expect(
      await within(sheet).findByText(
        'Not checked: nothing has asked the provider who is signed in.',
      ),
    ).toBeInTheDocument()
    expect(
      within(sheet).getByText(/It is not a dedicated, isolated home\./),
    ).toBeInTheDocument()
    expect(
      within(sheet).getByText('Adopted (an existing home)'),
    ).toBeInTheDocument()
    expect(
      within(sheet).getByText('ppf_a', { selector: 'dd, dd *' }),
    ).toBeInTheDocument()
  })

  it('a relative home in the answer is never painted in the detail either', async () => {
    const user = userEvent.setup()
    wrap()
    await user.click(
      await screen.findByRole('button', { name: 'review-codex' }),
    )
    const sheet = await screen.findByRole('dialog')
    await within(sheet).findByText('Adopted (an existing home)')
    expect(within(sheet).queryByText(/tenant-1\/xenv_1/)).toBeNull()
  })

  it('an account that is gone says so and shows no stale field', async () => {
    api.getAccount.mockRejectedValue(
      new ApiError(404, 'not_found', 'provider account not found'),
    )
    const user = userEvent.setup()
    wrap()
    await user.click(await screen.findByRole('button', { name: 'work-claude' }))
    const sheet = await screen.findByRole('dialog')
    expect(
      await within(sheet).findByText(/This account was not found\./),
    ).toBeInTheDocument()
    expect(within(sheet).queryByText('Adopted (an existing home)')).toBeNull()
    expect(within(sheet).queryByText(/Not checked/)).toBeNull()
  })
})

describe('ProviderAccountsPanel — adopt', () => {
  it('an account-only writer adopts by reference under a generated name, and success waits for the answer', async () => {
    const answer = deferred<ProviderAccountDTO>()
    api.adoptAccount.mockReturnValue(answer.promise)
    const user = userEvent.setup()
    wrap()
    const dialog = await openAdopt(user)
    // No profile read: no picker and no profile request — the reference path is enough.
    expect(within(dialog).queryByRole('combobox')).toBeNull()
    expect(api.listProfiles).not.toHaveBeenCalled()
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Profile reference' }),
      'ppf_b',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
    await waitFor(() => expect(api.adoptAccount).toHaveBeenCalledOnce())
    const [ref, body, scope] = api.adoptAccount.mock.calls[0]
    expect(ref).toBe('ppf_b')
    expect(body).toEqual({})
    expect(scope).toMatchObject({
      tenant: 't1',
      dispatchGuard: expect.any(Function),
    })
    // In flight: pending, announced as a status, and no success before the answer.
    expect(
      within(dialog).getByRole('button', { name: /Adopting/ }),
    ).toBeDisabled()
    expect(within(dialog).getByText(/^Adopting ppf_b\./)).toHaveAttribute(
      'role',
      'status',
    )
    expect(toast.success).not.toHaveBeenCalled()
    await act(async () => {
      answer.resolve(adopted)
    })
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'Adopted as claude-b',
        undefined,
      ),
    )
    // The list is read again and the new account opens from the engine's answer.
    expect(api.listAccounts.mock.calls.length).toBeGreaterThanOrEqual(2)
    await waitFor(() =>
      expect(api.getAccount).toHaveBeenCalledWith('ppf_b', expect.anything()),
    )
    expect(
      await screen.findByRole('heading', { name: /claude-b/ }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('dialog', { name: 'Adopt a provider profile' }),
    ).not.toBeInTheDocument()
  })

  it('an explicit name travels exactly as typed', async () => {
    api.adoptAccount.mockResolvedValue(account('ppf_b', 'claude-1'))
    const user = userEvent.setup()
    wrap()
    const dialog = await openAdopt(user)
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Profile reference' }),
      'ppf_b',
    )
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Account name (optional)' }),
      'claude-1',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
    await waitFor(() => expect(api.adoptAccount).toHaveBeenCalledOnce())
    expect(api.adoptAccount.mock.calls[0][1]).toEqual({ name: 'claude-1' })
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'Adopted as claude-1',
        undefined,
      ),
    )
  })

  it('a name conflict keeps the draft, shows the engine reason, and adds nothing', async () => {
    const reason =
      'account name "claude-1" is already taken in this environment; choose another name'
    api.adoptAccount.mockRejectedValue(new ApiError(409, 'conflict', reason))
    const user = userEvent.setup()
    wrap()
    const dialog = await openAdopt(user)
    const refBox = within(dialog).getByRole('textbox', {
      name: 'Profile reference',
    })
    const nameBox = within(dialog).getByRole('textbox', {
      name: 'Account name (optional)',
    })
    await user.type(refBox, 'ppf_b')
    await user.type(nameBox, 'claude-1')
    await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
    const alert = await within(dialog).findByRole('alert')
    expect(alert).toHaveTextContent('The profile was not adopted')
    expect(alert).toHaveTextContent(reason)
    expect(refBox).toHaveValue('ppf_b')
    expect(nameBox).toHaveValue('claude-1')
    expect(toast.success).not.toHaveBeenCalled()
    expect(api.listAccounts).toHaveBeenCalledOnce()
    expect(api.getAccount).not.toHaveBeenCalled()
  })

  it('a denied adoption changes nothing and says so', async () => {
    api.adoptAccount.mockRejectedValue(
      new ApiError(403, 'forbidden', 'forbidden'),
    )
    const user = userEvent.setup()
    wrap()
    const dialog = await openAdopt(user)
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Profile reference' }),
      'ppf_b',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
    expect(
      await within(dialog).findByText(
        'Your role cannot adopt accounts in this tenant. Nothing was changed.',
      ),
    ).toBeInTheDocument()
    expect(toast.warning).toHaveBeenCalled()
    expect(toast.success).not.toHaveBeenCalled()
    expect(api.listAccounts).toHaveBeenCalledOnce()
    expect(api.getAccount).not.toHaveBeenCalled()
  })

  it('an unreachable engine leaves the outcome unknown: the draft closes, the accounts are read again, and a read-only check reconciles', async () => {
    api.adoptAccount.mockRejectedValue(new NetworkError('down'))
    api.getAccount
      .mockRejectedValueOnce(
        new ApiError(404, 'not_found', 'provider account not found'),
      )
      .mockResolvedValueOnce(adopted)
    const user = userEvent.setup()
    wrap()
    const dialog = await openAdopt(user)
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Profile reference' }),
      'ppf_b',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
    // The draft may already have been applied: it closes, and the panel says so.
    expect(
      await screen.findByText(
        'The outcome of adopting ppf_b is not known here.',
      ),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('dialog', { name: 'Adopt a provider profile' }),
    ).not.toBeInTheDocument()
    // The account list is read again, and the profile's account read answers now.
    await waitFor(() => expect(api.listAccounts).toHaveBeenCalledTimes(2))
    expect(
      await screen.findByText('At this read, ppf_b is not an account.'),
    ).toBeInTheDocument()
    expect(api.getAccount).toHaveBeenCalledWith('ppf_b', expect.anything())
    // Checking again is a read; the adopt is never sent twice.
    await user.click(screen.getByRole('button', { name: 'Check again' }))
    expect(
      await screen.findByText('At this read, ppf_b is the account claude-b.'),
    ).toBeInTheDocument()
    expect(api.adoptAccount).toHaveBeenCalledOnce()
    expect(toast.success).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Open account' }))
    expect(
      await screen.findByRole('heading', { name: /claude-b/ }),
    ).toBeInTheDocument()
  })

  it('a server failure is an unknown outcome as well, never a success or a refusal', async () => {
    api.adoptAccount.mockRejectedValue(
      new ApiError(502, 'bad_gateway', 'upstream unavailable', 'req-5'),
    )
    const user = userEvent.setup()
    wrap()
    const dialog = await openAdopt(user)
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Profile reference' }),
      'ppf_b',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
    expect(
      await screen.findByText(
        'The outcome of adopting ppf_b is not known here.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText('The profile was not adopted')).toBeNull()
    await waitFor(() =>
      expect(api.getAccount).toHaveBeenCalledWith('ppf_b', expect.anything()),
    )
    expect(toast.success).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(
      screen.queryByText('The outcome of adopting ppf_b is not known here.'),
    ).toBeNull()
    expect(api.adoptAccount).toHaveBeenCalledOnce()
  })

  it('a reader without the write tier has no adopt control and sends nothing', async () => {
    grant(AR, PR)
    wrap()
    await screen.findByRole('button', { name: 'work-claude' })
    expect(
      screen.queryByRole('button', { name: 'Adopt a profile' }),
    ).not.toBeInTheDocument()
    expect(api.adoptAccount).not.toHaveBeenCalled()
    expect(api.listProfiles).not.toHaveBeenCalled()
  })

  it('with profile read the picker lists loaded profiles, omits retired ones, marks known accounts, and says the list is partial', async () => {
    grant(AR, AW, PR)
    api.listAccounts.mockResolvedValue(page([workClaude]))
    api.listProfiles.mockResolvedValue(
      page(
        [
          profile('ppf_a', 'Home A'),
          profile('ppf_b', 'Home B'),
          profile('ppf_gone', 'Old home', 'retired'),
        ],
        'p-2',
      ),
    )
    api.adoptAccount.mockResolvedValue(adopted)
    const user = userEvent.setup()
    wrap()
    await screen.findByRole('button', { name: 'work-claude' })
    const dialog = await openAdopt(user)
    // The profile page has answered (has_more=true): the picker says it is partial.
    expect(
      await within(dialog).findByText(/Only the loaded profiles are listed\./),
    ).toBeInTheDocument()
    expect(api.listProfiles).toHaveBeenCalledOnce()
    expect(
      within(dialog).getByRole('button', { name: 'Load more profiles' }),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('combobox', { name: 'Profile' }))
    expect(
      await screen.findByRole('option', { name: /Home A.*already an account/ }),
    ).toHaveAttribute('aria-disabled', 'true')
    expect(screen.queryByRole('option', { name: /Old home/ })).toBeNull()
    expect(
      screen.getByRole('option', { name: 'Enter a reference…' }),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('option', { name: /Home B/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
    await waitFor(() => expect(api.adoptAccount).toHaveBeenCalledOnce())
    expect(api.adoptAccount.mock.calls[0][0]).toBe('ppf_b')
    expect(api.adoptAccount.mock.calls[0][1]).toEqual({})
  })

  it('while the profile page loads the picker says so and the reference path stays available', async () => {
    grant(AR, AW, PR)
    const profiles = deferred<{
      items: ProviderProfileDTO[]
      has_more: boolean
    }>()
    api.listProfiles.mockReturnValue(profiles.promise)
    api.adoptAccount.mockResolvedValue(adopted)
    const user = userEvent.setup()
    wrap()
    const dialog = await openAdopt(user)
    expect(within(dialog).getByText('Loading profiles…')).toHaveAttribute(
      'role',
      'status',
    )
    expect(
      within(dialog).getByRole('combobox', { name: 'Profile' }),
    ).toBeDisabled()
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Profile reference' }),
      'ppf_b',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
    await waitFor(() => expect(api.adoptAccount).toHaveBeenCalledOnce())
    expect(api.adoptAccount.mock.calls[0][0]).toBe('ppf_b')
  })

  it('a failed profile read keeps the reference path open', async () => {
    grant(AR, AW, PR)
    api.listProfiles.mockRejectedValue(
      new ApiError(403, 'forbidden', 'workspace confined'),
    )
    api.adoptAccount.mockResolvedValue(adopted)
    const user = userEvent.setup()
    wrap()
    const dialog = await openAdopt(user)
    expect(
      await within(dialog).findByText(/Profiles could not be listed\./),
    ).toBeInTheDocument()
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Profile reference' }),
      'ppf_b',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
    await waitFor(() =>
      expect(api.adoptAccount.mock.calls[0]?.[0]).toBe('ppf_b'),
    )
  })

  it('every control of the adopt journey is labelled and no raw key is painted', async () => {
    grant(AR, AW, PR)
    api.listProfiles.mockResolvedValue(page([profile('ppf_b', 'Home B')]))
    const user = userEvent.setup()
    wrap()
    const dialog = await openAdopt(user)
    // The picker is offered once its first page has answered.
    await waitFor(() =>
      expect(
        within(dialog).getByRole('combobox', { name: 'Profile' }),
      ).toBeEnabled(),
    )
    await user.click(within(dialog).getByRole('combobox', { name: 'Profile' }))
    await user.click(
      await screen.findByRole('option', { name: 'Enter a reference…' }),
    )
    expect(
      within(dialog).getByRole('textbox', { name: 'Profile reference' }),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByRole('textbox', { name: 'Account name (optional)' }),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByRole('button', { name: 'Cancel' }),
    ).toBeInTheDocument()
    // Nothing to submit yet: the reference is empty.
    expect(within(dialog).getByRole('button', { name: 'Adopt' })).toBeDisabled()
    expect(findRawI18nKeys(document.body)).toEqual([])
  })
})

describe('ProviderAccountsPanel — authority boundary and revocation', () => {
  it('a tenant switch while adopt is in flight paints, announces and opens nothing of the old answer', async () => {
    const answer = deferred<ProviderAccountDTO>()
    api.adoptAccount.mockReturnValue(answer.promise)
    const user = userEvent.setup()
    const { rerender } = wrap()
    const dialog = await openAdopt(user)
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Profile reference' }),
      'ppf_b',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Adopt' }))
    await waitFor(() => expect(api.adoptAccount).toHaveBeenCalledOnce())

    auth.activeTenant = 't2'
    api.listAccounts.mockResolvedValue(page([otherTenant]))
    rerender()
    expect(
      await screen.findByRole('button', { name: 'other-tenant-account' }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('dialog', { name: 'Adopt a provider profile' }),
    ).not.toBeInTheDocument()

    await act(async () => {
      answer.resolve(adopted)
    })
    expect(toast.success).not.toHaveBeenCalled()
    expect(api.getAccount).not.toHaveBeenCalled()
    expect(screen.queryByText('claude-b')).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('a late list answer of the previous tenant is canceled and never painted', async () => {
    const first = deferred<{ items: ProviderAccountDTO[]; has_more: boolean }>()
    api.listAccounts
      .mockReturnValueOnce(first.promise)
      .mockResolvedValue(page([otherTenant]))
    const { qc, rerender } = wrap()
    await waitFor(() => expect(api.listAccounts).toHaveBeenCalledOnce())
    const signal = (
      api.listAccounts.mock.calls[0][1] as { signal?: AbortSignal }
    ).signal

    auth.activeTenant = 't2'
    rerender()
    expect(
      await screen.findByRole('button', { name: 'other-tenant-account' }),
    ).toBeInTheDocument()
    expect(signal?.aborted).toBe(true)
    await act(async () => {
      first.resolve(page([workClaude]))
    })
    expect(screen.queryByText('work-claude')).not.toBeInTheDocument()
    const stale = qc
      .getQueryCache()
      .findAll()
      .filter((q) => JSON.stringify(q.state.data ?? null).includes('ppf_a'))
    expect(stale).toEqual([])
  })

  it('losing write ends the adopt draft; regaining it does not reopen the dialog', async () => {
    const user = userEvent.setup()
    const { rerender } = wrap()
    const dialog = await openAdopt(user)
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Profile reference' }),
      'ppf_b',
    )
    grant(AR)
    rerender()
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    grant(AR, AW)
    rerender()
    expect(
      await screen.findByRole('button', { name: 'Adopt a profile' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(api.adoptAccount).not.toHaveBeenCalled()
    // A new gesture starts from an empty draft.
    const fresh = await openAdopt(user)
    expect(
      within(fresh).getByRole('textbox', { name: 'Profile reference' }),
    ).toHaveValue('')
  })
})

describe('managed provider account creation', () => {
  it('allows correcting a definitively refused first driver without retaining a retry intention', async () => {
    api.listAccounts.mockResolvedValue(page([]))
    // writeRunErr supplies only error.message; the client defaults code to internal.
    api.createAccount.mockRejectedValueOnce(
      new ApiError(
        400,
        'internal',
        'driver may use only lowercase letters, digits and - _ .',
      ),
    )
    const created = account('ppf_corrected', 'codex', { home_mode: 'managed' })
    api.createAccount.mockResolvedValueOnce(created)
    api.getAccount.mockResolvedValue(created)
    const user = userEvent.setup()
    wrap()
    await user.click(
      await screen.findByRole('button', { name: 'Create account' }),
    )
    const dialog = await screen.findByRole('dialog', {
      name: 'Create provider account',
    })
    const driver = within(dialog).getByRole('textbox', {
      name: 'Provider driver',
    })
    await user.clear(driver)
    await user.type(driver, 'bad/key')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await within(dialog).findByRole('alert')
    const first = api.createAccount.mock.calls[0]?.[0]
    expect(driver).toBeEnabled()
    expect(
      within(dialog).getByRole('textbox', { name: 'Account name (optional)' }),
    ).toBeEnabled()
    expect(
      within(dialog).queryByRole('button', { name: 'Retry creation' }),
    ).not.toBeInTheDocument()
    expect(
      within(dialog).queryByText(/may have completed/),
    ).not.toBeInTheDocument()
    expect(toast.success).not.toHaveBeenCalled()
    await user.clear(driver)
    await user.type(driver, 'codex')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await waitFor(() => expect(api.createAccount).toHaveBeenCalledTimes(2))
    expect(api.createAccount.mock.calls[1]?.[0]).toEqual({
      driver: 'codex',
      idempotency_key: expect.any(String),
    })
    expect(api.createAccount.mock.calls[1]?.[0].idempotency_key).not.toBe(
      first.idempotency_key,
    )
    await waitFor(() => expect(toast.success).toHaveBeenCalledOnce())
  })

  it('allows naming a driver whose automatic account name was definitively refused', async () => {
    api.listAccounts.mockResolvedValue(page([]))
    api.createAccount.mockRejectedValueOnce(
      new ApiError(
        422,
        'internal',
        'the driver "custom.driver" cannot seed an account name: invalid account name',
      ),
    )
    const created = account('ppf_named', 'custom-team', {
      driver: 'custom.driver',
      home_mode: 'managed',
    })
    api.createAccount.mockResolvedValueOnce(created)
    api.getAccount.mockResolvedValue(created)
    const user = userEvent.setup()
    wrap()
    await user.click(
      await screen.findByRole('button', { name: 'Create account' }),
    )
    const dialog = await screen.findByRole('dialog', {
      name: 'Create provider account',
    })
    const driver = within(dialog).getByRole('textbox', {
      name: 'Provider driver',
    })
    const name = within(dialog).getByRole('textbox', {
      name: 'Account name (optional)',
    })
    await user.clear(driver)
    await user.type(driver, 'custom.driver')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await within(dialog).findByRole('alert')
    expect(name).toBeEnabled()
    const first = api.createAccount.mock.calls[0]?.[0]
    await user.type(name, 'custom-team')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await waitFor(() => expect(api.createAccount).toHaveBeenCalledTimes(2))
    expect(api.createAccount.mock.calls[1]?.[0]).toEqual({
      driver: 'custom.driver',
      name: 'custom-team',
      idempotency_key: expect.any(String),
    })
    expect(api.createAccount.mock.calls[1]?.[0].idempotency_key).not.toBe(
      first.idempotency_key,
    )
    await waitFor(() => expect(toast.success).toHaveBeenCalledOnce())
  })

  it.each([
    ['post-reservation path validation', 'claude', 400, 'config is too long'],
    [
      'post-reservation inaccessible home',
      'claude',
      422,
      'config is not accessible on this execution environment',
    ],
    [
      'indistinguishable name conflict',
      'claude',
      409,
      'account name "claude" is already taken in this environment; choose another name',
    ],
    [
      'custody conflict',
      'claude',
      409,
      'account home custody is unresolved; retain the same retry key for operator reconciliation',
    ],
    [
      'intention conflict',
      'claude',
      409,
      'idempotency key already names another account creation intention',
    ],
    [
      'unknown server failure with invalid input',
      'bad/key',
      503,
      'account creation outcome is unresolved',
    ],
    ['unproven Unicode normalization', 'İ', 400, 'config is too long'],
  ])(
    'retains the same request for %s',
    async (_label, driverValue, status, message) => {
      api.listAccounts.mockResolvedValue(page([]))
      api.createAccount.mockRejectedValue(
        new ApiError(status, 'internal', message),
      )
      const user = userEvent.setup()
      wrap()
      await user.click(
        await screen.findByRole('button', { name: 'Create account' }),
      )
      const dialog = await screen.findByRole('dialog', {
        name: 'Create provider account',
      })
      const driver = within(dialog).getByRole('textbox', {
        name: 'Provider driver',
      })
      const name = within(dialog).getByRole('textbox', {
        name: 'Account name (optional)',
      })
      await user.clear(driver)
      await user.type(driver, driverValue)
      await user.click(within(dialog).getByRole('button', { name: 'Create' }))
      await within(dialog).findByRole('alert')
      const first = api.createAccount.mock.calls[0]?.[0]
      expect(driver).toBeDisabled()
      expect(name).toBeDisabled()
      expect(within(dialog).getByText(/may have completed/)).toBeInTheDocument()
      await user.click(
        within(dialog).getByRole('button', { name: 'Retry creation' }),
      )
      await waitFor(() => expect(api.createAccount).toHaveBeenCalledTimes(2))
      expect(api.createAccount.mock.calls[1]?.[0]).toEqual(first)
      expect(toast.success).not.toHaveBeenCalled()
    },
  )

  it.each([
    ['driver refusal', 'bad/key', 400],
    ['automatic-name refusal', 'custom.driver', 422],
    ['ambiguous conflict', 'claude', 409],
  ])(
    'preserves an earlier lost answer across remount and later %s',
    async (_label, driverValue, status) => {
      api.listAccounts.mockResolvedValue(page([]))
      api.createAccount.mockRejectedValueOnce(
        new NetworkError('The answer was lost.'),
      )
      // Wording does not confer phase evidence, including for known-invalid input.
      api.createAccount.mockRejectedValue(
        new ApiError(status, 'internal', 'Request refused.'),
      )
      const qc = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      })
      const tree = (room: boolean) => (
        <QueryClientProvider client={qc}>
          {room ? <ProviderAccountsPanel /> : null}
        </QueryClientProvider>
      )
      const ui = render(tree(true))
      const user = userEvent.setup()
      await user.click(
        await screen.findByRole('button', { name: 'Create account' }),
      )
      const dialog = await screen.findByRole('dialog', {
        name: 'Create provider account',
      })
      const driver = within(dialog).getByRole('textbox', {
        name: 'Provider driver',
      })
      await user.clear(driver)
      await user.type(driver, driverValue)
      await user.click(within(dialog).getByRole('button', { name: 'Create' }))
      await screen.findByText('The answer was lost.')
      const first = api.createAccount.mock.calls[0]?.[0]
      ui.rerender(tree(false))
      ui.rerender(tree(true))
      await user.click(
        await screen.findByRole('button', { name: 'Retry creation' }),
      )
      const retry = await screen.findByRole('dialog', {
        name: 'Create provider account',
      })
      await user.click(
        within(retry).getByRole('button', { name: 'Retry creation' }),
      )
      await within(retry).findByText('Request refused.')
      expect(
        within(retry).getByRole('textbox', { name: 'Provider driver' }),
      ).toBeDisabled()
      expect(
        within(retry).getByRole('textbox', { name: 'Account name (optional)' }),
      ).toBeDisabled()
      expect(within(retry).getByText(/may have completed/)).toBeInTheDocument()
      await user.click(
        within(retry).getByRole('button', { name: 'Retry creation' }),
      )
      await waitFor(() => expect(api.createAccount).toHaveBeenCalledTimes(3))
      expect(api.createAccount.mock.calls.map(([request]) => request)).toEqual([
        first,
        first,
        first,
      ])
      expect(toast.success).not.toHaveBeenCalled()
    },
  )

  it('creates the selected driver once and keeps vendor authentication unknown', async () => {
    grant(AR, AW)
    api.listAccounts.mockResolvedValue(page([]))
    const created = account('ppf_managed', 'codex-team', {
      driver: 'codex',
      home_mode: 'managed',
      auth_source: '',
    })
    api.createAccount.mockResolvedValue(created)
    api.getAccount.mockResolvedValue(created)
    const user = userEvent.setup()
    wrap()
    await user.click(
      await screen.findByRole('button', { name: 'Create account' }),
    )
    const dialog = await screen.findByRole('dialog', {
      name: 'Create provider account',
    })
    const driver = within(dialog).getByRole('textbox', {
      name: 'Provider driver',
    })
    await user.clear(driver)
    await user.type(driver, 'codex')
    await user.type(
      within(dialog).getByRole('textbox', { name: 'Account name (optional)' }),
      'codex-team',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await waitFor(() => expect(api.createAccount).toHaveBeenCalledOnce())
    expect(api.createAccount.mock.calls[0]?.[0]).toEqual({
      driver: 'codex',
      name: 'codex-team',
      idempotency_key: expect.any(String),
    })
    expect(api.createAccount.mock.calls[0]?.[1]).toMatchObject({
      tenant: 't1',
      dispatchGuard: expect.any(Function),
    })
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'Account codex-team created. Sign in separately before launching.',
        undefined,
      ),
    )
  })

  it('retries an uncertain creation with the identical intention after the room remounts', async () => {
    grant(AR, AW)
    api.listAccounts.mockResolvedValue(page([]))
    api.createAccount.mockRejectedValueOnce(
      new ApiError(
        503,
        'unavailable',
        'account creation outcome is unresolved',
      ),
    )
    const created = account('ppf_recovered', 'claude', {
      home_mode: 'managed',
      auth_source: '',
    })
    api.createAccount.mockResolvedValueOnce(created)
    api.getAccount.mockResolvedValue(created)
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const tree = (room: boolean) => (
      <QueryClientProvider client={qc}>
        {room ? <ProviderAccountsPanel /> : null}
      </QueryClientProvider>
    )
    const ui = render(tree(true))
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Create account' }),
    )
    const dialog = await screen.findByRole('dialog', {
      name: 'Create provider account',
    })
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await screen.findByText('account creation outcome is unresolved')
    expect(toast.success).not.toHaveBeenCalled()
    const first = api.createAccount.mock.calls[0]?.[0]
    ui.rerender(tree(false))
    ui.rerender(tree(true))
    await user.click(
      await screen.findByRole('button', { name: 'Retry creation' }),
    )
    const retry = await screen.findByRole('dialog', {
      name: 'Create provider account',
    })
    expect(
      within(retry).getByRole('textbox', { name: 'Provider driver' }),
    ).toBeDisabled()
    await user.click(
      within(retry).getByRole('button', { name: 'Retry creation' }),
    )
    await waitFor(() => expect(api.createAccount).toHaveBeenCalledTimes(2))
    expect(api.createAccount.mock.calls[1]?.[0]).toEqual(first)
    await waitFor(() => expect(toast.success).toHaveBeenCalledOnce())
  })

  it('offers no create control without the account write tier', async () => {
    grant(AR)
    api.listAccounts.mockResolvedValue(page([]))
    wrap()
    await screen.findByText('No provider accounts')
    expect(
      screen.queryByRole('button', { name: 'Create account' }),
    ).not.toBeInTheDocument()
    expect(api.createAccount).not.toHaveBeenCalled()
  })
})

describe('ProviderAccountsPanel — display name metadata', () => {
  it('changes only the selected color and keeps the existing display label', async () => {
    const user = userEvent.setup()
    let current = {
      ...workClaude,
      display_name: 'Research account',
      accent: 'blue',
    }
    api.getAccount.mockImplementation(async () => current)
    api.patchAccountMetadata.mockImplementation(async (_ref, body) => {
      current = { ...current, ...body }
      return current
    })
    wrap()
    await user.click(await screen.findByRole('button', { name: 'work-claude' }))
    await user.click(
      await screen.findByRole('button', { name: 'Edit label and color' }),
    )
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Color' }),
      'green',
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.patchAccountMetadata).toHaveBeenCalledOnce())
    expect(api.patchAccountMetadata).toHaveBeenCalledWith(
      'ppf_a',
      { accent: 'green' },
      { tenant: 't1', dispatchGuard: expect.any(Function) },
    )
    await waitFor(() =>
      expect(
        screen.queryByRole('combobox', { name: 'Color' }),
      ).not.toBeInTheDocument(),
    )
    expect(
      screen.getByRole('heading', { name: 'Research account' }),
    ).toBeInTheDocument()
  })

  it('retires the editor when account write is revoked, without retaining its draft', async () => {
    const user = userEvent.setup()
    const ui = wrap()
    await user.click(await screen.findByRole('button', { name: 'work-claude' }))
    await user.click(
      await screen.findByRole('button', { name: 'Edit label and color' }),
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Display name' }),
      'Private draft',
    )
    grant(AR)
    ui.rerender()
    expect(
      screen.queryByRole('textbox', { name: 'Display name' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Edit label and color' }),
    ).not.toBeInTheDocument()
    expect(api.patchAccountMetadata).not.toHaveBeenCalled()
    grant(AR, AW)
    ui.rerender()
    await user.click(
      await screen.findByRole('button', { name: 'Edit label and color' }),
    )
    expect(screen.getByRole('textbox', { name: 'Display name' })).toHaveValue(
      '',
    )
  })

  it('edits and clears the label while preserving the stable account name and reference', async () => {
    const user = userEvent.setup()
    let current = workClaude
    api.getAccount.mockImplementation(async () => current)
    api.patchAccountMetadata.mockImplementation(async (_ref, body) => {
      current = { ...current, ...body }
      api.listAccounts.mockResolvedValue(page([current, reviewCodex]))
      return current
    })
    wrap()
    await user.click(await screen.findByRole('button', { name: 'work-claude' }))
    for (const label of ['Research account', '']) {
      await user.click(
        await screen.findByRole('button', { name: 'Edit label and color' }),
      )
      const input = screen.getByRole('textbox', { name: 'Display name' })
      await user.clear(input)
      if (label) await user.type(input, label)
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(input).not.toBeInTheDocument())
      expect(api.patchAccountMetadata).toHaveBeenLastCalledWith(
        'ppf_a',
        { display_name: label },
        { tenant: 't1', dispatchGuard: expect.any(Function) },
      )
      const detail = screen.getByRole('dialog')
      expect(
        within(detail).getByText('work-claude', {
          exact: true,
          selector: 'dd',
        }),
      ).toBeInTheDocument()
      expect(within(detail).getAllByText('ppf_a').length).toBeGreaterThan(0)
      if (label)
        expect(within(detail).getAllByText(label).length).toBeGreaterThan(0)
    }
    expect(api.createAccount).not.toHaveBeenCalled()
    expect(api.adoptAccount).not.toHaveBeenCalled()
    expect(toast.success).toHaveBeenCalledTimes(2)
  })

  it.each(['read-only', 'retired'])(
    'does not offer metadata edit for %s accounts',
    async (state) => {
      if (state === 'read-only') grant(AR)
      else api.getAccount.mockResolvedValue({ ...workClaude, state: 'retired' })
      const user = userEvent.setup()
      wrap()
      await user.click(
        await screen.findByRole('button', { name: 'work-claude' }),
      )
      await screen.findByText(
        'Not checked: nothing has asked the provider who is signed in.',
      )
      expect(
        screen.queryByRole('button', { name: 'Edit label and color' }),
      ).not.toBeInTheDocument()
    },
  )

  it('preserves the submitted label after an unknown response for deliberate retry', async () => {
    const user = userEvent.setup()
    api.patchAccountMetadata.mockRejectedValue(
      new NetworkError('Lost response'),
    )
    wrap()
    await user.click(await screen.findByRole('button', { name: 'work-claude' }))
    await user.click(
      await screen.findByRole('button', { name: 'Edit label and color' }),
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Display name' }),
      'Research account',
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'The save may have completed',
    )
    expect(screen.getByRole('textbox', { name: 'Display name' })).toHaveValue(
      'Research account',
    )
    expect(toast.success).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(api.patchAccountMetadata).toHaveBeenCalledTimes(2)
    expect(api.patchAccountMetadata.mock.calls[1][1]).toEqual(
      api.patchAccountMetadata.mock.calls[0][1],
    )
  })

  it.each(['tenant', 'principal', 'credential'])(
    'discards the draft and late result when %s changes',
    async (kind) => {
      const user = userEvent.setup()
      const pending = deferred<ProviderAccountDTO>()
      api.patchAccountMetadata.mockReturnValue(pending.promise)
      const ui = wrap()
      await user.click(
        await screen.findByRole('button', { name: 'work-claude' }),
      )
      await user.click(
        await screen.findByRole('button', { name: 'Edit label and color' }),
      )
      await user.type(
        screen.getByRole('textbox', { name: 'Display name' }),
        'Private draft',
      )
      await user.click(screen.getByRole('button', { name: 'Save' }))
      if (kind === 'tenant') auth.activeTenant = 't2'
      if (kind === 'principal') auth.principal = 'u2'
      if (kind === 'credential')
        useSessionStore.getState().setSession({
          token: 'olvs_rotated',
          sessionId: 'sid-1',
          expiresAt: '2030-01-01T00:00:00Z',
        })
      api.listAccounts.mockResolvedValue(page([]))
      ui.rerender()
      await waitFor(() =>
        expect(
          screen.queryByRole('textbox', { name: 'Display name' }),
        ).not.toBeInTheDocument(),
      )
      await act(async () =>
        pending.resolve({ ...workClaude, display_name: 'Private draft' }),
      )
      expect(toast.success).not.toHaveBeenCalled()
      expect(screen.queryByText('Private draft')).not.toBeInTheDocument()
      expect(api.patchAccountMetadata).toHaveBeenCalledOnce()
      expect(api.patchAccountMetadata.mock.calls[0][2].tenant).toBe('t1')
    },
  )
})

it('clearing accent must remove the original detail-title swatch', async () => {
  const user = userEvent.setup()
  const colored = account('ppf_a', 'work-claude', { accent: 'blue' })
  let current: ProviderAccountDTO = colored
  api.listAccounts.mockImplementation(async () => page([current]))
  api.getAccount.mockImplementation(async () => current)
  api.patchAccountMetadata.mockImplementation(async (_ref, body) => {
    expect(body).toEqual({ accent: '' })
    current = account('ppf_a', 'work-claude') // real Go DTO omits cleared accent
    return current
  })
  wrap()
  await user.click(await screen.findByRole('button', { name: 'work-claude' }))
  const dialog = await screen.findByRole('dialog')
  expect(
    within(dialog)
      .getByRole('heading', { name: 'work-claude' })
      .querySelector('[data-provider-accent="blue"]'),
  ).not.toBeNull()
  await user.click(
    await screen.findByRole('button', { name: 'Edit label and color' }),
  )
  await user.selectOptions(screen.getByRole('combobox', { name: 'Color' }), '')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(api.patchAccountMetadata).toHaveBeenCalledOnce())
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Edit label and color' }),
    ).toBeInTheDocument(),
  )
  expect(api.getAccount.mock.calls.length).toBeGreaterThan(1)
  expect(
    within(dialog)
      .getByRole('heading', { name: 'work-claude' })
      .querySelector('[data-provider-accent]'),
  ).toBeNull()
})

// Compatibility controls: only a supported current value paints a swatch.
it.each([
  ['empty string', '', null],
  ['literal default', 'default', null],
  ['numeric zero', 0, null],
  ['supported current green', 'green', 'green'],
  ['omitted', undefined, null],
])(
  'current detail %s takes precedence over initial blue',
  async (_name, accent, expected) => {
    const user = userEvent.setup()
    const initial = account('ppf_a', 'work-claude', { accent: 'blue' })
    const current = account('ppf_a', 'work-claude', {
      display_name: 'Current detail',
      accent: accent as ProviderAccountDTO['accent'],
    })
    api.listAccounts.mockResolvedValue(page([initial]))
    api.getAccount.mockResolvedValue(current)
    wrap()
    await user.click(await screen.findByRole('button', { name: 'work-claude' }))
    const heading = await screen.findByRole('heading', {
      name: 'Current detail',
    })
    const swatch = heading.querySelector('[data-provider-accent]')
    if (expected === null) expect(swatch).toBeNull()
    else expect(swatch).toHaveAttribute('data-provider-accent', expected)
  },
)

it('can restore the opening label after an applied write loses its response', async () => {
  const user = userEvent.setup()
  let current = account('ppf_a', 'work-claude', {
    display_name: 'Original label',
  })
  api.listAccounts.mockImplementation(async () => page([current]))
  api.getAccount.mockImplementation(async () => current)
  api.patchAccountMetadata.mockImplementation(async (_ref, body) => {
    current = { ...current, ...body }
    throw new ApiError(503, 'unavailable', 'response lost after commit')
  })
  wrap()
  await user.click(
    await screen.findByRole('button', { name: 'Original label' }),
  )
  await user.click(
    await screen.findByRole('button', { name: 'Edit label and color' }),
  )
  const input = screen.getByRole('textbox', { name: 'Display name' })
  await user.clear(input)
  await user.type(input, 'Changed label')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await screen.findByRole('alert')
  expect(current.display_name).toBe('Changed label')
  await user.clear(input)
  await user.type(input, 'Original label')
  expect(screen.getByRole('button', { name: 'Save' })).not.toBeDisabled()
})

describe('ProviderAccountsPanel — uncertain metadata fields', () => {
  async function openEditor(user: ReturnType<typeof userEvent.setup>) {
    await user.click(await screen.findByRole('button', { name: 'work-claude' }))
    await user.click(
      await screen.findByRole('button', { name: 'Edit label and color' }),
    )
    return screen.findByRole('textbox', { name: 'Display name' })
  }

  it.each(['display_name', 'accent'] as const)(
    'restores an uncertain %s without overwriting a concurrent other field',
    async (field) => {
      const user = userEvent.setup()
      let current = {
        ...workClaude,
        display_name: 'Original label',
        accent: 'blue',
      }
      api.getAccount.mockImplementation(async () => current)
      let lost = true
      api.patchAccountMetadata.mockImplementation(async (_ref, body) => {
        current = { ...current, ...body }
        if (lost) {
          lost = false
          throw new ApiError(503, 'unavailable', 'lost after commit')
        }
        return current
      })
      wrap()
      const input = await openEditor(user)
      const select = screen.getByRole('combobox', { name: 'Color' })
      if (field === 'display_name') {
        await user.clear(input)
        await user.type(input, 'Changed label')
      } else await user.selectOptions(select, 'green')
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await screen.findByRole('alert')
      if (field === 'display_name') {
        current = { ...current, accent: 'red' }
        await user.clear(input)
        await user.type(input, 'Original label')
      } else {
        current = { ...current, display_name: 'Concurrent label' }
        await user.selectOptions(select, 'blue')
      }
      expect(screen.getByRole('button', { name: 'Save' })).not.toBeDisabled()
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await waitFor(() =>
        expect(api.patchAccountMetadata).toHaveBeenCalledTimes(2),
      )
      expect(api.patchAccountMetadata.mock.calls[1][1]).toEqual(
        field === 'display_name'
          ? { display_name: 'Original label' }
          : { accent: 'blue' },
      )
      expect(current).toMatchObject(
        field === 'display_name'
          ? { display_name: 'Original label', accent: 'red' }
          : { display_name: 'Concurrent label', accent: 'blue' },
      )
      await waitFor(() => expect(toast.success).toHaveBeenCalledOnce())
    },
  )

  it.each([400, 409, 422])(
    'keeps a submitted field uncertain after HTTP %s without write-phase evidence',
    async (status) => {
      const user = userEvent.setup()
      let current = { ...workClaude, display_name: 'Original' }
      api.getAccount.mockImplementation(async () => current)
      api.patchAccountMetadata.mockImplementation(async (_ref, body) => {
        current = { ...current, ...body }
        throw new ApiError(status, 'unclassified', 'Unknown write phase')
      })
      wrap()
      const input = await openEditor(user)
      await user.clear(input)
      await user.type(input, 'Changed')
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await screen.findByRole('alert')
      await user.clear(input)
      await user.type(input, 'Original')
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await waitFor(() =>
        expect(api.patchAccountMetadata).toHaveBeenCalledTimes(2),
      )
      expect(api.patchAccountMetadata.mock.calls[1][1]).toEqual({
        display_name: 'Original',
      })
      expect(current.display_name).toBe('Original')
    },
  )

  it.each(['display_name', 'accent'] as const)(
    'retries an uncertain explicit clear of %s without adding the other field',
    async (field) => {
      const user = userEvent.setup()
      let current: ProviderAccountDTO = {
        ...workClaude,
        display_name: 'Original',
        accent: 'blue',
      }
      api.getAccount.mockImplementation(async () => current)
      api.patchAccountMetadata.mockImplementation(async (_ref, body) => {
        const next = { ...current, ...body }
        if (next.display_name === '') delete next.display_name
        if (next.accent === '') delete next.accent
        current = next
        if (api.patchAccountMetadata.mock.calls.length === 1)
          throw new NetworkError('lost after clear')
        return current
      })
      wrap()
      const input = await openEditor(user)
      if (field === 'display_name') await user.clear(input)
      else
        await user.selectOptions(
          screen.getByRole('combobox', { name: 'Color' }),
          '',
        )
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await screen.findByRole('alert')
      await user.click(screen.getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(toast.success).toHaveBeenCalledOnce())
      expect(
        api.patchAccountMetadata.mock.calls.map((call) => call[1]),
      ).toEqual([{ [field]: '' }, { [field]: '' }])
      expect(current[field]).toBeUndefined()
    },
  )

  it('keeps the union of uncertain fields across later edits and restores both original values', async () => {
    const user = userEvent.setup()
    let current = { ...workClaude, display_name: 'Original', accent: 'blue' }
    api.getAccount.mockImplementation(async () => current)
    api.patchAccountMetadata.mockImplementation(async (_ref, body) => {
      current = { ...current, ...body }
      throw new NetworkError('lost after commit')
    })
    wrap()
    const input = await openEditor(user)
    await user.clear(input)
    await user.type(input, 'Changed')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByRole('alert')
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Color' }),
      'green',
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByRole('alert')
    await user.clear(input)
    await user.type(input, 'Original')
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Color' }),
      'blue',
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.patchAccountMetadata).toHaveBeenCalledTimes(3),
    )
    expect(api.patchAccountMetadata.mock.calls.map((call) => call[1])).toEqual([
      { display_name: 'Changed' },
      { display_name: 'Changed', accent: 'green' },
      { display_name: 'Original', accent: 'blue' },
    ])
    expect(current).toMatchObject({ display_name: 'Original', accent: 'blue' })
  })

  it('does not send known no-ops, including an unsent edit restored before saving', async () => {
    const user = userEvent.setup()
    api.getAccount.mockResolvedValue({
      ...workClaude,
      display_name: 'Original',
      accent: 'blue',
    })
    wrap()
    const input = await openEditor(user)
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    await user.clear(input)
    await user.type(input, 'Changed')
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Color' }),
      'green',
    )
    await user.clear(input)
    await user.type(input, 'Original')
    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Color' }),
      'blue',
    )
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(api.patchAccountMetadata).not.toHaveBeenCalled()
  })

  it('requires a successful new GET after uncertain cancel, even when the old detail remains cached', async () => {
    const user = userEvent.setup()
    let current = { ...workClaude, display_name: 'Original', accent: 'blue' }
    api.getAccount.mockImplementation(async () => current)
    api.patchAccountMetadata.mockImplementation(async (_ref, body) => {
      current = { ...current, ...body }
      throw new NetworkError('lost after commit')
    })
    wrap()
    const input = await openEditor(user)
    await user.clear(input)
    await user.type(input, 'Applied')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await screen.findByRole('alert')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    const readCount = api.getAccount.mock.calls.length
    const pending = deferred<ProviderAccountDTO>()
    api.getAccount.mockReturnValueOnce(pending.promise)
    await user.click(
      screen.getByRole('button', { name: 'Edit label and color' }),
    )
    expect(api.getAccount.mock.calls.length).toBe(readCount + 1)
    expect(
      screen.queryByRole('textbox', { name: 'Display name' }),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Edit label and color' }),
    ).toBeDisabled()
    await act(async () => pending.reject(new NetworkError('read unavailable')))
    await screen.findByRole('alert')
    expect(
      screen.queryByRole('textbox', { name: 'Display name' }),
    ).not.toBeInTheDocument()
    current = { ...current, accent: 'red' }
    await user.click(
      screen.getByRole('button', { name: 'Edit label and color' }),
    )
    expect(
      await screen.findByRole('textbox', { name: 'Display name' }),
    ).toHaveValue('Applied')
    expect(screen.getByRole('combobox', { name: 'Color' })).toHaveValue('red')
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    const restored = screen.getByRole('textbox', { name: 'Display name' })
    await user.clear(restored)
    await user.type(restored, 'Original')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(api.patchAccountMetadata).toHaveBeenCalledTimes(2),
    )
    expect(api.patchAccountMetadata.mock.calls[1][1]).toEqual({
      display_name: 'Original',
    })
    expect(current.accent).toBe('red')
  })

  it.each(['permission', 'tenant', 'principal', 'credential', 'unmount'])(
    'discards an opening GET when %s changes before its answer',
    async (kind) => {
      const user = userEvent.setup()
      const ui = wrap()
      await user.click(
        await screen.findByRole('button', { name: 'work-claude' }),
      )
      await screen.findByRole('button', { name: 'Edit label and color' })
      const pending = deferred<ProviderAccountDTO>()
      api.getAccount.mockReturnValueOnce(pending.promise)
      await user.click(
        screen.getByRole('button', { name: 'Edit label and color' }),
      )
      const signal = api.getAccount.mock.calls.at(-1)?.[1].signal as AbortSignal
      if (kind === 'permission') grant(AR)
      if (kind === 'tenant') auth.activeTenant = 't2'
      if (kind === 'principal') auth.principal = 'u2'
      if (kind === 'credential')
        useSessionStore.getState().setSession({
          token: 'olvs_rotated',
          sessionId: 'sid-1',
          expiresAt: '2030-01-01T00:00:00Z',
        })
      if (kind === 'unmount') await user.keyboard('{Escape}')
      else ui.rerender()
      await waitFor(() => expect(signal.aborted).toBe(true))
      await act(async () =>
        pending.resolve({ ...workClaude, display_name: 'Late private value' }),
      )
      expect(
        screen.queryByDisplayValue('Late private value'),
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole('textbox', { name: 'Display name' }),
      ).not.toBeInTheDocument()
      expect(api.patchAccountMetadata).not.toHaveBeenCalled()
    },
  )

  it('regrant reads the applied value anew and suppresses a late PATCH success', async () => {
    const user = userEvent.setup()
    let current = { ...workClaude, display_name: 'Original' }
    api.getAccount.mockImplementation(async () => current)
    const pending = deferred<ProviderAccountDTO>()
    api.patchAccountMetadata.mockImplementation((_ref, body) => {
      current = { ...current, ...body }
      return pending.promise
    })
    const ui = wrap()
    const input = await openEditor(user)
    await user.clear(input)
    await user.type(input, 'Applied')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    grant(AR)
    ui.rerender()
    await act(async () => pending.resolve(current))
    expect(toast.success).not.toHaveBeenCalled()
    grant(AR, AW)
    ui.rerender()
    const readCount = api.getAccount.mock.calls.length
    await user.click(
      await screen.findByRole('button', { name: 'Edit label and color' }),
    )
    expect(
      await screen.findByRole('textbox', { name: 'Display name' }),
    ).toHaveValue('Applied')
    expect(api.getAccount.mock.calls.length).toBe(readCount + 1)
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('does not edit a freshly retired account even if the detail snapshot was active', async () => {
    const user = userEvent.setup()
    wrap()
    await user.click(await screen.findByRole('button', { name: 'work-claude' }))
    await screen.findByRole('button', { name: 'Edit label and color' })
    api.getAccount.mockResolvedValue({ ...workClaude, state: 'retired' })
    await user.click(
      screen.getByRole('button', { name: 'Edit label and color' }),
    )
    expect(await screen.findByRole('status')).toHaveTextContent('Retired')
    expect(
      screen.queryByRole('textbox', { name: 'Display name' }),
    ).not.toBeInTheDocument()
    expect(api.patchAccountMetadata).not.toHaveBeenCalled()
  })
})
