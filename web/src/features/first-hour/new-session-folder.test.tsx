// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A first session needs no typed folder. The form shows the folder the product fills in
// (the last session's folder while it is still registered, else a new folder of the
// session's own, which the engine creates) and a typed path only after Change folder.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({ canReadFolders: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (permission: string) =>
      permission !== 'sessions:workspace:read' || auth.canReadFolders,
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
import type { RunDTO, WorkspaceDTO } from '@/features/agentops/types'
import { providersApi } from '@/features/providers/api'
import { http } from '@/lib/api'
import { useTenantStore } from '@/stores/tenant'
import { StartSessionForm } from './first-hour'
import { useClientSettings } from '@/features/settings/preferences'
import { SessionDefaultsSettings } from '@/features/settings/session-defaults-settings'

function wrap(
  ui: ReactNode,
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
) {
  render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
  return qc
}

function runs(items: Partial<RunDTO>[]) {
  vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
    items,
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listRuns>>)
}

function workspaces(items: Partial<WorkspaceDTO>[]) {
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
    items,
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
}

const REPO = {
  workspace_ref: 'ws_1',
  root_path: '/srv/work/repo',
  state: 'active',
}

beforeEach(() => {
  useClientSettings.getState().restore()
  vi.restoreAllMocks()
  auth.canReadFolders = true
  workspaces([])
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

it.each([false, true])(
  'declares partial default-folder choices only when has_more is %s',
  async (hasMore) => {
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
      items: [REPO],
      has_more: hasMore,
    } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
    useClientSettings.setState({
      sessionDefaults: { tool: '', account: '', model: '', folder: 'ws_1' },
    })
    wrap(<SessionDefaultsSettings />)
    await waitFor(() =>
      expect(
        screen.getByRole('combobox', { name: 'Default folder' }),
      ).toHaveTextContent('/srv/work/repo'),
    )
    const notice = screen.queryByText(
      'Registered folders: 1 loaded; there are more.',
    )
    if (hasMore) expect(notice).toBeVisible()
    else expect(notice).toBeNull()
  },
)

it('starts in the saved registered folder without a typed path', async () => {
  runs([])
  workspaces([REPO])
  useClientSettings.setState({
    sessionDefaults: { tool: '', account: '', model: '', folder: 'ws_1' },
  })
  wrap(<StartSessionForm />)
  const start = await screen.findByRole('button', { name: 'Start' })
  await waitFor(() => expect(start).toBeEnabled())
  await userEvent.setup().click(start)
  await waitFor(() => expect(launch.launchSession).toHaveBeenCalled())
  expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
    folder: REPO.root_path,
    workspace_ref: 'ws_1',
  })
})

it('explains a removed default folder and uses the first available folder', async () => {
  runs([])
  workspaces([REPO])
  useClientSettings.setState({
    sessionDefaults: { tool: '', account: '', model: '', folder: 'removed' },
  })
  wrap(<StartSessionForm />)
  const start = await screen.findByRole('button', { name: 'Start' })
  await waitFor(() => expect(start).toBeEnabled())
  expect(screen.getByText(/saved default.*no longer available/i)).toBeVisible()
  await userEvent.setup().click(start)
  await waitFor(() => expect(launch.launchSession).toHaveBeenCalled())
  expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
    folder: REPO.root_path,
    workspace_ref: 'ws_1',
  })
})

