// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The last registered folder is remembered; otherwise a first session needs an explicit
// folder choice. A private session folder remains available as an explicit choice.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const link = vi.hoisted(() => ({ search: '', navigate: vi.fn() }))
const auth = vi.hoisted(() => ({
  canReadFolders: true,
  // Registering a folder (a new typed path) and opening the AI tools place.
  canRegisterFolders: true,
  canOpenTools: true,
  // Writing files into a folder (attached documents are saved there).
  canWriteFiles: true,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (permission: string) =>
      (permission !== 'sessions:workspace:read' || auth.canReadFolders) &&
      (permission !== 'sessions:workspace:admin' || auth.canRegisterFolders) &&
      (permission !== 'sessions:account:read' || auth.canOpenTools) &&
      (permission !== 'sessions:workspace:write' || auth.canWriteFiles),
    isSuperadmin: true,
    activeTenant: 'tnt-a',
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => link.navigate,
  useRouterState: () => link.search,
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
import { consoleApi } from '@/features/console/api'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import type {
  RunDTO,
  WorkspaceDTO,
  WriteResponse,
} from '@/features/agentops/types'
import { providersApi } from '@/features/providers/api'
import { skillsApi } from '@/features/skills/api'
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { NewSessionHost } from './new-session-host'
import { useNewSessionDialog } from './new-session-store'
import { StartSessionForm, WorkStart } from './first-hour'
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

// "Another folder…" in the Folder dropdown: the one way to a typed path.
async function chooseTypedFolder(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('combobox', { name: 'Folder' }))
  await user.click(
    await screen.findByRole('option', { name: 'Another folder…' }),
  )
}

beforeEach(() => {
  useClientSettings.getState().restore()
  vi.restoreAllMocks()
  auth.canReadFolders = true
  auth.canRegisterFolders = true
  auth.canOpenTools = true
  auth.canWriteFiles = true
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
  vi.spyOn(agentOpsApi, 'listFolderCandidates').mockResolvedValue({
    tools: [],
    folders: [],
  })
})

it.each([false, true])(
  'declares partial default-folder choices only when has_more is %s',
  async (hasMore) => {
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
      items: [REPO],
      has_more: hasMore,
    } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
    useClientSettings.setState({
      sessionDefaults: {
        tool: '',
        account: '',
        model: '',
        effort: '',
        folder: 'ws_1',
      },
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

it('the work surface keeps the composer while no tool can start, and Enter does not start one', async () => {
  runs([])
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(readinessOf({}))
  wrap(<WorkStart />)
  const composer = await screen.findByRole('textbox', {
    name: 'First message (optional)',
  })
  expect(await screen.findByRole('button', { name: 'Start' })).toBeDisabled()
  // The way to get a tool ready sits on the same screen: one card, for the tool the
  // composer would start, and it follows the tool the person picks. The card is the one
  // instruction: the composer does not say «Install … first» a second time.
  expect(
    await screen.findByRole('heading', { name: 'Start a session' }),
  ).toBeVisible()
  expect(await screen.findByTestId('tool-claude')).toBeVisible()
  expect(screen.queryByRole('button', { name: 'Open AI tools' })).toBeNull()
  expect(screen.queryByTestId('tool-codex')).toBeNull()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: /^Tool and account: / }))
  await user.click(await screen.findByRole('menuitemradio', { name: 'Codex' }))
  expect(await screen.findByTestId('tool-codex')).toBeVisible()
  expect(screen.queryByTestId('tool-claude')).toBeNull()
  await user.type(composer, 'look at the repo{Enter}')
  expect(launch.launchSession).not.toHaveBeenCalled()
})

it('the work surface offers the folders the tools already worked in once a tool is ready, and registers none until confirmed', async () => {
  runs([])
  vi.spyOn(agentOpsApi, 'listFolderCandidates').mockResolvedValue({
    tools: [{ tool: 'claude', name: 'Claude Code', readable: true }],
    folders: [
      {
        path: '/srv/work/api',
        tool: 'claude',
        last_used: new Date().toISOString(),
        conversations: 4,
        preselected: true,
      },
    ],
  })
  const create = vi.spyOn(agentOpsApi, 'createWorkspace')
  wrap(<WorkStart />)
  // The composer is still the way to start; the folders are one line under it.
  expect(
    screen.getByRole('textbox', { name: 'First message (optional)' }),
  ).toBeVisible()
  const user = userEvent.setup()
  await user.click(await screen.findByRole('button', { name: 'Review' }))
  expect(
    await screen.findByRole('group', { name: 'Bring your folders' }),
  ).toBeVisible()
  expect(
    await screen.findByRole('checkbox', {
      name: 'Register /srv/work/api (Claude Code)',
    }),
  ).toBeChecked()
  expect(
    screen.getByRole('button', { name: 'Register selected (1)' }),
  ).toBeEnabled()
  // Nothing was registered by looking.
  expect(create).not.toHaveBeenCalled()
})

it('the composer is on screen before the engine says which tools can start, and keeps what is typed', async () => {
  runs([])
  let answer = (_: ReturnType<typeof readinessOf>) => {}
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockReturnValue(
    new Promise((resolve) => {
      answer = resolve
    }),
  )
  wrap(<WorkStart />)
  const composer = screen.getByRole('textbox', {
    name: 'First message (optional)',
  })
  // Unknown is not "no": no tool is called not ready, and Enter has nothing to send yet.
  expect(screen.queryByTestId('tool-claude')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Start' })).toBeNull()
  const user = userEvent.setup()
  await user.type(composer, 'typed early{Enter}')
  expect(launch.launchSession).not.toHaveBeenCalled()
  answer(readinessOf({ claude: 'own_login' }))
  await screen.findByRole('button', { name: 'Start' })
  expect(composer).toHaveValue('typed early')
  expect(composer).toHaveFocus()
})

it('the composer comes first and Enter starts the session', async () => {
  runs([])
  workspaces([REPO])
  wrap(<WorkStart />)
  const composer = await screen.findByRole('textbox', {
    name: 'First message (optional)',
  })
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled(),
  )
  expect(screen.queryByTestId('tool-claude')).toBeNull()
  expect(
    composer.compareDocumentPosition(
      screen.getByRole('button', { name: /^Tool and account: / }),
    ) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy()
  const user = userEvent.setup()
  await user.type(composer, 'first line{Shift>}{Enter}{/Shift}second')
  expect(launch.launchSession).not.toHaveBeenCalled()
  await user.keyboard('{Enter}')
  await waitFor(() => expect(launch.launchSession).toHaveBeenCalledOnce())
  expect(launch.launchSession.mock.calls[0][0].message).toBe(
    'first line\nsecond',
  )
})

it("the composer starts on the person's send key: with Ctrl+Enter chosen, Enter breaks the line", async () => {
  useClientSettings.setState({ sendKey: 'modEnter' })
  runs([])
  workspaces([REPO])
  wrap(<WorkStart />)
  const composer = await screen.findByRole('textbox', {
    name: 'First message (optional)',
  })
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled(),
  )
  const user = userEvent.setup()
  await user.type(composer, 'first line{Enter}second')
  expect(launch.launchSession).not.toHaveBeenCalled()
  await user.keyboard('{Control>}{Enter}{/Control}')
  await waitFor(() => expect(launch.launchSession).toHaveBeenCalledOnce())
  expect(launch.launchSession.mock.calls[0][0].message).toBe(
    'first line\nsecond',
  )
})

it('the composer does not start on Alt+Enter, as a session composer does not', async () => {
  runs([])
  workspaces([REPO])
  wrap(<WorkStart />)
  const composer = await screen.findByRole('textbox', {
    name: 'First message (optional)',
  })
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled(),
  )
  const user = userEvent.setup()
  await user.type(composer, 'first line')
  await user.keyboard('{Alt>}{Enter}{/Alt}')
  expect(launch.launchSession).not.toHaveBeenCalled()
})

it('the composer starts on Enter with its pills, and the next composer opens on that choice', async () => {
  runs([])
  const OTHER = {
    workspace_ref: 'ws_2',
    root_path: '/srv/work/other',
    state: 'active',
  }
  workspaces([REPO, OTHER])
  vi.spyOn(agentOpsApi, 'previewProfile').mockResolvedValue({
    reason: 'own_login',
    profile: {
      profile_ref: 'ppf_claude',
      driver: 'claude',
      environment_ref: 'xenv_1',
      state: 'active',
      local_environment: true,
      operable: true,
    },
  })
  vi.spyOn(agentOpsApi, 'profileModels').mockResolvedValue({
    profile_ref: 'ppf_claude',
    driver: 'claude',
    state: 'fresh',
    default_model: '',
    free_text: true,
    checked_at: '2026-10-12T10:00:00Z',
    refresh_interval_seconds: 120,
    models: [
      {
        id: 'opus-5-5',
        display_name: 'Claude Opus 5.5',
        is_default: true,
        effort_levels: ['low', 'medium', 'high'],
      },
      {
        id: 'haiku-5-5',
        display_name: 'Claude Haiku 5.5',
        effort_levels: ['low', 'high'],
      },
    ],
  })
  const user = userEvent.setup()
  wrap(<WorkStart />)
  const composer = await screen.findByRole('textbox', {
    name: 'First message (optional)',
  })
  await user.click(await screen.findByRole('button', { name: /^Model: / }))
  await user.click(await screen.findByText('Claude Haiku 5.5'))
  await user.click(screen.getByRole('button', { name: /^Effort: / }))
  await user.click(await screen.findByRole('menuitemradio', { name: 'High' }))
  await user.click(screen.getByRole('button', { name: /^Folder: / }))
  await user.click(
    await screen.findByRole('menuitemradio', { name: '/srv/work/other' }),
  )
  await user.type(composer, 'fix the tests{Enter}')
  await waitFor(() => expect(launch.launchSession).toHaveBeenCalledOnce())
  expect(launch.launchSession.mock.calls[0][0]).toMatchObject({
    quick: {
      driver: 'claude',
      model: 'haiku-5-5',
      effort: 'high',
      workspace_ref: 'ws_2',
    },
    message: 'fix the tests',
  })
  // The start is remembered where Settings › New sessions reads it.
  expect(useClientSettings.getState().sessionDefaults).toEqual({
    tool: 'claude',
    account: '',
    model: 'haiku-5-5',
    effort: 'high',
    folder: 'ws_2',
  })
  cleanup()
  wrap(<WorkStart />)
  expect(
    await screen.findByRole('button', {
      name: 'Tool and account: Claude Code',
    }),
  ).toBeVisible()
  expect(
    await screen.findByRole('button', { name: 'Model: Claude Haiku 5.5' }),
  ).toBeVisible()
  expect(
    await screen.findByRole('button', { name: 'Effort: High' }),
  ).toBeVisible()
  expect(
    await screen.findByRole('button', { name: 'Folder: /srv/work/other' }),
  ).toBeVisible()
})

it('New session focuses the composer, and More options hands the advanced launch its text and pills', async () => {
  runs([])
  workspaces([REPO])
  useNewSessionDialog.setState({ advanced: null, opener: null })
  const user = userEvent.setup()
  wrap(<WorkStart />)
  const composer = await screen.findByRole('textbox', {
    name: 'First message (optional)',
  })
  act(() => useNewSessionDialog.getState().compose())
  expect(composer).toHaveFocus()
  await user.type(composer, 'review the repo')
  await user.click(await screen.findByRole('button', { name: /^Folder: / }))
  await user.click(
    await screen.findByRole('menuitemradio', { name: '/srv/work/repo' }),
  )
  await user.click(screen.getByRole('button', { name: /^Folder: / }))
  await user.click(
    await screen.findByRole('menuitem', { name: 'More options' }),
  )
  expect(useNewSessionDialog.getState().advanced).toMatchObject({
    open: true,
    message: 'review the repo',
    driver: 'claude',
    workspaceRef: 'ws_1',
  })
  // Focus comes back to the text when the advanced launch closes.
  expect(useNewSessionDialog.getState().opener).toBe(composer)
  expect(launch.launchSession).not.toHaveBeenCalled()
})

it('starts in the saved registered folder without a typed path', async () => {
  runs([])
  workspaces([REPO])
  useClientSettings.setState({
    sessionDefaults: {
      tool: '',
      account: '',
      model: '',
      effort: '',
      folder: 'ws_1',
    },
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
    sessionDefaults: {
      tool: '',
      account: '',
      model: '',
      effort: '',
      folder: 'removed',
    },
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
  it('requires a folder choice before the first message starts a fresh session', async () => {
    const user = userEvent.setup()
    runs([])
    wrap(<StartSessionForm composer />)
    const folder = await screen.findByRole('button', {
      name: 'Folder: Choose a folder',
    })
    expect(folder).toHaveFocus()
    const message = screen.getByRole('textbox', {
      name: 'First message (optional)',
    })
    await user.type(message, 'inspect this project{Enter}')
    expect(launch.launchSession).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    await user.click(folder)
    await user.click(
      await screen.findByRole('menuitemradio', { name: 'Another folder…' }),
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Folder' }),
      '/srv/work/project',
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '/srv/work/project',
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
    expect(
      await screen.findByRole('combobox', { name: 'Folder' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Folder' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      folder: '/srv/work/repo',
      workspace_ref: 'ws_1',
    })
  })

  it('lets the user change the folder and requires a choice when the path is emptied', async () => {
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
    await screen.findByRole('combobox', { name: 'Folder' })
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
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    await user.click(screen.getByRole('combobox', { name: 'Folder' }))
    await user.click(
      await screen.findByRole('option', {
        name: 'A new folder for this session',
      }),
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(2))
    expect(launch.launchSession.mock.calls[1][0].quick).toMatchObject({
      folder: '',
      workspace_ref: undefined,
    })
  })

  // Issue: Change folder was a bare text field although the form had read the
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
    await screen.findByRole('combobox', { name: 'Folder' })
    expect(screen.queryByRole('textbox', { name: 'Folder' })).toBeNull()
    const picker = await screen.findByRole('combobox', { name: 'Folder' })
    expect(picker).toHaveTextContent('/srv/work/repo')
    await user.click(picker)
    const options = await screen.findAllByRole('option')
    expect(options.map((o) => o.textContent)).toEqual([
      '/srv/work/repo',
      'payments-api',
      'A new folder for this session',
      'Another folder…',
    ])
    expect(options[0].querySelector('[data-slot="path-text"]')).not.toBeNull()
    expect(options[1].querySelector('[data-slot="path-text"]')).toBeNull()
    for (const option of options.slice(0, 2)) {
      // A select option cannot contain another interactive control.
      expect(option.querySelector('button')).toBeNull()
    }
    await user.keyboard('payments-api{Enter}')
    expect(screen.queryByRole('textbox', { name: 'Folder' })).toBeNull()
    // Show the chosen name, but send its registered path to Start.
    expect(screen.getByRole('combobox', { name: 'Folder' })).toHaveTextContent(
      'payments-api',
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
    await screen.findByRole('combobox', { name: 'Folder' })
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

  it('offers a new folder or a typed path when no folder is registered, and asks for a path only on the second', async () => {
    const user = userEvent.setup()
    runs([])
    workspaces([])
    wrap(<StartSessionForm />)
    await screen.findByRole('combobox', { name: 'Folder' })
    expect(screen.queryByRole('textbox', { name: 'Folder' })).toBeNull()
    await chooseTypedFolder(user)
    expect(screen.getByRole('textbox', { name: 'Folder' })).toHaveFocus()
  })

  // The text field is decided when "Another folder…" opens it: a list that arrives later
  // must not swap it for a Select under the user's hands and drop what they typed.
  it('keeps the path being typed when the registered folders arrive late', async () => {
    const user = userEvent.setup()
    runs([])
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockRejectedValue(
      new Error('offline'),
    )
    const qc = wrap(<StartSessionForm />)
    await screen.findByRole('combobox', { name: 'Folder' })
    await chooseTypedFolder(user)
    await user.type(
      screen.getByRole('textbox', { name: 'Folder' }),
      '/srv/work/typed',
    )
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
      items: [REPO],
      has_more: true,
    } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
    await qc.refetchQueries()
    // Absence proves nothing until the list has arrived.
    await waitFor(() => expect(qc.isFetching()).toBe(0))
    expect(screen.getByRole('combobox', { name: 'Folder' })).toHaveTextContent(
      'Another folder…',
    )
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

  // review on 102dac51: a typed path was remembered as '' (Automatic), not as the folder
  // the engine registered for it.
  it('remembers a typed path as the folder the engine registered for it', async () => {
    const user = userEvent.setup()
    runs([])
    launch.launchSession.mockResolvedValue({
      run_ref: 'run_new',
      workspace_ref: 'ws_typed',
    })
    wrap(<StartSessionForm />)
    await chooseTypedFolder(user)
    await user.type(
      screen.getByRole('textbox', { name: 'Folder' }),
      '/srv/work/typed',
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() =>
      expect(useClientSettings.getState().sessionDefaults.folder).toBe(
        'ws_typed',
      ),
    )
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
    await screen.findByRole('combobox', { name: 'Folder' })
    expect(screen.queryByText(/could not be read/)).toBeNull()
    await chooseTypedFolder(user)
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
    await screen.findByRole('combobox', { name: 'Folder' })
    await chooseTypedFolder(user)
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
    await screen.findByRole('combobox', { name: 'Folder' })
    // No chip about an internal list in the form; the picker says the list is cut.
    expect(screen.queryByText(/Registered folders: 1 loaded/)).toBeNull()
    expect(
      screen.queryByText('Some registered folders are outside this list.'),
    ).toBeNull()
    await user.click(await screen.findByRole('combobox', { name: 'Folder' }))
    expect(
      await screen.findByText('Some registered folders are outside this list.'),
    ).toBeVisible()
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
    await screen.findByRole('combobox', { name: 'Folder' })
    await user.click(await screen.findByRole('combobox', { name: 'Folder' }))
    await user.click(
      await screen.findByRole('option', { name: /payments-api/ }),
    )
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
      items: [REPO],
      has_more: true,
    } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
    await qc.refetchQueries()
    await waitFor(() => expect(qc.isFetching()).toBe(0))
    // The new list has rendered once the open picker says it is cut short.
    const picker = screen.getByRole('combobox', { name: 'Folder' })
    await user.click(picker)
    await screen.findByText('Some registered folders are outside this list.')
    await user.keyboard('{Escape}')
    expect(picker).toHaveTextContent('/srv/work/payments')
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
    expect(
      await screen.findByRole('combobox', { name: 'Folder' }),
    ).toBeInTheDocument()
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
    await screen.findByRole('combobox', { name: 'Folder' })
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
    await new Promise((r) => setTimeout(r, 500))
    const folder = screen.getByRole('combobox', { name: 'Folder' })
    expect(folder).toHaveTextContent('/srv/work/repo')
    expect(folder).not.toHaveTextContent('/srv/work/other')
  })

  // An unreadable session list still allows an explicit private-folder choice.
  it('starts in a new folder when the session list cannot be read', async () => {
    const user = userEvent.setup()
    vi.spyOn(agentOpsApi, 'listRuns').mockRejectedValue(new Error('offline'))
    wrap(<StartSessionForm />)
    expect(
      await screen.findByRole('combobox', { name: 'Folder' }),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('combobox', { name: 'Folder' }))
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
    expect(
      await screen.findByRole('combobox', { name: 'Folder' }),
    ).toBeInTheDocument()
    expect(screen.queryByText('/srv/work/removed')).toBeNull()
  })

  it('requires an explicit new folder when the last folder is no longer registered', async () => {
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
      await screen.findByRole('combobox', { name: 'Folder' }),
    ).toBeInTheDocument()
    expect(screen.queryByText('/srv/work/removed')).toBeNull()
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    await user.click(screen.getByRole('combobox', { name: 'Folder' }))
    await user.click(
      await screen.findByRole('option', {
        name: 'A new folder for this session',
      }),
    )
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
    await screen.findByRole('combobox', { name: 'Folder' })
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
    await screen.findByRole('combobox', { name: 'Folder' })
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
    expect(
      await screen.findByRole('combobox', { name: 'Folder' }),
    ).toBeInTheDocument()
    cleanup()
    workspaces([])
    wrap(<StartSessionForm />, qc)
    expect(
      await screen.findByRole('combobox', { name: 'Folder' }),
    ).toBeInTheDocument()
    expect(screen.queryByText('/srv/work/repo')).toBeNull()
  })

  // An unreadable folder list still allows an explicit private-folder choice.
  it('requires an explicit new folder when the registered folders cannot be read', async () => {
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
      await screen.findByRole('combobox', { name: 'Folder' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    await user.click(screen.getByRole('combobox', { name: 'Folder' }))
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
})

it('opens a session link as an editable draft and launches only on Start', async () => {
  const unicodePrompt = 'Review café & tests\nDo not edit yet.' // language-data: non-ASCII prompt fixture
  vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'listProfiles').mockResolvedValue({
    items: [],
    has_more: false,
  })
  runs([])
  workspaces([REPO])
  link.search =
    '?' +
    new URLSearchParams({
      newSession: '1',
      tool: 'codex',
      workspace: 'ws_1',
      prompt: unicodePrompt,
    })
  useNewSessionDialog.setState({ open: false, opener: null, advanced: null })
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
    readinessOf({ claude: 'own_login', codex: 'own_login' }),
  )
  wrap(<NewSessionHost />)
  expect(
    await screen.findByRole('dialog', { name: 'New session' }),
  ).toBeVisible()
  const prompt = await screen.findByRole('textbox', {
    name: 'First message (optional)',
  })
  expect(prompt).toHaveValue(unicodePrompt)
  await waitFor(() =>
    expect(screen.getByRole('combobox', { name: 'Tool' })).toHaveTextContent(
      'Codex',
    ),
  )
  expect(screen.getByRole('combobox', { name: 'Folder' })).toHaveTextContent(
    '/srv/work/repo',
  )
  expect(launch.launchSession).not.toHaveBeenCalled()
  const user = userEvent.setup()
  await user.clear(prompt)
  await user.type(prompt, 'Review only')
  await user.click(screen.getByRole('button', { name: 'Start session' }))
  await waitFor(() =>
    expect(launch.launchSession).toHaveBeenCalledWith(
      expect.objectContaining({
        quick: expect.objectContaining({
          driver: 'codex',
          workspace_ref: 'ws_1',
        }),
        message: 'Review only',
      }),
      expect.anything(),
    ),
  )
  expect(link.navigate).toHaveBeenCalledWith(
    expect.objectContaining({ replace: true, search: expect.any(Function) }),
  )
  link.search = ''
  useTenantStore.setState({ activeTenant: 'tnt-b' })
  expect(useNewSessionDialog.getState()).toMatchObject({
    open: false,
    draft: null,
  })
})

it('uses registered folder names in the picker and thread header without storage paths', async () => {
  const path = '/var/lib/olivares/files/tenant-example/person-example'
  const workspace = { ...REPO, name: 'My files', root_path: path }
  workspaces([workspace])
  runs([])
  vi.spyOn(agentOpsApi, 'getWorkspace').mockResolvedValue(
    workspace as WorkspaceDTO,
  )
  const user = userEvent.setup()
  const qc = wrap(<WorkStart />)
  await user.click(await screen.findByRole('button', { name: /^Folder:/ }))
  expect(
    await screen.findByRole('menuitemradio', { name: 'My files' }),
  ).toBeVisible()
  expect(document.body.textContent).not.toContain(path)
  await user.click(screen.getByRole('menuitemradio', { name: 'My files' }))
  expect(screen.getByRole('button', { name: 'Folder: My files' })).toBeVisible()

  const { renderIntel } = await import('@/test/intel')
  const { ThreadHeader } = await import('@/features/sessions/thread-header')
  const { mergeSessions } = await import('@/features/sessions/provenance')
  const run = {
    run_ref: 'run-folder',
    state: 'stopped',
    workspace_ref: REPO.workspace_ref,
    workspace_path: path,
  } as RunDTO
  renderIntel(
    <ThreadHeader
      session={mergeSessions([], [run])[0]!}
      run={run}
      caps={[]}
      title="Folder session"
      titleHint="Folder session"
      titleMono={false}
      address="run-folder"
      reference={null}
      pinned={false}
      onTogglePin={null}
    />,
    { queryClient: qc },
  )
  await waitFor(() =>
    expect(screen.getByTestId('narrative-folder')).toHaveTextContent(
      'My files',
    ),
  )
  expect(screen.getByTestId('narrative-folder')).not.toHaveAttribute(
    'title',
    path,
  )
  expect(document.body.textContent).not.toContain('tenant-example')
  expect(document.body.textContent).not.toContain('person-example')
})

describe('a person who is given folders and tools', () => {
  beforeEach(() => {
    auth.canRegisterFolders = false
    auth.canOpenTools = false
  })

  it('a typed path says it must be a folder already registered, which an administrator adds', async () => {
    const user = userEvent.setup()
    runs([])
    workspaces([REPO])
    wrap(<StartSessionForm />)
    await chooseTypedFolder(user)
    expect(screen.getByRole('textbox', { name: 'Folder' })).toHaveFocus()
    expect(
      screen.getByText(
        'The path of a folder already registered on this server. An administrator registers new ones.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText('A folder on this server.')).toBeNull()
  })

  it('a person who registers folders keeps the hint that any folder on this server will do', async () => {
    auth.canRegisterFolders = true
    const user = userEvent.setup()
    runs([])
    workspaces([REPO])
    wrap(<StartSessionForm />)
    await chooseTypedFolder(user)
    expect(screen.getByText('A folder on this server.')).toBeInTheDocument()
  })

  it('a tool that cannot start says who readies it, with no door to a place the person cannot open', async () => {
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(readinessOf({}))
    runs([])
    workspaces([REPO])
    wrap(<StartSessionForm />)
    expect(
      await screen.findByText(/Ask an administrator to get it ready\./),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Open AI tools' })).toBeNull()
  })
})

describe('documents attached to a new session', () => {
  const MIB = 1024 * 1024
  const doc = (name: string, content = 'abc') => new File([content], name)
  const written = (path: string): WriteResponse => ({
    path,
    size: 3,
    sha256: 'h',
    created: true,
  })
  type User = ReturnType<typeof userEvent.setup>
  const chooseNewFolder = async (user: User) => {
    await user.click(await screen.findByRole('combobox', { name: 'Folder' }))
    await user.click(
      await screen.findByRole('option', {
        name: 'A new folder for this session',
      }),
    )
  }
  const chooseWorktree = async (user: User) =>
    user.click(await screen.findByRole('radio', { name: 'New worktree' }))
  const ONLY_FOLDERS =
    'Documents can be attached to work in one of your folders, in its current checkout.'

  beforeEach(() => {
    runs([{ run_ref: 'run_1', state: 'stopped', workspace_ref: 'ws_1' }])
    workspaces([REPO])
    vi.spyOn(agentOpsApi, 'statFile').mockRejectedValue(
      new ApiError(404, 'not_found', 'not found'),
    )
    // The skills a session gets, read beside the form: the default department's,
    // none here. Answered, so the page under test says only its own failures.
    vi.spyOn(consoleApi, 'listWorkspaces').mockResolvedValue({
      items: [{ id: 'ws-default', is_default: true }],
      has_more: false,
    } as Awaited<ReturnType<typeof consoleApi.listWorkspaces>>)
    vi.spyOn(skillsApi, 'selection').mockResolvedValue({
      digest: 'none',
      members: [],
    })
  })

  it('attaches documents with a button, shows each with its name and size, and removes one', async () => {
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    expect(
      await screen.findByRole('button', { name: 'Attach document' }),
    ).toBeInTheDocument()
    await user.upload(screen.getByTestId('attach-documents-input'), [
      doc('contrato.docx', 'x'.repeat(2048)),
      doc('extracto.pdf'),
    ])
    const items = within(
      screen.getByRole('list', { name: 'Documents' }),
    ).getAllByRole('listitem')
    expect(items.map((li) => li.textContent)).toEqual([
      'contrato.docx2 KiB',
      'extracto.pdf3 B',
    ])
    await user.click(
      screen.getByRole('button', { name: 'Remove extracto.pdf' }),
    )
    expect(screen.queryByText('extracto.pdf')).toBeNull()
    expect(screen.getByText('contrato.docx')).toBeInTheDocument()
  })

  it('holds More options while documents are attached, and says how to keep them', async () => {
    const user = userEvent.setup()
    const onAdvanced = vi.fn()
    vi.spyOn(consoleApi, 'listSecrets').mockResolvedValue({
      sealer_available: true,
      secrets: [],
    } as Awaited<ReturnType<typeof consoleApi.listSecrets>>)
    wrap(<StartSessionForm onAdvanced={onAdvanced} />)
    await screen.findByRole('button', { name: 'Attach document' })
    await user.upload(screen.getByTestId('attach-documents-input'), [
      doc('contrato.docx'),
    ])
    await user.click(screen.getByRole('button', { name: 'More options' }))
    const advanced = await screen.findByRole('button', {
      name: 'Advanced launch options',
    })
    // Held, it stays in the Tab order, says why and opens nothing.
    expect(advanced).toHaveAttribute('aria-disabled', 'true')
    expect(advanced).not.toBeDisabled()
    await user.click(advanced)
    expect(onAdvanced).not.toHaveBeenCalled()
    expect(
      screen.getByText(
        'Start here to keep the attached documents, or remove them to open more options.',
      ),
    ).toBeVisible()
    await user.click(
      screen.getByRole('button', { name: 'Remove contrato.docx' }),
    )
    const released = screen.getByRole('button', {
      name: 'Advanced launch options',
    })
    expect(released).not.toHaveAttribute('aria-disabled')
    await user.click(released)
    expect(onAdvanced).toHaveBeenCalledTimes(1)
  })

  it('holds More options for a folder path typed in, and says how to keep it', async () => {
    const user = userEvent.setup()
    const onAdvanced = vi.fn()
    vi.spyOn(consoleApi, 'listSecrets').mockResolvedValue({
      sealer_available: true,
      secrets: [],
    } as Awaited<ReturnType<typeof consoleApi.listSecrets>>)
    wrap(<StartSessionForm onAdvanced={onAdvanced} />)
    await user.click(await screen.findByRole('combobox', { name: 'Folder' }))
    await user.click(
      await screen.findByRole('option', { name: 'Another folder…' }),
    )
    await user.type(
      await screen.findByRole('textbox', { name: 'Folder' }),
      '/srv/work/new-place',
    )
    await user.click(screen.getByRole('button', { name: 'More options' }))
    expect(
      await screen.findByRole('button', { name: 'Advanced launch options' }),
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      screen.getByText(
        'Start here to keep the folder you typed, or pick it from the folder list to open more options.',
      ),
    ).toBeVisible()
    expect(onAdvanced).not.toHaveBeenCalled()
  })

  it('shows a document by the path it gets in the folder: whole on hover, its end kept', async () => {
    const LONG = 'escritura-de-compraventa-del-local-firmada-2026.pdf'
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    await user.upload(
      await screen.findByTestId('attach-documents-input'),
      doc(LONG),
    )
    const [item] = within(
      screen.getByRole('list', { name: 'Documents' }),
    ).getAllByRole('listitem')
    const name = within(item).getByTitle(LONG)
    expect(name).toHaveTextContent(LONG)
    // Cut in its middle: the extension sits in the part that is kept whole.
    expect(within(name).getByText(/\.pdf$/)).toBeInTheDocument()
  })

  it("refuses a file over the folder's own limit when it is attached, naming the file and that limit", async () => {
    workspaces([{ ...REPO, max_read_bytes: 5 * MIB }])
    const user = userEvent.setup()
    const write = vi.spyOn(agentOpsApi, 'writeFile')
    wrap(<StartSessionForm />)
    const big = doc('escrituras.pdf')
    Object.defineProperty(big, 'size', { value: 6 * MIB })
    await user.upload(await screen.findByTestId('attach-documents-input'), [
      big,
      doc('contrato.docx'),
    ])
    // The one alert on the page, and all it says.
    expect(screen.getByRole('alert').textContent).toBe(
      "escrituras.pdf is over this folder's limit of 5 MiB per file and was not attached.",
    )
    expect(
      screen.queryByRole('button', { name: 'Remove escrituras.pdf' }),
    ).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Remove contrato.docx' }),
    ).toBeInTheDocument()
    expect(write).not.toHaveBeenCalled()
  })

  it('says a document with the name of one already attached is attached once', async () => {
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    const input = await screen.findByTestId('attach-documents-input')
    await user.upload(input, doc('contrato.docx'))
    await user.upload(input, doc('contrato.docx', 'another'))
    expect(
      screen.getByText('contrato.docx is already attached.'),
    ).toHaveAttribute('role', 'status')
    expect(
      within(screen.getByRole('list', { name: 'Documents' })).getAllByRole(
        'listitem',
      ),
    ).toHaveLength(1)
  })

  it('saves the documents in the folder at Start under its authority, says so, and names them in the first message', async () => {
    const user = userEvent.setup()
    let land: (w: WriteResponse) => void = () => {}
    const write = vi
      .spyOn(agentOpsApi, 'writeFile')
      .mockReturnValue(new Promise((resolve) => (land = resolve)))
    wrap(<StartSessionForm />)
    await user.type(
      screen.getByRole('textbox', { name: 'First message (optional)' }),
      'Find the payment dates',
    )
    const file = doc('contrato.docx')
    await user.upload(await screen.findByTestId('attach-documents-input'), file)
    // The line Start adds to the message is shown before Start.
    expect(screen.getByText('Added to your first message:')).toBeVisible()
    expect(
      screen.getByText(
        'Documents I attached, saved in the folder: - contrato.docx',
      ),
    ).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    expect(
      await screen.findByText('Saving contrato.docx in the folder (1 of 1)…'),
    ).toHaveAttribute('role', 'status')
    expect(write).toHaveBeenCalledWith(
      'ws_1',
      'contrato.docx',
      file,
      expect.objectContaining({
        tenant: 'tnt-a',
        signal: expect.any(AbortSignal),
        dispatchGuard: expect.any(Function),
      }),
    )
    expect(agentOpsApi.statFile).toHaveBeenCalledWith(
      'ws_1',
      'contrato.docx',
      expect.objectContaining({ tenant: 'tnt-a' }),
    )
    expect(launch.launchSession).not.toHaveBeenCalled()
    await act(async () => land(written('contrato.docx')))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    const [launched] = launch.launchSession.mock.calls[0]
    expect(launched.quick).toMatchObject({ workspace_ref: 'ws_1' })
    expect(launched.message).toBe(
      'Find the payment dates\n\nDocuments I attached, saved in the folder:\n- contrato.docx',
    )
  })

  it('asks for a few words when documents are attached and the message is empty, so the documents line never names the session', async () => {
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    await user.upload(
      await screen.findByTestId('attach-documents-input'),
      doc('contrato.docx'),
    )
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    expect(
      screen.getByText(
        'Write a few words about what to do with the documents: the session is named by what you write.',
      ),
    ).toBeInTheDocument()
    await user.type(
      screen.getByRole('textbox', { name: 'First message (optional)' }),
      'Summarize',
    )
    expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled()
  })

  it('a document that could not be saved says which and why and which are already in the folder, keeps the text and the others, and is not sent twice', async () => {
    const user = userEvent.setup()
    const write = vi
      .spyOn(agentOpsApi, 'writeFile')
      .mockResolvedValueOnce(written('contrato.docx'))
      .mockRejectedValueOnce(new ApiError(500, 'internal', 'disk full'))
      .mockResolvedValueOnce(written('extracto.pdf'))
    wrap(<StartSessionForm />)
    const field = screen.getByRole('textbox', {
      name: 'First message (optional)',
    })
    await user.type(field, 'Reconcile')
    await user.upload(await screen.findByTestId('attach-documents-input'), [
      doc('contrato.docx'),
      doc('extracto.pdf'),
    ])
    await user.click(screen.getByRole('button', { name: 'Start' }))
    expect((await screen.findByRole('alert')).textContent).toBe(
      'The session did not start: extracto.pdf was not saved in the folder (disk full). Already in the folder: contrato.docx.',
    )
    expect(launch.launchSession).not.toHaveBeenCalled()
    expect(field).toHaveValue('Reconcile')
    expect(
      within(screen.getByRole('list', { name: 'Documents' })).getAllByRole(
        'listitem',
      ),
    ).toHaveLength(2)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(write.mock.calls.map((c) => c[1])).toEqual([
      'contrato.docx',
      'extracto.pdf',
      'extracto.pdf',
    ])
    expect(launch.launchSession.mock.calls[0][0].message).toBe(
      'Reconcile\n\nDocuments I attached, saved in the folder:\n- contrato.docx\n- extracto.pdf',
    )
  })

  it('stops when the dialog closes during Start: the documents after the one being saved are never sent', async () => {
    const user = userEvent.setup()
    let land: (w: WriteResponse) => void = () => {}
    const write = vi
      .spyOn(agentOpsApi, 'writeFile')
      .mockReturnValueOnce(new Promise((resolve) => (land = resolve)))
      .mockResolvedValue(written('later.pdf'))
    // What the dialog reads before it shows the form, as when a link opens it.
    vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
      items: [],
      has_more: false,
    })
    vi.spyOn(agentOpsApi, 'listProfiles').mockResolvedValue({
      items: [],
      has_more: false,
    })
    useNewSessionDialog.setState({
      open: true,
      opener: null,
      advanced: null,
      draft: null,
    })
    wrap(<NewSessionHost />)
    const dialog = await screen.findByRole('dialog', { name: 'New session' })
    await user.type(
      await within(dialog).findByRole('textbox', {
        name: 'First message (optional)',
      }),
      'Compare them',
    )
    await user.upload(await screen.findByTestId('attach-documents-input'), [
      doc('a.pdf'),
      doc('b.pdf'),
      doc('c.pdf'),
    ])
    await user.click(screen.getByRole('button', { name: 'Start session' }))
    await waitFor(() => expect(write).toHaveBeenCalledTimes(1))
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await act(async () => land(written('a.pdf')))
    expect(write).toHaveBeenCalledTimes(1)
    expect(write.mock.calls[0][1]).toBe('a.pdf')
    expect(agentOpsApi.statFile).toHaveBeenCalledTimes(1)
    expect(launch.launchSession).not.toHaveBeenCalled()
  })

  it.each([
    { place: 'a new folder for the session', choose: chooseNewFolder },
    { place: 'a worktree of its own', choose: chooseWorktree },
  ])(
    'offers no attach control for $place, and one line says where documents can be attached',
    async ({ choose }) => {
      const user = userEvent.setup()
      wrap(<StartSessionForm />)
      expect(
        await screen.findByRole('button', { name: 'Attach document' }),
      ).toBeInTheDocument()
      await choose(user)
      expect(
        screen.queryByRole('button', { name: 'Attach document' }),
      ).toBeNull()
      expect(screen.getByText(ONLY_FOLDERS)).toBeInTheDocument()
    },
  )

  it('offers no attach control in a read-only folder, and says why', async () => {
    workspaces([{ ...REPO, mount_mode: 'ro' }])
    wrap(<StartSessionForm />)
    expect(
      await screen.findByText(
        'This folder is read-only: documents can be attached to work in a folder the session can write in.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Attach document' })).toBeNull()
  })

  it('keeps documents attached before the place changed to one that takes none, as waiting, and Start says why until they are removed', async () => {
    const user = userEvent.setup()
    wrap(<StartSessionForm />)
    await user.type(
      screen.getByRole('textbox', { name: 'First message (optional)' }),
      'Read it',
    )
    await user.upload(
      await screen.findByTestId('attach-documents-input'),
      doc('contrato.docx'),
    )
    await chooseNewFolder(user)
    const [item] = within(
      screen.getByRole('list', { name: 'Documents' }),
    ).getAllByRole('listitem')
    expect(item).toHaveTextContent('contrato.docx3 B · waiting')
    expect(screen.getByText(ONLY_FOLDERS)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    expect(
      screen.getByText(
        'Start waits for the attached documents: choose a folder that can take them, or remove them.',
      ),
    ).toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: 'Remove contrato.docx' }),
    )
    expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled()
  })

  it('offers the attach control to a person who may write into folders, and none to one who may not', async () => {
    wrap(<StartSessionForm />)
    expect(
      await screen.findByRole('button', { name: 'Attach document' }),
    ).toBeInTheDocument()
    cleanup()
    auth.canWriteFiles = false
    wrap(<StartSessionForm />)
    expect(
      await screen.findByRole('combobox', { name: 'Folder' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Attach document' })).toBeNull()
  })
})
