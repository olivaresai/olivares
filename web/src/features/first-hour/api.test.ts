// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'

const tools = vi.hoisted(() => ({
  plan: vi.fn(),
  install: vi.fn(),
  job: vi.fn(),
}))
vi.mock('@/features/agent-tools/api', () => ({ agentToolsApi: tools }))

import { ApiError } from '@/lib/api/errors'
import { installLatest } from './api'

beforeEach(() => {
  vi.clearAllMocks()
})

describe('one-click install', () => {
  it('stops after a pending plan when the preparing operator leaves', async () => {
    let finishPlan!: (value: { digest: string }) => void
    tools.plan.mockReturnValue(
      new Promise((resolve) => {
        finishPlan = resolve
      }),
    )
    const controller = new AbortController()
    const authority = {
      signal: controller.signal,
      dispatchGuard: () => {
        if (controller.signal.aborted) throw new Error('Authority ended')
      },
    }
    const done = installLatest('claude', authority)
    controller.abort()
    finishPlan({ digest: 'd1' })
    await expect(done).rejects.toThrow('Authority ended')
    expect(tools.install).not.toHaveBeenCalled()
  })

  it('carries authority through install and stops polling after it ends', async () => {
    vi.useFakeTimers()
    try {
      const controller = new AbortController()
      const authority = {
        signal: controller.signal,
        dispatchGuard: () => {
          if (controller.signal.aborted) throw new Error('Authority ended')
        },
      }
      tools.plan.mockResolvedValue({ digest: 'd1' })
      tools.install.mockResolvedValue({ id: 'j1', state: 'running' })
      const done = installLatest('opencode', authority)
      const result = expect(done).rejects.toThrow('Authority ended')
      await vi.advanceTimersByTimeAsync(0)
      expect(tools.plan).toHaveBeenCalledWith(
        'opencode',
        'latest',
        controller.signal,
        authority,
      )
      expect(tools.install).toHaveBeenCalledWith(
        expect.objectContaining({ plan_digest: 'd1' }),
        authority,
      )
      controller.abort()
      await vi.runAllTimersAsync()
      await result
      expect(tools.job).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })

  it('reports each job it reads, so the page can show the download (#1086)', async () => {
    vi.useFakeTimers()
    try {
      tools.plan.mockResolvedValue({ digest: 'd1' })
      tools.install.mockResolvedValue({
        id: 'j1',
        state: 'running',
        progress: '',
      })
      tools.job
        .mockResolvedValueOnce({
          id: 'j1',
          state: 'running',
          progress: '  downloaded 1.0 MiB of 4.0 MiB (25%)\n',
        })
        .mockResolvedValueOnce({ id: 'j1', state: 'succeeded', progress: '' })
      const seen: string[] = []
      const done = installLatest('opencode', undefined, (job) =>
        seen.push(`${job.state}:${job.progress.trim()}`),
      )
      await vi.runAllTimersAsync()
      expect((await done).state).toBe('succeeded')
      expect(seen).toEqual([
        'running:',
        'running:downloaded 1.0 MiB of 4.0 MiB (25%)',
        'succeeded:',
      ])
    } finally {
      vi.useRealTimers()
    }
  })

  it('waits its turn when another tool is installing, instead of failing', async () => {
    vi.useFakeTimers()
    tools.plan.mockResolvedValue({ digest: 'd1' })
    tools.install
      .mockRejectedValueOnce(
        new ApiError(
          409,
          'install_busy',
          'Another host tool installation is running.',
        ),
      )
      .mockResolvedValueOnce({ id: 'j1', state: 'running' })
    tools.job.mockResolvedValue({ id: 'j1', state: 'succeeded' })
    const done = installLatest('claude')
    await vi.runAllTimersAsync()
    expect((await done).state).toBe('succeeded')
    expect(tools.install).toHaveBeenCalledTimes(2)
    vi.useRealTimers()
  })

  // The browser logs every non-2xx response to its console, so a second
  // install that is started while the first runs must not be sent at all.
  describe('two installs started from this console', () => {
    // Stands in for the engine: one job at a time, a second POST gets 409.
    function engineWithOneInstallAtATime() {
      let running = false
      const rejected = { busy: 0 }
      tools.plan.mockImplementation(async (driver: string) => ({
        digest: `plan-${driver}`,
      }))
      tools.install.mockImplementation(async (req: { request_id: string }) => {
        if (running) {
          rejected.busy++
          throw new ApiError(409, 'install_busy', 'Another install runs.')
        }
        running = true
        return { id: req.request_id, state: 'running' }
      })
      tools.job.mockImplementation(async (id: string) => {
        running = false
        return { id, state: 'succeeded' }
      })
      return rejected
    }

    it('sends the second install after the first one ended', async () => {
      vi.useFakeTimers()
      try {
        const rejected = engineWithOneInstallAtATime()
        const both = Promise.all([
          installLatest('claude'),
          installLatest('opencode'),
        ])
        await vi.runAllTimersAsync()
        expect((await both).map((job) => job.state)).toEqual([
          'succeeded',
          'succeeded',
        ])
        expect(tools.install).toHaveBeenCalledTimes(2)
        expect(rejected.busy).toBe(0)
      } finally {
        vi.useRealTimers()
      }
    })

    it('still installs the second tool when the first one fails', async () => {
      vi.useFakeTimers()
      try {
        engineWithOneInstallAtATime()
        tools.plan.mockImplementationOnce(async () => {
          throw new ApiError(502, 'transport', 'Release unreachable.')
        })
        const first = installLatest('claude')
        const firstFailed = expect(first).rejects.toThrow(
          'Release unreachable.',
        )
        const second = installLatest('opencode')
        await vi.runAllTimersAsync()
        await firstFailed
        expect((await second).state).toBe('succeeded')
      } finally {
        vi.useRealTimers()
      }
    })
  })
})
