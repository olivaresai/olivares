// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the regression: a folder can store the permission its sessions start under. The New session
// form offers it first; the person's own pick wins, and a Full default is offered only to
// someone who may run Full (the engine refuses anyone else).
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({ runAdmin: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (permission: string) =>
      permission !== 'sessions:run:admin' || auth.runAdmin,
    isSuperadmin: true,
    activeTenant: 'tnt-a',
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: () => '',
}))
const launch = vi.hoisted(() => ({ launchSession: vi.fn() }))
vi.mock('@/features/agentops/session-launch', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  launchSession: launch.launchSession,
}))
vi.mock('./api', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  signInApi: {
    status: (driver: string) =>
      Promise.resolve({ driver, installed: true, signed_in: true }),
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import type { WorkspaceDTO } from '@/features/agentops/types'
import { providersApi } from '@/features/providers/api'
import { useTenantStore } from '@/stores/tenant'
import { StartSessionForm } from './first-hour'

function renderForm(folder: Partial<WorkspaceDTO>) {
  vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
    items: [
      {
        run_ref: 'run_1',
        state: 'stopped',
        workspace_ref: 'ws_1',
        workspace_path: '/srv/work/repo',
      },
    ],
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listRuns>>)
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
    items: [
      {
        workspace_ref: 'ws_1',
        root_path: '/srv/work/repo',
        state: 'active',
        ...folder,
      },
    ],
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <StartSessionForm />
    </QueryClientProvider>,
  )
}

const started = async () => {
  await userEvent.click(await screen.findByRole('button', { name: 'Start' }))
  await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
  return launch.launchSession.mock.calls[0][0].quick
}

beforeEach(() => {
  vi.restoreAllMocks()
  auth.runAdmin = true
  launch.launchSession.mockReset()
  launch.launchSession.mockResolvedValue({ run_ref: 'run_new' })
  useTenantStore.setState({ activeTenant: 'tnt-a' })
  vi.spyOn(providersApi, 'list').mockResolvedValue({
    items: [],
    has_more: false,
  } as Awaited<ReturnType<typeof providersApi.list>>)
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
    readinessOf({ claude: 'own_login' }),
  )
})

