// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The install side of AI tools: review and approve an install, Detect and probe, the
// verification strength, read-only and busy reasons. Each tool's install form lives in its
// Install details sheet, behind the tool's menu (the profile rows are in
// agent-tools-profiles.test.tsx).
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import './i18n'
const { api, auth, toast } = vi.hoisted(() => ({
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
    // These cases are about the install forms: the role reads no accounts or keys.
    can: () => false,
    activeTenant: null,
    principal: { user_id: 'root' },
  },
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentToolsApi: api,
}))
vi.mock('@/components/ui/toaster', () => ({ toast }))
// The profile rows read each tool's own sign-in status: Claude Code and Codex answer
// "installed, signed in".
const { signIn } = vi.hoisted(() => ({
  signIn: {
    status: vi.fn((driver: string) =>
      Promise.resolve({ driver, installed: true, signed_in: true }),
    ),
  },
}))
vi.mock('@/features/first-hour/api', async (orig) => ({
  ...((await orig()) as object),
  signInApi: signIn,
}))
import { agentOpsApi } from '@/features/agentops/api'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import { ApiError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { AgentToolsView } from './agent-tools-view'
import type { ProviderSnapshot } from './api'

const snap = (driver: string): ProviderSnapshot => ({
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
})

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
    </QueryClientProvider>,
  )
}

/** The tool's menu, then Install details: the sheet with its version form and Detect. */
async function openDetails(
  user: ReturnType<typeof userEvent.setup>,
  name: string,
) {
  await user.click(
    await screen.findByRole('button', { name: `Options for ${name}` }),
  )
  await user.click(
    await screen.findByRole('menuitem', { name: 'Install details' }),
  )
  return screen.findByRole('dialog', { name: new RegExp(name) })
}
it('lets native readiness finish before starting optional provider probes', async () => {
  const ready = readinessOf({ claude: 'own_login', codex: 'own_login' })
  let finish!: (value: typeof ready) => void
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockReturnValueOnce(
    new Promise<typeof ready>((resolve) => {
      finish = resolve
    }),
  )
  api.inventory.mockResolvedValue({
    drivers: ['claude', 'codex', 'grok'],
    inventory: {
      installed: [
        {
          driver: 'claude',
          version: '2.1.261',
          state: 'installed',
          executable: '/tools/claude/claude',
        },
      ],
      leftovers: [],
    },
    read_only: false,
    jobs: [],
  })
  mount()
  try {
    await waitFor(() => expect(signIn.status).toHaveBeenCalled())
    expect(api.providers).not.toHaveBeenCalled()
  } finally {
    finish(ready)
  }
  await waitFor(() => expect(api.providers).toHaveBeenCalledTimes(1))
})

it('lets the first native sign-in read finish before starting optional provider probes', async () => {
  const status = { driver: 'claude', installed: true, signed_in: true }
  let finish!: (value: typeof status) => void
  signIn.status.mockImplementation((driver: string) =>
    driver === 'claude'
      ? new Promise<typeof status>((resolve) => {
          finish = resolve
        })
      : Promise.resolve({
          driver,
          installed: true,
          signed_in: true,
        }),
  )
  api.inventory.mockResolvedValue({
    drivers: ['claude', 'codex', 'grok'],
    inventory: {
      installed: [
        {
          driver: 'claude',
          version: '2.1.261',
          state: 'installed',
          executable: '/tools/claude/claude',
        },
      ],
      leftovers: [],
    },
    read_only: false,
    jobs: [],
  })
  mount()
  try {
    await waitFor(() =>
      expect(signIn.status).toHaveBeenCalledWith(
        'claude',
        't1',
        expect.anything(),
      ),
    )
    expect(api.providers).not.toHaveBeenCalled()
  } finally {
    finish(status)
  }
  await waitFor(() => expect(api.providers).toHaveBeenCalledTimes(1))
})

