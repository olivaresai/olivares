// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
const auth = vi.hoisted(() => ({
  principal: 'u1',
  tenant: 't1',
  canNhi: true,
  canRunWrite: true,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: auth.tenant,
    principal: { user_id: auth.principal, aal: 1 },
    can: (p: string) =>
      p === 'sessions:run:write'
        ? auth.canRunWrite
        : p !== 'governance:nhi:read' || auth.canNhi,
  }),
}))
const api = vi.hoisted(() => ({
  createRun: vi.fn(),
  listWorkspaces: vi.fn(),
  listProfiles: vi.fn(),
  profileLaunchReadiness: vi.fn(),
  input: vi.fn(),
}))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentOpsApi: api,
}))
const identities = vi.hoisted(() => ({ nhiLifecycle: vi.fn() }))
vi.mock('@/features/identity/api', () => ({ identityApi: identities }))
const launch = vi.hoisted(() => vi.fn())
vi.mock('./launch-agent-api', () => ({ launchRunAsAgent: launch }))
vi.mock('@/features/workspace-templates/api', () => ({
  templatesApi: {
    list: vi.fn().mockResolvedValue({ items: [], has_more: false }),
  },
  templatesKeys: { list: () => ['templates'], detail: () => ['template'] },
}))
const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', async (orig) => ({
  ...(await orig<typeof import('@tanstack/react-router')>()),
  useNavigate: () => navigate,
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))
import { RunCreateDialog } from './run-create-dialog'
import { fixtureReadiness } from './launch-readiness.fixture'
import { useSessionStore } from '@/stores/session'
const profile = {
  profile_ref: 'ppf_orchestrator',
  driver: 'claude',
  environment_ref: 'env',
  state: 'active',
  display_name: 'Orchestrator profile',
  local_environment: true,
  operable: true,
  session_work_grant: {
    role: 'orchestrator',
    workspace_id: '01a0ef7c-f25c-72f1-9cf8-3b5b2c8d6ab0',
    capabilities: ['work.read'],
    grant_id: 'grant',
  },
}
const agent = {
  identity_ref: 'agent-fixture',
  kind: 'agent',
  sponsor_ref: 'sponsor-fixture',
  enforcement: 'monitor',
  orphaned: false,
  offboard_state: 'none',
}
function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const el = () => (
    <QueryClientProvider client={qc}>
      <RunCreateDialog open onOpenChange={vi.fn()} />
    </QueryClientProvider>
  )
  const ui = render(el())
  return { ...ui, refresh: () => ui.rerender(el()) }
}
/** Every choice but the profile and the first message, and the
 * profile's requirements in full, are under Advanced options. */
