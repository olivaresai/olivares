// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// "Installed here" shows what a new session in the workspace receives, from what the
// launch reads: the tool programs the launch resolver names, the programs and npm
// registries of the provider profile each tool's session runs under, the workspace's
// skills selection, and the MCP servers a stored gateway has enabled.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import './i18n'
const { auth, skills, mcp, consoleApiMock, agentOps } = vi.hoisted(() => ({
  auth: { activeTenant: 't1', can: (_p: string): boolean => true },
  agentOps: { previewProfile: vi.fn(), toolsReadiness: vi.fn() },
  skills: { selection: vi.fn() },
  mcp: { get: vi.fn() },
  consoleApiMock: { listWorkspaces: vi.fn() },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('@/features/skills/api', async (orig) => ({
  ...(await orig<typeof import('@/features/skills/api')>()),
  skillsApi: skills,
}))
vi.mock('@/features/console/mcp-gateway-api', async (orig) => ({
  ...(await orig<typeof import('@/features/console/mcp-gateway-api')>()),
  mcpGatewayApi: mcp,
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: agentOps,
}))
vi.mock('@/features/console/api', async (orig) => ({
  ...(await orig<typeof import('@/features/console/api')>()),
  consoleApi: consoleApiMock,
}))
import { ApiError } from '@/lib/api/errors'
import { useModulesStore } from '@/stores/modules'
import { useWorkspaceStore } from '@/stores/workspace'
import type { ProviderSnapshot, ToolInventory } from './api'
import { InstalledHere } from './installed-here'
import { toolFacts } from './inventory'

const inventory: ToolInventory = {
  drivers: ['claude', 'codex', 'grok'],
  // Codex is installed but its session program does not resolve: a session gets no Codex.
  // Grok runs from the PATH: no managed install and no box on the page to link to.
  launch_programs: {
    claude: '/tools/claude/2.1.261/claude',
    grok: '/usr/local/bin/grok',
  },
  inventory: {
    installed: [
      {
        driver: 'claude',
        version: '2.1.200',
        state: 'installed',
        executable: '/tools/claude/2.1.200/claude',
      },
      {
        driver: 'claude',
        version: '2.1.261',
        state: 'installed',
        executable: '/tools/claude/2.1.261/claude',
      },
      {
        driver: 'codex',
        version: '0.162.0',
        state: 'installed',
        executable: '/tools/codex/codex',
      },
    ],
    leftovers: [],
  },
  read_only: false,
  jobs: [],
}
const gateway = (source: 'store' | 'file', sessionTools = false) => ({
  version: 1,
  source,
  read_only: false,
  session_tools: sessionTools,
  session_endpoint: '/session/mcp',
  governance: {},
  servers: [
    { id: 's1', name: 'github', enabled: true },
    { id: 's2', name: 'paused', enabled: false },
  ],
})

function mount(snaps: ProviderSnapshot[] = []) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <InstalledHere
        scope={['agent-tools', 0]}
        inventory={inventory}
        facts={toolFacts(inventory, snaps)}
      />
    </QueryClientProvider>,
  )
}
const row = async (name: string) =>
  (await screen.findByRole('heading', { level: 3, name })).closest('li')!

beforeEach(() => {
  useWorkspaceStore.getState().setActiveWorkspace('ws-eng', 'Engineering')
  skills.selection.mockResolvedValue({
    digest: 'd',
    members: [
      {
        pack_id: 'p1',
        pack_revision_id: 'r1',
        name: 'research',
        directory: 'research',
      },
    ],
  })
  mcp.get.mockResolvedValue(gateway('store'))
  agentOps.previewProfile.mockResolvedValue({ reason: 'own_login' })
  // Unread unless a test answers it: every tool is then previewed, as before readiness.
  agentOps.toolsReadiness.mockRejectedValue(
    new ApiError(503, 'unavailable', 'The tools readiness could not be read.'),
  )
  consoleApiMock.listWorkspaces.mockResolvedValue({
    items: [
      { id: 'ws-other', name: 'Other', is_default: false },
      { id: 'ws-default', name: 'Default', is_default: true },
    ],
  })
})
afterEach(() => {
  // Unmount before the mocks are cleared: a panel still waiting on the tools readiness would
  // otherwise send its previews into the next test's calls.
  cleanup()
  auth.can = () => true
  useWorkspaceStore.getState().clear()
  useModulesStore.getState().setOff([])
  vi.clearAllMocks()
})

it('lists what a new session in the active workspace receives, each linked to its page', async () => {
  mount()
  expect(
    await screen.findByText('What a new session in Engineering receives.'),
  ).toBeInTheDocument()
  const tools = within(await row('AI tools'))
  // The version of the install the launch resolver names, not the first installed row.
  expect(tools.getByRole('link', { name: 'Claude Code' })).toHaveAttribute(
    'href',
    '#tool-claude',
  )
  expect(tools.getByText('2.1.261')).toBeInTheDocument()
  expect(tools.queryByText('Codex')).toBeNull()
  expect(tools.getByText('Grok Build')).toBeInTheDocument()
  expect(tools.queryByRole('link', { name: 'Grok Build' })).toBeNull()
  const skillRow = within(await row('Skills'))
  expect(
    await skillRow.findByRole('link', { name: 'research' }),
  ).toHaveAttribute('href', '/skills?pack=p1')
  expect(skills.selection).toHaveBeenCalledWith(
    'workspace',
    'ws-eng',
    expect.anything(),
  )
  const servers = within(await row('MCP servers'))
  expect(await servers.findByRole('link', { name: 'github' })).toHaveAttribute(
    'href',
    '/mcp-servers',
  )
  expect(servers.queryByText('paused')).toBeNull()
  expect(servers.queryByText('Olivares session tools')).toBeNull()
  expect(
    servers.getByText('For Claude Code, Codex, OpenCode sessions.'),
  ).toBeInTheDocument()
})

it('reads the default workspace when all workspaces are selected, as a launch does', async () => {
  useWorkspaceStore.getState().clear()
  mount()
  expect(
    await screen.findByText('What a new session in Default receives.'),
  ).toBeInTheDocument()
  await within(await row('Skills')).findByRole('link', { name: 'research' })
  expect(skills.selection).toHaveBeenCalledWith(
    'workspace',
    'ws-default',
    expect.anything(),
  )
})

it('lists no MCP server from a file-sourced gateway, which a launch never configures', async () => {
  mcp.get.mockResolvedValue(gateway('file', true))
  mount()
  const servers = within(await row('MCP servers'))
  expect(await servers.findByText('None')).toBeInTheDocument()
  expect(servers.queryByRole('link')).toBeNull()
})

it('names the session tools of a stored gateway that turned them on', async () => {
  mcp.get.mockResolvedValue(gateway('store', true))
  mount()
  const servers = within(await row('MCP servers'))
  expect(
    await servers.findByRole('link', { name: 'Olivares session tools' }),
  ).toBeInTheDocument()
})

it('says the skills read failed, with the refusal a launch would give, instead of "None"', async () => {
  skills.selection.mockRejectedValue(
    new ApiError(400, 'skill_name_conflict', 'Two packs define research.'),
  )
  mount()
  const skillRow = within(await row('Skills'))
  expect(
    await skillRow.findByText(/Could not read it: .*research/),
  ).toBeInTheDocument()
  expect(skillRow.queryByText('None')).toBeNull()
})

it('says no workspace resolves instead of reading forever', async () => {
  useWorkspaceStore.getState().clear()
  consoleApiMock.listWorkspaces.mockResolvedValue({
    items: [{ id: 'ws-other', name: 'Other', is_default: false }],
  })
  mount()
  const skillRow = within(await row('Skills'))
  expect(
    await skillRow.findByText('No default workspace to read.'),
  ).toBeInTheDocument()
  expect(screen.queryByText('Reading…')).toBeNull()
  expect(skills.selection).not.toHaveBeenCalled()
})

it('says the skills module is off rather than "None"', async () => {
  useModulesStore.getState().setOff(['skills'])
  mount()
  const skillRow = within(await row('Skills'))
  expect(
    skillRow.getByText('The skills module is off: sessions receive none.'),
  ).toBeInTheDocument()
  expect(skillRow.queryByText('None')).toBeNull()
  expect(skills.selection).not.toHaveBeenCalled()
})

it('says the workspace list stopped before the default one', async () => {
  useWorkspaceStore.getState().clear()
  consoleApiMock.listWorkspaces.mockResolvedValue({
    items: [{ id: 'ws-other', name: 'Other', is_default: false }],
    has_more: true,
  })
  mount()
  expect(
    await within(await row('Skills')).findByText(
      'The workspace list stopped before the default workspace: choose one in the workspace switcher.',
    ),
  ).toBeInTheDocument()
})

it('shows no version for a tool whose report is not a version', async () => {
  mount([
    {
      instance: 'grok',
      driver: 'grok',
      default: true,
      config_dir: '/home/op/.grok',
      state: 'ready',
      installed: true,
      version: '{"type":"system","subtype":"init"}',
      limits: [],
      models: [],
      checked_at: '2026-10-11T10:00:00Z',
      source: 'grok --version',
    },
  ])
  const tools = within(await row('AI tools'))
  expect(tools.getByRole('link', { name: 'Grok Build' })).toBeInTheDocument()
  expect(tools.queryByText(/subtype/)).toBeNull()
})

const profile = (ref: string, extra: object) => ({
  reason: 'own_login',
  profile: {
    profile_ref: ref,
    driver: 'claude',
    environment_ref: 'env',
    display_name: `${ref} profile`,
    state: 'active',
    local_environment: true,
    operable: true,
    ...extra,
  },
})

it("lists the programs and npm registries of each tool's launch profile, never the token's vault entry", async () => {
  agentOps.previewProfile.mockImplementation(async (driver: string) =>
    driver === 'claude'
      ? profile('ppf_claude', {
          session_programs: ['jq'],
          session_registries: [
            {
              kind: 'npm',
              url: 'https://npm.acme.example/',
              scope: '@acme',
              secret: 'env/npm-acme',
            },
          ],
        })
      : { reason: 'own_login' },
  )
  mount()
  const packages = within(await row('Programs and registries'))
  expect(await packages.findByRole('link', { name: 'jq' })).toHaveAttribute(
    'href',
    '#tool-claude',
  )
  expect(
    packages.getByRole('link', { name: '@acme https://npm.acme.example/' }),
  ).toBeInTheDocument()
  expect(packages.getAllByText('Claude Code')).toHaveLength(2)
  expect(packages.queryByText(/npm-acme/)).toBeNull()
  expect(
    packages.getByText(
      /each tool's new session uses \(Claude Code: ppf_claude profile\)/,
    ),
  ).toBeInTheDocument()
  // One preview per tool a session can run here (the launch programs), none for Codex.
  expect(agentOps.previewProfile.mock.calls.map(([d]) => d)).toEqual([
    'claude',
    'grok',
  ])
})

it("lists the hosts a tool's launch profile declares for its sessions, and no row when none", async () => {
  agentOps.previewProfile.mockImplementation(async (driver: string) =>
    driver === 'claude'
      ? profile('ppf_claude', {
          session_egress_hosts: ['api.github.com', 'github.com'],
        })
      : { reason: 'own_login' },
  )
  mount()
  const hosts = within(await row('Network hosts'))
  expect(
    await hosts.findByRole('link', { name: 'github.com' }),
  ).toHaveAttribute('href', '#tool-claude')
  expect(
    hosts.getByRole('link', { name: 'api.github.com' }),
  ).toBeInTheDocument()
})

it('shows no network hosts row when no profile declares one', async () => {
  agentOps.previewProfile.mockImplementation(async (driver: string) =>
    driver === 'claude' ? profile('ppf_claude', {}) : { reason: 'own_login' },
  )
  mount()
  await within(await row('Programs and registries')).findByText(
    /each tool's new session uses/,
  )
  expect(screen.queryByText('Network hosts')).toBeNull()
})

it('names the launch profile by its account name when it has no display name', async () => {
  agentOps.previewProfile.mockImplementation(async (driver: string) =>
    driver === 'claude'
      ? profile('ppf_claude', {
          display_name: undefined,
          account_name: 'claude-b',
          session_programs: ['jq'],
        })
      : { reason: 'own_login' },
  )
  mount()
  const packages = within(await row('Programs and registries'))
  expect(
    await packages.findByText(
      /each tool's new session uses \(Claude Code: claude-b\)/,
    ),
  ).toBeInTheDocument()
})

it('says no profile exists yet when a launch would create one that declares nothing', async () => {
  mount()
  const packages = within(await row('Programs and registries'))
  expect(await packages.findByText('None')).toBeInTheDocument()
  expect(packages.getByText(/None exists yet/)).toBeInTheDocument()
})

it('asks no preview of a tool the tools readiness already refuses', async () => {
  // The preview refuses such a tool with a 409, which the browser reports as an error on
  // every fresh installation; the readiness Now and New session read says the same.
  agentOps.toolsReadiness.mockResolvedValue({
    tools: [
      {
        driver: 'claude',
        ready: false,
        code: 'nothing_to_run_on',
        message: 'Claude Code has nothing to run on yet.',
      },
      { driver: 'grok', ready: true, reason: 'own_login' },
    ],
  })
  mount()
  const packages = within(await row('Programs and registries'))
  expect(
    await packages.findByText(
      /Not listed because no session can start yet: Claude Code\.$/,
    ),
  ).toBeInTheDocument()
  expect(agentOps.previewProfile).toHaveBeenCalledTimes(1)
  expect(agentOps.previewProfile.mock.calls[0][0]).toBe('grok')
})

it('names the tools no session can start on, and shows any other failure as unread', async () => {
  agentOps.previewProfile.mockRejectedValue(
    new ApiError(
      409,
      'nothing_to_run_on',
      'Grok Build has nothing to run on yet.',
    ),
  )
  const { unmount } = mount()
  const refusedRow = within(await row('Programs and registries'))
  expect(await refusedRow.findByText('None')).toBeInTheDocument()
  expect(
    refusedRow.getByText(
      /Not listed because no session can start yet: Claude Code, Grok Build/,
    ),
  ).toBeInTheDocument()
  const refusedSkills = within(await row('Skills'))
  expect(await refusedSkills.findByText('None')).toBeInTheDocument()
  expect(refusedSkills.queryByRole('link', { name: 'research' })).toBeNull()
  unmount()
  // Another conflict (a saved default model no longer valid) is not a refused tool.
  agentOps.previewProfile.mockRejectedValue(
    new ApiError(409, 'conflict', 'The saved default model is gone.'),
  )
  const second = mount()
  expect(
    await within(await row('Programs and registries')).findByText(
      /Could not read it: .*default model/,
    ),
  ).toBeInTheDocument()
  second.unmount()
  agentOps.previewProfile.mockRejectedValue(
    new ApiError(503, 'unavailable', 'The sign-in status could not be read.'),
  )
  mount()
  const packages = within(await row('Programs and registries'))
  expect(
    await packages.findByText(/Could not read it: .*sign-in status/),
  ).toBeInTheDocument()
  expect(packages.queryByText('None')).toBeNull()
})

it('says the role cannot read provider profiles instead of "None"', async () => {
  auth.can = (p) => p !== 'sessions:profile:read'
  mount()
  const packages = within(await row('Programs and registries'))
  expect(
    packages.getByText('Your role cannot read provider profiles.'),
  ).toBeInTheDocument()
  expect(agentOps.previewProfile).not.toHaveBeenCalled()
})

it('resolves workspace and profile skills together for each launchable tool', async () => {
  useWorkspaceStore.getState().setActiveWorkspace('ws-sales', 'Sales')
  agentOps.previewProfile.mockImplementation(async (driver: string) =>
    profile('ppf_' + driver, {}),
  )
  skills.selection.mockImplementation(
    async (_kind, _id, _signal, profileRef) => ({
      members: [
        {
          name: profileRef === 'ppf_claude' ? 'claude-review' : 'grok-review',
          pack_id: 'pack-profile',
          pack_revision_id: 'rev-profile',
        },
      ],
    }),
  )
  mount()
  const skillRow = within(await row('Skills'))
  expect(
    await skillRow.findByRole('link', { name: 'claude-review' }),
  ).toHaveAttribute('href', '/skills?pack=pack-profile')
  expect(
    await skillRow.findByRole('link', { name: 'grok-review' }),
  ).toBeInTheDocument()
  expect(skills.selection).toHaveBeenCalledWith(
    'workspace',
    'ws-sales',
    expect.any(AbortSignal),
    'ppf_claude',
  )
  expect(skills.selection).toHaveBeenCalledWith(
    'workspace',
    'ws-sales',
    expect.any(AbortSignal),
    'ppf_grok',
  )
})