it('keeps tool actions available while provider details refresh and fetches the completed snapshot', async () => {
  api.providers.mockResolvedValueOnce({ providers: [], refreshing: true })
  api.inventory.mockResolvedValue({
    drivers: ['claude'],
    inventory: {
      installed: [
        {
          driver: 'claude',
          version: '2.1.261',
          state: 'installed',
          executable: '/tools/claude/claude',
        },
      ],
      leftovers: [],
    },
    read_only: false,
    jobs: [],
  })
  mount()
  expect(
    await screen.findByRole('button', { name: 'Options for Claude Code' }),
  ).toBeEnabled()
  await waitFor(() => expect(api.providers).toHaveBeenCalledTimes(2), {
    timeout: 2500,
  })
})

beforeEach(() => {
  vi.clearAllMocks()
  auth.isSuperadmin = true
  useTenantStore.setState({ activeTenant: 't1' })
  signIn.status.mockImplementation((driver: string) =>
    Promise.resolve({ driver, installed: true, signed_in: true }),
  )
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
    readinessOf({ claude: 'own_login', codex: 'own_login' }),
  )
  api.inventory.mockResolvedValue({
    drivers: ['claude', 'codex', 'grok'],
    inventory: { installed: [], leftovers: [] },
    read_only: false,
    jobs: [],
  })
  // Claude Code and Codex are on this server; Grok Build is not.
  api.providers.mockResolvedValue({
    providers: [snap('claude'), snap('codex')],
  })
  api.plan.mockResolvedValue({
    driver: 'claude',
    version: '2.1.261',
    digest: 'approved-digest',
    verification: 'openpgp',
    executable: '/tools/claude/claude',
  })
  api.install.mockResolvedValue({
    id: 'job1',
    state: 'running',
    driver: 'claude',
    version: '2.1.261',
    progress: 'downloading',
  })
  api.job.mockResolvedValue({
    id: 'job1',
    state: 'succeeded',
    driver: 'claude',
    version: '2.1.261',
    progress: 'placed',
  })
})
it('is headed AI tools, as the navigation names it', async () => {
  mount()
  expect(
    await screen.findByRole('heading', { name: 'AI tools' }),
  ).toBeInTheDocument()
})
it('requires a system administrator without reading host inventory', async () => {
  auth.isSuperadmin = false
  mount()
  expect(await screen.findByText(/system administrator/i)).toBeInTheDocument()
  expect(api.inventory).not.toHaveBeenCalled()
  expect(api.providers).not.toHaveBeenCalled()
})
it('previews the verified version before submitting the exact approval and reports the outcome', async () => {
  mount()
  const user = userEvent.setup()
  await openDetails(user, 'Claude Code')
  await user.click(
    await screen.findByRole('button', { name: /review claude code/i }),
  )
  // The review is on the page: the sheet closed to show it.
  expect(await screen.findByText('2.1.261')).toBeInTheDocument()
  expect(api.install).not.toHaveBeenCalled()
  await user.click(
    screen.getByRole('button', { name: /install approved version/i }),
  )
  await waitFor(() =>
    expect(api.install).toHaveBeenCalledWith(
      expect.objectContaining({
        plan_digest: 'approved-digest',
        request_id: expect.any(String),
      }),
      expect.any(Object),
    ),
  )
  expect(await screen.findByText(/installation complete/i)).toBeInTheDocument()
})
it('never offers installation after planning fails', async () => {
  api.plan.mockRejectedValue(new Error('Signature verifier unavailable'))
  mount()
  const user = userEvent.setup()
  await openDetails(user, 'Claude Code')
  await user.click(
    await screen.findByRole('button', { name: /review claude code/i }),
  )
  expect(
    await screen.findByText('Signature verifier unavailable'),
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /install approved version/i }),
  ).not.toBeInTheDocument()
})
// HU2-28: the release lookup failed for an hour (GitHub's limit for this network) and the page
// offered no way to try again but to start over.
it('offers Retry after planning fails, and plans the same version again', async () => {
  api.plan.mockRejectedValueOnce(new Error('GitHub answered HTTP 403'))
  mount()
  const user = userEvent.setup()
  await openDetails(user, 'Claude Code')
  await user.click(
    await screen.findByRole('button', { name: /review claude code/i }),
  )
  await screen.findByText('GitHub answered HTTP 403')
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByRole('button', { name: /install approved version/i }),
  ).toBeInTheDocument()
  expect(api.plan).toHaveBeenCalledTimes(2)
  expect(api.plan.mock.calls[1]).toEqual(api.plan.mock.calls[0])
})
it('keeps the same request ID when retrying an ambiguous install response', async () => {
  api.install
    .mockRejectedValueOnce(new Error('Connection lost'))
    .mockResolvedValue({ id: 'job1', state: 'succeeded' })
  mount()
  const user = userEvent.setup()
  await openDetails(user, 'Claude Code')
  await user.click(
    await screen.findByRole('button', { name: /review claude code/i }),
  )
  await user.click(
    await screen.findByRole('button', { name: /install approved version/i }),
  )
  await screen.findByText('Connection lost')
  await user.click(
    screen.getByRole('button', { name: /install approved version/i }),
  )
  await waitFor(() => expect(api.install).toHaveBeenCalledTimes(2))
  expect(api.install.mock.calls[0][0]).toEqual(api.install.mock.calls[1][0])
})

