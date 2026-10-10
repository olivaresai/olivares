// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A tool with several profiles: the New session dialog offers a profile select, defaulting
// to the profile used most recently that is ready, else the first ready one. A tool with
// one profile shows nothing new, and the engine picks the profile as it always did.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    isSuperadmin: true,
    activeTenant: 'tnt-a',
    principal: { user_id: 'u1', aal: 1 },
  }),
}))
const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useRouterState: () => '',
}))
const launch = vi.hoisted(() => ({ launchSession: vi.fn() }))
vi.mock('@/features/agentops/session-launch', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  launchSession: launch.launchSession,
}))
const signedIn = vi.hoisted(() => ({ refs: new Set<string>() }))
vi.mock('./api', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  signInApi: {
    status: (driver: string, _t: unknown, _s: unknown, ref?: string) =>
      Promise.resolve({
        driver,
        installed: true,
        signed_in: ref ? signedIn.refs.has(ref) : true,
      }),
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import type {
  ProviderAccountDTO,
  ProviderProfileDTO,
  RunDTO,
} from '@/features/agentops/types'
import { useTenantStore } from '@/stores/tenant'
import { StartSessionForm } from './first-hour'
import { readinessOf } from './readiness.fixture'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const account = (
  ref: string,
  name: string,
  driver = 'claude',
): ProviderAccountDTO => ({
  account_ref: ref,
  name,
  driver,
  environment_ref: 'xenv_1',
  state: 'active',
  home_mode: 'managed',
  home_generation: 1,
  home_relative: '',
  isolation_level: 'shared',
  auth_source: 'provider_account_home',
  identity: '',
  identity_source: 'none',
  created_at: '2026-10-01T10:00:00Z',
  updated_at: '2026-10-01T10:00:00Z',
})

const profile = (ref: string, accountName?: string): ProviderProfileDTO => ({
  profile_ref: ref,
  driver: 'claude',
  environment_ref: 'xenv_1',
  state: 'active',
  local_environment: true,
  operable: true,
  auth_source: 'provider_account_home',
  ...(accountName ? { account_name: accountName } : {}),
})

const run = (ref: string, profile: string, driver = 'claude') =>
  ({
    run_ref: ref,
    state: 'completed',
    provider_profile_ref: profile,
    provider_driver: driver,
  }) as unknown as RunDTO

beforeEach(() => {
  vi.restoreAllMocks()
  launch.launchSession.mockReset()
  launch.launchSession.mockResolvedValue({ run_ref: 'run_1' })
  signedIn.refs = new Set(['ppf_b', 'ppf_c'])
  useTenantStore.setState({ activeTenant: 'tnt-a' })
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
    readinessOf({ claude: 'own_login', codex: 'own_login' }),
  )
  vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
    items: [account('ppf_b', 'claude-b'), account('ppf_c', 'claude-c')],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
    items: [],
    has_more: false,
  })
})

const profileSelect = () => screen.findByRole('combobox', { name: 'Profile' })

describe('the profile in the New session dialog', () => {
  it('shows nothing new for a tool with one profile', async () => {
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
      items: [account('ppf_x', 'codex-b', 'codex')],
      has_more: false,
    })
    wrap(<StartSessionForm />)
    await screen.findByRole('radio', { name: 'Claude Code' })
    // Claude Code has only its default login: no select. (The one account is Codex's.)
    await waitFor(() => expect(agentOpsApi.listAccounts).toHaveBeenCalled())
    expect(screen.queryByRole('combobox', { name: 'Profile' })).toBeNull()
  })

  it('offers the profiles of the chosen tool and starts on the first ready one when none ran yet', async () => {
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    const select = await profileSelect()
    // The default login is the first profile of the tool, and is not named after it.
    expect(select).toHaveTextContent('Default sign-in')
    await user.click(select)
    expect(
      (await screen.findAllByRole('option')).map((o) => o.textContent),
    ).toEqual(['Default sign-in', 'claude-b', 'claude-c'])
    await user.keyboard('{Escape}')
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    // The default login is picked by the engine's own rule: no profile named.
    expect(launch.launchSession.mock.calls[0][0].quick).not.toHaveProperty(
      'profileRef',
    )
  })

  it('defaults to the profile that ran most recently, and starts on it', async () => {
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
      items: [run('r2', 'ppf_c'), run('r1', 'ppf_b')],
      has_more: false,
    })
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    const select = await profileSelect()
    await waitFor(() => expect(select).toHaveTextContent('claude-c'))
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      driver: 'claude',
      profileRef: 'ppf_c',
    })
  })

  it('skips a profile that is not signed in, and says so in the list', async () => {
    signedIn.refs = new Set(['ppf_b'])
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
      items: [run('r2', 'ppf_c'), run('r1', 'ppf_b')],
      has_more: false,
    })
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    const select = await profileSelect()
    // claude-c ran last but is signed out: the most recent ready one is claude-b.
    await waitFor(() => expect(select).toHaveTextContent('claude-b'))
    await user.click(select)
    const option = await screen.findByRole('option', { name: /claude-c/ })
    expect(option).toHaveTextContent('Not signed in')
    expect(option).toHaveAttribute('aria-disabled', 'true')
  })

  it('reads the profile names the engine gives: the unnamed profile is the default login', async () => {
    signedIn.refs = new Set(['ppf_x', 'ppf_b'])
    vi.spyOn(agentOpsApi, 'listProfiles').mockResolvedValue({
      items: [
        profile('ppf_d'),
        profile('ppf_x', 'claude'),
        profile('ppf_b', 'claude-b'),
      ],
      has_more: false,
    })
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
      items: [
        { ...account('ppf_x', 'claude'), home_mode: 'adopted' },
        account('ppf_b', 'claude-b'),
      ],
      has_more: false,
    })
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    const select = await profileSelect()
    await user.click(select)
    expect(
      (await screen.findAllByRole('option')).map((o) => o.textContent),
    ).toEqual(['Default sign-in', 'claude', 'claude-b'])
  })

  it('ignores sessions that ran on a profile that has been removed', async () => {
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
      items: [
        account('ppf_b', 'claude-b'),
        { ...account('ppf_c', 'claude-c'), state: 'retired' },
      ],
      has_more: false,
    })
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
      items: [run('r2', 'ppf_c'), run('r1', 'ppf_b')],
      has_more: false,
    })
    wrap(<StartSessionForm />)
    const select = await profileSelect()
    // claude-c ran last but is gone, and its sessions do not count for the default
    // login: the most recent ready profile is claude-b.
    await waitFor(() => expect(select).toHaveTextContent('claude-b'))
  })

  it('starts on a ready profile when the default login is not ready', async () => {
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({
        codex: 'own_login',
      }),
    )
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    // Codex is the only tool ready on its own, so the dialog opens on it.
    await user.click(await screen.findByRole('radio', { name: 'Claude Code' }))
    const select = await profileSelect()
    await waitFor(() => expect(select).toHaveTextContent('claude-b'))
    const start = screen.getByRole('button', { name: 'Start' })
    await waitFor(() => expect(start).toBeEnabled())
    await user.click(start)
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      profileRef: 'ppf_b',
    })
  })

  it('hides the select again for a tool with one profile', async () => {
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    await profileSelect()
    await user.click(screen.getByRole('radio', { name: 'Codex' }))
    await waitFor(() =>
      expect(screen.queryByRole('combobox', { name: 'Profile' })).toBeNull(),
    )
    expect(
      within(document.body).getByRole('radio', { name: 'Codex' }),
    ).toBeChecked()
  })
})
