// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The setup wizard is three steps — install a tool, sign it in, start a session —
// and nothing in it is blocked behind a step-up or demands optional work.
import { FEATURE_EXTENSIONS } from '@/features/extensions'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const business = FEATURE_EXTENSIONS.some((view) => view.id === 'finops')
const auth = vi.hoisted(() => ({ admin: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => auth.admin, isSuperadmin: auth.admin }),
}))
const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useRouterState: () => '',
  // The policy page's Tabs strip consults useRouter; there is no RouterProvider here.
  useRouter: () => undefined,
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))
// The policy page's panels are stubbed: what is under test is which tab the wizard's link
// opens and whether that tab is gated, not what an authoring panel renders.
vi.mock('@/features/recordings/recording-notice', () => ({
  RecordingNotice: () => null,
}))
vi.mock('@/features/claude-policy/policy-authoring-panel', () => ({
  PolicyAuthoringPanel: ({ surface }: { surface: string }) => (
    <div>PolicyAuthoringPanel {surface} mounted</div>
  ),
}))
vi.mock('@/features/claude-policy/cedar-opa-view', () => ({
  CedarOpaView: () => <div>CedarOpaView mounted</div>,
}))
vi.mock('@/features/claude-policy/managed-agents-hitl', () => ({
  ManagedAgentsHitl: () => <div>ManagedAgentsHitl mounted</div>,
}))
// One sign-in status per session tool the engine drives, so none answers undefined.
const status = vi.hoisted(() => ({
  claude: { driver: 'claude', installed: false, signed_in: false },
  codex: { driver: 'codex', installed: false, signed_in: false },
  grok: { driver: 'grok', installed: false, signed_in: false },
  opencode: { driver: 'opencode', installed: false, signed_in: false },
  'gemini-cli': { driver: 'gemini-cli', installed: false, signed_in: false },
}))
const launch = vi.hoisted(() => ({ launchSession: vi.fn() }))
vi.mock('@/features/agentops/session-launch', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  launchSession: launch.launchSession,
}))
vi.mock('@/features/first-hour/api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    signInApi: {
      status: (d: SessionTool) => Promise.resolve(status[d]),
      start: vi.fn(),
      get: vi.fn(),
      code: vi.fn(),
      cancel: vi.fn(),
    },
  }
})

import { agentOpsApi } from '@/features/agentops/api'
import type { ResolveProviderSource } from '@/features/agentops/types'
import { signInApi, type SessionTool } from '@/features/first-hour/api'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import { NewSessionDialog } from '@/features/first-hour/first-hour'
import ClaudePolicyView from '@/features/claude-policy/claude-policy-view'
import { useModulesStore } from '@/stores/modules'
import { OnboardingView } from './onboarding-view'

const DISMISS_KEY = 'olivares.onboarding.dismissed'

/** The engine's answer: the tool runs on this key (refused at its last test, or not). */
function runsOnKey(
  driver: SessionTool,
  provider: ResolveProviderSource,
  refused = false,
) {
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
    readinessOf({ [driver]: { provider, refused } }),
  )
}

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  localStorage.clear()
  useModulesStore.getState().setOff([])
  auth.admin = true
  for (const s of Object.values(status)) {
    s.installed = false
    s.signed_in = false
  }
  // What a tool runs on is the engine's answer (GET provider-profiles/readiness): its
  // own login once the stub says so; every other tool has nothing to run on yet.
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockImplementation(async () =>
    readinessOf(
      Object.fromEntries(
        Object.values(status)
          .filter((s) => s.installed && s.signed_in)
          .map((s) => [s.driver, 'own_login' as const]),
      ),
    ),
  )
})