it('states the verification strength of an installed tool before planning', async () => {
  api.inventory.mockResolvedValue({
    drivers: ['claude', 'codex'],
    verification_levels: {
      claude: 'openpgp',
      codex: 'sigstore-cosign',
    },
    inventory: { installed: [], leftovers: [] },
    read_only: false,
    jobs: [],
  })
  mount()
  const user = userEvent.setup()
  const claude = await openDetails(user, 'Claude Code')
  expect(
    within(claude).getByText('Publisher-signed release (OpenPGP).'),
  ).toBeInTheDocument()
  await user.keyboard('{Escape}')
  const codex = await openDetails(user, 'Codex')
  expect(
    within(codex).getByText(
      'Package digest and publisher signatures verified.',
    ),
  ).toBeInTheDocument()
  expect(api.plan).not.toHaveBeenCalled()
})

it('requires an explicit path action to probe an unregistered CLI version', async () => {
  api.detect
    .mockResolvedValueOnce({
      candidates: [
        {
          path: '/usr/local/bin/claude',
          match: 'unregistered-observed',
          executable: true,
          probe_skipped: 'Not executed without explicit selection',
        },
      ],
    })
    .mockResolvedValueOnce({
      candidates: [
        {
          path: '/usr/local/bin/claude',
          match: 'unregistered-observed',
          executable: true,
          version: '2.1.261',
        },
      ],
    })
  const user = userEvent.setup()
  mount()
  await openDetails(user, 'Claude Code')
  await user.click(
    await screen.findByRole('button', { name: 'Detect Claude Code' }),
  )
  expect(
    await screen.findByText('Not executed without explicit selection'),
  ).toBeInTheDocument()
  expect(api.detect).toHaveBeenCalledTimes(1)
  await user.click(
    screen.getByRole('button', {
      name: 'Probe version at /usr/local/bin/claude',
    }),
  )
  await waitFor(() => expect(api.detect).toHaveBeenCalledTimes(2))
  expect(await screen.findByText(/2.1.261/)).toBeInTheDocument()
})

it('shows a top-level detection failure instead of claiming no executable exists', async () => {
  api.detect.mockResolvedValueOnce({
    candidates: [],
    probe_error: 'Host inventory could not be read',
  })
  const user = userEvent.setup()
  mount()
  await openDetails(user, 'Claude Code')
  await user.click(
    await screen.findByRole('button', { name: 'Detect Claude Code' }),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Host inventory could not be read',
  )
  expect(
    screen.queryByText('No host executable detected.'),
  ).not.toBeInTheDocument()
})

