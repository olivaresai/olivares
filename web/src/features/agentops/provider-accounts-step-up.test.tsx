// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// An adoption that meets an assurance demand. The engine answers `403 step_up_required`
// before it authorizes anything, so the first POST had no effect. That is a definite
// refusal, not an unknown outcome. The privileged-mutation lifecycle then runs the step-up
// ceremony and RESUMES the same execution: the resumed POST is the first one that can take
// effect, and it is not a replay. The resume is dispatched without the options of the
// operator's original call, so its answer must still be reported, whatever it is.
//
// No production route declares a minimum assurance level for adoption today. The engine's
// demand is simulated at the typed client, and the real `usePrivilegedMutation` and step-up
// store drive the demand and its resume.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) =>
      p === 'sessions:account:read' || p === 'sessions:account:write',
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
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})

import { ApiError, NetworkError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { useStepUpStore } from '@/stores/step-up'
import { ProviderAccountsPanel } from './provider-accounts-panel'
import type { ProviderAccountDTO } from './types'

const adopted: ProviderAccountDTO = {
  account_ref: 'ppf_b',
  name: 'claude-b',
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
const stepUp = () =>
  new ApiError(403, 'step_up_required', 'step-up authentication required')

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <ProviderAccountsPanel />
    </QueryClientProvider>,
  )
  return qc
}

/** Submits an adoption of ppf_b and waits for its first dispatch to be refused by the
 *  engine's assurance demand. */
async function submitIntoStepUp(user: ReturnType<typeof userEvent.setup>) {
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
  await waitFor(() => expect(useStepUpStore.getState().request).not.toBeNull())
  return dialog
}

/** What the ceremony does once the session is elevated: consume the demand, which
 *  resumes the SAME execution through the privileged-mutation lifecycle. */
async function completeTheCeremony() {
  const request = useStepUpStore.getState().request
  expect(request?.retry).toBeTypeOf('function')
  await act(async () => {
    request?.retry?.()
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  useStepUpStore.getState().clear()
  useSessionStore.setState({
    token: 'olvs_step_up',
    sessionId: 'sid-step-up',
    expiresAt: '2030-01-01T00:00:00Z',
  })
  api.listAccounts.mockResolvedValue({ items: [], has_more: false })
  api.getAccount.mockImplementation(async (ref: string) => {
    if (ref === 'ppf_b') return adopted
    throw new ApiError(404, 'not_found', 'provider account not found')
  })
})

describe('an assurance demand before any effect', () => {
  it('is a definite refusal: no unknown-outcome notice, the draft stays, and the resumed success is announced', async () => {
    api.adoptAccount
      .mockRejectedValueOnce(stepUp())
      .mockResolvedValueOnce(adopted)
    const user = userEvent.setup()
    wrap()
    const dialog = await submitIntoStepUp(user)
    expect(screen.queryByText(/is not known here/)).toBeNull()
    expect(
      within(dialog).getByRole('textbox', { name: 'Profile reference' }),
    ).toHaveValue('ppf_b')
    expect(toast.success).not.toHaveBeenCalled()
    expect(api.getAccount).not.toHaveBeenCalled()

    await completeTheCeremony()
    await waitFor(() => expect(api.adoptAccount).toHaveBeenCalledTimes(2))
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'Adopted as claude-b',
        undefined,
      ),
    )
    expect(
      await screen.findByRole('heading', { name: /claude-b/ }),
    ).toBeInTheDocument()
    expect(screen.queryByText(/is not known here/)).toBeNull()
  })

  it('a refusal answered to the resumed POST keeps the engine’s sentence beside the draft', async () => {
    const reason =
      'account name "claude-b" is already taken in this environment; choose another name'
    api.adoptAccount
      .mockRejectedValueOnce(stepUp())
      .mockRejectedValueOnce(new ApiError(409, 'conflict', reason))
    const user = userEvent.setup()
    wrap()
    const dialog = await submitIntoStepUp(user)
    await completeTheCeremony()
    await waitFor(() => expect(api.adoptAccount).toHaveBeenCalledTimes(2))
    const alert = await within(dialog).findByRole('alert')
    expect(alert).toHaveTextContent('The profile was not adopted')
    expect(alert).toHaveTextContent(reason)
    expect(screen.queryByText(/is not known here/)).toBeNull()
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('a lost answer to the resumed POST is unknown: the draft closes and only reads reconcile it', async () => {
    api.adoptAccount
      .mockRejectedValueOnce(stepUp())
      .mockRejectedValueOnce(new NetworkError('down'))
    const user = userEvent.setup()
    wrap()
    await submitIntoStepUp(user)
    const reads = api.listAccounts.mock.calls.length
    await completeTheCeremony()
    expect(
      await screen.findByText(
        'The outcome of adopting ppf_b is not known here.',
      ),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('dialog', { name: 'Adopt a provider profile' }),
    ).not.toBeInTheDocument()
    await waitFor(() =>
      expect(api.listAccounts.mock.calls.length).toBeGreaterThan(reads),
    )
    expect(
      await screen.findByText('At this read, ppf_b is the account claude-b.'),
    ).toBeInTheDocument()
    // One POST refused before any effect, one effectful POST; reconciliation is GET only.
    expect(api.adoptAccount).toHaveBeenCalledTimes(2)
    await user.click(screen.getByRole('button', { name: 'Check again' }))
    await waitFor(() =>
      expect(api.getAccount.mock.calls.length).toBeGreaterThanOrEqual(2),
    )
    expect(api.adoptAccount).toHaveBeenCalledTimes(2)
  })
})
