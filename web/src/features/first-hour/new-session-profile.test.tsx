// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A tool with several profiles: the New session dialog offers a profile select, defaulting
// to the profile used most recently that is ready, else the first ready one. A tool with
// one profile hides the select and still carries a named account's reference.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
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
const signedIn = vi.hoisted(() => ({ refs: new Set<string>(), default: true }))
vi.mock('./api', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  signInApi: {
    status: (driver: string, _t: unknown, _s: unknown, ref?: string) =>
      Promise.resolve({
        driver,
        installed: true,
        signed_in: ref ? signedIn.refs.has(ref) : signedIn.default,
      }),
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import type {
  ProviderAccountDTO,
  ProviderProfileDTO,
  RunDTO,
} from '@/features/agentops/types'
import { providersApi } from '@/features/providers/api'
import type { ProviderRecordDTO } from '@/features/providers/types'
import { toast } from '@/components/ui/toaster'
import { useTenantStore } from '@/stores/tenant'
import { NewSessionDialog, StartSessionForm } from './first-hour'
import { firstHourKeys } from './api'
import { readinessOf } from './readiness.fixture'

function wrap(
  ui: ReactNode,
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
) {
  return {
    ...render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>),
    qc,
  }
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
  signedIn.default = true
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
  it('hides speed capabilities for a named provider-bound account', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.listAccounts).mockResolvedValue({
      items: [
        {
          ...account('ppf_b', 'claude-b'),
          auth_source: 'managed_injection',
          provider_record_ref: 'prv_bound',
        },
      ],
      has_more: false,
    })
    vi.spyOn(agentOpsApi, 'profileModels').mockResolvedValue({
      profile_ref: 'ppf_b',
      driver: 'claude',
      state: 'fresh',
      default_model: 'current-model',
      free_text: true,
      checked_at: '',
      refresh_interval_seconds: 120,
      models: [{ id: 'current-model', supports_fast_mode: true }],
    })
    wrap(<StartSessionForm />)
    await user.click(await profileSelect())
    await user.click(screen.getByRole('option', { name: 'claude-b' }))
    await waitFor(() => expect(agentOpsApi.profileModels).toHaveBeenCalled())
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Model' })).toHaveTextContent(
        'current-model',
      ),
    )
    expect(screen.queryByLabelText('Fast mode')).toBeNull()
  })
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

  // #1779 (srv17): with several sign-ins the advanced launch opened on "Select a profile";
  // it opens on the one this form shows.
  it('hands the advanced launch the sign-in the form shows', async () => {
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
      items: [run('r2', 'ppf_c'), run('r1', 'ppf_b')],
      has_more: false,
    })
    vi.spyOn(agentOpsApi, 'profileModels').mockResolvedValue({
      profile_ref: 'ppf_c',
      driver: 'claude',
      state: 'fresh',
      default_model: '',
      free_text: true,
      checked_at: '2026-10-12T10:00:00Z',
      refresh_interval_seconds: 120,
      models: [],
    })
    const onAdvanced = vi.fn()
    const user = userEvent.setup()
    wrap(<StartSessionForm onAdvanced={onAdvanced} />)
    await waitFor(async () =>
      expect(await profileSelect()).toHaveTextContent('claude-c'),
    )
    await user.click(screen.getByRole('button', { name: 'More options' }))
    await user.click(
      await screen.findByRole('button', { name: 'Advanced launch options' }),
    )
    expect(onAdvanced).toHaveBeenCalledWith({
      driver: 'claude',
      profileRef: 'ppf_c',
      model: '',
      effort: '',
    })
  })

  it('starts on a ready profile when the default login is not ready', async () => {
    signedIn.default = false
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

  it('starts on the sole adopted account when only that account is signed in', async () => {
    signedIn.default = false
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({ codex: 'own_login' }),
    )
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
      items: [{ ...account('ppf_b', 'claude-b'), home_mode: 'adopted' }],
      has_more: false,
    })
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    await user.click(await screen.findByRole('radio', { name: 'Claude Code' }))
    const start = screen.getByRole('button', { name: 'Start' })
    await waitFor(() => expect(start).toBeEnabled())
    expect(screen.queryByRole('combobox', { name: 'Profile' })).toBeNull()
    await user.click(start)
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      driver: 'claude',
      profileRef: 'ppf_b',
    })
  })

  it('refuses the sole signed-out account even when the default login is ready', async () => {
    signedIn.refs.clear()
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
      items: [{ ...account('ppf_b', 'claude-b'), home_mode: 'adopted' }],
      has_more: false,
    })
    wrap(<StartSessionForm />)
    await screen.findByRole('radio', { name: 'Claude Code' })
    await waitFor(() => expect(agentOpsApi.listAccounts).toHaveBeenCalled())
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled(),
    )
    expect(launch.launchSession).not.toHaveBeenCalled()
  })

  it('disables Start when the selected account signs out while the default stays ready', async () => {
    const { qc } = wrap(<StartSessionForm />)
    const select = await profileSelect()
    await waitFor(() => expect(select).toHaveTextContent('Default sign-in'))
    expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled()
    // Keep the user's explicit selection through the status refresh.
    const user = userEvent.setup()
    await user.click(select)
    await user.click(await screen.findByRole('option', { name: 'claude-b' }))
    await waitFor(() => expect(select).toHaveTextContent('claude-b'))
    signedIn.refs.delete('ppf_b')
    await qc.invalidateQueries({
      predicate: (q) => q.queryKey.includes('account-sign-in'),
    })
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled(),
    )
    expect(launch.launchSession).not.toHaveBeenCalled()
  })

  it('offers the signed-in account when no tool has a ready default login', async () => {
    signedIn.default = false
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(readinessOf({}))
    const user = userEvent.setup()
    wrap(<NewSessionDialog open onOpenChange={() => {}} />)
    await screen.findByRole('radio', { name: 'Claude Code' })
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled(),
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      profileRef: 'ppf_b',
    })
  })

  // #2551: with nothing ready, the New session dialog holds one sign-in card per tool.
  // "Use an API key instead" turns the card's sheet into the key field in place: the
  // page and the dialog stay (the old link reloaded and lost both).
  it('keeps the New session dialog when the sheet turns into the key field', async () => {
    signedIn.default = false
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(readinessOf({}))
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
      items: [],
      has_more: false,
    })
    const onOpenChange = vi.fn()
    const user = userEvent.setup()
    wrap(<NewSessionDialog open onOpenChange={onOpenChange} />)
    const dialog = await screen.findByRole('dialog', { name: 'New session' })
    const card = await within(dialog).findByTestId('tool-claude')
    // The sheet appears once the card's status is read.
    const useKey = await within(card).findByRole('button', {
      name: 'Use an API key instead',
    })
    // The control is a button in the sheet, not a link that reloads the page (#2551).
    expect(
      within(card).queryByRole('link', { name: 'Use an API key instead' }),
    ).not.toBeInTheDocument()
    await user.click(useKey)
    // The dialog and the card stayed: the sheet itself became the key field.
    expect(
      screen.getByRole('dialog', { name: 'New session' }),
    ).toBeInTheDocument()
    expect(onOpenChange).not.toHaveBeenCalled()
    expect(within(card).getByText('Anthropic')).toBeInTheDocument()
    const key = within(card).getByLabelText(/^API key/)
    await waitFor(() => expect(key).toHaveFocus())
  })

  // #2551 review: closing the dialog mid-write unmounted the form, retired its step-up
  // owner, and the write then finished with no toast, no refresh and no error report.
  it.each([
    ['stored', 'success'],
    ['refused', 'error'],
  ] as const)(
    'holds the New session dialog open while the key is being %s, then reports it',
    async (outcome, intent) => {
      signedIn.default = false
      vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(readinessOf({}))
      vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
        items: [],
        has_more: false,
      })
      const announced = vi.spyOn(toast, intent).mockReturnValue('test-toast')
      let settle!: () => void
      vi.spyOn(providersApi, 'create').mockImplementation(
        () =>
          new Promise<ProviderRecordDTO>((resolve, reject) => {
            settle = () =>
              outcome === 'stored'
                ? resolve({
                    provider_ref: 'prv-a',
                    kind: 'anthropic',
                    display_name: 'Anthropic',
                    state: 'active',
                  } as ProviderRecordDTO)
                : reject(new Error('upstream refused the key'))
          }),
      )
      const onOpenChange = vi.fn()
      const user = userEvent.setup()
      wrap(<NewSessionDialog open onOpenChange={onOpenChange} />)
      const dialog = await screen.findByRole('dialog', { name: 'New session' })
      const card = await within(dialog).findByTestId('tool-claude')
      await user.click(
        await within(card).findByRole('button', {
          name: 'Use an API key instead',
        }),
      )
      await user.type(within(card).getByLabelText(/^API key/), 'sk-pending')
      // Escape in the same tick as the click, before the pending write has re-rendered
      // anything (real use on the binary hit that gap): the dialog stays.
      fireEvent.click(
        within(card).getByRole('button', { name: 'Add provider' }),
      )
      fireEvent.keyDown(document.activeElement ?? document.body, {
        key: 'Escape',
      })
      expect(onOpenChange).not.toHaveBeenCalled()
      await waitFor(() => expect(settle).toBeDefined())
      // The outcome reaches the person, and the dialog closes as before. A refusal is
      // said inside the dialog, which hides the toast from screen readers (#2561).
      await act(async () => settle())
      if (outcome === 'stored') {
        await waitFor(() =>
          expect(announced).toHaveBeenCalledWith('Provider added.', undefined),
        )
      } else {
        expect(await within(card).findByRole('alert')).toHaveTextContent(
          'upstream refused the key',
        )
        expect(announced).not.toHaveBeenCalled()
      }
      await user.keyboard('{Escape}')
      await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
    },
  )

  it('waits for account discovery before enabling Start from the default login', async () => {
    let finish!: (
      answer: Awaited<ReturnType<typeof agentOpsApi.listAccounts>>,
    ) => void
    vi.spyOn(agentOpsApi, 'listAccounts').mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve
        }),
    )
    signedIn.refs.clear()
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    qc.setQueryData(
      firstHourKeys.readiness('tnt-a'),
      readinessOf({ claude: 'own_login' }),
    )
    wrap(<StartSessionForm />, qc)
    await waitFor(() => expect(agentOpsApi.listAccounts).toHaveBeenCalled())
    try {
      await screen.findByRole('button', { name: 'Change folder' })
      expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    } finally {
      finish({
        items: [{ ...account('ppf_b', 'claude-b'), home_mode: 'adopted' }],
        has_more: false,
      })
    }
    await screen.findByText(/claude-b.*not signed in/i)
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    expect(launch.launchSession).not.toHaveBeenCalled()
  })
})
