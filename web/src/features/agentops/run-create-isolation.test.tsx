// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The launch dialog's isolation choice: native by default, container on request. The
// choice decides what the engine is asked to run AND which readiness the dialog reads,
// so a server without a container runtime says so before Start instead of after it.
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

async function openAdvanced(user: ReturnType<typeof userEvent.setup>) {
  const toggle = await screen.findByRole('button', { name: 'Advanced options' })
  if (toggle.getAttribute('aria-expanded') !== 'true') await user.click(toggle)
  expect(
    await screen.findByText('Local requirements checked'),
  ).toBeInTheDocument()
}

async function chooseContainer(user: ReturnType<typeof userEvent.setup>) {
  await openAdvanced(user)
  await user.click(screen.getByLabelText('Isolation'))
  await user.click(await screen.findByRole('option', { name: 'Container' }))
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
    items: [homeA],
    has_more: false,
  })
  vi.mocked(templatesApi.list).mockResolvedValue({ items: [], has_more: false })
  vi.mocked(agentOpsApi.createRun).mockResolvedValue({} as never)
  vi.mocked(agentOpsApi.profileLaunchReadiness).mockImplementation(
    async (ref: string, selection) =>
      fixtureReadiness({
        profile_ref: ref,
        selection: {
          transport: 'stream-json',
          isolation: selection?.isolation ?? 'native',
        },
      }),
  )
})

describe('RunCreateDialog — isolation', () => {
  it('launches natively unless container is chosen', async () => {
    const user = userEvent.setup()
    wrap()
    await openAdvanced(user)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().isolation).toBe('native')
    expect(
      screen.queryByText(/runs the session in podman or Docker/),
    ).not.toBeInTheDocument()
  })

  it('asks for a container, reads the container readiness and says what it needs', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseContainer(user)
    await waitFor(() =>
      expect(agentOpsApi.profileLaunchReadiness).toHaveBeenCalledWith(
        'ppf_a',
        { transport: 'stream-json', isolation: 'container' },
        expect.anything(),
      ),
    )
    expect(
      await screen.findByText(/runs the session in podman or Docker/),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().isolation).toBe('container')
  })

  it('keeps Start closed when the server cannot run a container', async () => {
    vi.mocked(agentOpsApi.profileLaunchReadiness).mockImplementation(
      async (ref: string, selection) => {
        const container = selection?.isolation === 'container'
        const base = fixtureReadiness({
          profile_ref: ref,
          selection: {
            transport: 'stream-json',
            isolation: container ? 'container' : 'native',
          },
        })
        if (!container) return base
        return {
          ...base,
          configuration_state: 'unsupported',
          checks: base.checks.map((c) =>
            c.check === 'runner'
              ? { ...c, state: 'unsupported', code: 'isolation_unsupported' }
              : c,
          ),
        } as typeof base
      },
    )
    const user = userEvent.setup()
    wrap()
    await chooseContainer(user)
    // The runner and the isolation checks both give the reason, so it appears twice.
    expect(
      (
        await screen.findAllByText(
          /This server cannot provide this isolation\. Keep native/,
        )
      ).length,
    ).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })
})
