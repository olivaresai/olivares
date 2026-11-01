// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, expect, it, vi } from 'vitest'
import { agentOpsApi } from '@/features/agentops/api'
import type {
  RunActivityDTO,
  RunCommandsDTO,
  RunDTO,
} from '@/features/agentops/types'
import { compactSession } from './compact-session'

vi.mock('@/features/agentops/api', () => ({
  agentOpsApi: { commands: vi.fn(), activity: vi.fn(), input: vi.fn() },
}))
vi.mock('@/features/agentops/work-fence', () => ({
  currentControlFence: vi.fn().mockResolvedValue(undefined),
}))
const run = { run_ref: 'run-a', provider_driver: 'claude' } as RunDTO
const activity = (count: number) =>
  ({
    transcript: 'read',
    compactions: Array.from({ length: count }, () => ({ trigger: 'manual' })),
  }) as RunActivityDTO
const catalog: RunCommandsDTO = {
  tool: 'claude',
  reported: true,
  commands: [
    { name: 'compact', kind: 'command', source: 'built-in', description: '' },
  ],
}
afterEach(() => {
  vi.resetAllMocks()
  vi.useRealTimers()
})

it('waits for a new native compaction record before allowing the next message', async () => {
  vi.useFakeTimers()
  vi.mocked(agentOpsApi.commands).mockResolvedValue(catalog)
  vi.mocked(agentOpsApi.activity)
    .mockResolvedValueOnce(activity(1))
    .mockResolvedValueOnce(activity(1))
    .mockResolvedValueOnce(activity(2))
  vi.mocked(agentOpsApi.input).mockResolvedValue({ accepted: true })
  const done = vi.fn()
  const pending = compactSession(run, new AbortController().signal).then(done)
  await vi.advanceTimersByTimeAsync(1000)
  expect(done).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(1000)
  await pending
  expect(agentOpsApi.input).toHaveBeenCalledWith(
    'run-a',
    JSON.stringify({
      type: 'user',
      message: { role: 'user', content: '/compact' },
    }),
    undefined,
  )
  expect(done).toHaveBeenCalledOnce()
})

it.each([
  { ...catalog, stale: true },
  { ...catalog, reported: false },
  {
    ...catalog,
    commands: [{ ...catalog.commands[0]!, source: 'project' as const }],
  },
])(
  'refuses an unavailable or shadowed compaction command',
  async (commands) => {
    vi.mocked(agentOpsApi.commands).mockResolvedValue(commands)
    await expect(
      compactSession(run, new AbortController().signal),
    ).rejects.toThrow()
    expect(agentOpsApi.input).not.toHaveBeenCalled()
  },
)

it('does not continue on a refusal or an unreadable transcript', async () => {
  vi.mocked(agentOpsApi.commands).mockResolvedValue(catalog)
  vi.mocked(agentOpsApi.activity).mockResolvedValue({
    ...activity(0),
    transcript: 'missing',
  })
  await expect(
    compactSession(run, new AbortController().signal),
  ).rejects.toThrow()
  expect(agentOpsApi.input).not.toHaveBeenCalled()
})

it('times out rather than sending a message without confirmed compaction', async () => {
  vi.useFakeTimers()
  vi.mocked(agentOpsApi.commands).mockResolvedValue(catalog)
  vi.mocked(agentOpsApi.activity).mockResolvedValue(activity(0))
  vi.mocked(agentOpsApi.input).mockResolvedValue({ accepted: true })
  const pending = expect(
    compactSession(run, new AbortController().signal),
  ).rejects.toThrow()
  await vi.advanceTimersByTimeAsync(120000)
  await pending
})

it('cancels confirmation when the composer leaves its session', async () => {
  vi.useFakeTimers()
  vi.mocked(agentOpsApi.commands).mockResolvedValue(catalog)
  vi.mocked(agentOpsApi.activity).mockResolvedValue(activity(0))
  vi.mocked(agentOpsApi.input).mockResolvedValue({ accepted: true })
  const controller = new AbortController()
  const pending = expect(
    compactSession(run, controller.signal),
  ).rejects.toThrow()
  await vi.advanceTimersByTimeAsync(500)
  controller.abort()
  await pending
  expect(agentOpsApi.activity).toHaveBeenCalledTimes(1)
})
