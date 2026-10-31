// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A session started from a GitHub issue or pull request page: the launch dialog offers the
// field once a folder is chosen, the engine reads the page before any session exists, and
// the item's title and body lead the first message.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

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
    profileModels: vi.fn(),
    sourceItem: vi.fn(),
    input: vi.fn(),
    inputText: vi.fn(),
  },
}))
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
vi.mock('@tanstack/react-router', async (orig) => ({
  ...(await orig<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(),
}))

import { templatesApi } from '@/features/workspace-templates/api'
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

const PAGE = 'https://github.com/acme/widgets/issues/17'
const FIELD = 'GitHub issue or pull request'

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RunCreateDialog open onOpenChange={vi.fn()} />
    </QueryClientProvider>,
  )
}

const posted = () =>
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

async function chooseFolder(user: ReturnType<typeof userEvent.setup>) {
  await openAdvanced(user)
  await user.click(screen.getByLabelText('Folder'))
  await user.click(
    await screen.findByRole('option', { name: /Project folder/ }),
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  useSessionStore.setState({ credentialGeneration: 1 })
  useTenantStore.setState({ activeTenant: 't1' })
  vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
    items: [
      {
        workspace_ref: 'folder-a',
        name: 'Project folder',
        state: 'active',
        root_path: '/project',
        mount_mode: 'rw',
        max_read_bytes: 1024,
        dlp_mode: 'off',
      },
    ],
    has_more: false,
  })
  vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
    items: [homeA],
    has_more: false,
  })
  vi.mocked(templatesApi.list).mockResolvedValue({ items: [], has_more: false })
  vi.mocked(agentOpsApi.profileLaunchReadiness).mockImplementation(
    async (ref: string) => fixtureReadiness({ profile_ref: ref }),
  )
  vi.mocked(agentOpsApi.createRun).mockResolvedValue({
    run_ref: 'run_1',
    provider_driver: 'claude',
  } as never)
  vi.mocked(agentOpsApi.input).mockResolvedValue({ accepted: true } as never)
  vi.mocked(agentOpsApi.sourceItem).mockResolvedValue({
    url: PAGE,
    kind: 'issue',
    number: 17,
    title: 'Crash on save',
    body: 'Steps:\n1. save',
  })
})

describe('RunCreateDialog — start from a GitHub issue or pull request', () => {
  it('offers the page only once a folder is chosen', async () => {
    const user = userEvent.setup()
    wrap()
    await openAdvanced(user)
    expect(
      screen.queryByRole('textbox', { name: FIELD }),
    ).not.toBeInTheDocument()
    await user.click(screen.getByLabelText('Folder'))
    await user.click(
      await screen.findByRole('option', { name: /Project folder/ }),
    )
    expect(screen.getByRole('textbox', { name: FIELD })).toBeInTheDocument()
  })

  it('reads the page in the folder first, launches with it, and leads the first message with it', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    await user.type(screen.getByRole('textbox', { name: FIELD }), PAGE)
    await user.type(
      screen.getByRole('textbox', { name: 'First message (optional)' }),
      'only touch the parser',
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())

    expect(agentOpsApi.sourceItem).toHaveBeenCalledWith(
      'folder-a',
      PAGE,
      expect.objectContaining({
        tenant: 't1',
        signal: expect.any(AbortSignal),
      }),
    )
    expect(posted().source_url).toBe(PAGE)
    expect(posted().workspace_ref).toBe('folder-a')
    // The session is listed by the item's title, not by the start of its body.
    expect(posted().name).toBe('Crash on save')
    expect(
      vi.mocked(agentOpsApi.sourceItem).mock.invocationCallOrder[0],
    ).toBeLessThan(vi.mocked(agentOpsApi.createRun).mock.invocationCallOrder[0])

    await waitFor(() => expect(agentOpsApi.input).toHaveBeenCalledOnce())
    const frame = JSON.parse(
      vi.mocked(agentOpsApi.input).mock.calls[0][1] as string,
    ) as { message: { content: string } }
    const sent = frame.message.content
    expect(sent.indexOf('Crash on save')).toBe(0)
    expect(sent.indexOf('Steps:')).toBeGreaterThan(
      sent.indexOf('Crash on save'),
    )
    expect(sent.indexOf(PAGE)).toBeGreaterThan(sent.indexOf('Steps:'))
    expect(sent.indexOf('only touch the parser')).toBeGreaterThan(
      sent.indexOf(PAGE),
    )
  })

  it('makes no session when the page cannot be read, and says why', async () => {
    const sentence =
      'No approved GitHub repository binding lists acme/widgets for this workspace.'
    vi.mocked(agentOpsApi.sourceItem).mockRejectedValue(
      new ApiError(422, 'unprocessable', sentence),
    )
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    await user.type(screen.getByRole('textbox', { name: FIELD }), PAGE)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(sentence)
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
    expect(agentOpsApi.input).not.toHaveBeenCalled()
  })
})
