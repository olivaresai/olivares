// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The strip of the tool's background tasks above the composer, against the engine's
// activity read and its per-task stop.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const api = vi.hoisted(() => ({ activity: vi.fn(), stopTask: vi.fn() }))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))

import type { RunDTO } from '@/features/agentops/types'
import { BackgroundTaskStrip } from './background-task-strip'

const RUN = {
  run_ref: 'run_live',
  state: 'running',
  transport: 'stream-json',
  provider_driver: 'claude',
} as RunDTO

beforeEach(() => {
  vi.clearAllMocks()
  api.activity.mockResolvedValue({
    transcript: 'read',
    agents: [],
    files: [],
    tasks: [
      {
        tool_use_id: 'toolu_1',
        task_id: 'bxsdzgtr2',
        kind: 'shell',
        description: 'Watch the build',
        command: 'npm run watch',
        state: 'running',
        started_at: new Date(Date.now() - 65_000).toISOString(),
      },
      {
        tool_use_id: 'toolu_2',
        task_id: 'bdone0001',
        kind: 'shell',
        description: 'Old check',
        state: 'completed',
      },
    ],
  })
  api.stopTask.mockResolvedValue(RUN)
})

describe('BackgroundTaskStrip', () => {
  it('lists each running background task with its time and its own Stop, and stops only that one', async () => {
    const user = userEvent.setup()
    renderIntel(<BackgroundTaskStrip run={RUN} />)
    const strip = await screen.findByRole('list', { name: 'Background tasks' })
    const items = within(strip).getAllByRole('listitem')
    expect(items).toHaveLength(1)
    expect(items[0]).toHaveTextContent('Watch the build')
    expect(items[0]).toHaveTextContent(/1m/)
    await user.click(
      within(items[0]!).getByRole('button', { name: 'Stop Watch the build' }),
    )
    await waitFor(() =>
      expect(api.stopTask).toHaveBeenCalledWith('run_live', 'bxsdzgtr2'),
    )
  })

  it('offers no Stop for a tool that cannot stop one task, and nothing when no task runs', async () => {
    const { unmount } = renderIntel(
      <BackgroundTaskStrip run={{ ...RUN, provider_driver: 'codex' }} />,
    )
    await screen.findByRole('list', { name: 'Background tasks' })
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
    unmount()
    api.activity.mockResolvedValue({
      transcript: 'read',
      agents: [],
      files: [],
      tasks: [],
    })
    renderIntel(<BackgroundTaskStrip run={RUN} />)
    await waitFor(() => expect(api.activity).toHaveBeenCalled())
    expect(
      screen.queryByTestId('background-task-strip'),
    ).not.toBeInTheDocument()
  })
})
