// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Edit from here, on a sent message: change the words, choose where the new session's files
// come from, continue. The folder as it is now is chosen first; the files as they were at
// this message are offered only for the one turn its input began, named by the engine's
// input id on both, in a copy of the new session's own. No file of this session or its
// folder is put back, at any step.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const rights = vi.hoisted(() => ({ write: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    principal: { kind: 'user', actor: 'alice', user_id: 'alice' },
    can: (permission: string) =>
      permission !== 'sessions:run:write' || rights.write,
  }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    runCheckpoints: vi.fn(),
    gitStatus: vi.fn(),
    rewindRun: vi.fn(),
  },
}))

import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type {
  RunCheckpointDTO,
  RunCheckpointsDTO,
  RunDTO,
} from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { EditFromHere } from './edit-from-here'

// A session the person launched in the registered folder itself, and one in a worktree
// of its own.
const inFolder = {
  run_ref: 'run_1',
  state: 'idle',
  workspace_ref: 'ws_1',
  workspace_path: '/project',
  acts_for: { kind: 'user', user_id: 'alice' },
} as unknown as RunDTO
const inWorktree = {
  ...inFolder,
  worktree_branch: 'olivares/run_1',
} as unknown as RunDTO

// A kept turn, with the id of the accepted input that began it (`in_<seq>` unless said;
// null: the engine sent none).
function turn(
  seq: number,
  prompt: string,
  inputId: string | null = `in_${seq}`,
): RunCheckpointDTO {
  return {
    seq,
    prompt,
    clipped: false,
    at: '2026-11-02T10:00:00Z',
    open: false,
    files: [],
    truncated: false,
    ...(inputId ? { input_id: inputId } : {}),
  }
}

function read(turns: RunCheckpointDTO[], available = true): RunCheckpointsDTO {
  return { available, turns, files: [], truncated: false }
}

const keep = 'Start from the folder as it is now'
const files = 'Start from the files as they were at this message'
const keepNote =
  'The new session works in this folder, with its files as they are now.'
const ownCopy =
  "The new session works in the folder itself, with its files as they are now, not in this session's own copy."
const filesNote =
  'The new session works in its own copy of the folder, with the files as they were when this message was sent. This session and its folder stay exactly as they are.'
const noFiles =
  'The files of this message were not kept, so the new session can only start from the folder as it is now.'
const filesUnread =
  'The files of this message could not be read just now, so the new session starts from the folder as it is now.'
const filesReading = 'Looking for the files of this message…'
const notGit =
  "This folder is not a Git repository, so the new session cannot have a copy of its own with this message's files."

const onContinue = vi.fn()
const onClose = vi.fn()

// The dialog on a message whose accepted input is `inputId` (`in_2`, the second turn's,
// unless said; null: the engine named none), as the conversation opens it.
function show(
  run = inFolder,
  inputId: string | null = 'in_2',
  text = 'fix the typo',
) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const dialog = () => (
    <QueryClientProvider client={qc}>
      <EditFromHere
        run={run}
        text={text}
        inputId={inputId ?? undefined}
        onContinue={onContinue}
        onClose={onClose}
      />
    </QueryClientProvider>
  )
  const view = render(dialog())
  return {
    // The turns read again while the dialog is open (the Turns pane's poll, the focus).
    readAgain: () =>
      act(() =>
        qc.invalidateQueries({
          queryKey: agentOpsKeys.runCheckpoints('t1', run.run_ref),
        }),
      ),
    // The dialog closed, then opened again on the same message.
    reopen: () => {
      view.unmount()
      render(dialog())
    },
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  rights.write = true
  vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue(
    read([
      turn(1, 'add a readme'),
      turn(2, 'fix the typo'),
      turn(3, 'run the tests'),
    ]),
  )
  vi.mocked(agentOpsApi.gitStatus).mockResolvedValue({} as never)
})

