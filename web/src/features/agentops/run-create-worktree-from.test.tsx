// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The launch dialog opened for the work a handoff names (K4.A2): the worktree is chosen
// for the person, the commit it starts at is on screen, and that start is sent with the
// worktree. A dialog opened without a start is the dialog it always was: ticking the
// worktree sends no start.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// The one permission a case takes away.
const denied = vi.hoisted(() => ({ permission: '' }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) => permission !== denied.permission,
  }),
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
import { ApiError } from '@/lib/api/errors'
import { agentOpsApi } from './api'
import { fixtureReadiness } from './launch-readiness.fixture'
import { RunCreateDialog } from './run-create-dialog'
import type { ProviderProfileDTO } from './types'

const SHA = '0123456789abcdef0123456789abcdef01234567'

const homeA: ProviderProfileDTO = {
  profile_ref: 'ppf_a',
  driver: 'claude',
  environment_ref: 'xenv_1',
  display_name: 'Home A',
  state: 'active',
  local_environment: true,
  operable: true,
}

function wrap(initialWorktreeFrom?: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RunCreateDialog
        open
        onOpenChange={vi.fn()}
        initialWorktreeFrom={initialWorktreeFrom}
      />
    </QueryClientProvider>,
  )
}

const postedBody = () =>
  vi.mocked(agentOpsApi.createRun).mock.calls[0][0] as unknown as Record<
    string,
    unknown
  >

async function chooseFolder(user: ReturnType<typeof userEvent.setup>) {
  const toggle = await screen.findByRole('button', { name: 'Advanced options' })
  if (toggle.getAttribute('aria-expanded') !== 'true') await user.click(toggle)
  expect(
    await screen.findByText('Local requirements checked'),
  ).toBeInTheDocument()
  await user.click(screen.getByLabelText('Folder'))
  await user.click(
    await screen.findByRole('option', { name: /Project folder/ }),
  )
}

const worktreeBox = () =>
  screen.getByRole('checkbox', { name: 'Work in a new git worktree' })

const folderPicker = () => screen.getByRole('combobox', { name: 'Folder' })

beforeEach(() => {
  vi.clearAllMocks()
  denied.permission = ''
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
  vi.mocked(agentOpsApi.createRun).mockResolvedValue({} as never)
  vi.mocked(agentOpsApi.profileLaunchReadiness).mockImplementation(
    async (ref: string) => fixtureReadiness({ profile_ref: ref }),
  )
})

describe('RunCreateDialog — opened for the work a handoff names', () => {
  it('blocks Start and Enter until a folder is chosen', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await screen.findByText('Local requirements checked')
    const start = screen.getByRole('button', { name: 'Start' })
    expect(start).toBeDisabled()
    expect(
      screen.getByText('Choose a repository folder for the worktree.'),
    ).toBeInTheDocument()
    await user.click(start)
    fireEvent.submit(start.closest('form')!)
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
    await chooseFolder(user)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled(),
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody()).toMatchObject({
      workspace_ref: 'folder-a',
      worktree: true,
      worktree_from: SHA,
    })
  })

  it('shows the requested start before a folder is chosen', async () => {
    wrap(SHA)
    await screen.findByText('Local requirements checked')
    expect(worktreeBox()).toBeChecked()
    expect(screen.getByTestId('create-worktree-from')).toHaveTextContent(
      `Starts at ${SHA}`,
    )
  })

  it('keeps the worktree request when the folder is cleared', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await chooseFolder(user)
    await user.click(screen.getByLabelText('Folder'))
    await user.click(
      await screen.findByRole('option', {
        name: 'Default (My files)',
      }),
    )
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    expect(worktreeBox()).toBeChecked()
    expect(screen.getByTestId('create-worktree-from')).toHaveTextContent(SHA)
  })

  it('allows the ordinary temporary-folder launch when the worktree is explicitly unticked', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await screen.findByText('Local requirements checked')
    await user.click(worktreeBox())
    expect(screen.queryByTestId('create-worktree-from')).not.toBeInTheDocument()
    const start = screen.getByRole('button', { name: 'Start' })
    await waitFor(() => expect(start).toBeEnabled())
    await user.click(start)
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('')
    expect(postedBody()).not.toHaveProperty('worktree')
    expect(postedBody()).not.toHaveProperty('worktree_from')
  })

  it('has the worktree chosen and says where it starts', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await chooseFolder(user)
    expect(worktreeBox()).toBeChecked()
    expect(screen.getByTestId('create-worktree-from')).toHaveTextContent(
      `Starts at ${SHA}`,
    )
  })

  it('sends the start with the worktree', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await chooseFolder(user)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('folder-a')
    expect(postedBody().worktree).toBe(true)
    expect(postedBody().worktree_from).toBe(SHA)
    expect(postedBody()).not.toHaveProperty('from_checkpoint')
  })

  it('sends neither when the person unticks the worktree', async () => {
    const user = userEvent.setup()
    wrap(SHA)
    await chooseFolder(user)
    await user.click(worktreeBox())
    // The line that says where it starts goes with the choice it belongs to.
    expect(screen.queryByTestId('create-worktree-from')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody()).not.toHaveProperty('worktree')
    expect(postedBody()).not.toHaveProperty('worktree_from')
  })

  it('sends no start when it was opened without one', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    expect(screen.queryByTestId('create-worktree-from')).not.toBeInTheDocument()
    await user.click(worktreeBox())
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().worktree).toBe(true)
    expect(postedBody()).not.toHaveProperty('worktree_from')
    expect(postedBody()).not.toHaveProperty('from_checkpoint')
  })

  it('clears an ordinary worktree choice when its folder is cleared', async () => {
    const user = userEvent.setup()
    wrap()
    await chooseFolder(user)
    await user.click(worktreeBox())
    await user.click(screen.getByLabelText('Folder'))
    await user.click(
      await screen.findByRole('option', {
        name: 'Default (My files)',
      }),
    )
    expect(
      screen.queryByRole('checkbox', { name: 'Work in a new git worktree' }),
    ).not.toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled(),
    )
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(postedBody().workspace_ref).toBe('')
    expect(postedBody()).not.toHaveProperty('worktree')
  })
})

