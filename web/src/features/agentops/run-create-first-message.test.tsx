// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The launch dialog used to open on ten fields and no prompt, kept the admitted-agent
// help under "Current human operator", and its disabled button said nothing. It now
// opens on the profile and a first message; every other choice keeps today's default
// under Advanced options; the help follows the choice; and a Start that cannot act
// says what is missing.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
    principal: { user_id: 'u1', aal: 1 },
  }),
}))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentOpsApi: {
    createRun: vi.fn(),
    getRun: vi.fn(),
    listWorkspaces: vi.fn(),
    listProfiles: vi.fn(),
    profileLaunchReadiness: vi.fn(),
    input: vi.fn(),
    inputText: vi.fn(),
  },
}))
const identities = vi.hoisted(() => ({ nhiLifecycle: vi.fn() }))
vi.mock('@/features/identity/api', () => ({ identityApi: identities }))
vi.mock('@/features/workspace-templates/api', () => ({
  templatesApi: { list: vi.fn(), apply: vi.fn() },
  templatesKeys: {
    list: (t: string | null, p?: unknown) => ['tpl', t, 'list', p ?? null],
    detail: (t: string | null, id: string) => ['tpl', t, 'detail', id],
  },
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))
const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', async (orig) => ({
  ...(await orig<typeof import('@tanstack/react-router')>()),
  useNavigate: () => navigate,
}))

import { templatesApi } from '@/features/workspace-templates/api'
import { sentTurnsOf, useSentTurns } from '@/features/sessions/sent-turns'
import { ApiError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { agentOpsApi } from './api'
import { fixtureReadiness } from './launch-readiness.fixture'
import { RunCreateDialog } from './run-create-dialog'
import type { ProviderProfileDTO } from './types'

const homeA: ProviderProfileDTO = {
  profile_ref: 'ppf_a',
  driver: 'claude',
  environment_ref: 'xenv_1',
  display_name: 'Home A',
  state: 'active',
  local_environment: true,
  operable: true,
}
const homeB: ProviderProfileDTO = {
  ...homeA,
  profile_ref: 'ppf_b',
  display_name: 'Home B',
}
const tpl = {
  id: 'tpl-1',
  name: 'Security Audit',
  description: '',
  version: 1,
  author: 'system',
  builtin: true,
  body: {},
  created_at: '',
  updated_at: '',
}

function wrap(initialTemplateId?: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RunCreateDialog
        open
        onOpenChange={vi.fn()}
        initialTemplateId={initialTemplateId}
      />
    </QueryClientProvider>,
  )
}

const start = () => screen.getByRole('button', { name: 'Start' })
const posted = () =>
  vi.mocked(agentOpsApi.createRun).mock.calls[0][0] as unknown as Record<
    string,
    unknown
  >

beforeEach(() => {
  vi.clearAllMocks()
  useSessionStore.setState({ credentialGeneration: 1 })
  useTenantStore.setState({ activeTenant: 't1' })
  vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
    items: [homeA],
    has_more: false,
  })
  vi.mocked(templatesApi.list).mockResolvedValue({
    items: [tpl],
    has_more: false,
  })
  vi.mocked(templatesApi.apply).mockResolvedValue({
    applied: true,
    conflicts: [],
  })
  vi.mocked(agentOpsApi.profileLaunchReadiness).mockImplementation(
    async (ref: string) => fixtureReadiness({ profile_ref: ref }),
  )
  vi.mocked(agentOpsApi.createRun).mockResolvedValue({
    run_ref: 'run_1',
    provider_driver: 'claude',
  } as never)
  vi.mocked(agentOpsApi.input).mockResolvedValue({ accepted: true } as never)
  identities.nhiLifecycle.mockResolvedValue({ items: [], has_more: false })
})

afterEach(() => vi.useRealTimers())

