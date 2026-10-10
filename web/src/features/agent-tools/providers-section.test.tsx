// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import type { ProviderSnapshot } from './api'
import './i18n'
const { api, auth } = vi.hoisted(() => ({
  api: {
    inventory: vi.fn(),
    providers: vi.fn(),
    job: vi.fn(),
  },
  auth: {
    isSuperadmin: true,
    can: () => false,
    activeTenant: null,
    principal: { user_id: 'root' },
  },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentToolsApi: api,
}))
vi.mock('@/features/first-hour/api', async (orig) => ({
  ...((await orig()) as object),
  signInApi: {
    status: (driver: string) =>
      Promise.resolve({ driver, installed: true, signed_in: true }),
  },
}))
import { agentOpsApi } from '@/features/agentops/api'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import { useTenantStore } from '@/stores/tenant'
import { AgentToolsView } from './agent-tools-view'

const inTwoHours = new Date(Date.now() + 7_200_000).toISOString()
const claude: ProviderSnapshot = {
  instance: 'claude',
  driver: 'claude',
  default: true,
  config_dir: '/home/op/.claude',
  state: 'ready',
  installed: true,
  version: '2.1.289 (Claude Code)',
  auth_method: 'claude.ai',
  plan: 'max',
  email: 'c***@example.com',
  limits: [
    { label: '5-hour', percent: 37, resets_at: inTwoHours, severity: 'normal' },
    { label: 'Weekly', percent: 77, severity: 'warning' },
  ],
  models: [{ id: 'opus', name: 'Opus' }, { id: 'sonnet' }],
  checked_at: '2026-10-06T14:00:00Z',
  source: 'claude --version; claude auth status --json',
}
const codex: ProviderSnapshot = {
  instance: 'codex',
  driver: 'codex',
  default: true,
  config_dir: '/home/op/.codex',
  state: 'not_signed_in',
  installed: true,
  version: 'codex-cli 0.160.0',
  limits: [],
  models: [],
  next_command: 'codex login',
  checked_at: '2026-10-06T14:00:00Z',
  source: 'codex --version; codex app-server: account/read',
}

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AgentToolsView />
    </QueryClientProvider>,
  )
}
/** The tool's menu, then Install details: the sheet with the logins the tool reports. */
async function openOptions(name: string) {
  await screen.findByTestId(
    `tool-${name === 'Claude Code' ? 'claude' : 'codex'}`,
  )
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: `Options for ${name}` }),
  )
  await user.click(
    await screen.findByRole('menuitem', { name: 'Install details' }),
  )
}
beforeEach(() => {
  vi.clearAllMocks()
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(readinessOf({}))
  auth.isSuperadmin = true
  useTenantStore.setState({ activeTenant: 'tenant-1' })
  api.inventory.mockResolvedValue({
    drivers: [],
    inventory: { installed: [], leftovers: [] },
    read_only: false,
    jobs: [],
  })
  api.providers.mockResolvedValue({ providers: [claude, codex] })
})

it('shows each signed-in instance with its account, plan, usage windows and models', async () => {
  mount()
  await openOptions('Claude Code')
  const card = await screen.findByRole('article', { name: /claude code/i })
  expect(api.providers).toHaveBeenCalledWith(
    'tenant-1',
    expect.anything(),
    true,
  )
  expect(
    within(card).getByText('Authenticated as c***@example.com · max'),
  ).toBeInTheDocument()
  expect(within(card).getByText('2.1.289 (Claude Code)')).toBeInTheDocument()
  expect(within(card).getByText('/home/op/.claude')).toBeInTheDocument()
  const session = within(card).getByRole('progressbar', { name: '5-hour' })
  expect(session).toHaveAttribute('aria-valuenow', '37')
  expect(
    within(card).getByRole('progressbar', { name: 'Weekly' }),
  ).toHaveAttribute('aria-valuenow', '77')
  expect(within(card).getByText(/resets in 2 hours/i)).toBeInTheDocument()
  expect(within(card).getByText('Models: 2')).toBeInTheDocument()
  expect(within(card).getByText('Opus')).toBeInTheDocument()
  expect(within(card).getByText(/^Checked /)).toBeInTheDocument()
  expect(
    within(card).getByText('claude --version; claude auth status --json'),
  ).toBeInTheDocument()
})

it('gives a signed-out tool its own login command to copy, and no login form of ours', async () => {
  mount()
  await openOptions('Codex')
  const card = await screen.findByRole('article', { name: /codex/i })
  expect(within(card).getByText('Not signed in')).toBeInTheDocument()
  expect(within(card).getByText('codex login')).toBeInTheDocument()
  expect(
    within(card).getByRole('button', { name: /copy command/i }),
  ).toBeInTheDocument()
  expect(within(card).queryByRole('textbox')).toBeNull()
  expect(within(card).queryByRole('progressbar')).toBeNull()
})

