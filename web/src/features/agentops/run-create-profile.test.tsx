// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the launch dialog is where a session gets a provider PROFILE. What leaves the
// browser is the profile reference and nothing else: the homes are resolved and
// validated by the engine, which is why a client cannot post itself a home. The only
// active profile is preselected (defaults follow what exists); with
// several none is, and the launch waits for a choice. A profile this node cannot
// launch is offered as what it is: not launchable.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    agentOpsApi: {
      createRun: vi.fn(),
      listWorkspaces: vi.fn(),
      listProfiles: vi.fn(),
      profileLaunchReadiness: vi.fn(),
    },
  }
})
vi.mock('@/features/workspace-templates/api', () => ({
  templatesApi: { list: vi.fn(), apply: vi.fn() },
  templatesKeys: {
    list: (t: string | null, p?: unknown) => ['tpl', t, 'list', p ?? null],
    detail: (t: string | null, id: string) => ['tpl', t, 'detail', id],
  },
}))
vi.mock('@tanstack/react-router', async (orig) => ({
  ...(await orig<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

import { templatesApi } from '@/features/workspace-templates/api'
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
const codexHome: ProviderProfileDTO = {
  profile_ref: 'ppf_codex',
  driver: 'codex',
  environment_ref: 'xenv_1',
  display_name: 'Codex home',
  state: 'active',
  local_environment: true,
  operable: false,
}

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RunCreateDialog open onOpenChange={vi.fn()} />
    </QueryClientProvider>,
  )
}

const postedBody = () =>
  vi.mocked(agentOpsApi.createRun).mock.calls[0][0] as unknown as Record<
    string,
    unknown
  >

/** Every choice but the profile and the first message, and the
 * profile's requirements in full, are under Advanced options. */
async function openAdvanced(user = userEvent.setup()) {
  const toggle = await screen.findByRole('button', { name: 'Advanced options' })
  if (toggle.getAttribute('aria-expanded') !== 'true') await user.click(toggle)
}
async function waitReadyToRequest() {
  await openAdvanced()
  expect(
    await screen.findByText('Local requirements checked'),
  ).toBeInTheDocument()
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
    items: [homeA, codexHome],
    has_more: false,
  })
  vi.mocked(templatesApi.list).mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.mocked(agentOpsApi.createRun).mockResolvedValue({} as never)
  vi.mocked(agentOpsApi.profileLaunchReadiness).mockImplementation(
    async (ref: string) =>
      fixtureReadiness({
        profile_ref: ref,
        configuration_state: ref === 'ppf_codex' ? 'not_configured' : 'ready',
      }),
  )
})

