// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Add profile: a name filled in with the next free one, Account or API key, and for an
// account the tool's own official sign-in continuing in the same dialog. The engine stays
// the authority on the name, and a paid build can add ways to sign in under "Other ways".
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
const { extensions } = vi.hoisted(() => ({
  extensions: { profileSignInWays: [] as unknown[] },
}))
vi.mock('@/features/extensions', async (orig) => ({
  ...((await orig()) as object),
  PANEL_EXTENSIONS: new Proxy(
    {},
    {
      get: (_t, key) => (extensions as Record<string, unknown>)[key as string],
    },
  ),
}))
vi.mock('@/features/providers/api', async (orig) => ({
  ...((await orig()) as object),
  providersApi: providers,
}))

import { agentOpsApi } from '@/features/agentops/api'
import type {
  ProviderAccountDTO,
  ProviderProfileDTO,
} from '@/features/agentops/types'
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

const namedProfile = (
  ref: string,
  accountName: string,
  state = 'active',
): ProviderProfileDTO => ({
  profile_ref: ref,
  driver: 'claude',
  environment_ref: 'xenv_1',
  state,
  local_environment: true,
  operable: true,
  auth_source: 'provider_account_home',
  account_name: accountName,
})

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
  // The profiles carry no names unless a case says so (an engine before names).
  vi.spyOn(agentOpsApi, 'listProfiles').mockResolvedValue(page([]))
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

const created = (name: string, over: Partial<ProviderAccountDTO> = {}) =>
  account('ppf_new', name, over)

async function openAdd(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByTestId('tool-claude')
  await user.click(
    await screen.findByRole('button', { name: 'Add profile to Claude Code' }),
  )
  return screen.findByRole('dialog', { name: 'Add a profile' })
}

beforeEach(() => {
  extensions.profileSignInWays = []
  vi.spyOn(agentOpsApi, 'createAccount').mockImplementation(async (body) =>
    created(body.name ?? 'claude-e'),
  )
  vi.spyOn(agentOpsApi, 'patchProfile').mockResolvedValue({} as never)
  signIn.start.mockResolvedValue({
    id: 'si1',
    driver: 'claude',
    account_ref: 'ppf_new',
    state: 'needs_code',
    url: 'https://claude.ai/oauth/authorize?x=1',
  })
  signIn.get.mockResolvedValue({
    id: 'si1',
    driver: 'claude',
    account_ref: 'ppf_new',
    state: 'needs_code',
    url: 'https://claude.ai/oauth/authorize?x=1',
  })
  signIn.status.mockImplementation(
    (driver: string, _t: unknown, _s: unknown, ref?: string) =>
      Promise.resolve({
        driver,
        installed: true,
        signed_in: ref !== 'ppf_b' && ref !== 'ppf_new',
      }),
  )
})