// Edit from here, with the files as they were at the message: the new session starts in a
// worktree of its own made from that message's checkpoint. The worktree stays on, since the
// engine refuses those files without one; each refusal about the files is said in the
// person's words, by its code, with the folder as it is now as the way on.
describe('RunCreateDialog — opened for the files of a message', () => {
  const locked =
    'On, because the new session starts from the files as they were at a message, in a copy of its own.'
  const fromNow = 'Start from the folder as it is now'
  const folderLocked =
    'Set to the folder the message was sent in, because its files come only from there.'
  const notKept = 'The files of this message are no longer kept.'
  const notTaken = 'The files of this message could not be taken.'
  const notGit =
    "This folder is not a Git repository, so the new session cannot have a copy of its own with this message's files."
  const notYet =
    "This server cannot start a session from a message's files yet."
  const unlisted =
    'The folder this message was sent in is not in your list of folders, so a copy with its files cannot be made here.'
  const onOpenChange = vi.fn()

  const from = { run: 'run_1', turn: 2 }
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const dialog = (open: boolean) => (
    <QueryClientProvider client={qc}>
      <RunCreateDialog
        open={open}
        onOpenChange={onOpenChange}
        initialMessage="the edited words"
        initialWorkspaceRef="folder-a"
        initialFromCheckpoint={from}
      />
    </QueryClientProvider>
  )

  function openForMessage() {
    qc.clear()
    return render(dialog(true))
  }

  const createCalls = () => vi.mocked(agentOpsApi.createRun).mock.calls

  async function start(user: ReturnType<typeof userEvent.setup>) {
    const sent = createCalls().length
    const button = screen.getByRole('button', { name: 'Start' })
    await waitFor(() => expect(button).toBeEnabled())
    await user.click(button)
    await waitFor(() => expect(createCalls()).toHaveLength(sent + 1))
    return createCalls()[sent][0] as unknown as Record<string, unknown>
  }

  it('keeps the worktree on and locked, and says why', async () => {
    openForMessage()
    await screen.findByText('Local requirements checked')
    expect(worktreeBox()).toBeChecked()
    expect(worktreeBox()).toBeDisabled()
    expect(worktreeBox()).toHaveAccessibleDescription(
      expect.stringContaining(locked),
    )
  })

  it('sends exactly the worktree and the turn of that message', async () => {
    const user = userEvent.setup()
    openForMessage()
    await screen.findByText('Local requirements checked')
    const body = await start(user)
    expect(body).toMatchObject({ workspace_ref: 'folder-a', worktree: true })
    expect(body.from_checkpoint).toEqual({ run: 'run_1', turn: 2 })
    expect(body).not.toHaveProperty('worktree_from')
  })

  it('keeps the folder of the message while its files are asked for', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.createRun).mockRejectedValueOnce(
      new ApiError(
        404,
        'checkpoint_not_found',
        'this session has no such turn',
      ),
    )
    openForMessage()
    await screen.findByText('Local requirements checked')
    expect(folderPicker()).toBeDisabled()
    await user.click(folderPicker())
    expect(
      screen.queryByRole('option', { name: 'Default (My files)' }),
    ).toBeNull()
    expect(folderPicker()).toHaveTextContent('Project folder')
    // Leaving the files of the message frees the folder.
    await start(user)
    await user.click(await screen.findByRole('button', { name: fromNow }))
    expect(folderPicker()).toBeEnabled()
    await user.click(folderPicker())
    await user.click(
      await screen.findByRole('option', { name: 'Default (My files)' }),
    )
    const body = await start(user)
    expect(body.workspace_ref).toBe('')
    expect(body).not.toHaveProperty('from_checkpoint')
  })

  it('says why the folder cannot be changed, and only while it cannot', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.createRun).mockRejectedValueOnce(
      new ApiError(
        404,
        'checkpoint_not_found',
        'this session has no such turn',
      ),
    )
    openForMessage()
    await screen.findByText('Local requirements checked')
    expect(folderPicker()).toHaveAccessibleDescription(folderLocked)
    expect(screen.getByText(folderLocked)).toBeInTheDocument()
    await start(user)
    await user.click(await screen.findByRole('button', { name: fromNow }))
    expect(folderPicker()).not.toHaveAccessibleDescription(folderLocked)
    expect(screen.queryByText(folderLocked)).toBeNull()
  })

  it('asks for the files of the message again when the same request opens again', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.createRun).mockRejectedValueOnce(
      new ApiError(
        404,
        'checkpoint_not_found',
        'this session has no such turn',
      ),
    )
    const view = openForMessage()
    await screen.findByText('Local requirements checked')
    await start(user)
    await user.click(await screen.findByRole('button', { name: fromNow }))
    expect(worktreeBox()).not.toBeChecked()
    view.rerender(dialog(false))
    view.rerender(dialog(true))
    await screen.findByText('Local requirements checked')
    expect(worktreeBox()).toBeChecked()
    expect(worktreeBox()).toBeDisabled()
    const body = await start(user)
    expect(body).toMatchObject({ worktree: true, from_checkpoint: from })
  })

  it('puts the folder of the message back when the same request opens again', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.createRun).mockRejectedValueOnce(
      new ApiError(
        404,
        'checkpoint_not_found',
        'this session has no such turn',
      ),
    )
    const view = openForMessage()
    await screen.findByText('Local requirements checked')
    await start(user)
    await user.click(await screen.findByRole('button', { name: fromNow }))
    await user.click(folderPicker())
    await user.click(
      await screen.findByRole('option', { name: 'Default (My files)' }),
    )
    view.rerender(dialog(false))
    view.rerender(dialog(true))
    await screen.findByText('Local requirements checked')
    expect(folderPicker()).toBeDisabled()
    expect(folderPicker()).toHaveTextContent('Project folder')
    const body = await start(user)
    expect(body).toMatchObject({
      workspace_ref: 'folder-a',
      worktree: true,
      from_checkpoint: from,
    })
  })

  it.each([
    // Archived since, or past the list's end.
    ['is not in the list', ''],
    // Without the right to list folders, the list is never read.
    ['cannot be listed for the person', 'sessions:workspace:read'],
  ])(
    'says why Start waits when the folder of the message %s, and starts from the folder as it is now',
    async (_case, permission) => {
      const user = userEvent.setup()
      denied.permission = permission
      vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
        items: [],
        has_more: false,
      })
      openForMessage()
      await screen.findByText('Local requirements checked')
      const button = screen.getByRole('button', { name: 'Start' })
      await waitFor(() =>
        expect(button).toHaveAccessibleDescription(
          expect.stringContaining(unlisted),
        ),
      )
      expect(button).toBeDisabled()
      await user.click(screen.getByRole('button', { name: fromNow }))
      expect(screen.queryByText(unlisted)).toBeNull()
      expect(folderPicker()).toBeEnabled()
      expect(worktreeBox()).not.toBeChecked()
      expect(worktreeBox()).toBeEnabled()
      const body = await start(user)
      expect(body.workspace_ref).toBe('folder-a')
      expect(body).not.toHaveProperty('worktree')
      expect(body).not.toHaveProperty('from_checkpoint')
    },
  )

  // Until the folders are read, the folder of the message is not known to be listed:
  // Start says so, and asks for no folder the person cannot choose.
  it('says the folders are being read while Start waits for them', async () => {
    vi.mocked(agentOpsApi.listWorkspaces).mockImplementation(
      () => new Promise(() => {}),
    )
    openForMessage()
    const button = await screen.findByRole('button', { name: 'Start' })
    await waitFor(() =>
      expect(button).toHaveAccessibleDescription(
        expect.stringContaining('Reading your folders…'),
      ),
    )
    expect(button).toBeDisabled()
    expect(button).not.toHaveAccessibleDescription(
      expect.stringContaining('Choose a repository folder for the worktree.'),
    )
    expect(button).not.toHaveAccessibleDescription(
      expect.stringContaining(unlisted),
    )
  })

  // A list of folders that could not be read says nothing of the folder of the message:
  // Start says the folders could not be read, with Retry, and starts once a read answers.
  it('says the folders could not be read, with Retry, and never that the folder is not listed', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.listWorkspaces).mockRejectedValueOnce(
      new ApiError(500, 'module_error', 'could not list the folders'),
    )
    openForMessage()
    const button = await screen.findByRole('button', { name: 'Start' })
    await waitFor(() =>
      expect(button).toHaveAccessibleDescription(
        expect.stringContaining('Your folders could not be read'),
      ),
    )
    expect(button).toBeDisabled()
    expect(button).not.toHaveAccessibleDescription(
      expect.stringContaining(unlisted),
    )
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await start(user)).toMatchObject({
      workspace_ref: 'folder-a',
      worktree: true,
      from_checkpoint: from,
    })
  })

  // Starting from the folder as it is now needs no list of folders: beside Retry, it is the
  // one step that is true while the list cannot be read.
  it('offers the folder as it is now beside Retry when the folders could not be read', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.listWorkspaces).mockRejectedValue(
      new ApiError(500, 'module_error', 'could not list the folders'),
    )
    openForMessage()
    const button = await screen.findByRole('button', { name: 'Start' })
    await waitFor(() =>
      expect(button).toHaveAccessibleDescription(
        expect.stringContaining('Your folders could not be read'),
      ),
    )
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: fromNow }))
    const body = await start(user)
    expect(body.workspace_ref).toBe('folder-a')
    expect(body).not.toHaveProperty('worktree')
    expect(body).not.toHaveProperty('from_checkpoint')
  })

  it('starts nothing on Cancel', async () => {
    const user = userEvent.setup()
    openForMessage()
    await screen.findByText('Local requirements checked')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })

  it.each([
    ['checkpoint_not_found', 404, 'this session has no such turn', notKept],
    [
      'checkpoint_files_gone',
      500,
      'the files of that message are no longer kept',
      notTaken,
    ],
    [
      'not_a_git_repository',
      422,
      'A worktree needs a git repository, and /project is not one: run git init there, or start without a worktree.',
      notGit,
    ],
    // An engine before the field refuses it as an unknown one.
    [
      'module_error',
      400,
      'invalid request body: unknown field "from_checkpoint"',
      notYet,
    ],
  ])(
    "says %s (%i) in the person's words, and the folder as it is now asks for neither",
    async (code, status, message, sentence) => {
      const user = userEvent.setup()
      vi.mocked(agentOpsApi.createRun).mockRejectedValueOnce(
        new ApiError(status, code, message),
      )
      openForMessage()
      await screen.findByText('Local requirements checked')
      await start(user)
      const alert = (await screen.findByText(sentence)).closest(
        '[role="alert"]',
      ) as HTMLElement
      expect(alert).not.toBeNull()
      expect(alert).not.toHaveTextContent(message)
      await user.click(within(alert).getByRole('button', { name: fromNow }))
      expect(screen.queryByText(sentence)).toBeNull()
      expect(worktreeBox()).not.toBeChecked()
      expect(worktreeBox()).toBeEnabled()
      const body = await start(user)
      expect(body.workspace_ref).toBe('folder-a')
      expect(body).not.toHaveProperty('worktree')
      expect(body).not.toHaveProperty('from_checkpoint')
    },
  )

  it.each([
    [500, 'the fork could not take the files of that message: disk full'],
    [404, 'session not found'],
    // A 400 is the server not knowing the field only when it names that field.
    [400, 'invalid request body: invalid value for field "message"'],
    [400, 'invalid request body: unknown field "effort"'],
    [
      400,
      'invalid request body: invalid value for field "from_checkpoint.turn"',
    ],
    [
      422,
      'from_checkpoint names a session of another folder: fork it in its own folder',
    ],
  ])(
    'says any other refusal (%i) as New session says it',
    async (status, message) => {
      const user = userEvent.setup()
      vi.mocked(agentOpsApi.createRun).mockRejectedValueOnce(
        new ApiError(status, 'module_error', message),
      )
      openForMessage()
      await screen.findByText('Local requirements checked')
      await start(user)
      const alert = (await screen.findByText(message)).closest(
        '[role="alert"]',
      ) as HTMLElement
      expect(alert).not.toBeNull()
      for (const sentence of [notKept, notTaken, notGit, notYet])
        expect(alert).not.toHaveTextContent(sentence)
      expect(within(alert).queryByRole('button', { name: fromNow })).toBeNull()
    },
  )
})