it('reports a failed selected probe without a completion toast', async () => {
  api.detect
    .mockResolvedValueOnce({
      candidates: [
        {
          path: '/usr/local/bin/claude',
          match: 'unregistered-observed',
          executable: true,
        },
      ],
    })
    .mockResolvedValueOnce({
      candidates: [],
      probe_error: 'The selected host probe failed',
    })
  const user = userEvent.setup()
  mount()
  await openDetails(user, 'Claude Code')
  await user.click(
    await screen.findByRole('button', { name: 'Detect Claude Code' }),
  )
  await user.click(
    await screen.findByRole('button', {
      name: 'Probe version at /usr/local/bin/claude',
    }),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'The selected host probe failed',
  )
  expect(toast.success).not.toHaveBeenCalled()
})

it.each([
  { probe_error: 'probe exited 1: broken runtime' },
  { probe_skipped: 'refused: selected executable is not safe to run' },
])('preserves the selected candidate failure reason: %o', async (failure) => {
  const path = '/usr/local/bin/claude'
  api.detect
    .mockResolvedValueOnce({
      candidates: [{ path, match: 'unregistered-observed', executable: true }],
    })
    .mockResolvedValueOnce({
      candidates: [
        { path, match: 'unregistered-observed', executable: true, ...failure },
      ],
      probe_error:
        'One or more selected probes failed. Inspect each candidate’s reason.',
    })
  const user = userEvent.setup()
  mount()
  await openDetails(user, 'Claude Code')
  await user.click(
    await screen.findByRole('button', { name: 'Detect Claude Code' }),
  )
  await user.click(
    await screen.findByRole('button', { name: `Probe version at ${path}` }),
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    failure.probe_error ?? failure.probe_skipped!,
  )
  expect(toast.success).not.toHaveBeenCalled()
})

// EU18/EU20 (WEB 01f5fe2b): the page maps a failed read like every other panel. A
// refused inventory says the person has no access, not that the engine failed.
it('says the person has no access when the inventory read is refused', async () => {
  api.inventory.mockRejectedValue(
    new ApiError(
      403,
      'forbidden',
      'A system administrator session is required.',
    ),
  )
  mount()
  expect(
    await screen.findByText('You do not have access to AI tools.'),
  ).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /try again|retry/i })).toBeNull()
})

// EU on RC10: Claude Code showed "Installed" in its card and "No managed installation"
// below it. A tool on the server that Olivares did not install now says so, in its details.
it('says a tool found on the server was not installed by Olivares', async () => {
  const user = userEvent.setup()
  mount()
  const claude = await openDetails(user, 'Claude Code')
  expect(
    within(claude).getByText('Installed on this server, not by Olivares'),
  ).toBeInTheDocument()
  expect(within(claude).queryByText('No managed installation')).toBeNull()
})

// #472: OpenCode installed with npm, outside Olivares: it is a tool box with its own
// sign-in, not an "Install OpenCode" line.
it.each([
  ['grok', 'Grok Build'],
  ['opencode', 'OpenCode'],
])(
  'lists %s found on the server as installed outside Olivares, with its sign-in',
  async (driver, name) => {
    api.inventory.mockResolvedValue({
      drivers: [driver],
      inventory: { installed: [], leftovers: [] },
      read_only: false,
      jobs: [],
    })
    api.providers.mockResolvedValue({ providers: [snap(driver)] })
    signIn.status.mockImplementation((d: string) =>
      Promise.resolve({ driver: d, installed: true, signed_in: false }),
    )
    const user = userEvent.setup()
    mount()
    const box = await screen.findByTestId(`tool-${driver}`)
    expect(
      await within(box).findByText('Account · not signed in'),
    ).toBeInTheDocument()
    expect(
      within(box).getByRole('button', { name: /^sign in/i }),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('not-installed')).toBeNull()
    const details = await openDetails(user, name)
    expect(
      within(details).getByText('Installed on this server, not by Olivares'),
    ).toBeInTheDocument()
  },
)

