// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// AI tools, one boxed list per installed tool: a row per profile (a provider account), the
// default login as the first profile, tools that are not installed on one line with an
// Install menu, and the install details behind the tool's menu. The page names no tool of
// its own: the tools are whatever the engine's inventory lists.
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
    can: (_permission: string) => true,
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

// A profile as the engine lists it. `account_name` is the name of a profile that is an
// account; the default login is the profile without one.
const profile = (
  ref: string,
  accountName?: string,
  over: Partial<ProviderProfileDTO> = {},
): ProviderProfileDTO => ({
  profile_ref: ref,
  driver: 'claude',
  environment_ref: 'xenv_1',
  state: 'active',
  local_environment: true,
  operable: true,
  auth_source: 'provider_account_home',
  ...(accountName ? { account_name: accountName } : {}),
  ...over,
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

it('lets the profile list finish before starting optional provider probes', async () => {
  const accounts = page([account('ppf_b', 'claude-b')])
  let finish!: (value: typeof accounts) => void
  vi.spyOn(agentOpsApi, 'listAccounts').mockReturnValueOnce(
    new Promise<typeof accounts>((resolve) => {
      finish = resolve
    }),
  )
  mount()
  try {
    const claude = await screen.findByTestId('tool-claude')
    await within(claude).findByText('Account · signed in')
    expect(api.providers).not.toHaveBeenCalled()
  } finally {
    finish(accounts)
  }
  await screen.findByTestId('profile-claude-b')
  await waitFor(() => expect(api.providers).toHaveBeenCalledTimes(1))
})

it('lets named profile native sign-in finish before starting optional provider probes', async () => {
  const status = { driver: 'claude', installed: true, signed_in: false }
  let finish!: (value: typeof status) => void
  signIn.status.mockImplementation(
    (driver: string, _tenant: unknown, _signal: unknown, ref?: string) =>
      ref === 'ppf_b'
        ? new Promise<typeof status>((resolve) => {
            finish = resolve
          })
        : Promise.resolve({ driver, installed: true, signed_in: true }),
  )
  mount()
  try {
    await waitFor(() =>
      expect(signIn.status).toHaveBeenCalledWith(
        'claude',
        't1',
        expect.anything(),
        'ppf_b',
        expect.anything(),
      ),
    )
    expect(api.providers).not.toHaveBeenCalled()
  } finally {
    finish(status)
  }
  await waitFor(() => expect(api.providers).toHaveBeenCalledTimes(1))
})

it('keeps optional probes behind native status created by a completed profile list', async () => {
  const accounts = page([account('ppf_b', 'claude-b')])
  const status = { driver: 'claude', installed: true, signed_in: false }
  let finishList!: (value: typeof accounts) => void
  let finishStatus!: (value: typeof status) => void
  vi.spyOn(agentOpsApi, 'listAccounts').mockReturnValueOnce(
    new Promise<typeof accounts>((resolve) => {
      finishList = resolve
    }),
  )
  signIn.status.mockImplementation(
    (driver: string, _tenant: unknown, _signal: unknown, ref?: string) =>
      ref === 'ppf_b'
        ? new Promise<typeof status>((resolve) => {
            finishStatus = resolve
          })
        : Promise.resolve({ driver, installed: true, signed_in: true }),
  )
  mount()
  try {
    const claude = await screen.findByTestId('tool-claude')
    await within(claude).findByText('Account · signed in')
    expect(api.providers).not.toHaveBeenCalled()
    finishList(accounts)
    await waitFor(() =>
      expect(signIn.status).toHaveBeenCalledWith(
        'claude',
        't1',
        expect.anything(),
        'ppf_b',
        expect.anything(),
      ),
    )
    expect(api.providers).not.toHaveBeenCalled()
  } finally {
    finishList(accounts)
    finishStatus?.(status)
  }
  await waitFor(() => expect(api.providers).toHaveBeenCalledTimes(1))
  expect(agentOpsApi.listAccounts).toHaveBeenCalledTimes(1)
})

it('keeps a completed absence answer visible while provider details refresh', async () => {
  api.providers.mockResolvedValue({
    providers: [
      snap('claude'),
      snap('grok', { installed: false, state: 'not_installed' }),
    ],
    refreshing: true,
  })
  mount()
  expect(await screen.findByTestId('not-installed')).toHaveTextContent(
    'Not installed: Grok Build',
  )
  expect(screen.getByRole('button', { name: 'Install' })).toBeEnabled()
})

it('keeps host presence unknown while a cold provider snapshot is refreshing', async () => {
  api.providers.mockResolvedValueOnce({ providers: [], refreshing: true })
  mount()
  await waitFor(() => expect(api.providers).toHaveBeenCalledTimes(1))
  expect(screen.getByTestId('tool-claude')).toBeInTheDocument()
  expect(screen.queryByTestId('not-installed')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Install' }),
  ).not.toBeInTheDocument()
  expect(await screen.findByTestId('tool-codex')).toBeInTheDocument()
  expect(await screen.findByTestId('not-installed')).toHaveTextContent(
    'Not installed: Grok Build',
  )
})

describe('AI tools: one boxed list per installed tool', () => {
  it('lists a row per profile with how it signs in and one state', async () => {
    mount()
    const claude = await screen.findByTestId('tool-claude')
    expect(
      within(claude).getByRole('heading', { name: 'Claude Code' }),
    ).toBeInTheDocument()
    expect(within(claude).getByText('2.1.295')).toBeInTheDocument()
    // The tenant's default login first, then the named profiles.
    const default_ = await within(claude).findByTestId('profile-default')
    expect(
      within(default_).getByText('Account · signed in'),
    ).toBeInTheDocument()
    expect(within(default_).getByText('Ready')).toBeInTheDocument()
    const second = await within(claude).findByTestId('profile-claude-b')
    expect(
      await within(second).findByText('Account · not signed in'),
    ).toBeInTheDocument()
    expect(
      within(second).getByRole('button', { name: /^Sign in/ }),
    ).toBeInTheDocument()
    const third = await within(claude).findByTestId('profile-claude-c')
    expect(
      within(third).getByText('API key · Local stub (sk-…7f2a)'),
    ).toBeInTheDocument()
    expect(within(third).getByText('Ready')).toBeInTheDocument()
  })

  it('shows the default login as the first profile of a tool that has no named one', async () => {
    mount()
    const codex = await screen.findByTestId('tool-codex')
    expect(
      await within(codex).findByTestId('profile-default'),
    ).toHaveTextContent('Default sign-in')
    expect(within(codex).getByText('0.162.0')).toBeInTheDocument()
  })

  it('tells the default login from the profile names the engine gives: the one without a name', async () => {
    // The profile without an account name is the default login, even when a named profile
    // sits on an adopted home: the shape of the account no longer decides.
    vi.spyOn(agentOpsApi, 'listProfiles').mockResolvedValue(
      page([
        profile('ppf_d'),
        profile('ppf_x', 'claude'),
        profile('ppf_b', 'claude-b'),
      ]),
    )
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
      page([
        account('ppf_x', 'claude', { home_mode: 'adopted' }),
        account('ppf_b', 'claude-b'),
      ]),
    )
    mount()
    const claude = await screen.findByTestId('tool-claude')
    await waitFor(() =>
      expect(
        within(claude)
          .getAllByTestId(/^profile-/)
          .map((row) => row.getAttribute('data-testid')),
      ).toEqual(['profile-default', 'profile-claude', 'profile-claude-b']),
    )
    expect(within(claude).getByTestId('profile-default')).toHaveTextContent(
      'Default sign-in',
    )
  })

  it('keeps the shape rule when no profile carries a name, an engine before names', async () => {
    vi.spyOn(agentOpsApi, 'listProfiles').mockResolvedValue(
      page([profile('ppf_d'), profile('ppf_b')]),
    )
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
      page([account('ppf_b', 'claude', { home_mode: 'adopted' })]),
    )
    mount()
    const claude = await screen.findByTestId('tool-claude')
    expect(
      await within(claude).findByText('Account · not signed in'),
    ).toBeInTheDocument()
    expect(within(claude).getAllByTestId('profile-claude')).toHaveLength(1)
    expect(within(claude).queryByTestId('profile-default')).toBeNull()
  })

  it('lists the default login once when a named profile adopted it', async () => {
    // Its sign-in state differs from the default login's: the row that settles is the
    // named profile's own.
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
      page([account('ppf_b', 'claude', { home_mode: 'adopted' })]),
    )
    mount()
    const claude = await screen.findByTestId('tool-claude')
    expect(
      await within(claude).findByText('Account · not signed in'),
    ).toBeInTheDocument()
    expect(within(claude).getAllByTestId('profile-claude')).toHaveLength(1)
  })

  it("names the default login by what it is, so it never repeats a profile's name", async () => {
    // Two named profiles that happen to be called claude and claude-b, and a default login
    // that is signed out: three rows, three different names.
    signIn.status.mockImplementation(
      (driver: string, _t: unknown, _s: unknown, ref?: string) =>
        Promise.resolve({ driver, installed: true, signed_in: !!ref }),
    )
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
      page([account('ppf_x', 'claude'), account('ppf_b', 'claude-b')]),
    )
    mount()
    const claude = await screen.findByTestId('tool-claude')
    await waitFor(() =>
      expect(
        within(claude)
          .getAllByTestId(/^profile-/)
          .map((row) => row.getAttribute('data-testid')),
      ).toEqual(['profile-default', 'profile-claude', 'profile-claude-b']),
    )
    const names = within(claude)
      .getAllByTestId(/^profile-/)
      .map((row) => within(row).getAllByRole('button')[0].textContent)
    expect(names[0]).toMatch(/^Default sign-in/)
    expect(new Set(names).size).toBe(3)
    expect(names[1]).toMatch(/^claude/)
    expect(names[1]).not.toMatch(/Default/)
  })

  it('says in one quiet line when the profiles could not be read, with Retry', async () => {
    const read = vi
      .spyOn(agentOpsApi, 'listAccounts')
      .mockRejectedValueOnce(new ApiError(500, 'internal', 'boom'))
    mount()
    const note = await screen.findByText('The profiles could not be read.')
    expect(note.closest('[role="status"]')).not.toBeNull()
    expect(screen.getAllByText('The profiles could not be read.')).toHaveLength(
      1,
    )
    read.mockResolvedValue(page([account('ppf_b', 'claude-b')]))
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByTestId('profile-claude-b')).toBeInTheDocument()
    expect(screen.queryByText('The profiles could not be read.')).toBeNull()
  })

  it('says when there are more profiles than the page could read', async () => {
    const read = vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
      items: [account('ppf_b', 'claude-b')],
      has_more: true,
      cursor: 'next',
    })
    mount()
    expect(
      await screen.findByText('Some profiles are not shown.'),
    ).toBeInTheDocument()
    // It stops at a bound instead of reading for ever.
    expect(read.mock.calls.length).toBeLessThanOrEqual(25)
  })

  it('lists active profiles only, the same set the New session dialog counts', async () => {
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
      page([
        account('ppf_b', 'claude-b'),
        account('ppf_d', 'claude-d', { state: 'disabled' }),
        account('ppf_r', 'claude-r', { state: 'retired' }),
      ]),
    )
    mount()
    expect(await screen.findByTestId('profile-claude-b')).toBeInTheDocument()
    expect(screen.queryByTestId('profile-claude-d')).toBeNull()
    expect(screen.queryByTestId('profile-claude-r')).toBeNull()
  })

  it('puts the arrow last in a row, after the state and the Sign in button', async () => {
    mount()
    const row = await screen.findByTestId('profile-claude-b')
    await within(row).findByRole('button', { name: /^Sign in/ })
    expect(row.lastElementChild).toHaveAttribute('data-slot', 'row-arrow')
    expect(
      within(row)
        .getAllByRole('button')
        .map((b) => b.textContent),
    ).toEqual([
      expect.stringMatching(/^claude-b/),
      expect.stringMatching(/^Sign in/),
    ])
  })

  it('puts a tool that is not installed on one line with an Install menu, and plans its install', async () => {
    mount()
    expect(await screen.findByTestId('not-installed')).toHaveTextContent(
      'Not installed: Grok Build',
    )
    expect(screen.queryByTestId('tool-grok')).toBeNull()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /^Install/ }))
    await user.click(
      await screen.findByRole('menuitem', { name: 'Install Grok Build' }),
    )
    await waitFor(() =>
      expect(api.plan.mock.calls[0]).toEqual(['grok', 'stable']),
    )
    expect(
      await screen.findByRole('button', { name: 'Install approved version' }),
    ).toBeInTheDocument()
  })

  it('keeps paths, the probe command and the install log out of the page', async () => {
    api.job.mockResolvedValue({
      id: 'job1',
      state: 'succeeded',
      driver: 'claude',
      version: '2.1.295',
      progress: 'fetched 162956804 bytes sha256 4f57',
    })
    api.inventory.mockResolvedValue({
      drivers: ['claude'],
      inventory: { installed: [], leftovers: [] },
      read_only: false,
      jobs: [{ id: 'job1' }],
    })
    mount()
    await screen.findByTestId('tool-claude')
    expect(await screen.findByText(/Installation complete/)).toBeInTheDocument()
    expect(screen.queryByText('/home/op/.claude')).toBeNull()
    expect(screen.queryByText(/claude --version/)).toBeNull()
    expect(screen.queryByText(/fetched 162956804/)).toBeNull()
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Options for Claude Code' }),
    )
    await user.click(
      await screen.findByRole('menuitem', { name: 'Install details' }),
    )
    const sheet = await screen.findByRole('dialog', {
      name: /Claude Code/,
    })
    expect(within(sheet).getByText('/home/op/.claude')).toBeInTheDocument()
    expect(within(sheet).getByText(/fetched 162956804/)).toBeInTheDocument()
    expect(
      within(sheet).getByRole('textbox', { name: 'Version for Claude Code' }),
    ).toBeInTheDocument()
  })

  it('offers Update, Install details and the detailed profiles page from the tool menu', async () => {
    api.plan.mockResolvedValue({
      driver: 'claude',
      version: '2.1.300',
      digest: 'd2',
      verification: 'openpgp',
      executable: '/tools/claude/claude',
    })
    mount()
    await screen.findByTestId('tool-claude')
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Options for Claude Code' }),
    )
    expect(
      await screen.findByRole('menuitem', { name: 'Install details' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('menuitem', { name: 'Detailed profiles page' }),
    ).toHaveAttribute('href', '/provider-accounts')
    await user.click(screen.getByRole('menuitem', { name: 'Update' }))
    await waitFor(() =>
      expect(api.plan.mock.calls[0]).toEqual(['claude', 'latest']),
    )
  })

  it('names no tool of its own: a tool the engine lists is a box, with its profiles', async () => {
    api.inventory.mockResolvedValue({
      drivers: ['acme-agent'],
      inventory: { installed: [], leftovers: [] },
      read_only: false,
      jobs: [],
    })
    api.providers.mockResolvedValue({ providers: [snap('acme-agent')] })
    signIn.status.mockRejectedValue(
      new ApiError(
        400,
        'bad_request',
        'Choose claude, codex, grok or opencode.',
      ),
    )
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue(
      page([
        account('ppf_g', 'acme-agent', {
          driver: 'acme-agent',
          auth_source: 'managed_injection',
          provider_record_ref: 'prv_1',
        }),
      ]),
    )
    mount()
    const box = await screen.findByTestId('tool-acme-agent')
    expect(
      within(box).getByRole('heading', { name: 'acme-agent' }),
    ).toBeInTheDocument()
    // A tool with no login of its own gets no default-login row: one row, the profile.
    await waitFor(() =>
      expect(within(box).getAllByTestId('profile-acme-agent')).toHaveLength(1),
    )
    expect(
      await within(box).findByText('API key · Local stub (sk-…7f2a)'),
    ).toBeInTheDocument()
  })
})