describe('RunCreateDialog — provider profiles', () => {
  it.each([
    {
      label: 'Folder',
      choice: /Workspace A/,
      none: 'Temporary folder for this session',
    },
    { label: 'Template', choice: /Security Audit/, none: 'No template' },
  ])(
    'can clear an optional $label after choosing it while retaining the required profile',
    async ({ label, choice, none }) => {
      vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
        items: [
          {
            workspace_ref: 'ws-a',
            name: 'Workspace A',
            state: 'active',
            root_path: '/fixture',
            mount_mode: 'ro',
            max_read_bytes: 1024,
            dlp_mode: 'off',
          },
        ],
        has_more: false,
      })
      vi.mocked(templatesApi.list).mockResolvedValue({
        items: [
          {
            id: 'tpl-1',
            name: 'Security Audit',
            description: '',
            version: 1,
            author: 'system',
            builtin: true,
            body: {},
            created_at: '',
            updated_at: '',
          },
        ],
        has_more: false,
      })
      vi.mocked(templatesApi.apply).mockResolvedValue({
        applied: true,
        conflicts: [],
      })
      const user = userEvent.setup()
      wrap()
      await user.click(await screen.findByLabelText('Provider profile'))
      await user.click(await screen.findByRole('option', { name: /Home A/ }))
      await waitReadyToRequest()
      await user.click(await screen.findByLabelText(label))
      await user.click(await screen.findByRole('option', { name: choice }))
      expect(screen.getByLabelText(label)).toHaveTextContent(choice)
      await user.click(screen.getByLabelText(label))
      await user.click(await screen.findByRole('option', { name: none }))
      expect(screen.getByLabelText(label)).toHaveTextContent(none)
      await user.click(screen.getByLabelText('Provider profile'))
      expect(
        await screen.findByRole('option', { name: 'Select a profile' }),
      ).toHaveAttribute('data-disabled')
      await user.keyboard('{Escape}')
      await user.click(screen.getByRole('button', { name: 'Start' }))
      await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
      const body = postedBody()
      expect(body.workspace_ref).toBe('')
      expect(body).not.toHaveProperty('template_id')
      expect(body.provider_profile_ref).toBe(homeA.profile_ref)
    },
  )

  it('launches in the explicitly selected folder', async () => {
    vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
      items: [
        {
          workspace_ref: 'folder-a',
          name: 'Project folder',
          state: 'active',
          root_path: '/project',
          mount_mode: 'ro',
          max_read_bytes: 1024,
          dlp_mode: 'off',
        },
      ],
      has_more: false,
    })
    const user = userEvent.setup()
    wrap()
    await user.click(await screen.findByLabelText('Provider profile'))
    await user.click(await screen.findByRole('option', { name: /Home A/ }))
    await waitReadyToRequest()
    await user.click(screen.getByLabelText('Folder'))
    await user.click(
      await screen.findByRole('option', { name: /Project folder/ }),
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('folder-a')
  })

  it('requires a profile before launching, even when profiles are available', async () => {
    wrap()
    await waitFor(() =>
      expect(agentOpsApi.listProfiles).toHaveBeenCalledWith(
        { state: 'active', limit: 200 },
        expect.anything(),
      ),
    )
    await userEvent.click(screen.getByRole('button', { name: 'Start' }))
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })

  // HU 043: a Grok Build profile opened "New Claude Code session". The title names the
  // tool of the profile chosen, and no tool until one is.
  it('the title names the chosen tool, and no tool before a profile is chosen', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
      items: [
        homeA,
        {
          ...homeA,
          profile_ref: 'ppf_grok',
          driver: 'grok',
          display_name: 'Grok home',
        },
      ],
      has_more: false,
    })
    wrap()
    expect(
      await screen.findByRole('heading', { name: 'New session' }),
    ).toBeInTheDocument()
    await user.click(await screen.findByLabelText('Provider profile'))
    await user.click(await screen.findByRole('option', { name: /Grok home/ }))
    expect(
      await screen.findByRole('heading', { name: 'New Grok Build session' }),
    ).toBeInTheDocument()
    expect(screen.queryByText(/New Claude Code session/)).toBeNull()
  })

  it('posts only the profile REFERENCE when one is chosen — never a home', async () => {
    const user = userEvent.setup()
    wrap()
    await user.click(await screen.findByLabelText('Provider profile'))
    await user.click(await screen.findByRole('option', { name: /Home A/ }))
    await waitReadyToRequest()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalled())
    const body = postedBody()
    expect(body.provider_profile_ref).toBe('ppf_a')
    expect(body).not.toHaveProperty('config_home')
    expect(body).not.toHaveProperty('user_home')
    expect(body).not.toHaveProperty('provider_home')
    expect(body).not.toHaveProperty('environment_ref')
  })

  it('lets a not-enabled profile be selected and then blocks a known-inviable request', async () => {
    const user = userEvent.setup()
    wrap()
    await user.click(await screen.findByLabelText('Provider profile'))
    const codex = await screen.findByRole('option', { name: /Codex home/ })
    expect(codex).toHaveTextContent('not enabled in this environment')
    expect(codex).not.toHaveAttribute('data-disabled')
    await user.click(codex)
    // Start says it beside the button, in plain words.
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Start' }),
      ).toHaveAccessibleDescription(
        'This profile cannot start a session here. Its requirements under Advanced options say why.',
      ),
    )
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })

  // ⛔ ALL FOUR, NOT THE CURRENT DRIVER'S TWO. The engine owns the whole provider-home
  // family (providerHomeEnvName) for every launch, because a Claude launch that
  // accepted CODEX_HOME would hand the child a home nobody authorized for a provider
  // nobody selected. The dialog checked only HOME and CLAUDE_CONFIG_DIR, so a Codex
  // operator forwarding CODEX_HOME passed prevalidation and collected an avoidable
  // 400. Each name is asserted on its own: one case that types all four at once would
  // still pass with three of them unimplemented.
  it.each(['HOME', 'CLAUDE_CONFIG_DIR', 'CODEX_HOME', 'GROK_HOME'])(
    'refuses to forward %s under a profile before the engine has to',
    async (owned) => {
      const user = userEvent.setup()
      wrap()
      await user.click(await screen.findByLabelText('Provider profile'))
      await user.click(await screen.findByRole('option', { name: /Home A/ }))
      await waitReadyToRequest()
      await user.type(
        screen.getByPlaceholderText('PATH, HOME, TERM'),
        `PATH, ${owned}`,
      )
      expect(
        await screen.findByText(
          /CLAUDE_CONFIG_DIR, CODEX_HOME and GROK_HOME belong to the profile/i,
        ),
      ).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    },
  )

  // ⛔ EVERY NAME HERE IS ONE THE REAL SERVER ACCEPTS, and that is not a detail of the
  // fixture. This transport is MOCKED, so a positive case can only ever prove what the
  // browser POSTS — it cannot prove the launch succeeds. The first version of this case
  // used CODEX_MODEL, which the engine reserves along with every other `CODEX_*` name
  // (forbiddenInheritedEnvName), so it certified a browser/server mismatch as "ordinary"
  // and would have let an operator through the dialog into an avoidable 400.
  //
  // The same list is exercised against the ACTUAL server, over authenticated HTTP, by
  // TestEnvAllowPositiveNamesTheDialogAdmitsAreTheOnesTheServerAdmits in
  // modules/sessions/runtime_console_contract_test.go. Keep the two lists identical: the
  // pair is what makes either half worth anything, and neither is inferred from the other.
  const SERVER_PERMITTED = [
    'PATH',
    'TERM',
    'MY_PROJECT_FLAG',
    // Prefix-collision witnesses. The profile-owned check is on the EXACT name, so a
    // name that merely starts with one must pass; a `startsWith` implementation would
    // fail here. CLAUDE_CONFIG_DIR_EXTRA also witnesses the server's own rule, which is
    // exact for CLAUDE_CONFIG_DIR and a PREFIX only for CLAUDE_CODE_.
    'HOMEBREW_PREFIX',
    'HOME_BACKUP',
    'CLAUDE_CONFIG_DIR_EXTRA',
  ]

  it('admits the ordinary names the server admits: no warning, and the launch goes', async () => {
    // The positive control the four negatives need. A prevalidation that refused
    // everything would pass every case above and make env_allow useless. Listing all
    // six in one case is right for this direction: it fails if ANY of them is wrongly
    // refused, which is the whole claim.
    const user = userEvent.setup()
    wrap()
    await user.click(await screen.findByLabelText('Provider profile'))
    await user.click(await screen.findByRole('option', { name: /Home A/ }))
    await waitReadyToRequest()
    await user.type(
      screen.getByPlaceholderText('PATH, HOME, TERM'),
      SERVER_PERMITTED.join(', '),
    )
    expect(screen.queryByText(/belong to the profile/i)).toBeNull()
    const submit = screen.getByRole('button', { name: 'Start' })
    expect(submit).toBeEnabled()
    await user.click(submit)
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalled())
    expect(postedBody().env_allow).toEqual(SERVER_PERMITTED)
  })

  it('does not pretend to be the server: it warns about the four it owns, and no more', async () => {
    // The dialog is a PREVALIDATION of the profile-owned family, not a second copy of
    // the engine's reserved-name rule. CODEX_MODEL is reserved by the server and this
    // dialog does not warn about it — stated as a limit rather than left to be
    // discovered, because the locale message must not claim otherwise either.
    const user = userEvent.setup()
    wrap()
    await user.click(await screen.findByLabelText('Provider profile'))
    await user.click(await screen.findByRole('option', { name: /Home A/ }))
    await waitReadyToRequest()
    await user.type(
      screen.getByPlaceholderText('PATH, HOME, TERM'),
      'PATH, CODEX_MODEL',
    )
    expect(screen.queryByText(/belong to the profile/i)).toBeNull()
    expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled()
    // And the message the operator does see must not promise that everything else is
    // accepted, because this is exactly the name for which that promise is false.
    await user.clear(screen.getByPlaceholderText('PATH, HOME, TERM'))
    await user.type(screen.getByPlaceholderText('PATH, HOME, TERM'), 'HOME')
    const warning = await screen.findByText(/belong to the profile/i)
    expect(warning).toHaveTextContent(/subject to the server/i)
    expect(warning).not.toHaveTextContent(/other variables are fine/i)
  })
})

it('preserves an entered model in the existing native launch payload', async () => {
  const user = userEvent.setup()
  wrap()
  await user.click(await screen.findByLabelText('Provider profile'))
  await user.click(await screen.findByRole('option', { name: /Home A/ }))
  await waitReadyToRequest()
  await user.type(
    screen.getByRole('combobox', { name: 'Model' }),
    'custom-model',
  )
  await user.click(screen.getByRole('button', { name: 'Start' }))
  await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
  expect(postedBody().model).toBe('custom-model')
  expect(postedBody().provider_profile_ref).toBe('ppf_a')
})