it('keeps a stale snapshot and names the command that failed', async () => {
  api.providers.mockResolvedValue({
    providers: [
      { ...claude, stale: true, error: 'claude auth status --json: timed out' },
    ],
  })
  mount()
  await openOptions('Claude Code')
  const card = await screen.findByRole('article', { name: /claude code/i })
  expect(within(card).getByText('Stale')).toBeInTheDocument()
  expect(
    within(card).getByText(
      'Last good answer. The latest check failed: claude auth status --json: timed out',
    ),
  ).toBeInTheDocument()
  expect(
    within(card).getByText('Authenticated as c***@example.com · max'),
  ).toBeInTheDocument()
})

it("names an organization's own login and shows its method when the tool reports no plan", async () => {
  api.providers.mockResolvedValue({
    providers: [
      {
        ...claude,
        instance: 'claude/olivares',
        default: false,
        config_dir: '/data/tool-logins/tenant-1/claude',
        email: undefined,
        plan: undefined,
        auth_method: 'apiKey',
        limits: [],
        models: [],
      },
    ],
  })
  mount()
  await openOptions('Claude Code')
  const card = await screen.findByRole('article', {
    name: /this organization's login/i,
  })
  expect(within(card).getByText('Authenticated · apiKey')).toBeInTheDocument()
  expect(within(card).queryByText(/server user's login/i)).toBeNull()
})

it('reads only the default logins without an organization, and Refresh reads again', async () => {
  useTenantStore.setState({ activeTenant: null })
  mount()
  await openOptions('Claude Code')
  await screen.findByRole('article', { name: /claude code/i })
  expect(api.providers).toHaveBeenCalledWith(null, expect.anything(), true)
  expect(api.providers).toHaveBeenCalledTimes(1)
  const user = userEvent.setup()
  await user.keyboard('{Escape}')
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  await waitFor(() => expect(api.providers).toHaveBeenCalledTimes(2))
})

it('does not read providers without a system administrator session', async () => {
  auth.isSuperadmin = false
  mount()
  expect(await screen.findByText(/system administrator/i)).toBeInTheDocument()
  expect(api.providers).not.toHaveBeenCalled()
})

it('keeps server and organization logins under one tool, in its details', async () => {
  api.providers.mockResolvedValue({
    providers: [
      claude,
      { ...claude, instance: 'claude/organization', default: false },
    ],
  })
  mount()
  // The tool is one boxed list; its logins are not on the page until the details open.
  const box = await screen.findByTestId('tool-claude')
  expect(screen.getAllByText('Claude Code')).toHaveLength(1)
  expect(within(box).queryByRole('article')).toBeNull()
  await openOptions('Claude Code')
  const sheet = await screen.findByRole('dialog', { name: /Claude Code/ })
  expect(
    within(sheet).getByRole('article', { name: /server user's login/i }),
  ).toBeVisible()
  expect(
    within(sheet).getByRole('article', {
      name: /this organization's login/i,
    }),
  ).toBeVisible()
})

it('reports a failed snapshot read outside the disclosure and offers Retry', async () => {
  api.inventory.mockResolvedValue({
    drivers: ['claude'],
    inventory: { installed: [], leftovers: [] },
    read_only: false,
    jobs: [],
  })
  api.providers.mockRejectedValueOnce(new Error('Snapshot command failed'))
  mount()
  expect(await screen.findByText('Snapshot command failed')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
  await screen.findByTestId('tool-claude')
  await openOptions('Claude Code')
  expect(
    await screen.findByRole('article', { name: /claude code/i }),
  ).toBeVisible()
  expect(api.providers).toHaveBeenCalledTimes(2)
})

it('keeps readable login details when inventory fails, without offering installation', async () => {
  api.inventory.mockRejectedValueOnce(new Error('Inventory unavailable'))
  mount()
  expect(await screen.findByText('Inventory unavailable')).toBeVisible()
  await openOptions('Claude Code')
  const card = await screen.findByRole('article', { name: /claude code/i })
  expect(
    within(card).getByText('Authenticated as c***@example.com · max'),
  ).toBeVisible()
  expect(
    within(card).getByRole('progressbar', { name: '5-hour' }),
  ).toHaveAttribute('aria-valuenow', '37')
  expect(screen.queryByRole('button', { name: 'Install' })).toBeNull()
  expect(
    screen.getByRole('button', { name: 'Review Claude Code install' }),
  ).toBeDisabled()
  expect(
    screen.getByRole('button', { name: 'Review Claude Code install' }),
  ).toHaveAccessibleDescription('Not readable')
})