describe('the folder in the New session form', () => {
  it('asks a first session for no path: it starts in a new folder of its own', async () => {
    const user = userEvent.setup()
    runs([])
    wrap(<StartSessionForm />)
    await screen.findByRole('button', { name: 'Start' })
    expect(
      await screen.findByText('A new folder for this session'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Folder' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '',
      workspace_ref: undefined,
    })
  })

  it("fills in the last session's folder, not a failed one's nor one Olivares made for a run", async () => {
    const user = userEvent.setup()
    runs([
      // Newest first: a failed session may have failed on its folder.
      {
        run_ref: 'run_3',
        state: 'failed',
        workspace_ref: 'ws_gone',
        workspace_path: '/srv/work/gone',
      },
      // A session with no folder ran in one of its own.
      {
        run_ref: 'run_2',
        state: 'stopped',
        workspace_ref: '',
        workspace_path: '/var/lib/olivares/session-workspaces/run_2',
      },
      {
        run_ref: 'run_1',
        state: 'stopped',
        workspace_ref: 'ws_1',
        workspace_path: '/srv/work/repo',
      },
    ])
    workspaces([
      REPO,
      {
        workspace_ref: 'ws_gone',
        root_path: '/srv/work/gone',
        state: 'active',
      },
    ])
    wrap(<StartSessionForm />)
    expect(await screen.findByText('/srv/work/repo')).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Folder' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '/srv/work/repo',
      workspace_ref: 'ws_1',
    })
  })

  it('lets the user change the folder, and an emptied one is a new folder', async () => {
    const user = userEvent.setup()
    runs([
      {
        run_ref: 'run_1',
        state: 'stopped',
        workspace_ref: 'ws_1',
        workspace_path: '/srv/work/repo',
      },
    ])
    workspaces([REPO])
    wrap(<StartSessionForm />)
    await screen.findByText('/srv/work/repo')
    await user.click(screen.getByRole('button', { name: 'Change folder' }))
    // A typed path is the fallback: it opens from the list, not instead of it.
    await user.click(await screen.findByRole('combobox', { name: 'Folder' }))
    await user.click(
      await screen.findByRole('option', { name: 'Another folder…' }),
    )
    const field = screen.getByRole('textbox', { name: 'Folder' })
    expect(field).toHaveValue('/srv/work/repo')
    expect(field).toHaveFocus()
    await user.clear(field)
    await user.type(field, '/srv/work/other')
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '/srv/work/other',
      workspace_ref: undefined,
    })
    await user.clear(field)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(2))
    expect(launch.launchSession.mock.calls[1][0].quick).toMatchObject({
      folder: '',
      workspace_ref: undefined,
    })
  })

  // Issue #1788: Change folder was a bare text field although the form had read the
  // registered folders. The folders sessions use are listed and selectable.
  const PAYMENTS = {
    workspace_ref: 'ws_2',
    name: 'payments-api',
    root_path: '/srv/work/payments',
    state: 'active',
  }

  it('lists the registered folders to pick from, and a picked one keeps its registration', async () => {
    const user = userEvent.setup()
    runs([{ run_ref: 'run_1', state: 'stopped', workspace_ref: 'ws_1' }])
    workspaces([
      REPO,
      PAYMENTS,
      {
        workspace_ref: 'ws_off',
        root_path: '/srv/work/off',
        state: 'disabled',
      },
    ])
    wrap(<StartSessionForm />)
    await screen.findByText('/srv/work/repo')
    await user.click(screen.getByRole('button', { name: 'Change folder' }))
    expect(screen.queryByRole('textbox', { name: 'Folder' })).toBeNull()
    // Focus stays in the form: the button that was pressed is gone, the list takes it.
    const picker = await screen.findByRole('combobox', { name: 'Folder' })
    expect(picker).toHaveFocus()
    expect(picker).toHaveTextContent('/srv/work/repo')
    await user.click(picker)
    const options = await screen.findAllByRole('option')
    expect(options.map((o) => o.textContent)).toEqual([
      '/srv/work/repo',
      'payments-api (/srv/work/payments)',
      'A new folder for this session',
      'Another folder…',
    ])
    for (const option of options.slice(0, 2)) {
      expect(option.querySelector('[data-slot="path-text"]')).not.toBeNull()
      // A select option cannot contain another interactive control.
      expect(option.querySelector('button')).toBeNull()
    }
    expect(options[1].querySelector('[data-slot="path-text"]')).toHaveClass(
      'flex-1',
    )
    await user.keyboard('payments-api{Enter}')
    expect(screen.queryByRole('textbox', { name: 'Folder' })).toBeNull()
    // What the list shows is what Start sends.
    expect(screen.getByRole('combobox', { name: 'Folder' })).toHaveTextContent(
      'payments-api (/srv/work/payments)',
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '/srv/work/payments',
      workspace_ref: 'ws_2',
    })
  })

  it('lets the user pick a new folder from the list', async () => {
    const user = userEvent.setup()
    runs([{ run_ref: 'run_1', state: 'stopped', workspace_ref: 'ws_1' }])
    workspaces([REPO])
    wrap(<StartSessionForm />)
    await screen.findByText('/srv/work/repo')
    await user.click(screen.getByRole('button', { name: 'Change folder' }))
    await user.click(await screen.findByRole('combobox', { name: 'Folder' }))
    await user.click(
      await screen.findByRole('option', {
        name: 'A new folder for this session',
      }),
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '',
      workspace_ref: undefined,
    })
  })

  it('asks for a path only when no folder is registered', async () => {
    const user = userEvent.setup()
    runs([])
    workspaces([])
    wrap(<StartSessionForm />)
    await screen.findByText('A new folder for this session')
    await user.click(screen.getByRole('button', { name: 'Change folder' }))
    expect(screen.queryByRole('combobox', { name: 'Folder' })).toBeNull()
    expect(screen.getByRole('textbox', { name: 'Folder' })).toHaveFocus()
  })

  // The text field is decided when Change folder opens it: a list that arrives later
  // must not swap it for a Select under the user's hands and drop what they typed.
  it('keeps the path being typed when the registered folders arrive late', async () => {
    const user = userEvent.setup()
    runs([])
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockRejectedValue(
      new Error('offline'),
    )
    const qc = wrap(<StartSessionForm />)
    await screen.findByText('A new folder for this session')
    await user.click(screen.getByRole('button', { name: 'Change folder' }))
    await user.type(
      screen.getByRole('textbox', { name: 'Folder' }),
      '/srv/work/typed',
    )
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
      items: [REPO],
      has_more: true,
    } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
    await qc.refetchQueries()
    // Absence proves nothing until the list has rendered: its cut-short notice is the sign.
    expect(
      await screen.findByText(/Registered folders: 1 loaded/),
    ).toBeVisible()
    expect(screen.queryByRole('combobox', { name: 'Folder' })).toBeNull()
    expect(screen.getByRole('textbox', { name: 'Folder' })).toHaveValue(
      '/srv/work/typed',
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '/srv/work/typed',
      workspace_ref: undefined,
    })
  })

  // An unreadable list is not an empty one: the person who could pick is told why they
  // are asked for a path.
  it('says the registered folders could not be read, beside the typed-path field', async () => {
    const user = userEvent.setup()
    runs([])
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockRejectedValue(
      new Error('offline'),
    )
    wrap(<StartSessionForm />)
    await screen.findByText('A new folder for this session')
    await user.click(screen.getByRole('button', { name: 'Change folder' }))
    expect(screen.getByRole('textbox', { name: 'Folder' })).toBeVisible()
    expect(screen.getByText(/could not be read/)).toBeVisible()
  })

  it('does not say so to a principal who may not list folders', async () => {
    const user = userEvent.setup()
    auth.canReadFolders = false
    runs([])
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockRejectedValue(
      new Error('forbidden'),
    )
    wrap(<StartSessionForm />)
    await screen.findByText('A new folder for this session')
    await user.click(screen.getByRole('button', { name: 'Change folder' }))
    expect(screen.getByRole('textbox', { name: 'Folder' })).toBeVisible()
    expect(screen.queryByText(/could not be read/)).toBeNull()
  })

  // The list is capped: a person whose folder is beyond it must still be able to type it.
  it('says the list is cut short and still offers a typed path', async () => {
    const user = userEvent.setup()
    runs([{ run_ref: 'run_1', state: 'stopped', workspace_ref: 'ws_1' }])
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
      items: [REPO],
      has_more: true,
    } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
    wrap(<StartSessionForm />)
    expect(
      await screen.findByText(/Registered folders: 1 loaded/),
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Change folder' }))
    await user.click(await screen.findByRole('combobox', { name: 'Folder' }))
    expect(
      await screen.findByRole('option', { name: 'Another folder…' }),
    ).toBeVisible()
  })

  // Start sends the folder the form shows; if the list stops naming it, the form still
  // shows it (the engine refuses a folder deregistered meanwhile, with its own sentence).
  it('keeps showing a picked folder that the list no longer names', async () => {
    const user = userEvent.setup()
    runs([{ run_ref: 'run_1', state: 'stopped', workspace_ref: 'ws_1' }])
    workspaces([REPO, PAYMENTS])
    const qc = wrap(<StartSessionForm />)
    await screen.findByText('/srv/work/repo')
    await user.click(screen.getByRole('button', { name: 'Change folder' }))
    await user.click(await screen.findByRole('combobox', { name: 'Folder' }))
    await user.click(
      await screen.findByRole('option', { name: /payments-api/ }),
    )
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
      items: [REPO],
      has_more: true,
    } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
    await qc.refetchQueries()
    // The new list has rendered once its cut-short notice shows.
    await screen.findByText(/Registered folders: 1 loaded/)
    expect(screen.getByRole('combobox', { name: 'Folder' })).toHaveTextContent(
      '/srv/work/payments',
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '/srv/work/payments',
      workspace_ref: 'ws_2',
    })
  })

  // Start sends the folder the form shows: none before the read settles.
  it('holds Start, saying why, until the last folder is read', async () => {
    const user = userEvent.setup()
    let answer!: (v: Awaited<ReturnType<typeof agentOpsApi.listRuns>>) => void
    vi.spyOn(agentOpsApi, 'listRuns').mockReturnValue(
      new Promise((resolve) => (answer = resolve)),
    )
    workspaces([REPO])
    wrap(<StartSessionForm />)
    const start = await screen.findByRole('button', { name: 'Start' })
    expect(start).toBeDisabled()
    expect(start).toHaveAccessibleDescription(
      "Reading the last session's folder…",
    )
    expect(screen.queryByText('A new folder for this session')).toBeNull()
    answer({
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
    expect(await screen.findByText('/srv/work/repo')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '/srv/work/repo',
      workspace_ref: 'ws_1',
    })
  })

  // A later read (another session started, the window regained focus) does not move the
  // folder the user is looking at.
  it('keeps the folder it showed when the session list is read again', async () => {
    runs([
      {
        run_ref: 'run_1',
        state: 'stopped',
        workspace_ref: 'ws_1',
        workspace_path: '/srv/work/repo',
      },
    ])
    workspaces([
      REPO,
      { workspace_ref: 'ws_2', root_path: '/srv/work/other', state: 'active' },
    ])
    const qc = wrap(<StartSessionForm />)
    await screen.findByText('/srv/work/repo')
    runs([
      {
        run_ref: 'run_2',
        state: 'running',
        workspace_ref: 'ws_2',
        workspace_path: '/srv/work/other',
      },
    ])
    await qc.refetchQueries()
    // The new read is in the cache; the form gets a moment to (wrongly) show it.
    await waitFor(() =>
      expect(
        qc
          .getQueriesData<{ items: Partial<RunDTO>[] }>({})
          .some(([, data]) => data?.items?.[0]?.run_ref === 'run_2'),
      ).toBe(true),
    )
    await expect(
      screen.findByText('/srv/work/other', {}, { timeout: 500 }),
    ).rejects.toThrow()
    expect(screen.getByText('/srv/work/repo')).toBeInTheDocument()
  })

  // An unreadable session list is no reason to block a start: the engine still gives the
  // session a folder of its own.
  it('starts in a new folder when the session list cannot be read', async () => {
    const user = userEvent.setup()
    vi.spyOn(agentOpsApi, 'listRuns').mockRejectedValue(new Error('offline'))
    wrap(<StartSessionForm />)
    expect(
      await screen.findByText('A new folder for this session'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '',
      workspace_ref: undefined,
    })
  })

  // A folder an operator deregistered is not offered again: Start would register it
  // again (rw, DLP off) for an admin, and fail with 403 for an editor. Its session
  // ended normally; the registration went away later.
  it('skips a folder that is no longer registered for the last one that is', async () => {
    runs([
      {
        run_ref: 'run_2',
        state: 'stopped',
        workspace_ref: 'ws_removed',
        workspace_path: '/srv/work/removed',
      },
      {
        run_ref: 'run_1',
        state: 'stopped',
        workspace_ref: 'ws_1',
        workspace_path: '/srv/work/repo-at-run-time',
      },
    ])
    // Only ws_1 is still registered, and its folder is the registration's, not the run's.
    workspaces([REPO])
    wrap(<StartSessionForm />)
    expect(await screen.findByText('/srv/work/repo')).toBeInTheDocument()
    expect(screen.queryByText('/srv/work/removed')).toBeNull()
  })

  it('starts in a new folder, registering none, when the last folder is no longer registered', async () => {
    const user = userEvent.setup()
    runs([
      {
        run_ref: 'run_1',
        state: 'stopped',
        workspace_ref: 'ws_removed',
        workspace_path: '/srv/work/removed',
      },
    ])
    // The real start, so a re-registration would show.
    const real = await vi.importActual<
      typeof import('@/features/agentops/session-launch')
    >('@/features/agentops/session-launch')
    launch.launchSession.mockImplementation(real.launchSession)
    const createWorkspace = vi.spyOn(agentOpsApi, 'createWorkspace')
    vi.spyOn(agentOpsApi, 'resolveProfile').mockResolvedValue({
      profile: { profile_ref: 'prof_1' },
    } as Awaited<ReturnType<typeof agentOpsApi.resolveProfile>>)
    const createRun = vi
      .spyOn(agentOpsApi, 'createRun')
      .mockResolvedValue({ run_ref: 'run_new' } as RunDTO)
    // The permission template lookup.
    vi.spyOn(http, 'get').mockResolvedValue({
      items: [{ id: 'tpl-ec', name: 'Edits and commands', builtin: true }],
    })
    wrap(<StartSessionForm />)
    expect(
      await screen.findByText('A new folder for this session'),
    ).toBeInTheDocument()
    expect(screen.queryByText('/srv/work/removed')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(createRun).toHaveBeenCalledTimes(1))
    expect(createRun.mock.calls[0][0]).toMatchObject({ workspace_ref: '' })
    expect(createWorkspace).not.toHaveBeenCalled()
  })

  it('refuses a folder deregistered after it was shown without registering it again', async () => {
    const user = userEvent.setup()
    runs([{ run_ref: 'run_1', state: 'stopped', workspace_ref: 'ws_1' }])
    workspaces([REPO])
    const real = await vi.importActual<
      typeof import('@/features/agentops/session-launch')
    >('@/features/agentops/session-launch')
    launch.launchSession.mockImplementation(real.launchSession)
    const createWorkspace = vi
      .spyOn(agentOpsApi, 'createWorkspace')
      .mockResolvedValue({
        ...REPO,
        workspace_ref: 'ws_replacement',
      } as WorkspaceDTO)
    vi.spyOn(agentOpsApi, 'resolveProfile').mockResolvedValue({
      profile: { profile_ref: 'prof_1' },
    } as Awaited<ReturnType<typeof agentOpsApi.resolveProfile>>)
    const createRun = vi
      .spyOn(agentOpsApi, 'createRun')
      .mockImplementation(async (run) => {
        if (run.workspace_ref === REPO.workspace_ref)
          throw new Error('workspace_ref is not a registered workspace')
        return { run_ref: 'run_new' } as RunDTO
      })
    vi.spyOn(http, 'get').mockResolvedValue({
      items: [{ id: 'tpl-ec', name: 'Edits and commands', builtin: true }],
    })
    const qc = wrap(<StartSessionForm />)
    await screen.findByText('/srv/work/repo')
    workspaces([])
    await qc.refetchQueries()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    expect(
      await screen.findByText(/workspace_ref is not a registered workspace/),
    ).toBeInTheDocument()
    expect(createRun).toHaveBeenCalledWith(
      expect.objectContaining({ workspace_ref: 'ws_1' }),
      expect.anything(),
    )
    expect(createWorkspace).not.toHaveBeenCalled()
  })

  it('keeps the last session registration when two folders share a path', async () => {
    const user = userEvent.setup()
    runs([{ run_ref: 'run_1', state: 'stopped', workspace_ref: 'ws_readonly' }])
    workspaces([
      { ...REPO, mount_mode: 'rw' },
      { ...REPO, workspace_ref: 'ws_readonly', mount_mode: 'ro' },
    ])
    wrap(<StartSessionForm />)
    await screen.findByText('/srv/work/repo')
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '/srv/work/repo',
      workspace_ref: 'ws_readonly',
    })
  })

  // The list the form read a moment ago is not trusted: a folder deregistered since is
  // not offered when New session opens again.
  it('does not offer a folder deregistered since the form last opened', async () => {
    runs([
      {
        run_ref: 'run_1',
        state: 'stopped',
        workspace_ref: 'ws_1',
        workspace_path: '/srv/work/repo',
      },
    ])
    workspaces([REPO])
    const qc = wrap(<StartSessionForm />)
    expect(await screen.findByText('/srv/work/repo')).toBeInTheDocument()
    cleanup()
    workspaces([])
    wrap(<StartSessionForm />, qc)
    expect(
      await screen.findByText('A new folder for this session'),
    ).toBeInTheDocument()
    expect(screen.queryByText('/srv/work/repo')).toBeNull()
  })

  // An unreadable folder list is no reason to offer an unchecked folder or to block a
  // start: the session gets a new folder of its own.
  it('starts in a new folder when the registered folders cannot be read', async () => {
    const user = userEvent.setup()
    runs([
      {
        run_ref: 'run_1',
        state: 'stopped',
        workspace_ref: 'ws_1',
        workspace_path: '/srv/work/repo',
      },
    ])
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockRejectedValue(
      new Error('offline'),
    )
    wrap(<StartSessionForm />)
    expect(
      await screen.findByText('A new folder for this session'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '',
      workspace_ref: undefined,
    })
  })
})