describe('Edit from here', () => {
  it('starts on the folder as it is now, and Continue sends only the edited words', async () => {
    const user = userEvent.setup()
    show()
    const now = await screen.findByRole('radio', { name: keep })
    expect(now).toBeChecked()
    expect(now).toHaveAccessibleDescription(keepNote)
    const field = screen.getByRole('textbox', { name: 'Message' })
    expect(field).toHaveValue('fix the typo')
    await user.clear(field)
    await user.type(field, 'fix the typo in the title')
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    expect(onContinue).toHaveBeenCalledWith(
      'fix the typo in the title',
      undefined,
    )
    expect(onClose).toHaveBeenCalled()
    expect(agentOpsApi.rewindRun).not.toHaveBeenCalled()
  })

  // ON: the message's input id and the one turn that carries it. The words name nothing:
  // every turn here has the message's words, and the turn sent is the id's.
  it('starts from the files of the one turn its input began, in a copy of its own', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue(
      read([
        turn(1, 'fix the typo'),
        turn(2, 'fix the typo'),
        turn(3, 'fix the typo'),
      ]),
    )
    show()
    const back = await screen.findByRole('radio', { name: files })
    expect(back).not.toBeChecked()
    expect(back).toHaveAccessibleDescription(filesNote)
    await user.click(back)
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    expect(onContinue).toHaveBeenCalledWith('fix the typo', {
      run: 'run_1',
      turn: 2,
    })
    expect(agentOpsApi.rewindRun).not.toHaveBeenCalled()
  })

  it("names the first turn for the session's first message", async () => {
    const user = userEvent.setup()
    show(inFolder, 'in_1', 'add a readme')
    await user.click(await screen.findByRole('radio', { name: files }))
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    expect(onContinue).toHaveBeenCalledWith('add a readme', {
      run: 'run_1',
      turn: 1,
    })
  })

  it('offers a session in its own copy the same choice, and reads no Git status for it', async () => {
    const user = userEvent.setup()
    show(inWorktree)
    expect(
      await screen.findByRole('radio', { name: keep }),
    ).toHaveAccessibleDescription(ownCopy)
    await user.click(screen.getByRole('radio', { name: files }))
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    expect(onContinue).toHaveBeenCalledWith('fix the typo', {
      run: 'run_1',
      turn: 2,
    })
    expect(agentOpsApi.gitStatus).not.toHaveBeenCalled()
  })

  // OFF, the message's side: the engine named no input for it, so no turn is read for it,
  // whatever words or ids the kept turns have.
  it('offers only the fork with the edited words, and reads no turns, for a message without an input id', async () => {
    const user = userEvent.setup()
    show(inFolder, null)
    expect(await screen.findByText(noFiles)).toBeInTheDocument()
    expect(screen.getByText(keepNote)).toBeInTheDocument()
    expect(screen.queryByRole('radio')).toBeNull()
    expect(agentOpsApi.runCheckpoints).not.toHaveBeenCalled()
    expect(agentOpsApi.gitStatus).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    expect(onContinue).toHaveBeenCalledWith('fix the typo', undefined)
  })

  // OFF, the turns' side, and every answer that does not name exactly one turn.
  it.each([
    [
      'no kept turn carries an input id',
      read([turn(1, 'add a readme', null), turn(2, 'fix the typo', null)]),
    ],
    [
      'two turns carry its input id',
      read([turn(2, 'fix the typo', 'in_2'), turn(3, 'go on', 'in_2')]),
    ],
    [
      'two turns carry its input id, one of them clipped',
      read([
        { ...turn(2, 'fix the typo'), clipped: true },
        turn(3, 'go on', 'in_2'),
      ]),
    ],
    [
      'its one turn holds only the start of its words',
      read([{ ...turn(2, 'fix the typo'), clipped: true }]),
    ],
    ['no turn carries its input id', read([turn(1, 'add a readme')])],
    ['the session keeps no checkpoints', read([], false)],
  ])(
    'offers only the fork with the edited words when %s, and says why',
    async (_case, checkpoints) => {
      const user = userEvent.setup()
      vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue(checkpoints)
      show()
      expect(await screen.findByText(noFiles)).toBeInTheDocument()
      expect(screen.getByText(keepNote)).toBeInTheDocument()
      expect(screen.queryByRole('radio')).toBeNull()
      await user.click(screen.getByRole('button', { name: 'Continue' }))
      expect(onContinue).toHaveBeenCalledWith('fix the typo', undefined)
    },
  )

  it('says the files of this message are being looked for while the turns are read', () => {
    vi.mocked(agentOpsApi.runCheckpoints).mockImplementation(
      () => new Promise(() => {}),
    )
    show()
    expect(screen.getByText(filesReading)).toHaveAttribute('role', 'status')
    expect(screen.queryByRole('radio')).toBeNull()
    expect(screen.queryByText(noFiles)).toBeNull()
  })

  it('offers no second choice in a folder that is not a Git repository, and says why', async () => {
    vi.mocked(agentOpsApi.gitStatus).mockRejectedValue(
      new ApiError(404, 'not_found', 'not a git repository'),
    )
    show()
    expect(await screen.findByText(notGit)).toBeInTheDocument()
    expect(screen.queryByRole('radio')).toBeNull()
    expect(screen.queryByText(noFiles)).toBeNull()
  })

  it('never says the files were not kept when they could not be read', async () => {
    vi.mocked(agentOpsApi.runCheckpoints).mockRejectedValue(
      new ApiError(500, 'module_error', 'could not read the session turns'),
    )
    show()
    expect(await screen.findByText(filesUnread)).toBeInTheDocument()
    expect(screen.queryByText(noFiles)).toBeNull()
    expect(screen.queryByRole('radio')).toBeNull()
  })

  it('offers no second choice, and reads nothing for it, to a person who cannot start a session', () => {
    rights.write = false
    show()
    expect(screen.getByText(keepNote)).toBeInTheDocument()
    expect(screen.queryByRole('radio')).toBeNull()
    expect(agentOpsApi.runCheckpoints).not.toHaveBeenCalled()
    expect(agentOpsApi.gitStatus).not.toHaveBeenCalled()
  })

  // The engine gives a message's files only to the person who launched the session, and
  // answers anyone else as if the session did not exist.
  it.each([
    ['another person launched', { kind: 'user', user_id: 'bob' }],
    ['an automation dispatched', { kind: 'automation' }],
  ] as const)(
    'offers no second choice, and reads nothing for it, on a session %s',
    (_case, actsFor) => {
      show({ ...inFolder, acts_for: actsFor })
      expect(screen.getByText(keepNote)).toBeInTheDocument()
      expect(screen.queryByRole('radio')).toBeNull()
      expect(agentOpsApi.runCheckpoints).not.toHaveBeenCalled()
      expect(agentOpsApi.gitStatus).not.toHaveBeenCalled()
    },
  )

  // THE ANSWER IS TAKEN ONCE PER OPEN EDIT: a later read of the turns while the dialog
  // is open changes neither the offer nor the turn; opening it again answers anew.
  it('keeps the files on, with the same turn, when a later read finds a second turn with the id', async () => {
    const user = userEvent.setup()
    const dialog = show()
    await screen.findByRole('radio', { name: files })
    vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue(
      read([turn(2, 'fix the typo'), turn(3, 'go on', 'in_2')]),
    )
    await dialog.readAgain()
    expect(agentOpsApi.runCheckpoints).toHaveBeenCalledTimes(2)
    await user.click(screen.getByRole('radio', { name: files }))
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    expect(onContinue).toHaveBeenCalledWith('fix the typo', {
      run: 'run_1',
      turn: 2,
    })
  })

  it('keeps the files off when a later read finds the turn, and offers them when the edit opens again', async () => {
    vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue(
      read([turn(1, 'add a readme')]),
    )
    const dialog = show()
    expect(await screen.findByText(noFiles)).toBeInTheDocument()
    vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue(
      read([turn(1, 'add a readme'), turn(2, 'fix the typo')]),
    )
    await dialog.readAgain()
    expect(agentOpsApi.runCheckpoints).toHaveBeenCalledTimes(2)
    expect(screen.queryByRole('radio')).toBeNull()
    expect(screen.getByText(noFiles)).toBeInTheDocument()
    dialog.reopen()
    expect(
      await screen.findByRole('radio', { name: files }),
    ).toBeInTheDocument()
  })

  it('changes nothing on Cancel', async () => {
    const user = userEvent.setup()
    show()
    await user.click(await screen.findByRole('radio', { name: files }))
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onClose).toHaveBeenCalled()
    expect(onContinue).not.toHaveBeenCalled()
    expect(agentOpsApi.rewindRun).not.toHaveBeenCalled()
  })

  it('does not continue with an empty message', async () => {
    const user = userEvent.setup()
    show()
    await user.clear(screen.getByRole('textbox', { name: 'Message' }))
    expect(screen.getByRole('button', { name: 'Continue' })).toBeDisabled()
    await waitFor(() => expect(agentOpsApi.runCheckpoints).toHaveBeenCalled())
  })
})