describe('Add profile', () => {
  it('fills the name with the first free name of the tool', async () => {
    mount()
    const user = userEvent.setup()
    const dialog = await openAdd(user)
    // claude-b and claude-c are taken: the first free name is claude. The default login does
    // not hold it (it is no profile's name).
    expect(within(dialog).getByRole('textbox', { name: 'Name' })).toHaveValue(
      'claude',
    )
    expect(
      within(dialog).getByText('Lowercase letters, digits and dashes.'),
    ).toBeInTheDocument()
    expect(within(dialog).getByRole('radio', { name: 'Account' })).toBeChecked()
    expect(
      within(dialog).getByText(
        "Uses Claude Code's own sign-in. Olivares never sees your password.",
      ),
    ).toBeInTheDocument()
  })

  it('skips a name a profile carries even when the accounts read does not list it', async () => {
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
      page([account('ppf_x', 'claude')]),
    )
    vi.spyOn(agentOpsApi, 'listProfiles').mockResolvedValue(
      page([
        namedProfile('ppf_x', 'claude'),
        // A removed profile still holds its name.
        namedProfile('ppf_r', 'claude-b', 'retired'),
      ]),
    )
    const user = userEvent.setup()
    mount()
    const dialog = await openAdd(user)
    expect(within(dialog).getByRole('textbox', { name: 'Name' })).toHaveValue(
      'claude-c',
    )
  })

  it('starts the names of a tool whose key differs from its name from its name: gemini, not gemini-cli', async () => {
    api.inventory.mockResolvedValue({
      drivers: ['gemini-cli'],
      inventory: { installed: [], leftovers: [] },
      read_only: false,
      jobs: [],
    })
    api.providers.mockResolvedValue({ providers: [snap('gemini-cli')] })
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(page([]))
    const user = userEvent.setup()
    mount()
    await user.click(
      await screen.findByRole('button', { name: 'Add profile to Gemini CLI' }),
    )
    const dialog = await screen.findByRole('dialog', { name: 'Add a profile' })
    expect(within(dialog).getByRole('textbox', { name: 'Name' })).toHaveValue(
      'gemini',
    )
    expect(
      within(dialog).getByText(
        "Uses Gemini CLI's own sign-in. Olivares never sees your password.",
      ),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('radio', { name: 'API key' }))
    expect(
      within(dialog).getByRole('combobox', { name: 'API key' }),
    ).toBeEnabled()
  })

  it("creates the account under that name and continues to the tool's official sign-in in place", async () => {
    mount()
    const user = userEvent.setup()
    const dialog = await openAdd(user)
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await waitFor(() =>
      expect(agentOpsApi.createAccount).toHaveBeenCalledWith(
        {
          driver: 'claude',
          name: 'claude',
          idempotency_key: expect.any(String),
        },
        expect.objectContaining({ tenant: 't1' }),
      ),
    )
    // The same dialog is now the sign-in of the new profile.
    const step = await screen.findByRole('dialog', { name: 'Sign in claude' })
    await waitFor(() => expect(signIn.start).toHaveBeenCalledTimes(1))
    expect(signIn.start.mock.calls[0].slice(0, 3)).toEqual([
      'claude',
      't1',
      'ppf_new',
    ])
    expect(
      await within(step).findByRole('link', { name: 'Open the sign-in page' }),
    ).toHaveAttribute('href', 'https://claude.ai/oauth/authorize?x=1')
    expect(
      within(step).getByRole('textbox', { name: 'Code from the sign-in page' }),
    ).toBeInTheDocument()
    expect(
      within(step).getByText('olivares tool login claude --account claude'),
    ).toBeInTheDocument()
  })

  it('closes when the tool says the profile is signed in', async () => {
    signIn.get.mockResolvedValue({
      id: 'si1',
      driver: 'claude',
      account_ref: 'ppf_new',
      state: 'signed_in',
    })
    mount()
    const user = userEvent.setup()
    const dialog = await openAdd(user)
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await screen.findByRole('dialog', { name: 'Sign in claude' })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull(), {
      timeout: 4000,
    })
  })

  it("takes the engine's answer when the name it was given is taken", async () => {
    const make = vi
      .spyOn(agentOpsApi, 'createAccount')
      .mockRejectedValueOnce(
        new ApiError(409, 'conflict', 'That account name is taken.'),
      )
      .mockImplementation(async () => created('claude-e'))
    mount()
    const user = userEvent.setup()
    const dialog = await openAdd(user)
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await screen.findByRole('dialog', { name: 'Sign in claude-e' })
    expect(make).toHaveBeenCalledTimes(2)
    // The second request names nothing: the engine picks, and its pick wins.
    expect(make.mock.calls[1][0]).not.toHaveProperty('name')
  })

  it('keeps a name the person typed, and says so when the engine refuses it', async () => {
    vi.spyOn(agentOpsApi, 'createAccount').mockRejectedValue(
      new ApiError(409, 'conflict', 'That account name is taken.'),
    )
    mount()
    const user = userEvent.setup()
    const dialog = await openAdd(user)
    const name = within(dialog).getByRole('textbox', { name: 'Name' })
    await user.clear(name)
    await user.type(name, 'work')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'That account name is taken.',
    )
    expect(agentOpsApi.createAccount).toHaveBeenCalledTimes(1)
  })

  it('does not create a profile with a name the engine would refuse, and says why', async () => {
    mount()
    const user = userEvent.setup()
    const dialog = await openAdd(user)
    const name = within(dialog).getByRole('textbox', { name: 'Name' })
    await user.clear(name)
    await user.type(name, 'Work Claude')
    expect(
      within(dialog).getByRole('button', { name: 'Create' }),
    ).toBeDisabled()
    expect(
      within(dialog).getByText(
        'Use lowercase letters, digits and dashes, starting with a letter.',
      ),
    ).toBeInTheDocument()
    await user.clear(name)
    await user.type(name, 'claude-b')
    expect(
      within(dialog).getByRole('button', { name: 'Create' }),
    ).toBeDisabled()
    expect(
      within(dialog).getByText('That name is already taken.'),
    ).toBeInTheDocument()
  })

  it('keeps the API key choice off, with its reason, for who may not change profiles', async () => {
    auth.can = (permission) => permission !== 'sessions:profile:write'
    const user = userEvent.setup()
    mount()
    const dialog = await openAdd(user)
    expect(
      within(dialog).getByRole('radio', { name: 'API key' }),
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      within(dialog).getByText(
        'Choosing a key needs permission to change profiles.',
      ),
    ).toBeVisible()
    // The account route still works: no orphan account is left by a half-allowed key.
    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeEnabled()
  })

  it('ties the name problem to the field it is about', async () => {
    const user = userEvent.setup()
    mount()
    const dialog = await openAdd(user)
    const name = within(dialog).getByRole('textbox', { name: 'Name' })
    await user.clear(name)
    await user.type(name, 'Work Claude')
    expect(name).toHaveAccessibleDescription(
      expect.stringContaining(
        'Use lowercase letters, digits and dashes, starting with a letter.',
      ),
    )
  })

  it('tells a way a build adds who it is for: the profile, or the default login with no name', async () => {
    extensions.profileSignInWays = [
      {
        id: 'shell',
        Component: ({ accountName }: { accountName?: string }) => (
          <span>{`way for ${accountName ?? 'the default login'}`}</span>
        ),
      },
    ]
    signIn.status.mockImplementation(
      (driver: string, _t: unknown, _s: unknown, ref?: string) =>
        Promise.resolve({
          driver,
          installed: true,
          signed_in: !!ref && ref !== 'ppf_b',
        }),
    )
    const user = userEvent.setup()
    mount()
    const row = await within(
      await screen.findByTestId('tool-claude'),
    ).findByTestId('profile-default')
    await user.click(
      await within(row).findByRole('button', { name: /^Sign in/ }),
    )
    const dialog = await screen.findByRole('dialog', {
      name: /Sign in Claude Code/,
    })
    expect(
      within(dialog).getByText('way for the default login'),
    ).toBeInTheDocument()
  })

  it('says when the tool has no official sign-in to run, and points to the terminal', async () => {
    signIn.status.mockImplementation(
      (driver: string, _t: unknown, _s: unknown, ref?: string) =>
        ref === 'ppf_new'
          ? Promise.reject(
              new ApiError(
                400,
                'bad_request',
                'Choose claude, codex, grok or opencode.',
              ),
            )
          : Promise.resolve({ driver, installed: true, signed_in: true }),
    )
    const user = userEvent.setup()
    mount()
    const dialog = await openAdd(user)
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    const step = await screen.findByRole('dialog', { name: 'Sign in claude' })
    expect(
      await within(step).findByText(
        'This tool has no official sign-in to run here. Use the terminal command under Other ways.',
      ),
    ).toBeInTheDocument()
    expect(
      within(step).getByText('olivares tool login claude --account claude'),
    ).toBeInTheDocument()
  })

  it('lets an API key be chosen instead, and binds it with the existing profile update', async () => {
    mount()
    const user = userEvent.setup()
    const dialog = await openAdd(user)
    await user.click(within(dialog).getByRole('radio', { name: 'API key' }))
    expect(
      within(dialog).getByRole('button', { name: 'Create' }),
    ).toBeDisabled()
    await user.selectOptions(
      within(dialog).getByRole('combobox', { name: 'API key' }),
      'Local stub (sk-…7f2a)',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await waitFor(() =>
      expect(agentOpsApi.patchProfile).toHaveBeenCalledWith(
        'ppf_new',
        { auth_source: 'managed_injection', provider_record_ref: 'prv_1' },
        expect.anything(),
      ),
    )
    // No official sign-in for a key: the dialog closes.
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(signIn.start).not.toHaveBeenCalled()
  })

  it('offers Add a key… and opens the provider form on the key this tool runs on', async () => {
    mount()
    const user = userEvent.setup()
    const dialog = await openAdd(user)
    await user.click(within(dialog).getByRole('radio', { name: 'API key' }))
    await user.selectOptions(
      within(dialog).getByRole('combobox', { name: 'API key' }),
      'Add a key…',
    )
    expect(
      await screen.findByRole('dialog', { name: 'Add a provider' }),
    ).toBeInTheDocument()
  })

  it('shows a way to sign in that a paid build adds, and none by default', async () => {
    mount()
    const user = userEvent.setup()
    const dialog = await openAdd(user)
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await screen.findByRole('dialog', { name: 'Sign in claude' })
    expect(screen.queryByText('Sign in from the shell')).toBeNull()
  })

  it('renders the ways a build adds under Other ways', async () => {
    extensions.profileSignInWays = [
      {
        id: 'shell',
        Component: ({ accountName }: { accountName: string }) => (
          <span>{`Sign in from the shell: ${accountName}`}</span>
        ),
      },
    ]
    mount()
    const user = userEvent.setup()
    const dialog = await openAdd(user)
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    const step = await screen.findByRole('dialog', { name: 'Sign in claude' })
    expect(
      within(step).getByText('Sign in from the shell: claude'),
    ).toBeInTheDocument()
  })
})