// A status that cannot be read is said in the row, not shown as signed out; Refresh asks
// the tool again.
it('says when the login of a tool cannot be read, and Refresh asks again', async () => {
  api.inventory.mockResolvedValue({
    drivers: ['opencode'],
    inventory: { installed: [], leftovers: [] },
    read_only: false,
    jobs: [],
  })
  api.providers.mockResolvedValue({ providers: [snap('opencode')] })
  signIn.status.mockRejectedValue(
    new ApiError(500, 'internal', 'status failed'),
  )
  mount()
  expect(await screen.findByText('Account · not readable')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /^sign in/i })).toBeNull()
  const calls = signIn.status.mock.calls.length
  await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  await waitFor(() =>
    expect(signIn.status.mock.calls.length).toBeGreaterThan(calls),
  )
})

// HU2-30 (RC10): after Review -> Install, the sign-in row kept "Not installed" next to
// "Installed" until a reload. A finished install reads the tools' own status again.
it('reads the tool status again when an install finishes', async () => {
  const user = userEvent.setup()
  mount()
  await openDetails(user, 'Claude Code')
  await user.click(
    await screen.findByRole('button', { name: /review claude code/i }),
  )
  await screen.findByText('2.1.261')
  const before = signIn.status.mock.calls.filter(([d]) => d === 'claude').length
  await user.click(
    screen.getByRole('button', { name: /install approved version/i }),
  )
  expect(await screen.findByText(/installation complete/i)).toBeInTheDocument()
  await waitFor(() =>
    expect(
      signIn.status.mock.calls.filter(([d]) => d === 'claude').length,
    ).toBeGreaterThan(before),
  )
})

it('reads the inventory again when an install from the Install menu finishes', async () => {
  api.plan.mockResolvedValue({
    driver: 'grok',
    version: 'stable',
    digest: 'g1',
    verification: 'none-origin-only',
    executable: '/tools/grok/grok',
  })
  api.job.mockResolvedValue({
    id: 'job1',
    state: 'succeeded',
    driver: 'grok',
    version: 'stable',
    progress: 'placed',
  })
  const user = userEvent.setup()
  mount()
  await user.click(await screen.findByRole('button', { name: /^Install/ }))
  await user.click(
    await screen.findByRole('menuitem', { name: 'Install Grok Build' }),
  )
  const before = api.inventory.mock.calls.length
  await user.click(
    await screen.findByRole('button', { name: 'Install approved version' }),
  )
  await waitFor(() =>
    expect(api.inventory.mock.calls.length).toBeGreaterThan(before),
  )
})

it('explains why an empty version cannot be reviewed and clears the reason after entry', async () => {
  const user = userEvent.setup()
  mount()
  const sheet = await openDetails(user, 'Claude Code')
  const version = within(sheet).getByRole('textbox', {
    name: 'Version for Claude Code',
  })
  const review = within(sheet).getByRole('button', {
    name: 'Review Claude Code install',
  })
  await user.clear(version)
  expect(review).toBeDisabled()
  expect(review).toHaveAccessibleDescription(
    'Enter a version to review the installation.',
  )
  expect(
    within(sheet).getByText('Enter a version to review the installation.'),
  ).toBeVisible()
  await user.type(version, 'stable')
  expect(review).toBeEnabled()
  expect(review).not.toHaveAttribute('aria-describedby')
})

it.each(['read-only', 'installing'])(
  'explains why review actions are disabled while %s',
  async (state) => {
    if (state === 'read-only')
      api.inventory.mockResolvedValue({
        drivers: ['claude'],
        inventory: { installed: [], leftovers: [] },
        read_only: true,
        jobs: [],
      })
    if (state === 'installing') {
      api.inventory.mockResolvedValue({
        drivers: ['claude'],
        inventory: { installed: [], leftovers: [] },
        read_only: false,
        jobs: [{ id: 'job1' }],
      })
      api.job.mockResolvedValue({
        id: 'job1',
        state: 'running',
        driver: 'claude',
        version: '2.1.261',
        progress: 'downloading',
      })
    }
    const user = userEvent.setup()
    mount()
    const sheet = await openDetails(user, 'Claude Code')
    const review = within(sheet).getByRole('button', {
      name: 'Review Claude Code install',
    })
    const reason =
      state === 'read-only'
        ? 'This server is read-only. Installation is disabled.'
        : 'Installing…'
    await waitFor(() => expect(review).toBeDisabled())
    expect(review).toHaveAccessibleDescription(reason)
    for (const button of within(sheet).getAllByRole('button'))
      expect(button).toHaveAccessibleName()
  },
)