async function openAdvanced(user = userEvent.setup()) {
  const toggle = await screen.findByRole('button', { name: 'Advanced options' })
  if (toggle.getAttribute('aria-expanded') !== 'true') await user.click(toggle)
}
async function chooseProfile() {
  const user = userEvent.setup()
  await openAdvanced(user)
  await user.click(await screen.findByLabelText('Provider profile'))
  await user.click(
    await screen.findByRole('option', { name: /Orchestrator profile/ }),
  )
  await screen.findByText('Local requirements checked')
  return user
}
beforeEach(() => {
  vi.clearAllMocks()
  auth.principal = 'u1'
  auth.tenant = 't1'
  auth.canNhi = true
  auth.canRunWrite = true
  useSessionStore.setState({
    csrfToken: 'fixture-human',
    credentialGeneration: 1,
  })
  api.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
  api.listProfiles.mockResolvedValue({ items: [profile], has_more: false })
  api.profileLaunchReadiness.mockResolvedValue(
    fixtureReadiness({ profile_ref: profile.profile_ref }),
  )
  launch.mockResolvedValue({
    run_ref: 'run-fixture',
    agent_ref: agent.identity_ref,
  })
  identities.nhiLifecycle.mockResolvedValue({
    items: [
      agent,
      { ...agent, identity_ref: 'blocked', enforcement: 'blocked' },
      { ...agent, identity_ref: 'orphan', orphaned: true },
      { ...agent, identity_ref: 'human', kind: '' },
      { ...agent, identity_ref: 'offboard', offboard_state: 'finalized' },
    ],
    has_more: false,
  })
})
it('requires a separate admitted agent for an orchestration profile, with no automatic selection', async () => {
  mount()
  await chooseProfile()
  expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
  expect(
    screen.getByText(
      'An orchestration profile requires an independently authenticated agent identity.',
    ),
  ).toBeInTheDocument()
  expect(identities.nhiLifecycle).not.toHaveBeenCalled()
  expect(api.createRun).not.toHaveBeenCalled()
  expect(launch).not.toHaveBeenCalled()
})
it('launches with the explicitly selected admitted agent through OBO, without a human launch fallback', async () => {
  mount()
  const user = await chooseProfile()
  await user.selectOptions(
    screen.getByLabelText('Launch identity'),
    '__agent__',
  )
  await screen.findByRole('option', { name: 'agent-fixture' })
  expect(
    screen.queryByRole('option', { name: /^(blocked|orphan|human|offboard)$/ }),
  ).toBeNull()
  expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
  await user.selectOptions(
    screen.getByLabelText('Agent identity'),
    'agent-fixture',
  )
  await user.click(screen.getByRole('button', { name: 'Start' }))
  await waitFor(() => expect(launch).toHaveBeenCalledOnce())
  expect(launch).toHaveBeenCalledWith(
    expect.objectContaining({ provider_profile_ref: profile.profile_ref }),
    'agent-fixture',
    't1',
    expect.objectContaining({
      dispatchGuard: expect.any(Function),
      signal: expect.any(AbortSignal),
    }),
  )
  expect(api.createRun).not.toHaveBeenCalled()
})
it('sends the first message to a session launched as the admitted agent', async () => {
  api.input.mockResolvedValue({ accepted: true })
  mount()
  const user = await chooseProfile()
  await user.type(
    screen.getByRole('textbox', { name: 'First message (optional)' }),
    'Plan the release',
  )
  await user.selectOptions(
    screen.getByLabelText('Launch identity'),
    '__agent__',
  )
  await screen.findByRole('option', { name: 'agent-fixture' })
  await user.selectOptions(
    screen.getByLabelText('Agent identity'),
    'agent-fixture',
  )
  await user.click(screen.getByRole('button', { name: 'Start' }))
  await waitFor(() => expect(launch).toHaveBeenCalledOnce())
  expect(launch.mock.calls[0][0]).toMatchObject({ name: 'Plan the release' })
  await waitFor(() =>
    expect(api.input).toHaveBeenCalledWith(
      'run-fixture',
      expect.any(String),
      undefined,
      expect.objectContaining({
        signal: expect.any(AbortSignal),
        dispatchGuard: expect.any(Function),
      }),
    ),
  )
  expect(navigate).toHaveBeenCalledWith({
    to: '/sessions',
    search: { session: 'run:run-fixture', pane: 'narrative' },
  })
  expect(api.createRun).not.toHaveBeenCalled()
})
it('refuses the dispatch when the run permission is withdrawn while the launch is in flight', async () => {
  let dispatchGuard = () => {}
  launch.mockImplementation(
    (_body, _actor, _tenant, opts: { dispatchGuard: () => void }) => {
      dispatchGuard = opts.dispatchGuard
      return new Promise(() => {})
    },
  )
  const { refresh } = mount()
  const user = await chooseProfile()
  await user.selectOptions(
    screen.getByLabelText('Launch identity'),
    '__agent__',
  )
  await screen.findByRole('option', { name: 'agent-fixture' })
  await user.selectOptions(
    screen.getByLabelText('Agent identity'),
    'agent-fixture',
  )
  await user.click(screen.getByRole('button', { name: 'Start' }))
  await waitFor(() => expect(launch).toHaveBeenCalledOnce())
  expect(() => dispatchGuard()).not.toThrow()
  auth.canRunWrite = false
  refresh()
  expect(() => dispatchGuard()).toThrow(
    'This action is no longer permitted with your current permissions; nothing was sent.',
  )
})
it('does not read identities or launch without the NHI read permission', async () => {
  auth.canNhi = false
  mount()
  const user = await chooseProfile()
  await user.selectOptions(
    screen.getByLabelText('Launch identity'),
    '__agent__',
  )
  expect(screen.getByLabelText('Agent identity')).toBeDisabled()
  expect(identities.nhiLifecycle).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
})
it.each(['principal', 'tenant', 'credential'] as const)(
  'discards profile and agent selections on %s change',
  async (change) => {
    const ui = mount()
    const user = await chooseProfile()
    await user.selectOptions(
      screen.getByLabelText('Launch identity'),
      '__agent__',
    )
    await screen.findByRole('option', { name: 'agent-fixture' })
    await user.selectOptions(
      screen.getByLabelText('Agent identity'),
      'agent-fixture',
    )
    if (change === 'principal') auth.principal = 'u2'
    if (change === 'tenant') auth.tenant = 't2'
    if (change === 'credential')
      useSessionStore.setState({ credentialGeneration: 2 })
    ui.refresh()
    expect(screen.getByLabelText('Launch identity')).toHaveValue('')
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    expect(launch).not.toHaveBeenCalled()
  },
)
it('keeps launch disabled when the identity list is unavailable', async () => {
  identities.nhiLifecycle.mockRejectedValue(Error('fixture unavailable'))
  mount()
  const user = await chooseProfile()
  await user.selectOptions(
    screen.getByLabelText('Launch identity'),
    '__agent__',
  )
  await screen.findByText('Agent identity list unavailable.')
  expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
  expect(api.createRun).not.toHaveBeenCalled()
  expect(launch).not.toHaveBeenCalled()
})