describe('AI tools: a profile row', () => {
  it('opens the profile sheet from the arrow, with its reference under Details', async () => {
    mount()
    const row = await screen.findByTestId('profile-claude-b')
    const user = userEvent.setup()
    await user.click(within(row).getByRole('button', { name: /^claude-b/ }))
    const sheet = await screen.findByRole('dialog', { name: 'claude-b' })
    expect(within(sheet).getByText('Claude Code')).toBeInTheDocument()
    await user.click(within(sheet).getByText('Details'))
    expect(within(sheet).getByText('ppf_b')).toBeInTheDocument()
  })

  it("starts the tool's own sign-in in the profile's home from the Sign in button", async () => {
    signIn.start.mockResolvedValue({
      id: 'si1',
      driver: 'claude',
      account_ref: 'ppf_b',
      state: 'needs_code',
      url: 'https://claude.ai/oauth/authorize?x=1',
    })
    signIn.get.mockResolvedValue({
      id: 'si1',
      driver: 'claude',
      account_ref: 'ppf_b',
      state: 'needs_code',
      url: 'https://claude.ai/oauth/authorize?x=1',
    })
    mount()
    const row = await screen.findByTestId('profile-claude-b')
    const user = userEvent.setup()
    await user.click(
      await within(row).findByRole('button', { name: /^Sign in/ }),
    )
    const dialog = await screen.findByRole('dialog', {
      name: /Sign in claude-b/,
    })
    await waitFor(() => expect(signIn.start).toHaveBeenCalledTimes(1))
    expect(signIn.start.mock.calls[0].slice(0, 3)).toEqual([
      'claude',
      't1',
      'ppf_b',
    ])
    expect(
      await within(dialog).findByRole('link', {
        name: 'Open the sign-in page',
      }),
    ).toHaveAttribute('href', 'https://claude.ai/oauth/authorize?x=1')
  })
})
