// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The setup wizard is three steps — install a tool, sign it in, start a session —
// and nothing in it is blocked behind a step-up or demands optional work.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({ admin: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => auth.admin, isSuperadmin: auth.admin }),
}))
const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useRouterState: () => '',
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))
const status = vi.hoisted(() => ({
  claude: { driver: 'claude', installed: false, signed_in: false },
  codex: { driver: 'codex', installed: false, signed_in: false },
}))
const launch = vi.hoisted(() => ({ startSession: vi.fn() }))
vi.mock('@/features/first-hour/api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    startSession: launch.startSession,
    signInApi: {
      status: (d: 'claude' | 'codex') => Promise.resolve(status[d]),
      start: vi.fn(),
      get: vi.fn(),
      code: vi.fn(),
      cancel: vi.fn(),
    },
  }
})

import { agentOpsApi } from '@/features/agentops/api'
import { ApiError } from '@/lib/api/errors'
import { NewSessionDialog } from '@/features/first-hour/first-hour'
import { OnboardingView } from './onboarding-view'

const DISMISS_KEY = 'olivares.onboarding.dismissed'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  localStorage.clear()
  auth.admin = true
  status.claude = { driver: 'claude', installed: false, signed_in: false }
  status.codex = { driver: 'codex', installed: false, signed_in: false }
  // What a tool runs on is the engine's answer (GET provider-profiles/resolve):
  // its own login once the stub says so, otherwise the engine's refusal.
  vi.spyOn(agentOpsApi, 'previewProfile').mockImplementation(async (driver) => {
    const s = status[driver as 'claude' | 'codex']
    if (s?.installed && s.signed_in) return { reason: 'own_login' }
    throw new ApiError(409, 'conflict', `${driver} has nothing to run on yet.`)
  })
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

  it('offers the tool its own sign-in once it is installed', async () => {
    status.claude = { driver: 'claude', installed: true, signed_in: false }
    wrap(<OnboardingView />)
    expect(
      await screen.findByRole('button', { name: 'Sign in with Claude' }),
    ).toBeInTheDocument()
    expect(screen.getByText('Use an API key instead')).toBeInTheDocument()
  })

  it('names the key the engine will use when the tool is not signed in (HU 030)', async () => {
    status.claude = { driver: 'claude', installed: true, signed_in: false }
    vi.spyOn(agentOpsApi, 'previewProfile').mockImplementation(
      async (driver) => {
        if (driver === 'claude')
          return {
            reason: 'api_key',
            provider: {
              provider_ref: 'prv_a',
              kind: 'anthropic',
              display_name: 'Team key',
            },
          }
        throw new ApiError(
          409,
          'conflict',
          `${driver} has nothing to run on yet.`,
        )
      },
    )
    wrap(<OnboardingView />)
    expect(
      await screen.findByText('Uses your API key: Team key'),
    ).toBeInTheDocument()
    expect(
      await screen.findByRole('button', { name: 'Start' }),
    ).toBeInTheDocument()
  })

  it('starts a session once a tool is signed in', async () => {
    status.codex = { driver: 'codex', installed: true, signed_in: true }
    wrap(<OnboardingView />)
    expect(
      await screen.findByRole('button', { name: 'Start' }),
    ).toBeInTheDocument()
    expect(screen.getByLabelText('Folder')).toBeInTheDocument()
    expect(
      screen.getByLabelText('First message (optional)'),
    ).toBeInTheDocument()
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
// trip. It now waits only for each tool's first answer.
describe('the New session dialog', () => {
  it('shows the form with one tool ready and asks again for the refused one at most once', async () => {
    status.claude = { driver: 'claude', installed: true, signed_in: true }
    const preview = vi.mocked(agentOpsApi.previewProfile)
    const before = preview.mock.calls.length
    wrap(<NewSessionDialog open onOpenChange={() => {}} />)
    expect(
      await screen.findByRole('button', { name: 'Start' }),
    ).toBeInTheDocument()
    await new Promise((r) => setTimeout(r, 300))
    const asked = preview.mock.calls.length - before
    await new Promise((r) => setTimeout(r, 300))
    expect(preview.mock.calls.length - before).toBe(asked)
    // At most two per tool, for the four tools the engine drives (HU 043; it was two
    // tools, so four).
    expect(asked).toBeLessThanOrEqual(8)
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
    ).toEqual(['Claude Code', 'Codex', 'Grok Build', 'OpenCode'])
    expect(screen.getByRole('radio', { name: 'Claude Code' })).toHaveAttribute(
      'aria-checked',
      'true',
    )
    expect(screen.queryByText(/nothing to run on yet/)).toBeNull()
    await user.click(screen.getByRole('radio', { name: 'Grok Build' }))
    expect(screen.getAllByText(/nothing to run on yet/)).toHaveLength(1)
    expect(
      screen.getByText('grok has nothing to run on yet.'),
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
    vi.mocked(agentOpsApi.previewProfile).mockImplementation(async (driver) => {
      if (driver === 'claude' || driver === 'grok')
        return { reason: 'own_login' }
      throw new ApiError(
        409,
        'conflict',
        `${driver} has nothing to run on yet.`,
      )
    })
    launch.startSession.mockResolvedValue({ run_ref: 'run_g' })
    wrap(<NewSessionDialog open onOpenChange={() => {}} />)
    await user.click(await screen.findByRole('radio', { name: 'Grok Build' }))
    await user.click(screen.getByRole('button', { name: 'More options' }))
    await user.click(screen.getByRole('radio', { name: 'Read only' }))
    await user.click(screen.getByRole('button', { name: 'Start' }))
    expect(launch.startSession).toHaveBeenCalledWith(
      expect.objectContaining({ driver: 'grok', permission: 'readOnly' }),
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
// every mount, each answer a console error. It is the known state of a fresh
// install now: asked once while the answer is fresh, through remounts.
describe('a tool with nothing to run on yet', () => {
  it('is asked once while its answer is fresh, through remounts', async () => {
    status.claude = { driver: 'claude', installed: true, signed_in: true }
    // Installed with nothing to run on: a tool that is not installed is not asked
    // at all on a fresh install (HU 049).
    status.codex = { driver: 'codex', installed: true, signed_in: false }
    const preview = vi.mocked(agentOpsApi.previewProfile)
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const mount = () =>
      render(
        <QueryClientProvider client={qc}>
          <NewSessionDialog open onOpenChange={() => {}} />
        </QueryClientProvider>,
      )
    const codexAsks = () =>
      preview.mock.calls.filter(([d]) => d === 'codex').length
    const before = codexAsks()
    const first = mount()
    expect(
      await screen.findByRole('button', { name: 'Start' }),
    ).toBeInTheDocument()
    first.unmount()
    mount()
    expect(
      await screen.findByRole('button', { name: 'Start' }),
    ).toBeInTheDocument()
    expect(codexAsks() - before).toBe(1)
  })
})
