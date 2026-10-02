// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { waitForEngineRestart } from './engine-restart'

/** A clock the wait advances itself, so no test sleeps for real. */
function harness(answers: boolean[]) {
  let t = 0
  const seen: number[] = []
  return {
    seen,
    opts: {
      probe: async () => {
        seen.push(t)
        return answers.length > 1 ? answers.shift()! : answers[0]!
      },
      now: () => t,
      sleep: async (ms: number) => {
        t += ms
      },
      intervalMs: 1000,
      downWindowMs: 10_000,
      timeoutMs: 60_000,
    },
  }
}

describe('waitForEngineRestart — after an apply that answered restarting', () => {
  it('does not take the stopping engine’s last "ready" for the new one', async () => {
    // ready (old process), down, down, ready (new process)
    const h = harness([true, false, false, true])
    await expect(waitForEngineRestart(h.opts)).resolves.toBe(true)
    expect(h.seen).toHaveLength(4)
  })

  it('accepts ready after the window when the restart was too fast to see', async () => {
    const h = harness([true])
    await expect(waitForEngineRestart(h.opts)).resolves.toBe(true)
    expect(h.seen.at(-1)).toBeGreaterThanOrEqual(10_000)
  })

  it('gives up at the timeout instead of waiting forever', async () => {
    const h = harness([false])
    await expect(waitForEngineRestart(h.opts)).resolves.toBe(false)
  })

  it('stops when the page goes away', async () => {
    const ctl = new AbortController()
    ctl.abort()
    const h = harness([false])
    await expect(
      waitForEngineRestart({ ...h.opts, signal: ctl.signal }),
    ).resolves.toBe(false)
    expect(h.seen).toHaveLength(0)
  })
})