it('says the release is being verified while a review is planned', async () => {
  api.plan.mockImplementationOnce(() => new Promise(() => {}))
  const user = userEvent.setup()
  mount()
  await openDetails(user, 'Claude Code')
  await user.click(
    await screen.findByRole('button', { name: 'Review Claude Code install' }),
  )
  expect(await screen.findByText('Verifying release selection…')).toBeVisible()
})

it('explains review actions while an approved installation is starting', async () => {
  api.install.mockImplementationOnce(() => new Promise(() => {}))
  const user = userEvent.setup()
  mount()
  await openDetails(user, 'Claude Code')
  await user.click(
    await screen.findByRole('button', { name: 'Review Claude Code install' }),
  )
  await user.click(
    await screen.findByRole('button', { name: 'Install approved version' }),
  )
  const sheet = await openDetails(user, 'Claude Code')
  const review = within(sheet).getByRole('button', {
    name: 'Review Claude Code install',
  })
  await waitFor(() => expect(review).toBeDisabled())
  expect(review).toHaveAccessibleDescription('Starting…')
})

it('reads only the default logins without an organization', async () => {
  useTenantStore.setState({ activeTenant: null })
  mount()
  await screen.findByTestId('tool-claude')
  expect(api.providers).toHaveBeenCalledWith(null, expect.anything(), true)
})

it('names each tool once when both the login snapshots and the install inventory list it', async () => {
  const tools = {
    claude: 'Claude Code',
    codex: 'Codex',
    grok: 'Grok Build',
    opencode: 'OpenCode',
    ollama: 'Ollama',
  }
  api.inventory.mockResolvedValue({
    drivers: Object.keys(tools),
    inventory: { installed: [], leftovers: [] },
    read_only: false,
    jobs: [],
  })
  api.providers.mockResolvedValue({
    providers: Object.keys(tools)
      .filter((driver) => driver !== 'ollama')
      .map((driver) => ({
        ...snap(driver),
        state: 'not_installed' as const,
        installed: false,
        version: '',
      })),
  })
  mount()
  const line = await screen.findByTestId('not-installed')
  for (const name of Object.values(tools)) {
    expect(line.textContent?.split(name).length).toBe(2)
  }
  expect(screen.queryByRole('heading', { name: 'Providers' })).toBeNull()
})

it.each([
  ['grok', 'Grok Build', 'stable'],
  ['opencode', 'OpenCode', 'latest'],
  ['ollama', 'Ollama', 'latest'],
])(
  'offers %s its next install step from the Install menu, without opening details',
  async (driver, name, version) => {
    api.inventory.mockResolvedValue({
      drivers: [driver],
      inventory: { installed: [], leftovers: [] },
      read_only: false,
      jobs: [],
    })
    api.providers.mockResolvedValue({ providers: [] })
    const user = userEvent.setup()
    mount()
    await user.click(await screen.findByRole('button', { name: /^Install/ }))
    await user.click(
      await screen.findByRole('menuitem', { name: `Install ${name}` }),
    )
    await waitFor(() => expect(api.plan).toHaveBeenCalledWith(driver, version))
    expect(api.install).not.toHaveBeenCalled()
  },
)

it('lists a tool a login snapshot names even when the inventory does not', async () => {
  api.inventory.mockResolvedValue({
    drivers: ['claude'],
    inventory: { installed: [], leftovers: [] },
    read_only: false,
    jobs: [],
  })
  api.providers.mockResolvedValue({
    providers: [snap('claude'), snap('gemini-cli')],
  })
  mount()
  expect(await screen.findByTestId('tool-gemini-cli')).toBeInTheDocument()
  expect(
    within(screen.getByTestId('tool-gemini-cli')).getByRole('heading', {
      name: 'Gemini CLI',
    }),
  ).toBeInTheDocument()
})
