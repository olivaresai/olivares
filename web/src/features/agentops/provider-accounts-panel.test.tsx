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
