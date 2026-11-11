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
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, auth, signIn, providers } = vi.hoisted(() => ({
  api: {
    inventory: vi.fn(),
    pins: vi.fn().mockResolvedValue({ pins: [] }),
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
import { AccountBadge, AccountMarksProvider } from './account-marks'
import { AgentToolsView } from './agent-tools-view'
import type { ProviderSnapshot } from './api'

vi.mock('@tanstack/react-router', async () =>
  (await import('@/test/fake-router')).fakeRouterModule(),
)

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

function mount(elsewhere?: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
      {elsewhere}
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

  it('edits the display name and color in place, and the row wears them with no reload', async () => {
    // The engine keeps what it was sent; the list and the single read answer from it.
    let stored = account('ppf_b', 'claude-b')
    vi.spyOn(agentOpsApi, 'listAccounts').mockImplementation(async () =>
      page([stored, account('ppf_c', 'claude-c')]),
    )
    vi.spyOn(agentOpsApi, 'getAccount').mockImplementation(async () => stored)
    vi.spyOn(agentOpsApi, 'patchAccountMetadata').mockImplementation(
      async (_ref, body) => {
        stored = {
          ...stored,
          ...(body.display_name !== undefined && {
            display_name: body.display_name,
          }),
          ...(body.accent !== undefined && { accent: body.accent }),
        }
        return stored
      },
    )
    const user = userEvent.setup()
    // A badge elsewhere in the console (Sessions, the header, Now): the same component.
    mount(
      <AccountMarksProvider>
        <div data-testid="elsewhere">
          <AccountBadge profileRef="ppf_b" />
        </div>
      </AccountMarksProvider>,
    )
    const sheet = await openSheet(user, 'claude-b')
    await user.click(
      within(sheet).getByRole('button', { name: 'Edit label and color' }),
    )
    await user.type(
      await within(sheet).findByRole('textbox', { name: 'Display name' }),
      'Research',
    )
    await user.selectOptions(
      within(sheet).getByRole('combobox', { name: 'Color' }),
      'purple',
    )
    await user.click(within(sheet).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(agentOpsApi.patchAccountMetadata).toHaveBeenCalledWith(
        'ppf_b',
        { display_name: 'Research', accent: 'purple' },
        expect.objectContaining({ tenant: 't1' }),
      ),
    )
    const row = within(await screen.findByTestId('tool-claude')).getByTestId(
      'profile-claude-b',
    )
    await waitFor(() =>
      expect(
        within(row).getByText('Research', { ignore: '.sr-only' }),
      ).toBeTruthy(),
    )
    expect(row.querySelector('[data-provider-accent="purple"]')).not.toBeNull()
    const elsewhere = screen.getByTestId('elsewhere')
    await waitFor(() =>
      expect(within(elsewhere).getByText('Research')).toBeTruthy(),
    )
    expect(
      elsewhere.querySelector('[data-provider-accent="purple"]'),
    ).not.toBeNull()
  })

  it('offers the new colors of the palette in the editor', async () => {
    vi.spyOn(agentOpsApi, 'getAccount').mockResolvedValue(
      account('ppf_b', 'claude-b'),
    )
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'claude-b')
    await user.click(
      within(sheet).getByRole('button', { name: 'Edit label and color' }),
    )
    const color = await within(sheet).findByRole('combobox', { name: 'Color' })
    expect(
      within(color)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual([
      'Default',
      'Orange',
      'Green',
      'Amber',
      'Red',
      'Blue',
      'Purple',
      'Pink',
      'Teal',
      'Slate',
    ])
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

  it('switches a profile off and on from its row, and a switched-off profile stays listed', async () => {
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
      page([
        account('ppf_b', 'claude-b'),
        account('ppf_d', 'claude-d', { state: 'disabled' }),
      ]),
    )
    const user = userEvent.setup()
    mount()
    const claude = await screen.findByTestId('tool-claude')
    const on = await within(claude).findByRole('switch', {
      name: 'Turn off claude-b',
    })
    expect(on).toBeChecked()
    await user.click(on)
    await waitFor(() =>
      expect(agentOpsApi.patchProfile).toHaveBeenCalledWith(
        'ppf_b',
        { state: 'disabled' },
        expect.anything(),
      ),
    )
    const off = within(claude).getByRole('switch', { name: 'Turn on claude-d' })
    expect(off).not.toBeChecked()
    await user.click(off)
    await waitFor(() =>
      expect(agentOpsApi.patchProfile).toHaveBeenCalledWith(
        'ppf_d',
        { state: 'active' },
        expect.anything(),
      ),
    )
  })

  it("manages a profile's models: hide one, move one, add one the tool does not list", async () => {
    // The list is the person's preference: it needs a user principal to be kept.
    const principal = auth.principal
    auth.principal = { kind: 'user', user_id: 'root', aal: 1 } as never
    try {
      vi.spyOn(agentOpsApi, 'profileModels').mockResolvedValue({
        profile_ref: 'ppf_b',
        driver: 'claude',
        state: 'fresh',
        models: [
          {
            id: 'claude-opus-5-5',
            display_name: 'Claude Opus 5.5',
            supports_fast_mode: true,
          },
          { id: 'claude-sonnet-5-5', display_name: 'Claude Sonnet 5.5' },
        ],
        default_model: '',
        free_text: true,
        checked_at: '2026-11-09T10:00:00Z',
        refresh_interval_seconds: 120,
      })
      const user = userEvent.setup()
      mount()
      const sheet = await openSheet(user, 'claude-b')
      const models = await within(sheet).findByRole('region', {
        name: 'Models',
      })
      expect(within(models).getByText('Fast mode')).toBeInTheDocument()
      const order = () =>
        within(models)
          .getAllByRole('switch')
          .map((s) => s.getAttribute('aria-label'))

      await user.click(
        within(models).getByRole('switch', { name: 'Show Claude Sonnet 5.5' }),
      )
      expect(
        within(models).getByRole('switch', { name: 'Show Claude Sonnet 5.5' }),
      ).not.toBeChecked()

      await user.click(
        within(models).getByRole('button', {
          name: 'Move Claude Sonnet 5.5 up',
        }),
      )
      expect(order()).toEqual([
        'Show Claude Sonnet 5.5',
        'Show Claude Opus 5.5',
      ])

      await user.type(
        within(models).getByRole('textbox', { name: 'Add custom model' }),
        'my-model',
      )
      await user.click(within(models).getByRole('button', { name: 'Add' }))
      expect(
        within(models).getByRole('switch', { name: 'Show My Model' }),
      ).toBeChecked()
      await user.click(
        within(models).getByRole('button', { name: 'Remove My Model' }),
      )
      expect(
        within(models).queryByRole('switch', { name: 'Show My Model' }),
      ).toBeNull()
    } finally {
      auth.principal = principal
    }
  })

  it("sets a Claude profile's auto-compact window, refusing a value outside the tool's range", async () => {
    const user = userEvent.setup()
    mount()
    const sheet = await openSheet(user, 'claude-b')
    const runtime = await within(sheet).findByRole('region', {
      name: 'Runtime',
    })
    const field = within(runtime).getByRole('textbox', {
      name: 'Auto-compact after',
    })
    await user.type(field, '5000')
    expect(
      within(runtime).getByText(
        'Enter a whole number from 100000 to 1000000, or leave it empty.',
      ),
    ).toBeInTheDocument()
    expect(within(runtime).getByRole('button', { name: 'Save' })).toBeDisabled()
    await user.clear(field)
    await user.type(field, '250000')
    await user.click(within(runtime).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(agentOpsApi.patchProfile).toHaveBeenCalledWith(
        'ppf_b',
        { auto_compact_window: 250000 },
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
