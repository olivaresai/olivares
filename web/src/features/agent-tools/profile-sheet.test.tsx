// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The profile sheet (the arrow of a row): rename a profile, switch it between its own
// account and an API key, sign it in again, remove it. Renaming needs the newer engine; an
// older one is told apart by its answer and the old name stays, said out loud.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, auth, signIn, providers } = vi.hoisted(() => ({
  api: {
    inventory: vi.fn(),
    providers: vi.fn(),
    plan: vi.fn(),
    install: vi.fn(),
    job: vi.fn(),
    detect: vi.fn(),
  },
  auth: {
    isSuperadmin: true,
    can: (_permission: string): boolean => true,
    activeTenant: 't1' as string | null,
    principal: { user_id: 'root', aal: 1 },
  },
  signIn: {
    status: vi.fn(),
    start: vi.fn(),
    get: vi.fn(),
    code: vi.fn(),
    cancel: vi.fn(),
  },
  providers: { list: vi.fn() },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentToolsApi: api,
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))
vi.mock('@/features/first-hour/api', async (orig) => ({
  ...((await orig()) as object),
  signInApi: signIn,
}))
vi.mock('@/features/providers/api', async (orig) => ({
  ...((await orig()) as object),
  providersApi: providers,
}))

import { agentOpsApi } from '@/features/agentops/api'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import { ApiError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { AgentToolsView } from './agent-tools-view'
import type { ProviderSnapshot } from './api'

const snap = (driver: string, over: Partial<ProviderSnapshot> = {}) =>
  ({
    instance: driver,
    driver,
    default: true,
    config_dir: `/home/op/.${driver}`,
    state: 'ready',
    installed: true,
    version: `${driver}-cli 1.2.3`,
    limits: [],
    models: [],
    checked_at: '2026-10-09T10:00:00Z',
    source: `${driver} --version`,
    ...over,
  }) satisfies ProviderSnapshot

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
  home_mode: 'managed',
  home_generation: 1,
  home_relative: '',
  isolation_level: 'shared',
  auth_source: 'provider_account_home',
  identity: '',
  identity_source: 'none',
  created_at: '2026-10-01T10:00:00Z',
  updated_at: '2026-10-01T10:00:00Z',
  ...over,
})

const page = <T,>(items: T[]) => ({ items, has_more: false })

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.can = () => true
  auth.isSuperadmin = true
  useTenantStore.setState({ activeTenant: 't1' })
  api.inventory.mockResolvedValue({
    drivers: ['claude', 'codex', 'grok'],
    inventory: {
      installed: [
        {
          driver: 'claude',
          version: '2.1.295',
          state: 'installed',
          executable: '/tools/claude/claude',
        },
      ],
      leftovers: [],
    },
    read_only: false,
    jobs: [],
  })
  api.providers.mockResolvedValue({
    providers: [
      snap('claude', { version: '2.1.295 (Claude Code)' }),
      snap('codex', { version: 'codex-cli 0.162.0' }),
      snap('grok', { installed: false, state: 'not_installed', version: '' }),
    ],
  })
  api.plan.mockResolvedValue({
    driver: 'grok',
    version: 'stable',
    digest: 'd1',
    verification: 'none-origin-only',
    executable: '/tools/grok/grok',
  })
  signIn.status.mockImplementation(
    (driver: string, _tenant: unknown, _signal: unknown, ref?: string) =>
      Promise.resolve({
        driver,
        installed: driver !== 'grok',
        signed_in: ref !== 'ppf_b',
      }),
  )
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
    readinessOf({ claude: 'own_login', codex: 'own_login' }),
  )
  vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
    page([
      account('ppf_b', 'claude-b'),
      account('ppf_c', 'claude-c', {
        auth_source: 'managed_injection',
        provider_record_ref: 'prv_1',
      }),
    ]),
  )
  providers.list.mockResolvedValue(
    page([
      {
        provider_ref: 'prv_1',
        kind: 'anthropic',
        display_name: 'Local stub',
        key_hint: 'sk-…7f2a',
        state: 'active',
      },
    ]),
  )
})

async function openSheet(
  user: ReturnType<typeof userEvent.setup>,
  name: string,
  id = name,
) {
  const row = await within(
    await screen.findByTestId('tool-claude'),
  ).findByTestId(`profile-${id}`)
  await user.click(
    within(row).getByRole('button', { name: new RegExp(`^${name}`) }),
  )
  return screen.findByRole('dialog', { name })
}

beforeEach(() => {
  vi.spyOn(agentOpsApi, 'patchAccountMetadata').mockImplementation(
    async (ref, body) => account(ref, body.name ?? 'claude-b'),
  )
  vi.spyOn(agentOpsApi, 'patchProfile').mockResolvedValue({} as never)
  vi.spyOn(agentOpsApi, 'retireProfile').mockResolvedValue({} as never)
})