describe('the setup wizard', () => {
  it('is three steps, and a fresh install offers to install a tool', async () => {
    wrap(<OnboardingView />)
    expect(screen.getByText('Install an agent tool')).toBeInTheDocument()
    expect(screen.getByText('Sign it in')).toBeInTheDocument()
    expect(screen.getByText('Start a session')).toBeInTheDocument()
    // Both cards, however their two status answers are batched into renders.
    await waitFor(() =>
      expect(screen.getAllByRole('button', { name: 'Install' })).toHaveLength(
        2,
      ),
    )
    expect(screen.queryByText(/step-up|AAL3|passkey/i)).not.toBeInTheDocument()
  })

  // The 26.10.0 wizard showed seven steps, three of them behind a passkey, for a
  // workspace that already existed, a second administrator, a source and a
  // managed-settings policy. None of that is needed for the first session.
  it('asks only for what the first session needs; the rest are optional links', async () => {
    const { container } = wrap(<OnboardingView />)
    expect(await screen.findByText('0 of 3 done')).toBeInTheDocument()
    expect(
      screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent),
    ).toEqual([
      'Install an agent tool',
      'Sign it in',
      'Start a session',
      'Next, when you need them',
    ])
    expect(container.textContent).not.toMatch(
      /first workspace|invite an administrator|first source|policy enforcement|managed-settings|provider profile|privileged read|audit ledger|step-up|\bAAL\d|passkey|security key|\bpending\b|of \d+ verified/i,
    )
    for (const [name, href] of [
      ['Invite people', '/console?tab=people'],
      ['Connect an identity provider', '/console?tab=sso'],
      ['Set policies', '/claude-policy?tab=managed-settings'],
      ['Add MCP servers', '/console?tab=mcpGateway'],
      [
        business ? 'Set budgets' : 'Budgets',
        business ? '/finops' : '/stored-budgets',
      ],
    ])
      expect(screen.getByRole('link', { name })).toHaveAttribute('href', href)
  })

  // A fresh install runs without Cost (finops), whose page then shows only "Cost is not
  // enabled on this installation": the wizard does not offer a page with nothing on it.
  it('does not offer a next step whose module is off', () => {
    useModulesStore.getState().setOff(['finops', 'claude-policy'])
    wrap(<OnboardingView />)
    const next = screen.getByRole('region', {
      name: 'Next, when you need them',
    })
    expect(
      within(next)
        .getAllByRole('link')
        .map((a) => a.textContent),
    ).toEqual([
      'Invite people',
      'Connect an identity provider',
      'Add MCP servers',
    ])
  })

  // A fresh install runs without Security, and the policy page's default tab (Drift &
  // posture) reads Security's findings, so a bare /claude-policy showed only "Security is
  // not enabled on this installation" and a button that restarts the engine (#473).
  it('opens Set policies on a tab that works while Security is off', () => {
    useModulesStore.getState().setOff(['security'])
    wrap(<OnboardingView />)
    const href = screen
      .getByRole('link', { name: 'Set policies' })
      .getAttribute('href')
    cleanup()

    const was = window.location.href
    window.history.replaceState({}, '', href)
    try {
      const { container } = wrap(<ClaudePolicyView />)
      expect(
        container.querySelector('[data-slot="module-not-enabled"]'),
      ).toBeNull()
      expect(screen.getByText(/PolicyAuthoringPanel .* mounted/)).toBeVisible()
    } finally {
      window.history.replaceState({}, '', was)
    }
  })

  // A key or a local model needs no Claude Code or Codex install first. Each link opens
  // the same Providers form as a tool's "Use an API key instead", which brings the
  // person back here once the new provider passes its test.
  it('offers an API key or a local model before any tool is installed', async () => {
    wrap(<OnboardingView />)
    const install = screen.getByRole('region', {
      name: 'Install an agent tool',
    })
    expect(
      await within(install).findByRole('link', { name: 'Add an API key' }),
    ).toHaveAttribute(
      'href',
      expect.stringMatching(/^\/providers\?add=anthropic&returnTo=/),
    )
    expect(
      within(install).getByRole('link', { name: 'Add a local model' }),
    ).toHaveAttribute(
      'href',
      expect.stringMatching(/^\/providers\?add=ollama&returnTo=/),
    )
  })

  it('offers the tool its own sign-in once it is installed', async () => {
    status.claude = { driver: 'claude', installed: true, signed_in: false }
    wrap(<OnboardingView />)
    expect(
      await screen.findByRole('button', { name: 'Sign in with Claude' }),
    ).toBeInTheDocument()
    // HU2-18: the key form opens on the provider this tool runs on.
    expect(screen.getByText('Use an API key instead')).toHaveAttribute(
      'href',
      expect.stringMatching(/^\/providers\?add=anthropic&returnTo=/),
    )
    // A local model stays offered until a tool can start a session.
    expect(
      await screen.findByRole('link', { name: 'Add a local model' }),
    ).toHaveAttribute(
      'href',
      expect.stringMatching(/^\/providers\?add=ollama&/),
    )
  })

  it('names the key the engine will use when the tool is not signed in (HU 030)', async () => {
    status.claude = { driver: 'claude', installed: true, signed_in: false }
    runsOnKey('claude', {
      provider_ref: 'prv_a',
      kind: 'anthropic',
      display_name: 'Team key',
    })
    wrap(<OnboardingView />)
    expect(
      await screen.findByText('Uses your API key: Team key'),
    ).toBeInTheDocument()
    expect(
      await screen.findByRole('button', { name: 'Start' }),
    ).toBeInTheDocument()
  })

  // EU on RC10: a keyless local Ollama endpoint read "Uses your API key: EU local Ollama".
  it('names a local model server as one, not as an API key', async () => {
    status.codex = { driver: 'codex', installed: true, signed_in: false }
    runsOnKey('codex', {
      provider_ref: 'prv_local',
      kind: 'ollama',
      display_name: 'EU local Ollama',
    })
    wrap(<OnboardingView />)
    expect(
      await screen.findByText('Uses the local model server: EU local Ollama'),
    ).toBeInTheDocument()
    expect(screen.queryByText(/Uses your API key/)).toBeNull()
  })

  // HU2-17: after the key test said "Refused", the wizard counted step 2 as done and the
  // row said "Uses your API key".
  it('says the key was refused, and does not count it as signed in', async () => {
    status.claude = { driver: 'claude', installed: true, signed_in: false }
    runsOnKey(
      'claude',
      {
        provider_ref: 'prv_a',
        kind: 'anthropic',
        display_name: 'Team key',
      },
      true,
    )
    wrap(<OnboardingView />)
    expect(
      await screen.findByText(
        'The API key Team key was refused. Replace it under API keys.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText('Uses your API key: Team key')).toBeNull()
    expect(screen.getByText('1 of 3 done')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Start' })).toBeNull()
  })

  // Root 19:15Z (HU2 003): a session that failed counted as "Start a session" done.
  it('waits while it reads what the tools run on, instead of saying none is ready', async () => {
    status.codex = { driver: 'codex', installed: true, signed_in: false }
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockImplementation(
      () => new Promise(() => {}),
    )
    wrap(<OnboardingView />)
    expect(
      await screen.findAllByRole('status', { name: /^Loading/ }),
    ).not.toHaveLength(0)
    expect(screen.queryByText(/of 3 done/)).toBeNull()
    expect(screen.queryByText('Install and sign in a tool first.')).toBeNull()
    expect(screen.queryByRole('link', { name: 'Add a local model' })).toBeNull()
  })

  it('counts a tool that can start a session as installed, whichever it is', async () => {
    // Claude Code and Codex are not installed; OpenCode runs on the local Ollama.
    runsOnKey('opencode', {
      provider_ref: 'prv_ollama',
      kind: 'ollama',
    })
    wrap(<OnboardingView />)
    expect(await screen.findByText('2 of 3 done')).toBeInTheDocument()
    expect(screen.queryByText('Install a tool first.')).toBeNull()
    // Something can start a session: no other way to run one is offered.
    expect(screen.queryByRole('link', { name: 'Add a local model' })).toBeNull()
  })

  it.each([
    ['opencode', 'ok'],
    ['grok', 'ok'],
    ['opencode', 'refused'],
    ['grok', 'refused'],
  ] as const)(
    'shows installed %s while its provider is checked, then reports %s readiness',
    async (driver, probeState) => {
      const toolStatus = vi
        .spyOn(signInApi, 'status')
        .mockImplementation(async (tool) => ({
          driver: tool,
          installed: tool === driver,
          signed_in: false,
        }))
      let finishCheck!: () => void
      const checking = new Promise<void>((resolve) => {
        finishCheck = resolve
      })
      vi.spyOn(agentOpsApi, 'toolsReadiness').mockImplementation(async () => {
        await checking
        return readinessOf({
          [driver]: {
            provider: {
              provider_ref: 'prv_local',
              kind: 'ollama',
              display_name: 'Local model',
            },
            refused: probeState === 'refused',
          },
        })
      })
      wrap(<OnboardingView />)
      const install = screen.getByRole('region', {
        name: 'Install an agent tool',
      })
      const card = await within(install).findByTestId(`tool-${driver}`)
      expect(await within(card).findByText('Installed')).toBeInTheDocument()
      expect(screen.queryByText('Install a tool first.')).toBeNull()
      expect(screen.queryByRole('button', { name: 'Start' })).toBeNull()
      finishCheck()
      const ready = probeState === 'ok'
      expect(
        await screen.findByText(`${ready ? 2 : 1} of 3 done`),
      ).toBeInTheDocument()
      const signIn = screen.getByRole('region', { name: 'Sign it in' })
      expect(within(signIn).getByTestId(`tool-${driver}`)).toBeInTheDocument()
      if (ready) {
        expect(
          within(signIn).getByText('Uses the local model server: Local model'),
        ).toBeInTheDocument()
        expect(
          screen.getByRole('button', { name: 'Start' }),
        ).toBeInTheDocument()
      } else {
        expect(
          within(signIn).getByText(/Local model was refused/),
        ).toBeInTheDocument()
        expect(screen.queryByRole('button', { name: 'Start' })).toBeNull()
      }
      toolStatus.mockRestore()
    },
  )

  it('does not count a failed session as started', async () => {
    status.codex = { driver: 'codex', installed: true, signed_in: true }
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
      items: [{ run_ref: 'r1', state: 'failed' }],
      has_more: false,
    } as never)
    wrap(<OnboardingView />)
    expect(await screen.findByText('2 of 3 done')).toBeInTheDocument()
  })

  it('starts a session once a tool is signed in', async () => {
    status.codex = { driver: 'codex', installed: true, signed_in: true }
    wrap(<OnboardingView />)
    expect(
      await screen.findByRole('button', { name: 'Start' }),
    ).toBeInTheDocument()
    // The folder is filled in, not typed (a fresh install: a new folder of its own).
    expect(
      await screen.findByText('A new folder for this session'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Folder' })).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Change folder' }),
    ).toBeInTheDocument()
    expect(
      screen.getByLabelText('First message (optional)'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Add an API key' })).toBeNull()
  })

  it('lists the optional areas as links, not pending steps', () => {
    wrap(<OnboardingView />)
    expect(screen.getByRole('link', { name: 'Invite people' })).toHaveAttribute(
      'href',
      '/console?tab=people',
    )
    expect(screen.queryByText(/pending/i)).not.toBeInTheDocument()
  })

  it('is forbidden to a non-administrator', () => {
    auth.admin = false
    wrap(<OnboardingView />)
    expect(screen.queryByText('Install an agent tool')).not.toBeInTheDocument()
  })

  it('remembers Dismiss and opens again', async () => {
    const user = userEvent.setup()
    wrap(<OnboardingView />)
    await user.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(localStorage.getItem(DISMISS_KEY)).toBe('true')
    await user.click(
      screen.getByRole('button', { name: 'Open the setup guide' }),
    )
    expect(localStorage.getItem(DISMISS_KEY)).toBeNull()
    expect(screen.getByText('Install an agent tool')).toBeInTheDocument()
  })
})

// 09 IP journey: with Claude Code ready and Codex refused, the open New session
// dialog asked GET resolve for Codex about 1,700 times and never showed its form.
// A refused tool is asked again when the form mounts its query, and that refetch
// put the dialog back to loading, which unmounted the form: one request per round
// trip. It waits only for the first answer, now one read for every tool.
describe('the New session dialog', () => {
  it('shows the form with one tool ready and asks again at most once', async () => {
    status.claude = { driver: 'claude', installed: true, signed_in: true }
    const preview = vi.mocked(agentOpsApi.toolsReadiness)
    const before = preview.mock.calls.length
    wrap(<NewSessionDialog open onOpenChange={() => {}} />)
    expect(
      await screen.findByRole('button', { name: 'Start' }),
    ).toBeInTheDocument()
    await new Promise((r) => setTimeout(r, 300))
    const asked = preview.mock.calls.length - before
    await new Promise((r) => setTimeout(r, 300))
    expect(preview.mock.calls.length - before).toBe(asked)
    // One read answers every tool the engine drives: at most the first and one refetch.
    expect(asked).toBeLessThanOrEqual(2)
    expect(screen.getByRole('button', { name: 'Start' })).toBeInTheDocument()
  })
})

// HU 043: the simple dialog had the presets and no tool choice, so Grok Build or OpenCode
// could not be started from it. Every tool the engine drives is offered; one that cannot
// start says why, in the engine's own sentence; the presets are the same for every tool.
describe('the New session dialog: tool and preset', () => {
  // Root, 09b capture review: three "Install X first" lines showed at once. Only the
  // chosen tool's reason shows, with the way to AI tools; Start waits for a ready tool.
  it('offers every tool; the chosen one that cannot start says why, once', async () => {
    const user = userEvent.setup()
    status.claude = { driver: 'claude', installed: true, signed_in: true }
    wrap(<NewSessionDialog open onOpenChange={() => {}} />)
    const tools = await screen.findByRole('radiogroup', { name: 'Tool' })
    expect(
      [...tools.querySelectorAll('[role="radio"]')].map((r) => r.textContent),
    ).toEqual(['Claude Code', 'Codex', 'Grok Build', 'OpenCode', 'Gemini CLI'])
    expect(screen.getByRole('radio', { name: 'Claude Code' })).toHaveAttribute(
      'aria-checked',
      'true',
    )
    expect(screen.queryByText(/nothing to run on yet/)).toBeNull()
    await user.click(screen.getByRole('radio', { name: 'Grok Build' }))
    expect(screen.getAllByText(/nothing to run on yet/)).toHaveLength(1)
    expect(
      screen.getByText('Grok Build has nothing to run on yet.'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Open AI tools' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
  })

  it('has one expander: Advanced launch options is inside More options', async () => {
    const user = userEvent.setup()
    status.claude = { driver: 'claude', installed: true, signed_in: true }
    const onAdvanced = vi.fn()
    wrap(
      <NewSessionDialog open onOpenChange={() => {}} onAdvanced={onAdvanced} />,
    )
    await screen.findByRole('button', { name: 'Start' })
    expect(
      screen.queryByRole('button', { name: 'Advanced launch options' }),
    ).toBeNull()
    await user.click(screen.getByRole('button', { name: 'More options' }))
    await user.click(
      screen.getByRole('button', { name: 'Advanced launch options' }),
    )
    expect(onAdvanced).toHaveBeenCalled()
  })

  it('a ready Grok Build is picked and starts with the chosen preset', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.toolsReadiness).mockResolvedValue(
      readinessOf({ claude: 'own_login', grok: 'own_login' }),
    )
    launch.launchSession.mockResolvedValue({ run_ref: 'run_g' })
    wrap(<NewSessionDialog open onOpenChange={() => {}} />)
    await user.click(await screen.findByRole('radio', { name: 'Grok Build' }))
    await user.click(screen.getByRole('button', { name: 'More options' }))
    await user.click(screen.getByRole('radio', { name: 'Read only' }))
    await user.click(screen.getByRole('button', { name: 'Start' }))
    expect(launch.launchSession).toHaveBeenCalledWith(
      expect.objectContaining({
        quick: expect.objectContaining({
          driver: 'grok',
          permission: 'readOnly',
        }),
      }),
      expect.objectContaining({
        signal: expect.any(AbortSignal),
        dispatchGuard: expect.any(Function),
      }),
    )
    // SC 59 item 2: the new session opens on its conversation, also on a phone.
    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: '/sessions',
        search: { session: 'run:run_g', pane: 'narrative' },
      }),
    )
  })
})

// WEB and ID on 09b: a refused tool (409, nothing to run on yet) was asked again at
// every mount, each answer a console error. A tool with nothing to run on is data in
// the one readiness answer now: a remount asks that one read again, never once per tool.
describe('a tool with nothing to run on yet', () => {
  it('is part of one read per mount, through remounts', async () => {
    status.claude = { driver: 'claude', installed: true, signed_in: true }
    status.codex = { driver: 'codex', installed: true, signed_in: false }
    const readiness = vi.mocked(agentOpsApi.toolsReadiness)
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const mount = () =>
      render(
        <QueryClientProvider client={qc}>
          <NewSessionDialog open onOpenChange={() => {}} />
        </QueryClientProvider>,
      )
    const before = readiness.mock.calls.length
    const first = mount()
    expect(
      await screen.findByRole('button', { name: 'Start' }),
    ).toBeInTheDocument()
    first.unmount()
    mount()
    expect(
      await screen.findByRole('button', { name: 'Start' }),
    ).toBeInTheDocument()
    expect(readiness.mock.calls.length - before).toBeLessThanOrEqual(2)
  })
})
