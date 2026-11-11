// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The turns of a session: what each changed, the confirmation before a rewind, the
// message offered again in the message box, and the undo.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const principal = { kind: 'user', actor: 'user:u1', user_id: 'u1' } as const
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', principal, can: () => true }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { runCheckpoints: vi.fn(), rewindRun: vi.fn() },
}))

import { agentOpsApi } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { readSessionDraft, sessionDraftKey } from '@/lib/session-drafts'
import { TurnCheckpoints } from './turn-checkpoints'

const run = { run_ref: 'run_1', state: 'stopped' } as unknown as RunDTO

function show() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <TurnCheckpoints run={run} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  sessionStorage.clear()
  vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue({
    available: true,
    turns: [
      {
        seq: 1,
        prompt: 'add a readme',
        clipped: false,
        at: '2026-11-02T10:00:00Z',
        open: false,
        files: [{ path: 'README.md', status: 'added' }],
        truncated: false,
      },
      {
        seq: 2,
        prompt: 'fix the typo\nin the title',
        clipped: false,
        at: '2026-11-02T10:05:00Z',
        open: true,
        files: [
          { path: 'README.md', status: 'modified' },
          { path: 'src/main.go', status: 'modified' },
        ],
        truncated: false,
      },
    ],
    files: [
      { path: 'README.md', status: 'added' },
      { path: 'src/main.go', status: 'modified' },
    ],
    truncated: false,
  })
  vi.mocked(agentOpsApi.rewindRun).mockImplementation(
    async (_run, seq, to) => ({
      seq,
      to,
      files: [{ path: 'README.md', action: 'removed' }],
    }),
  )
})

describe('TurnCheckpoints', () => {
  it('lists the turns newest first, confirms a rewind, offers the message again and undoes it', async () => {
    const user = userEvent.setup()
    show()

    const rows = await screen.findAllByTestId('turn-checkpoint')
    expect(rows[0]).toHaveTextContent('fix the typo')
    expect(rows[0]).toHaveTextContent('Turn 2')
    expect(rows[0]).toHaveTextContent('Files changed: 2')
    expect(rows[1]).toHaveTextContent('add a readme')

    // Turn 1: the files of turn 1 and of every later turn are what it touches.
    await user.click(screen.getAllByRole('button', { name: 'Rewind' })[1])
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent('Rewind to before turn 1?')
    expect(dialog).toHaveTextContent('Files touched: 2')
    expect(dialog).toHaveTextContent('The tool keeps its conversation')
    await user.click(within(dialog).getByRole('button', { name: 'Rewind' }))

    expect(agentOpsApi.rewindRun).toHaveBeenCalledWith(
      'run_1',
      1,
      'start',
      expect.anything(),
    )
    const draft = sessionDraftKey('t1', principal, 'run_1')
    expect(readSessionDraft(draft)).toBe('add a readme')

    // Undo: the folder as turn 2 left it.
    await user.click(
      await screen.findByRole('button', { name: 'Undo the rewind' }),
    )
    expect(agentOpsApi.rewindRun).toHaveBeenLastCalledWith(
      'run_1',
      2,
      'end',
      expect.anything(),
    )
  })

  it('shows nothing for a session whose folder cannot be checkpointed', async () => {
    vi.mocked(agentOpsApi.runCheckpoints).mockResolvedValue({
      available: false,
      reason: 'the session folder is gone',
      turns: [],
      files: [],
      truncated: false,
    })
    show()
    await vi.waitFor(() =>
      expect(agentOpsApi.runCheckpoints).toHaveBeenCalled(),
    )
    expect(screen.queryByTestId('turn-checkpoints')).toBeNull()
  })
})