describe('Profile sheet', () => {
  it('renames a profile in place with the new name sent to the engine', async () => {
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'claude-b')
    const name = within(sheet).getByRole('textbox', { name: 'Name' })
    expect(name).toHaveValue('claude-b')
    expect(
      within(sheet).queryByRole('button', { name: 'Save name' }),
    ).toBeNull()
    await user.clear(name)
    await user.type(name, 'work')
    await user.click(within(sheet).getByRole('button', { name: 'Save name' }))
    await waitFor(() =>
      expect(agentOpsApi.patchAccountMetadata).toHaveBeenCalledWith(
        'ppf_b',
        { name: 'work' },
        expect.objectContaining({ tenant: 't1' }),
      ),
    )
  })

  it.each([400, 422])(
    "shows the engine's own message when it answers %i, and keeps the old name",
    async (status) => {
      vi.spyOn(agentOpsApi, 'patchAccountMetadata').mockRejectedValue(
        new ApiError(status, 'bad_request', 'The name is not allowed here.'),
      )
      const user = userEvent.setup()
      mount()
      const sheet = await openSheet(user, 'claude-b')
      const name = within(sheet).getByRole('textbox', { name: 'Name' })
      await user.clear(name)
      await user.type(name, 'work')
      await user.click(within(sheet).getByRole('button', { name: 'Save name' }))
      expect(
        await within(sheet).findByText('The name is not allowed here.'),
      ).toBeInTheDocument()
      expect(
        within(sheet).queryByText('Renaming needs a newer engine.'),
      ).toBeNull()
      expect(name).toHaveValue('claude-b')
    },
  )

  it('says so when an older engine ignores the new name', async () => {
    vi.spyOn(agentOpsApi, 'patchAccountMetadata').mockResolvedValue(
      account('ppf_b', 'claude-b'),
    )
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'claude-b')
    const name = within(sheet).getByRole('textbox', { name: 'Name' })
    await user.clear(name)
    await user.type(name, 'work')
    await user.click(within(sheet).getByRole('button', { name: 'Save name' }))
    expect(
      await within(sheet).findByText('Renaming needs a newer engine.'),
    ).toBeInTheDocument()
  })

  it('says a name that is taken is taken', async () => {
    vi.spyOn(agentOpsApi, 'patchAccountMetadata').mockRejectedValue(
      new ApiError(409, 'conflict', 'That account name is taken.'),
    )
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'claude-b')
    const name = within(sheet).getByRole('textbox', { name: 'Name' })
    await user.clear(name)
    await user.type(name, 'work')
    await user.click(within(sheet).getByRole('button', { name: 'Save name' }))
    expect(
      await within(sheet).findByText('That name is already taken.'),
    ).toBeInTheDocument()
  })

  it('does not send a name the engine would refuse', async () => {
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'claude-b')
    const name = within(sheet).getByRole('textbox', { name: 'Name' })
    await user.clear(name)
    await user.type(name, 'Work Claude')
    expect(
      within(sheet).getByRole('button', { name: 'Save name' }),
    ).toBeDisabled()
    expect(
      within(sheet).getByText(
        'Use lowercase letters, digits and dashes, starting with a letter.',
      ),
    ).toBeInTheDocument()
  })

  it('switches an account profile to an API key with the existing profile update', async () => {
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'claude-b')
    expect(within(sheet).getByRole('radio', { name: 'Account' })).toBeChecked()
    await user.click(within(sheet).getByRole('radio', { name: 'API key' }))
    const save = within(sheet).getByRole('button', { name: 'Save' })
    expect(save).toBeDisabled()
    await user.selectOptions(
      within(sheet).getByRole('combobox', { name: 'API key' }),
      'Local stub (sk-…7f2a)',
    )
    await user.click(save)
    await waitFor(() =>
      expect(agentOpsApi.patchProfile).toHaveBeenCalledWith(
        'ppf_b',
        { auth_source: 'managed_injection', provider_record_ref: 'prv_1' },
        expect.anything(),
      ),
    )
  })

  it('switches a key profile back to its own account, unbinding the key', async () => {
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'claude-c')
    expect(within(sheet).getByRole('radio', { name: 'API key' })).toBeChecked()
    await user.click(within(sheet).getByRole('radio', { name: 'Account' }))
    await user.click(within(sheet).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(agentOpsApi.patchProfile).toHaveBeenCalledWith(
        'ppf_c',
        { auth_source: 'provider_account_home', provider_record_ref: '' },
        expect.anything(),
      ),
    )
  })

  it('removes a profile after a confirmation, through the retire route', async () => {
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'claude-b')
    await user.click(
      within(sheet).getByRole('button', { name: 'Remove profile' }),
    )
    expect(agentOpsApi.retireProfile).not.toHaveBeenCalled()
    const confirm = await screen.findByRole('dialog', {
      name: 'Remove claude-b?',
    })
    await user.click(
      within(confirm).getByRole('button', { name: 'Remove profile' }),
    )
    await waitFor(() =>
      expect(agentOpsApi.retireProfile).toHaveBeenCalledWith('ppf_b'),
    )
  })

  it('offers no Remove to a person who may not retire profiles', async () => {
    auth.can = (permission) => permission !== 'sessions:profile:admin'
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'claude-b')
    expect(
      within(sheet).queryByRole('button', { name: 'Remove profile' }),
    ).toBeNull()
  })

  it('shows the default login with its state and Sign in again, and nothing to rename or remove', async () => {
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'Default sign-in', 'default')
    expect(within(sheet).getByText('Account · signed in')).toBeInTheDocument()
    expect(
      within(sheet).getByRole('button', { name: 'Sign in again' }),
    ).toBeInTheDocument()
    expect(within(sheet).queryByRole('textbox', { name: 'Name' })).toBeNull()
    expect(
      within(sheet).queryByRole('button', { name: 'Remove profile' }),
    ).toBeNull()
  })
})