describe('RunCreateDialog — a first message and Start', () => {
  it("opens on the only profile and starts with only a first message, on today's defaults", async () => {
    const user = userEvent.setup()
    wrap()
    await waitFor(() =>
      expect(
        screen.getByRole('combobox', { name: 'Provider profile' }),
      ).toHaveTextContent('Home A'),
    )
    await user.type(
      screen.getByRole('textbox', { name: 'First message (optional)' }),
      'Fix the failing test',
    )
    await waitFor(() => expect(start()).toBeEnabled())
    await user.click(start())
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    // No model chosen, so none is sent, as from the quick form; the engine reads an
    // absent model as an empty one (modules/sessions/runtime_dto.go createRunRequest).
    expect(posted()).toEqual({
      name: 'Fix the failing test',
      transport: 'stream-json',
      permission_mode: 'default',
      effort: '',
      workspace_ref: '',
      isolation: 'native',
      env_allow: [],
      provider_profile_ref: 'ppf_a',
    })
    await waitFor(() =>
      expect(agentOpsApi.input).toHaveBeenCalledWith(
        'run_1',
        JSON.stringify({
          type: 'user',
          message: { role: 'user', content: 'Fix the failing test' },
        }),
        undefined,
        expect.objectContaining({
          tenant: 't1',
          signal: expect.any(AbortSignal),
          dispatchGuard: expect.any(Function),
        }),
      ),
    )
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: '/sessions',
        search: { session: 'run:run_1', pane: 'narrative' },
      }),
    )
    // An admitted agent is never looked up for a launch as the current operator.
    expect(identities.nhiLifecycle).not.toHaveBeenCalled()
    // The dialog stays mounted for the next launch: it starts clean.
    expect(
      screen.getByRole('textbox', { name: 'First message (optional)' }),
    ).toHaveValue('')
    expect(
      screen.getByRole('button', { name: 'Advanced options' }),
    ).toHaveAttribute('aria-expanded', 'false')
  })

  // #500: a launch that waits for an approval answered 202; the dialog sat on Starting…
  // for 15 s posting the message into the waiting run, which refused it every time.
  it('opens a launch that waits for an approval at once, holding its first message', async () => {
    useSentTurns.setState({ byRun: {} })
    const waiting = {
      run_ref: 'run_1',
      provider_driver: 'claude',
      state: 'waiting_approval',
    }
    vi.mocked(agentOpsApi.createRun).mockResolvedValue(waiting as never)
    vi.mocked(agentOpsApi.getRun).mockResolvedValue(waiting as never)
    const user = userEvent.setup()
    wrap()
    await waitFor(() =>
      expect(
        screen.getByRole('combobox', { name: 'Provider profile' }),
      ).toHaveTextContent('Home A'),
    )
    await user.type(
      screen.getByRole('textbox', { name: 'First message (optional)' }),
      'Fix the failing test',
    )
    await waitFor(() => expect(start()).toBeEnabled())
    await user.click(start())
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: '/sessions',
        search: { session: 'run:run_1', pane: 'narrative' },
      }),
    )
    expect(agentOpsApi.input).not.toHaveBeenCalled()
    expect(sentTurnsOf('run_1')).toEqual([
      { text: 'Fix the failing test', waiting: true },
    ])
    // Forgetting the note ends its wait.
    useSentTurns.setState({ byRun: {} })
  })

  it('sends no first message for a start the operator no longer owns', async () => {
    let finish: (run: unknown) => void = () => {}
    vi.mocked(agentOpsApi.createRun).mockReturnValue(
      new Promise((resolve) => {
        finish = resolve
      }) as never,
    )
    const user = userEvent.setup()
    wrap()
    await user.type(
      screen.getByRole('textbox', { name: 'First message (optional)' }),
      'Fix the failing test',
    )
    await waitFor(() => expect(start()).toBeEnabled())
    await user.click(start())
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    act(() => {
      useSessionStore.setState({ credentialGeneration: 99 })
    })
    finish({ run_ref: 'run_1', provider_driver: 'claude' })
    await new Promise((r) => setTimeout(r, 50))
    expect(agentOpsApi.input).not.toHaveBeenCalled()
    expect(navigate).not.toHaveBeenCalled()
  })

  it.each(['credential', 'tenant'])(
    'does not retry the first message after a %s change',
    async (change) => {
      let rejectInput: (error: Error) => void = () => {}
      vi.mocked(agentOpsApi.input).mockReturnValueOnce(
        new Promise((_resolve, reject) => {
          rejectInput = reject
        }),
      )
      const user = userEvent.setup()
      wrap()
      await user.type(
        screen.getByRole('textbox', { name: 'First message (optional)' }),
        'Original prompt',
      )
      await waitFor(() => expect(start()).toBeEnabled())
      await user.click(start())
      await waitFor(() => expect(agentOpsApi.input).toHaveBeenCalledOnce())
      vi.useFakeTimers()
      await act(async () => {
        rejectInput(new ApiError(409, 'conflict', 'Child starting'))
        await vi.advanceTimersByTimeAsync(0)
        if (change === 'credential')
          useSessionStore.setState({ credentialGeneration: 2 })
        else useTenantStore.setState({ activeTenant: 't2' })
        await vi.advanceTimersByTimeAsync(15000)
      })
      expect(agentOpsApi.input).toHaveBeenCalledOnce()
      expect(navigate).not.toHaveBeenCalled()
    },
  )

  it("brings an orchestration profile's agent choice forward, with Advanced options closed", async () => {
    vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
      items: [
        {
          ...homeA,
          session_work_grant: {
            role: 'orchestrator',
            workspace_id: '01a0ef7c-f25c-72f1-9cf8-3b5b2c8d6ab0',
            capabilities: ['work.read'],
            grant_id: 'grant',
          },
        },
      ],
      has_more: false,
    })
    wrap()
    expect(await screen.findByLabelText('Launch identity')).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Advanced options' }),
    ).toHaveAttribute('aria-expanded', 'false')
    await waitFor(() =>
      expect(start()).toHaveAccessibleDescription(
        'An orchestration profile requires an independently authenticated agent identity.',
      ),
    )
  })

  it('keeps an agent identity in view when Advanced options closes', async () => {
    const user = userEvent.setup()
    wrap()
    const toggle = await screen.findByRole('button', {
      name: 'Advanced options',
    })
    await user.click(toggle)
    await user.selectOptions(
      screen.getByLabelText('Launch identity'),
      '__agent__',
    )
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByLabelText('Name')).toBeNull()
    expect(screen.getByLabelText('Launch identity')).toHaveValue('__agent__')
  })

  it('when the profiles cannot be read, Start says so and offers to try again', async () => {
    vi.mocked(agentOpsApi.listProfiles)
      .mockRejectedValueOnce(new ApiError(503, 'unavailable', 'down'))
      .mockResolvedValueOnce({ items: [homeA], has_more: false })
    const user = userEvent.setup()
    wrap()
    await waitFor(() =>
      expect(start()).toHaveAccessibleDescription(
        'Provider profiles could not be read. Retry',
      ),
    )
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(start()).toBeEnabled())
  })

  it('when the requirements cannot be read, Start says so and offers to read them again', async () => {
    vi.mocked(agentOpsApi.profileLaunchReadiness)
      .mockRejectedValueOnce(new ApiError(503, 'unavailable', 'down'))
      .mockResolvedValueOnce(fixtureReadiness())
    const user = userEvent.setup()
    wrap()
    await waitFor(() =>
      expect(start()).toHaveAccessibleDescription(
        "The profile's requirements could not be read. Read requirements again",
      ),
    )
    await user.click(
      screen.getByRole('button', { name: 'Read requirements again' }),
    )
    await waitFor(() => expect(start()).toBeEnabled())
  })

  it('opens with the focus in the first message', async () => {
    wrap()
    await waitFor(() =>
      expect(
        screen.getByRole('textbox', { name: 'First message (optional)' }),
      ).toHaveFocus(),
    )
  })

  it('without a first message, starts and sends nothing more', async () => {
    const user = userEvent.setup()
    wrap()
    await waitFor(() => expect(start()).toBeEnabled())
    await user.click(start())
    await waitFor(() => expect(navigate).toHaveBeenCalledOnce())
    expect(posted().name).toBe('')
    expect(agentOpsApi.input).not.toHaveBeenCalled()
    expect(agentOpsApi.inputText).not.toHaveBeenCalled()
  })

  it("keeps every other choice under Advanced options, each on today's default", async () => {
    const user = userEvent.setup()
    wrap()
    await screen.findByRole('combobox', { name: 'Provider profile' })
    for (const label of [
      'Launch identity',
      'Name',
      'Transport',
      'Permission mode',
      'Effort',
      'Model',
      'Folder',
      'Template',
      'Forward host env (names)',
    ])
      expect(screen.queryByLabelText(label)).toBeNull()
    const toggle = screen.getByRole('button', { name: 'Advanced options' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByLabelText('Launch identity')).toHaveValue('')
    expect(screen.getByLabelText('Name')).toHaveValue('')
    expect(screen.getByLabelText('Transport')).toHaveTextContent(
      'Governed (stream-json)',
    )
    expect(screen.getByLabelText('Permission mode')).toHaveTextContent(
      'default',
    )
    expect(screen.getByLabelText('Effort')).toHaveTextContent('Model default')
    expect(screen.getByRole('combobox', { name: 'Model' })).toBeInTheDocument()
    expect(screen.getByLabelText('Folder')).toHaveTextContent(
      'Temporary folder for this session',
    )
    expect(await screen.findByLabelText('Template')).toHaveTextContent(
      'No template',
    )
    expect(screen.getByPlaceholderText('PATH, HOME, TERM')).toHaveValue('')
    // The requirements of the chosen profile are there too, not up front.
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
  })

  it('says under the identity what the chosen identity means', async () => {
    const user = userEvent.setup()
    wrap()
    await user.click(
      await screen.findByRole('button', { name: 'Advanced options' }),
    )
    const human = 'The session runs with your own access.'
    const agent =
      'Choose an admitted agent to act on behalf of its human sponsor. Human access alone grants no agent authority.'
    expect(screen.getByText(human)).toBeInTheDocument()
    expect(screen.queryByText(agent)).toBeNull()
    await user.selectOptions(
      screen.getByLabelText('Launch identity'),
      '__agent__',
    )
    expect(screen.getByText(agent)).toBeInTheDocument()
    expect(screen.queryByText(human)).toBeNull()
    await user.selectOptions(screen.getByLabelText('Launch identity'), '')
    expect(screen.getByText(human)).toBeInTheDocument()
    expect(screen.queryByText(agent)).toBeNull()
  })

  it('names the missing admitted agent at Start', async () => {
    const user = userEvent.setup()
    wrap()
    await user.click(
      await screen.findByRole('button', { name: 'Advanced options' }),
    )
    await user.selectOptions(
      screen.getByLabelText('Launch identity'),
      '__agent__',
    )
    expect(start()).toBeDisabled()
    expect(start()).toHaveAccessibleDescription(
      'Choose the admitted agent this session runs as, or run it as yourself.',
    )
  })

  it('with several profiles chooses none, and the disabled Start says to choose one', async () => {
    vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
      items: [homeA, homeB],
      has_more: false,
    })
    const user = userEvent.setup()
    wrap()
    await waitFor(() =>
      expect(start()).toHaveAccessibleDescription('Choose a provider profile.'),
    )
    expect(start()).toBeDisabled()
    expect(agentOpsApi.profileLaunchReadiness).not.toHaveBeenCalled()
    await user.click(screen.getByLabelText('Provider profile'))
    await user.click(await screen.findByRole('option', { name: /Home B/ }))
    await waitFor(() => expect(start()).toBeEnabled())
    await user.click(start())
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(posted().provider_profile_ref).toBe('ppf_b')
  })

  it('with no profile, the disabled Start says so and offers Providers', async () => {
    vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
      items: [],
      has_more: false,
    })
    wrap()
    await waitFor(() =>
      expect(start()).toHaveAccessibleDescription(
        'No provider profile yet. Add a provider first. Open Providers',
      ),
    )
    expect(start()).toBeDisabled()
    expect(
      screen.getByRole('link', { name: 'Open Providers' }),
    ).toHaveAttribute('href', '/providers')
  })

  it('names what the chosen profile is missing when it cannot start', async () => {
    vi.mocked(agentOpsApi.profileLaunchReadiness).mockResolvedValue(
      fixtureReadiness({
        configuration_state: 'not_configured',
        checks: fixtureReadiness().checks.map((c) =>
          c.check === 'program'
            ? {
                check: 'program',
                state: 'not_configured',
                code: 'program_missing',
                remediation: 'configure_provider_program',
              }
            : c,
        ),
      }),
    )
    wrap()
    await waitFor(() =>
      expect(start()).toHaveAccessibleDescription(
        /^The resolved program is not present\..*Configure the official provider CLI on this runtime\.$/,
      ),
    )
    expect(start()).toBeDisabled()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })

  it('a template opened from the catalog shows its choice at once', async () => {
    wrap('tpl-1')
    const toggle = await screen.findByRole('button', {
      name: 'Advanced options',
    })
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const template = await screen.findByLabelText('Template')
    await waitFor(() => expect(template).toHaveTextContent('Security Audit'))
    expect(within(template).queryByText('No template')).toBeNull()
  })
})