describe("a folder's default permission in the New session form", () => {
  it('starts under the folder default and says so', async () => {
    renderForm({ default_permission: 'supervised' })
    expect(
      await screen.findByText("Starts as Supervised, this folder's default."),
    ).toBeInTheDocument()
    expect(await started()).toMatchObject({
      workspace_ref: 'ws_1',
      permission: 'ask',
    })
  })

  it('starts under the usual default when the folder has none, and says nothing', async () => {
    renderForm({})
    await screen.findByRole('button', { name: 'Start' })
    expect(screen.queryByText(/this folder's default/)).toBeNull()
    expect(await started()).toMatchObject({ permission: 'editsAndCommands' })
  })

  it("lets the person's own pick win over the folder default", async () => {
    const user = userEvent.setup()
    renderForm({ default_permission: 'supervised' })
    const access = await screen.findByRole('combobox', {
      name: 'What the agent may do',
    })
    expect(access).toHaveTextContent('Supervised')
    await user.click(access)
    await user.click(await screen.findByRole('option', { name: 'Read only' }))
    expect(await started()).toMatchObject({ permission: 'readOnly' })
  })

  it('offers a Full default to someone who may run Full, and says it plainly', async () => {
    renderForm({ default_permission: 'full' })
    expect(
      await screen.findByText("Starts as Full, this folder's default."),
    ).toBeInTheDocument()
    expect(await started()).toMatchObject({ permission: 'full' })
  })

  it('never starts someone in Auto they may not run: the usual default applies', async () => {
    auth.runAdmin = false
    renderForm({ default_permission: 'auto' })
    await screen.findByRole('button', { name: 'Start' })
    expect(screen.queryByText(/this folder's default/)).toBeNull()
    expect(await started()).toMatchObject({ permission: 'editsAndCommands' })
  })

  it('never starts someone in Full they may not run: the usual default applies', async () => {
    auth.runAdmin = false
    renderForm({ default_permission: 'full' })
    await screen.findByRole('button', { name: 'Start' })
    expect(screen.queryByText(/this folder's default/)).toBeNull()
    expect(await started()).toMatchObject({ permission: 'editsAndCommands' })
  })
})

describe('the first view of the New session form', () => {
  it('shows tool, folder and access as one dropdown each, with Start across the form', async () => {
    renderForm({})
    // The tool is a dropdown, not a segmented control that wraps onto a second row.
    expect(
      await screen.findByRole('combobox', { name: 'Tool' }),
    ).toHaveTextContent('Claude Code')
    expect(screen.queryByRole('radiogroup', { name: 'Tool' })).toBeNull()
    // The folder is the same dropdown in every state: no text with a Change folder link.
    expect(screen.getByRole('combobox', { name: 'Folder' })).toHaveTextContent(
      '/srv/work/repo',
    )
    expect(screen.queryByRole('button', { name: 'Change folder' })).toBeNull()
    // Access is on the first view with its current value, not behind More options.
    expect(
      screen.getByRole('combobox', { name: 'What the agent may do' }),
    ).toHaveTextContent('Edit files and run commands')
    expect(screen.getByRole('button', { name: 'Start' })).toHaveClass('w-full')
  })
})

describe('the permissions in the New session picker', () => {
  const openPicker = async () => {
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('combobox', { name: 'What the agent may do' }),
    )
    return { user, list: await screen.findByRole('listbox') }
  }

  it('lists the six permissions from the least to the most, and says what the chosen one does', async () => {
    renderForm({})
    const { user, list } = await openPicker()
    expect(
      within(list)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual([
      'Read only',
      'Supervised',
      'Auto-accept edits',
      'Edit files and run commands',
      'Auto',
      'Full',
    ])
    expect(
      within(list).getByRole('option', { name: 'Edit files and run commands' }),
    ).toHaveAttribute('aria-selected', 'true')
    // Full is shown plainly, in the warning style.
    expect(within(list).getByRole('option', { name: 'Full' })).toHaveClass(
      'text-warning',
    )
    await user.click(within(list).getByRole('option', { name: 'Supervised' }))
    expect(
      screen.getByRole('combobox', { name: 'What the agent may do' }),
    ).toHaveAccessibleDescription(
      'Asks before every action that is not a read.',
    )
  })

  it('offers Full and Auto only to someone who may run them', async () => {
    // Both let the tool act without a person approving each call; the engine refuses
    // them to anyone who does not run sessions as an administrator.
    auth.runAdmin = false
    renderForm({})
    const { list } = await openPicker()
    expect(within(list).queryByRole('option', { name: 'Full' })).toBeNull()
    expect(within(list).queryByRole('option', { name: 'Auto' })).toBeNull()
    expect(within(list).getAllByRole('option')).toHaveLength(4)
  })

  it('says a tool without an automatic mode will ask, in its own name', async () => {
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({ codex: 'own_login' }),
    )
    renderForm({})
    const { user, list } = await openPicker()
    expect(screen.queryByText(/has no automatic mode/)).toBeNull()
    await user.click(within(list).getByRole('option', { name: 'Auto' }))
    expect(
      await screen.findByText(
        'Codex has no automatic mode: it will ask before each action.',
      ),
    ).toBeInTheDocument()
    expect(await started()).toMatchObject({
      driver: 'codex',
      permission: 'auto',
    })
  })

  it('says nothing of the kind for Claude Code, which has one', async () => {
    renderForm({})
    const { user, list } = await openPicker()
    await user.click(within(list).getByRole('option', { name: 'Auto' }))
    expect(screen.queryByText(/has no automatic mode/)).toBeNull()
    expect(await started()).toMatchObject({
      driver: 'claude',
      permission: 'auto',
    })
  })
})
